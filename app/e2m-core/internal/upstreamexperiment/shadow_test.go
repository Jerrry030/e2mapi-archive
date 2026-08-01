package upstreamexperiment

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestShadowRankSimulatesDeterministicallyWithoutChangingInputs(t *testing.T) {
	recommendation := experimentRecommendation()
	now := recommendation.CreatedAt.Add(time.Minute)
	candidates := []contracts.UpstreamShadowCandidate{
		shadowCandidate("source-b", "channel-b", "6", "90"),
		shadowCandidate("source-a", "channel-a", "5", "80"),
		shadowCandidate("source-c", "channel-c", "5", "95"),
	}
	original := cloneShadowInputs(candidates)
	got, err := ShadowRank(recommendation, candidates, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Winner.ChannelID != "channel-c" || got.Ranking[1].ChannelID != "channel-a" || got.Ranking[2].ChannelID != "channel-b" {
		t.Fatalf("cost/quality ranking wrong: %+v", got.Ranking)
	}
	if got.ID != "" {
		t.Fatalf("domain layer assigned run identity %q", got.ID)
	}
	if !reflect.DeepEqual(candidates, original) {
		t.Fatal("shadow mutated candidate facts")
	}
}

func TestShadowRankFailsClosedOnForeignOrIncomparableFacts(t *testing.T) {
	recommendation := experimentRecommendation()
	now := recommendation.CreatedAt.Add(time.Minute)
	tests := []func(*contracts.UpstreamShadowCandidate){
		func(v *contracts.UpstreamShadowCandidate) { v.UserID++ },
		func(v *contracts.UpstreamShadowCandidate) { v.ModelKey = "other" },
		func(v *contracts.UpstreamShadowCandidate) { v.SettlementCurrency = "CNY" },
		func(v *contracts.UpstreamShadowCandidate) { v.PerTokens = 1000 },
		func(v *contracts.UpstreamShadowCandidate) { v.Cost = "1.00" },
		func(v *contracts.UpstreamShadowCandidate) { v.Constraints = v.Constraints[:2] },
	}
	for index, mutate := range tests {
		candidate := shadowCandidate("source-a", "channel-a", "5", "80")
		mutate(&candidate)
		if _, err := ShadowRank(recommendation, []contracts.UpstreamShadowCandidate{candidate}, now); !errors.Is(err, ErrInvalidShadow) {
			t.Fatalf("case %d: got %v", index, err)
		}
	}
}

func TestShadowRankExcludesBlockedCandidateAndReturnsNoCandidateForUnknown(t *testing.T) {
	recommendation := experimentRecommendation()
	now := recommendation.CreatedAt.Add(time.Minute)
	passed := shadowCandidate("source-a", "channel-a", "6", "80")
	blocked := shadowCandidate("source-b", "channel-b", "1", "100")
	blocked.Constraints[0].Status = contracts.UpstreamRecommendationConstraintBlocked
	blocked.Constraints[0].ReasonCode = "quality_floor"
	got, err := ShadowRank(recommendation, []contracts.UpstreamShadowCandidate{blocked, passed}, now)
	if err != nil || got.Winner.ChannelID != passed.ChannelID || len(got.Ranking) != 1 {
		t.Fatalf("blocked candidate ranked: %+v %v", got, err)
	}
	passed.Constraints[1].Status = contracts.UpstreamRecommendationConstraintUnknown
	passed.Constraints[1].ReasonCode = "capacity_unknown"
	if _, err := ShadowRank(recommendation, []contracts.UpstreamShadowCandidate{blocked, passed}, now); !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("all unsafe candidates should fail closed: %v", err)
	}
}

func shadowCandidate(source, channel, cost, quality string) contracts.UpstreamShadowCandidate {
	constraints := make([]contracts.UpstreamRecommendationConstraint, 0)
	for _, kind := range contracts.UpstreamRecommendationRequiredConstraints() {
		constraints = append(constraints, contracts.UpstreamRecommendationConstraint{Kind: kind, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{string(kind) + "-evidence"}})
	}
	return contracts.UpstreamShadowCandidate{
		UserID: 42, SourceID: source, ChannelID: channel, GroupKey: "group-a", ModelKey: "model-a",
		PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", PerTokens: 1_000_000,
		Cost: contracts.CanonicalDecimal(cost), QualityScore: contracts.CanonicalDecimal(quality),
		Constraints: constraints, EvidenceIDs: []string{channel + "-price", channel + "-quality"},
	}
}

func cloneShadowInputs(values []contracts.UpstreamShadowCandidate) []contracts.UpstreamShadowCandidate {
	result := make([]contracts.UpstreamShadowCandidate, len(values))
	for index, value := range values {
		result[index] = cloneShadowCandidate(value)
	}
	return result
}
