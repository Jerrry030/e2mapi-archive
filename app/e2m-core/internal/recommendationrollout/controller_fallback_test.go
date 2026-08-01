package recommendationrollout

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/upstreamexperiment"
	"e2m.local/core/internal/upstreamrecommendation"
)

type controllerFallbackStore struct {
	*store.MemoryStore
	rollout        contracts.RecommendationRollout
	recommendation contracts.UpstreamRecommendation
	current        store.UpstreamRecommendationInputs
	exactQuality   []contracts.ChannelHealthSnapshot
	qualityAdvance store.QualityOnlyFactAdvanceProof
	dryRun         contracts.UpstreamDryRunResult
	policy         contracts.RecommendationExecutionPolicy

	currentReads  int
	decisionReads int
	enqueueCalls  int
	enqueueAction contracts.RecommendationRolloutOperationAction
	enqueueTarget contracts.RecommendationRolloutStage
	enqueueState  contracts.RecommendationRolloutState
}

func (s *controllerFallbackStore) GetRecommendationRollout(context.Context, string) (contracts.RecommendationRollout, error) {
	return s.rollout, nil
}

func (s *controllerFallbackStore) GetUpstreamRecommendation(context.Context, int64, string) (contracts.UpstreamRecommendation, error) {
	return s.recommendation, nil
}

func (s *controllerFallbackStore) ReadUpstreamRecommendationInputs(context.Context, int64) (store.UpstreamRecommendationInputs, error) {
	s.currentReads++
	return cloneControllerFallbackSnapshot(s.current), nil
}

func (s *controllerFallbackStore) ReadRecommendationRolloutDecisionInputs(context.Context, int64, string) (store.RecommendationRolloutDecisionInputs, error) {
	s.decisionReads++
	qualityAdvance := s.qualityAdvance
	qualityAdvance.Mutations = append([]store.UpstreamIntelligenceFactMutation(nil), s.qualityAdvance.Mutations...)
	return store.RecommendationRolloutDecisionInputs{
		Recommendation:         s.recommendation,
		Current:                cloneControllerFallbackSnapshot(s.current),
		ExactQualityEvidence:   append([]contracts.ChannelHealthSnapshot(nil), s.exactQuality...),
		QualityOnlyFactAdvance: qualityAdvance,
	}, nil
}

func (s *controllerFallbackStore) GetUpstreamDryRunResult(context.Context, int64, string) (contracts.UpstreamDryRunResult, error) {
	return s.dryRun, nil
}

func (s *controllerFallbackStore) GetRecommendationExecutionPolicy(context.Context, int64, contracts.RecommendationExecutionScope, string) (contracts.RecommendationExecutionPolicy, error) {
	return s.policy, nil
}

func (s *controllerFallbackStore) GetRecommendationRolloutExecutionStats(context.Context, store.RecommendationRolloutExecutionStatsFilter) (store.RecommendationRolloutExecutionStats, error) {
	return store.RecommendationRolloutExecutionStats{}, nil
}

func (s *controllerFallbackStore) EnqueueRecommendationRolloutOperation(_ context.Context, _ string, _ int64, next contracts.RecommendationRolloutState, action contracts.RecommendationRolloutOperationAction, target contracts.RecommendationRolloutStage) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, error) {
	s.enqueueCalls++
	s.enqueueAction, s.enqueueTarget, s.enqueueState = action, target, next
	updated := s.rollout
	updated.State = next
	updated.Version++
	operation := contracts.RecommendationRolloutOperation{
		ID: "operation-rollback", RolloutID: s.rollout.State.ID, UserID: s.rollout.State.UserID,
		PlanID: s.rollout.State.PlanID, Action: action, TargetStage: target,
		Status: contracts.RecommendationRolloutOperationPending,
	}
	updated.LastOperationID = operation.ID
	return updated, operation, nil
}

type controllerFallbackPlanner struct {
	plan  contracts.ReconcilePlan
	calls int
}

func (p *controllerFallbackPlanner) PlanScheduling(context.Context, string, map[string]bool) (contracts.ReconcilePlan, error) {
	p.calls++
	return p.plan, nil
}

