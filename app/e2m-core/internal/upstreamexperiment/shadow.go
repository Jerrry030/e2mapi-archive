// Package upstreamexperiment implements side-effect-free shadow ranking and a
// narrow dry-run bridge to publish.Engine.PlanScheduling.
package upstreamexperiment

import (
	"errors"
	"math/big"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
)

var (
	ErrInvalidShadow = errors.New("upstream experiment: invalid shadow input")
	ErrNoCandidate   = errors.New("upstream experiment: no eligible candidate")
)

// ShadowRank simulates cost/quality ordering only. It cannot access desired
// state or publish. All candidates must be comparable to the recommendation;
// malformed or foreign candidates fail the whole evaluation closed.
func ShadowRank(recommendation contracts.UpstreamRecommendation, candidates []contracts.UpstreamShadowCandidate, now time.Time) (contracts.UpstreamShadowResult, error) {
	if recommendation.UserID <= 0 || strings.TrimSpace(recommendation.ID) == "" || strings.TrimSpace(recommendation.Fingerprint) == "" ||
		now.IsZero() || !now.Before(recommendation.ExpiresAt) || len(candidates) == 0 {
		return contracts.UpstreamShadowResult{}, ErrInvalidShadow
	}
	type scored struct {
		candidate contracts.UpstreamShadowCandidate
		cost      *big.Rat
		quality   *big.Rat
	}
	eligible := make([]scored, 0, len(candidates))
	evidence := make([]string, 0)
	seenChannels := make(map[string]bool)
	for _, candidate := range candidates {
		cost, quality, ok := validShadowCandidate(recommendation, candidate)
		if !ok || seenChannels[candidate.ChannelID] {
			return contracts.UpstreamShadowResult{}, ErrInvalidShadow
		}
		seenChannels[candidate.ChannelID] = true
		if constraintsPass(candidate.Constraints) {
			copyCandidate := cloneShadowCandidate(candidate)
			eligible = append(eligible, scored{candidate: copyCandidate, cost: cost, quality: quality})
			evidence = append(evidence, candidate.EvidenceIDs...)
		}
	}
	if len(eligible) == 0 {
		return contracts.UpstreamShadowResult{}, ErrNoCandidate
	}
	sort.Slice(eligible, func(i, j int) bool {
		if comparison := eligible[i].cost.Cmp(eligible[j].cost); comparison != 0 {
			return comparison < 0
		}
		if comparison := eligible[i].quality.Cmp(eligible[j].quality); comparison != 0 {
			return comparison > 0
		}
		return eligible[i].candidate.ChannelID < eligible[j].candidate.ChannelID
	})
	ranking := make([]contracts.UpstreamShadowCandidate, len(eligible))
	for index := range eligible {
		ranking[index] = eligible[index].candidate
	}
	return contracts.UpstreamShadowResult{
		// ID is assigned by the application boundary for each experiment run.
		// A recommendation fingerprint identifies evidence, not an execution;
		// deriving the ID from it would make a later run conflict with its new
		// EvaluatedAt instead of producing immutable, append-only evidence.
		UserID: recommendation.UserID, RecommendationID: recommendation.ID,
		RecommendationFingerprint: recommendation.Fingerprint, Winner: ranking[0], Ranking: ranking,
		EvidenceIDs: normalizeUnique(evidence), EvaluatedAt: now.UTC(),
	}, nil
}

func validShadowCandidate(recommendation contracts.UpstreamRecommendation, candidate contracts.UpstreamShadowCandidate) (*big.Rat, *big.Rat, bool) {
	if candidate.UserID != recommendation.UserID || strings.TrimSpace(candidate.SourceID) == "" || strings.TrimSpace(candidate.ChannelID) == "" ||
		strings.TrimSpace(candidate.GroupKey) == "" || candidate.ModelKey != recommendation.ModelKey || candidate.PriceDimension != recommendation.PriceDimension ||
		candidate.SettlementCurrency != recommendation.SettlementCurrency || candidate.PerTokens != recommendation.PerTokens ||
		len(candidate.EvidenceIDs) == 0 || len(normalizeUnique(candidate.EvidenceIDs)) != len(candidate.EvidenceIDs) || !validConstraints(candidate.Constraints) {
		return nil, nil, false
	}
	cost, costErr := candidate.Cost.Rat()
	quality, qualityErr := candidate.QualityScore.Rat()
	if costErr != nil || qualityErr != nil || cost.Sign() < 0 || quality.Sign() < 0 || quality.Cmp(big.NewRat(100, 1)) > 0 {
		return nil, nil, false
	}
	return cost, quality, true
}

func validConstraints(values []contracts.UpstreamRecommendationConstraint) bool {
	if len(values) != len(contracts.UpstreamRecommendationRequiredConstraints()) {
		return false
	}
	seen := make(map[contracts.UpstreamRecommendationConstraintKind]bool)
	for _, value := range values {
		if !contracts.IsUpstreamRecommendationConstraintKind(value.Kind) || !contracts.IsUpstreamRecommendationConstraintStatus(value.Status) || seen[value.Kind] ||
			len(value.EvidenceIDs) == 0 || len(normalizeUnique(value.EvidenceIDs)) != len(value.EvidenceIDs) {
			return false
		}
		seen[value.Kind] = true
	}
	return true
}

func constraintsPass(values []contracts.UpstreamRecommendationConstraint) bool {
	if !validConstraints(values) {
		return false
	}
	for _, value := range values {
		if value.Status != contracts.UpstreamRecommendationConstraintPassed {
			return false
		}
	}
	return true
}

func normalizeUnique(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func cloneShadowCandidate(value contracts.UpstreamShadowCandidate) contracts.UpstreamShadowCandidate {
	value.EvidenceIDs = append([]string(nil), value.EvidenceIDs...)
	value.Constraints = append([]contracts.UpstreamRecommendationConstraint(nil), value.Constraints...)
	for index := range value.Constraints {
		value.Constraints[index].EvidenceIDs = append([]string(nil), value.Constraints[index].EvidenceIDs...)
	}
	return value
}
