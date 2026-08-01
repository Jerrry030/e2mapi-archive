package store

import (
	"context"
	"time"

	"e2m.local/contracts"
)

// HybridRoutingStore is the narrow durable execution surface used by the
// three-pool worker. Keeping it separate makes remote-write orchestration easy
// to test without exposing the rest of Store.
type HybridRoutingStore interface {
	GetInstance(context.Context, string) (contracts.Instance, error)
	GetHybridAllocation(context.Context, string) (contracts.HybridAllocation, error)
	ListHybridGatewayBindings(context.Context, int64, string) ([]contracts.HybridGatewayBinding, error)
	ListHybridRoutingExecutions(context.Context, int64, string, int) ([]contracts.HybridRoutingExecution, error)
	ListPublishedBindings(context.Context, string) ([]contracts.PublishedBinding, error)
	GetVirtualKey(context.Context, int64, string) (contracts.VirtualKey, error)
	GetWallet(context.Context, int64, string) (contracts.Wallet, error)
	ListSupplyCandidates(context.Context, contracts.ResourceClass, string) ([]contracts.SupplyCandidate, error)
	GetSupplyDailyUsage(context.Context, int64, string, string, string) (contracts.SupplyDailyUsage, error)
	ClaimHybridRoutingExecution(context.Context, string, time.Duration) (contracts.HybridRoutingExecution, bool, error)
	RenewHybridRoutingExecution(context.Context, string, string, int64, time.Duration) (contracts.HybridRoutingExecution, error)
	PlanHybridRoutingExecution(context.Context, contracts.HybridRoutingExecutionPlan) (contracts.HybridRoutingExecution, error)
	CompleteHybridRoutingExecution(context.Context, contracts.HybridRoutingExecutionCompletion) (contracts.HybridRoutingExecution, error)
}

func AsHybridRoutingStore(value any) (HybridRoutingStore, bool) {
	st, ok := value.(HybridRoutingStore)
	return st, ok
}
