package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryRecommendationExecutionPolicyDefaultsDisabledAndUsesVersionCAS(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	st.now = func() time.Time { return time.Date(2026, 7, 26, 1, 0, 0, 123456789, time.UTC) }

	input := validRecommendationExecutionPolicyFixture()
	seedRecommendationExecutionPlan(st, input.UserID, input.PlanID, "pool-1")
	created, err := st.UpsertRecommendationExecutionPolicy(ctx, input, 0)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Enabled || created.Version != 1 || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("unsafe or incomplete create: %+v", created)
	}
	if _, err := st.UpsertRecommendationExecutionPolicy(ctx, input, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate create error=%v, want ErrConflict", err)
	}

	update := created
	update.Enabled = true
	update.MinimumSavings = "0.125"
	update.Version, update.CreatedAt, update.UpdatedAt = 0, time.Time{}, time.Time{}
	updated, err := st.UpsertRecommendationExecutionPolicy(ctx, update, created.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Enabled || updated.Version != 2 || updated.ID != created.ID || updated.CreatedAt != created.CreatedAt {
		t.Fatalf("CAS update did not preserve identity: %+v", updated)
	}
	if _, err := st.UpsertRecommendationExecutionPolicy(ctx, update, created.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS error=%v, want ErrConflict", err)
	}
	wrongID := updated
	wrongID.ID = "another-policy"
	wrongID.Version, wrongID.CreatedAt, wrongID.UpdatedAt = 0, time.Time{}, time.Time{}
	if _, err := st.UpsertRecommendationExecutionPolicy(ctx, wrongID, updated.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("identity replacement error=%v, want ErrConflict", err)
	}
}

func TestMemoryRecommendationExecutionPolicyOwnerScopeUniquenessAndReads(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	plan := validRecommendationExecutionPolicyFixture()
	seedRecommendationExecutionPlan(st, plan.UserID, plan.PlanID, "pool-1")
	createdPlan, err := st.UpsertRecommendationExecutionPolicy(ctx, plan, 0)
	if err != nil {
		t.Fatal(err)
	}
	pool := validRecommendationExecutionPolicyFixture()
	pool.Scope, pool.PlanID, pool.PoolID = contracts.RecommendationExecutionScopePool, "", "pool-1"
	st.poolRolloutTargets = append(st.poolRolloutTargets, contracts.PoolRolloutTarget{PoolID: pool.PoolID, UserID: pool.UserID, Scope: contracts.PoolRolloutScopeUser, Enabled: true})
	if _, err := st.UpsertRecommendationExecutionPolicy(ctx, pool, 0); err != nil {
		t.Fatal(err)
	}
	foreign := plan
	foreign.UserID = 43
	foreign.PlanID = "plan-foreign"
	seedRecommendationExecutionPlan(st, foreign.UserID, foreign.PlanID, "pool-2")
	if _, err := st.UpsertRecommendationExecutionPolicy(ctx, foreign, 0); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetRecommendationExecutionPolicy(ctx, 42, contracts.RecommendationExecutionScopePlan, "plan-1")
	if err != nil || got.ID != createdPlan.ID {
		t.Fatalf("get=%+v err=%v", got, err)
	}
	if _, err := st.GetRecommendationExecutionPolicy(ctx, 43, contracts.RecommendationExecutionScopePool, "pool-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner read error=%v, want ErrNotFound", err)
	}
	listed, err := st.ListRecommendationExecutionPolicies(ctx, 42)
	if err != nil || len(listed) != 2 || listed[0].Scope != contracts.RecommendationExecutionScopePlan || listed[1].Scope != contracts.RecommendationExecutionScopePool {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
	if _, err := st.ListRecommendationExecutionPolicies(ctx, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ownerless list error=%v, want ErrInvalid", err)
	}
}

