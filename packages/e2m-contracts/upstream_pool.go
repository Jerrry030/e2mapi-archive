package contracts

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxUpstreamSourceIdentityBytes bounds the stable, non-secret source identity
// shared by channel allocation and upstream-intelligence link resolution.
const MaxUpstreamSourceIdentityBytes = MaxConnectorIdentifierBytes

// IsUpstreamSourceIdentity accepts only short, opaque metadata. Core persists
// this value, so URLs, credentials, header/cookie material, controls and
// surrounding whitespace are rejected before it reaches a store or audit.
func IsUpstreamSourceIdentity(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > MaxUpstreamSourceIdentityBytes ||
		!utf8.ValidString(value) || LooksLikeConnectorSensitiveValue(value) {
		return false
	}
	for index, character := range value {
		if !(unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' || character == '_') ||
			index == 0 && !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			return false
		}
	}
	parts := strings.FieldsFunc(strings.ToLower(value), func(character rune) bool {
		return character == '-' || character == '_'
	})
	for index, part := range parts {
		switch part {
		case "authorization", "bearer", "token", "cookie", "credential", "credentials", "secret",
			"password", "passwd", "header", "headers", "apikey", "xapikey", "privatekey",
			"clientsecret", "rawresponse", "rawbody", "responsebody", "baseurl", "endpointurl",
			"http", "https", "ftp", "ws", "wss", "www", "url", "endpoint":
			return false
		}
		if index+1 < len(parts) && sensitiveUpstreamSourceIdentityPair(part, parts[index+1]) {
			return false
		}
	}
	return true
}

func sensitiveUpstreamSourceIdentityPair(left, right string) bool {
	switch left + "-" + right {
	case "access-token", "refresh-token", "id-token", "api-key", "x-api", "set-cookie",
		"proxy-authorization", "client-secret", "private-key", "raw-response", "raw-body",
		"response-body", "base-url", "endpoint-url":
		return true
	default:
		return false
	}
}

// This file models the platform-managed upstream layer. E2M maintains a pool of
// upstream channels and publishes a desired routing plan onto an account's
// gateway instance. The health checker keeps that plan healthy by scoring and
// switching backups. Core stores only opaque Connector-local binding IDs here.

// UpstreamPoolStatus tracks whether a pool is available for publishing.
type UpstreamPoolStatus string

const (
	UpstreamPoolActive      UpstreamPoolStatus = "active"
	UpstreamPoolMaintenance UpstreamPoolStatus = "maintenance"
	UpstreamPoolRetired     UpstreamPoolStatus = "retired"
)

// UpstreamDeliveryMode separates legacy Connector-delivery inventory from
// centrally metered Hybrid Supply inventory. Empty legacy values normalize to
// connector so an upgrade never silently moves existing traffic into E2M's
// data plane.
type UpstreamDeliveryMode string

const (
	UpstreamDeliveryConnector     UpstreamDeliveryMode = "connector"
	UpstreamDeliverySupplyGateway UpstreamDeliveryMode = "supply_gateway"
)

func (m UpstreamDeliveryMode) Normalize() UpstreamDeliveryMode {
	if m == "" {
		return UpstreamDeliveryConnector
	}
	return m
}

func (m UpstreamDeliveryMode) Valid() bool {
	m = m.Normalize()
	return m == UpstreamDeliveryConnector || m == UpstreamDeliverySupplyGateway
}

// UpstreamChannelStatus is the platform-side lifecycle of one managed channel.
// It is distinct from the gateway-reported health status: the platform decides
// whether a channel is offered at all, the health checker decides whether a
// published channel is currently schedulable.
type UpstreamChannelStatus string

const (
	UpstreamChannelActive      UpstreamChannelStatus = "active"
	UpstreamChannelMaintenance UpstreamChannelStatus = "maintenance"
	UpstreamChannelRetired     UpstreamChannelStatus = "retired"
)

