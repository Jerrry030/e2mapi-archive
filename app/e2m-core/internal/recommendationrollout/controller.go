package recommendationrollout

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/recommendationexecution"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/upstreamexperiment"
	"e2m.local/core/internal/upstreamrecommendation"
)

const (
	DefaultObservationSeconds = 5 * 60
	// Four worker-lease windows reserve bounded execution/read-back time around
	// the four observation windows. Start requires strictly more remaining TTL.
	DefaultRolloutExecutionMargin = 4 * DefaultWorkerLease
)

var (
	ErrControllerInvalid     = errors.New("recommendation rollout controller: invalid input")
	ErrControllerNotFound    = errors.New("recommendation rollout controller: not found")
	ErrControllerConflict    = errors.New("recommendation rollout controller: state conflict")
	ErrControllerBlocked     = errors.New("recommendation rollout controller: safety gates blocked")
	ErrControllerUnavailable = errors.New("recommendation rollout controller: dependency unavailable")
)

// ControlStore is the complete, typed boundary used to derive rollout state.
// The browser never supplies a candidate, account id, weight, generation, or
// gate. Every one of those values is rebuilt from Store and Gateway reads.
type ControlStore interface {
	store.Store
	store.RecommendationRolloutStore
	store.RecommendationRolloutExecutionStatsStore
	store.UpstreamRecommendationStore
	store.UpstreamExperimentStore
	store.UpstreamRecommendationInputStore
	store.RecommendationRolloutDecisionInputStore
	store.RecommendationExecutionPolicyStore
}

// Planner is deliberately read-only. It exposes only the same dry-run method
// used by UI-14; a controller cannot reach publish/apply through this type.
type Planner interface {
	PlanScheduling(context.Context, string, map[string]bool) (contracts.ReconcilePlan, error)
}

// Controller creates and advances durable staged rollouts. Worker owns the
// gateway side effects; Controller owns current evidence and policy gates.
type Controller struct {
	store              ControlStore
	gateway            Gateway
	planner            Planner
	now                func() time.Time
	observationSeconds int
	forwardEnabled     func() bool
}

// MutationResult is the exact rollout transition committed by one controller
// call. Operation is non-nil only when that same store transaction enqueued
// durable work; callers must not issue a second read to infer that operation.
type MutationResult struct {
	Rollout   contracts.RecommendationRollout
	Operation *contracts.RecommendationRolloutOperation
}

func NewController(st ControlStore, gateway Gateway, planner Planner, observationSeconds int) (*Controller, error) {
	if st == nil || gateway == nil || planner == nil {
		return nil, ErrControllerInvalid
	}
	if observationSeconds <= 0 {
		observationSeconds = DefaultObservationSeconds
	}
	if observationSeconds > 7*24*60*60 {
		return nil, ErrControllerInvalid
	}
	return &Controller{
		store: st, gateway: gateway, planner: planner,
		now: func() time.Time { return time.Now().UTC() }, observationSeconds: observationSeconds,
		forwardEnabled: func() bool { return true },
	}, nil
}

// SetForwardEnabled installs the process-level auto-apply kill switch. It is
// consulted only for widening traffic; rollback deliberately ignores it.
func (c *Controller) SetForwardEnabled(enabled func() bool) {
	if c == nil {
		return
	}
	if enabled == nil {
		c.forwardEnabled = func() bool { return false }
		return
	}
	c.forwardEnabled = enabled
}

func (c *Controller) List(ctx context.Context, filter contracts.RecommendationRolloutFilter) ([]contracts.RecommendationRollout, error) {
	if c == nil || c.store == nil {
		return nil, ErrControllerUnavailable
	}
	return c.store.ListRecommendationRollouts(ctx, filter)
}

func (c *Controller) Get(ctx context.Context, id string) (contracts.RecommendationRollout, []contracts.RecommendationRolloutOperation, error) {
	if c == nil || c.store == nil || strings.TrimSpace(id) == "" {
		return contracts.RecommendationRollout{}, nil, ErrControllerInvalid
	}
	rollout, err := c.store.GetRecommendationRollout(ctx, strings.TrimSpace(id))
	if err != nil {
		return contracts.RecommendationRollout{}, nil, mapControllerStoreError(err)
	}
	operations, err := c.store.ListRecommendationRolloutOperations(ctx, rollout.State.ID)
	if err != nil {
		return contracts.RecommendationRollout{}, nil, mapControllerStoreError(err)
	}
	return rollout, operations, nil
}

// Start verifies recommendation/dry-run/policy/current facts, reads one exact
// remote weight snapshot, and atomically claims the plan generation while
// inserting the first 10% operation. Unknown weights fail closed; unrelated
// accounts, including explicit zeroes, remain unchanged in the exact baseline.
func (c *Controller) Start(ctx context.Context, userID int64, recommendationID string) (MutationResult, error) {
	if c == nil || c.store == nil || c.gateway == nil || userID <= 0 || strings.TrimSpace(recommendationID) == "" {
		return MutationResult{}, ErrControllerInvalid
	}
	if c.forwardEnabled == nil || !c.forwardEnabled() {
		return MutationResult{}, ErrControllerBlocked
	}
	recommendation, snapshot, dryRun, plan, revalidation, err := c.currentDecision(ctx, userID, strings.TrimSpace(recommendationID), "", 0)
	if err != nil {
		return MutationResult{}, err
	}
	if recommendation.Status != contracts.UpstreamRecommendationDryRunPassed || dryRun.ID == "" || len(recommendation.AffectedDownstreams) != 1 {
		return MutationResult{}, ErrControllerConflict
	}
	if !recommendationTTLAllowsRollout(snapshot.GeneratedAt, recommendation.ExpiresAt, c.observationSeconds) {
		return MutationResult{}, ErrControllerBlocked
	}
	baseline, fromAccount, toAccount, err := c.readStartBaseline(ctx, recommendation, snapshot, plan)
	if err != nil {
		return MutationResult{}, err
	}
	baselineFingerprint, err := contracts.RecommendationRolloutBaselineFingerprint(baseline)
	if err != nil {
		return MutationResult{}, ErrControllerBlocked
	}
	now := snapshot.GeneratedAt.UTC()
	rolloutID, err := newRolloutID()
	if err != nil {
		return MutationResult{}, ErrControllerUnavailable
	}
	state := contracts.RecommendationRolloutState{
		ID: rolloutID, UserID: userID, PlanID: plan.ID, RecommendationID: recommendation.ID,
		RecommendationFingerprint: recommendation.Fingerprint, FactVersion: recommendation.IntelligenceFactVersion,
		EvidenceIDs: append([]string(nil), recommendation.EvidenceIDs...), BaselineFingerprint: baselineFingerprint,
		Status: contracts.RecommendationRolloutApplying, Stage: contracts.RecommendationRolloutStageNone,
		PendingStage: contracts.RecommendationRolloutStage10, ObservationSeconds: c.observationSeconds,
		RecommendationExpiresAt: recommendation.ExpiresAt, StartedAt: now, UpdatedAt: now,
	}
	// Start's first operation is the state-machine Evaluate result. Re-run it
	// before persistence so a malformed revalidation cannot create an applying
	// rollout merely because its outer shape looks plausible.
	probe := state
	probe.Status, probe.PendingStage, probe.SchedulingGeneration = contracts.RecommendationRolloutReady, 0, plan.SchedulingGeneration+1
	revalidation.SchedulingGeneration = probe.SchedulingGeneration
	decision := Advance(probe, contracts.RecommendationRolloutEvent{
		Type: contracts.RecommendationRolloutEventEvaluate, UserID: userID, PlanID: plan.ID,
		RecommendationID: recommendation.ID, Now: now, Revalidation: &revalidation,
	})
	if len(decision.Reasons) != 0 || decision.Action.Kind != contracts.RecommendationRolloutActionApplyStage || decision.Action.TargetStage != contracts.RecommendationRolloutStage10 {
		return MutationResult{}, ErrControllerBlocked
	}
	created, operation, err := c.store.CreateRecommendationRollout(ctx, contracts.RecommendationRolloutCreate{
		Rollout: contracts.RecommendationRollout{
			State: state, InstanceID: plan.InstanceID, FromChannelID: recommendation.FromChannelID,
			ToChannelID: recommendation.ToChannelID, RecommendationPlanGeneration: plan.SchedulingGeneration,
			FromAccountID: fromAccount, ToAccountID: toAccount, BaselineWeights: baseline,
		},
		ExpectedPlanGeneration: plan.SchedulingGeneration,
		FirstAction:            contracts.RecommendationRolloutOperationApplyStage, FirstTargetStage: contracts.RecommendationRolloutStage10,
	})
	if err != nil {
		return MutationResult{}, mapControllerStoreError(err)
	}
	return MutationResult{Rollout: created, Operation: &operation}, nil
}

