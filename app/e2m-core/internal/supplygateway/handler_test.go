package supplygateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/vault"
)

type fakeStore struct {
	mu                 sync.Mutex
	reservation        contracts.SupplyReservationResult
	reservations       []contracts.SupplyReservationResult
	reserveHashes      []string
	reserveRequestIDs  []string
	reserveExclusions  [][]string
	settled            []tokenUsage
	conservativeReason []string
	releasedReason     []string
	reserveErr         error
	settleErr          error
	conservativeErr    error
	releaseErr         error
}

func (f *fakeStore) ReserveSupplyRequest(_ context.Context, hash, requestID, _, _ string, excludedChannelIDs []string) (contracts.SupplyReservationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reserveHashes = append(f.reserveHashes, hash)
	f.reserveRequestIDs = append(f.reserveRequestIDs, requestID)
	f.reserveExclusions = append(f.reserveExclusions, append([]string(nil), excludedChannelIDs...))
	if len(f.reservations) > 0 {
		index := len(f.reserveRequestIDs) - 1
		if index < len(f.reservations) {
			return f.reservations[index], nil
		}
		return contracts.SupplyReservationResult{}, store.ErrNoSupply
	}
	if len(excludedChannelIDs) > 0 {
		return contracts.SupplyReservationResult{}, store.ErrNoSupply
	}
	return f.reservation, f.reserveErr
}

func (f *fakeStore) SettleSupplyRequest(_ context.Context, _ string, prompt, completion int64) (contracts.SupplySettlementResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settled = append(f.settled, tokenUsage{PromptTokens: prompt, CompletionTokens: completion})
	return contracts.SupplySettlementResult{}, f.settleErr
}

func (f *fakeStore) SettleSupplyRequestConservatively(_ context.Context, _, reason string) (contracts.SupplySettlementResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conservativeReason = append(f.conservativeReason, reason)
	return contracts.SupplySettlementResult{}, f.conservativeErr
}

func (f *fakeStore) ReleaseSupplyRequest(_ context.Context, _, reason string) (contracts.SupplySettlementResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releasedReason = append(f.releasedReason, reason)
	return contracts.SupplySettlementResult{}, f.releaseErr
}

func newGatewayForTest(t *testing.T, upstream *httptest.Server) (*Handler, *fakeStore) {
	t.Helper()
	v := vault.NewMemoryVault()
	_, _ = v.Store(context.Background(), "credential_ref:upstream", "sk-upstream-secret")
	fake := &fakeStore{reservation: contracts.SupplyReservationResult{
		Reservation: contracts.WalletReservation{ID: "reservation-1"},
		Candidate: contracts.SupplyCandidate{
			Channel:  contracts.UpstreamChannel{ID: "channel-1"},
			Endpoint: contracts.SupplyChannelEndpoint{ChannelID: "channel-1", BaseURL: upstream.URL + "/v1", SecretRef: "credential_ref:upstream"},
		},
	}}
	h, err := New(fake, v, Config{Currency: "CNY", Client: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return h, fake
}

func TestGatewayNonStreamingSettlesReportedUsageAndProtectsCredentials(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-upstream-secret" {
			t.Fatalf("unexpected upstream request: %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "secret=cookie")
		_, _ = fmt.Fprint(w, `{"id":"chat-1","usage":{"prompt_tokens":11,"completion_tokens":7},"choices":[]}`)
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[]}`))
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	req.Header.Set("X-E2M-Request-ID", "caller-controlled-id")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Set-Cookie") != "" {
		t.Fatalf("response=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if len(fake.settled) != 1 || fake.settled[0] != (tokenUsage{PromptTokens: 11, CompletionTokens: 7}) || len(fake.releasedReason) != 0 {
		t.Fatalf("settled=%+v released=%v", fake.settled, fake.releasedReason)
	}
	if len(fake.reserveHashes) != 1 || fake.reserveHashes[0] != contracts.HashVirtualKey("e2m_v1_downstream") {
		t.Fatalf("hashes=%v", fake.reserveHashes)
	}
	if fake.reserveRequestIDs[0] == "caller-controlled-id" || !strings.HasPrefix(fake.reserveRequestIDs[0], "req_") {
		t.Fatalf("request id must be gateway-generated: %v", fake.reserveRequestIDs)
	}
}

func TestGatewayStreamingSettlesUsageBeforeDone(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		options, _ := payload["stream_options"].(map[string]any)
		if options["include_usage"] != true || options["vendor_option"] != "preserved" || payload["unknown"] != "preserved" {
			t.Fatalf("stream options were not merged safely: %#v", payload)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","stream":true,"unknown":"preserved","stream_options":{"include_usage":false,"vendor_option":"preserved"}}`))
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "[DONE]") || len(fake.settled) != 1 || fake.settled[0] != (tokenUsage{PromptTokens: 3, CompletionTokens: 4}) {
		t.Fatalf("response=%d body=%s settled=%+v", recorder.Code, recorder.Body.String(), fake.settled)
	}
}

