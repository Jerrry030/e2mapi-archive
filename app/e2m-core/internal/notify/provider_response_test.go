package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFeishuRejectsMalformedSuccessfulHTTPResponse(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	err := NewFeishu(srv.URL, "").Send(context.Background(), Event{Text: "test"})
	if err == nil {
		t.Fatal("malformed Feishu response was accepted")
	}
	code, _, retryable := SafeDeliveryError(err)
	if code != "invalid_provider_response" || !retryable {
		t.Fatalf("unexpected Feishu classification: code=%q retryable=%v err=%v", code, retryable, err)
	}
}

func TestFeishuRequiresExplicitZeroCode(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `{"msg":"ok"}`, `{"code":null}`, `null`} {
		body := body
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			if err := NewFeishu(srv.URL, "").Send(context.Background(), Event{Text: "test"}); err == nil {
				t.Fatalf("Feishu response %s accepted without explicit code=0", body)
			}
		})
	}
}

func TestQQRequiresValidOneBotSuccessEnvelope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		body      string
		wantError bool
		wantCode  string
		retryable bool
	}{
		{name: "accepted", body: `{"status":"ok","retcode":0}`},
		{name: "malformed JSON", body: `not-json`, wantError: true, wantCode: "invalid_provider_response", retryable: true},
		{name: "empty proxy response", body: ``, wantError: true, wantCode: "invalid_provider_response", retryable: true},
		{name: "provider rejected", body: `{"status":"failed","retcode":100}`, wantError: true, wantCode: "delivery_rejected"},
		{name: "missing status", body: `{"retcode":0}`, wantError: true, wantCode: "delivery_rejected"},
		{name: "missing retcode", body: `{"status":"ok"}`, wantError: true, wantCode: "delivery_rejected"},
		{name: "null envelope", body: `null`, wantError: true, wantCode: "delivery_rejected"},
		{name: "empty envelope", body: `{}`, wantError: true, wantCode: "delivery_rejected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/send_group_msg" {
					t.Errorf("OneBot path=%q", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			qq := NewQQ(srv.URL, "token", 12345)
			err := qq.Send(context.Background(), Event{Text: "test"})
			if !tt.wantError {
				if err != nil {
					t.Fatalf("valid OneBot response rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid OneBot response was accepted")
			}
			code, _, retryable := SafeDeliveryError(err)
			if code != tt.wantCode || retryable != tt.retryable {
				t.Fatalf("classification: code=%q retryable=%v err=%v", code, retryable, err)
			}
		})
	}
}

func TestQQSendsBearerTokenAndGroup(t *testing.T) {
	t.Parallel()
	var gotAuthorization string
	var gotGroupID float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode OneBot request: %v", err)
		}
		gotGroupID, _ = payload["group_id"].(float64)
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0}`))
	}))
	defer srv.Close()

	qq := NewQQ(srv.URL, "secret-token", 9876)
	if err := qq.Send(context.Background(), Event{Text: "test"}); err != nil {
		t.Fatalf("send QQ message: %v", err)
	}
	if gotAuthorization != "Bearer secret-token" || gotGroupID != 9876 {
		t.Fatalf("request auth/group: authorization=%q group=%v", gotAuthorization, gotGroupID)
	}
}
