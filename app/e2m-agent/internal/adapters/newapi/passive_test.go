package newapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

func TestReadPassiveObservationsMapsConsumeAndErrorLogs(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/log/" || r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("New-Api-User") != "1" {
			t.Fatalf("unexpected request %s auth=%q uid=%q", r.URL.String(), r.Header.Get("Authorization"), r.Header.Get("New-Api-User"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{"page": 1, "page_size": 100, "total": 2, "items": []map[string]any{
				{"id": 2, "created_at": now.Unix(), "type": 5, "content": "upstream 429 rate limit", "model_name": "gpt-test", "use_time": 3, "channel": 8, "request_id": "req-2", "other": `{"status_code":429}`},
				{"id": 1, "created_at": now.Add(-time.Second).Unix(), "type": 2, "model_name": "gpt-test", "prompt_tokens": 3, "completion_tokens": 4, "use_time": 2, "channel": 7, "request_id": "req-1", "other": `{"frt":120}`},
			}},
		})
	}))
	defer server.Close()

	gateway := NewGateway(gateways.Config{BaseURL: server.URL, NewAPIUserID: "1", NewAPIToken: "token", HTTPClient: server.Client()})
	page, err := gateway.ReadPassiveObservations(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("read observations: %v", err)
	}
	if len(page.Observations) != 2 || page.NextCursor == "" {
		t.Fatalf("page = %+v", page)
	}
	success, failure := page.Observations[0], page.Observations[1]
	if success.RemoteID != "7" || !success.Success || success.FirstTokenMS != 120 || success.TotalMS != 2000 || success.InputTokens != 3 || success.OutputTokens != 4 {
		t.Fatalf("success = %+v", success)
	}
	if failure.RemoteID != "8" || failure.Success || failure.ErrorType != contracts.ErrorRateLimit || failure.StatusCode != 429 || failure.TotalMS != 3000 {
		t.Fatalf("failure = %+v", failure)
	}
	if capabilities := gateway.ObservationCapabilities(); !capabilities.PassiveCollection || !capabilities.FirstTokenMS || !capabilities.TotalMS {
		t.Fatalf("capabilities = %+v", capabilities)
	}
}

func TestReadPassiveObservationsCursorDeduplicatesSameSecond(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rows := []map[string]any{{"id": 1, "created_at": now.Unix(), "type": 2, "model_name": "gpt", "channel": 7, "request_id": "req-1", "other": `{}`}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"page": 1, "page_size": 100, "total": len(rows), "items": rows}})
	}))
	defer server.Close()
	gateway := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client()})

	first, err := gateway.ReadPassiveObservations(t.Context(), "", 10)
	if err != nil || len(first.Observations) != 1 {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	rows = append([]map[string]any{{"id": 2, "created_at": now.Unix(), "type": 2, "model_name": "gpt", "channel": 7, "request_id": "req-2", "other": `{}`}}, rows...)
	second, err := gateway.ReadPassiveObservations(t.Context(), first.NextCursor, 10)
	if err != nil || len(second.Observations) != 1 || second.Observations[0].ObservationID == first.Observations[0].ObservationID {
		t.Fatalf("second = %+v err=%v", second, err)
	}
	third, err := gateway.ReadPassiveObservations(t.Context(), second.NextCursor, 10)
	if err != nil || len(third.Observations) != 0 {
		t.Fatalf("third = %+v err=%v", third, err)
	}
}
