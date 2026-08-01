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

// repairExpiredApplyingDecisions recovers distributed claims abandoned by a
// crashed Core. It never assumes that a claimed intent reached the gateway:
// PlanScheduling first compares the intended state with gateway facts. A fully
// applied intent is completed durably; a partial intent is drained to a local
// fail-closed state; an untouched intent remains leased until the normal
// deadline so any already-durable Connector task has time to expire.
func (o *Orchestrator) repairExpiredApplyingDecisions(ctx context.Context, plan contracts.RoutePlan, _ []contracts.PublishedBinding) error {
	now := o.now().UTC()
	decisions, err := o.store.ListAutoSwitchDecisions(ctx, contracts.AutoSwitchDecisionFilter{
		PlanID: plan.ID, Statuses: []contracts.AutoSwitchStatus{contracts.AutoSwitchApplying}, Limit: 100,
	})
	if err != nil {
		return err
	}
	for _, decision := range decisions {
		leaseUntil := now.Add(autoSwitchApplyingLease)
		claimed, ownsRepair, claimErr := o.store.ClaimExpiredAutoSwitchDecision(
			context.WithoutCancel(ctx), decision.ID, now, now.Add(-autoSwitchApplyingLease), leaseUntil,
		)
		if claimErr != nil {
			return claimErr
		}
		if !ownsRepair {
			continue
		}
		workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), autoSwitchApplyingWorkTimeout)
		workCtx = o.withApplyingSideEffectGuard(workCtx, claimed)
		repairErr := o.repairExpiredApplyingDecision(workCtx, plan, claimed, now)
		cancel()
		if repairErr != nil {
			return repairErr
		}
	}
	return nil
}

func (o *Orchestrator) repairExpiredApplyingDecision(ctx context.Context, plan contracts.RoutePlan, decision contracts.AutoSwitchDecision, now time.Time) error {
	if err := o.renewApplyingLease(ctx, decision); err != nil {
		return err
	}
	if plan.Status == contracts.RoutePlanSuspended {
		return o.terminateSuspendedApplyingDecision(ctx, plan, decision)
	}
	desired := decisionSchedulingIntent(decision)
	preview, err := o.engine.PlanScheduling(o.autoCtx(ctx), plan.ID, desired)
	if err != nil {
		return fmt.Errorf("repair expired decision %s: verify gateway state: %w", decision.ID, err)
	}

	if isObservationClaim(decision) {
		return o.repairExpiredObservationClaim(ctx, plan, decision, preview, now)
	}
	if schedulingIntentSatisfied(preview, desired) {
		return o.completeAppliedDecisionRepair(ctx, plan, decision, desired, now)
	}
	return o.abortPartialDecisionRepair(ctx, plan, decision, preview, now)
}

// terminateSuspendedApplyingDecision preserves suspension as the stronger
// operator intent. Both sides of an interrupted switch are drained, and no
// circuit is opened: a later plan publish starts from explicit operator state.
func (o *Orchestrator) terminateSuspendedApplyingDecision(
	ctx context.Context,
	plan contracts.RoutePlan,
	decision contracts.AutoSwitchDecision,
) error {
	currentPlan, err := o.store.GetRoutePlan(ctx, plan.ID)
	if err != nil {
		return err
	}
	if currentPlan.Status != contracts.RoutePlanSuspended {
		return store.ErrConflict
	}
	desired := make(map[string]bool, 2)
	if decision.FromChannelID != "" {
		desired[decision.FromChannelID] = false
	}
	if decision.ToChannelID != "" {
		desired[decision.ToChannelID] = false
	}
	if len(desired) > 0 {
		if err := o.renewApplyingLease(ctx, decision); err != nil {
			return err
		}
		drainCtx := contracts.WithReconcileSideEffectGuard(o.autoCtx(ctx), func(guardCtx context.Context) error {
			current, getErr := o.store.GetRoutePlan(guardCtx, plan.ID)
			if getErr != nil {
				return getErr
			}
			if current.Status != contracts.RoutePlanSuspended {
				return store.ErrConflict
			}
			if current.SchedulingGeneration != decision.SchedulingGeneration {
				return store.ErrConflict
			}
			return o.renewApplyingLease(guardCtx, decision)
		})
		drainCtx = contracts.WithGatewaySchedulingFence(drainCtx, contracts.GatewaySchedulingFence{
			Scope: "auto-switch/plan/" + decision.PlanID, Version: decision.SchedulingGeneration,
		})
		drained, err := o.engine.ApplyScheduling(drainCtx, plan.ID, desired)
		if err != nil {
			return fmt.Errorf("terminate expired decision %s for suspended plan: drain gateway state: %w", decision.ID, err)
		}
		decision.DryRunResult = drained
	}
	_, err = o.finalizeNote(ctx, decision, contracts.AutoSwitchFailed, contracts.RiskLevelL3,
		"expired switch canceled because route plan is suspended",
		"source and replacement were drained; the route plan remains suspended", nil)
	return err
}