// UpstreamPool is a platform-curated group of interchangeable upstream channels
// (e.g. "Claude 稳定池", "GPT 成本池"). Owners subscribe to a pool via a plan;
// they never see or edit the individual channels.
type UpstreamPool struct {
	ID string `json:"id"`
	// ResourceClass is set only for the two E2M-managed commercial pools.
	// Owner-provided resources remain Connector-local and are never catalogued
	// as an E2M supply pool.
	ResourceClass ResourceClass        `json:"resource_class,omitempty"`
	DeliveryMode  UpstreamDeliveryMode `json:"delivery_mode,omitempty"`
	Name          string               `json:"name"`
	Provider      string               `json:"provider,omitempty"`
	Models        []string             `json:"models,omitempty"`
	Region        string               `json:"region,omitempty"`
	Status        UpstreamPoolStatus   `json:"status"`
	Description   string               `json:"description,omitempty"`
	Labels        map[string]string    `json:"labels,omitempty"`
	// SafetyStockThreshold is an operational replenishment alert threshold.
	SafetyStockThreshold int       `json:"safety_stock_threshold,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// UpstreamChannel is one concrete upstream resource inside a pool. Binding IDs
// are opaque identifiers resolved by the target Connector's local secret store;
// Core never resolves them to plaintext.
type UpstreamChannel struct {
	ID     string `json:"id"`
	PoolID string `json:"pool_id"`
	// AccountOwnership is immutable after creation. Platform-managed channels
	// permit full remote lifecycle; owner-provided channels are update-only.
	AccountOwnership GatewayAccountOwnership `json:"account_ownership"`
	// SourceID groups credentials supplied by the same stable upstream source.
	// It is deliberately independent from Provider: one model vendor may be
	// reachable through several separately scheduled upstream sources.
	SourceID            string                 `json:"source_id,omitempty"`
	DisplayName         string                 `json:"display_name"`
	Provider            string                 `json:"provider,omitempty"`
	Models              []string               `json:"models,omitempty"`
	ProbeCapability     QualityProbeCapability `json:"probe_capability,omitempty"`
	ProbeEndpointPath   string                 `json:"probe_endpoint_path,omitempty"`
	Groups              []string               `json:"groups,omitempty"`
	CredentialBindingID string                 `json:"credential_binding_id"`
	ProxyBindingID      string                 `json:"proxy_binding_id,omitempty"`
	Priority            int                    `json:"priority,omitempty"`
	Weight              int                    `json:"weight,omitempty"`
	CostHint            float64                `json:"cost_hint,omitempty"`
	Status              UpstreamChannelStatus  `json:"status"`
	// InventoryState controls supply admission independently from scheduling.
	InventoryState UpstreamInventoryState `json:"inventory_state,omitempty"`
	Labels         map[string]string      `json:"labels,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

// IsInventoryReady treats legacy zero-value rows as ready. Product creation
// paths persist draft explicitly; direct legacy fixtures remain compatible.
func (c UpstreamChannel) IsInventoryReady() bool {
	return c.InventoryState == "" || c.InventoryState == UpstreamInventoryReady
}

// SourceIdentity returns the stable identity used to enforce one key per
// upstream source in a downstream plan. Legacy rows without source_id remain
// independent by conservatively falling back to their channel ID.
func (c UpstreamChannel) SourceIdentity() string {
	if sourceID := strings.TrimSpace(c.SourceID); sourceID != "" {
		return sourceID
	}
	return c.ID
}

// RoutePlanStatus tracks a plan through its publish lifecycle.
type RoutePlanStatus string

const (
	RoutePlanDraft     RoutePlanStatus = "draft"
	RoutePlanPublished RoutePlanStatus = "published"
	RoutePlanSuspended RoutePlanStatus = "suspended"
)

// RolloutMode controls how aggressively a reconcile brings desired-active
// channels into scheduling. It lets the platform ship an upstream switch as a
// gradual, observable change instead of an all-at-once flip.
type RolloutMode string

const (
	// RolloutImmediate enables every desired-active channel in one apply (the
	// original behaviour).
	RolloutImmediate RolloutMode = "immediate"
	// RolloutCanary enables only a small first wave (RolloutCanaryCount, default
	// 1) of newly-activated channels per apply; the operator observes, then
	// applies again to widen.
	RolloutCanary RolloutMode = "canary"
	// RolloutBatched enables at most RolloutBatchSize newly-activated channels
	// per apply, so a large pool rolls in over several applies.
	RolloutBatched RolloutMode = "batched"
)

// RoutePlan binds a pool to a specific owner instance: it is the desired-state
// declaration the platform reconciles onto the gateway. One instance may have
// at most one active plan per pool.
type RoutePlan struct {
	ID         string          `json:"id"`
	UserID     int64           `json:"user_id"`     // owner account
	InstanceID string          `json:"instance_id"` // target gateway instance
	PoolID     string          `json:"pool_id"`
	Tier       string          `json:"tier,omitempty"` // "stability" | "cost" | "performance"
	Status     RoutePlanStatus `json:"status"`
	// MaxChannels caps how many channels from the pool are published to this
	// instance (0 = all active channels).
	MaxChannels int `json:"max_channels,omitempty"`
	// Rollout controls how the reconcile brings newly-activated channels into
	// scheduling: immediate (default), canary, or batched. This is the core of
	// the platform-managed gradual upstream switch.
	Rollout RolloutMode `json:"rollout,omitempty"`
	// RolloutBatchSize caps how many newly-enabled channels one apply may bring
	// in when Rollout is "batched" (0 -> default of 1).
	RolloutBatchSize int `json:"rollout_batch_size,omitempty"`
	// RolloutCanaryCount is the first-wave size when Rollout is "canary"
	// (0 -> default of 1).
	RolloutCanaryCount int               `json:"rollout_canary_count,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
	// SchedulingGeneration is the current owner of this plan's gateway and
	// binding mutations. It advances before every real apply, including a
	// no-op barrier, and is never accepted from an API update payload.
	SchedulingGeneration int64     `json:"scheduling_generation,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// PublishedBindingState is the reconcile state of one channel on one instance.
type PublishedBindingState string

const (
	// BindingPending means the platform intends to publish but has not yet
	// confirmed it exists on the gateway.
	BindingPending PublishedBindingState = "pending"
	// BindingActive means the channel is present and scheduling on the gateway.
	BindingActive PublishedBindingState = "active"
	// BindingDisabled means the channel is present but taken out of scheduling
	// (e.g. drained by an operator or auto-switch).
	BindingDisabled PublishedBindingState = "disabled"
	// BindingFailed means the last publish attempt errored.
	BindingFailed PublishedBindingState = "failed"
	// BindingRevoked means the platform withdrew the channel from the instance.
	BindingRevoked PublishedBindingState = "revoked"
	// OwnerMetadataUpdateNotDispatchedMarker is persisted before the original
	// validation error when Core proves an owner metadata update never created a
	// Connector task. Publish and scheduling-fence readers must share this exact
	// value so the durable proof cannot drift between components.
	OwnerMetadataUpdateNotDispatchedMarker = "e2m:owner_metadata_update_not_dispatched"
	// LegacyManagedAccountSchedulingFencePrefix recognizes the exact historical
	// pre-dispatch validation error written before the explicit marker above was
	// introduced. It remains a read-only compatibility contract so old poisoned
	// owner rows can be repaired consistently by every scheduling component.
	LegacyManagedAccountSchedulingFencePrefix = "orchestrator: managed account scheduling requires the current route plan fence: account "
)

// PublishedBindingVerificationStatus records evidence that a gateway-side
// binding can serve a real model request. It is intentionally independent from
// PublishedBindingState: an account may have been created and enabled without
// any successful request having crossed it yet.
type PublishedBindingVerificationStatus string

const (
	BindingVerificationPublishedPending     PublishedBindingVerificationStatus = "published_pending"
	BindingVerificationAwaitingFirstRequest PublishedBindingVerificationStatus = "awaiting_first_request"
	BindingVerificationProbeVerified        PublishedBindingVerificationStatus = "probe_verified"
	BindingVerificationPassiveVerified      PublishedBindingVerificationStatus = "passive_verified"
	BindingVerificationFailed               PublishedBindingVerificationStatus = "verification_failed"
)

// IsCallable is the single business gate for evidence that a published
// binding has served a real model request. Publication acknowledgement alone
// is deliberately insufficient.
func (s PublishedBindingVerificationStatus) IsCallable() bool {
	return s == BindingVerificationProbeVerified || s == BindingVerificationPassiveVerified
}

// PublishedBindingVerificationSource identifies the evidence class without
// exposing request contents or credentials.
type PublishedBindingVerificationSource string

const (
	BindingVerificationSourcePublish PublishedBindingVerificationSource = "publish"
	BindingVerificationSourceProbe   PublishedBindingVerificationSource = "probe"
	BindingVerificationSourcePassive PublishedBindingVerificationSource = "passive"
)

// PublishedBinding records that a pool channel was published to an instance —
// the paper trail linking desired plan to actual gateway account. RemoteID is
// the gateway-native account/channel id once created, so reconcile can update
// or revoke it later.
type PublishedBinding struct {
	ID         string `json:"id"`
	PlanID     string `json:"plan_id"`
	InstanceID string `json:"instance_id"`
	ChannelID  string `json:"channel_id"`
	RemoteID   string `json:"remote_id,omitempty"`
	// AccountOwnership is copied from the channel so an orphaned binding still
	// cannot accidentally delete an owner-provided remote account.
	AccountOwnership      GatewayAccountOwnership            `json:"account_ownership"`
	State                 PublishedBindingState              `json:"state"`
	LastError             string                             `json:"last_error,omitempty"`
	VerificationStatus    PublishedBindingVerificationStatus `json:"verification_status"`
	VerificationSource    PublishedBindingVerificationSource `json:"verification_source,omitempty"`
	VerifiedAt            *time.Time                         `json:"verified_at,omitempty"`
	VerificationErrorCode string                             `json:"verification_error_code,omitempty"`
	// SchedulingGeneration fences the local fact ledger in the same ordering
	// domain as the Connector mutation that produced this state.
	SchedulingGeneration int64     `json:"scheduling_generation,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// RequiresSchedulingFence reports whether a published binding may still own a
// remote scheduling side effect. Generic failures remain fenced because a
// timeout cannot prove the Connector did not apply the write. Only an
// owner-provided metadata failure carrying the durable pre-dispatch marker is
// known not to own the remote account. The exact historical pre-dispatch
// sentinel is retained for old rows; embedded or incomplete text is not proof.
func (b PublishedBinding) RequiresSchedulingFence() bool {
	if b.AccountOwnership.Normalize() != GatewayAccountOwnerProvided || b.State != BindingFailed {
		return true
	}
	lastError := strings.TrimSpace(b.LastError)
	if strings.HasPrefix(lastError, OwnerMetadataUpdateNotDispatchedMarker+":") {
		return false
	}
	legacyPreDispatch := strings.HasPrefix(lastError, LegacyManagedAccountSchedulingFencePrefix) &&
		strings.Contains(lastError[len(LegacyManagedAccountSchedulingFencePrefix):], " belongs to route plan ")
	return !legacyPreDispatch
}

// IsCallable requires both a schedulable gateway state and real callability
// evidence. Customer-facing readiness and capacity projections must use this
// helper instead of treating BindingActive as proof of usability.
func (b PublishedBinding) IsCallable() bool {
	return b.State == BindingActive && b.VerificationStatus.IsCallable()
}

// ReconcileActionType is one step in a publish diff.
type ReconcileActionType string

const (
	ReconcileCreate      ReconcileActionType = "create"      // channel in plan, provisioned onto gateway
	ReconcileEnable      ReconcileActionType = "enable"      // present but not scheduling; bring in
	ReconcileDisable     ReconcileActionType = "disable"     // present & scheduling but plan wants it out
	ReconcileRevoke      ReconcileActionType = "revoke"      // on gateway but no longer in plan (drained, not deleted)
	ReconcileUpdate      ReconcileActionType = "update"      // present; push changed spec (models/groups/priority)
	ReconcileDeprovision ReconcileActionType = "deprovision" // delete the remote account the platform created
	ReconcileHold        ReconcileActionType = "hold"        // desired active but held back by the rollout policy
	ReconcileNoop        ReconcileActionType = "noop"        // already in desired state
)

// ReconcileAction is one planned change for a single channel, produced by the
// diff of desired plan vs actual gateway state. dry-run returns these without
// executing; apply runs them and records the outcome.
type ReconcileAction struct {
	Type      ReconcileActionType `json:"type"`
	ChannelID string              `json:"channel_id"`
	RemoteID  string              `json:"remote_id,omitempty"`
	Detail    string              `json:"detail,omitempty"`
}

// ReconcilePlan is the full set of actions for one instance, plus a summary.
type ReconcilePlan struct {
	InstanceID string            `json:"instance_id"`
	PlanID     string            `json:"plan_id"`
	DryRun     bool              `json:"dry_run"`
	Actions    []ReconcileAction `json:"actions"`
	CreatedAt  time.Time         `json:"created_at"`
}
