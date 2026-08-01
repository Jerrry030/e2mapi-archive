package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/strategy"
)

// This file serves the Phase 5 health-driven auto-switch surface: the decision
// history and inspection, a per-plan summary that folds current health + the
// live auto-switch status into one payload the console renders, the persisted
// route-strategy config, and on-demand evaluate/observe admin actions. Every
// real switch still flows through the orchestrator + reconcile engine; these
// endpoints only read the audit trail and trigger an evaluation.

// handleListAutoSwitchDecisions lists decisions, newest-first, scoped to the
// caller's account. Optional filters: plan_id, instance_id, pool_id, status,
// limit. A business user is confined to their own account; a platform admin may
// pass user_id to scope, or omit it to see all.
func (s *Server) handleListAutoSwitchDecisions(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	userID, ok := s.scopeOwnerUser(w, r, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	filter := contracts.AutoSwitchDecisionFilter{
		UserID:     userID,
		PlanID:     strings.TrimSpace(r.URL.Query().Get("plan_id")),
		InstanceID: strings.TrimSpace(r.URL.Query().Get("instance_id")),
		PoolID:     strings.TrimSpace(r.URL.Query().Get("pool_id")),
	}
	if st := strings.TrimSpace(r.URL.Query().Get("status")); st != "" {
		filter.Statuses = []contracts.AutoSwitchStatus{contracts.AutoSwitchStatus(st)}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Limit = n
		}
	}
	decisions, err := s.store.ListAutoSwitchDecisions(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if decisions == nil {
		decisions = []contracts.AutoSwitchDecision{}
	}
	writeJSON(w, http.StatusOK, decisions)
}

// handleGetAutoSwitchDecision returns one decision (with its dry-run preview),
// enforcing account read scope.
func (s *Server) handleGetAutoSwitchDecision(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	d, err := s.store.GetAutoSwitchDecision(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "auto-switch decision not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !s.requireOwnerRead(w, r, d.UserID) {
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// channelHealthView is the per-channel health line the summary surfaces so the
// console can show success rate / p95 latencies / health state without a second
// round-trip.
type channelHealthView struct {
	ChannelID             string                          `json:"channel_id"`
	DisplayName           string                          `json:"display_name,omitempty"`
	Status                contracts.UpstreamChannelStatus `json:"status"`
	Live                  bool                            `json:"live"`
	BindingState          contracts.PublishedBindingState `json:"binding_state,omitempty"`
	SampleCount           int                             `json:"sample_count"`
	Model                 string                          `json:"model,omitempty"`
	SuccessRate           float64                         `json:"success_rate"`
	UpstreamErrorRate     float64                         `json:"upstream_error_rate"`
	TTFTP95               float64                         `json:"ttft_p95"`
	DurationP95           float64                         `json:"duration_p95"`
	QualityScore          float64                         `json:"quality_score"`
	HealthScore           float64                         `json:"health_score"`
	EjectScore            float64                         `json:"eject_score"`
	QualityBelowThreshold bool                            `json:"quality_below_threshold"`
	BadWindows            int                             `json:"bad_windows"`
	CohortPercentage      int                             `json:"cohort_percentage"`
	CohortKnown           bool                            `json:"cohort_known"`
	CohortMember          bool                            `json:"cohort_member"`
	// Ejected is an applied scheduling state, not a score recommendation. Only
	// a durable open/half-open circuit may set it; a manually disabled binding
	// or a score below the threshold remains distinguishable.
	Ejected                   bool                            `json:"ejected"`
	HardFailure               bool                            `json:"hard_failure"`
	Penalties                 strategy.PenaltyBreakdown       `json:"penalties"`
	HealthState               contracts.HealthState           `json:"health_state"`
	CircuitState              contracts.QualityCircuitState   `json:"circuit_state,omitempty"`
	ProbeAfter                *time.Time                      `json:"probe_after,omitempty"`
	LastProbeAt               *time.Time                      `json:"last_probe_at,omitempty"`
	ConsecutiveProbeSuccesses int                             `json:"consecutive_probe_successes"`
	LastScore                 *float64                        `json:"last_score,omitempty"`
	LastReason                *contracts.QualityCircuitReason `json:"last_reason,omitempty"`
	RestorePending            bool                            `json:"restore_pending,omitempty"`
	RecoveryReady             bool                            `json:"recovery_ready,omitempty"`
	RecoveryStage             int                             `json:"recovery_stage,omitempty"`
	RecoveryStageStartedAt    *time.Time                      `json:"recovery_stage_started_at,omitempty"`
	RecoveryObserveAfter      *time.Time                      `json:"recovery_observe_after,omitempty"`
	EvidenceUpdatedAt         *time.Time                      `json:"evidence_updated_at,omitempty"`
	EvidenceFresh             bool                            `json:"evidence_fresh"`
	EvidenceConfidence        float64                         `json:"evidence_confidence"`
}

// autoSwitchSummary is the per-plan payload: the resolved strategy, the current
// active decision (if any), the most recent decisions, and the per-channel
// health of the plan's pool. It is exactly the "control console" view the design
// doc asks for (current strategy, health score, success rate, p95s, current
// status, recent switches).
type autoSwitchSummary struct {
	PlanID          string                         `json:"plan_id"`
	InstanceID      string                         `json:"instance_id"`
	PoolID          string                         `json:"pool_id"`
	UserID          int64                          `json:"user_id"`
	Strategy        contracts.RouteStrategyType    `json:"strategy"`
	StrategySource  contracts.StrategyScope        `json:"strategy_source,omitempty"`
	AutoApply       bool                           `json:"auto_apply"`
	ActiveDecision  *contracts.AutoSwitchDecision  `json:"active_decision,omitempty"`
	RecentDecisions []contracts.AutoSwitchDecision `json:"recent_decisions"`
	Channels        []channelHealthView            `json:"channels"`
}

// handleAutoSwitchSummary folds the plan's resolved strategy, current auto-switch
// status, recent decisions, and per-channel health into one payload.
func (s *Server) handleAutoSwitchSummary(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	ctx := r.Context()
	plan, err := s.store.GetRoutePlan(ctx, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "route plan not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !s.requireOwnerRead(w, r, plan.UserID) {
		return
	}

	strat, source := s.resolvePlanStrategy(ctx, plan)
	sum := autoSwitchSummary{
		PlanID:         plan.ID,
		InstanceID:     plan.InstanceID,
		PoolID:         plan.PoolID,
		UserID:         plan.UserID,
		Strategy:       strat.Type.Normalize(),
		StrategySource: source,
		AutoApply:      strat.AutoApply,
	}

	// Recent decisions + the current active one.
	decs, err := s.store.ListAutoSwitchDecisions(ctx, contracts.AutoSwitchDecisionFilter{PlanID: plan.ID, Limit: 20})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	sum.RecentDecisions = decs
	if sum.RecentDecisions == nil {
		sum.RecentDecisions = []contracts.AutoSwitchDecision{}
	}
	for i := range decs {
		if decs[i].Status.IsActive() {
			d := decs[i]
			sum.ActiveDecision = &d
			break
		}
	}

	// Per-channel health of the plan's pool, marking currently-live channels.
	live := map[string]bool{}
	bindingState := map[string]contracts.PublishedBindingState{}
	bindings, err := s.store.ListPublishedBindings(ctx, plan.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	for _, b := range bindings {
		bindingState[b.ChannelID] = b.State
		if b.State == contracts.BindingActive {
			live[b.ChannelID] = true
		}
	}
	circuits, err := s.store.ListQualityCircuitRuntimes(ctx, contracts.QualityCircuitRuntimeFilter{PlanID: plan.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	circuitByChannel := make(map[string]contracts.QualityCircuitRuntime, len(circuits))
	for _, circuit := range circuits {
		circuitByChannel[circuit.ChannelID] = circuit
	}
	channels, err := s.store.ListUpstreamChannels(ctx, plan.PoolID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	type cohortResult struct {
		selected map[string]bool
		known    bool
	}
	cohortCache := map[string]cohortResult{}
	cohortReader, canReadCohort := s.autoswitch.(SourceQualityCohortReader)
	views := make([]channelHealthView, 0, len(channels))
	for _, ch := range channels {
		v := channelHealthView{
			ChannelID:    ch.ID,
			DisplayName:  ch.DisplayName,
			Status:       ch.Status,
			Live:         live[ch.ID],
			BindingState: bindingState[ch.ID],
			HealthState:  contracts.HealthUnknown,
			EjectScore:   effectiveEjectScore(strat),
		}
		if snap, ok := s.worstSnapshot5m(ctx, plan.InstanceID, ch.ID, strat); ok {
			v.SampleCount = snap.SampleCount
			v.Model = snap.Model
			v.SuccessRate = snap.SuccessRate
			v.UpstreamErrorRate = snap.UpstreamErrorRate
			v.TTFTP95 = snap.TTFTP95
			v.DurationP95 = snap.DurationP95
			v.HealthState = snap.HealthState
			updatedAt := snap.CreatedAt
			if updatedAt.IsZero() {
				updatedAt = snap.BucketStart
			}
			if !updatedAt.IsZero() {
				v.EvidenceUpdatedAt = timePointer(updatedAt)
				v.EvidenceFresh = time.Since(updatedAt) <= 2*contracts.Window5m.Duration()
			}
			minSamples := strat.Thresholds.MinSamples
			if minSamples <= 0 {
				minSamples = 5
			}
			v.EvidenceConfidence = math.Min(1, float64(snap.QualitySampleCount)/float64(minSamples))
			evaluation := strategy.EvaluatePenalty(strat, strategy.Candidate{Channel: ch, Snapshot: snap, State: snap.HealthState})
			v.QualityScore = evaluation.Score
			v.HealthScore = evaluation.Score
			v.QualityBelowThreshold = evaluation.Score <= v.EjectScore
			v.HardFailure = evaluation.HardFailure
			v.Penalties = evaluation.Penalties
			if evaluation.Eject && !evaluation.HardFailure {
				v.BadWindows = s.consecutiveBadQualityWindows(ctx, plan, strat, ch)
				v.CohortPercentage = strategy.QualityEjectionPercentage(v.BadWindows)
				if canReadCohort {
					cacheKey := ch.SourceIdentity() + "\x00" + strconv.Itoa(v.CohortPercentage)
					cohort, exists := cohortCache[cacheKey]
					if !exists {
						cohort.selected, cohort.known = cohortReader.SourceQualityCohort(
							ctx, ch.SourceIdentity(), v.CohortPercentage,
						)
						cohortCache[cacheKey] = cohort
					}
					v.CohortKnown = cohort.known
					v.CohortMember = cohort.known && cohort.selected[plan.ID]
				}
			}
		} else {
			// No evidence starts at full quality. Binding state does not manufacture
			// a quality verdict; it is reported independently.
			v.QualityScore = 100
			v.HealthScore = 100
		}
		if circuit, ok := circuitByChannel[ch.ID]; ok {
			v.CircuitState = circuit.State
			v.Ejected = circuit.State == contracts.QualityCircuitOpen || circuit.State == contracts.QualityCircuitHalfOpen
			v.ProbeAfter = circuit.ProbeAfter
			v.LastProbeAt = circuit.LastProbeAt
			v.ConsecutiveProbeSuccesses = circuit.ConsecutiveProbeSuccesses
			lastScore := circuit.LastScore
			v.LastScore = &lastScore
			lastReason := circuit.LastReason
			v.LastReason = &lastReason
			v.RestorePending = circuit.RestorePending
			v.RecoveryReady = circuit.RecoveryReady
			v.RecoveryStage = circuit.RecoveryStage
			v.RecoveryStageStartedAt = circuit.RecoveryStageStartedAt
			v.RecoveryObserveAfter = circuit.RecoveryObserveAfter
		}
		views = append(views, v)
	}
	sum.Channels = views
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) consecutiveBadQualityWindows(ctx context.Context, plan contracts.RoutePlan, strat contracts.RouteStrategy, channel contracts.UpstreamChannel) int {
	snapshots, err := s.store.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{
		ChannelID: channel.ID, InstanceID: plan.InstanceID, Window: contracts.Window5m,
		IncludeHistory: true, Since: time.Now().UTC().Add(-30 * time.Minute),
	})
	if err != nil || len(snapshots) == 0 {
		return 1
	}
	streak := 0
	for _, bucket := range strategy.IndependentWindowBuckets(snapshots, contracts.Window5m.Duration()) {
		worst := bucket[0]
		worstEvaluation := strategy.EvaluatePenalty(strat, strategy.Candidate{Channel: channel, Snapshot: worst, State: worst.HealthState})
		for _, snapshot := range bucket[1:] {
			evaluation := strategy.EvaluatePenalty(strat, strategy.Candidate{Channel: channel, Snapshot: snapshot, State: snapshot.HealthState})
			if evaluation.Score < worstEvaluation.Score {
				worst, worstEvaluation = snapshot, evaluation
			}
		}
		if !worstEvaluation.Eject {
			break
		}
		streak++
	}
	if streak == 0 {
		return 1
	}
	return streak
}

// latestSnapshot5m returns a channel's most recent 5m snapshot, ok=false when
// none exists.
func (s *Server) worstSnapshot5m(ctx context.Context, instanceID, channelID string, strat contracts.RouteStrategy) (contracts.ChannelHealthSnapshot, bool) {
	snaps, err := s.store.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{
		ChannelID: channelID, InstanceID: instanceID, Window: contracts.Window5m,
	})
	if err != nil {
		return contracts.ChannelHealthSnapshot{}, false
	}
	if len(snaps) == 0 {
		// Compatibility for observations written before instance scoping existed.
		// Never fall back to another concrete instance.
		legacy, legacyErr := s.store.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{
			ChannelID: channelID, Window: contracts.Window5m,
		})
		if legacyErr != nil {
			return contracts.ChannelHealthSnapshot{}, false
		}
		for _, snap := range legacy {
			if snap.InstanceID == "" {
				snaps = append(snaps, snap)
			}
		}
	}
	if len(snaps) == 0 {
		return contracts.ChannelHealthSnapshot{}, false
	}
	worst := snaps[0]
	worstScore := strategy.EvaluatePenalty(strat, strategy.Candidate{Snapshot: worst}).Score
	for _, sn := range snaps[1:] {
		score := strategy.EvaluatePenalty(strat, strategy.Candidate{Snapshot: sn}).Score
		if score < worstScore || score == worstScore && sn.CreatedAt.After(worst.CreatedAt) {
			worst = sn
			worstScore = score
		}
	}
	return worst, true
}

func effectiveEjectScore(strat contracts.RouteStrategy) float64 {
	if score := strat.Thresholds.EjectScore; score > 0 && score <= 100 {
		return score
	}
	return 60
}

// resolvePlanStrategy resolves the plan's effective strategy for display,
// mirroring the orchestrator's precedence plan > pool > user > default and
// reporting which scope supplied it (empty scope means the built-in default).
func (s *Server) resolvePlanStrategy(ctx context.Context, plan contracts.RoutePlan) (contracts.RouteStrategy, contracts.StrategyScope) {
	lookups := []struct {
		scope  contracts.StrategyScope
		filter contracts.RouteStrategyFilter
	}{
		{contracts.StrategyScopePlan, contracts.RouteStrategyFilter{Scope: contracts.StrategyScopePlan, PlanID: plan.ID}},
		{contracts.StrategyScopePool, contracts.RouteStrategyFilter{Scope: contracts.StrategyScopePool, PoolID: plan.PoolID}},
		{contracts.StrategyScopeUser, contracts.RouteStrategyFilter{Scope: contracts.StrategyScopeUser, UserID: plan.UserID}},
	}
	for _, l := range lookups {
		if l.filter.PlanID == "" && l.filter.PoolID == "" && l.filter.UserID == 0 {
			continue
		}
		found, err := s.store.ListRouteStrategies(ctx, l.filter)
		if err == nil && len(found) > 0 {
			return found[0], l.scope
		}
	}
	// Built-in default: stability-first, auto-applying (matches the orchestrator).
	typ := contracts.RouteStrategyType(plan.Labels["strategy"]).Normalize()
	return contracts.RouteStrategy{Type: typ, AutoApply: true}, ""
}

// handleAutoSwitchEvaluate triggers an on-demand evaluation of a plan (platform
// admin only). It returns the produced decision, or 204 when nothing needed
// switching.
func (s *Server) handleAutoSwitchEvaluate(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.autoswitch == nil {
		writeError(w, http.StatusServiceUnavailable, "autoswitch_disabled", "auto-switch controller is not enabled")
		return
	}
	planID := r.PathValue("id")
	plan, err := s.store.GetRoutePlan(r.Context(), planID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "route plan not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	dec, err := s.autoswitch.Evaluate(r.Context(), planID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "evaluate_error", err.Error())
		return
	}
	s.auditUpstream(r, plan.UserID, "auto_switch.evaluate", "route_plan", planID)
	if dec == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, dec)
}

// handleAutoSwitchObserve advances one observing decision now (platform admin
// only), rather than waiting for the background runner's next tick.
func (s *Server) handleAutoSwitchObserve(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.autoswitch == nil {
		writeError(w, http.StatusServiceUnavailable, "autoswitch_disabled", "auto-switch controller is not enabled")
		return
	}
	id := r.PathValue("id")
	if _, err := s.store.GetAutoSwitchDecision(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "auto-switch decision not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	dec, err := s.autoswitch.Observe(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "observe_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dec)
}

// --- route strategies (platform-curated policy; platform admin only) ---

func (s *Server) handleListRouteStrategies(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	filter := contracts.RouteStrategyFilter{
		Scope:  contracts.StrategyScope(strings.TrimSpace(r.URL.Query().Get("scope"))),
		PlanID: strings.TrimSpace(r.URL.Query().Get("plan_id")),
		PoolID: strings.TrimSpace(r.URL.Query().Get("pool_id")),
	}
	userID, ok := parseOptionalUserID(w, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	filter.UserID = userID
	strategies, err := s.store.ListRouteStrategies(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if strategies == nil {
		strategies = []contracts.RouteStrategy{}
	}
	writeJSON(w, http.StatusOK, strategies)
}

func (s *Server) handleUpsertRouteStrategy(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	var input contracts.RouteStrategy
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if msg := validRouteStrategy(input); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	normalized, auditUserID, targetMessage, err := s.normalizeRouteStrategyTarget(r.Context(), input)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "validation_failed", targetMessage)
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	saved, err := s.store.UpsertRouteStrategy(r.Context(), normalized)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.auditUpstream(r, auditUserID, "route_strategy.upsert", "route_strategy", saved.ID)
	writeJSON(w, http.StatusOK, saved)
}

// normalizeRouteStrategyTarget makes the semantic scope key canonical before it
// reaches persistence. Unrelated owner fields must never create a second row for
// the same plan, pool, or user. The HTTP API upserts by scope, not by a caller-
// supplied record id.
func (s *Server) normalizeRouteStrategyTarget(ctx context.Context, input contracts.RouteStrategy) (contracts.RouteStrategy, int64, string, error) {
	input.ID = ""
	switch input.Scope {
	case contracts.StrategyScopePlan:
		plan, err := s.store.GetRoutePlan(ctx, input.PlanID)
		input.PoolID = ""
		input.UserID = 0
		return input, plan.UserID, "plan_id does not exist", err
	case contracts.StrategyScopePool:
		_, err := s.store.GetUpstreamPool(ctx, input.PoolID)
		input.PlanID = ""
		input.UserID = 0
		return input, 0, "pool_id does not exist", err
	case contracts.StrategyScopeUser:
		user, err := s.store.GetUser(ctx, input.UserID)
		input.PlanID = ""
		input.PoolID = ""
		return input, user.ID, "user_id does not exist", err
	default:
		return input, 0, "invalid strategy scope", store.ErrNotFound
	}
}

func (s *Server) handleDeleteRouteStrategy(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.store.DeleteRouteStrategy(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "route strategy not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.auditUpstream(r, 0, "route_strategy.delete", "route_strategy", id)
	w.WriteHeader(http.StatusNoContent)
}

// validRouteStrategy checks a strategy's scope carries its owning id and the
// type/weights are sane. The engine fills unset thresholds/weights with type
// defaults, so a minimally-specified strategy is valid.
func validRouteStrategy(s contracts.RouteStrategy) string {
	switch s.Scope {
	case contracts.StrategyScopePlan:
		if strings.TrimSpace(s.PlanID) == "" {
			return "plan_id is required for a plan-scoped strategy"
		}
	case contracts.StrategyScopePool:
		if strings.TrimSpace(s.PoolID) == "" {
			return "pool_id is required for a pool-scoped strategy"
		}
	case contracts.StrategyScopeUser:
		if s.UserID == 0 {
			return "user_id is required for a user-scoped strategy"
		}
	default:
		return "scope must be plan, pool, or user"
	}
	switch s.Type {
	case contracts.StrategyStabilityFirst, contracts.StrategyCostFirst,
		contracts.StrategyLatencyFirst, contracts.StrategyBalanced:
	default:
		return "type must be stability_first, cost_first, latency_first, or balanced"
	}
	w := s.Weights
	if w != (contracts.StrategyWeights{}) {
		weights := []float64{w.Success, w.TTFT, w.Duration, w.Stability, w.Cost}
		total := 0.0
		for _, weight := range weights {
			if math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 || weight > 1 {
				return "strategy weights must be finite and between 0 and 1"
			}
			total += weight
		}
		if math.Abs(total-1) > 0.001 {
			return "strategy weights must sum to 1"
		}
	}
	t := s.Thresholds
	thresholdValues := []float64{
		t.TargetSuccessRate, t.FloorSuccessRate, t.MaxTTFTP95MS, t.MaxDurationP95MS, t.EjectScore,
	}
	for _, value := range thresholdValues {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "strategy thresholds must be finite"
		}
	}
	if t.MinSamples < 0 || t.ConsecutiveFailureLimit < 0 ||
		t.MaxTTFTP95MS < 0 || t.MaxDurationP95MS < 0 {
		return "strategy thresholds cannot be negative"
	}
	if t.TargetSuccessRate < 0 || t.TargetSuccessRate > 1 ||
		t.FloorSuccessRate < 0 || t.FloorSuccessRate > 1 {
		return "success-rate thresholds must be between 0 and 1"
	}
	if t.EjectScore < 0 || t.EjectScore > 100 {
		return "eject_score must be between 0 and 100"
	}
	// Zero means "use the engine default", so compare the effective values. A
	// partial override must not become floor > target after defaults are filled.
	targetSuccessRate := t.TargetSuccessRate
	if targetSuccessRate == 0 {
		targetSuccessRate = 0.95
	}
	floorSuccessRate := t.FloorSuccessRate
	if floorSuccessRate == 0 {
		floorSuccessRate = 0.85
	}
	if floorSuccessRate > targetSuccessRate {
		return "floor_success_rate cannot exceed target_success_rate"
	}
	const maxGuardSeconds = 7 * 24 * 60 * 60
	if s.CooldownSeconds < 0 || s.CooldownSeconds > maxGuardSeconds {
		return "cooldown_seconds must be between 0 and 604800"
	}
	if s.RecoveryObservationSeconds < 0 || s.RecoveryObservationSeconds > maxGuardSeconds {
		return "recovery_observation_seconds must be between 0 and 604800"
	}
	if s.MaxAutoSwitchesPerHour < 0 || s.MaxAutoSwitchesPerHour > 3600 {
		return "max_auto_switches_per_hour must be between 0 and 3600"
	}
	return ""
}
