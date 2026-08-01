package newapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestLifecycleCreatesWrappedChannelFromLocalBinding(t *testing.T) {
	var create map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"success":true,"data":{"items":[]}}`))
		case http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&create)
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":12,"name":"A","tag":"e2m:channel-a"}}`))
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
		}
	}))
	defer server.Close()
	g := NewGateway(gateways.Config{BaseURL: server.URL, NewAPIUserID: "1", NewAPIToken: "admin", HTTPClient: server.Client(), BindingResolver: lifecycleResolver{"key-a": "sk-local"}})
	result, err := g.ProvisionAccount(t.Context(), contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountPlatformManaged, ChannelID: "channel-a", DisplayName: "A", Type: "type_1",
		Models: []string{"gpt-test"}, Groups: []string{"default"}, CredentialBindingID: "key-a", Schedulable: true,
	})
	if err != nil || result.RemoteID != "12" || !result.Created {
		t.Fatalf("provision = %+v err=%v", result, err)
	}
	channel, _ := create["channel"].(map[string]any)
	if channel["key"] != "sk-local" || channel["tag"] != "e2m:channel-a" {
		t.Fatalf("body = %+v", create)
	}
}

func TestLifecycleRestrictsJSONCredentialFields(t *testing.T) {
	tests := []struct {
		name       string
		credential string
		wantOK     bool
	}{
		{name: "supply binding", credential: `{"key":"e2m_v1_virtual","base_url":"https://supply.example.com/v1"}`, wantOK: true},
		{name: "routing override", credential: `{"key":"e2m_v1_virtual","base_url":"https://supply.example.com/v1","weight":100}`},
		{name: "identity override", credential: `{"key":"e2m_v1_virtual","base_url":"https://supply.example.com/v1","id":99}`},
		{name: "missing base url", credential: `{"key":"e2m_v1_virtual"}`},
		{name: "relative base url", credential: `{"key":"e2m_v1_virtual","base_url":"/v1"}`},
		{name: "trailing data", credential: `{"key":"e2m_v1_virtual","base_url":"https://supply.example.com/v1"} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"success":true,"data":{"items":[]}}`))
					return
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				_, _ = w.Write([]byte(`{"success":true,"data":{"id":12}}`))
			}))
			defer server.Close()
			gateway := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client(), BindingResolver: lifecycleResolver{"binding": test.credential}})
			_, err := gateway.ProvisionAccount(t.Context(), contracts.GatewayAccountSpec{
				Ownership: contracts.GatewayAccountPlatformManaged, ChannelID: "channel-a", CredentialBindingID: "binding",
				Weight: 7, Groups: []string{"default"}, Models: []string{"gpt-test"},
			})
			if test.wantOK {
				if err != nil || requests != 2 {
					t.Fatalf("err=%v requests=%d", err, requests)
				}
				channel := body["channel"].(map[string]any)
				if channel["key"] != "e2m_v1_virtual" || channel["base_url"] != "https://supply.example.com/v1" || channel["weight"] != float64(7) {
					t.Fatalf("channel=%#v", channel)
				}
				return
			}
			var gatewayErr *gateways.Error
			if !errors.As(err, &gatewayErr) || gatewayErr.Code != "invalid_gateway_request" || requests != 1 {
				t.Fatalf("err=%#v requests=%d", err, requests)
			}
		})
	}
}

