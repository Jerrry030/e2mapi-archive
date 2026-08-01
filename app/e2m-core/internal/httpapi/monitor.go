package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/auth"
	"e2m.local/core/internal/store"
)

type updateMonitorPolicyRequest struct {
	Enabled              bool `json:"enabled"`
	CheckIntervalSeconds int  `json:"check_interval_seconds"`
	FailStreak           int  `json:"fail_streak"`
	AutoSwitch           bool `json:"auto_switch"`
	CooldownSeconds      int  `json:"cooldown_seconds"`
	DriftDetection       bool `json:"drift_detection"`
}

func validateMonitorPolicy(policy contracts.InstanceMonitorPolicy) bool {
	validInterval := policy.CheckIntervalSeconds == 30 || policy.CheckIntervalSeconds == 60 || policy.CheckIntervalSeconds == 300
	validCooldown := policy.CooldownSeconds == 300 || policy.CooldownSeconds == 900 || policy.CooldownSeconds == 1800
	return validInterval && policy.FailStreak >= 1 && policy.FailStreak <= 5 && validCooldown
}

func (s *Server) handleGetInstanceMonitorPolicy(w http.ResponseWriter, r *http.Request) {
	instance, ok := s.instanceForRead(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	policy, err := s.store.GetInstanceMonitorPolicy(r.Context(), instance.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func (s *Server) handleUpdateInstanceMonitorPolicy(w http.ResponseWriter, r *http.Request) {
	instance, ok := s.instanceForWrite(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var input updateMonitorPolicyRequest
	if err := decodeStrictJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	policy := contracts.InstanceMonitorPolicy{
		InstanceID:           instance.ID,
		UserID:               instance.UserID,
		Enabled:              input.Enabled,
		CheckIntervalSeconds: input.CheckIntervalSeconds,
		FailStreak:           input.FailStreak,
		AutoSwitch:           input.AutoSwitch,
		CooldownSeconds:      input.CooldownSeconds,
		DriftDetection:       input.DriftDetection,
	}
	if !validateMonitorPolicy(policy) {
		writeError(w, http.StatusBadRequest, "validation_failed", "monitor policy values are outside the supported presets")
		return
	}
	updated, err := s.store.UpsertInstanceMonitorPolicy(r.Context(), policy)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "instance not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: instance.UserID, InstanceID: instance.ID,
		ActorType: "user", ActorID: actor.Email,
		Action: "instance.monitor_policy.update", RiskLevel: contracts.RiskLevelL1,
		TargetType: "instance", TargetID: instance.ID, Result: "accepted",
	})
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleCheckInstanceHealthNow(w http.ResponseWriter, r *http.Request) {
	instance, ok := s.instanceForWrite(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	controller, ok := s.health.(HealthController)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "health_unavailable", "health checker is not configured")
		return
	}
	snapshot, err := controller.CheckNow(r.Context(), instance.ID)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusGatewayTimeout, "health_check_timeout", "health check did not finish")
			return
		}
		if strings.Contains(err.Error(), "already running") {
			writeError(w, http.StatusConflict, "health_check_running", "an instance health check is already running")
			return
		}
		s.writeOrchError(w, err)
		return
	}
	if !auth.IsPlatformAdmin(currentUser(r)) {
		managed, err := s.managedRemoteIDs(r.Context(), instanceIDSet(instance.ID))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", "managed account classification is temporarily unavailable")
			return
		}
		snapshot = filterManagedHealthSnapshot(snapshot, managed[instance.ID])
	}
	writeJSON(w, http.StatusOK, snapshot)
}
