// Package recommendationcoordination composes the pure UI-13..16 domains. It
// emits typed intents and deliberately has no store, HTTP, publish, autoswitch,
// quality-circuit, or gateway dependency.
package recommendationcoordination

import (
	"sort"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/recommendationexecution"
	"e2m.local/core/internal/recommendationrollout"
	"e2m.local/core/internal/upstreamexperiment"
	"e2m.local/core/internal/upstreamrecommendation"
)

const OptimizationKindCost = "cost_optimization"

// Evaluate rechecks every authorization and freshness gate before every
// traffic-widening rollout event. A rollout already marked rollback_required
// has a separate safety path: retrying its exact baseline restoration must not
// be disabled by an expired recommendation, stale preview, or execution-policy
// kill switch. The rollout state machine still owns rollback state, baseline,
// and scheduling-generation validation.
func Evaluate(input contracts.RecommendationCoordinationInput) (contracts.RecommendationCoordinationDecision, contracts.RecommendationCoordinationAuditIntent) {
	now := input.Event.Now.UTC()
	decision := contracts.RecommendationCoordinationDecision{
		IntentKind:                contracts.RecommendationCoordinationNoop,
		RecommendationID:          input.Recommendation.ID,
		RecommendationFingerprint: input.Recommendation.Fingerprint,
		PlanID:                    input.Rollout.PlanID,
		OptimizationKind:          OptimizationKindCost,
		BlockedReasons:            []contracts.RecommendationCoordinationBlockReason{},
		EvaluatedAt:               now,
	}
	add := func(reason contracts.RecommendationCoordinationBlockReason) {
		for _, existing := range decision.BlockedReasons {
			if existing == reason {
				return
			}
		}
		decision.BlockedReasons = append(decision.BlockedReasons, reason)
	}

	decision.RecommendationValidity = upstreamrecommendation.ValidateCurrent(input.Recommendation, input.CurrentFacts)
	decision.PreviewValidity = upstreamexperiment.ValidatePreview(input.SavedPreview, input.CurrentPreview)
	decision.Authorization = recommendationexecution.Evaluate(input.Policy, input.Authorization)

	rollbackRetry := input.Rollout.Status == contracts.RecommendationRolloutRollbackRequired &&
		input.Event.Type == contracts.RecommendationRolloutEventRetryRollback
	if rollbackRetry {
		if !rollbackCoherent(input) {
			add(contracts.RecommendationCoordinationBlockedInvalidInput)
		}
	} else {
		if !coherent(input) {
			add(contracts.RecommendationCoordinationBlockedInvalidInput)
		}
		if !decision.RecommendationValidity.Current {
			add(contracts.RecommendationCoordinationBlockedRecommendationStale)
		}
		if !decision.PreviewValidity.Current {
			add(contracts.RecommendationCoordinationBlockedPreviewStale)
		}
		if !decision.Authorization.Allowed {
			add(contracts.RecommendationCoordinationBlockedUnauthorized)
		}
	}

	if len(decision.BlockedReasons) == 0 {
		decision.RolloutDecision = recommendationrollout.Advance(input.Rollout, input.Event)
		if len(decision.RolloutDecision.Reasons) > 0 || decision.RolloutDecision.State.Status == contracts.RecommendationRolloutBlocked {
			add(contracts.RecommendationCoordinationBlockedRollout)
		} else {
			decision.IntentKind = mapIntent(decision.RolloutDecision.Action.Kind)
			decision.Allowed = true
		}
	}
	if !decision.Allowed {
		// Fail closed without advancing caller-owned rollout state.
		decision.RolloutDecision = contracts.RecommendationRolloutDecision{
			State:       input.Rollout,
			Action:      contracts.RecommendationRolloutAction{Kind: contracts.RecommendationRolloutActionNone},
			Reasons:     []contracts.RecommendationRolloutBlockReason{},
			FailedGates: []contracts.RecommendationRolloutGateKind{},
		}
		decision.IntentKind = contracts.RecommendationCoordinationNoop
	}
	sort.Slice(decision.BlockedReasons, func(i, j int) bool { return decision.BlockedReasons[i] < decision.BlockedReasons[j] })
	audit := auditIntent(decision, input)
	return decision, audit
}

