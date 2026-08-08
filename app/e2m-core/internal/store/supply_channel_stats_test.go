package store

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
)

// The reliability buckets must count exactly the outcomes settlement was told
// about: success and failure add one sample, neutral and idempotent replays
// add none, and timings land on both the usage row and the bucket sums.
func TestMemorySupplyChannelStatsAccumulateFromSettlement(t *testing.T) {
	st, owner, _, endpoint, plaintext := seedMemoryHybridSupply(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 7, 10, 2, 30, 0, time.UTC)
	st.now = func() time.Time { return base }
	st.mu.Lock()
	st.wallets[walletMapKey(owner.ID, "CNY")] = contracts.Wallet{UserID: owner.ID, Currency: "CNY", AvailableMicros: 500_000, Version: 1, UpdatedAt: base}
	st.mu.Unlock()

	reserved, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "stats-1", "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatal(err)
	}
	settled, err := st.SettleSupplyRequest(ctx, reserved.Reservation.ID, 1_000, 2_000,
		contracts.SupplyTelemetry{FirstTokenMS: 250, DurationMS: 1_400, Outcome: contracts.SupplyOutcomeSuccess})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Usage.FirstTokenMS != 250 || settled.Usage.DurationMS != 1_400 {
		t.Fatalf("usage timings not recorded: %+v", settled.Usage)
	}
	// An idempotent replay of the same reservation must not add a sample.
	if _, err := st.SettleSupplyRequest(ctx, reserved.Reservation.ID, 99, 99,
		contracts.SupplyTelemetry{FirstTokenMS: 9, DurationMS: 9, Outcome: contracts.SupplyOutcomeSuccess}); err != nil {
		t.Fatal(err)
	}

	reserved2, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "stats-2", "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatal(err)
	}
	// A failed attempt usually has a duration but no first byte.
	if _, err := st.ReleaseSupplyRequest(ctx, reserved2.Reservation.ID, "upstream_transport_error",
		contracts.SupplyTelemetry{DurationMS: 600, Outcome: contracts.SupplyOutcomeFailure}); err != nil {
		t.Fatal(err)
	}

	reserved3, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "stats-3", "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Neutral endings record timings on the usage row but add no sample.
	neutral, err := st.ReleaseSupplyRequest(ctx, reserved3.Reservation.ID, "upstream_http_non_retryable",
		contracts.SupplyTelemetry{FirstTokenMS: 80, DurationMS: 120, Outcome: contracts.SupplyOutcomeNeutral})
	if err != nil {
		t.Fatal(err)
	}
	if neutral.Usage.FirstTokenMS != 80 || neutral.Usage.DurationMS != 120 {
		t.Fatalf("neutral usage timings not recorded: %+v", neutral.Usage)
	}

	buckets, err := st.ListSupplyChannelStats(ctx, endpoint.ChannelID, base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 {
		t.Fatalf("buckets=%+v", buckets)
	}
	bucket := buckets[0]
	if !bucket.BucketStart.Equal(contracts.SupplyStatsBucketStart(base)) {
		t.Fatalf("bucket start=%v", bucket.BucketStart)
	}
	if bucket.Requests != 2 || bucket.Failures != 1 {
		t.Fatalf("sample counts wrong: %+v", bucket)
	}
	if bucket.TTFTSumMS != 250 || bucket.TTFTSamples != 1 || bucket.DurationSumMS != 2_000 || bucket.DurationSamples != 2 {
		t.Fatalf("timing sums wrong: %+v", bucket)
	}

	// The since filter excludes the bucket once it falls before the window.
	after, err := st.ListSupplyChannelStats(ctx, endpoint.ChannelID, base.Add(10*time.Minute))
	if err != nil || len(after) != 0 {
		t.Fatalf("after=%+v err=%v", after, err)
	}
	if _, err := st.ListSupplyChannelStats(ctx, "  ", base); err == nil {
		t.Fatal("blank channel id must be rejected")
	}
}

// Writing a sample must prune the same channel's buckets that have aged past
// the retention horizon.
func TestMemorySupplyChannelStatsRetentionPrune(t *testing.T) {
	st, owner, _, endpoint, plaintext := seedMemoryHybridSupply(t)
	ctx := context.Background()
	old := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return old }
	st.mu.Lock()
	st.wallets[walletMapKey(owner.ID, "CNY")] = contracts.Wallet{UserID: owner.ID, Currency: "CNY", AvailableMicros: 500_000, Version: 1, UpdatedAt: old}
	st.mu.Unlock()

	reserved, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "old-1", "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SettleSupplyRequest(ctx, reserved.Reservation.ID, 1, 1,
		contracts.SupplyTelemetry{DurationMS: 5, Outcome: contracts.SupplyOutcomeSuccess}); err != nil {
		t.Fatal(err)
	}

	now := old.Add(supplyStatsRetention + time.Hour)
	st.now = func() time.Time { return now }
	reserved2, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "new-1", "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SettleSupplyRequest(ctx, reserved2.Reservation.ID, 1, 1,
		contracts.SupplyTelemetry{DurationMS: 5, Outcome: contracts.SupplyOutcomeSuccess}); err != nil {
		t.Fatal(err)
	}

	buckets, err := st.ListSupplyChannelStats(ctx, endpoint.ChannelID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || !buckets[0].BucketStart.Equal(contracts.SupplyStatsBucketStart(now)) {
		t.Fatalf("retention prune failed: %+v", buckets)
	}
}
