package autoswitch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/strategy"
)

const qualityCircuitCASAttempts = 8

// qualityCircuitStates loads the durable scheduling gates for one plan. Open
// and half-open are both excluded from normal traffic; half-open only describes
// recovery progress and is not a canary-traffic state.
func (o *Orchestrator) qualityCircuitStates(ctx context.Context, planID string) (map[string]contracts.QualityCircuitState, error) {
	runtimes, err := o.store.ListQualityCircuitRuntimes(ctx, contracts.QualityCircuitRuntimeFilter{PlanID: planID})
	if err != nil {
		return nil, err
	}
	states := make(map[string]contracts.QualityCircuitState, len(runtimes))
	for _, runtime := range runtimes {
		states[runtime.ChannelID] = runtime.State
	}
	return states, nil
}

func qualityCircuitBlocksScheduling(state contracts.QualityCircuitState) bool {
	return state == contracts.QualityCircuitOpen || state == contracts.QualityCircuitHalfOpen
}

// openQualityCircuit persists an ejection after the binding-side effect has
// succeeded. Replaying the same timestamp is idempotent; a later explicit
// ejection reopens and extends backoff. Store versions keep stale schedulers
// from overwriting recovery progress.
func (o *Orchestrator) openQualityCircuit(ctx context.Context, planID, channelID string, evaluation strategy.PenaltyEvaluation, openedAt time.Time) (contracts.QualityCircuitRuntime, error) {
	if planID == "" || channelID == "" {
		return contracts.QualityCircuitRuntime{}, fmt.Errorf("open quality circuit: plan and channel are required")
	}
	if openedAt.IsZero() {
		openedAt = o.now()
	}
	openedAt = openedAt.UTC()
	evaluation.ChannelID = channelID
	if !evaluation.Eject {
		evaluation.Eject = true
		if evaluation.Reason.Code == "" {
			evaluation.Reason = strategy.Reason{
				Code: strategy.CircuitReasonOpened,
				Text: "channel was removed from downstream scheduling",
			}
		}
	}

	for attempt := 0; attempt < qualityCircuitCASAttempts; attempt++ {
		current, err := o.store.GetQualityCircuitRuntime(ctx, planID, channelID)
		expectedVersion := int64(0)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return contracts.QualityCircuitRuntime{}, err
		}
		if err == nil {
			expectedVersion = current.Version
			if current.State == contracts.QualityCircuitOpen {
				// A duplicate completion for the same ejection is idempotent, but a
				// later explicit ejection event must count as a reopen and extend
				// cooldown. Recovery/probe failures therefore cannot be hidden behind
				// an old open row.
				if current.LastTransitionAt != nil && !openedAt.After(*current.LastTransitionAt) {
					return current, nil
				}
			}
		}

		pure := qualityCircuitFromRuntime(planID, channelID, current)
		eventKind := strategy.CircuitQualityWindow
		// A half-open source that is explicitly removed has failed guarded
		// recovery. Model it as a failed recovery probe so the FSM reopens it and
		// advances cooldown instead of accepting passive evidence.
		if current.State == contracts.QualityCircuitHalfOpen {
			eventKind = strategy.CircuitRecoveryProbe
		}
		policy := strategy.DefaultQualityCircuitPolicy()
		var transition strategy.QualityCircuitTransition
		if current.State == contracts.QualityCircuitOpen {
			// Open circuits intentionally ignore ordinary quality windows. This
			// caller represents a concrete, successfully-applied ejection instead,
			// so replay it through closed while retaining the prior open count; the
			// FSM then records a real reopen and computes the next backoff.
			pure.State = strategy.CircuitClosed
			transition = strategy.AdvanceQualityCircuit(pure, strategy.QualityCircuitEvent{
				Kind: strategy.CircuitQualityWindow, Now: openedAt, Evaluation: evaluation,
			}, policy)
		} else {
			transition = strategy.AdvanceQualityCircuit(pure, strategy.QualityCircuitEvent{
				Kind: eventKind, Now: openedAt, Evaluation: evaluation,
			}, policy)
		}
		input := qualityCircuitRuntimeFromState(planID, channelID, current, transition.Circuit)
		saved, saveErr := o.store.UpsertQualityCircuitRuntime(ctx, input, expectedVersion)
		if saveErr == nil {
			return saved, nil
		}
		if !errors.Is(saveErr, store.ErrConflict) {
			return contracts.QualityCircuitRuntime{}, saveErr
		}
	}
	return contracts.QualityCircuitRuntime{}, fmt.Errorf("open quality circuit %s/%s: %w", planID, channelID, store.ErrConflict)
}

