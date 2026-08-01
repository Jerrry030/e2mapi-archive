// Package upstreamretention removes expired raw upstream-intelligence history
// without weakening the longer-lived change/evidence ledger.
package upstreamretention

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"e2m.local/core/internal/store"
)

const (
	DefaultInterval         = 24 * time.Hour
	DefaultHistoryRetention = 90 * 24 * time.Hour
)

type RetentionStore interface {
	ListUpstreamIntelligenceRetentionOwners(context.Context, time.Time, int64, int) ([]int64, error)
	PruneUpstreamIntelligenceHistory(context.Context, int64, time.Time, int) (store.UpstreamIntelligenceRetentionResult, error)
}

type Runner struct {
	store            RetentionStore
	interval         time.Duration
	historyRetention time.Duration
	ownerPageSize    int
	batchSize        int
	now              func() time.Time
}

type Option func(*Runner)

func WithHistoryRetention(retention time.Duration) Option {
	return func(r *Runner) {
		if retention >= DefaultHistoryRetention {
			r.historyRetention = retention
		}
	}
}

func WithBatchSizes(ownerPageSize, batchSize int) Option {
	return func(r *Runner) {
		if ownerPageSize > 0 {
			r.ownerPageSize = min(ownerPageSize, store.MaxUpstreamIntelligenceRetentionOwnerPage)
		}
		if batchSize > 0 {
			r.batchSize = min(batchSize, store.MaxUpstreamIntelligenceRetentionBatchSize)
		}
	}
}

// WithClock is intended for deterministic tests. Production uses UTC wall
// time and computes one shared cutoff for the entire owner sweep.
func WithClock(now func() time.Time) Option {
	return func(r *Runner) {
		if now != nil {
			r.now = now
		}
	}
}

func New(st RetentionStore, interval time.Duration, options ...Option) *Runner {
	if interval <= 0 {
		interval = DefaultInterval
	}
	runner := &Runner{
		store: st, interval: interval, historyRetention: DefaultHistoryRetention,
		ownerPageSize: store.DefaultUpstreamIntelligenceRetentionOwnerPage,
		batchSize:     store.DefaultUpstreamIntelligenceRetentionBatchSize,
		now:           func() time.Time { return time.Now().UTC() },
	}
	for _, option := range options {
		option(runner)
	}
	return runner
}

func (r *Runner) Run(ctx context.Context) {
	if r == nil || r.store == nil {
		return
	}
	r.runAndLog(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runAndLog(ctx)
		}
	}
}

func (r *Runner) runAndLog(ctx context.Context) {
	if err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
		log.Printf("upstream intelligence retention: sweep incomplete: %v", err)
	}
}

// RunOnce gives every eligible owner at most one bounded prune transaction.
// A large owner therefore cannot starve smaller owners. Failed owners are
// reported but do not block later owners and are retried by the next sweep.
func (r *Runner) RunOnce(ctx context.Context) error {
	if r == nil || r.store == nil || r.now == nil || r.historyRetention < DefaultHistoryRetention {
		return errors.New("upstream intelligence retention: runner is not configured")
	}
	cutoff := r.now().UTC().Add(-r.historyRetention)
	var sweepErr error
	var afterUserID int64
	for {
		owners, err := r.store.ListUpstreamIntelligenceRetentionOwners(ctx, cutoff, afterUserID, r.ownerPageSize)
		if err != nil {
			return errors.Join(sweepErr, fmt.Errorf("list owners after %d: %w", afterUserID, err))
		}
		if len(owners) == 0 {
			return sweepErr
		}
		for _, userID := range owners {
			if userID <= afterUserID {
				return errors.Join(sweepErr, fmt.Errorf("owner cursor did not advance after %d", afterUserID))
			}
			afterUserID = userID
			if _, err := r.store.PruneUpstreamIntelligenceHistory(ctx, userID, cutoff, r.batchSize); err != nil {
				if ctx.Err() != nil {
					return errors.Join(sweepErr, ctx.Err())
				}
				sweepErr = errors.Join(sweepErr, fmt.Errorf("owner %d: %w", userID, err))
			}
		}
		if len(owners) < r.ownerPageSize {
			return sweepErr
		}
	}
}
