package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"e2m.local/contracts"
)

func createPostgresAutoSwitchPlan(t *testing.T, ctx context.Context, st *PostgresStore, id string, generation int64) contracts.RoutePlan {
	t.Helper()
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID:                   id,
		Status:               contracts.RoutePlanPublished,
		SchedulingGeneration: generation,
	})
	if err != nil {
		t.Fatalf("create route plan: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM auto_switch_decisions WHERE plan_id=$1`, id)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM route_plans WHERE id=$1`, id)
	})
	return plan
}

func TestPostgresAutoSwitchClaimAndTransitionAreAtomic(t *testing.T) {
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

	key := "test-" + newID("autoswitch")
	createPostgresAutoSwitchPlan(t, ctx, st, key, 0)
	callerNow := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	leaseUntil := callerNow.Add(time.Minute)
	input := contracts.AutoSwitchDecision{
		PlanID: key, Fingerprint: key, Status: contracts.AutoSwitchApplying,
		CreatedAt: callerNow, LeaseUntil: &leaseUntil,
	}

	const callers = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	var winners atomic.Int32
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			d, claimed, claimErr := st.ClaimAutoSwitchDecision(ctx, input)
			if claimErr != nil {
				errs <- claimErr
				return
			}
			if claimed {
				winners.Add(1)
			}
			ids <- d.ID
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errs)
	for claimErr := range errs {
		t.Errorf("claim: %v", claimErr)
	}
	if winners.Load() != 1 {
		t.Fatalf("claim winners=%d, want 1", winners.Load())
	}
	var decisionID string
	for id := range ids {
		if decisionID == "" {
			decisionID = id
		} else if id != decisionID {
			t.Fatalf("claim returned different owner ids: %s and %s", decisionID, id)
		}
	}

	owner, err := st.GetAutoSwitchDecision(ctx, decisionID)
	if err != nil {
		t.Fatalf("get owner: %v", err)
	}
	owner.Status = contracts.AutoSwitchObserving
	if _, err := st.TransitionAutoSwitchDecision(ctx, owner, contracts.AutoSwitchApplying); err != nil {
		t.Fatalf("valid transition: %v", err)
	}
	owner.Status = contracts.AutoSwitchCompleted
	if _, err := st.TransitionAutoSwitchDecision(ctx, owner, contracts.AutoSwitchApplying); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale transition error=%v, want ErrConflict", err)
	}
}

func TestPostgresAutoSwitchStatusFilterAppliesBeforeLimit(t *testing.T) {
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

	key := "test-" + newID("autoswitch-list")
	createPostgresAutoSwitchPlan(t, ctx, st, key, 0)
	observing, err := st.CreateAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		PlanID: key, Fingerprint: key + "-observing", Status: contracts.AutoSwitchObserving,
	})
	if err != nil {
		t.Fatalf("create observing decision: %v", err)
	}
	for i := 0; i < 110; i++ {
		if _, err := st.CreateAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
			PlanID: key, Fingerprint: key, Status: contracts.AutoSwitchCompleted,
		}); err != nil {
			t.Fatalf("create newer terminal decision %d: %v", i, err)
		}
	}

	got, err := st.ListAutoSwitchDecisions(ctx, contracts.AutoSwitchDecisionFilter{
		PlanID: key, Statuses: []contracts.AutoSwitchStatus{contracts.AutoSwitchObserving},
	})
	if err != nil {
		t.Fatalf("list observing decisions: %v", err)
	}
	if len(got) != 1 || got[0].ID != observing.ID {
		t.Fatalf("observing decisions=%+v, want older decision %s", got, observing.ID)
	}
}

func TestPostgresExpiredAutoSwitchClaimIsAtomic(t *testing.T) {
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

	key := "test-" + newID("autoswitch-repair")
	plan := createPostgresAutoSwitchPlan(t, ctx, st, key, 0)
	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	decision, err := st.CreateAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		PlanID: key, Fingerprint: key, Status: contracts.AutoSwitchApplying, LeaseUntil: &expired,
		SchedulingGeneration: plan.SchedulingGeneration,
	})
	if err != nil {
		t.Fatalf("create expired decision: %v", err)
	}

	const callers = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	var winners atomic.Int32
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, claimed, claimErr := st.ClaimExpiredAutoSwitchDecision(
				ctx, decision.ID, now, now.Add(-time.Minute), now.Add(time.Minute),
			)
			if claimErr != nil {
				t.Errorf("claim expired: %v", claimErr)
			} else if claimed {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("repair claim winners=%d, want 1", winners.Load())
	}
}

