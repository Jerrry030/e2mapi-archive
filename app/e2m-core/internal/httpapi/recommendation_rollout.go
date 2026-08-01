package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/recommendationrollout"
)

type recommendationRolloutGateResponse struct {
	Status      contracts.RecommendationRolloutGateStatus    `json:"status"`
	ReasonCodes []contracts.RecommendationRolloutBlockReason `json:"reason_codes"`
}

type recommendationRolloutOperationResponse struct {
	ID          string                                            `json:"id"`
	Action      contracts.RecommendationRolloutOperationAction    `json:"action"`
	TargetStage contracts.RecommendationRolloutStage              `json:"target_stage"`
	Status      contracts.RecommendationRolloutOperationStatus    `json:"status"`
	Attempts    int                                               `json:"attempts"`
	ErrorCode   contracts.RecommendationRolloutOperationErrorCode `json:"error_code,omitempty"`
	CreatedAt   time.Time                                         `json:"created_at"`
	UpdatedAt   time.Time                                         `json:"updated_at"`
}

type recommendationRolloutResponse struct {
	ID                        string                                       `json:"id"`
	RecommendationID          string                                       `json:"recommendation_id"`
	RecommendationFingerprint string                                       `json:"recommendation_fingerprint"`
	PlanID                    string                                       `json:"plan_id"`
	FactVersion               int64                                        `json:"fact_version"`
	EvidenceIDs               []string                                     `json:"evidence_ids"`
	AccountCount              int                                          `json:"account_count"`
	BaselineFingerprint       string                                       `json:"baseline_fingerprint"`
	BaselineVerified          bool                                         `json:"baseline_verified"`
	SchedulingGeneration      int64                                        `json:"scheduling_generation"`
	Status                    contracts.RecommendationRolloutStatus        `json:"status"`
	Stage                     contracts.RecommendationRolloutStage         `json:"stage"`
	PendingStage              contracts.RecommendationRolloutStage         `json:"pending_stage"`
	ObserveUntil              *time.Time                                   `json:"observe_until,omitempty"`
	RecommendationExpiresAt   time.Time                                    `json:"recommendation_expires_at"`
	RollbackReasons           []contracts.RecommendationRolloutBlockReason `json:"rollback_reasons"`
	Gate                      recommendationRolloutGateResponse            `json:"gate"`
	LatestOperation           *recommendationRolloutOperationResponse      `json:"latest_operation,omitempty"`
	LastAfterVerified         bool                                         `json:"last_after_verified"`
	RollbackVerified          bool                                         `json:"rollback_verified"`
	StartedAt                 time.Time                                    `json:"started_at"`
	UpdatedAt                 time.Time                                    `json:"updated_at"`
}