func TestGatewayExactSettlementFailureFallsBackWithoutReleasing(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"chat-1","usage":{"prompt_tokens":5,"completion_tokens":2},"choices":[]}`)
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	fake.settleErr = errors.New("ledger unavailable")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || len(fake.settled) != 1 || len(fake.conservativeReason) != 1 || fake.conservativeReason[0] != "exact_settlement_failed" || len(fake.releasedReason) != 0 {
		t.Fatalf("response=%d settled=%v conservative=%v released=%v", recorder.Code, fake.settled, fake.conservativeReason, fake.releasedReason)
	}
}

func TestGatewaySettlementFailureLeavesReservationActive(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"chat-1","usage":{"prompt_tokens":5,"completion_tokens":2},"choices":[]}`)
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	fake.settleErr = errors.New("exact ledger unavailable")
	fake.conservativeErr = errors.New("fallback ledger unavailable")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadGateway || len(fake.settled) != 1 || len(fake.conservativeReason) != 1 || len(fake.releasedReason) != 0 {
		t.Fatalf("response=%d settled=%v conservative=%v released=%v", recorder.Code, fake.settled, fake.conservativeReason, fake.releasedReason)
	}
}

func TestGatewayRejectsNonObjectStreamOptionsBeforeReservation(t *testing.T) {
	upstream := httptest.NewTLSServer(http.NotFoundHandler())
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","stream":true,"stream_options":"invalid"}`))
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest || len(fake.reserveRequestIDs) != 0 {
		t.Fatalf("response=%d reservations=%v", recorder.Code, fake.reserveRequestIDs)
	}
}

func TestGatewaySuccessfulResponseWithoutUsageSettlesConservatively(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"chat-1","choices":[]}`)
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadGateway || len(fake.conservativeReason) != 1 || fake.conservativeReason[0] != "usage_missing" || len(fake.releasedReason) != 0 {
		t.Fatalf("response=%d conservative=%v released=%v", recorder.Code, fake.conservativeReason, fake.releasedReason)
	}
}

func TestGatewayUpstreamFailureExhaustsCandidatesAfterReleasingReservation(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"error":"busy"}`)
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadGateway || len(fake.releasedReason) != 1 || fake.releasedReason[0] != "upstream_http_retryable" || !strings.Contains(recorder.Body.String(), "all eligible upstream channels failed") {
		t.Fatalf("response=%d body=%s released=%v", recorder.Code, recorder.Body.String(), fake.releasedReason)
	}
}

func TestGatewayRetriesAnotherChannelAfterHTTPFailure(t *testing.T) {
	var calls int
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"error":"busy"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":2,"completion_tokens":3},"choices":[]}`)
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	second := fake.reservation
	second.Reservation.ID = "reservation-2"
	second.Candidate.Channel.ID = "channel-2"
	second.Candidate.Endpoint.ChannelID = "channel-2"
	fake.reservations = []contracts.SupplyReservationResult{fake.reservation, second}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK || calls != 2 || len(fake.releasedReason) != 1 || fake.releasedReason[0] != "upstream_http_retryable" || len(fake.settled) != 1 {
		t.Fatalf("response=%d calls=%d released=%v settled=%v body=%s", recorder.Code, calls, fake.releasedReason, fake.settled, recorder.Body.String())
	}
	if len(fake.reserveExclusions) != 2 || len(fake.reserveExclusions[0]) != 0 || len(fake.reserveExclusions[1]) != 1 || fake.reserveExclusions[1][0] != "channel-1" {
		t.Fatalf("reserve exclusions=%v", fake.reserveExclusions)
	}
	if fake.reserveRequestIDs[0] == fake.reserveRequestIDs[1] || !strings.HasPrefix(fake.reserveRequestIDs[1], fake.reserveRequestIDs[0]+"_retry_") {
		t.Fatalf("retry must use a distinct idempotency key: %v", fake.reserveRequestIDs)
	}
}