func TestMemoryRecommendationExecutionPolicyStrictValidation(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		mutate func(*contracts.RecommendationExecutionPolicy)
	}{
		{"owner", func(p *contracts.RecommendationExecutionPolicy) { p.UserID = 0 }},
		{"scope", func(p *contracts.RecommendationExecutionPolicy) { p.Scope = "user" }},
		{"plan xor pool", func(p *contracts.RecommendationExecutionPolicy) { p.PoolID = "pool-too" }},
		{"empty target", func(p *contracts.RecommendationExecutionPolicy) { p.PlanID = "" }},
		{"target whitespace", func(p *contracts.RecommendationExecutionPolicy) { p.PlanID = " plan-1" }},
		{"target control", func(p *contracts.RecommendationExecutionPolicy) { p.PlanID = "plan\n1" }},
		{"cap zero", func(p *contracts.RecommendationExecutionPolicy) { p.DailyExecutionCap = 0 }},
		{"cooldown negative", func(p *contracts.RecommendationExecutionPolicy) { p.CooldownSeconds = -1 }},
		{"cooldown overflow", func(p *contracts.RecommendationExecutionPolicy) {
			p.CooldownSeconds = maxRecommendationExecutionCooldownSeconds + 1
		}},
		{"minimum negative", func(p *contracts.RecommendationExecutionPolicy) { p.MinimumSavings = "-0.1" }},
		{"minimum noncanonical", func(p *contracts.RecommendationExecutionPolicy) { p.MinimumSavings = "0.10" }},
		{"server version", func(p *contracts.RecommendationExecutionPolicy) { p.Version = 99 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := NewMemoryStore(time.Now())
			input := validRecommendationExecutionPolicyFixture()
			seedRecommendationExecutionPlan(st, input.UserID, input.PlanID, "pool-1")
			test.mutate(&input)
			_, err := st.UpsertRecommendationExecutionPolicy(ctx, input, 0)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error=%v, want ErrInvalid", err)
			}
		})
	}
}

func TestMemoryRecommendationExecutionPolicyReturnsIndependentValues(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	seedRecommendationExecutionPlan(st, 42, "plan-1", "pool-1")
	created, err := st.UpsertRecommendationExecutionPolicy(ctx, validRecommendationExecutionPolicyFixture(), 0)
	if err != nil {
		t.Fatal(err)
	}
	created.PlanID = "mutated"
	got, err := st.GetRecommendationExecutionPolicy(ctx, 42, contracts.RecommendationExecutionScopePlan, "plan-1")
	if err != nil || got.PlanID != "plan-1" {
		t.Fatalf("caller mutated stored policy: %+v err=%v", got, err)
	}
}

func TestMemoryRecommendationExecutionPolicyCASAllowsOneWinner(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	seedRecommendationExecutionPlan(st, 42, "plan-1", "pool-1")
	created, err := st.UpsertRecommendationExecutionPolicy(ctx, validRecommendationExecutionPolicyFixture(), 0)
	if err != nil {
		t.Fatal(err)
	}
	const contenders = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	var winners atomic.Int64
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(cap int) {
			defer wg.Done()
			<-start
			candidate := created
			candidate.DailyExecutionCap = cap
			candidate.Version, candidate.CreatedAt, candidate.UpdatedAt = 0, time.Time{}, time.Time{}
			if _, updateErr := st.UpsertRecommendationExecutionPolicy(ctx, candidate, created.Version); updateErr == nil {
				winners.Add(1)
			} else if !errors.Is(updateErr, ErrConflict) {
				t.Errorf("CAS error=%v", updateErr)
			}
		}(i + 1)
	}
	close(start)
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("CAS winners=%d, want 1", winners.Load())
	}
}

func TestRecommendationExecutionPolicyMigrationIsFailClosedScopedAndVersioned(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0064_recommendation_execution_policies.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(raw))
	for _, required := range []string{
		"enabled             boolean not null default false",
		"unique (user_id,scope,plan_id,pool_id)",
		"scope in ('plan','pool')", "numeric(38,18)", "version > 0",
		"scope='plan' and plan_id <> ''", "scope='pool' and pool_id <> ''",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration lacks %q", required)
		}
	}
}

func validRecommendationExecutionPolicyFixture() contracts.RecommendationExecutionPolicy {
	return contracts.RecommendationExecutionPolicy{
		UserID: 42, Scope: contracts.RecommendationExecutionScopePlan, PlanID: "plan-1",
		Enabled: false, KillSwitch: false, DailyExecutionCap: 3,
		CooldownSeconds: 300, MinimumSavings: "0.1",
	}
}

func seedRecommendationExecutionPlan(st *MemoryStore, userID int64, planID, poolID string) {
	st.routePlans = append(st.routePlans, contracts.RoutePlan{ID: planID, UserID: userID, PoolID: poolID})
}
