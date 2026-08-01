package autoswitch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/healthmetrics"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/strategy"
)

const recoveryRolloutObservationWindow = 5 * time.Minute

var recoveryRolloutStages = [...]int{10, 25, 50, 100}

type recoveryRolloutMember struct {
	runtime contracts.QualityCircuitRuntime
	plan    contracts.RoutePlan
	channel contracts.UpstreamChannel
	binding contracts.PublishedBinding
}

// advanceRecoveryRollouts coordinates guarded re-entry across downstreams that
// use the same stable source. It deliberately does not emulate percentage
// traffic inside a gateway: every admitted member remains a normal boolean
// account binding, while the stable cohort bounds cross-customer blast radius.
func (o *Orchestrator) advanceRecoveryRollouts(ctx context.Context, now time.Time) error {
	membersBySource, err := o.recoveryRolloutMembers(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for sourceID, members := range membersBySource {
		if err := o.advanceSourceRecoveryRollout(ctx, sourceID, members, now); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (o *Orchestrator) recoveryRolloutMembers(ctx context.Context) (map[string][]recoveryRolloutMember, error) {
	runtimes, err := o.store.ListQualityCircuitRuntimes(ctx, contracts.QualityCircuitRuntimeFilter{})
	if err != nil {
		return nil, err
	}
	result := make(map[string][]recoveryRolloutMember)
	for _, runtime := range runtimes {
		if !runtime.RecoveryReady {
			continue
		}
		plan, err := o.store.GetRoutePlan(ctx, runtime.PlanID)
		if err != nil || plan.Status != contracts.RoutePlanPublished {
			continue
		}
		channel, err := o.store.GetUpstreamChannel(ctx, runtime.ChannelID)
		if err != nil || strings.TrimSpace(channel.SourceIdentity()) == "" {
			continue
		}
		bindings, err := o.store.ListPublishedBindings(ctx, runtime.PlanID)
		if err != nil {
			return nil, err
		}
		var binding contracts.PublishedBinding
		for _, candidate := range bindings {
			if candidate.ChannelID == runtime.ChannelID {
				binding = candidate
				break
			}
		}
		if binding.ChannelID == "" || strings.TrimSpace(binding.RemoteID) == "" {
			continue
		}
		sourceID := channel.SourceIdentity()
		result[sourceID] = append(result[sourceID], recoveryRolloutMember{
			runtime: runtime, plan: plan, channel: channel, binding: binding,
		})
	}
	return result, nil
}

func (o *Orchestrator) advanceSourceRecoveryRollout(
	ctx context.Context,
	sourceID string,
	members []recoveryRolloutMember,
	now time.Time,
) error {
	if len(members) == 0 {
		return nil
	}
	// A newly-ready downstream joins the current source stage. The stage itself
	// is monotonic and follows the highest durable value already observed.
	currentStage := 0
	for _, member := range members {
		if member.runtime.RecoveryStage > currentStage {
			currentStage = member.runtime.RecoveryStage
		}
	}
	if currentStage == 0 {
		currentStage = recoveryRolloutStages[0]
	} else {
		ready, regressed, err := o.recoveryStageVerdict(ctx, members, currentStage, now)
		if err != nil {
			return err
		}
		if regressed {
			return o.reopenRecoveryRollout(ctx, sourceID, members, now)
		}
		if ready {
			if currentStage == 100 {
				return o.completeRecoveryRollout(ctx, members, now)
			}
			currentStage = nextRecoveryRolloutStage(currentStage)
		}
	}

	planIDs := make([]string, 0, len(members))
	alreadyAdmitted := make([]string, 0, len(members))
	memberByPlan := make(map[string]recoveryRolloutMember, len(members))
	for _, member := range members {
		planIDs = append(planIDs, member.plan.ID)
		memberByPlan[member.plan.ID] = member
		if member.binding.State == contracts.BindingActive && member.runtime.State == contracts.QualityCircuitClosed {
			alreadyAdmitted = append(alreadyAdmitted, member.plan.ID)
		}
	}
	selected := strategy.StableRecoveryCohortPlanIDs(planIDs, alreadyAdmitted, sourceID, currentStage)
	selectedIDs := make([]string, 0, len(selected))
	for planID, chosen := range selected {
		if chosen {
			selectedIDs = append(selectedIDs, planID)
		}
	}
	sort.Strings(selectedIDs)
	for _, planID := range selectedIDs {
		member := memberByPlan[planID]
		if err := o.admitRecoveryMember(ctx, member, currentStage, now); err != nil {
			return err
		}
	}
	return o.markRecoveryStage(ctx, members, currentStage, now)
}

// recoveryStageVerdict returns (readyToExpand, regressed). Only admitted
// members contribute evidence. Missing or stale evidence holds the stage; it
// never manufactures health.
func (o *Orchestrator) recoveryStageVerdict(
	ctx context.Context,
	members []recoveryRolloutMember,
	stage int,
	now time.Time,
) (bool, bool, error) {
	planIDs := make([]string, 0, len(members))
	alreadyAdmitted := make([]string, 0, len(members))
	for _, member := range members {
		planIDs = append(planIDs, member.plan.ID)
		if member.runtime.State == contracts.QualityCircuitClosed && member.binding.State == contracts.BindingActive {
			alreadyAdmitted = append(alreadyAdmitted, member.plan.ID)
		}
	}
	expectedCohort := strategy.StableRecoveryCohortPlanIDs(planIDs, alreadyAdmitted, members[0].channel.SourceIdentity(), stage)
	admitted := 0
	for _, member := range members {
		if !expectedCohort[member.plan.ID] {
			// A held-out member has no production traffic by design, so it cannot
			// provide passive evidence for the current stage.
			continue
		}
		if member.runtime.RecoveryStage != stage || member.runtime.State != contracts.QualityCircuitClosed ||
			member.binding.State != contracts.BindingActive || member.runtime.RecoveryObserveAfter == nil {
			// Every selected member must be confirmed live before the stage can
			// advance. At 100%, this also prevents a newly-ready member from being
			// skipped while another worker is admitting it.
			return false, false, nil
		}
		admitted++
		if now.Before(*member.runtime.RecoveryObserveAfter) {
			return false, false, nil
		}
		strat := o.strategyFor(ctx, member.plan)
		observations, err := o.store.ListChannelObservations(ctx, contracts.ChannelObservationFilter{
			ChannelID: member.channel.ID, InstanceID: member.plan.InstanceID,
			Source: contracts.ObservationPassive, Since: timeValue(member.runtime.RecoveryStageStartedAt), Until: now,
		})
		if err != nil {
			return false, false, err
		}
		minSamples := strat.Thresholds.MinSamples
		if minSamples <= 0 {
			minSamples = 5
		}
		if len(observations) < minSamples {
			return false, false, nil
		}
		snapshot := healthmetrics.AggregateScope(contracts.ChannelHealthScope{
			ChannelID: member.channel.ID, InstanceID: member.plan.InstanceID,
			PoolID: member.plan.PoolID,
		}, contracts.Window5m, observations, healthmetrics.DefaultThresholds())
		evaluation := strategy.EvaluatePenalty(strat, strategy.Candidate{
			Channel: member.channel, Snapshot: snapshot, State: snapshot.HealthState,
		})
		if evaluation.Eject || evaluation.Score < strategy.DefaultQualityCircuitPolicy().RecoveryScore {
			return false, true, nil
		}
	}
	return admitted == len(expectedCohort) && admitted > 0, false, nil
}

func (o *Orchestrator) admitRecoveryMember(ctx context.Context, member recoveryRolloutMember, stage int, now time.Time) error {
	current, err := o.store.GetQualityCircuitRuntime(ctx, member.runtime.PlanID, member.runtime.ChannelID)
	if err != nil {
		return err
	}
	if !current.RecoveryReady {
		return nil
	}
	if member.binding.State != contracts.BindingActive {
		current.RestorePending = true
		current.RecoveryStage = stage
		current.RecoveryStageStartedAt = timePointer(now)
		current.RecoveryObserveAfter = timePointer(now.Add(recoveryRolloutObservationWindow))
		current.LastReason = contracts.QualityCircuitReason{
			Code: "recovery_restore_pending",
			Text: fmt.Sprintf("selected for %d%% guarded recovery stage", stage),
		}
		current.ProbeAfter = timePointer(now.Add(qualityProbeLease))
		pending, err := o.store.UpsertQualityCircuitRuntime(ctx, current, current.Version)
		if errors.Is(err, store.ErrConflict) {
			return nil
		}
		if err != nil {
			return err
		}
		return o.completePendingQualityRestore(o.withSchedulerTime(context.WithoutCancel(ctx), now), pending, now)
	}
	if current.State != contracts.QualityCircuitClosed {
		return nil
	}
	return o.updateRecoveryRuntimeAndEmit(ctx, current, func(next *contracts.QualityCircuitRuntime) {
		stageChanged := next.RecoveryStage != stage
		next.RecoveryStage = stage
		if stageChanged || next.RecoveryStageStartedAt == nil {
			next.RecoveryStageStartedAt = timePointer(now)
		}
		if stageChanged || next.RecoveryObserveAfter == nil {
			next.RecoveryObserveAfter = timePointer(now.Add(recoveryRolloutObservationWindow))
		}
	})
}

func (o *Orchestrator) markRecoveryStage(ctx context.Context, members []recoveryRolloutMember, stage int, now time.Time) error {
	for _, member := range members {
		current, err := o.store.GetQualityCircuitRuntime(ctx, member.runtime.PlanID, member.runtime.ChannelID)
		if err != nil || !current.RecoveryReady || current.RecoveryStage == stage {
			continue
		}
		if err := o.updateRecoveryRuntimeAndEmit(ctx, current, func(next *contracts.QualityCircuitRuntime) {
			next.RecoveryStage = stage
			if next.State == contracts.QualityCircuitClosed {
				next.RecoveryStageStartedAt = timePointer(now)
				next.RecoveryObserveAfter = timePointer(now.Add(recoveryRolloutObservationWindow))
				next.LastReason = contracts.QualityCircuitReason{
					Code: "recovery_stage_expanded",
					Text: fmt.Sprintf("recovery rollout expanded to %d%%", stage),
				}
			}
		}); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) reopenRecoveryRollout(
	ctx context.Context,
	sourceID string,
	members []recoveryRolloutMember,
	now time.Time,
) error {
	for _, member := range members {
		if member.binding.State == contracts.BindingActive {
			if _, err := o.engine.ApplyScheduling(o.autoCtx(ctx), member.plan.ID, map[string]bool{member.channel.ID: false}); err != nil {
				return err
			}
		}
		evaluation := ejectionEvaluation(member.channel.ID, "recovery_regressed", "guarded recovery traffic regressed for source "+sourceID, 0)
		opened, err := o.openQualityCircuit(ctx, member.plan.ID, member.channel.ID, evaluation, now)
		if err != nil {
			return err
		}
		o.emitRecoveryTransition(ctx, opened)
	}
	return nil
}

func (o *Orchestrator) completeRecoveryRollout(ctx context.Context, members []recoveryRolloutMember, now time.Time) error {
	for _, member := range members {
		current, err := o.store.GetQualityCircuitRuntime(ctx, member.runtime.PlanID, member.runtime.ChannelID)
		if err != nil || !current.RecoveryReady {
			continue
		}
		if err := o.updateRecoveryRuntimeAndEmit(ctx, current, func(next *contracts.QualityCircuitRuntime) {
			next.RecoveryReady = false
			next.RecoveryStage = 0
			next.RecoveryStageStartedAt = nil
			next.RecoveryObserveAfter = nil
			next.ConsecutiveProbeSuccesses = 0
			next.LastTransitionAt = timePointer(now)
			next.LastReason = contracts.QualityCircuitReason{
				Code: strategy.CircuitReasonRestored,
				Text: "guarded recovery rollout completed at 100%",
			}
		}); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) updateRecoveryRuntime(
	ctx context.Context,
	current contracts.QualityCircuitRuntime,
	mutate func(*contracts.QualityCircuitRuntime),
) error {
	mutate(&current)
	_, err := o.store.UpsertQualityCircuitRuntime(ctx, current, current.Version)
	if errors.Is(err, store.ErrConflict) {
		return nil
	}
	return err
}

func (o *Orchestrator) updateRecoveryRuntimeAndEmit(
	ctx context.Context,
	current contracts.QualityCircuitRuntime,
	mutate func(*contracts.QualityCircuitRuntime),
) error {
	mutate(&current)
	saved, err := o.store.UpsertQualityCircuitRuntime(ctx, current, current.Version)
	if errors.Is(err, store.ErrConflict) {
		return nil
	}
	if err == nil {
		o.emitRecoveryTransition(ctx, saved)
	}
	return err
}

func nextRecoveryRolloutStage(current int) int {
	for _, stage := range recoveryRolloutStages {
		if stage > current {
			return stage
		}
	}
	return 100
}
