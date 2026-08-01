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

func TestPostgresChannelHealthScopeAndBucketIdentity(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	channelID := "test-" + newID("health-scope")
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM channel_health_snapshots WHERE channel_id=$1`, channelID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM channel_observations WHERE channel_id=$1`, channelID)
	})
	now := time.Now().UTC().Truncate(time.Minute)
	for _, observation := range []contracts.ChannelObservation{
		{ChannelID: channelID, InstanceID: "inst-a", Model: "model-a", Success: true, ObservedAt: now},
		{ChannelID: channelID, InstanceID: "inst-b", Model: "model-a", Success: false, ObservedAt: now},
		{ChannelID: channelID, InstanceID: "inst-a", Model: "model-b", Success: true, ObservedAt: now},
	} {
		if _, err := st.AppendChannelObservation(ctx, observation); err != nil {
			t.Fatalf("append observation: %v", err)
		}
	}
	observations, err := st.ListChannelObservations(ctx, contracts.ChannelObservationFilter{
		ChannelID: channelID, InstanceID: "inst-a", Model: "model-a", ExactScope: true,
	})
	if err != nil || len(observations) != 1 {
		t.Fatalf("exact observation scope: observations=%+v err=%v", observations, err)
	}

	base := contracts.ChannelHealthSnapshot{
		ChannelID: channelID, InstanceID: "inst-a", Model: "model-a",
		Window: contracts.Window5m, BucketStart: now, CreatedAt: now,
		SampleCount: 100, SuccessRate: 0.75, ErrorRate: 0.25,
		QualitySampleCount: 80, QualitySuccessRate: 0.9, QualityErrorRate: 0.1,
		UpstreamErrorRate: 0.1, UpstreamFailureCount: 2,
		AuthFailureCount: 1, InsufficientBalanceCount: 1,
		HealthState: contracts.HealthDegraded,
	}
	first, err := st.UpsertChannelHealthSnapshot(ctx, base)
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	base.SuccessRate = 0.95
	base.ErrorRate = 0.05
	base.HealthState = contracts.HealthHealthy
	base.CreatedAt = now.Add(time.Microsecond)
	updated, err := st.UpsertChannelHealthSnapshot(ctx, base)
	if err != nil {
		t.Fatalf("same bucket refresh: %v", err)
	}
	if updated.ID == first.ID {
		t.Fatalf("same scope bucket revision reused id: %q", first.ID)
	}
	if replayed, err := st.UpsertChannelHealthSnapshot(ctx, updated); err != nil || replayed.ID != updated.ID {
		t.Fatalf("idempotent snapshot replay=%+v err=%v", replayed, err)
	}
	for _, snapshot := range []contracts.ChannelHealthSnapshot{
		{ChannelID: channelID, InstanceID: "inst-b", Model: "model-a", Window: contracts.Window5m, BucketStart: now, CreatedAt: now, SuccessRate: 0.1},
		{ChannelID: channelID, InstanceID: "inst-a", Model: "model-b", Window: contracts.Window5m, BucketStart: now, CreatedAt: now, SuccessRate: 0.2},
		{ChannelID: channelID, InstanceID: "inst-a", Model: "model-a", Window: contracts.Window5m, BucketStart: now.Add(time.Minute), CreatedAt: now.Add(time.Minute), SuccessRate: 1},
	} {
		if _, err := st.UpsertChannelHealthSnapshot(ctx, snapshot); err != nil {
			t.Fatalf("scoped snapshot: %v", err)
		}
	}

	filter := contracts.ChannelHealthSnapshotFilter{
		ChannelID: channelID, InstanceID: "inst-a", Model: "model-a", Window: contracts.Window5m,
	}
	current, err := st.ListChannelHealthSnapshots(ctx, filter)
	if err != nil || len(current) != 1 || current[0].SuccessRate != 1 {
		t.Fatalf("current snapshot: snapshots=%+v err=%v", current, err)
	}
	filter.IncludeHistory = true
	history, err := st.ListChannelHealthSnapshots(ctx, filter)
	if err != nil || len(history) != 3 {
		t.Fatalf("snapshot history: snapshots=%+v err=%v", history, err)
	}
	if history[1].UpstreamFailureCount != 2 || history[1].AuthFailureCount != 1 || history[1].InsufficientBalanceCount != 1 {
		t.Fatalf("responsibility fields did not round-trip: %+v", history[1])
	}
	if history[1].SampleCount != 100 || history[1].SuccessRate != 0.95 || history[1].ErrorRate != 0.05 ||
		history[1].QualitySampleCount != 80 || history[1].QualitySuccessRate != 0.9 || history[1].QualityErrorRate != 0.1 {
		t.Fatalf("factual/quality metrics did not round-trip independently: %+v", history[1])
	}
	if history[2].ID != first.ID || history[2].SuccessRate != 0.75 || history[2].ErrorRate != 0.25 || history[2].HealthState != contracts.HealthDegraded {
		t.Fatalf("original immutable revision changed: %+v", history[2])
	}
}

