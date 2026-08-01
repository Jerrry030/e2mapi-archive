package autoswitch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/strategy"
)

const (
	qualityProbeBatchSize = 100
	// Connector tasks may wait up to 30s and their completion/result poll can
	// reach that same bound. Keep the distributed claim longer than the complete
	// Core wait so a second Runner cannot issue the same physical attempt early.
	qualityProbeLease          = 75 * time.Second
	qualityHalfOpenProbePeriod = time.Minute
	qualityProbeRetryDelay     = 5 * time.Minute
)

// RecoverDueCircuits actively probes due open/half-open circuits. A successful
// probe is evidence, not permission by itself: the persisted FSM requires three
// strong samples before the binding is re-enabled. CAS claiming ensures that
// parallel runners cannot issue the same logical probe side effect.
func (o *Orchestrator) RecoverDueCircuits(ctx context.Context) error {
	if o == nil || o.store == nil {
		return nil
	}
	now := o.now().UTC()
	runtimes, err := o.store.ListQualityCircuitRuntimes(ctx, contracts.QualityCircuitRuntimeFilter{
		States: []contracts.QualityCircuitState{
			contracts.QualityCircuitOpen,
			contracts.QualityCircuitHalfOpen,
		},
		ProbeDueBefore: now,
		Limit:          qualityProbeBatchSize,
	})
	if err != nil {
		return err
	}
	var firstErr error
	for _, runtime := range runtimes {
		if err := o.recoverQualityCircuit(ctx, runtime, now); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// Probe completion only makes a binding eligible for guarded re-entry. The
	// source-wide coordinator admits a stable 10/25/50/100 percent cohort and
	// verifies fresh passive evidence between stages.
	if err := o.advanceRecoveryRollouts(ctx, now); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (o *Orchestrator) recoverQualityCircuit(ctx context.Context, runtime contracts.QualityCircuitRuntime, now time.Time) error {
	claimed, claimedOK, err := o.claimQualityProbe(ctx, runtime, now)
	if err != nil || !claimedOK {
		return err
	}
	// A persisted claim owns this logical attempt even if the runner is shutting
	// down. Connector task identity remains stable across its internal retries.
	workCtx := o.withSchedulerTime(context.WithoutCancel(ctx), now)
	if claimed.RestorePending {
		if strings.HasPrefix(claimed.LastReason.Code, "manual_recovery_") {
			return o.completePendingManualRecovery(workCtx, claimed, now)
		}
		return o.completePendingQualityRestore(workCtx, claimed, now)
	}
	plan, binding, channel, instance, model, err := o.qualityProbeScope(workCtx, claimed)
	if err != nil {
		return o.deferClaimedQualityProbe(workCtx, claimed, now, "probe_scope_unavailable", err.Error())
	}
	if o.prober == nil {
		return o.deferClaimedQualityProbe(workCtx, claimed, now, "probe_unsupported", "active quality probe is not configured")
	}
	if !connectorSupportsQualityProbe(workCtx, o.store, instance, channel.ProbeCapability, channel.ProbeEndpointPath) {
		return o.deferClaimedQualityProbe(workCtx, claimed, now, "probe_unsupported", "connector does not advertise active quality probes")
	}

	identity := fmt.Sprintf("quality-circuit/%s/%s/v%d", claimed.PlanID, claimed.ChannelID, claimed.Version)
	result, probeErr := o.prober.ProbeQuality(workCtx, instance.ID, contracts.ConnectorGatewayQualityProbeInput{
		AccountID:    binding.RemoteID,
		ChannelID:    channel.ID,
		Model:        model,
		Capability:   channel.ProbeCapability,
		EndpointPath: channel.ProbeEndpointPath,
	}, identity)
	if probeErr != nil {
		return o.deferClaimedQualityProbe(workCtx, claimed, now, "probe_execution_failed", probeErr.Error())
	}
	probeAt := result.ObservedAt.UTC()
	// Connector clocks can drift, and test/fake probes may omit a timestamp.
	// Circuit ordering uses the scheduler's monotonic attempt time unless the
	// measured timestamp is inside this claimed attempt.
	if probeAt.IsZero() || probeAt.Before(now) || probeAt.After(now.Add(qualityProbeLease)) {
		probeAt = now
	}
	if !validQualityProbeOutcome(result) {
		result.Success = false
		result.ErrorType = contracts.ErrorUnknown
	}
	if result.ErrorType == contracts.ErrorPlatform {
		return o.deferClaimedQualityProbe(workCtx, claimed, probeAt, "probe_platform_error", "active probe failed inside the downstream platform")
	}
	evaluation := qualityProbeEvaluation(o.strategyFor(workCtx, plan), channel, instance.ID, result)
	return o.advanceClaimedQualityProbe(workCtx, claimed, evaluation, probeAt)
}

func validQualityProbeOutcome(result contracts.ConnectorGatewayQualityProbeResult) bool {
	if result.Success {
		return result.Status >= 200 && result.Status < 300 && result.ErrorType == contracts.ErrorNone &&
			result.FirstTokenMS > 0 && result.TotalMS > 0 && result.FirstTokenMS <= result.TotalMS
	}
	return result.ErrorType != contracts.ErrorNone
}

func (o *Orchestrator) claimQualityProbe(ctx context.Context, runtime contracts.QualityCircuitRuntime, now time.Time) (contracts.QualityCircuitRuntime, bool, error) {
	if runtime.ProbeAfter == nil || runtime.ProbeAfter.After(now) || !qualityCircuitBlocksScheduling(runtime.State) {
		return runtime, false, nil
	}
	claim := runtime
	leaseUntil := now.Add(qualityProbeLease)
	claim.ProbeAfter = &leaseUntil
	if !runtime.RestorePending {
		claim.LastReason = contracts.QualityCircuitReason{Code: "probe_claimed", Text: "recovery probe claimed by scheduler"}
	}
	saved, err := o.store.UpsertQualityCircuitRuntime(ctx, claim, runtime.Version)
	if errors.Is(err, store.ErrConflict) {
		return contracts.QualityCircuitRuntime{}, false, nil
	}
	return saved, err == nil, err
}

func (o *Orchestrator) qualityProbeScope(
	ctx context.Context,
	runtime contracts.QualityCircuitRuntime,
) (contracts.RoutePlan, contracts.PublishedBinding, contracts.UpstreamChannel, contracts.Instance, string, error) {
	plan, err := o.store.GetRoutePlan(ctx, runtime.PlanID)
	if err != nil {
		return contracts.RoutePlan{}, contracts.PublishedBinding{}, contracts.UpstreamChannel{}, contracts.Instance{}, "", err
	}
	if plan.Status != contracts.RoutePlanPublished {
		return contracts.RoutePlan{}, contracts.PublishedBinding{}, contracts.UpstreamChannel{}, contracts.Instance{}, "", fmt.Errorf("route plan is not published")
	}
	bindings, err := o.store.ListPublishedBindings(ctx, plan.ID)
	if err != nil {
		return contracts.RoutePlan{}, contracts.PublishedBinding{}, contracts.UpstreamChannel{}, contracts.Instance{}, "", err
	}
	var binding contracts.PublishedBinding
	for _, candidate := range bindings {
		if candidate.ChannelID == runtime.ChannelID {
			binding = candidate
			break
		}
	}
	if binding.ChannelID == "" || binding.State != contracts.BindingDisabled || strings.TrimSpace(binding.RemoteID) == "" {
		return contracts.RoutePlan{}, contracts.PublishedBinding{}, contracts.UpstreamChannel{}, contracts.Instance{}, "", fmt.Errorf("quality-isolated binding is unavailable")
	}
	channel, err := o.store.GetUpstreamChannel(ctx, runtime.ChannelID)
	if err != nil {
		return contracts.RoutePlan{}, contracts.PublishedBinding{}, contracts.UpstreamChannel{}, contracts.Instance{}, "", err
	}
	instance, err := o.store.GetInstance(ctx, plan.InstanceID)
	if err != nil {
		return contracts.RoutePlan{}, contracts.PublishedBinding{}, contracts.UpstreamChannel{}, contracts.Instance{}, "", err
	}
	model := firstProbeModel(channel.Models, nil)
	if pool, poolErr := o.store.GetUpstreamPool(ctx, plan.PoolID); poolErr == nil {
		model = firstProbeModel(channel.Models, pool.Models)
	}
	if model == "" {
		return contracts.RoutePlan{}, contracts.PublishedBinding{}, contracts.UpstreamChannel{}, contracts.Instance{}, "", fmt.Errorf("channel has no probe model")
	}
	return plan, binding, channel, instance, model, nil
}

func (o *Orchestrator) completePendingQualityRestore(ctx context.Context, runtime contracts.QualityCircuitRuntime, now time.Time) error {
	bindings, err := o.store.ListPublishedBindings(ctx, runtime.PlanID)
	if err != nil {
		return o.deferClaimedQualityProbe(ctx, runtime, now, "restore_scope_unavailable", err.Error())
	}
	found := false
	active := false
	for _, binding := range bindings {
		if binding.ChannelID == runtime.ChannelID {
			found = true
			active = binding.State == contracts.BindingActive
			break
		}
	}
	if !found {
		return o.deferClaimedQualityProbe(ctx, runtime, now, "restore_scope_unavailable", "published binding is unavailable")
	}
	if !active {
		if _, err := o.engine.ApplyScheduling(o.autoCtx(ctx), runtime.PlanID, map[string]bool{runtime.ChannelID: true}); err != nil {
			return o.deferClaimedQualityProbe(ctx, runtime, now, "restore_apply_failed", err.Error())
		}
	}
	return o.closePendingQualityRestore(ctx, runtime, now)
}

func firstProbeModel(channelModels, poolModels []string) string {
	for _, models := range [][]string{channelModels, poolModels} {
		for _, model := range models {
			if model = strings.TrimSpace(model); model != "" && contracts.IsConnectorQualityProbeField(model) {
				return model
			}
		}
	}
	return ""
}

func connectorSupportsQualityProbe(ctx context.Context, st store.Store, instance contracts.Instance, probeCapability contracts.QualityProbeCapability, endpointPath string) bool {
	if !contracts.IsQualityProbeCapability(probeCapability) || !contracts.IsQualityProbeEndpointPath(endpointPath) {
		return false
	}
	if strings.TrimSpace(instance.ConnectorID) == "" {
		return false
	}
	connector, err := st.GetConnector(ctx, instance.ConnectorID)
	if err != nil || connector.Status != contracts.ConnectorStatusOnline || connector.Gateway.GatewayStatus != "ok" ||
		connector.LastSeenAt == nil || connector.LastSeenAt.Before(schedulerTime(ctx).Add(-time.Minute)) {
		return false
	}
	taskSupported := false
	for _, taskCapability := range connector.Gateway.Capabilities {
		if taskCapability == contracts.ConnectorTaskGatewayQualityProbe {
			taskSupported = true
			break
		}
	}
	probe := connector.Gateway.QualityProbe
	if !taskSupported || probe == nil || !probe.Enabled || probe.RecoveryMode != contracts.QualityProbeRecoveryAutomatic ||
		!probe.FirstTokenMS || !probe.TotalMS {
		return false
	}
	capabilitySupported := false
	for _, capability := range probe.Capabilities {
		if capability == probeCapability {
			capabilitySupported = true
			break
		}
	}
	if !capabilitySupported {
		return false
	}
	for _, candidate := range probe.EndpointPaths {
		if candidate == endpointPath {
			return true
		}
	}
	return false
}

func qualityProbeEvaluation(
	strat contracts.RouteStrategy,
	channel contracts.UpstreamChannel,
	instanceID string,
	result contracts.ConnectorGatewayQualityProbeResult,
) strategy.PenaltyEvaluation {
	snapshot := contracts.ChannelHealthSnapshot{
		ChannelID:                channel.ID,
		InstanceID:               instanceID,
		Capability:               result.Capability,
		EndpointPath:             result.EndpointPath,
		Window:                   contracts.Window1m,
		SampleCount:              probeEvidenceSamples(strat),
		SuccessRate:              boolFloat(result.Success),
		ErrorRate:                boolFloat(!result.Success),
		QualitySampleCount:       probeEvidenceSamples(strat),
		QualitySuccessRate:       boolFloat(result.Success),
		QualityErrorRate:         boolFloat(!result.Success),
		UpstreamErrorRate:        boolFloat(!result.Success && upstreamResponsibleError(result.ErrorType)),
		TTFTP95:                  result.FirstTokenMS,
		DurationP95:              result.TotalMS,
		HealthState:              contracts.HealthHealthy,
		AuthFailureCount:         boolInt(result.ErrorType == contracts.ErrorAuth),
		InsufficientBalanceCount: boolInt(result.ErrorType == contracts.ErrorInsufficientBalance),
	}
	if !result.Success {
		snapshot.HealthState = contracts.HealthUnhealthy
	}
	eval := strategy.EvaluatePenalty(strat, strategy.Candidate{Channel: channel, Snapshot: snapshot, State: snapshot.HealthState})
	if !result.Success {
		eval.Eject = true
		eval.Score = 0
		eval.Reason = strategy.Reason{Code: "recovery_probe_failed", Text: "active recovery probe failed"}
	}
	return eval
}

func probeEvidenceSamples(strat contracts.RouteStrategy) int {
	if strat.Thresholds.MinSamples > 5 {
		return strat.Thresholds.MinSamples
	}
	return 5
}

func (o *Orchestrator) advanceClaimedQualityProbe(
	ctx context.Context,
	claimed contracts.QualityCircuitRuntime,
	evaluation strategy.PenaltyEvaluation,
	probeAt time.Time,
) error {
	current, err := o.store.GetQualityCircuitRuntime(ctx, claimed.PlanID, claimed.ChannelID)
	if err != nil {
		return err
	}
	if current.Version != claimed.Version || !qualityCircuitBlocksScheduling(current.State) {
		return nil
	}
	transition := strategy.AdvanceQualityCircuit(
		qualityCircuitForProbe(current, probeAt),
		strategy.QualityCircuitEvent{Kind: strategy.CircuitRecoveryProbe, Now: probeAt, Evaluation: evaluation},
		strategy.DefaultQualityCircuitPolicy(),
	)
	next := qualityCircuitRuntimeFromState(current.PlanID, current.ChannelID, current, transition.Circuit)
	next.LastProbeAt = timePointer(probeAt)
	if transition.Action == strategy.CircuitRestore {
		// Three probes prove readiness, not permission for an all-at-once restore.
		// Keep normal traffic blocked until the source-wide rollout coordinator
		// selects this stable downstream cohort.
		current.State = contracts.QualityCircuitHalfOpen
		current.LastProbeAt = timePointer(probeAt)
		current.LastTransitionAt = timePointer(probeAt)
		current.ConsecutiveProbeSuccesses = transition.Circuit.ConsecutiveProbeSuccesses
		current.LastScore = transition.Circuit.LastScore
		current.RestorePending = false
		current.RecoveryReady = true
		current.RecoveryStage = 0
		current.RecoveryStageStartedAt = nil
		current.RecoveryObserveAfter = nil
		current.LastReason = contracts.QualityCircuitReason{Code: "recovery_ready", Text: "active probes passed; waiting for guarded traffic rollout"}
		current.ProbeAfter = nil
		saved, saveErr := o.store.UpsertQualityCircuitRuntime(ctx, current, current.Version)
		if errors.Is(saveErr, store.ErrConflict) {
			return nil
		}
		if saveErr == nil {
			o.emitRecoveryTransition(ctx, saved)
		}
		return saveErr
	} else if next.State == contracts.QualityCircuitHalfOpen {
		due := probeAt.Add(qualityHalfOpenProbePeriod)
		next.ProbeAfter = &due
	}
	_, err = o.store.UpsertQualityCircuitRuntime(ctx, next, current.Version)
	if errors.Is(err, store.ErrConflict) {
		return nil
	}
	return err
}

func (o *Orchestrator) closePendingQualityRestore(ctx context.Context, runtime contracts.QualityCircuitRuntime, now time.Time) error {
	current, err := o.store.GetQualityCircuitRuntime(ctx, runtime.PlanID, runtime.ChannelID)
	if err != nil {
		return err
	}
	if current.Version != runtime.Version || !current.RestorePending {
		return nil
	}
	closed := current
	closed.State = contracts.QualityCircuitClosed
	closed.OpenedAt = nil
	closed.ProbeAfter = nil
	closed.HalfOpenSince = nil
	closed.LastTransitionAt = timePointer(now)
	closed.OpenCount = 0
	closed.ConsecutiveProbeSuccesses = 3
	closed.RestorePending = false
	closed.RecoveryReady = true
	if closed.RecoveryStage == 0 {
		closed.RecoveryStage = 10
	}
	if closed.RecoveryStageStartedAt == nil {
		closed.RecoveryStageStartedAt = timePointer(now)
	}
	if closed.RecoveryObserveAfter == nil {
		closed.RecoveryObserveAfter = timePointer(now.Add(recoveryRolloutObservationWindow))
	}
	closed.LastReason = contracts.QualityCircuitReason{
		Code: "recovery_canary_admitted",
		Text: fmt.Sprintf("active probes passed; admitted to %d%% recovery stage", closed.RecoveryStage),
	}
	saved, err := o.store.UpsertQualityCircuitRuntime(ctx, closed, current.Version)
	if errors.Is(err, store.ErrConflict) {
		return nil
	}
	if err == nil {
		o.emitRecoveryTransition(ctx, saved)
	}
	return err
}

func qualityCircuitForProbe(runtime contracts.QualityCircuitRuntime, probeAt time.Time) strategy.QualityCircuit {
	circuit := qualityCircuitFromRuntime(runtime.PlanID, runtime.ChannelID, runtime)
	// ProbeAfter was moved forward as the distributed lease. The claim itself is
	// proof that the pre-claim due time elapsed, so the pure FSM must evaluate the
	// claimed result now rather than treating its own lease as cooldown.
	if circuit.State == strategy.CircuitOpen && circuit.ProbeAfter.After(probeAt) {
		circuit.ProbeAfter = probeAt
	}
	return circuit
}

func (o *Orchestrator) deferClaimedQualityProbe(
	ctx context.Context,
	claimed contracts.QualityCircuitRuntime,
	now time.Time,
	code, text string,
) error {
	current, err := o.store.GetQualityCircuitRuntime(ctx, claimed.PlanID, claimed.ChannelID)
	if err != nil {
		return err
	}
	if current.Version != claimed.Version || !qualityCircuitBlocksScheduling(current.State) {
		return nil
	}
	// Connector/task/scope failures are failed recovery attempts, not a pause.
	// Reopen through the same pure FSM so a prior success streak is broken and
	// OpenCount drives capped exponential backoff with stable jitter.
	evaluation := strategy.PenaltyEvaluation{
		ChannelID: current.ChannelID,
		Score:     0,
		Evidence:  1,
		Eject:     true,
		Reason:    strategy.Reason{Code: code, Text: text},
	}
	transition := strategy.AdvanceQualityCircuit(
		qualityCircuitForProbe(current, now),
		strategy.QualityCircuitEvent{Kind: strategy.CircuitRecoveryProbe, Now: now, Evaluation: evaluation},
		strategy.DefaultQualityCircuitPolicy(),
	)
	next := qualityCircuitRuntimeFromState(current.PlanID, current.ChannelID, current, transition.Circuit)
	// Infrastructure failures affect recovery confidence and retry timing, but
	// are not upstream quality samples. Preserve the last factual source score.
	next.LastScore = current.LastScore
	// Keep restore_pending across a failed restore so the next due run retries
	// the idempotent enable instead of issuing a fourth quality probe.
	next.RestorePending = current.RestorePending
	next.LastReason = contracts.QualityCircuitReason{Code: code, Text: text}
	_, err = o.store.UpsertQualityCircuitRuntime(ctx, next, current.Version)
	if errors.Is(err, store.ErrConflict) {
		return nil
	}
	return err
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func upstreamResponsibleError(kind contracts.ObservationErrorType) bool {
	return kind != contracts.ErrorNone && kind != contracts.ErrorClient && kind != contracts.ErrorCanceled && kind != contracts.ErrorPlatform
}
