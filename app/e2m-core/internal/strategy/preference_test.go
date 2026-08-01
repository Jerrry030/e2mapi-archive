package strategy

import (
	"math"
	"reflect"
	"testing"

	"e2m.local/contracts"
)

func preferenceCandidate(id string, cost float64) Candidate {
	return Candidate{Channel: contracts.UpstreamChannel{ID: id, CostHint: cost}}
}

func preferenceEligible(id string, confidence, safety float64, scores subs) ScoredCandidate {
	return ScoredCandidate{
		ChannelID:  id,
		Confidence: confidence,
		Score:      safety,
		Snapshot: contracts.ChannelHealthSnapshot{
			SuccessScore:   scores.success,
			TTFTScore:      scores.ttft,
			DurationScore:  scores.duration,
			StabilityScore: scores.stability,
			CostScore:      scores.cost,
		},
	}
}

func preferenceIDs(ranked []ScoredCandidate) []string {
	ids := make([]string, len(ranked))
	for i := range ranked {
		ids[i] = ranked[i].ChannelID
	}
	return ids
}

func TestRankEligibleByPreferenceFourPresetsChooseDifferentWinners(t *testing.T) {
	candidates := []Candidate{
		preferenceCandidate("stable", 4),
		preferenceCandidate("fast", 3),
		preferenceCandidate("balanced", 2),
		preferenceCandidate("budget", 1),
	}
	eligible := []ScoredCandidate{
		// stability_first: 80; latency_first: 25; balanced: 50
		preferenceEligible("stable", 1, 80, subs{success: 100, stability: 100}),
		// stability_first: 15; latency_first: 70; balanced: 40
		preferenceEligible("fast", 1, 80, subs{ttft: 100, duration: 100}),
		// stability_first: 63.5; latency_first: 69; balanced: 68
		preferenceEligible("balanced", 1, 80, subs{success: 60, ttft: 70, duration: 70, stability: 60, cost: 100}),
		preferenceEligible("budget", 1, 80, subs{}),
	}

	tests := []struct {
		typ  contracts.RouteStrategyType
		want string
	}{
		{contracts.StrategyStabilityFirst, "stable"},
		{contracts.StrategyLatencyFirst, "fast"},
		{contracts.StrategyBalanced, "balanced"},
		{contracts.StrategyCostFirst, "budget"},
	}
	for _, test := range tests {
		t.Run(string(test.typ), func(t *testing.T) {
			ranked := RankEligibleByPreference(contracts.RouteStrategy{Type: test.typ}, candidates, eligible)
			if len(ranked) == 0 || ranked[0].ChannelID != test.want {
				t.Fatalf("%s winner = %v, want %q", test.typ, preferenceIDs(ranked), test.want)
			}
		})
	}
}

func TestRankEligibleByPreferenceCostUsesPositiveHintAndPutsUnknownLast(t *testing.T) {
	candidates := []Candidate{
		preferenceCandidate("premium", 10),
		preferenceCandidate("cheap", 2),
		preferenceCandidate("zero", 0),
		preferenceCandidate("negative", -1),
		preferenceCandidate("nan", math.NaN()),
		preferenceCandidate("infinite", math.Inf(1)),
	}
	eligible := []ScoredCandidate{
		// Deliberately contradict CostScore and safety score: CostHint is the
		// source of truth for cost_first, while unknown hints are never free.
		preferenceEligible("premium", 1, 70, subs{cost: 100}),
		preferenceEligible("cheap", 1, 60, subs{cost: 0}),
		preferenceEligible("zero", 1, 99, subs{cost: 100}),
		preferenceEligible("negative", 1, 98, subs{cost: 100}),
		preferenceEligible("nan", 1, 97, subs{cost: 100}),
		preferenceEligible("infinite", 1, 96, subs{cost: 100}),
	}

	got := preferenceIDs(RankEligibleByPreference(
		contracts.RouteStrategy{Type: contracts.StrategyCostFirst}, candidates, eligible,
	))
	want := []string{"cheap", "premium", "zero", "negative", "nan", "infinite"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cost ranking = %v, want %v", got, want)
	}
}

