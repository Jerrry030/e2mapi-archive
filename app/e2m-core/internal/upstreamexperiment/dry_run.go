package upstreamexperiment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
)

var ErrInvalidDryRun = errors.New("upstream experiment: invalid dry-run")

const actionHashDomain = "e2m.upstream-experiment.action-set.v1"

// SchedulingPlanner is intentionally narrower than autoswitch.Reconciler. A
// Bridge has no ApplyScheduling method and therefore cannot execute a gateway
// mutation through its dependency.
type SchedulingPlanner interface {
	PlanScheduling(ctx context.Context, planID string, desired map[string]bool) (contracts.ReconcilePlan, error)
}

type Bridge struct {
	planner SchedulingPlanner
	now     func() time.Time
}

func NewBridge(planner SchedulingPlanner, now func() time.Time) (*Bridge, error) {
	if planner == nil {
		return nil, ErrInvalidDryRun
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Bridge{planner: planner, now: now}, nil
}

// DryRun invokes exactly PlanScheduling. publish.Engine records that call as a
// ReconcileRunDryRun; the bridge rejects any non-dry plan before returning it.
func (bridge *Bridge) DryRun(ctx context.Context, recommendation contracts.UpstreamRecommendation) (contracts.UpstreamDryRunResult, error) {
	if bridge == nil || bridge.planner == nil || !dryRunnable(recommendation) {
		return contracts.UpstreamDryRunResult{}, ErrInvalidDryRun
	}
	desired := map[string]bool{recommendation.FromChannelID: false, recommendation.ToChannelID: true}
	plan, err := bridge.planner.PlanScheduling(ctx, recommendation.AffectedPlanIDs[0], copyIntent(desired))
	if err != nil {
		return contracts.UpstreamDryRunResult{}, err
	}
	if !plan.DryRun || plan.PlanID != recommendation.AffectedPlanIDs[0] {
		return contracts.UpstreamDryRunResult{}, ErrInvalidDryRun
	}
	hash, err := ActionSetHash(plan)
	if err != nil {
		return contracts.UpstreamDryRunResult{}, err
	}
	createdAt := bridge.now().UTC()
	// The planner may timestamp its preview after the recommendation-input
	// snapshot used by the caller's clock. Preserve that exact preview while
	// keeping the enclosing evidence causally ordered; a result cannot predate
	// the plan it records.
	if plan.CreatedAt.After(createdAt) {
		createdAt = plan.CreatedAt.UTC()
	}
	return contracts.UpstreamDryRunResult{
		// ID is assigned by the application boundary. ActionSetHash is stable
		// evidence of the preview, but it is not a run identity: two previews of
		// the same action set are separate immutable observations.
		UserID: recommendation.UserID, RecommendationID: recommendation.ID, RecommendationFingerprint: recommendation.Fingerprint,
		IntelligenceFactVersion: recommendation.IntelligenceFactVersion, CostLedgerFactVersion: recommendation.CostLedgerFactVersion,
		LinkFactVersion: recommendation.LinkFactVersion, PlanGeneration: recommendation.PlanGeneration,
		PlanID: plan.PlanID, FromChannelID: recommendation.FromChannelID, ToChannelID: recommendation.ToChannelID,
		DesiredScheduling: copyIntent(desired), ReconcileKind: contracts.ReconcileRunDryRun, Plan: clonePlan(plan),
		ActionHashVersion: contracts.UpstreamExperimentActionHashVersionV1, ActionSetHash: hash, CreatedAt: createdAt,
	}, nil
}

// ActionSetHash includes semantic action fields but excludes display-only
// Detail and CreatedAt. Action ordering from adapters cannot change identity.
func ActionSetHash(plan contracts.ReconcilePlan) (string, error) {
	if !plan.DryRun || strings.TrimSpace(plan.PlanID) == "" || strings.TrimSpace(plan.InstanceID) == "" {
		return "", ErrInvalidDryRun
	}
	actions := append([]contracts.ReconcileAction(nil), plan.Actions...)
	for _, action := range actions {
		if !validAction(action) {
			return "", ErrInvalidDryRun
		}
	}
	sort.Slice(actions, func(i, j int) bool {
		left := strings.Join([]string{string(actions[i].Type), actions[i].ChannelID, actions[i].RemoteID}, "\x00")
		right := strings.Join([]string{string(actions[j].Type), actions[j].ChannelID, actions[j].RemoteID}, "\x00")
		return left < right
	})
	parts := []string{actionHashDomain, contracts.UpstreamExperimentActionHashVersionV1, plan.PlanID, plan.InstanceID, strconv.Itoa(len(actions))}
	for _, action := range actions {
		parts = append(parts, string(action.Type), action.ChannelID, action.RemoteID)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:]), nil
}

