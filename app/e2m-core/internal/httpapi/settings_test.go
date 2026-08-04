package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"e2m.local/contracts"
	"e2m.local/core/internal/pricing"
	"e2m.local/core/internal/settings"
)

func TestCommerceSettingsRoundTripAndHotApply(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	ctx := context.Background()

	settingsSvc := settings.New(st)
	if err := settingsSvc.LoadOrSeed(ctx, contracts.CommerceSettings{USDToCNYRate: "7.00"}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	srv.SetSettings(settingsSvc)
	table, err := pricing.Parse([]byte(`{"gpt-4o-mini":{"input_cost_per_token":1.5e-7,"output_cost_per_token":6e-7}}`))
	if err != nil {
		t.Fatalf("parse table: %v", err)
	}
	srv.SetPricing(pricing.NewService(table, settingsSvc.USDToCNYRate))
	handler := srv.Routes()

	admin := createLoginUser(t, authSvc, "settings-admin@example.com", contracts.UserRolePlatformAdmin)
	owner := createLoginUser(t, authSvc, "settings-owner@example.com", contracts.UserRoleOwner)
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")
	ownerToken, _, _ := authSvc.Login(ctx, owner.Email, "password123")

	if w := do(t, handler, http.MethodGet, "/api/v1/admin/settings/commerce", ownerToken, nil); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin settings read must 403, got %d", w.Code)
	}
	if w := do(t, handler, http.MethodGet, "/api/v1/admin/settings/commerce", adminToken, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"usd_to_cny_rate":"7.00"`) {
		t.Fatalf("settings read failed: %d %s", w.Code, w.Body.String())
	}

	// The seeded rate prices the preview: 0.15 USD/M x 7.00 = 1.05 CNY/M.
	if w := do(t, handler, http.MethodGet, "/api/v1/platform/pricing/preview?model=gpt-4o-mini", adminToken, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"input_micros_per_million":1050000`) {
		t.Fatalf("preview at seeded rate failed: %d %s", w.Code, w.Body.String())
	}

	// Updating through the settings module hot-applies to pricing.
	if w := do(t, handler, http.MethodPut, "/api/v1/admin/settings/commerce", adminToken, map[string]any{
		"usd_to_cny_rate": "8.00", "balance_alert_threshold": "5.00",
	}); w.Code != http.StatusOK {
		t.Fatalf("settings update failed: %d %s", w.Code, w.Body.String())
	}
	if w := do(t, handler, http.MethodGet, "/api/v1/platform/pricing/preview?model=gpt-4o-mini", adminToken, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"input_micros_per_million":1200000`) {
		t.Fatalf("preview must follow the live rate: %d %s", w.Code, w.Body.String())
	}
	if settingsSvc.BalanceAlertThresholdMicros() != 5_000_000 {
		t.Fatalf("threshold accessor must hot-apply, got %d", settingsSvc.BalanceAlertThresholdMicros())
	}

	// Clearing the rate disables base-table pricing fail-closed.
	if w := do(t, handler, http.MethodPut, "/api/v1/admin/settings/commerce", adminToken, map[string]any{
		"usd_to_cny_rate": "", "balance_alert_threshold": "",
	}); w.Code != http.StatusOK {
		t.Fatalf("settings clear failed: %d %s", w.Code, w.Body.String())
	}
	if w := do(t, handler, http.MethodGet, "/api/v1/platform/pricing/preview?model=gpt-4o-mini", adminToken, nil); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "pricing_disabled") {
		t.Fatalf("cleared rate must disable pricing, got %d %s", w.Code, w.Body.String())
	}

	// Invalid values are rejected without touching the stored section.
	if w := do(t, handler, http.MethodPut, "/api/v1/admin/settings/commerce", adminToken, map[string]any{
		"usd_to_cny_rate": "-1",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid rate must 400, got %d %s", w.Code, w.Body.String())
	}

	// The update left an audit trail.
	audits, err := st.ListAuditsByTarget(ctx, "settings", "commerce")
	if err != nil || len(audits) < 2 {
		t.Fatalf("settings updates must be audited, err=%v count=%d", err, len(audits))
	}
}

func TestCommerceSettingsSeedOnlyOnFirstBoot(t *testing.T) {
	_, st, _ := newTestServer(t)
	ctx := context.Background()

	first := settings.New(st)
	if err := first.LoadOrSeed(ctx, contracts.CommerceSettings{USDToCNYRate: "7.20"}); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if _, err := first.SetCommerce(ctx, contracts.UpdateCommerceSettingsRequest{USDToCNYRate: "8.50"}); err != nil {
		t.Fatalf("operator update: %v", err)
	}

	// A restart with a different environment must keep the operator's value.
	second := settings.New(st)
	if err := second.LoadOrSeed(ctx, contracts.CommerceSettings{USDToCNYRate: "6.00"}); err != nil {
		t.Fatalf("second load: %v", err)
	}
	if second.Commerce().USDToCNYRate != "8.50" {
		t.Fatalf("database value must win over the environment seed, got %q", second.Commerce().USDToCNYRate)
	}
}
