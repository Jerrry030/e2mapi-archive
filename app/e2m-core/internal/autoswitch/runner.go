package autoswitch

import (
	"context"
	"log"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// DefaultInterval is how often the runner sweeps plans when no interval is set.
const DefaultInterval = time.Minute

// Runner drives the orchestrator on a cadence: each tick it evaluates every
// published plan for a needed switch and advances any observing decision whose
// window has elapsed. It is the background loop that makes the closed loop run
// in production; all real work stays in the Orchestrator, which is independently
// testable without the ticker.
type Runner struct {
	store    store.Store
	orch     *Orchestrator
	interval time.Duration
}

// NewRunner builds a Runner. A non-positive interval falls back to DefaultInterval.
func NewRunner(st store.Store, orch *Orchestrator, interval time.Duration) *Runner {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Runner{store: st, orch: orch, interval: interval}
}

// Run ticks until ctx is cancelled. It never returns an error: a sweep failure
// is logged and the loop continues, so a transient store hiccup does not kill
// the auto-switch loop.
func (r *Runner) Run(ctx context.Context) {
	log.Printf("auto-switch runner started (interval=%s)", r.interval)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("auto-switch runner stopped: %v", ctx.Err())
			return
		case <-ticker.C:
			r.sweep(ctx)
		}
	}
}

// sweep repairs abandoned apply claims independently of normal plan
// evaluation, then evaluates and observes every published plan once. Keeping
// repair as its own pass ensures a plan suspended during an apply can still be
// drained and its transient decision terminated.
func (r *Runner) sweep(ctx context.Context) {
	if err := r.orch.RecoverDueCircuits(ctx); err != nil {
		log.Printf("auto-switch: recover quality circuits: %v", err)
	}
	plans := r.routePlans(ctx)
	for _, plan := range plans {
		if plan.Status != contracts.RoutePlanPublished && plan.Status != contracts.RoutePlanSuspended {
			continue
		}
		if err := r.orch.repairExpiredApplyingDecisions(ctx, plan, nil); err != nil {
			log.Printf("auto-switch: repair expired decisions for plan %s: %v", plan.ID, err)
		}
	}
	for _, plan := range plans {
		if plan.Status != contracts.RoutePlanPublished {
			continue
		}
		if _, err := r.orch.Evaluate(ctx, plan.ID); err != nil {
			log.Printf("auto-switch: evaluate plan %s: %v", plan.ID, err)
		}
		if _, err := r.orch.ObservePending(ctx, plan.ID); err != nil {
			log.Printf("auto-switch: observe plan %s: %v", plan.ID, err)
		}
	}
}

// routePlans enumerates every route plan. An empty user filter is platform
// scope, so the store can return all plans directly.
func (r *Runner) routePlans(ctx context.Context) []contracts.RoutePlan {
	plans, err := r.store.ListRoutePlans(ctx, 0)
	if err != nil {
		log.Printf("auto-switch: list plans: %v", err)
		return nil
	}
	return plans
}
