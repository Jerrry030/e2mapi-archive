package cpa

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

type lifecycleResolver map[string]string

func (r lifecycleResolver) ResolveBinding(_ context.Context, id string) (string, error) {
	return r[id], nil
}

type countingLifecycleResolver struct {
	value string
	calls int
}

func (r *countingLifecycleResolver) ResolveBinding(context.Context, string) (string, error) {
	r.calls++
	return r.value, nil
}

func TestLifecycleWritesNamedAuthFileFromLocalBinding(t *testing.T) {
	var create map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[]}`))
		case http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&create)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer server.Close()
	g := NewGateway(gateways.Config{BaseURL: server.URL, BearerToken: "admin", HTTPClient: server.Client(), BindingResolver: lifecycleResolver{"key-a": `{"api_key":"sk-local"}`}})
	result, err := g.ProvisionAccount(t.Context(), contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountPlatformManaged, ChannelID: "channel-a", CredentialBindingID: "key-a", Schedulable: true,
	})
	if err != nil || result.RemoteID != "e2m-channel-a.json" || !result.Created {
		t.Fatalf("provision = %+v err=%v", result, err)
	}
	if create["api_key"] != "sk-local" {
		t.Fatalf("auth file = %+v", create)
	}
}

func TestPlatformManagedUpdateStillWritesResolvedCredential(t *testing.T) {
	resolver := &countingLifecycleResolver{value: `{"api_key":"opaque-test-credential","provider":"openai"}`}
	var update map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != managementBase+"/auth-files" || r.URL.Query().Get("name") != "managed.json" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		_ = json.NewDecoder(r.Body).Decode(&update)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	gateway := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client(), BindingResolver: resolver})
	_, err := gateway.UpdateAccount(t.Context(), contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountPlatformManaged, RemoteID: "managed.json",
		CredentialBindingID: "binding-a", Schedulable: true,
	})
	if err != nil {
		t.Fatalf("managed update: %v", err)
	}
	if resolver.calls != 1 || update["api_key"] == nil || update["provider"] != "openai" {
		t.Fatal("managed update did not retain full binding-backed write behavior")
	}
}

func TestOwnerProvidedUpdateUsesExistingAuthFileWithoutCreateOrDelete(t *testing.T) {
	var requests []string
	var update map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if r.Method != http.MethodPatch || r.URL.Path != managementBase+"/auth-files/fields" || r.URL.RawQuery != "" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		_ = json.NewDecoder(r.Body).Decode(&update)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	gateway := NewGateway(gateways.Config{
		BaseURL: server.URL, BearerToken: "admin", HTTPClient: server.Client(),
		BindingResolver: panicLifecycleResolver{},
	})

	result, err := gateway.UpdateAccount(t.Context(), contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountOwnerProvided, ChannelID: "owner-channel",
		RemoteID: "owner.json", DisplayName: "unmanaged name", Priority: 11,
		CredentialBindingID: "owner-file",
	})
	if err != nil || result.RemoteID != "owner.json" || result.Created {
		t.Fatalf("update = %+v err=%v", result, err)
	}
	if len(requests) != 1 || requests[0] != "PATCH "+managementBase+"/auth-files/fields?" {
		t.Fatalf("requests = %#v", requests)
	}
	if update["name"] != "owner.json" || update["priority"] != float64(11) || len(update) != 2 {
		t.Fatalf("owner field patch = %+v", update)
	}
}

type panicLifecycleResolver struct{}

func (panicLifecycleResolver) ResolveBinding(context.Context, string) (string, error) {
	panic("owner-provided update resolved a credential binding")
}

func TestOwnerProvidedZeroValuesAreUnmanaged(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	gateway := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client(), BindingResolver: panicLifecycleResolver{}})
	result, err := gateway.UpdateAccount(t.Context(), contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountOwnerProvided, RemoteID: "owner.json", CredentialBindingID: "must-not-resolve",
	})
	if err != nil || result.RemoteID != "owner.json" || requests != 0 {
		t.Fatalf("zero-value owner update = %+v err=%v requests=%d", result, err, requests)
	}
}
