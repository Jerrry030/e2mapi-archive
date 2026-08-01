// Package upstreamintelligence contains pure decision-domain functions for
// upstream intelligence. It has no store or HTTP dependency, so callers must
// resolve owner-scoped links and quality snapshots before invoking it.
package upstreamintelligence

import (
	"math/big"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
)

// FrontierCandidate is one explicitly resolved rate -> channel -> quality
// join. Builder still verifies every identity and link invariant; callers may
// not make a candidate eligible merely by supplying a channel id or score.
type FrontierCandidate struct {
	OwnerID int64
	Rate    contracts.UpstreamIntelligenceRateReadModel
	Link    *contracts.UpstreamIntelligenceLink
	// ResolvedChannelID and TargetVerified are supplied by the owner-scoped
	// resolver. They let a verified source_identity link safely resolve to a
	// concrete allocated channel without exposing the opaque identity in DTOs.
	// A direct channel link must resolve to its own ChannelID as well.
	ResolvedChannelID      string
	ResolvedChannelOwnerID int64
	TargetVerified         bool
	Quality                *QualityCandidate
}

// QualityCandidate is a canonical-decimal projection of a channel health
// snapshot. Score direction is high-is-good. MinimumSampleCount is the policy
// threshold used to decide whether the snapshot is sufficient.
type QualityCandidate struct {
	OwnerID                 int64
	ChannelID               string
	ModelKey                string
	SnapshotID              string
	Window                  contracts.HealthWindow
	QualityScore            *contracts.CanonicalDecimal
	QualitySampleCount      int
	MinimumSampleCount      int
	SuccessRate             *contracts.CanonicalDecimal
	TTFTP95Milliseconds     *contracts.CanonicalDecimal
	DurationP95Milliseconds *contracts.CanonicalDecimal
	HealthState             contracts.HealthState
	ObservedAt              time.Time
	FreshUntil              time.Time
}

// BuildFrontier marks every input as eligible or blocked, then computes the
// cost-minimizing / quality-maximizing Pareto frontier independently inside
// each comparable cohort. Blocked points never participate in dominance.
// Output order is deterministic and follows input order.
func BuildFrontier(candidates []FrontierCandidate, now time.Time) []contracts.UpstreamIntelligenceFrontierPoint {
	points := make([]contracts.UpstreamIntelligenceFrontierPoint, len(candidates))
	cohorts := make(map[frontierCohort][]frontierComparable)
	for index, candidate := range candidates {
		point, cost, quality := buildPoint(candidate, now)
		points[index] = point
		if point.Status != contracts.UpstreamIntelligenceFrontierEligible {
			continue
		}
		key := frontierCohort{
			model:     candidate.Rate.ModelKey,
			dimension: candidate.Rate.PriceDimension, currency: candidate.Rate.SettlementCurrency,
			perTokens: candidate.Rate.PerTokens,
		}
		cohorts[key] = append(cohorts[key], frontierComparable{index: index, cost: cost, quality: quality})
	}
	for _, cohort := range cohorts {
		markParetoFrontier(points, cohort)
	}
	return points
}

type frontierCohort struct {
	model     string
	dimension contracts.UpstreamPriceDimension
	currency  string
	perTokens int64
}

type frontierComparable struct {
	index   int
	cost    *big.Rat
	quality *big.Rat
}