func TestRankEligibleByPreferenceRequiresEvidenceBeforeApplyingPreset(t *testing.T) {
	candidates := []Candidate{
		preferenceCandidate("sampled-known", 100),
		preferenceCandidate("sampled-unknown", 0),
		preferenceCandidate("thin-cheap", 1),
	}
	eligible := []ScoredCandidate{
		preferenceEligible("thin-cheap", 0.2, 100, subs{success: 100, ttft: 100, duration: 100, stability: 100, cost: 100}),
		preferenceEligible("sampled-unknown", 1, 99, subs{success: 50, ttft: 50, duration: 50, stability: 50, cost: 50}),
		preferenceEligible("sampled-known", 1, 70, subs{success: 50, ttft: 50, duration: 50, stability: 50, cost: 0}),
	}

	got := preferenceIDs(RankEligibleByPreference(
		contracts.RouteStrategy{Type: contracts.StrategyCostFirst}, candidates, eligible,
	))
	want := []string{"sampled-known", "sampled-unknown", "thin-cheap"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cost evidence ranking = %v, want %v", got, want)
	}

	// The same evidence barrier applies to quality presets: a thin perfect
	// snapshot cannot outrank a sufficiently sampled, merely good snapshot.
	got = preferenceIDs(RankEligibleByPreference(
		contracts.RouteStrategy{Type: contracts.StrategyLatencyFirst}, nil,
		[]ScoredCandidate{
			preferenceEligible("thin-perfect", 0.8, 100, subs{ttft: 100, duration: 100}),
			preferenceEligible("sampled-good", 1, 70, subs{ttft: 70, duration: 70}),
		},
	))
	if want := []string{"sampled-good", "thin-perfect"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("latency evidence ranking = %v, want %v", got, want)
	}
}

func TestRankEligibleByPreferenceThinDataFallsBackWithoutPresetBenefit(t *testing.T) {
	eligible := []ScoredCandidate{
		preferenceEligible("perfect-but-thinner", 0.2, 100, subs{ttft: 100, duration: 100}),
		preferenceEligible("observed", 0.8, 60, subs{ttft: 0, duration: 0}),
	}
	got := preferenceIDs(RankEligibleByPreference(
		contracts.RouteStrategy{Type: contracts.StrategyLatencyFirst}, nil, eligible,
	))
	want := []string{"observed", "perfect-but-thinner"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("thin-data fallback = %v, want %v", got, want)
	}

	candidates := []Candidate{
		preferenceCandidate("perfect-but-thinner", 1),
		preferenceCandidate("observed", 0),
	}
	got = preferenceIDs(RankEligibleByPreference(
		contracts.RouteStrategy{Type: contracts.StrategyCostFirst}, candidates, eligible,
	))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("thin-data cost fallback = %v, want %v", got, want)
	}
}

func TestRankEligibleByPreferenceHonorsResolvedWeights(t *testing.T) {
	eligible := []ScoredCandidate{
		preferenceEligible("fast", 1, 80, subs{ttft: 100}),
		preferenceEligible("stable", 1, 80, subs{stability: 100}),
	}
	strategy := contracts.RouteStrategy{
		Type:    contracts.StrategyLatencyFirst,
		Weights: contracts.StrategyWeights{Stability: 1},
	}
	got := preferenceIDs(RankEligibleByPreference(strategy, nil, eligible))
	if want := []string{"stable", "fast"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("custom-weight ranking = %v, want %v", got, want)
	}
}

func TestRankEligibleByPreferenceDoesNotGateOrOverwriteSafetyResults(t *testing.T) {
	candidates := []Candidate{
		{
			Channel:     contracts.UpstreamChannel{ID: "retired", Status: contracts.UpstreamChannelRetired},
			AuthFailure: true,
		},
		preferenceCandidate("active", 0),
	}
	eligible := []ScoredCandidate{
		preferenceEligible("active", 1, 91, subs{ttft: 10, duration: 10}),
		preferenceEligible("retired", 1, 42, subs{ttft: 100, duration: 100}),
	}
	original := append([]ScoredCandidate(nil), eligible...)

	ranked := RankEligibleByPreference(
		contracts.RouteStrategy{Type: contracts.StrategyLatencyFirst}, candidates, eligible,
	)
	if got, want := preferenceIDs(ranked), []string{"retired", "active"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("preference layer re-gated caller's eligible set: got %v, want %v", got, want)
	}
	if ranked[0].Score != 42 || ranked[1].Score != 91 {
		t.Fatalf("preference layer overwrote safety scores: %+v", ranked)
	}
	if !reflect.DeepEqual(eligible, original) {
		t.Fatalf("preference layer mutated caller slice: got %+v, want %+v", eligible, original)
	}
}

func TestRankEligibleByPreferenceTiesAreDeterministic(t *testing.T) {
	eligible := []ScoredCandidate{
		preferenceEligible("charlie", 1, 80, subs{success: 80}),
		preferenceEligible("bravo", 1, 80, subs{success: 80}),
		preferenceEligible("alpha", 1, 80, subs{success: 80}),
	}
	got := preferenceIDs(RankEligibleByPreference(
		contracts.RouteStrategy{Type: contracts.StrategyBalanced}, nil, eligible,
	))
	want := []string{"alpha", "bravo", "charlie"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tie ranking = %v, want %v", got, want)
	}
}
