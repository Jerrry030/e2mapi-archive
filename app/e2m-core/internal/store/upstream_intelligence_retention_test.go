package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryPruneUpstreamIntelligenceHistoryIsOwnerScopedBoundedAndVersioned(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }

	for _, run := range []contracts.UpstreamCollectionRun{
		retentionTestRun("old-a", 11, "source-a", now.Add(-120*24*time.Hour), 1),
		retentionTestRun("old-b", 11, "source-a", now.Add(-110*24*time.Hour), 2),
		retentionTestRun("current-a", 11, "source-a", now.Add(-100*24*time.Hour), 3),
		retentionTestRun("other-owner", 22, "source-b", now.Add(-120*24*time.Hour), 1),
	} {
		seedMemoryRetentionRun(st, run)
	}
	st.upstreamIntelVersions[11] = contracts.UpstreamIntelligenceFactVersion{UserID: 11, FactVersion: 3, UpdatedAt: now.Add(-time.Hour)}

	result, err := st.PruneUpstreamIntelligenceHistory(ctx, 11, now.Add(-90*24*time.Hour), 1)
	if err != nil {
		t.Fatalf("first prune: %v", err)
	}
	if result.RunsDeleted != 1 || result.BatchesDeleted != 1 || result.WalletsDeleted != 1 || result.OffersDeleted != 1 ||
		result.FinalizedDeleted != 1 || result.ResultFactVersion != 4 {
		t.Fatalf("first result=%+v", result)
	}
	assertMemoryRetentionRun(t, st, 11, "old-a", false)
	assertMemoryRetentionRun(t, st, 11, "old-b", true)
	assertMemoryRetentionRun(t, st, 11, "current-a", true)
	assertMemoryRetentionRun(t, st, 22, "other-owner", true)

	result, err = st.PruneUpstreamIntelligenceHistory(ctx, 11, now.Add(-90*24*time.Hour), 100)
	if err != nil || result.RunsDeleted != 1 || result.ResultFactVersion != 5 {
		t.Fatalf("second result=%+v err=%v", result, err)
	}
	assertMemoryRetentionRun(t, st, 11, "old-b", false)
	assertMemoryRetentionRun(t, st, 11, "current-a", true)

	result, err = st.PruneUpstreamIntelligenceHistory(ctx, 11, now.Add(-90*24*time.Hour), 100)
	if err != nil || result.RunsDeleted != 0 || result.ResultFactVersion != 0 {
		t.Fatalf("empty result=%+v err=%v", result, err)
	}
	version, _ := st.GetUpstreamIntelligenceFactVersion(ctx, 11)
	if version.FactVersion != 5 {
		t.Fatalf("empty prune changed fact version: %+v", version)
	}
}

func TestMemoryPruneUpstreamIntelligenceHistoryProtectsCurrentAndReferencedEvidence(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }

	runs := []contracts.UpstreamCollectionRun{
		retentionTestRun("event-evidence", 31, "source-a", now.Add(-150*24*time.Hour), 1),
		retentionTestRun("absence-evidence", 31, "source-a", now.Add(-140*24*time.Hour), 2),
		retentionTestRun("latest-complete", 31, "source-a", now.Add(-130*24*time.Hour), 3),
		retentionTestRun("latest-failed", 31, "source-a", now.Add(-120*24*time.Hour), 4),
		retentionTestRun("deletable", 31, "source-a", now.Add(-160*24*time.Hour), 5),
	}
	runs[3].Status = contracts.UpstreamCollectionFailed
	runs[3].Coverage = contracts.UpstreamCoverageUnavailable
	runs[3].ErrorCode = contracts.UpstreamCollectionErrorUpstreamUnavailable
	runs[3].Retryable = true
	for _, run := range runs {
		seedMemoryRetentionRun(st, run)
	}
	st.upstreamIntelAbsences = []UpstreamSnapshotAbsence{{
		UserID: 31, SourceID: "source-a", ComparisonKey: "group:1:g", LastPresentRunID: "absence-evidence",
	}}
	st.upstreamIntelChanges = []contracts.UpstreamChangeEvent{{
		UserID: 31, SourceID: "source-a", BeforeObservationID: "offer-event-evidence",
	}}

	result, err := st.PruneUpstreamIntelligenceHistory(ctx, 31, now.Add(-90*24*time.Hour), 100)
	if err != nil || result.RunsDeleted != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, runID := range []string{"event-evidence", "absence-evidence", "latest-complete", "latest-failed"} {
		assertMemoryRetentionRun(t, st, 31, runID, true)
	}
	assertMemoryRetentionRun(t, st, 31, "deletable", false)
	if len(st.upstreamIntelChanges) != 1 || len(st.upstreamIntelAbsences) != 1 {
		t.Fatalf("long-lived evidence was deleted: changes=%d absences=%d", len(st.upstreamIntelChanges), len(st.upstreamIntelAbsences))
	}
}

