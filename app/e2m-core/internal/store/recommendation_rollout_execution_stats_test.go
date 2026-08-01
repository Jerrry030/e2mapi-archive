package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryRecommendationRolloutExecutionStatsAreExactByPlanAndPool(t *testing.T) {
	ctx := context.Background()
	st, input := newMemoryRecommendationRolloutFixture(t)
	first, _, err := st.CreateRecommendationRollout(ctx, input)
	if err != nil {
		t.Fatal(err)
	}

	planFilter := RecommendationRolloutExecutionStatsFilter{
		UserID: input.Rollout.State.UserID, Scope: contracts.RecommendationExecutionScopePlan,
		PlanID: input.Rollout.State.PlanID, Since: first.CreatedAt,
	}
	planStats, err := st.GetRecommendationRolloutExecutionStats(ctx, planFilter)
	if err != nil || planStats.Count != 1 || planStats.LastStartedAt == nil || !planStats.LastStartedAt.Equal(first.CreatedAt) {
		t.Fatalf("plan stats=%+v err=%v", planStats, err)
	}
	planFilter.ExcludeRolloutID = first.State.ID
	excluded, err := st.GetRecommendationRolloutExecutionStats(ctx, planFilter)
	if err != nil || excluded.Count != 0 || excluded.LastStartedAt != nil {
		t.Fatalf("excluded active rollout stats=%+v err=%v", excluded, err)
	}
	planFilter.ExcludeRolloutID = ""
	plan, err := st.GetRoutePlan(ctx, input.Rollout.State.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	poolFilter := RecommendationRolloutExecutionStatsFilter{
		UserID: input.Rollout.State.UserID, Scope: contracts.RecommendationExecutionScopePool,
		PoolID: plan.PoolID, Since: first.CreatedAt.Add(-time.Second),
	}
	poolStats, err := st.GetRecommendationRolloutExecutionStats(ctx, poolFilter)
	if err != nil || poolStats.Count != 1 {
		t.Fatalf("pool stats=%+v err=%v", poolStats, err)
	}

	// Counts starts in every lifecycle state. A failed rollout has consumed
	// remote-write budget and cannot be made free by filtering to successes.
	st.mu.Lock()
	st.recommendationRollouts[0].State.Status = contracts.RecommendationRolloutRollbackRequired
	st.mu.Unlock()
	failedStats, err := st.GetRecommendationRolloutExecutionStats(ctx, poolFilter)
	if err != nil || failedStats.Count != 1 {
		t.Fatalf("failed stats=%+v err=%v", failedStats, err)
	}

	planFilter.Since = first.CreatedAt.Add(time.Microsecond)
	afterStats, err := st.GetRecommendationRolloutExecutionStats(ctx, planFilter)
	if err != nil || afterStats.Count != 0 || afterStats.LastStartedAt != nil {
		t.Fatalf("after stats=%+v err=%v", afterStats, err)
	}
}

func TestRecommendationRolloutExecutionStatsRejectInvalidAndForeignScopes(t *testing.T) {
	st, input := newMemoryRecommendationRolloutFixture(t)
	now := input.Rollout.State.StartedAt
	for _, filter := range []RecommendationRolloutExecutionStatsFilter{
		{},
		{UserID: input.Rollout.State.UserID, Scope: contracts.RecommendationExecutionScopePlan, PlanID: input.Rollout.State.PlanID},
		{UserID: input.Rollout.State.UserID, Scope: contracts.RecommendationExecutionScopePlan, PlanID: input.Rollout.State.PlanID, PoolID: "also", Since: now},
		{UserID: input.Rollout.State.UserID, Scope: contracts.RecommendationExecutionScopePool, PoolID: " pool ", Since: now},
		{UserID: input.Rollout.State.UserID, Scope: contracts.RecommendationExecutionScopePlan, PlanID: input.Rollout.State.PlanID, Since: now, ExcludeRolloutID: " rollout "},
	} {
		if _, err := st.GetRecommendationRolloutExecutionStats(context.Background(), filter); !errors.Is(err, ErrInvalid) {
			t.Fatalf("filter=%+v error=%v", filter, err)
		}
	}
	foreign := RecommendationRolloutExecutionStatsFilter{
		UserID: input.Rollout.State.UserID + 1, Scope: contracts.RecommendationExecutionScopePlan,
		PlanID: input.Rollout.State.PlanID, Since: now,
	}
	if _, err := st.GetRecommendationRolloutExecutionStats(context.Background(), foreign); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign error=%v", err)
	}
}
