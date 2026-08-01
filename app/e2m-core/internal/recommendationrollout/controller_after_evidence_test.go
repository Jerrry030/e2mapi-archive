package recommendationrollout

import (
	"errors"
	"math"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

func TestAfterEvidenceRequiresFreshBindingCallability(t *testing.T) {
	observeUntil := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	generatedAt := observeUntil.Add(time.Minute)
	rollout := contracts.RecommendationRollout{State: contracts.RecommendationRolloutState{
		PlanID: "plan-1", RecommendationFingerprint: "fp-1", SchedulingGeneration: 7,
		Status: contracts.RecommendationRolloutObserving, Stage: contracts.RecommendationRolloutStage10,
		StageStartedAt: ptrTime(observeUntil.Add(-time.Minute)), ObserveUntil: &observeUntil,
	}}
	recommendation := contracts.UpstreamRecommendation{
		Fingerprint: "fp-1", FromChannelID: "from", ToChannelID: "to", ModelKey: "gpt-test",
		AffectedDownstreams: []string{"instance-1"},
	}
	snapshot := store.UpstreamRecommendationInputs{
		GeneratedAt: generatedAt,
		Intelligence: store.UpstreamIntelligenceCurrentSnapshot{QualitySnapshots: []contracts.ChannelHealthSnapshot{
			healthyAfter("quality-from", "from", generatedAt), healthyAfter("quality-to", "to", generatedAt),
		}},
		Bindings: []contracts.PublishedBinding{
			verifiedAfter("binding-from", "from", generatedAt), verifiedAfter("binding-to", "to", generatedAt),
		},
	}
	got, err := afterEvidenceFromSnapshot(rollout, recommendation, snapshot)
	if err != nil || got.Callability != contracts.RecommendationRolloutGatePassed || len(got.EvidenceIDs) != 4 {
		t.Fatalf("fresh callability was not accepted: got=%+v err=%v", got, err)
	}

	snapshot.Bindings[1].VerificationStatus = contracts.BindingVerificationAwaitingFirstRequest
	snapshot.Bindings[1].VerificationSource = contracts.BindingVerificationSourcePublish
	snapshot.Bindings[1].VerifiedAt = nil
	if _, err := afterEvidenceFromSnapshot(rollout, recommendation, snapshot); !errors.Is(err, ErrControllerBlocked) {
		t.Fatalf("awaiting binding must block callability, err=%v", err)
	}

	snapshot.Bindings[1] = verifiedAfter("binding-to", "to", observeUntil.Add(-time.Second))
	if _, err := afterEvidenceFromSnapshot(rollout, recommendation, snapshot); !errors.Is(err, ErrControllerBlocked) {
		t.Fatalf("pre-boundary verification must block callability, err=%v", err)
	}
}

func TestAfterEvidencePreservesCallableUnhealthySnapshotAsQualityFailure(t *testing.T) {
	observeUntil := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	generatedAt := observeUntil.Add(time.Minute)
	rollout := contracts.RecommendationRollout{State: contracts.RecommendationRolloutState{
		PlanID: "plan-1", RecommendationFingerprint: "fp-1", SchedulingGeneration: 7,
		Status: contracts.RecommendationRolloutObserving, Stage: contracts.RecommendationRolloutStage10,
		StageStartedAt: ptrTime(observeUntil.Add(-time.Minute)), ObserveUntil: &observeUntil,
	}}
	recommendation := contracts.UpstreamRecommendation{
		Fingerprint: "fp-1", FromChannelID: "from", ToChannelID: "to", ModelKey: "gpt-test",
		AffectedDownstreams: []string{"instance-1"},
	}
	unhealthyFrom := healthyAfter("quality-from", "from", generatedAt)
	unhealthyFrom.HealthState, unhealthyFrom.QualitySuccessRate = contracts.HealthUnhealthy, 0
	unhealthyTo := healthyAfter("quality-to", "to", generatedAt)
	unhealthyTo.HealthState, unhealthyTo.QualitySuccessRate = contracts.HealthUnhealthy, 0
	snapshot := store.UpstreamRecommendationInputs{
		GeneratedAt: generatedAt,
		Intelligence: store.UpstreamIntelligenceCurrentSnapshot{QualitySnapshots: []contracts.ChannelHealthSnapshot{
			healthyAfter("baseline-quality-from", "from", observeUntil.Add(-time.Minute)),
			healthyAfter("baseline-quality-to", "to", observeUntil.Add(-time.Minute)),
			unhealthyFrom, unhealthyTo,
		}},
		Bindings: []contracts.PublishedBinding{
			verifiedAfter("binding-from", "from", generatedAt), verifiedAfter("binding-to", "to", generatedAt),
		},
	}
	got, err := afterEvidenceFromSnapshot(rollout, recommendation, snapshot)
	if err != nil || got.Callability != contracts.RecommendationRolloutGatePassed || got.Quality != contracts.RecommendationRolloutGateBlocked || len(got.EvidenceIDs) != 4 {
		t.Fatalf("callable quality regression was not typed exactly: got=%+v err=%v", got, err)
	}
}

func TestAfterEvidenceBlocksQualityWhenEitherCompleteSideRegresses(t *testing.T) {
	observeUntil := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	generatedAt := observeUntil.Add(time.Minute)
	rollout := contracts.RecommendationRollout{State: contracts.RecommendationRolloutState{
		PlanID: "plan-1", RecommendationFingerprint: "fp-1", SchedulingGeneration: 7,
		Status: contracts.RecommendationRolloutObserving, Stage: contracts.RecommendationRolloutStage10,
		StageStartedAt: ptrTime(observeUntil.Add(-time.Minute)), ObserveUntil: &observeUntil,
	}}
	recommendation := contracts.UpstreamRecommendation{
		Fingerprint: "fp-1", FromChannelID: "from", ToChannelID: "to", ModelKey: "gpt-test",
		AffectedDownstreams: []string{"instance-1"},
	}
	unhealthy := healthyAfter("quality-from", "from", generatedAt)
	unhealthy.QualitySuccessRate = .94
	snapshot := store.UpstreamRecommendationInputs{
		GeneratedAt: generatedAt,
		Intelligence: store.UpstreamIntelligenceCurrentSnapshot{QualitySnapshots: []contracts.ChannelHealthSnapshot{
			unhealthy, healthyAfter("quality-to", "to", generatedAt),
		}},
		Bindings: []contracts.PublishedBinding{
			verifiedAfter("binding-from", "from", generatedAt), verifiedAfter("binding-to", "to", generatedAt),
		},
	}
	got, err := afterEvidenceFromSnapshot(rollout, recommendation, snapshot)
	if err != nil || got.Quality != contracts.RecommendationRolloutGateBlocked {
		t.Fatalf("one-sided quality regression was not typed: got=%+v err=%v", got, err)
	}
}

func TestRecommendationQualityPredicateMatchesGeneratorThresholds(t *testing.T) {
	base := healthyAfter("quality", "from", time.Now().UTC())
	if !qualitySnapshotPassesRecommendation(base) {
		t.Fatal("healthy baseline rejected")
	}
	for _, test := range []struct {
		name   string
		mutate func(*contracts.ChannelHealthSnapshot)
	}{
		{"samples", func(v *contracts.ChannelHealthSnapshot) { v.QualitySampleCount = 4 }},
		{"success", func(v *contracts.ChannelHealthSnapshot) { v.QualitySuccessRate = .949 }},
		{"ttft", func(v *contracts.ChannelHealthSnapshot) { v.TTFTP95 = 4000.01 }},
		{"duration", func(v *contracts.ChannelHealthSnapshot) { v.DurationP95 = 20000.01 }},
		{"auth", func(v *contracts.ChannelHealthSnapshot) { v.AuthFailureCount = 1 }},
		{"balance", func(v *contracts.ChannelHealthSnapshot) { v.InsufficientBalanceCount = 1 }},
		{"nan", func(v *contracts.ChannelHealthSnapshot) { v.QualityScore = math.NaN() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			if qualitySnapshotPassesRecommendation(value) {
				t.Fatalf("invalid quality accepted: %+v", value)
			}
		})
	}
}

func TestAfterEvidenceRejectsConflictingPostBoundaryQualityRows(t *testing.T) {
	observeUntil := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	generatedAt := observeUntil.Add(2 * time.Minute)
	rollout := contracts.RecommendationRollout{State: contracts.RecommendationRolloutState{
		PlanID: "plan-1", RecommendationFingerprint: "fp-1", SchedulingGeneration: 7,
		Status: contracts.RecommendationRolloutObserving, Stage: contracts.RecommendationRolloutStage10,
		StageStartedAt: ptrTime(observeUntil.Add(-time.Minute)), ObserveUntil: &observeUntil,
	}}
	recommendation := contracts.UpstreamRecommendation{
		Fingerprint: "fp-1", FromChannelID: "from", ToChannelID: "to", ModelKey: "gpt-test",
		AffectedDownstreams: []string{"instance-1"},
	}
	older := healthyAfter("quality-from-old", "from", observeUntil.Add(30*time.Second))
	newer := healthyAfter("quality-from-new", "from", observeUntil.Add(time.Minute))
	snapshot := store.UpstreamRecommendationInputs{
		GeneratedAt: generatedAt,
		Intelligence: store.UpstreamIntelligenceCurrentSnapshot{QualitySnapshots: []contracts.ChannelHealthSnapshot{
			older, newer, healthyAfter("quality-to", "to", observeUntil.Add(time.Minute)),
		}},
		Bindings: []contracts.PublishedBinding{
			verifiedAfter("binding-from", "from", generatedAt), verifiedAfter("binding-to", "to", generatedAt),
		},
	}
	if _, err := afterEvidenceFromSnapshot(rollout, recommendation, snapshot); !errors.Is(err, ErrControllerBlocked) {
		t.Fatalf("multiple post-boundary rows for one channel must fail closed, err=%v", err)
	}
}

func TestAfterEvidenceRejectsMalformedWantedQualityRow(t *testing.T) {
	observeUntil := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	generatedAt := observeUntil.Add(time.Minute)
	rollout := contracts.RecommendationRollout{State: contracts.RecommendationRolloutState{
		PlanID: "plan-1", RecommendationFingerprint: "fp-1", SchedulingGeneration: 7,
		Status: contracts.RecommendationRolloutObserving, Stage: contracts.RecommendationRolloutStage10,
		StageStartedAt: ptrTime(observeUntil.Add(-time.Minute)), ObserveUntil: &observeUntil,
	}}
	recommendation := contracts.UpstreamRecommendation{
		Fingerprint: "fp-1", FromChannelID: "from", ToChannelID: "to", ModelKey: "gpt-test",
		AffectedDownstreams: []string{"instance-1"},
	}
	malformed := healthyAfter("quality-from", "from", generatedAt)
	malformed.InstanceID = "wrong-instance"
	snapshot := store.UpstreamRecommendationInputs{
		GeneratedAt: generatedAt,
		Intelligence: store.UpstreamIntelligenceCurrentSnapshot{QualitySnapshots: []contracts.ChannelHealthSnapshot{
			malformed, healthyAfter("quality-to", "to", generatedAt),
		}},
		Bindings: []contracts.PublishedBinding{
			verifiedAfter("binding-from", "from", generatedAt), verifiedAfter("binding-to", "to", generatedAt),
		},
	}
	if _, err := afterEvidenceFromSnapshot(rollout, recommendation, snapshot); !errors.Is(err, ErrControllerBlocked) {
		t.Fatalf("malformed wanted quality row must fail closed, err=%v", err)
	}
}

func healthyAfter(id, channelID string, at time.Time) contracts.ChannelHealthSnapshot {
	return contracts.ChannelHealthSnapshot{
		ID: id, ChannelID: channelID, InstanceID: "instance-1", Model: "gpt-test", Window: contracts.Window5m,
		HealthState: contracts.HealthHealthy, QualitySampleCount: 6, QualitySuccessRate: 1, QualityScore: 95,
		TTFTP95: 100, DurationP95: 500, CreatedAt: at,
	}
}

func verifiedAfter(id, channelID string, at time.Time) contracts.PublishedBinding {
	return contracts.PublishedBinding{
		ID: id, PlanID: "plan-1", InstanceID: "instance-1", ChannelID: channelID, State: contracts.BindingActive,
		VerificationStatus: contracts.BindingVerificationPassiveVerified,
		VerificationSource: contracts.BindingVerificationSourcePassive, VerifiedAt: &at,
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
