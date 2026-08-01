// Package inventorymonitor turns the inventory snapshot into edge-triggered
// operator alerts. The snapshot itself remains the source of truth; this
// runner only remembers the last state seen by this process so a low-stock
// condition is not emitted on every sweep.
package inventorymonitor

import (
	"context"
	"log"
	"time"

	"e2m.local/core/internal/store"
)

const DefaultInterval = time.Minute

type Event struct {
	PoolID    string `json:"pool_id"`
	State     string `json:"state"`
	Available int    `json:"available"`
	Threshold int    `json:"threshold"`
}

type EventSink func(context.Context, Event)

type Runner struct {
	store    store.UpstreamLifecycleStore
	interval time.Duration
	sink     EventSink
	lastLow  map[string]bool
}

func New(st store.UpstreamLifecycleStore, interval time.Duration, sink EventSink) *Runner {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Runner{store: st, interval: interval, sink: sink, lastLow: make(map[string]bool)}
}

func (r *Runner) Run(ctx context.Context) {
	if r == nil || r.store == nil {
		return
	}
	r.sweep(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sweep(ctx)
		}
	}
}

func (r *Runner) sweep(ctx context.Context) {
	snapshot, err := r.store.GetUpstreamInventory(ctx, "")
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("inventory monitor: read snapshot failed: %v", err)
		}
		return
	}
	seen := make(map[string]struct{}, len(snapshot.Pools))
	for _, pool := range snapshot.Pools {
		seen[pool.PoolID] = struct{}{}
		low := pool.SafetyStockThreshold > 0 && pool.Available < pool.SafetyStockThreshold
		previous, known := r.lastLow[pool.PoolID]
		r.lastLow[pool.PoolID] = low
		// Emit an existing low condition on startup, and thereafter only state
		// transitions. A healthy startup is deliberately quiet.
		if (!known && !low) || (known && previous == low) {
			continue
		}
		if r.sink != nil {
			state := "recovered"
			if low {
				state = "low"
			}
			r.sink(ctx, Event{PoolID: pool.PoolID, State: state, Available: pool.Available, Threshold: pool.SafetyStockThreshold})
		}
	}
	for poolID := range r.lastLow {
		if _, ok := seen[poolID]; !ok {
			delete(r.lastLow, poolID)
		}
	}
}
