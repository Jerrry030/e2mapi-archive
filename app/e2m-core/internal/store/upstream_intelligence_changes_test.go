package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestCompleteSnapshotAbsenceRequiresTwoStrictlyNewerRuns(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(time.Now())
	seedMemoryIntelligenceOwner(t, store, 88, "inst", "conn")
	source, err := store.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source", UserID: 88, ConnectorID: "conn", InstanceID: "inst", LocalRef: "local",
		Mode: contracts.UpstreamSourceOwned, Provider: "sub2api", DisplayName: "owned", Status: contracts.UpstreamSourceActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	finalizeMemorySnapshot(t, store, 88, source.ID, "run-present", base, contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, []contracts.UpstreamOfferObservation{
		memoryChangeOffer("run-present", 88, source.ID, base, "g1", "m1", contracts.UpstreamPriceInput),
		memoryChangeOffer("run-present", 88, source.ID, base, "g1", "m1", contracts.UpstreamPriceOutput),
	})
	absences, err := store.ListUpstreamSnapshotAbsences(ctx, 88, source.ID)
	if err != nil || len(absences) != 2 {
		t.Fatalf("present absence baseline=%+v err=%v", absences, err)
	}
	for _, absence := range absences {
		if absence.ConsecutiveCompleteRuns != 0 || absence.LastPresentRunID != "run-present" {
			t.Fatalf("present state=%+v", absence)
		}
	}

	finalizeMemorySnapshot(t, store, 88, source.ID, "run-missing-one", base.Add(time.Minute), contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, nil)
	absences, _ = store.ListUpstreamSnapshotAbsences(ctx, 88, source.ID)
	for _, absence := range absences {
		wantCount := 1
		if absence.ComparisonKey == upstreamModelAbsenceKey("g1", "m1") {
			wantCount = 0
		}
		if absence.ConsecutiveCompleteRuns != wantCount || wantCount == 1 && (absence.FirstAbsentAt == nil || absence.LastAbsentRunID != "run-missing-one") {
			t.Fatalf("first absence=%+v", absence)
		}
	}
	changes, err := store.ListUpstreamChangeEvents(ctx, contracts.UpstreamChangeEventFilter{UserID: 88, SourceID: source.ID})
	if err != nil || len(changes) != 0 {
		t.Fatalf("first absence changes=%+v err=%v", changes, err)
	}

	finalizeMemorySnapshot(t, store, 88, source.ID, "run-missing-two", base.Add(2*time.Minute), contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, nil)
	changes, err = store.ListUpstreamChangeEvents(ctx, contracts.UpstreamChangeEventFilter{UserID: 88, SourceID: source.ID})
	if err != nil || len(changes) != 1 || changes[0].Type != contracts.UpstreamChangeGroupRemoved ||
		changes[0].GroupKey != "g1" || changes[0].BeforeObservationID == "" || changes[0].Fingerprint == "" {
		t.Fatalf("confirmed removal=%+v err=%v", changes, err)
	}
	if _, _, err := store.FinalizeUpstreamCollectionRun(ctx, 88, "run-missing-two"); err != nil {
		t.Fatalf("finalization replay: %v", err)
	}
	replayed, _ := store.ListUpstreamChangeEvents(ctx, contracts.UpstreamChangeEventFilter{UserID: 88, SourceID: source.ID})
	if len(replayed) != 1 {
		t.Fatalf("replay duplicated changes: %+v", replayed)
	}
}

func TestPartialFailedAndLateRunsDoNotAdvanceAbsence(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(time.Now())
	seedMemoryIntelligenceOwner(t, store, 89, "inst", "conn")
	source, err := store.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source", UserID: 89, ConnectorID: "conn", InstanceID: "inst", LocalRef: "local",
		Mode: contracts.UpstreamSourceOwned, Provider: "sub2api", DisplayName: "owned", Status: contracts.UpstreamSourceActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	finalizeMemorySnapshot(t, store, 89, source.ID, "run-present", base, contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete,
		[]contracts.UpstreamOfferObservation{memoryChangeOffer("run-present", 89, source.ID, base, "g1", "m1", contracts.UpstreamPriceInput)})
	finalizeMemorySnapshot(t, store, 89, source.ID, "run-partial", base.Add(time.Minute), contracts.UpstreamCollectionPartial, contracts.UpstreamCoveragePartial,
		[]contracts.UpstreamOfferObservation{memoryChangeOffer("run-partial", 89, source.ID, base.Add(time.Minute), "other", "other", contracts.UpstreamPriceInput)})
	finalizeMemorySnapshot(t, store, 89, source.ID, "run-failed", base.Add(2*time.Minute), contracts.UpstreamCollectionFailed, contracts.UpstreamCoverageUnavailable, nil)
	absences, _ := store.ListUpstreamSnapshotAbsences(ctx, 89, source.ID)
	if len(absences) != 2 || absences[0].ConsecutiveCompleteRuns != 0 || absences[1].ConsecutiveCompleteRuns != 0 {
		t.Fatalf("partial/failed advanced absence: %+v", absences)
	}

	finalizeMemorySnapshot(t, store, 89, source.ID, "run-complete-new", base.Add(4*time.Minute), contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, nil)
	finalizeMemorySnapshot(t, store, 89, source.ID, "run-complete-late", base.Add(3*time.Minute), contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, nil)
	absences, _ = store.ListUpstreamSnapshotAbsences(ctx, 89, source.ID)
	for _, absence := range absences {
		wantCount := 1
		if absence.ComparisonKey == upstreamModelAbsenceKey("g1", "m1") {
			wantCount = 0
		}
		if absence.ConsecutiveCompleteRuns != wantCount || wantCount == 1 && absence.LastAbsentRunID != "run-complete-new" {
			t.Fatalf("late complete run advanced absence: %+v", absence)
		}
	}
	changes, _ := store.ListUpstreamChangeEvents(ctx, contracts.UpstreamChangeEventFilter{UserID: 89, SourceID: source.ID})
	if len(changes) != 0 {
		t.Fatalf("late complete run emitted removal: %+v", changes)
	}
}

