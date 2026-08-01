package autoswitch

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/strategy"
)

func TestEvaluatePersistsOpenCircuitAfterSuccessfulEjection(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)

	decision, err := New(f.st, f.eng, WithClock(f.clk.now)).Evaluate(f.ctx, f.plan.ID)
	if err != nil || decision == nil || decision.Status != contracts.AutoSwitchObserving {
		t.Fatalf("evaluate: decision=%+v err=%v", decision, err)
	}
	runtime, err := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if err != nil {
		t.Fatalf("get primary circuit: %v", err)
	}
	if runtime.State != contracts.QualityCircuitOpen || runtime.OpenedAt == nil || runtime.ProbeAfter == nil || runtime.OpenCount != 1 {
		t.Fatalf("primary circuit = %+v, want first open transition", runtime)
	}
	if runtime.LastScore > 60 || runtime.LastReason.Code == "" {
		t.Fatalf("primary circuit lost quality evidence: %+v", runtime)
	}
}

func TestPersistedOpenAndHalfOpenCircuitsAreStrongCandidateGates(t *testing.T) {
	for _, state := range []contracts.QualityCircuitState{contracts.QualityCircuitOpen, contracts.QualityCircuitHalfOpen} {
		t.Run(string(state), func(t *testing.T) {
			local := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
			seedSnapshot(t, local.st, "ch-primary", 0.40, contracts.HealthUnhealthy)
			seedSnapshot(t, local.st, "ch-backup", 0.99, contracts.HealthHealthy)
			if _, err := local.st.UpsertQualityCircuitRuntime(local.ctx, contracts.QualityCircuitRuntime{
				PlanID: local.plan.ID, ChannelID: "ch-backup", State: state, OpenCount: 1, LastScore: 99,
			}, 0); err != nil {
				t.Fatalf("seed circuit: %v", err)
			}

			decision, err := New(local.st, local.eng, WithClock(local.clk.now)).Evaluate(local.ctx, local.plan.ID)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if decision == nil || decision.Status != contracts.AutoSwitchCompleted || decision.ToChannelID != "" {
				t.Fatalf("blocked backup selected: %+v", decision)
			}
			if local.gw.schedulable("acc-backup") {
				t.Fatal("open/half-open backup was admitted to normal traffic")
			}
		})
	}
}

func TestDecisionFallbackRepairsMissingCircuitBeforeRanking(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.99, contracts.HealthHealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)

	// Simulate a process dying after gateway/binding ejection and decision
	// persistence but before the circuit write.
	if _, err := f.eng.ApplyScheduling(f.ctx, f.plan.ID, map[string]bool{"ch-primary": false, "ch-backup": true}); err != nil {
		t.Fatalf("seed applied switch: %v", err)
	}
	appliedAt := f.clk.now()
	decision, err := f.st.CreateAutoSwitchDecision(f.ctx, contracts.AutoSwitchDecision{
		PlanID: f.plan.ID, InstanceID: f.plan.InstanceID, PoolID: f.plan.PoolID,
		FromChannelID: "ch-primary", ToChannelID: "ch-backup", Status: contracts.AutoSwitchCompleted,
		AutoApplied: true, AppliedAt: &appliedAt, CreatedAt: appliedAt,
	})
	if err != nil {
		t.Fatalf("seed decision: %v", err)
	}

	result, err := New(f.st, f.eng, WithClock(f.clk.now)).Evaluate(f.ctx, f.plan.ID)
	if err != nil || result != nil {
		t.Fatalf("evaluate after repair: decision=%+v err=%v", result, err)
	}
	runtime, err := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if err != nil || runtime.State != contracts.QualityCircuitOpen || runtime.LastReason.Code != "decision_fallback" {
		t.Fatalf("fallback did not repair circuit for decision %s: runtime=%+v err=%v", decision.ID, runtime, err)
	}
	if f.gw.schedulable("acc-primary") {
		t.Fatal("fallback repair re-enabled the ejected source")
	}
}

