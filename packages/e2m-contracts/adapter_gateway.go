package contracts

// Gateway-agnostic account view and switch intent. Adapters (sub2api / new-api /
// CPA) map their native account models onto GatewayAccount so the orchestrator
// and console stay gateway-neutral.

// GatewayAccount is one upstream account as seen through a gateway's admin API.
type GatewayAccount struct {
	ID       string `json:"id"`
	Platform string `json:"platform,omitempty"`
	Type     string `json:"type,omitempty"`
	// Models is the sanitized model allowlist reported by the gateway. An empty
	// list means the gateway did not expose a restriction; it never contains
	// native credentials, URLs, mappings, or raw response fragments.
	Models      []string `json:"models,omitempty"`
	Status      string   `json:"status,omitempty"`
	Schedulable bool     `json:"schedulable"`
	Priority    int      `json:"priority,omitempty"`
	GroupIDs    []string `json:"group_ids,omitempty"`
	ProxyID     string   `json:"proxy_id,omitempty"`
	DisplayName string   `json:"display_name,omitempty"`
	// ExternalRef is a non-secret stable idempotency marker written by E2M.
	ExternalRef string `json:"external_ref,omitempty"`
	// Balance is the upstream-reported remaining balance/quota for this
	// account or channel, when the gateway exposes one (new-api channel
	// balance). Nil means "not reported"; 0 is a real zero.
	Balance *float64 `json:"balance,omitempty"`
	// UsedQuota is the cumulative consumption counter, when reported.
	UsedQuota *float64 `json:"used_quota,omitempty"`
	// CurrentWeight is the gateway-reported share of traffic assigned to this
	// account. Nil means the gateway did not report a weight; a pointer to zero
	// is an explicit 0% share and must not be treated as unknown or false.
	CurrentWeight *int `json:"current_weight,omitempty"`
}

// AccountSwitch is a single "switch upstream" intent: take one account out of
// scheduling and (optionally) bring a backup account in. EnableAccountID may be
// empty to only disable.
type AccountSwitch struct {
	InstanceID       string `json:"instance_id"`
	DisableAccountID string `json:"disable_account_id"`
	EnableAccountID  string `json:"enable_account_id,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

// GatewayAccountSpec is the gateway-neutral desired shape of one upstream
// account/channel. Core sends only opaque local binding IDs; the Connector
// resolves their values from its own secret store.
type GatewayAccountSpec struct {
	// Ownership is a lifecycle safety boundary, not display metadata.
	// Platform-managed accounts may be created, updated, and deleted by E2M;
	// owner-provided accounts may only be updated in place.
	Ownership GatewayAccountOwnership `json:"ownership"`
	// ChannelID is the managed UpstreamChannel id this spec derives from, used
	// as a stable external name/label on the gateway so re-provisioning is
	// idempotent.
	ChannelID string `json:"channel_id,omitempty"`
	// RemoteID, when set, targets an existing remote account for update/delete.
	RemoteID string `json:"remote_id,omitempty"`
	// DisplayName is the human label shown on the gateway.
	DisplayName string `json:"display_name,omitempty"`
	// Provider/Type describe the upstream vendor (e.g. "anthropic", "oauth").
	Provider string `json:"provider,omitempty"`
	Type     string `json:"type,omitempty"`
	// Models the channel serves (new-api channel models routing).
	Models []string `json:"models,omitempty"`
	// Groups the account belongs to on the gateway (new-api group, sub2api
	// group ids).
	Groups []string `json:"groups,omitempty"`
	// Priority/Weight hint the gateway scheduler; 0 means "unset".
	Priority int `json:"priority,omitempty"`
	Weight   int `json:"weight,omitempty"`
	// Schedulable is the desired initial scheduling participation.
	Schedulable         bool   `json:"schedulable"`
	CredentialBindingID string `json:"credential_binding_id,omitempty"`
	ProxyBindingID      string `json:"proxy_binding_id,omitempty"`
}

// GatewayAccountOwnership defines who owns the remote account lifecycle.
// The zero value is normalized to platform_managed for records created before
// protocol v2, but new API writes should always be explicit.
type GatewayAccountOwnership string

const (
	GatewayAccountPlatformManaged GatewayAccountOwnership = "platform_managed"
	GatewayAccountOwnerProvided   GatewayAccountOwnership = "owner_provided"
)

func (o GatewayAccountOwnership) Normalize() GatewayAccountOwnership {
	if o == "" {
		return GatewayAccountPlatformManaged
	}
	return o
}

func (o GatewayAccountOwnership) Valid() bool {
	switch o.Normalize() {
	case GatewayAccountPlatformManaged, GatewayAccountOwnerProvided:
		return true
	default:
		return false
	}
}

// GatewayProvisionResult reports the outcome of a provision/update call: the
// gateway-native id of the account/channel so the platform can record a binding
// and reconcile it later.
type GatewayProvisionResult struct {
	RemoteID string `json:"remote_id"`
	// Created is true when a new remote account was created (vs. an existing one
	// adopted/updated).
	Created bool `json:"created"`
}

// Executable account capabilities declared by the control plane, not
// self-reported by Connectors.
const (
	CapabilitySetAccountSchedulable  CapabilityName = "set_account_schedulable"
	CapabilitySetAccountTrafficShare CapabilityName = "set_account_traffic_share"
	CapabilitySwitchUpstream         CapabilityName = "switch_upstream"
)
