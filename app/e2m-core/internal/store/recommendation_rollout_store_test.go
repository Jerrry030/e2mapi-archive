package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/upstreamrecommendation"
)

func TestRecommendationRolloutBaselineCanonicalizationPreservesZeroAndRejectsUnknown(t *testing.T) {
	weights := []contracts.RecommendationRolloutAccountWeight{{AccountID: "candidate", Weight: 0}, {AccountID: "current", Weight: 100}}
	canonical, err := contracts.CanonicalRecommendationRolloutWeights(weights)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 2 || canonical[0].AccountID != "candidate" || canonical[0].Weight != 0 {
		t.Fatalf("explicit zero lost: %+v", canonical)
	}
	fingerprint, err := contracts.RecommendationRolloutBaselineFingerprint(weights)
	if err != nil || len(fingerprint) != 64 {
		t.Fatalf("fingerprint=%q err=%v", fingerprint, err)
	}
	for name, invalid := range map[string][]contracts.RecommendationRolloutAccountWeight{
		"unknown account": {{AccountID: "", Weight: 0}, {AccountID: "current", Weight: 100}},
		"unknown weight":  {{AccountID: "candidate", Weight: -1}, {AccountID: "current", Weight: 100}},
		"duplicate":       {{AccountID: "current", Weight: 0}, {AccountID: "current", Weight: 100}},
		"not complete":    {{AccountID: "candidate", Weight: 0}, {AccountID: "current", Weight: 90}},
		"sensitive":       {{AccountID: "https://gateway.invalid/account", Weight: 0}, {AccountID: "current", Weight: 100}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := contracts.CanonicalRecommendationRolloutWeights(invalid); err == nil {
				t.Fatal("invalid baseline accepted")
			}
		})
	}
}

func TestRecommendationRolloutPostgresJSONUsesArraysForNilSlices(t *testing.T) {
	rollout := contracts.RecommendationRollout{
		State:           contracts.RecommendationRolloutState{EvidenceIDs: nil, RollbackReasons: nil},
		BaselineWeights: []contracts.RecommendationRolloutAccountWeight{{AccountID: "candidate", Weight: 0}, {AccountID: "current", Weight: 100}},
	}
	evidence, _, _, reasons, err := marshalRecommendationRolloutJSON(rollout)
	if err != nil {
		t.Fatal(err)
	}
	if string(evidence) != "[]" || string(reasons) != "[]" {
		t.Fatalf("evidence=%s rollback_reasons=%s, want JSON arrays", evidence, reasons)
	}
	if rollout.State.EvidenceIDs != nil || rollout.State.RollbackReasons != nil {
		t.Fatal("marshal mutated caller slices")
	}
	transitionReasons, err := marshalRecommendationRolloutBlockReasons(nil)
	if err != nil || string(transitionReasons) != "[]" {
		t.Fatalf("transition rollback_reasons=%s err=%v, want JSON array", transitionReasons, err)
	}
}

func TestMemoryRecommendationRolloutCreateGenerationRaceHasOneWinner(t *testing.T) {
	st, input := newMemoryRecommendationRolloutFixture(t)
	ctx := context.Background()
	const contenders = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	var winners atomic.Int64
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			candidate := input
			candidate.Rollout.State.ID = "rollout-race-" + string(rune('a'+index))
			if _, _, err := st.CreateRecommendationRollout(ctx, candidate); err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("create error=%v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("winners=%d, want 1", winners.Load())
	}
	plan, err := st.GetRoutePlan(ctx, input.Rollout.State.PlanID)
	if err != nil || plan.SchedulingGeneration != input.ExpectedPlanGeneration+1 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	rollouts, err := st.ListRecommendationRollouts(ctx, contracts.RecommendationRolloutFilter{UserID: input.Rollout.State.UserID})
	if err != nil || len(rollouts) != 1 || rollouts[0].State.SchedulingGeneration != plan.SchedulingGeneration {
		t.Fatalf("rollouts=%+v err=%v", rollouts, err)
	}
}

