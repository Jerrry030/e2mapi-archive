package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/upstreamexperiment"
	"e2m.local/core/internal/upstreamrecommendation"
)

func (s *Server) handleRunUpstreamRecommendationShadow(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	userID, ok := s.parseRecommendationActionUser(w, r)
	if !ok {
		return
	}
	if !decodeEmptyRecommendationAction(w, r) {
		return
	}
	recommendations, experiments, inputs, ok := s.recommendationExperimentStores(w)
	if !ok {
		return
	}
	current, err := recommendations.GetUpstreamRecommendation(r.Context(), userID, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeUpstreamRecommendationStoreError(w, err, "recommendation")
		return
	}
	if current.Status != contracts.UpstreamRecommendationOpen {
		writeError(w, http.StatusConflict, "state_conflict", "recommendation is not open for shadow evaluation")
		return
	}
	snapshot, err := inputs.ReadUpstreamRecommendationInputs(r.Context(), userID)
	if err != nil {
		writeUpstreamRecommendationStoreError(w, err, "recommendation input snapshot")
		return
	}
	facts := currentRecommendationFacts(snapshot, current)
	if validity := upstreamrecommendation.ValidateCurrent(current, facts); !validity.Current {
		s.expireRecommendationIfNeeded(r.Context(), recommendations, current, snapshot.GeneratedAt)
		writeJSON(w, http.StatusConflict, map[string]any{"code": "stale_recommendation", "reasons": validity.Reasons})
		return
	}
	candidates, err := trustedShadowCandidates(snapshot, current)
	if err != nil {
		writeError(w, http.StatusConflict, "shadow_blocked", "trusted evidence cannot produce comparable shadow candidates")
		return
	}
	result, err := upstreamexperiment.ShadowRank(current, candidates, facts.Now)
	if err != nil {
		writeError(w, http.StatusConflict, "shadow_blocked", "shadow evaluation found no eligible candidate")
		return
	}
	result.ID, err = newUpstreamExperimentID("shadow")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_error", "could not create experiment identity")
		return
	}
	ready, saved, err := experiments.CompleteUpstreamShadow(r.Context(), current, result)
	if err != nil {
		writeUpstreamRecommendationStoreError(w, err, "shadow completion")
		return
	}
	s.auditRecommendationExperiment(r, userID, "upstream_recommendation.shadow", "upstream_shadow", saved.ID)
	writeJSON(w, http.StatusOK, map[string]any{"recommendation": projectUpstreamRecommendation(ready), "experiment": projectUpstreamShadow(saved)})
}

