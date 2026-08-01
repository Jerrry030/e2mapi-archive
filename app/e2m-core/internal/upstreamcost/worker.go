package upstreamcost

import (
	"context"
	"fmt"
	"log"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

type Worker struct {
	store    store.UpstreamCostAttributionJobStore
	workerID string
	interval time.Duration
	lease    time.Duration
}

func NewWorker(st store.UpstreamCostAttributionJobStore, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = time.Second
	}
	return &Worker{
		store: st, workerID: fmt.Sprintf("upstream-cost-%d", time.Now().UnixNano()),
		interval: interval, lease: 30 * time.Second,
	}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		w.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) drain(ctx context.Context) {
	for {
		job, claimed, err := w.store.ClaimUpstreamCostAttributionJob(ctx, w.workerID, w.lease)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("upstream cost attribution: claim failed: %v", err)
			}
			return
		}
		if !claimed {
			return
		}
		w.process(ctx, job)
	}
}

// ProcessOne claims and processes at most one job. It is intentionally small
// so crash/replay and retry semantics can be exercised without a ticker.
func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	job, claimed, err := w.store.ClaimUpstreamCostAttributionJob(ctx, w.workerID, w.lease)
	if err != nil || !claimed {
		return claimed, err
	}
	w.process(ctx, job)
	return true, nil
}

func (w *Worker) process(ctx context.Context, job store.UpstreamCostAttributionJob) {
	links, offers, err := w.store.LoadUpstreamCostAttributionEvidence(ctx, job)
	if err != nil {
		w.retry(ctx, job, "evidence_read_failed")
		return
	}
	facts, err := AttributeObservation(AttributionInput{
		OwnerID: job.UserID, InstanceID: job.InstanceID, ChannelID: job.ChannelID,
		UsageObservationID: job.UsageObservationID,
		Observation: contracts.ConnectorChannelObservation{
			ObservationID: job.UsageObservationID, Model: job.ModelKey, ObservedAt: job.OccurredAt,
			CostUsage: &contracts.ConnectorCostUsage{
				InputTokens: job.InputTokens, OutputTokens: job.OutputTokens,
				CachedInputTokens: job.CachedInputTokens, RequestCount: job.RequestCount,
				GroupKey: optionalGroupKey(job.GroupKey),
			},
		},
		Links: links, Offers: offers, CalculationVersion: job.CalculationVersion,
	})
	if err != nil {
		// Input was validated before the job entered the durable outbox. A
		// deterministic mismatch is operationally visible but left lease-expiry
		// recoverable; it must never be converted into guessed financial facts.
		w.retry(ctx, job, "ledger_write_failed")
		return
	}
	if _, _, err := w.store.CompleteUpstreamCostAttributionJob(ctx, job, facts); err != nil {
		w.retry(ctx, job, "ledger_write_failed")
	}
}

func (w *Worker) retry(ctx context.Context, job store.UpstreamCostAttributionJob, errorCode string) {
	if _, err := w.store.RetryUpstreamCostAttributionJob(ctx, job, errorCode, attributionRetryDelay(job.Attempts)); err != nil && ctx.Err() == nil {
		log.Printf("upstream cost attribution: retry transition failed: %v", err)
	}
}

func optionalGroupKey(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func attributionRetryDelay(attempt int64) time.Duration {
	switch attempt {
	case 0, 1:
		return time.Second
	case 2:
		return 5 * time.Second
	case 3:
		return 30 * time.Second
	default:
		return 5 * time.Minute
	}
}
