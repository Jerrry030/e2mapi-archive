package httpapi

import (
	"net/http"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/auth"
)

type ownerRoutingPreference string

const (
	ownerRoutingSmartAuto    ownerRoutingPreference = "smart_auto"
	ownerRoutingPriceFirst   ownerRoutingPreference = "price_first"
	ownerRoutingSpeedFirst   ownerRoutingPreference = "speed_first"
	ownerRoutingSuccessFirst ownerRoutingPreference = "success_first"
)

type ownerRoutingPreferenceRequest struct {
	Preference ownerRoutingPreference `json:"preference"`
}

type ownerRoutingPreferenceResponse struct {
	Preference        ownerRoutingPreference      `json:"preference"`
	EffectiveStrategy contracts.RouteStrategyType `json:"effective_strategy"`
}

var ownerPreferenceStrategies = map[ownerRoutingPreference]contracts.RouteStrategyType{
	ownerRoutingSmartAuto:    contracts.StrategyBalanced,
	ownerRoutingPriceFirst:   contracts.StrategyCostFirst,
	ownerRoutingSpeedFirst:   contracts.StrategyLatencyFirst,
	ownerRoutingSuccessFirst: contracts.StrategyStabilityFirst,
}

var ownerStrategyPreferences = map[contracts.RouteStrategyType]ownerRoutingPreference{
	contracts.StrategyBalanced:       ownerRoutingSmartAuto,
	contracts.StrategyCostFirst:      ownerRoutingPriceFirst,
	contracts.StrategyLatencyFirst:   ownerRoutingSpeedFirst,
	contracts.StrategyStabilityFirst: ownerRoutingSuccessFirst,
}

// handleGetOwnerRoutingPreference returns only the current owner's four-choice
// preference. Internal policy controls are deliberately not exposed.
func (s *Server) handleGetOwnerRoutingPreference(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	owner, ok := requireCurrentOwner(w, r)
	if !ok || !rejectOwnerRoutingPreferenceQuery(w, r) {
		return
	}
	strategies, err := s.store.ListRouteStrategies(r.Context(), contracts.RouteStrategyFilter{
		Scope: contracts.StrategyScopeUser, UserID: owner.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	typ := contracts.StrategyBalanced
	if len(strategies) > 0 {
		typ = strategies[0].Type.Normalize()
	}
	writeJSON(w, http.StatusOK, ownerRoutingPreferenceView(typ))
}

// handleUpdateOwnerRoutingPreference accepts one product choice and persists a
// complete platform-curated user strategy. Strict JSON rejects user_id and all
// advanced RouteStrategy fields.
func (s *Server) handleUpdateOwnerRoutingPreference(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	owner, ok := requireCurrentOwner(w, r)
	if !ok || !rejectOwnerRoutingPreferenceQuery(w, r) {
		return
	}
	var input ownerRoutingPreferenceRequest
	if err := decodeStrictJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	typ, ok := ownerPreferenceStrategies[input.Preference]
	if !ok {
		writeError(w, http.StatusBadRequest, "validation_failed", "preference must be smart_auto, price_first, speed_first, or success_first")
		return
	}

	// An owner may change only the ranking preset. If the platform already
	// attached stricter safety/governance controls at user scope, preserve them
	// instead of resetting approval, thresholds, or anti-flapping limits.
	strategyToSave := ownerRoutingStrategyTemplate(owner.ID, typ)
	existing, err := s.store.ListRouteStrategies(r.Context(), contracts.RouteStrategyFilter{
		Scope: contracts.StrategyScopeUser, UserID: owner.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if len(existing) > 0 {
		strategyToSave = ownerPreferenceOverExisting(existing[0], owner.ID, typ)
	}
	saved, err := s.store.UpsertRouteStrategy(r.Context(), strategyToSave)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: owner.ID, ActorType: "user", ActorID: owner.Email,
		Action: "route_strategy.owner_preference.update", RiskLevel: contracts.RiskLevelL1,
		TargetType: "route_strategy", TargetID: saved.ID, Result: "accepted",
	})
	writeJSON(w, http.StatusOK, ownerRoutingPreferenceView(saved.Type))
}

func requireCurrentOwner(w http.ResponseWriter, r *http.Request) (contracts.User, bool) {
	owner := currentUser(r)
	if owner.ID <= 0 || !auth.IsOwner(owner) {
		writeError(w, http.StatusForbidden, "forbidden", "owner role required")
		return contracts.User{}, false
	}
	return owner, true
}

func rejectOwnerRoutingPreferenceQuery(w http.ResponseWriter, r *http.Request) bool {
	if strings.TrimSpace(r.URL.RawQuery) != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "routing preference does not accept query parameters")
		return false
	}
	return true
}

func ownerRoutingPreferenceView(typ contracts.RouteStrategyType) ownerRoutingPreferenceResponse {
	// The owner product default is smart_auto/balanced. RouteStrategyType.Normalize
	// intentionally defaults legacy/unknown values to stability_first, so handle
	// the zero value here before normalization to keep GET and execution aligned.
	if typ == "" {
		typ = contracts.StrategyBalanced
	} else {
		typ = typ.Normalize()
	}
	return ownerRoutingPreferenceResponse{
		Preference: ownerStrategyPreferences[typ], EffectiveStrategy: typ,
	}
}

// Zero weights select the engine's platform-maintained per-type blend. Owners
// cannot modify the fixed quality gates or anti-flapping controls below.
func ownerRoutingStrategyTemplate(userID int64, typ contracts.RouteStrategyType) contracts.RouteStrategy {
	return contracts.RouteStrategy{
		Name: "Managed owner routing preference", Type: typ,
		Scope: contracts.StrategyScopeUser, UserID: userID,
		Thresholds: contracts.StrategyThresholds{
			MinSamples: 5, TargetSuccessRate: 0.95, FloorSuccessRate: 0.85,
			MaxTTFTP95MS: 4000, MaxDurationP95MS: 20000, ConsecutiveFailureLimit: 3,
		},
		AutoApply: true, ApprovalRequired: false,
		CooldownSeconds: 600, RecoveryObservationSeconds: 900, MaxAutoSwitchesPerHour: 6,
	}
}

func ownerPreferenceOverExisting(existing contracts.RouteStrategy, userID int64, typ contracts.RouteStrategyType) contracts.RouteStrategy {
	existing.Type = typ
	existing.Weights = contracts.StrategyWeights{}
	existing.Scope = contracts.StrategyScopeUser
	existing.UserID = userID
	existing.PlanID = ""
	existing.PoolID = ""
	return existing
}
