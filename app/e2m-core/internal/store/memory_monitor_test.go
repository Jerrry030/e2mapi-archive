package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryInstanceMonitorPolicyDefaultsAndUpsert(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	st := NewMemoryStore(clock)
	st.now = func() time.Time { return clock }
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: 42, Name: "Gateway", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}

	policy, err := st.GetInstanceMonitorPolicy(ctx, instance.ID)
	if err != nil {
		t.Fatalf("get default policy: %v", err)
	}
	if !policy.Enabled || policy.CheckIntervalSeconds != 60 || policy.FailStreak != 2 ||
		policy.AutoSwitch || policy.CooldownSeconds != 300 || !policy.DriftDetection || policy.UserID != 42 {
		t.Fatalf("unexpected default policy: %+v", policy)
	}

	clock = clock.Add(time.Minute)
	policy.Enabled = false
	policy.CheckIntervalSeconds = 300
	policy.FailStreak = 5
	policy.AutoSwitch = true
	policy.CooldownSeconds = 1800
	policy.DriftDetection = false
	updated, err := st.UpsertInstanceMonitorPolicy(ctx, policy)
	if err != nil {
		t.Fatalf("upsert policy: %v", err)
	}
	if updated.UpdatedAt == nil || !updated.UpdatedAt.Equal(clock) || updated.UserID != instance.UserID {
		t.Fatalf("upsert did not preserve owner/time: %+v", updated)
	}
	got, err := st.GetInstanceMonitorPolicy(ctx, instance.ID)
	if err != nil || got != updated {
		t.Fatalf("get updated policy: got=%+v err=%v", got, err)
	}
}

func TestMemoryInstanceMonitorPolicyRejectsWrongOwnerAndUnknownInstance(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: 7, Name: "Gateway", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	policy := contracts.DefaultInstanceMonitorPolicy(instance.ID, 8)
	if _, err := st.UpsertInstanceMonitorPolicy(ctx, policy); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong owner error = %v, want ErrConflict", err)
	}
	policy = contracts.DefaultInstanceMonitorPolicy("missing", 7)
	if _, err := st.UpsertInstanceMonitorPolicy(ctx, policy); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown instance error = %v, want ErrNotFound", err)
	}
}
