package connector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalBindingStorePersistsPrivateOpaqueValues(t *testing.T) {
	store := NewLocalBindingStore(t.TempDir())
	if err := store.Save(map[string]string{"upstream-a": "sk-local-secret", "proxy-a": "http://proxy.local"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	value, err := store.ResolveBinding(context.Background(), "upstream-a")
	if err != nil || value != "sk-local-secret" {
		t.Fatalf("resolve = %q err=%v", value, err)
	}
}

func TestLocalBindingsAPIIsRetired(t *testing.T) {
	config := NewLocalConfigStore(t.TempDir())
	h := NewLocalAPIHandler(LocalAPIConfig{Store: config, Token: "local-token"})
	req := httptest.NewRequest(http.MethodPut, "http://127.0.0.1/api/local/connector/bindings",
		strings.NewReader(`{"bindings":{"upstream-a":"sk-local-secret"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-E2M-Local-Token", "local-token")
	req.Header.Set("Origin", "http://127.0.0.1")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || strings.Contains(rec.Body.String(), "sk-local-secret") {
		t.Fatalf("retired bindings response = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := config.BindingResolver().ResolveBinding(context.Background(), "upstream-a"); err == nil {
		t.Fatal("retired bindings endpoint persisted a value")
	}

	get := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/local/connector/bindings", nil)
	get.Header.Set("X-E2M-Local-Token", "local-token")
	get.RemoteAddr = "127.0.0.1:12345"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, get)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET bindings = %d", rec.Code)
	}
}
