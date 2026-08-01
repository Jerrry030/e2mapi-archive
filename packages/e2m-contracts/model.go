package contracts

import "time"

type InstanceKind string

const (
	InstanceKindSub2API InstanceKind = "sub2api"
	InstanceKindNewAPI  InstanceKind = "newapi"
	InstanceKindCPA     InstanceKind = "cpa"
)

type InstanceStatus string

const (
	InstanceStatusUnknown     InstanceStatus = "unknown"
	InstanceStatusActive      InstanceStatus = "active"
	InstanceStatusDegraded    InstanceStatus = "degraded"
	InstanceStatusOffline     InstanceStatus = "offline"
	InstanceStatusMaintenance InstanceStatus = "maintenance"
)

type Instance struct {
	ID string `json:"id"`
	// UserID is the owner account. Mutating instance operations require the actor
	// to hold the owner role for this user, unless they are platform admin.
	UserID      int64          `json:"user_id"`
	Name        string         `json:"name"`
	Kind        InstanceKind   `json:"kind"`
	Status      InstanceStatus `json:"status"`
	ConnectorID string         `json:"connector_id,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type CapabilityName string

const (
	CapabilityListAccounts  CapabilityName = "list_accounts"
	CapabilityCreateAccount CapabilityName = "create_account"
	CapabilityUpdateAccount CapabilityName = "update_account"
	CapabilityDeleteAccount CapabilityName = "delete_account"
)

type CapabilityMode string

const (
	CapabilityModeRead  CapabilityMode = "read"
	CapabilityModeWrite CapabilityMode = "write"
)

type RiskLevel string

const (
	RiskLevelL0 RiskLevel = "L0"
	RiskLevelL1 RiskLevel = "L1"
	RiskLevelL2 RiskLevel = "L2"
	RiskLevelL3 RiskLevel = "L3"
)

// EventLevel describes how urgently an operator needs to react to a recorded
// outcome. It is independent from RiskLevel, which grades the operation itself.
type EventLevel string

const (
	EventLevelInfo     EventLevel = "L0"
	EventLevelNotice   EventLevel = "L1"
	EventLevelWarning  EventLevel = "L2"
	EventLevelCritical EventLevel = "L3"
)

func (l EventLevel) Valid() bool {
	switch l {
	case EventLevelInfo, EventLevelNotice, EventLevelWarning, EventLevelCritical:
		return true
	default:
		return false
	}
}

// DefaultEventLevel derives an outcome severity when a writer only supplied
// the operation risk. Successful sensitive operations are notices, not warnings.
func DefaultEventLevel(risk RiskLevel, result string) EventLevel {
	switch result {
	case "running":
		return EventLevelInfo
	case "retrying", "paused", "rejected":
		return EventLevelNotice
	case "failed":
		return EventLevelWarning
	case "accepted", "success":
		if risk == RiskLevelL0 {
			return EventLevelInfo
		}
		return EventLevelNotice
	default:
		if risk == RiskLevelL0 {
			return EventLevelInfo
		}
		return EventLevelNotice
	}
}

// EffectiveEventLevel keeps records created before event_level compatible.
func (a OperationAudit) EffectiveEventLevel() EventLevel {
	if a.EventLevel.Valid() {
		return a.EventLevel
	}
	return DefaultEventLevel(a.RiskLevel, a.Result)
}

// EffectiveMinEventLevel keeps routes created by older clients compatible.
func (r NotificationRoute) EffectiveMinEventLevel() EventLevel {
	if r.MinEventLevel.Valid() {
		return r.MinEventLevel
	}
	return EventLevel(r.MinRiskLevel)
}

type AdapterCapability struct {
	System      InstanceKind   `json:"system"`
	Name        CapabilityName `json:"name"`
	Mode        CapabilityMode `json:"mode"`
	RiskLevel   RiskLevel      `json:"risk_level"`
	Supported   bool           `json:"supported"`
	Description string         `json:"description,omitempty"`
}

var executableGatewayCapabilityDefinitions = []AdapterCapability{
	{Name: CapabilityListAccounts, Mode: CapabilityModeRead, RiskLevel: RiskLevelL0, Supported: true, Description: "list gateway accounts"},
	{Name: CapabilitySetAccountSchedulable, Mode: CapabilityModeWrite, RiskLevel: RiskLevelL1, Supported: true, Description: "change account scheduling state"},
	{Name: CapabilitySwitchUpstream, Mode: CapabilityModeWrite, RiskLevel: RiskLevelL1, Supported: true, Description: "switch scheduled upstream account"},
	{Name: CapabilityCreateAccount, Mode: CapabilityModeWrite, RiskLevel: RiskLevelL2, Supported: true, Description: "create a platform-managed gateway account"},
	{Name: CapabilityUpdateAccount, Mode: CapabilityModeWrite, RiskLevel: RiskLevelL2, Supported: true, Description: "update a gateway account"},
	{Name: CapabilityDeleteAccount, Mode: CapabilityModeWrite, RiskLevel: RiskLevelL2, Supported: true, Description: "delete a platform-managed gateway account after drain"},
}

// ExecutableGatewayCapabilities is the control plane's authoritative matrix of
// gateway operations currently exposed through Connector v2 and Core.
func ExecutableGatewayCapabilities() []AdapterCapability {
	systems := [...]InstanceKind{InstanceKindSub2API, InstanceKindNewAPI, InstanceKindCPA}
	capabilities := make([]AdapterCapability, 0, len(systems)*len(executableGatewayCapabilityDefinitions)+1)
	for _, system := range systems {
		for _, capability := range executableGatewayCapabilityDefinitions {
			capability.System = system
			capabilities = append(capabilities, capability)
		}
		// The initial typed traffic-share implementation targets NewAPI's
		// numeric channel weight. Do not advertise it for gateways whose
		// schedulers currently expose only binary participation.
		if system == InstanceKindNewAPI {
			capabilities = append(capabilities, AdapterCapability{
				System: system, Name: CapabilitySetAccountTrafficShare,
				Mode: CapabilityModeWrite, RiskLevel: RiskLevelL1, Supported: true,
				Description: "set an account's numeric traffic share",
			})
		}
	}
	return capabilities
}

type SignalType string

const (
	SignalTypeHealth SignalType = "health"
	SignalTypeError  SignalType = "error"
	SignalTypeUsage  SignalType = "usage"
)

type SignalSummary struct {
	Type             SignalType `json:"type"`
	HealthStatus     string     `json:"health_status,omitempty"`
	RuntimeStatus    string     `json:"runtime_status,omitempty"`
	RecentErrorCount int        `json:"recent_error_count,omitempty"`
	AccountCount     int        `json:"account_count,omitempty"`
	ModelCount       int        `json:"model_count,omitempty"`
	CollectedAt      time.Time  `json:"collected_at"`
}

type OperationAudit struct {
	ID         string `json:"id"`
	UserID     int64  `json:"user_id"`
	InstanceID string `json:"instance_id,omitempty"`
	ActorType  string `json:"actor_type"`
	ActorID    string `json:"actor_id"`
	Action     string `json:"action"`
	// RiskLevel grades the operation itself for authorization and review.
	RiskLevel RiskLevel `json:"risk_level"`
	// EventLevel grades the outcome for activity feeds and notifications.
	// Empty values are legacy rows and fall back to RiskLevel at read time.
	EventLevel    EventLevel `json:"event_level,omitempty"`
	TargetType    string     `json:"target_type"`
	TargetID      string     `json:"target_id"`
	RequestHash   string     `json:"request_payload_hash,omitempty"`
	Result        string     `json:"result"`
	ErrorMessage  string     `json:"error_message,omitempty"`
	ApprovalID    string     `json:"approval_id,omitempty"`
	WorkflowRunID string     `json:"workflow_run_id,omitempty"`
	// Details contains allowlisted, non-secret business context for rendering.
	Details   map[string]string `json:"details,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

type NotificationChannel string

const (
	NotificationChannelQQ      NotificationChannel = "qq"
	NotificationChannelFeishu  NotificationChannel = "feishu"
	NotificationChannelWebhook NotificationChannel = "webhook"
)

type NotificationRoute struct {
	ID           string              `json:"id"`
	UserID       int64               `json:"user_id"`
	Name         string              `json:"name"`
	Channel      NotificationChannel `json:"channel"`
	TargetRef    string              `json:"target_ref"`
	MinRiskLevel RiskLevel           `json:"min_risk_level"`
	// MinEventLevel is the notification severity threshold. Empty values fall
	// back to MinRiskLevel for clients and rows created before this field.
	MinEventLevel EventLevel `json:"min_event_level,omitempty"`
	Enabled       bool       `json:"enabled"`
	// Template customizes the outgoing message. Placeholders: {title} {text}
	// {riskLevel} {eventLevel} {result} {userId} {instanceId} plus event-specific fields
	// ({instanceName}, {accountName}, {balance}...). Empty = "{title}\n{text}".
	Template        string    `json:"template,omitempty"`
	QuietWindow     string    `json:"quiet_window,omitempty"`
	EscalationAfter string    `json:"escalation_after,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
