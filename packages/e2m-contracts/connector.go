package contracts

import (
	"encoding/json"
	"strconv"
	"time"
)

// ConnectorProtocolVersion 3 requires every fenced gateway write to acquire a
// durable Core execution permit after leasing and immediately before the local
// gateway side effect. A v2 Connector must never lease v3 work because it
// would execute a leased mutation without that final generation check.
const ConnectorProtocolVersion = 3

// ConnectorStatus tracks the lifecycle and operator-visible health of an
// outbound connector registered to one user account and one instance.
type ConnectorStatus string

const (
	ConnectorStatusOnline  ConnectorStatus = "online"
	ConnectorStatusOffline ConnectorStatus = "offline"
	ConnectorStatusRevoked ConnectorStatus = "revoked"
)

// Connector is the customer-side control-plane runtime. Connector tokens are
// stored only as hashes in Core and are never serialized on this object.
type Connector struct {
	ID              string                `json:"connector_id"`
	UserID          int64                 `json:"user_id"`
	InstanceID      string                `json:"instance_id"`
	Name            string                `json:"name,omitempty"`
	Status          ConnectorStatus       `json:"status"`
	Version         string                `json:"version,omitempty"`
	ProtocolVersion int                   `json:"protocol_version"`
	Gateway         ConnectorRuntimeState `json:"gateway,omitempty"`
	LastSeenAt      *time.Time            `json:"last_seen_at,omitempty"`
	RevokedAt       *time.Time            `json:"revoked_at,omitempty"`
	CreatedAt       time.Time             `json:"created_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
	TokenHash       string                `json:"-"`
}

type ConnectorFilter struct {
	UserID     int64
	InstanceID string
	Status     ConnectorStatus
}

// ConnectorEnrollment is a single-use installation grant. It is bound to the
// exact instance selected by the user before the token is issued.
type ConnectorEnrollment struct {
	ID          string     `json:"id"`
	UserID      int64      `json:"user_id"`
	InstanceID  string     `json:"instance_id"`
	ConnectorID string     `json:"connector_id"`
	Name        string     `json:"name,omitempty"`
	TokenHash   string     `json:"-"`
	ExpiresAt   time.Time  `json:"expires_at"`
	UsedAt      *time.Time `json:"used_at,omitempty"`
	CreatedBy   string     `json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type CreateConnectorEnrollmentRequest struct {
	UserID           int64  `json:"user_id"`
	InstanceID       string `json:"instance_id"`
	Name             string `json:"name,omitempty"`
	ConnectorID      string `json:"connector_id,omitempty"`
	ExpiresInSeconds int    `json:"expires_in_seconds,omitempty"`
	LocalConfigPort  int    `json:"local_config_port,omitempty"`
}

type CreateConnectorEnrollmentResponse struct {
	Enrollment     ConnectorEnrollment   `json:"enrollment"`
	Token          string                `json:"token"`
	CoreURL        string                `json:"core_url"`
	InstallCommand string                `json:"install_command"`
	InstallGuide   ConnectorInstallGuide `json:"install_guide"`
}

// ConnectorInstallGuide contains bootstrap information only. Gateway address,
// authentication details, and credentials are configured in the local UI.
type ConnectorInstallGuide struct {
	ConnectorID         string   `json:"connector_id"`
	InstanceID          string   `json:"instance_id"`
	CoreURL             string   `json:"core_url"`
	ConnectorImageRef   string   `json:"connector_image_ref"`
	ContainerName       string   `json:"container_name"`
	DataVolumeName      string   `json:"data_volume_name"`
	EnrollmentTokenFile string   `json:"enrollment_token_file"`
	LocalConfigURL      string   `json:"local_config_url"`
	LocalConfigPort     int      `json:"local_config_port"`
	Warnings            []string `json:"warnings,omitempty"`
	DockerComposeYAML   string   `json:"docker_compose_yaml"`
	DockerRunCommand    string   `json:"docker_run_command"`
}

// ConnectorRuntimeState is the non-sensitive status summary sent to Core.
// It deliberately excludes endpoint URLs, auth styles, credentials, and raw
// local errors.
type ConnectorRuntimeState struct {
	ProtocolVersion   int                 `json:"protocol_version"`
	GatewayConfigured bool                `json:"gateway_configured"`
	GatewayKind       string              `json:"gateway_kind,omitempty"`
	GatewayStatus     string              `json:"gateway_status,omitempty"`
	ErrorCode         string              `json:"error_code,omitempty"`
	Capabilities      []ConnectorTaskType `json:"capabilities,omitempty"`
	// BindingEncryptionPublicKey is the Connector's public X25519 key. The
	// matching private key never leaves the Connector's private data directory.
	BindingEncryptionPublicKey string                             `json:"binding_encryption_public_key,omitempty"`
	ObservationCapabilities    *ConnectorObservationCapabilities  `json:"observation_capabilities,omitempty"`
	QualityProbe               *ConnectorQualityProbeCapabilities `json:"quality_probe,omitempty"`
}

type QualityProbeCapability string

const (
	QualityProbeTextStream QualityProbeCapability = "text_stream"
)

const (
	QualityProbeRecoveryManual    = "manual"
	QualityProbeRecoveryAutomatic = "automatic"
)

const (
	QualityProbeEndpointMessages        = "/v1/messages"
	QualityProbeEndpointResponses       = "/v1/responses"
	QualityProbeEndpointChatCompletions = "/v1/chat/completions"
)

// ConnectorQualityProbeCapabilities makes automatic recovery support
// explicit. Manual means the gateway cannot currently provide complete real
// request evidence, or the operator has not enabled synthetic traffic locally.
type ConnectorQualityProbeCapabilities struct {
	RecoveryMode       string                   `json:"recovery_mode"`
	Enabled            bool                     `json:"enabled"`
	Capabilities       []QualityProbeCapability `json:"capabilities,omitempty"`
	EndpointPaths      []string                 `json:"endpoint_paths,omitempty"`
	FirstTokenMS       bool                     `json:"first_token_ms"`
	TotalMS            bool                     `json:"total_ms"`
	MaxRequestsPerHour int                      `json:"max_requests_per_hour,omitempty"`
	MinIntervalSeconds int                      `json:"min_interval_seconds,omitempty"`
}

// ConnectorObservationCapabilities describes facts a Connector can read from
// its locally configured gateway. Each metric is explicit so a missing TTFT or
// duration API is never mistaken for a measured zero. PassiveCollection=false
// means the adapter does not currently produce real-request observations.
type ConnectorObservationCapabilities struct {
	PassiveCollection   bool `json:"passive_collection"`
	SuccessEvents       bool `json:"success_events"`
	FailureEvents       bool `json:"failure_events"`
	ErrorClassification bool `json:"error_classification"`
	FirstTokenMS        bool `json:"first_token_ms"`
	TotalMS             bool `json:"total_ms"`
	// TokenCounts is the legacy quality-telemetry capability. It says nothing
	// about field presence and must not authorize financial attribution.
	TokenCounts bool `json:"token_counts"`
	// CostUsage is present only when the adapter can preserve upstream field
	// presence. Per-field flags let Core distinguish partial support without
	// treating an unsupported metric as a measured zero.
	CostUsage *ConnectorCostUsageCapabilities `json:"cost_usage,omitempty"`
}

type ConnectorCostUsageCapabilities struct {
	InputTokens       bool `json:"input_tokens"`
	OutputTokens      bool `json:"output_tokens"`
	CachedInputTokens bool `json:"cached_input_tokens"`
	RequestCount      bool `json:"request_count"`
	GroupKey          bool `json:"group_key"`
}

type ConnectorEnrollRequest struct {
	EnrollmentToken string `json:"enrollment_token"`
	ConnectorID     string `json:"connector_id,omitempty"`
	InstanceID      string `json:"instance_id,omitempty"`
	Version         string `json:"version,omitempty"`
	ProtocolVersion int    `json:"protocol_version"`
}

type ConnectorEnrollResponse struct {
	ProtocolVersion int       `json:"protocol_version"`
	Connector       Connector `json:"connector"`
	ConnectorToken  string    `json:"connector_token"`
	ServerTime      time.Time `json:"server_time"`
	NextPollAfter   string    `json:"next_poll_after"`
}

type BindInstanceConnectorRequest struct {
	ConnectorID string `json:"connector_id"`
}

type RotateConnectorTokenResponse struct {
	Connector      Connector `json:"connector"`
	ConnectorToken string    `json:"connector_token"`
}

type ConnectorTaskStatus string

const (
	ConnectorTaskPending   ConnectorTaskStatus = "pending"
	ConnectorTaskLeased    ConnectorTaskStatus = "leased"
	ConnectorTaskExecuting ConnectorTaskStatus = "executing"
	ConnectorTaskSucceeded ConnectorTaskStatus = "succeeded"
	ConnectorTaskFailed    ConnectorTaskStatus = "failed"
	ConnectorTaskExpired   ConnectorTaskStatus = "expired"
)

// ConnectorTaskType is a closed list of domain actions. Core cannot provide a
// URL, HTTP method, header, or gateway authentication mode.
type ConnectorTaskType string

const (
	ConnectorTaskGatewayHealth               ConnectorTaskType = "gateway.health.get"
	ConnectorTaskGatewayAccountsList         ConnectorTaskType = "gateway.accounts.list"
	ConnectorTaskGatewayQualityProbe         ConnectorTaskType = "gateway.account.quality.probe"
	ConnectorTaskGatewayBindingProof         ConnectorTaskType = "gateway.binding.proof"
	ConnectorTaskGatewayBindingInstall       ConnectorTaskType = "gateway.binding.install"
	ConnectorTaskGatewaySchedulableSet       ConnectorTaskType = "gateway.account.schedulable.set"
	ConnectorTaskGatewayTrafficShareSet      ConnectorTaskType = "gateway.account.traffic_share.set"
	ConnectorTaskGatewaySwitch               ConnectorTaskType = "gateway.account.switch"
	ConnectorTaskGatewaySchedulingBarrier    ConnectorTaskType = "gateway.scheduling.barrier"
	ConnectorTaskGatewayAccountCreate        ConnectorTaskType = "gateway.account.create"
	ConnectorTaskGatewayAccountUpdate        ConnectorTaskType = "gateway.account.update"
	ConnectorTaskGatewayAccountDelete        ConnectorTaskType = "gateway.account.delete"
	ConnectorTaskUpstreamIntelligenceCollect ConnectorTaskType = "upstream.intelligence.collect"
)

func (t ConnectorTaskType) RiskLevel() RiskLevel {
	switch t {
	case ConnectorTaskGatewayHealth, ConnectorTaskGatewayAccountsList, ConnectorTaskGatewayQualityProbe,
		ConnectorTaskGatewayBindingProof, ConnectorTaskUpstreamIntelligenceCollect:
		return RiskLevelL0
	case ConnectorTaskGatewaySchedulableSet, ConnectorTaskGatewayTrafficShareSet, ConnectorTaskGatewaySwitch,
		ConnectorTaskGatewaySchedulingBarrier:
		return RiskLevelL1
	case ConnectorTaskGatewayBindingInstall, ConnectorTaskGatewayAccountCreate, ConnectorTaskGatewayAccountUpdate,
		ConnectorTaskGatewayAccountDelete:
		return RiskLevelL2
	default:
		return RiskLevelL3
	}
}

type ConnectorGatewayHealthInput struct{}

type ConnectorGatewayAccountsListInput struct{}

type ConnectorUpstreamIntelligenceCollectReason string

const ConnectorUpstreamIntelligenceCollectManualRefresh ConnectorUpstreamIntelligenceCollectReason = "manual_refresh"

// ConnectorUpstreamIntelligenceCollectInput identifies a Core-owned source.
// SourceID is deliberately opaque: Connector-local refs, endpoints and
// credentials are never valid task input.
type ConnectorUpstreamIntelligenceCollectInput struct {
	SchemaVersion int                                        `json:"schema_version"`
	SourceID      string                                     `json:"source_id"`
	Reason        ConnectorUpstreamIntelligenceCollectReason `json:"reason"`
}

// ConnectorUpstreamIntelligenceCollectResult is the sanitized completion
// summary. Normalized observations use the authenticated ingest endpoint and
// raw upstream responses never cross this boundary.
type ConnectorUpstreamIntelligenceCollectResult struct {
	RunID      string                   `json:"run_id"`
	Status     UpstreamCollectionStatus `json:"status"`
	Coverage   UpstreamEvidenceCoverage `json:"coverage"`
	FactCount  int                      `json:"fact_count"`
	ObservedAt time.Time                `json:"observed_at"`
}

const (
	ConnectorGatewayBindingProofDomain             = "e2m-binding-proof-v1"
	ConnectorGatewayBindingProofChallengeHexLength = 64
	ConnectorGatewayBindingProofHexLength          = 64
)

// ConnectorGatewayBindingProofInput asks a Connector to prove that one opaque
// binding resolves to the same key held by Core. The binding value remains on
// the Connector host; only a challenge-bound HMAC crosses the wire.
type ConnectorGatewayBindingProofInput struct {
	ChannelID string `json:"channel_id"`
	BindingID string `json:"binding_id"`
	Challenge string `json:"challenge"`
}

type ConnectorGatewayBindingProofResult struct {
	Proof string `json:"proof"`
}

const (
	ConnectorBindingEncryptionAlgorithm     = "x25519-hkdf-sha256-aes256gcm-v1"
	ConnectorBindingEncryptionKeyDomain     = "e2m-binding-encryption-key-v1"
	ConnectorBindingEncryptionPublicKeySize = 32
	ConnectorBindingEncryptionNonceSize     = 12
	ConnectorBindingEncryptionMaxPlaintext  = 64 << 10
	ConnectorBindingEncryptionMaxCiphertext = ConnectorBindingEncryptionMaxPlaintext + 16
)

// ConnectorGatewayBindingInstallInput is an authenticated encryption envelope.
// Core resolves the delivery key only while creating this envelope. The task
// queue receives ciphertext and non-sensitive binding metadata only.
type ConnectorGatewayBindingInstallInput struct {
	BindingID  string `json:"binding_id"`
	ChannelID  string `json:"channel_id"`
	KeyVersion int64  `json:"key_version"`
	Ciphertext string `json:"ciphertext"`
}

// ConnectorGatewayBindingInstallResult acknowledges only the installed
// identity and version. It can never echo decrypted binding material.
type ConnectorGatewayBindingInstallResult struct {
	BindingID  string `json:"binding_id"`
	ChannelID  string `json:"channel_id"`
	KeyVersion int64  `json:"key_version"`
}

// ConnectorGatewayBindingEncryptionAAD binds ciphertext to its complete task
// identity so a valid envelope cannot be replayed to another Connector,
// instance, binding, channel, or key version.
func ConnectorGatewayBindingEncryptionAAD(connectorID, instanceID, bindingID, channelID string, keyVersion int64) []byte {
	return []byte(ConnectorBindingEncryptionAlgorithm + "\x00" + connectorID + "\x00" + instanceID + "\x00" + bindingID + "\x00" + channelID + "\x00" + strconv.FormatInt(keyVersion, 10))
}

// ConnectorGatewayBindingProofMessage is the protocol-v1, domain-separated
// HMAC message shared by Core and Connector.
func ConnectorGatewayBindingProofMessage(input ConnectorGatewayBindingProofInput) []byte {
	return []byte(ConnectorGatewayBindingProofDomain + "\x00" + input.ChannelID + "\x00" + input.BindingID + "\x00" + input.Challenge)
}

// ConnectorGatewayQualityProbeInput identifies an already-published gateway
// account. Gateway URLs and credentials remain connector-local and can never be
// supplied by a Core task.
type ConnectorGatewayQualityProbeInput struct {
	AccountID    string                 `json:"account_id"`
	ChannelID    string                 `json:"channel_id"`
	Model        string                 `json:"model"`
	Capability   QualityProbeCapability `json:"capability"`
	EndpointPath string                 `json:"endpoint_path"`
}

type ConnectorGatewaySchedulableSetInput struct {
	AccountID   string `json:"account_id"`
	Schedulable bool   `json:"schedulable"`
	// Fence is present only for automatically scheduled mutations. Manual
	// operations omit it and retain protocol-v1 behavior.
	Fence *GatewaySchedulingFence `json:"fence,omitempty"`
}

// ConnectorGatewayTrafficShareSetInput changes one account's numeric traffic
// share. Fence is mandatory because a delayed rollout command must never
// overwrite a newer stage or rollback. Weight is deliberately numeric: 0 is a
// real 0% share, not a boolean scheduling state.
type ConnectorGatewayTrafficShareSetInput struct {
	AccountID string                 `json:"account_id"`
	Weight    int                    `json:"weight"`
	Fence     GatewaySchedulingFence `json:"fence"`
}

type ConnectorGatewaySwitchInput struct {
	DisableAccountID string                  `json:"disable_account_id"`
	EnableAccountID  string                  `json:"enable_account_id,omitempty"`
	Fence            *GatewaySchedulingFence `json:"fence,omitempty"`
}

// ConnectorGatewaySchedulingBarrierInput advances the Connector's durable
// scheduling watermark without mutating the gateway. Fence is required and is
// allocated by Core from the scheduling operation's ordering context.
type ConnectorGatewaySchedulingBarrierInput struct {
	Fence GatewaySchedulingFence `json:"fence"`
}

// ConnectorGatewayAccountCreateInput and UpdateInput carry only desired
// metadata plus opaque Connector-local binding IDs. Secret material and
// gateway admin details never enter Core or the task queue.
type ConnectorGatewayAccountCreateInput struct {
	Spec  GatewayAccountSpec      `json:"spec"`
	Fence *GatewaySchedulingFence `json:"fence,omitempty"`
}

type ConnectorGatewayAccountUpdateInput struct {
	Spec  GatewayAccountSpec      `json:"spec"`
	Fence *GatewaySchedulingFence `json:"fence,omitempty"`
}

type ConnectorGatewayAccountDeleteInput struct {
	AccountID string                  `json:"account_id"`
	Ownership GatewayAccountOwnership `json:"ownership"`
	Fence     *GatewaySchedulingFence `json:"fence,omitempty"`
}

type ConnectorGatewayHealthResult struct {
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checked_at"`
}

type ConnectorGatewayAccountsListResult struct {
	Accounts []GatewayAccount `json:"accounts"`
}

// ConnectorGatewayQualityProbeResult is the measured outcome of one real
// gateway request. A failed upstream response is still a successfully executed
// task and is represented by Success=false rather than a ConnectorTaskError.
// A successful recovery sample must include both first-token and total latency;
// availability-only checks are diagnostics, not evidence that can restore
// normal scheduling.
type ConnectorGatewayQualityProbeResult struct {
	Success      bool                   `json:"success"`
	Status       int                    `json:"status,omitempty"`
	ErrorType    ObservationErrorType   `json:"error_type,omitempty"`
	FirstTokenMS float64                `json:"first_token_ms,omitempty"`
	TotalMS      float64                `json:"total_ms,omitempty"`
	Capability   QualityProbeCapability `json:"capability"`
	EndpointPath string                 `json:"endpoint_path"`
	ObservedAt   time.Time              `json:"observed_at"`
}

type ConnectorGatewayMutationResult struct {
	RemoteID string `json:"remote_id,omitempty"`
	Created  bool   `json:"created,omitempty"`
}

// ConnectorGatewayTrafficShareSetResult is the non-sensitive mutation
// receipt. Echoing the applied identity, numeric value, and fence lets Core
// reject a stale or mismatched acknowledgement without receiving gateway
// connection details or credentials.
type ConnectorGatewayTrafficShareSetResult struct {
	AccountID string                 `json:"account_id"`
	Weight    int                    `json:"weight"`
	Fence     GatewaySchedulingFence `json:"fence"`
}

// ConnectorTaskError is intentionally limited to an allowlisted code and a
// retryability bit. Human-readable connector or gateway errors never cross the
// wire boundary into Core.
type ConnectorTaskError struct {
	Code      string `json:"code"`
	Retryable bool   `json:"retryable,omitempty"`
}

type ConnectorTask struct {
	ID          string `json:"id"`
	UserID      int64  `json:"user_id"`
	InstanceID  string `json:"instance_id"`
	ConnectorID string `json:"connector_id"`
	// PlanID and SchedulingGeneration bind a queued gateway mutation to the
	// exact route-plan intent that authorized it. They are omitted for read-only
	// tasks and legacy/manual writes that carry no automatic scheduling fence.
	PlanID               string `json:"plan_id,omitempty"`
	SchedulingGeneration int64  `json:"scheduling_generation,omitempty"`
	// ExecutionScope, ExecutionID and ExecutionGeneration bind a fenced task
	// to a non-RoutePlan Core execution. They are all empty for RoutePlan and
	// unfenced tasks and are immutable after creation.
	ExecutionScope      string              `json:"execution_scope,omitempty"`
	ExecutionID         string              `json:"execution_id,omitempty"`
	ExecutionGeneration int64               `json:"execution_generation,omitempty"`
	Type                ConnectorTaskType   `json:"type"`
	SchemaVersion       int                 `json:"schema_version"`
	RiskLevel           RiskLevel           `json:"risk_level"`
	Status              ConnectorTaskStatus `json:"status"`
	Input               json.RawMessage     `json:"input,omitempty"`
	Result              json.RawMessage     `json:"result,omitempty"`
	Error               ConnectorTaskError  `json:"error,omitempty"`
	IdempotencyKey      string              `json:"idempotency_key"`
	LeaseOwner          string              `json:"lease_owner,omitempty"`
	LeaseNonce          string              `json:"lease_nonce,omitempty"`
	LeaseUntil          *time.Time          `json:"lease_until,omitempty"`
	Attempts            int                 `json:"attempts"`
	MaxAttempts         int                 `json:"max_attempts"`
	// AvailableAt makes deferred lifecycle work durable. A Connector cannot
	// lease the task before this store-controlled timestamp.
	AvailableAt time.Time `json:"available_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ConnectorTaskFilter struct {
	UserID      int64
	InstanceID  string
	ConnectorID string
	Status      ConnectorTaskStatus
	Types       []ConnectorTaskType
	Limit       int
}

// ConnectorTaskSummary is the user-facing task history shape. Lifecycle target
// IDs are projected for correlation, while lease ownership and task payloads
// remain available only on the Connector wire API.
type ConnectorTaskSummary struct {
	ID              string              `json:"id"`
	InstanceID      string              `json:"instance_id"`
	ConnectorID     string              `json:"connector_id"`
	Type            ConnectorTaskType   `json:"type"`
	SchemaVersion   int                 `json:"schema_version"`
	RiskLevel       RiskLevel           `json:"risk_level"`
	Status          ConnectorTaskStatus `json:"status"`
	Error           ConnectorTaskError  `json:"error,omitempty"`
	Attempts        int                 `json:"attempts"`
	MaxAttempts     int                 `json:"max_attempts"`
	TargetChannelID string              `json:"target_channel_id,omitempty"`
	TargetAccountID string              `json:"target_account_id,omitempty"`
	// TargetTrafficShare is presence-safe so an applied 0% share remains
	// distinguishable from tasks that do not change numeric traffic share.
	TargetTrafficShare *int `json:"target_traffic_share,omitempty"`
	// SchedulingFence contains only non-secret ordering metadata. Raw task
	// input remains private, while operators can prove a delayed lifecycle task
	// belongs to the route plan's current scheduling generation.
	SchedulingFence *GatewaySchedulingFence `json:"scheduling_fence,omitempty"`
	AvailableAt     time.Time               `json:"available_at"`
	ExpiresAt       time.Time               `json:"expires_at"`
	CreatedAt       time.Time               `json:"created_at"`
	UpdatedAt       time.Time               `json:"updated_at"`
}

func SummarizeConnectorTask(task ConnectorTask) ConnectorTaskSummary {
	summary := ConnectorTaskSummary{
		ID:            task.ID,
		InstanceID:    task.InstanceID,
		ConnectorID:   task.ConnectorID,
		Type:          task.Type,
		SchemaVersion: task.SchemaVersion,
		RiskLevel:     task.RiskLevel,
		Status:        task.Status,
		Error:         task.Error,
		Attempts:      task.Attempts,
		MaxAttempts:   task.MaxAttempts,
		AvailableAt:   task.AvailableAt,
		ExpiresAt:     task.ExpiresAt,
		CreatedAt:     task.CreatedAt,
		UpdatedAt:     task.UpdatedAt,
	}
	switch task.Type {
	case ConnectorTaskGatewayAccountCreate:
		var input ConnectorGatewayAccountCreateInput
		if json.Unmarshal(task.Input, &input) == nil {
			summary.TargetChannelID = validConnectorTaskSummaryTargetID(input.Spec.ChannelID)
		}
	case ConnectorTaskGatewayAccountUpdate:
		var input ConnectorGatewayAccountUpdateInput
		if json.Unmarshal(task.Input, &input) == nil {
			summary.TargetChannelID = validConnectorTaskSummaryTargetID(input.Spec.ChannelID)
			summary.TargetAccountID = validConnectorTaskSummaryTargetID(input.Spec.RemoteID)
		}
	case ConnectorTaskGatewayAccountDelete:
		var input ConnectorGatewayAccountDeleteInput
		if json.Unmarshal(task.Input, &input) == nil {
			summary.TargetAccountID = validConnectorTaskSummaryTargetID(input.AccountID)
			summary.SchedulingFence = validConnectorTaskSummaryFence(input.Fence)
		}
	case ConnectorTaskGatewayTrafficShareSet:
		var input ConnectorGatewayTrafficShareSetInput
		if json.Unmarshal(task.Input, &input) == nil && ValidateConnectorGatewayTrafficShareSetInput(input) {
			summary.TargetAccountID = input.AccountID
			weight := input.Weight
			summary.TargetTrafficShare = &weight
			summary.SchedulingFence = validConnectorTaskSummaryFence(&input.Fence)
		}
	}
	return summary
}

func validConnectorTaskSummaryFence(input *GatewaySchedulingFence) *GatewaySchedulingFence {
	if input == nil || !IsGatewaySchedulingFence(*input) {
		return nil
	}
	fence := *input
	return &fence
}

func validConnectorTaskSummaryTargetID(value string) string {
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '_' || r == '-' || r == '@' || r == '+' {
			continue
		}
		return ""
	}
	return value
}

type ConnectorTaskLeaseRequest struct {
	ConnectorID     string                `json:"connector_id"`
	MaxTasks        int                   `json:"max_tasks,omitempty"`
	LeaseSeconds    int                   `json:"lease_seconds,omitempty"`
	Version         string                `json:"version,omitempty"`
	ProtocolVersion int                   `json:"protocol_version"`
	RuntimeState    ConnectorRuntimeState `json:"runtime_state,omitempty"`
}

type ConnectorTaskLeaseResponse struct {
	ProtocolVersion int             `json:"protocol_version"`
	Tasks           []ConnectorTask `json:"tasks"`
	ServerTime      time.Time       `json:"server_time"`
	NextPollAfter   string          `json:"next_poll_after"`
}

// ConnectorTaskExecutionRequest exchanges the exact live lease for a durable
// execution permit. Core serializes this transition with route-plan generation
// changes; once granted, the plan remains frozen until this execution reaches
// an explicit terminal or retry state.
type ConnectorTaskExecutionRequest struct {
	ConnectorID string `json:"connector_id"`
	LeaseNonce  string `json:"lease_nonce"`
}

type ConnectorTaskExecutionResolution string

const (
	ConnectorTaskExecutionConfirmedApplied       ConnectorTaskExecutionResolution = "confirmed_applied"
	ConnectorTaskExecutionConfirmedNotApplied    ConnectorTaskExecutionResolution = "confirmed_not_applied"
	ConnectorTaskExecutionRevokedUnverifiable    ConnectorTaskExecutionResolution = "connector_revoked_unverifiable"
	ConnectorTaskExecutionResolutionMaxNoteRunes                                  = 1000
)

func (resolution ConnectorTaskExecutionResolution) Valid() bool {
	switch resolution {
	case ConnectorTaskExecutionConfirmedApplied,
		ConnectorTaskExecutionConfirmedNotApplied,
		ConnectorTaskExecutionRevokedUnverifiable:
		return true
	default:
		return false
	}
}

// ConnectorTaskExecutionResolveRequest is an operator-only recovery contract
// for an execution whose remote outcome cannot be inferred from timeout. The
// exact execution nonce prevents resolving a newer attempt by mistake.
type ConnectorTaskExecutionResolveRequest struct {
	LeaseNonce   string                           `json:"lease_nonce"`
	Resolution   ConnectorTaskExecutionResolution `json:"resolution"`
	EvidenceNote string                           `json:"evidence_note"`
	Result       json.RawMessage                  `json:"result,omitempty"`
}

type ConnectorTaskCompleteRequest struct {
	ConnectorID string             `json:"connector_id"`
	LeaseNonce  string             `json:"lease_nonce"`
	Success     bool               `json:"success"`
	Result      json.RawMessage    `json:"result,omitempty"`
	Error       ConnectorTaskError `json:"error,omitempty"`
}
