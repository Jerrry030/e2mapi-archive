package contracts

import "time"

type RecommendationCoordinationIntentKind string

const (
	RecommendationCoordinationNoop            RecommendationCoordinationIntentKind = "noop"
	RecommendationCoordinationPersistDecision RecommendationCoordinationIntentKind = "persist_decision"
	RecommendationCoordinationApplyStage      RecommendationCoordinationIntentKind = "apply_stage"
	RecommendationCoordinationPlanRollback    RecommendationCoordinationIntentKind = "plan_rollback"
)

type RecommendationCoordinationBlockReason string

const (
	RecommendationCoordinationBlockedInvalidInput        RecommendationCoordinationBlockReason = "invalid_input"
	RecommendationCoordinationBlockedRecommendationStale RecommendationCoordinationBlockReason = "recommendation_stale"
	RecommendationCoordinationBlockedPreviewStale        RecommendationCoordinationBlockReason = "preview_stale"
	RecommendationCoordinationBlockedUnauthorized        RecommendationCoordinationBlockReason = "unauthorized"
	RecommendationCoordinationBlockedRollout             RecommendationCoordinationBlockReason = "rollout_blocked"
)

type RecommendationCoordinationInput struct {
	Recommendation UpstreamRecommendation             `json:"recommendation"`
	CurrentFacts   UpstreamRecommendationCurrentFacts `json:"current_facts"`
	SavedPreview   UpstreamDryRunResult               `json:"saved_preview"`
	CurrentPreview UpstreamDryRunCurrent              `json:"current_preview"`
	Policy         RecommendationExecutionPolicy      `json:"policy"`
	Authorization  RecommendationExecutionContext     `json:"authorization"`
	Rollout        RecommendationRolloutState         `json:"rollout"`
	Event          RecommendationRolloutEvent         `json:"event"`
}

// RecommendationCoordinationDecision is a typed side-effect intent. The
// caller may persist/audit it and pass Action to the existing reconcile layer;
// the coordinator itself never performs either operation.
type RecommendationCoordinationDecision struct {
	Allowed                   bool                                    `json:"allowed"`
	IntentKind                RecommendationCoordinationIntentKind    `json:"intent_kind"`
	RecommendationID          string                                  `json:"recommendation_id"`
	RecommendationFingerprint string                                  `json:"recommendation_fingerprint"`
	PlanID                    string                                  `json:"plan_id"`
	OptimizationKind          string                                  `json:"optimization_kind"`
	RecommendationValidity    UpstreamRecommendationValidity          `json:"recommendation_validity"`
	PreviewValidity           UpstreamDryRunValidity                  `json:"preview_validity"`
	Authorization             RecommendationExecutionAuthorization    `json:"authorization"`
	RolloutDecision           RecommendationRolloutDecision           `json:"rollout_decision"`
	BlockedReasons            []RecommendationCoordinationBlockReason `json:"blocked_reasons"`
	EvaluatedAt               time.Time                               `json:"evaluated_at"`
}

type RecommendationCoordinationAuditIntent struct {
	UserID                    int64                                   `json:"user_id"`
	PlanID                    string                                  `json:"plan_id"`
	RecommendationID          string                                  `json:"recommendation_id"`
	RecommendationFingerprint string                                  `json:"recommendation_fingerprint"`
	OptimizationKind          string                                  `json:"optimization_kind"`
	Outcome                   string                                  `json:"outcome"`
	IntentKind                RecommendationCoordinationIntentKind    `json:"intent_kind"`
	TargetStage               RecommendationRolloutStage              `json:"target_stage"`
	SchedulingGeneration      int64                                   `json:"scheduling_generation"`
	BlockedReasons            []RecommendationCoordinationBlockReason `json:"blocked_reasons"`
	OccurredAt                time.Time                               `json:"occurred_at"`
}
