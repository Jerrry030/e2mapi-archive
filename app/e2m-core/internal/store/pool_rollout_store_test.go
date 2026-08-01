package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

func seedPoolRolloutStore(t *testing.T) (*MemoryStore, contracts.User, contracts.Instance, contracts.UpstreamPool) {
	t.Helper()
	ctx := context.Background()
	st := NewMemoryStore(time.Now().UTC())
	user, err := st.CreateUser(ctx, contracts.User{
		Email: "rollout@example.test", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID, Name: "Gateway", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "Managed", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatal(err)
	}
	return st, user, instance, pool
}

func TestPoolRolloutDenyByDefaultAndPrecedence(t *testing.T) {
	ctx := context.Background()
	st, user, instance, pool := seedPoolRolloutStore(t)

	resolved, err := st.ResolvePoolRollout(ctx, pool.ID, user.ID, instance.ID)
	if err != nil || resolved.Enabled || resolved.Source != "" {
		t.Fatalf("default resolution=%+v err=%v", resolved, err)
	}

	userTarget, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeUser, UserID: user.ID,
		Enabled: true, Rollout: contracts.RolloutBatched, RolloutBatchSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = st.ResolvePoolRollout(ctx, pool.ID, user.ID, instance.ID)
	if err != nil || !resolved.Enabled || resolved.Source != contracts.PoolRolloutScopeUser ||
		resolved.TargetID != userTarget.ID || resolved.Rollout != contracts.RolloutBatched || resolved.RolloutBatchSize != 2 {
		t.Fatalf("user resolution=%+v err=%v", resolved, err)
	}

	instanceTarget, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: user.ID,
		InstanceID: instance.ID, Enabled: false, Rollout: contracts.RolloutCanary, RolloutCanaryCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = st.ResolvePoolRollout(ctx, pool.ID, user.ID, instance.ID)
	if err != nil || resolved.Enabled || resolved.Source != contracts.PoolRolloutScopeInstance || resolved.TargetID != instanceTarget.ID {
		t.Fatalf("instance resolution=%+v err=%v", resolved, err)
	}
}

func TestPoolRolloutUpsertIsIdempotentAndDeleteRestoresParent(t *testing.T) {
	ctx := context.Background()
	st, user, instance, pool := seedPoolRolloutStore(t)
	first, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeUser, UserID: user.ID, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeUser, UserID: user.ID,
		Enabled: false, Rollout: contracts.RolloutCanary, RolloutCanaryCount: 2,
	})
	if err != nil || second.ID != first.ID || second.CreatedAt != first.CreatedAt || !second.UpdatedAt.Equal(first.UpdatedAt) && second.UpdatedAt.Before(first.UpdatedAt) {
		t.Fatalf("idempotent update first=%+v second=%+v err=%v", first, second, err)
	}
	targets, err := st.ListPoolRolloutTargets(ctx, pool.ID)
	if err != nil || len(targets) != 1 || targets[0].Enabled {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	if err := st.DeletePoolRolloutTarget(ctx, pool.ID, contracts.PoolRolloutScopeUser, user.ID, ""); err != nil {
		t.Fatal(err)
	}
	resolved, err := st.ResolvePoolRollout(ctx, pool.ID, user.ID, instance.ID)
	if err != nil || resolved.Enabled {
		t.Fatalf("after delete resolution=%+v err=%v", resolved, err)
	}
	if err := st.DeletePoolRolloutTarget(ctx, pool.ID, contracts.PoolRolloutScopeUser, user.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeat delete err=%v", err)
	}
}

