package autoswitch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/publish"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/strategy"
)

type blockingRoutePlanReadStore struct {
	store.Store
	planID  string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type republishOnRoutePlanReadStore struct {
	store.Store
	planID string
	reads  int
	mu     sync.Mutex
}

// legacyRoutePlanLifecycleStore models route-plan status writes made before
// UpdateRoutePlan became a semantic-generation CAS. The production API now
// advances the generation for every material desired-state change; these
// compatibility repair tests deliberately keep the expired decision on the
// same generation so the legacy recovery path remains executable.
type legacyRoutePlanLifecycleStore struct {
	store.Store
	planID string
	status contracts.RoutePlanStatus
}

func (s *legacyRoutePlanLifecycleStore) GetRoutePlan(ctx context.Context, id string) (contracts.RoutePlan, error) {
	plan, err := s.Store.GetRoutePlan(ctx, id)
	if err == nil && id == s.planID && s.status != "" {
		plan.Status = s.status
	}
	return plan, err
}

func (s *legacyRoutePlanLifecycleStore) ListRoutePlans(ctx context.Context, userID int64) ([]contracts.RoutePlan, error) {
	plans, err := s.Store.ListRoutePlans(ctx, userID)
	if err != nil {
		return nil, err
	}
	for index := range plans {
		if plans[index].ID == s.planID && s.status != "" {
			plans[index].Status = s.status
		}
	}
	return plans, nil
}

func (s *legacyRoutePlanLifecycleStore) UpdateRoutePlan(ctx context.Context, input contracts.RoutePlan) (contracts.RoutePlan, error) {
	if input.ID != s.planID {
		return s.Store.UpdateRoutePlan(ctx, input)
	}
	current, err := s.Store.GetRoutePlan(ctx, input.ID)
	if err != nil {
		return contracts.RoutePlan{}, err
	}
	if input.SchedulingGeneration != current.SchedulingGeneration || input.UserID != current.UserID ||
		input.InstanceID != current.InstanceID || input.PoolID != current.PoolID {
		return contracts.RoutePlan{}, store.ErrConflict
	}
	s.status = input.Status
	current.Status = input.Status
	return current, nil
}

func (s *republishOnRoutePlanReadStore) GetRoutePlan(ctx context.Context, id string) (contracts.RoutePlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.planID {
		s.reads++
		// terminateSuspendedApplyingDecision reads once, diffScheduling reads
		// once, then the per-item side-effect guard performs the third read.
		if s.reads == 3 {
			plan, err := s.Store.GetRoutePlan(ctx, s.planID)
			if err != nil {
				return contracts.RoutePlan{}, err
			}
			plan.Status = contracts.RoutePlanPublished
			if _, err := s.Store.UpdateRoutePlan(ctx, plan); err != nil {
				return contracts.RoutePlan{}, err
			}
		}
	}
	return s.Store.GetRoutePlan(ctx, id)
}

func (s *blockingRoutePlanReadStore) GetRoutePlan(ctx context.Context, id string) (contracts.RoutePlan, error) {
	blocked := false
	if id == s.planID {
		s.once.Do(func() {
			blocked = true
			close(s.started)
		})
	}
	if blocked {
		select {
		case <-s.release:
		case <-ctx.Done():
			return contracts.RoutePlan{}, ctx.Err()
		}
	}
	return s.Store.GetRoutePlan(ctx, id)
}

func TestExpiredApplyingClaimBeforeSideEffectIsReleasedForFreshEvaluation(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	decision := seedExpiredApplyingDecision(t, f, false)

	result, err := New(f.st, f.eng, WithClock(f.clk.now)).Evaluate(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if result == nil || result.ID == decision.ID || result.Status != contracts.AutoSwitchObserving {
		t.Fatalf("fresh evaluation=%+v, want a new observing decision", result)
	}
	repaired, err := f.st.GetAutoSwitchDecision(f.ctx, decision.ID)
	if err != nil || repaired.Status != contracts.AutoSwitchFailed || repaired.LeaseUntil != nil {
		t.Fatalf("expired claim was not terminated: %+v err=%v", repaired, err)
	}
	if f.gw.schedulable("acc-primary") || !f.gw.schedulable("acc-backup") {
		t.Fatal("fresh evaluation did not apply the source-to-backup switch")
	}
}

func TestExpiredApplyingClaimRepairsGatewayStateBeforeBindingWrite(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	decision := seedExpiredApplyingDecision(t, f, false)
	setGatewayScheduling(t, f, "acc-primary", false)
	setGatewayScheduling(t, f, "acc-backup", true)

	result, err := New(f.st, f.eng, WithClock(f.clk.now)).Evaluate(f.ctx, f.plan.ID)
	if err != nil || result != nil {
		old, _ := f.st.GetAutoSwitchDecision(f.ctx, decision.ID)
		bindings, _ := f.st.ListPublishedBindings(f.ctx, f.plan.ID)
		t.Fatalf("evaluate after repair: decision=%+v err=%v old=%+v bindings=%+v calls=%+v", result, err, old, bindings, f.gw.calls)
	}
	repaired, err := f.st.GetAutoSwitchDecision(f.ctx, decision.ID)
	if err != nil || repaired.Status != contracts.AutoSwitchObserving || repaired.LeaseUntil != nil || !repaired.AutoApplied {
		t.Fatalf("gateway-applied decision was not resumed: %+v err=%v", repaired, err)
	}
	bindings, _ := f.st.ListPublishedBindings(f.ctx, f.plan.ID)
	states := map[string]contracts.PublishedBindingState{}
	for _, binding := range bindings {
		states[binding.ChannelID] = binding.State
	}
	if states["ch-primary"] != contracts.BindingDisabled || states["ch-backup"] != contracts.BindingActive {
		t.Fatalf("binding facts were not repaired: %+v", states)
	}
	circuit, err := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if err != nil || circuit.State != contracts.QualityCircuitOpen {
		t.Fatalf("source circuit was not repaired: %+v err=%v", circuit, err)
	}
}

func TestExpiredApplyingPartialReplacementIsDrainedBeforeRetry(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	decision := seedExpiredApplyingDecision(t, f, false)
	setGatewayScheduling(t, f, "acc-backup", true)

	result, err := New(f.st, f.eng, WithClock(f.clk.now)).Evaluate(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if result == nil || result.ID == decision.ID || result.Status != contracts.AutoSwitchObserving {
		t.Fatalf("fresh retry=%+v, want a new observing decision", result)
	}
	old, _ := f.st.GetAutoSwitchDecision(f.ctx, decision.ID)
	if old.Status != contracts.AutoSwitchFailed {
		t.Fatalf("partial claim status=%q, want failed", old.Status)
	}
	if f.gw.schedulable("acc-primary") || !f.gw.schedulable("acc-backup") {
		t.Fatal("fresh retry did not finish the intended switch")
	}
	var disabledBackup bool
	for _, call := range f.gw.calls {
		if call.accountID == "acc-backup" && !call.enabled {
			disabledBackup = true
		}
	}
	if !disabledBackup {
		t.Fatal("repair did not drain the partially enabled replacement before retry")
	}
}

func TestExpiredObservationClaimReturnsToObservingWhenGatewayStillMatches(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	decision := seedExpiredApplyingDecision(t, f, true)
	seedCtx := contracts.WithGatewaySchedulingFence(f.ctx, contracts.GatewaySchedulingFence{
		Scope: "auto-switch/plan/" + f.plan.ID, Version: decision.SchedulingGeneration,
	})
	if _, err := f.eng.ApplyScheduling(seedCtx, f.plan.ID, map[string]bool{
		"ch-primary": false, "ch-backup": true,
	}); err != nil {
		t.Fatalf("seed applied switch: %v", err)
	}

	if err := New(f.st, f.eng, WithClock(f.clk.now)).repairExpiredApplyingDecisions(f.ctx, f.plan, nil); err != nil {
		t.Fatalf("repair observation: %v", err)
	}
	repaired, err := f.st.GetAutoSwitchDecision(f.ctx, decision.ID)
	if err != nil || repaired.Status != contracts.AutoSwitchObserving || repaired.LeaseUntil != nil {
		t.Fatalf("observation claim was not restored: %+v err=%v", repaired, err)
	}
	if f.gw.schedulable("acc-primary") || !f.gw.schedulable("acc-backup") {
		t.Fatal("observation repair changed a matching gateway state")
	}
}

func TestExpiredObservationClaimFailsClosedWhenGatewayDiverged(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	decision := seedExpiredApplyingDecision(t, f, true)

	if err := New(f.st, f.eng, WithClock(f.clk.now)).repairExpiredApplyingDecisions(f.ctx, f.plan, nil); err != nil {
		t.Fatalf("repair observation: %v", err)
	}
	repaired, err := f.st.GetAutoSwitchDecision(f.ctx, decision.ID)
	if err != nil || repaired.Status != contracts.AutoSwitchRolledBack || repaired.LeaseUntil != nil {
		t.Fatalf("diverged observation was not terminated: %+v err=%v", repaired, err)
	}
	if f.gw.schedulable("acc-primary") || f.gw.schedulable("acc-backup") {
		t.Fatal("diverged observation repair did not enforce local fail-closed state")
	}
	circuit, err := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-backup")
	if err != nil || circuit.State != contracts.QualityCircuitOpen {
		t.Fatalf("failed replacement circuit=%+v err=%v", circuit, err)
	}
}

func TestRunnerRepairsExpiredApplyingDecisionForSuspendedPlan(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	decision := seedExpiredApplyingDecision(t, f, false)
	suspendPlanWithoutSchedulingTakeover(t, &f)
	setGatewayScheduling(t, f, "acc-backup", true)

	NewRunner(f.st, New(f.st, f.eng, WithClock(f.clk.now)), time.Minute).sweep(f.ctx)

	repaired, err := f.st.GetAutoSwitchDecision(f.ctx, decision.ID)
	if err != nil || repaired.Status != contracts.AutoSwitchFailed || repaired.LeaseUntil != nil {
		t.Fatalf("suspended decision was not terminated: %+v err=%v", repaired, err)
	}
	if f.gw.schedulable("acc-primary") || f.gw.schedulable("acc-backup") {
		t.Fatal("suspended-plan repair did not drain both sides of the interrupted switch")
	}
	plan, err := f.st.GetRoutePlan(f.ctx, f.plan.ID)
	if err != nil || plan.Status != contracts.RoutePlanSuspended {
		t.Fatalf("repair changed suspended plan lifecycle: %+v err=%v", plan, err)
	}
	decisions, err := f.st.ListAutoSwitchDecisions(f.ctx, contracts.AutoSwitchDecisionFilter{PlanID: f.plan.ID})
	if err != nil || len(decisions) != 1 {
		t.Fatalf("suspended plan was evaluated for a new switch: decisions=%+v err=%v", decisions, err)
	}
}

func TestSuspendedRepairDoesNotDrainPlanRepublishedBeforeSideEffect(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	decision := seedExpiredApplyingDecision(t, f, false)
	suspendPlanWithoutSchedulingTakeover(t, &f)
	setGatewayScheduling(t, f, "acc-backup", true)
	gate := &blockingRoutePlanReadStore{
		Store: f.st, planID: f.plan.ID, started: make(chan struct{}), release: make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() {
		done <- New(gate, f.eng, WithClock(f.clk.now)).repairExpiredApplyingDecisions(f.ctx, f.plan, nil)
	}()
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("suspended repair did not re-read plan status before draining")
	}

	currentPlan, err := f.st.GetRoutePlan(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("load suspended plan: %v", err)
	}
	currentPlan.Status = contracts.RoutePlanPublished
	if _, err := f.st.UpdateRoutePlan(f.ctx, currentPlan); err != nil {
		t.Fatalf("republish plan: %v", err)
	}
	close(gate.release)
	if err := <-done; !errors.Is(err, store.ErrConflict) {
		t.Fatalf("repair after concurrent republish error=%v, want conflict", err)
	}
	if !f.gw.schedulable("acc-primary") || !f.gw.schedulable("acc-backup") || len(f.gw.calls) != 0 {
		t.Fatalf("stale suspended repair changed republished gateway state: calls=%+v", f.gw.calls)
	}
	repaired, err := f.st.GetAutoSwitchDecision(f.ctx, decision.ID)
	if err != nil || repaired.Status != contracts.AutoSwitchApplying || repaired.LeaseUntil == nil {
		t.Fatalf("republished decision was not left for published repair: %+v err=%v", repaired, err)
	}
}

func TestSuspendedRepairItemGuardStopsDrainAfterConcurrentRepublish(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	decision := seedExpiredApplyingDecision(t, f, false)
	suspendPlanWithoutSchedulingTakeover(t, &f)
	setGatewayScheduling(t, f, "acc-backup", true)
	gate := &republishOnRoutePlanReadStore{Store: f.st, planID: f.plan.ID}
	eng := publish.New(gate, f.gw)

	err := New(gate, eng, WithClock(f.clk.now)).repairExpiredApplyingDecisions(f.ctx, f.plan, nil)
	if err == nil || !strings.Contains(err.Error(), "side effect fence") {
		t.Fatalf("repair after item-level republish error=%v, want side effect fence", err)
	}
	if !f.gw.schedulable("acc-primary") || !f.gw.schedulable("acc-backup") || len(f.gw.calls) != 0 {
		t.Fatalf("stale suspended item changed republished gateway state: calls=%+v", f.gw.calls)
	}
	current, getErr := f.st.GetAutoSwitchDecision(f.ctx, decision.ID)
	if getErr != nil || current.Status != contracts.AutoSwitchApplying || current.LeaseUntil == nil {
		t.Fatalf("failed repair did not retain natural lease: decision=%+v err=%v", current, getErr)
	}
}

func TestRecoveredCircuitIgnoresPreRecoverySnapshotsUntilFreshWindow(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	lastProbe := f.clk.now()
	staleAt := lastProbe.Add(-time.Minute)
	if _, err := f.st.UpsertChannelHealthSnapshot(f.ctx, contracts.ChannelHealthSnapshot{
		ChannelID: "ch-primary", InstanceID: f.plan.InstanceID, Window: contracts.Window5m,
		BucketStart: staleAt, CreatedAt: staleAt,
		SampleCount: 20, SuccessRate: .4, ErrorRate: .6,
		QualitySampleCount: 20, QualitySuccessRate: .4, QualityErrorRate: .6,
		UpstreamErrorRate: .6, TTFTP95: 600, DurationP95: 3000,
		HealthState: contracts.HealthUnhealthy,
	}); err != nil {
		t.Fatalf("seed stale snapshot: %v", err)
	}
	if _, err := f.st.UpsertQualityCircuitRuntime(f.ctx, contracts.QualityCircuitRuntime{
		PlanID: f.plan.ID, ChannelID: "ch-primary", State: contracts.QualityCircuitClosed,
		LastProbeAt: &lastProbe, LastTransitionAt: &lastProbe, LastScore: 95,
		LastReason: contracts.QualityCircuitReason{Code: strategy.CircuitReasonRestored, Text: "recovered"},
	}, 0); err != nil {
		t.Fatalf("seed recovered circuit: %v", err)
	}

	decision, err := New(f.st, f.eng, WithClock(f.clk.now)).Evaluate(f.ctx, f.plan.ID)
	if err != nil || decision != nil {
		t.Fatalf("stale pre-recovery snapshot re-ejected source: decision=%+v err=%v", decision, err)
	}
	if !f.gw.schedulable("acc-primary") {
		t.Fatal("recovered source was disabled by stale evidence")
	}

	f.clk.add(6 * time.Minute)
	seedSnapshot(t, f.st, "ch-primary", 0.40, contracts.HealthUnhealthy)
	decision, err = New(f.st, f.eng, WithClock(f.clk.now)).Evaluate(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("fresh evaluation: %v", err)
	}
	// Stable cohort membership determines whether this plan joins the first
	// progressive batch; either outcome proves fresh evidence was accepted.
	if decision == nil {
		t.Fatal("fresh post-recovery quality window was ignored")
	}
}

func TestConsecutiveBadWindowsCountsIndependentFiveMinuteBuckets(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary()})
	base := f.clk.now().Add(-10 * time.Minute)
	for bucket := 0; bucket < 3; bucket++ {
		at := base.Add(time.Duration(bucket) * 5 * time.Minute)
		for model := 0; model < 31; model++ {
			_, err := f.st.UpsertChannelHealthSnapshot(f.ctx, contracts.ChannelHealthSnapshot{
				ChannelID: "ch-primary", InstanceID: f.plan.InstanceID,
				Model: "model-" + twoDigits(model), Window: contracts.Window5m,
				BucketStart: at, CreatedAt: at,
				SampleCount: 20, SuccessRate: .5, ErrorRate: .5,
				QualitySampleCount: 20, QualitySuccessRate: .5, QualityErrorRate: .5,
				UpstreamErrorRate: .5, TTFTP95: 600, DurationP95: 3000,
				HealthState: contracts.HealthUnhealthy,
			})
			if err != nil {
				t.Fatalf("seed snapshot: %v", err)
			}
		}
	}
	o := New(f.st, f.eng, WithClock(f.clk.now))
	if got := o.consecutiveBadWindows(f.ctx, f.plan, o.strategyFor(f.ctx, f.plan), "ch-primary"); got != 3 {
		t.Fatalf("bad window streak=%d, want 3 complete buckets", got)
	}
}

func seedExpiredApplyingDecision(t *testing.T, f fixture, observationClaim bool) contracts.AutoSwitchDecision {
	t.Helper()
	expired := f.clk.now().Add(-time.Minute)
	plan, err := f.st.ClaimRoutePlanScheduling(f.ctx, f.plan.ID, contracts.RoutePlanPublished)
	if err != nil {
		t.Fatalf("claim plan generation: %v", err)
	}
	decision := contracts.AutoSwitchDecision{
		PlanID: f.plan.ID, UserID: f.plan.UserID, InstanceID: f.plan.InstanceID, PoolID: f.plan.PoolID,
		FromChannelID: "ch-primary", ToChannelID: "ch-backup", Fingerprint: "expired-" + f.plan.ID,
		Status: contracts.AutoSwitchApplying, LeaseUntil: &expired, CreatedAt: f.clk.now().Add(-2 * time.Minute),
		SchedulingGeneration: plan.SchedulingGeneration,
	}
	if observationClaim {
		appliedAt := f.clk.now().Add(-10 * time.Minute)
		observeUntil := f.clk.now().Add(-time.Minute)
		decision.AutoApplied = true
		decision.AppliedAt = &appliedAt
		decision.ObserveUntil = &observeUntil
	}
	saved, err := f.st.CreateAutoSwitchDecision(f.ctx, decision)
	if err != nil {
		t.Fatalf("seed expired decision: %v", err)
	}
	return saved
}

// suspendPlanWithoutSchedulingTakeover models a legacy lifecycle write that
// did not advance the gateway scheduling owner. Production rollback uses the
// atomic takeover API; this fixture keeps the expired decision current so the
// compatibility repair path remains covered.
func suspendPlanWithoutSchedulingTakeover(t *testing.T, f *fixture) {
	t.Helper()
	legacy, ok := f.st.(*legacyRoutePlanLifecycleStore)
	if !ok {
		legacy = &legacyRoutePlanLifecycleStore{Store: f.st, planID: f.plan.ID}
		f.st = legacy
		f.eng = publish.New(f.st, f.gw)
	}
	plan, err := f.st.GetRoutePlan(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("load plan for suspension: %v", err)
	}
	plan.Status = contracts.RoutePlanSuspended
	if _, err := f.st.UpdateRoutePlan(f.ctx, plan); err != nil {
		t.Fatalf("suspend plan: %v", err)
	}
	f.plan, err = f.st.GetRoutePlan(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("reload legacy-suspended plan: %v", err)
	}
}

func setGatewayScheduling(t *testing.T, f fixture, accountID string, enabled bool) {
	t.Helper()
	for i := range f.gw.accounts {
		if f.gw.accounts[i].ID == accountID {
			f.gw.accounts[i].Schedulable = enabled
			return
		}
	}
	t.Fatalf("gateway account %s not found", accountID)
}

func twoDigits(value int) string {
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
