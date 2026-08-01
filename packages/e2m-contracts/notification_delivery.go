package contracts

import "time"

// NotificationDeliveryKind distinguishes normal operational messages from
// explicit operator tests without persisting channel credentials or payloads.
type NotificationDeliveryKind string

const (
	NotificationDeliveryKindEvent NotificationDeliveryKind = "event"
	NotificationDeliveryKindTest  NotificationDeliveryKind = "test"
)

// NotificationDeliveryStatus is the durable outbox lifecycle.
type NotificationDeliveryStatus string

const (
	NotificationDeliveryPending    NotificationDeliveryStatus = "pending"
	NotificationDeliveryProcessing NotificationDeliveryStatus = "processing"
	NotificationDeliveryRetrying   NotificationDeliveryStatus = "retrying"
	NotificationDeliverySucceeded  NotificationDeliveryStatus = "succeeded"
	NotificationDeliveryFailed     NotificationDeliveryStatus = "failed"
)

func (s NotificationDeliveryStatus) Valid() bool {
	switch s {
	case NotificationDeliveryPending, NotificationDeliveryProcessing,
		NotificationDeliveryRetrying, NotificationDeliverySucceeded, NotificationDeliveryFailed:
		return true
	default:
		return false
	}
}

// NotificationDelivery is a route snapshot plus the non-secret event envelope
// needed to retry delivery after Core restarts. Target refs and credentials are
// resolved from the current route/Vault only at send time and are never copied
// into this row.
type NotificationDelivery struct {
	ID               string                     `json:"id"`
	UserID           int64                      `json:"user_id"`
	RouteID          string                     `json:"route_id"`
	RouteName        string                     `json:"route_name"`
	TargetRef        string                     `json:"-"`
	Template         string                     `json:"-"`
	Channel          NotificationChannel        `json:"channel"`
	Kind             NotificationDeliveryKind   `json:"kind"`
	Status           NotificationDeliveryStatus `json:"status"`
	EventLevel       EventLevel                 `json:"event_level"`
	RiskLevel        RiskLevel                  `json:"risk_level"`
	Result           string                     `json:"result,omitempty"`
	InstanceID       string                     `json:"instance_id,omitempty"`
	Title            string                     `json:"title"`
	Text             string                     `json:"text"`
	Fields           map[string]string          `json:"fields,omitempty"`
	Attempts         int                        `json:"attempts"`
	MaxAttempts      int                        `json:"max_attempts"`
	NextAttemptAt    time.Time                  `json:"next_attempt_at,omitempty"`
	LastErrorCode    string                     `json:"last_error_code,omitempty"`
	LastErrorMessage string                     `json:"last_error_message,omitempty"`
	LeaseOwner       string                     `json:"-"`
	LeaseUntil       *time.Time                 `json:"-"`
	LeaseVersion     int64                      `json:"-"`
	CreatedAt        time.Time                  `json:"created_at"`
	UpdatedAt        time.Time                  `json:"updated_at"`
	SentAt           *time.Time                 `json:"sent_at,omitempty"`
	RetriedFromID    string                     `json:"retried_from_id,omitempty"`
}

type NotificationDeliveryFilter struct {
	UserID    int64
	RouteID   string
	TargetRef string
	Channel   NotificationChannel
	Status    NotificationDeliveryStatus
	Limit     int
}

type NotificationChannelState string

const (
	NotificationChannelUnconfigured NotificationChannelState = "unconfigured"
	NotificationChannelUnknown      NotificationChannelState = "unknown"
	NotificationChannelHealthy      NotificationChannelState = "healthy"
	NotificationChannelFailing      NotificationChannelState = "failing"
)

// NotificationChannelStatus contains only operational facts safe for clients.
// It deliberately excludes URLs, secrets, provider response bodies and route
// target refs.
type NotificationChannelStatus struct {
	Channel       NotificationChannel      `json:"channel"`
	Scope         string                   `json:"scope"`
	Configured    bool                     `json:"configured"`
	State         NotificationChannelState `json:"state"`
	LastSuccessAt *time.Time               `json:"last_success_at,omitempty"`
	LastFailureAt *time.Time               `json:"last_failure_at,omitempty"`
	LastErrorCode string                   `json:"last_error_code,omitempty"`
}

// NotificationTargetScope distinguishes the platform-wide sender from a
// credential owned and managed by one client account.
type NotificationTargetScope string

const (
	NotificationTargetScopeSystem   NotificationTargetScope = "system"
	NotificationTargetScopePersonal NotificationTargetScope = "personal"
)

// NotificationTarget is the deliberately redacted view of a client-owned
// Feishu/QQ destination. Endpoint paths, query strings, group IDs and secrets
// never cross the Vault boundary back to the console.
type NotificationTarget struct {
	UserID                  int64                   `json:"user_id"`
	Channel                 NotificationChannel     `json:"channel"`
	Scope                   NotificationTargetScope `json:"scope"`
	TargetRef               string                  `json:"target_ref"`
	Configured              bool                    `json:"configured"`
	EndpointHost            string                  `json:"endpoint_host,omitempty"`
	SigningSecretConfigured bool                    `json:"signing_secret_configured,omitempty"`
	AccessTokenConfigured   bool                    `json:"access_token_configured,omitempty"`
	GroupIDMasked           string                  `json:"group_id_masked,omitempty"`
}

// UpsertNotificationTargetRequest uses pointers so an omitted or empty field
// can preserve an existing secret. Optional secrets are cleared only through
// their explicit clear flag.
type UpsertNotificationTargetRequest struct {
	UserID             int64   `json:"user_id,omitempty"`
	WebhookURL         *string `json:"webhook_url,omitempty"`
	SigningSecret      *string `json:"signing_secret,omitempty"`
	ClearSigningSecret bool    `json:"clear_signing_secret,omitempty"`
	OneBotURL          *string `json:"onebot_url,omitempty"`
	AccessToken        *string `json:"access_token,omitempty"`
	ClearAccessToken   bool    `json:"clear_access_token,omitempty"`
	GroupID            *string `json:"group_id,omitempty"`
}
