package contracts

import "time"

// UpstreamInventoryState is the supply-side admission lifecycle of one
// concrete upstream key. Only ready stock can be newly claimed.
type UpstreamInventoryState string

const (
	UpstreamInventoryDraft       UpstreamInventoryState = "draft"
	UpstreamInventoryTesting     UpstreamInventoryState = "testing"
	UpstreamInventoryReady       UpstreamInventoryState = "ready"
	UpstreamInventoryQuarantined UpstreamInventoryState = "quarantined"
	UpstreamInventoryRetired     UpstreamInventoryState = "retired"
)

func (s UpstreamInventoryState) Valid() bool {
	return s == UpstreamInventoryDraft || s == UpstreamInventoryTesting ||
		s == UpstreamInventoryReady || s == UpstreamInventoryQuarantined ||
		s == UpstreamInventoryRetired
}

// UpstreamInventoryItem is an admin-only operational projection. It contains
// ownership identifiers and aggregate proof/deployment facts, but never secret
// references or plaintext keys.
type UpstreamInventoryItem struct {
	Channel             UpstreamChannel        `json:"channel"`
	InventoryState      UpstreamInventoryState `json:"inventory_state"`
	Allocated           bool                   `json:"allocated"`
	AllocatedUserID     int64                  `json:"allocated_user_id,omitempty"`
	FirstPlanID         string                 `json:"first_plan_id,omitempty"`
	AllocatedAt         *time.Time             `json:"allocated_at,omitempty"`
	Delivery            *UpstreamKeyDelivery   `json:"delivery,omitempty"`
	TargetInstances     int                    `json:"target_instances"`
	ProofVerified       int                    `json:"proof_verified"`
	ProofMismatch       int                    `json:"proof_mismatch"`
	DeploymentsDeployed int                    `json:"deployments_deployed"`
	DeploymentsPending  int                    `json:"deployments_pending"`
	DeploymentsFailed   int                    `json:"deployments_failed"`
}

// UpstreamInventoryImportEntry is the store-level, all-or-nothing bulk import
// input. SecretRef is private and must never be serialized by an API response.
type UpstreamInventoryImportEntry struct {
	Channel     UpstreamChannel `json:"channel"`
	SecretRef   string          `json:"-"`
	MaskedValue string          `json:"masked_value,omitempty"`
}

type UpstreamInventoryImportResult struct {
	Channels []UpstreamInventoryImportedChannel `json:"channels"`
	Imported int                                `json:"imported"`
}

// UpstreamInventoryImportedChannel is a response-safe view that omits local
// credential/proxy binding identifiers.
type UpstreamInventoryImportedChannel struct {
	ID             string                 `json:"id"`
	PoolID         string                 `json:"pool_id"`
	SourceID       string                 `json:"source_id"`
	DisplayName    string                 `json:"display_name"`
	Provider       string                 `json:"provider,omitempty"`
	Status         UpstreamChannelStatus  `json:"status"`
	InventoryState UpstreamInventoryState `json:"inventory_state"`
}

func SafeImportedUpstreamChannel(channel UpstreamChannel) UpstreamInventoryImportedChannel {
	return UpstreamInventoryImportedChannel{
		ID: channel.ID, PoolID: channel.PoolID, SourceID: channel.SourceID,
		DisplayName: channel.DisplayName, Provider: channel.Provider,
		Status: channel.Status, InventoryState: channel.InventoryState,
	}
}

type UpstreamPoolInventorySummary struct {
	PoolID               string `json:"pool_id"`
	Total                int    `json:"total"`
	Ready                int    `json:"ready"`
	Allocated            int    `json:"allocated"`
	Available            int    `json:"available"`
	Draft                int    `json:"draft"`
	Testing              int    `json:"testing"`
	Quarantined          int    `json:"quarantined"`
	Retired              int    `json:"retired"`
	ProofUnverified      int    `json:"proof_unverified"`
	ProofMismatch        int    `json:"proof_mismatch"`
	DeploymentsFailed    int    `json:"deployments_failed"`
	SafetyStockThreshold int    `json:"safety_stock_threshold"`
	BelowSafetyStock     bool   `json:"below_safety_stock"`
}

