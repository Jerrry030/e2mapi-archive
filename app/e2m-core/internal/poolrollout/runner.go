// Package poolrollout executes durable customer-service rollout operations.
// Rule writes only enqueue desired work; this runner owns the gateway side
// effects through the existing publish engine and finishes each leased row
// with a durable success/failure result.
package poolrollout

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

const (
	DefaultInterval = 5 * time.Second
	defaultLease    = 75 * time.Second
	defaultBatch    = 25
)

type Persistence interface {
	store.PoolRolloutStore
	store.Store
}

type Publisher interface {
	Apply(context.Context, string) (contracts.ReconcilePlan, error)
	Rollback(context.Context, string) (contracts.ReconcilePlan, error)
}

type Runner struct {
	store     Persistence
	publisher Publisher
	workerID  string
	interval  time.Duration
	lease     time.Duration
	batch     int
}

func New(st Persistence, publisher Publisher, interval time.Duration) *Runner {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Runner{
		store: st, publisher: publisher, workerID: fmt.Sprintf("pool-rollout-%d", time.Now().UnixNano()),
		interval: interval, lease: defaultLease, batch: defaultBatch,
	}
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
	if r == nil || r.store == nil || r.publisher == nil {
		return
	}
	pools, err := r.store.ListUpstreamPools(ctx)
	if err != nil {
		log.Printf("pool rollout: discovery failed: %v", err)
		return
	}
	for _, pool := range pools {
		if _, err := r.store.EnsurePoolRolloutOperations(ctx, pool.ID); err != nil {
			log.Printf("pool rollout: discover pool %s failed: %v", pool.ID, err)
			return
		}
	}
	if err := r.store.ReconcileUserDeactivations(ctx); err != nil {
		log.Printf("pool rollout: reconcile user deactivation failed: %v", err)
		return
	}
	limit := r.batch
	if limit <= 0 {
		limit = defaultBatch
	}
	for i := 0; i < limit && ctx.Err() == nil; i++ {
		operation, claimed, err := r.store.ClaimPoolRolloutOperation(ctx, r.workerID, r.lease)
		if err != nil {
			log.Printf("pool rollout: claim failed: %v", err)
			return
		}
		if !claimed {
			if err := r.store.ReconcileUserDeactivations(ctx); err != nil {
				log.Printf("pool rollout: finalize user deactivation failed: %v", err)
			}
			return
		}
		processed := r.process(ctx, operation)
		if err := r.store.ReconcileUserDeactivations(ctx); err != nil {
			log.Printf("pool rollout: reconcile user deactivation after %s failed: %v", operation.ID, err)
			return
		}
		if !processed {
			// A failed operation is retryable, but not again in the same sweep;
			// otherwise one broken gateway could consume the entire batch in a
			// tight loop. The next ticker/startup sweep retries it durably.
			return
		}
	}
}

func (r *Runner) process(ctx context.Context, operation contracts.PoolRolloutOperation) bool {
	status := contracts.PoolRolloutOperationSucceeded
	err := r.execute(ctx, &operation)
	if errors.Is(err, errOperationSuperseded) {
		return true
	}
	if err != nil {
		status = contracts.PoolRolloutOperationFailed
	}
	errorMessage := ""
	if err != nil {
		errorMessage = err.Error()
	}
	if _, completeErr := r.store.CompletePoolRolloutOperation(
		ctx, operation.ID, r.workerID, operation.Version, status, errorMessage,
	); completeErr != nil && !errors.Is(completeErr, store.ErrConflict) {
		log.Printf("pool rollout: complete %s failed: %v", operation.ID, completeErr)
		return false
	}
	return err == nil
}

