package newapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

func TestListAccountsMapsRealNewAPIResponseShape(t *testing.T) {
	const response = `{
		"data":{"items":[{
			"id":1,"type":1,"key":"sk-must-not-leak","openai_organization":"",
			"test_model":"gpt-4o-mini","status":1,"name":" e2m-seed-newapi-primary ",
			"weight":0,"created_time":1752220200,"test_time":1752220300,
			"response_time":810,"base_url":"https://upstream.example/v1","other":"",
			"balance":88.4,"balance_updated_time":1752220400,"models":"gpt-4o-mini,gpt-5-mini",
			"group":" default, vip,default ","used_quota":1200,"model_mapping":"",
			"status_code_mapping":"","priority":10,"auto_ban":1,"other_info":"",
			"tag":" e2m:seed-probe-newapi ","setting":"","param_override":"",
			"header_override":"","remark":"","channel_info":{},"settings":{}
		}],"page":1,"page_size":100,"total":1,"type_counts":{"1":1}},
		"message":"","success":true
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("p") != "1" || r.URL.Query().Get("page_size") != "100" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer admin-token" || r.Header.Get("New-Api-User") != "1" {
			t.Errorf("unexpected auth headers")
		}
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()

	gateway := NewGateway(gateways.Config{
		BaseURL: server.URL, NewAPIUserID: "1", NewAPIToken: "admin-token", HTTPClient: server.Client(),
	})
	accounts, err := gateway.ListAccounts(t.Context())
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("accounts = %+v", accounts)
	}
	account := accounts[0]
	if account.ID != "1" || account.Type != "type_1" || account.Status != "active" || !account.Schedulable || account.Priority != 10 {
		t.Fatalf("account routing fields = %+v", account)
	}
	if account.DisplayName != "e2m-seed-newapi-primary" || account.ExternalRef != "e2m:seed-probe-newapi" {
		t.Fatalf("account identity fields = %+v", account)
	}
	if !reflect.DeepEqual(account.GroupIDs, []string{"default", "vip"}) {
		t.Fatalf("group_ids = %#v", account.GroupIDs)
	}
	if !reflect.DeepEqual(account.Models, []string{"gpt-4o-mini", "gpt-5-mini"}) {
		t.Fatalf("models = %#v", account.Models)
	}
	if account.Balance == nil || *account.Balance != 88.4 || account.UsedQuota == nil || *account.UsedQuota != 1200 {
		t.Fatalf("account quota fields = %+v", account)
	}
	if account.CurrentWeight == nil || *account.CurrentWeight != 0 {
		t.Fatalf("current_weight = %v, want known zero", account.CurrentWeight)
	}
	raw, err := json.Marshal(accounts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-must-not-leak") || strings.Contains(string(raw), "upstream.example") {
		t.Fatalf("sensitive native fields leaked: %s", raw)
	}
}

func TestListAccountsKeepsMissingWeightUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"items":[{"id":1,"status":1}],"total":1}}`))
	}))
	defer server.Close()
	accounts, err := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client()}).ListAccounts(t.Context())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts = %+v, err = %v", accounts, err)
	}
	if accounts[0].CurrentWeight != nil {
		t.Fatalf("missing native weight became known: %v", *accounts[0].CurrentWeight)
	}
}

func TestSetTrafficShareWritesExactNativeWeightIncludingZero(t *testing.T) {
	for _, weight := range []int{0, 10, 100} {
		t.Run(fmt.Sprintf("weight_%d", weight), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut || r.URL.Path != "/api/channel/" {
					t.Fatalf("request = %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len(body) != 2 || body["id"] != float64(7) || body["weight"] != float64(weight) {
					t.Fatalf("body = %#v", body)
				}
				if _, exists := body["status"]; exists {
					t.Fatalf("traffic share write changed scheduling status: %#v", body)
				}
				_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
			}))
			defer server.Close()
			gateway := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client()})
			if err := gateway.SetTrafficShare(t.Context(), "7", weight); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSetTrafficShareRejectsInvalidInputBeforeHTTP(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	gateway := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client()})
	for _, input := range []struct {
		account string
		weight  int
	}{{"bad", 10}, {"7", -1}, {"7", 101}} {
		if err := gateway.SetTrafficShare(t.Context(), input.account, input.weight); err == nil {
			t.Fatalf("input %+v was accepted", input)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid inputs made %d HTTP requests", requests)
	}
}

