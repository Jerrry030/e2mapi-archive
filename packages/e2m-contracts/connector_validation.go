package contracts

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode"
)

const (
	MaxConnectorAccounts         = 5000
	MaxConnectorAccountGroups    = 128
	MaxConnectorIdentifierBytes  = 128
	MaxConnectorShortIDBytes     = 64
	MaxConnectorDisplayNameRunes = 256
	MaxConnectorVersionBytes     = 64
)

var (
	connectorSemverPattern              = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-((?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
	connectorIdentifierPattern          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]*$`)
	connectorSensitiveAssignmentPattern = regexp.MustCompile(`(?i)(^|[^[:alnum:]])["']*(authorization|proxy[-_]?authorization|token|access[-_]?token|refresh[-_]?token|id[-_]?token|api[-_]?key|x[-_]?api[-_]?key|cookie|set[-_]?cookie|credential(s)?|secret|password|passwd|client[-_]?secret|private[-_]?key|header(s)?|raw[-_]?response|raw[-_]?body|response[-_]?body|base[-_]?url|endpoint[-_]?url)["']*[:=]`)
)

func IsConnectorVersion(value string) bool {
	return value != "" && len(value) <= MaxConnectorVersionBytes && connectorSemverPattern.MatchString(value)
}

func IsConnectorReportedErrorCode(value string) bool {
	switch value {
	case "connector_task_failed",
		"idempotency_conflict",
		"scheduling_fence_stale",
		"scheduling_fence_conflict",
		"schema_version_unsupported",
		"connector_mismatch",
		"instance_mismatch",
		"invalid_task_input",
		"task_type_unsupported",
		"result_encoding_failed",
		"gateway_not_configured",
		"gateway_config_unavailable",
		"gateway_test_failed",
		"gateway_kind_unsupported",
		"gateway_auth_unsupported",
		"invalid_gateway_request",
		"gateway_request_failed",
		"gateway_unreachable",
		"gateway_timeout",
		"gateway_response_invalid",
		"gateway_response_too_large",
		"gateway_redirect_rejected",
		"gateway_auth_failed",
		"gateway_resource_not_found",
		"gateway_unavailable",
		"gateway_rejected",
		"invalid_account_id",
		"binding_not_found",
		"binding_version_stale",
		"ownership_violation",
		"quality_probe_disabled",
		"quality_probe_rate_limited",
		"quality_probe_scope_unsupported",
		"source_not_found",
		"source_paused",
		"auth_failed",
		"rate_limited",
		"schema_unsupported",
		"response_too_large",
		"upstream_unavailable",
		"local_store_failed":
		return true
	default:
		return false
	}
}

func IsConnectorTaskErrorCode(value string) bool {
	return IsConnectorReportedErrorCode(value) || value == "expired" || value == "max_attempts_exceeded" ||
		value == "execution_abandoned" || value == "execution_outcome_unknown"
}

func SanitizeConnectorRuntimeState(input ConnectorRuntimeState) ConnectorRuntimeState {
	out := ConnectorRuntimeState{
		ProtocolVersion:   ConnectorProtocolVersion,
		GatewayConfigured: input.GatewayConfigured,
	}
	switch value := strings.ToLower(strings.TrimSpace(input.GatewayKind)); value {
	case string(InstanceKindSub2API), string(InstanceKindNewAPI), string(InstanceKindCPA):
		out.GatewayKind = value
	}
	switch value := strings.ToLower(strings.TrimSpace(input.GatewayStatus)); value {
	case "missing", "configured", "ok", "error":
		out.GatewayStatus = value
	}
	if code := strings.ToLower(strings.TrimSpace(input.ErrorCode)); IsConnectorReportedErrorCode(code) {
		out.ErrorCode = code
	}
	if IsConnectorBindingEncryptionPublicKey(input.BindingEncryptionPublicKey) {
		out.BindingEncryptionPublicKey = input.BindingEncryptionPublicKey
	}

	seen := make(map[ConnectorTaskType]struct{}, 4)
	for _, capability := range input.Capabilities {
		capability = ConnectorTaskType(strings.TrimSpace(string(capability)))
		if !IsConnectorCapability(capability) {
			continue
		}
		if capability == ConnectorTaskGatewayTrafficShareSet &&
			(!out.GatewayConfigured || out.GatewayKind != string(InstanceKindNewAPI)) {
			continue
		}
		if _, exists := seen[capability]; exists {
			continue
		}
		seen[capability] = struct{}{}
		out.Capabilities = append(out.Capabilities, capability)
	}
	if input.ObservationCapabilities != nil {
		capabilities := *input.ObservationCapabilities
		if capabilities.CostUsage != nil {
			costUsage := *capabilities.CostUsage
			capabilities.CostUsage = &costUsage
		}
		// Metric flags cannot be true when no passive event stream exists.
		if !capabilities.PassiveCollection {
			capabilities.SuccessEvents = false
			capabilities.FailureEvents = false
			capabilities.ErrorClassification = false
			capabilities.FirstTokenMS = false
			capabilities.TotalMS = false
			capabilities.TokenCounts = false
			capabilities.CostUsage = nil
		}
		if !capabilities.FailureEvents {
			capabilities.ErrorClassification = false
		}
		out.ObservationCapabilities = &capabilities
	}
	if input.QualityProbe != nil {
		probe := ConnectorQualityProbeCapabilities{RecoveryMode: QualityProbeRecoveryManual}
		if input.QualityProbe.Enabled && input.QualityProbe.RecoveryMode == QualityProbeRecoveryAutomatic {
			probe.RecoveryMode = QualityProbeRecoveryAutomatic
			probe.Enabled = true
			probe.FirstTokenMS = input.QualityProbe.FirstTokenMS
			probe.TotalMS = input.QualityProbe.TotalMS
			if input.QualityProbe.MaxRequestsPerHour > 0 && input.QualityProbe.MaxRequestsPerHour <= 360 {
				probe.MaxRequestsPerHour = input.QualityProbe.MaxRequestsPerHour
			}
			if input.QualityProbe.MinIntervalSeconds >= 10 && input.QualityProbe.MinIntervalSeconds <= 3600 {
				probe.MinIntervalSeconds = input.QualityProbe.MinIntervalSeconds
			}
		}
		for _, capability := range input.QualityProbe.Capabilities {
			if IsQualityProbeCapability(capability) && !containsProbeCapability(probe.Capabilities, capability) {
				probe.Capabilities = append(probe.Capabilities, capability)
			}
		}
		for _, endpoint := range input.QualityProbe.EndpointPaths {
			if IsQualityProbeEndpointPath(endpoint) && !containsString(probe.EndpointPaths, endpoint) {
				probe.EndpointPaths = append(probe.EndpointPaths, endpoint)
			}
		}
		out.QualityProbe = &probe
	}
	return out
}

func IsQualityProbeCapability(value QualityProbeCapability) bool {
	return value == QualityProbeTextStream
}

func IsQualityProbeEndpointPath(value string) bool {
	switch strings.TrimSpace(value) {
	case QualityProbeEndpointMessages, QualityProbeEndpointResponses, QualityProbeEndpointChatCompletions:
		return true
	default:
		return false
	}
}

func containsProbeCapability(values []QualityProbeCapability, value QualityProbeCapability) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func IsConnectorCapability(value ConnectorTaskType) bool {
	switch value {
	case ConnectorTaskGatewayHealth,
		ConnectorTaskGatewayAccountsList,
		ConnectorTaskGatewayQualityProbe,
		ConnectorTaskGatewayBindingProof,
		ConnectorTaskGatewayBindingInstall,
		ConnectorTaskGatewaySchedulableSet,
		ConnectorTaskGatewayTrafficShareSet,
		ConnectorTaskGatewaySwitch,
		ConnectorTaskGatewaySchedulingBarrier,
		ConnectorTaskGatewayAccountCreate,
		ConnectorTaskGatewayAccountUpdate,
		ConnectorTaskGatewayAccountDelete,
		ConnectorTaskUpstreamIntelligenceCollect:
		return true
	default:
		return false
	}
}

// IsGatewaySchedulingFence accepts only complete, bounded ordering metadata.
// Scope is intentionally a restricted identifier path so URLs, credentials,
// control characters, and arbitrary operator text cannot enter receipts or
// user-visible task summaries.
func IsGatewaySchedulingFence(value GatewaySchedulingFence) bool {
	if value.Version <= 0 || value.Sequence <= 0 || len(value.Scope) == 0 || len(value.Scope) > 256 ||
		LooksLikeConnectorSensitiveValue(value.Scope) {
		return false
	}
	for _, r := range value.Scope {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '_' || r == '-' || r == '/' {
			continue
		}
		return false
	}
	return true
}

// ValidateConnectorGatewayTrafficShareSetInput validates the complete closed
// task payload. Numeric zero is accepted as an explicit share; missing or
// malformed generation fencing fails closed.
func ValidateConnectorGatewayTrafficShareSetInput(input ConnectorGatewayTrafficShareSetInput) bool {
	accountID, ok := normalizeConnectorIdentifier(input.AccountID, MaxConnectorIdentifierBytes)
	return ok && accountID == input.AccountID && input.Weight >= 0 && input.Weight <= 100 &&
		IsGatewaySchedulingFence(input.Fence)
}

// ValidateConnectorGatewayTrafficShareSetResult validates a receipt against
// the exact requested account, numeric share, and generation fence. A valid
// but different receipt is not an acknowledgement of this task.
func ValidateConnectorGatewayTrafficShareSetResult(
	input ConnectorGatewayTrafficShareSetInput,
	result ConnectorGatewayTrafficShareSetResult,
) bool {
	return ValidateConnectorGatewayTrafficShareSetInput(input) &&
		result.AccountID == input.AccountID && result.Weight == input.Weight && result.Fence == input.Fence &&
		ValidateConnectorGatewayTrafficShareSetInput(ConnectorGatewayTrafficShareSetInput{
			AccountID: result.AccountID, Weight: result.Weight, Fence: result.Fence,
		})
}

// ConnectorTaskSchedulingFence extracts the ordering tuple from every typed
// gateway mutation that can be generated by an automatic route-plan owner.
// The bool is false for read-only tasks and for explicitly manual mutations
// that omit their optional fence.
func ConnectorTaskSchedulingFence(task ConnectorTask) (GatewaySchedulingFence, bool) {
	switch task.Type {
	case ConnectorTaskGatewayTrafficShareSet:
		var input ConnectorGatewayTrafficShareSetInput
		if json.Unmarshal(task.Input, &input) == nil {
			return input.Fence, true
		}
	case ConnectorTaskGatewaySchedulableSet:
		var input ConnectorGatewaySchedulableSetInput
		if json.Unmarshal(task.Input, &input) == nil && input.Fence != nil {
			return *input.Fence, true
		}
	case ConnectorTaskGatewaySwitch:
		var input ConnectorGatewaySwitchInput
		if json.Unmarshal(task.Input, &input) == nil && input.Fence != nil {
			return *input.Fence, true
		}
	case ConnectorTaskGatewaySchedulingBarrier:
		var input ConnectorGatewaySchedulingBarrierInput
		if json.Unmarshal(task.Input, &input) == nil {
			return input.Fence, true
		}
	case ConnectorTaskGatewayAccountCreate:
		var input ConnectorGatewayAccountCreateInput
		if json.Unmarshal(task.Input, &input) == nil && input.Fence != nil {
			return *input.Fence, true
		}
	case ConnectorTaskGatewayAccountUpdate:
		var input ConnectorGatewayAccountUpdateInput
		if json.Unmarshal(task.Input, &input) == nil && input.Fence != nil {
			return *input.Fence, true
		}
	case ConnectorTaskGatewayAccountDelete:
		var input ConnectorGatewayAccountDeleteInput
		if json.Unmarshal(task.Input, &input) == nil && input.Fence != nil {
			return *input.Fence, true
		}
	}
	return GatewaySchedulingFence{}, false
}

// GatewaySchedulingFencePlanID accepts only the platform-owned route-plan
// ordering domain. Other scopes may still be valid Connector-local fences,
// but they cannot participate in Core's route-plan task CAS.
func GatewaySchedulingFencePlanID(fence GatewaySchedulingFence) (string, bool) {
	if !IsGatewaySchedulingFence(fence) {
		return "", false
	}
	prefix := ""
	for _, candidate := range []string{"auto-switch/plan/", "recommendation/rollout/", "recommendation-rollout/"} {
		if strings.HasPrefix(fence.Scope, candidate) {
			prefix = candidate
			break
		}
	}
	if prefix == "" {
		return "", false
	}
	planID := strings.TrimPrefix(fence.Scope, prefix)
	if planID == "" || strings.Contains(planID, "/") {
		return "", false
	}
	_, ok := normalizeConnectorIdentifier(planID, MaxConnectorIdentifierBytes)
	return planID, ok
}

func IsConnectorUpstreamIntelligenceSourceID(value string) bool {
	if value != strings.TrimSpace(value) || len(value) > MaxConnectorIdentifierBytes {
		return false
	}
	prefix := "uisrc-"
	if strings.HasPrefix(value, "uisrc_") {
		prefix = "uisrc_"
	} else if !strings.HasPrefix(value, prefix) {
		return false
	}
	if len(value) == len(prefix) {
		return false
	}
	_, ok := normalizeConnectorIdentifier(value, MaxConnectorIdentifierBytes)
	return ok
}

func IsConnectorUpstreamIntelligenceRunID(value string) bool {
	if value != strings.TrimSpace(value) || len(value) > MaxConnectorIdentifierBytes {
		return false
	}
	prefix := "uirun-"
	if strings.HasPrefix(value, "uirun_") {
		prefix = "uirun_"
	} else if !strings.HasPrefix(value, prefix) {
		return false
	}
	if len(value) == len(prefix) {
		return false
	}
	_, ok := normalizeConnectorIdentifier(value, MaxConnectorIdentifierBytes)
	return ok
}

func ValidateConnectorUpstreamIntelligenceCollectInput(input ConnectorUpstreamIntelligenceCollectInput) bool {
	return input.SchemaVersion == 1 && IsConnectorUpstreamIntelligenceSourceID(input.SourceID) &&
		input.Reason == ConnectorUpstreamIntelligenceCollectManualRefresh
}

func ValidateConnectorUpstreamIntelligenceCollectResult(result ConnectorUpstreamIntelligenceCollectResult) bool {
	if !IsConnectorUpstreamIntelligenceRunID(result.RunID) || result.FactCount < 0 || result.ObservedAt.IsZero() {
		return false
	}
	switch result.Status {
	case UpstreamCollectionSucceeded:
		return result.Coverage == UpstreamCoverageComplete
	case UpstreamCollectionPartial:
		return result.Coverage == UpstreamCoveragePartial && result.FactCount > 0
	case UpstreamCollectionFailed:
		return result.Coverage == UpstreamCoverageUnavailable && result.FactCount == 0
	default:
		return false
	}
}

func IsConnectorBindingEncryptionPublicKey(value string) bool {
	if strings.TrimSpace(value) != value || value == "" {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) == ConnectorBindingEncryptionPublicKeySize &&
		base64.RawURLEncoding.EncodeToString(raw) == value && !allZeroBytes(raw)
}

func IsConnectorBindingCiphertext(value string) bool {
	if value == "" || len(value) > 2*(ConnectorBindingEncryptionMaxCiphertext+256) {
		return false
	}
	var envelope struct {
		Algorithm          string `json:"algorithm"`
		EphemeralPublicKey string `json:"ephemeral_public_key"`
		Nonce              string `json:"nonce"`
		Sealed             string `json:"sealed"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) ||
		envelope.Algorithm != ConnectorBindingEncryptionAlgorithm ||
		!IsConnectorBindingEncryptionPublicKey(envelope.EphemeralPublicKey) {
		return false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != ConnectorBindingEncryptionNonceSize ||
		base64.RawURLEncoding.EncodeToString(nonce) != envelope.Nonce {
		return false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(envelope.Sealed)
	return err == nil && len(sealed) >= 16 && len(sealed) <= ConnectorBindingEncryptionMaxCiphertext &&
		base64.RawURLEncoding.EncodeToString(sealed) == envelope.Sealed
}

func allZeroBytes(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}

// IsConnectorQualityProbeField accepts only short, non-sensitive identifiers
// suitable for a probe task. It intentionally rejects URLs, credentials, and
// control characters before task input can be persisted or sent over the wire.
func IsConnectorQualityProbeField(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > MaxConnectorIdentifierBytes || LooksLikeConnectorSensitiveValue(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func NormalizeConnectorGatewayAccounts(accounts []GatewayAccount) ([]GatewayAccount, string) {
	if len(accounts) > MaxConnectorAccounts {
		return nil, "accounts exceeds maximum length"
	}
	out := make([]GatewayAccount, len(accounts))
	for i, account := range accounts {
		normalized, field := normalizeConnectorGatewayAccount(account)
		if field != "" {
			return nil, "accounts[" + decimalIndex(i) + "]." + field + " is invalid"
		}
		out[i] = normalized
	}
	return out, ""
}

func normalizeConnectorGatewayAccount(input GatewayAccount) (GatewayAccount, string) {
	out := input
	var ok bool
	if out.ID, ok = normalizeConnectorIdentifier(input.ID, MaxConnectorIdentifierBytes); !ok {
		return GatewayAccount{}, "id"
	}
	if out.Platform, ok = normalizeConnectorIdentifier(input.Platform, MaxConnectorShortIDBytes); !ok && input.Platform != "" {
		return GatewayAccount{}, "platform"
	}
	if out.Type, ok = normalizeConnectorIdentifier(input.Type, MaxConnectorShortIDBytes); !ok && input.Type != "" {
		return GatewayAccount{}, "type"
	}
	if out.Status, ok = normalizeConnectorIdentifier(input.Status, MaxConnectorShortIDBytes); !ok && input.Status != "" {
		return GatewayAccount{}, "status"
	}
	if out.ProxyID, ok = normalizeConnectorIdentifier(input.ProxyID, MaxConnectorIdentifierBytes); !ok && input.ProxyID != "" {
		return GatewayAccount{}, "proxy_id"
	}
	if out.ExternalRef, ok = normalizeConnectorExternalRef(input.ExternalRef); !ok && input.ExternalRef != "" {
		return GatewayAccount{}, "external_ref"
	}
	if len(input.GroupIDs) > MaxConnectorAccountGroups {
		return GatewayAccount{}, "group_ids"
	}
	seenGroups := make(map[string]struct{}, len(input.GroupIDs))
	out.GroupIDs = nil
	for _, group := range input.GroupIDs {
		normalized, valid := normalizeConnectorIdentifier(group, MaxConnectorIdentifierBytes)
		if !valid {
			return GatewayAccount{}, "group_ids"
		}
		if _, exists := seenGroups[normalized]; exists {
			continue
		}
		seenGroups[normalized] = struct{}{}
		out.GroupIDs = append(out.GroupIDs, normalized)
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if !validConnectorDisplayName(displayName) {
		return GatewayAccount{}, "display_name"
	}
	out.DisplayName = displayName
	if input.Balance != nil && (math.IsNaN(*input.Balance) || math.IsInf(*input.Balance, 0)) {
		return GatewayAccount{}, "balance"
	}
	if input.UsedQuota != nil && (math.IsNaN(*input.UsedQuota) || math.IsInf(*input.UsedQuota, 0)) {
		return GatewayAccount{}, "used_quota"
	}
	if input.CurrentWeight != nil {
		if *input.CurrentWeight < 0 || *input.CurrentWeight > 100 {
			return GatewayAccount{}, "current_weight"
		}
		// Do not retain an alias to the decoder/caller-owned value.
		currentWeight := *input.CurrentWeight
		out.CurrentWeight = &currentWeight
	}
	return out, ""
}

func normalizeConnectorIdentifier(value string, maxBytes int) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxBytes || value == "." || value == ".." ||
		!connectorIdentifierPattern.MatchString(value) || LooksLikeConnectorSensitiveValue(value) {
		return "", false
	}
	return value, true
}

func normalizeConnectorExternalRef(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if normalized, ok := normalizeConnectorIdentifier(value, MaxConnectorIdentifierBytes); ok {
		return normalized, true
	}
	const namespace = "e2m:"
	if len(value) > MaxConnectorIdentifierBytes || !strings.HasPrefix(value, namespace) || LooksLikeConnectorSensitiveValue(value) {
		return "", false
	}
	suffix, ok := normalizeConnectorIdentifier(strings.TrimPrefix(value, namespace), MaxConnectorIdentifierBytes-len(namespace))
	if !ok {
		return "", false
	}
	return namespace + suffix, true
}

func validConnectorDisplayName(value string) bool {
	if value == "" {
		return true
	}
	if len([]rune(value)) > MaxConnectorDisplayNameRunes || LooksLikeConnectorSensitiveValue(value) {
		return false
	}
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, `"`) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func LooksLikeConnectorSensitiveValue(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	normalizedSpaces := strings.Join(strings.Fields(lower), " ")
	withoutSpaces := strings.Map(func(character rune) rune {
		if unicode.IsSpace(character) {
			return -1
		}
		return character
	}, lower)
	for _, marker := range []string{
		"://", "www.", "authorization:", "authorization=", "bearer ",
		"token=", "token:", "api_key=", "api-key:", "x-api-key:",
		"cookie:", "cookie=", "set-cookie:", "proxy-authorization:",
		"header:", "headers=", "raw_response", "raw-response",
	} {
		if strings.Contains(lower, marker) || strings.Contains(normalizedSpaces, marker) || strings.Contains(withoutSpaces, marker) {
			return true
		}
	}
	if connectorSensitiveAssignmentPattern.MatchString(withoutSpaces) {
		return true
	}
	for _, prefix := range []string{
		"sk-", "sk_", "e2m_conn_", "e2m_enroll_", "ghp_", "github_pat_", "xox",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if len(value) > 40 && strings.HasPrefix(lower, "eyj") && strings.Count(value, ".") == 2 {
		return true
	}
	// Opaque high-entropy strings are not useful account metadata and are a
	// common shape for API keys. Real gateway identifiers may be long, but they
	// normally contain separators or non-hex characters.
	if len(value) >= 40 && allASCIIHex(value) {
		return true
	}
	return false
}

func ValidateConnectorTaskExecutionEvidenceNote(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > ConnectorTaskExecutionResolutionMaxNoteRunes || LooksLikeConnectorSensitiveValue(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

func allASCIIHex(value string) bool {
	for _, r := range value {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' {
			continue
		}
		return false
	}
	return true
}

func decimalIndex(value int) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	position := len(buf)
	for value > 0 {
		position--
		buf[position] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[position:])
}
