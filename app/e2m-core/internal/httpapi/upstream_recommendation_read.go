package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// Browser response DTOs intentionally omit owner identity, Connector-local
// fields, gateway remote IDs and raw reconcile details. Experiments are built
// from a trusted server snapshot; the browser cannot submit candidates.
type upstreamRecommendationResponse struct {
	ID     string                                 `json:"id"`
	Status contracts.UpstreamRecommendationStatus `json:"status"`

	IntelligenceFactVersion int64 `json:"intelligence_fact_version"`
	CostLedgerFactVersion   int64 `json:"cost_ledger_fact_version"`
	LinkFactVersion         int64 `json:"link_fact_version"`
	PlanGeneration          int64 `json:"plan_generation"`

	FromSourceID  string `json:"from_source_id"`
	FromChannelID string `json:"from_channel_id"`
	FromGroupKey  string `json:"from_group_key"`
	ToSourceID    string `json:"to_source_id"`
	ToChannelID   string `json:"to_channel_id"`
	ToGroupKey    string `json:"to_group_key"`
	ModelKey      string `json:"model_key"`

	PriceDimension     contracts.UpstreamPriceDimension             `json:"price_dimension"`
	SettlementCurrency string                                       `json:"settlement_currency"`
	PerTokens          int64                                        `json:"per_tokens"`
	AffectedPlanIDs    []string                                     `json:"affected_plan_ids"`
	EvidenceIDs        []string                                     `json:"evidence_ids"`
	Constraints        []contracts.UpstreamRecommendationConstraint `json:"constraints"`
	FromCost           contracts.UpstreamRecommendationCostRange    `json:"from_cost"`
	ToCost             contracts.UpstreamRecommendationCostRange    `json:"to_cost"`
	Savings            contracts.UpstreamRecommendationSavingsRange `json:"savings"`
	FormulaVersion     string                                       `json:"formula_version"`
	StrategyVersion    string                                       `json:"strategy_version"`
	Fingerprint        string                                       `json:"fingerprint"`
	DryRunID           string                                       `json:"dry_run_id,omitempty"`
	CreatedAt          time.Time                                    `json:"created_at"`
	ExpiresAt          time.Time                                    `json:"expires_at"`
}

type upstreamShadowCandidateResponse struct {
	SourceID           string                                       `json:"source_id"`
	ChannelID          string                                       `json:"channel_id"`
	GroupKey           string                                       `json:"group_key"`
	ModelKey           string                                       `json:"model_key"`
	PriceDimension     contracts.UpstreamPriceDimension             `json:"price_dimension"`
	SettlementCurrency string                                       `json:"settlement_currency"`
	PerTokens          int64                                        `json:"per_tokens"`
	Cost               contracts.CanonicalDecimal                   `json:"cost"`
	QualityScore       contracts.CanonicalDecimal                   `json:"quality_score"`
	Constraints        []contracts.UpstreamRecommendationConstraint `json:"constraints"`
	EvidenceIDs        []string                                     `json:"evidence_ids"`
}

type upstreamShadowResponse struct {
	ID                        string                            `json:"id"`
	RecommendationID          string                            `json:"recommendation_id"`
	RecommendationFingerprint string                            `json:"recommendation_fingerprint"`
	Winner                    upstreamShadowCandidateResponse   `json:"winner"`
	Ranking                   []upstreamShadowCandidateResponse `json:"ranking"`
	EvidenceIDs               []string                          `json:"evidence_ids"`
	EvaluatedAt               time.Time                         `json:"evaluated_at"`
}

type upstreamDryRunIntentResponse struct {
	ChannelID string `json:"channel_id"`
	Enabled   bool   `json:"enabled"`
}

type upstreamDryRunActionResponse struct {
	Type      contracts.ReconcileActionType `json:"type"`
	ChannelID string                        `json:"channel_id"`
}

type upstreamDryRunResponse struct {
	ID                        string                         `json:"id"`
	RecommendationID          string                         `json:"recommendation_id"`
	RecommendationFingerprint string                         `json:"recommendation_fingerprint"`
	IntelligenceFactVersion   int64                          `json:"intelligence_fact_version"`
	CostLedgerFactVersion     int64                          `json:"cost_ledger_fact_version"`
	LinkFactVersion           int64                          `json:"link_fact_version"`
	PlanGeneration            int64                          `json:"plan_generation"`
	PlanID                    string                         `json:"plan_id"`
	FromChannelID             string                         `json:"from_channel_id"`
	ToChannelID               string                         `json:"to_channel_id"`
	DesiredScheduling         []upstreamDryRunIntentResponse `json:"desired_scheduling"`
	Actions                   []upstreamDryRunActionResponse `json:"actions"`
	ActionFingerprint         string                         `json:"action_fingerprint"`
	CreatedAt                 time.Time                      `json:"created_at"`
}