func TestApplyingDecisionRepairsPostApplyCircuitCrashWindow(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.99, contracts.HealthHealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	if _, err := f.eng.ApplyScheduling(f.ctx, f.plan.ID, map[string]bool{"ch-primary": false, "ch-backup": true}); err != nil {
		t.Fatalf("seed applied switch: %v", err)
	}
	decision, err := f.st.CreateAutoSwitchDecision(f.ctx, contracts.AutoSwitchDecision{
		PlanID: f.plan.ID, InstanceID: f.plan.InstanceID, PoolID: f.plan.PoolID,
		FromChannelID: "ch-primary", ToChannelID: "ch-backup", Status: contracts.AutoSwitchApplying,
		CreatedAt: f.clk.now(),
	})
	if err != nil {
		t.Fatalf("seed applying decision: %v", err)
	}

	result, err := New(f.st, f.eng, WithClock(f.clk.now)).Evaluate(f.ctx, f.plan.ID)
	if err != nil || result != nil {
		t.Fatalf("evaluate after applying repair: decision=%+v err=%v", result, err)
	}
	runtime, err := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if err != nil || runtime.State != contracts.QualityCircuitOpen {
		t.Fatalf("applying fallback did not repair decision %s: runtime=%+v err=%v", decision.ID, runtime, err)
	}
	if _, err := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-backup"); err == nil {
		t.Fatal("initial applying claim falsely quarantined its active replacement")
	}
}

func TestReplacementObservationFailureOpensIndependentCircuit(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	o := New(f.st, f.eng, WithClock(f.clk.now), WithObservationWindow(time.Minute))

	decision, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil || decision == nil || decision.Status != contracts.AutoSwitchObserving {
		t.Fatalf("initial switch: decision=%+v err=%v", decision, err)
	}
	primaryBefore, err := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if err != nil {
		t.Fatalf("get primary circuit: %v", err)
	}
	seedSnapshot(t, f.st, "ch-backup", 0.30, contracts.HealthUnhealthy)
	f.clk.add(2 * time.Minute)
	rolled, err := o.Observe(f.ctx, decision.ID)
	if err != nil || rolled.Status != contracts.AutoSwitchRolledBack {
		t.Fatalf("observe: decision=%+v err=%v", rolled, err)
	}

	primaryAfter, err := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if err != nil {
		t.Fatalf("get primary circuit after rollback: %v", err)
	}
	backup, err := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-backup")
	if err != nil {
		t.Fatalf("get replacement circuit: %v", err)
	}
	if primaryAfter.State != contracts.QualityCircuitOpen || primaryAfter.Version != primaryBefore.Version {
		t.Fatalf("original circuit changed while replacement failed: before=%+v after=%+v", primaryBefore, primaryAfter)
	}
	if backup.State != contracts.QualityCircuitOpen || backup.LastReason.Code == "" {
		t.Fatalf("replacement did not get independent open circuit: %+v", backup)
	}
	if f.gw.schedulable("acc-primary") || f.gw.schedulable("acc-backup") {
		t.Fatal("failed replacement or isolated original remained schedulable")
	}
}

func TestOpenQualityCircuitIsIdempotent(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary()})
	o := New(f.st, f.eng, WithClock(f.clk.now))
	eval := strategy.PenaltyEvaluation{
		ChannelID: "ch-primary", Score: 40, Eject: true,
		Reason: strategy.Reason{Code: strategy.GatePenaltyThreshold, Text: "quality below threshold"},
	}
	first, err := o.openQualityCircuit(context.Background(), f.plan.ID, "ch-primary", eval, f.clk.now())
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	second, err := o.openQualityCircuit(context.Background(), f.plan.ID, "ch-primary", eval, f.clk.now())
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if second.Version != first.Version || second.OpenCount != first.OpenCount || !second.ProbeAfter.Equal(*first.ProbeAfter) {
		t.Fatalf("repeat open advanced circuit: first=%+v second=%+v", first, second)
	}
}

func TestLaterExplicitEjectionReopensExistingCircuit(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary()})
	o := New(f.st, f.eng, WithClock(f.clk.now))
	eval := strategy.PenaltyEvaluation{
		ChannelID: "ch-primary", Score: 40, Eject: true,
		Reason: strategy.Reason{Code: strategy.GatePenaltyThreshold, Text: "quality below threshold"},
	}
	first, err := o.openQualityCircuit(f.ctx, f.plan.ID, "ch-primary", eval, f.clk.now())
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	second, err := o.openQualityCircuit(f.ctx, f.plan.ID, "ch-primary", eval, f.clk.now().Add(time.Minute))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if second.Version != first.Version+1 || second.OpenCount != first.OpenCount+1 || !second.ProbeAfter.After(*first.ProbeAfter) {
		t.Fatalf("later ejection did not reopen/back off: first=%+v second=%+v", first, second)
	}
}
