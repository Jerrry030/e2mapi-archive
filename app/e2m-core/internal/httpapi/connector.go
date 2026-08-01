package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

func (s *Server) authorizeConnector(w http.ResponseWriter, r *http.Request) (contracts.Connector, bool) {
	token := bearerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "connector token is required")
		return contracts.Connector{}, false
	}
	connector, err := s.store.GetConnectorByTokenHash(r.Context(), hashConnectorToken(token))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid connector token")
		return contracts.Connector{}, false
	}
	return connector, true
}

func (s *Server) requireConnectorIdentity(w http.ResponseWriter, r *http.Request, requestedID string, connector contracts.Connector) bool {
	if requestedID != connector.ID {
		if recorder, ok := store.AsOperationalEventRecorder(s.store); ok {
			_ = recorder.RecordCrossOwnerRejected(r.Context())
		}
		writeError(w, http.StatusForbidden, "forbidden", "connector token does not match connector_id")
		return false
	}
	return true
}

func (s *Server) handleLeaseConnectorTasks(w http.ResponseWriter, r *http.Request) {
	var req contracts.ConnectorTaskLeaseRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(req.ConnectorID) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "connector_id is required")
		return
	}
	connector, ok := s.authorizeConnector(w, r)
	if !ok || !s.requireConnectorIdentity(w, r, req.ConnectorID, connector) {
		return
	}
	if req.ProtocolVersion != contracts.ConnectorProtocolVersion {
		writeError(w, http.StatusUpgradeRequired, "protocol_version_unsupported", "connector protocol version is not supported")
		return
	}
	if !contracts.IsConnectorVersion(req.Version) {
		writeError(w, http.StatusBadRequest, "validation_failed", "version must be a strict semantic version")
		return
	}
	if req.RuntimeState.ProtocolVersion != contracts.ConnectorProtocolVersion {
		writeError(w, http.StatusBadRequest, "protocol_version_mismatch", "runtime_state protocol version must match request protocol version")
		return
	}
	req.RuntimeState = contracts.SanitizeConnectorRuntimeState(req.RuntimeState)
	_, err := s.store.RecordConnectorSeen(r.Context(), connector.ID, req.Version, req.RuntimeState)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusForbidden, "connector_revoked", "connector is revoked")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	tasks, err := s.store.LeaseConnectorTasks(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if tasks == nil {
		tasks = []contracts.ConnectorTask{}
	}
	writeJSON(w, http.StatusOK, contracts.ConnectorTaskLeaseResponse{
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		Tasks:           tasks,
		ServerTime:      time.Now().UTC(),
		NextPollAfter:   "5s",
	})
}

func (s *Server) handleCompleteConnectorTask(w http.ResponseWriter, r *http.Request) {
	var req contracts.ConnectorTaskCompleteRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(req.ConnectorID) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "connector_id is required")
		return
	}
	if strings.TrimSpace(req.LeaseNonce) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "lease_nonce is required")
		return
	}
	connector, ok := s.authorizeConnector(w, r)
	if !ok || !s.requireConnectorIdentity(w, r, req.ConnectorID, connector) {
		return
	}
	if connector.ProtocolVersion != contracts.ConnectorProtocolVersion {
		writeError(w, http.StatusUpgradeRequired, "protocol_version_unsupported", "connector protocol version is not supported")
		return
	}
	existing, err := s.store.GetConnectorTask(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "connector task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if existing.ConnectorID != connector.ID {
		if recorder, ok := store.AsOperationalEventRecorder(s.store); ok {
			_ = recorder.RecordCrossOwnerRejected(r.Context())
		}
		writeError(w, http.StatusForbidden, "forbidden", "task belongs to another connector")
		return
	}
	if existing.InstanceID != connector.InstanceID || existing.UserID != connector.UserID {
		if recorder, ok := store.AsOperationalEventRecorder(s.store); ok {
			_ = recorder.RecordCrossOwnerRejected(r.Context())
		}
		writeError(w, http.StatusForbidden, "forbidden", "task belongs to another instance")
		return
	}
	if existing.SchemaVersion != 1 {
		writeError(w, http.StatusUpgradeRequired, "task_schema_unsupported", "connector task schema version is not supported")
		return
	}
	if err := validateConnectorTaskCompletion(existing, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_task_result", err.Error())
		return
	}
	task, err := s.store.CompleteConnectorTask(r.Context(), r.PathValue("id"), req)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "connector task not found")
		case errors.Is(err, store.ErrConflict):
			writeError(w, http.StatusConflict, "task_conflict", "task lease is no longer active")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		}
		return
	}
	result := "accepted"
	if task.Status == contracts.ConnectorTaskPending {
		result = "retrying"
	} else if !req.Success {
		result = "failed"
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:       task.UserID,
		InstanceID:   task.InstanceID,
		ActorType:    "connector",
		ActorID:      req.ConnectorID,
		Action:       connectorTaskCompletionAuditAction(task.Type),
		RiskLevel:    task.RiskLevel,
		EventLevel:   connectorTaskEventLevel(task, result),
		TargetType:   "connector_task",
		TargetID:     task.ID,
		Result:       result,
		ErrorMessage: task.Error.Code,
		Details:      s.connectorTaskAuditDetails(r.Context(), task),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBeginConnectorTaskExecution(w http.ResponseWriter, r *http.Request) {
	var req contracts.ConnectorTaskExecutionRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(req.ConnectorID) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "connector_id is required")
		return
	}
	if strings.TrimSpace(req.LeaseNonce) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "lease_nonce is required")
		return
	}
	connector, ok := s.authorizeConnector(w, r)
	if !ok || !s.requireConnectorIdentity(w, r, req.ConnectorID, connector) {
		return
	}
	if connector.ProtocolVersion != contracts.ConnectorProtocolVersion {
		writeError(w, http.StatusUpgradeRequired, "protocol_version_unsupported", "connector protocol version is not supported")
		return
	}
	existing, err := s.store.GetConnectorTask(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "connector task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if existing.ConnectorID != connector.ID || existing.InstanceID != connector.InstanceID || existing.UserID != connector.UserID {
		if recorder, ok := store.AsOperationalEventRecorder(s.store); ok {
			_ = recorder.RecordCrossOwnerRejected(r.Context())
		}
		writeError(w, http.StatusForbidden, "forbidden", "task belongs to another connector or instance")
		return
	}
	if existing.SchemaVersion != 1 {
		writeError(w, http.StatusUpgradeRequired, "task_schema_unsupported", "connector task schema version is not supported")
		return
	}
	task, err := s.store.BeginConnectorTaskExecution(r.Context(), existing.ID, req)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "connector task not found")
		case errors.Is(err, store.ErrConflict):
			writeError(w, http.StatusConflict, "task_execution_conflict", "task is not authorized to begin execution")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// handleResolveConnectorTaskExecution is the only operator recovery edge for