func (s *Server) handleListRecommendationRollouts(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	userID, filter, ok := s.parseRecommendationRolloutQuery(w, r, true)
	if !ok {
		return
	}
	controller, ok := s.recommendationRolloutController(w)
	if !ok {
		return
	}
	values, err := controller.List(r.Context(), filter)
	if err != nil {
		writeRecommendationRolloutError(w, err)
		return
	}
	response := make([]recommendationRolloutResponse, 0, len(values))
	for _, value := range values {
		if value.State.UserID != userID {
			writeError(w, http.StatusConflict, "owner_scope_mismatch", "rollout owner scope is inconsistent")
			return
		}
		current, operations, err := controller.Get(r.Context(), value.State.ID)
		if err != nil {
			writeRecommendationRolloutError(w, err)
			return
		}
		if current.State.ID != value.State.ID || current.State.UserID != userID {
			writeError(w, http.StatusConflict, "owner_scope_mismatch", "rollout owner scope is inconsistent")
			return
		}
		response = append(response, projectRecommendationRollout(current, operations))
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleGetRecommendationRollout(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	userID, _, ok := s.parseRecommendationRolloutQuery(w, r, false)
	if !ok || !validRecommendationRolloutPathID(w, r, "rollout") {
		return
	}
	controller, ok := s.recommendationRolloutController(w)
	if !ok {
		return
	}
	value, operations, err := controller.Get(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeRecommendationRolloutError(w, err)
		return
	}
	// Get intentionally accepts only an opaque rollout id, so scope again after
	// lookup. Return 404, not a cross-owner existence oracle.
	if value.State.ID != strings.TrimSpace(r.PathValue("id")) || value.State.UserID != userID {
		writeError(w, http.StatusNotFound, "not_found", "recommendation rollout not found")
		return
	}
	writeJSON(w, http.StatusOK, projectRecommendationRollout(value, operations))
}

func (s *Server) handleStartRecommendationRollout(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	userID, _, ok := s.parseRecommendationRolloutQuery(w, r, false)
	if !ok || !validRecommendationRolloutPathID(w, r, "recommendation") || !decodeEmptyRecommendationAction(w, r) {
		return
	}
	controller, ok := s.recommendationRolloutController(w)
	if !ok {
		return
	}
	result, err := controller.Start(r.Context(), userID, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeRecommendationRolloutError(w, err)
		return
	}
	value := result.Rollout
	if value.State.UserID != userID {
		writeError(w, http.StatusConflict, "owner_scope_mismatch", "rollout owner scope is inconsistent")
		return
	}
	s.auditUpstream(r, userID, "upstream_recommendation.rollout.start", "recommendation_rollout", value.State.ID)
	writeJSON(w, http.StatusCreated, projectRecommendationRolloutMutation(result))
}

func (s *Server) handleAdvanceRecommendationRollout(w http.ResponseWriter, r *http.Request) {
	s.handleRecommendationRolloutMutation(w, r, "upstream_recommendation.rollout.advance", func(controller RecommendationRolloutController, userID int64, id string) (recommendationrollout.MutationResult, error) {
		return controller.Advance(r.Context(), userID, id)
	})
}

func (s *Server) handleRollbackRecommendationRollout(w http.ResponseWriter, r *http.Request) {
	s.handleRecommendationRolloutMutation(w, r, "upstream_recommendation.rollout.rollback", func(controller RecommendationRolloutController, userID int64, id string) (recommendationrollout.MutationResult, error) {
		return controller.Rollback(r.Context(), userID, id)
	})
}

func (s *Server) handleRecommendationRolloutMutation(w http.ResponseWriter, r *http.Request, auditAction string, mutate func(RecommendationRolloutController, int64, string) (recommendationrollout.MutationResult, error)) {
	setNoStore(w)
	userID, _, ok := s.parseRecommendationRolloutQuery(w, r, false)
	if !ok || !validRecommendationRolloutPathID(w, r, "rollout") || !decodeEmptyRecommendationAction(w, r) {
		return
	}
	controller, ok := s.recommendationRolloutController(w)
	if !ok {
		return
	}
	result, err := mutate(controller, userID, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeRecommendationRolloutError(w, err)
		return
	}
	value := result.Rollout
	if value.State.UserID != userID {
		writeError(w, http.StatusConflict, "owner_scope_mismatch", "rollout owner scope is inconsistent")
		return
	}
	s.auditUpstream(r, userID, auditAction, "recommendation_rollout", value.State.ID)
	writeJSON(w, http.StatusOK, projectRecommendationRolloutMutation(result))
}

func projectRecommendationRolloutMutation(result recommendationrollout.MutationResult) recommendationRolloutResponse {
	if result.Operation == nil {
		return projectRecommendationRollout(result.Rollout, nil)
	}
	return projectRecommendationRollout(result.Rollout, []contracts.RecommendationRolloutOperation{*result.Operation})
}

func (s *Server) recommendationRolloutController(w http.ResponseWriter) (RecommendationRolloutController, bool) {
	if s.recommendationRollouts == nil {
		writeError(w, http.StatusServiceUnavailable, "recommendation_rollouts_disabled", "recommendation rollout controller is not enabled")
		return nil, false
	}
	return s.recommendationRollouts, true
}

func (s *Server) parseRecommendationRolloutQuery(w http.ResponseWriter, r *http.Request, list bool) (int64, contracts.RecommendationRolloutFilter, bool) {
	if !requirePlatformAdmin(w, r) {
		return 0, contracts.RecommendationRolloutFilter{}, false
	}
	allowed := upstreamIntelligenceStringSet("user_id")
	enums := map[string]map[string]bool{}
	positive := map[string]bool{}
	if list {
		allowed["status"], allowed["plan_id"], allowed["limit"] = true, true, true
		statuses := make(map[string]bool)
		for _, status := range []contracts.RecommendationRolloutStatus{
			contracts.RecommendationRolloutReady, contracts.RecommendationRolloutApplying, contracts.RecommendationRolloutObserving,
			contracts.RecommendationRolloutRollbackRequired, contracts.RecommendationRolloutCompleted,
			contracts.RecommendationRolloutRolledBack, contracts.RecommendationRolloutBlocked,
		} {
			statuses[string(status)] = true
		}
		enums["status"], positive["limit"] = statuses, true
	}
	query, ok := parseUpstreamIntelligenceQuery(w, r, allowed, enums, positive)
	if !ok {
		return 0, contracts.RecommendationRolloutFilter{}, false
	}
	if _, ok := s.scopeOwnerUser(w, r, strconv.FormatInt(query.userID, 10)); !ok {
		return 0, contracts.RecommendationRolloutFilter{}, false
	}
	filter := contracts.RecommendationRolloutFilter{UserID: query.userID}
	if list {
		filter.Status = contracts.RecommendationRolloutStatus(query.values["status"])
		filter.PlanID = strings.TrimSpace(query.values["plan_id"])
		if filter.PlanID != "" && !validUpstreamIntelligenceWireIdentifier(filter.PlanID, 256) {
			writeError(w, http.StatusBadRequest, "validation_failed", "plan_id is invalid")
			return 0, contracts.RecommendationRolloutFilter{}, false
		}
		if raw := query.values["limit"]; raw != "" {
			filter.Limit, _ = strconv.Atoi(raw)
		}
	}
	return query.userID, filter, true
}

func validRecommendationRolloutPathID(w http.ResponseWriter, r *http.Request, subject string) bool {
	if !validUpstreamIntelligenceWireIdentifier(strings.TrimSpace(r.PathValue("id")), 256) {
		writeError(w, http.StatusBadRequest, "validation_failed", subject+" id is invalid")
		return false
	}
	return true
}

func projectRecommendationRollout(value contracts.RecommendationRollout, operations []contracts.RecommendationRolloutOperation) recommendationRolloutResponse {
	baselineFingerprint, baselineErr := contracts.RecommendationRolloutBaselineFingerprint(value.BaselineWeights)
	response := recommendationRolloutResponse{
		ID: value.State.ID, RecommendationID: value.State.RecommendationID,
		RecommendationFingerprint: value.State.RecommendationFingerprint, PlanID: value.State.PlanID,
		FactVersion: value.State.FactVersion, EvidenceIDs: append([]string{}, value.State.EvidenceIDs...),
		AccountCount: len(value.BaselineWeights), BaselineFingerprint: value.State.BaselineFingerprint,
		BaselineVerified:     baselineErr == nil && baselineFingerprint == value.State.BaselineFingerprint,
		SchedulingGeneration: value.State.SchedulingGeneration, Status: value.State.Status,
		Stage: value.State.Stage, PendingStage: value.State.PendingStage, ObserveUntil: cloneHTTPTime(value.State.ObserveUntil),
		RecommendationExpiresAt: value.State.RecommendationExpiresAt,
		RollbackReasons:         append([]contracts.RecommendationRolloutBlockReason{}, value.State.RollbackReasons...),
		Gate:                    projectRecommendationRolloutGate(value),
		LastAfterVerified:       recommendationLastAfterVerified(value),
		RollbackVerified:        recommendationRollbackVerified(value), StartedAt: value.State.StartedAt, UpdatedAt: value.State.UpdatedAt,
	}
	if latest := latestRecommendationRolloutOperation(value, operations); latest != nil {
		response.LatestOperation = projectRecommendationRolloutOperation(*latest)
	}
	return response
}

func projectRecommendationRolloutGate(value contracts.RecommendationRollout) recommendationRolloutGateResponse {
	status := contracts.RecommendationRolloutGateUnknown
	reasons := []contracts.RecommendationRolloutBlockReason{}
	if (value.State.Status == contracts.RecommendationRolloutRollbackRequired || value.State.Status == contracts.RecommendationRolloutBlocked) && len(value.State.RollbackReasons) > 0 {
		status = contracts.RecommendationRolloutGateBlocked
		reasons = append(reasons, value.State.RollbackReasons...)
	}
	// Individual forward gates are not durable state. Reporting them as passed
	// after their observation instant would overstate freshness, so the public
	// projection remains unknown unless a durable block reason exists.
	return recommendationRolloutGateResponse{Status: status, ReasonCodes: reasons}
}

func latestRecommendationRolloutOperation(value contracts.RecommendationRollout, operations []contracts.RecommendationRolloutOperation) *contracts.RecommendationRolloutOperation {
	for index := range operations {
		if operations[index].ID == value.LastOperationID {
			copy := operations[index]
			return &copy
		}
	}
	if len(operations) > 0 {
		copy := operations[0]
		return &copy
	}
	return nil
}

func projectRecommendationRolloutOperation(value contracts.RecommendationRolloutOperation) *recommendationRolloutOperationResponse {
	return &recommendationRolloutOperationResponse{
		ID: value.ID, Action: value.Action, TargetStage: value.TargetStage, Status: value.Status,
		Attempts: value.Attempts, ErrorCode: value.ErrorCode, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func recommendationRollbackVerified(value contracts.RecommendationRollout) bool {
	after := value.State.LastAfterEvidence
	if value.State.Status != contracts.RecommendationRolloutRolledBack || value.State.Stage != contracts.RecommendationRolloutStageNone ||
		value.State.PendingStage != contracts.RecommendationRolloutStageNone || after == nil ||
		after.Stage != contracts.RecommendationRolloutStageNone || after.RecommendationFingerprint != value.State.RecommendationFingerprint ||
		after.BaselineFingerprint == "" || after.BaselineFingerprint != value.State.BaselineFingerprint ||
		after.SchedulingGeneration != value.State.SchedulingGeneration || after.Callability != contracts.RecommendationRolloutGateUnknown ||
		after.Quality != contracts.RecommendationRolloutGateUnknown || after.ObservedAt.IsZero() || !after.FreshUntil.After(after.ObservedAt) {
		return false
	}
	return len(after.EvidenceIDs) == 1 && after.EvidenceIDs[0] == "weight-set-sha256:"+value.State.BaselineFingerprint
}

func recommendationLastAfterVerified(value contracts.RecommendationRollout) bool {
	after := value.State.LastAfterEvidence
	// Rollback read-back is scheduling proof, not a health observation. Its
	// callability and quality are intentionally unknown and are projected only
	// through rollback_verified above.
	if after == nil || after.Stage == contracts.RecommendationRolloutStageNone ||
		after.SchedulingGeneration != value.State.SchedulingGeneration || after.Callability != contracts.RecommendationRolloutGatePassed ||
		after.Quality != contracts.RecommendationRolloutGatePassed || after.ObservedAt.IsZero() || !after.FreshUntil.After(after.ObservedAt) {
		return false
	}
	return after.Stage == value.State.Stage && after.RecommendationFingerprint == value.State.RecommendationFingerprint
}

func cloneHTTPTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func writeRecommendationRolloutError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, recommendationrollout.ErrControllerNotFound):
		writeError(w, http.StatusNotFound, "not_found", "recommendation rollout not found")
	case errors.Is(err, recommendationrollout.ErrControllerInvalid):
		writeError(w, http.StatusBadRequest, "validation_failed", "recommendation rollout request is invalid")
	case errors.Is(err, recommendationrollout.ErrControllerConflict):
		writeError(w, http.StatusConflict, "state_conflict", "recommendation rollout changed; refresh and retry")
	case errors.Is(err, recommendationrollout.ErrControllerBlocked):
		writeError(w, http.StatusConflict, "rollout_blocked", "recommendation rollout safety gates blocked the action")
	case errors.Is(err, recommendationrollout.ErrControllerUnavailable):
		writeError(w, http.StatusServiceUnavailable, "recommendation_rollout_unavailable", "recommendation rollout dependency is unavailable")
	default:
		writeError(w, http.StatusInternalServerError, "rollout_error", "recommendation rollout action failed")
	}
}