func TestPostgresChannelObservationAppendIsIdempotentByID(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	id := "test-" + newID("observation-idempotency")
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM channel_observations WHERE id=$1`, id)
	})
	input := contracts.ChannelObservation{
		ID: id, ChannelID: "ch-a", InstanceID: "inst-a", Model: "model-a",
		Success: true, FirstTokenMS: 100, TotalMS: 200,
		ObservedAt: time.Date(2026, 7, 13, 12, 0, 0, 123456789, time.UTC),
	}
	first, err := st.AppendChannelObservation(ctx, input)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	second, err := st.AppendChannelObservation(ctx, input)
	if err != nil || second != first {
		t.Fatalf("idempotent retry: second=%+v first=%+v err=%v", second, first, err)
	}
	conflict := input
	conflict.ChannelID = "ch-b"
	if _, err := st.AppendChannelObservation(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed content error=%v, want ErrConflict", err)
	}
}

func TestChannelHealthSnapshotScopeLockKeyIsPostgresTextSafeAndUnambiguous(t *testing.T) {
	base := contracts.ChannelHealthSnapshot{
		InstanceID: "a\x00:b", ChannelID: "c:d", Model: "m|n", Capability: "chat",
		EndpointPath: "/v1/chat", Window: contracts.Window5m,
		BucketStart: time.Date(2026, 7, 27, 8, 9, 10, 11, time.UTC),
	}
	key := channelHealthSnapshotScopeLockKey(base)
	if key == "" || strings.ContainsRune(key, '\x00') {
		t.Fatalf("PostgreSQL-unsafe advisory key %q", key)
	}
	changed := base
	changed.InstanceID = "a"
	changed.ChannelID = "b\x00:c:d"
	if other := channelHealthSnapshotScopeLockKey(changed); other == key {
		t.Fatalf("distinct scopes collided: %q", key)
	}
}

func TestPostgresChannelHealthSnapshotImmutableRevisionContractLive(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	suffix := newID("quality-revision-live")
	channelID := "channel-" + suffix
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM channel_health_snapshots WHERE channel_id=$1`, channelID)
	})
	base := time.Now().UTC().Truncate(time.Minute)
	input := contracts.ChannelHealthSnapshot{
		ChannelID: channelID, InstanceID: "instance-" + suffix, Model: "gpt-live",
		Capability: "chat", EndpointPath: "/v1/chat/completions", Window: contracts.Window5m,
		BucketStart: base, CreatedAt: base, SampleCount: 10, SuccessRate: .9,
		QualitySampleCount: 10, QualitySuccessRate: .9, QualityScore: 80,
		HealthState: contracts.HealthDegraded,
	}

	const workers = 8
	results := make([]contracts.ChannelHealthSnapshot, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range results {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			<-start
			results[i], errs[i] = st.UpsertChannelHealthSnapshot(ctx, input)
		}(index)
	}
	close(start)
	group.Wait()
	firstID := results[0].ID
	for index := range results {
		if errs[index] != nil || results[index].ID == "" || results[index].ID != firstID {
			t.Fatalf("concurrent replay[%d]=%+v err=%v first=%q", index, results[index], errs[index], firstID)
		}
	}
	var revisionCount int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM channel_health_snapshots WHERE channel_id=$1`, channelID).Scan(&revisionCount); err != nil || revisionCount != 1 {
		t.Fatalf("concurrent replay rows=%d err=%v", revisionCount, err)
	}

	changed := input
	changed.CreatedAt = base.Add(time.Microsecond)
	changed.QualityScore = 65
	second, err := st.UpsertChannelHealthSnapshot(ctx, changed)
	if err != nil || second.ID == "" || second.ID == firstID {
		t.Fatalf("changed revision=%+v err=%v", second, err)
	}
	conflict := second
	conflict.QualityScore = 1
	if _, err := st.UpsertChannelHealthSnapshot(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed id reuse error=%v, want ErrConflict", err)
	}

	history, err := st.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{
		ChannelID: channelID, InstanceID: input.InstanceID, Model: input.Model,
		Capability: input.Capability, EndpointPath: input.EndpointPath, Window: input.Window,
		BucketStart: base, IncludeHistory: true,
	})
	if err != nil || len(history) != 2 || history[0].ID != second.ID || history[1].ID != firstID || history[1].QualityScore != 80 {
		t.Fatalf("immutable revision history=%+v err=%v", history, err)
	}
	var originalScore float64
	if err := st.pool.QueryRow(ctx, `SELECT quality_score FROM channel_health_snapshots WHERE id=$1`, firstID).Scan(&originalScore); err != nil || originalScore != 80 {
		t.Fatalf("exact historical id score=%v err=%v", originalScore, err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE channel_health_snapshots SET quality_score=0 WHERE id=$1`, firstID); err == nil {
		t.Fatal("database accepted in-place quality evidence mutation")
	}
}
