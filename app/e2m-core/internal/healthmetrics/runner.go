package healthmetrics

import (
	"context"
	"log"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// DefaultRecomputeInterval is how often the runner refreshes snapshots when no
// interval is configured. One minute matches the 1m window: it keeps the fast
// window current so the strategy engine and auto-switch never decide on stale
// health.
const DefaultRecomputeInterval = time.Minute

// Runner is the background cadence for the metrics layer. Each tick it recomputes
// windowed snapshots for every live pool's channels, so a channel that has gone
// idle decays to HealthUnknown instead of holding a stale healthy verdict, and a
// channel receiving traffic keeps a fresh success-rate / TTFT / duration picture
// even between ingestion bursts. All real aggregation lives in the Service, which
// stays independently testable without the ticker.
//
// This is the "eyes" half of the closed loop: ingestion (the passive-observation
// intake) appends facts; this runner turns them into the snapshots the auto-switch
// "brain" reads. Recompute-after-ingest handles low-volume freshness; this ticker
// handles idle decay and high-volume pools uniformly.
type Runner struct {
	store    store.Store
	svc      *Service
	interval time.Duration
}

// NewRunner builds a Runner over a store and metrics service. A non-positive
// interval falls back to DefaultRecomputeInterval.
func NewRunner(st store.Store, svc *Service, interval time.Duration) *Runner {
	if interval <= 0 {
		interval = DefaultRecomputeInterval
	}
	return &Runner{store: st, svc: svc, interval: interval}
}

// Run ticks until ctx is cancelled. It never returns an error: a sweep failure is
// logged and the loop continues, so a transient store hiccup does not kill the
// metrics cadence (and with it the auto-switch loop's freshness).
func (r *Runner) Run(ctx context.Context) {
	log.Printf("health-metrics runner started (interval=%s)", r.interval)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("health-metrics runner stopped: %v", ctx.Err())
			return
		case <-ticker.C:
			r.sweep(ctx)
		}
	}
}

// sweep recomputes snapshots for every live pool once. Retired pools are skipped
// (their channels are dead and would only churn unknown snapshots); a per-pool
// failure is logged and the sweep continues so one bad pool cannot stall the rest.
func (r *Runner) sweep(ctx context.Context) {
	pools, err := r.store.ListUpstreamPools(ctx)
	if err != nil {
		log.Printf("health-metrics: list pools: %v", err)
		return
	}
	for _, p := range pools {
		if p.Status == contracts.UpstreamPoolRetired {
			continue
		}
		if _, err := r.svc.RecomputePool(ctx, p.ID); err != nil {
			log.Printf("health-metrics: recompute pool %s: %v", p.ID, err)
		}
	}
}