func buildPoint(candidate FrontierCandidate, now time.Time) (contracts.UpstreamIntelligenceFrontierPoint, *big.Rat, *big.Rat) {
	point := contracts.UpstreamIntelligenceFrontierPoint{
		Rate: candidate.Rate, LinkState: contracts.UpstreamIntelligenceFrontierUnlinked,
		QualityScore: nil, QualityEvidence: nil, Status: contracts.UpstreamIntelligenceFrontierBlocked,
		BlockedReasons: make([]contracts.UpstreamIntelligenceComparabilityReason, 0), OnFrontier: false,
	}
	if !candidate.Rate.UpstreamIntelligenceComparability.Valid() {
		addBlocker(&point, contracts.UpstreamIntelligenceNotComparableUnknownEvidence)
	} else if !candidate.Rate.Comparable {
		addBlocker(&point, candidate.Rate.ComparabilityReason)
	}
	if strings.TrimSpace(candidate.Rate.SettlementCurrency) == "" {
		addBlocker(&point, contracts.UpstreamIntelligenceNotComparableMissingCurrency)
	}
	if candidate.Rate.PerTokens <= 0 || !validPriceDimension(candidate.Rate.PriceDimension) {
		addBlocker(&point, contracts.UpstreamIntelligenceNotComparableMissingUnit)
	}
	cost := positiveDecimal(candidate.Rate.EffectiveUnitCost)
	if cost == nil {
		addBlocker(&point, contracts.UpstreamIntelligenceNotComparableMissingPrice)
	}

	if !verifiedLink(candidate) {
		addBlocker(&point, contracts.UpstreamIntelligenceNotComparableUnlinkedQuality)
		return point, nil, nil
	}
	point.LinkState = contracts.UpstreamIntelligenceFrontierLinked
	point.ChannelID = candidate.ResolvedChannelID
	quality, evidence, blockers := qualityEvidence(candidate, now)
	point.QualityEvidence = evidence
	for _, reason := range blockers {
		addBlocker(&point, reason)
	}
	if quality != nil {
		point.QualityScore = cloneDecimal(candidate.Quality.QualityScore)
	}
	if len(point.BlockedReasons) == 0 {
		point.Status = contracts.UpstreamIntelligenceFrontierEligible
		return point, cost, quality
	}
	return point, nil, nil
}

func verifiedLink(candidate FrontierCandidate) bool {
	link := candidate.Link
	if candidate.OwnerID <= 0 || link == nil || !candidate.TargetVerified || strings.TrimSpace(candidate.ResolvedChannelID) == "" ||
		candidate.ResolvedChannelOwnerID != candidate.OwnerID || link.UserID != candidate.OwnerID ||
		link.IntelligenceSourceID != candidate.Rate.Source.ID || link.Status != contracts.UpstreamLinkActive ||
		link.VerifiedAt == nil || link.VerifiedAt.IsZero() || link.PriceDimension != candidate.Rate.PriceDimension {
		return false
	}
	switch link.Scope {
	case contracts.UpstreamLinkChannel:
		return strings.TrimSpace(link.ChannelID) != "" && link.ChannelID == candidate.ResolvedChannelID && strings.TrimSpace(link.UpstreamSourceIdentity) == ""
	case contracts.UpstreamLinkSourceIdentity:
		return strings.TrimSpace(link.UpstreamSourceIdentity) != "" && strings.TrimSpace(link.ChannelID) == ""
	default:
		return false
	}
}

func qualityEvidence(candidate FrontierCandidate, now time.Time) (*big.Rat, *contracts.UpstreamIntelligenceFrontierQualityEvidence, []contracts.UpstreamIntelligenceComparabilityReason) {
	quality := candidate.Quality
	if now.IsZero() || quality == nil || quality.OwnerID != candidate.OwnerID || quality.ChannelID != candidate.ResolvedChannelID ||
		quality.ModelKey != candidate.Rate.ModelKey || strings.TrimSpace(quality.SnapshotID) == "" ||
		quality.Window.Duration() <= 0 || quality.ObservedAt.IsZero() || quality.FreshUntil.IsZero() ||
		quality.FreshUntil.Before(quality.ObservedAt) {
		return nil, nil, []contracts.UpstreamIntelligenceComparabilityReason{contracts.UpstreamIntelligenceNotComparableQualityUnavailable}
	}
	freshness := qualityFreshness(quality.ObservedAt, quality.FreshUntil, now)
	evidence := &contracts.UpstreamIntelligenceFrontierQualityEvidence{
		SnapshotID: quality.SnapshotID, Window: quality.Window, QualitySampleCount: quality.QualitySampleCount,
		MinimumSampleCount: quality.MinimumSampleCount, SuccessRate: cloneDecimal(quality.SuccessRate),
		TTFTP95Milliseconds: cloneDecimal(quality.TTFTP95Milliseconds), DurationP95Milliseconds: cloneDecimal(quality.DurationP95Milliseconds),
		HealthState: quality.HealthState, ObservedAt: quality.ObservedAt, FreshUntil: quality.FreshUntil, Freshness: freshness,
	}
	blockers := make([]contracts.UpstreamIntelligenceComparabilityReason, 0, 3)
	score := boundedDecimal(quality.QualityScore, 0, 100)
	if quality.HealthState == contracts.HealthUnknown {
		score = nil
	}
	if score == nil {
		blockers = append(blockers, contracts.UpstreamIntelligenceNotComparableQualityUnavailable)
	}
	if quality.MinimumSampleCount <= 0 || quality.QualitySampleCount < quality.MinimumSampleCount {
		blockers = append(blockers, contracts.UpstreamIntelligenceNotComparableQualityInsufficient)
	}
	if freshness != contracts.UpstreamFreshnessCurrent {
		blockers = append(blockers, contracts.UpstreamIntelligenceNotComparableQualityStale)
	}
	return score, evidence, blockers
}

