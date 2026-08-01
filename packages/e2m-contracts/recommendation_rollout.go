package contracts

import "time"

// RecommendationRolloutStage is the real traffic share admitted to an
// intelligence-selected candidate. It is independent from the quality circuit
// recovery cohort, which has different failure and restoration semantics.
type RecommendationRolloutStage int

const (
	// Stage is the percentage of the persisted source-account baseline moved
	// to the destination account. Unrelated account weights are unchanged; 100
	// drains the source even when the pair owns less than all global traffic.
	RecommendationRolloutStageNone RecommendationRolloutStage = 0
	RecommendationRolloutStage10   RecommendationRolloutStage = 10
	RecommendationRolloutStage25   RecommendationRolloutStage = 25
	RecommendationRolloutStage50   RecommendationRolloutStage = 50
	RecommendationRolloutStage100  RecommendationRolloutStage = 100
)

func IsRecommendationRolloutStage(value RecommendationRolloutStage) bool {
	switch value {
	case RecommendationRolloutStageNone, RecommendationRolloutStage10,
		RecommendationRolloutStage25, RecommendationRolloutStage50,
		RecommendationRolloutStage100:
		return true
	default:
		return false
	}
}

type RecommendationRolloutStatus string

const (
	RecommendationRolloutReady            RecommendationRolloutStatus = "ready"
	RecommendationRolloutApplying         RecommendationRolloutStatus = "applying"
	RecommendationRolloutObserving        RecommendationRolloutStatus = "observing"
	RecommendationRolloutRollbackRequired RecommendationRolloutStatus = "rollback_required"
	RecommendationRolloutCompleted        RecommendationRolloutStatus = "completed"
	RecommendationRolloutRolledBack       RecommendationRolloutStatus = "rolled_back"
	RecommendationRolloutBlocked          RecommendationRolloutStatus = "blocked"
)

func IsRecommendationRolloutStatus(value RecommendationRolloutStatus) bool {
	switch value {
	case RecommendationRolloutReady, RecommendationRolloutApplying,
		RecommendationRolloutObserving, RecommendationRolloutRollbackRequired,
		RecommendationRolloutCompleted, RecommendationRolloutRolledBack,
		RecommendationRolloutBlocked:
		return true
	default:
		return false
	}
}

// RecommendationRolloutGateKind is a closed set. Every revalidation must carry
// every gate exactly once; omissions and unknown values fail closed.
type RecommendationRolloutGateKind string

const (
	RecommendationRolloutGateAuthorization RecommendationRolloutGateKind = "authorization"
	RecommendationRolloutGateDryRun        RecommendationRolloutGateKind = "dry_run"
	RecommendationRolloutGatePrice         RecommendationRolloutGateKind = "price"
	RecommendationRolloutGateBalance       RecommendationRolloutGateKind = "balance"
	RecommendationRolloutGateCapacity      RecommendationRolloutGateKind = "capacity"
	RecommendationRolloutGateQuality       RecommendationRolloutGateKind = "quality"
	RecommendationRolloutGateLifecycle     RecommendationRolloutGateKind = "lifecycle"
	RecommendationRolloutGateMaintenance   RecommendationRolloutGateKind = "maintenance"
	RecommendationRolloutGateCallability   RecommendationRolloutGateKind = "callability"
)

func RecommendationRolloutRequiredGates() []RecommendationRolloutGateKind {
	return []RecommendationRolloutGateKind{
		RecommendationRolloutGateAuthorization,
		RecommendationRolloutGateDryRun,
		RecommendationRolloutGatePrice,
		RecommendationRolloutGateBalance,
		RecommendationRolloutGateCapacity,
		RecommendationRolloutGateQuality,
		RecommendationRolloutGateLifecycle,
		RecommendationRolloutGateMaintenance,
		RecommendationRolloutGateCallability,
	}
}

func IsRecommendationRolloutGateKind(value RecommendationRolloutGateKind) bool {
	for _, required := range RecommendationRolloutRequiredGates() {
		if value == required {
			return true
		}
	}
	return false
}

type RecommendationRolloutGateStatus string

const (
	RecommendationRolloutGatePassed  RecommendationRolloutGateStatus = "passed"
	RecommendationRolloutGateBlocked RecommendationRolloutGateStatus = "blocked"
	RecommendationRolloutGateUnknown RecommendationRolloutGateStatus = "unknown"
)

func IsRecommendationRolloutGateStatus(value RecommendationRolloutGateStatus) bool {
	return value == RecommendationRolloutGatePassed || value == RecommendationRolloutGateBlocked || value == RecommendationRolloutGateUnknown
}

type RecommendationRolloutGate struct {
	Kind       RecommendationRolloutGateKind   `json:"kind"`
	Status     RecommendationRolloutGateStatus `json:"status"`
	ReasonCode string                          `json:"reason_code,omitempty"`
}

