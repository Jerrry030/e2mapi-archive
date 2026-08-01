package poolrollout

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

type fakePublisher struct {
	store       store.Store
	applyCalls  int
	rollbacks   int
	applyErr    error
	beforeApply func(context.Context)
}

func (p *fakePublisher) Apply(ctx context.Context, planID string) (contracts.ReconcilePlan, error) {
	p.applyCalls++
	if p.beforeApply != nil {
		p.beforeApply(ctx)
	}
	if err := contracts.RunReconcileSideEffectGuard(ctx); err != nil {
		return contracts.ReconcilePlan{}, err
	}
	if p.applyErr != nil {
		return contracts.ReconcilePlan{}, p.applyErr
	}
	plan, err := p.store.GetRoutePlan(ctx, planID)
	if err != nil {
		return contracts.ReconcilePlan{}, err
	}
	plan.Status = contracts.RoutePlanPublished
	if _, err := p.store.UpdateRoutePlan(ctx, plan); err != nil {
		return contracts.ReconcilePlan{}, err
	}
	return contracts.ReconcilePlan{PlanID: planID}, nil
}

func (p *fakePublisher) Rollback(ctx context.Context, planID string) (contracts.ReconcilePlan, error) {
	p.rollbacks++
	if err := contracts.RunReconcileSideEffectGuard(ctx); err != nil {
		return contracts.ReconcilePlan{}, err
	}
	plan, err := p.store.GetRoutePlan(ctx, planID)
	if err != nil {
		return contracts.ReconcilePlan{}, err
	}
	plan.Status = contracts.RoutePlanSuspended
	if _, err := p.store.UpdateRoutePlan(ctx, plan); err != nil {
		return contracts.ReconcilePlan{}, err
	}
	return contracts.ReconcilePlan{PlanID: planID}, nil
}

type fixture struct {
	ctx       context.Context
	store     *store.MemoryStore
	user      contracts.User
	instance  contracts.Instance
	pool      contracts.UpstreamPool
	plan      contracts.RoutePlan
	publisher *fakePublisher
	runner    *Runner
}

func newFixture(t *testing.T, planStatus contracts.RoutePlanStatus) *fixture {
	t.Helper()
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now().UTC())
	user, err := st.CreateUser(ctx, contracts.User{Email: "rollout-runner@example.test", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "Gateway", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "Managed", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID, Status: planStatus, Rollout: contracts.RolloutImmediate})
	if err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{store: st}
	runner := New(st, publisher, time.Hour)
	runner.batch = 1
	return &fixture{ctx: ctx, store: st, user: user, instance: instance, pool: pool, plan: plan, publisher: publisher, runner: runner}
}

