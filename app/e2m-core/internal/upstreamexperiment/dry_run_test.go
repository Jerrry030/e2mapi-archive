package upstreamexperiment

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"e2m.local/contracts"
)

type planOnlySpy struct {
	calls   int
	planID  string
	desired map[string]bool
	result  contracts.ReconcilePlan
}

func (spy *planOnlySpy) PlanScheduling(_ context.Context, planID string, desired map[string]bool) (contracts.ReconcilePlan, error) {
	spy.calls++
	spy.planID = planID
	spy.desired = copyIntent(desired)
	return clonePlan(spy.result), nil
}

func TestDryRunBridgeHasOnlyPlanCapabilityAndProducesCanonicalResult(t *testing.T) {
	recommendation := experimentRecommendation()
	spy := &planOnlySpy{result: previewPlan(recommendation)}
	bridge, err := NewBridge(spy, func() time.Time { return recommendation.CreatedAt.Add(2 * time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	got, err := bridge.DryRun(context.Background(), recommendation)
	if err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 || spy.planID != recommendation.AffectedPlanIDs[0] || !reflect.DeepEqual(spy.desired, map[string]bool{"channel-1": false, "channel-2": true}) {
		t.Fatalf("PlanScheduling call wrong: %+v", spy)
	}
	if got.ReconcileKind != contracts.ReconcileRunDryRun || !got.Plan.DryRun || len(got.ActionSetHash) != 64 || got.ActionHashVersion != contracts.UpstreamExperimentActionHashVersionV1 {
		t.Fatalf("dry-run result wrong: %+v", got)
	}
	if got.ID != "" {
		t.Fatalf("domain layer assigned run identity %q", got.ID)
	}
	// Compile-time surface proof: this dependency intentionally provides no
	// ApplyScheduling method, desired-state mutation, or connector task API.
	var _ SchedulingPlanner = spy
}

func TestDryRunResultCannotPredatePlannerPreview(t *testing.T) {
	recommendation := experimentRecommendation()
	plan := previewPlan(recommendation)
	plan.CreatedAt = recommendation.CreatedAt.Add(3 * time.Minute)
	bridge, err := NewBridge(&planOnlySpy{result: plan}, func() time.Time {
		return recommendation.CreatedAt.Add(2 * time.Minute)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := bridge.DryRun(context.Background(), recommendation)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(plan.CreatedAt) {
		t.Fatalf("result created_at=%s, want planner preview time %s", got.CreatedAt, plan.CreatedAt)
	}
}

func TestDryRunRejectsPlannerReturningNonDryPlan(t *testing.T) {
	recommendation := experimentRecommendation()
	plan := previewPlan(recommendation)
	plan.DryRun = false
	bridge, _ := NewBridge(&planOnlySpy{result: plan}, nil)
	if _, err := bridge.DryRun(context.Background(), recommendation); !errors.Is(err, ErrInvalidDryRun) {
		t.Fatalf("non-dry result accepted: %v", err)
	}
}

func TestActionSetHashStableAcrossOrderDetailAndTime(t *testing.T) {
	recommendation := experimentRecommendation()
	first := previewPlan(recommendation)
	second := previewPlan(recommendation)
	second.Actions[0], second.Actions[1] = second.Actions[1], second.Actions[0]
	second.Actions[0].Detail = "localized display text"
	second.CreatedAt = second.CreatedAt.Add(time.Hour)
	left, err := ActionSetHash(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := ActionSetHash(second)
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("non-semantic fields changed hash: %s != %s", left, right)
	}
}

func TestActionSetHashChangesForEverySemanticActionField(t *testing.T) {
	recommendation := experimentRecommendation()
	base := previewPlan(recommendation)
	want, _ := ActionSetHash(base)
	tests := []func(*contracts.ReconcilePlan){
		func(v *contracts.ReconcilePlan) { v.PlanID = "other-plan" },
		func(v *contracts.ReconcilePlan) { v.InstanceID = "other-instance" },
		func(v *contracts.ReconcilePlan) { v.Actions[0].Type = contracts.ReconcileEnable },
		func(v *contracts.ReconcilePlan) { v.Actions[0].ChannelID = "other-channel" },
		func(v *contracts.ReconcilePlan) { v.Actions[0].RemoteID = "other-remote" },
		func(v *contracts.ReconcilePlan) { v.Actions = v.Actions[:1] },
	}
	for index, mutate := range tests {
		changed := previewPlan(recommendation)
		mutate(&changed)
		got, err := ActionSetHash(changed)
		if err != nil {
			t.Fatal(err)
		}
		if got == want {
			t.Fatalf("semantic mutation %d did not change hash", index)
		}
	}
}

func TestValidatePreviewDetectsStaleFactsGenerationIntentAndActions(t *testing.T) {
	recommendation := experimentRecommendation()
	bridge, _ := NewBridge(&planOnlySpy{result: previewPlan(recommendation)}, func() time.Time { return recommendation.CreatedAt.Add(time.Minute) })
	saved, err := bridge.DryRun(context.Background(), recommendation)
	if err != nil {
		t.Fatal(err)
	}
	current := currentPreview(saved)
	if got := ValidatePreview(saved, current); !got.Current || len(got.Reasons) != 0 {
		t.Fatalf("same preview stale: %+v", got)
	}
	current.IntelligenceFactVersion++
	current.CostLedgerFactVersion++
	current.LinkFactVersion++
	current.PlanGeneration++
	current.RecommendationFingerprint = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	current.DesiredScheduling[current.ToChannelID] = false
	current.Plan.Actions[0].RemoteID = "changed"
	got := ValidatePreview(saved, current)
	for _, reason := range []contracts.UpstreamDryRunStaleReason{
		contracts.UpstreamDryRunStaleFingerprint, contracts.UpstreamDryRunStaleIntelligenceVersion,
		contracts.UpstreamDryRunStaleCostVersion, contracts.UpstreamDryRunStaleLinkVersion,
		contracts.UpstreamDryRunStalePlanGeneration, contracts.UpstreamDryRunStaleIntent,
		contracts.UpstreamDryRunStaleActionSet,
	} {
		assertDryRunStale(t, got, reason)
	}
}

func previewPlan(recommendation contracts.UpstreamRecommendation) contracts.ReconcilePlan {
	return contracts.ReconcilePlan{
		InstanceID: "instance-1", PlanID: recommendation.AffectedPlanIDs[0], DryRun: true,
		Actions: []contracts.ReconcileAction{
			{Type: contracts.ReconcileDisable, ChannelID: recommendation.FromChannelID, RemoteID: "remote-1", Detail: "display"},
			{Type: contracts.ReconcileEnable, ChannelID: recommendation.ToChannelID, RemoteID: "remote-2", Detail: "display"},
		}, CreatedAt: recommendation.CreatedAt.Add(time.Minute),
	}
}

func currentPreview(saved contracts.UpstreamDryRunResult) contracts.UpstreamDryRunCurrent {
	return contracts.UpstreamDryRunCurrent{
		UserID: saved.UserID, RecommendationID: saved.RecommendationID, RecommendationFingerprint: saved.RecommendationFingerprint,
		IntelligenceFactVersion: saved.IntelligenceFactVersion, CostLedgerFactVersion: saved.CostLedgerFactVersion,
		LinkFactVersion: saved.LinkFactVersion, PlanGeneration: saved.PlanGeneration, PlanID: saved.PlanID,
		FromChannelID: saved.FromChannelID, ToChannelID: saved.ToChannelID, DesiredScheduling: copyIntent(saved.DesiredScheduling), Plan: clonePlan(saved.Plan),
	}
}

func assertDryRunStale(t *testing.T, got contracts.UpstreamDryRunValidity, want contracts.UpstreamDryRunStaleReason) {
	t.Helper()
	for _, reason := range got.Reasons {
		if reason == want {
			return
		}
	}
	t.Fatalf("missing stale reason %q in %+v", want, got)
}

func experimentRecommendation() contracts.UpstreamRecommendation {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	return contracts.UpstreamRecommendation{
		ID: "rec-1", UserID: 42, Status: contracts.UpstreamRecommendationReadyForDryRun,
		IntelligenceFactVersion: 7, CostLedgerFactVersion: 9, LinkFactVersion: 4, PlanGeneration: 11,
		FromSourceID: "source-1", FromChannelID: "channel-1", FromGroupKey: "group-a",
		ToSourceID: "source-2", ToChannelID: "channel-2", ToGroupKey: "group-a", ModelKey: "model-a",
		PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", PerTokens: 1_000_000,
		AffectedPlanIDs: []string{"plan-1"}, AffectedDownstreams: []string{"downstream-1"}, EvidenceIDs: []string{"evidence-1"},
		FormulaVersion: contracts.UpstreamRecommendationFormulaVersionV1, StrategyVersion: contracts.UpstreamRecommendationStrategyVersionV1,
		Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
}
