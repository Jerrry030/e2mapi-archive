package inventorymonitor

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

func TestSweepEmitsLowAndRecoveryOnlyOnTransitions(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now())
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "inventory", Status: contracts.UpstreamPoolMaintenance})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetUpstreamPoolSafetyStock(ctx, pool.ID, 1); err != nil {
		t.Fatal(err)
	}
	var events []Event
	runner := New(st, time.Hour, func(_ context.Context, event Event) { events = append(events, event) })
	runner.sweep(ctx)
	runner.sweep(ctx)
	if len(events) != 1 || events[0].State != "low" {
		t.Fatalf("startup low condition should be emitted once: %+v", events)
	}

	_, err = st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, SourceID: "source-a", DisplayName: "key-a",
		AccountOwnership: contracts.GatewayAccountPlatformManaged,
		Status:           contracts.UpstreamChannelActive, InventoryState: contracts.UpstreamInventoryReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.sweep(ctx)
	runner.sweep(ctx)
	if len(events) != 2 || events[1].State != "recovered" || events[1].Available != 1 {
		t.Fatalf("recovery should be emitted once: %+v", events)
	}
}

func TestSweepDoesNotEmitHealthyStartup(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now())
	pool, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "healthy"})
	_ = st.SetUpstreamPoolSafetyStock(ctx, pool.ID, 0)
	var events []Event
	runner := New(st, time.Hour, func(_ context.Context, event Event) { events = append(events, event) })
	runner.sweep(ctx)
	if len(events) != 0 {
		t.Fatalf("healthy startup must remain quiet: %+v", events)
	}
}
