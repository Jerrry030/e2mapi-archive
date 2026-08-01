package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

// Observations are append-only and filterable; the newest-first order with a
// limit is what the aggregator relies on to read a window cheaply.
func TestMemoryChannelObservationsAppendAndFilter(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	base := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	seed := []contracts.ChannelObservation{
		{ChannelID: "ch-a", InstanceID: "inst-1", PoolID: "pool-1", Model: "model-a", Success: true, ObservedAt: base.Add(-3 * time.Minute)},
		{ChannelID: "ch-a", InstanceID: "inst-1", PoolID: "pool-1", Model: "model-b", Success: false, ErrorType: contracts.ErrorTimeout, ObservedAt: base.Add(-2 * time.Minute)},
		{ChannelID: "ch-b", InstanceID: "inst-1", PoolID: "pool-1", Success: true, Source: contracts.ObservationProbe, ObservedAt: base.Add(-1 * time.Minute)},
	}
	for i := range seed {
		got, err := st.AppendChannelObservation(ctx, seed[i])
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if got.ID == "" {
			t.Fatalf("append must assign id, got %+v", got)
		}
		if got.Source == "" {
			t.Fatalf("append must default source, got %+v", got)
		}
	}

	// Filter by channel.
	chA, err := st.ListChannelObservations(ctx, contracts.ChannelObservationFilter{ChannelID: "ch-a"})
	if err != nil {
		t.Fatalf("list ch-a: %v", err)
	}
	if len(chA) != 2 {
		t.Fatalf("want 2 ch-a observations, got %d", len(chA))
	}
	modelA, err := st.ListChannelObservations(ctx, contracts.ChannelObservationFilter{
		ChannelID: "ch-a", InstanceID: "inst-1", Model: "model-a",
	})
	if err != nil || len(modelA) != 1 || modelA[0].Model != "model-a" {
		t.Fatalf("instance/model scope filter failed: observations=%+v err=%v", modelA, err)
	}
	// Newest first.
	if !chA[0].ObservedAt.After(chA[1].ObservedAt) {
		t.Fatalf("observations must be newest-first, got %v then %v", chA[0].ObservedAt, chA[1].ObservedAt)
	}

	// Filter by source.
	probes, err := st.ListChannelObservations(ctx, contracts.ChannelObservationFilter{Source: contracts.ObservationProbe})
	if err != nil {
		t.Fatalf("list probes: %v", err)
	}
	if len(probes) != 1 || probes[0].ChannelID != "ch-b" {
		t.Fatalf("source filter failed, got %+v", probes)
	}

	// Filter by Since window.
	recent, err := st.ListChannelObservations(ctx, contracts.ChannelObservationFilter{Since: base.Add(-90 * time.Second)})
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	if len(recent) != 1 || recent[0].ChannelID != "ch-b" {
		t.Fatalf("since filter should keep only the newest, got %+v", recent)
	}

	// Limit caps the result.
	limited, err := st.ListChannelObservations(ctx, contracts.ChannelObservationFilter{Limit: 1})
	if err != nil {
		t.Fatalf("list limited: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("limit=1 should return one, got %d", len(limited))
	}
}

func TestMemoryChannelObservationsSortsOutOfOrderBeforeLimit(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	base := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	for _, observedAt := range []time.Time{base, base.Add(-time.Minute), base.Add(time.Minute)} {
		if _, err := st.AppendChannelObservation(ctx, contracts.ChannelObservation{
			ChannelID: "ch-a", ObservedAt: observedAt,
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	got, err := st.ListChannelObservations(ctx, contracts.ChannelObservationFilter{ChannelID: "ch-a", Limit: 1})
	if err != nil || len(got) != 1 || !got[0].ObservedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("newest observation must win limit regardless of insert order: got=%+v err=%v", got, err)
	}
}

func TestMemoryChannelObservationAppendIsIdempotentByID(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	input := contracts.ChannelObservation{
		ID: "obs-stable-1", ChannelID: "ch-a", InstanceID: "inst-a", Model: "model-a",
		Success: true, FirstTokenMS: 100, TotalMS: 200,
	}
	first, err := st.AppendChannelObservation(ctx, input)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	second, err := st.AppendChannelObservation(ctx, input)
	if err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if second != first {
		t.Fatalf("idempotent retry changed stored fact: first=%+v second=%+v", first, second)
	}
	stored, _ := st.ListChannelObservations(ctx, contracts.ChannelObservationFilter{ChannelID: "ch-a"})
	if len(stored) != 1 {
		t.Fatalf("idempotent retry appended %d rows, want 1", len(stored))
	}

	conflict := input
	conflict.Model = "different-model"
	if _, err := st.AppendChannelObservation(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed content error=%v, want ErrConflict", err)
	}
}

func TestMemoryChannelObservationRetryCanonicalizesSubMicrosecondTime(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	input := contracts.ChannelObservation{
		ID: "obs-submicro-1", ChannelID: "ch-a", Model: "model-a", Success: true,
		ObservedAt: time.Date(2026, 7, 13, 12, 0, 0, 123456789, time.UTC),
	}
	first, err := st.AppendChannelObservation(ctx, input)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	second, err := st.AppendChannelObservation(ctx, input)
	if err != nil || second != first {
		t.Fatalf("sub-microsecond retry: first=%+v second=%+v err=%v", first, second, err)
	}
	if first.ObservedAt.Nanosecond()%1000 != 0 {
		t.Fatalf("stored timestamp was not canonicalized: %v", first.ObservedAt)
	}
}

func TestMemoryChannelObservationRetryMayRepeatResolvedDefaults(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	input := contracts.ChannelObservation{ID: "obs-defaults-1", ChannelID: "ch-a", Success: true}
	first, err := st.AppendChannelObservation(ctx, input)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	if _, err := st.AppendChannelObservation(ctx, first); err != nil {
		t.Fatalf("retry with resolved source/time should be idempotent: %v", err)
	}
}

// Snapshots append immutable revisions inside one exact scope bucket. Different
// downstreams, models, windows, buckets and revisions remain queryable.
func TestMemoryChannelHealthSnapshotUpsert(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))

	bucket := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	first, err := st.UpsertChannelHealthSnapshot(ctx, contracts.ChannelHealthSnapshot{
		ChannelID: "ch-a", InstanceID: "inst-1", PoolID: "pool-1", Model: "model-a",
		Window: contracts.Window5m, BucketStart: bucket,
		SampleCount: 100, SuccessRate: 0.5, ErrorRate: 0.5,
		QualitySampleCount: 50, QualitySuccessRate: 0.9, QualityErrorRate: 0.1,
		HealthState: contracts.HealthDegraded,
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.ID == "" || first.CreatedAt.IsZero() {
		t.Fatalf("upsert must assign id + created_at, got %+v", first)
	}
	if first.SampleCount != 100 || first.QualitySampleCount != 50 || first.SuccessRate != 0.5 || first.QualitySuccessRate != 0.9 {
		t.Fatalf("memory store conflated factual and quality metrics: %+v", first)
	}

	// Changed facts in the same scope bucket append a new evidence revision.
	second, err := st.UpsertChannelHealthSnapshot(ctx, contracts.ChannelHealthSnapshot{
		ChannelID: "ch-a", InstanceID: "inst-1", PoolID: "pool-1", Model: "model-a",
		Window: contracts.Window5m, BucketStart: bucket, CreatedAt: first.CreatedAt.Add(time.Microsecond),
		SuccessRate: 0.99, HealthState: contracts.HealthHealthy,
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("changed evidence reused immutable id %q", first.ID)
	}
	replayed, err := st.UpsertChannelHealthSnapshot(ctx, second)
	if err != nil || replayed.ID != second.ID {
		t.Fatalf("identical replay=%+v err=%v", replayed, err)
	}

	// Different window, downstream, model, and bucket each create a new row.
	if _, err := st.UpsertChannelHealthSnapshot(ctx, contracts.ChannelHealthSnapshot{
		ChannelID: "ch-a", InstanceID: "inst-1", Model: "model-a", Window: contracts.Window1m, BucketStart: bucket, SuccessRate: 1,
	}); err != nil {
		t.Fatalf("1m upsert: %v", err)
	}
	for _, snap := range []contracts.ChannelHealthSnapshot{
		{ChannelID: "ch-a", InstanceID: "inst-2", Model: "model-a", Window: contracts.Window5m, BucketStart: bucket, SuccessRate: 0.1},
		{ChannelID: "ch-a", InstanceID: "inst-1", Model: "model-b", Window: contracts.Window5m, BucketStart: bucket, SuccessRate: 0.2},
		{ChannelID: "ch-a", InstanceID: "inst-1", Model: "model-a", Window: contracts.Window5m, BucketStart: bucket.Add(time.Minute), SuccessRate: 1},
	} {
		if _, err := st.UpsertChannelHealthSnapshot(ctx, snap); err != nil {
			t.Fatalf("scoped upsert: %v", err)
		}
	}

	all, err := st.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{ChannelID: "ch-a", IncludeHistory: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 6 {
		t.Fatalf("want 6 independently scoped/bucketed revision rows, got %d", len(all))
	}
	var historicalFirst contracts.ChannelHealthSnapshot
	for _, snapshot := range all {
		if snapshot.ID == first.ID {
			historicalFirst = snapshot
		}
	}
	if historicalFirst.ID == "" || historicalFirst.SuccessRate != 0.5 || historicalFirst.HealthState != contracts.HealthDegraded {
		t.Fatalf("old evidence was not preserved: %+v", historicalFirst)
	}

	// Window filter returns just that window, with the updated value.
	five, err := st.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{
		ChannelID: "ch-a", InstanceID: "inst-1", Model: "model-a", Window: contracts.Window5m,
	})
	if err != nil {
		t.Fatalf("list 5m: %v", err)
	}
	if len(five) != 1 || five[0].SuccessRate != 1 || !five[0].BucketStart.Equal(bucket.Add(time.Minute)) {
		t.Fatalf("default query should return only the newest scope bucket, got %+v", five)
	}
}

func TestMemoryAllocatedQualitySnapshotAdvancesOwnerFactVersionOnChangeOnly(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	st := NewMemoryStore(base)
	st.now = func() time.Time { return base }
	st.channelAllocations["allocated"] = upstreamChannelAllocation{UserID: 42, SourceID: "source-42"}
	st.channelAllocations["foreign"] = upstreamChannelAllocation{UserID: 77, SourceID: "source-77"}

	input := contracts.ChannelHealthSnapshot{
		ID: "quality-stable", ChannelID: "allocated", InstanceID: "instance-a", Model: "model-a",
		Window: contracts.Window5m, BucketStart: base, CreatedAt: base,
		QualitySampleCount: 10, QualitySuccessRate: .9, QualityScore: 80, HealthState: contracts.HealthDegraded,
	}
	first, err := st.UpsertChannelHealthSnapshot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	version, _ := st.GetUpstreamIntelligenceFactVersion(ctx, 42)
	if version.FactVersion != 1 {
		t.Fatalf("insert version=%d, want 1", version.FactVersion)
	}
	if _, err := st.UpsertChannelHealthSnapshot(ctx, first); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	replayed, _ := st.GetUpstreamIntelligenceFactVersion(ctx, 42)
	if replayed.FactVersion != 1 {
		t.Fatalf("idempotent replay advanced version to %d", replayed.FactVersion)
	}
	changedInput := first
	changedInput.ID = ""
	changedInput.QualityScore = 70
	changedInput.CreatedAt = base.Add(time.Minute)
	changedRevision, err := st.UpsertChannelHealthSnapshot(ctx, changedInput)
	if err != nil {
		t.Fatalf("changed snapshot: %v", err)
	}
	if changedRevision.ID == first.ID {
		t.Fatalf("changed snapshot reused immutable evidence id %q", first.ID)
	}
	changed, _ := st.GetUpstreamIntelligenceFactVersion(ctx, 42)
	if changed.FactVersion != 2 {
		t.Fatalf("changed snapshot version=%d, want 2", changed.FactVersion)
	}
	history, err := st.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{
		ChannelID: "allocated", InstanceID: "instance-a", Model: "model-a",
		Window: contracts.Window5m, BucketStart: base, IncludeHistory: true,
	})
	if err != nil || len(history) != 2 || history[1].ID != first.ID || history[1].QualityScore != 80 {
		t.Fatalf("immutable history=%+v err=%v", history, err)
	}
	foreign, _ := st.GetUpstreamIntelligenceFactVersion(ctx, 77)
	if foreign.FactVersion != 0 {
		t.Fatalf("unrelated owner version changed: %+v", foreign)
	}
	if _, err := st.UpsertChannelHealthSnapshot(ctx, contracts.ChannelHealthSnapshot{
		ChannelID: "unallocated", Window: contracts.Window5m, BucketStart: base, CreatedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	final, _ := st.GetUpstreamIntelligenceFactVersion(ctx, 42)
	if final.FactVersion != 2 {
		t.Fatalf("unallocated snapshot changed owner version to %d", final.FactVersion)
	}
}