func TestControllerAdvanceUsesQualityOnlyFallbackAndRollsBackExactlyQualityFailed(t *testing.T) {
	fixture, controller, planner := newControllerFallbackFixture(t)
	got, err := controller.Advance(context.Background(), fixture.rollout.State.UserID, fixture.rollout.State.ID)
	if !errors.Is(err, ErrControllerBlocked) {
		t.Fatalf("Advance error=%v, want ErrControllerBlocked", err)
	}
	if fixture.currentReads != 1 || fixture.decisionReads != 1 || planner.calls != 1 {
		t.Fatalf("path counters current=%d decision=%d planner=%d", fixture.currentReads, fixture.decisionReads, planner.calls)
	}
	if fixture.enqueueCalls != 1 || fixture.enqueueAction != contracts.RecommendationRolloutOperationRollback || fixture.enqueueTarget != contracts.RecommendationRolloutStageNone {
		t.Fatalf("enqueue calls=%d action=%s target=%d", fixture.enqueueCalls, fixture.enqueueAction, fixture.enqueueTarget)
	}
	if got.Rollout.State.Status != contracts.RecommendationRolloutRollbackRequired || len(got.Rollout.State.RollbackReasons) != 1 ||
		got.Rollout.State.RollbackReasons[0] != contracts.RecommendationRolloutBlockedQuality {
		t.Fatalf("typed rollback state=%+v", got.Rollout.State)
	}
	if got.Operation == nil || got.Operation.ID != "operation-rollback" || got.Rollout.LastOperationID != got.Operation.ID ||
		got.Operation.RolloutID != got.Rollout.State.ID || got.Operation.UserID != got.Rollout.State.UserID || got.Operation.PlanID != got.Rollout.State.PlanID ||
		got.Operation.Action != contracts.RecommendationRolloutOperationRollback || got.Operation.TargetStage != contracts.RecommendationRolloutStageNone ||
		got.Operation.Status != contracts.RecommendationRolloutOperationPending {
		t.Fatalf("atomic rollback operation=%+v", got.Operation)
	}
	if got.Rollout.State.LastAfterEvidence == nil || got.Rollout.State.LastAfterEvidence.Callability != contracts.RecommendationRolloutGatePassed ||
		got.Rollout.State.LastAfterEvidence.Quality != contracts.RecommendationRolloutGateBlocked || len(got.Rollout.State.LastAfterEvidence.EvidenceIDs) != 4 {
		t.Fatalf("after evidence=%+v", got.Rollout.State.LastAfterEvidence)
	}
	wantAfterIDs := []string{"binding-expensive", "binding-cheap", "quality-expensive-after", "quality-cheap-after"}
	if !sameRolloutIDs(got.Rollout.State.LastAfterEvidence.EvidenceIDs, wantAfterIDs) {
		t.Fatalf("after evidence ids=%v want=%v", got.Rollout.State.LastAfterEvidence.EvidenceIDs, wantAfterIDs)
	}
}