func (s *Server) handleRunUpstreamRecommendationDryRun(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	userID, ok := s.parseRecommendationActionUser(w, r)
	if !ok {
		return
	}
	if !decodeEmptyRecommendationAction(w, r) {
		return
	}
	if s.publish == nil {
		writeError(w, http.StatusServiceUnavailable, "publish_unavailable", "dry-run planner is not configured")
		return
	}
	recommendations, experiments, inputs, ok := s.recommendationExperimentStores(w)
	if !ok {
		return
	}
	current, err := recommendations.GetUpstreamRecommendation(r.Context(), userID, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeUpstreamRecommendationStoreError(w, err, "recommendation")
		return
	}
	if current.Status != contracts.UpstreamRecommendationReadyForDryRun {
		writeError(w, http.StatusConflict, "state_conflict", "recommendation is not ready for dry-run")
		return
	}
	snapshot, err := inputs.ReadUpstreamRecommendationInputs(r.Context(), userID)
	if err != nil {
		writeUpstreamRecommendationStoreError(w, err, "recommendation input snapshot")
		return
	}
	facts := currentRecommendationFacts(snapshot, current)
	if validity := upstreamrecommendation.ValidateCurrent(current, facts); !validity.Current {
		s.expireRecommendationIfNeeded(r.Context(), recommendations, current, snapshot.GeneratedAt)
		writeJSON(w, http.StatusConflict, map[string]any{"code": "stale_recommendation", "reasons": validity.Reasons})
		return
	}
	dryRunID, err := newUpstreamExperimentID("dry")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_error", "could not create experiment identity")
		return
	}
	// Bind preview time to the same consistency snapshot used for validity.
	// Mixing wall time here would let a long request create evidence outside the
	// recommendation's trusted decision boundary.
	bridge, err := upstreamexperiment.NewBridge(s.publish, func() time.Time { return facts.Now })
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "dry_run_unavailable", "dry-run planner is not available")
		return
	}
	result, err := bridge.DryRun(contracts.WithReconcileTrigger(actorCtx(r), contracts.ReconcileTriggerManual), current)
	if err != nil {
		writeError(w, http.StatusConflict, "dry_run_blocked", "dry-run planning failed closed")
		return
	}
	result.ID = dryRunID
	if err := validateDryRunActions(result); err != nil {
		writeError(w, http.StatusConflict, "dry_run_blocked", "dry-run action set does not exactly match the recommendation")
		return
	}
	passed, saved, err := experiments.CompleteUpstreamDryRun(r.Context(), current, result)
	if err != nil {
		writeUpstreamRecommendationStoreError(w, err, "dry-run completion")
		return
	}
	s.auditRecommendationExperiment(r, userID, "upstream_recommendation.dry_run", "upstream_dry_run", saved.ID)
	writeJSON(w, http.StatusOK, map[string]any{"recommendation": projectUpstreamRecommendation(passed), "experiment": projectUpstreamDryRun(saved)})
}

func (s *Server) parseRecommendationActionUser(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if !requirePlatformAdmin(w, r) {
		return 0, false
	}
	query, ok := parseUpstreamIntelligenceQuery(w, r, upstreamIntelligenceStringSet("user_id"), nil, nil)
	if !ok {
		return 0, false
	}
	if !s.requireOwnerWrite(w, r, query.userID) {
		return 0, false
	}
	if !validUpstreamIntelligenceWireIdentifier(strings.TrimSpace(r.PathValue("id")), 256) {
		writeError(w, http.StatusBadRequest, "validation_failed", "recommendation id is invalid")
		return 0, false
	}
	return query.userID, true
}

func decodeEmptyRecommendationAction(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var request map[string]any
	if err := decodeStrictJSON(r, &request); err != nil || request == nil || len(request) != 0 {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be an empty JSON object")
		return false
	}
	return true
}

func (s *Server) recommendationExperimentStores(w http.ResponseWriter) (store.UpstreamRecommendationStore, store.UpstreamExperimentStore, store.UpstreamRecommendationInputStore, bool) {
	recommendations, recOK := s.store.(store.UpstreamRecommendationStore)
	experiments, experimentOK := s.store.(store.UpstreamExperimentStore)
	inputs, inputOK := s.store.(store.UpstreamRecommendationInputStore)
	if !recOK || !experimentOK || !inputOK {
		writeError(w, http.StatusServiceUnavailable, "upstream_experiments_disabled", "recommendation experiment service is not enabled")
		return nil, nil, nil, false
	}
	return recommendations, experiments, inputs, true
}

func (s *Server) expireRecommendationIfNeeded(ctx context.Context, values store.UpstreamRecommendationStore, current contracts.UpstreamRecommendation, now time.Time) {
	if now.Before(current.ExpiresAt) {
		return
	}
	next, err := upstreamrecommendation.Transition(current, contracts.UpstreamRecommendationEvent{
		Type: contracts.UpstreamRecommendationEventExpire, UserID: current.UserID, Now: now,
	})
	if err == nil {
		_, _ = values.TransitionUpstreamRecommendation(ctx, next, current.Status)
	}
}

