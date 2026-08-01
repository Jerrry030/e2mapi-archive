package healthmetrics

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// The service must aggregate only the observations inside each window: a sample
// older than 1m must shape the 5m snapshot but not the 1m one.
func TestServiceRecomputeChannelWindows(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	svc := NewService(st, WithClock(func() time.Time { return now }),
		WithWindows(contracts.Window1m, contracts.Window5m))

	// 6 fresh successes (within 1m) + 4 old failures (2m ago, inside 5m only).
	for i := 0; i < 6; i++ {
		mustRecord(t, svc, ctx, contracts.ChannelObservation{
			ChannelID: "ch-1", PoolID: "pool-1", Success: true,
			FirstTokenMS: 200, TotalMS: 900, ObservedAt: now.Add(-20 * time.Second),
		})
	}
	for i := 0; i < 4; i++ {
		mustRecord(t, svc, ctx, contracts.ChannelObservation{
			ChannelID: "ch-1", PoolID: "pool-1", Success: false,
			ErrorType: contracts.ErrorServer, ObservedAt: now.Add(-2 * time.Minute),
		})
	}

	snaps, err := svc.RecomputeChannel(ctx, "ch-1")
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("want 2 window snapshots, got %d", len(snaps))
	}

	one := snapByWindow(t, snaps, contracts.Window1m)
	if one.SampleCount != 6 || !approx(one.SuccessRate, 1.0) {
		t.Fatalf("1m window should see only the 6 fresh successes, got count=%d rate=%v", one.SampleCount, one.SuccessRate)
	}
	five := snapByWindow(t, snaps, contracts.Window5m)
	if five.SampleCount != 10 || !approx(five.SuccessRate, 0.6) {
		t.Fatalf("5m window should see all 10 samples, got count=%d rate=%v", five.SampleCount, five.SuccessRate)
	}

	// An identical recompute must resolve to the existing immutable revision,
	// not append a duplicate or mutate historical evidence.
	if _, err := svc.RecomputeChannel(ctx, "ch-1"); err != nil {
		t.Fatalf("recompute #2: %v", err)
	}
	stored, err := st.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{ChannelID: "ch-1"})
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("identical recompute must keep one revision per window, got %d current rows", len(stored))
	}
}

// RecordObservation defaults source to passive and stamps ObservedAt.
func TestServiceRecordDefaults(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	svc := NewService(st, WithClock(func() time.Time { return now }))

	saved, err := svc.RecordObservation(ctx, contracts.ChannelObservation{ChannelID: "ch-1", Success: true})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if saved.Source != contracts.ObservationPassive {
		t.Fatalf("want default passive source, got %q", saved.Source)
	}
	if !saved.ObservedAt.Equal(now) {
		t.Fatalf("want stamped observed_at %v, got %v", now, saved.ObservedAt)
	}
}

// One noisy downstream must not lower the snapshot of every other downstream
// using the same upstream channel. Model latency/error distributions are also
// isolated inside a downstream.
func TestServiceRecomputeChannelIsolatesInstanceAndModel(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 5, 12, 0, 20, 0, time.UTC)
	st := store.NewMemoryStore(now)
	svc := NewService(st, WithClock(func() time.Time { return now }), WithWindows(contracts.Window1m))

	seed := func(instanceID, model string, success bool, ttft float64) {
		for i := 0; i < 5; i++ {
			observation := contracts.ChannelObservation{
				ChannelID: "ch-shared", InstanceID: instanceID, PoolID: "pool-1", Model: model,
				Success: success, FirstTokenMS: ttft, TotalMS: ttft * 2,
				ObservedAt: now.Add(-10 * time.Second),
			}
			if !success {
				observation.ErrorType = contracts.ErrorServer
			}
			mustRecord(t, svc, ctx, observation)
		}
	}
	seed("inst-good", "model-a", true, 200)
	seed("inst-bad", "model-a", false, 0)
	seed("inst-good", "model-b", true, 9000)

	if _, err := svc.RecomputeChannel(ctx, "ch-shared"); err != nil {
		t.Fatalf("recompute: %v", err)
	}
	get := func(instanceID, model string) contracts.ChannelHealthSnapshot {
		snaps, err := st.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{
			ChannelID: "ch-shared", InstanceID: instanceID, Model: model, Window: contracts.Window1m,
		})
		if err != nil || len(snaps) != 1 {
			t.Fatalf("snapshot %s/%s: snapshots=%+v err=%v", instanceID, model, snaps, err)
		}
		return snaps[0]
	}
	good := get("inst-good", "model-a")
	bad := get("inst-bad", "model-a")
	slow := get("inst-good", "model-b")
	if good.HealthState != contracts.HealthHealthy || good.SuccessRate != 1 {
		t.Fatalf("healthy downstream was polluted: %+v", good)
	}
	if bad.HealthState != contracts.HealthUnhealthy || bad.SuccessRate != 0 {
		t.Fatalf("bad downstream did not retain its own failures: %+v", bad)
	}
	if slow.HealthState != contracts.HealthUnhealthy || slow.TTFTP95 != 9000 {
		t.Fatalf("slow model was mixed with fast model: %+v", slow)
	}
}

