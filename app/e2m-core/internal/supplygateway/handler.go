package supplygateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/vault"
)

const maxRequestBody = 8 << 20

type Store interface {
	ReserveSupplyRequest(context.Context, string, string, string, string, []string) (contracts.SupplyReservationResult, error)
	SettleSupplyRequest(context.Context, string, int64, int64) (contracts.SupplySettlementResult, error)
	SettleSupplyRequestConservatively(context.Context, string, string) (contracts.SupplySettlementResult, error)
	ReleaseSupplyRequest(context.Context, string, string) (contracts.SupplySettlementResult, error)
}

type Config struct {
	Currency      string
	Client        *http.Client
	SettleTimeout time.Duration
}

// Channel labels configuring per-upstream data-plane behavior. Both are
// operator-set through the platform upstream labels field.
//
//	e2m.model_mapping        {"requested-model":"upstream-model", ...}
//	e2m.error_cooldown_rules [{"status":429,"keywords":["quota"],"cooldown_seconds":600}, ...]
const (
	modelMappingLabel       = "e2m.model_mapping"
	errorCooldownRulesLabel = "e2m.error_cooldown_rules"
)

type errorCooldownRule struct {
	Status          int      `json:"status"`
	Keywords        []string `json:"keywords,omitempty"`
	CooldownSeconds int      `json:"cooldown_seconds"`
}

type Handler struct {
	store         Store
	vault         vault.Vault
	client        *http.Client
	currency      string
	settleTimeout time.Duration

	// cooldowns parks channels whose upstream errors matched an operator
	// cooldown rule. In-process state: a restart clears it, and multi-node
	// deployments cool per node. That is deliberate — durable circuit state
	// belongs to the parked quality-circuit subsystem.
	cooldownMu sync.Mutex
	cooldowns  map[string]time.Time
}

func New(st Store, secrets vault.Vault, cfg Config) (*Handler, error) {
	currency := strings.ToUpper(strings.TrimSpace(cfg.Currency))
	if st == nil || secrets == nil || len(currency) != 3 {
		return nil, errors.New("supply gateway: store, vault, and three-letter currency are required")
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	settleTimeout := cfg.SettleTimeout
	if settleTimeout <= 0 {
		settleTimeout = 10 * time.Second
	}
	return &Handler{store: st, vault: secrets, client: client, currency: currency,
		settleTimeout: settleTimeout, cooldowns: map[string]time.Time{}}, nil
}

// activeCooldowns prunes expired entries and returns the channels currently
// parked by an error cooldown rule.
func (h *Handler) activeCooldowns() []string {
	h.cooldownMu.Lock()
	defer h.cooldownMu.Unlock()
	now := time.Now()
	out := make([]string, 0, len(h.cooldowns))
	for channelID, until := range h.cooldowns {
		if until.Before(now) {
			delete(h.cooldowns, channelID)
			continue
		}
		out = append(out, channelID)
	}
	return out
}

// applyCooldownRules parks the channel when the upstream failure matches one
// of the channel's operator-authored rules: same HTTP status, plus (when the
// rule lists keywords) any case-insensitive keyword found in the response
// snippet.
func (h *Handler) applyCooldownRules(channel contracts.UpstreamChannel, status int, snippet []byte) {
	raw := strings.TrimSpace(channel.Labels[errorCooldownRulesLabel])
	if raw == "" || status <= 0 {
		return
	}
	var rules []errorCooldownRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return
	}
	lowered := strings.ToLower(string(snippet))
	for _, rule := range rules {
		if rule.Status != status || rule.CooldownSeconds <= 0 {
			continue
		}
		matched := len(rule.Keywords) == 0
		for _, keyword := range rule.Keywords {
			if keyword = strings.ToLower(strings.TrimSpace(keyword)); keyword != "" && strings.Contains(lowered, keyword) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		cooldown := time.Duration(rule.CooldownSeconds) * time.Second
		if cooldown > 24*time.Hour {
			cooldown = 24 * time.Hour
		}
		h.cooldownMu.Lock()
		h.cooldowns[channel.ID] = time.Now().Add(cooldown)
		h.cooldownMu.Unlock()
		log.Printf("supply-gateway: channel %s parked for %s by cooldown rule (status %d)", channel.ID, cooldown, status)
		return
	}
}

// mappedUpstreamModel resolves the channel's model rename for the requested
// model; empty means "send the requested model unchanged".
func mappedUpstreamModel(channel contracts.UpstreamChannel, requestedModel string) string {
	raw := strings.TrimSpace(channel.Labels[modelMappingLabel])
	if raw == "" {
		return ""
	}
	var mapping map[string]string
	if err := json.Unmarshal([]byte(raw), &mapping); err != nil {
		return ""
	}
	mapped := strings.TrimSpace(mapping[requestedModel])
	if mapped == requestedModel {
		return ""
	}
	return mapped
}

// withModel rewrites the request body's model field for channels that rename
// models upstream.
func withModel(body []byte, model string) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	rawModel, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	payload["model"] = rawModel
	return json.Marshal(payload)
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/chat/completions", h.handleChatCompletions)
	mux.HandleFunc("POST /v1/messages", h.handleMessages)
	return mux
}

type chatRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

type tokenUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

func (h *Handler) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "a valid E2M virtual key is required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body could not be read")
		return
	}
	var input chatRequest
	if err := json.Unmarshal(body, &input); err != nil || strings.TrimSpace(input.Model) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "model and a valid JSON body are required")
		return
	}
	if input.Stream {
		body, err = withStreamUsage(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "stream_options must be a JSON object")
			return
		}
	}
	requestID, err := newRequestID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "request id could not be generated")
		return
	}
	// E2M owns this correlation identifier. Set it before reservation and
	// upstream work so successful responses, deterministic upstream 4xx
	// responses, and gateway-generated failures can all be traced uniformly.
	w.Header().Set("X-E2M-Request-ID", requestID)
	excludedChannelIDs := make([]string, 0)
	excluded := make(map[string]struct{})
	// Channels parked by an operator cooldown rule sit out candidate
	// selection entirely for the duration of their cooldown.
	for _, cooled := range h.activeCooldowns() {
		excluded[cooled] = struct{}{}
		excludedChannelIDs = append(excludedChannelIDs, cooled)
	}
	initialExclusions := len(excludedChannelIDs)
	for attempt := 0; ; attempt++ {
		attemptRequestID := requestID
		if attempt > 0 {
			// Reservations are idempotent by virtual key + request ID. Each
			// failover therefore needs its own ledger identity while retaining
			// the root request ID as the downstream correlation identifier.
			attemptRequestID = fmt.Sprintf("%s_retry_%d", requestID, attempt)
		}
		reserved, reserveErr := h.store.ReserveSupplyRequest(r.Context(), contracts.HashVirtualKey(token), attemptRequestID, strings.TrimSpace(input.Model), h.currency, excludedChannelIDs)
		if reserveErr != nil {
			if len(excludedChannelIDs) == initialExclusions && !(errors.Is(reserveErr, store.ErrNoSupply) && initialExclusions > 0) {
				h.writeStoreError(w, reserveErr)
			} else {
				writeUpstreamsExhausted(w)
			}
			return
		}

		finish := newRequestFinalizer(h, reserved.Reservation.ID)
		channelID := strings.TrimSpace(reserved.Candidate.Channel.ID)
		if channelID == "" {
			channelID = strings.TrimSpace(reserved.Candidate.Endpoint.ChannelID)
		}
		if _, alreadyTried := excluded[channelID]; channelID == "" || alreadyTried {
			_ = finish.release("invalid_failover_candidate")
			writeUpstreamsExhausted(w)
			return
		}

		response, failureReason := h.callUpstream(r, body, requestID, reserved)
		if failureReason != "" {
			if response != nil {
				snippet, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
				_ = response.Body.Close()
				h.applyCooldownRules(reserved.Candidate.Channel, response.StatusCode, snippet)
			}
			if err := finish.release(failureReason); err != nil {
				writeError(w, http.StatusServiceUnavailable, "accounting_unavailable", "failed upstream reservation could not be released")
				return
			}
			if r.Context().Err() != nil {
				return
			}
			excluded[channelID] = struct{}{}
			excludedChannelIDs = append(excludedChannelIDs, channelID)
			continue
		}

		defer response.Body.Close()
		defer finish.releaseIfPending("gateway_aborted")
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			h.proxyJSON(w, response, finish)
			return
		}
		finish.markUpstreamSucceeded()
		if input.Stream || strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
			h.proxyStream(w, response, finish)
			return
		}
		h.proxyJSON(w, response, finish)
		return
	}
}

