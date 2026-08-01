package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryRecommendationRolloutDecisionInputsReturnsCurrentAndExactHistoricalQuality(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 4, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }
	st.instances = []contracts.Instance{{ID: "instance-owner", UserID: 41}, {ID: "instance-foreign", UserID: 42}}
	st.channelAllocations["from"] = upstreamChannelAllocation{UserID: 41}
	st.channelAllocations["to"] = upstreamChannelAllocation{UserID: 41}
	st.channelAllocations["foreign"] = upstreamChannelAllocation{UserID: 42}
	recommendation := recommendationDecisionInputFixture(41, "recommendation", now.Add(-4*time.Minute))
	st.upstreamRecommendations = []contracts.UpstreamRecommendation{recommendation}
	st.channelSnapshots = []contracts.ChannelHealthSnapshot{
		recommendationDecisionQuality("quality-from", "from", "instance-owner", now.Add(-4*time.Minute), contracts.HealthHealthy),
		recommendationDecisionQuality("quality-to", "to", "instance-owner", now.Add(-4*time.Minute), contracts.HealthHealthy),
		recommendationDecisionQuality("quality-current-from", "from", "instance-owner", now.Add(-time.Minute), contracts.HealthUnhealthy),
		recommendationDecisionQuality("quality-current-to", "to", "instance-owner", now.Add(-time.Minute), contracts.HealthHealthy),
	}

	got, err := st.ReadRecommendationRolloutDecisionInputs(ctx, 41, recommendation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Recommendation.ID != recommendation.ID || len(got.ExactQualityEvidence) != 2 || len(got.Current.Intelligence.QualitySnapshots) != 2 {
		t.Fatalf("decision inputs=%+v", got)
	}
	for _, quality := range got.ExactQualityEvidence {
		if quality.ID != "quality-from" && quality.ID != "quality-to" {
			t.Fatalf("unexpected exact evidence=%+v", got.ExactQualityEvidence)
		}
	}
	for _, quality := range got.Current.Intelligence.QualitySnapshots {
		if quality.ID == "quality-from" || quality.ID == "quality-to" {
			t.Fatalf("current projection leaked historical row=%+v", got.Current.Intelligence.QualitySnapshots)
		}
	}
}

