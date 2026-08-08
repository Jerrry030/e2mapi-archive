package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"e2m.local/contracts"
	"e2m.local/core/internal/vault"
)

// The routing preference is billing-relevant configuration: setting it must
// persist and surface on reads, garbage must be rejected, one customer must
// never reach another's key, and every actual change must leave its own
// audit entry.
func TestPlatformKeyRoutingPreferenceUpdate(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	srv.SetVault(vault.NewMemoryVault())
	handler := srv.Routes()
	ctx := context.Background()

	clientA := createLoginUser(t, authSvc, "routing-a@example.com", contracts.UserRoleClient)
	tokenA, _, _ := authSvc.Login(ctx, clientA.Email, "password123")
	clientB := createLoginUser(t, authSvc, "routing-b@example.com", contracts.UserRoleClient)
	tokenB, _, _ := authSvc.Login(ctx, clientB.Email, "password123")
	admin := createLoginUser(t, authSvc, "routing-admin@example.com", contracts.UserRolePlatformAdmin)
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")

	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{
		Name: "Routing pool", Models: []string{"gpt-test"},
		ResourceClass: contracts.ResourceClassEconomy, DeliveryMode: contracts.UpstreamDeliverySupplyGateway,
		Status: contracts.UpstreamPoolActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	created := do(t, handler, http.MethodPost, "/api/v1/platform/keys", tokenA, map[string]any{
		"group_id": pool.ID, "name": "routing key",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", created.Code, created.Body.String())
	}
	var createdPayload struct {
		Key contracts.VirtualKey `json:"key"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdPayload); err != nil || createdPayload.Key.ID == "" {
		t.Fatalf("decode created key: %v", err)
	}
	keyPath := "/api/v1/platform/keys/" + createdPayload.Key.ID
	if createdPayload.Key.RoutingPreference != "" {
		t.Fatalf("a new key must follow the platform default, got %q", createdPayload.Key.RoutingPreference)
	}

	// Set: persists, echoes on the update response, and shows up on reads.
	updated := do(t, handler, http.MethodPut, keyPath, tokenA, map[string]any{"routing_preference": "price_first"})
	if updated.Code != http.StatusOK {
		t.Fatalf("set preference: %d %s", updated.Code, updated.Body.String())
	}
	var updatedKey contracts.VirtualKey
	if err := json.Unmarshal(updated.Body.Bytes(), &updatedKey); err != nil || updatedKey.RoutingPreference != contracts.SupplyRoutingPriceFirst {
		t.Fatalf("update response=%s err=%v", updated.Body.String(), err)
	}
	detail := do(t, handler, http.MethodGet, keyPath, tokenA, nil)
	if detail.Code != http.StatusOK || !jsonHasField(t, detail.Body.Bytes(), "routing_preference", "price_first") {
		t.Fatalf("detail=%d %s", detail.Code, detail.Body.String())
	}

	// Garbage is rejected and the stored value survives.
	if w := do(t, handler, http.MethodPut, keyPath, tokenA, map[string]any{"routing_preference": "cheapest!!"}); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid preference must 400, got %d %s", w.Code, w.Body.String())
	}
	afterInvalid := do(t, handler, http.MethodGet, keyPath, tokenA, nil)
	if !jsonHasField(t, afterInvalid.Body.Bytes(), "routing_preference", "price_first") {
		t.Fatalf("rejected update must not change the key: %s", afterInvalid.Body.String())
	}

	// Another customer cannot even see the key, let alone steer it.
	if w := do(t, handler, http.MethodPut, keyPath, tokenB, map[string]any{"routing_preference": "speed_first"}); w.Code != http.StatusNotFound {
		t.Fatalf("cross-customer update must 404, got %d %s", w.Code, w.Body.String())
	}

	// A platform admin manages the key on the customer's behalf.
	adminSet := do(t, handler, http.MethodPut, keyPath+"?user_id="+userIDString(clientA.ID), adminToken, map[string]any{"routing_preference": "success_first"})
	if adminSet.Code != http.StatusOK || !jsonHasField(t, adminSet.Body.Bytes(), "routing_preference", "success_first") {
		t.Fatalf("admin set=%d %s", adminSet.Code, adminSet.Body.String())
	}

	// Empty string clears back to the platform default and the field
	// disappears from the JSON instead of lingering as "".
	cleared := do(t, handler, http.MethodPut, keyPath, tokenA, map[string]any{"routing_preference": ""})
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear preference: %d %s", cleared.Code, cleared.Body.String())
	}
	var clearedRaw map[string]any
	if err := json.Unmarshal(cleared.Body.Bytes(), &clearedRaw); err != nil {
		t.Fatal(err)
	}
	if _, present := clearedRaw["routing_preference"]; present {
		t.Fatalf("cleared key must omit routing_preference: %s", cleared.Body.String())
	}

	// Three actual changes happened (set, admin set, clear); the rejected and
	// cross-customer attempts must not have left preference audits.
	audits, err := st.ListAuditsByTarget(ctx, "virtual_key", createdPayload.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	preferenceAudits := 0
	for _, audit := range audits {
		if audit.Action == "platform_key.routing_preference.update" {
			preferenceAudits++
		}
	}
	if preferenceAudits != 3 {
		t.Fatalf("preference audits=%d audits=%+v", preferenceAudits, audits)
	}

	// A no-op update (same value again) records the generic key update but
	// not a preference change.
	if w := do(t, handler, http.MethodPut, keyPath, tokenA, map[string]any{"routing_preference": ""}); w.Code != http.StatusOK {
		t.Fatalf("no-op update: %d %s", w.Code, w.Body.String())
	}
	audits, err = st.ListAuditsByTarget(ctx, "virtual_key", createdPayload.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	preferenceAudits = 0
	for _, audit := range audits {
		if audit.Action == "platform_key.routing_preference.update" {
			preferenceAudits++
		}
	}
	if preferenceAudits != 3 {
		t.Fatalf("a no-op update must not add a preference audit, got %d", preferenceAudits)
	}
}

func jsonHasField(t *testing.T, raw []byte, field, want string) bool {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode %s: %v", string(raw), err)
	}
	return fmt.Sprintf("%v", payload[field]) == want
}
