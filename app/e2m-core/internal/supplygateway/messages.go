package supplygateway

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// The /v1/messages bridge accepts Anthropic Messages requests downstream and
// serves them from the same OpenAI-compatible upstream pool as
// /v1/chat/completions: one reservation/settlement path, one failover loop,
// one cooldown state. Only the protocol at each edge differs — the request
// body, the response body, the SSE event grammar, and the error shape are
// translated here. Tool use is not translated yet; requests carrying tools
// are rejected up front instead of being silently forwarded in a shape the
// upstream would misread.

type anthropicMessagesRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int64              `json:"max_tokens"`
	Messages      []anthropicMessage `json:"messages"`
	System        json.RawMessage    `json:"system,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	Tools         json.RawMessage    `json:"tools,omitempty"`
	ToolChoice    json.RawMessage    `json:"tool_choice,omitempty"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// flattenAnthropicContent accepts the two content encodings the Messages API
// allows — a bare string or an array of blocks — and returns the concatenated
// text. Any non-text block means the request needs a capability this bridge
// does not translate yet.
func flattenAnthropicContent(raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", errors.New("message content is required")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var blocks []anthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", errors.New("message content must be a string or an array of content blocks")
	}
	var builder strings.Builder
	for _, block := range blocks {
		if block.Type != "text" {
			return "", fmt.Errorf("content block type %q is not supported by the messages bridge yet", block.Type)
		}
		builder.WriteString(block.Text)
	}
	return builder.String(), nil
}

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openAIRequestFromAnthropic validates the Anthropic request and builds the
// OpenAI-compatible upstream body. It returns a client-facing message on
// rejection so unsupported features fail loudly instead of degrading.
func openAIRequestFromAnthropic(input anthropicMessagesRequest) ([]byte, string) {
	if strings.TrimSpace(input.Model) == "" {
		return nil, "model is required"
	}
	if input.MaxTokens < 1 {
		return nil, "max_tokens must be a positive integer"
	}
	if len(input.Messages) == 0 {
		return nil, "messages must not be empty"
	}
	if isPresentJSON(input.Tools) || isPresentJSON(input.ToolChoice) {
		return nil, "tool use is not supported by the messages bridge yet"
	}
	messages := make([]openAIChatMessage, 0, len(input.Messages)+1)
	if isPresentJSON(input.System) {
		system, err := flattenAnthropicContent(input.System)
		if err != nil {
			return nil, "system: " + err.Error()
		}
		messages = append(messages, openAIChatMessage{Role: "system", Content: system})
	}
	for index, message := range input.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			return nil, fmt.Sprintf("messages[%d].role must be user or assistant", index)
		}
		content, err := flattenAnthropicContent(message.Content)
		if err != nil {
			return nil, fmt.Sprintf("messages[%d]: %s", index, err.Error())
		}
		messages = append(messages, openAIChatMessage{Role: message.Role, Content: content})
	}
	payload := map[string]any{
		"model":      strings.TrimSpace(input.Model),
		"messages":   messages,
		"max_tokens": input.MaxTokens,
		"stream":     input.Stream,
	}
	if input.Temperature != nil {
		payload["temperature"] = *input.Temperature
	}
	if input.TopP != nil {
		payload["top_p"] = *input.TopP
	}
	if len(input.StopSequences) > 0 {
		payload["stop"] = input.StopSequences
	}
	if input.Stream {
		payload["stream_options"] = map[string]bool{"include_usage": true}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "request could not be encoded"
	}
	return body, ""
}

