package supplygateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

func TestMessagesBridgeTranslatesRequestAndResponse(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-upstream-secret" {
			t.Fatalf("unexpected upstream request: %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"chat-1","usage":{"prompt_tokens":11,"completion_tokens":7},"choices":[{"message":{"role":"assistant","content":"bridged"},"finish_reason":"length"}]}`)
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-test","max_tokens":64,"system":"be brief","temperature":0.5,`+
			`"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"text","text":"ok"}]}]}`))
	req.Header.Set("x-api-key", "e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("response=%d body=%s", recorder.Code, recorder.Body.String())
	}

	// The upstream must have seen a well-formed OpenAI request.
	if upstreamBody["model"] != "claude-test" || upstreamBody["max_tokens"] != float64(64) || upstreamBody["temperature"] != 0.5 {
		t.Fatalf("upstream body=%#v", upstreamBody)
	}
	messages, _ := upstreamBody["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("upstream messages=%#v", messages)
	}
	first, _ := messages[0].(map[string]any)
	last, _ := messages[2].(map[string]any)
	if first["role"] != "system" || first["content"] != "be brief" || last["role"] != "assistant" || last["content"] != "ok" {
		t.Fatalf("translated messages=%#v", messages)
	}

	// The downstream must receive an Anthropic message document.
	var reply struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Role       string `json:"role"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reply.ID, "msg_") || reply.Type != "message" || reply.Role != "assistant" || reply.Model != "claude-test" {
		t.Fatalf("reply=%+v", reply)
	}
	if reply.StopReason != "max_tokens" || len(reply.Content) != 1 || reply.Content[0].Text != "bridged" {
		t.Fatalf("reply=%+v", reply)
	}
	if reply.Usage.InputTokens != 11 || reply.Usage.OutputTokens != 7 {
		t.Fatalf("usage=%+v", reply.Usage)
	}
	if len(fake.settled) != 1 || fake.settled[0] != (tokenUsage{PromptTokens: 11, CompletionTokens: 7}) || len(fake.releasedReason) != 0 {
		t.Fatalf("settled=%+v released=%v", fake.settled, fake.releasedReason)
	}
}

func TestMessagesBridgeStreamsAnthropicEventGrammar(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		options, _ := payload["stream_options"].(map[string]any)
		if payload["stream"] != true || options["include_usage"] != true {
			t.Fatalf("stream request was not built for usage reporting: %#v", payload)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-test","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("response=%d headers=%v", recorder.Code, recorder.Header())
	}
	for _, event := range []string{"event: message_start", "event: content_block_start", "event: content_block_delta",
		"event: content_block_stop", "event: message_delta", "event: message_stop"} {
		if !strings.Contains(body, event) {
			t.Fatalf("missing %q in stream:\n%s", event, body)
		}
	}
	if strings.Index(body, "message_start") > strings.Index(body, "content_block_delta") ||
		strings.Index(body, "content_block_stop") > strings.Index(body, "message_delta") ||
		strings.Index(body, "message_delta") > strings.Index(body, "message_stop") {
		t.Fatalf("events out of order:\n%s", body)
	}
	if !strings.Contains(body, `"text":"hel"`) || !strings.Contains(body, `"text":"lo"`) {
		t.Fatalf("text deltas missing:\n%s", body)
	}
	if !strings.Contains(body, `"stop_reason":"end_turn"`) || !strings.Contains(body, `"input_tokens":3`) || !strings.Contains(body, `"output_tokens":4`) {
		t.Fatalf("final delta missing stop reason or usage:\n%s", body)
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("OpenAI stream markers must not leak downstream:\n%s", body)
	}
	if len(fake.settled) != 1 || fake.settled[0] != (tokenUsage{PromptTokens: 3, CompletionTokens: 4}) {
		t.Fatalf("settled=%+v", fake.settled)
	}
}

func TestMessagesBridgeRejectsUnsupportedAndInvalidRequests(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("a rejected request must never reach the upstream")
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	for name, body := range map[string]string{
		"tools":          `{"model":"claude-test","max_tokens":16,"tools":[{"name":"t"}],"messages":[{"role":"user","content":"hi"}]}`,
		"tool_choice":    `{"model":"claude-test","max_tokens":16,"tool_choice":{"type":"auto"},"messages":[{"role":"user","content":"hi"}]}`,
		"image block":    `{"model":"claude-test","max_tokens":16,"messages":[{"role":"user","content":[{"type":"image","source":{}}]}]}`,
		"missing tokens": `{"model":"claude-test","messages":[{"role":"user","content":"hi"}]}`,
		"bad role":       `{"model":"claude-test","max_tokens":16,"messages":[{"role":"tool","content":"hi"}]}`,
		"no messages":    `{"model":"claude-test","max_tokens":16,"messages":[]}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", "e2m_v1_downstream")
		recorder := httptest.NewRecorder()
		h.Routes().ServeHTTP(recorder, req)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d body=%s", name, recorder.Code, recorder.Body.String())
		}
		var reply struct {
			Type  string `json:"type"`
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil || reply.Type != "error" || reply.Error.Type != "invalid_request_error" {
			t.Fatalf("%s: not an anthropic error document: %s err=%v", name, recorder.Body.String(), err)
		}
	}
	if len(fake.reserveHashes) != 0 {
		t.Fatalf("rejected requests must not reserve: %v", fake.reserveHashes)
	}
}