func currentRecommendationFacts(snapshot store.UpstreamRecommendationInputs, value contracts.UpstreamRecommendation) contracts.UpstreamRecommendationCurrentFacts {
	result := contracts.UpstreamRecommendationCurrentFacts{UserID: snapshot.UserID, Now: snapshot.GeneratedAt}
	generated, err := generateTrustedCurrentRecommendations(snapshot, "current-")
	if err != nil {
		return result
	}
	for _, current := range generated.Recommendations {
		if current.Fingerprint != value.Fingerprint {
			continue
		}
		return contracts.UpstreamRecommendationCurrentFacts{
			UserID: current.UserID, IntelligenceFactVersion: current.IntelligenceFactVersion,
			CostLedgerFactVersion: current.CostLedgerFactVersion, LinkFactVersion: current.LinkFactVersion,
			PlanGeneration: current.PlanGeneration, FromSourceID: current.FromSourceID, FromChannelID: current.FromChannelID,
			FromGroupKey: current.FromGroupKey, ToSourceID: current.ToSourceID, ToChannelID: current.ToChannelID,
			ToGroupKey: current.ToGroupKey, ModelKey: current.ModelKey, PriceDimension: current.PriceDimension,
			SettlementCurrency: current.SettlementCurrency, PerTokens: current.PerTokens,
			AffectedPlanIDs: append([]string{}, current.AffectedPlanIDs...), AffectedDownstreams: append([]string{}, current.AffectedDownstreams...),
			EvidenceIDs: append([]string{}, current.EvidenceIDs...), FormulaVersion: current.FormulaVersion,
			StrategyVersion: current.StrategyVersion, Now: snapshot.GeneratedAt,
		}
	}
	return result
}

func trustedShadowCandidates(snapshot store.UpstreamRecommendationInputs, recommendation contracts.UpstreamRecommendation) ([]contracts.UpstreamShadowCandidate, error) {
	generated, err := generateTrustedCurrentRecommendations(snapshot, "shadow-")
	if err != nil {
		return nil, err
	}
	for _, current := range generated.Recommendations {
		if current.Fingerprint != recommendation.Fingerprint {
			continue
		}
		if validity := upstreamrecommendation.ValidateCurrent(recommendation, recommendationCurrentFactsFromGenerated(current, snapshot.GeneratedAt)); !validity.Current {
			return nil, errors.New("stale recommendation")
		}
		from, fromOK := shadowCandidateFromGenerated(snapshot, current, false)
		to, toOK := shadowCandidateFromGenerated(snapshot, current, true)
		if !fromOK || !toOK {
			return nil, errors.New("quality evidence not reproduced")
		}
		return []contracts.UpstreamShadowCandidate{from, to}, nil
	}
	return nil, errors.New("trusted recommendation not reproduced")
}

func generateTrustedCurrentRecommendations(snapshot store.UpstreamRecommendationInputs, prefix string) (contracts.UpstreamRecommendationGenerationResult, error) {
	sequence := 0
	return upstreamrecommendation.Generate(upstreamRecommendationGeneratorInputs(snapshot), func() string {
		sequence++
		return prefix + strconv.Itoa(sequence)
	})
}

func recommendationCurrentFactsFromGenerated(current contracts.UpstreamRecommendation, now time.Time) contracts.UpstreamRecommendationCurrentFacts {
	return contracts.UpstreamRecommendationCurrentFacts{
		UserID: current.UserID, IntelligenceFactVersion: current.IntelligenceFactVersion, CostLedgerFactVersion: current.CostLedgerFactVersion,
		LinkFactVersion: current.LinkFactVersion, PlanGeneration: current.PlanGeneration, FromSourceID: current.FromSourceID,
		FromChannelID: current.FromChannelID, FromGroupKey: current.FromGroupKey, ToSourceID: current.ToSourceID,
		ToChannelID: current.ToChannelID, ToGroupKey: current.ToGroupKey, ModelKey: current.ModelKey,
		PriceDimension: current.PriceDimension, SettlementCurrency: current.SettlementCurrency, PerTokens: current.PerTokens,
		AffectedPlanIDs: append([]string{}, current.AffectedPlanIDs...), AffectedDownstreams: append([]string{}, current.AffectedDownstreams...),
		EvidenceIDs: append([]string{}, current.EvidenceIDs...), FormulaVersion: current.FormulaVersion, StrategyVersion: current.StrategyVersion, Now: now,
	}
}