func TestPresentSnapshotResetsAbsenceAndModelRemovalIsSpecific(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(time.Now())
	seedMemoryIntelligenceOwner(t, store, 90, "inst", "conn")
	source, err := store.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source", UserID: 90, ConnectorID: "conn", InstanceID: "inst", LocalRef: "local",
		Mode: contracts.UpstreamSourceOwned, Provider: "sub2api", DisplayName: "owned", Status: contracts.UpstreamSourceActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	initial := []contracts.UpstreamOfferObservation{
		memoryChangeOffer("initial", 90, source.ID, base, "g1", "m1", contracts.UpstreamPriceInput),
		memoryChangeOffer("initial", 90, source.ID, base, "g1", "m2", contracts.UpstreamPriceInput),
	}
	finalizeMemorySnapshot(t, store, 90, source.ID, "initial", base, contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, initial)
	for index, runID := range []string{"missing-once", "present-again", "missing-again-one", "missing-again-two"} {
		observedAt := base.Add(time.Duration(index+1) * time.Minute)
		offers := []contracts.UpstreamOfferObservation{memoryChangeOffer(runID, 90, source.ID, observedAt, "g1", "m2", contracts.UpstreamPriceInput)}
		if runID == "present-again" {
			offers = append(offers, memoryChangeOffer(runID, 90, source.ID, observedAt, "g1", "m1", contracts.UpstreamPriceInput))
		}
		finalizeMemorySnapshot(t, store, 90, source.ID, runID, observedAt, contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, offers)
	}
	changes, err := store.ListUpstreamChangeEvents(ctx, contracts.UpstreamChangeEventFilter{UserID: 90, SourceID: source.ID})
	if err != nil || len(changes) != 1 || changes[0].Type != contracts.UpstreamChangeModelRemoved || changes[0].ModelKey != "m1" || changes[0].GroupKey != "g1" {
		t.Fatalf("model removal=%+v err=%v", changes, err)
	}
}

func TestGroupRecoveryDoesNotImmediatelyEmitChildModelRemoval(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(time.Now())
	seedMemoryIntelligenceOwner(t, store, 93, "inst", "conn")
	source, err := store.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source", UserID: 93, ConnectorID: "conn", InstanceID: "inst", LocalRef: "local",
		Mode: contracts.UpstreamSourceOwned, Provider: "sub2api", DisplayName: "owned", Status: contracts.UpstreamSourceActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 24, 2, 30, 0, 0, time.UTC)
	finalizeMemorySnapshot(t, store, 93, source.ID, "present", base, contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete,
		[]contracts.UpstreamOfferObservation{memoryChangeOffer("present", 93, source.ID, base, "g1", "m1", contracts.UpstreamPriceInput)})
	finalizeMemorySnapshot(t, store, 93, source.ID, "group-missing-one", base.Add(time.Minute), contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, nil)
	finalizeMemorySnapshot(t, store, 93, source.ID, "group-missing-two", base.Add(2*time.Minute), contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, nil)
	recoveryOffer := func(runID string, observedAt time.Time) []contracts.UpstreamOfferObservation {
		return []contracts.UpstreamOfferObservation{memoryChangeOffer(runID, 93, source.ID, observedAt, "g1", "m2", contracts.UpstreamPriceInput)}
	}
	finalizeMemorySnapshot(t, store, 93, source.ID, "group-recovered-one", base.Add(3*time.Minute), contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, recoveryOffer("group-recovered-one", base.Add(3*time.Minute)))
	changes, _ := store.ListUpstreamChangeEvents(ctx, contracts.UpstreamChangeEventFilter{UserID: 93, SourceID: source.ID})
	if len(changes) != 1 || changes[0].Type != contracts.UpstreamChangeGroupRemoved {
		t.Fatalf("group recovery immediately emitted child removal: %+v", changes)
	}
	finalizeMemorySnapshot(t, store, 93, source.ID, "group-recovered-two", base.Add(4*time.Minute), contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, recoveryOffer("group-recovered-two", base.Add(4*time.Minute)))
	changes, _ = store.ListUpstreamChangeEvents(ctx, contracts.UpstreamChangeEventFilter{UserID: 93, SourceID: source.ID})
	if len(changes) != 2 || changes[0].Type != contracts.UpstreamChangeModelRemoved && changes[1].Type != contracts.UpstreamChangeModelRemoved {
		t.Fatalf("second recovered snapshot lacks model removal: %+v", changes)
	}
}

