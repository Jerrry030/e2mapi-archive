package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

type poolRolloutTargetRequest struct {
	Scope              contracts.PoolRolloutScope `json:"scope"`
	UserID             int64                      `json:"user_id"`
	InstanceID         string                     `json:"instance_id,omitempty"`
	Enabled            bool                       `json:"enabled"`
	Rollout            contracts.RolloutMode      `json:"rollout"`
	RolloutBatchSize   int                        `json:"rollout_batch_size,omitempty"`
	RolloutCanaryCount int                        `json:"rollout_canary_count,omitempty"`
	Note               string                     `json:"note,omitempty"`
}

type poolRolloutPreview struct {
	PoolID     string                            `json:"pool_id"`
	Targets    []contracts.PoolRolloutTarget     `json:"targets"`
	Instances  []contracts.PoolRolloutResolution `json:"instances"`
	Operations []contracts.PoolRolloutOperation  `json:"operations"`
}

func (s *Server) poolRolloutStore(w http.ResponseWriter) (store.PoolRolloutStore, bool) {
	rollout, ok := store.AsPoolRolloutStore(s.store)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "pool_rollout_unavailable", "pool rollout persistence is unavailable")
	}
	return rollout, ok
}

func (s *Server) handleListPoolRolloutTargets(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	rolloutStore, ok := s.poolRolloutStore(w)
	if !ok {
		return
	}
	poolID := strings.TrimSpace(r.PathValue("id"))
	if _, err := s.store.GetUpstreamPool(r.Context(), poolID); err != nil {
		writePoolRolloutStoreError(w, err)
		return
	}
	targets, err := rolloutStore.ListPoolRolloutTargets(r.Context(), poolID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	instances, err := s.store.ListInstances(r.Context(), 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	operations, err := rolloutStore.ListPoolRolloutOperations(r.Context(), poolID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	preview := poolRolloutPreview{PoolID: poolID, Targets: targets, Instances: make([]contracts.PoolRolloutResolution, 0, len(instances)), Operations: operations}
	if preview.Targets == nil {
		preview.Targets = []contracts.PoolRolloutTarget{}
	}
	for _, instance := range instances {
		resolution, resolveErr := rolloutStore.ResolvePoolRollout(r.Context(), poolID, instance.UserID, instance.ID)
		if resolveErr != nil {
			writeError(w, http.StatusInternalServerError, "store_error", resolveErr.Error())
			return
		}
		preview.Instances = append(preview.Instances, resolution)
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) handleUpsertPoolRolloutTarget(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	rolloutStore, ok := s.poolRolloutStore(w)
	if !ok {
		return
	}
	poolID := strings.TrimSpace(r.PathValue("id"))
	if _, err := s.store.GetUpstreamPool(r.Context(), poolID); err != nil {
		writePoolRolloutStoreError(w, err)
		return
	}
	var request poolRolloutTargetRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	request.InstanceID = strings.TrimSpace(request.InstanceID)
	if !request.Scope.Valid() || request.UserID <= 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "scope and user_id are required")
		return
	}
	user, err := s.store.GetUser(r.Context(), request.UserID)
	if err != nil {
		writePoolRolloutStoreError(w, err)
		return
	}
	if !user.Enabled || !userHasRole(user.Roles, contracts.UserRoleClient) {
		writeError(w, http.StatusBadRequest, "validation_failed", "target user must be an enabled client")
		return
	}
	if request.Scope == contracts.PoolRolloutScopeInstance {
		instance, instanceErr := s.store.GetInstance(r.Context(), request.InstanceID)
		if instanceErr != nil {
			writePoolRolloutStoreError(w, instanceErr)
			return
		}
		if instance.UserID != request.UserID {
			writeError(w, http.StatusBadRequest, "validation_failed", "instance does not belong to user_id")
			return
		}
	}
	target, err := rolloutStore.UpsertPoolRolloutTarget(r.Context(), contracts.PoolRolloutTarget{
		PoolID: poolID, Scope: request.Scope, UserID: request.UserID,
		InstanceID: request.InstanceID, Enabled: request.Enabled, Rollout: request.Rollout,
		RolloutBatchSize: request.RolloutBatchSize, RolloutCanaryCount: request.RolloutCanaryCount,
		Note: request.Note,
	})
	if err != nil {
		writePoolRolloutStoreError(w, err)
		return
	}
	if _, err := rolloutStore.EnsurePoolRolloutOperations(r.Context(), poolID); err != nil {
		writeError(w, http.StatusInternalServerError, "rollout_operation_failed", err.Error())
		return
	}
	s.auditPoolRollout(r, target, "pool_rollout_target.upsert")
	writeJSON(w, http.StatusOK, target)
}

func (s *Server) handleDeletePoolRolloutTarget(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	rolloutStore, ok := s.poolRolloutStore(w)
	if !ok {
		return
	}
	poolID := strings.TrimSpace(r.PathValue("id"))
	scope := contracts.PoolRolloutScope(strings.TrimSpace(r.URL.Query().Get("scope")))
	userID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("user_id")), 10, 64)
	if err != nil || userID <= 0 || !scope.Valid() {
		writeError(w, http.StatusBadRequest, "validation_failed", "valid scope and user_id are required")
		return
	}
	instanceID := strings.TrimSpace(r.URL.Query().Get("instance_id"))
	before, _ := rolloutStore.ResolvePoolRollout(r.Context(), poolID, userID, instanceID)
	if err := rolloutStore.DeletePoolRolloutTarget(r.Context(), poolID, scope, userID, instanceID); err != nil {
		writePoolRolloutStoreError(w, err)
		return
	}
	if _, err := rolloutStore.EnsurePoolRolloutOperations(r.Context(), poolID); err != nil {
		writeError(w, http.StatusInternalServerError, "rollout_operation_failed", err.Error())
		return
	}
	target := contracts.PoolRolloutTarget{
		ID: before.TargetID, PoolID: poolID, Scope: scope, UserID: userID, InstanceID: instanceID,
	}
	s.auditPoolRollout(r, target, "pool_rollout_target.delete")
	w.WriteHeader(http.StatusNoContent)
}

func writePoolRolloutStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "pool rollout target was not found")
	case errors.Is(err, store.ErrInvalid), errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusBadRequest, "validation_failed", "invalid pool rollout target")
	default:
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
	}
}

func userHasRole(roles []contracts.UserRole, want contracts.UserRole) bool {
	for _, role := range roles {
		if role == want {
			return true
		}
	}
	return false
}

func (s *Server) auditPoolRollout(r *http.Request, target contracts.PoolRolloutTarget, action string) {
	actor := currentUser(r)
	details := map[string]string{
		"pool_id": target.PoolID, "scope": string(target.Scope),
		"user_id": strconv.FormatInt(target.UserID, 10), "enabled": strconv.FormatBool(target.Enabled),
		"rollout": string(target.Rollout),
	}
	if target.InstanceID != "" {
		details["instance_id"] = target.InstanceID
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: target.UserID, InstanceID: target.InstanceID,
		ActorType: "user", ActorID: actor.Email, Action: action,
		RiskLevel: contracts.RiskLevelL1, EventLevel: contracts.EventLevelNotice,
		TargetType: "pool_rollout_target", TargetID: target.ID,
		Result: "accepted", Details: details,
	})
}
