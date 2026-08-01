package autoswitch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/strategy"
)

func (o *Orchestrator) emitRecoveryTransition(ctx context.Context, runtime contracts.QualityCircuitRuntime) {
	plan, err := o.store.GetRoutePlan(ctx, runtime.PlanID)
	if err != nil {
		return
	}
	level, result, title := recoveryTransitionPresentation(runtime)
	_, _ = o.store.AppendAudit(ctx, contracts.OperationAudit{
		UserID: plan.UserID, InstanceID: plan.InstanceID,
		ActorType: "workflow", ActorID: "quality-recovery",
		Action: "quality_circuit.recovery_transition", RiskLevel: contracts.RiskLevelL1,
		EventLevel: level, TargetType: "quality_circuit",
		TargetID: runtime.PlanID + "/" + runtime.ChannelID, Result: result,
		Details: map[string]string{
			"reason_code":    runtime.LastReason.Code,
			"recovery_stage": recoveryStageText(runtime.RecoveryStage),
		},
	})
	o.notifyRecovery(ctx, runtime, title, level, result)
}

func recoveryTransitionPresentation(runtime contracts.QualityCircuitRuntime) (contracts.EventLevel, string, string) {
	switch runtime.LastReason.Code {
	case "recovery_regressed":
		return contracts.EventLevelWarning, "failed", "线路恢复回退"
	case strategy.CircuitReasonRestored:
		return contracts.EventLevelNotice, "accepted", "线路恢复完成"
	case "manual_recovery_completed":
		return contracts.EventLevelNotice, "accepted", "人工恢复完成"
	case "recovery_ready":
		return contracts.EventLevelInfo, "running", "线路恢复探测通过"
	default:
		return contracts.EventLevelInfo, "running", "线路恢复进度更新"
	}
}

// completePendingManualRecovery performs the operator-authorized enable, then
// closes the circuit without inventing automatic-probe or rollout evidence.
// RestorePending is written before the remote effect; RecoverDueCircuits uses
// the same marker to retry after an interrupted process.
func (o *Orchestrator) completePendingManualRecovery(
	ctx context.Context,
	runtime contracts.QualityCircuitRuntime,
	now time.Time,
) error {
	if !runtime.RestorePending || !strings.HasPrefix(runtime.LastReason.Code, "manual_recovery_") {
		return store.ErrConflict
	}
	bindings, err := o.store.ListPublishedBindings(ctx, runtime.PlanID)
	if err != nil {
		return o.deferPendingManualRecovery(ctx, runtime, now, "manual_recovery_scope_unavailable", err.Error(),
			fmt.Errorf("manual recovery scope unavailable: %w", err))
	}
	found, active := false, false
	for _, binding := range bindings {
		if binding.ChannelID == runtime.ChannelID {
			found = true
			active = binding.State == contracts.BindingActive
			break
		}
	}
	if !found {
		const text = "published binding is unavailable"
		return o.deferPendingManualRecovery(ctx, runtime, now, "manual_recovery_scope_unavailable", text,
			errors.New("manual recovery scope unavailable: "+text))
	}
	if !active {
		if _, err := o.engine.ApplyScheduling(o.manualRecoveryCtx(ctx), runtime.PlanID, map[string]bool{runtime.ChannelID: true}); err != nil {
			return o.deferPendingManualRecovery(ctx, runtime, now, "manual_recovery_apply_failed", err.Error(),
				fmt.Errorf("manual recovery gateway apply failed: %w", err))
		}
	}
	current, err := o.store.GetQualityCircuitRuntime(ctx, runtime.PlanID, runtime.ChannelID)
	if err != nil {
		return err
	}
	if current.Version != runtime.Version || !current.RestorePending ||
		!strings.HasPrefix(current.LastReason.Code, "manual_recovery_") {
		return nil
	}
	closed := current
	closed.State = contracts.QualityCircuitClosed
	closed.OpenedAt = nil
	closed.ProbeAfter = nil
	closed.HalfOpenSince = nil
	closed.LastTransitionAt = timePointer(now)
	closed.OpenCount = 0
	closed.ConsecutiveProbeSuccesses = 0
	closed.RestorePending = false
	closed.RecoveryReady = false
	closed.RecoveryStage = 0
	closed.RecoveryStageStartedAt = nil
	closed.RecoveryObserveAfter = nil
	closed.LastReason = contracts.QualityCircuitReason{
		Code: "manual_recovery_completed",
		Text: "operator restored the isolated binding; passive monitoring remains active",
	}
	saved, err := o.store.UpsertQualityCircuitRuntime(ctx, closed, current.Version)
	if errors.Is(err, store.ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	o.emitRecoveryTransition(ctx, saved)
	return nil
}

func (o *Orchestrator) deferPendingManualRecovery(
	ctx context.Context,
	runtime contracts.QualityCircuitRuntime,
	now time.Time,
	code, text string,
	cause error,
) error {
	if cause == nil {
		cause = errors.New(text)
	}
	current, err := o.store.GetQualityCircuitRuntime(ctx, runtime.PlanID, runtime.ChannelID)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("load manual recovery retry state: %w", err))
	}
	if current.Version != runtime.Version || !current.RestorePending ||
		!strings.HasPrefix(current.LastReason.Code, "manual_recovery_") {
		return cause
	}
	current.ProbeAfter = timePointer(now.Add(qualityProbeRetryDelay))
	current.LastReason = contracts.QualityCircuitReason{Code: code, Text: text}
	_, saveErr := o.store.UpsertQualityCircuitRuntime(ctx, current, current.Version)
	if errors.Is(saveErr, store.ErrConflict) {
		return cause
	}
	if saveErr != nil {
		return errors.Join(cause, fmt.Errorf("persist manual recovery retry state: %w", saveErr))
	}
	// Persisting retry intent does not mean the operator-authorized remote
	// enable succeeded. Return the original failure so HTTP callers cannot
	// mistake a durable retry marker for a completed recovery.
	return cause
}

