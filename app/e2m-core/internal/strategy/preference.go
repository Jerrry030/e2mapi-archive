package strategy

import (
	"math"
	"sort"

	"e2m.local/contracts"
)

// RankEligibleByPreference applies a user's routing preset to candidates that
// have already survived RankByPenalty. It deliberately does not repeat any
// lifecycle or quality gate: callers keep RankByPenalty as the safety layer and
// use this function only as the preference layer over that safe set.
//
// The returned slice is a copy. ScoredCandidate.Score remains the safety score
// produced by RankByPenalty; preference scores are transient sort keys and do
// not change the meaning of that field.
//
// A candidate needs full safety evidence (Confidence >= 1) before a preference
// can improve its position. Thin or unknown windows follow all fully evidenced
// candidates and fall back to evidence, safety score and channel ID. This keeps
// a fresh channel's undeducted 100-point safety score or an optimistic sub-score
// from masquerading as proven quality.
func RankEligibleByPreference(s contracts.RouteStrategy, candidates []Candidate, eligible []ScoredCandidate) []ScoredCandidate {
	if len(eligible) == 0 {
		return nil
	}

	rs := resolve(s)
	costs := candidateCosts(candidates)
	ranked := append([]ScoredCandidate(nil), eligible...)

	sort.SliceStable(ranked, func(i, j int) bool {
		left, right := ranked[i], ranked[j]
		leftTrusted, rightTrusted := preferenceTrusted(left), preferenceTrusted(right)
		if leftTrusted != rightTrusted {
			return leftTrusted
		}

		if rs.typ == contracts.StrategyCostFirst {
			leftCost, leftKnown := costs[left.ChannelID]
			rightCost, rightKnown := costs[right.ChannelID]
			// Price only becomes a preference key after the candidate has enough
			// evidence. Within the thin-data fallback group, merely reporting a
			// very small price must not let a channel jump its safer peers.
			if leftTrusted {
				if leftKnown != rightKnown {
					return leftKnown
				}
				if leftKnown && leftCost != rightCost {
					return leftCost < rightCost
				}
			}
		} else if leftTrusted {
			leftPreference := weightedPreferenceScore(rs.weights, left.Snapshot)
			rightPreference := weightedPreferenceScore(rs.weights, right.Snapshot)
			if leftPreference != rightPreference {
				return leftPreference > rightPreference
			}
		}

		// Low-confidence candidates never receive a preset-derived advantage.
		// Prefer the one with more evidence, then preserve the safety engine's
		// score as the common fallback for all presets.
		if !leftTrusted {
			leftConfidence := finitePreferenceValue(left.Confidence, 0, 1)
			rightConfidence := finitePreferenceValue(right.Confidence, 0, 1)
			if leftConfidence != rightConfidence {
				return leftConfidence > rightConfidence
			}
		}
		leftSafety := finitePreferenceValue(left.Score, 0, 100)
		rightSafety := finitePreferenceValue(right.Score, 0, 100)
		if leftSafety != rightSafety {
			return leftSafety > rightSafety
		}
		return left.ChannelID < right.ChannelID
	})
	return ranked
}

// candidateCosts returns only usable price hints. Zero, negative and non-finite
// values mean "unknown" and therefore never look like free capacity.
func candidateCosts(candidates []Candidate) map[string]float64 {
	costs := make(map[string]float64, len(candidates))
	for i := range candidates {
		id := candidates[i].Channel.ID
		cost := candidates[i].Channel.CostHint
		if id == "" || cost <= 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
			continue
		}
		costs[id] = cost
	}
	return costs
}

func preferenceTrusted(candidate ScoredCandidate) bool {
	return !math.IsNaN(candidate.Confidence) && !math.IsInf(candidate.Confidence, 0) && candidate.Confidence >= 1
}

func weightedPreferenceScore(weights contracts.StrategyWeights, snapshot contracts.ChannelHealthSnapshot) float64 {
	score := finitePreferenceValue(snapshot.SuccessScore, 0, 100)*finitePreferenceWeight(weights.Success) +
		finitePreferenceValue(snapshot.TTFTScore, 0, 100)*finitePreferenceWeight(weights.TTFT) +
		finitePreferenceValue(snapshot.DurationScore, 0, 100)*finitePreferenceWeight(weights.Duration) +
		finitePreferenceValue(snapshot.StabilityScore, 0, 100)*finitePreferenceWeight(weights.Stability) +
		finitePreferenceValue(snapshot.CostScore, 0, 100)*finitePreferenceWeight(weights.Cost)
	return finitePreferenceValue(score, 0, 100)
}

func finitePreferenceWeight(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	return value
}

func finitePreferenceValue(value, low, high float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return low
	}
	return clamp(value, low, high)
}