func TestMemoryRecommendationRolloutDecisionInputsFailsClosedForMissingOrForeignEvidence(t *testing.T) {
	now := time.Date(2026, 7, 27, 4, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name    string
		quality []contracts.ChannelHealthSnapshot
	}{
		{name: "missing", quality: []contracts.ChannelHealthSnapshot{
			recommendationDecisionQuality("quality-from", "from", "instance-owner", now.Add(-time.Minute), contracts.HealthHealthy),
		}},
		{name: "foreign-instance", quality: []contracts.ChannelHealthSnapshot{
			recommendationDecisionQuality("quality-from", "from", "instance-owner", now.Add(-time.Minute), contracts.HealthHealthy),
			recommendationDecisionQuality("quality-to", "to", "instance-foreign", now.Add(-time.Minute), contracts.HealthHealthy),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			st := NewMemoryStore(now)
			st.now = func() time.Time { return now }
			st.instances = []contracts.Instance{{ID: "instance-owner", UserID: 41}, {ID: "instance-foreign", UserID: 42}}
			st.channelAllocations["from"] = upstreamChannelAllocation{UserID: 41}
			st.channelAllocations["to"] = upstreamChannelAllocation{UserID: 41}
			st.upstreamRecommendations = []contracts.UpstreamRecommendation{recommendationDecisionInputFixture(41, "recommendation", now)}
			st.channelSnapshots = test.quality
			if _, err := st.ReadRecommendationRolloutDecisionInputs(context.Background(), 41, "recommendation"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestMemoryRecommendationRolloutDecisionInputsReturnsDeepCopies(t *testing.T) {
	now := time.Date(2026, 7, 27, 4, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }
	st.instances = []contracts.Instance{{ID: "instance-owner", UserID: 41}}
	st.channelAllocations["from"] = upstreamChannelAllocation{UserID: 41}
	st.channelAllocations["to"] = upstreamChannelAllocation{UserID: 41}
	recommendation := recommendationDecisionInputFixture(41, "recommendation", now)
	recommendation.EvidenceIDs = []string{"quality-from", "quality-to"}
	st.upstreamRecommendations = []contracts.UpstreamRecommendation{recommendation}
	st.channelSnapshots = []contracts.ChannelHealthSnapshot{
		recommendationDecisionQuality("quality-from", "from", "instance-owner", now, contracts.HealthHealthy),
		recommendationDecisionQuality("quality-to", "to", "instance-owner", now, contracts.HealthHealthy),
	}
	first, err := st.ReadRecommendationRolloutDecisionInputs(context.Background(), 41, recommendation.ID)
	if err != nil {
		t.Fatal(err)
	}
	first.Recommendation.EvidenceIDs[0] = "mutated"
	first.Recommendation.Constraints[0].EvidenceIDs[0] = "mutated"
	first.ExactQualityEvidence[0].ID = "mutated"
	second, err := st.ReadRecommendationRolloutDecisionInputs(context.Background(), 41, recommendation.ID)
	if err != nil || second.Recommendation.EvidenceIDs[0] != "quality-from" ||
		second.Recommendation.Constraints[0].EvidenceIDs[0] != "quality-from" || second.ExactQualityEvidence[0].ID == "mutated" {
		t.Fatalf("decision read polluted store: %+v err=%v", second, err)
	}
}

func TestValidQualityOnlyFactAdvanceProofRequiresExactGapFreeQualityLineage(t *testing.T) {
	baseTime := time.Date(2026, 7, 27, 5, 0, 0, 0, time.UTC)
	valid := QualityOnlyFactAdvanceProof{
		UserID: 41, BaselineFactVersion: 10, CurrentFactVersion: 12, LineageWatermark: 9, Complete: true,
		Mutations: []UpstreamIntelligenceFactMutation{
			{UserID: 41, FactVersion: 11, Kind: UpstreamIntelligenceFactMutationQuality, EvidenceID: "quality-11", CreatedAt: baseTime},
			{UserID: 41, FactVersion: 12, Kind: UpstreamIntelligenceFactMutationQuality, EvidenceID: "quality-12", CreatedAt: baseTime.Add(time.Second)},
		},
	}
	if !ValidQualityOnlyFactAdvanceProof(valid, 41, 10, 12) {
		t.Fatal("exact gap-free quality lineage was rejected")
	}

	tests := []struct {
		name    string
		mutate  func(*QualityOnlyFactAdvanceProof)
		owner   int64
		base    int64
		current int64
	}{
		{name: "store marked incomplete", mutate: func(v *QualityOnlyFactAdvanceProof) { v.Complete = false }},
		{name: "proof owner", mutate: func(v *QualityOnlyFactAdvanceProof) { v.UserID++ }},
		{name: "baseline mismatch", mutate: func(v *QualityOnlyFactAdvanceProof) { v.BaselineFactVersion-- }},
		{name: "current mismatch", mutate: func(v *QualityOnlyFactAdvanceProof) { v.CurrentFactVersion++ }},
		{name: "negative watermark", mutate: func(v *QualityOnlyFactAdvanceProof) { v.LineageWatermark = -1 }},
		{name: "baseline before watermark", mutate: func(v *QualityOnlyFactAdvanceProof) { v.LineageWatermark = 11 }},
		{name: "missing mutation", mutate: func(v *QualityOnlyFactAdvanceProof) { v.Mutations = v.Mutations[:1] }},
		{name: "extra mutation", mutate: func(v *QualityOnlyFactAdvanceProof) { v.Mutations = append(v.Mutations, v.Mutations[1]) }},
		{name: "version gap", mutate: func(v *QualityOnlyFactAdvanceProof) { v.Mutations[0].FactVersion = 12 }},
		{name: "version reordered", mutate: func(v *QualityOnlyFactAdvanceProof) { v.Mutations[0], v.Mutations[1] = v.Mutations[1], v.Mutations[0] }},
		{name: "mutation owner", mutate: func(v *QualityOnlyFactAdvanceProof) { v.Mutations[0].UserID++ }},
		{name: "collection mutation", mutate: func(v *QualityOnlyFactAdvanceProof) { v.Mutations[0].Kind = UpstreamIntelligenceFactMutationCollection }},
		{name: "link mutation", mutate: func(v *QualityOnlyFactAdvanceProof) { v.Mutations[0].Kind = UpstreamIntelligenceFactMutationLink }},
		{name: "source mutation", mutate: func(v *QualityOnlyFactAdvanceProof) { v.Mutations[0].Kind = UpstreamIntelligenceFactMutationSource }},
		{name: "retention mutation", mutate: func(v *QualityOnlyFactAdvanceProof) { v.Mutations[0].Kind = UpstreamIntelligenceFactMutationRetention }},
		{name: "unknown mutation", mutate: func(v *QualityOnlyFactAdvanceProof) { v.Mutations[0].Kind = UpstreamIntelligenceFactMutationUnknown }},
		{name: "blank evidence", mutate: func(v *QualityOnlyFactAdvanceProof) { v.Mutations[0].EvidenceID = " " }},
		{name: "zero mutation time", mutate: func(v *QualityOnlyFactAdvanceProof) { v.Mutations[0].CreatedAt = time.Time{} }},
		{name: "time reversal", mutate: func(v *QualityOnlyFactAdvanceProof) {
			v.Mutations[1].CreatedAt = v.Mutations[0].CreatedAt.Add(-time.Second)
		}},
		{name: "expected owner", owner: 42},
		{name: "expected baseline", base: 9},
		{name: "expected current", current: 13},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Mutations = append([]UpstreamIntelligenceFactMutation(nil), valid.Mutations...)
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			owner, baseline, current := test.owner, test.base, test.current
			if owner == 0 {
				owner = 41
			}
			if baseline == 0 {
				baseline = 10
			}
			if current == 0 {
				current = 12
			}
			if ValidQualityOnlyFactAdvanceProof(candidate, owner, baseline, current) {
				t.Fatal("malformed or mismatched lineage was accepted")
			}
		})
	}

	for _, interval := range [][2]int64{{0, 1}, {10, 10}, {11, 10}} {
		if ValidQualityOnlyFactAdvanceProof(valid, 41, interval[0], interval[1]) {
			t.Fatalf("invalid expected interval %v was accepted", interval)
		}
	}
}

func recommendationDecisionInputFixture(userID int64, id string, at time.Time) contracts.UpstreamRecommendation {
	return contracts.UpstreamRecommendation{
		ID: id, UserID: userID, FromChannelID: "from", ToChannelID: "to", ModelKey: "gpt-test",
		AffectedDownstreams: []string{"instance-owner"}, CreatedAt: at,
		Constraints: []contracts.UpstreamRecommendationConstraint{{
			Kind: contracts.UpstreamRecommendationConstraintQuality, Status: contracts.UpstreamRecommendationConstraintPassed,
			EvidenceIDs: []string{"quality-from", "quality-to"},
		}},
	}
}

func recommendationDecisionQuality(id, channelID, instanceID string, at time.Time, state contracts.HealthState) contracts.ChannelHealthSnapshot {
	return contracts.ChannelHealthSnapshot{
		ID: id, ChannelID: channelID, InstanceID: instanceID, Model: "gpt-test", Window: contracts.Window5m,
		BucketStart: at.Truncate(time.Minute), CreatedAt: at, HealthState: state,
		QualitySampleCount: 6, QualitySuccessRate: 1, QualityScore: 95, TTFTP95: 100, DurationP95: 500,
	}
}
