package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestUI17OperationalMetricsLiveSemantics(t *testing.T) {
	if os.Getenv("E2M_UI17_OPERATIONAL_METRICS_LIVE") != "1" {
		t.Skip("E2M_UI17_OPERATIONAL_METRICS_LIVE is not set")
	}
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	before, err := st.GetOperationalMetrics(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 7, 27, 1, 0, 0, 0, time.UTC)
	observed, completed := started.Add(40*time.Millisecond), started.Add(80*time.Millisecond)
	input := UpstreamIntelligenceIngest{
		Source: contracts.UpstreamIntelligenceSource{
			ID: "ui17-source-0069", UserID: 900069, ConnectorID: "ui17-connector-0069", InstanceID: "ui17-instance-0069",
			LocalRef: "source-local-ref", Mode: contracts.UpstreamSourceOwned, Provider: "sub2api", DisplayName: "UI17 disposable source",
			Currency: "USD", PollIntervalSeconds: 300, Status: contracts.UpstreamSourceActive,
			Capabilities: contracts.UpstreamIntelligenceCapabilities{Balance: true, Groups: true, Rates: true, Prices: true},
		},
		Run: contracts.UpstreamCollectionRun{
			ID: "ui17-live-succeeded", UserID: 900069, SourceID: "ui17-source-0069", ConnectorID: "ui17-connector-0069",
			Trigger: contracts.UpstreamCollectionManual, Status: contracts.UpstreamCollectionSucceeded, Coverage: contracts.UpstreamCoverageComplete,
			StartedAt: started, ObservedAt: observed, CompletedAt: &completed, BatchCount: 1, FactCount: 1, PageCount: 1,
		},
		Batch: UpstreamIntelligenceIngestBatch{
			RunID: "ui17-live-succeeded", UserID: 900069, SourceID: "ui17-source-0069", BatchNo: 0, BatchCount: 1,
			PayloadHash: "2222222222222222222222222222222222222222222222222222222222222222",
			OfferCount:  1,
		},
		Offers: []contracts.UpstreamOfferObservation{{
			ID: "ui17-live-offer", RunID: "ui17-live-succeeded", UserID: 900069, SourceID: "ui17-source-0069",
			GroupKey: "ui17-live-group", ModelKey: "ui17-live-model", PriceDimension: contracts.UpstreamPriceInput,
			PerTokens: 1_000_000, Accuracy: contracts.UpstreamEvidenceExact, Coverage: contracts.UpstreamCoverageComplete,
			ObservedAt: observed, EffectiveAt: observed, FreshUntil: observed.Add(time.Hour), AdapterSchemaVersion: 1,
		}},
	}
	manifest, err := contracts.CalculateUpstreamIntelligenceManifestHash([]contracts.UpstreamIntelligenceManifestBatch{{BatchNo: 0, PayloadHash: input.Batch.PayloadHash}})
	if err != nil {
		t.Fatal(err)
	}
	input.Run.ManifestHash, input.Batch.ManifestHash = manifest, manifest

	if _, _, _, duplicate, err := st.IngestUpstreamIntelligenceBatch(ctx, input); err != nil || duplicate {
		t.Fatalf("first ingest duplicate=%v err=%v", duplicate, err)
	}
	var sourceStageErr, runStageErr, offerStageErr, batchStageErr error
	tx, err := st.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = lockUpstreamIntelligenceRunTx(ctx, tx, input.Source.UserID, input.Run.ID); err == nil {
		_, _, sourceStageErr = upsertUpstreamSourceTx(ctx, tx, input.Source)
		if sourceStageErr == nil {
			_, runStageErr = createUpstreamRunTx(ctx, tx, input.Run)
		}
		if sourceStageErr == nil && runStageErr == nil {
			_, offerStageErr = appendUpstreamOfferTx(ctx, tx, input.Offers[0])
		}
		if sourceStageErr == nil && runStageErr == nil && offerStageErr == nil {
			_, _, batchStageErr = upsertUpstreamBatchTx(ctx, tx, input.Batch)
		}
	}
	_ = tx.Rollback(ctx)
	t.Logf("replay stages source=%v run=%v offer=%v batch=%v", sourceStageErr, runStageErr, offerStageErr, batchStageErr)
	if _, _, _, duplicate, err := st.IngestUpstreamIntelligenceBatch(ctx, input); err != nil || !duplicate {
		t.Fatalf("replay ingest duplicate=%v err=%v", duplicate, err)
	}
	first, firstVersion, err := st.FinalizeUpstreamCollectionRun(ctx, 900069, input.Run.ID)
	if err != nil {
		t.Fatalf("finalize: %T %#v", err, err)
	}
	replay, replayVersion, err := st.FinalizeUpstreamCollectionRun(ctx, 900069, input.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.FinalizedFactVersion == 0 || replay.FinalizedFactVersion != first.FinalizedFactVersion || replayVersion.FactVersion != firstVersion.FactVersion {
		t.Fatalf("finalization replay first=%+v/%+v replay=%+v/%+v", first, firstVersion, replay, replayVersion)
	}
	after, err := st.GetOperationalMetrics(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got := after.IngestFactsByOutcome["accepted"] - before.IngestFactsByOutcome["accepted"]; got != 1 {
		t.Fatalf("accepted fact delta=%d", got)
	}
	if got := after.IngestFactsByOutcome["duplicate"] - before.IngestFactsByOutcome["duplicate"]; got != 1 {
		t.Fatalf("duplicate fact delta=%d", got)
	}
	if got := after.IngestRunsByResult["succeeded"] - before.IngestRunsByResult["succeeded"]; got != 1 {
		t.Fatalf("succeeded run delta=%d", got)
	}
	if got := after.CollectionFactsByResult["succeeded"] - before.CollectionFactsByResult["succeeded"]; got != 1 {
		t.Fatalf("succeeded fact delta=%d", got)
	}
	if got := after.CollectionCoverageByLevel["complete"] - before.CollectionCoverageByLevel["complete"]; got != 1 {
		t.Fatalf("complete coverage delta=%d", got)
	}
	if got := after.CollectionRunDurationSeconds["succeeded"].Count - before.CollectionRunDurationSeconds["succeeded"].Count; got != 1 {
		t.Fatalf("succeeded duration count delta=%d", got)
	}
	secondStore, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secondStore.Close)
	global, err := secondStore.GetOperationalMetrics(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if global.IngestRunsByResult["succeeded"] != after.IngestRunsByResult["succeeded"] ||
		global.IngestFactsByOutcome["duplicate"] != after.IngestFactsByOutcome["duplicate"] {
		t.Fatalf("independent store saw different global counters first=%+v second=%+v", after, global)
	}

	var rollbackBefore, rollbackAfter int64
	if err := st.pool.QueryRow(ctx, "SELECT COALESCE((SELECT total FROM operational_event_counters WHERE kind='cross_owner_rejected'),0)").Scan(&rollbackBefore); err != nil {
		t.Fatal(err)
	}
	tx, err = st.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operational_event_counters(kind,total) VALUES('cross_owner_rejected',7)
		ON CONFLICT(kind) DO UPDATE SET total=operational_event_counters.total+EXCLUDED.total`); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, "SELECT COALESCE((SELECT total FROM operational_event_counters WHERE kind='cross_owner_rejected'),0)").Scan(&rollbackAfter); err != nil {
		t.Fatal(err)
	}
	if rollbackAfter != rollbackBefore {
		t.Fatalf("rollback changed counter before=%d after=%d", rollbackBefore, rollbackAfter)
	}
	t.Logf("replay-safe finalization version=%d; counter rollback unchanged=%d", firstVersion.FactVersion, rollbackAfter)
}

func TestUI17FalseRemovalSignalSurvivesLivePostgresRollback(t *testing.T) {
	if os.Getenv("E2M_UI17_OPERATIONAL_METRICS_LIVE") != "1" {
		t.Skip("E2M_UI17_OPERATIONAL_METRICS_LIVE is not set")
	}
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	const userID int64 = 900069
	const runID = "ui17-false-removal"
	before, err := st.GetOperationalMetrics(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	versionBefore, err := st.GetUpstreamIntelligenceFactVersion(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	var eventsBefore, violationsBefore int64
	if err := st.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM upstream_change_events WHERE user_id=$1),
		COALESCE((SELECT total FROM operational_event_counters WHERE kind='false_removal_invariant'),0)`, userID).Scan(&eventsBefore, &violationsBefore); err != nil {
		t.Fatal(err)
	}

	comparisonKey := upstreamGroupAbsenceKey("ui17-corrupt-group")
	if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_snapshot_absences
		(source_id,user_id,comparison_key,consecutive_complete_runs,last_present_observation_id,last_present_run_id,first_absent_at,last_absent_run_id)
		VALUES ('ui17-source-0069',$1,$2,1,'ui17-prior-offer',$3,NULL,'ui17-prior-absent')
		ON CONFLICT (source_id,comparison_key) DO UPDATE SET
		 consecutive_complete_runs=1,last_present_observation_id='ui17-prior-offer',last_present_run_id=$3,
		 first_absent_at=NULL,last_absent_run_id='ui17-prior-absent'`, userID, comparisonKey, runID); err != nil {
		t.Fatal(err)
	}

	started := time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC)
	observed, completed := started.Add(20*time.Millisecond), started.Add(40*time.Millisecond)
	payloadHash := "3333333333333333333333333333333333333333333333333333333333333333"
	manifest, err := contracts.CalculateUpstreamIntelligenceManifestHash([]contracts.UpstreamIntelligenceManifestBatch{{BatchNo: 0, PayloadHash: payloadHash}})
	if err != nil {
		t.Fatal(err)
	}
	input := UpstreamIntelligenceIngest{
		Source: contracts.UpstreamIntelligenceSource{
			ID: "ui17-source-0069", UserID: userID, ConnectorID: "ui17-connector-0069", InstanceID: "ui17-instance-0069",
			LocalRef: "source-local-ref", Mode: contracts.UpstreamSourceOwned, Provider: "sub2api", DisplayName: "UI17 disposable source",
			Currency: "USD", PollIntervalSeconds: 300, Status: contracts.UpstreamSourceActive,
			Capabilities: contracts.UpstreamIntelligenceCapabilities{Balance: true, Groups: true, Rates: true, Prices: true},
		},
		Run: contracts.UpstreamCollectionRun{
			ID: runID, UserID: userID, SourceID: "ui17-source-0069", ConnectorID: "ui17-connector-0069",
			Trigger: contracts.UpstreamCollectionManual, Status: contracts.UpstreamCollectionSucceeded, Coverage: contracts.UpstreamCoverageComplete,
			StartedAt: started, ObservedAt: observed, CompletedAt: &completed, ManifestHash: manifest,
			BatchCount: 1, FactCount: 0, PageCount: 0,
		},
		Batch: UpstreamIntelligenceIngestBatch{
			RunID: runID, UserID: userID, SourceID: "ui17-source-0069", BatchNo: 0, BatchCount: 1,
			PayloadHash: payloadHash, ManifestHash: manifest,
		},
	}
	if _, _, _, duplicate, err := st.IngestUpstreamIntelligenceBatch(ctx, input); err != nil || duplicate {
		t.Fatalf("ingest duplicate=%v err=%v", duplicate, err)
	}
	_, _, err = st.FinalizeUpstreamCollectionRun(ctx, userID, runID)
	var invariantErr falseRemovalInvariantError
	if !errors.As(err, &invariantErr) {
		t.Fatalf("finalize error=%T %v, want falseRemovalInvariantError", err, err)
	}

	gotRun, err := st.GetUpstreamCollectionRun(ctx, userID, runID)
	if err != nil {
		t.Fatal(err)
	}
	versionAfter, err := st.GetUpstreamIntelligenceFactVersion(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := st.GetOperationalMetrics(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var eventsAfter, violationsAfter, absenceCount int64
	var firstAbsentAt *time.Time
	if err := st.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM upstream_change_events WHERE user_id=$1),
		COALESCE((SELECT total FROM operational_event_counters WHERE kind='false_removal_invariant'),0),
		(SELECT consecutive_complete_runs FROM upstream_snapshot_absences WHERE source_id='ui17-source-0069' AND comparison_key=$2),
		(SELECT first_absent_at FROM upstream_snapshot_absences WHERE source_id='ui17-source-0069' AND comparison_key=$2)`,
		userID, comparisonKey).Scan(&eventsAfter, &violationsAfter, &absenceCount, &firstAbsentAt); err != nil {
		t.Fatal(err)
	}
	if violationsAfter != violationsBefore+1 {
		t.Fatalf("false-removal counter before=%d after=%d", violationsBefore, violationsAfter)
	}
	if gotRun.FinalizedFactVersion != 0 || versionAfter.FactVersion != versionBefore.FactVersion || eventsAfter != eventsBefore || absenceCount != 1 || firstAbsentAt != nil {
		t.Fatalf("business rollback failed run_version=%d fact_version=%d/%d events=%d/%d absence_count=%d first_absent=%v",
			gotRun.FinalizedFactVersion, versionBefore.FactVersion, versionAfter.FactVersion, eventsBefore, eventsAfter, absenceCount, firstAbsentAt)
	}
	if after.IngestRunsByResult["succeeded"] != before.IngestRunsByResult["succeeded"] ||
		after.CollectionCoverageByLevel["complete"] != before.CollectionCoverageByLevel["complete"] ||
		after.CollectionRunDurationSeconds["succeeded"].Count != before.CollectionRunDurationSeconds["succeeded"].Count {
		t.Fatalf("rolled-back finalization changed operational counters before=%+v after=%+v", before, after)
	}
	t.Logf("false-removal violation persisted %d->%d; run/version/change/absence/counters rolled back", violationsBefore, violationsAfter)
}