func (r *Runner) execute(ctx context.Context, operation *contracts.PoolRolloutOperation) error {
	current, err := r.store.GetPoolRolloutOperation(ctx, operation.ID)
	if err != nil || current.Version != operation.Version || current.Status != contracts.PoolRolloutOperationRunning ||
		current.LeaseOwner != r.workerID || current.LeaseUntil == nil || !current.LeaseUntil.After(time.Now().UTC()) {
		if err == nil {
			err = store.ErrConflict
		}
		return err
	}
	resolution, err := r.store.ResolvePoolRollout(ctx, operation.PoolID, operation.UserID, operation.InstanceID)
	if err != nil {
		return err
	}
	desiredAction := contracts.PoolRolloutOperationPublish
	if !resolution.Enabled {
		desiredAction = contracts.PoolRolloutOperationDrain
	}
	if store.PoolRolloutOperationFingerprint(resolution, desiredAction, operation.PlanID) != operation.DesiredFingerprint {
		return r.superseded(ctx, operation, "effective rollout rule changed")
	}
	if operation.PlanID == "" {
		if operation.Action == contracts.PoolRolloutOperationDrain {
			return nil
		}
		return r.superseded(ctx, operation, "route plan is not ready; onboarding owns first publish")
	}
	plan, err := r.store.GetRoutePlan(ctx, operation.PlanID)
	if err != nil {
		return err
	}
	if plan.PoolID != operation.PoolID || plan.UserID != operation.UserID || plan.InstanceID != operation.InstanceID {
		return store.ErrConflict
	}
	if operation.Action == contracts.PoolRolloutOperationPublish && plan.Labels["managed_by"] == "e2m-onboarding" {
		return r.superseded(ctx, operation, "onboarding owns publish for managed route plans")
	}
	if operation.Action == contracts.PoolRolloutOperationPublish {
		if err := r.guardPublish(ctx, operation); err != nil {
			return r.supersededAfterFenceLoss(ctx, operation, "pool is no longer active")
		}
	}
	actorCtx := contracts.WithActor(ctx, contracts.Actor{Type: "system", ID: "e2m-pool-rollout"})
	actorCtx = contracts.WithReconcileTrigger(actorCtx, contracts.ReconcileTriggerAuto)
	actorCtx = contracts.WithReconcileSideEffectGuard(actorCtx, func(guardCtx context.Context) error {
		var renewed contracts.PoolRolloutOperation
		var guardErr error
		if operation.Action == contracts.PoolRolloutOperationPublish {
			renewed, guardErr = r.store.GuardPoolRolloutPublish(guardCtx, operation.ID, r.workerID, operation.Version, r.lease)
		} else {
			renewed, guardErr = r.store.RenewPoolRolloutOperation(guardCtx, operation.ID, r.workerID, operation.Version, r.lease)
		}
		if guardErr == nil {
			*operation = renewed
		}
		if guardErr != nil {
			return guardErr
		}
		currentResolution, resolveErr := r.store.ResolvePoolRollout(
			guardCtx, operation.PoolID, operation.UserID, operation.InstanceID,
		)
		if resolveErr != nil {
			return resolveErr
		}
		currentAction := contracts.PoolRolloutOperationPublish
		if !currentResolution.Enabled {
			currentAction = contracts.PoolRolloutOperationDrain
		}
		if store.PoolRolloutOperationFingerprint(currentResolution, currentAction, operation.PlanID) != operation.DesiredFingerprint {
			return errRolloutDesiredChanged
		}
		if operation.Action != contracts.PoolRolloutOperationPublish {
			return nil
		}
		currentPlan, planErr := r.store.GetRoutePlan(guardCtx, operation.PlanID)
		if planErr != nil {
			return planErr
		}
		if currentPlan.Labels["managed_by"] == "e2m-onboarding" {
			// Managed-plan ownership is checked again immediately before every
			// gateway mutation, closing the discovery/claim race.
			return errOnboardingOwnsPublish
		}
		return nil
	})
	switch operation.Action {
	case contracts.PoolRolloutOperationDrain:
		if plan.Status == contracts.RoutePlanDraft {
			// A failed/partial first publish can leave external bindings behind
			// while the plan is still draft. Promote only the local lifecycle so
			// Rollback can run its reversible drain diff over those receipts.
			plan.Status = contracts.RoutePlanPublished
			plan, err = r.store.UpdateRoutePlan(ctx, plan)
			if err != nil {
				return err
			}
		}
		if plan.Status != contracts.RoutePlanPublished && plan.Status != contracts.RoutePlanSuspended {
			return fmt.Errorf("route plan %s is not published", plan.ID)
		}
		_, err = r.publisher.Rollback(actorCtx, plan.ID)
		if errors.Is(err, errRolloutDesiredChanged) {
			return r.superseded(ctx, operation, "effective rollout rule changed before gateway mutation")
		}
		return err
	case contracts.PoolRolloutOperationPublish:
		if plan.Status == contracts.RoutePlanPublished && plan.Labels["pool_rollout_operation"] == operation.ID &&
			plan.Rollout == resolution.Rollout && plan.RolloutBatchSize == resolution.RolloutBatchSize &&
			plan.RolloutCanaryCount == resolution.RolloutCanaryCount {
			// Apply marks a draft published only after every gateway action and
			// binding receipt succeeds. This is the crash-safe completion marker:
			// reclaiming the operation must not widen a canary a second time.
			return nil
		}
		if err := r.guardPublish(ctx, operation); err != nil {
			return r.supersededAfterFenceLoss(ctx, operation, "pool is no longer active")
		}
		plan.Status = contracts.RoutePlanDraft
		plan.Rollout = resolution.Rollout
		plan.RolloutBatchSize = resolution.RolloutBatchSize
		plan.RolloutCanaryCount = resolution.RolloutCanaryCount
		if plan.Labels == nil {
			plan.Labels = make(map[string]string)
		}
		plan.Labels["pool_rollout_operation"] = operation.ID
		plan, err = r.store.UpdateRoutePlan(ctx, plan)
		if err != nil {
			return err
		}
		_, err = r.publisher.Apply(actorCtx, plan.ID)
		if errors.Is(err, errRolloutDesiredChanged) {
			return r.superseded(ctx, operation, "effective rollout rule changed before gateway mutation")
		}
		if errors.Is(err, errOnboardingOwnsPublish) {
			return r.superseded(ctx, operation, "onboarding owns publish for managed route plans")
		}
		if errors.Is(err, errPoolInactive) {
			return r.supersededAfterFenceLoss(ctx, operation, "pool is no longer active before gateway mutation")
		}
		return err
	default:
		return fmt.Errorf("unsupported rollout action %q", operation.Action)
	}
}

