package retirement

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

type rollbackFake struct {
	failRollback        map[string]error
	failApply           map[string]error
	beforeRollbackGuard func()
	beforeApplyGuard    func()
	rollbackCalls       []string
	applyCalls          []string
	applyResults        map[string]contracts.ReconcilePlan
}

func (f *rollbackFake) Rollback(ctx context.Context, planID string) (contracts.ReconcilePlan, error) {
	if f.beforeRollbackGuard != nil {
		f.beforeRollbackGuard()
	}
	if err := contracts.RunReconcileSideEffectGuard(ctx); err != nil {
		return contracts.ReconcilePlan{}, err
	}
	f.rollbackCalls = append(f.rollbackCalls, planID)
	return contracts.ReconcilePlan{}, f.failRollback[planID]
}

func (f *rollbackFake) Apply(ctx context.Context, planID string) (contracts.ReconcilePlan, error) {
	if f.beforeApplyGuard != nil {
		f.beforeApplyGuard()
	}
	if err := contracts.RunReconcileSideEffectGuard(ctx); err != nil {
		return contracts.ReconcilePlan{}, err
	}
	f.applyCalls = append(f.applyCalls, planID)
	return f.applyResults[planID], f.failApply[planID]
}

func TestRunnerPersistsProgressAndRetiresOnlyAfterEveryRollback(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now().UTC())
	pool, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "pool", Status: contracts.UpstreamPoolActive})
	for _, id := range []string{"a", "b"} {
		_, _ = st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-" + id, UserID: 1, InstanceID: "inst-" + id, PoolID: pool.ID, Status: contracts.RoutePlanPublished})
	}
	job, err := st.CreatePoolRetirementJob(ctx, pool.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("gateway unavailable")
	fake := &rollbackFake{failRollback: map[string]error{"plan-b": failure}}
	partial, err := New(st, fake).RunJob(ctx, job.ID)
	if !errors.Is(err, failure) || partial.Status != contracts.PoolRetirementPartial || partial.CompletedPlans != 1 || partial.FailedPlans != 1 {
		t.Fatalf("partial=%+v err=%v", partial, err)
	}
	maintained, _ := st.GetUpstreamPool(ctx, pool.ID)
	if maintained.Status != contracts.UpstreamPoolMaintenance {
		t.Fatalf("pool retired before drain: %+v", maintained)
	}
	delete(fake.failRollback, "plan-b")
	completed, err := New(st, fake).RunJob(ctx, job.ID)
	if err != nil || completed.Status != contracts.PoolRetirementCompleted || completed.CompletedPlans != 2 || completed.CleanupCompletedPlans != 2 {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	retired, _ := st.GetUpstreamPool(ctx, pool.ID)
	if retired.Status != contracts.UpstreamPoolRetired {
		t.Fatalf("pool not retired: %+v", retired)
	}
}

func TestRunnerCompletesNeverPublishedDraftWithoutGatewayRollback(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now().UTC())
	pool, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "pool", Status: contracts.UpstreamPoolActive})
	_, _ = st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "draft-plan", UserID: 1, InstanceID: "inst", PoolID: pool.ID, Status: contracts.RoutePlanDraft})
	job, err := st.CreatePoolRetirementJob(ctx, pool.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	fake := &rollbackFake{}
	completed, err := New(st, fake).RunJob(ctx, job.ID)
	if err != nil || completed.Status != contracts.PoolRetirementCompleted || completed.CompletedPlans != 1 {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	if len(fake.rollbackCalls) != 0 {
		t.Fatalf("draft without remote binding reached rollback: %v", fake.rollbackCalls)
	}
	if len(fake.applyCalls) != 0 {
		t.Fatalf("draft cleanup calls: %v", fake.applyCalls)
	}
}

func TestRunnerRetriesFinalCleanupAndDoesNotCompleteEarly(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now().UTC())
	pool, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "pool", Status: contracts.UpstreamPoolActive})
	plan, _ := st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-a", UserID: 1, InstanceID: "inst-a", PoolID: pool.ID, Status: contracts.RoutePlanPublished})
	_, _ = st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "channel-a", RemoteID: "remote-a",
		State: contracts.BindingActive, AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	job, _ := st.CreatePoolRetirementJob(ctx, pool.ID, 7)
	failure := errors.New("connector unavailable")
	fake := &rollbackFake{failApply: map[string]error{plan.ID: failure}, applyResults: map[string]contracts.ReconcilePlan{
		plan.ID: {PlanID: plan.ID, Actions: []contracts.ReconcileAction{{Type: contracts.ReconcileDeprovision, ChannelID: "channel-a", RemoteID: "remote-a"}}},
	}}

	partial, err := New(st, fake).RunJob(ctx, job.ID)
	if !errors.Is(err, failure) || partial.Status != contracts.PoolRetirementCleanup || partial.CleanupFailedPlans != 1 {
		t.Fatalf("cleanup failure job=%+v err=%v", partial, err)
	}
	retired, _ := st.GetUpstreamPool(ctx, pool.ID)
	if retired.Status != contracts.UpstreamPoolRetired || partial.CompletedAt != nil {
		t.Fatalf("failed cleanup must keep retired incomplete job: pool=%+v job=%+v", retired, partial)
	}
	delete(fake.failApply, plan.ID)
	completed, err := New(st, fake).RunJob(ctx, job.ID)
	if err != nil || completed.Status != contracts.PoolRetirementCompleted || completed.CleanupCompletedPlans != 1 {
		t.Fatalf("retried cleanup job=%+v err=%v", completed, err)
	}
}

