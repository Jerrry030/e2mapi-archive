package sub2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

func TestReadPassiveObservationsMapsSuccessAndAttributedFailures(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	type call struct {
		path string
		page string
	}
	var calls []call
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "admin-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		calls = append(calls, call{path: r.URL.Path, page: r.URL.Query().Get("page")})
		var data any
		switch r.URL.Path {
		case typedAdminBase + "/usage":
			data = map[string]any{"items": []map[string]any{
				{"id": 12, "account_id": 7, "request_id": "success-12", "model": "requested", "upstream_model": "gpt-real", "input_tokens": 11, "output_tokens": 5, "first_token_ms": 120, "duration_ms": 730, "created_at": now},
			}}
		case typedAdminBase + "/ops/errors":
			data = map[string]any{"items": []map[string]any{
				{"id": 24, "created_at": now.Add(time.Millisecond), "phase": "auth", "type": "authentication", "error_owner": "provider", "error_source": "upstream_http", "status_code": 401, "model": "gpt-real", "request_id": "provider-auth", "account_id": 7},
				{"id": 25, "created_at": now.Add(2 * time.Millisecond), "phase": "internal", "type": "panic", "error_owner": "platform", "error_source": "gateway", "status_code": 500, "model": "gpt-real", "request_id": "platform-failure", "account_id": 7},
				{"id": 26, "created_at": now.Add(3 * time.Millisecond), "phase": "request", "type": "client_disconnected", "error_owner": "client", "error_source": "client_request", "status_code": 499, "model": "gpt-real", "request_id": "canceled", "account_id": 7},
				{"id": 27, "created_at": now.Add(4 * time.Millisecond), "phase": "upstream", "type": "server", "error_owner": "provider", "error_source": "upstream_http", "status_code": 503, "model": "gpt-real", "request_id": "server-failure", "account_id": 7},
			}}
		case typedAdminBase + "/ops/errors/27":
			data = map[string]any{"id": 27, "response_latency_ms": 400, "time_to_first_token_ms": 90}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	}))
	defer server.Close()

	gateway := NewGateway(gateways.Config{BaseURL: server.URL, XAPIKey: "admin-key", HTTPClient: server.Client()})
	page, err := gateway.ReadPassiveObservations(t.Context(), "", 10)
	if err != nil {
		t.Fatalf("read passive observations: %v", err)
	}
	if page.NextCursor != "v1.12.27" {
		t.Fatalf("next cursor = %q", page.NextCursor)
	}
	if len(page.Observations) != 5 {
		t.Fatalf("observations = %+v", page.Observations)
	}
	byID := map[string]contracts.ConnectorChannelObservation{}
	for _, observation := range page.Observations {
		byID[observation.ObservationID] = observation
	}
	success := byID["sub2api.usage.v1.12"]
	if !success.Success || success.RemoteID != "7" || success.Model != "gpt-real" || success.FirstTokenMS != 120 ||
		success.TotalMS != 730 || success.InputTokens != 11 || success.OutputTokens != 5 {
		t.Fatalf("success observation = %+v", success)
	}
	if got := byID["sub2api.error.v1.24"]; got.ErrorType != contracts.ErrorAuth {
		t.Fatalf("provider auth observation = %+v", got)
	}
	if got := byID["sub2api.error.v1.25"]; got.ErrorType != contracts.ErrorPlatform {
		t.Fatalf("platform observation = %+v", got)
	}
	if got := byID["sub2api.error.v1.26"]; got.ErrorType != contracts.ErrorCanceled {
		t.Fatalf("canceled observation = %+v", got)
	}
	if got := byID["sub2api.error.v1.27"]; got.ErrorType != contracts.ErrorServer || got.FirstTokenMS != 90 || got.TotalMS != 400 {
		t.Fatalf("server observation = %+v", got)
	}
	if !reflect.DeepEqual(calls, []call{
		{typedAdminBase + "/usage", "1"},
		{typedAdminBase + "/ops/errors", "1"},
		{typedAdminBase + "/ops/errors/27", ""},
	}) {
		t.Fatalf("gateway calls = %+v", calls)
	}
	capabilities := gateway.ObservationCapabilities()
	if !capabilities.PassiveCollection || !capabilities.FirstTokenMS || !capabilities.TotalMS || !capabilities.ErrorClassification {
		t.Fatalf("capabilities = %+v", capabilities)
	}
}

