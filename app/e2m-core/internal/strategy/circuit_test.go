package strategy

import (
	"testing"
	"time"
)

func TestQualityCircuitEjectCooldownHalfOpenRestore(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	policy := QualityCircuitPolicy{
		BaseCooldown:      5 * time.Minute,
		MaxCooldown:       time.Hour,
		HalfOpenSuccesses: 3,
		RecoveryScore:     85,
	}
	rt := QualityCircuit{ScopeKey: "instance-1/source-a"}

	opened := AdvanceQualityCircuit(rt, QualityCircuitEvent{
		Kind: CircuitQualityWindow, Now: now,
		Evaluation: penaltyEval(45, 1, true, false),
	}, policy)
	if opened.Circuit.State != CircuitOpen || opened.Action != CircuitEject || !opened.Changed {
		t.Fatalf("bad window must open and eject: %+v", opened)
	}
	if !opened.Circuit.ProbeAfter.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("first recovery probe should be due after base cooldown, got %v", opened.Circuit.ProbeAfter)
	}

	tooEarly := AdvanceQualityCircuit(opened.Circuit, QualityCircuitEvent{
		Kind: CircuitRecoveryProbe, Now: now.Add(4 * time.Minute),
		Evaluation: penaltyEval(100, 1, false, false),
	}, policy)
	if tooEarly.Circuit.State != CircuitOpen || tooEarly.Action != CircuitKeepEjected || tooEarly.Changed {
		t.Fatalf("early probe must not change the circuit: %+v", tooEarly)
	}

	half := AdvanceQualityCircuit(opened.Circuit, QualityCircuitEvent{
		Kind: CircuitRecoveryProbe, Now: now.Add(5 * time.Minute),
		Evaluation: penaltyEval(90, 1, false, false),
	}, policy)
	if half.Circuit.State != CircuitHalfOpen || half.Action != CircuitKeepEjected || half.Circuit.ConsecutiveProbeSuccesses != 1 {
		t.Fatalf("first good due probe must enter probe-only half-open: %+v", half)
	}

	second := AdvanceQualityCircuit(half.Circuit, QualityCircuitEvent{
		Kind: CircuitRecoveryProbe, Now: now.Add(6 * time.Minute),
		Evaluation: penaltyEval(92, 1, false, false),
	}, policy)
	if second.Circuit.State != CircuitHalfOpen || second.Action != CircuitKeepEjected ||
		second.Circuit.ConsecutiveProbeSuccesses != 2 || !second.Changed {
		t.Fatalf("recovery requires all configured probes: %+v", second)
	}

	restored := AdvanceQualityCircuit(second.Circuit, QualityCircuitEvent{
		Kind: CircuitRecoveryProbe, Now: now.Add(7 * time.Minute),
		Evaluation: penaltyEval(95, 1, false, false),
	}, policy)
	if restored.Circuit.State != CircuitClosed || restored.Action != CircuitRestore || !restored.Changed {
		t.Fatalf("third strong probe must restore scheduling: %+v", restored)
	}
}

func TestQualityCircuitProbeFailureReopensWithBackoff(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	policy := QualityCircuitPolicy{BaseCooldown: time.Minute, MaxCooldown: 10 * time.Minute, HalfOpenSuccesses: 2}
	rt := QualityCircuit{
		ScopeKey: "instance-1/source-a", State: CircuitHalfOpen, OpenCount: 1,
		ConsecutiveProbeSuccesses: 1,
	}

	failed := AdvanceQualityCircuit(rt, QualityCircuitEvent{
		Kind: CircuitRecoveryProbe, Now: now,
		Evaluation: penaltyEval(40, 1, true, false),
	}, policy)
	if failed.Circuit.State != CircuitOpen || failed.Action != CircuitKeepEjected || failed.Circuit.OpenCount != 2 {
		t.Fatalf("half-open relapse must reopen without a duplicate eject: %+v", failed)
	}
	if !failed.Circuit.ProbeAfter.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("second open should exponentially back off, got %v", failed.Circuit.ProbeAfter)
	}
}

func TestQualityCircuitCooldownJitterNeverShortensBaseAndCapsAtMaximum(t *testing.T) {
	policy := QualityCircuitPolicy{
		BaseCooldown:           5 * time.Minute,
		MaxCooldown:            time.Hour,
		CooldownJitterFraction: .20,
	}
	for _, scope := range []string{"source-a", "source-b", "source-c", "source-d"} {
		first := cooldownFor(scope, 1, policy.withDefaults())
		if first < 5*time.Minute || first > 6*time.Minute {
			t.Fatalf("scope %s initial cooldown=%v, want [5m,6m]", scope, first)
		}
		second := cooldownFor(scope, 2, policy.withDefaults())
		if second < 10*time.Minute || second > 12*time.Minute {
			t.Fatalf("scope %s second cooldown=%v, want [10m,12m]", scope, second)
		}
		if capped := cooldownFor(scope, 20, policy.withDefaults()); capped != time.Hour {
			t.Fatalf("scope %s capped cooldown=%v, want 1h", scope, capped)
		}
	}
}

func TestQualityCircuitNeedsRealProbeEvidence(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	rt := QualityCircuit{ScopeKey: "instance-1/source-a", State: CircuitOpen, ProbeAfter: now}

	result := AdvanceQualityCircuit(rt, QualityCircuitEvent{
		Kind: CircuitRecoveryProbe, Now: now,
		Evaluation: penaltyEval(100, 0, false, false),
	}, QualityCircuitPolicy{BaseCooldown: time.Minute})
	if result.Circuit.State != CircuitOpen || result.Action != CircuitKeepEjected {
		t.Fatalf("an empty probe cannot restore a source: %+v", result)
	}
}

func TestQualityCircuitIgnoresPassiveRecoveryWhileOpen(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	rt := QualityCircuit{ScopeKey: "instance-1/source-a", State: CircuitOpen, ProbeAfter: now}

	result := AdvanceQualityCircuit(rt, QualityCircuitEvent{
		Kind: CircuitQualityWindow, Now: now.Add(time.Minute),
		Evaluation: penaltyEval(100, 1, false, false),
	}, QualityCircuitPolicy{})
	if result.Circuit.State != CircuitOpen || result.Action != CircuitKeepEjected || result.Changed {
		t.Fatalf("passive/silent window must not restore an open source: %+v", result)
	}
}

func TestRecoveryProbeDue(t *testing.T) {
	now := time.Now().UTC()
	if RecoveryProbeDue(QualityCircuit{State: CircuitOpen, ProbeAfter: now.Add(time.Second)}, now) {
		t.Fatal("future probe must not be due")
	}
	if !RecoveryProbeDue(QualityCircuit{State: CircuitOpen, ProbeAfter: now}, now) {
		t.Fatal("open circuit at its probe time must be due")
	}
	if RecoveryProbeDue(QualityCircuit{State: CircuitHalfOpen, ProbeAfter: now}, now) {
		t.Fatal("half-open follow-up cadence is controlled by its probe runner")
	}
}

func penaltyEval(score, evidence float64, eject, hard bool) PenaltyEvaluation {
	return PenaltyEvaluation{
		Score: score, Evidence: evidence, Eject: eject, HardFailure: hard,
		Reason: Reason{Code: GatePenaltyThreshold, Text: "quality below threshold"},
	}
}
