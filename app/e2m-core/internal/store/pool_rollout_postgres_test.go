package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPostgresPoolRolloutEligibilityAndInstanceOwnership(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	suffix := newID("pool-rollout-pg")
	owner, err := st.CreateUser(ctx, contracts.User{
		Email: suffix + "-owner@example.test", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, contracts.User{
		Email: suffix + "-other@example.test", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: owner.ID, Name: "Pool rollout PG", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatal(err)
	}
	poolID := "pool-" + suffix
	if _, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: "Pool rollout PG"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM pool_rollout_operations WHERE pool_id=$1`, poolID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM pool_rollout_targets WHERE pool_id=$1`, poolID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM route_plans WHERE pool_id=$1`, poolID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_pools WHERE id=$1`, poolID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM instances WHERE id=$1`, instance.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=ANY($1)`, []int64{owner.ID, other.ID})
	})

	if _, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: poolID, Scope: contracts.PoolRolloutScopeInstance, UserID: other.ID,
		InstanceID: instance.ID, Enabled: true,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched instance owner error=%v, want ErrInvalid", err)
	}
	target, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: poolID, Scope: contracts.PoolRolloutScopeInstance, UserID: owner.ID,
		InstanceID: instance.ID, Enabled: true, Rollout: contracts.RolloutBatched, RolloutBatchSize: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: "plan-" + suffix, UserID: owner.ID, InstanceID: instance.ID, PoolID: poolID,
		Status: contracts.RoutePlanPublished, Rollout: contracts.RolloutBatched, RolloutBatchSize: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	owner, err = st.GetUser(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	owner.Enabled = false
	if _, err := st.UpdateUser(ctx, owner); err != nil {
		t.Fatal(err)
	}
	resolved, err := st.ResolvePoolRollout(ctx, poolID, owner.ID, instance.ID)
	if err != nil || resolved.Enabled || resolved.TargetID != target.ID || resolved.Rollout != contracts.RolloutBatched || resolved.RolloutBatchSize != 3 {
		t.Fatalf("ineligible resolution=%+v err=%v", resolved, err)
	}
	created, err := st.EnsurePoolRolloutOperations(ctx, poolID)
	if err != nil || len(created) != 1 || created[0].Action != contracts.PoolRolloutOperationDrain || created[0].PlanID != plan.ID {
		t.Fatalf("ineligible drain operations=%+v err=%v", created, err)
	}
	claimed, ok, err := st.ClaimPoolRolloutOperation(ctx, "pg-rollout-worker", time.Minute)
	if err != nil || !ok || claimed.DesiredFingerprint != PoolRolloutOperationFingerprint(resolved, contracts.PoolRolloutOperationDrain, plan.ID) {
		t.Fatalf("claimed=%+v ok=%v err=%v", claimed, ok, err)
	}
}
