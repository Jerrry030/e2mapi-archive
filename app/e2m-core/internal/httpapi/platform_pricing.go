package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/pricing"
)

// platformRateMultiplierLabel stores a group's sell-price multiplier in basis
// points on the pool labels (10000 = 1.0x). Labels are operator-internal; the
// customer-visible outcome is the materialized upstream price.
const platformRateMultiplierLabel = "e2m.rate_multiplier_bps"

const defaultRateMultiplierBps = int64(10_000)

// SetPricing wires the base price table service. Nil keeps base-table pricing
// disabled: groups then require explicit per-upstream prices as before.
func (s *Server) SetPricing(service *pricing.Service) { s.pricing = service }

func poolRateMultiplierBps(pool contracts.UpstreamPool) int64 {
	raw := strings.TrimSpace(pool.Labels[platformRateMultiplierLabel])
	if raw == "" {
		return defaultRateMultiplierBps
	}
	bps, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || bps <= 0 {
		return defaultRateMultiplierBps
	}
	return bps
}

// parseRateMultiplier accepts a decimal like "1.25" with at most four decimal
// places, between 0.0001 and 100, and returns basis points.
func parseRateMultiplier(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if raw == "" || len(parts) > 2 || parts[0] == "" || len(parts[0]) > 3 {
		return 0, false
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return 0, false
	}
	fractionRaw := ""
	if len(parts) == 2 {
		fractionRaw = parts[1]
		if fractionRaw == "" || len(fractionRaw) > 4 {
			return 0, false
		}
	}
	fractionRaw += strings.Repeat("0", 4-len(fractionRaw))
	fraction, err := strconv.ParseInt(fractionRaw, 10, 64)
	if err != nil || fraction < 0 {
		return 0, false
	}
	bps := whole*10_000 + fraction
	if bps <= 0 || bps > 100*10_000 {
		return 0, false
	}
	return bps, true
}

func formatRateMultiplierBps(bps int64) string {
	whole := bps / 10_000
	fraction := bps % 10_000
	out := strconv.FormatInt(whole, 10)
	if fraction > 0 {
		digits := strconv.FormatInt(fraction+10_000, 10)[1:]
		digits = strings.TrimRight(digits, "0")
		out += "." + digits
	}
	return out
}

// fillPricesFromBaseTable materializes endpoint sell prices from the base
// table. It fails closed when any model is unpriced or when models resolve to
// different converted prices (the V1 one-price-per-upstream rule). Supplier
// cost stays zero: the base table knows sell-side list prices only.
func fillPricesFromBaseTable(endpoint *contracts.SupplyChannelEndpoint, models []string, service *pricing.Service, multiplierBps int64) string {
	if len(models) == 0 {
		return "prices are required when the upstream declares no models"
	}
	var input, output int64
	for index, model := range models {
		quote, ok := service.QuoteCNY(model, multiplierBps)
		if !ok {
			return "the base price table has no entry for model " + model + "; provide explicit prices"
		}
		if index == 0 {
			input, output = quote.InputMicrosPerMillion, quote.OutputMicrosPerMillion
			continue
		}
		if quote.InputMicrosPerMillion != input || quote.OutputMicrosPerMillion != output {
			return "models on this upstream resolve to different base prices; V1 requires one price per upstream — provide explicit prices or split the upstream by model family"
		}
	}
	if input <= 0 || output <= 0 {
		return "base table resolved a non-positive price; provide explicit prices"
	}
	endpoint.InputPriceMicrosPerMillion = input
	endpoint.OutputPriceMicrosPerMillion = output
	endpoint.InputSupplierMicrosPerMillion = 0
	endpoint.OutputSupplierMicrosPerMillion = 0
	return ""
}

// handleGetPlatformPricingPreview resolves a model's sell price from the base
// table at a group's multiplier so operators can see (and prefill) prices
// before creating an upstream.
func (s *Server) handleGetPlatformPricingPreview(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	if !s.pricing.Enabled() {
		writeError(w, http.StatusNotFound, "pricing_disabled", "base price table pricing is not configured (set E2M_USD_TO_CNY_RATE)")
		return
	}
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if model == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "model is required")
		return
	}
	multiplier := defaultRateMultiplierBps
	if groupID := strings.TrimSpace(r.URL.Query().Get("group_id")); groupID != "" {
		group, err := s.platformGroup(r, groupID)
		if err != nil {
			writePlatformStoreError(w, err)
			return
		}
		multiplier = poolRateMultiplierBps(group)
	}
	quote, ok := s.pricing.QuoteCNY(model, multiplier)
	if !ok {
		writeError(w, http.StatusNotFound, "model_not_priced", "the base price table has no entry for this model")
		return
	}
	writeJSON(w, http.StatusOK, quote)
}
