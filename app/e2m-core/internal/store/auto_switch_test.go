package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"e2m.local/contracts"
)

// The memory store must persist auto-switch decisions, update their lifecycle,
// filter by plan/status newest-first, and enforce fingerprint idempotency - the
// query shape the Phase 4 orchestrator depends on.
func TestMemoryAutoSwitchDecisionLifecycle(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC))
	_, _ = st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-a", Status: contracts.RoutePlanPublished})

	created, err := st.CreateAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		PlanID: "plan-a", UserID: 101, Strategy: contracts.StrategyStabilityFirst,
		FromChannelID: "ch-1", ToChannelID: "ch-2", Fingerprint: "fp-1",
		RiskLevel: contracts.RiskLevelL1, Status: contracts.AutoSwitchObserving,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("create should stamp id/timestamps: %+v", created)
	}

	got, err := st.GetAutoSwitchDecision(ctx, created.ID)
	if err != nil || got.ID != created.ID {
		t.Fatalf("get: %+v err=%v", got, err)
	}

	// While active, the fingerprint is found (idempotency guard).
	if _, err := st.FindActiveAutoSwitchDecisionByFingerprint(ctx, "plan-a", "fp-1"); err != nil {
		t.Fatalf("active fingerprint should be found: %v", err)
	}

	// Resolve it through the lifecycle CAS; generic lifecycle updates are
	// intentionally disabled so stale snapshots cannot revive terminal rows.
	created.Status = contracts.AutoSwitchCompleted
	if _, err := st.TransitionAutoSwitchDecision(ctx, created, contracts.AutoSwitchObserving); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if _, err := st.FindActiveAutoSwitchDecisionByFingerprint(ctx, "plan-a", "fp-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolved fingerprint should not be found, got %v", err)
	}
	if _, err := st.UpdateAutoSwitchDecision(ctx, created); !errors.Is(err, ErrConflict) {
		t.Fatalf("generic lifecycle update error=%v, want ErrConflict", err)
	}

	// A second plan's decision is isolated by plan and status filters.
	if _, err := st.CreateAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		PlanID: "plan-b", Fingerprint: "fp-2", Status: contracts.AutoSwitchProposed,
	}); err != nil {
		t.Fatalf("create plan-b: %v", err)
	}

	planA, _ := st.ListAutoSwitchDecisions(ctx, contracts.AutoSwitchDecisionFilter{PlanID: "plan-a"})
	if len(planA) != 1 || planA[0].PlanID != "plan-a" {
		t.Fatalf("plan filter: %+v", planA)
	}
	proposed, _ := st.ListAutoSwitchDecisions(ctx, contracts.AutoSwitchDecisionFilter{
		Statuses: []contracts.AutoSwitchStatus{contracts.AutoSwitchProposed},
	})
	if len(proposed) != 1 || proposed[0].PlanID != "plan-b" {
		t.Fatalf("status filter: %+v", proposed)
	}
}

