package store

import (
	"context"
	"time"

	"e2m.local/contracts"
)

// PoolRolloutStore is an optional persistence extension while rollout is wired
// into the central Store interface. Keeping the assertion explicit also makes
// it possible for existing narrow test decorators to opt in deliberately.
type PoolRolloutStore interface {
	UpsertPoolRolloutTarget(context.Context, contracts.PoolRolloutTarget) (contracts.PoolRolloutTarget, error)
	DeletePoolRolloutTarget(context.Context, string, contracts.PoolRolloutScope, int64, string) error
	ListPoolRolloutTargets(context.Context, string) ([]contracts.PoolRolloutTarget, error)
	ResolvePoolRollout(context.Context, string, int64, string) (contracts.PoolRolloutResolution, error)
	GetPoolRolloutOperation(context.Context, string) (contracts.PoolRolloutOperation, error)
	GuardPoolRolloutPublish(context.Context, string, string, int64, time.Duration) (contracts.PoolRolloutOperation, error)
	EnsurePoolRolloutOperations(context.Context, string) ([]contracts.PoolRolloutOperation, error)
	ClaimPoolRolloutOperation(context.Context, string, time.Duration) (contracts.PoolRolloutOperation, bool, error)
	RenewPoolRolloutOperation(context.Context, string, string, int64, time.Duration) (contracts.PoolRolloutOperation, error)
	CompletePoolRolloutOperation(context.Context, string, string, int64, contracts.PoolRolloutOperationStatus, string) (contracts.PoolRolloutOperation, error)
	ListPoolRolloutOperations(context.Context, string) ([]contracts.PoolRolloutOperation, error)
}

func AsPoolRolloutStore(st Store) (PoolRolloutStore, bool) {
	rollout, ok := st.(PoolRolloutStore)
	return rollout, ok
}
