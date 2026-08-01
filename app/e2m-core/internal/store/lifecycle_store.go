package store

import (
	"context"
	"time"

	"e2m.local/contracts"
)

// UpstreamLifecycleStore is deliberately an optional extension of Store. The
// HTTP lifecycle endpoints require it explicitly, so older store decorators
// used by unrelated tests do not need boilerplate forwarding methods.
type UpstreamLifecycleStore interface {
	Store

	GetUpstreamInventory(ctx context.Context, poolID string) (contracts.UpstreamInventorySnapshot, error)
	ImportUpstreamInventory(ctx context.Context, poolID string, entries []contracts.UpstreamInventoryImportEntry) (contracts.UpstreamInventoryImportResult, error)
	SetUpstreamPoolSafetyStock(ctx context.Context, poolID string, threshold int) error
	SetUpstreamInventoryState(ctx context.Context, channelID string, state contracts.UpstreamInventoryState) (contracts.UpstreamInventoryStateRecord, error)
	MigrateUpstreamChannel(ctx context.Context, channelID, targetPoolID, reason string, actorUserID int64) (contracts.UpstreamChannelMigration, error)

	StartUpstreamKeyRotation(ctx context.Context, channelID, secretRef, maskedValue string) (contracts.UpstreamKeyRotation, error)
	GetUpstreamKeyRotation(ctx context.Context, channelID string) (contracts.UpstreamKeyRotation, error)
	BeginUpstreamKeyRotationRollback(ctx context.Context, channelID string) (contracts.KeyRotationSecrets, error)
	BeginUpstreamKeyRotationFinalize(ctx context.Context, channelID string) (contracts.KeyRotationSecrets, error)
	CompleteUpstreamKeyRotationFinalize(ctx context.Context, channelID string, expectedVersion int64) (contracts.UpstreamKeyRotation, error)
	AbortUpstreamKeyRotationFinalize(ctx context.Context, channelID string, expectedVersion int64) error

	CreatePoolRetirementJob(ctx context.Context, poolID string, createdBy int64) (contracts.PoolRetirementJob, error)
	GetPoolRetirementJob(ctx context.Context, id string) (contracts.PoolRetirementJob, error)
	ListPoolRetirementJobs(ctx context.Context, poolID string) ([]contracts.PoolRetirementJob, error)
	ClaimPoolRetirementItem(ctx context.Context, jobID string) (contracts.PoolRetirementItem, bool, error)
	RenewPoolRetirementItem(ctx context.Context, jobID, planID string, expectedAttempts int, lease time.Duration) (contracts.PoolRetirementItem, error)
	CompletePoolRetirementItem(ctx context.Context, jobID, planID string, expectedAttempts int, errorMessage string) (contracts.PoolRetirementJob, error)
	// FinalizePoolRetirementJob atomically retires the pool and opens the durable
	// final-cleanup phase. A non-empty job is not completed by this transition.
	FinalizePoolRetirementJob(ctx context.Context, jobID string) (contracts.PoolRetirementJob, error)
	ClaimPoolRetirementCleanupItem(ctx context.Context, jobID string) (contracts.PoolRetirementItem, bool, error)
	RenewPoolRetirementCleanupItem(ctx context.Context, jobID, planID string, expectedAttempts int, lease time.Duration) (contracts.PoolRetirementItem, error)
	CompletePoolRetirementCleanupItem(ctx context.Context, jobID, planID string, expectedAttempts int, errorMessage string) (contracts.PoolRetirementJob, error)
}

func AsUpstreamLifecycleStore(st Store) (UpstreamLifecycleStore, bool) {
	lifecycle, ok := st.(UpstreamLifecycleStore)
	return lifecycle, ok
}
