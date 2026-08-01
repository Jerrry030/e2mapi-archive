package strategy

import (
	"bytes"
	"crypto/sha1"
	"sort"
	"time"

	"e2m.local/contracts"
)

// QualityEjectionPercentage expands a soft-failure cohort as consecutive bad
// windows accumulate, while deliberately never selecting every downstream in
// one sweep.
func QualityEjectionPercentage(consecutiveBadWindows int) int {
	switch {
	case consecutiveBadWindows >= 3:
		return 75
	case consecutiveBadWindows == 2:
		return 50
	default:
		return 25
	}
}

// InStableQualityCohort assigns a plan/channel pair to a deterministic rollout
// bucket. Increasing 25 -> 50 -> 75 only adds downstreams; it never reshuffles
// an earlier batch.
func InStableQualityCohort(planID, channelID string, percentage int) bool {
	if percentage >= 100 {
		return true
	}
	if percentage <= 0 {
		return false
	}
	sum := sha1.Sum([]byte(planID + "\x00" + channelID))
	bucket := int(sum[0])<<8 | int(sum[1])
	return bucket%100 < percentage
}

// StableQualityCohortPlanIDs returns a deterministic prefix of the plans bound
// to one stable upstream source. At least one plan remains a holdout for every
// non-empty group, including small groups where hash percentages alone could
// otherwise select every downstream.
func StableQualityCohortPlanIDs(planIDs []string, sourceID string, percentage int) map[string]bool {
	return StableQualityIncidentCohortPlanIDs(planIDs, nil, sourceID, percentage)
}

// StableQualityIncidentCohortPlanIDs keeps an in-flight source incident stable
// after its first downstreams have been removed from scheduling. activePlanIDs
// are currently carrying traffic; isolatedPlanIDs are published downstreams
// whose binding is already held out by a durable quality circuit. Previously
// isolated members remain selected and count against the requested percentage,
// so recomputing the cohort cannot rotate through every active downstream.
//
// For a multi-downstream source, at least one currently-active plan remains an
// observation holdout. A single active downstream is selected because there is
// no separate downstream available to observe.
func StableQualityIncidentCohortPlanIDs(activePlanIDs, isolatedPlanIDs []string, sourceID string, percentage int) map[string]bool {
	return StableQualityAffectedIncidentCohortPlanIDs(activePlanIDs, nil, isolatedPlanIDs, sourceID, percentage)
}

// StableQualityAffectedIncidentCohortPlanIDs selects only active downstreams
// that currently have an attributable soft-quality ejection. Healthy/unknown
// active downstreams remain observers: they count in the incident denominator
// and satisfy the holdout requirement, but never consume an ejection slot.
// Already-isolated members remain selected so the cohort is monotonic across
// sweeps and process restarts.
func StableQualityAffectedIncidentCohortPlanIDs(
	affectedPlanIDs, observerPlanIDs, isolatedPlanIDs []string,
	sourceID string,
	percentage int,
) map[string]bool {
	affected := uniquePlanIDs(affectedPlanIDs)
	observers := uniquePlanIDs(observerPlanIDs)
	isolated := uniquePlanIDs(isolatedPlanIDs)
	for planID := range affected {
		delete(observers, planID)
		delete(isolated, planID)
	}
	for planID := range observers {
		delete(isolated, planID)
	}

	selected := make(map[string]bool, len(isolated))
	for planID := range isolated {
		selected[planID] = true
	}
	total := len(affected) + len(observers) + len(isolated)
	if total == 0 || percentage <= 0 {
		return selected
	}

	target := total * percentage / 100
	if target == 0 {
		target = 1
	}
	if total > 1 && target >= total {
		target = total - 1
	}
	// Existing isolation is authoritative. It may exceed a later caller's stage
	// but must never be undone or exchanged for another active member.
	if target <= len(selected) || len(affected) == 0 {
		return selected
	}

	ranked := rankQualityCohortPlans(affected, sourceID)
	maxAdds := len(affected)
	if len(observers) == 0 && total > 1 {
		// With no healthy/unknown observer, keep one affected member on real
		// traffic. Isolated members cannot be the holdout because they no longer
		// measure production behavior.
		maxAdds--
	}
	adds := target - len(selected)
	if adds > maxAdds {
		adds = maxAdds
	}
	for i := 0; i < adds; i++ {
		selected[ranked[i].id] = true
	}
	return selected
}