// Advance observes a completed stage and, when healthy, queues exactly the
// next 25/50/100 operation. Calling it before ObserveUntil fails closed.
func (c *Controller) Advance(ctx context.Context, userID int64, rolloutID string) (MutationResult, error) {
	rollout, err := c.ownedRollout(ctx, userID, rolloutID)
	if err != nil {
		return MutationResult{}, err
	}
	if rollout.State.Status != contracts.RecommendationRolloutObserving || rollout.State.ObserveUntil == nil || c.now().UTC().Before(*rollout.State.ObserveUntil) {
		return MutationResult{}, ErrControllerConflict
	}
	recommendation, snapshot, revalidation, err := c.currentRolloutDecision(ctx, rollout)
	if err != nil {
		var afterErr error
		recommendation, snapshot, revalidation, afterErr = c.currentRolloutAfterDecision(ctx, rollout)
		if afterErr != nil {
			return c.requireRollback(ctx, rollout, contracts.RecommendationRolloutBlockedGate)
		}
	}
	after, err := afterEvidenceFromSnapshot(rollout, recommendation, snapshot)
	if err != nil {
		return c.requireRollback(ctx, rollout, contracts.RecommendationRolloutBlockedAfterEvidence)
	}
	now := c.now().UTC()
	decision := Advance(rollout.State, contracts.RecommendationRolloutEvent{
		Type: contracts.RecommendationRolloutEventObserve, UserID: rollout.State.UserID,
		PlanID: rollout.State.PlanID, RecommendationID: rollout.State.RecommendationID,
		Now: now, Revalidation: &revalidation, AfterEvidence: &after,
	})
	if decision.State.Status == contracts.RecommendationRolloutRollbackRequired {
		result, enqueueErr := c.enqueueRollback(ctx, rollout, decision.State)
		if enqueueErr != nil {
			return MutationResult{}, enqueueErr
		}
		return result, ErrControllerBlocked
	}
	if len(decision.Reasons) != 0 {
		return MutationResult{}, ErrControllerBlocked
	}
	if decision.State.Status == contracts.RecommendationRolloutCompleted {
		completed, transitionErr := c.store.TransitionRecommendationRolloutState(ctx, rollout.State.ID, rollout.Version, decision.State)
		if transitionErr != nil {
			return MutationResult{}, mapControllerStoreError(transitionErr)
		}
		return MutationResult{Rollout: completed}, nil
	}
	// Observe and Evaluate are pure transitions. Persist them together with the
	// next operation so a process crash cannot strand a widened rollout in a
	// ready state without durable work.
	evaluated := Advance(decision.State, contracts.RecommendationRolloutEvent{
		Type: contracts.RecommendationRolloutEventEvaluate, UserID: rollout.State.UserID,
		PlanID: rollout.State.PlanID, RecommendationID: rollout.State.RecommendationID,
		Now: now, Revalidation: &revalidation,
	})
	if len(evaluated.Reasons) != 0 || evaluated.Action.Kind != contracts.RecommendationRolloutActionApplyStage {
		return c.requireRollback(ctx, rollout, contracts.RecommendationRolloutBlockedGate)
	}
	updated, operation, err := c.store.EnqueueRecommendationRolloutOperation(ctx, rollout.State.ID, rollout.Version, evaluated.State,
		contracts.RecommendationRolloutOperationApplyStage, evaluated.Action.TargetStage)
	if err != nil {
		return MutationResult{}, mapControllerStoreError(err)
	}
	return MutationResult{Rollout: updated, Operation: &operation}, nil
}

// Rollback deliberately does not read recommendation freshness, current
// dry-run, or execution policy. These may block widening, never restoration.
func (c *Controller) Rollback(ctx context.Context, userID int64, rolloutID string) (MutationResult, error) {
	rollout, err := c.ownedRollout(ctx, userID, rolloutID)
	if err != nil {
		return MutationResult{}, err
	}
	if rollout.State.Status == contracts.RecommendationRolloutRolledBack {
		return MutationResult{Rollout: rollout}, nil
	}
	return c.requireRollback(ctx, rollout, contracts.RecommendationRolloutBlockedOperatorRequested)
}

// Revalidate rebuilds every forward gate from a single Store snapshot and a
// freshly planned dry-run. It is also the Worker pre-write guard.
func (c *Controller) Revalidate(ctx context.Context, rollout contracts.RecommendationRollout) (contracts.RecommendationRolloutRevalidation, error) {
	if c == nil || c.forwardEnabled == nil || !c.forwardEnabled() {
		return contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
	}
	_, _, revalidation, err := c.currentRolloutDecision(ctx, rollout)
	if err != nil {
		return contracts.RecommendationRolloutRevalidation{}, err
	}
	return revalidation, nil
}

// AfterEvidence derives browser-safe forward evidence from a newly refreshed
// post-observation quality snapshot. Rollback evidence is deliberately not
// produced here: only Worker sees and verifies the final full gateway set.
func (c *Controller) AfterEvidence(ctx context.Context, rollout contracts.RecommendationRollout, stage contracts.RecommendationRolloutStage) (contracts.RecommendationRolloutAfterEvidence, error) {
	if c == nil || c.store == nil || !contracts.IsRecommendationRolloutStage(stage) || stage == contracts.RecommendationRolloutStageNone || stage != rollout.State.Stage {
		return contracts.RecommendationRolloutAfterEvidence{}, ErrControllerInvalid
	}
	recommendation, snapshot, _, err := c.currentRolloutDecision(ctx, rollout)
	if err != nil {
		return contracts.RecommendationRolloutAfterEvidence{}, err
	}
	return afterEvidenceFromSnapshot(rollout, recommendation, snapshot)
}

