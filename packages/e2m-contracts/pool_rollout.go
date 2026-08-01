package contracts

import "time"

// PoolRolloutScope identifies the business object that controls access to a
// managed upstream pool. Instance rules override user rules; when neither is
// present the pool is denied. This makes pool delivery an explicit allowlist
// instead of an implicit side effect of activating a pool.
type PoolRolloutScope string

const (
	PoolRolloutScopeUser     PoolRolloutScope = "user"
	PoolRolloutScopeInstance PoolRolloutScope = "instance"
)

func (s PoolRolloutScope) Valid() bool {
	return s == PoolRolloutScopeUser || s == PoolRolloutScopeInstance
}

func (s PoolRolloutOperationStatus) Terminal() bool {
	return s == PoolRolloutOperationSucceeded || s == PoolRolloutOperationSuperseded
}

// PoolRolloutLegacyCompatible grants only stores that do not implement the
// explicit rollout extension. It lets old embedders retain their historical
// behaviour while current Memory/Postgres stores are deny-by-default.
func PoolRolloutLegacyCompatible(poolID string, userID int64, instanceID string) PoolRolloutResolution {
	return PoolRolloutResolution{
		PoolID: poolID, UserID: userID, InstanceID: instanceID,
		Enabled: true, Rollout: RolloutImmediate,
	}
}

// PoolRolloutTarget is one durable allow/deny rule. A user rule controls all
// of that customer's current and future instances; a narrower instance rule
// can override it. Rollout fields are copied to the automatically managed
// RoutePlan so an admitted customer can start with a canary or batched rollout.
type PoolRolloutTarget struct {
	ID                 string           `json:"id"`
	PoolID             string           `json:"pool_id"`
	Scope              PoolRolloutScope `json:"scope"`
	UserID             int64            `json:"user_id,omitempty"`
	InstanceID         string           `json:"instance_id,omitempty"`
	Enabled            bool             `json:"enabled"`
	Rollout            RolloutMode      `json:"rollout"`
	RolloutBatchSize   int              `json:"rollout_batch_size,omitempty"`
	RolloutCanaryCount int              `json:"rollout_canary_count,omitempty"`
	Note               string           `json:"note,omitempty"`
	CreatedAt          time.Time        `json:"created_at"`
	UpdatedAt          time.Time        `json:"updated_at"`
}

// PoolRolloutResolution is the effective, precedence-resolved access decision
// used by onboarding and exposed to the admin preview. Source is "instance",
// "user", or "default" (the default is always deny).
type PoolRolloutResolution struct {
	PoolID          string           `json:"pool_id"`
	UserID          int64            `json:"user_id"`
	InstanceID      string           `json:"instance_id"`
	Enabled         bool             `json:"enabled"`
	Source          PoolRolloutScope `json:"source,omitempty"`
	TargetID        string           `json:"target_id,omitempty"`
	TargetUpdatedAt *time.Time       `json:"target_updated_at,omitempty"`
	// DesiredUpdatedAt advances when eligibility changes outside the rollout
	// target itself (notably a user deactivation request). Durable operation
	// fingerprints include it so an old successful publish cannot suppress a
	// newly-required drain that happens to reference the same target.
	DesiredUpdatedAt   *time.Time  `json:"desired_updated_at,omitempty"`
	Rollout            RolloutMode `json:"rollout"`
	RolloutBatchSize   int         `json:"rollout_batch_size,omitempty"`
	RolloutCanaryCount int         `json:"rollout_canary_count,omitempty"`
}

// PoolRolloutOperation is the durable, retryable control-plane work produced
// when an access rule changes. A disabled effective rule creates a drain
// operation for every affected live route plan; enabling a rule creates a
// publish operation that applies the chosen rollout policy. The onboarding
// runner (or a dedicated worker) executes these operations through the publish
// engine, so an API success never pretends that gateway withdrawal already
// happened.
type PoolRolloutOperationAction string

const (
	PoolRolloutOperationDrain   PoolRolloutOperationAction = "drain"
	PoolRolloutOperationPublish PoolRolloutOperationAction = "publish"
)

type PoolRolloutOperationStatus string

const (
	PoolRolloutOperationPending    PoolRolloutOperationStatus = "pending"
	PoolRolloutOperationRunning    PoolRolloutOperationStatus = "running"
	PoolRolloutOperationSucceeded  PoolRolloutOperationStatus = "succeeded"
	PoolRolloutOperationFailed     PoolRolloutOperationStatus = "failed"
	PoolRolloutOperationSuperseded PoolRolloutOperationStatus = "superseded"
)

type PoolRolloutOperation struct {
	ID                 string                     `json:"id"`
	PoolID             string                     `json:"pool_id"`
	UserID             int64                      `json:"user_id"`
	InstanceID         string                     `json:"instance_id"`
	PlanID             string                     `json:"plan_id,omitempty"`
	TargetID           string                     `json:"target_id,omitempty"`
	Action             PoolRolloutOperationAction `json:"action"`
	Status             PoolRolloutOperationStatus `json:"status"`
	DesiredFingerprint string                     `json:"desired_fingerprint"`
	Attempts           int                        `json:"attempts"`
	LastError          string                     `json:"last_error,omitempty"`
	Version            int64                      `json:"version"`
	LeaseOwner         string                     `json:"lease_owner,omitempty"`
	LeaseUntil         *time.Time                 `json:"lease_until,omitempty"`
	CreatedAt          time.Time                  `json:"created_at"`
	UpdatedAt          time.Time                  `json:"updated_at"`
}