// callUpstream performs only work that happens before E2M commits a response
// to the downstream client. A non-empty failure reason is therefore safe for
// the caller to release and retry on another channel.
func (h *Handler) callUpstream(r *http.Request, body []byte, requestID string, reserved contracts.SupplyReservationResult) (*http.Response, string) {
	secret, err := h.vault.Resolve(r.Context(), reserved.Candidate.Endpoint.SecretRef)
	if err != nil {
		return nil, "credential_unavailable"
	}
	upstreamURL, err := chatCompletionURL(reserved.Candidate.Endpoint.BaseURL, reserved.Candidate.Endpoint.AllowInsecure)
	if err != nil {
		return nil, "endpoint_invalid"
	}
	if mapped := mappedUpstreamModel(reserved.Candidate.Channel, strings.TrimSpace(reserved.Usage.Model)); mapped != "" {
		rewritten, rewriteErr := withModel(body, mapped)
		if rewriteErr != nil {
			return nil, "request_build_failed"
		}
		body = rewritten
	}
	upstreamRequest, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, "request_build_failed"
	}
	upstreamRequest.Header.Set("Authorization", "Bearer "+secret.Value)
	upstreamRequest.Header.Set("Content-Type", "application/json")
	upstreamRequest.Header.Set("Accept", r.Header.Get("Accept"))
	upstreamRequest.Header.Set("X-E2M-Correlation", requestID)
	response, err := h.client.Do(upstreamRequest)
	if err != nil {
		return nil, "upstream_transport_error"
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if retryableUpstreamStatus(response.StatusCode) {
			return response, "upstream_http_retryable"
		}
		// A deterministic client/request rejection (for example 400, 401,
		// 403 or 404) is not evidence that another channel can deliver the
		// same request. Returning it as a committed upstream response avoids
		// broadcasting bad prompts or invalid model names across the pool.
		return response, ""
	}
	return response, ""
}

func retryableUpstreamStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

func writeUpstreamsExhausted(w http.ResponseWriter) {
	writeError(w, http.StatusBadGateway, "upstream_error", "all eligible upstream channels failed")
}

func (h *Handler) proxyJSON(w http.ResponseWriter, response *http.Response, finish *requestFinalizer) {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBody+1))
		if err != nil || len(body) > maxRequestBody {
			_ = finish.release("upstream_rejection_unreadable")
			writeError(w, http.StatusBadGateway, "upstream_error", "upstream rejection could not be read")
			return
		}
		if err := finish.release("upstream_http_non_retryable"); err != nil {
			writeError(w, http.StatusServiceUnavailable, "accounting_unavailable", "upstream reservation could not be released")
			return
		}
		copyResponseHeaders(w.Header(), response.Header, false)
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(body)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBody+1))
	if err != nil || len(body) > maxRequestBody {
		_ = finish.settleConservatively("upstream_response_invalid")
		writeError(w, http.StatusBadGateway, "upstream_error", "upstream response could not be read")
		return
	}
	usage, ok := usageFromJSON(body)
	if !ok {
		_ = finish.settleConservatively("usage_missing")
		writeError(w, http.StatusBadGateway, "usage_missing", "upstream response did not include billable usage")
		return
	}
	if err := finish.settleWithFallback(usage, "exact_settlement_failed"); err != nil {
		writeError(w, http.StatusBadGateway, "settlement_failed", "request accounting could not be finalized")
		return
	}
	copyResponseHeaders(w.Header(), response.Header, false)
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(body)
}

func (h *Handler) proxyStream(w http.ResponseWriter, response *http.Response, finish *requestFinalizer) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		_ = finish.settleConservatively("streaming_unsupported")
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "streaming is unavailable")
		return
	}
	copyResponseHeaders(w.Header(), response.Header, true)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(response.StatusCode)
	reader := bufio.NewReader(response.Body)
	var usage tokenUsage
	usageSeen, doneSeen := false, false
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			if parsed, found, done := usageFromSSELine(line); found {
				usage, usageSeen = parsed, true
			} else if done {
				doneSeen = true
			}
			if _, err := io.WriteString(w, line); err != nil {
				_ = finish.settleConservatively("client_disconnected")
				return
			}
			flusher.Flush()
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				_ = finish.settleConservatively("upstream_stream_error")
				return
			}
			break
		}
	}
	if !doneSeen || !usageSeen {
		_ = finish.settleConservatively("usage_missing")
		return
	}
	_ = finish.settleWithFallback(usage, "exact_settlement_failed")
}