func TestListAccountsPaginatesToTotalAndDeduplicatesIDs(t *testing.T) {
	requestedPages := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		requestedPages = append(requestedPages, query.Get("p"))
		if query.Get("page_size") != "100" {
			t.Errorf("page_size = %q", query.Get("page_size"))
		}
		var items string
		switch query.Get("p") {
		case "1":
			items = `[{"id":1,"name":"one","status":1},{"id":2,"name":"two","status":1}]`
		case "2":
			items = `[{"id":2,"name":"duplicate","status":1},{"id":3,"name":"three","status":2}]`
		default:
			t.Fatalf("unexpected page %q", query.Get("p"))
		}
		_, _ = fmt.Fprintf(w, `{"success":true,"data":{"items":%s,"page":%s,"page_size":100,"total":3}}`, items, query.Get("p"))
	}))
	defer server.Close()

	gateway := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client()})
	accounts, err := gateway.ListAccounts(t.Context())
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	if !reflect.DeepEqual(requestedPages, []string{"1", "2"}) {
		t.Fatalf("requested pages = %#v", requestedPages)
	}
	if len(accounts) != 3 || accounts[0].ID != "1" || accounts[1].ID != "2" || accounts[2].ID != "3" {
		t.Fatalf("accounts = %+v", accounts)
	}
	if accounts[1].DisplayName != "two" {
		t.Fatalf("duplicate replaced first result: %+v", accounts[1])
	}
}

func TestListAccountsOmitsNativeTagAndUnsafeGroups(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"items":[{
			"id":1,"name":"native","status":1,"tag":"owner managed channel",
			"group":"default, Chinese group,sk-secret,default,vip"
		}],"total":1}}`))
	}))
	defer server.Close()

	gateway := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client()})
	accounts, err := gateway.ListAccounts(t.Context())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts = %+v, error = %v", accounts, err)
	}
	if accounts[0].ExternalRef != "" {
		t.Fatalf("external_ref = %q", accounts[0].ExternalRef)
	}
	if !reflect.DeepEqual(accounts[0].GroupIDs, []string{"default", "vip"}) {
		t.Fatalf("group_ids = %#v", accounts[0].GroupIDs)
	}
}

func TestListAccountsRejectsInvalidE2MExternalRef(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"items":[{
			"id":1,"name":"managed","status":1,"tag":"e2m:sk-secret"
		}],"total":1}}`))
	}))
	defer server.Close()

	gateway := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client()})
	_, err := gateway.ListAccounts(t.Context())
	var gatewayErr *gateways.Error
	if !errors.As(err, &gatewayErr) || gatewayErr.Code != "gateway_response_invalid" {
		t.Fatalf("error = %#v", err)
	}
}

func TestListAccountsRejectsTotalAboveConnectorLimit(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = fmt.Fprintf(w, `{"success":true,"data":{"items":[],"page":1,"page_size":100,"total":%d}}`, contracts.MaxConnectorAccounts+1)
	}))
	defer server.Close()

	gateway := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client()})
	_, err := gateway.ListAccounts(t.Context())
	var gatewayErr *gateways.Error
	if !errors.As(err, &gatewayErr) || gatewayErr.Code != "gateway_response_too_large" {
		t.Fatalf("error = %#v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestListAccountsRejectsPaginationThatStopsBeforeTotal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := `[{"id":1,"name":"one","status":1}]`
		if r.URL.Query().Get("p") == "2" {
			items = `[]`
		}
		_, _ = fmt.Fprintf(w, `{"success":true,"data":{"items":%s,"total":2}}`, items)
	}))
	defer server.Close()

	gateway := NewGateway(gateways.Config{BaseURL: server.URL, HTTPClient: server.Client()})
	_, err := gateway.ListAccounts(t.Context())
	var gatewayErr *gateways.Error
	if !errors.As(err, &gatewayErr) || gatewayErr.Code != "gateway_response_invalid" || !gatewayErr.Retryable {
		t.Fatalf("error = %#v", err)
	}
}
