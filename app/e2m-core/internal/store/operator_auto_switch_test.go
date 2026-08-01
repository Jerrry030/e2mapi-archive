package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryClaimApprovedAutoSwitchDecisionHasSingleOwner(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC))
	user, err := st.CreateUser(ctx, contracts.User{
		Email: "operator-claim@local.dev", Roles: []contracts.UserRole{contracts.UserRoleOwner}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "operator-claim", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "operator-claim", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID, Status: contracts.RoutePlanPublished,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := st.CreateAutoSwitchDecision(ctx, contracts.AutoSwitchDecision{
		UserID: user.ID, PlanID: plan.ID, InstanceID: instance.ID, PoolID: pool.ID,
		Fingerprint: "operator-claim-fingerprint", Status: contracts.AutoSwitchApproved,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.SchedulingGeneration != plan.SchedulingGeneration {
		t.Fatalf("decision generation=%d plan=%d", decision.SchedulingGeneration, plan.SchedulingGeneration)
	}

	type result struct {
		decision contracts.AutoSwitchDecision
		claimed  bool
		err      error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, owns, claimErr := st.ClaimApprovedAutoSwitchDecision(ctx, decision.ID, time.Minute)
			results <- result{decision: claimed, claimed: owns, err: claimErr}
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.claimed {
			winners++
			if result.decision.Status != contracts.AutoSwitchApplying || result.decision.LeaseUntil == nil ||
				result.decision.LeaseVersion != 1 || result.decision.SchedulingGeneration != plan.SchedulingGeneration+1 {
				t.Fatalf("invalid owner claim: %+v", result.decision)
			}
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners=%d, want 1", winners)
	}
}

func TestApprovedIsActiveRejectedIsTerminal(t *testing.T) {
	if !contracts.AutoSwitchApproved.IsActive() {
		t.Fatal("approved decision must retain the active fingerprint")
	}
	if contracts.AutoSwitchRejected.IsActive() {
		t.Fatal("rejected decision must release the active fingerprint")
	}
}