func decisionSchedulingIntent(decision contracts.AutoSwitchDecision) map[string]bool {
	desired := make(map[string]bool, 2)
	if decision.ToChannelID != "" {
		desired[decision.ToChannelID] = true
	}
	if decision.FromChannelID != "" {
		desired[decision.FromChannelID] = false
	}
	return desired
}

func schedulingIntentSatisfied(preview contracts.ReconcilePlan, desired map[string]bool) bool {
	// PlanScheduling deliberately omits noops. An empty diff therefore means
	// every existing binding already matches the requested gateway state.
	return len(desired) > 0 && len(preview.Actions) == 0
}

func schedulingAction(preview contracts.ReconcilePlan, channelID string) (contracts.ReconcileActionType, bool) {
	for _, action := range preview.Actions {
		if action.ChannelID == channelID {
			return action.Type, true
		}
	}
	return "", false
}

func isObservationClaim(decision contracts.AutoSwitchDecision) bool {
	return decision.AutoApplied && decision.AppliedAt != nil && decision.ObserveUntil != nil
}

func (o *Orchestrator) completeAppliedDecisionRepair(
	ctx context.Context,
	plan contracts.RoutePlan,
	decision contracts.AutoSwitchDecision,
	desired map[string]bool,
	now time.Time,
) error {
	if err := o.renewApplyingLease(ctx, decision); err != nil {
		return err
	}
	applied, err := o.engine.ApplyScheduling(o.autoCtx(ctx), plan.ID, desired)
	if err != nil {
		return fmt.Errorf("repair expired decision %s: persist applied gateway state: %w", decision.ID, err)
	}
	decision.DryRunResult = applied
	decision.AutoApplied = true
	decision.AppliedAt = timePointer(now)
	if err := o.renewApplyingLease(ctx, decision); err != nil {
		return err
	}
	if err := o.ensureQualityCircuitOpen(ctx, plan, decision.FromChannelID, "expired_apply_repaired", "gateway switch completed before scheduler restart", now); err != nil {
		return err
	}
	if decision.ToChannelID == "" {
		_, err = o.finalizeNote(ctx, decision, contracts.AutoSwitchCompleted, contracts.RiskLevelL3,
			"repaired an expired fail-closed ejection", "gateway state was already applied; durable decision and circuit were repaired", nil)
		return err
	}
	observeUntil := now.Add(o.observationWindow(o.strategyFor(ctx, plan)))
	decision.ObserveUntil = &observeUntil
	_, err = o.finalizeNote(ctx, decision, contracts.AutoSwitchObserving, contracts.RiskLevelL1,
		"repaired an applied switch after scheduler restart", "gateway state was already applied; observation resumed", nil)
	return err
}

func (o *Orchestrator) abortPartialDecisionRepair(
	ctx context.Context,
	plan contracts.RoutePlan,
	decision contracts.AutoSwitchDecision,
	preview contracts.ReconcilePlan,
	now time.Time,
) error {
	_, sourceNeedsChange := schedulingAction(preview, decision.FromChannelID)
	sourceAlreadyDisabled := decision.FromChannelID != "" && !sourceNeedsChange && o.hasPublishedBinding(ctx, plan.ID, decision.FromChannelID)
	cleanup := make(map[string]bool, 2)
	if decision.ToChannelID != "" {
		cleanup[decision.ToChannelID] = false
	}
	if sourceAlreadyDisabled && decision.FromChannelID != "" {
		cleanup[decision.FromChannelID] = false
	}
	if len(cleanup) > 0 {
		if err := o.renewApplyingLease(ctx, decision); err != nil {
			return err
		}
		cleaned, err := o.engine.ApplyScheduling(o.autoCtx(ctx), plan.ID, cleanup)
		if err != nil {
			return fmt.Errorf("repair expired decision %s: drain partial gateway state: %w", decision.ID, err)
		}
		decision.DryRunResult = cleaned
	}
	if sourceAlreadyDisabled {
		decision.AutoApplied = true
		decision.AppliedAt = timePointer(now)
		if err := o.renewApplyingLease(ctx, decision); err != nil {
			return err
		}
		if err := o.ensureQualityCircuitOpen(ctx, plan, decision.FromChannelID, "expired_partial_apply", "source was disabled before scheduler restart", now); err != nil {
			return err
		}
	}
	_, err := o.finalizeNote(ctx, decision, contracts.AutoSwitchFailed, contracts.RiskLevelL3,
		"expired switch claim was not fully applied", "partial replacement state was drained; a fresh quality evaluation may retry", nil)
	return err
}