func qualityCircuitFromRuntime(planID, channelID string, runtime contracts.QualityCircuitRuntime) strategy.QualityCircuit {
	state := strategy.QualityCircuitState(runtime.State)
	if state == "" {
		state = strategy.CircuitClosed
	}
	return strategy.QualityCircuit{
		ScopeKey:                  planID + "/" + channelID,
		State:                     state,
		OpenedAt:                  timeValue(runtime.OpenedAt),
		ProbeAfter:                timeValue(runtime.ProbeAfter),
		HalfOpenSince:             timeValue(runtime.HalfOpenSince),
		LastTransition:            timeValue(runtime.LastTransitionAt),
		OpenCount:                 runtime.OpenCount,
		ConsecutiveProbeSuccesses: runtime.ConsecutiveProbeSuccesses,
		LastScore:                 runtime.LastScore,
		LastReason: strategy.Reason{
			Code: runtime.LastReason.Code,
			Text: runtime.LastReason.Text,
		},
	}
}

func qualityCircuitRuntimeFromState(planID, channelID string, prior contracts.QualityCircuitRuntime, circuit strategy.QualityCircuit) contracts.QualityCircuitRuntime {
	prior.PlanID = planID
	prior.ChannelID = channelID
	prior.State = contracts.QualityCircuitState(circuit.State)
	prior.OpenedAt = timePointer(circuit.OpenedAt)
	prior.ProbeAfter = timePointer(circuit.ProbeAfter)
	prior.HalfOpenSince = timePointer(circuit.HalfOpenSince)
	prior.LastTransitionAt = timePointer(circuit.LastTransition)
	prior.OpenCount = circuit.OpenCount
	prior.ConsecutiveProbeSuccesses = circuit.ConsecutiveProbeSuccesses
	prior.LastScore = circuit.LastScore
	prior.LastReason = contracts.QualityCircuitReason{
		Code: circuit.LastReason.Code,
		Text: circuit.LastReason.Text,
	}
	if circuit.State == strategy.CircuitOpen || circuit.State == strategy.CircuitClosed {
		prior.RestorePending = false
	}
	if circuit.State == strategy.CircuitOpen {
		prior.RecoveryReady = false
		prior.RecoveryStage = 0
		prior.RecoveryStageStartedAt = nil
		prior.RecoveryObserveAfter = nil
	}
	return prior
}

func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}