func shadowCandidateFromGenerated(snapshot store.UpstreamRecommendationInputs, current contracts.UpstreamRecommendation, destination bool) (contracts.UpstreamShadowCandidate, bool) {
	if len(current.AffectedDownstreams) != 1 {
		return contracts.UpstreamShadowCandidate{}, false
	}
	sourceID, channelID, groupKey, cost := current.FromSourceID, current.FromChannelID, current.FromGroupKey, current.FromCost.Expected
	if destination {
		sourceID, channelID, groupKey, cost = current.ToSourceID, current.ToChannelID, current.ToGroupKey, current.ToCost.Expected
	}
	qualityScore := contracts.CanonicalDecimal("")
	qualityCount := 0
	for _, quality := range snapshot.Intelligence.QualitySnapshots {
		if quality.ChannelID != channelID || quality.InstanceID != current.AffectedDownstreams[0] || quality.Model != current.ModelKey || !containsString(current.EvidenceIDs, quality.ID) {
			continue
		}
		canonical, err := canonicalQualityScore(quality.QualityScore)
		if err != nil {
			return contracts.UpstreamShadowCandidate{}, false
		}
		qualityScore = canonical
		qualityCount++
	}
	if qualityCount != 1 {
		return contracts.UpstreamShadowCandidate{}, false
	}
	evidence := append([]string{}, current.EvidenceIDs...)
	return contracts.UpstreamShadowCandidate{
		UserID: current.UserID, SourceID: sourceID, ChannelID: channelID, GroupKey: groupKey, ModelKey: current.ModelKey,
		PriceDimension: current.PriceDimension, SettlementCurrency: current.SettlementCurrency, PerTokens: current.PerTokens,
		Cost: cost, QualityScore: qualityScore,
		Constraints: cloneRecommendationConstraintsForResponse(current.Constraints), EvidenceIDs: evidence,
	}, true
}

func validateDryRunActions(value contracts.UpstreamDryRunResult) error {
	allowed := map[string]contracts.ReconcileActionType{
		value.FromChannelID: contracts.ReconcileDisable,
		value.ToChannelID:   contracts.ReconcileEnable,
	}
	seen := make(map[string]bool)
	realActions := 0
	for _, action := range value.Plan.Actions {
		if action.Type == contracts.ReconcileNoop {
			if _, ok := allowed[action.ChannelID]; !ok || seen[action.ChannelID] {
				return errors.New("unexpected action")
			}
			seen[action.ChannelID] = true
			continue
		}
		want, ok := allowed[action.ChannelID]
		if !ok || action.Type != want || seen[action.ChannelID] {
			return errors.New("unexpected action")
		}
		seen[action.ChannelID] = true
		realActions++
	}
	// A no-op is valid only for the already-desired side; at least one real
	// action must remain or there is no executable switch to approve.
	if realActions == 0 {
		return errors.New("empty action set")
	}
	return nil
}

func canonicalQualityScore(value float64) (contracts.CanonicalDecimal, error) {
	text := strconv.FormatFloat(value, 'f', -1, 64)
	rational, ok := new(big.Rat).SetString(text)
	if !ok {
		return "", errors.New("invalid quality score")
	}
	return contracts.QuantizeCanonicalDecimal(rational, contracts.UpstreamDecimalMaxScale)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func newUpstreamExperimentID(prefix string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(raw), nil
}

func (s *Server) auditRecommendationExperiment(r *http.Request, userID int64, action, targetType, targetID string) {
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: userID, ActorType: "user", ActorID: actor.Email, Action: action,
		RiskLevel: contracts.RiskLevelL0, TargetType: targetType, TargetID: targetID, Result: "accepted",
	})
}