func (c *Controller) currentRolloutDecision(ctx context.Context, rollout contracts.RecommendationRollout) (
	contracts.UpstreamRecommendation, store.UpstreamRecommendationInputs, contracts.RecommendationRolloutRevalidation, error,
) {
	recommendation, snapshot, _, _, revalidation, err := c.currentDecision(ctx, rollout.State.UserID, rollout.State.RecommendationID, rollout.State.ID, rollout.State.SchedulingGeneration)
	if err != nil {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.RecommendationRolloutRevalidation{}, err
	}
	if revalidation.RecommendationFingerprint != rollout.State.RecommendationFingerprint || revalidation.FactVersion != rollout.State.FactVersion ||
		!sameRolloutIDs(revalidation.EvidenceIDs, rollout.State.EvidenceIDs) {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
	}
	return recommendation, snapshot, revalidation, nil
}

// currentRolloutAfterDecision preserves every immutable identity and mapping
// fence while allowing a freshly observed quality regression to become typed
// after-evidence. Forward revalidation remains blocked by the same regression;
// this path is used only after the observation boundary to choose an exact
// rollback reason instead of collapsing quality failure into a generic gate.
func (c *Controller) currentRolloutAfterDecision(ctx context.Context, rollout contracts.RecommendationRollout) (
	contracts.UpstreamRecommendation, store.UpstreamRecommendationInputs, contracts.RecommendationRolloutRevalidation, error,
) {
	decisionInputs, err := c.store.ReadRecommendationRolloutDecisionInputs(ctx, rollout.State.UserID, rollout.State.RecommendationID)
	if err != nil {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.RecommendationRolloutRevalidation{}, mapControllerStoreError(err)
	}
	recommendation, snapshot := decisionInputs.Recommendation, decisionInputs.Current
	plan, ok := exactRecommendationPlan(snapshot, rollout.State.PlanID, rollout.InstanceID)
	if !ok || plan.SchedulingGeneration != rollout.State.SchedulingGeneration || recommendation.Fingerprint != rollout.State.RecommendationFingerprint ||
		recommendation.IntelligenceFactVersion != rollout.State.FactVersion || !sameRolloutIDs(recommendation.EvidenceIDs, rollout.State.EvidenceIDs) {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
	}
	// Only a complete, fresh pair of post-boundary quality rows may use this
	// narrow regression path. Mapping, policy, lifecycle or callability failures
	// continue to fail through the generic current-decision gate above.
	qualityRows := make(map[string]contracts.ChannelHealthSnapshot, 2)
	for _, quality := range snapshot.Intelligence.QualitySnapshots {
		if quality.ChannelID != recommendation.FromChannelID && quality.ChannelID != recommendation.ToChannelID {
			continue
		}
		if quality.InstanceID != rollout.InstanceID || quality.Model != recommendation.ModelKey || quality.Window != contracts.Window5m ||
			rollout.State.ObserveUntil == nil || quality.CreatedAt.Before(*rollout.State.ObserveUntil) || quality.CreatedAt.After(snapshot.GeneratedAt) ||
			strings.TrimSpace(quality.ID) == "" || quality.QualitySampleCount <= 0 || !projectableRolloutQualitySnapshot(quality) {
			return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
		}
		if _, duplicate := qualityRows[quality.ChannelID]; duplicate {
			return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
		}
		qualityRows[quality.ChannelID] = quality
	}
	fromQuality, fromOK := qualityRows[recommendation.FromChannelID]
	toQuality, toOK := qualityRows[recommendation.ToChannelID]
	if !fromOK || !toOK || qualitySnapshotPassesRecommendation(fromQuality) && qualitySnapshotPassesRecommendation(toQuality) {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
	}
	// Derive the normal decision from a copy containing the last known healthy
	// recommendation-quality rows. If any non-quality fact, policy, mapping,
	// lifecycle, callability binding or dry-run fence changed, this still fails.
	healthySnapshot := snapshot
	healthySnapshot.Intelligence.QualitySnapshots = make([]contracts.ChannelHealthSnapshot, 0, len(snapshot.Intelligence.QualitySnapshots))
	for _, quality := range snapshot.Intelligence.QualitySnapshots {
		if quality.ChannelID == recommendation.FromChannelID || quality.ChannelID == recommendation.ToChannelID {
			continue
		}
		healthySnapshot.Intelligence.QualitySnapshots = append(healthySnapshot.Intelligence.QualitySnapshots, quality)
	}
	qualityEvidenceIDs, qualityIDsOK := recommendationConstraintEvidence(recommendation, contracts.UpstreamRecommendationConstraintQuality)
	if !qualityIDsOK || len(qualityEvidenceIDs) != 2 || len(decisionInputs.ExactQualityEvidence) != 2 {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
	}
	exactQuality := make(map[string]contracts.ChannelHealthSnapshot, 2)
	for _, candidate := range decisionInputs.ExactQualityEvidence {
		if !rolloutQualityEvidenceMatches(candidate, recommendation, rollout, snapshot.GeneratedAt) ||
			!containsRolloutID(qualityEvidenceIDs, candidate.ID) || !qualitySnapshotPassesRecommendation(candidate) {
			return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
		}
		if _, duplicate := exactQuality[candidate.ChannelID]; duplicate {
			return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
		}
		exactQuality[candidate.ChannelID] = candidate
	}
	if len(exactQuality) != 2 {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
	}
	for _, channelID := range []string{recommendation.FromChannelID, recommendation.ToChannelID} {
		candidate, found := exactQuality[channelID]
		if !found {
			return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
		}
		healthySnapshot.Intelligence.QualitySnapshots = append(healthySnapshot.Intelligence.QualitySnapshots, candidate)
	}
	// Historical quality freshness is evaluated at the recommendation's own
	// generation boundary. Other facts stay at the current transaction time.
	// The shared intelligence version may advance for collections, links,
	// retention or quality writes, so a higher counter is not provenance. Store
	// must supply a gap-free owner lineage proving that every intervening write
	// was quality-only; this boundary verifies the proof independently before
	// the semantic generator comparison below.
	qualityReferenceTime := recommendation.CreatedAt
	if qualityReferenceTime.IsZero() || qualityReferenceTime.After(snapshot.GeneratedAt) ||
		recommendation.IntelligenceFactVersion != recommendation.LinkFactVersion ||
		snapshot.Intelligence.FactVersion.FactVersion <= recommendation.IntelligenceFactVersion ||
		recommendation.CostLedgerFactVersion != snapshot.CostLedgerFactVersion ||
		!store.ValidQualityOnlyFactAdvanceProof(decisionInputs.QualityOnlyFactAdvance, rollout.State.UserID,
			recommendation.IntelligenceFactVersion, snapshot.Intelligence.FactVersion.FactVersion) {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
	}
	_, _, _, _, revalidation, err := c.currentDecisionFromSnapshotAtQuality(ctx, rollout.State.UserID, rollout.State.ID, rollout.State.SchedulingGeneration, recommendation, healthySnapshot, qualityReferenceTime)
	if err != nil || revalidation.RecommendationFingerprint != rollout.State.RecommendationFingerprint || revalidation.FactVersion != rollout.State.FactVersion ||
		!sameRolloutIDs(revalidation.EvidenceIDs, rollout.State.EvidenceIDs) {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
	}
	return recommendation, snapshot, revalidation, nil
}