func TestPostgresAutoSwitchLeaseUsesDatabaseClockAndFencesStaleOwner(t *testing.T) {
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

	key := "test-" + newID("autoswitch-clock")
	createPostgresAutoSwitchPlan(t, ctx, st, key, 0)
	callerNow := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	requestedUntil := callerNow.Add(2 * time.Minute)
	var dbBefore time.Time
	if err := st.pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&dbBefore); err != nil {
		t.Fatalf("read database clock: %v", err)
	}
	owner, claimed, err := st.ClaimAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		PlanID: key, Fingerprint: key, CreatedAt: callerNow, LeaseUntil: &requestedUntil,
	})
	if err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}
	var dbAfter time.Time
	if err := st.pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&dbAfter); err != nil {
		t.Fatalf("read database clock after claim: %v", err)
	}
	if owner.LeaseUntil == nil || owner.LeaseUntil.Before(dbBefore.Add(2*time.Minute)) || owner.LeaseUntil.After(dbAfter.Add(2*time.Minute)) {
		t.Fatalf("lease_until=%v, want database-now + 2m in [%v,%v]", owner.LeaseUntil, dbBefore.Add(2*time.Minute), dbAfter.Add(2*time.Minute))
	}

	// Force expiry in the database, then claim repair with a wildly skewed Core
	// clock. Only the requested duration is relevant to the stored deadline.
	if _, err := st.pool.Exec(ctx,
		`UPDATE auto_switch_decisions SET lease_until=statement_timestamp()-interval '1 second' WHERE id=$1`, owner.ID,
	); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	callerRepairNow := time.Date(2099, 8, 7, 6, 5, 4, 0, time.UTC)
	dbBefore = time.Time{}
	if err := st.pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&dbBefore); err != nil {
		t.Fatalf("read database clock before repair: %v", err)
	}
	repair, repaired, err := st.ClaimExpiredAutoSwitchDecision(
		ctx, owner.ID, callerRepairNow, callerRepairNow.Add(-time.Minute), callerRepairNow.Add(3*time.Minute),
	)
	if err != nil || !repaired {
		t.Fatalf("repair claim: repaired=%v err=%v", repaired, err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&dbAfter); err != nil {
		t.Fatalf("read database clock after repair: %v", err)
	}
	if repair.LeaseVersion != owner.LeaseVersion+1 {
		t.Fatalf("repair version=%d, want %d", repair.LeaseVersion, owner.LeaseVersion+1)
	}
	if repair.LeaseUntil == nil || repair.LeaseUntil.Before(dbBefore.Add(3*time.Minute)) || repair.LeaseUntil.After(dbAfter.Add(3*time.Minute)) {
		t.Fatalf("repair lease_until=%v, want database-now + 3m", repair.LeaseUntil)
	}

	if _, err := st.RenewAutoSwitchDecisionLease(ctx, owner.ID, owner.LeaseVersion, time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale owner renew error=%v, want ErrConflict", err)
	}
	owner.Status = contracts.AutoSwitchCompleted
	if _, err := st.TransitionAutoSwitchDecision(ctx, owner, contracts.AutoSwitchApplying); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale owner transition error=%v, want ErrConflict", err)
	}
	if err := st.ReleaseAutoSwitchDecisionLease(ctx, owner.ID, owner.LeaseVersion); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale owner release error=%v, want ErrConflict", err)
	}
	repair, err = st.RenewAutoSwitchDecisionLease(ctx, repair.ID, repair.LeaseVersion, time.Minute)
	if err != nil {
		t.Fatalf("current owner renew: %v", err)
	}
	if err := st.ReleaseAutoSwitchDecisionLease(ctx, repair.ID, repair.LeaseVersion); err != nil {
		t.Fatalf("current owner release: %v", err)
	}
	repair.Status = contracts.AutoSwitchFailed
	repair.LeaseUntil = nil
	if _, err := st.TransitionAutoSwitchDecision(ctx, repair, contracts.AutoSwitchApplying); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired current owner transition error=%v, want ErrConflict", err)
	}
}