// RecommendationRolloutRevalidation is a single, current decision snapshot.
// Its identity and evidence bindings must match the recommendation exactly.
type RecommendationRolloutRevalidation struct {
	UserID                    int64                       `json:"user_id"`
	PlanID                    string                      `json:"plan_id"`
	RecommendationID          string                      `json:"recommendation_id"`
	RecommendationFingerprint string                      `json:"recommendation_fingerprint"`
	FactVersion               int64                       `json:"fact_version"`
	EvidenceIDs               []string                    `json:"evidence_ids"`
	EvidenceObservedAt        time.Time                   `json:"evidence_observed_at"`
	EvidenceFreshUntil        time.Time                   `json:"evidence_fresh_until"`
	RecommendationExpiresAt   time.Time                   `json:"recommendation_expires_at"`
	SchedulingGeneration      int64                       `json:"scheduling_generation"`
	Gates                     []RecommendationRolloutGate `json:"gates"`
}

// RecommendationRolloutAfterEvidence proves the traffic admitted at one stage
// remained callable and healthy through its observation window. Rollback uses
// StageNone plus the exact baseline fingerprint and a canonical weight-set
// digest to prove restoration; its callability/quality fields remain unknown
// because a scheduling read-back is not a service-health observation.
type RecommendationRolloutAfterEvidence struct {
	Stage                     RecommendationRolloutStage      `json:"stage"`
	RecommendationFingerprint string                          `json:"recommendation_fingerprint"`
	BaselineFingerprint       string                          `json:"baseline_fingerprint,omitempty"`
	SchedulingGeneration      int64                           `json:"scheduling_generation"`
	EvidenceIDs               []string                        `json:"evidence_ids"`
	ObservedAt                time.Time                       `json:"observed_at"`
	FreshUntil                time.Time                       `json:"fresh_until"`
	Callability               RecommendationRolloutGateStatus `json:"callability"`
	Quality                   RecommendationRolloutGateStatus `json:"quality"`
}

// RecommendationRolloutState is a durable pure-domain state. Stage is the
// currently admitted traffic; PendingStage is non-zero only while an apply is
// awaiting acknowledgement.
type RecommendationRolloutState struct {
	ID                        string                              `json:"id"`
	UserID                    int64                               `json:"user_id"`
	PlanID                    string                              `json:"plan_id"`
	RecommendationID          string                              `json:"recommendation_id"`
	RecommendationFingerprint string                              `json:"recommendation_fingerprint"`
	FactVersion               int64                               `json:"fact_version"`
	EvidenceIDs               []string                            `json:"evidence_ids"`
	BaselineFingerprint       string                              `json:"baseline_fingerprint"`
	SchedulingGeneration      int64                               `json:"scheduling_generation"`
	Status                    RecommendationRolloutStatus         `json:"status"`
	Stage                     RecommendationRolloutStage          `json:"stage"`
	PendingStage              RecommendationRolloutStage          `json:"pending_stage"`
	ObservationSeconds        int                                 `json:"observation_seconds"`
	RecommendationExpiresAt   time.Time                           `json:"recommendation_expires_at"`
	StartedAt                 time.Time                           `json:"started_at"`
	StageStartedAt            *time.Time                          `json:"stage_started_at,omitempty"`
	ObserveUntil              *time.Time                          `json:"observe_until,omitempty"`
	LastAfterEvidence         *RecommendationRolloutAfterEvidence `json:"last_after_evidence,omitempty"`
	RollbackReasons           []RecommendationRolloutBlockReason  `json:"rollback_reasons"`
	UpdatedAt                 time.Time                           `json:"updated_at"`
}

type RecommendationRolloutEventType string

const (
	RecommendationRolloutEventEvaluate         RecommendationRolloutEventType = "evaluate"
	RecommendationRolloutEventStageApplied     RecommendationRolloutEventType = "stage_applied"
	RecommendationRolloutEventStageApplyFailed RecommendationRolloutEventType = "stage_apply_failed"
	RecommendationRolloutEventObserve          RecommendationRolloutEventType = "observe"
	RecommendationRolloutEventRetryRollback    RecommendationRolloutEventType = "retry_rollback"
	RecommendationRolloutEventRollbackApplied  RecommendationRolloutEventType = "rollback_applied"
	RecommendationRolloutEventRollbackFailed   RecommendationRolloutEventType = "rollback_failed"
)

func IsRecommendationRolloutEventType(value RecommendationRolloutEventType) bool {
	switch value {
	case RecommendationRolloutEventEvaluate, RecommendationRolloutEventStageApplied,
		RecommendationRolloutEventStageApplyFailed, RecommendationRolloutEventObserve,
		RecommendationRolloutEventRetryRollback, RecommendationRolloutEventRollbackApplied,
		RecommendationRolloutEventRollbackFailed:
		return true
	default:
		return false
	}
}

type RecommendationRolloutEvent struct {
	Type                      RecommendationRolloutEventType      `json:"type"`
	UserID                    int64                               `json:"user_id"`
	PlanID                    string                              `json:"plan_id"`
	RecommendationID          string                              `json:"recommendation_id"`
	Now                       time.Time                           `json:"now"`
	AppliedStage              RecommendationRolloutStage          `json:"applied_stage,omitempty"`
	RecommendationFingerprint string                              `json:"recommendation_fingerprint,omitempty"`
	FactVersion               int64                               `json:"fact_version,omitempty"`
	SchedulingGeneration      int64                               `json:"scheduling_generation,omitempty"`
	Revalidation              *RecommendationRolloutRevalidation  `json:"revalidation,omitempty"`
	AfterEvidence             *RecommendationRolloutAfterEvidence `json:"after_evidence,omitempty"`
}

