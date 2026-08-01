package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

// The memory store must upsert a route strategy by (scope, owner) without
// accumulating duplicates, list by filter, and delete by id -- the query shape
// the Phase 5 orchestrator and console depend on.
func TestMemoryRouteStrategyUpsertByScope(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC))

	// First save creates.
	a, err := st.UpsertRouteStrategy(ctx, contracts.RouteStrategy{
		Scope: contracts.StrategyScopeUser, UserID: 101, Type: contracts.StrategyCostFirst, AutoApply: true,
	})
	if err != nil {
		t.Fatalf("upsert create: %v", err)
	}
	if a.ID == "" || a.CreatedAt.IsZero() {
		t.Fatalf("create should stamp id/timestamps: %+v", a)
	}

	// Re-saving the same (scope, owner) replaces in place, keeping the id.
	b, err := st.UpsertRouteStrategy(ctx, contracts.RouteStrategy{
		Scope: contracts.StrategyScopeUser, UserID: 101, Type: contracts.StrategyStabilityFirst,
	})
	if err != nil {
		t.Fatalf("upsert replace: %v", err)
	}
	if b.ID != a.ID {
		t.Fatalf("replace should reuse id: %s vs %s", b.ID, a.ID)
	}
	all, _ := st.ListRouteStrategies(ctx, contracts.RouteStrategyFilter{UserID: 101})
	if len(all) != 1 {
		t.Fatalf("expected exactly one user strategy, got %d", len(all))
	}
	if all[0].Type != contracts.StrategyStabilityFirst {
		t.Fatalf("replace should update type: %s", all[0].Type)
	}

	// A pool-scoped strategy is isolated by the scope filter.
	if _, err := st.UpsertRouteStrategy(ctx, contracts.RouteStrategy{
		Scope: contracts.StrategyScopePool, PoolID: "pool-1", Type: contracts.StrategyLatencyFirst,
	}); err != nil {
		t.Fatalf("upsert pool: %v", err)
	}
	pools, _ := st.ListRouteStrategies(ctx, contracts.RouteStrategyFilter{Scope: contracts.StrategyScopePool})
	if len(pools) != 1 || pools[0].PoolID != "pool-1" {
		t.Fatalf("pool scope filter: %+v", pools)
	}

	// Delete by id.
	if err := st.DeleteRouteStrategy(ctx, b.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetRouteStrategy(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted strategy should be gone, got %v", err)
	}
}