// rollbackCoherent intentionally depends only on the durable recommendation,
// rollout, and event identities. Current facts, dry-run previews, and execution
// policy are advisory for widening traffic but cannot revoke the ability to
// restore an already-captured baseline.
func rollbackCoherent(input contracts.RecommendationCoordinationInput) bool {
	return input.Recommendation.UserID > 0 && input.Recommendation.UserID == input.Rollout.UserID &&
		input.Recommendation.UserID == input.Event.UserID && strings.TrimSpace(input.Recommendation.ID) != "" &&
		input.Recommendation.ID == input.Rollout.RecommendationID && input.Recommendation.ID == input.Event.RecommendationID &&
		strings.TrimSpace(input.Recommendation.Fingerprint) != "" && input.Recommendation.Fingerprint == input.Rollout.RecommendationFingerprint &&
		strings.TrimSpace(input.Rollout.PlanID) != "" && input.Rollout.PlanID == input.Event.PlanID && !input.Event.Now.IsZero()
}

func coherent(input contracts.RecommendationCoordinationInput) bool {
	return input.Recommendation.UserID > 0 && input.Recommendation.UserID == input.CurrentFacts.UserID &&
		input.Recommendation.UserID == input.SavedPreview.UserID && input.Recommendation.UserID == input.CurrentPreview.UserID &&
		input.Recommendation.UserID == input.Policy.UserID && input.Recommendation.UserID == input.Authorization.UserID &&
		input.Recommendation.UserID == input.Rollout.UserID && input.Recommendation.UserID == input.Event.UserID &&
		strings.TrimSpace(input.Recommendation.ID) != "" && input.Recommendation.ID == input.SavedPreview.RecommendationID &&
		input.Recommendation.ID == input.CurrentPreview.RecommendationID && input.Recommendation.ID == input.Rollout.RecommendationID &&
		input.Recommendation.ID == input.Event.RecommendationID && input.Recommendation.Fingerprint == input.SavedPreview.RecommendationFingerprint &&
		input.Recommendation.Fingerprint == input.CurrentPreview.RecommendationFingerprint && input.Recommendation.Fingerprint == input.Rollout.RecommendationFingerprint &&
		input.Rollout.PlanID == input.SavedPreview.PlanID && input.Rollout.PlanID == input.CurrentPreview.PlanID &&
		input.Rollout.PlanID == input.Authorization.PlanID && input.Rollout.PlanID == input.Event.PlanID && !input.Event.Now.IsZero()
}

func mapIntent(kind contracts.RecommendationRolloutActionKind) contracts.RecommendationCoordinationIntentKind {
	switch kind {
	case contracts.RecommendationRolloutActionApplyStage:
		return contracts.RecommendationCoordinationApplyStage
	case contracts.RecommendationRolloutActionPlanRollback:
		return contracts.RecommendationCoordinationPlanRollback
	case contracts.RecommendationRolloutActionNone:
		return contracts.RecommendationCoordinationPersistDecision
	default:
		return contracts.RecommendationCoordinationNoop
	}
}

func auditIntent(decision contracts.RecommendationCoordinationDecision, input contracts.RecommendationCoordinationInput) contracts.RecommendationCoordinationAuditIntent {
	outcome := "blocked"
	if decision.Allowed {
		outcome = "allowed"
	}
	return contracts.RecommendationCoordinationAuditIntent{
		UserID: input.Recommendation.UserID, PlanID: input.Rollout.PlanID,
		RecommendationID: input.Recommendation.ID, RecommendationFingerprint: input.Recommendation.Fingerprint,
		OptimizationKind: OptimizationKindCost, Outcome: outcome, IntentKind: decision.IntentKind,
		TargetStage:          decision.RolloutDecision.Action.TargetStage,
		SchedulingGeneration: input.Rollout.SchedulingGeneration,
		BlockedReasons:       append([]contracts.RecommendationCoordinationBlockReason(nil), decision.BlockedReasons...),
		OccurredAt:           input.Event.Now.UTC(),
	}
}
