package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

type monitorHealthStub struct {
	snapshot contracts.InstanceHealthSnapshot
	err      error
	called   []string
}

func (s *monitorHealthStub) Snapshots(string) []contracts.InstanceHealthSnapshot { return nil }
func (s *monitorHealthStub) CheckNow(_ context.Context, instanceID string) (contracts.InstanceHealthSnapshot, error) {
	s.called = append(s.called, instanceID)
	return s.snapshot, s.err
}

func TestInstanceMonitorPolicyAPI(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	owner := createLoginUser(t, authSvc, "monitor-owner@example.com", contracts.UserRoleOwner)
	other := createLoginUser(t, authSvc, "monitor-other@example.com", contracts.UserRoleOwner)
	ownerToken, _, _ := authSvc.Login(context.Background(), owner.Email, "password123")
	otherToken, _, _ := authSvc.Login(context.Background(), other.Email, "password123")
	instance, err := st.CreateInstance(context.Background(), contracts.Instance{
		UserID: owner.ID, Name: "Gateway", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	h := srv.Routes()
	path := "/api/v1/instances/" + instance.ID + "/monitor-policy"

	w := do(t, h, http.MethodGet, path, ownerToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get default policy: %d %s", w.Code, w.Body.String())
	}
	var policy contracts.InstanceMonitorPolicy
	decodeResponse(t, w, &policy)
	if !policy.Enabled || policy.AutoSwitch || policy.CheckIntervalSeconds != 60 {
		t.Fatalf("unexpected default policy: %+v", policy)
	}
	if body := w.Body.String(); containsJSONField(body, "user_id") {
		t.Fatalf("monitor policy exposed redundant ownership: %s", body)
	}

	w = do(t, h, http.MethodPut, path, ownerToken, map[string]any{
		"enabled": true, "check_interval_seconds": 30, "fail_streak": 3,
		"auto_switch": false, "cooldown_seconds": 900, "drift_detection": true,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update policy: %d %s", w.Code, w.Body.String())
	}
	decodeResponse(t, w, &policy)
	if policy.CheckIntervalSeconds != 30 || policy.FailStreak != 3 || policy.CooldownSeconds != 900 {
		t.Fatalf("unexpected updated policy: %+v", policy)
	}

	w = do(t, h, http.MethodPut, path, ownerToken, map[string]any{
		"enabled": true, "check_interval_seconds": 10, "fail_streak": 0,
		"auto_switch": false, "cooldown_seconds": 60, "drift_detection": true,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid presets status = %d, want 400: %s", w.Code, w.Body.String())
	}
	w = do(t, h, http.MethodPut, path, ownerToken, map[string]any{
		"enabled": true, "check_interval_seconds": 60, "fail_streak": 2,
		"auto_switch": false, "cooldown_seconds": 300, "drift_detection": true,
		"lease_seconds": 30,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown internal field status = %d, want 400: %s", w.Code, w.Body.String())
	}
	w = do(t, h, http.MethodGet, path, otherToken, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-account read status = %d, want 403", w.Code)
	}

	audits, err := st.ListAudits(context.Background(), owner.ID)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	found := false
	for _, audit := range audits {
		if audit.Action == "instance.monitor_policy.update" && audit.InstanceID == instance.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("policy update audit was not recorded")
	}
}

func containsJSONField(body, field string) bool {
	return strings.Contains(body, `"`+field+`"`)
}

func TestInstanceHealthCheckNowAPI(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	owner := createLoginUser(t, authSvc, "check-now-owner@example.com", contracts.UserRoleOwner)
	token, _, _ := authSvc.Login(context.Background(), owner.Email, "password123")
	instance, err := st.CreateInstance(context.Background(), contracts.Instance{
		UserID: owner.ID, Name: "Gateway", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	stub := &monitorHealthStub{snapshot: contracts.InstanceHealthSnapshot{
		InstanceID: instance.ID, UserID: owner.ID, CheckedAt: time.Now().UTC(),
	}}
	srv.health = stub
	path := "/api/v1/instances/" + instance.ID + "/health-check"
	w := do(t, srv.Routes(), http.MethodPost, path, token, nil)
	if w.Code != http.StatusOK || len(stub.called) != 1 || stub.called[0] != instance.ID {
		t.Fatalf("check now: status=%d called=%v body=%s", w.Code, stub.called, w.Body.String())
	}

	stub.err = store.ErrNotFound
	w = do(t, srv.Routes(), http.MethodPost, path, token, nil)
	if w.Code == http.StatusOK {
		t.Fatal("checker error was reported as success")
	}
	if !errors.Is(stub.err, store.ErrNotFound) {
		t.Fatal("test setup lost checker error")
	}
}

func TestInstanceHealthCheckNowUnavailable(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	owner := createLoginUser(t, authSvc, "check-now-unavailable@example.com", contracts.UserRoleOwner)
	token, _, _ := authSvc.Login(context.Background(), owner.Email, "password123")
	instance, _ := st.CreateInstance(context.Background(), contracts.Instance{
		UserID: owner.ID, Name: "Gateway", Kind: contracts.InstanceKindSub2API,
	})
	w := do(t, srv.Routes(), http.MethodPost, "/api/v1/instances/"+instance.ID+"/health-check", token, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable checker status = %d, want 503", w.Code)
	}
}

func TestInstanceHealthCheckNowReportsConcurrentCheck(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	owner := createLoginUser(t, authSvc, "check-now-running@example.com", contracts.UserRoleOwner)
	token, _, _ := authSvc.Login(context.Background(), owner.Email, "password123")
	instance, _ := st.CreateInstance(context.Background(), contracts.Instance{
		UserID: owner.ID, Name: "Gateway", Kind: contracts.InstanceKindSub2API,
	})
	srv.health = &monitorHealthStub{err: errors.New("health: instance check is already running")}
	w := do(t, srv.Routes(), http.MethodPost, "/api/v1/instances/"+instance.ID+"/health-check", token, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("concurrent check status = %d, want 409: %s", w.Code, w.Body.String())
	}
}
