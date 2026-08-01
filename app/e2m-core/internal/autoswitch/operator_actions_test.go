package autoswitch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

func TestOperatorDecisionApproveExecuteRequiresExactStateAndIsFenced(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	dispatch := &captureDispatcher{}
	_, _ = f.st.CreateNotificationRoute(f.ctx, contracts.NotificationRoute{
		UserID: f.plan.UserID, Name: "operator-actions", Channel: contracts.NotificationChannelFeishu,
		Enabled: true, MinEventLevel: contracts.EventLevelInfo,
	})
	o := New(f.st, f.eng, WithClock(f.clk.now), WithObservationWindow(15*time.Minute),
		WithStrategy(contracts.RouteStrategy{Type: contracts.StrategyStabilityFirst, AutoApply: false}),
		WithNotifier(dispatch))
	proposed, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil || proposed == nil || proposed.Status != contracts.AutoSwitchProposed {
		t.Fatalf("proposed decision = %+v err=%v", proposed, err)
	}
	approved, err := o.Approve(f.ctx, proposed.ID, "checked by on-call")
	if err != nil || approved.Status != contracts.AutoSwitchApproved {
		t.Fatalf("approve = %+v err=%v", approved, err)
	}
	if _, err := o.Approve(f.ctx, proposed.ID, "duplicate click"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate approval must conflict, err=%v", err)
	}
	result, err := o.Execute(f.ctx, approved.ID)
	if err != nil || result == nil || result.Status != contracts.AutoSwitchObserving {
		t.Fatalf("execute = %+v err=%v", result, err)
	}
	if _, err := o.Execute(f.ctx, approved.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("only approved decisions may execute, err=%v", err)
	}
	latest, err := f.st.GetAutoSwitchDecision(f.ctx, approved.ID)
	if err != nil || latest.Status != contracts.AutoSwitchObserving || !latest.AutoApplied {
		t.Fatalf("latest decision = %+v err=%v", latest, err)
	}
	if f.gw.schedulable("acc-primary") || !f.gw.schedulable("acc-backup") {
		t.Fatal("approved execution did not switch scheduling")
	}
	if len(f.gw.calls) != 2 {
		t.Fatalf("gateway side effects = %+v, want one enable and one disable", f.gw.calls)
	}
	if len(dispatch.events) < 2 {
		t.Fatalf("approval/execution notifications missing: %+v", dispatch.events)
	}
}

func TestOperatorRejectIsTerminalAndRequiresProposedState(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	o := New(f.st, f.eng, WithClock(f.clk.now), WithStrategy(contracts.RouteStrategy{
		Type: contracts.StrategyStabilityFirst, AutoApply: false,
	}))
	proposed, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil || proposed == nil {
		t.Fatal(err)
	}
	rejected, err := o.Reject(f.ctx, proposed.ID, "risk not accepted")
	if err != nil || rejected.Status != contracts.AutoSwitchRejected || rejected.ResolvedAt == nil {
		t.Fatalf("reject = %+v err=%v", rejected, err)
	}
	if _, err := o.Reject(f.ctx, proposed.ID, "duplicate"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate rejection must conflict, err=%v", err)
	}
	if !f.gw.schedulable("acc-primary") || f.gw.schedulable("acc-backup") {
		t.Fatal("reject must not change gateway scheduling")
	}
	if rejected.Status.IsActive() {
		t.Fatal("rejected decision must not block a fresh failure evaluation")
	}
}

