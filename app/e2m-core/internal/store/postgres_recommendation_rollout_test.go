package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/upstreamrecommendation"
)

func TestPostgresRecommendationRolloutRollbackAtomicallyPreemptsActiveForward(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	for _, claimedFirst := range []bool{false, true} {
		name := "pending"
		if claimedFirst {
			name = "running"
		}
		t.Run(name, func(t *testing.T) {
			input := newPostgresRecommendationRolloutFixture(t, ctx, st)
			rollout, forward, err := st.CreateRecommendationRollout(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if claimedFirst {
				var claimed bool
				rollout, forward, claimed, err = st.ClaimRecommendationRolloutOperation(ctx, "forward-worker", time.Minute)
				if err != nil || !claimed {
					t.Fatalf("claim=%v rollout=%+v operation=%+v err=%v", claimed, rollout, forward, err)
				}
			}

			next := rollout.State
			next.Status = contracts.RecommendationRolloutRollbackRequired
			next.PendingStage = contracts.RecommendationRolloutStageNone
			next.RollbackReasons = []contracts.RecommendationRolloutBlockReason{contracts.RecommendationRolloutBlockedOperatorRequested}
			rollback, rollbackOperation, err := st.EnqueueRecommendationRolloutOperation(ctx, rollout.State.ID, rollout.Version, next,
				contracts.RecommendationRolloutOperationRollback, contracts.RecommendationRolloutStageNone)
			if err != nil {
				t.Fatal(err)
			}
			if rollbackOperation.Action != contracts.RecommendationRolloutOperationRollback ||
				rollbackOperation.Status != contracts.RecommendationRolloutOperationPending ||
				rollback.State.SchedulingGeneration <= rollout.State.SchedulingGeneration {
				t.Fatalf("rollback=%+v operation=%+v", rollback, rollbackOperation)
			}

			plan, err := st.GetRoutePlan(ctx, rollback.State.PlanID)
			if err != nil || plan.SchedulingGeneration != rollback.State.SchedulingGeneration {
				t.Fatalf("plan=%+v rollout generation=%d err=%v", plan, rollback.State.SchedulingGeneration, err)
			}
			operations, err := st.ListRecommendationRolloutOperations(ctx, rollout.State.ID)
			if err != nil || len(operations) != 2 {
				t.Fatalf("operations=%+v err=%v", operations, err)
			}
			var superseded contracts.RecommendationRolloutOperation
			for _, operation := range operations {
				if operation.ID == forward.ID {
					superseded = operation
				}
			}
			if superseded.ID == "" || superseded.Status != contracts.RecommendationRolloutOperationSuperseded ||
				superseded.Version != forward.Version+1 || superseded.LeaseOwner != "" || superseded.LeaseUntil != nil {
				t.Fatalf("forward before=%+v after=%+v", forward, superseded)
			}
			if _, _, err := st.EnqueueRecommendationRolloutOperation(ctx, rollback.State.ID, rollback.Version, rollback.State,
				contracts.RecommendationRolloutOperationRollback, contracts.RecommendationRolloutStageNone); !errors.Is(err, ErrConflict) {
				t.Fatalf("duplicate active rollback error=%v", err)
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
		})
	}
}

func TestPostgresRecommendationRolloutRejectedRollbackHasNoPartialPreemption(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	input := newPostgresRecommendationRolloutFixture(t, ctx, st)
	rollout, forward, err := st.CreateRecommendationRollout(ctx, input)
	if err != nil {
		t.Fatal(err)
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
	next.PendingStage = contracts.RecommendationRolloutStageNone
	next.RollbackReasons = []contracts.RecommendationRolloutBlockReason{contracts.RecommendationRolloutBlockedOperatorRequested}
	if _, _, err := st.EnqueueRecommendationRolloutOperation(ctx, rollout.State.ID, rollout.Version, next,
		contracts.RecommendationRolloutOperationRollback, contracts.RecommendationRolloutStageNone); !errors.Is(err, ErrConflict) {
		t.Fatalf("rejected rollback error=%v", err)
	}
	operations, err := st.ListRecommendationRolloutOperations(ctx, rollout.State.ID)
	if err != nil || len(operations) != 1 {
		t.Fatalf("operations=%+v err=%v", operations, err)
	}
	got := operations[0]
	if got.ID != forward.ID || got.Status != forward.Status || got.Version != forward.Version || got.LeaseOwner != forward.LeaseOwner || got.LeaseUntil != nil {
		t.Fatalf("rejected transaction mutated forward operation: before=%+v after=%+v", forward, got)
	}
}

func TestPostgresRecommendationRolloutBindingReplacementFencesClaimAndCompletion(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	t.Run("pending operation cannot be claimed", func(t *testing.T) {
		input := newPostgresRecommendationRolloutFixture(t, ctx, st)
		rollout, operation, err := st.CreateRecommendationRollout(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		replacePostgresRecommendationRolloutBinding(t, ctx, st, rollout)

		if gotRollout, gotOperation, claimed, err := st.ClaimRecommendationRolloutOperation(ctx, "replacement-worker", time.Minute); err != nil || claimed {
			t.Fatalf("stale operation was claimable: claimed=%v rollout=%+v operation=%+v err=%v", claimed, gotRollout, gotOperation, err)
		}
		operations, err := st.ListRecommendationRolloutOperations(ctx, rollout.State.ID)
		if err != nil || len(operations) != 1 || operations[0].ID != operation.ID ||
			operations[0].Status != contracts.RecommendationRolloutOperationPending || operations[0].Version != operation.Version {
			t.Fatalf("rejected claim mutated operation: before=%+v after=%+v err=%v", operation, operations, err)
		}
	})

	t.Run("leased operation cannot complete", func(t *testing.T) {
		input := newPostgresRecommendationRolloutFixture(t, ctx, st)
		rollout, _, err := st.CreateRecommendationRollout(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		rollout, operation, claimed, err := st.ClaimRecommendationRolloutOperation(ctx, "replacement-worker", time.Minute)
		if err != nil || !claimed {
			t.Fatalf("claim=%v rollout=%+v operation=%+v err=%v", claimed, rollout, operation, err)
		}
		replacePostgresRecommendationRolloutBinding(t, ctx, st, rollout)

		next := rollout.State
		next.Status = contracts.RecommendationRolloutRollbackRequired
		next.PendingStage = contracts.RecommendationRolloutStageNone
		next.RollbackReasons = []contracts.RecommendationRolloutBlockReason{contracts.RecommendationRolloutBlockedApplyFailed}
		if _, _, err := st.CompleteRecommendationRolloutOperation(ctx, contracts.RecommendationRolloutCompletion{
			OperationID: operation.ID, WorkerID: "replacement-worker", ExpectedOperationVersion: operation.Version,
			ExpectedRolloutVersion: rollout.Version, OperationStatus: contracts.RecommendationRolloutOperationFailed,
			ErrorCode: contracts.RecommendationRolloutOperationErrorPlanChanged, NextState: next,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale binding completion error=%v, want ErrConflict", err)
		}
		storedRollout, err := st.GetRecommendationRollout(ctx, rollout.State.ID)
		if err != nil || storedRollout.Version != rollout.Version || storedRollout.State.Status != rollout.State.Status ||
			storedRollout.State.SchedulingGeneration != rollout.State.SchedulingGeneration {
			t.Fatalf("rejected completion mutated rollout: before=%+v after=%+v err=%v", rollout, storedRollout, err)
		}
		operations, err := st.ListRecommendationRolloutOperations(ctx, rollout.State.ID)
		if err != nil || len(operations) != 1 || operations[0].ID != operation.ID ||
			operations[0].Status != operation.Status || operations[0].Version != operation.Version ||
			operations[0].LeaseOwner != operation.LeaseOwner || operations[0].LeaseUntil == nil {
			t.Fatalf("rejected completion mutated operation: before=%+v after=%+v err=%v", operation, operations, err)
		}
	})
}

func replacePostgresRecommendationRolloutBinding(t *testing.T, ctx context.Context, st *PostgresStore, rollout contracts.RecommendationRollout) {
	t.Helper()
	plan, err := st.ClaimRoutePlanScheduling(ctx, rollout.State.PlanID, contracts.RoutePlanPublished)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := st.ListPublishedBindings(ctx, rollout.State.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range bindings {
		if binding.ChannelID != rollout.ToChannelID {
			continue
		}
		binding.RemoteID += "-replacement"
		binding.SchedulingGeneration = plan.SchedulingGeneration
		replaced, err := st.UpsertPublishedBinding(ctx, binding)
		if err != nil {
			t.Fatal(err)
		}
		if replaced.RemoteID == rollout.ToAccountID || replaced.IsCallable() ||
			replaced.VerificationStatus != contracts.BindingVerificationPublishedPending {
			t.Fatalf("replacement retained prior execution identity: %+v", replaced)
		}
		return
	}
	t.Fatal("rollout target binding not found")
}

func newPostgresRecommendationRolloutFixture(t *testing.T, ctx context.Context, st *PostgresStore) contracts.RecommendationRolloutCreate {
	t.Helper()
	suffix := newID("recommendation-rollout-pg")
	now := time.Now().UTC().Truncate(time.Microsecond)
	user, err := st.CreateUser(ctx, contracts.User{
		Email: suffix + "@example.test", PasswordHash: "test", Enabled: true,
		Roles: []contracts.UserRole{contracts.UserRoleClient},
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: suffix, Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatal(err)
	}
	poolID, planID := "pool-"+suffix, "plan-"+suffix
	fromChannelID, toChannelID := "channel-from-"+suffix, "channel-to-"+suffix
	fromAccountID, toAccountID := "account-from-"+suffix, "account-to-"+suffix
	if _, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: suffix, Status: contracts.UpstreamPoolActive}); err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: planID, UserID: user.ID, InstanceID: instance.ID, PoolID: poolID, Status: contracts.RoutePlanPublished,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = st.ClaimRoutePlanScheduling(ctx, plan.ID, contracts.RoutePlanPublished)
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range []contracts.UpstreamChannel{
		{ID: fromChannelID, PoolID: poolID, SourceID: "source-from-" + suffix, DisplayName: "from", CredentialBindingID: "binding-from-" + suffix, Status: contracts.UpstreamChannelActive, AccountOwnership: contracts.GatewayAccountPlatformManaged},
		{ID: toChannelID, PoolID: poolID, SourceID: "source-to-" + suffix, DisplayName: "to", CredentialBindingID: "binding-to-" + suffix, Status: contracts.UpstreamChannelActive, AccountOwnership: contracts.GatewayAccountPlatformManaged},
	} {
		if _, err := st.CreateUpstreamChannel(ctx, channel); err != nil {
			t.Fatal(err)
		}
	}
	for _, binding := range []contracts.PublishedBinding{
		{PlanID: plan.ID, InstanceID: instance.ID, ChannelID: fromChannelID, RemoteID: fromAccountID, State: contracts.BindingActive, SchedulingGeneration: plan.SchedulingGeneration},
		{PlanID: plan.ID, InstanceID: instance.ID, ChannelID: toChannelID, RemoteID: toAccountID, State: contracts.BindingActive, SchedulingGeneration: plan.SchedulingGeneration},
	} {
		if _, err := st.UpsertPublishedBinding(ctx, binding); err != nil {
			t.Fatal(err)
		}
	}
	recommendation, err := upstreamrecommendation.Build("recommendation-"+suffix, contracts.UpstreamRecommendationCandidate{
		UserID: user.ID, IntelligenceFactVersion: 7, CostLedgerFactVersion: 8, LinkFactVersion: 7,
		PlanGeneration: plan.SchedulingGeneration, FromSourceID: "source-from-" + suffix, FromChannelID: fromChannelID,
		FromGroupKey: "default", ToSourceID: "source-to-" + suffix, ToChannelID: toChannelID, ToGroupKey: "default", ModelKey: "model-a",
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
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUpstreamRecommendation(ctx, recommendation); err != nil {
		t.Fatal(err)
	}
	baseline := []contracts.RecommendationRolloutAccountWeight{{AccountID: fromAccountID, Weight: 100}, {AccountID: toAccountID, Weight: 0}}
	baselineFingerprint, err := contracts.RecommendationRolloutBaselineFingerprint(baseline)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM recommendation_rollouts WHERE id=$1`, "rollout-"+suffix)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_recommendations WHERE user_id=$1 AND id=$2`, user.ID, recommendation.ID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM published_bindings WHERE plan_id=$1`, plan.ID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_channel_allocations WHERE channel_id=ANY($1)`, []string{fromChannelID, toChannelID})
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM route_plans WHERE id=$1`, plan.ID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_channels WHERE id=ANY($1)`, []string{fromChannelID, toChannelID})
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_pools WHERE id=$1`, poolID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM instances WHERE id=$1`, instance.ID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, user.ID)
	})

	state := contracts.RecommendationRolloutState{
		ID: "rollout-" + suffix, UserID: user.ID, PlanID: plan.ID, RecommendationID: recommendation.ID,
		RecommendationFingerprint: recommendation.Fingerprint, FactVersion: recommendation.IntelligenceFactVersion,
		EvidenceIDs: append([]string(nil), recommendation.EvidenceIDs...), BaselineFingerprint: baselineFingerprint,
		Status: contracts.RecommendationRolloutApplying, Stage: contracts.RecommendationRolloutStageNone,
		PendingStage: contracts.RecommendationRolloutStage10, ObservationSeconds: 60,
		RecommendationExpiresAt: recommendation.ExpiresAt, StartedAt: now, UpdatedAt: now,
	}
	return contracts.RecommendationRolloutCreate{
		Rollout: contracts.RecommendationRollout{
			State: state, InstanceID: instance.ID, FromChannelID: fromChannelID, ToChannelID: toChannelID,
			RecommendationPlanGeneration: plan.SchedulingGeneration, FromAccountID: fromAccountID, ToAccountID: toAccountID,
			BaselineWeights: baseline,
		},
		ExpectedPlanGeneration: plan.SchedulingGeneration,
		FirstAction:            contracts.RecommendationRolloutOperationApplyStage, FirstTargetStage: contracts.RecommendationRolloutStage10,
	}
}
