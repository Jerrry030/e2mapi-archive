package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
)

// The stats window is bounded by the store's retention; asking for more than
// is kept would silently under-report, so the bound is enforced here.
const (
	defaultStatsWindowMinutes = 60
	maxStatsWindowMinutes     = 14 * 24 * 60
)

// platformUpstreamStatsResponse aggregates a channel's reliability buckets
// over the requested window. Rates and averages are pointers so a window
// without samples reports absent values instead of a fabricated zero: an
// empty window means "no opinion", never "0% success" or "0ms latency".
type platformUpstreamStatsResponse struct {
	ChannelID       string                               `json:"channel_id"`
	WindowMinutes   int                                  `json:"window_minutes"`
	BucketSeconds   int                                  `json:"bucket_seconds"`
	Requests        int64                                `json:"requests"`
	Failures        int64                                `json:"failures"`
	SuccessRate     *float64                             `json:"success_rate,omitempty"`
	AvgTTFTMS       *int64                               `json:"avg_ttft_ms,omitempty"`
	AvgDurationMS   *int64                               `json:"avg_duration_ms,omitempty"`
	TTFTSamples     int64                                `json:"ttft_samples"`
	DurationSamples int64                                `json:"duration_samples"`
	Buckets         []contracts.SupplyChannelStatsBucket `json:"buckets"`
}

func (s *Server) handleGetPlatformUpstreamStats(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	channelID := strings.TrimSpace(r.PathValue("id"))
	if _, err := s.store.GetUpstreamChannel(r.Context(), channelID); err != nil {
		writePlatformStoreError(w, err)
		return
	}
	windowMinutes := defaultStatsWindowMinutes
	if raw := strings.TrimSpace(r.URL.Query().Get("window_minutes")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxStatsWindowMinutes {
			writeError(w, http.StatusBadRequest, "validation_failed",
				"window_minutes must be an integer between 1 and "+strconv.Itoa(maxStatsWindowMinutes))
			return
		}
		windowMinutes = parsed
	}
	since := time.Now().UTC().Add(-time.Duration(windowMinutes) * time.Minute)
	buckets, err := s.store.ListSupplyChannelStats(r.Context(), channelID, contracts.SupplyStatsBucketStart(since))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	response := platformUpstreamStatsResponse{
		ChannelID:     channelID,
		WindowMinutes: windowMinutes,
		BucketSeconds: int(contracts.SupplyStatsBucket / time.Second),
		Buckets:       buckets,
	}
	var ttftSum, durationSum int64
	for _, bucket := range buckets {
		response.Requests += bucket.Requests
		response.Failures += bucket.Failures
		response.TTFTSamples += bucket.TTFTSamples
		response.DurationSamples += bucket.DurationSamples
		ttftSum += bucket.TTFTSumMS
		durationSum += bucket.DurationSumMS
	}
	if response.Requests > 0 {
		rate := float64(response.Requests-response.Failures) / float64(response.Requests)
		response.SuccessRate = &rate
	}
	if response.TTFTSamples > 0 {
		avg := ttftSum / response.TTFTSamples
		response.AvgTTFTMS = &avg
	}
	if response.DurationSamples > 0 {
		avg := durationSum / response.DurationSamples
		response.AvgDurationMS = &avg
	}
	writeJSON(w, http.StatusOK, response)
}
