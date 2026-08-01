package autoswitch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// Approve records the operator's acceptance of the exact proposed intent. It
// deliberately performs no gateway write; Execute obtains the fenced lease.
func (o *Orchestrator) Approve(ctx context.Context, decisionID, note string) (*contracts.AutoSwitchDecision, error) {
	decision, err := o.store.GetAutoSwitchDecision(ctx, strings.TrimSpace(decisionID))
	if err != nil {
		return nil, err
	}
	if decision.Status != contracts.AutoSwitchProposed {
		return nil, fmt.Errorf("auto-switch decision is %s, not proposed: %w", decision.Status, store.ErrConflict)
	}
	decision.Status = contracts.AutoSwitchApproved
	decision.ObservationNote = operatorDecisionNote("approved", note)
	saved, err := o.store.TransitionAutoSwitchDecision(ctx, decision, contracts.AutoSwitchProposed)
	if err != nil {
		return nil, err
	}
	o.notifyDecision(ctx, saved, contracts.RiskLevelL2, "自动切换已批准")
	return &saved, nil
}

// Reject closes a proposed intent without changing gateway state. Once an
// intent is approved, it must either be executed or expire through the normal
// decision lifecycle; reject cannot silently revoke a separate approval step.
func (o *Orchestrator) Reject(ctx context.Context, decisionID, note string) (*contracts.AutoSwitchDecision, error) {
	decision, err := o.store.GetAutoSwitchDecision(ctx, strings.TrimSpace(decisionID))
	if err != nil {
		return nil, err
	}
	if decision.Status != contracts.AutoSwitchProposed {
		return nil, fmt.Errorf("auto-switch decision is %s, not proposed: %w", decision.Status, store.ErrConflict)
	}
	now := o.now().UTC()
	decision.Status = contracts.AutoSwitchRejected
	decision.ResolvedAt = timePointer(now)
	decision.ObservationNote = operatorDecisionNote("rejected", note)
	saved, err := o.store.TransitionAutoSwitchDecision(ctx, decision, contracts.AutoSwitchProposed)
	if err != nil {
		return nil, err
	}
	o.notifyDecision(ctx, saved, contracts.RiskLevelL2, "自动切换已拒绝")
	return &saved, nil
}

// Execute applies an approved decision behind a plan-generation fence and a
// bounded lease. It re-plans immediately before applying so approval never
// authorizes a stale or broadened action set.
func (o *Orchestrator) Execute(ctx context.Context, decisionID string) (*contracts.AutoSwitchDecision, error) {
	claimed, ownsClaim, err := o.store.ClaimApprovedAutoSwitchDecision(ctx, strings.TrimSpace(decisionID), autoSwitchApplyingLease)
	if err != nil {
		return nil, err
	}
	if !ownsClaim {
		return nil, fmt.Errorf("auto-switch decision is %s, not approved: %w", claimed.Status, store.ErrConflict)
	}
	workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), autoSwitchApplyingWorkTimeout)
	defer cancel()
	workCtx = o.withApplyingSideEffectGuard(workCtx, claimed)
	intent := map[string]bool{claimed.FromChannelID: false}
	if claimed.ToChannelID != "" {
		intent[claimed.ToChannelID] = true
	}
	preview, err := o.engine.PlanScheduling(o.autoCtx(workCtx), claimed.PlanID, intent)
	if err != nil {
		return o.finalize(workCtx, claimed, contracts.AutoSwitchFailed, contracts.RiskLevelL3,
			"approved decision preflight failed: "+err.Error(), nil)
	}
	if !sameSchedulingActions(claimed.DryRunResult.Actions, preview.Actions) {
		return o.finalize(workCtx, claimed, contracts.AutoSwitchFailed, contracts.RiskLevelL3,
			"approved decision is stale; current scheduling preview differs", nil)
	}
	applied, applyErr := o.engine.ApplyScheduling(o.autoCtx(workCtx), claimed.PlanID, intent)
	claimed.DryRunResult = applied
	if applyErr != nil {
		note := ""
		if claimed.ToChannelID != "" {
			if _, drainErr := o.engine.ApplyScheduling(o.autoCtx(workCtx), claimed.PlanID, map[string]bool{claimed.ToChannelID: false}); drainErr != nil {
				note = "; replacement cleanup failed: " + drainErr.Error()
			}
		}
		if o.bindingDisabled(workCtx, claimed.PlanID, claimed.FromChannelID) {
			claimed.AutoApplied = true
			now := o.now().UTC()
			claimed.AppliedAt = timePointer(now)
			if leaseErr := o.renewApplyingLease(workCtx, claimed); leaseErr != nil {
				note += "; applying lease lost before source circuit: " + leaseErr.Error()
			} else if _, circuitErr := o.openQualityCircuit(workCtx, claimed.PlanID, claimed.FromChannelID,
				ejectionEvaluation(claimed.FromChannelID, "operator_apply_failed", "operator-approved apply partially isolated source", 0), now); circuitErr != nil {
				note += "; source circuit persistence failed: " + circuitErr.Error()
			}
		}
		return o.finalizeNote(workCtx, claimed, contracts.AutoSwitchFailed, contracts.RiskLevelL3,
			"approved decision apply failed: "+applyErr.Error(), strings.TrimPrefix(note, "; "), nil)
	}
	now := o.now().UTC()
	claimed.AutoApplied = true
	claimed.AppliedAt = timePointer(now)
	plan, err := o.store.GetRoutePlan(workCtx, claimed.PlanID)
	if err != nil {
		return nil, err
	}
	claimed.ObserveUntil = timePointer(now.Add(o.observationWindow(o.strategyFor(workCtx, plan))))
	if err := o.renewApplyingLease(workCtx, claimed); err != nil {
		return nil, err
	}
	if _, err := o.openQualityCircuit(workCtx, claimed.PlanID, claimed.FromChannelID,
		ejectionEvaluation(claimed.FromChannelID, "operator_approved", "operator-approved quality isolation", 0), now); err != nil {
		return o.finalize(workCtx, claimed, contracts.AutoSwitchFailed, contracts.RiskLevelL3,
			"approved switch succeeded but circuit persistence failed: "+err.Error(), nil)
	}
	return o.finalize(workCtx, claimed, contracts.AutoSwitchObserving, claimed.RiskLevel,
		"operator-approved switch applied and entered observation", nil)
}