func TestPlatformManagedUpdateStillWritesResolvedCredential(t *testing.T) {
	resolver := &countingLifecycleResolver{value: "opaque-test-credential"}
	var update map[string]any
	requests := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/channel/":
			_ = json.NewDecoder(r.Body).Decode(&update)
			_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/channel/12/status":
			_, _ = w.Write([]byte(`{"success":true,"data":true}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	gateway := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client(), BindingResolver: resolver})
	_, err := gateway.UpdateAccount(t.Context(), contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountPlatformManaged, ChannelID: "channel-a", RemoteID: "12",
		DisplayName: "managed", CredentialBindingID: "binding-a", Schedulable: true,
	})
	if err != nil {
		t.Fatalf("managed update: %v", err)
	}
	if resolver.calls != 1 || update["key"] == nil || update["tag"] != "e2m:channel-a" ||
		!reflect.DeepEqual(requests, []string{"PUT /api/channel/", "POST /api/channel/12/status"}) {
		t.Fatal("managed update did not retain full binding-backed write behavior")
	}
}

func TestLifecycleResolvesCreatedChannelWhenCurrentNewAPIOmitsData(t *testing.T) {
	listCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listCalls++
			items := `[]`
			total := `0`
			if listCalls == 2 {
				items = `[{"id":27,"name":"A","status":1,"tag":"e2m:channel-a"}]`
				total = `1`
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"items":` + items + `,"total":` + total + `}}`))
		case http.MethodPost:
			if r.URL.Path != "/api/channel/" {
				t.Fatalf("create path = %q", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"success":true,"message":""}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	g := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client(), BindingResolver: lifecycleResolver{"key-a": "sk-local"}})
	result, err := g.ProvisionAccount(t.Context(), contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountPlatformManaged, ChannelID: "channel-a", DisplayName: "A",
		CredentialBindingID: "key-a", Schedulable: true,
	})
	if err != nil || result.RemoteID != "27" || !result.Created {
		t.Fatalf("provision = %+v err=%v", result, err)
	}
	if listCalls != 2 {
		t.Fatalf("list calls = %d, want 2", listCalls)
	}
}

func TestLifecycleRejectsAmbiguousCreatedChannelReadback(t *testing.T) {
	listCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listCalls++
			items := `[]`
			total := `0`
			if listCalls == 2 {
				items = `[{"id":27,"status":1,"tag":"e2m:channel-a"},{"id":28,"status":1,"tag":"e2m:channel-a"}]`
				total = `2`
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"items":` + items + `,"total":` + total + `}}`))
		case http.MethodPost:
			_, _ = w.Write([]byte(`{"success":true,"message":""}`))
		}
	}))
	defer server.Close()

	g := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client(), BindingResolver: lifecycleResolver{"key-a": "sk-local"}})
	_, err := g.ProvisionAccount(t.Context(), contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountPlatformManaged, ChannelID: "channel-a", CredentialBindingID: "key-a",
	})
	var gatewayErr *gateways.Error
	if !errors.As(err, &gatewayErr) || gatewayErr.Code != "gateway_response_invalid" {
		t.Fatalf("error = %#v", err)
	}
}

func TestOwnerProvidedUpdateUsesExistingChannelWithoutCreateOrDelete(t *testing.T) {
	requests := make([]string, 0, 1)
	var updateBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/channel/":
			_ = json.NewDecoder(r.Body).Decode(&updateBody)
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":12}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	g := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client(), BindingResolver: panicLifecycleResolver{}})
	result, err := g.UpdateAccount(t.Context(), contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountOwnerProvided, RemoteID: "12", ChannelID: "channel-a",
		DisplayName: "A", Models: []string{"model-a"}, Priority: 9,
		CredentialBindingID: "key-a", Schedulable: false,
	})
	if err != nil || result.RemoteID != "12" || result.Created {
		t.Fatalf("update = %+v err=%v", result, err)
	}
	if !reflect.DeepEqual(requests, []string{"PUT /api/channel/"}) {
		t.Fatalf("requests = %#v", requests)
	}
	if updateBody["id"] != float64(12) || updateBody["name"] != "A" || updateBody["models"] != "model-a" || updateBody["priority"] != float64(9) {
		t.Fatalf("owner routing metadata = %#v", updateBody)
	}
	for _, forbidden := range []string{"key", "status", "tag", "group", "weight", "proxy", "type"} {
		if _, present := updateBody[forbidden]; present {
			t.Fatalf("owner update managed zero/unowned field %q: %#v", forbidden, updateBody)
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
		Ownership: contracts.GatewayAccountOwnerProvided, RemoteID: "12", CredentialBindingID: "must-not-resolve",
	})
	if err != nil || result.RemoteID != "12" || requests != 0 {
		t.Fatalf("zero-value owner update = %+v err=%v requests=%d", result, err, requests)
	}
}