func projectUpstreamRecommendation(value contracts.UpstreamRecommendation) upstreamRecommendationResponse {
	return upstreamRecommendationResponse{
		ID: value.ID, Status: value.Status,
		IntelligenceFactVersion: value.IntelligenceFactVersion, CostLedgerFactVersion: value.CostLedgerFactVersion,
		LinkFactVersion: value.LinkFactVersion, PlanGeneration: value.PlanGeneration,
		FromSourceID: value.FromSourceID, FromChannelID: value.FromChannelID, FromGroupKey: value.FromGroupKey,
		ToSourceID: value.ToSourceID, ToChannelID: value.ToChannelID, ToGroupKey: value.ToGroupKey, ModelKey: value.ModelKey,
		PriceDimension: value.PriceDimension, SettlementCurrency: value.SettlementCurrency, PerTokens: value.PerTokens,
		AffectedPlanIDs: append([]string{}, value.AffectedPlanIDs...),
		EvidenceIDs:     append([]string{}, value.EvidenceIDs...), Constraints: cloneRecommendationConstraintsForResponse(value.Constraints),
		FromCost: value.FromCost, ToCost: value.ToCost, Savings: value.Savings,
		FormulaVersion: value.FormulaVersion, StrategyVersion: value.StrategyVersion, Fingerprint: value.Fingerprint,
		DryRunID: value.DryRunID, CreatedAt: value.CreatedAt, ExpiresAt: value.ExpiresAt,
	}
}

func projectUpstreamShadow(value contracts.UpstreamShadowResult) upstreamShadowResponse {
	ranking := make([]upstreamShadowCandidateResponse, 0, len(value.Ranking))
	for _, candidate := range value.Ranking {
		ranking = append(ranking, projectUpstreamShadowCandidate(candidate))
	}
	return upstreamShadowResponse{
		ID: value.ID, RecommendationID: value.RecommendationID, RecommendationFingerprint: value.RecommendationFingerprint,
		Winner: projectUpstreamShadowCandidate(value.Winner), Ranking: ranking,
		EvidenceIDs: append([]string{}, value.EvidenceIDs...), EvaluatedAt: value.EvaluatedAt,
	}
}

func projectUpstreamShadowCandidate(value contracts.UpstreamShadowCandidate) upstreamShadowCandidateResponse {
	return upstreamShadowCandidateResponse{
		SourceID: value.SourceID, ChannelID: value.ChannelID, GroupKey: value.GroupKey, ModelKey: value.ModelKey,
		PriceDimension: value.PriceDimension, SettlementCurrency: value.SettlementCurrency, PerTokens: value.PerTokens,
		Cost: value.Cost, QualityScore: value.QualityScore,
		Constraints: cloneRecommendationConstraintsForResponse(value.Constraints), EvidenceIDs: append([]string{}, value.EvidenceIDs...),
	}
}

func projectUpstreamDryRun(value contracts.UpstreamDryRunResult) upstreamDryRunResponse {
	intent := make([]upstreamDryRunIntentResponse, 0, 2)
	for _, channelID := range []string{value.FromChannelID, value.ToChannelID} {
		if enabled, ok := value.DesiredScheduling[channelID]; ok {
			intent = append(intent, upstreamDryRunIntentResponse{ChannelID: channelID, Enabled: enabled})
		}
	}
	actions := make([]upstreamDryRunActionResponse, 0, len(value.Plan.Actions))
	for _, action := range value.Plan.Actions {
		actions = append(actions, upstreamDryRunActionResponse{Type: action.Type, ChannelID: action.ChannelID})
	}
	return upstreamDryRunResponse{
		ID: value.ID, RecommendationID: value.RecommendationID, RecommendationFingerprint: value.RecommendationFingerprint,
		IntelligenceFactVersion: value.IntelligenceFactVersion, CostLedgerFactVersion: value.CostLedgerFactVersion,
		LinkFactVersion: value.LinkFactVersion, PlanGeneration: value.PlanGeneration, PlanID: value.PlanID,
		FromChannelID: value.FromChannelID, ToChannelID: value.ToChannelID, DesiredScheduling: intent,
		Actions: actions, ActionFingerprint: value.ActionSetHash, CreatedAt: value.CreatedAt,
	}
}

func cloneRecommendationConstraintsForResponse(values []contracts.UpstreamRecommendationConstraint) []contracts.UpstreamRecommendationConstraint {
	result := append([]contracts.UpstreamRecommendationConstraint{}, values...)
	for index := range result {
		result[index].EvidenceIDs = append([]string{}, result[index].EvidenceIDs...)
	}
	return result
}

func (s *Server) handleListUpstreamRecommendations(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	query, ok := s.parseRecommendationReadQuery(w, r, true)
	if !ok {
		return
	}
	reader, ok := s.store.(store.UpstreamRecommendationStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "upstream_recommendations_disabled", "recommendation persistence is not enabled")
		return
	}
	values, err := reader.ListUpstreamRecommendations(r.Context(), contracts.UpstreamRecommendationFilter{
		UserID: query.userID, Status: contracts.UpstreamRecommendationStatus(query.values["status"]), Limit: recommendationQueryLimit(query),
	})
	if err != nil {
		writeUpstreamRecommendationStoreError(w, err, "recommendations")
		return
	}
	response := make([]upstreamRecommendationResponse, 0, len(values))
	for _, value := range values {
		response = append(response, projectUpstreamRecommendation(value))
	}
	writeUpstreamIntelligenceReadJSON(w, response)
}

