package recommendationcoordination

import (
	"reflect"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/upstreamexperiment"
	"e2m.local/core/internal/upstreamrecommendation"
)

func TestEvaluateComposesFreshPreviewAuthorizationAndCostRollout(t *testing.T) {
	input := coordinationFixture(t)
	decision, audit := Evaluate(input)
	if !decision.Allowed || decision.IntentKind != contracts.RecommendationCoordinationApplyStage ||
		decision.RolloutDecision.Action.TargetStage != contracts.RecommendationRolloutStage10 {
		t.Fatalf("valid coordination blocked: %+v", decision)
	}
	if audit.OptimizationKind != OptimizationKindCost || audit.OptimizationKind == "quality_recovery" || audit.Outcome != "allowed" ||
		audit.TargetStage != contracts.RecommendationRolloutStage10 {
		t.Fatalf("typed audit wrong: %+v", audit)
	}
}

func TestEvaluateChecksIndependentAuthorizationBeforeEveryRolloutStage(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*contracts.RecommendationCoordinationInput)
	}{
		{"default disabled", func(v *contracts.RecommendationCoordinationInput) { v.Policy.Enabled = false }},
		{"kill switch", func(v *contracts.RecommendationCoordinationInput) { v.Policy.KillSwitch = true }},
		{"scope", func(v *contracts.RecommendationCoordinationInput) { v.Policy.PlanID = "other" }},
		{"daily cap", func(v *contracts.RecommendationCoordinationInput) {
			v.Authorization.DailyExecutionCount = v.Policy.DailyExecutionCap
		}},
		{"cooldown", func(v *contracts.RecommendationCoordinationInput) {
			recent := v.Event.Now.Add(-time.Second)
			v.Authorization.LastExecutedAt = &recent
		}},
		{"minimum savings", func(v *contracts.RecommendationCoordinationInput) { v.Authorization.ExpectedSavings = "0.01" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := coordinationFixture(t)
			test.mutate(&input)
			before := input.Rollout
			decision, audit := Evaluate(input)
			if decision.Allowed || decision.IntentKind != contracts.RecommendationCoordinationNoop ||
				decision.RolloutDecision.Action.Kind != contracts.RecommendationRolloutActionNone || audit.Outcome != "blocked" {
				t.Fatalf("authorization gate %s leaked rollout: decision=%+v audit=%+v", test.name, decision, audit)
			}
			assertCoordinationReason(t, decision, contracts.RecommendationCoordinationBlockedUnauthorized)
			if !reflect.DeepEqual(decision.RolloutDecision.State, before) {
				t.Fatalf("blocked authorization advanced state: before=%+v after=%+v", before, decision.RolloutDecision.State)
			}
		})
	}
}

func TestEvaluateStaleFactsAndPreviewDoNotEmitActions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*contracts.RecommendationCoordinationInput)
		reason contracts.RecommendationCoordinationBlockReason
	}{
		{"recommendation fact", func(v *contracts.RecommendationCoordinationInput) { v.CurrentFacts.IntelligenceFactVersion++ }, contracts.RecommendationCoordinationBlockedRecommendationStale},
		{"recommendation evidence", func(v *contracts.RecommendationCoordinationInput) { v.CurrentFacts.EvidenceIDs[0] = "changed" }, contracts.RecommendationCoordinationBlockedRecommendationStale},
		{"preview generation", func(v *contracts.RecommendationCoordinationInput) { v.CurrentPreview.PlanGeneration++ }, contracts.RecommendationCoordinationBlockedPreviewStale},
		{"preview actions", func(v *contracts.RecommendationCoordinationInput) {
			v.CurrentPreview.Plan.Actions[0].RemoteID = "changed"
		}, contracts.RecommendationCoordinationBlockedPreviewStale},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := coordinationFixture(t)
			test.mutate(&input)
			decision, _ := Evaluate(input)
			if decision.Allowed || decision.IntentKind != contracts.RecommendationCoordinationNoop {
				t.Fatalf("stale input emitted action: %+v", decision)
			}
			assertCoordinationReason(t, decision, test.reason)
		})
	}
}