func (o *Orchestrator) hasPublishedBinding(ctx context.Context, planID, channelID string) bool {
	bindings, err := o.store.ListPublishedBindings(ctx, planID)
	if err != nil {
		return false
	}
	for _, binding := range bindings {
		if binding.ChannelID == channelID {
			return true
		}
	}
	return false
}

func (o *Orchestrator) repairExpiredObservationClaim(
	ctx context.Context,
	plan contracts.RoutePlan,
	decision contracts.AutoSwitchDecision,
	preview contracts.ReconcilePlan,
	now time.Time,
) error {
	desired := decisionSchedulingIntent(decision)
	if schedulingIntentSatisfied(preview, desired) {
		decision.Status = contracts.AutoSwitchObserving
		decision.LeaseUntil = nil
		if err := o.renewApplyingLease(ctx, decision); err != nil {
			return err
		}
		_, err := o.store.TransitionAutoSwitchDecision(ctx, decision, contracts.AutoSwitchApplying)
		if errors.Is(err, store.ErrConflict) {
			return nil
		}
		return err
	}

	cleanup := make(map[string]bool, 2)
	if decision.FromChannelID != "" {
		cleanup[decision.FromChannelID] = false
	}
	if decision.ToChannelID != "" {
		cleanup[decision.ToChannelID] = false
	}
	if err := o.renewApplyingLease(ctx, decision); err != nil {
		return err
	}
	cleaned, err := o.engine.ApplyScheduling(o.autoCtx(ctx), plan.ID, cleanup)
	if err != nil {
		return fmt.Errorf("repair expired observation %s: enforce fail-closed state: %w", decision.ID, err)
	}
	decision.DryRunResult = cleaned
	if err := o.renewApplyingLease(ctx, decision); err != nil {
		return err
	}
	if err := o.ensureQualityCircuitOpen(ctx, plan, decision.ToChannelID, "expired_observation_repaired", "replacement failed while observation claim was in progress", now); err != nil {
		return err
	}
	resolved := now
	decision.Status = contracts.AutoSwitchRolledBack
	decision.ResolvedAt = &resolved
	decision.LeaseUntil = nil
	decision.ObservationNote = "observation repair drained the replacement; the downstream remains fail-closed until a healthy source recovers"
	if err := o.renewApplyingLease(ctx, decision); err != nil {
		return err
	}
	saved, err := o.store.TransitionAutoSwitchDecision(ctx, decision, contracts.AutoSwitchApplying)
	if errors.Is(err, store.ErrConflict) {
		return nil
	}
	if err == nil {
		o.notifyDecision(ctx, saved, contracts.RiskLevelL3, "automatic switch observation repaired")
	}
	return err
}

func (o *Orchestrator) ensureQualityCircuitOpen(
	ctx context.Context,
	plan contracts.RoutePlan,
	channelID, code, text string,
	now time.Time,
) error {
	if channelID == "" {
		return nil
	}
	current, err := o.store.GetQualityCircuitRuntime(ctx, plan.ID, channelID)
	if err == nil && qualityCircuitBlocksScheduling(current.State) {
		return nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	evaluation := ejectionEvaluation(channelID, code, text, 0)
	if channel, channelErr := o.store.GetUpstreamChannel(ctx, channelID); channelErr == nil {
		snapshot := o.latestSnapshot(ctx, plan, channelID)
		evaluation = strategy.EvaluatePenalty(o.strategyFor(ctx, plan), strategy.Candidate{
			Channel: channel, Snapshot: snapshot, State: snapshot.HealthState,
		})
		evaluation.Eject = true
		evaluation.Reason = strategy.Reason{Code: code, Text: text}
	}
	_, err = o.openQualityCircuit(ctx, plan.ID, channelID, evaluation, now)
	return err
}
