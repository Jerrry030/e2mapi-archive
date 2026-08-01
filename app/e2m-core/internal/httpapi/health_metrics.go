package httpapi

import (
	"net/http"
	"strings"

	"e2m.local/contracts"
)

// handleListChannelHealthSnapshots reads the aggregated snapshots for ops and
// debugging (the console's user-scoped health view is served by the auto-switch
// summary). Snapshots describe platform-curated channels, so this mirrors the
// pools/channels surface: platform admin only.
func (s *Server) handleListChannelHealthSnapshots(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	filter := contracts.ChannelHealthSnapshotFilter{
		ChannelID:  strings.TrimSpace(r.URL.Query().Get("channel_id")),
		InstanceID: strings.TrimSpace(r.URL.Query().Get("instance_id")),
		PoolID:     strings.TrimSpace(r.URL.Query().Get("pool_id")),
		Model:      strings.TrimSpace(r.URL.Query().Get("model")),
		Window:     contracts.HealthWindow(strings.TrimSpace(r.URL.Query().Get("window"))),
	}
	snaps, err := s.store.ListChannelHealthSnapshots(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if snaps == nil {
		snaps = []contracts.ChannelHealthSnapshot{}
	}
	writeJSON(w, http.StatusOK, snaps)
}