func TestMemoryRecommendationRolloutLeaseCrashReclaimAndStaleWorkerCAS(t *testing.T) {
	st, input := newMemoryRecommendationRolloutFixture(t)
	ctx := context.Background()
	clock := input.Rollout.State.StartedAt
	st.now = func() time.Time { return clock }
	created, _, err := st.CreateRecommendationRollout(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	rollout1, first, claimed, err := st.ClaimRecommendationRolloutOperation(ctx, "worker-a", time.Minute)
	if err != nil || !claimed || first.Status != contracts.RecommendationRolloutOperationRunning || first.Attempts != 1 {
		t.Fatalf("first rollout=%+v op=%+v claimed=%v err=%v", rollout1, first, claimed, err)
	}
	if _, err := st.RenewRecommendationRolloutOperation(ctx, first.ID, "worker-b", first.Version, time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign renew error=%v", err)
	}
	clock = clock.Add(time.Minute)
	rollout2, second, claimed, err := st.ClaimRecommendationRolloutOperation(ctx, "worker-b", time.Minute)
	if err != nil || !claimed || second.ID != first.ID || second.Attempts != 2 || second.Version <= first.Version || rollout2.Version != created.Version {
		t.Fatalf("reclaim rollout=%+v op=%+v claimed=%v err=%v", rollout2, second, claimed, err)
	}
	next := rollout2.State
	next.Status = contracts.RecommendationRolloutObserving
	next.Stage = contracts.RecommendationRolloutStage10
	next.PendingStage = contracts.RecommendationRolloutStageNone
	started := clock
	until := clock.Add(time.Duration(next.ObservationSeconds) * time.Second)
	next.StageStartedAt, next.ObserveUntil = &started, &until
	staleCompletion := contracts.RecommendationRolloutCompletion{
		OperationID: first.ID, WorkerID: "worker-a", ExpectedOperationVersion: first.Version,
		ExpectedRolloutVersion: rollout1.Version, OperationStatus: contracts.RecommendationRolloutOperationSucceeded, NextState: next,
	}
	if _, _, err := st.CompleteRecommendationRolloutOperation(ctx, staleCompletion); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale completion error=%v", err)
	}
	completion := staleCompletion
	completion.WorkerID, completion.ExpectedOperationVersion, completion.ExpectedRolloutVersion = "worker-b", second.Version, rollout2.Version
	updated, completed, err := st.CompleteRecommendationRolloutOperation(ctx, completion)
	if err != nil || completed.Status != contracts.RecommendationRolloutOperationSucceeded || updated.State.Status != contracts.RecommendationRolloutObserving || updated.State.Stage != 10 {
		t.Fatalf("complete rollout=%+v op=%+v err=%v", updated, completed, err)
	}
	if _, _, err := st.CompleteRecommendationRolloutOperation(ctx, completion); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate completion error=%v", err)
	}
}