func TestOperatorRejectCannotRevokeApprovedDecision(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	o := New(f.st, f.eng, WithClock(f.clk.now), WithStrategy(contracts.RouteStrategy{
		Type: contracts.StrategyStabilityFirst, AutoApply: false,
	}))
	proposed, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil || proposed == nil {
		t.Fatal(err)
	}
	if _, err := o.Approve(f.ctx, proposed.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Reject(f.ctx, proposed.ID, "late rejection"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("approved decision was rejectable, err=%v", err)
	}
}

func TestManualRecoveryClosesCircuitWithoutProbeEvidence(t *testing.T) {
	f := seedRecoveryFixture(t)
	dispatch := &captureDispatcher{}
	_, _ = f.st.CreateNotificationRoute(f.ctx, contracts.NotificationRoute{
		UserID: f.plan.UserID, Name: "manual-recovery", Channel: contracts.NotificationChannelFeishu,
		Enabled: true, MinEventLevel: contracts.EventLevelInfo,
	})
	var eventMu sync.Mutex
	var events []RecoveryEvent
	o := New(f.st, f.eng, WithClock(f.clk.now), WithNotifier(dispatch),
		WithRecoveryEventSink(func(_ context.Context, event RecoveryEvent) {
			eventMu.Lock()
			events = append(events, event)
			eventMu.Unlock()
		}))
	openCircuitForRecovery(t, f, o)
	runtime, err := o.ManualRecover(f.ctx, f.plan.ID, "ch-primary", "upstream owner confirmed")
	if err != nil {
		t.Fatalf("manual recover: %v", err)
	}
	if runtime.State != contracts.QualityCircuitClosed || runtime.RestorePending || runtime.RecoveryReady ||
		runtime.RecoveryStage != 0 || runtime.ConsecutiveProbeSuccesses != 0 ||
		runtime.LastReason.Code != "manual_recovery_completed" {
		t.Fatalf("manual recovery manufactured automatic evidence: %+v", runtime)
	}
	if !f.gw.schedulable("acc-primary") || !f.gw.schedulable("acc-backup") {
		t.Fatal("manual recovery should restore only the isolated binding and retain current service")
	}
	again, err := o.ManualRecover(f.ctx, f.plan.ID, "ch-primary", "duplicate click")
	if err != nil || again.Version != runtime.Version {
		t.Fatalf("idempotent manual recovery = %+v err=%v", again, err)
	}
	if len(dispatch.events) != 1 || len(events) != 1 || events[0].Status != "manual_recovery_completed" {
		t.Fatalf("recovery events notify=%d sink=%+v", len(dispatch.events), events)
	}
	audits, err := f.st.ListAudits(f.ctx, f.plan.UserID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		if audit.Action == "quality_circuit.recovery_transition" && audit.Details["reason_code"] == "manual_recovery_completed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("manual recovery transition audit missing: %+v", audits)
	}
}

func TestManualRecoveryIgnoresPreRecoveryQualityUntilFreshWindow(t *testing.T) {
	f := seedRecoveryFixture(t)
	o := New(f.st, f.eng, WithClock(f.clk.now))
	staleAt := f.clk.now().Add(-time.Minute)
	seedSnapshotForInstance(t, f.st, "ch-primary", f.plan.InstanceID, staleAt, .4, contracts.HealthUnhealthy)
	seedSnapshotForInstance(t, f.st, "ch-backup", f.plan.InstanceID, staleAt, .99, contracts.HealthHealthy)
	openCircuitForRecovery(t, f, o)

	runtime, err := o.ManualRecover(f.ctx, f.plan.ID, "ch-primary", "upstream owner confirmed")
	if err != nil {
		t.Fatalf("manual recover: %v", err)
	}
	if runtime.LastReason.Code != "manual_recovery_completed" || runtime.LastTransitionAt == nil {
		t.Fatalf("manual recovery freshness boundary missing: %+v", runtime)
	}
	if watermark := recoveryEvidenceWatermark(runtime); !watermark.Equal(*runtime.LastTransitionAt) {
		t.Fatalf("manual recovery watermark=%v, want %v", watermark, *runtime.LastTransitionAt)
	}

	primary, err := f.st.GetUpstreamChannel(f.ctx, "ch-primary")
	if err != nil {
		t.Fatal(err)
	}
	selected, known := o.sourceQualityCohort(f.ctx, primary.SourceIdentity(), 25)
	if !known || len(selected) != 0 {
		t.Fatalf("pre-recovery snapshot entered source cohort: selected=%v known=%v", selected, known)
	}
	decision, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil || decision != nil {
		t.Fatalf("pre-recovery snapshot re-ejected manual recovery: decision=%+v err=%v", decision, err)
	}
	if !f.gw.schedulable("acc-primary") {
		t.Fatal("manual recovery was disabled before fresh passive evidence")
	}

	f.clk.add(decisionWindow.Duration() + time.Minute)
	seedSnapshotForInstance(t, f.st, "ch-primary", f.plan.InstanceID, f.clk.now(), .4, contracts.HealthUnhealthy)
	selected, known = o.sourceQualityCohort(f.ctx, primary.SourceIdentity(), 25)
	if !known || !selected[f.plan.ID] {
		t.Fatalf("fresh post-recovery failure did not enter source cohort: selected=%v known=%v", selected, known)
	}
	decision, err = o.Evaluate(f.ctx, f.plan.ID)
	if err != nil || decision == nil {
		t.Fatalf("fresh post-recovery failure was ignored: decision=%+v err=%v", decision, err)
	}
}

func TestManualRecoveryApplyFailurePersistsRetryAndReturnsError(t *testing.T) {
	f := seedRecoveryFixture(t)
	o := New(f.st, f.eng, WithClock(f.clk.now))
	openCircuitForRecovery(t, f, o)
	f.gw.failOn["acc-primary"] = true

	if _, err := o.ManualRecover(f.ctx, f.plan.ID, "ch-primary", "upstream owner confirmed"); err == nil {
		t.Fatal("gateway enable failure was reported as a successful manual recovery")
	}
	runtime, err := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.RestorePending || runtime.State == contracts.QualityCircuitClosed ||
		runtime.LastReason.Code != "manual_recovery_apply_failed" || runtime.ProbeAfter == nil {
		t.Fatalf("manual recovery retry state not retained: %+v", runtime)
	}
	if f.gw.schedulable("acc-primary") {
		t.Fatal("failed manual recovery restored gateway traffic")
	}
}

func TestSameSchedulingActionsIgnoresOrderButRejectsScopeChange(t *testing.T) {
	expected := []contracts.ReconcileAction{
		{Type: contracts.ReconcileEnable, ChannelID: "backup"},
		{Type: contracts.ReconcileDisable, ChannelID: "primary"},
	}
	actual := []contracts.ReconcileAction{
		{Type: contracts.ReconcileDisable, ChannelID: "primary", Detail: "fresh detail"},
		{Type: contracts.ReconcileEnable, ChannelID: "backup"},
	}
	if !sameSchedulingActions(expected, actual) {
		t.Fatal("action ordering or detail text should not invalidate the approved intent")
	}
	actual[1].ChannelID = "other"
	if sameSchedulingActions(expected, actual) {
		t.Fatal("changed channel scope must invalidate an approval")
	}
}
