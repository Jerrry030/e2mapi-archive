package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"e2m.local/contracts"
)

func TestInstanceWritesRejectInternalAndLocalFields(t *testing.T) {
	tests := []struct {
		name string
		role contracts.UserRole
	}{
		{name: "owner", role: contracts.UserRoleOwner},
		{name: "platform admin", role: contracts.UserRolePlatformAdmin},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, st, authSvc := newTestServer(t)
			ctx := context.Background()
			actor := createLoginUser(t, authSvc, tt.name+"-labels@example.com", tt.role)
			ownerID := actor.ID
			if tt.role == contracts.UserRolePlatformAdmin {
				ownerID = createStoreUser(t, st, tt.name+"-owner@example.com", contracts.UserRoleOwner).ID
			}
			token, _, err := authSvc.Login(ctx, actor.Email, "password123")
			if err != nil {
				t.Fatalf("login: %v", err)
			}
			h := srv.Routes()

			for _, forbidden := range []map[string]any{
				{"labels": map[string]string{"gateway_url": "https://gateway.internal", "admin_key": "must-not-reach-core"}},
				{"status": "https://gateway.internal/?token=secret"},
				{"version": "Bearer must-not-reach-core"},
			} {
				body := map[string]any{"user_id": ownerID, "name": "Must not be created", "kind": contracts.InstanceKindSub2API}
				for key, value := range forbidden {
					body[key] = value
				}
				w := do(t, h, http.MethodPost, "/api/v1/instances", token, body)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("create with internal field must be rejected: %d %s", w.Code, w.Body.String())
				}
			}
			instances, err := st.ListInstances(ctx, ownerID)
			if err != nil {
				t.Fatalf("list instances: %v", err)
			}
			if len(instances) != 0 {
				t.Fatalf("rejected create persisted an instance: %+v", instances)
			}

			existing, err := st.CreateInstance(ctx, contracts.Instance{
				UserID: ownerID,
				Name:   "Existing",
				Kind:   contracts.InstanceKindSub2API,
			})
			if err != nil {
				t.Fatalf("create existing instance: %v", err)
			}
			w := do(t, h, http.MethodPut, "/api/v1/instances/"+existing.ID, token, map[string]any{
				"name": "Must not be updated",
				"kind": contracts.InstanceKindSub2API,
				"labels": map[string]string{
					"gateway_url": "https://gateway.internal",
					"admin_key":   "must-not-reach-core",
				},
			})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("update with labels must be rejected: %d %s", w.Code, w.Body.String())
			}
			persisted, err := st.GetInstance(ctx, existing.ID)
			if err != nil {
				t.Fatalf("get existing instance: %v", err)
			}
			if persisted.Name != existing.Name {
				t.Fatalf("rejected update changed the instance name to %q", persisted.Name)
			}
		})
	}
}

func TestAccountMutationsRejectUnsafeIdentifiers(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	ctx := context.Background()
	user := createLoginUser(t, authSvc, "unsafe-account-id@example.com", contracts.UserRoleOwner)
	token, _, err := authSvc.Login(ctx, user.Email, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "Gateway", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}

	w := do(t, srv.Routes(), http.MethodPost, "/api/v1/instances/"+instance.ID+"/accounts/switch", token, map[string]any{
		"disable_account_id": "https://gateway.internal/?token=secret",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unsafe switch id must be rejected: %d %s", w.Code, w.Body.String())
	}

	w = do(t, srv.Routes(), http.MethodPost, "/api/v1/instances/"+instance.ID+"/accounts/switch", token, map[string]any{
		"disable_account_id": "same-id",
		"enable_account_id":  "same-id",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("same switch ids must be rejected: %d %s", w.Code, w.Body.String())
	}
}

func TestAccountMutationsCannotBypassManagedRoutePlan(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	ctx := context.Background()
	owner := createLoginUser(t, authSvc, "managed-account-fence@example.com", contracts.UserRoleOwner)
	token, _, err := authSvc.Login(ctx, owner.Email, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: owner.ID, Name: "Gateway", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "managed"})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	channel, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: pool.ID, DisplayName: "source A"})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: owner.ID, InstanceID: instance.ID, PoolID: pool.ID, Status: contracts.RoutePlanPublished,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: instance.ID, ChannelID: channel.ID,
		RemoteID: "managed-account", State: contracts.BindingActive,
	}); err != nil {
		t.Fatalf("create binding: %v", err)
	}

	h := srv.Routes()
	w := do(t, h, http.MethodPost, "/api/v1/instances/"+instance.ID+"/accounts/managed-account/schedulable", token, map[string]any{
		"schedulable": false, "reason": "manual override",
	})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "managed_account_requires_route_plan") {
		t.Fatalf("managed schedulable mutation must be fenced: %d %s", w.Code, w.Body.String())
	}

	w = do(t, h, http.MethodPost, "/api/v1/instances/"+instance.ID+"/accounts/switch", token, map[string]any{
		"disable_account_id": "managed-account", "enable_account_id": "unmanaged-account",
	})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "managed_account_requires_route_plan") {
		t.Fatalf("managed switch mutation must be fenced: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminCreateInstanceRequiresEnabledClientTarget(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	ctx := context.Background()
	admin := createLoginUser(t, authSvc, "instance-target-admin@example.com", contracts.UserRoleAdmin)
	supplier := createLoginUser(t, authSvc, "instance-target-supplier@example.com", contracts.UserRoleSupplier)
	disabled := createLoginUser(t, authSvc, "instance-target-disabled@example.com", contracts.UserRoleClient)
	disabledUpdate := disabled
	disabledUpdate.Enabled = false
	if _, err := st.UpdateUser(ctx, disabledUpdate); err != nil {
		t.Fatalf("disable client: %v", err)
	}
	token, _, err := authSvc.Login(ctx, admin.Email, "password123")
	if err != nil {
		t.Fatalf("login admin: %v", err)
	}

	for _, target := range []contracts.User{supplier, disabled} {
		w := do(t, srv.Routes(), http.MethodPost, "/api/v1/instances", token, map[string]any{
			"user_id": target.ID, "name": "invalid target", "kind": contracts.InstanceKindSub2API,
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("target %s: got %d %s", target.Email, w.Code, w.Body.String())
		}
	}
}