func (o *Orchestrator) manualRecoveryCtx(ctx context.Context) context.Context {
	ctx = contracts.WithReconcileTrigger(ctx, contracts.ReconcileTriggerManual)
	return contracts.WithActor(ctx, contracts.Actor{Type: "workflow", ID: "manual-recovery"})
}

func (o *Orchestrator) notifyRecovery(
	ctx context.Context,
	runtime contracts.QualityCircuitRuntime,
	title string,
	level contracts.EventLevel,
	result string,
) {
	plan, err := o.store.GetRoutePlan(ctx, runtime.PlanID)
	if err != nil {
		return
	}
	if o.recoveryEventSink != nil {
		o.recoveryEventSink(ctx, RecoveryEvent{
			UserID: plan.UserID, InstanceID: plan.InstanceID,
			Status: runtime.LastReason.Code, Stage: runtime.RecoveryStage,
		})
	}
	if o.notifier == nil {
		return
	}
	routes, err := o.store.ListNotificationRoutes(ctx, plan.UserID)
	if err != nil || len(routes) == 0 {
		return
	}
	o.notifier.DispatchAll(ctx, notify.Event{
		UserID: plan.UserID, InstanceID: plan.InstanceID,
		EventLevel: level, RiskLevel: contracts.RiskLevelL1, Result: result,
		Title: title, Text: recoveryOwnerText(runtime),
		Fields: map[string]string{
			"status": string(runtime.State), "stage": recoveryStageText(runtime.RecoveryStage),
		},
	}, routes)
}

func recoveryOwnerText(runtime contracts.QualityCircuitRuntime) string {
	switch runtime.LastReason.Code {
	case "manual_recovery_completed":
		return "已按管理员确认恢复一条隔离线路，并继续观察真实请求质量。"
	case "recovery_stage_expanded", "recovery_canary_admitted":
		return "线路已进入 " + recoveryStageText(runtime.RecoveryStage) + " 灰度回归观察。"
	case strategy.CircuitReasonRestored:
		return "线路已完成分阶段恢复，当前回归已结束。"
	case "recovery_regressed":
		return "灰度恢复期间质量再次下降，系统已重新隔离该线路。"
	default:
		return "线路恢复状态已更新。"
	}
}

func recoveryStageText(stage int) string {
	if stage <= 0 {
		return "待观察"
	}
	return fmt.Sprintf("%d%%", stage)
}