// isPresentJSON reports whether an optional raw field carries a value beyond
// JSON null/absent, without judging its shape.
func isPresentJSON(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

func anthropicStopReason(finishReason string) string {
	switch finishReason {
	case "length":
		return "max_tokens"
	default:
		return "end_turn"
	}
}

func anthropicMessageID(requestID string) string {
	return "msg_" + strings.TrimPrefix(requestID, "req_")
}

func anthropicUsage(usage tokenUsage) map[string]int64 {
	return map[string]int64{"input_tokens": usage.PromptTokens, "output_tokens": usage.CompletionTokens}
}

// messagesToken accepts both credential conventions Anthropic clients use:
// the x-api-key header and an Authorization bearer token.
func messagesToken(r *http.Request) (string, bool) {
	if key := strings.TrimSpace(r.Header.Get("x-api-key")); key != "" {
		return key, strings.HasPrefix(key, "e2m_v1_") && len(key) > len("e2m_v1_")
	}
	return bearerToken(r.Header.Get("Authorization"))
}

func (h *Handler) handleMessages(w http.ResponseWriter, r *http.Request) {
	token, ok := messagesToken(r)
	if !ok {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "a valid E2M virtual key is required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "request body could not be read")
		return
	}
	var input anthropicMessagesRequest
	if err := json.Unmarshal(rawBody, &input); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "request body must be valid JSON")
		return
	}
	body, rejection := openAIRequestFromAnthropic(input)
	if rejection != "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", rejection)
		return
	}
	requestID, err := newRequestID()
	if err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "request id could not be generated")
		return
	}
	w.Header().Set("X-E2M-Request-ID", requestID)
	excludedChannelIDs := make([]string, 0)
	excluded := make(map[string]struct{})
	for _, cooled := range h.activeCooldowns() {
		excluded[cooled] = struct{}{}
		excludedChannelIDs = append(excludedChannelIDs, cooled)
	}
	initialExclusions := len(excludedChannelIDs)
	for attempt := 0; ; attempt++ {
		attemptRequestID := requestID
		if attempt > 0 {
			attemptRequestID = fmt.Sprintf("%s_retry_%d", requestID, attempt)
		}
		reserved, reserveErr := h.store.ReserveSupplyRequest(r.Context(), contracts.HashVirtualKey(token), attemptRequestID, strings.TrimSpace(input.Model), h.currency, excludedChannelIDs)
		if reserveErr != nil {
			if len(excludedChannelIDs) == initialExclusions && !(errors.Is(reserveErr, store.ErrNoSupply) && initialExclusions > 0) {
				h.writeAnthropicStoreError(w, reserveErr)
			} else {
				writeAnthropicError(w, http.StatusBadGateway, "upstream_error", "all eligible upstream channels failed")
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
			writeAnthropicError(w, http.StatusBadGateway, "upstream_error", "all eligible upstream channels failed")
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
				writeAnthropicError(w, http.StatusServiceUnavailable, "api_error", "failed upstream reservation could not be released")
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
			h.bridgeUpstreamRejection(w, response, finish)
			return
		}
		finish.markUpstreamSucceeded()
		if input.Stream {
			h.bridgeStream(w, response, finish, requestID, strings.TrimSpace(input.Model))
			return
		}
		h.bridgeJSON(w, response, finish, requestID, strings.TrimSpace(input.Model))
		return
	}
}

// bridgeUpstreamRejection converts a deterministic upstream rejection into the
// Anthropic error shape. The upstream status is preserved; the OpenAI error
// message is extracted when the body parses, so an Anthropic-protocol caller
// never receives an OpenAI-shaped error document.
func (h *Handler) bridgeUpstreamRejection(w http.ResponseWriter, response *http.Response, finish *requestFinalizer) {
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBody+1))
	if err != nil || len(body) > maxRequestBody {
		_ = finish.release("upstream_rejection_unreadable")
		writeAnthropicError(w, http.StatusBadGateway, "upstream_error", "upstream rejection could not be read")
		return
	}
	if err := finish.release("upstream_http_non_retryable"); err != nil {
		writeAnthropicError(w, http.StatusServiceUnavailable, "api_error", "upstream reservation could not be released")
		return
	}
	message := "upstream rejected the request"
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && strings.TrimSpace(parsed.Error.Message) != "" {
		message = strings.TrimSpace(parsed.Error.Message)
	}
	writeAnthropicError(w, response.StatusCode, "upstream_error", message)
}

// bridgeJSON settles on the upstream's reported usage and rewrites the OpenAI
// response document as an Anthropic message.
func (h *Handler) bridgeJSON(w http.ResponseWriter, response *http.Response, finish *requestFinalizer, requestID, model string) {
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBody+1))
	if err != nil || len(body) > maxRequestBody {
		_ = finish.settleConservatively("upstream_response_invalid")
		writeAnthropicError(w, http.StatusBadGateway, "upstream_error", "upstream response could not be read")
		return
	}
	usage, ok := usageFromJSON(body)
	if !ok {
		_ = finish.settleConservatively("usage_missing")
		writeAnthropicError(w, http.StatusBadGateway, "upstream_error", "upstream response did not include billable usage")
		return
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &parsed) != nil || len(parsed.Choices) == 0 {
		_ = finish.settleConservatively("upstream_response_invalid")
		writeAnthropicError(w, http.StatusBadGateway, "upstream_error", "upstream response could not be translated")
		return
	}
	if err := finish.settleWithFallback(usage, "exact_settlement_failed"); err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "request accounting could not be finalized")
		return
	}
	content := []map[string]string{}
	if parsed.Choices[0].Message.Content != "" {
		content = append(content, map[string]string{"type": "text", "text": parsed.Choices[0].Message.Content})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            anthropicMessageID(requestID),
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   anthropicStopReason(parsed.Choices[0].FinishReason),
		"stop_sequence": nil,
		"usage":         anthropicUsage(usage),
	})
}

