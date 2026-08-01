package strategy

import (
	"reflect"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestStableQualityCohortPlanIDsKeepsAStableHoldout(t *testing.T) {
	plans := []string{"plan-c", "plan-a", "plan-b", "plan-a"}
	selected := StableQualityCohortPlanIDs(plans, "source-a", 75)
	if len(selected) != 2 {
		t.Fatalf("selected=%v, want two of three unique plans", selected)
	}
	for planID := range selected {
		if planID == "" {
			t.Fatal("empty plan selected")
		}
	}
	reordered := StableQualityCohortPlanIDs([]string{"plan-b", "plan-c", "plan-a"}, "source-a", 75)
	if !reflect.DeepEqual(selected, reordered) {
		t.Fatalf("cohort changed with input order: %v vs %v", selected, reordered)
	}
}

func TestQualityEjectionPercentageUsesIndependentWindowStages(t *testing.T) {
	for windows, want := range map[int]int{1: 25, 2: 50, 3: 75, 10: 75} {
		if got := QualityEjectionPercentage(windows); got != want {
			t.Fatalf("windows=%d percentage=%d, want %d", windows, got, want)
		}
	}
}

func TestStableQualityCohortPlanIDsCanSelectSinglePlanLocally(t *testing.T) {
	if selected := StableQualityCohortPlanIDs([]string{"only"}, "source-a", 75); !selected["only"] {
		t.Fatalf("single-plan source must still fail closed locally: %v", selected)
	}
}

func TestStableQualityIncidentCohortDoesNotRotateAfterEjection(t *testing.T) {
	active := []string{"plan-a", "plan-b", "plan-c", "plan-d"}
	first := StableQualityIncidentCohortPlanIDs(active, nil, "source-a", 25)
	if len(first) != 1 {
		t.Fatalf("first 25%% cohort=%v, want one plan", first)
	}
	var isolated string
	for planID := range first {
		isolated = planID
	}
	remaining := make([]string, 0, 3)
	for _, planID := range active {
		if planID != isolated {
			remaining = append(remaining, planID)
		}
	}

	recomputed := StableQualityIncidentCohortPlanIDs(remaining, []string{isolated}, "source-a", 25)
	if len(recomputed) != 1 || !recomputed[isolated] {
		t.Fatalf("same incident rotated after first ejection: first=%v recomputed=%v", first, recomputed)
	}
	expanded := StableQualityIncidentCohortPlanIDs(remaining, []string{isolated}, "source-a", 50)
	if len(expanded) != 2 || !expanded[isolated] {
		t.Fatalf("50%% stage did not expand monotonically: first=%v expanded=%v", first, expanded)
	}
	activeAt75 := make([]string, 0, 2)
	isolatedAt75 := make([]string, 0, 2)
	for _, planID := range active {
		if expanded[planID] {
			isolatedAt75 = append(isolatedAt75, planID)
		} else {
			activeAt75 = append(activeAt75, planID)
		}
	}
	final := StableQualityIncidentCohortPlanIDs(activeAt75, isolatedAt75, "source-a", 75)
	if len(final) != 3 {
		t.Fatalf("75%% stage=%v, want three plans", final)
	}
	holdouts := 0
	for _, planID := range activeAt75 {
		if !final[planID] {
			holdouts++
		}
	}
	if holdouts != 1 {
		t.Fatalf("75%% stage left %d active holdouts, want one: %v", holdouts, final)
	}
}

func TestStableQualityIncidentCohortNeverUsesIsolatedPlanAsOnlyHoldout(t *testing.T) {
	selected := StableQualityIncidentCohortPlanIDs(
		[]string{"active-a", "active-b"},
		[]string{"isolated-a", "isolated-b"},
		"source-a",
		75,
	)
	if len(selected) != 3 || !selected["isolated-a"] || !selected["isolated-b"] {
		t.Fatalf("unexpected incident cohort: %v", selected)
	}
	if selected["active-a"] && selected["active-b"] {
		t.Fatalf("isolated plan became the only holdout: %v", selected)
	}
}

func TestStableQualityAffectedCohortDoesNotLetHealthyObserverConsumeSlot(t *testing.T) {
	selected := StableQualityAffectedIncidentCohortPlanIDs(
		[]string{"affected"}, []string{"healthy"}, nil, "source-a", 25,
	)
	if len(selected) != 1 || !selected["affected"] || selected["healthy"] {
		t.Fatalf("healthy observer consumed the ejection slot: %v", selected)
	}
}

func TestStableQualityAffectedCohortCanRemoveEveryAffectedWithObserver(t *testing.T) {
	selected := StableQualityAffectedIncidentCohortPlanIDs(
		[]string{"bad-a", "bad-b"}, []string{"healthy"}, nil, "source-a", 75,
	)
	if len(selected) != 2 || !selected["bad-a"] || !selected["bad-b"] || selected["healthy"] {
		t.Fatalf("healthy observer did not allow every affected member to be removed: %v", selected)
	}
}

func TestStableQualityAffectedCohortKeepsAffectedHoldoutWhenEveryoneIsBad(t *testing.T) {
	selected := StableQualityAffectedIncidentCohortPlanIDs(
		[]string{"bad-a", "bad-b", "bad-c", "bad-d"}, nil, nil, "source-a", 75,
	)
	if len(selected) != 3 {
		t.Fatalf("all-bad 75%% cohort=%v, want three selected", selected)
	}
}

func TestStableRecoveryCohortIsMonotonicAndReachesAll(t *testing.T) {
	plans := []string{"plan-a", "plan-b", "plan-c", "plan-d", "plan-e", "plan-f", "plan-g", "plan-h", "plan-i", "plan-j"}
	ten := StableRecoveryCohortPlanIDs(plans, nil, "source-a", 10)
	if len(ten) != 1 {
		t.Fatalf("10%% cohort=%v", ten)
	}
	admitted := make([]string, 0, len(ten))
	for planID := range ten {
		admitted = append(admitted, planID)
	}
	twentyFive := StableRecoveryCohortPlanIDs(plans, admitted, "source-a", 25)
	if len(twentyFive) != 3 {
		t.Fatalf("25%% cohort=%v", twentyFive)
	}
	for planID := range ten {
		if !twentyFive[planID] {
			t.Fatalf("admitted canary reshuffled out: %s", planID)
		}
	}
	all := StableRecoveryCohortPlanIDs(plans, nil, "source-a", 100)
	if len(all) != len(plans) {
		t.Fatalf("100%% cohort=%v", all)
	}
}

func TestIndependentWindowBucketsRejectsOverlappingRollingWindows(t *testing.T) {
	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	snapshots := []contracts.ChannelHealthSnapshot{
		{Model: "a", BucketStart: base},
		{Model: "b", BucketStart: base},
		{Model: "a", BucketStart: base.Add(-time.Minute)},
		{Model: "a", BucketStart: base.Add(-5 * time.Minute)},
		{Model: "a", BucketStart: base.Add(-10 * time.Minute)},
	}
	buckets := IndependentWindowBuckets(snapshots, 5*time.Minute)
	if len(buckets) != 3 || len(buckets[0]) != 2 {
		t.Fatalf("independent buckets=%v, want three groups with both newest models", buckets)
	}
	if !buckets[0][0].BucketStart.Equal(base) ||
		!buckets[1][0].BucketStart.Equal(base.Add(-5*time.Minute)) ||
		!buckets[2][0].BucketStart.Equal(base.Add(-10*time.Minute)) {
		t.Fatalf("unexpected bucket boundaries: %+v", buckets)
	}
}

func TestIndependentWindowBucketsFallsBackToCreatedAt(t *testing.T) {
	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	buckets := IndependentWindowBuckets([]contracts.ChannelHealthSnapshot{
		{CreatedAt: base},
		{CreatedAt: base.Add(-4 * time.Minute)},
		{CreatedAt: base.Add(-5 * time.Minute)},
	}, 5*time.Minute)
	if len(buckets) != 2 || !buckets[1][0].CreatedAt.Equal(base.Add(-5*time.Minute)) {
		t.Fatalf("created-at fallback selected overlapping windows: %+v", buckets)
	}
}