func TestEvaluateRechecksAuthorizationForStageAcknowledgementAndObservation(t *testing.T) {
	input := coordinationFixture(t)
	first, _ := Evaluate(input)
	if !first.Allowed {
		t.Fatalf("initial evaluate failed: %+v", first)
	}
	applying := first.RolloutDecision.State
	input.Rollout = applying
	input.Event = baseRolloutEvent(input, contracts.RecommendationRolloutEventStageApplied, input.Event.Now.Add(time.Second))
	input.Event.AppliedStage = contracts.RecommendationRolloutStage10
	input.Event.RecommendationFingerprint = input.Recommendation.Fingerprint
	input.Event.FactVersion = input.Recommendation.IntelligenceFactVersion
	input.Event.SchedulingGeneration = input.Recommendation.PlanGeneration
	input.Authorization.Now = input.Event.Now
	input.CurrentFacts.Now = input.Event.Now
	input.Policy.KillSwitch = true

	blocked, _ := Evaluate(input)
	if blocked.Allowed || blocked.RolloutDecision.State.Status != contracts.RecommendationRolloutApplying {
		t.Fatalf("kill switch did not stop stage acknowledgement: %+v", blocked)
	}
	assertCoordinationReason(t, blocked, contracts.RecommendationCoordinationBlockedUnauthorized)
}

func TestEvaluateMapsRollbackAsCostOptimizationTypedIntent(t *testing.T) {
	input := coordinationFixture(t)
	input.Rollout.Status = contracts.RecommendationRolloutRollbackRequired
	input.Rollout.RollbackReasons = []contracts.RecommendationRolloutBlockReason{contracts.RecommendationRolloutBlockedQuality}
	input.Event = baseRolloutEvent(input, contracts.RecommendationRolloutEventRetryRollback, input.Event.Now)
	decision, audit := Evaluate(input)
	if !decision.Allowed || decision.IntentKind != contracts.RecommendationCoordinationPlanRollback ||
		decision.RolloutDecision.Action.BaselineFingerprint != input.Rollout.BaselineFingerprint {
		t.Fatalf("rollback intent wrong: %+v", decision)
	}
	if audit.OptimizationKind != OptimizationKindCost || audit.IntentKind != contracts.RecommendationCoordinationPlanRollback {
		t.Fatalf("rollback reused wrong semantic: %+v", audit)
	}
}

func TestEvaluateRollbackBypassesWideningFreshnessAndAuthorizationGates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*contracts.RecommendationCoordinationInput)
	}{
		{
			name: "kill switch",
			mutate: func(input *contracts.RecommendationCoordinationInput) {
				input.Policy.KillSwitch = true
			},
		},
		{
			name: "expired recommendation",
			mutate: func(input *contracts.RecommendationCoordinationInput) {
				input.Recommendation.ExpiresAt = input.Event.Now.Add(-time.Second)
			},
		},
		{
			name: "stale dry run preview",
			mutate: func(input *contracts.RecommendationCoordinationInput) {
				input.CurrentPreview.PlanGeneration++
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := coordinationFixture(t)
			input.Rollout.Status = contracts.RecommendationRolloutRollbackRequired
			input.Rollout.RollbackReasons = []contracts.RecommendationRolloutBlockReason{contracts.RecommendationRolloutBlockedQuality}
			input.Event = baseRolloutEvent(input, contracts.RecommendationRolloutEventRetryRollback, input.Event.Now)
			test.mutate(&input)

			decision, audit := Evaluate(input)
			if !decision.Allowed || decision.IntentKind != contracts.RecommendationCoordinationPlanRollback ||
				decision.RolloutDecision.Action.Kind != contracts.RecommendationRolloutActionPlanRollback ||
				decision.RolloutDecision.Action.BaselineFingerprint != input.Rollout.BaselineFingerprint ||
				decision.RolloutDecision.Action.ExpectedSchedulingGeneration != input.Rollout.SchedulingGeneration {
				t.Fatalf("safe rollback was blocked: decision=%+v audit=%+v", decision, audit)
			}
			if audit.Outcome != "allowed" || audit.IntentKind != contracts.RecommendationCoordinationPlanRollback {
				t.Fatalf("rollback audit = %+v", audit)
			}
		})
	}
}

func TestEvaluateNonRollbackStillFailsClosedWhenBypassedRollbackGatesFail(t *testing.T) {
	input := coordinationFixture(t)
	before := input.Rollout
	input.Policy.KillSwitch = true
	input.Recommendation.ExpiresAt = input.Event.Now.Add(-time.Second)
	input.CurrentPreview.PlanGeneration++

	decision, audit := Evaluate(input)
	if decision.Allowed || decision.IntentKind != contracts.RecommendationCoordinationNoop ||
		decision.RolloutDecision.Action.Kind != contracts.RecommendationRolloutActionNone || audit.Outcome != "blocked" {
		t.Fatalf("traffic-widening event bypassed safety gates: decision=%+v audit=%+v", decision, audit)
	}
	if !reflect.DeepEqual(decision.RolloutDecision.State, before) {
		t.Fatalf("blocked traffic-widening event advanced state: before=%+v after=%+v", before, decision.RolloutDecision.State)
	}
	assertCoordinationReason(t, decision, contracts.RecommendationCoordinationBlockedRecommendationStale)
	assertCoordinationReason(t, decision, contracts.RecommendationCoordinationBlockedPreviewStale)
	assertCoordinationReason(t, decision, contracts.RecommendationCoordinationBlockedUnauthorized)
}