// bridgeStream consumes the upstream OpenAI SSE stream and re-emits it as the
// Anthropic event grammar. Settlement mirrors proxyStream: exact settlement
// needs the upstream usage frame, anything less settles conservatively.
func (h *Handler) bridgeStream(w http.ResponseWriter, response *http.Response, finish *requestFinalizer, requestID, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		_ = finish.settleConservatively("streaming_unsupported")
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	emit := func(event string, payload any) bool {
		raw, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// The Anthropic grammar fronts input_tokens in message_start, but an
	// OpenAI upstream only reports usage in its final frame; the authoritative
	// counts therefore arrive in message_delta.
	if !emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": anthropicMessageID(requestID), "type": "message", "role": "assistant", "model": model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]int64{"input_tokens": 0, "output_tokens": 0},
	}}) {
		_ = finish.settleConservatively("client_disconnected")
		return
	}

	reader := bufio.NewReader(response.Body)
	var usage tokenUsage
	usageSeen, doneSeen, blockStarted := false, false, false
	finishReason := ""
	for {
		line, readErr := reader.ReadString('\n')
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if data == "[DONE]" {
				doneSeen = true
			} else {
				var chunk struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
						FinishReason string `json:"finish_reason"`
					} `json:"choices"`
					Usage *tokenUsage `json:"usage"`
				}
				if json.Unmarshal([]byte(data), &chunk) == nil {
					if chunk.Usage != nil && chunk.Usage.PromptTokens >= 0 && chunk.Usage.CompletionTokens >= 0 {
						usage, usageSeen = *chunk.Usage, true
					}
					if len(chunk.Choices) > 0 {
						if chunk.Choices[0].FinishReason != "" {
							finishReason = chunk.Choices[0].FinishReason
						}
						if text := chunk.Choices[0].Delta.Content; text != "" {
							if !blockStarted {
								if !emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0,
									"content_block": map[string]string{"type": "text", "text": ""}}) {
									_ = finish.settleConservatively("client_disconnected")
									return
								}
								blockStarted = true
							}
							if !emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
								"delta": map[string]string{"type": "text_delta", "text": text}}) {
								_ = finish.settleConservatively("client_disconnected")
								return
							}
						}
					}
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				_ = finish.settleConservatively("upstream_stream_error")
				emit("error", map[string]any{"type": "error", "error": map[string]string{
					"type": "upstream_error", "message": "upstream stream ended unexpectedly"}})
				return
			}
			break
		}
		if doneSeen {
			break
		}
	}
	if !doneSeen || !usageSeen {
		_ = finish.settleConservatively("usage_missing")
		emit("error", map[string]any{"type": "error", "error": map[string]string{
			"type": "upstream_error", "message": "upstream stream did not include billable usage"}})
		return
	}
	if blockStarted {
		if !emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}) {
			_ = finish.settleConservatively("client_disconnected")
			return
		}
	}
	if !emit("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": anthropicStopReason(finishReason), "stop_sequence": nil},
		"usage": anthropicUsage(usage)}) {
		_ = finish.settleConservatively("client_disconnected")
		return
	}
	emit("message_stop", map[string]any{"type": "message_stop"})
	_ = finish.settleWithFallback(usage, "exact_settlement_failed")
}

func (h *Handler) writeAnthropicStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "virtual key or requested model is unavailable")
	case errors.Is(err, store.ErrRateLimited):
		writeAnthropicError(w, http.StatusTooManyRequests, "rate_limit_error", "per-user concurrency or request-rate limit exceeded")
	case errors.Is(err, store.ErrConflict):
		writeAnthropicError(w, http.StatusPaymentRequired, "insufficient_quota", "wallet, budget, capacity, or price policy rejected the request")
	case errors.Is(err, store.ErrInvalid):
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "request accounting parameters are invalid")
	default:
		writeAnthropicError(w, http.StatusServiceUnavailable, "api_error", "supply gateway could not reserve capacity")
	}
}

func writeAnthropicError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"type": "error", "error": map[string]string{"type": code, "message": message}})
}