// a fenced mutation whose remote outcome is uncertain. It is intentionally a
// session-authenticated platform-admin API, never a Connector bearer route.
// The Store commits the terminal state and its L3 audit atomically.
func (s *Server) handleResolveConnectorTaskExecution(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	var req contracts.ConnectorTaskExecutionResolveRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.LeaseNonce = strings.TrimSpace(req.LeaseNonce)
	req.EvidenceNote = strings.TrimSpace(req.EvidenceNote)
	if req.LeaseNonce == "" || !req.Resolution.Valid() || !contracts.ValidateConnectorTaskExecutionEvidenceNote(req.EvidenceNote) {
		writeError(w, http.StatusBadRequest, "validation_failed", "lease_nonce, a supported resolution, and a safe evidence_note are required")
		return
	}
	existing, err := s.store.GetConnectorTask(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "connector task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if err := validateConnectorTaskExecutionResolution(existing, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_resolution", err.Error())
		return
	}
	actor := currentUser(r)
	resolved, err := s.store.ResolveConnectorTaskExecution(r.Context(), existing.ID, req, contracts.OperationAudit{
		UserID:     existing.UserID,
		InstanceID: existing.InstanceID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "connector_task.resolve_execution",
		RiskLevel:  contracts.RiskLevelL3,
		EventLevel: contracts.EventLevelCritical,
		TargetType: "connector_task",
		TargetID:   existing.ID,
		Result:     string(req.Resolution),
		Details: map[string]string{
			"resolution":         string(req.Resolution),
			"evidence_note":      req.EvidenceNote,
			"connector_id":       existing.ConnectorID,
			"lease_nonce_sha256": store.ConnectorTaskLeaseNonceAuditHash(req.LeaseNonce),
		},
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "connector task not found")
		case errors.Is(err, store.ErrConflict):
			writeError(w, http.StatusConflict, "task_execution_conflict", "execution resolution no longer matches the task state")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, contracts.SummarizeConnectorTask(resolved))
}

func validateConnectorTaskExecutionResolution(task contracts.ConnectorTask, req *contracts.ConnectorTaskExecutionResolveRequest) error {
	result := bytes.TrimSpace(req.Result)
	if req.Resolution != contracts.ConnectorTaskExecutionConfirmedApplied {
		if len(result) != 0 {
			return errors.New("only confirmed_applied may include a result")
		}
		req.Result = nil
		return nil
	}
	completion := contracts.ConnectorTaskCompleteRequest{Success: true, Result: result}
	if err := validateConnectorTaskCompletion(task, &completion); err != nil {
		return err
	}
	req.Result = completion.Result
	return nil
}

func connectorTaskEventLevel(task contracts.ConnectorTask, result string) contracts.EventLevel {
	switch result {
	case "accepted":
		switch task.Type {
		case contracts.ConnectorTaskGatewayHealth,
			contracts.ConnectorTaskGatewayAccountsList,
			contracts.ConnectorTaskGatewayQualityProbe,
			contracts.ConnectorTaskGatewayBindingProof,
			contracts.ConnectorTaskUpstreamIntelligenceCollect:
			return contracts.EventLevelInfo
		default:
			return contracts.EventLevelNotice
		}
	case "retrying":
		return contracts.EventLevelNotice
	default:
		return contracts.EventLevelWarning
	}
}

