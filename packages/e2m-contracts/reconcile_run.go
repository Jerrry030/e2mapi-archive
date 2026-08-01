package contracts

import (
	"context"
	"time"
)

// This file models the reconcile-run history: a persisted record of every
// publish/reconcile execution (dry-run, apply, rollback). Runs are written by
// the publish engine's unified execution layer, not the HTTP handler, so
// background/automatic switches (health-driven, Phase 4) are recorded too and
// the audit trail is never bypassed.

// ReconcileRunKind is which publish operation produced a run.
type ReconcileRunKind string

const (
	ReconcileRunDryRun   ReconcileRunKind = "dry_run"
	ReconcileRunApply    ReconcileRunKind = "apply"
	ReconcileRunRollback ReconcileRunKind = "rollback"
)

// ReconcileRunTrigger records what initiated a run so the history distinguishes
// an operator click from a health-driven automatic switch.
type ReconcileRunTrigger string

const (
	ReconcileTriggerManual ReconcileRunTrigger = "manual"
	ReconcileTriggerAuto   ReconcileRunTrigger = "auto"
	ReconcileTriggerSystem ReconcileRunTrigger = "system"
)

// ReconcileRunStatus is the outcome of a run.
type ReconcileRunStatus string

const (
	// ReconcileRunSucceeded: dry-run computed cleanly, or apply/rollback
	// executed every action without error.
	ReconcileRunSucceeded ReconcileRunStatus = "succeeded"
	// ReconcileRunPartial: apply/rollback ran but at least one action errored;
	// successful actions were still persisted.
	ReconcileRunPartial ReconcileRunStatus = "partial"
	// ReconcileRunFailed: the run could not be computed (load/validation error);
	// nothing was applied.
	ReconcileRunFailed ReconcileRunStatus = "failed"
)

// ReconcileRun is the persisted history record of one publish/reconcile
// execution. Actions mirror the ReconcilePlan returned to the caller so the
// console can render what changed without re-deriving it.
type ReconcileRun struct {
	ID         string              `json:"id"`
	PlanID     string              `json:"plan_id"`
	InstanceID string              `json:"instance_id,omitempty"`
	UserID     int64               `json:"user_id,omitempty"`
	Kind       ReconcileRunKind    `json:"kind"`
	Trigger    ReconcileRunTrigger `json:"trigger"`
	// ActorType/ActorID identify who initiated the run (from the request actor),
	// mirroring the audit trail's operator attribution.
	ActorType  string             `json:"actor_type,omitempty"`
	ActorID    string             `json:"actor_id,omitempty"`
	Status     ReconcileRunStatus `json:"status"`
	Actions    []ReconcileAction  `json:"actions"`
	Error      string             `json:"error,omitempty"`
	StartedAt  time.Time          `json:"started_at"`
	FinishedAt time.Time          `json:"finished_at"`
}

type reconcileTriggerCtxKey struct{}

// WithReconcileTrigger attaches the run trigger to ctx so the publish engine can
// label a run manual/auto/system. Health-driven automatic switches set "auto".
func WithReconcileTrigger(ctx context.Context, t ReconcileRunTrigger) context.Context {
	return context.WithValue(ctx, reconcileTriggerCtxKey{}, t)
}

// ReconcileTriggerFromContext returns the attached trigger, if any.
func ReconcileTriggerFromContext(ctx context.Context) (ReconcileRunTrigger, bool) {
	t, ok := ctx.Value(reconcileTriggerCtxKey{}).(ReconcileRunTrigger)
	return t, ok
}
