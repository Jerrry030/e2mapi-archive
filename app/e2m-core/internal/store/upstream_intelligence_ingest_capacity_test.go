package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMemoryUpstreamIntelligenceIngestCapacityIsOwnerScopedIdempotentAndWindowed(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 12, 0, 30, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }
	limit := UpstreamIntelligenceIngestCapacityLimit{Window: time.Minute, MaxBatches: 2, MaxFacts: 3}
	request := UpstreamIntelligenceIngestCapacityRequest{
		UserID: 11, RunID: "run-a", BatchNo: 0, PayloadHash: capacityTestHash('a'), FactCount: 2, Limit: limit,
	}

	first, err := st.AdmitUpstreamIntelligenceIngest(ctx, request)
	if err != nil || !first.Admitted || first.Replay || first.BatchesUsed != 1 || first.FactsUsed != 2 ||
		!first.WindowStart.Equal(now.Truncate(time.Minute)) || !first.WindowEnd.Equal(now.Truncate(time.Minute).Add(time.Minute)) {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	replay, err := st.AdmitUpstreamIntelligenceIngest(ctx, request)
	if err != nil || !replay.Admitted || !replay.Replay || replay.BatchesUsed != 1 || replay.FactsUsed != 2 {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}

	second := request
	second.BatchNo, second.PayloadHash, second.FactCount = 1, capacityTestHash('b'), 1
	if got, err := st.AdmitUpstreamIntelligenceIngest(ctx, second); err != nil || got.BatchesUsed != 2 || got.FactsUsed != 3 {
		t.Fatalf("second=%+v err=%v", got, err)
	}
	blocked := request
	blocked.RunID, blocked.PayloadHash, blocked.FactCount = "run-b", capacityTestHash('c'), 1
	got, err := st.AdmitUpstreamIntelligenceIngest(ctx, blocked)
	if !errors.Is(err, ErrUpstreamIntelligenceIngestQuotaExceeded) || got.Admitted || got.BatchesUsed != 2 || got.FactsUsed != 3 ||
		!got.WindowEnd.Equal(now.Truncate(time.Minute).Add(time.Minute)) {
		t.Fatalf("blocked=%+v err=%v", got, err)
	}
	oversizedForConfiguredQuota := blocked
	oversizedForConfiguredQuota.UserID = 23
	oversizedForConfiguredQuota.FactCount = limit.MaxFacts + 1
	got, err = st.AdmitUpstreamIntelligenceIngest(ctx, oversizedForConfiguredQuota)
	if !errors.Is(err, ErrUpstreamIntelligenceIngestQuotaExceeded) || got.Admitted || got.BatchesUsed != 0 || got.FactsUsed != 0 {
		t.Fatalf("single batch over configured quota=%+v err=%v", got, err)
	}

	otherOwner := blocked
	otherOwner.UserID = 22
	if got, err := st.AdmitUpstreamIntelligenceIngest(ctx, otherOwner); err != nil || got.BatchesUsed != 1 || got.FactsUsed != 1 {
		t.Fatalf("other owner=%+v err=%v", got, err)
	}
	now = now.Add(time.Minute)
	if got, err := st.AdmitUpstreamIntelligenceIngest(ctx, blocked); err != nil || got.BatchesUsed != 1 || got.FactsUsed != 1 {
		t.Fatalf("next window=%+v err=%v", got, err)
	}
}

func TestMemoryUpstreamIntelligenceIngestCapacityPrunesExpiredWindows(t *testing.T) {
	st := NewMemoryStore(time.Now())
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	limit := UpstreamIntelligenceIngestCapacityLimit{Window: time.Minute, MaxBatches: 10, MaxFacts: 10}
	request := UpstreamIntelligenceIngestCapacityRequest{
		UserID: 51, RunID: "first", BatchNo: 0, PayloadHash: capacityTestHash('a'), FactCount: 1, Limit: limit,
	}
	if _, err := st.AdmitUpstreamIntelligenceIngest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(st.upstreamIntelIngestCapacity) != 1 {
		t.Fatalf("capacity windows=%d, want 1", len(st.upstreamIntelIngestCapacity))
	}
	now = now.Add(time.Minute)
	request.RunID, request.PayloadHash = "second", capacityTestHash('b')
	if _, err := st.AdmitUpstreamIntelligenceIngest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(st.upstreamIntelIngestCapacity) != 1 {
		t.Fatalf("capacity windows=%d after expiry, want 1", len(st.upstreamIntelIngestCapacity))
	}
}

func TestMemoryUpstreamIntelligenceIngestCapacityConcurrentAdmissionIsBounded(t *testing.T) {
	st := NewMemoryStore(time.Now())
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	limit := UpstreamIntelligenceIngestCapacityLimit{Window: time.Minute, MaxBatches: 10, MaxFacts: 10}
	var wait sync.WaitGroup
	errorsOut := make(chan error, 100)
	for index := 0; index < 100; index++ {
		wait.Add(1)
		go func(batchNo int) {
			defer wait.Done()
			_, err := st.AdmitUpstreamIntelligenceIngest(context.Background(), UpstreamIntelligenceIngestCapacityRequest{
				UserID: 31, RunID: "parallel", BatchNo: batchNo, PayloadHash: capacityTestHash(byte('a' + batchNo%6)), FactCount: 1, Limit: limit,
			})
			errorsOut <- err
		}(index)
	}
	wait.Wait()
	close(errorsOut)
	accepted, rejected := 0, 0
	for err := range errorsOut {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrUpstreamIntelligenceIngestQuotaExceeded):
			rejected++
		default:
			t.Fatalf("unexpected admission error: %v", err)
		}
	}
	if accepted != 10 || rejected != 90 {
		t.Fatalf("accepted=%d rejected=%d", accepted, rejected)
	}
}

func TestUpstreamIntelligenceIngestCapacityMigrationUsesOwnerLockedAtomicAdmission(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0068_upstream_intelligence_ingest_capacity.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"PRIMARY KEY (user_id, window_start, window_seconds)",
		"FOR UPDATE", "p_user_id", "p_run_id", "p_batch_no", "p_payload_hash",
		"v_batches + 1 > p_max_batches", "v_facts + p_fact_count > p_max_facts",
		"admitted BOOLEAN", "FALSE,FALSE", "RETURN QUERY SELECT", "ON DELETE CASCADE",
		"idx_upstream_intelligence_ingest_capacity_windows_expiry", "LIMIT 1000", "FOR UPDATE SKIP LOCKED",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("capacity migration lacks %q", required)
		}
	}
}

func capacityTestHash(character byte) string { return strings.Repeat(string(character), 64) }