// repairDecisionCircuitFallbacks closes the non-transactional crash window
// between a successful gateway ejection and circuit persistence. Only a
// disabled binding backed by a durable auto-switch decision is repaired; a
// manually disabled binding is never inferred to be a quality failure.
func (o *Orchestrator) repairDecisionCircuitFallbacks(ctx context.Context, planID string, bindings []contracts.PublishedBinding) error {
	decisions, err := o.store.ListAutoSwitchDecisions(ctx, contracts.AutoSwitchDecisionFilter{PlanID: planID, Limit: 100})
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if binding.State != contracts.BindingDisabled {
			continue
		}
		decision, ejectedAt, ok := fallbackEjectionDecision(decisions, binding.ChannelID)
		if !ok {
			continue
		}
		if current, err := o.store.GetQualityCircuitRuntime(ctx, planID, binding.ChannelID); err == nil {
			if qualityCircuitBlocksScheduling(current.State) {
				continue
			}
			transitionedAt := current.UpdatedAt
			if current.LastTransitionAt != nil {
				transitionedAt = *current.LastTransitionAt
			}
			// The fallback also repairs a crash after a recovery worker persisted
			// closed but before it re-enabled the binding. A newer completed
			// ejection decision makes the disabled binding authoritative here.
			if !transitionedAt.Before(ejectedAt) {
				continue
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		evaluation := strategy.PenaltyEvaluation{
			ChannelID: binding.ChannelID,
			Score:     0,
			Eject:     true,
			Reason: strategy.Reason{
				Code: "decision_fallback",
				Text: fmt.Sprintf("repaired quality isolation from auto-switch decision %s", decision.ID),
			},
		}
		if _, err := o.openQualityCircuit(context.WithoutCancel(ctx), planID, binding.ChannelID, evaluation, ejectedAt); err != nil {
			return err
		}
	}
	// Upgrade/repair path: an older recovery worker could enable the binding
	// before persisting closed. Mark that exact state restore-pending so the
	// current recovery worker closes it without re-probing or re-ejecting it.
	for _, binding := range bindings {
		if binding.State != contracts.BindingActive {
			continue
		}
		current, err := o.store.GetQualityCircuitRuntime(ctx, planID, binding.ChannelID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if current.State != contracts.QualityCircuitHalfOpen || current.RestorePending ||
			current.ConsecutiveProbeSuccesses < 3 {
			continue
		}
		current.RestorePending = true
		current.LastReason = contracts.QualityCircuitReason{Code: "restore_pending", Text: "repairing recovered active binding"}
		if _, err := o.store.UpsertQualityCircuitRuntime(context.WithoutCancel(ctx), current, current.Version); err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}

func fallbackEjectionDecision(decisions []contracts.AutoSwitchDecision, channelID string) (contracts.AutoSwitchDecision, time.Time, bool) {
	for _, decision := range decisions {
		if decision.AutoApplied && decision.AppliedAt != nil && decision.FromChannelID == channelID {
			return decision, *decision.AppliedAt, true
		}
		// The decision claim is persisted before reconcile. If Core dies after
		// the source binding is disabled but before AutoApplied/AppliedAt and the
		// circuit are saved, an applying source decision is the durable intent
		// needed to repair that exact crash window.
		if decision.Status == contracts.AutoSwitchApplying && decision.FromChannelID == channelID {
			return decision, decisionFallbackTime(decision), true
		}
		// A replacement that failed apply/observation is explicitly drained and
		// gets its own circuit. The original source's decision remains separate.
		if decision.ToChannelID == channelID && (decision.Status == contracts.AutoSwitchRolledBack || decision.Status == contracts.AutoSwitchFailed) {
			return decision, decisionFallbackTime(decision), true
		}
		// During observation the applying claim retains AppliedAt. A crash after
		// draining a failed replacement but before terminal persistence is thus
		// distinguishable from the initial applying claim, where the replacement
		// starts disabled and must not be falsely quarantined.
		if decision.Status == contracts.AutoSwitchApplying && decision.AutoApplied && decision.AppliedAt != nil && decision.ToChannelID == channelID {
			return decision, decisionFallbackTime(decision), true
		}
	}
	return contracts.AutoSwitchDecision{}, time.Time{}, false
}

func decisionFallbackTime(decision contracts.AutoSwitchDecision) time.Time {
	if decision.ResolvedAt != nil {
		return *decision.ResolvedAt
	}
	if !decision.UpdatedAt.IsZero() {
		return decision.UpdatedAt
	}
	if decision.AppliedAt != nil {
		return *decision.AppliedAt
	}
	return decision.CreatedAt
}

func ejectionEvaluation(channelID, code, text string, score float64) strategy.PenaltyEvaluation {
	if score < 0 || score > 100 {
		score = 0
	}
	return strategy.PenaltyEvaluation{
		ChannelID: channelID,
		Score:     score,
		Eject:     true,
		Reason:    strategy.Reason{Code: code, Text: text},
	}
}

func (o *Orchestrator) bindingDisabled(ctx context.Context, planID, channelID string) bool {
	bindings, err := o.store.ListPublishedBindings(ctx, planID)
	if err != nil {
		return false
	}
	for _, binding := range bindings {
		if binding.ChannelID == channelID {
			return binding.State == contracts.BindingDisabled
		}
	}
	return false
}
