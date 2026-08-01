package onboarding

import (
	"testing"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

func TestRunnerPoolRolloutDenyByDefault(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	rolloutStore, ok := store.AsPoolRolloutStore(fixture.store)
	if !ok {
		t.Fatal("fixture store does not support pool rollout")
	}
	if err := rolloutStore.DeletePoolRolloutTarget(
		fixture.ctx, fixture.pool.ID, contracts.PoolRolloutScopeInstance,
		fixture.instance.UserID, fixture.instance.ID,
	); err != nil {
		t.Fatal(err)
	}
	fixture.runner.RunOnce(fixture.ctx)
	workflows, err := fixture.store.ListOnboardingWorkflows(fixture.ctx, contracts.OnboardingWorkflowFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) != 0 || fixture.publisher.calls != 0 {
		t.Fatalf("workflows=%+v publish=%d", workflows, fixture.publisher.calls)
	}
}

func TestRunnerPoolRolloutCopiesCanaryPolicy(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	rolloutStore, _ := store.AsPoolRolloutStore(fixture.store)
	if _, err := rolloutStore.UpsertPoolRolloutTarget(fixture.ctx, contracts.PoolRolloutTarget{
		PoolID: fixture.pool.ID, Scope: contracts.PoolRolloutScopeInstance,
		UserID: fixture.instance.UserID, InstanceID: fixture.instance.ID,
		Enabled: true, Rollout: contracts.RolloutCanary, RolloutCanaryCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	fixture.runner.RunOnce(fixture.ctx)
	plans, err := fixture.store.ListRoutePlans(fixture.ctx, fixture.instance.UserID)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%+v err=%v", plans, err)
	}
	if plans[0].Rollout != contracts.RolloutCanary || plans[0].RolloutCanaryCount != 2 {
		t.Fatalf("plan=%+v", plans[0])
	}
}

func TestRunnerPoolRolloutDisableDormantsExistingWorkflow(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	rolloutStore, _ := store.AsPoolRolloutStore(fixture.store)
	if err := fixture.runner.discover(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := rolloutStore.UpsertPoolRolloutTarget(fixture.ctx, contracts.PoolRolloutTarget{
		PoolID: fixture.pool.ID, Scope: contracts.PoolRolloutScopeInstance,
		UserID: fixture.instance.UserID, InstanceID: fixture.instance.ID,
		Enabled: false, Rollout: contracts.RolloutImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	fixture.runner.RunOnce(fixture.ctx)
	workflow := onlyOnboardingWorkflow(t, fixture)
	if workflow.Status != contracts.OnboardingDormantStatus || workflow.Stage != contracts.OnboardingDormant ||
		workflow.LastErrorCode != "pool_rollout_disabled" {
		t.Fatalf("workflow=%+v", workflow)
	}
}

func TestRunnerPoolRolloutReenablesSuspendedManagedPlan(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	rolloutStore, _ := store.AsPoolRolloutStore(fixture.store)
	plan, err := fixture.store.CreateRoutePlan(fixture.ctx, contracts.RoutePlan{
		UserID: fixture.instance.UserID, InstanceID: fixture.instance.ID, PoolID: fixture.pool.ID,
		Status: contracts.RoutePlanSuspended, Rollout: contracts.RolloutImmediate,
		Labels: map[string]string{"managed_by": "e2m-onboarding"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rolloutStore.UpsertPoolRolloutTarget(fixture.ctx, contracts.PoolRolloutTarget{
		PoolID: fixture.pool.ID, Scope: contracts.PoolRolloutScopeInstance,
		UserID: fixture.instance.UserID, InstanceID: fixture.instance.ID,
		Enabled: true, Rollout: contracts.RolloutBatched, RolloutBatchSize: 1,
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := fixture.runner.ensurePlan(fixture.ctx, contracts.OnboardingWorkflow{
		UserID: fixture.instance.UserID, InstanceID: fixture.instance.ID, PoolID: fixture.pool.ID,
	}, fixture.instance)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != plan.ID || resolved.Status != contracts.RoutePlanDraft ||
		resolved.Rollout != contracts.RolloutBatched || resolved.RolloutBatchSize != 1 {
		t.Fatalf("resolved=%+v", resolved)
	}
}

func TestRunnerDisabledDormantIsNotWokenByDiscovery(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	rolloutStore, _ := store.AsPoolRolloutStore(fixture.store)
	if err := fixture.runner.discover(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := rolloutStore.UpsertPoolRolloutTarget(fixture.ctx, contracts.PoolRolloutTarget{
		PoolID: fixture.pool.ID, Scope: contracts.PoolRolloutScopeInstance,
		UserID: fixture.instance.UserID, InstanceID: fixture.instance.ID,
		Enabled: false, Rollout: contracts.RolloutImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	fixture.runner.RunOnce(fixture.ctx)
	dormant := onlyOnboardingWorkflow(t, fixture)
	if dormant.Status != contracts.OnboardingDormantStatus || dormant.LastErrorCode != "pool_rollout_disabled" {
		t.Fatalf("dormant=%+v", dormant)
	}
	for i := 0; i < 3; i++ {
		fixture.runner.RunOnce(fixture.ctx)
	}
	stable := onlyOnboardingWorkflow(t, fixture)
	if stable.Status != contracts.OnboardingDormantStatus || stable.Version != dormant.Version ||
		stable.DesiredGeneration != dormant.DesiredGeneration || fixture.publisher.calls != 0 {
		t.Fatalf("disabled workflow was woken: before=%+v after=%+v publish=%d", dormant, stable, fixture.publisher.calls)
	}
}

func TestRunnerHeldRolloutWaitsForExplicitPolicyChange(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	rolloutStore, _ := store.AsPoolRolloutStore(fixture.store)
	if _, err := rolloutStore.UpsertPoolRolloutTarget(fixture.ctx, contracts.PoolRolloutTarget{
		PoolID: fixture.pool.ID, Scope: contracts.PoolRolloutScopeInstance,
		UserID: fixture.instance.UserID, InstanceID: fixture.instance.ID,
		Enabled: true, Rollout: contracts.RolloutCanary, RolloutCanaryCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	fixture.publisher.hold = true
	fixture.runner.RunOnce(fixture.ctx)
	dormant := onlyOnboardingWorkflow(t, fixture)
	if dormant.Status != contracts.OnboardingDormantStatus || dormant.LastErrorCode != "rollout_observation_pending" || fixture.publisher.calls != 1 {
		t.Fatalf("dormant=%+v publish=%d", dormant, fixture.publisher.calls)
	}
	for i := 0; i < 3; i++ {
		fixture.runner.RunOnce(fixture.ctx)
	}
	stable := onlyOnboardingWorkflow(t, fixture)
	if stable.Version != dormant.Version || fixture.publisher.calls != 1 {
		t.Fatalf("ordinary retries widened rollout: before=%+v after=%+v publish=%d", dormant, stable, fixture.publisher.calls)
	}

	// Explicitly widening the approved canary count changes the desired
	// fingerprint, which wakes this exact workflow for the next observed wave.
	if _, err := rolloutStore.UpsertPoolRolloutTarget(fixture.ctx, contracts.PoolRolloutTarget{
		PoolID: fixture.pool.ID, Scope: contracts.PoolRolloutScopeInstance,
		UserID: fixture.instance.UserID, InstanceID: fixture.instance.ID,
		Enabled: true, Rollout: contracts.RolloutCanary, RolloutCanaryCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.runner.discover(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	woken := onlyOnboardingWorkflow(t, fixture)
	if woken.Status != contracts.OnboardingPending || woken.DesiredGeneration <= dormant.DesiredGeneration {
		t.Fatalf("explicit policy change did not wake rollout: %+v", woken)
	}
}
