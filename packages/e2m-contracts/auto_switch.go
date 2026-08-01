package contracts

import "time"

// This file models Phase 4: the automatic-switch decision. The strategy engine
// (Phase 3) is pure and only ranks channels; it never acts. The auto-switch
// orchestrator turns a ranking into a concrete "switch this failing channel out
// and let a healthy backup carry traffic" intent, runs it through the existing
// RoutePlan + reconcile engine (dry-run -> risk grade -> canary apply ->
// observe -> rollback), and records one AutoSwitchDecision per intent so the
// whole closed loop is auditable and explainable.
//
// Design invariants (from docs/development/health-driven-auto-switching.md):
//   - Automatic execution never bypasses publish/reconcile.
//   - The first version never auto delete/deprovisions; a switch only disables
//     the failing channel and (re-)enables healthy ones.
//   - "Switch out fast, switch back slow": a switch is applied quickly on
//     failure, but kept under an observation window before it is called done,
//     and rolled back automatically if the window does not improve.
//   - Idempotent per failure window: the same failing intent must not spawn
//     duplicate equivalent decisions (Fingerprint dedupes active decisions).

// AutoSwitchStatus is the lifecycle of one decision.
type AutoSwitchStatus string

const (
	// AutoSwitchProposed: a switch is warranted but was not auto-applied
	// (approval required, auto-apply disabled, or mid-risk). Advisory only; the
	// desired state was left unchanged.
	AutoSwitchProposed AutoSwitchStatus = "proposed"
	// AutoSwitchApproved is the operator's side-effect-free acceptance of the
	// exact dry-run intent. Execute must still obtain a bounded scheduling lease
	// and verify the preview has not changed before touching the gateway.
	AutoSwitchApproved AutoSwitchStatus = "approved"
	// AutoSwitchRejected is an operator-declined intent. It is terminal and must
	// not block a future evaluation after health facts change.
	AutoSwitchRejected AutoSwitchStatus = "rejected"
	// AutoSwitchSkipped: a switch was considered but deliberately not taken
	// (no eligible backup, high risk, dampened, or nothing to change).
	AutoSwitchSkipped AutoSwitchStatus = "skipped"
	// AutoSwitchApplying: apply is in progress (transient).
	AutoSwitchApplying AutoSwitchStatus = "applying"
	// AutoSwitchObserving: the switch was applied and is inside its observation
	// window, awaiting a pass/fail verdict.
	AutoSwitchObserving AutoSwitchStatus = "observing"
	// AutoSwitchCompleted: the observation window passed; the switch stands.
	AutoSwitchCompleted AutoSwitchStatus = "completed"
	// AutoSwitchRolledBack: the observation window failed; the switch was
	// automatically reverted (failing channel restored) via reconcile.
	AutoSwitchRolledBack AutoSwitchStatus = "rolled_back"
	// AutoSwitchFailed: the apply itself errored; no clean switch happened.
	AutoSwitchFailed AutoSwitchStatus = "failed"
)

// IsActive reports whether a decision still blocks an equivalent new one. Only
// non-terminal states block: a terminal decision (skipped/completed/rolled_back
// /failed) never dedupes a fresh evaluation.
func (s AutoSwitchStatus) IsActive() bool {
	switch s {
	case AutoSwitchProposed, AutoSwitchApproved, AutoSwitchApplying, AutoSwitchObserving:
		return true
	default:
		return false
	}
}

// AutoSwitchDecision is the persisted record of one automatic-switch intent and
// its outcome. RiskLevel reuses the platform RiskLevel scale so a decision reads
// the same as an audit row and drives the same notify gating: L1 low (auto
// canary-appliable), L2 mid (proposed for a human), L3 high (skipped + alert).
type AutoSwitchDecision struct {
	ID         string `json:"id"`
	UserID     int64  `json:"user_id,omitempty"`
	PlanID     string `json:"plan_id"`
	InstanceID string `json:"instance_id,omitempty"`
	PoolID     string `json:"pool_id,omitempty"`

	Strategy RouteStrategyType   `json:"strategy"`
	Trigger  ReconcileRunTrigger `json:"trigger"`
	// TriggerReason is the human (Chinese) explanation of why the switch fired,
	// e.g. which channel was gated out and on what signal.
	TriggerReason string `json:"trigger_reason,omitempty"`

	// FromChannelID is the failing channel being switched out; ToChannelID is
	// the best eligible backup that will carry traffic. ToChannelID is empty
	// when there is no eligible backup (which makes the decision high-risk).
	FromChannelID string `json:"from_channel_id,omitempty"`
	ToChannelID   string `json:"to_channel_id,omitempty"`

	RiskLevel  RiskLevel `json:"risk_level"`
	RiskReason string    `json:"risk_reason,omitempty"`

	Status      AutoSwitchStatus `json:"status"`
	AutoApplied bool             `json:"auto_applied"`

	// Fingerprint is a stable key of the switch intent (plan+from+to+strategy).
	// The orchestrator refuses to create a second active decision with the same
	// fingerprint, so one failure window yields exactly one live decision.
	Fingerprint string `json:"fingerprint,omitempty"`

	// DryRunResult is the reconcile preview the decision was graded on; it is
	// always computed before any apply, and recorded even when the decision is
	// only proposed/skipped so an operator sees exactly what would have changed.
	DryRunResult ReconcilePlan `json:"dry_run_result"`

	Error           string `json:"error,omitempty"`
	ObservationNote string `json:"observation_note,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// AppliedAt is set when the switch reconcile was applied; ObserveUntil marks
	// the end of the observation window; ResolvedAt is set when the decision
	// reaches a terminal state. All three are nil until they happen.
	AppliedAt    *time.Time `json:"applied_at,omitempty"`
	ObserveUntil *time.Time `json:"observe_until,omitempty"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
	// LeaseUntil bounds the transient applying state. A scheduler that dies
	// after claiming work cannot block the same failure fingerprint forever;
	// another runner repairs the decision after this durable deadline.
	LeaseUntil   *time.Time `json:"lease_until,omitempty"`
	LeaseVersion int64      `json:"lease_version,omitempty"`
	// SchedulingGeneration orders gateway mutations across every decision for
	// the same plan. Unlike LeaseVersion, it never resets for a new decision.
	SchedulingGeneration int64 `json:"scheduling_generation,omitempty"`
}

// AutoSwitchDecisionFilter narrows a decision query. Zero-value fields are
// ignored; Statuses (if set) keeps only decisions in one of the listed states,
// and Limit caps the newest-first result.
type AutoSwitchDecisionFilter struct {
	PlanID     string
	InstanceID string
	PoolID     string
	UserID     int64
	Statuses   []AutoSwitchStatus
	Limit      int
}

// Matches reports whether a decision satisfies the filter (shared by the memory
// store and any caller that filters an in-memory slice).
func (f AutoSwitchDecisionFilter) Matches(d AutoSwitchDecision) bool {
	if f.PlanID != "" && d.PlanID != f.PlanID {
		return false
	}
	if f.InstanceID != "" && d.InstanceID != f.InstanceID {
		return false
	}
	if f.PoolID != "" && d.PoolID != f.PoolID {
		return false
	}
	if f.UserID != 0 && d.UserID != f.UserID {
		return false
	}
	if len(f.Statuses) > 0 {
		ok := false
		for _, s := range f.Statuses {
			if d.Status == s {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}
