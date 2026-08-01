package recommendationrollout

import (
	"context"
	"errors"
	"log"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

const (
	DefaultRunnerInterval     = time.Second
	DefaultRollbackRetryBase  = 5 * time.Second
	DefaultRollbackRetryMax   = 5 * time.Minute
	DefaultRollbackRetryLimit = 8
)

// Runner advances observation windows and makes failed forward writes converge
// toward the persisted baseline. It never performs gateway writes itself.
type Runner struct {
	store    store.RecommendationRolloutStore
	advancer interface {
		Advance(context.Context, int64, string) (MutationResult, error)
	}
	interval time.Duration
	now      func() time.Time
}

func NewRunner(controller *Controller, interval time.Duration) (*Runner, error) {
	if controller == nil || controller.store == nil {
		return nil, ErrControllerInvalid
	}
	if interval <= 0 {
		interval = DefaultRunnerInterval
	}
	return &Runner{store: controller.store, advancer: controller, interval: interval, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (r *Runner) Run(ctx context.Context) {
	if r == nil {
		return
	}
	r.RunOnce(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.RunOnce(ctx)
		}
	}
}

func (r *Runner) RunOnce(ctx context.Context) {
	if r == nil || r.store == nil || r.advancer == nil || ctx.Err() != nil {
		return
	}
	observing, err := r.store.ListRecommendationRollouts(ctx, contracts.RecommendationRolloutFilter{
		Status: contracts.RecommendationRolloutObserving, Limit: 500,
	})
	if err != nil {
		log.Printf("recommendation rollout runner: observation scan failed")
		return
	}
	for _, rollout := range observing {
		if ctx.Err() != nil {
			return
		}
		if rollout.State.ObserveUntil == nil || r.now().UTC().Before(*rollout.State.ObserveUntil) {
			continue
		}
		_, advanceErr := r.advancer.Advance(ctx, rollout.State.UserID, rollout.State.ID)
		if advanceErr != nil && !errors.Is(advanceErr, ErrControllerBlocked) && !errors.Is(advanceErr, ErrControllerConflict) {
			log.Printf("recommendation rollout runner: observation transition failed")
		}
	}

	rollbackRequired, err := r.store.ListRecommendationRollouts(ctx, contracts.RecommendationRolloutFilter{
		Status: contracts.RecommendationRolloutRollbackRequired, Limit: 500,
	})
	if err != nil {
		log.Printf("recommendation rollout runner: rollback scan failed")
		return
	}
	for _, rollout := range rollbackRequired {
		if ctx.Err() != nil {
			return
		}
		r.ensureRollback(ctx, rollout)
	}
}

func (r *Runner) ensureRollback(ctx context.Context, rollout contracts.RecommendationRollout) {
	operations, err := r.store.ListRecommendationRolloutOperations(ctx, rollout.State.ID)
	if err != nil {
		return
	}
	failedRollbacks := 0
	for _, operation := range operations {
		if operation.Status == contracts.RecommendationRolloutOperationPending || operation.Status == contracts.RecommendationRolloutOperationRunning {
			return
		}
		if operation.Action == contracts.RecommendationRolloutOperationRollback && operation.Status == contracts.RecommendationRolloutOperationFailed {
			failedRollbacks++
		}
	}
	if failedRollbacks >= DefaultRollbackRetryLimit {
		return
	}
	if len(operations) > 0 && operations[0].Action == contracts.RecommendationRolloutOperationRollback &&
		operations[0].Status == contracts.RecommendationRolloutOperationFailed {
		if r.now().UTC().Before(operations[0].UpdatedAt.Add(rollbackRetryDelay(failedRollbacks))) {
			return
		}
	}
	state := rollout.State
	if len(state.RollbackReasons) == 0 {
		state.RollbackReasons = []contracts.RecommendationRolloutBlockReason{contracts.RecommendationRolloutBlockedRollbackFailed}
	}
	_, _, _ = r.store.EnqueueRecommendationRolloutOperation(ctx, rollout.State.ID, rollout.Version, state,
		contracts.RecommendationRolloutOperationRollback, contracts.RecommendationRolloutStageNone)
}

func rollbackRetryDelay(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	delay := DefaultRollbackRetryBase
	for count := 1; count < failures && delay < DefaultRollbackRetryMax; count++ {
		delay *= 2
		if delay > DefaultRollbackRetryMax {
			delay = DefaultRollbackRetryMax
		}
	}
	return delay
}
