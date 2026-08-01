package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"e2m.local/contracts"
)

func validConnectorCompletionError(req contracts.ConnectorTaskCompleteRequest) bool {
	if req.Success {
		return req.Error == (contracts.ConnectorTaskError{})
	}
	return contracts.IsConnectorReportedErrorCode(req.Error.Code)
}

func validStoredConnectorTaskError(taskError contracts.ConnectorTaskError) bool {
	if taskError.Code == "" {
		return !taskError.Retryable
	}
	return contracts.IsConnectorTaskErrorCode(taskError.Code)
}

func validConnectorTaskExecutionResolution(task contracts.ConnectorTask, req contracts.ConnectorTaskExecutionResolveRequest) bool {
	if task.Status != contracts.ConnectorTaskExecuting || strings.TrimSpace(req.LeaseNonce) == "" || req.LeaseNonce != task.LeaseNonce ||
		!req.Resolution.Valid() || !contracts.ValidateConnectorTaskExecutionEvidenceNote(req.EvidenceNote) {
		return false
	}
	trimmedResult := bytes.TrimSpace(req.Result)
	if req.Resolution != contracts.ConnectorTaskExecutionConfirmedApplied {
		return len(trimmedResult) == 0
	}
	return validResolvedConnectorTaskResult(task, trimmedResult)
}

// Store-level validation is intentionally conservative and duplicated from
// the wire boundary so direct Store callers cannot persist an untyped applied
// claim. HTTP performs richer canonicalization before entering the Store.
func validResolvedConnectorTaskResult(task contracts.ConnectorTask, raw json.RawMessage) bool {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || len(raw) > 1<<20 {
		return false
	}
	var target any
	switch task.Type {
	case contracts.ConnectorTaskGatewayTrafficShareSet:
		var input contracts.ConnectorGatewayTrafficShareSetInput
		var result contracts.ConnectorGatewayTrafficShareSetResult
		if strictStoreJSON(task.Input, &input) != nil || strictStoreJSON(raw, &result) != nil {
			return false
		}
		return contracts.ValidateConnectorGatewayTrafficShareSetResult(input, result)
	case contracts.ConnectorTaskGatewaySchedulableSet, contracts.ConnectorTaskGatewaySwitch,
		contracts.ConnectorTaskGatewaySchedulingBarrier, contracts.ConnectorTaskGatewayAccountCreate,
		contracts.ConnectorTaskGatewayAccountUpdate, contracts.ConnectorTaskGatewayAccountDelete:
		target = &contracts.ConnectorGatewayMutationResult{}
	default:
		return false
	}
	return strictStoreJSON(raw, target) == nil
}

func strictStoreJSON(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return err
	}
	return nil
}

// ConnectorTaskLeaseNonceAuditHash returns the irreversible correlation value
// used by the operator resolution audit. The live nonce is an execution
// credential and must never be copied into audit details or API responses.
func ConnectorTaskLeaseNonceAuditHash(leaseNonce string) string {
	digest := sha256.Sum256([]byte(leaseNonce))
	return fmt.Sprintf("sha256:%x", digest)
}

func validConnectorTaskResolutionAudit(task contracts.ConnectorTask, req contracts.ConnectorTaskExecutionResolveRequest, audit contracts.OperationAudit) bool {
	if audit.UserID != task.UserID || audit.InstanceID != task.InstanceID || audit.ActorType != "user" || strings.TrimSpace(audit.ActorID) == "" ||
		audit.Action != "connector_task.resolve_execution" || audit.RiskLevel != contracts.RiskLevelL3 ||
		audit.EventLevel != contracts.EventLevelCritical || audit.TargetType != "connector_task" || audit.TargetID != task.ID ||
		audit.Result != string(req.Resolution) || audit.ErrorMessage != "" || audit.Details == nil ||
		audit.Details["resolution"] != string(req.Resolution) || audit.Details["evidence_note"] != strings.TrimSpace(req.EvidenceNote) ||
		audit.Details["connector_id"] != task.ConnectorID ||
		audit.Details["lease_nonce_sha256"] != ConnectorTaskLeaseNonceAuditHash(task.LeaseNonce) {
		return false
	}
	for key, value := range audit.Details {
		if key != "resolution" && key != "evidence_note" && key != "connector_id" && key != "lease_nonce_sha256" {
			return false
		}
		if contracts.LooksLikeConnectorSensitiveValue(value) {
			return false
		}
	}
	return true
}