// StableRecoveryCohortPlanIDs chooses the guarded recovery batch for a source.
// Existing admitted downstreams are monotonic members; increasing the stage
// only adds plans and never reshuffles live canaries. Unlike ejection, 100% is a
// valid terminal stage because all bindings have already produced independent
// active-probe evidence.
func StableRecoveryCohortPlanIDs(planIDs, admittedPlanIDs []string, sourceID string, percentage int) map[string]bool {
	all := uniquePlanIDs(planIDs)
	selected := make(map[string]bool, len(all))
	for planID := range uniquePlanIDs(admittedPlanIDs) {
		if _, exists := all[planID]; exists {
			selected[planID] = true
		}
	}
	if len(all) == 0 || percentage <= 0 {
		return selected
	}
	if percentage > 100 {
		percentage = 100
	}
	target := (len(all)*percentage + 99) / 100
	if target < 1 {
		target = 1
	}
	if target <= len(selected) {
		return selected
	}
	remaining := make(map[string]struct{}, len(all)-len(selected))
	for planID := range all {
		if !selected[planID] {
			remaining[planID] = struct{}{}
		}
	}
	for _, ranked := range rankQualityCohortPlans(remaining, "recovery\x00"+sourceID) {
		if len(selected) >= target {
			break
		}
		selected[ranked.id] = true
	}
	return selected
}

func uniquePlanIDs(planIDs []string) map[string]struct{} {
	unique := make(map[string]struct{}, len(planIDs))
	for _, planID := range planIDs {
		if planID != "" {
			unique[planID] = struct{}{}
		}
	}
	return unique
}

type qualityCohortRankedPlan struct {
	id   string
	hash [sha1.Size]byte
}

func rankQualityCohortPlans(unique map[string]struct{}, sourceID string) []qualityCohortRankedPlan {
	ranked := make([]qualityCohortRankedPlan, 0, len(unique))
	for planID := range unique {
		ranked = append(ranked, qualityCohortRankedPlan{
			id: planID, hash: sha1.Sum([]byte(sourceID + "\x00" + planID)),
		})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if cmp := bytes.Compare(ranked[i].hash[:], ranked[j].hash[:]); cmp != 0 {
			return cmp < 0
		}
		return ranked[i].id < ranked[j].id
	})
	return ranked
}

// IndependentWindowBuckets groups equal snapshot starts (such as per-model
// rows) newest-first, selecting only starts at least window apart. BucketStart
// is authoritative; legacy rows fall back to CreatedAt.
func IndependentWindowBuckets(snapshots []contracts.ChannelHealthSnapshot, window time.Duration) [][]contracts.ChannelHealthSnapshot {
	if window <= 0 {
		return nil
	}
	byStart := make(map[time.Time][]contracts.ChannelHealthSnapshot)
	starts := make([]time.Time, 0)
	for _, snapshot := range snapshots {
		start := snapshot.BucketStart
		if start.IsZero() {
			start = snapshot.CreatedAt
		}
		if start.IsZero() {
			continue
		}
		start = start.UTC()
		if _, exists := byStart[start]; !exists {
			starts = append(starts, start)
		}
		byStart[start] = append(byStart[start], snapshot)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].After(starts[j]) })
	groups := make([][]contracts.ChannelHealthSnapshot, 0, len(starts))
	var latestSelected time.Time
	for _, start := range starts {
		if !latestSelected.IsZero() && latestSelected.Sub(start) < window {
			continue
		}
		groups = append(groups, byStart[start])
		latestSelected = start
	}
	return groups
}