func TestControllerQualityFallbackRejectsNonQualityAndMalformedEvidenceChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*controllerFallbackStore, *controllerFallbackPlanner)
	}{
		{"cost version", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) { s.current.CostLedgerFactVersion++ }},
		{"offer price", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			changed := contracts.CanonicalDecimal("11")
			s.current.Intelligence.Offers[0].EffectiveUnitCost = &changed
		}},
		{"wallet balance", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			changed := contracts.CanonicalDecimal("99")
			s.current.Intelligence.Wallets[0].ID = "wallet-expensive-after"
			s.current.Intelligence.Wallets[0].BalanceAmount = &changed
		}},
		{"link inactive", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			s.current.Intelligence.Links[0].Status = contracts.UpstreamLinkInactive
		}},
		{"mapping", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			s.current.Intelligence.LinkResolutions[0].TargetVerified = false
		}},
		{"plan generation", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			s.current.RoutePlans[0].SchedulingGeneration++
		}},
		{"lifecycle", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			s.current.Channels[0].Status = contracts.UpstreamChannelMaintenance
		}},
		{"policy", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) { s.policy.Enabled = false }},
		{"dry run action set", func(_ *controllerFallbackStore, p *controllerFallbackPlanner) {
			p.plan.Actions = append(p.plan.Actions, contracts.ReconcileAction{Type: contracts.ReconcileHold, ChannelID: "channel-expensive"})
		}},
		{"binding", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			s.current.Bindings[0].VerificationStatus = contracts.BindingVerificationPublishedPending
		}},
		{"missing exact evidence", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) { s.exactQuality = s.exactQuality[:1] }},
		{"wrong scope exact evidence", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			s.exactQuality[0].InstanceID = "foreign"
		}},
		{"future exact evidence", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			s.exactQuality[0].CreatedAt = s.recommendation.CreatedAt.Add(time.Second)
		}},
		{"future post quality", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			s.current.Intelligence.QualitySnapshots[0].CreatedAt = s.current.GeneratedAt.Add(time.Second)
		}},
		{"nan post quality", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			s.current.Intelligence.QualitySnapshots[0].QualityScore = math.NaN()
		}},
		{"lineage incomplete", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			s.qualityAdvance.Complete = false
		}},
		{"lineage gap", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			s.qualityAdvance.Mutations = nil
		}},
		{"lineage non quality", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			s.qualityAdvance.Mutations[0].Kind = store.UpstreamIntelligenceFactMutationCollection
		}},
		{"lineage baseline before watermark", func(s *controllerFallbackStore, _ *controllerFallbackPlanner) {
			s.qualityAdvance.LineageWatermark = s.qualityAdvance.BaselineFactVersion + 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, controller, planner := newControllerFallbackFixture(t)
			test.mutate(fixture, planner)
			got, err := controller.Advance(context.Background(), fixture.rollout.State.UserID, fixture.rollout.State.ID)
			if err != nil {
				t.Fatalf("generic gate rollback error=%v", err)
			}
			if fixture.enqueueCalls != 1 || fixture.enqueueAction != contracts.RecommendationRolloutOperationRollback || fixture.enqueueTarget != contracts.RecommendationRolloutStageNone {
				t.Fatalf("enqueue calls=%d action=%s target=%d", fixture.enqueueCalls, fixture.enqueueAction, fixture.enqueueTarget)
			}
			if got.Rollout.State.LastAfterEvidence != nil || len(got.Rollout.State.RollbackReasons) != 1 || got.Rollout.State.RollbackReasons[0] != contracts.RecommendationRolloutBlockedGate {
				t.Fatalf("non-quality change was mistyped: %+v", got.Rollout.State)
			}
		})
	}
}