func TestServiceSnapshotBucketsRetainHistoryAndCurrentDefaultsLatest(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 5, 12, 0, 20, 0, time.UTC)
	st := store.NewMemoryStore(now)
	svc := NewService(st, WithClock(func() time.Time { return now }), WithWindows(contracts.Window1m))
	scope := contracts.ChannelHealthScope{ChannelID: "ch-1", InstanceID: "inst-1", Model: "model-a"}
	mustRecord(t, svc, ctx, contracts.ChannelObservation{
		ChannelID: scope.ChannelID, InstanceID: scope.InstanceID, Model: scope.Model,
		Success: true, ObservedAt: now,
	})
	if _, err := svc.RecomputeScopeAt(ctx, scope, now); err != nil {
		t.Fatalf("first recompute: %v", err)
	}
	later := now.Add(time.Minute)
	if _, err := svc.RecomputeScopeAt(ctx, scope, later); err != nil {
		t.Fatalf("second recompute: %v", err)
	}

	filter := contracts.ChannelHealthSnapshotFilter{
		ChannelID: scope.ChannelID, InstanceID: scope.InstanceID, Model: scope.Model, Window: contracts.Window1m,
	}
	current, err := st.ListChannelHealthSnapshots(ctx, filter)
	if err != nil || len(current) != 1 || !current[0].BucketStart.Equal(later.Truncate(time.Minute)) {
		t.Fatalf("current snapshot: %+v err=%v", current, err)
	}
	filter.IncludeHistory = true
	history, err := st.ListChannelHealthSnapshots(ctx, filter)
	if err != nil || len(history) != 2 {
		t.Fatalf("snapshot history: %+v err=%v", history, err)
	}
}

func TestServiceIdleScopeDecaysOnceThenStopsChurning(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	st := store.NewMemoryStore(now)
	svc := NewService(st, WithClock(func() time.Time { return now }), WithWindows(contracts.Window1m))
	for i := 0; i < 5; i++ {
		mustRecord(t, svc, ctx, contracts.ChannelObservation{
			ChannelID: "ch-1", InstanceID: "inst-1", Model: "model-a", Success: true, ObservedAt: now,
		})
	}
	if _, err := svc.RecomputeChannel(ctx, "ch-1"); err != nil {
		t.Fatalf("initial recompute: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := svc.RecomputeChannel(ctx, "ch-1"); err != nil {
		t.Fatalf("idle decay: %v", err)
	}
	now = now.Add(time.Minute)
	if snaps, err := svc.RecomputeChannel(ctx, "ch-1"); err != nil || len(snaps) != 0 {
		t.Fatalf("unknown scope should stop churning: snapshots=%+v err=%v", snaps, err)
	}
	history, err := st.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{
		ChannelID: "ch-1", InstanceID: "inst-1", Model: "model-a", IncludeHistory: true,
	})
	if err != nil || len(history) != 2 || history[0].HealthState != contracts.HealthUnknown {
		t.Fatalf("want sampled snapshot plus one unknown decay: history=%+v err=%v", history, err)
	}
}

// RecomputePool must fan out across a pool's channels.
func TestServiceRecomputePool(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "p"})
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	chA, _ := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: pool.ID, DisplayName: "A"})
	chB, _ := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: pool.ID, DisplayName: "B"})

	svc := NewService(st, WithClock(func() time.Time { return now }),
		WithWindows(contracts.Window1m))
	for _, id := range []string{chA.ID, chB.ID} {
		mustRecord(t, svc, ctx, contracts.ChannelObservation{ChannelID: id, PoolID: pool.ID, Success: true, FirstTokenMS: 100, TotalMS: 500, ObservedAt: now})
	}

	snaps, err := svc.RecomputePool(ctx, pool.ID)
	if err != nil {
		t.Fatalf("recompute pool: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("want a snapshot per channel, got %d", len(snaps))
	}
}

func mustRecord(t *testing.T, svc *Service, ctx context.Context, o contracts.ChannelObservation) {
	t.Helper()
	if _, err := svc.RecordObservation(ctx, o); err != nil {
		t.Fatalf("record observation: %v", err)
	}
}

func snapByWindow(t *testing.T, snaps []contracts.ChannelHealthSnapshot, w contracts.HealthWindow) contracts.ChannelHealthSnapshot {
	t.Helper()
	for _, s := range snaps {
		if s.Window == w {
			return s
		}
	}
	t.Fatalf("no snapshot for window %q", w)
	return contracts.ChannelHealthSnapshot{}
}