func TestPoolRolloutRejectsMismatchedInstanceOwner(t *testing.T) {
	ctx := context.Background()
	st, user, _, pool := seedPoolRolloutStore(t)
	other, err := st.CreateUser(ctx, contracts.User{
		Email: "other-rollout@example.test", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherInstance, err := st.CreateInstance(ctx, contracts.Instance{UserID: other.ID, Name: "Other", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: user.ID,
		InstanceID: otherInstance.ID, Enabled: true,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched owner err=%v", err)
	}
}

func TestPoolRolloutDoesNotCreatePublishOperationBeforeOnboardingCreatesPlan(t *testing.T) {
	ctx := context.Background()
	st, user, instance, pool := seedPoolRolloutStore(t)
	if _, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: user.ID,
		InstanceID: instance.ID, Enabled: true, Rollout: contracts.RolloutImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	created, err := st.EnsurePoolRolloutOperations(ctx, pool.ID)
	if err != nil || len(created) != 0 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	operations, err := st.ListPoolRolloutOperations(ctx, pool.ID)
	if err != nil || len(operations) != 0 {
		t.Fatalf("operations=%+v err=%v", operations, err)
	}
}

func TestPoolRolloutLeavesOnboardingManagedFirstPublishToCurrentWorkflow(t *testing.T) {
	ctx := context.Background()
	st, user, instance, pool := seedPoolRolloutStore(t)
	if _, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanDraft, Rollout: contracts.RolloutImmediate,
		Labels: map[string]string{"managed_by": "e2m-onboarding"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID,
		ConnectorID: "connector-first-publish", DesiredFingerprint: "generation-one",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: user.ID,
		InstanceID: instance.ID, Enabled: true, Rollout: contracts.RolloutCanary, RolloutCanaryCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	created, err := st.EnsurePoolRolloutOperations(ctx, pool.ID)
	if err != nil || len(created) != 0 {
		t.Fatalf("onboarding first publish created=%+v err=%v", created, err)
	}
}

func TestPoolRolloutStillPublishesManualDraftPlan(t *testing.T) {
	ctx := context.Background()
	st, user, instance, pool := seedPoolRolloutStore(t)
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanDraft, Rollout: contracts.RolloutImmediate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: user.ID,
		InstanceID: instance.ID, Enabled: true, Rollout: contracts.RolloutCanary, RolloutCanaryCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	created, err := st.EnsurePoolRolloutOperations(ctx, pool.ID)
	if err != nil || len(created) != 1 || created[0].Action != contracts.PoolRolloutOperationPublish || created[0].PlanID != plan.ID {
		t.Fatalf("manual draft created=%+v err=%v", created, err)
	}
}

func TestPoolRolloutNeverPublishesOnboardingManagedPlan(t *testing.T) {
	ctx := context.Background()
	st, user, instance, pool := seedPoolRolloutStore(t)
	_, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanPublished, Rollout: contracts.RolloutImmediate,
		Labels: map[string]string{"managed_by": "e2m-onboarding"},
	})
	if err != nil {
		t.Fatal(err)
	}
	nextCheck := time.Now().UTC().Add(time.Hour)
	if _, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID,
		Stage: contracts.OnboardingActive, Status: contracts.OnboardingReady,
		NextAttemptAt: &nextCheck, DesiredFingerprint: "ready-generation",
		DesiredGeneration: 1, LastReadyGeneration: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: user.ID,
		InstanceID: instance.ID, Enabled: true, Rollout: contracts.RolloutCanary, RolloutCanaryCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	created, err := st.EnsurePoolRolloutOperations(ctx, pool.ID)
	if err != nil || len(created) != 0 {
		t.Fatalf("managed plan publish must remain owned by onboarding: created=%+v err=%v", created, err)
	}
}

func TestPoolRolloutResolutionDisablesIneligibleClientAndEnqueuesDrain(t *testing.T) {
	ctx := context.Background()
	st, user, instance, pool := seedPoolRolloutStore(t)
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanPublished, Rollout: contracts.RolloutImmediate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: user.ID,
		InstanceID: instance.ID, Enabled: true, Rollout: contracts.RolloutImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	user.Enabled = false
	if _, err := st.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	resolved, err := st.ResolvePoolRollout(ctx, pool.ID, user.ID, instance.ID)
	if err != nil || resolved.Enabled || resolved.TargetID == "" {
		t.Fatalf("disabled client resolution=%+v err=%v", resolved, err)
	}
	created, err := st.EnsurePoolRolloutOperations(ctx, pool.ID)
	if err != nil || len(created) != 1 || created[0].Action != contracts.PoolRolloutOperationDrain || created[0].PlanID != plan.ID {
		t.Fatalf("disabled client drain=%+v err=%v", created, err)
	}
}

func TestPoolRolloutOperationsAreDurableIdempotentAndLeased(t *testing.T) {
	ctx := context.Background()
	st, user, instance, pool := seedPoolRolloutStore(t)
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanPublished, Rollout: contracts.RolloutImmediate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: user.ID,
		InstanceID: instance.ID, Enabled: false, Rollout: contracts.RolloutImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	created, err := st.EnsurePoolRolloutOperations(ctx, pool.ID)
	if err != nil || len(created) != 1 || created[0].Action != contracts.PoolRolloutOperationDrain || created[0].PlanID != plan.ID {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	duplicate, err := st.EnsurePoolRolloutOperations(ctx, pool.ID)
	if err != nil || len(duplicate) != 0 {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	claimed, ok, err := st.ClaimPoolRolloutOperation(ctx, "worker-a", time.Minute)
	if err != nil || !ok || claimed.Status != contracts.PoolRolloutOperationRunning || claimed.Attempts != 1 || claimed.Version != 2 {
		t.Fatalf("claimed=%+v ok=%v err=%v", claimed, ok, err)
	}
	if _, ok, err := st.ClaimPoolRolloutOperation(ctx, "worker-b", time.Minute); err != nil || ok {
		t.Fatalf("second claim ok=%v err=%v", ok, err)
	}
	if _, err := st.CompletePoolRolloutOperation(ctx, claimed.ID, "worker-b", claimed.Version, contracts.PoolRolloutOperationSucceeded, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign completion err=%v", err)
	}
	completed, err := st.CompletePoolRolloutOperation(ctx, claimed.ID, "worker-a", claimed.Version, contracts.PoolRolloutOperationSucceeded, "")
	if err != nil || completed.Status != contracts.PoolRolloutOperationSucceeded || completed.LeaseOwner != "" || completed.LeaseUntil != nil {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func TestPoolRolloutRuleChangeSupersedesPendingOperation(t *testing.T) {
	ctx := context.Background()
	st, user, instance, pool := seedPoolRolloutStore(t)
	if _, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanPublished, Rollout: contracts.RolloutImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: user.ID,
		InstanceID: instance.ID, Enabled: false, Rollout: contracts.RolloutImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsurePoolRolloutOperations(ctx, pool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: user.ID,
		InstanceID: instance.ID, Enabled: true, Rollout: contracts.RolloutCanary, RolloutCanaryCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsurePoolRolloutOperations(ctx, pool.ID); err != nil {
		t.Fatal(err)
	}
	operations, err := st.ListPoolRolloutOperations(ctx, pool.ID)
	if err != nil || len(operations) != 2 {
		t.Fatalf("operations=%+v err=%v", operations, err)
	}
	byAction := make(map[contracts.PoolRolloutOperationAction]contracts.PoolRolloutOperation)
	for _, operation := range operations {
		byAction[operation.Action] = operation
	}
	if byAction[contracts.PoolRolloutOperationPublish].Status != contracts.PoolRolloutOperationPending ||
		byAction[contracts.PoolRolloutOperationDrain].Status != contracts.PoolRolloutOperationSuperseded {
		t.Fatalf("operations=%+v", operations)
	}
}

func TestPoolRolloutFailedDrainIsMadePendingForDurableRetry(t *testing.T) {
	ctx := context.Background()
	st, user, instance, pool := seedPoolRolloutStore(t)
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanPublished, Rollout: contracts.RolloutImmediate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: user.ID,
		InstanceID: instance.ID, Enabled: false, Rollout: contracts.RolloutImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	created, err := st.EnsurePoolRolloutOperations(ctx, pool.ID)
	if err != nil || len(created) != 1 || created[0].PlanID != plan.ID {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	claimed, ok, err := st.ClaimPoolRolloutOperation(ctx, "retry-worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claimed=%+v ok=%v err=%v", claimed, ok, err)
	}
	failed, err := st.CompletePoolRolloutOperation(
		ctx, claimed.ID, "retry-worker", claimed.Version, contracts.PoolRolloutOperationFailed, "gateway unavailable",
	)
	if err != nil || failed.Status != contracts.PoolRolloutOperationFailed {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	if _, err := st.EnsurePoolRolloutOperations(ctx, pool.ID); err != nil {
		t.Fatal(err)
	}
	operations, err := st.ListPoolRolloutOperations(ctx, pool.ID)
	if err != nil || len(operations) != 1 || operations[0].Status != contracts.PoolRolloutOperationPending || operations[0].LastError != "" {
		t.Fatalf("operations=%+v err=%v", operations, err)
	}
}