func TestMessagesBridgeAuthAcceptsBearerAndRejectsBadKeys(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1},"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	h, _ := newGatewayForTest(t, upstream)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-test","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("bearer auth must work: %d %s", recorder.Code, recorder.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("x-api-key", "sk-not-an-e2m-key")
	recorder = httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "authentication_error") {
		t.Fatalf("foreign key must be rejected in anthropic shape: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestMessagesBridgeMapsStoreAndUpstreamErrorsToAnthropicShape(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":{"message":"model not found upstream","type":"invalid_request_error"}}`)
	}))
	defer upstream.Close()

	// Reservation conflicts surface as 402 in the anthropic error shape.
	h, fake := newGatewayForTest(t, upstream)
	fake.reserveErr = store.ErrConflict
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-test","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusPaymentRequired || !strings.Contains(recorder.Body.String(), `"type":"error"`) {
		t.Fatalf("conflict: %d %s", recorder.Code, recorder.Body.String())
	}

	// A deterministic upstream rejection keeps its status but is rewritten
	// from the OpenAI error document into the Anthropic one, and the
	// reservation is released.
	h2, fake2 := newGatewayForTest(t, upstream)
	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-test","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "e2m_v1_downstream")
	recorder = httptest.NewRecorder()
	h2.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("upstream rejection status: %d", recorder.Code)
	}
	var reply struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil || reply.Type != "error" || reply.Error.Message != "model not found upstream" {
		t.Fatalf("rejection not translated: %s err=%v", recorder.Body.String(), err)
	}
	if len(fake2.releasedReason) != 1 || fake2.releasedReason[0] != "upstream_http_non_retryable" || len(fake2.settled) != 0 {
		t.Fatalf("released=%v settled=%v", fake2.releasedReason, fake2.settled)
	}
}

func TestMessagesBridgeStreamWithoutUsageSettlesConservatively(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-test","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if len(fake.conservativeReason) != 1 || fake.conservativeReason[0] != "usage_missing" || len(fake.settled) != 0 {
		t.Fatalf("conservative=%v settled=%v", fake.conservativeReason, fake.settled)
	}
	if !strings.Contains(recorder.Body.String(), "event: error") {
		t.Fatalf("missing error event:\n%s", recorder.Body.String())
	}
}

func TestMessagesBridgeFailsOverToAnotherChannel(t *testing.T) {
	var calls int
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":2,"completion_tokens":2},"choices":[{"message":{"content":"second"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	h, fake := newGatewayForTest(t, upstream)
	endpoint := func(channel string) contracts.SupplyReservationResult {
		return contracts.SupplyReservationResult{
			Reservation: contracts.WalletReservation{ID: "reservation-" + channel},
			Candidate: contracts.SupplyCandidate{
				Channel:  contracts.UpstreamChannel{ID: channel},
				Endpoint: contracts.SupplyChannelEndpoint{ChannelID: channel, BaseURL: upstream.URL + "/v1", SecretRef: "credential_ref:upstream"},
			},
		}
	}
	fake.reservations = []contracts.SupplyReservationResult{endpoint("channel-a"), endpoint("channel-b")}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-test","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "second") {
		t.Fatalf("failover response: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.releasedReason) != 1 || fake.releasedReason[0] != "upstream_http_retryable" || len(fake.settled) != 1 {
		t.Fatalf("released=%v settled=%v", fake.releasedReason, fake.settled)
	}
	if len(fake.reserveExclusions) != 2 || len(fake.reserveExclusions[1]) != 1 || fake.reserveExclusions[1][0] != "channel-a" {
		t.Fatalf("exclusions=%v", fake.reserveExclusions)
	}
}