// ManualRecover re-enables one quality-isolated binding without manufacturing
// probe evidence. The circuit CAS persists restore_pending before the remote
// side effect and completePendingQualityRestore repairs an interrupted apply.
func (o *Orchestrator) ManualRecover(ctx context.Context, planID, channelID, note string) (contracts.QualityCircuitRuntime, error) {
	planID, channelID = strings.TrimSpace(planID), strings.TrimSpace(channelID)
	current, err := o.store.GetQualityCircuitRuntime(ctx, planID, channelID)
	if err != nil {
		return contracts.QualityCircuitRuntime{}, err
	}
	if current.State == contracts.QualityCircuitClosed && !current.RestorePending {
		return current, nil
	}
	if current.RestorePending {
		if strings.HasPrefix(current.LastReason.Code, "manual_recovery_") {
			// A prior caller owns the immediate attempt. The persisted pending
			// marker lets the recovery runner repair it after its lease deadline;
			// repeated API requests are side-effect free and must not be reported
			// as a completed recovery.
			return contracts.QualityCircuitRuntime{}, fmt.Errorf("manual recovery is pending retry: %w", store.ErrConflict)
		}
		return contracts.QualityCircuitRuntime{}, fmt.Errorf("automatic recovery restore is already pending: %w", store.ErrConflict)
	}
	if !qualityCircuitBlocksScheduling(current.State) {
		return contracts.QualityCircuitRuntime{}, fmt.Errorf("quality circuit is not isolated: %w", store.ErrConflict)
	}
	bindings, err := o.store.ListPublishedBindings(ctx, planID)
	if err != nil {
		return contracts.QualityCircuitRuntime{}, err
	}
	found := false
	for _, binding := range bindings {
		if binding.ChannelID == channelID && strings.TrimSpace(binding.RemoteID) != "" && binding.State == contracts.BindingDisabled {
			found = true
			break
		}
	}
	if !found {
		return contracts.QualityCircuitRuntime{}, fmt.Errorf("quality-isolated published binding is unavailable: %w", store.ErrConflict)
	}
	now := o.now().UTC()
	current.RestorePending = true
	current.RecoveryReady = false
	current.RecoveryStage = 0
	current.RecoveryStageStartedAt = nil
	current.RecoveryObserveAfter = nil
	current.ProbeAfter = timePointer(now.Add(qualityProbeLease))
	current.LastReason = contracts.QualityCircuitReason{Code: "manual_recovery_pending", Text: operatorDecisionNote("manual recovery", note)}
	pending, err := o.store.UpsertQualityCircuitRuntime(ctx, current, current.Version)
	if errors.Is(err, store.ErrConflict) {
		latest, getErr := o.store.GetQualityCircuitRuntime(ctx, planID, channelID)
		return latest, getErr
	}
	if err != nil {
		return contracts.QualityCircuitRuntime{}, err
	}
	workCtx := o.withSchedulerTime(context.WithoutCancel(ctx), now)
	if err := o.completePendingManualRecovery(workCtx, pending, now); err != nil {
		return contracts.QualityCircuitRuntime{}, err
	}
	return o.store.GetQualityCircuitRuntime(ctx, planID, channelID)
}

func operatorDecisionNote(action, note string) string {
	note = strings.TrimSpace(note)
	if note == "" {
		return action + " by platform operator"
	}
	runes := []rune(note)
	if len(runes) > 500 {
		note = string(runes[:500])
	}
	return action + " by platform operator: " + note
}

func sameSchedulingActions(expected, actual []contracts.ReconcileAction) bool {
	if len(expected) != len(actual) {
		return false
	}
	counts := make(map[string]int, len(expected))
	for _, action := range expected {
		counts[string(action.Type)+"\x00"+action.ChannelID]++
	}
	for _, action := range actual {
		key := string(action.Type) + "\x00" + action.ChannelID
		if counts[key] == 0 {
			return false
		}
		counts[key]--
	}
	return true
}
