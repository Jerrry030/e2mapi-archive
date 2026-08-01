package connector

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const upstreamIntelligenceMetricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// UpstreamIntelligenceMetricsHandler exposes only low-cardinality, durable
// outbox facts. It intentionally has no dependency on the local configuration
// UI and must be mounted on the Agent's independent private metrics listener.
type UpstreamIntelligenceMetricsHandler struct {
	outbox *UpstreamIntelligenceOutbox
	now    func() time.Time
}

func NewUpstreamIntelligenceMetricsHandler(outbox *UpstreamIntelligenceOutbox) http.Handler {
	return &UpstreamIntelligenceMetricsHandler{
		outbox: outbox,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

func (handler *UpstreamIntelligenceMetricsHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", upstreamIntelligenceMetricsContentType)
	response.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if handler == nil || handler.outbox == nil || handler.now == nil {
		http.Error(response, "metrics unavailable", http.StatusServiceUnavailable)
		return
	}
	snapshot, err := handler.outbox.Metrics()
	if err != nil {
		http.Error(response, "metrics collection failed", http.StatusServiceUnavailable)
		return
	}
	now := handler.now().UTC()
	if now.IsZero() {
		http.Error(response, "metrics collection failed", http.StatusServiceUnavailable)
		return
	}

	_, _ = fmt.Fprintln(response, "# HELP e2m_upstream_intelligence_outbox_depth Number of durable upstream intelligence batches awaiting Core acknowledgement.")
	_, _ = fmt.Fprintln(response, "# TYPE e2m_upstream_intelligence_outbox_depth gauge")
	_, _ = fmt.Fprintf(response, "e2m_upstream_intelligence_outbox_depth %d\n", snapshot.Depth)
	_, _ = fmt.Fprintln(response, "# HELP e2m_upstream_intelligence_outbox_oldest_age_seconds Age in seconds of the oldest durable upstream intelligence batch awaiting Core acknowledgement.")
	_, _ = fmt.Fprintln(response, "# TYPE e2m_upstream_intelligence_outbox_oldest_age_seconds gauge")
	if snapshot.Depth == 0 {
		_, _ = fmt.Fprintln(response, "e2m_upstream_intelligence_outbox_oldest_age_seconds 0")
		return
	}
	if snapshot.OldestEnqueuedAt == nil {
		// A legacy v1 non-empty queue has exact depth but no persisted enqueue
		// time. Keep this sample absent instead of fabricating an age.
		return
	}
	age := now.Sub(snapshot.OldestEnqueuedAt.UTC()).Seconds()
	if age < 0 {
		age = 0
	}
	_, _ = fmt.Fprintf(response, "e2m_upstream_intelligence_outbox_oldest_age_seconds %s\n", strconv.FormatFloat(age, 'f', -1, 64))
}