type RecommendationRolloutActionKind string

const (
	RecommendationRolloutActionNone         RecommendationRolloutActionKind = "none"
	RecommendationRolloutActionApplyStage   RecommendationRolloutActionKind = "apply_stage"
	RecommendationRolloutActionPlanRollback RecommendationRolloutActionKind = "plan_rollback"
)

// RecommendationRolloutAction is an intent for the existing reconcile layer,
// never a direct gateway command. The adapter must retain generation fencing.
type RecommendationRolloutAction struct {
	Kind                         RecommendationRolloutActionKind `json:"kind"`
	TargetStage                  RecommendationRolloutStage      `json:"target_stage"`
	ExpectedSchedulingGeneration int64                           `json:"expected_scheduling_generation"`
	RecommendationFingerprint    string                          `json:"recommendation_fingerprint"`
	BaselineFingerprint          string                          `json:"baseline_fingerprint,omitempty"`
}

type RecommendationRolloutBlockReason string

const (
	RecommendationRolloutBlockedInvalidState          RecommendationRolloutBlockReason = "invalid_state"
	RecommendationRolloutBlockedInvalidEvent          RecommendationRolloutBlockReason = "invalid_event"
	RecommendationRolloutBlockedOwnerScope            RecommendationRolloutBlockReason = "owner_scope_mismatch"
	RecommendationRolloutBlockedRecommendationExpired RecommendationRolloutBlockReason = "recommendation_expired"
	RecommendationRolloutBlockedFingerprintChanged    RecommendationRolloutBlockReason = "fingerprint_changed"
	RecommendationRolloutBlockedFactVersionChanged    RecommendationRolloutBlockReason = "fact_version_changed"
	RecommendationRolloutBlockedEvidenceChanged       RecommendationRolloutBlockReason = "evidence_changed"
	RecommendationRolloutBlockedEvidenceStale         RecommendationRolloutBlockReason = "evidence_stale"
	RecommendationRolloutBlockedGenerationChanged     RecommendationRolloutBlockReason = "generation_changed"
	RecommendationRolloutBlockedMissingGate           RecommendationRolloutBlockReason = "missing_gate"
	RecommendationRolloutBlockedDuplicateGate         RecommendationRolloutBlockReason = "duplicate_gate"
	RecommendationRolloutBlockedGate                  RecommendationRolloutBlockReason = "gate_blocked"
	RecommendationRolloutBlockedUnknownGate           RecommendationRolloutBlockReason = "gate_unknown"
	RecommendationRolloutBlockedStageMismatch         RecommendationRolloutBlockReason = "stage_mismatch"
	RecommendationRolloutBlockedApplyFailed           RecommendationRolloutBlockReason = "apply_failed"
	RecommendationRolloutBlockedAfterEvidence         RecommendationRolloutBlockReason = "after_evidence_invalid"
	RecommendationRolloutBlockedCallability           RecommendationRolloutBlockReason = "callability_failed"
	RecommendationRolloutBlockedQuality               RecommendationRolloutBlockReason = "quality_failed"
	RecommendationRolloutBlockedRollbackFailed        RecommendationRolloutBlockReason = "rollback_failed"
	RecommendationRolloutBlockedOperatorRequested     RecommendationRolloutBlockReason = "operator_requested"
)

func IsRecommendationRolloutBlockReason(value RecommendationRolloutBlockReason) bool {
	switch value {
	case RecommendationRolloutBlockedInvalidState, RecommendationRolloutBlockedInvalidEvent,
		RecommendationRolloutBlockedOwnerScope, RecommendationRolloutBlockedRecommendationExpired,
		RecommendationRolloutBlockedFingerprintChanged, RecommendationRolloutBlockedFactVersionChanged,
		RecommendationRolloutBlockedEvidenceChanged, RecommendationRolloutBlockedEvidenceStale,
		RecommendationRolloutBlockedGenerationChanged, RecommendationRolloutBlockedMissingGate,
		RecommendationRolloutBlockedDuplicateGate, RecommendationRolloutBlockedGate,
		RecommendationRolloutBlockedUnknownGate, RecommendationRolloutBlockedStageMismatch,
		RecommendationRolloutBlockedApplyFailed, RecommendationRolloutBlockedAfterEvidence,
		RecommendationRolloutBlockedCallability, RecommendationRolloutBlockedQuality,
		RecommendationRolloutBlockedRollbackFailed, RecommendationRolloutBlockedOperatorRequested:
		return true
	default:
		return false
	}
}

type RecommendationRolloutDecision struct {
	State       RecommendationRolloutState         `json:"state"`
	Action      RecommendationRolloutAction        `json:"action"`
	Reasons     []RecommendationRolloutBlockReason `json:"reasons"`
	FailedGates []RecommendationRolloutGateKind    `json:"failed_gates"`
}