type UpstreamInventorySnapshot struct {
	Pools  []UpstreamPoolInventorySummary `json:"pools"`
	Items  []UpstreamInventoryItem        `json:"items"`
	Alerts []UpstreamInventoryAlert       `json:"alerts"`
	AsOf   time.Time                      `json:"as_of"`
}

type UpstreamInventoryAlert struct {
	PoolID    string `json:"pool_id"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Available int    `json:"available"`
	Threshold int    `json:"threshold"`
}

type UpstreamChannelMigration struct {
	ChannelID  string    `json:"channel_id"`
	FromPoolID string    `json:"from_pool_id"`
	ToPoolID   string    `json:"to_pool_id"`
	Reason     string    `json:"reason"`
	MigratedAt time.Time `json:"migrated_at"`
}

type PoolRetirementJobStatus string

const (
	PoolRetirementPending    PoolRetirementJobStatus = "pending"
	PoolRetirementRunning    PoolRetirementJobStatus = "running"
	PoolRetirementPartial    PoolRetirementJobStatus = "partial"
	PoolRetirementFinalizing PoolRetirementJobStatus = "finalizing"
	// PoolRetirementCleanup means every plan has been durably drained and the
	// pool is retired, but final-generation deprovision reconciliation is still
	// pending or retrying.
	PoolRetirementCleanup   PoolRetirementJobStatus = "cleanup"
	PoolRetirementCompleted PoolRetirementJobStatus = "completed"
)

type PoolRetirementItemStatus string

const (
	PoolRetirementItemPending   PoolRetirementItemStatus = "pending"
	PoolRetirementItemRunning   PoolRetirementItemStatus = "running"
	PoolRetirementItemCompleted PoolRetirementItemStatus = "completed"
	PoolRetirementItemFailed    PoolRetirementItemStatus = "failed"
)

type PoolRetirementCleanupStatus string

const (
	PoolRetirementCleanupPending   PoolRetirementCleanupStatus = "pending"
	PoolRetirementCleanupRunning   PoolRetirementCleanupStatus = "running"
	PoolRetirementCleanupCompleted PoolRetirementCleanupStatus = "completed"
	PoolRetirementCleanupFailed    PoolRetirementCleanupStatus = "failed"
)

type PoolRetirementJob struct {
	ID                    string                  `json:"id"`
	PoolID                string                  `json:"pool_id"`
	Status                PoolRetirementJobStatus `json:"status"`
	TotalPlans            int                     `json:"total_plans"`
	CompletedPlans        int                     `json:"completed_plans"`
	FailedPlans           int                     `json:"failed_plans"`
	CleanupCompletedPlans int                     `json:"cleanup_completed_plans"`
	CleanupFailedPlans    int                     `json:"cleanup_failed_plans"`
	LastError             string                  `json:"last_error,omitempty"`
	CreatedBy             int64                   `json:"created_by,omitempty"`
	CreatedAt             time.Time               `json:"created_at"`
	UpdatedAt             time.Time               `json:"updated_at"`
	CompletedAt           *time.Time              `json:"completed_at,omitempty"`
	Items                 []PoolRetirementItem    `json:"items,omitempty"`
}

type PoolRetirementItem struct {
	JobID      string                   `json:"job_id"`
	PlanID     string                   `json:"plan_id"`
	InstanceID string                   `json:"instance_id"`
	Status     PoolRetirementItemStatus `json:"status"`
	LastError  string                   `json:"last_error,omitempty"`
	// Attempts is also the drain claim's fencing version. It advances only
	// when the item is (re)claimed; lease renewal deliberately leaves it stable.
	Attempts         int                         `json:"attempts"`
	LeaseUntil       *time.Time                  `json:"lease_until,omitempty"`
	CleanupStatus    PoolRetirementCleanupStatus `json:"cleanup_status"`
	CleanupLastError string                      `json:"cleanup_last_error,omitempty"`
	// CleanupAttempts is the independent final-cleanup claim fencing version.
	CleanupAttempts   int        `json:"cleanup_attempts"`
	CleanupLeaseUntil *time.Time `json:"cleanup_lease_until,omitempty"`
	UpdatedAt         time.Time  `json:"updated_at"`
}