func TestGatewayDoesNotBroadcastNonRetryableUpstreamRejection(t *testing.T) {
	var calls int
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"message":"invalid prompt","type":"invalid_request_error"}}`)
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	second := fake.reservation
	second.Reservation.ID = "reservation-2"
	second.Candidate.Channel.ID = "channel-2"
	second.Candidate.Endpoint.ChannelID = "channel-2"
	fake.reservations = []contracts.SupplyReservationResult{fake.reservation, second}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest || calls != 1 || len(fake.reserveRequestIDs) != 1 || len(fake.settled) != 0 ||
		len(fake.releasedReason) != 1 || fake.releasedReason[0] != "upstream_http_non_retryable" ||
		!strings.Contains(recorder.Body.String(), "invalid prompt") || recorder.Header().Get("X-E2M-Request-ID") == "" ||
		recorder.Header().Get("X-E2M-Request-ID") != fake.reserveRequestIDs[0] {
		t.Fatalf("response=%d calls=%d reservations=%v released=%v settled=%v body=%s", recorder.Code, calls, fake.reserveRequestIDs, fake.releasedReason, fake.settled, recorder.Body.String())
	}
}

func TestGatewayRetriesAnotherChannelAfterTransportFailure(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1},"choices":[]}`)
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	first := fake.reservation
	first.Candidate.Endpoint.BaseURL = "https://127.0.0.1:1/v1"
	second := fake.reservation
	second.Reservation.ID = "reservation-2"
	second.Candidate.Channel.ID = "channel-2"
	second.Candidate.Endpoint.ChannelID = "channel-2"
	fake.reservations = []contracts.SupplyReservationResult{first, second}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK || len(fake.releasedReason) != 1 || fake.releasedReason[0] != "upstream_transport_error" || len(fake.settled) != 1 {
		t.Fatalf("response=%d released=%v settled=%v body=%s", recorder.Code, fake.releasedReason, fake.settled, recorder.Body.String())
	}
}

func TestGatewayDoesNotFailOverWhenReservationReleaseFails(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	fake.releaseErr = errors.New("ledger unavailable")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable || len(fake.reserveRequestIDs) != 1 || !strings.Contains(recorder.Body.String(), "accounting_unavailable") {
		t.Fatalf("response=%d reservations=%v body=%s", recorder.Code, fake.reserveRequestIDs, recorder.Body.String())
	}
}

func TestGatewayModelMappingRewritesUpstreamModel(t *testing.T) {
	var receivedModel string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		receivedModel = payload.Model
		_, _ = w.Write([]byte(`{"id":"x","usage":{"prompt_tokens":1,"completion_tokens":2}}`))
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	fake.reservation.Candidate.Channel.Labels = map[string]string{
		modelMappingLabel: `{"gpt-test":"upstream-real-model"}`,
	}
	fake.reservation.Usage = contracts.SupplyUsageRecord{Model: "gpt-test"}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	r.Header.Set("Authorization", "Bearer e2m_v1_key")
	h.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("mapped request failed: %d %s (release reasons %v)", w.Code, w.Body.String(), fake.releasedReason)
	}
	if receivedModel != "upstream-real-model" {
		t.Fatalf("upstream must receive the mapped model, got %q", receivedModel)
	}
}

