package httpapi

import (
	"net/http"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/upstreammargin"
)

func (s *Server) handleUpstreamIntelligenceMargin(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	query, ok := parseUpstreamIntelligenceQuery(w, r, upstreamIntelligenceStringSet("user_id", "window"), map[string]map[string]bool{
		"window": upstreamIntelligenceStringSet(string(contracts.UpstreamIntelligenceWindow24h), string(contracts.UpstreamIntelligenceWindow7d)),
	}, nil)
	if !ok {
		return
	}
	window := contracts.UpstreamIntelligenceReadWindow(query.values["window"])
	if window == "" {
		window = contracts.UpstreamIntelligenceWindow24h
	}
	reader, ok := s.store.(store.UpstreamMarginReadStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "upstream_margin_disabled", "upstream margin cost read model is not enabled")
		return
	}
	windowEnd := time.Now().UTC()
	windowStart := windowEnd.Add(-24 * time.Hour)
	if window == contracts.UpstreamIntelligenceWindow7d {
		windowStart = windowEnd.Add(-7 * 24 * time.Hour)
	}
	costs, err := reader.ReadUpstreamMarginCostFacts(r.Context(), query.userID, windowStart, windowEnd)
	if err != nil {
		writeUpstreamIntelligenceReadStoreError(w, err, "upstream margin cost facts")
		return
	}
	model, err := upstreammargin.Aggregate(query.userID, costs, nil)
	if err != nil {
		writeError(w, http.StatusConflict, "read_model_conflict", "upstream margin cost facts are inconsistent")
		return
	}
	response := contracts.UpstreamMarginCostReadResponse{
		Window: window, WindowStart: windowStart, WindowEnd: windowEnd, GeneratedAt: windowEnd,
		Costs: model.Costs, TotalCostFactCount: model.TotalCostFactCount,
		AttributableCostFactCount: model.AttributableCostFactCount, UncoveredCostFactCount: model.UncoveredCostFactCount,
		AttributableCoverage: model.AttributableCoverage, MinimumAttributableCoverage: model.MinimumAttributableCoverage,
		CoverageGatePassed: model.CoverageGatePassed, BlockedReasons: browserSafeMarginBlockedReasons(model),
	}
	writeUpstreamIntelligenceReadJSON(w, response)
}

func browserSafeMarginBlockedReasons(model contracts.UpstreamMarginReadModel) []contracts.UpstreamMarginBlockedReason {
	out := make([]contracts.UpstreamMarginBlockedReason, 0, len(model.Claim.BlockedReasons))
	for _, reason := range model.Claim.BlockedReasons {
		if reason != contracts.UpstreamMarginBlockedRevenueUnavailable && reason != contracts.UpstreamMarginBlockedCrossCurrency {
			out = append(out, reason)
		}
	}
	return out
}
