package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPostgresUpstreamSourceAndPermanentAllocation(t *testing.T) {
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

	suffix := newID("source-allocation")
	sourceID := "source-" + suffix
	poolID := "pool-" + suffix
	key1, key2 := "key-1-"+suffix, "key-2-"+suffix
	ownerPlan1, ownerPlan2 := "plan-owner-1-"+suffix, "plan-owner-2-"+suffix
	otherPlan := "plan-other-" + suffix
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM published_bindings WHERE plan_id=ANY($1)`, []string{ownerPlan1, ownerPlan2, otherPlan})
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_channel_allocations WHERE channel_id=ANY($1)`, []string{key1, key2})
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM route_plans WHERE id=ANY($1)`, []string{ownerPlan1, ownerPlan2, otherPlan})
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_channels WHERE id=ANY($1)`, []string{key1, key2})
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_pools WHERE id=$1`, poolID)
	})

	if _, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: "source allocation test"}); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	for _, channelID := range []string{key1, key2} {
		created, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
			ID: channelID, PoolID: poolID, SourceID: sourceID, DisplayName: channelID,
		})
		if err != nil {
			t.Fatalf("create channel %s: %v", channelID, err)
		}
		got, err := st.GetUpstreamChannel(ctx, channelID)
		if err != nil || got.SourceID != created.SourceID {
			t.Fatalf("source_id round trip for %s: got=%+v err=%v", channelID, got, err)
		}
	}
	for _, plan := range []contracts.RoutePlan{
		{ID: ownerPlan1, UserID: 101, InstanceID: "inst-owner-1", PoolID: poolID},
		{ID: ownerPlan2, UserID: 101, InstanceID: "inst-owner-2", PoolID: poolID},
		{ID: otherPlan, UserID: 202, InstanceID: "inst-other", PoolID: poolID},
	} {
		if _, err := st.CreateRoutePlan(ctx, plan); err != nil {
			t.Fatalf("create plan %s: %v", plan.ID, err)
		}
	}
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{PlanID: ownerPlan1, ChannelID: key1}); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{PlanID: ownerPlan2, ChannelID: key1}); err != nil {
		t.Fatalf("same user reuse: %v", err)
	}
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{PlanID: ownerPlan2, ChannelID: key2}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("same-source second key error=%v, want ErrDuplicate", err)
	}
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{PlanID: otherPlan, ChannelID: key1}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("cross-user key reuse error=%v, want ErrDuplicate", err)
	}
	var allocations int
	if err := st.pool.QueryRow(ctx, `SELECT COUNT(*) FROM upstream_channel_allocations WHERE channel_id=ANY($1)`, []string{key1, key2}).Scan(&allocations); err != nil {
		t.Fatalf("count allocations: %v", err)
	}
	if allocations != 1 {
		t.Fatalf("rejected claims left partial allocations: %d", allocations)
	}
	allocated, err := st.GetUpstreamChannel(ctx, key1)
	if err != nil {
		t.Fatalf("get allocated channel: %v", err)
	}
	allocated.SourceID = "source-changed"
	if _, err := st.UpdateUpstreamChannel(ctx, allocated); !errors.Is(err, ErrConflict) {
		t.Fatalf("allocated source identity update error=%v, want ErrConflict", err)
	}
}
