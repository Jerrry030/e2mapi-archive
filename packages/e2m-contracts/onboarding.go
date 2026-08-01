package contracts

import "time"

// OnboardingStage identifies the durable business step currently being
// executed for one instance/pool pair. It deliberately contains no secret
// material; KeyVersionSummary records only the delivered versions.
type OnboardingStage string

const (
	OnboardingWaitingConnector   OnboardingStage = "waiting_connector"
	OnboardingCheckingGateway    OnboardingStage = "checking_gateway"
	OnboardingAssigningKeys      OnboardingStage = "assigning_keys"
	OnboardingDeliveringBindings OnboardingStage = "delivering_bindings"
	OnboardingPublishing         OnboardingStage = "publishing"
	OnboardingVerifying          OnboardingStage = "verifying"
	OnboardingActive             OnboardingStage = "active"
	OnboardingFailedRetryable    OnboardingStage = "failed_retryable"
	// OnboardingDormant preserves a completed workflow's history while its
	// shared pool is unavailable. Dormant workflows are never claimed.
	OnboardingDormant OnboardingStage = "dormant"
)

// Valid reports whether the stage can be persisted.
func (s OnboardingStage) Valid() bool {
	switch s {
	case OnboardingWaitingConnector, OnboardingCheckingGateway,
		OnboardingAssigningKeys, OnboardingDeliveringBindings,
		OnboardingPublishing, OnboardingVerifying, OnboardingActive,
		OnboardingFailedRetryable, OnboardingDormant:
		return true
	default:
		return false
	}
}

// OnboardingStatus is execution state, kept separate from the business stage
// so an operation can be retried at the step where it failed.
type OnboardingStatus string

const (
	OnboardingPending       OnboardingStatus = "pending"
	OnboardingRunning       OnboardingStatus = "running"
	OnboardingRetryable     OnboardingStatus = "retryable"
	OnboardingReady         OnboardingStatus = "active"
	OnboardingDormantStatus OnboardingStatus = "dormant"
)

// Valid reports whether the status can be persisted.
func (s OnboardingStatus) Valid() bool {
	switch s {
	case OnboardingPending, OnboardingRunning, OnboardingRetryable, OnboardingReady,
		OnboardingDormantStatus:
		return true
	default:
		return false
	}
}

// OnboardingWorkflow is one durable automatic-onboarding run. Identity is
// unique by (InstanceID, PoolID). LeaseOwner, LeaseUntil, and Version fence
// concurrent Core workers. ConnectorID may be empty while the instance waits
// for enrollment.
type OnboardingWorkflow struct {
	ID          string `json:"id"`
	UserID      int64  `json:"user_id"`
	InstanceID  string `json:"instance_id"`
	PoolID      string `json:"pool_id"`
	ConnectorID string `json:"connector_id,omitempty"`

	Stage  OnboardingStage  `json:"stage"`
	Status OnboardingStatus `json:"status"`

	Attempts          int              `json:"attempts"`
	NextAttemptAt     *time.Time       `json:"next_attempt_at,omitempty"`
	LastErrorCode     string           `json:"last_error_code,omitempty"`
	PlanID            string           `json:"plan_id,omitempty"`
	KeyVersionSummary map[string]int64 `json:"key_version_summary,omitempty"`
	// DesiredFingerprint is a secret-free digest of the pool catalog, delivery
	// key versions, and Connector identity that this workflow last reconciled.
	DesiredFingerprint string `json:"desired_fingerprint,omitempty"`
	// DesiredGeneration increases whenever that digest or Connector changes.
	DesiredGeneration int64 `json:"desired_generation"`
	// LastReadyGeneration and LastReadyAt distinguish first activation, a
	// periodic verification, desired-state updates, and same-generation repair.
	LastReadyGeneration int64      `json:"last_ready_generation"`
	LastReadyAt         *time.Time `json:"last_ready_at,omitempty"`

	Version    int64      `json:"version"`
	LeaseOwner string     `json:"lease_owner,omitempty"`
	LeaseUntil *time.Time `json:"lease_until,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// OnboardingWorkflowFilter narrows operations queries. Zero values are
// ignored. Results are ordered by most recently updated first.
type OnboardingWorkflowFilter struct {
	UserID      int64
	InstanceID  string
	PoolID      string
	ConnectorID string
	Stages      []OnboardingStage
	Statuses    []OnboardingStatus
	Limit       int
}

// Matches reports whether a workflow satisfies the filter.
func (f OnboardingWorkflowFilter) Matches(w OnboardingWorkflow) bool {
	if f.UserID != 0 && f.UserID != w.UserID {
		return false
	}
	if f.InstanceID != "" && f.InstanceID != w.InstanceID {
		return false
	}
	if f.PoolID != "" && f.PoolID != w.PoolID {
		return false
	}
	if f.ConnectorID != "" && f.ConnectorID != w.ConnectorID {
		return false
	}
	if len(f.Stages) > 0 {
		matched := false
		for _, stage := range f.Stages {
			if stage == w.Stage {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(f.Statuses) > 0 {
		matched := false
		for _, status := range f.Statuses {
			if status == w.Status {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