func TestMemoryAutoSwitchClaimAndTransitionAreAtomic(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC))
	if _, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-a", Status: contracts.RoutePlanPublished}); err != nil {
		t.Fatal(err)
	}
	callerNow := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	leaseUntil := callerNow.Add(time.Minute)
	input := contracts.AutoSwitchDecision{
		PlanID: "plan-a", Fingerprint: "fp-1", CreatedAt: callerNow, LeaseUntil: &leaseUntil,
	}

	const callers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	var claimCount atomic.Int32
	ids := make(chan string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			d, claimed, err := st.ClaimAutoSwitchDecision(ctx, input)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if claimed {
				claimCount.Add(1)
			}
			ids <- d.ID
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	if claimCount.Load() != 1 {
		t.Fatalf("claim winners=%d, want 1", claimCount.Load())
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

func TestMemoryAutoSwitchLeaseUsesStoreClockAndFencesStaleOwner(t *testing.T) {
	ctx := context.Background()
	storeNow := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	st := NewMemoryStore(time.Time{})
	st.now = func() time.Time { return storeNow }
	if _, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-skew", Status: contracts.RoutePlanPublished}); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	callerNow := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	requestedUntil := callerNow.Add(2 * time.Minute)

	owner, claimed, err := st.ClaimAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		PlanID: "plan-skew", Fingerprint: "fp-skew", CreatedAt: callerNow, LeaseUntil: &requestedUntil,
	})
	if err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}
	wantUntil := storeNow.Add(2 * time.Minute)
	if owner.LeaseUntil == nil || !owner.LeaseUntil.Equal(wantUntil) {
		t.Fatalf("lease_until=%v, want store-clock deadline %v", owner.LeaseUntil, wantUntil)
	}

	storeNow = storeNow.Add(30 * time.Second)
	renewed, err := st.RenewAutoSwitchDecisionLease(ctx, owner.ID, owner.LeaseVersion, time.Minute)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if renewed.LeaseUntil == nil || !renewed.LeaseUntil.Equal(storeNow.Add(time.Minute)) {
		t.Fatalf("renewed lease_until=%v", renewed.LeaseUntil)
	}

	storeNow = storeNow.Add(2 * time.Minute)
	callerRepairNow := time.Date(2040, 2, 3, 4, 5, 6, 0, time.UTC)
	repair, repaired, err := st.ClaimExpiredAutoSwitchDecision(
		ctx, owner.ID, callerRepairNow, callerRepairNow.Add(-time.Minute), callerRepairNow.Add(3*time.Minute),
	)
	if err != nil || !repaired {
		t.Fatalf("repair claim: repaired=%v err=%v", repaired, err)
	}
	if repair.LeaseVersion != owner.LeaseVersion+1 {
		t.Fatalf("repair version=%d, want %d", repair.LeaseVersion, owner.LeaseVersion+1)
	}
	if repair.LeaseUntil == nil || !repair.LeaseUntil.Equal(storeNow.Add(3*time.Minute)) {
		t.Fatalf("repair lease_until=%v", repair.LeaseUntil)
	}

	if _, err := st.RenewAutoSwitchDecisionLease(ctx, owner.ID, owner.LeaseVersion, time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale owner renew error=%v, want ErrConflict", err)
	}
	owner.Status = contracts.AutoSwitchCompleted
	if _, err := st.TransitionAutoSwitchDecision(ctx, owner, contracts.AutoSwitchApplying); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale owner transition error=%v, want ErrConflict", err)
	}
}

func TestMemoryObservationClaimAdvancesLeaseGeneration(t *testing.T) {
	ctx := context.Background()
	storeNow := time.Date(2030, 7, 14, 1, 2, 3, 0, time.UTC)
	st := NewMemoryStore(time.Time{})
	st.now = func() time.Time { return storeNow }
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-observe", Status: contracts.RoutePlanPublished, SchedulingGeneration: 7})
	if err != nil {
		t.Fatal(err)
	}
	observing, err := st.CreateAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		PlanID: "plan-observe", Fingerprint: "fp-observe", Status: contracts.AutoSwitchObserving,
		LeaseVersion: 7, SchedulingGeneration: plan.SchedulingGeneration,
	})
	if err != nil {
		t.Fatalf("create observing: %v", err)
	}
	claimed, err := st.ClaimAutoSwitchObservation(ctx, observing, 90*time.Second)
	if err != nil {
		t.Fatalf("claim observation: %v", err)
	}
	if claimed.Status != contracts.AutoSwitchApplying || claimed.LeaseVersion != 8 {
		t.Fatalf("claimed=%+v", claimed)
	}
	if claimed.LeaseUntil == nil || !claimed.LeaseUntil.Equal(storeNow.Add(90*time.Second)) {
		t.Fatalf("lease_until=%v", claimed.LeaseUntil)
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
	if err := st.ReleaseAutoSwitchDecisionLease(ctx, claimed.ID, claimed.LeaseVersion-1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale release error=%v, want ErrConflict", err)
	}
	if err := st.ReleaseAutoSwitchDecisionLease(ctx, claimed.ID, claimed.LeaseVersion); err != nil {
		t.Fatalf("release: %v", err)
	}
	claimed.Status = contracts.AutoSwitchCompleted
	claimed.LeaseUntil = nil
	if _, err := st.TransitionAutoSwitchDecision(ctx, claimed, contracts.AutoSwitchApplying); !errors.Is(err, ErrConflict) {
		t.Fatalf("transition after release error=%v, want ErrConflict", err)
	}
}

func TestMemoryExpiredAutoSwitchClaimHasOneRepairOwner(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Time{})
	now := time.Now().UTC()
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-repair", Status: contracts.RoutePlanPublished})
	if err != nil {
		t.Fatal(err)
	}
	expired := now.Add(-time.Minute)
	decision, err := st.CreateAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		PlanID: "plan-repair", Fingerprint: "fp-repair", Status: contracts.AutoSwitchApplying,
		LeaseUntil: &expired, SchedulingGeneration: plan.SchedulingGeneration,
	})
	if err != nil {
		t.Fatalf("create expired decision: %v", err)
	}

	const callers = 16
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

