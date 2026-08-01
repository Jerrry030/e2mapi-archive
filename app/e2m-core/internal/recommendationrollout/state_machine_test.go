package recommendationrollout

import (
	"reflect"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestAdvanceCompletesRealTenTwentyFiveFiftyHundredStages(t *testing.T) {
	state, now := validRollout()
	for _, want := range []contracts.RecommendationRolloutStage{
		contracts.RecommendationRolloutStage10,
		contracts.RecommendationRolloutStage25,
		contracts.RecommendationRolloutStage50,
		contracts.RecommendationRolloutStage100,
	} {
		evaluation := Advance(state, eventWithRevalidation(state, now, contracts.RecommendationRolloutEventEvaluate))
		if evaluation.Action.Kind != contracts.RecommendationRolloutActionApplyStage || evaluation.Action.TargetStage != want || evaluation.State.Status != contracts.RecommendationRolloutApplying {
			t.Fatalf("stage %d evaluation wrong: %+v", want, evaluation)
		}
		state = evaluation.State
		applied := baseEvent(state, now.Add(time.Second), contracts.RecommendationRolloutEventStageApplied)
		applied.AppliedStage = want
		applied.RecommendationFingerprint = state.RecommendationFingerprint
		applied.FactVersion = state.FactVersion
		applied.SchedulingGeneration = state.SchedulingGeneration
		ack := Advance(state, applied)
		if ack.State.Status != contracts.RecommendationRolloutObserving || ack.State.Stage != want || ack.State.ObserveUntil == nil {
			t.Fatalf("stage %d apply acknowledgement wrong: %+v", want, ack)
		}
		state = ack.State
		now = state.ObserveUntil.Add(time.Second)
		observed := eventWithRevalidation(state, now, contracts.RecommendationRolloutEventObserve)
		observed.AfterEvidence = passingAfter(state, now)
		verdict := Advance(state, observed)
		if len(verdict.Reasons) != 0 {
			t.Fatalf("stage %d observation failed: %+v", want, verdict)
		}
		if want == contracts.RecommendationRolloutStage100 {
			if verdict.State.Status != contracts.RecommendationRolloutCompleted {
				t.Fatalf("100%% did not complete: %+v", verdict.State)
			}
		} else if verdict.State.Status != contracts.RecommendationRolloutReady {
			t.Fatalf("stage %d did not become ready: %+v", want, verdict.State)
		}
		state = verdict.State
		now = now.Add(time.Second)
	}
}

func TestAdvanceRequiresEveryGateAtEveryStage(t *testing.T) {
	state, now := validRollout()
	event := eventWithRevalidation(state, now, contracts.RecommendationRolloutEventEvaluate)
	event.Revalidation.Gates = event.Revalidation.Gates[:len(event.Revalidation.Gates)-1]
	got := Advance(state, event)
	if got.State.Status != contracts.RecommendationRolloutBlocked || got.Action.Kind != contracts.RecommendationRolloutActionNone {
		t.Fatalf("missing gate did not block: %+v", got)
	}
	assertReason(t, got, contracts.RecommendationRolloutBlockedMissingGate)
}

func TestAdvanceUnknownGateFailsClosed(t *testing.T) {
	state, now := validRollout()
	event := eventWithRevalidation(state, now, contracts.RecommendationRolloutEventEvaluate)
	event.Revalidation.Gates[0].Status = contracts.RecommendationRolloutGateUnknown
	got := Advance(state, event)
	if got.Action.Kind != contracts.RecommendationRolloutActionNone {
		t.Fatalf("unknown gate emitted apply: %+v", got)
	}
	assertReason(t, got, contracts.RecommendationRolloutBlockedUnknownGate)
}

func TestAdvanceFactEvidenceGenerationOrExpiryChangeBlocks(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*contracts.RecommendationRolloutRevalidation)
		reason contracts.RecommendationRolloutBlockReason
	}{
		{"fingerprint", func(v *contracts.RecommendationRolloutRevalidation) { v.RecommendationFingerprint = "changed" }, contracts.RecommendationRolloutBlockedFingerprintChanged},
		{"fact version", func(v *contracts.RecommendationRolloutRevalidation) { v.FactVersion++ }, contracts.RecommendationRolloutBlockedFactVersionChanged},
		{"evidence", func(v *contracts.RecommendationRolloutRevalidation) { v.EvidenceIDs[0] = "changed" }, contracts.RecommendationRolloutBlockedEvidenceChanged},
		{"generation", func(v *contracts.RecommendationRolloutRevalidation) { v.SchedulingGeneration++ }, contracts.RecommendationRolloutBlockedGenerationChanged},
		{"expired", func(v *contracts.RecommendationRolloutRevalidation) { v.RecommendationExpiresAt = v.EvidenceObservedAt }, contracts.RecommendationRolloutBlockedRecommendationExpired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, now := validRollout()
			event := eventWithRevalidation(state, now, contracts.RecommendationRolloutEventEvaluate)
			test.mutate(event.Revalidation)
			got := Advance(state, event)
			if got.Action.Kind != contracts.RecommendationRolloutActionNone {
				t.Fatalf("changed evidence emitted apply: %+v", got)
			}
			assertReason(t, got, test.reason)
		})
	}
}