type requestFinalizer struct {
	h                 *Handler
	reservationID     string
	mu                sync.Mutex
	done              bool
	upstreamSucceeded bool
}

func newRequestFinalizer(h *Handler, reservationID string) *requestFinalizer {
	return &requestFinalizer{h: h, reservationID: reservationID}
}

func (f *requestFinalizer) markUpstreamSucceeded() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upstreamSucceeded = true
}

func (f *requestFinalizer) settle(usage tokenUsage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), f.h.settleTimeout)
	defer cancel()
	_, err := f.h.store.SettleSupplyRequest(ctx, f.reservationID, usage.PromptTokens, usage.CompletionTokens)
	if err == nil {
		f.done = true
	}
	return err
}

func (f *requestFinalizer) settleWithFallback(usage tokenUsage, fallbackReason string) error {
	if err := f.settle(usage); err != nil {
		if fallbackErr := f.settleConservatively(fallbackReason); fallbackErr != nil {
			return errors.Join(err, fallbackErr)
		}
	}
	return nil
}

func (f *requestFinalizer) settleConservatively(reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), f.h.settleTimeout)
	defer cancel()
	_, err := f.h.store.SettleSupplyRequestConservatively(ctx, f.reservationID, reason)
	if err == nil {
		f.done = true
	}
	return err
}

func (f *requestFinalizer) release(reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done || f.upstreamSucceeded {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), f.h.settleTimeout)
	defer cancel()
	_, err := f.h.store.ReleaseSupplyRequest(ctx, f.reservationID, reason)
	if err == nil {
		f.done = true
	}
	return err
}

func (f *requestFinalizer) releaseIfPending(reason string) { _ = f.release(reason) }

func withStreamUsage(body []byte) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	options := make(map[string]json.RawMessage)
	if raw, ok := payload["stream_options"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &options); err != nil || options == nil {
			return nil, errors.New("stream_options must be an object")
		}
	}
	options["include_usage"] = json.RawMessage("true")
	rawOptions, err := json.Marshal(options)
	if err != nil {
		return nil, err
	}
	payload["stream_options"] = rawOptions
	return json.Marshal(payload)
}

func bearerToken(value string) (string, bool) {
	parts := strings.Fields(value)
	returnValue := ""
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		returnValue = strings.TrimSpace(parts[1])
	}
	return returnValue, strings.HasPrefix(returnValue, "e2m_v1_") && len(returnValue) > len("e2m_v1_")
}

func chatCompletionURL(base string, allowInsecure bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Scheme != "https" && !(allowInsecure && parsed.Scheme == "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid upstream base URL")
	}
	path := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(path, "/v1") {
		path += "/chat/completions"
	} else {
		path += "/v1/chat/completions"
	}
	parsed.Path = path
	return parsed.String(), nil
}

func usageFromJSON(body []byte) (tokenUsage, bool) {
	var payload struct {
		Usage *tokenUsage `json:"usage"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Usage == nil || payload.Usage.PromptTokens < 0 || payload.Usage.CompletionTokens < 0 {
		return tokenUsage{}, false
	}
	return *payload.Usage, true
}

func usageFromSSELine(line string) (tokenUsage, bool, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return tokenUsage{}, false, false
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "[DONE]" {
		return tokenUsage{}, false, true
	}
	usage, ok := usageFromJSON([]byte(data))
	return usage, ok, false
}

func copyResponseHeaders(dst, src http.Header, stream bool) {
	for _, name := range []string{"Content-Type", "OpenAI-Organization", "OpenAI-Processing-Ms", "OpenAI-Version", "X-Request-ID"} {
		if value := src.Get(name); value != "" {
			dst.Set(name, value)
		}
	}
	if stream {
		dst.Set("Connection", "keep-alive")
	}
}

func newRequestID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "req_" + hex.EncodeToString(raw), nil
}

func (h *Handler) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "virtual key or requested model is unavailable")
	case errors.Is(err, store.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, "rate_limited", "per-user concurrency or request-rate limit exceeded")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusPaymentRequired, "insufficient_quota", "wallet, budget, capacity, or price policy rejected the request")
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "request accounting parameters are invalid")
	default:
		writeError(w, http.StatusServiceUnavailable, "supply_unavailable", "supply gateway could not reserve capacity")
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"type": code, "code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