func qualitySnapshotPassesRecommendation(value contracts.ChannelHealthSnapshot) bool {
	return value.HealthState == contracts.HealthHealthy && value.QualitySampleCount >= 5 && value.QualitySuccessRate >= 0.95 &&
		value.TTFTP95 <= 4000 && value.DurationP95 <= 20000 && value.AuthFailureCount == 0 && value.InsufficientBalanceCount == 0 &&
		finiteRolloutQualityMetric(value.QualitySuccessRate) && finiteRolloutQualityMetric(value.TTFTP95) &&
		finiteRolloutQualityMetric(value.DurationP95) && finiteRolloutQualityMetric(value.QualityScore)
}

func finiteRolloutQualityMetric(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func projectableRolloutQualitySnapshot(value contracts.ChannelHealthSnapshot) bool {
	return finiteRolloutQualityMetric(value.QualitySuccessRate) && finiteRolloutQualityMetric(value.TTFTP95) &&
		finiteRolloutQualityMetric(value.DurationP95) && finiteRolloutQualityMetric(value.QualityScore)
}

func rolloutQualityEvidenceMatches(value contracts.ChannelHealthSnapshot, recommendation contracts.UpstreamRecommendation, rollout contracts.RecommendationRollout, currentAt time.Time) bool {
	return strings.TrimSpace(value.ID) != "" &&
		(value.ChannelID == recommendation.FromChannelID || value.ChannelID == recommendation.ToChannelID) &&
		value.InstanceID == rollout.InstanceID && value.Model == recommendation.ModelKey && value.Window == contracts.Window5m &&
		!value.CreatedAt.IsZero() && !value.CreatedAt.After(currentAt) && !value.CreatedAt.After(recommendation.CreatedAt) &&
		recommendation.CreatedAt.Sub(value.CreatedAt) <= 5*time.Minute && value.QualitySampleCount > 0
}

func containsRolloutID(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (c *Controller) currentDecision(ctx context.Context, userID int64, recommendationID, rolloutID string, rolloutGeneration int64) (
	contracts.UpstreamRecommendation, store.UpstreamRecommendationInputs, contracts.UpstreamDryRunResult,
	contracts.RoutePlan, contracts.RecommendationRolloutRevalidation, error,
) {
	recommendation, err := c.store.GetUpstreamRecommendation(ctx, userID, recommendationID)
	if err != nil {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.UpstreamDryRunResult{}, contracts.RoutePlan{}, contracts.RecommendationRolloutRevalidation{}, mapControllerStoreError(err)
	}
	if recommendation.Status != contracts.UpstreamRecommendationDryRunPassed || len(recommendation.AffectedPlanIDs) != 1 || len(recommendation.AffectedDownstreams) != 1 {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.UpstreamDryRunResult{}, contracts.RoutePlan{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerConflict
	}
	snapshot, err := c.store.ReadUpstreamRecommendationInputs(ctx, userID)
	if err != nil {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.UpstreamDryRunResult{}, contracts.RoutePlan{}, contracts.RecommendationRolloutRevalidation{}, mapControllerStoreError(err)
	}
	return c.currentDecisionFromSnapshot(ctx, userID, rolloutID, rolloutGeneration, recommendation, snapshot)
}

func (c *Controller) currentDecisionFromSnapshot(
	ctx context.Context, userID int64, rolloutID string, rolloutGeneration int64,
	recommendation contracts.UpstreamRecommendation, snapshot store.UpstreamRecommendationInputs,
) (
	contracts.UpstreamRecommendation, store.UpstreamRecommendationInputs, contracts.UpstreamDryRunResult,
	contracts.RoutePlan, contracts.RecommendationRolloutRevalidation, error,
) {
	return c.currentDecisionFromSnapshotAtQuality(ctx, userID, rolloutID, rolloutGeneration, recommendation, snapshot, time.Time{})
}

func (c *Controller) currentDecisionFromSnapshotAtQuality(
	ctx context.Context, userID int64, rolloutID string, rolloutGeneration int64,
	recommendation contracts.UpstreamRecommendation, snapshot store.UpstreamRecommendationInputs, qualityReferenceTime time.Time,
) (
	contracts.UpstreamRecommendation, store.UpstreamRecommendationInputs, contracts.UpstreamDryRunResult,
	contracts.RoutePlan, contracts.RecommendationRolloutRevalidation, error,
) {
	plan, ok := exactRecommendationPlan(snapshot, recommendation.AffectedPlanIDs[0], recommendation.AffectedDownstreams[0])
	if !ok {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.UpstreamDryRunResult{}, contracts.RoutePlan{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
	}
	if rolloutGeneration > 0 && plan.SchedulingGeneration != rolloutGeneration {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.UpstreamDryRunResult{}, contracts.RoutePlan{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
	}
	generatedRecommendation, qualityRefresh, ok := trustedCurrentRecommendation(snapshot, recommendation, rolloutGeneration, qualityReferenceTime)
	if !ok {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.UpstreamDryRunResult{}, contracts.RoutePlan{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
	}
	if !qualityRefresh {
		validity := upstreamrecommendation.ValidateCurrent(recommendation, currentFactsFromGenerated(generatedRecommendation, snapshot.GeneratedAt))
		if !validity.Current {
			return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.UpstreamDryRunResult{}, contracts.RoutePlan{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
		}
	}
	if qualityRefresh && rolloutGeneration <= 0 {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.UpstreamDryRunResult{}, contracts.RoutePlan{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
	}
	dryRun, err := c.store.GetUpstreamDryRunResult(ctx, userID, recommendation.DryRunID)
	if err != nil {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.UpstreamDryRunResult{}, contracts.RoutePlan{}, contracts.RecommendationRolloutRevalidation{}, mapControllerStoreError(err)
	}
	bridge, err := upstreamexperiment.NewBridge(c.planner, func() time.Time { return snapshot.GeneratedAt })
	if err != nil {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.UpstreamDryRunResult{}, contracts.RoutePlan{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerUnavailable
	}
	// DryRun is a pure planner, but its public lifecycle boundary accepts only
	// ready_for_dry_run. Reconstruct that immediately preceding immutable state
	// instead of bypassing validation or passing the persisted passed state.
	planningRecommendation := recommendation
	planningRecommendation.Status = contracts.UpstreamRecommendationReadyForDryRun
	planningRecommendation.DryRunID = ""
	currentDry, err := bridge.DryRun(ctx, planningRecommendation)
	if err != nil {
		return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.UpstreamDryRunResult{}, contracts.RoutePlan{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
	}
	preview := contracts.UpstreamDryRunCurrent{
		UserID: currentDry.UserID, RecommendationID: currentDry.RecommendationID,
		RecommendationFingerprint: currentDry.RecommendationFingerprint,
		IntelligenceFactVersion:   currentDry.IntelligenceFactVersion, CostLedgerFactVersion: currentDry.CostLedgerFactVersion,
		LinkFactVersion: currentDry.LinkFactVersion, PlanGeneration: currentDry.PlanGeneration,
		PlanID: currentDry.PlanID, FromChannelID: currentDry.FromChannelID, ToChannelID: currentDry.ToChannelID,
		DesiredScheduling: currentDry.DesiredScheduling, Plan: currentDry.Plan,
	}
	if rolloutGeneration > 0 {
		// The immutable preview is bound to the recommendation generation. The
		// live generation was independently required to equal rolloutGeneration
		// above, so normalize only the preview generation changed by the rollout's
		// own fencing takeover.
		preview.PlanGeneration = dryRun.PlanGeneration
	}
	previewValidity := upstreamexperiment.ValidatePreview(dryRun, preview)
	policy, policyErr := c.store.GetRecommendationExecutionPolicy(ctx, userID, contracts.RecommendationExecutionScopePlan, plan.ID)
	if errors.Is(policyErr, store.ErrNotFound) {
		// A pool policy is the only fallback, and it must be explicit for this
		// owner's exact pool. Missing both policies remains default deny.
		policy, policyErr = c.store.GetRecommendationExecutionPolicy(ctx, userID, contracts.RecommendationExecutionScopePool, plan.PoolID)
	}
	authorization := contracts.RecommendationExecutionAuthorization{}
	if policyErr == nil {
		target := recommendationExecutionStatsFilter(policy, plan, snapshot.GeneratedAt, rolloutID)
		stats, statsErr := c.store.GetRecommendationRolloutExecutionStats(ctx, target)
		if statsErr != nil {
			policyErr = statsErr
		}
		authorization = recommendationexecution.Evaluate(policy, contracts.RecommendationExecutionContext{
			UserID: userID, PlanID: plan.ID, PoolID: plan.PoolID,
			ExpectedSavings: recommendation.Savings.PercentLower, DailyExecutionCount: stats.Count,
			LastExecutedAt: stats.LastStartedAt, Now: snapshot.GeneratedAt,
		})
	}
	gates := make([]contracts.RecommendationRolloutGate, 0, len(contracts.RecommendationRolloutRequiredGates()))
	for _, kind := range contracts.RecommendationRolloutRequiredGates() {
		status := contracts.RecommendationRolloutGatePassed
		switch kind {
		case contracts.RecommendationRolloutGateAuthorization:
			if policyErr != nil || !authorization.Allowed {
				status = contracts.RecommendationRolloutGateBlocked
			}
		case contracts.RecommendationRolloutGateDryRun:
			if !previewValidity.Current {
				status = contracts.RecommendationRolloutGateBlocked
			}
		case contracts.RecommendationRolloutGatePrice,
			contracts.RecommendationRolloutGateBalance, contracts.RecommendationRolloutGateCapacity,
			contracts.RecommendationRolloutGateQuality, contracts.RecommendationRolloutGateCallability:
			if !recommendationConstraintsPass(recommendation) {
				status = contracts.RecommendationRolloutGateBlocked
			}
		case contracts.RecommendationRolloutGateLifecycle, contracts.RecommendationRolloutGateMaintenance:
			if !recommendationLifecyclePasses(snapshot, recommendation, plan) {
				status = contracts.RecommendationRolloutGateBlocked
			}
		}
		gates = append(gates, contracts.RecommendationRolloutGate{Kind: kind, Status: status})
	}
	for _, gate := range gates {
		if gate.Status != contracts.RecommendationRolloutGatePassed {
			return contracts.UpstreamRecommendation{}, store.UpstreamRecommendationInputs{}, contracts.UpstreamDryRunResult{}, contracts.RoutePlan{}, contracts.RecommendationRolloutRevalidation{}, ErrControllerBlocked
		}
	}
	generation := plan.SchedulingGeneration
	revalidation := contracts.RecommendationRolloutRevalidation{
		UserID: userID, PlanID: plan.ID, RecommendationID: recommendation.ID,
		RecommendationFingerprint: recommendation.Fingerprint, FactVersion: recommendation.IntelligenceFactVersion,
		EvidenceIDs: append([]string(nil), recommendation.EvidenceIDs...), EvidenceObservedAt: snapshot.GeneratedAt,
		EvidenceFreshUntil: snapshot.GeneratedAt.Add(time.Minute), RecommendationExpiresAt: recommendation.ExpiresAt,
		SchedulingGeneration: generation, Gates: gates,
	}
	return recommendation, snapshot, dryRun, plan, revalidation, nil
}

func (c *Controller) readStartBaseline(ctx context.Context, recommendation contracts.UpstreamRecommendation, snapshot store.UpstreamRecommendationInputs, plan contracts.RoutePlan) ([]contracts.RecommendationRolloutAccountWeight, string, string, error) {
	accounts, err := c.gateway.ListAccounts(ctx, plan.InstanceID)
	if err != nil {
		return nil, "", "", ErrControllerUnavailable
	}
	weights := make([]contracts.RecommendationRolloutAccountWeight, 0, len(accounts))
	seen := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		id := strings.TrimSpace(account.ID)
		if id == "" || seen[id] || account.CurrentWeight == nil || *account.CurrentWeight < 0 || *account.CurrentWeight > 100 {
			return nil, "", "", ErrControllerBlocked
		}
		seen[id] = true
		weights = append(weights, contracts.RecommendationRolloutAccountWeight{AccountID: id, Weight: *account.CurrentWeight})
	}
	baseline, err := contracts.CanonicalRecommendationRolloutWeights(weights)
	if err != nil {
		return nil, "", "", ErrControllerBlocked
	}
	bindingAccounts := make(map[string]string)
	for _, binding := range snapshot.Bindings {
		if binding.PlanID != plan.ID || binding.InstanceID != plan.InstanceID || binding.State == contracts.BindingRevoked || strings.TrimSpace(binding.RemoteID) == "" {
			continue
		}
		if _, duplicate := bindingAccounts[binding.ChannelID]; duplicate {
			return nil, "", "", ErrControllerBlocked
		}
		bindingAccounts[binding.ChannelID] = strings.TrimSpace(binding.RemoteID)
	}
	fromAccount, toAccount := bindingAccounts[recommendation.FromChannelID], bindingAccounts[recommendation.ToChannelID]
	if fromAccount == "" || toAccount == "" || fromAccount == toAccount || len(bindingAccounts) != len(baseline) {
		return nil, "", "", ErrControllerBlocked
	}
	for _, remoteID := range bindingAccounts {
		if !seen[remoteID] {
			return nil, "", "", ErrControllerBlocked
		}
	}
	for _, value := range baseline {
		switch value.AccountID {
		case fromAccount:
			// Every named stage must produce a distinct, observable integer
			// migration. A no-op or repeated stage fails closed before any write.
			if !sourceBaselineSupportsEveryStage(value.Weight) {
				return nil, "", "", ErrControllerBlocked
			}
		case toAccount:
			// An already-serving destination is supported; 100% still means the
			// source reaches zero while unrelated account weights remain exact.
		}
	}
	return baseline, fromAccount, toAccount, nil
}

func recommendationTTLAllowsRollout(now, expiresAt time.Time, observationSeconds int) bool {
	if now.IsZero() || expiresAt.IsZero() || observationSeconds <= 0 || observationSeconds > 7*24*60*60 {
		return false
	}
	required := 4*time.Duration(observationSeconds)*time.Second + DefaultRolloutExecutionMargin
	return expiresAt.After(now.Add(required))
}

func (c *Controller) ownedRollout(ctx context.Context, userID int64, rolloutID string) (contracts.RecommendationRollout, error) {
	if c == nil || c.store == nil || userID <= 0 || strings.TrimSpace(rolloutID) == "" {
		return contracts.RecommendationRollout{}, ErrControllerInvalid
	}
	rollout, err := c.store.GetRecommendationRollout(ctx, strings.TrimSpace(rolloutID))
	if err != nil {
		return contracts.RecommendationRollout{}, mapControllerStoreError(err)
	}
	if rollout.State.UserID != userID {
		return contracts.RecommendationRollout{}, ErrControllerNotFound
	}
	return rollout, nil
}

func (c *Controller) requireRollback(ctx context.Context, rollout contracts.RecommendationRollout, reason contracts.RecommendationRolloutBlockReason) (MutationResult, error) {
	state := rollout.State
	state.Status = contracts.RecommendationRolloutRollbackRequired
	state.PendingStage = contracts.RecommendationRolloutStageNone
	state.ObserveUntil = nil
	state.RollbackReasons = appendUniqueRolloutReason(state.RollbackReasons, reason)
	state.UpdatedAt = c.now().UTC()
	return c.enqueueRollback(ctx, rollout, state)
}

func (c *Controller) enqueueRollback(ctx context.Context, rollout contracts.RecommendationRollout, state contracts.RecommendationRolloutState) (MutationResult, error) {
	updated, operation, err := c.store.EnqueueRecommendationRolloutOperation(ctx, rollout.State.ID, rollout.Version, state,
		contracts.RecommendationRolloutOperationRollback, contracts.RecommendationRolloutStageNone)
	if err != nil {
		return MutationResult{}, mapControllerStoreError(err)
	}
	return MutationResult{Rollout: updated, Operation: &operation}, nil
}

func trustedCurrentRecommendation(snapshot store.UpstreamRecommendationInputs, recommendation contracts.UpstreamRecommendation, rolloutGeneration int64, qualityReferenceTime time.Time) (contracts.UpstreamRecommendation, bool, bool) {
	input := recommendationGeneratorInputs(snapshot)
	input.QualityReferenceTime = qualityReferenceTime
	if rolloutGeneration > 0 {
		// The rollout owns a newer plan generation, while the recommendation and
		// immutable dry-run remain bound to the generation immediately before its
		// takeover. Rebuild all other facts from the current snapshot, but restore
		// only that expected generation inside the pure generator. The live plan
		// generation was checked against rolloutGeneration above.
		for index := range input.RoutePlans {
			if len(recommendation.AffectedPlanIDs) == 1 && input.RoutePlans[index].ID == recommendation.AffectedPlanIDs[0] {
				input.RoutePlans[index].SchedulingGeneration = recommendation.PlanGeneration
			}
		}
	}
	sequence := 0
	generated, err := upstreamrecommendation.Generate(input, func() string {
		sequence++
		return "current-recommendation-" + strconv.Itoa(sequence)
	})
	if err != nil {
		return contracts.UpstreamRecommendation{}, false, false
	}
	for _, current := range generated.Recommendations {
		if current.Fingerprint == recommendation.Fingerprint {
			return current, false, true
		}
		if rolloutGeneration > 0 && sameRecommendationExceptRefreshedQuality(recommendation, current) {
			// A rollout is expected to obtain new rolling quality rows. The live
			// generator has just re-proved the complete decision; only the global
			// intelligence version and quality evidence identities may advance.
			// Return the immutable recommendation identity expected by the rollout
			// while retaining the current snapshot time for expiry/freshness.
			return current, true, true
		}
	}
	return contracts.UpstreamRecommendation{}, false, false
}

func sameRecommendationExceptRefreshedQuality(saved, current contracts.UpstreamRecommendation) bool {
	if upstreamrecommendation.Validate(saved) != nil || upstreamrecommendation.Validate(current) != nil ||
		saved.Status != contracts.UpstreamRecommendationDryRunPassed || strings.TrimSpace(saved.DryRunID) == "" ||
		current.Status != contracts.UpstreamRecommendationOpen || strings.TrimSpace(current.DryRunID) != "" ||
		current.CreatedAt.Before(saved.CreatedAt) || current.ExpiresAt.Sub(current.CreatedAt) != saved.ExpiresAt.Sub(saved.CreatedAt) {
		return false
	}
	savedQuality, savedOK := recommendationConstraintEvidence(saved, contracts.UpstreamRecommendationConstraintQuality)
	currentQuality, currentOK := recommendationConstraintEvidence(current, contracts.UpstreamRecommendationConstraintQuality)
	if !savedOK || !currentOK {
		return false
	}
	savedNonQuality, savedOK := withoutRolloutIDs(saved.EvidenceIDs, savedQuality)
	currentNonQuality, currentOK := withoutRolloutIDs(current.EvidenceIDs, currentQuality)
	if !savedOK || !currentOK || len(savedQuality) != 2 || len(currentQuality) != 2 ||
		!sameRolloutIDs(savedNonQuality, currentNonQuality) ||
		saved.IntelligenceFactVersion != saved.LinkFactVersion || current.IntelligenceFactVersion != current.LinkFactVersion ||
		current.IntelligenceFactVersion <= saved.IntelligenceFactVersion ||
		saved.CostLedgerFactVersion != current.CostLedgerFactVersion {
		return false
	}

	// Normalize only lifecycle/display identity, the global intelligence/link
	// versions, and quality evidence IDs. Every mapping, price/cost value,
	// non-quality evidence ID, constraint and strategy field remains exact.
	saved.ID, saved.Status, saved.DryRunID = current.ID, current.Status, current.DryRunID
	saved.IntelligenceFactVersion, saved.LinkFactVersion = current.IntelligenceFactVersion, current.LinkFactVersion
	saved.CreatedAt, saved.ExpiresAt, saved.Fingerprint = current.CreatedAt, current.ExpiresAt, current.Fingerprint
	saved.EvidenceIDs = append([]string(nil), current.EvidenceIDs...)
	saved.Constraints = cloneRolloutConstraints(saved.Constraints)
	for index := range saved.Constraints {
		if saved.Constraints[index].Kind == contracts.UpstreamRecommendationConstraintQuality {
			saved.Constraints[index].EvidenceIDs = append([]string(nil), currentQuality...)
		}
	}
	return reflect.DeepEqual(saved, current)
}

func recommendationConstraintEvidence(value contracts.UpstreamRecommendation, kind contracts.UpstreamRecommendationConstraintKind) ([]string, bool) {
	var result []string
	count := 0
	for _, constraint := range value.Constraints {
		if constraint.Kind == kind {
			result, count = append([]string(nil), constraint.EvidenceIDs...), count+1
		}
	}
	return result, count == 1 && validUniqueRolloutIDs(result)
}

func withoutRolloutIDs(values, removed []string) ([]string, bool) {
	if !validUniqueRolloutIDs(values) || !validUniqueRolloutIDs(removed) {
		return nil, false
	}
	remove := make(map[string]bool, len(removed))
	for _, value := range removed {
		remove[value] = false
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := remove[value]; !ok {
			result = append(result, value)
		} else {
			remove[value] = true
		}
	}
	for _, found := range remove {
		if !found {
			return nil, false
		}
	}
	return result, true
}

func validUniqueRolloutIDs(values []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func cloneRolloutConstraints(values []contracts.UpstreamRecommendationConstraint) []contracts.UpstreamRecommendationConstraint {
	result := append([]contracts.UpstreamRecommendationConstraint(nil), values...)
	for index := range result {
		result[index].EvidenceIDs = append([]string(nil), result[index].EvidenceIDs...)
	}
	return result
}

func afterEvidenceFromSnapshot(rollout contracts.RecommendationRollout, recommendation contracts.UpstreamRecommendation, snapshot store.UpstreamRecommendationInputs) (contracts.RecommendationRolloutAfterEvidence, error) {
	if rollout.State.Status != contracts.RecommendationRolloutObserving || rollout.State.Stage == contracts.RecommendationRolloutStageNone ||
		rollout.State.StageStartedAt == nil || rollout.State.ObserveUntil == nil || snapshot.GeneratedAt.Before(*rollout.State.ObserveUntil) ||
		recommendation.Fingerprint != rollout.State.RecommendationFingerprint || len(recommendation.AffectedDownstreams) != 1 {
		return contracts.RecommendationRolloutAfterEvidence{}, ErrControllerBlocked
	}
	channels := map[string]bool{recommendation.FromChannelID: false, recommendation.ToChannelID: false}
	evidence := make([]string, 0, len(channels))
	observedAt := time.Time{}
	freshUntil := time.Time{}
	selectedQuality := make(map[string]contracts.ChannelHealthSnapshot, len(channels))
	for _, quality := range snapshot.Intelligence.QualitySnapshots {
		if _, wanted := channels[quality.ChannelID]; !wanted || quality.InstanceID != recommendation.AffectedDownstreams[0] ||
			quality.Model != recommendation.ModelKey || quality.Window != contracts.Window5m {
			continue
		}
		if quality.CreatedAt.Before(*rollout.State.ObserveUntil) {
			continue
		}
		if strings.TrimSpace(quality.ID) == "" || quality.CreatedAt.After(snapshot.GeneratedAt) || quality.QualitySampleCount <= 0 {
			return contracts.RecommendationRolloutAfterEvidence{}, ErrControllerBlocked
		}
		if current, exists := selectedQuality[quality.ChannelID]; exists {
			if !quality.CreatedAt.After(current.CreatedAt) {
				continue
			}
			return contracts.RecommendationRolloutAfterEvidence{}, ErrControllerBlocked
		}
		selectedQuality[quality.ChannelID] = quality
		channels[quality.ChannelID] = true
		evidence = append(evidence, quality.ID)
		if quality.CreatedAt.After(observedAt) {
			observedAt = quality.CreatedAt
		}
		candidateFreshUntil := quality.CreatedAt.Add(5 * time.Minute)
		if freshUntil.IsZero() || candidateFreshUntil.Before(freshUntil) {
			freshUntil = candidateFreshUntil
		}
	}
	for _, found := range channels {
		if !found {
			return contracts.RecommendationRolloutAfterEvidence{}, ErrControllerBlocked
		}
	}
	// A healthy aggregate is quality evidence, not proof that either published
	// binding is callable. Require fresh successful passive/probe verification
	// for both rollout channels after the stage observation boundary. This keeps
	// synthetic health rows from being promoted into a callability PASS.
	verifiedBindings := map[string]bool{recommendation.FromChannelID: false, recommendation.ToChannelID: false}
	for _, binding := range snapshot.Bindings {
		if _, wanted := verifiedBindings[binding.ChannelID]; !wanted || binding.PlanID != rollout.State.PlanID ||
			binding.InstanceID != recommendation.AffectedDownstreams[0] || binding.State != contracts.BindingActive ||
			(binding.VerificationStatus != contracts.BindingVerificationPassiveVerified &&
				binding.VerificationStatus != contracts.BindingVerificationProbeVerified) || binding.VerifiedAt == nil ||
			binding.VerifiedAt.Before(*rollout.State.ObserveUntil) || binding.VerifiedAt.After(snapshot.GeneratedAt) {
			continue
		}
		if verifiedBindings[binding.ChannelID] || strings.TrimSpace(binding.ID) == "" {
			return contracts.RecommendationRolloutAfterEvidence{}, ErrControllerBlocked
		}
		verifiedBindings[binding.ChannelID] = true
		evidence = append(evidence, binding.ID)
		if binding.VerifiedAt.After(observedAt) {
			observedAt = binding.VerifiedAt.UTC()
		}
		candidateFreshUntil := binding.VerifiedAt.Add(5 * time.Minute)
		if freshUntil.IsZero() || candidateFreshUntil.Before(freshUntil) {
			freshUntil = candidateFreshUntil
		}
	}
	for _, found := range verifiedBindings {
		if !found {
			return contracts.RecommendationRolloutAfterEvidence{}, ErrControllerBlocked
		}
	}
	if observedAt.IsZero() || freshUntil.IsZero() || !snapshot.GeneratedAt.Before(freshUntil) {
		return contracts.RecommendationRolloutAfterEvidence{}, ErrControllerBlocked
	}
	qualityGate := contracts.RecommendationRolloutGatePassed
	for _, quality := range selectedQuality {
		if !qualitySnapshotPassesRecommendation(quality) {
			qualityGate = contracts.RecommendationRolloutGateBlocked
		}
	}
	return contracts.RecommendationRolloutAfterEvidence{
		Stage: rollout.State.Stage, RecommendationFingerprint: rollout.State.RecommendationFingerprint,
		SchedulingGeneration: rollout.State.SchedulingGeneration, EvidenceIDs: evidence,
		ObservedAt: observedAt.UTC(), FreshUntil: freshUntil.UTC(),
		Callability: contracts.RecommendationRolloutGatePassed, Quality: qualityGate,
	}, nil
}

func recommendationGeneratorInputs(snapshot store.UpstreamRecommendationInputs) upstreamrecommendation.GeneratorInputs {
	intelligence := snapshot.Intelligence
	if snapshot.UserID <= 0 || intelligence.UserID != snapshot.UserID || intelligence.FactVersion.UserID != snapshot.UserID ||
		snapshot.GeneratedAt.IsZero() || intelligence.GeneratedAt.IsZero() || !snapshot.GeneratedAt.Equal(intelligence.GeneratedAt) {
		return upstreamrecommendation.GeneratorInputs{}
	}
	input := upstreamrecommendation.GeneratorInputs{
		UserID: snapshot.UserID, GeneratedAt: snapshot.GeneratedAt,
		IntelligenceFactVersion: intelligence.FactVersion.FactVersion, CostLedgerFactVersion: snapshot.CostLedgerFactVersion,
		Sources:           append([]contracts.UpstreamIntelligenceSource(nil), intelligence.Sources...),
		LatestRuns:        append([]contracts.UpstreamCollectionRun(nil), intelligence.LatestRuns...),
		Wallets:           append([]contracts.UpstreamWalletObservation(nil), intelligence.Wallets...),
		Offers:            append([]contracts.UpstreamOfferObservation(nil), intelligence.Offers...),
		Links:             append([]contracts.UpstreamIntelligenceLink(nil), intelligence.Links...),
		QualitySnapshots:  append([]contracts.ChannelHealthSnapshot(nil), intelligence.QualitySnapshots...),
		CostFacts:         append([]contracts.UpstreamCostFact(nil), snapshot.CostFacts...),
		RoutePlans:        append([]contracts.RoutePlan(nil), snapshot.RoutePlans...),
		AllocatedChannels: append([]contracts.UpstreamChannel(nil), snapshot.Channels...),
		Bindings:          append([]contracts.PublishedBinding(nil), snapshot.Bindings...),
	}
	for _, resolution := range intelligence.LinkResolutions {
		if resolution.UserID != snapshot.UserID {
			return upstreamrecommendation.GeneratorInputs{}
		}
		if resolution.ResolvedChannelID == "" && resolution.ResolvedChannelOwnerID == 0 && !resolution.TargetVerified {
			continue
		}
		if resolution.LinkID == "" || resolution.ResolvedChannelID == "" || resolution.ResolvedChannelOwnerID != snapshot.UserID {
			return upstreamrecommendation.GeneratorInputs{}
		}
		input.LinkResolutions = append(input.LinkResolutions, upstreamrecommendation.GeneratorLinkResolution{
			LinkID: resolution.LinkID, UserID: snapshot.UserID, ChannelID: resolution.ResolvedChannelID, TargetVerified: resolution.TargetVerified,
		})
	}
	return input
}

func currentFactsFromGenerated(current contracts.UpstreamRecommendation, now time.Time) contracts.UpstreamRecommendationCurrentFacts {
	return contracts.UpstreamRecommendationCurrentFacts{
		UserID: current.UserID, IntelligenceFactVersion: current.IntelligenceFactVersion,
		CostLedgerFactVersion: current.CostLedgerFactVersion, LinkFactVersion: current.LinkFactVersion,
		PlanGeneration: current.PlanGeneration, FromSourceID: current.FromSourceID, FromChannelID: current.FromChannelID,
		FromGroupKey: current.FromGroupKey, ToSourceID: current.ToSourceID, ToChannelID: current.ToChannelID,
		ToGroupKey: current.ToGroupKey, ModelKey: current.ModelKey, PriceDimension: current.PriceDimension,
		SettlementCurrency: current.SettlementCurrency, PerTokens: current.PerTokens,
		AffectedPlanIDs:     append([]string(nil), current.AffectedPlanIDs...),
		AffectedDownstreams: append([]string(nil), current.AffectedDownstreams...),
		EvidenceIDs:         append([]string(nil), current.EvidenceIDs...), FormulaVersion: current.FormulaVersion,
		StrategyVersion: current.StrategyVersion, Now: now,
	}
}

func recommendationExecutionStatsFilter(policy contracts.RecommendationExecutionPolicy, plan contracts.RoutePlan, now time.Time, excludeRolloutID string) store.RecommendationRolloutExecutionStatsFilter {
	since := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	filter := store.RecommendationRolloutExecutionStatsFilter{
		UserID: plan.UserID, Scope: policy.Scope, Since: since, ExcludeRolloutID: excludeRolloutID,
	}
	if policy.Scope == contracts.RecommendationExecutionScopePlan {
		filter.PlanID = plan.ID
	} else {
		filter.PoolID = plan.PoolID
	}
	return filter
}

func exactRecommendationPlan(snapshot store.UpstreamRecommendationInputs, planID, instanceID string) (contracts.RoutePlan, bool) {
	var result contracts.RoutePlan
	count := 0
	for _, plan := range snapshot.RoutePlans {
		if plan.ID == planID && plan.InstanceID == instanceID && plan.UserID == snapshot.UserID && plan.Status == contracts.RoutePlanPublished {
			result, count = plan, count+1
		}
	}
	return result, count == 1 && result.SchedulingGeneration > 0
}

func recommendationConstraintsPass(recommendation contracts.UpstreamRecommendation) bool {
	seen := make(map[contracts.UpstreamRecommendationConstraintKind]bool)
	for _, constraint := range recommendation.Constraints {
		if constraint.Status != contracts.UpstreamRecommendationConstraintPassed || seen[constraint.Kind] {
			return false
		}
		seen[constraint.Kind] = true
	}
	return seen[contracts.UpstreamRecommendationConstraintQuality] && seen[contracts.UpstreamRecommendationConstraintCapacity] && seen[contracts.UpstreamRecommendationConstraintBalance]
}

func recommendationLifecyclePasses(snapshot store.UpstreamRecommendationInputs, recommendation contracts.UpstreamRecommendation, plan contracts.RoutePlan) bool {
	if plan.Status != contracts.RoutePlanPublished {
		return false
	}
	channels := map[string]bool{}
	for _, channel := range snapshot.Channels {
		if channel.PoolID == plan.PoolID && channel.Status == contracts.UpstreamChannelActive && channel.IsInventoryReady() {
			channels[channel.ID] = true
		}
	}
	return channels[recommendation.FromChannelID] && channels[recommendation.ToChannelID]
}

func sameRolloutIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]int, len(left))
	for _, value := range left {
		seen[value]++
	}
	for _, value := range right {
		seen[value]--
	}
	for _, count := range seen {
		if count != 0 {
			return false
		}
	}
	return true
}

func appendUniqueRolloutReason(values []contracts.RecommendationRolloutBlockReason, value contracts.RecommendationRolloutBlockReason) []contracts.RecommendationRolloutBlockReason {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func mapControllerStoreError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ErrControllerNotFound
	case errors.Is(err, store.ErrConflict):
		return ErrControllerConflict
	case errors.Is(err, store.ErrInvalid):
		return ErrControllerInvalid
	default:
		return ErrControllerUnavailable
	}
}

func newRolloutID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "rec-rollout-" + hex.EncodeToString(raw[:]), nil
}
