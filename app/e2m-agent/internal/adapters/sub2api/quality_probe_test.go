package sub2api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

func TestProbeQualityMeasuresFirstContentAndCompletion(t *testing.T) {
	var guardCalls atomic.Int32
	var probeCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "local-admin-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case typedAdminBase + "/accounts/7":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"id": 7, "platform": "openai", "type": "apikey",
				"extra": map[string]any{"openai_responses_mode": "force_responses"},
			}})
		case typedAdminBase + "/accounts/7/test":
			probeCalls.Add(1)
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model_id"] != "gpt-test" {
				t.Errorf("probe body=%v err=%v", body, err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("test server does not support streaming")
			}
			time.Sleep(15 * time.Millisecond)
			_, _ = w.Write([]byte("data: {\"type\":\"content\",\"text\":\"ok\"}\n\n"))
			flusher.Flush()
			time.Sleep(15 * time.Millisecond)
			_, _ = w.Write([]byte("data: {\"type\":\"test_complete\",\"success\":true}\n\n"))
			flusher.Flush()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	gateway := NewGateway(gateways.Config{
		BaseURL: server.URL, XAPIKey: "local-admin-key", HTTPClient: server.Client(),
		QualityProbeGuard: func() error {
			guardCalls.Add(1)
			return nil
		},
	})
	result, err := gateway.ProbeQuality(t.Context(), contracts.ConnectorGatewayQualityProbeInput{
		AccountID: "7", Model: "gpt-test", Capability: contracts.QualityProbeTextStream,
		EndpointPath: contracts.QualityProbeEndpointResponses,
	})
	if err != nil {
		t.Fatalf("probe quality: %v", err)
	}
	if !result.Success || result.Status != http.StatusOK || result.ErrorType != contracts.ErrorNone {
		t.Fatalf("probe outcome=%+v", result)
	}
	if result.FirstTokenMS <= 0 || result.TotalMS <= result.FirstTokenMS {
		t.Fatalf("latency facts must satisfy 0 < TTFT < total: %+v", result)
	}
	if result.Capability != contracts.QualityProbeTextStream || result.EndpointPath != contracts.QualityProbeEndpointResponses || result.ObservedAt.IsZero() {
		t.Fatalf("probe scope/time=%+v", result)
	}
	if guardCalls.Load() != 1 || probeCalls.Load() != 1 {
		t.Fatalf("guard calls=%d probe calls=%d", guardCalls.Load(), probeCalls.Load())
	}
}

func TestProbeQualityMapsStructuredFailures(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		sse       string
		wantError contracts.ObservationErrorType
	}{
		{name: "SSE rate limit", status: http.StatusOK, sse: "data: {\"type\":\"error\",\"error\":\"API returned 429: rate limit exceeded\"}\n\n", wantError: contracts.ErrorRateLimit},
		{name: "HTTP server failure", status: http.StatusServiceUnavailable, wantError: contracts.ErrorServer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case typedAdminBase + "/accounts/7":
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
						"id": 7, "platform": "openai", "type": "apikey",
						"extra": map[string]any{"openai_responses_mode": "force_responses"},
					}})
				case typedAdminBase + "/accounts/7/test":
					w.WriteHeader(tt.status)
					_, _ = w.Write([]byte(tt.sse))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			gateway := NewGateway(gateways.Config{
				BaseURL: server.URL, XAPIKey: "local-admin-key", HTTPClient: server.Client(),
				QualityProbeGuard: func() error { return nil },
			})
			result, err := gateway.ProbeQuality(t.Context(), contracts.ConnectorGatewayQualityProbeInput{
				AccountID: "7", Model: "gpt-test", Capability: contracts.QualityProbeTextStream,
				EndpointPath: contracts.QualityProbeEndpointResponses,
			})
			if err != nil {
				t.Fatalf("probe quality: %v", err)
			}
			if result.Success || result.ErrorType != tt.wantError || result.TotalMS <= 0 || result.ObservedAt.IsZero() {
				t.Fatalf("failure mapping=%+v", result)
			}
			if result.Capability != contracts.QualityProbeTextStream || result.EndpointPath != contracts.QualityProbeEndpointResponses {
				t.Fatalf("failure lost probe scope: %+v", result)
			}
		})
	}
}

func TestProbeQualityRejectsMismatchedScopeBeforeBudgetOrProbe(t *testing.T) {
	var guardCalls atomic.Int32
	var probeCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == typedAdminBase+"/accounts/7" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"id": 7, "platform": "openai", "type": "apikey",
				"extra": map[string]any{"openai_responses_mode": "force_responses"},
			}})
			return
		}
		probeCalls.Add(1)
		http.Error(w, "unexpected probe", http.StatusInternalServerError)
	}))
	defer server.Close()

	gateway := NewGateway(gateways.Config{
		BaseURL: server.URL, XAPIKey: "local-admin-key", HTTPClient: server.Client(),
		QualityProbeGuard: func() error {
			guardCalls.Add(1)
			return nil
		},
	})
	_, err := gateway.ProbeQuality(t.Context(), contracts.ConnectorGatewayQualityProbeInput{
		AccountID: "7", Model: "gpt-test", Capability: contracts.QualityProbeTextStream,
		EndpointPath: contracts.QualityProbeEndpointChatCompletions,
	})
	var gatewayErr *gateways.Error
	if err == nil || !errors.As(err, &gatewayErr) || gatewayErr.Code != "quality_probe_scope_unsupported" {
		t.Fatalf("scope error=%v", err)
	}
	if guardCalls.Load() != 0 || probeCalls.Load() != 0 {
		t.Fatalf("mismatched scope consumed budget/probe: guard=%d probe=%d", guardCalls.Load(), probeCalls.Load())
	}
}