// ValidatePreview compares a persisted result with a newly planned preview.
// It is fail-closed and suitable for the execution boundary before creating a
// decision. This function itself performs no execution.
func ValidatePreview(saved contracts.UpstreamDryRunResult, current contracts.UpstreamDryRunCurrent) contracts.UpstreamDryRunValidity {
	reasons := make([]contracts.UpstreamDryRunStaleReason, 0)
	add := func(reason contracts.UpstreamDryRunStaleReason) {
		for _, existing := range reasons {
			if existing == reason {
				return
			}
		}
		reasons = append(reasons, reason)
	}
	currentHash, hashErr := ActionSetHash(current.Plan)
	if !validSaved(saved) || !validCurrentPreview(current) || hashErr != nil {
		add(contracts.UpstreamDryRunStaleInvalidCurrent)
	}
	if saved.UserID != current.UserID || saved.RecommendationID != current.RecommendationID || saved.PlanID != current.PlanID {
		add(contracts.UpstreamDryRunStaleOwnerScope)
	}
	if saved.RecommendationFingerprint != current.RecommendationFingerprint {
		add(contracts.UpstreamDryRunStaleFingerprint)
	}
	if saved.IntelligenceFactVersion != current.IntelligenceFactVersion {
		add(contracts.UpstreamDryRunStaleIntelligenceVersion)
	}
	if saved.CostLedgerFactVersion != current.CostLedgerFactVersion {
		add(contracts.UpstreamDryRunStaleCostVersion)
	}
	if saved.LinkFactVersion != current.LinkFactVersion {
		add(contracts.UpstreamDryRunStaleLinkVersion)
	}
	if saved.PlanGeneration != current.PlanGeneration {
		add(contracts.UpstreamDryRunStalePlanGeneration)
	}
	if saved.FromChannelID != current.FromChannelID || saved.ToChannelID != current.ToChannelID || !sameIntent(saved.DesiredScheduling, current.DesiredScheduling) {
		add(contracts.UpstreamDryRunStaleIntent)
	}
	if hashErr != nil || saved.ActionSetHash != currentHash {
		add(contracts.UpstreamDryRunStaleActionSet)
	}
	sort.Slice(reasons, func(i, j int) bool { return reasons[i] < reasons[j] })
	return contracts.UpstreamDryRunValidity{Current: len(reasons) == 0, Reasons: reasons}
}

func dryRunnable(value contracts.UpstreamRecommendation) bool {
	return value.UserID > 0 && strings.TrimSpace(value.ID) != "" && len(value.Fingerprint) == 64 && value.IntelligenceFactVersion > 0 &&
		value.CostLedgerFactVersion > 0 && value.LinkFactVersion > 0 && value.PlanGeneration > 0 && value.Status == contracts.UpstreamRecommendationReadyForDryRun &&
		strings.TrimSpace(value.FromChannelID) != "" && strings.TrimSpace(value.ToChannelID) != "" && value.FromChannelID != value.ToChannelID &&
		len(value.AffectedPlanIDs) == 1 && strings.TrimSpace(value.AffectedPlanIDs[0]) != ""
}

func validSaved(value contracts.UpstreamDryRunResult) bool {
	return value.UserID > 0 && strings.TrimSpace(value.RecommendationID) != "" && len(value.RecommendationFingerprint) == 64 &&
		value.IntelligenceFactVersion > 0 && value.CostLedgerFactVersion > 0 && value.LinkFactVersion > 0 && value.PlanGeneration > 0 &&
		value.ReconcileKind == contracts.ReconcileRunDryRun && value.ActionHashVersion == contracts.UpstreamExperimentActionHashVersionV1 &&
		len(value.ActionSetHash) == 64 && sameIntent(value.DesiredScheduling, map[string]bool{value.FromChannelID: false, value.ToChannelID: true})
}

func validCurrentPreview(value contracts.UpstreamDryRunCurrent) bool {
	return value.UserID > 0 && strings.TrimSpace(value.RecommendationID) != "" && len(value.RecommendationFingerprint) == 64 &&
		value.IntelligenceFactVersion > 0 && value.CostLedgerFactVersion > 0 && value.LinkFactVersion > 0 && value.PlanGeneration > 0 &&
		value.Plan.DryRun && value.PlanID == value.Plan.PlanID && sameIntent(value.DesiredScheduling, map[string]bool{value.FromChannelID: false, value.ToChannelID: true})
}

func validAction(value contracts.ReconcileAction) bool {
	if strings.TrimSpace(value.ChannelID) == "" {
		return false
	}
	switch value.Type {
	case contracts.ReconcileCreate, contracts.ReconcileEnable, contracts.ReconcileDisable, contracts.ReconcileRevoke,
		contracts.ReconcileUpdate, contracts.ReconcileDeprovision, contracts.ReconcileHold:
		return true
	default:
		return false
	}
}

func sameIntent(left, right map[string]bool) bool {
	if len(left) != len(right) || len(left) == 0 {
		return false
	}
	for key, value := range left {
		if strings.TrimSpace(key) == "" {
			return false
		}
		rightValue, exists := right[key]
		if !exists || rightValue != value {
			return false
		}
	}
	return true
}

func copyIntent(value map[string]bool) map[string]bool {
	result := make(map[string]bool, len(value))
	for key, enabled := range value {
		result[key] = enabled
	}
	return result
}

func clonePlan(value contracts.ReconcilePlan) contracts.ReconcilePlan {
	value.Actions = append([]contracts.ReconcileAction(nil), value.Actions...)
	return value
}
