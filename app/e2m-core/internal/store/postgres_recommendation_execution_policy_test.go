package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPostgresRecommendationExecutionPolicyScopeCASAndOwnerIsolation(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	owner, err := st.CreateUser(ctx, testRecommendationExecutionPolicyUser())
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, testRecommendationExecutionPolicyUser())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=ANY($1)`, []int64{owner.ID, other.ID})
	})

	input := validRecommendationExecutionPolicyFixture()
	input.UserID, input.PlanID = owner.ID, "plan-"+newID("policy")
	if _, err := st.pool.Exec(ctx,
		`INSERT INTO route_plans (id,user_id,instance_id,pool_id,status,created_at,updated_at) VALUES ($1,$2,$3,$4,'draft',statement_timestamp(),statement_timestamp())`,
		input.PlanID, owner.ID, "instance-"+newID("policy"), "pool-"+newID("policy")); err != nil {
		t.Fatal(err)
	}
	created, err := st.UpsertRecommendationExecutionPolicy(ctx, input, 0)
	if err != nil || created.Enabled || created.Version != 1 {
		t.Fatalf("create=%+v err=%v", created, err)
	}
	if _, err := st.UpsertRecommendationExecutionPolicy(ctx, input, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate create error=%v, want ErrConflict", err)
	}
	update := created
	update.Enabled = true
	update.Version, update.CreatedAt, update.UpdatedAt = 0, time.Time{}, time.Time{}
	updated, err := st.UpsertRecommendationExecutionPolicy(ctx, update, created.Version)
	if err != nil || !updated.Enabled || updated.Version != 2 {
		t.Fatalf("update=%+v err=%v", updated, err)
	}
	if _, err := st.UpsertRecommendationExecutionPolicy(ctx, update, created.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS error=%v, want ErrConflict", err)
	}
	got, err := st.GetRecommendationExecutionPolicy(ctx, owner.ID, input.Scope, input.PlanID)
	if err != nil || got.ID != created.ID {
		t.Fatalf("get=%+v err=%v", got, err)
	}
	if _, err := st.GetRecommendationExecutionPolicy(ctx, other.ID, input.Scope, input.PlanID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner get error=%v, want ErrNotFound", err)
	}
}

func testRecommendationExecutionPolicyUser() contracts.User {
	return contracts.User{
		Email: newID("policy-owner") + "@example.com", PasswordHash: "test",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	}
}