func TestEvaluateOwnerOrRecommendationMismatchFailsClosed(t *testing.T) {
	input := coordinationFixture(t)
	input.Event.UserID++
	decision, _ := Evaluate(input)
	if decision.Allowed {
		t.Fatal("cross-owner event authorized")
	}
	assertCoordinationReason(t, decision, contracts.RecommendationCoordinationBlockedInvalidInput)
}

func coordinationFixture(t *testing.T) contracts.RecommendationCoordinationInput {
	t.Helper()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	recommendation := coordinationRecommendation(now)
	plan := contracts.ReconcilePlan{
		InstanceID: "instance-1", PlanID: "plan-1", DryRun: true,
		Actions: []contracts.ReconcileAction{
			{Type: contracts.ReconcileEnable, ChannelID: "channel-2", RemoteID: "remote-2"},
			{Type: contracts.ReconcileDisable, ChannelID: "channel-1", RemoteID: "remote-1"},
		}, CreatedAt: now,
	}
	hash, err := upstreamexperiment.ActionSetHash(plan)
	if err != nil {
		t.Fatal(err)
	}
	desired := map[string]bool{"channel-1": false, "channel-2": true}
	rollout := contracts.RecommendationRolloutState{
		ID: "rollout-1", UserID: 42, PlanID: "plan-1", RecommendationID: recommendation.ID,
		RecommendationFingerprint: recommendation.Fingerprint, FactVersion: recommendation.IntelligenceFactVersion,
		EvidenceIDs: append([]string(nil), recommendation.EvidenceIDs...), BaselineFingerprint: "baseline-1",
		SchedulingGeneration: recommendation.PlanGeneration, Status: contracts.RecommendationRolloutReady,
		Stage: contracts.RecommendationRolloutStageNone, ObservationSeconds: 60,
		RecommendationExpiresAt: recommendation.ExpiresAt, StartedAt: now, UpdatedAt: now,
	}
	event := contracts.RecommendationRolloutEvent{
		Type: contracts.RecommendationRolloutEventEvaluate, UserID: 42, PlanID: "plan-1", RecommendationID: recommendation.ID, Now: now.Add(time.Minute),
		Revalidation: &contracts.RecommendationRolloutRevalidation{
			UserID: 42, PlanID: "plan-1", RecommendationID: recommendation.ID, RecommendationFingerprint: recommendation.Fingerprint,
			FactVersion: recommendation.IntelligenceFactVersion, EvidenceIDs: append([]string(nil), recommendation.EvidenceIDs...),
			EvidenceObservedAt: now, EvidenceFreshUntil: now.Add(time.Hour), RecommendationExpiresAt: recommendation.ExpiresAt,
			SchedulingGeneration: recommendation.PlanGeneration, Gates: passingRolloutGates(),
		},
	}
	return contracts.RecommendationCoordinationInput{
		Recommendation: recommendation,
		CurrentFacts: contracts.UpstreamRecommendationCurrentFacts{
			UserID: 42, IntelligenceFactVersion: 7, CostLedgerFactVersion: 9, LinkFactVersion: 4, PlanGeneration: 11,
			FromSourceID: "source-1", FromChannelID: "channel-1", FromGroupKey: "group-a",
			ToSourceID: "source-2", ToChannelID: "channel-2", ToGroupKey: "group-a", ModelKey: "model-a",
			PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", PerTokens: 1_000_000,
			AffectedPlanIDs: []string{"plan-1"}, AffectedDownstreams: []string{"downstream-1"}, EvidenceIDs: []string{"evidence-1"},
			FormulaVersion: contracts.UpstreamRecommendationFormulaVersionV1, StrategyVersion: contracts.UpstreamRecommendationStrategyVersionV1, Now: event.Now,
		},
		SavedPreview: contracts.UpstreamDryRunResult{
			UserID: 42, RecommendationID: recommendation.ID, RecommendationFingerprint: recommendation.Fingerprint,
			IntelligenceFactVersion: 7, CostLedgerFactVersion: 9, LinkFactVersion: 4, PlanGeneration: 11,
			PlanID: "plan-1", FromChannelID: "channel-1", ToChannelID: "channel-2", DesiredScheduling: cloneBoolMap(desired),
			ReconcileKind: contracts.ReconcileRunDryRun, Plan: plan, ActionHashVersion: contracts.UpstreamExperimentActionHashVersionV1,
			ActionSetHash: hash, CreatedAt: now,
		},
		CurrentPreview: contracts.UpstreamDryRunCurrent{
			UserID: 42, RecommendationID: recommendation.ID, RecommendationFingerprint: recommendation.Fingerprint,
			IntelligenceFactVersion: 7, CostLedgerFactVersion: 9, LinkFactVersion: 4, PlanGeneration: 11,
			PlanID: "plan-1", FromChannelID: "channel-1", ToChannelID: "channel-2", DesiredScheduling: cloneBoolMap(desired), Plan: plan,
		},
		Policy: contracts.RecommendationExecutionPolicy{
			ID: "policy-1", UserID: 42, Scope: contracts.RecommendationExecutionScopePlan, PlanID: "plan-1", Enabled: true,
			DailyExecutionCap: 3, CooldownSeconds: 300, MinimumSavings: "0.1",
		},
		Authorization: contracts.RecommendationExecutionContext{
			UserID: 42, PlanID: "plan-1", PoolID: "pool-1", ExpectedSavings: recommendation.Savings.PercentLower, Now: event.Now,
		},
		Rollout: rollout, Event: event,
	}
}