func TestGatewayErrorCooldownRuleParksChannel(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"quota exceeded for this key"}}`))
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	fake.reservation.Candidate.Channel.Labels = map[string]string{
		errorCooldownRulesLabel: `[{"status":429,"keywords":["quota"],"cooldown_seconds":300}]`,
	}
	fake.reservation.Usage = contracts.SupplyUsageRecord{Model: "gpt-test"}

	send := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
		r.Header.Set("Authorization", "Bearer e2m_v1_key")
		h.Routes().ServeHTTP(w, r)
		return w
	}

	if w := send(); w.Code != http.StatusBadGateway {
		t.Fatalf("429 with retry must exhaust candidates, got %d %s", w.Code, w.Body.String())
	}
	if cooled := h.activeCooldowns(); len(cooled) != 1 || cooled[0] != "channel-1" {
		t.Fatalf("channel must be parked by the cooldown rule, got %v", cooled)
	}

	// The next request must exclude the parked channel from its very first
	// reservation attempt.
	before := len(fake.reserveExclusions)
	_ = send()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.reserveExclusions) <= before {
		t.Fatalf("second request made no reservation attempt")
	}
	first := fake.reserveExclusions[before]
	found := false
	for _, id := range first {
		if id == "channel-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("first reservation of the next request must exclude the cooled channel, got %v", first)
	}
}

func TestChatCompletionURLRequiresExplicitHTTPOptIn(t *testing.T) {
	if _, err := chatCompletionURL("http://upstream.test/v1", false); err == nil {
		t.Fatal("plain HTTP accepted without explicit opt-in")
	}
	if got, err := chatCompletionURL("http://upstream.test/v1", true); err != nil || got != "http://upstream.test/v1/chat/completions" {
		t.Fatalf("explicit development HTTP endpoint: got=%q err=%v", got, err)
	}
}

func TestGatewayRejectsInvalidKeyAndMapsReservationConflict(t *testing.T) {
	upstream := httptest.NewTLSServer(http.NotFoundHandler())
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`)))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing key=%d", recorder.Code)
	}
	fake.reserveErr = store.ErrConflict
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder = httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusPaymentRequired {
		t.Fatalf("conflict=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestUsageParserRejectsNegativeValues(t *testing.T) {
	_, ok := usageFromJSON([]byte(`{"usage":{"prompt_tokens":-1,"completion_tokens":1}}`))
	if ok {
		t.Fatal("negative usage accepted")
	}
	var body map[string]any
	recorder := httptest.NewRecorder()
	writeError(recorder, http.StatusBadRequest, "invalid", "message")
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body["error"] == nil {
		t.Fatalf("error body=%s err=%v", recorder.Body.String(), err)
	}
}

func TestGatewayUsesNativeE2MGroupKeyWalletAndUsage(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-native-upstream" {
			t.Fatalf("unexpected native upstream request: %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprint(w, `{"id":"chat-native","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":500}}`)
	}))
	defer upstream.Close()

	ctx := context.Background()
	st := store.NewMemoryStore(time.Now())
	user, err := st.CreateUser(ctx, contracts.User{Email: "native-gateway@example.com", PasswordHash: "hash", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	group, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "Native stable", ResourceClass: contracts.ResourceClassStable, DeliveryMode: contracts.UpstreamDeliverySupplyGateway, Models: []string{"gpt-native"}})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: group.ID, DisplayName: "native", Models: []string{"gpt-native"}, AccountOwnership: contracts.GatewayAccountPlatformManaged, InventoryState: contracts.UpstreamInventoryReady})
	if err != nil {
		t.Fatal(err)
	}
	secrets := vault.NewMemoryVault()
	if _, err = secrets.Store(ctx, "credential_ref:native-upstream", "sk-native-upstream"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpsertSupplyChannelEndpoint(ctx, contracts.SupplyChannelEndpoint{
		ChannelID: channel.ID, BaseURL: upstream.URL + "/v1", SecretRef: "credential_ref:native-upstream", Currency: "CNY",
		InputPriceMicrosPerMillion: 2_000_000, OutputPriceMicrosPerMillion: 4_000_000,
		InputSupplierMicrosPerMillion: 1_000_000, OutputSupplierMicrosPerMillion: 2_000_000,
		MaxRequestMicros: 100_000, MaxConcurrency: 2, CapacityPercent: 100, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	plaintext := "e2m_v1_native_downstream"
	key, err := st.CreateVirtualKey(ctx, contracts.VirtualKey{UserID: user.ID, GroupID: group.ID, Name: "native", ResourceClass: contracts.ResourceClassStable, Prefix: "e2m_v1_", TokenHash: contracts.HashVirtualKey(plaintext), SecretRef: "credential_ref:native-key", Models: []string{"gpt-native"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.AdjustWalletBalance(ctx, user.ID, "CNY", 500_000, "native-seed", "test"); err != nil {
		t.Fatal(err)
	}
	h, err := New(st, secrets, Config{Currency: "CNY", Client: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-native","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("native gateway response=%d body=%s", recorder.Code, recorder.Body.String())
	}
	wallet, err := st.GetWallet(ctx, user.ID, "CNY")
	if err != nil || wallet.AvailableMicros != 496_000 || wallet.ReservedMicros != 0 {
		t.Fatalf("wallet=%+v err=%v", wallet, err)
	}
	usage, err := st.ListSupplyUsage(ctx, contracts.SupplyUsageFilter{UserID: user.ID, GroupID: group.ID, VirtualKeyID: key.ID, Limit: 10})
	if err != nil || len(usage) != 1 || usage[0].Status != contracts.SupplyUsageSettled || usage[0].SettledMicros != 4_000 {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
}

var _ Store = (*fakeStore)(nil)