func (s *Server) connectorTaskAuditDetails(ctx context.Context, task contracts.ConnectorTask) map[string]string {
	details := map[string]string{}
	put := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" {
			details[key] = value
		}
	}
	put("next_attempt_at", taskAvailableAtForAudit(task))
	put("reason_code", task.Error.Code)
	switch task.Type {
	case contracts.ConnectorTaskGatewayAccountsList:
		var result contracts.ConnectorGatewayAccountsListResult
		if json.Unmarshal(task.Result, &result) == nil {
			details["account_count"] = strconv.Itoa(len(result.Accounts))
		}
	case contracts.ConnectorTaskGatewayQualityProbe:
		var input contracts.ConnectorGatewayQualityProbeInput
		if json.Unmarshal(task.Input, &input) == nil {
			put("account_id", input.AccountID)
			put("channel_id", input.ChannelID)
		}
		var result contracts.ConnectorGatewayQualityProbeResult
		if json.Unmarshal(task.Result, &result) == nil {
			details["probe_status"] = "unavailable"
			if result.Success {
				details["probe_status"] = "available"
			}
		}
	case contracts.ConnectorTaskGatewayBindingProof:
		var input contracts.ConnectorGatewayBindingProofInput
		if json.Unmarshal(task.Input, &input) == nil {
			put("channel_id", input.ChannelID)
		}
	case contracts.ConnectorTaskGatewayBindingInstall:
		var input contracts.ConnectorGatewayBindingInstallInput
		if json.Unmarshal(task.Input, &input) == nil {
			put("channel_id", input.ChannelID)
		}
	case contracts.ConnectorTaskGatewaySchedulableSet:
		var input contracts.ConnectorGatewaySchedulableSetInput
		if json.Unmarshal(task.Input, &input) == nil {
			put("account_id", input.AccountID)
			details["schedulable"] = strconv.FormatBool(input.Schedulable)
		}
	case contracts.ConnectorTaskGatewayTrafficShareSet:
		var input contracts.ConnectorGatewayTrafficShareSetInput
		if decodeStrictConnectorPayload(task.Input, &input) == nil &&
			contracts.ValidateConnectorGatewayTrafficShareSetInput(input) {
			put("account_id", input.AccountID)
			details["traffic_share"] = strconv.Itoa(input.Weight)
			details["fence_scope"] = input.Fence.Scope
			details["fence_version"] = strconv.FormatInt(input.Fence.Version, 10)
			details["fence_sequence"] = strconv.FormatInt(input.Fence.Sequence, 10)
		}
	case contracts.ConnectorTaskGatewaySwitch:
		var input contracts.ConnectorGatewaySwitchInput
		if json.Unmarshal(task.Input, &input) == nil {
			put("from_account_id", input.DisableAccountID)
			put("to_account_id", input.EnableAccountID)
		}
	case contracts.ConnectorTaskGatewayAccountCreate:
		var input contracts.ConnectorGatewayAccountCreateInput
		if json.Unmarshal(task.Input, &input) == nil {
			put("channel_id", input.Spec.ChannelID)
			put("account_name", input.Spec.DisplayName)
		}
	case contracts.ConnectorTaskGatewayAccountUpdate:
		var input contracts.ConnectorGatewayAccountUpdateInput
		if json.Unmarshal(task.Input, &input) == nil {
			put("channel_id", input.Spec.ChannelID)
			put("account_id", input.Spec.RemoteID)
			put("account_name", input.Spec.DisplayName)
		}
	case contracts.ConnectorTaskGatewayAccountDelete:
		var input contracts.ConnectorGatewayAccountDeleteInput
		if json.Unmarshal(task.Input, &input) == nil {
			put("account_id", input.AccountID)
		}
	case contracts.ConnectorTaskUpstreamIntelligenceCollect:
		var input contracts.ConnectorUpstreamIntelligenceCollectInput
		if json.Unmarshal(task.Input, &input) == nil {
			put("source_id", input.SourceID)
		}
		var result contracts.ConnectorUpstreamIntelligenceCollectResult
		if json.Unmarshal(task.Result, &result) == nil {
			put("run_id", result.RunID)
			details["fact_count"] = strconv.Itoa(result.FactCount)
		}
	}
	s.enrichConnectorTaskAuditDetails(ctx, task, details)
	if len(details) == 0 {
		return nil
	}
	return details
}

func (s *Server) enrichConnectorTaskAuditDetails(ctx context.Context, task contracts.ConnectorTask, details map[string]string) {
	if instance, err := s.store.GetInstance(ctx, task.InstanceID); err == nil {
		details["instance_name"] = instance.Name
	}
	for idKey, nameKey := range map[string]string{
		"channel_id": "channel_name",
	} {
		if id := strings.TrimSpace(details[idKey]); id != "" {
			if channel, err := s.store.GetUpstreamChannel(ctx, id); err == nil {
				details[nameKey] = channel.DisplayName
			}
		}
	}

	accounts := map[string]string{}
	if tasks, err := s.store.ListConnectorTasks(ctx, contracts.ConnectorTaskFilter{
		UserID: task.UserID, InstanceID: task.InstanceID, ConnectorID: task.ConnectorID,
		Status: contracts.ConnectorTaskSucceeded, Types: []contracts.ConnectorTaskType{contracts.ConnectorTaskGatewayAccountsList},
		Limit: 20,
	}); err == nil {
		for _, listedTask := range tasks {
			var listed contracts.ConnectorGatewayAccountsListResult
			if json.Unmarshal(listedTask.Result, &listed) != nil {
				continue
			}
			for _, account := range listed.Accounts {
				if id := strings.TrimSpace(account.ID); id != "" && strings.TrimSpace(account.DisplayName) != "" {
					accounts[id] = strings.TrimSpace(account.DisplayName)
				}
			}
			break
		}
	}
	bindings, _ := s.store.ListPublishedBindings(ctx, "")
	for _, binding := range bindings {
		instanceID := strings.TrimSpace(binding.InstanceID)
		if instanceID == "" {
			if plan, err := s.store.GetRoutePlan(ctx, binding.PlanID); err == nil && plan.UserID == task.UserID {
				instanceID = plan.InstanceID
			}
		}
		if instanceID != task.InstanceID {
			continue
		}
		if remoteID := strings.TrimSpace(binding.RemoteID); remoteID != "" && accounts[remoteID] == "" {
			if channel, err := s.store.GetUpstreamChannel(ctx, binding.ChannelID); err == nil {
				accounts[remoteID] = strings.TrimSpace(channel.DisplayName)
			}
		}
	}
	for idKey, nameKey := range map[string]string{
		"account_id":      "account_name",
		"from_account_id": "from_account_name",
		"to_account_id":   "to_account_name",
	} {
		if details[nameKey] != "" {
			continue
		}
		if name := accounts[strings.TrimSpace(details[idKey])]; name != "" {
			details[nameKey] = name
		}
	}
}