func coordinationRecommendation(now time.Time) contracts.UpstreamRecommendation {
	candidate := contracts.UpstreamRecommendationCandidate{
		UserID:                  42,
		IntelligenceFactVersion: 7, CostLedgerFactVersion: 9, LinkFactVersion: 4, PlanGeneration: 11,
		FromSourceID: "source-1", FromChannelID: "channel-1", FromGroupKey: "group-a",
		ToSourceID: "source-2", ToChannelID: "channel-2", ToGroupKey: "group-a", ModelKey: "model-a",
		PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", PerTokens: 1_000_000,
		AffectedPlanIDs: []string{"plan-1"}, AffectedDownstreams: []string{"downstream-1"}, EvidenceIDs: []string{"evidence-1"},
		Constraints: passingRecommendationConstraints(), FromCost: contracts.UpstreamRecommendationCostRange{Lower: "8", Expected: "10", Upper: "12"},
		ToCost:         contracts.UpstreamRecommendationCostRange{Lower: "6", Expected: "6", Upper: "6"},
		FormulaVersion: contracts.UpstreamRecommendationFormulaVersionV1, StrategyVersion: contracts.UpstreamRecommendationStrategyVersionV1,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	recommendation, err := upstreamrecommendation.Build("rec-1", candidate)
	if err != nil {
		panic(err)
	}
	recommendation.Status = contracts.UpstreamRecommendationDryRunPassed
	recommendation.DryRunID = "dry-1"
	return recommendation
}

func passingRecommendationConstraints() []contracts.UpstreamRecommendationConstraint {
	result := make([]contracts.UpstreamRecommendationConstraint, 0)
	for _, kind := range contracts.UpstreamRecommendationRequiredConstraints() {
		result = append(result, contracts.UpstreamRecommendationConstraint{Kind: kind, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{string(kind) + "-evidence"}})
	}
	return result
}

func passingRolloutGates() []contracts.RecommendationRolloutGate {
	result := make([]contracts.RecommendationRolloutGate, 0)
	for _, kind := range contracts.RecommendationRolloutRequiredGates() {
		result = append(result, contracts.RecommendationRolloutGate{Kind: kind, Status: contracts.RecommendationRolloutGatePassed})
	}
	return result
}

func baseRolloutEvent(input contracts.RecommendationCoordinationInput, typ contracts.RecommendationRolloutEventType, now time.Time) contracts.RecommendationRolloutEvent {
	return contracts.RecommendationRolloutEvent{Type: typ, UserID: input.Recommendation.UserID, PlanID: input.Rollout.PlanID, RecommendationID: input.Recommendation.ID, Now: now}
}

func cloneBoolMap(value map[string]bool) map[string]bool {
	result := make(map[string]bool, len(value))
	for key, enabled := range value {
		result[key] = enabled
	}
	return result
}

func assertCoordinationReason(t *testing.T, got contracts.RecommendationCoordinationDecision, want contracts.RecommendationCoordinationBlockReason) {
	t.Helper()
	for _, reason := range got.BlockedReasons {
		if reason == want {
			return
		}
	}
	t.Fatalf("missing coordination reason %q in %+v", want, got)
}