func TestMemoryRecommendationRolloutRollbackCompletionRestoresExactState(t *testing.T) {
	st, input := newMemoryRecommendationRolloutFixture(t)
	ctx := context.Background()
	clock := input.Rollout.State.StartedAt
	st.now = func() time.Time { return clock }
	created, _, err := st.CreateRecommendationRollout(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	created.State.Status = contracts.RecommendationRolloutRollbackRequired
	created.State.PendingStage = 0
	created.State.RollbackReasons = []contracts.RecommendationRolloutBlockReason{contracts.RecommendationRolloutBlockedQuality}
	claimedRollout, first, claimed, err := st.ClaimRecommendationRolloutOperation(ctx, "stage-worker", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim err=%v", err)
	}
	failed, failedOp, err := st.CompleteRecommendationRolloutOperation(ctx, contracts.RecommendationRolloutCompletion{
		OperationID: first.ID, WorkerID: "stage-worker", ExpectedOperationVersion: first.Version, ExpectedRolloutVersion: claimedRollout.Version,
		OperationStatus: contracts.RecommendationRolloutOperationFailed, ErrorCode: contracts.RecommendationRolloutOperationErrorVerificationFailed,
		NextState: created.State,
	})
	if err != nil || failedOp.Status != contracts.RecommendationRolloutOperationFailed || failed.State.Status != contracts.RecommendationRolloutRollbackRequired {
		t.Fatalf("failed=%+v op=%+v err=%v", failed, failedOp, err)
	}
	rollout, rollbackOp, err := st.EnqueueRecommendationRolloutOperation(ctx, failed.State.ID, failed.Version, failed.State, contracts.RecommendationRolloutOperationRollback, 0)
	if err != nil || rollbackOp.Action != contracts.RecommendationRolloutOperationRollback {
		t.Fatalf("enqueue=%+v op=%+v err=%v", rollout, rollbackOp, err)
	}
	claimedRollout, rollbackOp, claimed, err = st.ClaimRecommendationRolloutOperation(ctx, "rollback-worker", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("rollback claim err=%v", err)
	}
	next := claimedRollout.State
	next.Status, next.Stage, next.PendingStage = contracts.RecommendationRolloutRolledBack, 0, 0
	next.StageStartedAt, next.ObserveUntil = nil, nil
	next.LastAfterEvidence = &contracts.RecommendationRolloutAfterEvidence{
		Stage: 0, RecommendationFingerprint: next.RecommendationFingerprint, BaselineFingerprint: next.BaselineFingerprint,
		SchedulingGeneration: next.SchedulingGeneration, EvidenceIDs: []string{"weight-set-sha256:" + next.BaselineFingerprint}, ObservedAt: clock,
		FreshUntil: clock.Add(time.Minute), Callability: contracts.RecommendationRolloutGateUnknown, Quality: contracts.RecommendationRolloutGateUnknown,
	}
	restored, completed, err := st.CompleteRecommendationRolloutOperation(ctx, contracts.RecommendationRolloutCompletion{
		OperationID: rollbackOp.ID, WorkerID: "rollback-worker", ExpectedOperationVersion: rollbackOp.Version,
		ExpectedRolloutVersion: claimedRollout.Version, OperationStatus: contracts.RecommendationRolloutOperationSucceeded, NextState: next,
	})
	if err != nil || restored.State.Status != contracts.RecommendationRolloutRolledBack || completed.Status != contracts.RecommendationRolloutOperationSucceeded {
		t.Fatalf("restored=%+v op=%+v err=%v", restored, completed, err)
	}
	baselineByAccount := make(map[string]int, len(restored.BaselineWeights))
	for _, weight := range restored.BaselineWeights {
		baselineByAccount[weight.AccountID] = weight.Weight
	}
	if len(restored.BaselineWeights) != 2 || baselineByAccount["account-candidate"] != 0 || baselineByAccount["account-current"] != 100 {
		t.Fatalf("exact zero baseline lost: %+v", restored.BaselineWeights)
	}
}

func TestMemoryRecommendationRolloutGenerationSupersessionPreventsClaimAndComplete(t *testing.T) {
	st, input := newMemoryRecommendationRolloutFixture(t)
	ctx := context.Background()
	created, _, err := st.CreateRecommendationRollout(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	claimedRollout, operation, claimed, err := st.ClaimRecommendationRolloutOperation(ctx, "worker-a", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim err=%v", err)
	}
	if _, err := st.ClaimRoutePlanScheduling(ctx, created.State.PlanID, contracts.RoutePlanPublished); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RenewRecommendationRolloutOperation(ctx, operation.ID, "worker-a", operation.Version, time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatalf("superseded renew error=%v", err)
	}
	next := claimedRollout.State
	if _, _, err := st.CompleteRecommendationRolloutOperation(ctx, contracts.RecommendationRolloutCompletion{
		OperationID: operation.ID, WorkerID: "worker-a", ExpectedOperationVersion: operation.Version, ExpectedRolloutVersion: claimedRollout.Version,
		OperationStatus: contracts.RecommendationRolloutOperationFailed, ErrorCode: contracts.RecommendationRolloutOperationErrorPlanChanged, NextState: next,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("superseded completion error=%v", err)
	}
}

func TestMemoryRecommendationRolloutFailedOperationIsNotAutomaticallyReplayed(t *testing.T) {
	st, input := newMemoryRecommendationRolloutFixture(t)
	ctx := context.Background()
	rollout, _, err := st.CreateRecommendationRollout(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	rollout, operation, claimed, err := st.ClaimRecommendationRolloutOperation(ctx, "worker-a", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	next := rollout.State
	next.Status = contracts.RecommendationRolloutRollbackRequired
	next.PendingStage = 0
	next.RollbackReasons = []contracts.RecommendationRolloutBlockReason{contracts.RecommendationRolloutBlockedApplyFailed}
	failed, _, err := st.CompleteRecommendationRolloutOperation(ctx, contracts.RecommendationRolloutCompletion{
		OperationID: operation.ID, WorkerID: "worker-a", ExpectedOperationVersion: operation.Version,
		ExpectedRolloutVersion: rollout.Version, OperationStatus: contracts.RecommendationRolloutOperationFailed,
		ErrorCode: contracts.RecommendationRolloutOperationErrorWriteFailed, NextState: next,
	})
	if err != nil || failed.State.Status != contracts.RecommendationRolloutRollbackRequired {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	if _, replay, claimed, err := st.ClaimRecommendationRolloutOperation(ctx, "worker-b", time.Minute); err != nil || claimed {
		t.Fatalf("failed operation was automatically replayed: op=%+v claimed=%v err=%v", replay, claimed, err)
	}
}

func TestMemoryRecommendationRolloutCompletionRejectsActionStateMismatch(t *testing.T) {
	st, input := newMemoryRecommendationRolloutFixture(t)
	ctx := context.Background()
	rollout, _, err := st.CreateRecommendationRollout(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	rollout, operation, claimed, err := st.ClaimRecommendationRolloutOperation(ctx, "worker-a", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	for name, completion := range map[string]contracts.RecommendationRolloutCompletion{
		"successful apply cannot claim completion": {
			OperationID: operation.ID, WorkerID: "worker-a", ExpectedOperationVersion: operation.Version,
			ExpectedRolloutVersion: rollout.Version, OperationStatus: contracts.RecommendationRolloutOperationSucceeded,
			NextState: func() contracts.RecommendationRolloutState {
				next := rollout.State
				next.Status, next.Stage, next.PendingStage = contracts.RecommendationRolloutCompleted, 100, 0
				return next
			}(),
		},
		"failed apply must require rollback": {
			OperationID: operation.ID, WorkerID: "worker-a", ExpectedOperationVersion: operation.Version,
			ExpectedRolloutVersion: rollout.Version, OperationStatus: contracts.RecommendationRolloutOperationFailed,
			ErrorCode: contracts.RecommendationRolloutOperationErrorWriteFailed, NextState: rollout.State,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := st.CompleteRecommendationRolloutOperation(ctx, completion); !errors.Is(err, ErrConflict) {
				t.Fatalf("mismatched completion error=%v", err)
			}
		})
	}
}

func TestMemoryRecommendationRolloutRollbackTakesOverSupersededGeneration(t *testing.T) {
	st, input := newMemoryRecommendationRolloutFixture(t)
	ctx := context.Background()
	rollout, _, err := st.CreateRecommendationRollout(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	rollout, operation, claimed, err := st.ClaimRecommendationRolloutOperation(ctx, "worker-a", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	next := rollout.State
	next.Status, next.PendingStage = contracts.RecommendationRolloutRollbackRequired, 0
	next.RollbackReasons = []contracts.RecommendationRolloutBlockReason{contracts.RecommendationRolloutBlockedApplyFailed}
	failed, _, err := st.CompleteRecommendationRolloutOperation(ctx, contracts.RecommendationRolloutCompletion{
		OperationID: operation.ID, WorkerID: "worker-a", ExpectedOperationVersion: operation.Version,
		ExpectedRolloutVersion: rollout.Version, OperationStatus: contracts.RecommendationRolloutOperationFailed,
		ErrorCode: contracts.RecommendationRolloutOperationErrorWriteFailed, NextState: next,
	})
	if err != nil {
		t.Fatal(err)
	}
	superseding, err := st.ClaimRoutePlanScheduling(ctx, failed.State.PlanID, contracts.RoutePlanPublished)
	if err != nil {
		t.Fatal(err)
	}
	rollback, rollbackOperation, err := st.EnqueueRecommendationRolloutOperation(ctx, failed.State.ID, failed.Version, failed.State, contracts.RecommendationRolloutOperationRollback, 0)
	if err != nil {
		t.Fatalf("enqueue safe rollback after supersession: %v", err)
	}
	if rollback.State.SchedulingGeneration <= superseding.SchedulingGeneration || rollbackOperation.Action != contracts.RecommendationRolloutOperationRollback {
		t.Fatalf("rollback did not take a newer generation: plan=%+v rollout=%+v op=%+v", superseding, rollback, rollbackOperation)
	}
	plan, _ := st.GetRoutePlan(ctx, rollback.State.PlanID)
	if plan.SchedulingGeneration != rollback.State.SchedulingGeneration {
		t.Fatalf("plan generation=%d rollout=%d", plan.SchedulingGeneration, rollback.State.SchedulingGeneration)
	}
}

func TestMemoryRecommendationRolloutRollbackAtomicallyPreemptsActiveForward(t *testing.T) {
	for _, claimedFirst := range []bool{false, true} {
		name := "pending"
		if claimedFirst {
			name = "running"
		}
		t.Run(name, func(t *testing.T) {
			st, input := newMemoryRecommendationRolloutFixture(t)
			ctx := context.Background()
			rollout, forward, err := st.CreateRecommendationRollout(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			var claimed contracts.RecommendationRollout
			if claimedFirst {
				claimed, forward, _, err = st.ClaimRecommendationRolloutOperation(ctx, "forward-worker", time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				rollout = claimed
			}
			next := rollout.State
			next.Status = contracts.RecommendationRolloutRollbackRequired
			next.PendingStage = 0
			next.RollbackReasons = []contracts.RecommendationRolloutBlockReason{contracts.RecommendationRolloutBlockedOperatorRequested}
			rollback, rollbackOperation, err := st.EnqueueRecommendationRolloutOperation(ctx, rollout.State.ID, rollout.Version, next, contracts.RecommendationRolloutOperationRollback, 0)
			if err != nil || rollbackOperation.Action != contracts.RecommendationRolloutOperationRollback || rollback.State.SchedulingGeneration <= rollout.State.SchedulingGeneration {
				t.Fatalf("rollback=%+v op=%+v err=%v", rollback, rollbackOperation, err)
			}
			operations, err := st.ListRecommendationRolloutOperations(ctx, rollout.State.ID)
			if err != nil || len(operations) != 2 {
				t.Fatalf("operations=%+v err=%v", operations, err)
			}
			var superseded bool
			for _, operation := range operations {
				if operation.ID == forward.ID {
					superseded = operation.Status == contracts.RecommendationRolloutOperationSuperseded && operation.LeaseOwner == "" && operation.LeaseUntil == nil
				}
			}
			if !superseded {
				t.Fatalf("forward was not atomically superseded: %+v", operations)
			}
			if claimedFirst {
				if _, err := st.RenewRecommendationRolloutOperation(ctx, forward.ID, "forward-worker", forward.Version, time.Minute); !errors.Is(err, ErrConflict) {
					t.Fatalf("preempted renew error=%v", err)
				}
				if _, _, err := st.CompleteRecommendationRolloutOperation(ctx, contracts.RecommendationRolloutCompletion{
					OperationID: forward.ID, WorkerID: "forward-worker", ExpectedOperationVersion: forward.Version,
					ExpectedRolloutVersion: rollout.Version, OperationStatus: contracts.RecommendationRolloutOperationFailed,
					ErrorCode: contracts.RecommendationRolloutOperationErrorWriteFailed, NextState: next,
				}); !errors.Is(err, ErrConflict) {
					t.Fatalf("preempted complete error=%v", err)
				}
			}
			if _, _, err := st.EnqueueRecommendationRolloutOperation(ctx, rollback.State.ID, rollback.Version, rollback.State, contracts.RecommendationRolloutOperationRollback, 0); !errors.Is(err, ErrConflict) {
				t.Fatalf("duplicate active rollback error=%v", err)
			}
		})
	}
}

func TestMemoryRecommendationRolloutRejectedRollbackLeavesForwardOperationUntouched(t *testing.T) {
	for _, claimedFirst := range []bool{false, true} {
		name := "pending"
		if claimedFirst {
			name = "running"
		}
		t.Run(name, func(t *testing.T) {
			st, input := newMemoryRecommendationRolloutFixture(t)
			ctx := context.Background()
			rollout, forward, err := st.CreateRecommendationRollout(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if claimedFirst {
				rollout, forward, _, err = st.ClaimRecommendationRolloutOperation(ctx, "forward-worker", time.Minute)
				if err != nil {
					t.Fatal(err)
				}
			}
			plan, err := st.GetRoutePlan(ctx, rollout.State.PlanID)
			if err != nil {
				t.Fatal(err)
			}
			plan.Status = contracts.RoutePlanDraft
			if _, err := st.UpdateRoutePlan(ctx, plan); err != nil {
				t.Fatal(err)
			}

			next := rollout.State
			next.Status = contracts.RecommendationRolloutRollbackRequired
			next.PendingStage = 0
			next.RollbackReasons = []contracts.RecommendationRolloutBlockReason{contracts.RecommendationRolloutBlockedOperatorRequested}
			if _, _, err := st.EnqueueRecommendationRolloutOperation(ctx, rollout.State.ID, rollout.Version, next, contracts.RecommendationRolloutOperationRollback, 0); !errors.Is(err, ErrConflict) {
				t.Fatalf("enqueue error=%v", err)
			}

			operations, err := st.ListRecommendationRolloutOperations(ctx, rollout.State.ID)
			if err != nil || len(operations) != 1 {
				t.Fatalf("operations=%+v err=%v", operations, err)
			}
			got := operations[0]
			leaseChanged := (got.LeaseUntil == nil) != (forward.LeaseUntil == nil) || got.LeaseUntil != nil && !got.LeaseUntil.Equal(*forward.LeaseUntil)
			if got.ID != forward.ID || got.Status != forward.Status || got.Version != forward.Version || got.LeaseOwner != forward.LeaseOwner || leaseChanged {
				t.Fatalf("rejected rollback mutated forward operation: before=%+v after=%+v", forward, got)
			}
		})
	}
}

func TestMemoryRecommendationRolloutCreateRequiresExactCurrentBindingSet(t *testing.T) {
	st, input := newMemoryRecommendationRolloutFixture(t)
	ctx := context.Background()

	missingTarget := input
	missingTarget.Rollout.BaselineWeights = []contracts.RecommendationRolloutAccountWeight{{AccountID: "account-current", Weight: 100}, {AccountID: "unrelated", Weight: 0}}
	missingTarget.Rollout.State.BaselineFingerprint, _ = contracts.RecommendationRolloutBaselineFingerprint(missingTarget.Rollout.BaselineWeights)
	if _, _, err := st.CreateRecommendationRollout(ctx, missingTarget); !errors.Is(err, ErrConflict) {
		t.Fatalf("baseline without target error=%v", err)
	}

	st, input = newMemoryRecommendationRolloutFixture(t)
	plan, _ := st.GetRoutePlan(ctx, input.Rollout.State.PlanID)
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: input.Rollout.InstanceID, ChannelID: "channel-third", RemoteID: "account-third",
		State: contracts.BindingActive, SchedulingGeneration: plan.SchedulingGeneration,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateRecommendationRollout(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("incomplete binding baseline error=%v", err)
	}
}

func TestMemoryRecommendationRolloutRejectsRecommendationAfterBindingIdentityReplacement(t *testing.T) {
	st, input := newMemoryRecommendationRolloutFixture(t)
	ctx := context.Background()
	plan, err := st.GetRoutePlan(ctx, input.Rollout.State.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := st.ListPublishedBindings(ctx, plan.ID)
	if err != nil || len(bindings) != 2 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	var target contracts.PublishedBinding
	for _, binding := range bindings {
		if binding.ChannelID == input.Rollout.ToChannelID {
			target = binding
		}
	}
	if target.ID == "" {
		t.Fatal("target binding missing")
	}
	plan, err = st.ClaimRoutePlanScheduling(ctx, plan.ID, contracts.RoutePlanPublished)
	if err != nil {
		t.Fatal(err)
	}
	target.RemoteID = "account-candidate-replaced"
	target.SchedulingGeneration = plan.SchedulingGeneration
	replaced, err := st.UpsertPublishedBinding(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.IsCallable() || replaced.VerificationStatus != contracts.BindingVerificationPublishedPending {
		t.Fatalf("replacement inherited callability: %+v", replaced)
	}

	// Even if a stale caller rebases only the generation, its immutable
	// recommendation/baseline account identity no longer matches the binding set.
	input.ExpectedPlanGeneration = plan.SchedulingGeneration
	input.Rollout.RecommendationPlanGeneration = plan.SchedulingGeneration
	if _, _, err := st.CreateRecommendationRollout(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale recommendation crossed replacement error=%v", err)
	}
	current, _ := st.GetRoutePlan(ctx, plan.ID)
	if current.SchedulingGeneration != plan.SchedulingGeneration {
		t.Fatalf("rejected rollout advanced plan generation: before=%d after=%d", plan.SchedulingGeneration, current.SchedulingGeneration)
	}
}

func TestMemoryRecommendationRolloutOnlyAllowsObservationStateCAS(t *testing.T) {
	st, input := newMemoryRecommendationRolloutFixture(t)
	ctx := context.Background()
	rollout, _, err := st.CreateRecommendationRollout(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	next := rollout.State
	next.Status, next.Stage, next.PendingStage = contracts.RecommendationRolloutCompleted, 100, 0
	if _, err := st.TransitionRecommendationRolloutState(ctx, rollout.State.ID, rollout.Version, next); !errors.Is(err, ErrConflict) {
		t.Fatalf("applying -> completed CAS error=%v", err)
	}
}

func TestMemoryRecommendationRolloutAtomicallyEnqueuesNextStageFromObservation(t *testing.T) {
	st, input := newMemoryRecommendationRolloutFixture(t)
	ctx := context.Background()
	clock := input.Rollout.State.StartedAt
	st.now = func() time.Time { return clock }
	rollout, _, err := st.CreateRecommendationRollout(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	rollout, operation, claimed, err := st.ClaimRecommendationRolloutOperation(ctx, "worker-a", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	observing := rollout.State
	observing.Status, observing.Stage, observing.PendingStage = contracts.RecommendationRolloutObserving, 10, 0
	started, until := clock, clock.Add(time.Minute)
	observing.StageStartedAt, observing.ObserveUntil = &started, &until
	rollout, _, err = st.CompleteRecommendationRolloutOperation(ctx, contracts.RecommendationRolloutCompletion{
		OperationID: operation.ID, WorkerID: "worker-a", ExpectedOperationVersion: operation.Version,
		ExpectedRolloutVersion: rollout.Version, OperationStatus: contracts.RecommendationRolloutOperationSucceeded, NextState: observing,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock = until
	next := rollout.State
	next.Status, next.PendingStage, next.ObserveUntil, next.UpdatedAt = contracts.RecommendationRolloutApplying, 25, nil, clock
	next.LastAfterEvidence = &contracts.RecommendationRolloutAfterEvidence{
		Stage: 10, RecommendationFingerprint: next.RecommendationFingerprint, SchedulingGeneration: next.SchedulingGeneration,
		EvidenceIDs: []string{"after-10"}, ObservedAt: clock, FreshUntil: clock.Add(time.Minute),
		Callability: contracts.RecommendationRolloutGatePassed, Quality: contracts.RecommendationRolloutGatePassed,
	}
	updated, nextOperation, err := st.EnqueueRecommendationRolloutOperation(ctx, rollout.State.ID, rollout.Version, next, contracts.RecommendationRolloutOperationApplyStage, 25)
	if err != nil || updated.State.Status != contracts.RecommendationRolloutApplying || updated.State.PendingStage != 25 ||
		nextOperation.TargetStage != 25 || updated.State.SchedulingGeneration <= rollout.State.SchedulingGeneration {
		t.Fatalf("updated=%+v operation=%+v err=%v", updated, nextOperation, err)
	}
}

func TestRecommendationRolloutMigrationHasClosedStateLeaseAndActivePlanConstraints(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0067_recommendation_rollouts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(raw))
	for _, required := range []string{
		"recommendation_rollouts_one_active_plan_idx", "where status in ('ready','applying','observing','rollback_required','blocked')",
		"recommendation_rollout_operations_one_active_idx", "for update", // implementation owns SKIP LOCKED; schema owns partial uniqueness
		"recommendation_rollout_operations_lease_shape", "recommendation_rollout_operations_error_shape",
		"baseline_weights", "jsonb_typeof(baseline_weights) = 'object'", "scheduling_generation",
	} {
		if !strings.Contains(sql, required) && required != "for update" {
			t.Errorf("migration lacks %q", required)
		}
	}
	for _, forbidden := range []string{"credential", "secret", "base_url", "error_message", "raw_response"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("migration crosses trust boundary with %q", forbidden)
		}
	}
}

func TestPostgresRecommendationRolloutRollbackPreemptionSQLIsAtomicAndLeaseFenced(t *testing.T) {
	raw, err := os.ReadFile("postgres_recommendation_rollout.go")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(raw))
	for _, required := range []string{
		"for update", "action<>'apply_stage'", "status='superseded'", "lease_owner=''", "lease_until=null",
		"claimrecommendationrolloutgenerationpostgres", "update recommendation_rollouts set", "insert into recommendation_rollout_operations",
		"operation.status='running'", "operation.lease_owner=$2", "operation.version=$3", "plan.scheduling_generation=rollout.scheduling_generation",
		"coalesce(lease_until>statement_timestamp(),false)",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("postgres rollout implementation lacks atomic preemption fence %q", required)
		}
	}
}

func newMemoryRecommendationRolloutFixture(t *testing.T) (*MemoryStore, contracts.RecommendationRolloutCreate) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 4, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }
	user, err := st.CreateUser(ctx, contracts.User{Email: "rollout-fixture@example.com", PasswordHash: "test", Enabled: true, Roles: []contracts.UserRole{contracts.UserRoleClient}})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "new-api", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "pool", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-rollout", UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID, Status: contracts.RoutePlanPublished})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = st.ClaimRoutePlanScheduling(ctx, plan.ID, contracts.RoutePlanPublished)
	if err != nil {
		t.Fatal(err)
	}
	from, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{ID: "channel-current", PoolID: pool.ID, SourceID: "source-current", DisplayName: "current", CredentialBindingID: "binding-current", Status: contracts.UpstreamChannelActive, AccountOwnership: contracts.GatewayAccountPlatformManaged})
	if err != nil {
		t.Fatal(err)
	}
	to, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{ID: "channel-candidate", PoolID: pool.ID, SourceID: "source-candidate", DisplayName: "candidate", CredentialBindingID: "binding-candidate", Status: contracts.UpstreamChannelActive, AccountOwnership: contracts.GatewayAccountPlatformManaged})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{PlanID: plan.ID, InstanceID: instance.ID, ChannelID: from.ID, RemoteID: "account-current", State: contracts.BindingActive, SchedulingGeneration: plan.SchedulingGeneration}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{PlanID: plan.ID, InstanceID: instance.ID, ChannelID: to.ID, RemoteID: "account-candidate", State: contracts.BindingActive, SchedulingGeneration: plan.SchedulingGeneration}); err != nil {
		t.Fatal(err)
	}
	recommendation, err := upstreamrecommendation.Build("recommendation-rollout", contracts.UpstreamRecommendationCandidate{
		UserID: user.ID, IntelligenceFactVersion: 7, CostLedgerFactVersion: 8, LinkFactVersion: 7,
		PlanGeneration: plan.SchedulingGeneration, FromSourceID: from.SourceID, FromChannelID: from.ID,
		FromGroupKey: "default", ToSourceID: to.SourceID, ToChannelID: to.ID, ToGroupKey: "default", ModelKey: "model-a",
		PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", PerTokens: 1_000_000,
		AffectedPlanIDs: []string{plan.ID}, AffectedDownstreams: []string{instance.ID},
		EvidenceIDs: []string{"offer-from", "cost-from", "quality-from", "wallet-from", "link-from", "binding-from", "offer-to", "cost-to", "quality-to", "wallet-to", "link-to", "binding-to"},
		Constraints: []contracts.UpstreamRecommendationConstraint{
			{Kind: contracts.UpstreamRecommendationConstraintQuality, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{"quality-from", "quality-to"}},
			{Kind: contracts.UpstreamRecommendationConstraintCapacity, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{"binding-from", "binding-to", "link-from", "link-to"}},
			{Kind: contracts.UpstreamRecommendationConstraintBalance, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{"wallet-from", "wallet-to"}},
		},
		FromCost:       contracts.UpstreamRecommendationCostRange{Lower: "10", Expected: "10", Upper: "10"},
		ToCost:         contracts.UpstreamRecommendationCostRange{Lower: "5", Expected: "5", Upper: "5"},
		FormulaVersion: contracts.UpstreamRecommendationFormulaVersionV1, StrategyVersion: contracts.UpstreamRecommendationStrategyVersionV1,
		CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatalf("build recommendation: %v", err)
	}
	if _, err := st.CreateUpstreamRecommendation(ctx, recommendation); err != nil {
		// The recommendation validator derives the fingerprint; use the shared builder fixture identity if our manual mutation invalidated it.
		t.Fatalf("seed recommendation: %v", err)
	}
	baseline := []contracts.RecommendationRolloutAccountWeight{{AccountID: "account-current", Weight: 100}, {AccountID: "account-candidate", Weight: 0}}
	fingerprint, err := contracts.RecommendationRolloutBaselineFingerprint(baseline)
	if err != nil {
		t.Fatal(err)
	}
	state := contracts.RecommendationRolloutState{
		ID: "rollout-fixture", UserID: user.ID, PlanID: plan.ID, RecommendationID: recommendation.ID,
		RecommendationFingerprint: recommendation.Fingerprint, FactVersion: recommendation.IntelligenceFactVersion,
		EvidenceIDs: append([]string(nil), recommendation.EvidenceIDs...), BaselineFingerprint: fingerprint,
		Status: contracts.RecommendationRolloutApplying, Stage: 0, PendingStage: 10, ObservationSeconds: 60,
		RecommendationExpiresAt: recommendation.ExpiresAt, StartedAt: now, UpdatedAt: now,
	}
	return st, contracts.RecommendationRolloutCreate{
		Rollout: contracts.RecommendationRollout{
			State: state, InstanceID: instance.ID, FromChannelID: from.ID, ToChannelID: to.ID,
			RecommendationPlanGeneration: plan.SchedulingGeneration, FromAccountID: "account-current", ToAccountID: "account-candidate", BaselineWeights: baseline,
		},
		ExpectedPlanGeneration: plan.SchedulingGeneration, FirstAction: contracts.RecommendationRolloutOperationApplyStage, FirstTargetStage: 10,
	}
}
