package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"e2m.local/contracts"
	"e2m.local/core/internal/pricing"
	"e2m.local/core/internal/vault"
)

func pricingTestService(t *testing.T) *pricing.Service {
	t.Helper()
	table, err := pricing.Parse([]byte(`{
		"gpt-4o-mini": {"input_cost_per_token": 1.5e-7, "output_cost_per_token": 6e-7},
		"gpt-4o": {"input_cost_per_token": 2.5e-6, "output_cost_per_token": 1e-5}
	}`))
	if err != nil {
		t.Fatalf("parse test table: %v", err)
	}
	return pricing.NewService(table, pricing.StaticRate(7.0))
}

func TestRateMultiplierParsing(t *testing.T) {
	valid := map[string]int64{"1": 10_000, "1.25": 12_500, "0.5": 5_000, "100": 1_000_000, "0.0001": 1}
	for raw, want := range valid {
		got, ok := parseRateMultiplier(raw)
		if !ok || got != want {
			t.Fatalf("parseRateMultiplier(%q) = %d,%v want %d", raw, got, ok, want)
		}
		if parsed, ok2 := parseRateMultiplier(formatRateMultiplierBps(got)); !ok2 || parsed != got {
			t.Fatalf("format/parse round trip failed for %q", raw)
		}
	}
	for _, raw := range []string{"", "0", "-1", "100.0001", "1.00001", "abc", "1.2.3"} {
		if _, ok := parseRateMultiplier(raw); ok {
			t.Fatalf("parseRateMultiplier(%q) must be rejected", raw)
		}
	}
}

