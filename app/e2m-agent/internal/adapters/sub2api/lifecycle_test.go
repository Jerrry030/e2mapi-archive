package sub2api

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

func TestLifecycleUsesLocalBindingAndStableExternalRef(t *testing.T) {
	var create map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"code":0,"data":[]}`))
		case r.Method == http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&create)
			_, _ = w.Write([]byte(`{"code":0,"data":{"id":9,"external_ref":"channel-a"}}`))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"code":0,"data":null}`))
		}
	}))
	defer server.Close()
	g := NewGateway(gateways.Config{BaseURL: server.URL, XAPIKey: "admin", HTTPClient: server.Client(), BindingResolver: lifecycleResolver{"key-a": "sk-local"}})
	result, err := g.ProvisionAccount(t.Context(), contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountPlatformManaged, ChannelID: "channel-a", DisplayName: "A", CredentialBindingID: "key-a", Schedulable: true,
	})
	if err != nil || result.RemoteID != "9" || !result.Created {
		t.Fatalf("provision = %+v err=%v", result, err)
	}
	credentials, _ := create["credentials"].(map[string]any)
	if create["external_ref"] != "channel-a" || credentials["api_key"] != "sk-local" {
		t.Fatalf("body = %+v", create)
	}
	if err := g.DeleteAccount(t.Context(), "9"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestPlatformManagedUpdateStillWritesResolvedCredential(t *testing.T) {
	resolver := &countingLifecycleResolver{value: "opaque-test-credential"}
	var update map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != typedAdminBase+"/accounts/7" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&update)
		_, _ = w.Write([]byte(`{"code":0,"data":{"id":7}}`))
	}))
	defer server.Close()
	gateway := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client(), BindingResolver: resolver})
	_, err := gateway.UpdateAccount(t.Context(), contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountPlatformManaged, ChannelID: "channel-a", RemoteID: "7",
		DisplayName: "managed", CredentialBindingID: "binding-a", Schedulable: true,
	})
	if err != nil {
		t.Fatalf("managed update: %v", err)
	}
	credentials, ok := update["credentials"].(map[string]any)
	if resolver.calls != 1 || !ok || credentials["api_key"] == nil || update["external_ref"] != "channel-a" {
		t.Fatal("managed update did not retain full binding-backed write behavior")
	}
}

func TestOwnerProvidedUpdateUsesExistingAccountWithoutCreateOrDelete(t *testing.T) {
	var requests []string
	var update map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Method != http.MethodPut || r.URL.Path != typedAdminBase+"/accounts/7" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&update)
		_, _ = w.Write([]byte(`{"code":0,"data":{"id":7}}`))
	}))
	defer server.Close()
	gateway := NewGateway(gateways.Config{
		BaseURL: server.URL, XAPIKey: "admin", HTTPClient: server.Client(),
		BindingResolver: panicLifecycleResolver{},
	})

	result, err := gateway.UpdateAccount(t.Context(), contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountOwnerProvided, ChannelID: "owner-channel",
		RemoteID: "7", DisplayName: "Owner account", Priority: 8,
		CredentialBindingID: "owner-key",
	})
	if err != nil || result.RemoteID != "7" || result.Created {
		t.Fatalf("update = %+v err=%v", result, err)
	}
	if len(requests) != 1 || requests[0] != "PUT "+typedAdminBase+"/accounts/7" {
		t.Fatalf("requests = %#v", requests)
	}
	if update["name"] != "Owner account" || update["priority"] != float64(8) {
		t.Fatalf("owner routing metadata = %+v", update)
	}
	for _, forbidden := range []string{"credentials", "extra", "group_ids", "proxy_id", "status", "schedulable", "external_ref"} {
		if _, present := update[forbidden]; present {
			t.Fatalf("owner update managed zero/unowned field %q: %+v", forbidden, update)
		}
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
		Ownership: contracts.GatewayAccountOwnerProvided, RemoteID: "7", CredentialBindingID: "must-not-resolve",
	})
	if err != nil || result.RemoteID != "7" || requests != 0 {
		t.Fatalf("zero-value owner update = %+v err=%v requests=%d", result, err, requests)
	}
}