func TestSameRecommendationExceptRefreshedQualityAllowsOnlyCanonicalQualityRefresh(t *testing.T) {
	snapshot := controllerFallbackGeneratorSnapshot(time.Date(2026, 7, 27, 4, 0, 0, 0, time.UTC), "instance-1")
	saved := mustControllerFallbackRecommendation(t, snapshot, "saved")
	saved.Status, saved.DryRunID = contracts.UpstreamRecommendationDryRunPassed, "dry-run-1"
	currentSnapshot := cloneControllerFallbackSnapshot(snapshot)
	currentSnapshot.GeneratedAt = currentSnapshot.GeneratedAt.Add(time.Minute)
	currentSnapshot.Intelligence.GeneratedAt = currentSnapshot.GeneratedAt
	currentSnapshot.Intelligence.FactVersion.FactVersion++
	currentSnapshot.Intelligence.FactVersion.UpdatedAt = currentSnapshot.GeneratedAt
	for index := range currentSnapshot.Intelligence.QualitySnapshots {
		currentSnapshot.Intelligence.QualitySnapshots[index].ID += "-refreshed"
		currentSnapshot.Intelligence.QualitySnapshots[index].CreatedAt = currentSnapshot.GeneratedAt.Add(-time.Second)
	}
	current := mustControllerFallbackRecommendation(t, currentSnapshot, "current")
	if !sameRecommendationExceptRefreshedQuality(saved, current) {
		t.Fatal("canonical quality refresh was rejected")
	}

	for _, test := range []struct {
		name   string
		mutate func(*store.UpstreamRecommendationInputs)
	}{
		{"version not advanced", func(v *store.UpstreamRecommendationInputs) {
			v.Intelligence.FactVersion.FactVersion = saved.IntelligenceFactVersion
		}},
		{"cost version", func(v *store.UpstreamRecommendationInputs) { v.CostLedgerFactVersion++ }},
		{"plan generation", func(v *store.UpstreamRecommendationInputs) {
			v.RoutePlans[0].SchedulingGeneration++
			for index := range v.Bindings {
				v.Bindings[index].SchedulingGeneration++
			}
		}},
		{"offer identity", func(v *store.UpstreamRecommendationInputs) {
			v.Intelligence.Offers[0].ID = "offer-replaced"
			v.CostFacts[0].PriceObservationID = "offer-replaced"
		}},
		{"cost identity", func(v *store.UpstreamRecommendationInputs) { v.CostFacts[0].ID = "cost-replaced" }},
		{"wallet identity", func(v *store.UpstreamRecommendationInputs) { v.Intelligence.Wallets[0].ID = "wallet-replaced" }},
		{"link identity", func(v *store.UpstreamRecommendationInputs) {
			v.Intelligence.Links[0].ID = "link-replaced"
			v.Intelligence.LinkResolutions[0].LinkID = "link-replaced"
		}},
		{"binding identity", func(v *store.UpstreamRecommendationInputs) { v.Bindings[0].ID = "binding-replaced" }},
		{"price", func(v *store.UpstreamRecommendationInputs) {
			changed := contracts.CanonicalDecimal("11")
			v.Intelligence.Offers[0].EffectiveUnitCost = &changed
			v.CostFacts[0].UnitCost, v.CostFacts[0].Amount = &changed, &changed
		}},
		{"mapping", func(v *store.UpstreamRecommendationInputs) {
			v.Intelligence.Links[0].ChannelID = "channel-expensive-v2"
			v.Intelligence.LinkResolutions[0].ResolvedChannelID = "channel-expensive-v2"
			v.Channels[0].ID = "channel-expensive-v2"
			v.Bindings[0].ChannelID = "channel-expensive-v2"
			v.Intelligence.QualitySnapshots[0].ChannelID = "channel-expensive-v2"
			v.CostFacts[0].ChannelID = "channel-expensive-v2"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneControllerFallbackSnapshot(currentSnapshot)
			test.mutate(&candidate)
			changed := mustControllerFallbackRecommendation(t, candidate, "changed")
			if sameRecommendationExceptRefreshedQuality(saved, changed) {
				t.Fatalf("%s was accepted", test.name)
			}
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*contracts.UpstreamRecommendation)
	}{
		{"fingerprint", func(v *contracts.UpstreamRecommendation) { v.Fingerprint = saved.Fingerprint }},
		{"owner", func(v *contracts.UpstreamRecommendation) { v.UserID++ }},
		{"currency", func(v *contracts.UpstreamRecommendation) { v.SettlementCurrency = "CNY" }},
		{"per tokens", func(v *contracts.UpstreamRecommendation) { v.PerTokens++ }},
		{"formula", func(v *contracts.UpstreamRecommendation) { v.FormulaVersion = "formula-changed" }},
		{"strategy", func(v *contracts.UpstreamRecommendation) { v.StrategyVersion = "strategy-changed" }},
		{"quality status", func(v *contracts.UpstreamRecommendation) {
			for index := range v.Constraints {
				if v.Constraints[index].Kind == contracts.UpstreamRecommendationConstraintQuality {
					v.Constraints[index].Status = contracts.UpstreamRecommendationConstraintUnknown
					v.Constraints[index].ReasonCode = "unknown"
				}
			}
		}},
		{"link version detached", func(v *contracts.UpstreamRecommendation) { v.LinkFactVersion-- }},
		{"ttl changed", func(v *contracts.UpstreamRecommendation) { v.ExpiresAt = v.ExpiresAt.Add(time.Second) }},
		{"created at reversed", func(v *contracts.UpstreamRecommendation) {
			v.CreatedAt = saved.CreatedAt.Add(-time.Second)
			v.ExpiresAt = v.CreatedAt.Add(time.Hour)
		}},
		{"missing global quality", func(v *contracts.UpstreamRecommendation) {
			quality, _ := recommendationConstraintEvidence(*v, contracts.UpstreamRecommendationConstraintQuality)
			for index, id := range v.EvidenceIDs {
				if id == quality[0] {
					v.EvidenceIDs[index] = "unrelated-evidence"
					break
				}
			}
		}},
		{"duplicate global evidence", func(v *contracts.UpstreamRecommendation) { v.EvidenceIDs[1] = v.EvidenceIDs[0] }},
		{"duplicate quality constraint", func(v *contracts.UpstreamRecommendation) {
			for _, constraint := range v.Constraints {
				if constraint.Kind == contracts.UpstreamRecommendationConstraintQuality {
					v.Constraints = append(v.Constraints, constraint)
					break
				}
			}
		}},
		{"duplicate quality id", func(v *contracts.UpstreamRecommendation) {
			for index := range v.Constraints {
				if v.Constraints[index].Kind == contracts.UpstreamRecommendationConstraintQuality {
					v.Constraints[index].EvidenceIDs[1] = v.Constraints[index].EvidenceIDs[0]
				}
			}
		}},
		{"blank quality id", func(v *contracts.UpstreamRecommendation) {
			for index := range v.Constraints {
				if v.Constraints[index].Kind == contracts.UpstreamRecommendationConstraintQuality {
					v.Constraints[index].EvidenceIDs[0] = " "
				}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneControllerFallbackRecommendation(current)
			test.mutate(&changed)
			if sameRecommendationExceptRefreshedQuality(saved, changed) {
				t.Fatalf("%s was accepted", test.name)
			}
		})
	}
}

func newControllerFallbackFixture(t *testing.T) (*controllerFallbackStore, *Controller, *controllerFallbackPlanner) {
	t.Helper()
	createdAt := time.Date(2026, 7, 27, 4, 0, 0, 0, time.UTC)
	currentAt := createdAt.Add(10 * time.Minute)
	observeUntil := currentAt.Add(-time.Minute)
	instanceID := "instance-1"
	snapshot := controllerFallbackGeneratorSnapshot(createdAt, instanceID)
	generated, err := upstreamrecommendation.Generate(recommendationGeneratorInputs(snapshot), func() string { return "recommendation-1" })
	if err != nil || len(generated.Recommendations) != 1 {
		t.Fatalf("generate=%+v err=%v", generated, err)
	}
	recommendation := generated.Recommendations[0]
	recommendation.Status = contracts.UpstreamRecommendationDryRunPassed
	recommendation.DryRunID = "dry-run-1"

	planner := &controllerFallbackPlanner{plan: contracts.ReconcilePlan{
		InstanceID: instanceID, PlanID: "plan-1", DryRun: true,
		Actions:   []contracts.ReconcileAction{{Type: contracts.ReconcileDisable, ChannelID: recommendation.FromChannelID}, {Type: contracts.ReconcileEnable, ChannelID: recommendation.ToChannelID}},
		CreatedAt: createdAt,
	}}
	bridge, err := upstreamexperiment.NewBridge(planner, func() time.Time { return createdAt })
	if err != nil {
		t.Fatal(err)
	}
	planning := recommendation
	planning.Status, planning.DryRunID = contracts.UpstreamRecommendationReadyForDryRun, ""
	dryRun, err := bridge.DryRun(context.Background(), planning)
	if err != nil {
		t.Fatal(err)
	}
	dryRun.ID = recommendation.DryRunID
	planner.calls = 0

	current := cloneControllerFallbackSnapshot(snapshot)
	current.GeneratedAt, current.Intelligence.GeneratedAt = currentAt, currentAt
	current.Intelligence.FactVersion.FactVersion++
	current.Intelligence.FactVersion.UpdatedAt = currentAt
	current.RoutePlans[0].SchedulingGeneration++
	for index := range current.Intelligence.Offers {
		current.Intelligence.Offers[index].FreshUntil = currentAt.Add(time.Hour)
		current.Intelligence.Wallets[index].FreshUntil = currentAt.Add(time.Hour)
	}
	historical := append([]contracts.ChannelHealthSnapshot(nil), snapshot.Intelligence.QualitySnapshots...)
	current.Intelligence.QualitySnapshots = []contracts.ChannelHealthSnapshot{
		healthyAfter("quality-expensive-after", recommendation.FromChannelID, currentAt.Add(-30*time.Second)),
		healthyAfter("quality-cheap-after", recommendation.ToChannelID, currentAt.Add(-30*time.Second)),
	}
	current.Intelligence.QualitySnapshots[1].HealthState = contracts.HealthUnhealthy
	current.Intelligence.QualitySnapshots[1].QualitySuccessRate = .94
	for index := range current.Intelligence.QualitySnapshots {
		current.Intelligence.QualitySnapshots[index].InstanceID = instanceID
	}
	for index := range current.Bindings {
		verifiedAt := currentAt.Add(-30 * time.Second)
		current.Bindings[index].VerifiedAt = &verifiedAt
		current.Bindings[index].VerificationSource = contracts.BindingVerificationSourcePassive
		current.Bindings[index].VerificationStatus = contracts.BindingVerificationPassiveVerified
	}

	baseline := []contracts.RecommendationRolloutAccountWeight{{AccountID: "account-expensive", Weight: 80}, {AccountID: "account-cheap", Weight: 10}, {AccountID: "account-other", Weight: 10}}
	baselineFingerprint, err := contracts.RecommendationRolloutBaselineFingerprint(baseline)
	if err != nil {
		t.Fatal(err)
	}
	state := contracts.RecommendationRolloutState{
		ID: "rollout-1", UserID: recommendation.UserID, PlanID: "plan-1", RecommendationID: recommendation.ID,
		RecommendationFingerprint: recommendation.Fingerprint, FactVersion: recommendation.IntelligenceFactVersion,
		EvidenceIDs: append([]string(nil), recommendation.EvidenceIDs...), BaselineFingerprint: baselineFingerprint,
		SchedulingGeneration: current.RoutePlans[0].SchedulingGeneration, Status: contracts.RecommendationRolloutObserving,
		Stage: contracts.RecommendationRolloutStage10, ObservationSeconds: 300, RecommendationExpiresAt: recommendation.ExpiresAt,
		StartedAt: createdAt, StageStartedAt: ptrTime(observeUntil.Add(-5 * time.Minute)), ObserveUntil: &observeUntil, UpdatedAt: observeUntil,
	}
	fixture := &controllerFallbackStore{
		MemoryStore: store.NewMemoryStore(createdAt), recommendation: recommendation, current: current, exactQuality: historical, dryRun: dryRun,
		qualityAdvance: store.QualityOnlyFactAdvanceProof{
			UserID: recommendation.UserID, BaselineFactVersion: recommendation.IntelligenceFactVersion,
			CurrentFactVersion: current.Intelligence.FactVersion.FactVersion,
			LineageWatermark:   recommendation.IntelligenceFactVersion, Complete: true,
			Mutations: []store.UpstreamIntelligenceFactMutation{{
				UserID: recommendation.UserID, FactVersion: current.Intelligence.FactVersion.FactVersion,
				Kind: store.UpstreamIntelligenceFactMutationQuality, EvidenceID: "quality-cheap-after", CreatedAt: currentAt,
			}},
		},
		rollout: contracts.RecommendationRollout{State: state, InstanceID: instanceID, FromChannelID: recommendation.FromChannelID, ToChannelID: recommendation.ToChannelID,
			RecommendationPlanGeneration: recommendation.PlanGeneration, FromAccountID: "account-expensive", ToAccountID: "account-cheap", BaselineWeights: baseline, Version: 5, CreatedAt: createdAt},
		policy: contracts.RecommendationExecutionPolicy{ID: "policy-1", UserID: recommendation.UserID, Scope: contracts.RecommendationExecutionScopePlan,
			PlanID: "plan-1", Enabled: true, DailyExecutionCap: 10, MinimumSavings: "0", Version: 1},
	}
	controller, err := NewController(fixture, &workerGatewayFixture{}, planner, 300)
	if err != nil {
		t.Fatal(err)
	}
	controller.now = func() time.Time { return currentAt }
	return fixture, controller, planner
}

func controllerFallbackGeneratorSnapshot(now time.Time, instanceID string) store.UpstreamRecommendationInputs {
	userID := int64(42)
	completed, verified := now.Add(-2*time.Minute), now.Add(-time.Hour)
	prices := []contracts.CanonicalDecimal{"10", "6"}
	sources := []string{"source-expensive", "source-cheap"}
	channels := []string{"channel-expensive", "channel-cheap"}
	result := store.UpstreamRecommendationInputs{
		UserID: userID, GeneratedAt: now, CostLedgerFactVersion: 9,
		Intelligence: store.UpstreamIntelligenceCurrentSnapshot{UserID: userID, GeneratedAt: now,
			FactVersion: contracts.UpstreamIntelligenceFactVersion{UserID: userID, FactVersion: 7, UpdatedAt: now}},
		RoutePlans: []contracts.RoutePlan{{ID: "plan-1", UserID: userID, InstanceID: instanceID, PoolID: "pool-1", Status: contracts.RoutePlanPublished, SchedulingGeneration: 19}},
	}
	for index := range sources {
		suffix := "expensive"
		if index == 1 {
			suffix = "cheap"
		}
		price, balance, quantity := prices[index], contracts.CanonicalDecimal("100"), int64(1_000_000)
		runID, offerID := "run-"+suffix, "offer-"+suffix
		result.Intelligence.Sources = append(result.Intelligence.Sources, contracts.UpstreamIntelligenceSource{ID: sources[index], UserID: userID, Status: contracts.UpstreamSourceActive})
		result.Intelligence.LatestRuns = append(result.Intelligence.LatestRuns, contracts.UpstreamCollectionRun{ID: runID, UserID: userID, SourceID: sources[index], Status: contracts.UpstreamCollectionSucceeded, Coverage: contracts.UpstreamCoverageComplete, ObservedAt: now.Add(-3 * time.Minute), CompletedAt: &completed, FinalizedFactVersion: int64(6 + index)})
		result.Intelligence.Wallets = append(result.Intelligence.Wallets, contracts.UpstreamWalletObservation{ID: "wallet-" + suffix, RunID: runID, UserID: userID, SourceID: sources[index], BalanceAmount: &balance, UnitKind: contracts.UpstreamWalletFiat, Currency: "USD", Accuracy: contracts.UpstreamEvidenceExact, Coverage: contracts.UpstreamCoverageComplete, ObservedAt: now.Add(-3 * time.Minute), FreshUntil: now.Add(time.Hour)})
		result.Intelligence.Offers = append(result.Intelligence.Offers, contracts.UpstreamOfferObservation{ID: offerID, RunID: runID, UserID: userID, SourceID: sources[index], GroupKey: "paid", ModelKey: "gpt-test", PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", PerTokens: 1_000_000, EffectiveUnitCost: &price, FormulaVersion: "effective-cost/v1", Accuracy: contracts.UpstreamEvidenceExact, Coverage: contracts.UpstreamCoverageComplete, ObservedAt: now.Add(-3 * time.Minute), EffectiveAt: now.Add(-time.Hour), FreshUntil: now.Add(time.Hour)})
		result.Intelligence.Links = append(result.Intelligence.Links, contracts.UpstreamIntelligenceLink{ID: "link-" + suffix, UserID: userID, IntelligenceSourceID: sources[index], Scope: contracts.UpstreamLinkChannel, ChannelID: channels[index], PriceDimension: contracts.UpstreamPriceInput, Status: contracts.UpstreamLinkActive, VerifiedAt: &verified})
		result.Intelligence.LinkResolutions = append(result.Intelligence.LinkResolutions, store.UpstreamIntelligenceLinkResolution{LinkID: "link-" + suffix, UserID: userID, ResolvedChannelID: channels[index], ResolvedChannelOwnerID: userID, TargetVerified: true})
		result.Intelligence.QualitySnapshots = append(result.Intelligence.QualitySnapshots, contracts.ChannelHealthSnapshot{ID: "quality-" + suffix, ChannelID: channels[index], InstanceID: instanceID, Model: "gpt-test", Window: contracts.Window5m, QualitySampleCount: 20, QualitySuccessRate: .99, TTFTP95: 500, DurationP95: 1200, QualityScore: 95, HealthState: contracts.HealthHealthy, CreatedAt: now.Add(-time.Minute)})
		result.Channels = append(result.Channels, contracts.UpstreamChannel{ID: channels[index], PoolID: "pool-1", Models: []string{"gpt-test"}, Groups: []string{"paid"}, Status: contracts.UpstreamChannelActive, InventoryState: contracts.UpstreamInventoryReady})
		result.Bindings = append(result.Bindings, contracts.PublishedBinding{ID: "binding-" + suffix, PlanID: "plan-1", InstanceID: instanceID, ChannelID: channels[index], RemoteID: "account-" + suffix, State: contracts.BindingActive, VerificationStatus: contracts.BindingVerificationPassiveVerified, SchedulingGeneration: 19})
		priceAt := now.Add(-time.Hour)
		result.CostFacts = append(result.CostFacts, contracts.UpstreamCostFact{ID: "cost-" + suffix, UserID: userID, FactVersion: 9, UsageObservationID: "usage-" + suffix, ChannelID: channels[index], InstanceID: instanceID, IntelligenceSourceID: sources[index], ModelKey: "gpt-test", GroupKey: "paid", PriceDimension: contracts.UpstreamPriceInput, Quantity: &quantity, PerTokens: 1_000_000, PriceObservationID: offerID, PriceEffectiveAt: &priceAt, UnitCost: &price, Amount: &price, Currency: "USD", Attribution: contracts.UpstreamCostExact, PriceStatus: contracts.UpstreamCostPriceValid, CalculationVersion: contracts.UpstreamCostCalculationVersionV1, OccurredAt: now.Add(-30 * time.Second)})
	}
	return result
}

func cloneControllerFallbackSnapshot(value store.UpstreamRecommendationInputs) store.UpstreamRecommendationInputs {
	result := value
	result.Intelligence = value.Intelligence
	result.Intelligence.Sources = append([]contracts.UpstreamIntelligenceSource(nil), value.Intelligence.Sources...)
	result.Intelligence.LatestRuns = append([]contracts.UpstreamCollectionRun(nil), value.Intelligence.LatestRuns...)
	result.Intelligence.Wallets = append([]contracts.UpstreamWalletObservation(nil), value.Intelligence.Wallets...)
	result.Intelligence.Offers = append([]contracts.UpstreamOfferObservation(nil), value.Intelligence.Offers...)
	result.Intelligence.Links = append([]contracts.UpstreamIntelligenceLink(nil), value.Intelligence.Links...)
	result.Intelligence.LinkResolutions = append([]store.UpstreamIntelligenceLinkResolution(nil), value.Intelligence.LinkResolutions...)
	result.Intelligence.QualitySnapshots = append([]contracts.ChannelHealthSnapshot(nil), value.Intelligence.QualitySnapshots...)
	result.CostFacts = append([]contracts.UpstreamCostFact(nil), value.CostFacts...)
	result.RoutePlans = append([]contracts.RoutePlan(nil), value.RoutePlans...)
	result.Channels = append([]contracts.UpstreamChannel(nil), value.Channels...)
	result.Bindings = append([]contracts.PublishedBinding(nil), value.Bindings...)
	return result
}

func mustControllerFallbackRecommendation(t *testing.T, snapshot store.UpstreamRecommendationInputs, id string) contracts.UpstreamRecommendation {
	t.Helper()
	generated, err := upstreamrecommendation.Generate(recommendationGeneratorInputs(snapshot), func() string { return id })
	if err != nil || len(generated.Recommendations) != 1 {
		t.Fatalf("generate %s: result=%+v err=%v", id, generated, err)
	}
	return generated.Recommendations[0]
}

func cloneControllerFallbackRecommendation(value contracts.UpstreamRecommendation) contracts.UpstreamRecommendation {
	result := value
	result.AffectedPlanIDs = append([]string(nil), value.AffectedPlanIDs...)
	result.AffectedDownstreams = append([]string(nil), value.AffectedDownstreams...)
	result.EvidenceIDs = append([]string(nil), value.EvidenceIDs...)
	result.Constraints = cloneRolloutConstraints(value.Constraints)
	return result
}