func TestRunnerResumesCleanupAfterFinalizeCrash(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now().UTC())
	pool, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "pool", Status: contracts.UpstreamPoolActive})
	plan, _ := st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-a", UserID: 1, InstanceID: "inst-a", PoolID: pool.ID, Status: contracts.RoutePlanPublished})
	job, _ := st.CreatePoolRetirementJob(ctx, pool.ID, 7)
	item, _, _ := st.ClaimPoolRetirementItem(ctx, job.ID)
	_, _ = st.CompletePoolRetirementItem(ctx, job.ID, item.PlanID, item.Attempts, "")
	cleanup, err := st.FinalizePoolRetirementJob(ctx, job.ID)
	if err != nil || cleanup.Status != contracts.PoolRetirementCleanup {
		t.Fatalf("finalize cleanup=%+v err=%v", cleanup, err)
	}
	fake := &rollbackFake{applyResults: map[string]contracts.ReconcilePlan{plan.ID: {PlanID: plan.ID}}}
	completed, err := New(st, fake).RunJob(ctx, job.ID)
	if err != nil || completed.Status != contracts.PoolRetirementCompleted || len(fake.rollbackCalls) != 0 || len(fake.applyCalls) != 0 {
		t.Fatalf("resume job=%+v rollback=%v apply=%v err=%v", completed, fake.rollbackCalls, fake.applyCalls, err)
	}
}

func TestRunnerRejectsCleanupWithoutExpectedDeprovisionReceipt(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now().UTC())
	pool, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "pool", Status: contracts.UpstreamPoolActive})
	plan, _ := st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-a", UserID: 1, InstanceID: "inst-a", PoolID: pool.ID, Status: contracts.RoutePlanPublished})
	_, _ = st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "channel-a", RemoteID: "remote-a",
		State: contracts.BindingActive, AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	job, _ := st.CreatePoolRetirementJob(ctx, pool.ID, 7)
	fake := &rollbackFake{applyResults: map[string]contracts.ReconcilePlan{plan.ID: {PlanID: plan.ID}}}
	partial, err := New(st, fake).RunJob(ctx, job.ID)
	if err == nil || partial.Status != contracts.PoolRetirementCleanup || partial.CleanupFailedPlans != 1 {
		t.Fatalf("missing deprovision job=%+v err=%v", partial, err)
	}
}