func (r *Runner) guardPublish(ctx context.Context, operation *contracts.PoolRolloutOperation) error {
	guarded, err := r.store.GuardPoolRolloutPublish(ctx, operation.ID, r.workerID, operation.Version, r.lease)
	if err == nil {
		*operation = guarded
	}
	return err
}

func (r *Runner) supersededAfterFenceLoss(ctx context.Context, operation *contracts.PoolRolloutOperation, reason string) error {
	current, err := r.store.GetPoolRolloutOperation(ctx, operation.ID)
	if err == nil && current.Status == contracts.PoolRolloutOperationSuperseded {
		return errOperationSuperseded
	}
	if err != nil {
		return err
	}
	return r.superseded(ctx, operation, reason)
}

// superseded closes a stale claimed operation without performing its external
// side effect. process must not overwrite that terminal result as succeeded.
func (r *Runner) superseded(ctx context.Context, operation *contracts.PoolRolloutOperation, reason string) error {
	_, err := r.store.CompletePoolRolloutOperation(
		ctx, operation.ID, r.workerID, operation.Version, contracts.PoolRolloutOperationSuperseded, reason,
	)
	if err != nil {
		return err
	}
	return errOperationSuperseded
}

var errOperationSuperseded = errors.New("pool rollout operation superseded")
var errOnboardingOwnsPublish = errors.New("onboarding owns managed plan publish")
var errRolloutDesiredChanged = errors.New("pool rollout desired state changed")
var errPoolInactive = errors.New("pool is no longer active")
