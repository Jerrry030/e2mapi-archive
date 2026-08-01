package contracts

import "time"

type DeliveryKeyProofStatus string

const (
	DeliveryKeyProofUnverified DeliveryKeyProofStatus = "unverified"
	DeliveryKeyProofVerified   DeliveryKeyProofStatus = "verified"
	DeliveryKeyProofMismatch   DeliveryKeyProofStatus = "mismatch"
)

func (s DeliveryKeyProofStatus) Valid() bool {
	return s == DeliveryKeyProofUnverified || s == DeliveryKeyProofVerified || s == DeliveryKeyProofMismatch
}

type DeliveryKeyDeploymentStatus string

const (
	DeliveryKeyDeploymentPending  DeliveryKeyDeploymentStatus = "pending"
	DeliveryKeyDeploymentDeployed DeliveryKeyDeploymentStatus = "deployed"
	DeliveryKeyDeploymentFailed   DeliveryKeyDeploymentStatus = "failed"
)

func (s DeliveryKeyDeploymentStatus) Valid() bool {
	return s == DeliveryKeyDeploymentPending || s == DeliveryKeyDeploymentDeployed || s == DeliveryKeyDeploymentFailed
}

// UpstreamKeyDelivery is the platform-private mapping between a managed
// channel and the Core-vault secret used only to deliver that key to its
// permanent owner. SecretRef is never serialized.
type UpstreamKeyDelivery struct {
	ID               string                 `json:"id"`
	ChannelID        string                 `json:"channel_id"`
	SecretRef        string                 `json:"-"`
	MaskedValue      string                 `json:"masked_value"`
	KeyVersion       int64                  `json:"key_version"`
	ProofStatus      DeliveryKeyProofStatus `json:"proof_status"`
	ProofConnectorID string                 `json:"proof_connector_id,omitempty"`
	ProofCheckedAt   *time.Time             `json:"proof_checked_at,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
}

// UpstreamKeyProofReceipt is the proof fact for one delivery key on one
// concrete gateway instance. Proofs on another instance cannot satisfy or
// overwrite this receipt.
type UpstreamKeyProofReceipt struct {
	ChannelID   string                 `json:"channel_id"`
	InstanceID  string                 `json:"instance_id"`
	KeyVersion  int64                  `json:"key_version"`
	ConnectorID string                 `json:"connector_id"`
	Status      DeliveryKeyProofStatus `json:"status"`
	CheckedAt   time.Time              `json:"checked_at"`
}

// AssignedUpstreamKey is the owner-safe view of one permanently allocated,
// platform-managed key. The delivery ID is opaque; channel/source identifiers
// and the vault reference stay private to the control plane.
type AssignedUpstreamKey struct {
	ID               string                 `json:"id"`
	DisplayName      string                 `json:"display_name"`
	Provider         string                 `json:"provider,omitempty"`
	MaskedValue      string                 `json:"masked_value"`
	KeyVersion       int64                  `json:"key_version"`
	ProofStatus      DeliveryKeyProofStatus `json:"proof_status"`
	ProofConnectorID string                 `json:"-"`
	ProofCheckedAt   *time.Time             `json:"proof_checked_at,omitempty"`
	AllocatedAt      time.Time              `json:"allocated_at"`
	ChannelID        string                 `json:"-"`
	SecretRef        string                 `json:"-"`
}

// UpstreamKeyDeployment records the latest acknowledged lifecycle write for a
// delivery-key version on one instance. It is remote write acknowledgement,
// not gateway read-back attestation.
type UpstreamKeyDeployment struct {
	ChannelID   string                      `json:"channel_id"`
	InstanceID  string                      `json:"instance_id"`
	KeyVersion  int64                       `json:"key_version"`
	ConnectorID string                      `json:"connector_id"`
	Status      DeliveryKeyDeploymentStatus `json:"status"`
	DeployedAt  *time.Time                  `json:"deployed_at,omitempty"`
	UpdatedAt   time.Time                   `json:"updated_at"`
}

type RevealAssignedUpstreamKeyRequest struct {
	Password string `json:"password"`
}

type RevealAssignedUpstreamKeyResponse struct {
	Value     string    `json:"value"`
	ExpiresAt time.Time `json:"expires_at"`
}
