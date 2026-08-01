package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"e2m.local/contracts"
)

func TestMemoryClaimPlanChannelsIsPermanentAndIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newUpstreamStore()
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: "pool-claim", Name: "claim"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		ID: "source-a-original", PoolID: pool.ID, SourceID: "source-a", Priority: 10,
		AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: "plan-claim", UserID: 101, InstanceID: "inst-claim", PoolID: pool.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := st.ClaimPlanChannels(ctx, plan.ID)
	if err != nil || len(selected) != 1 || selected[0].ID != first.ID {
		t.Fatalf("initial claim=%+v err=%v", selected, err)
	}
	// Better inventory arriving later cannot replace the user's permanent Key.
	if _, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		ID: "source-a-better", PoolID: pool.ID, SourceID: "source-a", Priority: 1,
	}); err != nil {
		t.Fatal(err)
	}
	bindings, _ := st.ListPublishedBindings(ctx, plan.ID)
	if len(bindings) != 1 || bindings[0].State != contracts.BindingPending {
		t.Fatalf("pending binding: %+v", bindings)
	}
	bindings[0].State = contracts.BindingRevoked
	if _, err := st.UpsertPublishedBinding(ctx, bindings[0]); err != nil {
		t.Fatal(err)
	}
	repeated, err := st.ClaimPlanChannels(ctx, plan.ID)
	if err != nil || len(repeated) != 1 || repeated[0].ID != first.ID {
		t.Fatalf("repeat claim=%+v err=%v", repeated, err)
	}
	bindings, _ = st.ListPublishedBindings(ctx, plan.ID)
	if len(bindings) != 1 || bindings[0].State != contracts.BindingRevoked {
		t.Fatalf("idempotent claim rewrote binding: %+v", bindings)
	}

	secondPlan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: "plan-claim-second", UserID: 101, InstanceID: "inst-claim-second", PoolID: pool.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	sameOwner, err := st.ClaimPlanChannels(ctx, secondPlan.ID)
	if err != nil || len(sameOwner) != 1 || sameOwner[0].ID != first.ID {
		t.Fatalf("same owner did not retain key: %+v err=%v", sameOwner, err)
	}
	if got := len(st.channelAllocations); got != 1 {
		t.Fatalf("allocations=%d, want 1", got)
	}
}

func TestMemoryClaimPlanChannelsConcurrentUsersNeverShareKey(t *testing.T) {
	ctx := context.Background()
	st := newUpstreamStore()
	pool, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: "pool-race", Name: "race"})
	_, _ = st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		ID: "only-key", PoolID: pool.ID, SourceID: "source-only",
	})
	plans := []contracts.RoutePlan{
		{ID: "plan-race-a", UserID: 201, InstanceID: "inst-race-a", PoolID: pool.ID},
		{ID: "plan-race-b", UserID: 202, InstanceID: "inst-race-b", PoolID: pool.ID},
	}
	for _, plan := range plans {
		if _, err := st.CreateRoutePlan(ctx, plan); err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan []contracts.UpstreamChannel, 2)
	var wg sync.WaitGroup
	for _, plan := range plans {
		plan := plan
		wg.Add(1)
		go func() {
			defer wg.Done()
			selected, err := st.ClaimPlanChannels(ctx, plan.ID)
			if err != nil {
				t.Errorf("claim %s: %v", plan.ID, err)
			}
			results <- selected
		}()
	}
	wg.Wait()
	close(results)
	selectedCount := 0
	for result := range results {
		selectedCount += len(result)
	}
	if selectedCount != 1 {
		t.Fatalf("total selected keys=%d, want 1", selectedCount)
	}
	if got := len(st.channelAllocations); got != 1 {
		t.Fatalf("allocations=%d, want 1", got)
	}
}

func TestMemoryClaimPlanChannelsFailsClosedForInactivePool(t *testing.T) {
	ctx := context.Background()
	st := newUpstreamStore()
	pool, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{
		ID: "pool-maintenance", Name: "maintenance", Status: contracts.UpstreamPoolActive,
	})
	plan, _ := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: "plan-maintenance", UserID: 301, InstanceID: "inst-maintenance", PoolID: pool.ID,
	})
	pool.Status = contracts.UpstreamPoolMaintenance
	_, _ = st.UpdateUpstreamPool(ctx, pool)
	if _, err := st.ClaimPlanChannels(ctx, plan.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("inactive pool error=%v, want ErrConflict", err)
	}
	if len(st.channelAllocations) != 0 || len(st.publishedBindings) != 0 {
		t.Fatal("inactive pool left allocation or binding")
	}
}