func taskAvailableAtForAudit(task contracts.ConnectorTask) string {
	if task.Status != contracts.ConnectorTaskPending || task.AvailableAt.IsZero() {
		return ""
	}
	return task.AvailableAt.UTC().Format(time.RFC3339)
}
func connectorTaskCompletionAuditAction(taskType contracts.ConnectorTaskType) string {
	switch taskType {
	case contracts.ConnectorTaskGatewayHealth,
		contracts.ConnectorTaskGatewayAccountsList,
		contracts.ConnectorTaskGatewayQualityProbe,
		contracts.ConnectorTaskGatewayBindingProof,
		contracts.ConnectorTaskGatewayBindingInstall,
		contracts.ConnectorTaskGatewaySchedulableSet,
		contracts.ConnectorTaskGatewayTrafficShareSet,
		contracts.ConnectorTaskGatewaySwitch,
		contracts.ConnectorTaskGatewaySchedulingBarrier,
		contracts.ConnectorTaskGatewayAccountCreate,
		contracts.ConnectorTaskGatewayAccountUpdate,
		contracts.ConnectorTaskGatewayAccountDelete,
		contracts.ConnectorTaskUpstreamIntelligenceCollect:
		return "connector_task.complete." + string(taskType)
	default:
		return "connector_task.complete"
	}
}

func (s *Server) handleEnrollConnector(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	var req contracts.ConnectorEnrollRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	enrollmentToken := strings.TrimSpace(req.EnrollmentToken)
	if enrollmentToken == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "enrollment_token is required")
		return
	}
	if strings.TrimSpace(req.ConnectorID) == "" || strings.TrimSpace(req.InstanceID) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "connector_id and instance_id are required")
		return
	}
	if req.ProtocolVersion != contracts.ConnectorProtocolVersion {
		writeError(w, http.StatusUpgradeRequired, "protocol_version_unsupported", "connector protocol version is not supported")
		return
	}
	if !contracts.IsConnectorVersion(req.Version) {
		writeError(w, http.StatusBadRequest, "validation_failed", "version must be a strict semantic version")
		return
	}
	connectorToken, err := newConnectorToken("e2m_conn_")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_error", err.Error())
		return
	}
	connector, _, err := s.store.UseConnectorEnrollment(r.Context(), hashConnectorToken(enrollmentToken), contracts.Connector{
		ID:              strings.TrimSpace(req.ConnectorID),
		InstanceID:      strings.TrimSpace(req.InstanceID),
		Version:         req.Version,
		ProtocolVersion: req.ProtocolVersion,
		TokenHash:       hashConnectorToken(connectorToken),
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusUnauthorized, "invalid_enrollment_token", "invalid enrollment token")
		case errors.Is(err, store.ErrConflict):
			writeError(w, http.StatusConflict, "enrollment_unavailable", "enrollment token is expired or already used")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		}
		return
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     connector.UserID,
		ActorType:  "connector",
		ActorID:    connector.ID,
		Action:     "connector.enroll",
		RiskLevel:  contracts.RiskLevelL0,
		TargetType: "connector",
		TargetID:   connector.ID,
		Result:     "accepted",
	})
	writeJSON(w, http.StatusCreated, contracts.ConnectorEnrollResponse{
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		Connector:       connector,
		ConnectorToken:  connectorToken,
		ServerTime:      time.Now().UTC(),
		NextPollAfter:   "5s",
	})
}

