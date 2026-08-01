package recommendationrollout

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
)

type runnerControlStore struct {
	*workerStoreFixture
	rollouts   []contracts.RecommendationRollout
	operations map[string][]contracts.RecommendationRolloutOperation
	enqueued   []contracts.RecommendationRolloutOperationAction
}

type runnerAdvancer struct{}

func (runnerAdvancer) Advance(context.Context, int64, string) (MutationResult, error) {
	return MutationResult{}, nil
}

func (s *runnerControlStore) ListRecommendationRollouts(_ context.Context, filter contracts.RecommendationRolloutFilter) ([]contracts.RecommendationRollout, error) {
	result := make([]contracts.RecommendationRollout, 0)
	for _, rollout := range s.rollouts {
		if filter.Status == "" || rollout.State.Status == filter.Status {
			result = append(result, rollout)
		}
	}
	return result, nil
}

func (s *runnerControlStore) ListRecommendationRolloutOperations(_ context.Context, rolloutID string) ([]contracts.RecommendationRolloutOperation, error) {
	return append([]contracts.RecommendationRolloutOperation(nil), s.operations[rolloutID]...), nil
}

func (s *runnerControlStore) EnqueueRecommendationRolloutOperation(_ context.Context, rolloutID string, _ int64, state contracts.RecommendationRolloutState, action contracts.RecommendationRolloutOperationAction, target contracts.RecommendationRolloutStage) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, error) {
	s.enqueued = append(s.enqueued, action)
	return contracts.RecommendationRollout{State: state}, contracts.RecommendationRolloutOperation{RolloutID: rolloutID, Action: action, TargetStage: target}, nil
}

func TestRunnerQueuesRollbackAfterForwardFailureAndBoundsRetry(t *testing.T) {
	now := workerNow()
	rollout, _, _ := workerFixture(10, contracts.RecommendationRolloutOperationApplyStage)
	rollout.State.Status = contracts.RecommendationRolloutRollbackRequired
	rollout.State.PendingStage = 0
	rollout.State.RollbackReasons = []contracts.RecommendationRolloutBlockReason{contracts.RecommendationRolloutBlockedApplyFailed}
	base := &runnerControlStore{
		workerStoreFixture: &workerStoreFixture{}, rollouts: []contracts.RecommendationRollout{rollout},
		operations: map[string][]contracts.RecommendationRolloutOperation{rollout.State.ID: {{
			Action: contracts.RecommendationRolloutOperationApplyStage, Status: contracts.RecommendationRolloutOperationFailed, UpdatedAt: now,
		}}},
	}
	runner := &Runner{store: base, advancer: runnerAdvancer{}, interval: time.Second, now: func() time.Time { return now }}
	runner.now = func() time.Time { return now }
	runner.RunOnce(context.Background())
	if len(base.enqueued) != 1 || base.enqueued[0] != contracts.RecommendationRolloutOperationRollback {
		t.Fatalf("rollback enqueue=%v", base.enqueued)
	}

	failed := make([]contracts.RecommendationRolloutOperation, DefaultRollbackRetryLimit)
	for index := range failed {
		failed[index] = contracts.RecommendationRolloutOperation{
			Action: contracts.RecommendationRolloutOperationRollback, Status: contracts.RecommendationRolloutOperationFailed, UpdatedAt: now.Add(-time.Hour),
		}
	}
	base.enqueued = nil
	base.operations[rollout.State.ID] = failed
	runner.RunOnce(context.Background())
	if len(base.enqueued) != 0 {
		t.Fatalf("retry limit ignored: %v", base.enqueued)
	}
}

func TestRollbackRetryDelayIsBounded(t *testing.T) {
	if got := rollbackRetryDelay(0); got != 0 {
		t.Fatalf("zero failures delay=%s", got)
	}
	if got := rollbackRetryDelay(100); got != DefaultRollbackRetryMax {
		t.Fatalf("max delay=%s", got)
	}
}