func TestMemorySchedulingGenerationAdvancesAcrossDecisionsAndOwners(t *testing.T) {
	ctx := context.Background()
	storeNow := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	st := NewMemoryStore(time.Time{})
	st.now = func() time.Time { return storeNow }
	if _, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-generation", Status: contracts.RoutePlanPublished}); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	leaseUntil := storeNow.Add(time.Minute)

	first, claimed, err := st.ClaimAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		PlanID: "plan-generation", Fingerprint: "first", CreatedAt: storeNow, LeaseUntil: &leaseUntil,
	})
	if err != nil || !claimed || first.SchedulingGeneration <= 0 {
		t.Fatalf("first claim=%+v claimed=%v err=%v", first, claimed, err)
	}
	// A duplicate fingerprint returns the existing owner and must not change its
	// persisted generation, even though other plan scopes may consume values.
	duplicate, claimed, err := st.ClaimAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		PlanID: "plan-generation", Fingerprint: "first", CreatedAt: storeNow, LeaseUntil: &leaseUntil,
	})
	if err != nil || claimed || duplicate.SchedulingGeneration != first.SchedulingGeneration {
		t.Fatalf("duplicate claim=%+v claimed=%v err=%v", duplicate, claimed, err)
	}

	first.Status = contracts.AutoSwitchCompleted
	first.LeaseUntil = nil
	if _, err := st.TransitionAutoSwitchDecision(ctx, first, contracts.AutoSwitchApplying); err != nil {
		t.Fatalf("complete first decision: %v", err)
	}
	second, claimed, err := st.ClaimAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		PlanID: "plan-generation", Fingerprint: "second", CreatedAt: storeNow, LeaseUntil: &leaseUntil,
	})
	if err != nil || !claimed || second.SchedulingGeneration <= first.SchedulingGeneration {
		t.Fatalf("second claim=%+v first_generation=%d claimed=%v err=%v", second, first.SchedulingGeneration, claimed, err)
	}

	second.Status = contracts.AutoSwitchObserving
	second.LeaseUntil = nil
	observing, err := st.TransitionAutoSwitchDecision(ctx, second, contracts.AutoSwitchApplying)
	if err != nil {
		t.Fatalf("start observing: %v", err)
	}
	observation, err := st.ClaimAutoSwitchObservation(ctx, observing, time.Minute)
	if err != nil || observation.SchedulingGeneration <= second.SchedulingGeneration {
		t.Fatalf("observation claim=%+v err=%v", observation, err)
	}

	storeNow = storeNow.Add(2 * time.Minute)
	repair, repaired, err := st.ClaimExpiredAutoSwitchDecision(
		ctx, observation.ID, storeNow, storeNow.Add(-time.Minute), storeNow.Add(time.Minute),
	)
	if err != nil || !repaired || repair.SchedulingGeneration <= observation.SchedulingGeneration {
		t.Fatalf("repair claim=%+v repaired=%v err=%v", repair, repaired, err)
	}

	manual, err := st.ClaimRoutePlanScheduling(ctx, "plan-generation", contracts.RoutePlanPublished)
	if err != nil || manual.SchedulingGeneration <= repair.SchedulingGeneration {
		t.Fatalf("manual generation=%d repair_generation=%d err=%v", manual.SchedulingGeneration, repair.SchedulingGeneration, err)
	}
}

func TestMemoryPlanGenerationFencesStaleDecisionTransition(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	st := NewMemoryStore(time.Time{})
	if _, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-stale", Status: contracts.RoutePlanPublished}); err != nil {
		t.Fatal(err)
	}
	leaseUntil := now.Add(time.Minute)
	decision, claimed, err := st.ClaimAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		PlanID: "plan-stale", Fingerprint: "stale", CreatedAt: now, LeaseUntil: &leaseUntil,
	})
	if err != nil || !claimed {
		t.Fatalf("claim decision: claimed=%v err=%v", claimed, err)
	}
	if _, err := st.ClaimRoutePlanScheduling(ctx, "plan-stale", contracts.RoutePlanPublished); err != nil {
		t.Fatalf("supersede plan: %v", err)
	}
	decision.Status = contracts.AutoSwitchCompleted
	decision.LeaseUntil = nil
	if _, err := st.TransitionAutoSwitchDecision(ctx, decision, contracts.AutoSwitchApplying); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale transition error=%v, want ErrConflict", err)
	}
}