func (s *Server) handleListConnectors(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.scopeOwnerUser(w, r, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	connectors, err := s.store.ListConnectors(r.Context(), contracts.ConnectorFilter{
		UserID:     userID,
		InstanceID: strings.TrimSpace(r.URL.Query().Get("instance_id")),
		Status:     contracts.ConnectorStatus(strings.TrimSpace(r.URL.Query().Get("status"))),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if connectors == nil {
		connectors = []contracts.Connector{}
	}
	for i := range connectors {
		connectors[i] = effectiveConnectorStatus(connectors[i], time.Now().UTC())
	}
	writeJSON(w, http.StatusOK, connectors)
}

func (s *Server) handleCreateConnectorEnrollment(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	var input contracts.CreateConnectorEnrollmentRequest
	if err := decodeStrictJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(input.InstanceID) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "instance_id is required")
		return
	}
	inst, ok := s.instanceForWrite(w, r, input.InstanceID)
	if !ok {
		return
	}
	if input.UserID != 0 && input.UserID != inst.UserID {
		writeError(w, http.StatusForbidden, "forbidden", "instance belongs to another user")
		return
	}
	input.UserID = inst.UserID
	applyInstanceDefaultsToEnrollment(&input, inst)
	var requestedUserID string
	if input.UserID != 0 {
		requestedUserID = strconv.FormatInt(input.UserID, 10)
	}
	userID, ok := s.scopeOwnerUser(w, r, requestedUserID)
	if !ok {
		return
	}
	if userID == 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id is required")
		return
	}
	actor := currentUser(r)
	if !s.requireOwnerWrite(w, r, userID) {
		return
	}
	if _, ok := s.enabledUserWithRole(w, r, userID, contracts.UserRoleClient, "owner"); !ok {
		return
	}
	token, err := newConnectorToken("e2m_enroll_")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_error", err.Error())
		return
	}
	expires := time.Now().UTC().Add(enrollmentTTL(input.ExpiresInSeconds))
	enrollment, err := s.store.CreateConnectorEnrollment(r.Context(), contracts.ConnectorEnrollment{
		UserID:      userID,
		InstanceID:  input.InstanceID,
		ConnectorID: strings.TrimSpace(input.ConnectorID),
		Name:        strings.TrimSpace(input.Name),
		TokenHash:   hashConnectorToken(token),
		ExpiresAt:   expires,
		CreatedBy:   actor.Email,
	})
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) || errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "duplicate_connector", "connector_id already has an active enrollment or connector")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	coreURL := s.externalCoreURL(r)
	installGuide := connectorInstallGuide(coreURL, enrollment.ConnectorID, input)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     userID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "connector.enrollment.create",
		RiskLevel:  contracts.RiskLevelL1,
		TargetType: "connector_enrollment",
		TargetID:   enrollment.ID,
		Result:     "accepted",
	})
	writeJSON(w, http.StatusCreated, contracts.CreateConnectorEnrollmentResponse{
		Enrollment:     enrollment,
		Token:          token,
		CoreURL:        coreURL,
		InstallCommand: installGuide.DockerRunCommand,
		InstallGuide:   installGuide,
	})
}

func (s *Server) createConnectorEnrollmentForInstance(r *http.Request, inst contracts.Instance, createdBy string) (contracts.CreateConnectorEnrollmentResponse, error) {
	input := contracts.CreateConnectorEnrollmentRequest{
		UserID:           inst.UserID,
		InstanceID:       inst.ID,
		ExpiresInSeconds: 7 * 24 * 60 * 60,
	}
	applyInstanceDefaultsToEnrollment(&input, inst)
	token, err := newConnectorToken("e2m_enroll_")
	if err != nil {
		return contracts.CreateConnectorEnrollmentResponse{}, err
	}
	enrollment, err := s.store.CreateConnectorEnrollment(r.Context(), contracts.ConnectorEnrollment{
		UserID:      inst.UserID,
		InstanceID:  inst.ID,
		ConnectorID: strings.TrimSpace(input.ConnectorID),
		Name:        strings.TrimSpace(input.Name),
		TokenHash:   hashConnectorToken(token),
		ExpiresAt:   time.Now().UTC().Add(enrollmentTTL(input.ExpiresInSeconds)),
		CreatedBy:   createdBy,
	})
	if err != nil {
		return contracts.CreateConnectorEnrollmentResponse{}, err
	}
	coreURL := s.externalCoreURL(r)
	installGuide := connectorInstallGuide(coreURL, enrollment.ConnectorID, input)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     inst.UserID,
		InstanceID: inst.ID,
		ActorType:  "user",
		ActorID:    createdBy,
		Action:     "connector.enrollment.create",
		RiskLevel:  contracts.RiskLevelL1,
		TargetType: "connector_enrollment",
		TargetID:   enrollment.ID,
		Result:     "accepted",
	})
	return contracts.CreateConnectorEnrollmentResponse{
		Enrollment:     enrollment,
		Token:          token,
		CoreURL:        coreURL,
		InstallCommand: installGuide.DockerRunCommand,
		InstallGuide:   installGuide,
	}, nil
}

func applyInstanceDefaultsToEnrollment(input *contracts.CreateConnectorEnrollmentRequest, inst contracts.Instance) {
	if strings.TrimSpace(input.Name) == "" {
		input.Name = strings.TrimSpace(inst.Name) + " 连接器"
	}
	if strings.TrimSpace(input.InstanceID) == "" {
		input.InstanceID = inst.ID
	}
	if strings.TrimSpace(inst.ConnectorID) != "" {
		input.ConnectorID = strings.TrimSpace(inst.ConnectorID)
	}
}

func (s *Server) handleRotateConnectorToken(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	connector, ok := s.connectorForWrite(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	token, err := newConnectorToken("e2m_conn_")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_error", err.Error())
		return
	}
	updated, err := s.store.UpdateConnectorToken(r.Context(), connector.ID, hashConnectorToken(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "connector not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     updated.UserID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "connector.token.rotate",
		RiskLevel:  contracts.RiskLevelL1,
		TargetType: "connector",
		TargetID:   updated.ID,
		Result:     "accepted",
	})
	writeJSON(w, http.StatusOK, contracts.RotateConnectorTokenResponse{Connector: updated, ConnectorToken: token})
}