func TestPostgresAutoSwitchObservationClaimAdvancesGeneration(t *testing.T) {
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

	key := "test-" + newID("autoswitch-observation")
	plan := createPostgresAutoSwitchPlan(t, ctx, st, key, 7)
	observing, err := st.CreateAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		PlanID: key, Fingerprint: key, Status: contracts.AutoSwitchObserving, LeaseVersion: 7,
		SchedulingGeneration: plan.SchedulingGeneration,
	})
	if err != nil {
		t.Fatalf("create observing: %v", err)
	}
	claimed, err := st.ClaimAutoSwitchObservation(ctx, observing, 90*time.Second)
	if err != nil {
		t.Fatalf("claim observation: %v", err)
	}
	if claimed.Status != contracts.AutoSwitchApplying || claimed.LeaseVersion != 8 || claimed.LeaseUntil == nil {
		t.Fatalf("claimed=%+v", claimed)
	}
	if _, err := st.ClaimAutoSwitchObservation(ctx, observing, time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate observation claim error=%v, want ErrConflict", err)
	}
	if _, err := st.UpdateAutoSwitchDecision(ctx, claimed); !errors.Is(err, ErrConflict) {
		t.Fatalf("generic applying update error=%v, want ErrConflict", err)
	}
	staleUpdate := claimed
	staleUpdate.Status = contracts.AutoSwitchCompleted
	if _, err := st.UpdateAutoSwitchDecision(ctx, staleUpdate); !errors.Is(err, ErrConflict) {
		t.Fatalf("generic update over applying owner error=%v, want ErrConflict", err)
	}
	illegalClaim := observing
	illegalClaim.Status = contracts.AutoSwitchApplying
	if _, err := st.TransitionAutoSwitchDecision(ctx, illegalClaim, contracts.AutoSwitchObserving); !errors.Is(err, ErrConflict) {
		t.Fatalf("generic observation claim error=%v, want ErrConflict", err)
	}
	claimed.Status = contracts.AutoSwitchCompleted
	claimed.LeaseUntil = nil
	if _, err := st.TransitionAutoSwitchDecision(ctx, claimed, contracts.AutoSwitchApplying); err != nil {
		t.Fatalf("current owner terminal transition: %v", err)
	}
}

func TestPostgresRoutePlanTakeoverSupersedesActiveAutoSwitchDecision(t *testing.T) {
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

	key := "test-" + newID("autoswitch-takeover")
	createPostgresAutoSwitchPlan(t, ctx, st, key, 0)
	now := time.Now().UTC()
	leaseUntil := now.Add(time.Minute)
	owner, claimed, err := st.ClaimAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		PlanID: key, Fingerprint: key, CreatedAt: now, LeaseUntil: &leaseUntil,
	})
	if err != nil || !claimed {
		t.Fatalf("claim decision: claimed=%v err=%v", claimed, err)
	}
	takeover, err := st.ClaimRoutePlanScheduling(ctx, key, contracts.RoutePlanPublished)
	if err != nil {
		t.Fatalf("claim route plan: %v", err)
	}
	if takeover.SchedulingGeneration <= owner.SchedulingGeneration {
		t.Fatalf("takeover generation=%d, owner=%d", takeover.SchedulingGeneration, owner.SchedulingGeneration)
	}
	superseded, err := st.GetAutoSwitchDecision(ctx, owner.ID)
	if err != nil {
		t.Fatalf("get superseded decision: %v", err)
	}
	if superseded.Status != contracts.AutoSwitchFailed || superseded.LeaseUntil != nil || superseded.ResolvedAt == nil {
		t.Fatalf("superseded decision=%+v", superseded)
	}
	if _, err := st.FindActiveAutoSwitchDecisionByFingerprint(ctx, key, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("active fingerprint after takeover error=%v, want ErrNotFound", err)
	}
	owner.Status = contracts.AutoSwitchCompleted
	owner.LeaseUntil = nil
	if _, err := st.TransitionAutoSwitchDecision(ctx, owner, contracts.AutoSwitchApplying); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale owner transition error=%v, want ErrConflict", err)
	}
}
