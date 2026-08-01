package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

type recommendationExecutionPolicyWrite struct {
	UserID            int64                                  `json:"user_id"`
	Scope             contracts.RecommendationExecutionScope `json:"scope"`
	PlanID            string                                 `json:"plan_id,omitempty"`
	PoolID            string                                 `json:"pool_id,omitempty"`
	Enabled           bool                                   `json:"enabled"`
	KillSwitch        bool                                   `json:"kill_switch"`
	DailyExecutionCap int                                    `json:"daily_execution_cap"`
	CooldownSeconds   int                                    `json:"cooldown_seconds"`
	MinimumSavings    contracts.CanonicalDecimal             `json:"minimum_savings"`
	ExpectedVersion   int64                                  `json:"expected_version"`
}

func (s *Server) handleListRecommendationExecutionPolicies(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	reader, ok := s.store.(store.RecommendationExecutionPolicyStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "recommendation_execution_disabled", "recommendation execution policy store is disabled")
		return
	}
	userID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("user_id")), 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id is required")
		return
	}
	policies, err := reader.ListRecommendationExecutionPolicies(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "execution policies could not be read")
		return
	}
	if policies == nil {
		policies = []contracts.RecommendationExecutionPolicy{}
	}
	writeJSON(w, http.StatusOK, policies)
}

func (s *Server) handleUpsertRecommendationExecutionPolicy(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	writer, ok := s.store.(store.RecommendationExecutionPolicyStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "recommendation_execution_disabled", "recommendation execution policy store is disabled")
		return
	}
	var request recommendationExecutionPolicyWrite
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if err := decodeStrictJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid execution policy request")
		return
	}
	if !s.validateRecommendationExecutionPolicyTarget(w, r, request) {
		return
	}
	input := contracts.RecommendationExecutionPolicy{
		UserID: request.UserID, Scope: request.Scope, PlanID: strings.TrimSpace(request.PlanID), PoolID: strings.TrimSpace(request.PoolID),
		Enabled: request.Enabled, KillSwitch: request.KillSwitch, DailyExecutionCap: request.DailyExecutionCap,
		CooldownSeconds: request.CooldownSeconds, MinimumSavings: request.MinimumSavings,
	}
	saved, err := writer.UpsertRecommendationExecutionPolicy(r.Context(), input, request.ExpectedVersion)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "version_conflict", "execution policy changed; refresh and retry")
			return
		}
		if errors.Is(err, store.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "validation_failed", "execution policy is invalid")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "execution policy could not be saved")
		return
	}
	s.auditUpstream(r, saved.UserID, "recommendation_execution_policy.upsert", "recommendation_execution_policy", saved.ID)
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) validateRecommendationExecutionPolicyTarget(w http.ResponseWriter, r *http.Request, request recommendationExecutionPolicyWrite) bool {
	if request.UserID <= 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id is required")
		return false
	}
	switch request.Scope {
	case contracts.RecommendationExecutionScopePlan:
		if strings.TrimSpace(request.PlanID) == "" || strings.TrimSpace(request.PoolID) != "" {
			writeError(w, http.StatusBadRequest, "validation_failed", "plan scope requires only plan_id")
			return false
		}
		plan, err := s.store.GetRoutePlan(r.Context(), strings.TrimSpace(request.PlanID))
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "route plan not found")
			return false
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", "route plan could not be read")
			return false
		}
		if plan.UserID != request.UserID {
			writeError(w, http.StatusForbidden, "owner_scope_mismatch", "route plan does not belong to user")
			return false
		}
	case contracts.RecommendationExecutionScopePool:
		if strings.TrimSpace(request.PoolID) == "" || strings.TrimSpace(request.PlanID) != "" {
			writeError(w, http.StatusBadRequest, "validation_failed", "pool scope requires only pool_id")
			return false
		}
		rolloutStore, ok := s.store.(store.PoolRolloutStore)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "pool_rollout_disabled", "pool rollout authorization is unavailable")
			return false
		}
		targets, err := rolloutStore.ListPoolRolloutTargets(r.Context(), strings.TrimSpace(request.PoolID))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", "pool rollout authorization could not be read")
			return false
		}
		for _, target := range targets {
			if target.UserID == request.UserID && target.Enabled {
				return true
			}
		}
		writeError(w, http.StatusForbidden, "owner_scope_mismatch", "pool is not enabled for user")
		return false
	default:
		writeError(w, http.StatusBadRequest, "validation_failed", "scope must be plan or pool")
		return false
	}
	return true
}