func (s *Server) handleRevokeConnector(w http.ResponseWriter, r *http.Request) {
	connector, ok := s.connectorForWrite(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	updated, err := s.store.RevokeConnector(r.Context(), connector.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "connector not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     updated.UserID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "connector.revoke",
		RiskLevel:  contracts.RiskLevelL1,
		TargetType: "connector",
		TargetID:   updated.ID,
		Result:     "accepted",
	})
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleBindInstanceConnector(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instanceForWrite(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var input contracts.BindInstanceConnectorRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	connectorID := strings.TrimSpace(input.ConnectorID)
	if connectorID != "" {
		connector, err := s.store.GetConnector(r.Context(), connectorID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "connector not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		if connector.UserID != inst.UserID {
			writeError(w, http.StatusForbidden, "forbidden", "connector belongs to another user")
			return
		}
		if connector.Status == contracts.ConnectorStatusRevoked {
			writeError(w, http.StatusConflict, "connector_revoked", "connector is revoked")
			return
		}
		if connector.InstanceID != inst.ID {
			writeError(w, http.StatusConflict, "connector_instance_mismatch", "connector is registered for another instance")
			return
		}
	}
	updated, err := s.store.UpdateInstanceConnector(r.Context(), inst.ID, connectorID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "instance not found")
		case errors.Is(err, store.ErrConflict):
			writeError(w, http.StatusConflict, "connector_binding_conflict", "connector is revoked or no longer matches the instance")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		}
		return
	}
	actor := currentUser(r)
	action := "instance.connector.unbind"
	targetID := inst.ID
	if connectorID != "" {
		action = "instance.connector.bind"
		targetID = connectorID
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     updated.UserID,
		InstanceID: updated.ID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     action,
		RiskLevel:  contracts.RiskLevelL1,
		TargetType: "connector",
		TargetID:   targetID,
		Result:     "accepted",
	})
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) connectorForWrite(w http.ResponseWriter, r *http.Request, id string) (contracts.Connector, bool) {
	connector, err := s.store.GetConnector(r.Context(), strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "connector not found")
		} else {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		}
		return contracts.Connector{}, false
	}
	if !s.requireOwnerWrite(w, r, connector.UserID) {
		return contracts.Connector{}, false
	}
	return connector, true
}