func (s *Server) handleGetUpstreamRecommendation(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	query, ok := s.parseRecommendationReadQuery(w, r, false)
	if !ok {
		return
	}
	reader, ok := s.store.(store.UpstreamRecommendationStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "upstream_recommendations_disabled", "recommendation persistence is not enabled")
		return
	}
	value, err := reader.GetUpstreamRecommendation(r.Context(), query.userID, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeUpstreamRecommendationStoreError(w, err, "recommendation")
		return
	}
	writeUpstreamIntelligenceReadJSON(w, projectUpstreamRecommendation(value))
}

func (s *Server) handleListUpstreamShadowResults(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	query, ok := s.parseExperimentReadQuery(w, r)
	if !ok {
		return
	}
	reader, ok := s.store.(store.UpstreamExperimentStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "upstream_experiments_disabled", "experiment persistence is not enabled")
		return
	}
	values, err := reader.ListUpstreamShadowResults(r.Context(), query.userID, query.values["recommendation_id"], recommendationQueryLimit(query))
	if err != nil {
		writeUpstreamRecommendationStoreError(w, err, "shadow experiments")
		return
	}
	response := make([]upstreamShadowResponse, 0, len(values))
	for _, value := range values {
		response = append(response, projectUpstreamShadow(value))
	}
	writeUpstreamIntelligenceReadJSON(w, response)
}

func (s *Server) handleListUpstreamDryRunResults(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	query, ok := s.parseExperimentReadQuery(w, r)
	if !ok {
		return
	}
	reader, ok := s.store.(store.UpstreamExperimentStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "upstream_experiments_disabled", "experiment persistence is not enabled")
		return
	}
	values, err := reader.ListUpstreamDryRunResults(r.Context(), query.userID, query.values["recommendation_id"], recommendationQueryLimit(query))
	if err != nil {
		writeUpstreamRecommendationStoreError(w, err, "dry-run experiments")
		return
	}
	response := make([]upstreamDryRunResponse, 0, len(values))
	for _, value := range values {
		response = append(response, projectUpstreamDryRun(value))
	}
	writeUpstreamIntelligenceReadJSON(w, response)
}

func (s *Server) parseRecommendationReadQuery(w http.ResponseWriter, r *http.Request, list bool) (upstreamIntelligenceQuery, bool) {
	allowed := upstreamIntelligenceStringSet("user_id")
	enums := map[string]map[string]bool{}
	positive := map[string]bool{}
	if list {
		allowed["status"], allowed["limit"] = true, true
		statuses := make(map[string]bool)
		for _, status := range []contracts.UpstreamRecommendationStatus{
			contracts.UpstreamRecommendationOpen, contracts.UpstreamRecommendationShadowing, contracts.UpstreamRecommendationReadyForDryRun,
			contracts.UpstreamRecommendationDryRunning, contracts.UpstreamRecommendationDryRunPassed, contracts.UpstreamRecommendationDryRunBlocked,
			contracts.UpstreamRecommendationDismissed, contracts.UpstreamRecommendationExpired,
		} {
			statuses[string(status)] = true
		}
		enums["status"], positive["limit"] = statuses, true
	}
	query, ok := parseUpstreamIntelligenceQuery(w, r, allowed, enums, positive)
	if !ok {
		return upstreamIntelligenceQuery{}, false
	}
	if _, ok := s.scopeOwnerUser(w, r, strconv.FormatInt(query.userID, 10)); !ok {
		return upstreamIntelligenceQuery{}, false
	}
	return query, true
}

func (s *Server) parseExperimentReadQuery(w http.ResponseWriter, r *http.Request) (upstreamIntelligenceQuery, bool) {
	query, ok := parseUpstreamIntelligenceQuery(w, r,
		upstreamIntelligenceStringSet("user_id", "recommendation_id", "limit"), nil, upstreamIntelligenceStringSet("limit"))
	if !ok {
		return upstreamIntelligenceQuery{}, false
	}
	if recommendationID := query.values["recommendation_id"]; recommendationID != "" && !validUpstreamIntelligenceWireIdentifier(recommendationID, 256) {
		writeError(w, http.StatusBadRequest, "validation_failed", "recommendation_id is invalid")
		return upstreamIntelligenceQuery{}, false
	}
	if _, ok := s.scopeOwnerUser(w, r, strconv.FormatInt(query.userID, 10)); !ok {
		return upstreamIntelligenceQuery{}, false
	}
	return query, true
}

func recommendationQueryLimit(query upstreamIntelligenceQuery) int {
	if raw := query.values["limit"]; raw != "" {
		value, _ := strconv.Atoi(raw)
		return value
	}
	return contracts.DefaultUpstreamIntelligenceListLimit
}

func writeUpstreamRecommendationStoreError(w http.ResponseWriter, err error, subject string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", subject+" not found")
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, "validation_failed", subject+" is invalid")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "state_conflict", subject+" changed; refresh and retry")
	default:
		writeError(w, http.StatusInternalServerError, "store_error", subject+" could not be read")
	}
}
