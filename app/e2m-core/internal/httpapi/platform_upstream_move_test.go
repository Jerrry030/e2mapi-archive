package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"e2m.local/contracts"
	"e2m.local/core/internal/vault"
)

// TestPlatformUpstreamCanMoveBetweenGroups covers the operator moving an
// upstream account to another group, which the console exposes as an editable
// group selector.
func TestPlatformUpstreamCanMoveBetweenGroups(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	srv.SetVault(vault.NewMemoryVault())
	srv.EnableInsecureSupplyUpstreams()
	handler := srv.Routes()
	ctx := context.Background()

	admin := createLoginUser(t, authSvc, "move-admin@example.com", contracts.UserRolePlatformAdmin)
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")

	newGroup := func(key, name string) contracts.UpstreamPool {
		t.Helper()
		response := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/groups", adminToken, key, map[string]any{
			"name": name, "models": []string{"gpt-4o-mini"}, "status": "active",
		})
		if response.Code != http.StatusCreated {
			t.Fatalf("create group %s: %d %s", name, response.Code, response.Body.String())
		}
		var group contracts.UpstreamPool
		if err := json.Unmarshal(response.Body.Bytes(), &group); err != nil || group.ID == "" {
			t.Fatalf("decode group %s: %v", name, err)
		}
		return group
	}
	source := newGroup("move-group-source", "主力池")
	target := newGroup("move-group-target", "备用池")

	created := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/upstreams", adminToken, "move-upstream-1", map[string]any{
		"group_id": source.ID, "name": "可迁移上游", "base_url": "http://mock-openai:8093/v1",
		"api_key": "move-secret", "models": []string{"gpt-4o-mini"},
		"prices": map[string]any{"gpt-4o-mini": map[string]any{
			"input_micros_per_million": 1000, "output_micros_per_million": 2000,
		}},
		"capacity": map[string]any{"max_concurrency": 4, "max_request_micros": 1_000_000},
		"status":   "active",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create upstream: %d %s", created.Code, created.Body.String())
	}
	var upstream platformUpstreamResponse
	if err := json.Unmarshal(created.Body.Bytes(), &upstream); err != nil || upstream.ID == "" {
		t.Fatalf("decode upstream: %v", err)
	}
	if upstream.GroupID != source.ID {
		t.Fatalf("upstream must start in the source group, got %s", upstream.GroupID)
	}

	moved := do(t, handler, http.MethodPut, "/api/v1/platform/upstreams/"+upstream.ID, adminToken, map[string]any{
		"group_id": target.ID,
	})
	if moved.Code != http.StatusOK {
		t.Fatalf("move upstream: %d %s", moved.Code, moved.Body.String())
	}
	var afterMove platformUpstreamResponse
	if err := json.Unmarshal(moved.Body.Bytes(), &afterMove); err != nil {
		t.Fatalf("decode moved upstream: %v", err)
	}
	if afterMove.GroupID != target.ID {
		t.Fatalf("upstream must report the target group, got %s", afterMove.GroupID)
	}

	// The move must be visible through the group-scoped listing on both sides.
	listed := do(t, handler, http.MethodGet, "/api/v1/platform/upstreams?group_id="+target.ID, adminToken, nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), upstream.ID) {
		t.Fatalf("target group must list the moved upstream: %d %s", listed.Code, listed.Body.String())
	}
	empty := do(t, handler, http.MethodGet, "/api/v1/platform/upstreams?group_id="+source.ID, adminToken, nil)
	if empty.Code != http.StatusOK || strings.Contains(empty.Body.String(), upstream.ID) {
		t.Fatalf("source group must no longer list the upstream: %d %s", empty.Code, empty.Body.String())
	}

	// Editing without touching group_id keeps the current membership.
	renamed := do(t, handler, http.MethodPut, "/api/v1/platform/upstreams/"+upstream.ID, adminToken, map[string]any{
		"name": "改个名字",
	})
	if renamed.Code != http.StatusOK || !strings.Contains(renamed.Body.String(), target.ID) {
		t.Fatalf("omitting group_id must keep membership: %d %s", renamed.Code, renamed.Body.String())
	}

	// A retired group cannot receive upstreams.
	if w := do(t, handler, http.MethodDelete, "/api/v1/platform/groups/"+source.ID, adminToken, nil); w.Code != http.StatusOK && w.Code != http.StatusAccepted {
		t.Fatalf("retire source group: %d %s", w.Code, w.Body.String())
	}
	rejected := do(t, handler, http.MethodPut, "/api/v1/platform/upstreams/"+upstream.ID, adminToken, map[string]any{
		"group_id": source.ID,
	})
	if rejected.Code != http.StatusConflict || !strings.Contains(rejected.Body.String(), "group_retired") {
		t.Fatalf("moving into a retired group must 409: %d %s", rejected.Code, rejected.Body.String())
	}

	// An unknown group is rejected as not found, not silently ignored.
	missing := do(t, handler, http.MethodPut, "/api/v1/platform/upstreams/"+upstream.ID, adminToken, map[string]any{
		"group_id": "pgrp-does-not-exist",
	})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown target group must 404, got %d %s", missing.Code, missing.Body.String())
	}
}