func (s *Server) handleListConnectorTasks(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.scopeOwnerUser(w, r, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	filter := contracts.ConnectorTaskFilter{
		UserID:      userID,
		InstanceID:  strings.TrimSpace(r.URL.Query().Get("instance_id")),
		ConnectorID: strings.TrimSpace(r.URL.Query().Get("connector_id")),
		Status:      contracts.ConnectorTaskStatus(strings.TrimSpace(r.URL.Query().Get("status"))),
		Limit:       limit,
	}
	tasks, err := s.store.ListConnectorTasks(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	summaries := make([]contracts.ConnectorTaskSummary, 0, len(tasks))
	for _, task := range tasks {
		summaries = append(summaries, contracts.SummarizeConnectorTask(task))
	}
	writeJSON(w, http.StatusOK, summaries)
}

func validateConnectorCompletion(taskType contracts.ConnectorTaskType, req *contracts.ConnectorTaskCompleteRequest) error {
	return validateConnectorTaskCompletion(contracts.ConnectorTask{Type: taskType}, req)
}

func validateConnectorTaskCompletion(task contracts.ConnectorTask, req *contracts.ConnectorTaskCompleteRequest) error {
	if len(req.Result) > 1<<20 {
		return errors.New("task result exceeds 1 MiB")
	}
	hadReportedError := req.Error.Code != "" || req.Error.Retryable
	if !req.Success {
		if len(bytes.TrimSpace(req.Result)) != 0 {
			return errors.New("failed task cannot include a result")
		}
		if !contracts.IsConnectorReportedErrorCode(req.Error.Code) {
			return errors.New("failed task requires an allowlisted error code")
		}
		req.Result = nil
		return nil
	}
	if hadReportedError {
		return errors.New("successful task cannot include an error")
	}
	if len(bytes.TrimSpace(req.Result)) == 0 || bytes.Equal(bytes.TrimSpace(req.Result), []byte("null")) {
		return errors.New("successful task result is required and must be an object")
	}
	var target any
	switch task.Type {
	case contracts.ConnectorTaskGatewayHealth:
		target = &contracts.ConnectorGatewayHealthResult{}
	case contracts.ConnectorTaskGatewayAccountsList:
		target = &contracts.ConnectorGatewayAccountsListResult{}
	case contracts.ConnectorTaskGatewayQualityProbe:
		target = &contracts.ConnectorGatewayQualityProbeResult{}
	case contracts.ConnectorTaskGatewayBindingProof:
		target = &contracts.ConnectorGatewayBindingProofResult{}
	case contracts.ConnectorTaskGatewayBindingInstall:
		target = &contracts.ConnectorGatewayBindingInstallResult{}
	case contracts.ConnectorTaskGatewayTrafficShareSet:
		target = &contracts.ConnectorGatewayTrafficShareSetResult{}
	case contracts.ConnectorTaskUpstreamIntelligenceCollect:
		target = &contracts.ConnectorUpstreamIntelligenceCollectResult{}
	case contracts.ConnectorTaskGatewaySchedulableSet,
		contracts.ConnectorTaskGatewaySwitch,
		contracts.ConnectorTaskGatewaySchedulingBarrier,
		contracts.ConnectorTaskGatewayAccountCreate,
		contracts.ConnectorTaskGatewayAccountUpdate,
		contracts.ConnectorTaskGatewayAccountDelete:
		target = &contracts.ConnectorGatewayMutationResult{}
	default:
		return fmt.Errorf("unsupported task type %q", task.Type)
	}
	decoder := json.NewDecoder(bytes.NewReader(req.Result))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode typed result: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("task result must contain one JSON value")
	}
	if err := validateTypedConnectorResult(target); err != nil {
		return err
	}
	if task.Type == contracts.ConnectorTaskGatewayTrafficShareSet {
		var input contracts.ConnectorGatewayTrafficShareSetInput
		if err := decodeStrictConnectorPayload(task.Input, &input); err != nil ||
			!contracts.ValidateConnectorGatewayTrafficShareSetResult(input, *target.(*contracts.ConnectorGatewayTrafficShareSetResult)) {
			return errors.New("traffic-share result does not exactly match the persisted task input")
		}
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return err
	}
	req.Result = canonical
	return nil
}

func validateTypedConnectorResult(value any) error {
	switch result := value.(type) {
	case *contracts.ConnectorGatewayHealthResult:
		if strings.TrimSpace(result.Status) == "" || result.CheckedAt.IsZero() {
			return errors.New("health result requires status and checked_at")
		}
	case *contracts.ConnectorGatewayAccountsListResult:
		if result.Accounts == nil {
			return errors.New("accounts result requires an accounts array")
		}
		normalized, validationError := contracts.NormalizeConnectorGatewayAccounts(result.Accounts)
		if validationError != "" {
			return errors.New(validationError)
		}
		result.Accounts = normalized
	case *contracts.ConnectorGatewayQualityProbeResult:
		if !contracts.IsQualityProbeCapability(result.Capability) || !contracts.IsQualityProbeEndpointPath(result.EndpointPath) {
			return errors.New("quality probe result requires capability and endpoint_path")
		}
		if result.ObservedAt.IsZero() {
			return errors.New("quality probe result requires observed_at")
		}
		if result.Status < 0 || result.Status > 599 {
			return errors.New("quality probe status must be between 0 and 599")
		}
		if !finiteNonNegative(result.FirstTokenMS) || !finiteNonNegative(result.TotalMS) {
			return errors.New("quality probe latency values must be finite and non-negative")
		}
		if result.FirstTokenMS > 0 && result.TotalMS > 0 && result.FirstTokenMS > result.TotalMS {
			return errors.New("quality probe first_token_ms cannot exceed total_ms")
		}
		if !validObservationErrorType(result.ErrorType) {
			return errors.New("quality probe error_type is not supported")
		}
		if result.Success && result.ErrorType != contracts.ErrorNone {
			return errors.New("successful quality probe cannot include error_type")
		}
		if result.Success && (result.Status < http.StatusOK || result.Status >= http.StatusMultipleChoices) {
			return errors.New("successful quality probe requires a 2xx status")
		}
		if result.Success && (result.FirstTokenMS <= 0 || result.TotalMS <= 0) {
			return errors.New("successful quality probe requires first_token_ms and total_ms")
		}
		if !result.Success && result.ErrorType == contracts.ErrorNone {
			return errors.New("failed quality probe requires error_type")
		}
	case *contracts.ConnectorGatewayBindingProofResult:
		proof, err := hex.DecodeString(result.Proof)
		if err != nil || len(proof) != contracts.ConnectorGatewayBindingProofHexLength/2 || strings.ToLower(result.Proof) != result.Proof {
			return errors.New("binding proof result must be 64 lowercase hexadecimal characters")
		}
	case *contracts.ConnectorGatewayBindingInstallResult:
		if !contracts.IsConnectorQualityProbeField(result.BindingID) ||
			!contracts.IsConnectorQualityProbeField(result.ChannelID) || result.KeyVersion <= 0 {
			return errors.New("binding install result requires binding_id, channel_id, and key_version")
		}
	case *contracts.ConnectorGatewayTrafficShareSetResult:
		if !contracts.ValidateConnectorGatewayTrafficShareSetInput(contracts.ConnectorGatewayTrafficShareSetInput{
			AccountID: result.AccountID,
			Weight:    result.Weight,
			Fence:     result.Fence,
		}) {
			return errors.New("traffic-share result requires a valid account_id, weight, and complete scheduling fence")
		}
	case *contracts.ConnectorUpstreamIntelligenceCollectResult:
		if !contracts.ValidateConnectorUpstreamIntelligenceCollectResult(*result) {
			return errors.New("upstream intelligence collect result is invalid")
		}
	case *contracts.ConnectorGatewayMutationResult:
		result.RemoteID = strings.TrimSpace(result.RemoteID)
	}
	return nil
}

func decodeStrictConnectorPayload(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("connector payload is required and must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("connector payload must contain one JSON value")
	}
	return nil
}

func decodeStrictJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func effectiveConnectorStatus(connector contracts.Connector, now time.Time) contracts.Connector {
	if connector.Status == contracts.ConnectorStatusRevoked {
		return connector
	}
	if connector.LastSeenAt == nil || connector.LastSeenAt.Before(now.Add(-time.Minute)) {
		connector.Status = contracts.ConnectorStatusOffline
	}
	return connector
}

func newConnectorToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

func hashConnectorToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func enrollmentTTL(seconds int) time.Duration {
	if seconds <= 0 {
		return 24 * time.Hour
	}
	if seconds < 5*60 {
		seconds = 5 * 60
	}
	if seconds > 7*24*60*60 {
		seconds = 7 * 24 * 60 * 60
	}
	return time.Duration(seconds) * time.Second
}

func (s *Server) externalCoreURL(r *http.Request) string {
	if s.publicBaseURL != "" {
		return s.publicBaseURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return strings.TrimRight(scheme+"://"+r.Host, "/")
}

const connectorLocalConfigPort = 18081

func connectorInstallGuide(coreURL, connectorID string, input contracts.CreateConnectorEnrollmentRequest) contracts.ConnectorInstallGuide {
	image := "ghcr.io/e2m/e2m-agent:latest"
	hostPort := input.LocalConfigPort
	if hostPort < 1024 || hostPort > 65535 {
		hostPort = connectorLocalConfigPort
	}
	container := dockerName("e2m-agent", connectorID)
	volume := dockerName("e2m-agent-data", connectorID)
	guide := contracts.ConnectorInstallGuide{
		ConnectorID:         connectorID,
		InstanceID:          strings.TrimSpace(input.InstanceID),
		CoreURL:             coreURL,
		ConnectorImageRef:   image,
		ContainerName:       container,
		DataVolumeName:      volume,
		EnrollmentTokenFile: "e2m-connector-enrollment-token",
		LocalConfigURL:      fmt.Sprintf("http://127.0.0.1:%d", hostPort),
		LocalConfigPort:     hostPort,
		Warnings:            connectorInstallWarnings(coreURL),
	}
	guide.DockerRunCommand = connectorDockerRunCommand(guide)
	guide.DockerComposeYAML = connectorDockerComposeYAML(guide)
	return guide
}

func connectorDockerRunCommand(guide contracts.ConnectorInstallGuide) string {
	return fmt.Sprintf(
		"docker volume create %s && docker run -d --name %s --restart unless-stopped -p 127.0.0.1:%d:%d -v %s:/var/lib/e2m-agent --mount type=bind,src=$(pwd)/%s,dst=/run/secrets/e2m_connector_enrollment,readonly -e E2M_CORE_URL='%s' -e E2M_CONNECTOR_ID='%s' -e E2M_INSTANCE_ID='%s' -e E2M_CONNECTOR_ENABLED='true' -e E2M_CONNECTOR_ENROLL_TOKEN_FILE='/run/secrets/e2m_connector_enrollment' -e E2M_CONNECTOR_TOKEN_FILE='/var/lib/e2m-agent/connector.token' -e E2M_LOCAL_UI_ADDR='0.0.0.0:%d' -e E2M_LOCAL_UI_ALLOW_PRIVATE_PEERS='true' '%s'",
		guide.DataVolumeName, guide.ContainerName, guide.LocalConfigPort, connectorLocalConfigPort, guide.DataVolumeName,
		shellQuoteValue(guide.EnrollmentTokenFile), shellQuoteValue(guide.CoreURL), shellQuoteValue(guide.ConnectorID), shellQuoteValue(guide.InstanceID), connectorLocalConfigPort, shellQuoteValue(guide.ConnectorImageRef),
	)
}

func connectorDockerComposeYAML(guide contracts.ConnectorInstallGuide) string {
	var b strings.Builder
	b.WriteString("services:\n")
	b.WriteString("  e2m-agent:\n")
	b.WriteString("    image: " + yamlQuote(guide.ConnectorImageRef) + "\n")
	b.WriteString("    container_name: " + yamlQuote(guide.ContainerName) + "\n")
	b.WriteString("    restart: unless-stopped\n")
	b.WriteString("    environment:\n")
	b.WriteString("      E2M_CORE_URL: " + yamlQuote(guide.CoreURL) + "\n")
	b.WriteString("      E2M_CONNECTOR_ID: " + yamlQuote(guide.ConnectorID) + "\n")
	b.WriteString("      E2M_INSTANCE_ID: " + yamlQuote(guide.InstanceID) + "\n")
	b.WriteString("      E2M_CONNECTOR_ENABLED: \"true\"\n")
	b.WriteString("      E2M_CONNECTOR_ENROLL_TOKEN_FILE: \"/run/secrets/e2m_connector_enrollment\"\n")
	b.WriteString("      E2M_CONNECTOR_TOKEN_FILE: \"/var/lib/e2m-agent/connector.token\"\n")
	b.WriteString("      E2M_LOCAL_UI_ADDR: " + yamlQuote(fmt.Sprintf("0.0.0.0:%d", connectorLocalConfigPort)) + "\n")
	b.WriteString("      E2M_LOCAL_UI_ALLOW_PRIVATE_PEERS: \"true\"\n")
	b.WriteString("    ports:\n")
	b.WriteString("      - " + yamlQuote(fmt.Sprintf("127.0.0.1:%d:%d", guide.LocalConfigPort, connectorLocalConfigPort)) + "\n")
	b.WriteString("    volumes:\n")
	b.WriteString("      - " + yamlQuote(guide.DataVolumeName+":/var/lib/e2m-agent") + "\n")
	b.WriteString("    secrets:\n")
	b.WriteString("      - e2m_connector_enrollment\n")
	b.WriteString("secrets:\n")
	b.WriteString("  e2m_connector_enrollment:\n")
	b.WriteString("    file: " + yamlQuote("./"+guide.EnrollmentTokenFile) + "\n")
	b.WriteString("volumes:\n")
	b.WriteString("  " + guide.DataVolumeName + ": {}\n")
	return b.String()
}

func connectorInstallWarnings(coreURL string) []string {
	var warnings []string
	if strings.Contains(coreURL, "localhost") || strings.Contains(coreURL, "127.0.0.1") {
		warnings = append(warnings, "Core 地址使用了 localhost/127.0.0.1。连接器在 Docker 容器内运行时，localhost 指向连接器容器自身；同一 compose 网络请改用 http://e2m-core:8080，Core 在宿主机请改用 http://host.docker.internal:18080，生产环境请使用连接器可访问的公网或内网地址。")
	}
	return warnings
}

func yamlQuote(value string) string {
	return strconv.Quote(value)
}

func shellQuoteValue(value string) string {
	return strings.ReplaceAll(value, "'", "'\"'\"'")
}

func dockerName(prefix, id string) string {
	raw := strings.TrimSpace(prefix + "-" + id)
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		return prefix
	}
	return out
}
