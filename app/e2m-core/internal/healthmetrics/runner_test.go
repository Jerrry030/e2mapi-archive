package healthmetrics

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// The runner's sweep must recompute snapshots for live pools and skip retired
// ones, so an idle/dead pool does not churn unknown snapshots while active pools
// stay fresh for the strategy engine.
func TestRunnerSweepRecomputesLivePoolsOnly(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	st := store.NewMemoryStore(now)

	live, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "live", Status: contracts.UpstreamPoolActive})
	retired, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "retired", Status: contracts.UpstreamPoolRetired})
	liveCh, _ := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: live.ID, DisplayName: "L", Status: contracts.UpstreamChannelActive})
	retiredCh, _ := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: retired.ID, DisplayName: "R", Status: contracts.UpstreamChannelActive})

	svc := NewService(st, WithClock(func() time.Time { return now }), WithWindows(contracts.Window1m))
	// Seed one observation on each channel so a recompute would produce a sampled
	// snapshot (distinguishable from "never recomputed").
	for _, id := range []string{liveCh.ID, retiredCh.ID} {
		if _, err := svc.RecordObservation(ctx, contracts.ChannelObservation{ChannelID: id, Success: true, FirstTokenMS: 100, TotalMS: 400, ObservedAt: now}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	NewRunner(st, svc, time.Minute).sweep(ctx)

	// Live channel got a snapshot; retired pool's channel did not.
	liveSnaps, _ := st.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{ChannelID: liveCh.ID})
	if len(liveSnaps) != 1 || liveSnaps[0].SampleCount != 1 {
		t.Fatalf("live pool channel should have a recomputed snapshot, got %+v", liveSnaps)
	}
	retiredSnaps, _ := st.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{ChannelID: retiredCh.ID})
	if len(retiredSnaps) != 0 {
		t.Fatalf("retired pool must be skipped, got %d snapshots", len(retiredSnaps))
	}
}

// NewRunner clamps a non-positive interval to the default so a misconfigured
// cadence still ticks.
func TestNewRunnerDefaultInterval(t *testing.T) {
	r := NewRunner(nil, nil, 0)
	if r.interval != DefaultRecomputeInterval {
		t.Fatalf("want default interval %s, got %s", DefaultRecomputeInterval, r.interval)
	}
}
