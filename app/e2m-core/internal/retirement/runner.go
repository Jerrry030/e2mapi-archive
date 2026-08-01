package retirement

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// ReconcileEngine supplies both retirement phases. Rollback performs the
// reversible drain while the pool is in maintenance. Apply runs only after the
// pool is durably retired and queues final-generation deferred deletes.
type ReconcileEngine interface {
	Rollback(context.Context, string) (contracts.ReconcilePlan, error)
	Apply(context.Context, string) (contracts.ReconcilePlan, error)
}

// Runner drains one durable pool-retirement job. Each plan is claimed before
// the external gateway mutation and completed afterward. Failed items stay
// retryable; the pool becomes retired only after every item succeeded.
type Runner struct {
	store    store.UpstreamLifecycleStore
	publish  ReconcileEngine
	interval time.Duration
	lease    time.Duration
}

const DefaultInterval = 30 * time.Second
const DefaultItemLease = 2 * time.Minute

func New(st store.UpstreamLifecycleStore, publish ReconcileEngine, intervals ...time.Duration) *Runner {
	interval := DefaultInterval
	if len(intervals) > 0 && intervals[0] > 0 {
		interval = intervals[0]
	}
	return &Runner{store: st, publish: publish, interval: interval, lease: DefaultItemLease}
}

// Run continuously resumes durable retirement jobs. The job and item leases
// remain the source of truth, so a process restart or a second Core replica is
// safe: only claimable items are executed.
func (r *Runner) Run(ctx context.Context) {
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
	if r == nil || r.store == nil || r.publish == nil {
		return
	}
	jobs, err := r.store.ListPoolRetirementJobs(ctx, "")
	if err != nil {
		log.Printf("retirement: list jobs failed: %v", err)
		return
	}
	for _, job := range jobs {
		if job.Status == contracts.PoolRetirementCompleted {
			continue
		}
		if _, err := r.RunJob(ctx, job.ID); err != nil && ctx.Err() == nil {
			log.Printf("retirement: job %s deferred: %v", job.ID, err)
		}
	}
}

func (r *Runner) RunJob(ctx context.Context, jobID string) (contracts.PoolRetirementJob, error) {
	if r == nil || r.store == nil || r.publish == nil {
		return contracts.PoolRetirementJob{}, errors.New("retirement: runner is not configured")
	}
	for {
		item, claimed, err := r.store.ClaimPoolRetirementItem(ctx, jobID)
		if err != nil {
			return contracts.PoolRetirementJob{}, err
		}
		if !claimed {
			break
		}
		guardedCtx := contracts.WithReconcileSideEffectGuard(ctx, func(guardCtx context.Context) error {
			renewed, renewErr := r.store.RenewPoolRetirementItem(
				guardCtx, item.JobID, item.PlanID, item.Attempts, r.lease,
			)
			if renewErr == nil {
				item = renewed
			}
			return renewErr
		})
		plan, planErr := r.store.GetRoutePlan(ctx, item.PlanID)
		if planErr != nil {
			return contracts.PoolRetirementJob{}, planErr
		}
		bindings, bindingErr := r.store.ListPublishedBindings(ctx, item.PlanID)
		if bindingErr != nil {
			return contracts.PoolRetirementJob{}, bindingErr
		}
		// A draft without a non-revoked remote binding has never put anything on
		// the gateway. Treat it as already drained instead of calling Rollback,
		// whose lifecycle contract correctly rejects draft plans.
		alreadyDrained := plan.Status == contracts.RoutePlanDraft
		for _, binding := range bindings {
			if binding.State != contracts.BindingRevoked && strings.TrimSpace(binding.RemoteID) != "" {
				alreadyDrained = false
				break
			}
		}
		var runErr error
		if !alreadyDrained {
			if runErr = contracts.RunReconcileSideEffectGuard(guardedCtx); runErr == nil {
				_, runErr = r.publish.Rollback(guardedCtx, item.PlanID)
			}
		}
		message := ""
		if runErr != nil {
			message = runErr.Error()
		}
		if _, err := r.store.CompletePoolRetirementItem(ctx, jobID, item.PlanID, item.Attempts, message); err != nil {
			return contracts.PoolRetirementJob{}, err
		}
		if runErr != nil {
			job, getErr := r.store.GetPoolRetirementJob(ctx, jobID)
			if getErr != nil {
				return contracts.PoolRetirementJob{}, getErr
			}
			return job, runErr
		}
	}
	job, err := r.store.GetPoolRetirementJob(ctx, jobID)
	if err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	if job.Status == contracts.PoolRetirementFinalizing {
		job, err = r.store.FinalizePoolRetirementJob(ctx, jobID)
		if err != nil {
			return contracts.PoolRetirementJob{}, err
		}
	}
	for job.Status == contracts.PoolRetirementCleanup {
		item, claimed, claimErr := r.store.ClaimPoolRetirementCleanupItem(ctx, jobID)
		if claimErr != nil {
			return contracts.PoolRetirementJob{}, claimErr
		}
		if !claimed {
			break
		}
		guardedCtx := contracts.WithReconcileSideEffectGuard(ctx, func(guardCtx context.Context) error {
			renewed, renewErr := r.store.RenewPoolRetirementCleanupItem(
				guardCtx, item.JobID, item.PlanID, item.CleanupAttempts, r.lease,
			)
			if renewErr == nil {
				item = renewed
			}
			return renewErr
		})
		bindings, bindingErr := r.store.ListPublishedBindings(ctx, item.PlanID)
		if bindingErr != nil {
			return contracts.PoolRetirementJob{}, bindingErr
		}
		expectedDeletes := expectedDeprovisions(bindings)
		var cleanupErr error
		if len(expectedDeletes) > 0 {
			cleanupErr = contracts.RunReconcileSideEffectGuard(guardedCtx)
			if cleanupErr == nil {
				result, applyErr := r.publish.Apply(guardedCtx, item.PlanID)
				cleanupErr = applyErr
				if cleanupErr == nil {
					cleanupErr = validateDeprovisions(expectedDeletes, result.Actions)
				}
			}
		}
		message := ""
		if cleanupErr != nil {
			message = cleanupErr.Error()
		}
		job, err = r.store.CompletePoolRetirementCleanupItem(ctx, jobID, item.PlanID, item.CleanupAttempts, message)
		if err != nil {
			return contracts.PoolRetirementJob{}, err
		}
		if cleanupErr != nil {
			return job, cleanupErr
		}
	}
	return r.store.GetPoolRetirementJob(ctx, jobID)
}

type deprovisionIdentity struct {
	channelID string
	remoteID  string
}

func expectedDeprovisions(bindings []contracts.PublishedBinding) map[deprovisionIdentity]struct{} {
	out := make(map[deprovisionIdentity]struct{})
	for _, binding := range bindings {
		if strings.TrimSpace(binding.RemoteID) == "" ||
			binding.AccountOwnership.Normalize() != contracts.GatewayAccountPlatformManaged {
			continue
		}
		out[deprovisionIdentity{channelID: binding.ChannelID, remoteID: binding.RemoteID}] = struct{}{}
	}
	return out
}

func validateDeprovisions(expected map[deprovisionIdentity]struct{}, actions []contracts.ReconcileAction) error {
	if len(expected) == 0 {
		return nil
	}
	for _, action := range actions {
		if action.Type != contracts.ReconcileDeprovision {
			continue
		}
		delete(expected, deprovisionIdentity{channelID: action.ChannelID, remoteID: action.RemoteID})
	}
	if len(expected) != 0 {
		return fmt.Errorf("retirement: final cleanup did not confirm %d required deprovision action(s)", len(expected))
	}
	return nil
}
