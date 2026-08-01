package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPostgresPoolRetirementJobPersistsInitialTotals(t *testing.T) {
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

	suffix := newID("retirement-total")
	poolID := "pool-" + suffix
	planID := "plan-" + suffix
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM pool_retirement_jobs WHERE pool_id=$1`, poolID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM route_plans WHERE id=$1`, planID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_pools WHERE id=$1`, poolID)
	})

	if _, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: "retirement total"}); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	if _, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: planID, UserID: 1, InstanceID: "instance-" + suffix, PoolID: poolID,
	}); err != nil {
		t.Fatalf("create route plan: %v", err)
	}

	job, err := st.CreatePoolRetirementJob(ctx, poolID, 1)
	if err != nil {
		t.Fatalf("create retirement job: %v", err)
	}
	if job.TotalPlans != 1 || len(job.Items) != 1 || job.Status != contracts.PoolRetirementPending {
		t.Fatalf("job did not expose persisted initial totals: %+v", job)
	}
	var total int
	var status contracts.PoolRetirementJobStatus
	if err := st.pool.QueryRow(ctx,
		`SELECT total_plans,status FROM pool_retirement_jobs WHERE id=$1`, job.ID,
	).Scan(&total, &status); err != nil {
		t.Fatalf("read retirement job header: %v", err)
	}
	if total != 1 || status != contracts.PoolRetirementPending {
		t.Fatalf("retirement job header total=%d status=%q", total, status)
	}
}

func TestPostgresPoolRetirementCannotBeReopenedByOrdinaryUpdate(t *testing.T) {
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

	suffix := newID("retirement-reopen")
	poolID := "pool-" + suffix
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM pool_retirement_jobs WHERE pool_id=$1`, poolID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_pools WHERE id=$1`, poolID)
	})

	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: "retirement reopen"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePoolRetirementJob(ctx, pool.ID, 1); err != nil {
		t.Fatal(err)
	}
	pool.Status = contracts.UpstreamPoolActive
	if _, err := st.UpdateUpstreamPool(ctx, pool); !errors.Is(err, ErrConflict) {
		t.Fatalf("reopen active retirement error=%v, want ErrConflict", err)
	}
	got, err := st.GetUpstreamPool(ctx, pool.ID)
	if err != nil || got.Status != contracts.UpstreamPoolMaintenance {
		t.Fatalf("retiring pool changed: pool=%+v err=%v", got, err)
	}
}

func TestPostgresRetirementDrainAndCleanupClaimsFenceStaleWorkers(t *testing.T) {
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

	suffix := newID("retirement-fence")
	poolID := "pool-" + suffix
	planID := "plan-" + suffix
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM pool_retirement_jobs WHERE pool_id=$1`, poolID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM route_plans WHERE id=$1`, planID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_pools WHERE id=$1`, poolID)
	})
	if _, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: "retirement fence"}); err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: planID, UserID: 1, InstanceID: "instance-" + suffix, PoolID: poolID,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.CreatePoolRetirementJob(ctx, poolID, 1)
	if err != nil {
		t.Fatal(err)
	}

	drainA, claimed, err := st.ClaimPoolRetirementItem(ctx, job.ID)
	if err != nil || !claimed || drainA.Attempts != 1 {
		t.Fatalf("claim drain A=%+v claimed=%v err=%v", drainA, claimed, err)
	}
	if _, err := st.pool.Exec(ctx,
		`UPDATE pool_retirement_items SET lease_until=statement_timestamp()-interval '1 second' WHERE job_id=$1 AND plan_id=$2`,
		job.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	drainB, claimed, err := st.ClaimPoolRetirementItem(ctx, job.ID)
	if err != nil || !claimed || drainB.Attempts != 2 {
		t.Fatalf("claim drain B=%+v claimed=%v err=%v", drainB, claimed, err)
	}
	if _, err := st.RenewPoolRetirementItem(ctx, job.ID, plan.ID, drainA.Attempts, time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale drain guard error=%v, want ErrConflict", err)
	}
	if _, err := st.CompletePoolRetirementItem(ctx, job.ID, plan.ID, drainA.Attempts, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale drain complete error=%v, want ErrConflict", err)
	}
	if _, err := st.RenewPoolRetirementItem(ctx, job.ID, plan.ID, drainB.Attempts, time.Minute); err != nil {
		t.Fatalf("renew drain B: %v", err)
	}
	if _, err := st.CompletePoolRetirementItem(ctx, job.ID, plan.ID, drainB.Attempts, ""); err != nil {
		t.Fatalf("complete drain B: %v", err)
	}
	if _, err := st.FinalizePoolRetirementJob(ctx, job.ID); err != nil {
		t.Fatalf("finalize retirement: %v", err)
	}

	cleanupA, claimed, err := st.ClaimPoolRetirementCleanupItem(ctx, job.ID)
	if err != nil || !claimed || cleanupA.CleanupAttempts != 1 {
		t.Fatalf("claim cleanup A=%+v claimed=%v err=%v", cleanupA, claimed, err)
	}
	if _, err := st.pool.Exec(ctx,
		`UPDATE pool_retirement_items SET cleanup_lease_until=statement_timestamp()-interval '1 second' WHERE job_id=$1 AND plan_id=$2`,
		job.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	cleanupB, claimed, err := st.ClaimPoolRetirementCleanupItem(ctx, job.ID)
	if err != nil || !claimed || cleanupB.CleanupAttempts != 2 {
		t.Fatalf("claim cleanup B=%+v claimed=%v err=%v", cleanupB, claimed, err)
	}
	if _, err := st.RenewPoolRetirementCleanupItem(ctx, job.ID, plan.ID, cleanupA.CleanupAttempts, time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale cleanup guard error=%v, want ErrConflict", err)
	}
	if _, err := st.CompletePoolRetirementCleanupItem(ctx, job.ID, plan.ID, cleanupA.CleanupAttempts, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale cleanup complete error=%v, want ErrConflict", err)
	}
	if _, err := st.RenewPoolRetirementCleanupItem(ctx, job.ID, plan.ID, cleanupB.CleanupAttempts, time.Minute); err != nil {
		t.Fatalf("renew cleanup B: %v", err)
	}
	completed, err := st.CompletePoolRetirementCleanupItem(ctx, job.ID, plan.ID, cleanupB.CleanupAttempts, "")
	if err != nil || completed.Status != contracts.PoolRetirementCompleted {
		t.Fatalf("complete cleanup B=%+v err=%v", completed, err)
	}
}
