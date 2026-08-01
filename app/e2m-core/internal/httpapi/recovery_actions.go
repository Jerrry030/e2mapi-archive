package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// RecoveryActionController is the operator-only mutation surface implemented
// by autoswitch.Orchestrator. It stays separate from the legacy evaluate/read
// interface so alternate deployments can opt into these actions explicitly.
type RecoveryActionController interface {
	Approve(ctx context.Context, decisionID, note string) (*contracts.AutoSwitchDecision, error)
	Reject(ctx context.Context, decisionID, note string) (*contracts.AutoSwitchDecision, error)
	Execute(ctx context.Context, decisionID string) (*contracts.AutoSwitchDecision, error)
	ManualRecover(ctx context.Context, planID, channelID, note string) (contracts.QualityCircuitRuntime, error)
}

type operatorNoteRequest struct {
	Note string `json:"note"`
}

func decodeOptionalOperatorNote(w http.ResponseWriter, r *http.Request) (operatorNoteRequest, bool) {
	var input operatorNoteRequest
	if r.Body != nil && r.ContentLength != 0 {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return operatorNoteRequest{}, false
		}
	}
	input.Note = strings.TrimSpace(input.Note)
	if len([]rune(input.Note)) > 500 {
		writeError(w, http.StatusBadRequest, "validation_failed", "note must not exceed 500 characters")
		return operatorNoteRequest{}, false
	}
	return input, true
}

func (s *Server) recoveryActions(w http.ResponseWriter, r *http.Request) (RecoveryActionController, bool) {
	if !requirePlatformAdmin(w, r) {
		return nil, false
	}
	controller, ok := s.autoswitch.(RecoveryActionController)
	if !ok || controller == nil {
		writeError(w, http.StatusServiceUnavailable, "autoswitch_disabled", "recovery action controller is not enabled")
		return nil, false
	}
	return controller, true
}

func (s *Server) handleApproveAutoSwitchDecision(w http.ResponseWriter, r *http.Request) {
	controller, ok := s.recoveryActions(w, r)
	if !ok {
		return
	}
	input, ok := decodeOptionalOperatorNote(w, r)
	if !ok {
		return
	}
	decision, err := controller.Approve(r.Context(), r.PathValue("id"), input.Note)
	if err != nil {
		s.writeRecoveryActionError(w, err)
		return
	}
	s.auditRecoveryAction(r, *decision, "auto_switch.approve", "accepted", "")
	writeJSON(w, http.StatusOK, decision)
}

func (s *Server) handleRejectAutoSwitchDecision(w http.ResponseWriter, r *http.Request) {
	controller, ok := s.recoveryActions(w, r)
	if !ok {
		return
	}
	input, ok := decodeOptionalOperatorNote(w, r)
	if !ok {
		return
	}
	decision, err := controller.Reject(r.Context(), r.PathValue("id"), input.Note)
	if err != nil {
		s.writeRecoveryActionError(w, err)
		return
	}
	s.auditRecoveryAction(r, *decision, "auto_switch.reject", "rejected", "")
	writeJSON(w, http.StatusOK, decision)
}

func (s *Server) handleExecuteAutoSwitchDecision(w http.ResponseWriter, r *http.Request) {
	controller, ok := s.recoveryActions(w, r)
	if !ok {
		return
	}
	if r.ContentLength > 0 {
		if _, ok := decodeOptionalOperatorNote(w, r); !ok {
			return
		}
	}
	decision, err := controller.Execute(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeRecoveryActionError(w, err)
		return
	}
	result := "accepted"
	if decision.Status == contracts.AutoSwitchFailed {
		result = "failed"
	}
	s.auditRecoveryAction(r, *decision, "auto_switch.execute", result, decision.Error)
	writeJSON(w, http.StatusOK, decision)
}

func (s *Server) handleManualRecoverQualityCircuit(w http.ResponseWriter, r *http.Request) {
	controller, ok := s.recoveryActions(w, r)
	if !ok {
		return
	}
	planID, channelID := r.PathValue("id"), r.PathValue("channelId")
	plan, err := s.store.GetRoutePlan(r.Context(), planID)
	if err != nil {
		s.writeRecoveryActionError(w, err)
		return
	}
	input, ok := decodeOptionalOperatorNote(w, r)
	if !ok {
		return
	}
	runtime, err := controller.ManualRecover(r.Context(), planID, channelID, input.Note)
	if err != nil {
		s.auditManualRecoveryAction(r, plan, planID, channelID, "failed", "", err.Error())
		s.writeRecoveryActionError(w, err)
		return
	}
	s.auditManualRecoveryAction(r, plan, planID, channelID, "accepted", runtime.LastReason.Code, "")
	writeJSON(w, http.StatusOK, runtime)
}

func (s *Server) auditManualRecoveryAction(
	r *http.Request,
	plan contracts.RoutePlan,
	planID, channelID, result, reasonCode, errMsg string,
) {
	actor := currentUser(r)
	eventLevel := contracts.EventLevelNotice
	if result == "failed" {
		eventLevel = contracts.EventLevelWarning
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: plan.UserID, InstanceID: plan.InstanceID,
		ActorType: "user", ActorID: actor.Email,
		Action: "quality_circuit.manual_recover", RiskLevel: contracts.RiskLevelL2,
		EventLevel: eventLevel, TargetType: "quality_circuit",
		TargetID: planID + "/" + channelID, Result: result, ErrorMessage: errMsg,
		Details: map[string]string{"reason_code": reasonCode},
	})
}

func (s *Server) auditRecoveryAction(r *http.Request, decision contracts.AutoSwitchDecision, action, result, errMsg string) {
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: decision.UserID, InstanceID: decision.InstanceID,
		ActorType: "user", ActorID: actor.Email,
		Action: action, RiskLevel: decision.RiskLevel, EventLevel: contracts.DefaultEventLevel(decision.RiskLevel, result),
		TargetType: "auto_switch_decision", TargetID: decision.ID, Result: result, ErrorMessage: errMsg,
		Details: map[string]string{"status": string(decision.Status)},
	})
}

func (s *Server) writeRecoveryActionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "recovery target not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "stale_recovery_action", "recovery state changed; refresh and retry")
	default:
		writeError(w, http.StatusInternalServerError, "recovery_action_failed", err.Error())
	}
}