func TestMemoryPruneUpstreamIntelligenceHistoryProtectsCurrentFactPerIdentity(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }

	older := retentionTestRun("older", 41, "source-a", now.Add(-150*24*time.Hour), 1)
	newer := retentionTestRun("newer", 41, "source-a", now.Add(-120*24*time.Hour), 2)
	seedMemoryRetentionRun(st, older)
	seedMemoryRetentionRun(st, newer)
	// The newer complete snapshot changed the existing default offer but did not
	// contain this independent legacy model. Its current read-model value still
	// lives in the old run and must survive raw-history retention.
	st.upstreamIntelOffers = append(st.upstreamIntelOffers, contracts.UpstreamOfferObservation{
		UserID: 41, SourceID: "source-a", RunID: older.ID, ID: "legacy-current",
		GroupKey: "default", ModelKey: "legacy", PriceDimension: contracts.UpstreamPriceInput,
		ObservedAt: older.ObservedAt,
	})

	result, err := st.PruneUpstreamIntelligenceHistory(ctx, 41, now.Add(-90*24*time.Hour), 100)
	if err != nil || result.RunsDeleted != 0 {
		t.Fatalf("current fact run pruned: result=%+v err=%v", result, err)
	}
	assertMemoryRetentionRun(t, st, 41, "older", true)
	assertMemoryRetentionRun(t, st, 41, "newer", true)
}

func TestMemoryPruneUpstreamIntelligenceHistoryIgnoresUnfinalizedCurrentFactCandidates(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }

	finalized := retentionTestRun("finalized", 42, "source-a", now.Add(-150*24*time.Hour), 1)
	unfinalized := retentionTestRun("unfinalized", 42, "source-a", now.Add(-120*24*time.Hour), 0)
	seedMemoryRetentionRun(st, finalized)
	seedMemoryRetentionRun(st, unfinalized)

	result, err := st.PruneUpstreamIntelligenceHistory(ctx, 42, now.Add(-90*24*time.Hour), 100)
	if err != nil || result.RunsDeleted != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	// Only finalized observations feed the current materialized read model.
	// A newer unfinalized batch must not become retention evidence.
	assertMemoryRetentionRun(t, st, 42, "finalized", true)
	assertMemoryRetentionRun(t, st, 42, "unfinalized", false)
}

func TestMemoryListUpstreamIntelligenceRetentionOwnersUsesStableCursor(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	for _, userID := range []int64{30, 10, 20} {
		run := retentionTestRun("old", userID, "source", now.Add(-100*24*time.Hour), 1)
		seedMemoryRetentionRun(st, run)
	}
	seedMemoryRetentionRun(st, retentionTestRun("fresh", 40, "source", now.Add(-10*24*time.Hour), 1))

	first, err := st.ListUpstreamIntelligenceRetentionOwners(ctx, now.Add(-90*24*time.Hour), 0, 2)
	if err != nil || len(first) != 2 || first[0] != 10 || first[1] != 20 {
		t.Fatalf("first=%v err=%v", first, err)
	}
	second, err := st.ListUpstreamIntelligenceRetentionOwners(ctx, now.Add(-90*24*time.Hour), first[1], 2)
	if err != nil || len(second) != 1 || second[0] != 30 {
		t.Fatalf("second=%v err=%v", second, err)
	}
	if _, err := st.ListUpstreamIntelligenceRetentionOwners(ctx, time.Time{}, 0, 2); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero cutoff err=%v, want invalid", err)
	}
}

