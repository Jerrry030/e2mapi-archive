package contracts

import "time"

type KeyRotationStatus string

const (
	KeyRotationStable      KeyRotationStatus = "stable"
	KeyRotationDeploying   KeyRotationStatus = "deploying"
	KeyRotationRollingBack KeyRotationStatus = "rolling_back"
	KeyRotationFinalizing  KeyRotationStatus = "finalizing"
)

func (s KeyRotationStatus) Valid() bool {
	return s == KeyRotationStable || s == KeyRotationDeploying ||
		s == KeyRotationRollingBack || s == KeyRotationFinalizing
}

// UpstreamKeyRotation is an admin-safe view of the two-version rotation
// ledger. Vault references are deliberately private.
type UpstreamKeyRotation struct {
	ChannelID           string            `json:"channel_id"`
	CurrentKeyVersion   int64             `json:"current_key_version"`
	CurrentMaskedValue  string            `json:"current_masked_value"`
	PreviousKeyVersion  int64             `json:"previous_key_version,omitempty"`
	PreviousMaskedValue string            `json:"previous_masked_value,omitempty"`
	Status              KeyRotationStatus `json:"status"`
	StartedAt           *time.Time        `json:"started_at,omitempty"`
	TargetInstances     int               `json:"target_instances"`
	ConfirmedInstances  int               `json:"confirmed_instances"`
	PendingInstances    []string          `json:"pending_instances,omitempty"`
	CanFinalize         bool              `json:"can_finalize"`
	CanRollback         bool              `json:"can_rollback"`
	UpdatedAt           time.Time         `json:"updated_at"`
}

// KeyRotationSecrets is store-private despite living in contracts. HTTP
// handlers must never serialize it; it only carries references for Vault
// operations after a durable state transition.
type KeyRotationSecrets struct {
	Rotation          UpstreamKeyRotation `json:"-"`
	CurrentSecretRef  string              `json:"-"`
	PreviousSecretRef string              `json:"-"`
}