func qualityFreshness(observedAt, freshUntil, now time.Time) contracts.UpstreamIntelligenceFreshness {
	if !now.After(freshUntil) {
		return contracts.UpstreamFreshnessCurrent
	}
	grace := freshUntil.Sub(observedAt)
	if grace <= 0 {
		return contracts.UpstreamFreshnessExpired
	}
	if now.After(freshUntil.Add(grace)) {
		return contracts.UpstreamFreshnessExpired
	}
	return contracts.UpstreamFreshnessStale
}

// markParetoFrontier is O(n log n): after ordering by increasing cost and
// decreasing quality, only the best quality among cheaper cost groups is
// needed. The equal-cost group is handled together so equal points do not
// dominate one another, while a higher-quality peer at the same cost does.
func markParetoFrontier(points []contracts.UpstreamIntelligenceFrontierPoint, cohort []frontierComparable) {
	sort.SliceStable(cohort, func(i, j int) bool {
		if comparison := cohort[i].cost.Cmp(cohort[j].cost); comparison != 0 {
			return comparison < 0
		}
		if comparison := cohort[i].quality.Cmp(cohort[j].quality); comparison != 0 {
			return comparison > 0
		}
		return cohort[i].index < cohort[j].index
	})
	var bestCheaperQuality *big.Rat
	for start := 0; start < len(cohort); {
		end := start + 1
		for end < len(cohort) && cohort[end].cost.Cmp(cohort[start].cost) == 0 {
			end++
		}
		bestSameCostQuality := cohort[start].quality
		for index := start; index < end; index++ {
			candidate := cohort[index]
			dominatedByCheaper := bestCheaperQuality != nil && bestCheaperQuality.Cmp(candidate.quality) >= 0
			dominatedAtSameCost := bestSameCostQuality.Cmp(candidate.quality) > 0
			points[candidate.index].OnFrontier = !dominatedByCheaper && !dominatedAtSameCost
		}
		if bestCheaperQuality == nil || bestSameCostQuality.Cmp(bestCheaperQuality) > 0 {
			bestCheaperQuality = new(big.Rat).Set(bestSameCostQuality)
		}
		start = end
	}
}

func validPriceDimension(value contracts.UpstreamPriceDimension) bool {
	switch value {
	case contracts.UpstreamPriceInput, contracts.UpstreamPriceOutput, contracts.UpstreamPriceCachedInput, contracts.UpstreamPriceRequest:
		return true
	default:
		return false
	}
}

func positiveDecimal(value *contracts.CanonicalDecimal) *big.Rat {
	if value == nil {
		return nil
	}
	result, err := value.Rat()
	if err != nil || result.Sign() < 0 {
		return nil
	}
	return result
}

func boundedDecimal(value *contracts.CanonicalDecimal, minimum, maximum int64) *big.Rat {
	if value == nil {
		return nil
	}
	result, err := value.Rat()
	if err != nil || result.Cmp(big.NewRat(minimum, 1)) < 0 || result.Cmp(big.NewRat(maximum, 1)) > 0 {
		return nil
	}
	return result
}

func cloneDecimal(value *contracts.CanonicalDecimal) *contracts.CanonicalDecimal {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func addBlocker(point *contracts.UpstreamIntelligenceFrontierPoint, reason contracts.UpstreamIntelligenceComparabilityReason) {
	if !contracts.IsUpstreamIntelligenceComparabilityReason(reason) {
		return
	}
	for _, existing := range point.BlockedReasons {
		if existing == reason {
			return
		}
	}
	point.BlockedReasons = append(point.BlockedReasons, reason)
	sort.SliceStable(point.BlockedReasons, func(i, j int) bool { return point.BlockedReasons[i] < point.BlockedReasons[j] })
}