func TestAdvanceObservationFailureRestoresHealthyBaselineNotQualityCircuitSemantics(t *testing.T) {
	state, now := observingFixture(t)
	event := eventWithRevalidation(state, now, contracts.RecommendationRolloutEventObserve)
	event.AfterEvidence = passingAfter(state, now)
	event.AfterEvidence.Callability = contracts.RecommendationRolloutGateBlocked
	got := Advance(state, event)
	if got.State.Status != contracts.RecommendationRolloutRollbackRequired || got.Action.Kind != contracts.RecommendationRolloutActionPlanRollback ||
		got.Action.BaselineFingerprint != state.BaselineFingerprint || got.Action.TargetStage != contracts.RecommendationRolloutStageNone {
		t.Fatalf("failure did not request exact baseline rollback: %+v", got)
	}
	assertReason(t, got, contracts.RecommendationRolloutBlockedCallability)

	after := passingAfter(got.State, now.Add(time.Second))
	after.Stage = contracts.RecommendationRolloutStageNone
	after.BaselineFingerprint = state.BaselineFingerprint
	after.EvidenceIDs = []string{"weight-set-sha256:" + state.BaselineFingerprint}
	after.Callability = contracts.RecommendationRolloutGateUnknown
	after.Quality = contracts.RecommendationRolloutGateUnknown
	rolled := baseEvent(got.State, now.Add(time.Second), contracts.RecommendationRolloutEventRollbackApplied)
	rolled.AppliedStage = contracts.RecommendationRolloutStageNone
	rolled.RecommendationFingerprint = state.RecommendationFingerprint
	rolled.SchedulingGeneration = state.SchedulingGeneration
	rolled.AfterEvidence = after
	verified := Advance(got.State, rolled)
	if verified.State.Status != contracts.RecommendationRolloutRolledBack || verified.State.Stage != contracts.RecommendationRolloutStageNone {
		t.Fatalf("baseline rollback not verified: %+v", verified)
	}
}

func TestAdvanceRollbackFailureRemainsRetryableAndNeverClaimsSuccess(t *testing.T) {
	state, now := observingFixture(t)
	event := eventWithRevalidation(state, now, contracts.RecommendationRolloutEventObserve)
	event.AfterEvidence = passingAfter(state, now)
	event.AfterEvidence.Quality = contracts.RecommendationRolloutGateUnknown
	failedObservation := Advance(state, event)

	rollbackFailed := baseEvent(failedObservation.State, now.Add(time.Second), contracts.RecommendationRolloutEventRollbackFailed)
	got := Advance(failedObservation.State, rollbackFailed)
	if got.State.Status != contracts.RecommendationRolloutRollbackRequired || got.Action.Kind != contracts.RecommendationRolloutActionPlanRollback {
		t.Fatalf("rollback failure was terminalized incorrectly: %+v", got)
	}
	assertReason(t, got, contracts.RecommendationRolloutBlockedRollbackFailed)

	retry := baseEvent(got.State, now.Add(2*time.Second), contracts.RecommendationRolloutEventRetryRollback)
	retried := Advance(got.State, retry)
	if retried.Action.Kind != contracts.RecommendationRolloutActionPlanRollback || retried.Action.BaselineFingerprint != state.BaselineFingerprint {
		t.Fatalf("rollback could not be retried: %+v", retried)
	}
}

func TestAdvanceStageAcknowledgementIsGenerationAndFingerprintFenced(t *testing.T) {
	state, now := validRollout()
	evaluation := Advance(state, eventWithRevalidation(state, now, contracts.RecommendationRolloutEventEvaluate))
	event := baseEvent(evaluation.State, now.Add(time.Second), contracts.RecommendationRolloutEventStageApplied)
	event.AppliedStage = contracts.RecommendationRolloutStage10
	event.RecommendationFingerprint = "stale-worker"
	event.FactVersion = evaluation.State.FactVersion
	event.SchedulingGeneration = evaluation.State.SchedulingGeneration
	got := Advance(evaluation.State, event)
	if got.State.Status != contracts.RecommendationRolloutRollbackRequired || got.Action.Kind != contracts.RecommendationRolloutActionPlanRollback {
		t.Fatalf("stale acknowledgement did not roll back: %+v", got)
	}
	assertReason(t, got, contracts.RecommendationRolloutBlockedFingerprintChanged)
}