func TestPostgresUpstreamIntelligenceRetentionSourceGuardsOwnerEvidenceAndBatching(t *testing.T) {
	raw, err := os.ReadFile("upstream_intelligence_retention.go")
	if err != nil {
		t.Fatal(err)
	}
	code := strings.ToLower(string(raw))
	for _, required := range []string{
		"run.user_id=$1 and run.received_at<$2",
		"limit $3",
		"for update of run skip locked",
		"newer.user_id=run.user_id and newer.source_id=run.source_id",
		"newer.status='succeeded' and newer.coverage='complete'",
		"absence.user_id=run.user_id and absence.source_id=run.source_id",
		"current_wallet.run_id=run.id\n\t\t  and run.finalized_fact_version>0",
		"newer_wallet_run.finalized_fact_version>0",
		"current_offer.run_id=run.id\n\t\t  and run.finalized_fact_version>0",
		"newer_offer_run.finalized_fact_version>0",
		"event.user_id=run.user_id and event.source_id=run.source_id",
		"delete from upstream_collection_runs where user_id=$1 and id=any($2::text[])",
		"recordupstreamintelligencefactmutationtx(ctx, tx, userid, upstreamintelligencefactmutationretention, \"\")",
	} {
		if !strings.Contains(code, required) {
			t.Errorf("postgres retention lacks %q", required)
		}
	}
	if strings.Contains(code, "delete from upstream_change_events") || strings.Contains(code, "delete from upstream_snapshot_absences") {
		t.Fatal("raw-history retention deletes long-lived evidence tables")
	}
}

func TestUpstreamIntelligenceRetentionMigrationIndexesPrunePredicates(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0059_upstream_intelligence_retention_indexes.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(raw))
	for _, required := range []string{
		"(user_id, received_at, id)",
		"(user_id, source_id, observed_at desc, id desc)",
		"(user_id, source_id, observed_at desc, run_id desc, id desc)",
		"(user_id, source_id, group_key, model_key, price_dimension, observed_at desc, run_id desc, id desc)",
		"where finalized_fact_version > 0",
		"last_present_run_id",
		"before_observation_id",
		"after_observation_id",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("retention migration lacks %q", required)
		}
	}
}

func retentionTestRun(id string, userID int64, sourceID string, receivedAt time.Time, version int64) contracts.UpstreamCollectionRun {
	completedAt := receivedAt
	return contracts.UpstreamCollectionRun{
		ID: id, UserID: userID, SourceID: sourceID, ConnectorID: "connector",
		Status: contracts.UpstreamCollectionSucceeded, Coverage: contracts.UpstreamCoverageComplete,
		ObservedAt: receivedAt, ReceivedAt: receivedAt, CompletedAt: &completedAt, FinalizedFactVersion: version,
	}
}

func seedMemoryRetentionRun(st *MemoryStore, run contracts.UpstreamCollectionRun) {
	st.upstreamIntelRuns = append(st.upstreamIntelRuns, run)
	st.upstreamIntelBatches = append(st.upstreamIntelBatches, UpstreamIntelligenceIngestBatch{UserID: run.UserID, SourceID: run.SourceID, RunID: run.ID})
	st.upstreamIntelWallets = append(st.upstreamIntelWallets, contracts.UpstreamWalletObservation{
		UserID: run.UserID, SourceID: run.SourceID, RunID: run.ID, ID: "wallet-" + run.ID,
		ObservedAt: run.ObservedAt,
	})
	st.upstreamIntelOffers = append(st.upstreamIntelOffers, contracts.UpstreamOfferObservation{
		UserID: run.UserID, SourceID: run.SourceID, RunID: run.ID, ID: "offer-" + run.ID,
		GroupKey: "default", ModelKey: "model", PriceDimension: contracts.UpstreamPriceInput,
		ObservedAt: run.ObservedAt,
	})
	if run.FinalizedFactVersion > 0 {
		st.upstreamIntelFinalized[memoryUpstreamFinalizationKey(run.UserID, run.ID)] = memoryUpstreamFinalization{UserID: run.UserID, RunID: run.ID, FactVersion: run.FinalizedFactVersion}
	}
}

func assertMemoryRetentionRun(t *testing.T, st *MemoryStore, userID int64, runID string, want bool) {
	t.Helper()
	found := false
	for _, run := range st.upstreamIntelRuns {
		if run.UserID == userID && run.ID == runID {
			found = true
			break
		}
	}
	if found != want {
		t.Fatalf("owner %d run %s exists=%v want=%v", userID, runID, found, want)
	}
}