func TestPlatformGroupRateMultiplierAndBasePriceAutofill(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	srv.SetVault(vault.NewMemoryVault())
	srv.EnableInsecureSupplyUpstreams()
	srv.SetPricing(pricingTestService(t))
	handler := srv.Routes()
	ctx := context.Background()

	admin := createLoginUser(t, authSvc, "pricing-admin@example.com", contracts.UserRolePlatformAdmin)
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")

	// Create a group with a 1.5x multiplier.
	groupResponse := do(t, handler, http.MethodPost, "/api/v1/platform/groups", adminToken, map[string]any{
		"name": "priced stable", "models": []string{"gpt-4o-mini"}, "status": "active",
		"resource_class": "stable", "rate_multiplier": "1.5",
	})
	if groupResponse.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", groupResponse.Code, groupResponse.Body.String())
	}
	var group contracts.UpstreamPool
	if err := json.Unmarshal(groupResponse.Body.Bytes(), &group); err != nil || group.ID == "" {
		t.Fatalf("decode group: %v", err)
	}
	if group.Labels[platformRateMultiplierLabel] != "15000" {
		t.Fatalf("multiplier label missing: %+v", group.Labels)
	}
	if w := do(t, handler, http.MethodPost, "/api/v1/platform/groups", adminToken, map[string]any{
		"name": "bad multiplier", "resource_class": "stable", "rate_multiplier": "0",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid multiplier must 400, got %d %s", w.Code, w.Body.String())
	}

	// Preview: 0.15 USD/M × 7.0 × 1.5 = 1.575 CNY/M.
	preview := do(t, handler, http.MethodGet, "/api/v1/platform/pricing/preview?model=gpt-4o-mini&group_id="+group.ID, adminToken, nil)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"input_micros_per_million":1575000`) {
		t.Fatalf("pricing preview failed: %d %s", preview.Code, preview.Body.String())
	}
	if w := do(t, handler, http.MethodGet, "/api/v1/platform/pricing/preview?model=unknown-model", adminToken, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown model preview must 404, got %d %s", w.Code, w.Body.String())
	}

	// Upstream created without prices materializes base-table prices.
	upstream := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/upstreams", adminToken, "pricing-upstream-1", map[string]any{
		"group_id": group.ID, "name": "auto priced", "base_url": "http://mock-openai:8093/v1",
		"api_key": "upstream-secret", "models": []string{"gpt-4o-mini"},
		"capacity": map[string]any{"max_concurrency": 4, "max_request_micros": 1_000_000},
		"status":   "active",
	})
	if upstream.Code != http.StatusCreated || !strings.Contains(upstream.Body.String(), `"input_micros_per_million":1575000`) ||
		!strings.Contains(upstream.Body.String(), `"output_micros_per_million":6300000`) {
		t.Fatalf("base-table autofill failed: %d %s", upstream.Code, upstream.Body.String())
	}

	// Explicit prices always win over the base table.
	explicit := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/upstreams", adminToken, "pricing-upstream-2", map[string]any{
		"group_id": group.ID, "name": "explicit priced", "base_url": "http://mock-openai:8093/v1",
		"api_key": "upstream-secret-2", "models": []string{"gpt-4o-mini"},
		"prices": map[string]any{"gpt-4o-mini": map[string]any{
			"input_micros_per_million": 999, "output_micros_per_million": 1999,
		}},
		"capacity": map[string]any{"max_concurrency": 4, "max_request_micros": 1_000_000},
		"status":   "active",
	})
	if explicit.Code != http.StatusCreated || !strings.Contains(explicit.Body.String(), `"input_micros_per_million":999`) {
		t.Fatalf("explicit price must win: %d %s", explicit.Code, explicit.Body.String())
	}

	// Models with different base prices cannot share one auto-filled upstream.
	mixedGroup := do(t, handler, http.MethodPost, "/api/v1/platform/groups", adminToken, map[string]any{
		"name": "mixed models", "models": []string{"gpt-4o-mini", "gpt-4o"}, "status": "active",
		"resource_class": "stable", "rate_multiplier": "1",
	})
	var mixed contracts.UpstreamPool
	if err := json.Unmarshal(mixedGroup.Body.Bytes(), &mixed); err != nil || mixed.ID == "" {
		t.Fatalf("decode mixed group: %v", err)
	}
	rejected := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/upstreams", adminToken, "pricing-upstream-3", map[string]any{
		"group_id": mixed.ID, "name": "mixed auto", "base_url": "http://mock-openai:8093/v1",
		"api_key": "upstream-secret-3", "models": []string{"gpt-4o-mini", "gpt-4o"},
		"capacity": map[string]any{"max_concurrency": 4, "max_request_micros": 1_000_000},
		"status":   "active",
	})
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "different base prices") {
		t.Fatalf("mixed-price autofill must be rejected: %d %s", rejected.Code, rejected.Body.String())
	}
}

func TestPlatformModelMarketShowsBestPriceWithoutOperationalDetail(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	srv.SetVault(vault.NewMemoryVault())
	srv.EnableInsecureSupplyUpstreams()
	handler := srv.Routes()
	ctx := context.Background()

	admin := createLoginUser(t, authSvc, "market-admin@example.com", contracts.UserRolePlatformAdmin)
	client := createLoginUser(t, authSvc, "market-client@example.com", contracts.UserRoleOwner)
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")
	clientToken, _, _ := authSvc.Login(ctx, client.Email, "password123")

	groupResponse := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/groups", adminToken, "market-group", map[string]any{
		"name": "market stable", "models": []string{"gpt-4o-mini"}, "status": "active", "resource_class": "stable",
	})
	var group contracts.UpstreamPool
	if err := json.Unmarshal(groupResponse.Body.Bytes(), &group); err != nil || group.ID == "" {
		t.Fatalf("decode group: %v %s", err, groupResponse.Body.String())
	}
	for index, price := range []int64{2000, 1000} {
		upstream := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/upstreams", adminToken,
			"market-upstream-"+strconv.Itoa(index), map[string]any{
				"group_id": group.ID, "name": "market upstream " + strconv.Itoa(index),
				"base_url": "http://mock-openai:8093/v1", "api_key": "market-secret-" + strconv.Itoa(index),
				"models": []string{"gpt-4o-mini"},
				"prices": map[string]any{"gpt-4o-mini": map[string]any{
					"input_micros_per_million": price, "output_micros_per_million": price * 2,
				}},
				"capacity": map[string]any{"max_concurrency": 4, "max_request_micros": 1_000_000},
				"status":   "active",
			})
		if upstream.Code != http.StatusCreated {
			t.Fatalf("create upstream %d: %d %s", index, upstream.Code, upstream.Body.String())
		}
	}

	market := do(t, handler, http.MethodGet, "/api/v1/platform/model-market", clientToken, nil)
	if market.Code != http.StatusOK {
		t.Fatalf("model market: %d %s", market.Code, market.Body.String())
	}
	body := market.Body.String()
	if !strings.Contains(body, `"input_micros_per_million":1000`) || strings.Contains(body, `"input_micros_per_million":2000`) {
		t.Fatalf("market must show the best price only: %s", body)
	}
	for _, forbidden := range []string{"market-secret", "base_url", "supplier", "mock-openai"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("market leaked operational detail %q: %s", forbidden, body)
		}
	}
	if w := do(t, handler, http.MethodGet, "/api/v1/platform/model-market", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("market requires a session, got %d", w.Code)
	}
}

func TestUpstreamCreateWithoutPricesFailsClosedWhenPricingDisabled(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	srv.SetVault(vault.NewMemoryVault())
	srv.EnableInsecureSupplyUpstreams()
	handler := srv.Routes()
	ctx := context.Background()

	admin := createLoginUser(t, authSvc, "freeguard-admin@example.com", contracts.UserRolePlatformAdmin)
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")
	groupResponse := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/groups", adminToken, "freeguard-group", map[string]any{
		"name": "无价保护", "models": []string{"gpt-4o-mini"}, "status": "active",
	})
	var group contracts.UpstreamPool
	if err := json.Unmarshal(groupResponse.Body.Bytes(), &group); err != nil || group.ID == "" {
		t.Fatalf("decode group: %v", err)
	}

	// No pricing service configured and no explicit prices: creating the
	// upstream must fail instead of silently minting a zero-priced (free) one.
	rejected := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/upstreams", adminToken, "freeguard-upstream", map[string]any{
		"group_id": group.ID, "name": "无价上游", "base_url": "http://mock-openai:8093/v1",
		"api_key": "freeguard-secret", "models": []string{"gpt-4o-mini"},
		"capacity": map[string]any{"max_concurrency": 4, "max_request_micros": 1_000_000},
		"status":   "active",
	})
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "prices are required") {
		t.Fatalf("price-less create without pricing must 400, got %d %s", rejected.Code, rejected.Body.String())
	}
}

func TestPricingPreviewDisabledWithoutService(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	handler := srv.Routes()
	ctx := context.Background()
	admin := createLoginUser(t, authSvc, "pricing-off-admin@example.com", contracts.UserRolePlatformAdmin)
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")

	if w := do(t, handler, http.MethodGet, "/api/v1/platform/pricing/preview?model=gpt-4o-mini", adminToken, nil); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "pricing_disabled") {
		t.Fatalf("preview without a configured rate must 404 pricing_disabled, got %d %s", w.Code, w.Body.String())
	}
}