func TestAdvanceDoesNotMutateInputSlices(t *testing.T) {
	state, now := validRollout()
	original := append([]string(nil), state.EvidenceIDs...)
	event := eventWithRevalidation(state, now, contracts.RecommendationRolloutEventEvaluate)
	_ = Advance(state, event)
	if !reflect.DeepEqual(state.EvidenceIDs, original) {
		t.Fatalf("input mutated: %v -> %v", original, state.EvidenceIDs)
	}
}

func observingFixture(t *testing.T) (contracts.RecommendationRolloutState, time.Time) {
	t.Helper()
	state, now := validRollout()
	evaluation := Advance(state, eventWithRevalidation(state, now, contracts.RecommendationRolloutEventEvaluate))
	applied := baseEvent(evaluation.State, now.Add(time.Second), contracts.RecommendationRolloutEventStageApplied)
	applied.AppliedStage = contracts.RecommendationRolloutStage10
	applied.RecommendationFingerprint = state.RecommendationFingerprint
	applied.FactVersion = state.FactVersion
	applied.SchedulingGeneration = state.SchedulingGeneration
	ack := Advance(evaluation.State, applied)
	if ack.State.ObserveUntil == nil {
		t.Fatalf("fixture did not enter observation: %+v", ack)
	}
	return ack.State, ack.State.ObserveUntil.Add(time.Second)
}

func validRollout() (contracts.RecommendationRolloutState, time.Time) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	return contracts.RecommendationRolloutState{
		ID: "rollout-1", UserID: 42, PlanID: "plan-1", RecommendationID: "rec-1",
		RecommendationFingerprint: "fp-1", FactVersion: 7, EvidenceIDs: []string{"price-1", "quality-1"},
		BaselineFingerprint: "baseline-1", SchedulingGeneration: 11,
		Status: contracts.RecommendationRolloutReady, Stage: contracts.RecommendationRolloutStageNone,
		ObservationSeconds: 60, RecommendationExpiresAt: now.Add(24 * time.Hour), StartedAt: now, UpdatedAt: now,
	}, now.Add(time.Minute)
}

func eventWithRevalidation(state contracts.RecommendationRolloutState, now time.Time, typ contracts.RecommendationRolloutEventType) contracts.RecommendationRolloutEvent {
	event := baseEvent(state, now, typ)
	event.Revalidation = &contracts.RecommendationRolloutRevalidation{
		UserID: state.UserID, PlanID: state.PlanID, RecommendationID: state.RecommendationID,
		RecommendationFingerprint: state.RecommendationFingerprint, FactVersion: state.FactVersion,
		EvidenceIDs: append([]string(nil), state.EvidenceIDs...), EvidenceObservedAt: now.Add(-time.Minute),
		EvidenceFreshUntil: now.Add(time.Hour), RecommendationExpiresAt: state.RecommendationExpiresAt,
		SchedulingGeneration: state.SchedulingGeneration, Gates: passingGates(),
	}
	return event
}

func baseEvent(state contracts.RecommendationRolloutState, now time.Time, typ contracts.RecommendationRolloutEventType) contracts.RecommendationRolloutEvent {
	return contracts.RecommendationRolloutEvent{Type: typ, UserID: state.UserID, PlanID: state.PlanID, RecommendationID: state.RecommendationID, Now: now}
}

func passingGates() []contracts.RecommendationRolloutGate {
	result := make([]contracts.RecommendationRolloutGate, 0)
	for _, kind := range contracts.RecommendationRolloutRequiredGates() {
		result = append(result, contracts.RecommendationRolloutGate{Kind: kind, Status: contracts.RecommendationRolloutGatePassed})
	}
	return result
}

func passingAfter(state contracts.RecommendationRolloutState, now time.Time) *contracts.RecommendationRolloutAfterEvidence {
	return &contracts.RecommendationRolloutAfterEvidence{
		Stage: state.Stage, RecommendationFingerprint: state.RecommendationFingerprint,
		SchedulingGeneration: state.SchedulingGeneration, EvidenceIDs: []string{"after-1"},
		ObservedAt: now.Add(-time.Minute), FreshUntil: now.Add(time.Hour),
		Callability: contracts.RecommendationRolloutGatePassed, Quality: contracts.RecommendationRolloutGatePassed,
	}
}

func assertReason(t *testing.T, got contracts.RecommendationRolloutDecision, want contracts.RecommendationRolloutBlockReason) {
	t.Helper()
	for _, reason := range got.Reasons {
		if reason == want {
			return
		}
	}
	t.Fatalf("missing reason %q in %+v", want, got)
}
