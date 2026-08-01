package cpa

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

func TestReadPassiveObservationsConsumesUsageQueue(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request %s auth=%q", r.URL.String(), r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/v0/management/auth-files" {
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{{"id": 17, "name": "e2m-channel.json", "status": "ok"}}})
			return
		}
		if r.URL.Path != "/v0/management/usage-queue" || r.URL.Query().Get("count") != "10" {
			t.Fatalf("unexpected request %s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"timestamp": now, "latency_ms": 900, "ttft_ms": 120, "auth_index": 17, "model": "gpt-test", "request_id": "req-ok", "failed": false, "tokens": map[string]any{"input_tokens": 3, "output_tokens": 4}, "fail": map[string]any{"status_code": 200}},
			{"timestamp": now.Add(time.Millisecond), "latency_ms": 450, "ttft_ms": 0, "auth_index": 17, "model": "gpt-test", "request_id": "req-fail", "failed": true, "tokens": map[string]any{}, "fail": map[string]any{"status_code": 401, "body": "unauthorized"}},
		})
	}))
	defer server.Close()

	gateway := NewGateway(gateways.Config{BaseURL: server.URL, BearerToken: "secret", HTTPClient: server.Client(), CPAUsageStatisticsEnabled: true})
	page, err := gateway.ReadPassiveObservations(t.Context(), "", 10)
	if err != nil {
		t.Fatalf("read observations: %v", err)
	}
	if len(page.Observations) != 2 || page.NextCursor == "" {
		t.Fatalf("page = %+v", page)
	}
	if got := page.Observations[0]; !got.Success || got.RemoteID != "e2m-channel.json" || got.FirstTokenMS != 120 || got.TotalMS != 900 || got.InputTokens != 3 || got.OutputTokens != 4 {
		t.Fatalf("success = %+v", got)
	}
	if got := page.Observations[1]; got.Success || got.ErrorType != contracts.ErrorAuth || got.StatusCode != 401 {
		t.Fatalf("failure = %+v", got)
	}
	if capabilities := gateway.ObservationCapabilities(); !capabilities.PassiveCollection || !capabilities.FirstTokenMS || !capabilities.TotalMS {
		t.Fatalf("capabilities = %+v", capabilities)
	}
}

func TestReadPassiveObservationsRejectsOversizedQueueResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{{"name": "one.json", "status": "ok"}}})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{}, {}})
	}))
	defer server.Close()
	gateway := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client(), CPAUsageStatisticsEnabled: true})
	if _, err := gateway.ReadPassiveObservations(t.Context(), "", 1); err == nil {
		t.Fatal("expected oversized response to fail closed")
	}
}