func (f *fixture) grant(t *testing.T, enabled bool, mode contracts.RolloutMode) {
	t.Helper()
	_, err := f.store.UpsertPoolRolloutTarget(f.ctx, contracts.PoolRolloutTarget{
		PoolID: f.pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: f.user.ID,
		InstanceID: f.instance.ID, Enabled: enabled, Rollout: mode,
		RolloutCanaryCount: 1, RolloutBatchSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.EnsurePoolRolloutOperations(f.ctx, f.pool.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerDrainsDisabledPublishedPlanDurably(t *testing.T) {
	f := newFixture(t, contracts.RoutePlanPublished)
	f.grant(t, false, contracts.RolloutImmediate)
	f.runner.RunOnce(f.ctx)
	if f.publisher.rollbacks != 1 {
		t.Fatalf("rollback calls=%d", f.publisher.rollbacks)
	}
	plan, _ := f.store.GetRoutePlan(f.ctx, f.plan.ID)
	if plan.Status != contracts.RoutePlanSuspended {
		t.Fatalf("plan status=%s", plan.Status)
	}
	operations, _ := f.store.ListPoolRolloutOperations(f.ctx, f.pool.ID)
	if len(operations) != 1 || operations[0].Status != contracts.PoolRolloutOperationSucceeded || operations[0].LeaseOwner != "" {
		t.Fatalf("operations=%+v", operations)
	}
	// A repeated discovery/run is a no-op after durable success.
	if _, err := f.store.EnsurePoolRolloutOperations(f.ctx, f.pool.ID); err != nil {
		t.Fatal(err)
	}
	f.runner.RunOnce(f.ctx)
	if f.publisher.rollbacks != 1 {
		t.Fatalf("repeated rollback calls=%d", f.publisher.rollbacks)
	}
}

func TestRunnerRestoresSuspendedPlanWithCanaryAndCrashMarker(t *testing.T) {
	f := newFixture(t, contracts.RoutePlanSuspended)
	f.grant(t, true, contracts.RolloutCanary)
	f.runner.RunOnce(f.ctx)
	if f.publisher.applyCalls != 1 {
		t.Fatalf("apply calls=%d", f.publisher.applyCalls)
	}
	plan, _ := f.store.GetRoutePlan(f.ctx, f.plan.ID)
	if plan.Status != contracts.RoutePlanPublished || plan.Rollout != contracts.RolloutCanary || plan.RolloutCanaryCount != 1 ||
		plan.Labels["pool_rollout_operation"] == "" {
		t.Fatalf("plan=%+v", plan)
	}
	// Simulate completion-row loss after gateway+plan success. Reclaiming the
	// same operation sees the plan marker and must not apply the next canary wave.
	operations, _ := f.store.ListPoolRolloutOperations(f.ctx, f.pool.ID)
	operation := operations[0]
	operation.Status = contracts.PoolRolloutOperationRunning
	operation.LeaseOwner = f.runner.workerID
	f.runner.execute(f.ctx, &operation) // stale synthetic version is rejected
	if f.publisher.applyCalls != 1 {
		t.Fatalf("crash retry widened rollout; apply calls=%d", f.publisher.applyCalls)
	}
}

func TestRunnerPersistsFailureForRetry(t *testing.T) {
	f := newFixture(t, contracts.RoutePlanSuspended)
	f.grant(t, true, contracts.RolloutBatched)
	f.publisher.applyErr = errors.New("gateway unavailable")
	f.runner.RunOnce(f.ctx)
	operations, _ := f.store.ListPoolRolloutOperations(f.ctx, f.pool.ID)
	if len(operations) != 1 || operations[0].Status != contracts.PoolRolloutOperationFailed || operations[0].LastError == "" || operations[0].Attempts != 1 {
		t.Fatalf("operations=%+v", operations)
	}
	f.publisher.applyErr = nil
	f.runner.RunOnce(f.ctx)
	operations, _ = f.store.ListPoolRolloutOperations(f.ctx, f.pool.ID)
	if operations[0].Status != contracts.PoolRolloutOperationSucceeded || operations[0].Attempts != 2 || f.publisher.applyCalls != 2 {
		t.Fatalf("retry operations=%+v apply=%d", operations, f.publisher.applyCalls)
	}
}

func TestRunnerSupersedesStaleRuleBeforeSideEffect(t *testing.T) {
	f := newFixture(t, contracts.RoutePlanPublished)
	f.grant(t, false, contracts.RolloutImmediate)
	operation, claimed, err := f.store.ClaimPoolRolloutOperation(f.ctx, f.runner.workerID, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	_, err = f.store.UpsertPoolRolloutTarget(f.ctx, contracts.PoolRolloutTarget{
		PoolID: f.pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: f.user.ID,
		InstanceID: f.instance.ID, Enabled: true, Rollout: contracts.RolloutImmediate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.runner.execute(f.ctx, &operation); !errors.Is(err, errOperationSuperseded) {
		t.Fatalf("execute err=%v", err)
	}
	if f.publisher.rollbacks != 0 || f.publisher.applyCalls != 0 {
		t.Fatalf("side effects rollback=%d apply=%d", f.publisher.rollbacks, f.publisher.applyCalls)
	}
}

func TestRunnerSupersedesPublishClaimedBeforeOnboardingOwnedThePlan(t *testing.T) {
	f := newFixture(t, contracts.RoutePlanDraft)
	f.grant(t, true, contracts.RolloutCanary)

	plan, err := f.store.GetRoutePlan(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan.Labels = map[string]string{"managed_by": "e2m-onboarding"}
	if _, err := f.store.UpdateRoutePlan(f.ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpsertOnboardingWorkflow(f.ctx, contracts.OnboardingWorkflow{
		UserID: f.user.ID, InstanceID: f.instance.ID, PoolID: f.pool.ID,
		ConnectorID: "connector-race", DesiredFingerprint: "generation-race",
	}); err != nil {
		t.Fatal(err)
	}

	f.runner.RunOnce(f.ctx)
	if f.publisher.applyCalls != 0 {
		t.Fatalf("onboarding-owned first publish reached publisher: calls=%d", f.publisher.applyCalls)
	}
	operations, err := f.store.ListPoolRolloutOperations(f.ctx, f.pool.ID)
	if err != nil || len(operations) != 1 || operations[0].Status != contracts.PoolRolloutOperationSuperseded || operations[0].Attempts != 1 {
		t.Fatalf("operations=%+v err=%v", operations, err)
	}
	plan, err = f.store.GetRoutePlan(f.ctx, f.plan.ID)
	if err != nil || plan.Status != contracts.RoutePlanDraft || plan.Labels["pool_rollout_operation"] != "" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

func TestRunnerSupersedesOldRunningPolicyAndOnlyNewOperationApplies(t *testing.T) {
	f := newFixture(t, contracts.RoutePlanDraft)
	f.grant(t, true, contracts.RolloutCanary)
	old, claimed, err := f.store.ClaimPoolRolloutOperation(f.ctx, f.runner.workerID, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("old claim=%v err=%v", claimed, err)
	}
	if _, err := f.store.UpsertPoolRolloutTarget(f.ctx, contracts.PoolRolloutTarget{
		PoolID: f.pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: f.user.ID,
		InstanceID: f.instance.ID, Enabled: true, Rollout: contracts.RolloutBatched, RolloutBatchSize: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.EnsurePoolRolloutOperations(f.ctx, f.pool.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.runner.execute(f.ctx, &old); !errors.Is(err, errOperationSuperseded) {
		t.Fatalf("old execute err=%v", err)
	}
	if f.publisher.applyCalls != 0 {
		t.Fatalf("stale operation applied: calls=%d", f.publisher.applyCalls)
	}
	f.runner.RunOnce(f.ctx)
	if f.publisher.applyCalls != 1 {
		t.Fatalf("new operation apply calls=%d", f.publisher.applyCalls)
	}
	operations, err := f.store.ListPoolRolloutOperations(f.ctx, f.pool.ID)
	if err != nil || len(operations) != 2 {
		t.Fatalf("operations=%+v err=%v", operations, err)
	}
	statuses := map[contracts.PoolRolloutOperationStatus]int{}
	for _, operation := range operations {
		statuses[operation.Status]++
	}
	if statuses[contracts.PoolRolloutOperationSuperseded] != 1 || statuses[contracts.PoolRolloutOperationSucceeded] != 1 {
		t.Fatalf("operation statuses=%+v operations=%+v", statuses, operations)
	}
}

func TestRunnerSideEffectFenceSupersedesInPlacePolicyUpdate(t *testing.T) {
	f := newFixture(t, contracts.RoutePlanDraft)
	f.grant(t, true, contracts.RolloutCanary)
	changed := false
	f.publisher.beforeApply = func(context.Context) {
		if changed {
			return
		}
		changed = true
		if _, err := f.store.UpsertPoolRolloutTarget(f.ctx, contracts.PoolRolloutTarget{
			PoolID: f.pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: f.user.ID,
			InstanceID: f.instance.ID, Enabled: true, Rollout: contracts.RolloutBatched, RolloutBatchSize: 4,
		}); err != nil {
			t.Fatalf("policy update: %v", err)
		}
	}
	f.runner.RunOnce(f.ctx)
	if f.publisher.applyCalls != 1 {
		t.Fatalf("publisher calls=%d", f.publisher.applyCalls)
	}
	plan, err := f.store.GetRoutePlan(f.ctx, f.plan.ID)
	if err != nil || plan.Status != contracts.RoutePlanDraft {
		t.Fatalf("stale policy mutated gateway plan=%+v err=%v", plan, err)
	}
	operations, err := f.store.ListPoolRolloutOperations(f.ctx, f.pool.ID)
	if err != nil || len(operations) != 1 || operations[0].Status != contracts.PoolRolloutOperationSuperseded {
		t.Fatalf("operations=%+v err=%v", operations, err)
	}
}