func TestSameTimestampCompleteRunDoesNotAdvanceAbsence(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(time.Now())
	seedMemoryIntelligenceOwner(t, store, 92, "inst", "conn")
	source, err := store.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source", UserID: 92, ConnectorID: "conn", InstanceID: "inst", LocalRef: "local",
		Mode: contracts.UpstreamSourceOwned, Provider: "sub2api", DisplayName: "owned", Status: contracts.UpstreamSourceActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	observedAt := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	finalizeMemorySnapshot(t, store, 92, source.ID, "run-present", observedAt, contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete,
		[]contracts.UpstreamOfferObservation{memoryChangeOffer("run-present", 92, source.ID, observedAt, "g1", "m1", contracts.UpstreamPriceInput)})
	finalizeMemorySnapshot(t, store, 92, source.ID, "run-same-time", observedAt, contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, nil)
	absences, err := store.ListUpstreamSnapshotAbsences(ctx, 92, source.ID)
	if err != nil || len(absences) != 2 {
		t.Fatalf("same-time absence state=%+v err=%v", absences, err)
	}
	for _, absence := range absences {
		if absence.ConsecutiveCompleteRuns != 0 || absence.LastAbsentRunID != "" {
			t.Fatalf("same timestamp advanced absence: %+v", absence)
		}
	}
}

func finalizeMemorySnapshot(t *testing.T, store *MemoryStore, userID int64, sourceID, runID string, observedAt time.Time, status contracts.UpstreamCollectionStatus, coverage contracts.UpstreamEvidenceCoverage, offers []contracts.UpstreamOfferObservation) {
	t.Helper()
	run := newMemoryIntelligenceRun(runID, userID, sourceID, "conn", observedAt)
	run.Status, run.Coverage, run.FactCount = status, coverage, len(offers)
	if status == contracts.UpstreamCollectionFailed {
		run.ErrorCode, run.Retryable = contracts.UpstreamCollectionErrorUpstreamUnavailable, true
	}
	payloadHash := hash64("change-" + runID)
	run.ManifestHash = manifestHash(t, payloadHash)
	if _, err := store.CreateUpstreamCollectionRun(context.Background(), run); err != nil {
		t.Fatalf("create %s: %v", runID, err)
	}
	if _, _, err := store.UpsertUpstreamIntelligenceIngestBatch(context.Background(), UpstreamIntelligenceIngestBatch{
		RunID: runID, UserID: userID, SourceID: sourceID, BatchNo: 0, BatchCount: 1,
		PayloadHash: payloadHash, ManifestHash: run.ManifestHash, OfferCount: len(offers),
	}); err != nil {
		t.Fatalf("batch %s: %v", runID, err)
	}
	for _, offer := range offers {
		offer.Coverage = coverage
		if _, err := store.AppendUpstreamOfferObservation(context.Background(), offer); err != nil {
			t.Fatalf("offer %s: %v", runID, err)
		}
	}
	if _, _, err := store.FinalizeUpstreamCollectionRun(context.Background(), userID, runID); err != nil {
		t.Fatalf("finalize %s: %v", runID, err)
	}
}

func memoryChangeOffer(runID string, userID int64, sourceID string, observedAt time.Time, groupKey, modelKey string, dimension contracts.UpstreamPriceDimension) contracts.UpstreamOfferObservation {
	offer := memoryOffer(runID, userID, sourceID, observedAt)
	offer.ID = "offer-" + runID + "-" + groupKey + "-" + modelKey + "-" + string(dimension)
	offer.GroupKey, offer.ModelKey, offer.PriceDimension = groupKey, modelKey, dimension
	return offer
}

func TestCompleteSnapshotAbsenceHelperRejectsIncompleteFact(t *testing.T) {
	run := newMemoryIntelligenceRun("run", 91, "source", "conn", time.Now().UTC())
	offer := memoryOffer(run.ID, run.UserID, run.SourceID, run.ObservedAt)
	offer.Coverage = contracts.UpstreamCoveragePartial
	if _, _, err := reconcileCompleteSnapshotAbsences(run, []contracts.UpstreamOfferObservation{offer}, nil, run.ObservedAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("incomplete fact error=%v, want conflict", err)
	}
}
