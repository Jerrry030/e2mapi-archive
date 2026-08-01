package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPostgresUpstreamIntelligenceIngestCapacityAtomicOwnerIsolationAndReplay(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("E2M_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL store: %v", err)
	}
	t.Cleanup(st.Close)

	ownerA := createPostgresCapacityOwner(t, ctx, st, "a")
	ownerB := createPostgresCapacityOwner(t, ctx, st, "b")
	limit := UpstreamIntelligenceIngestCapacityLimit{Window: time.Hour, MaxBatches: 10, MaxFacts: 10}
	var wait sync.WaitGroup
	errorsOut := make(chan error, 50)
	for batchNo := 0; batchNo < 50; batchNo++ {
		wait.Add(1)
		go func(batchNo int) {
			defer wait.Done()
			_, admitErr := st.AdmitUpstreamIntelligenceIngest(context.Background(), UpstreamIntelligenceIngestCapacityRequest{
				UserID: ownerA, RunID: "parallel-" + newID("run"), BatchNo: batchNo,
				PayloadHash: capacityTestHash(byte('a' + batchNo%6)), FactCount: 1, Limit: limit,
			})
			errorsOut <- admitErr
		}(batchNo)
	}
	wait.Wait()
	close(errorsOut)
	accepted, rejected := 0, 0
	for admitErr := range errorsOut {
		switch {
		case admitErr == nil:
			accepted++
		case errors.Is(admitErr, ErrUpstreamIntelligenceIngestQuotaExceeded):
			rejected++
		default:
			t.Fatalf("unexpected parallel admission error: %v", admitErr)
		}
	}
	if accepted != 10 || rejected != 40 {
		t.Fatalf("parallel accepted=%d rejected=%d", accepted, rejected)
	}
	replayRaceOwner := createPostgresCapacityOwner(t, ctx, st, "same-payload-race")
	replayResults := make(chan UpstreamIntelligenceIngestCapacityResult, 20)
	replayErrors := make(chan error, 20)
	for attempt := 0; attempt < 20; attempt++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, admitErr := st.AdmitUpstreamIntelligenceIngest(context.Background(), UpstreamIntelligenceIngestCapacityRequest{
				UserID: replayRaceOwner, RunID: "same-payload", BatchNo: 0,
				PayloadHash: capacityTestHash('f'), FactCount: 1, Limit: limit,
			})
			replayResults <- result
			replayErrors <- admitErr
		}()
	}
	wait.Wait()
	close(replayResults)
	close(replayErrors)
	for admitErr := range replayErrors {
		if admitErr != nil {
			t.Fatalf("same-payload concurrent admission: %v", admitErr)
		}
	}
	charged, freeReplays := 0, 0
	for result := range replayResults {
		if result.BatchesUsed != 1 || result.FactsUsed != 1 || !result.Admitted {
			t.Fatalf("same-payload result=%+v", result)
		}
		if result.Replay {
			freeReplays++
		} else {
			charged++
		}
	}
	if charged != 1 || freeReplays != 19 {
		t.Fatalf("same-payload charged=%d free_replays=%d", charged, freeReplays)
	}
	blocked, err := st.AdmitUpstreamIntelligenceIngest(ctx, UpstreamIntelligenceIngestCapacityRequest{
		UserID: ownerA, RunID: "blocked-window-evidence", BatchNo: 0, PayloadHash: capacityTestHash('c'), FactCount: 1, Limit: limit,
	})
	if !errors.Is(err, ErrUpstreamIntelligenceIngestQuotaExceeded) || blocked.Admitted ||
		blocked.WindowEnd.Before(time.Now()) || blocked.WindowEnd.Sub(blocked.WindowStart) != time.Hour ||
		blocked.BatchesUsed != limit.MaxBatches || blocked.FactsUsed != limit.MaxFacts {
		t.Fatalf("blocked window evidence=%+v err=%v", blocked, err)
	}
	if got, err := st.AdmitUpstreamIntelligenceIngest(ctx, UpstreamIntelligenceIngestCapacityRequest{
		UserID: ownerB, RunID: "isolated", BatchNo: 0, PayloadHash: capacityTestHash('b'), FactCount: 1, Limit: limit,
	}); err != nil || got.BatchesUsed != 1 || got.FactsUsed != 1 {
		t.Fatalf("owner B=%+v err=%v", got, err)
	}
	overQuota, err := st.AdmitUpstreamIntelligenceIngest(ctx, UpstreamIntelligenceIngestCapacityRequest{
		UserID: ownerB, RunID: "over-configured-fact-quota", BatchNo: 0, PayloadHash: capacityTestHash('c'),
		FactCount: limit.MaxFacts + 1, Limit: limit,
	})
	if !errors.Is(err, ErrUpstreamIntelligenceIngestQuotaExceeded) || overQuota.Admitted || overQuota.BatchesUsed != 1 || overQuota.FactsUsed != 1 {
		t.Fatalf("single batch over configured quota=%+v err=%v", overQuota, err)
	}

	// Seed a durable receipt in a deliberately old window. The SQL function
	// must recognize it before charging the current owner window.
	replayOwner := createPostgresCapacityOwner(t, ctx, st, "replay")
	fixture := seedPostgresCapacityReceipt(t, ctx, st, replayOwner)
	if _, err := st.pool.Exec(ctx, `UPDATE upstream_intelligence_ingest_capacity_windows
		SET window_start=window_start-interval '2 hours',expires_at=expires_at-interval '2 hours' WHERE user_id=$1`, replayOwner); err != nil {
		t.Fatalf("age replay capacity window: %v", err)
	}
	got, err := st.AdmitUpstreamIntelligenceIngest(ctx, UpstreamIntelligenceIngestCapacityRequest{
		UserID: replayOwner, RunID: fixture.RunID, BatchNo: fixture.BatchNo, PayloadHash: fixture.PayloadHash,
		FactCount: fixture.WalletCount + fixture.OfferCount, Limit: UpstreamIntelligenceIngestCapacityLimit{Window: time.Hour, MaxBatches: 1, MaxFacts: 1},
	})
	if err != nil || !got.Replay || got.BatchesUsed != 0 || got.FactsUsed != 0 {
		t.Fatalf("cross-window replay=%+v err=%v", got, err)
	}
	var expiredWindows int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM upstream_intelligence_ingest_capacity_windows WHERE user_id=$1`, replayOwner).Scan(&expiredWindows); err != nil || expiredWindows != 0 {
		t.Fatalf("expired replay windows=%d err=%v", expiredWindows, err)
	}
}

func createPostgresCapacityOwner(t *testing.T, ctx context.Context, st *PostgresStore, label string) int64 {
	t.Helper()
	user, err := st.CreateUser(ctx, contracts.User{
		Email: newID("capacity-"+label) + "@example.test", PasswordHash: "test",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create capacity owner %s: %v", label, err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user.ID) })
	return user.ID
}

func seedPostgresCapacityReceipt(t *testing.T, ctx context.Context, st *PostgresStore, ownerID int64) UpstreamIntelligenceIngestBatch {
	t.Helper()
	// The receipt has FKs through source/run/Connector. Use the same fixture
	// primitives as the scale test, then retain only its first run for replay.
	owner, err := st.GetUser(ctx, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: owner.ID, Name: "capacity replay", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatal(err)
	}
	connectorID := "conn-" + newID("capacity-replay")
	if _, err := st.pool.Exec(ctx, `INSERT INTO connectors
		(connector_id,user_id,instance_id,name,status,token_hash,version,protocol_version,gateway_state)
		VALUES ($1,$2,$3,'capacity replay','online',$4,'1.0.0',$5,'{}'::jsonb)`,
		connectorID, owner.ID, instance.ID, "sha256:"+newID("hash"), contracts.ConnectorProtocolVersion); err != nil {
		t.Fatal(err)
	}
	source, err := st.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		UserID: owner.ID, ConnectorID: connectorID, InstanceID: instance.ID, LocalRef: "capacity-replay",
		Mode: contracts.UpstreamSourceExternal, Provider: "sub2api", DisplayName: "capacity replay", PollIntervalSeconds: 300, Status: contracts.UpstreamSourceActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	completed := now
	run, err := st.CreateUpstreamCollectionRun(ctx, contracts.UpstreamCollectionRun{
		ID: "run-" + newID("capacity-replay"), UserID: owner.ID, SourceID: source.ID, ConnectorID: connectorID,
		Trigger: contracts.UpstreamCollectionScheduled, Status: contracts.UpstreamCollectionSucceeded, Coverage: contracts.UpstreamCoverageComplete,
		StartedAt: now, ObservedAt: now, CompletedAt: &completed, BatchCount: 1, FactCount: 1, PageCount: 1,
		ManifestHash: capacityTestHash('d'), SnapshotHash: capacityTestHash('e'),
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, _, err := st.UpsertUpstreamIntelligenceIngestBatch(ctx, UpstreamIntelligenceIngestBatch{
		RunID: run.ID, UserID: owner.ID, SourceID: source.ID, BatchNo: 0, BatchCount: 1,
		PayloadHash: capacityTestHash('f'), ManifestHash: run.ManifestHash, OfferCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_intelligence_ingest_capacity_windows
		(user_id,window_start,window_seconds,expires_at,batches_used,facts_used)
		VALUES ($1,date_trunc('hour',statement_timestamp()),3600,date_trunc('hour',statement_timestamp())+interval '1 hour',1,1)`, ownerID); err != nil {
		t.Fatalf("seed replay capacity window: %v", err)
	}
	return receipt
}