func TestReadPassiveObservationsUsesIndependentCursorAndSkipsIneligibleRows(t *testing.T) {
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.URL.Path {
		case typedAdminBase + "/usage":
			data = map[string]any{"items": []map[string]any{
				{"id": 11, "account_id": 7, "model": "gpt", "created_at": now},
				{"id": 10, "account_id": 7, "model": "gpt", "created_at": now},
			}}
		case typedAdminBase + "/ops/errors":
			data = map[string]any{"items": []map[string]any{
				{"id": 23, "created_at": now, "phase": "upstream", "type": "server", "error_owner": "provider", "status_code": 503, "model": "gpt"},
				{"id": 22, "created_at": now, "phase": "upstream", "type": "server", "error_owner": "provider", "status_code": 503, "model": "gpt", "account_id": 7},
			}}
		case typedAdminBase + "/ops/errors/22":
			data = map[string]any{"id": 22}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	}))
	defer server.Close()
	gateway := NewGateway(gateways.Config{BaseURL: server.URL, XAPIKey: "key", HTTPClient: server.Client()})
	page, err := gateway.ReadPassiveObservations(t.Context(), "v1.10.21", 10)
	if err != nil {
		t.Fatalf("read passive observations: %v", err)
	}
	if page.NextCursor != "v1.11.23" || len(page.Observations) != 2 {
		t.Fatalf("page = %+v", page)
	}
	for _, observation := range page.Observations {
		if observation.ObservationID == "sub2api.usage.v1.10" || observation.ObservationID == "sub2api.error.v1.23" {
			t.Fatalf("cursor/ineligible row leaked: %+v", observation)
		}
	}
}

func TestClassifyPassiveErrorPreservesDownstreamAttribution(t *testing.T) {
	tests := []struct {
		name string
		item errorRecord
		want contracts.ObservationErrorType
	}{
		{"provider auth", errorRecord{Owner: "provider", Phase: "auth", StatusCode: 401}, contracts.ErrorAuth},
		{"client auth", errorRecord{Owner: "client", Phase: "auth", StatusCode: 401}, contracts.ErrorClient},
		{"cancel", errorRecord{Owner: "client", StatusCode: 499}, contracts.ErrorCanceled},
		{"platform", errorRecord{Owner: "platform", StatusCode: 500}, contracts.ErrorPlatform},
		{"balance", errorRecord{Owner: "provider", Type: "insufficient_balance", StatusCode: 400}, contracts.ErrorInsufficientBalance},
		{"balance beats provider forbidden", errorRecord{Owner: "provider", Type: "insufficient_balance", StatusCode: 403}, contracts.ErrorInsufficientBalance},
		{"rate limit", errorRecord{Owner: "provider", StatusCode: 429}, contracts.ErrorRateLimit},
		{"provider other 4xx", errorRecord{Owner: "provider", StatusCode: 422}, contracts.ErrorUnknown},
		{"timeout", errorRecord{Owner: "provider", Type: "upstream_timeout", StatusCode: 504}, contracts.ErrorTimeout},
		{"server", errorRecord{Owner: "provider", StatusCode: 503}, contracts.ErrorServer},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyPassiveError(test.item); got != test.want {
				t.Fatalf("classify = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPassiveCursorValidation(t *testing.T) {
	for _, raw := range []string{"v2.1.2", "v1.-1.2", "v1.1.bad", "garbage"} {
		if _, err := decodePassiveCursor(raw); err == nil {
			t.Fatalf("invalid cursor accepted: %s", strconv.Quote(raw))
		}
	}
}

func TestReadPassiveObservationsRetainsFailureWhenDetailExpired(t *testing.T) {
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.URL.Path {
		case typedAdminBase + "/usage":
			data = map[string]any{"items": []any{}}
		case typedAdminBase + "/ops/errors":
			data = map[string]any{"items": []map[string]any{{
				"id": 9, "created_at": now, "phase": "upstream", "type": "server",
				"error_owner": "provider", "status_code": 503, "model": "gpt", "account_id": 7,
			}}}
		case typedAdminBase + "/ops/errors/9":
			http.NotFound(w, r)
			return
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	}))
	defer server.Close()
	gateway := NewGateway(gateways.Config{BaseURL: server.URL, XAPIKey: "key", HTTPClient: server.Client()})
	page, err := gateway.ReadPassiveObservations(t.Context(), "", 10)
	if err != nil {
		t.Fatalf("read passive observations: %v", err)
	}
	if len(page.Observations) != 1 || page.Observations[0].ErrorType != contracts.ErrorServer ||
		page.Observations[0].FirstTokenMS != 0 || page.Observations[0].TotalMS != 0 {
		t.Fatalf("expired detail lost failure fact: %+v", page)
	}
}
