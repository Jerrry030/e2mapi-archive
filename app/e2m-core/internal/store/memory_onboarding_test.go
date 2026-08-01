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

func TestMemoryOnboardingWorkflowLeaseCASAndRepair(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }

	created, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: 7, InstanceID: "inst-a", PoolID: "pool-a",
	})
	if err != nil {
		t.Fatalf("upsert waiting workflow: %v", err)
	}
	if created.ConnectorID != "" || created.Stage != contracts.OnboardingWaitingConnector ||
		created.Status != contracts.OnboardingPending || created.Version != 1 {
		t.Fatalf("unexpected defaults: %+v", created)
	}
	duplicate, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: 7, InstanceID: "inst-a", PoolID: "pool-a",
	})
	if err != nil || duplicate.ID != created.ID || duplicate.Version != created.Version {
		t.Fatalf("idempotent upsert=%+v err=%v", duplicate, err)
	}
	if _, ok, err := st.ClaimOnboardingWorkflow(ctx, "waiting-worker", time.Minute); err != nil || ok {
		t.Fatalf("workflow without connector was claimable: ok=%v err=%v", ok, err)
	}
	created, err = st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: 7, InstanceID: "inst-a", PoolID: "pool-a", ConnectorID: "connector-a",
	})
	if err != nil || created.ConnectorID != "connector-a" {
		t.Fatalf("bind connector: workflow=%+v err=%v", created, err)
	}

	var winners atomic.Int32
	claimed := make(chan contracts.OnboardingWorkflow, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			workflow, ok, claimErr := st.ClaimOnboardingWorkflow(ctx, "worker-"+string(rune('a'+worker)), time.Minute)
			if claimErr != nil {
				t.Errorf("claim: %v", claimErr)
				return
			}
			if ok {
				winners.Add(1)
				claimed <- workflow
			}
		}(i)
	}
	wg.Wait()
	close(claimed)
	if winners.Load() != 1 {
		t.Fatalf("claim winners=%d, want 1", winners.Load())
	}
	owner := <-claimed
	if owner.Attempts != 1 || owner.Version != 3 || owner.LeaseUntil == nil {
		t.Fatalf("claim fields: %+v", owner)
	}
	if _, ok, err := st.ClaimOnboardingWorkflow(ctx, "live-contender", time.Minute); err != nil || ok {
		t.Fatalf("live lease was stolen: ok=%v err=%v", ok, err)
	}
	renewed, err := st.RenewOnboardingWorkflowLease(ctx, owner.ID, owner.LeaseOwner, owner.Version, time.Minute)
	if err != nil || renewed.Version != owner.Version+1 || renewed.LeaseUntil == nil ||
		!renewed.LeaseUntil.Equal(now.Add(time.Minute)) {
		t.Fatalf("renewed workflow=%+v err=%v", renewed, err)
	}
	if _, err := st.RenewOnboardingWorkflowLease(ctx, owner.ID, owner.LeaseOwner, owner.Version, time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale renew error=%v, want ErrConflict", err)
	}
	if err := st.ReleaseOnboardingWorkflowLease(ctx, owner.ID, owner.LeaseOwner, owner.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale release error=%v, want ErrConflict", err)
	}
	owner = renewed

	advance := owner
	advance.Stage = contracts.OnboardingCheckingGateway
	advanced, err := st.TransitionOnboardingWorkflow(ctx, advance, owner.Version)
	if err != nil {
		t.Fatalf("advance running stage: %v", err)
	}
	if advanced.Status != contracts.OnboardingRunning || advanced.Version != 5 || advanced.LeaseUntil == nil {
		t.Fatalf("advanced workflow: %+v", advanced)
	}
	if _, err := st.TransitionOnboardingWorkflow(ctx, advance, owner.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS error=%v, want ErrConflict", err)
	}

	// An expired running lease is repairable exactly once and fences the old
	// owner by advancing both attempts and version.
	now = now.Add(2 * time.Minute)
	repaired, ok, err := st.ClaimOnboardingWorkflow(ctx, "repair-worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("repair claim=%+v ok=%v err=%v", repaired, ok, err)
	}
	if repaired.Attempts != 2 || repaired.Version != 6 || repaired.LeaseOwner != "repair-worker" {
		t.Fatalf("repair fields: %+v", repaired)
	}
	if _, err := st.TransitionOnboardingWorkflow(ctx, advanced, advanced.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired owner transition error=%v, want ErrConflict", err)
	}

	retryAt := now.Add(5 * time.Minute)
	retryable := repaired
	retryable.Stage = contracts.OnboardingFailedRetryable
	retryable.Status = contracts.OnboardingRetryable
	retryable.NextAttemptAt = &retryAt
	retryable.LastErrorCode = "gateway_unavailable"
	retried, err := st.TransitionOnboardingWorkflow(ctx, retryable, repaired.Version)
	if err != nil {
		t.Fatalf("schedule retry: %v", err)
	}
	if retried.LeaseOwner != "" || retried.LeaseUntil != nil {
		t.Fatalf("retry transition retained lease: %+v", retried)
	}
	if _, ok, err := st.ClaimOnboardingWorkflow(ctx, "early", time.Minute); err != nil || ok {
		t.Fatalf("early retry claim ok=%v err=%v", ok, err)
	}
	now = retryAt
	retryClaim, ok, err := st.ClaimOnboardingWorkflow(ctx, "retry", time.Minute)
	if err != nil || !ok || retryClaim.ID != created.ID {
		t.Fatalf("due retry claim=%+v ok=%v err=%v", retryClaim, ok, err)
	}
	retryClaim.Stage = contracts.OnboardingActive
	retryClaim.Status = contracts.OnboardingReady
	retryClaim.KeyVersionSummary = map[string]int64{"channel-a": 3}
	activeCheckAt := now.Add(time.Minute)
	retryClaim.NextAttemptAt = &activeCheckAt
	active, err := st.TransitionOnboardingWorkflow(ctx, retryClaim, retryClaim.Version)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if _, ok, err := st.ClaimOnboardingWorkflow(ctx, "unused", time.Minute); err != nil || ok {
		t.Fatalf("active workflow was claimable: %+v ok=%v err=%v", active, ok, err)
	}
	now = activeCheckAt
	activeCheck, ok, err := st.ClaimOnboardingWorkflow(ctx, "active-check", time.Minute)
	if err != nil || !ok || activeCheck.ID != active.ID || activeCheck.Stage != contracts.OnboardingActive {
		t.Fatalf("due active workflow was not claimable: %+v ok=%v err=%v", activeCheck, ok, err)
	}
}

func TestMemoryOnboardingWorkflowReleaseAllowsSingleImmediateTakeover(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }
	created, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: 11, InstanceID: "inst-release", PoolID: "pool-release", ConnectorID: "connector-release",
	})
	if err != nil {
		t.Fatal(err)
	}
	owner, ok, err := st.ClaimOnboardingWorkflow(ctx, "old-process", 10*time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", owner, ok, err)
	}
	if err := st.ReleaseOnboardingWorkflowLease(ctx, owner.ID, owner.LeaseOwner, owner.Version); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := st.TransitionOnboardingWorkflow(ctx, owner, owner.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("released owner transition error=%v, want ErrConflict", err)
	}

	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			_, claimed, claimErr := st.ClaimOnboardingWorkflow(ctx, "new-process-"+string(rune('a'+worker)), time.Minute)
			if claimErr != nil {
				t.Errorf("takeover claim: %v", claimErr)
				return
			}
			if claimed {
				winners.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("takeover winners=%d, want 1 (workflow %s)", winners.Load(), created.ID)
	}
}

func TestMemoryOnboardingConnectorChangeResetsWorkflow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }
	created, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: 8, InstanceID: "inst-b", PoolID: "pool-b", ConnectorID: "connector-old",
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := st.ClaimOnboardingWorkflow(ctx, "worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	claimed.Stage = contracts.OnboardingActive
	claimed.Status = contracts.OnboardingReady
	claimed.PlanID = "plan-b"
	claimed.KeyVersionSummary = map[string]int64{"channel-b": 4}
	activeCheckAt := now.Add(time.Minute)
	claimed.NextAttemptAt = &activeCheckAt
	if _, err := st.TransitionOnboardingWorkflow(ctx, claimed, claimed.Version); err != nil {
		t.Fatal(err)
	}

	reset, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: 8, InstanceID: "inst-b", PoolID: "pool-b", ConnectorID: "connector-new",
	})
	if err != nil {
		t.Fatalf("reset connector: %v", err)
	}
	if reset.ID != created.ID || reset.Stage != contracts.OnboardingWaitingConnector ||
		reset.Status != contracts.OnboardingPending || reset.Attempts != 0 ||
		reset.ConnectorID != "connector-new" || len(reset.KeyVersionSummary) != 0 {
		t.Fatalf("connector reset: %+v", reset)
	}
}

func TestMemoryOnboardingDesiredFingerprintChangeResetsActiveWorkflow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }
	created, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: 9, InstanceID: "inst-c", PoolID: "pool-c", ConnectorID: "connector-c",
		DesiredFingerprint: "fingerprint-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := st.ClaimOnboardingWorkflow(ctx, "worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	claimed.Stage, claimed.Status = contracts.OnboardingActive, contracts.OnboardingReady
	claimed.PlanID = "plan-c"
	claimed.KeyVersionSummary = map[string]int64{"channel-c": 1}
	recheckAt := now.Add(time.Hour)
	claimed.NextAttemptAt = &recheckAt
	if _, err := st.TransitionOnboardingWorkflow(ctx, claimed, claimed.Version); err != nil {
		t.Fatal(err)
	}

	reset, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: 9, InstanceID: "inst-c", PoolID: "pool-c", ConnectorID: "connector-c",
		DesiredFingerprint: "fingerprint-v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reset.ID != created.ID || reset.Status != contracts.OnboardingPending ||
		reset.Stage != contracts.OnboardingCheckingGateway || reset.NextAttemptAt != nil ||
		reset.DesiredGeneration != created.DesiredGeneration+1 || reset.DesiredFingerprint != "fingerprint-v2" ||
		len(reset.KeyVersionSummary) != 0 {
		t.Fatalf("desired reset=%+v", reset)
	}
}

func TestMemoryOnboardingNewWorkflowAlwaysStartsAtGenerationOne(t *testing.T) {
	st := NewMemoryStore(time.Now().UTC())
	created, err := st.UpsertOnboardingWorkflow(context.Background(), contracts.OnboardingWorkflow{
		UserID: 10, InstanceID: "inst-generation", PoolID: "pool-generation",
		DesiredGeneration: 99, DesiredFingerprint: "fingerprint",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.DesiredGeneration != 1 {
		t.Fatalf("desired generation=%d, want 1", created.DesiredGeneration)
	}
}

func TestMemoryOnboardingDormantIsNotClaimedAndWakesOnActiveDiscoveryUpsert(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }
	created, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: 12, InstanceID: "inst-dormant", PoolID: "pool-dormant", ConnectorID: "connector-dormant",
		DesiredFingerprint: "same-fingerprint",
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := st.ClaimOnboardingWorkflow(ctx, "worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	claimed.Stage = contracts.OnboardingDormant
	claimed.Status = contracts.OnboardingDormantStatus
	claimed.LastErrorCode = "pool_inactive"
	claimed.NextAttemptAt = nil
	dormant, err := st.TransitionOnboardingWorkflow(ctx, claimed, claimed.Version)
	if err != nil {
		t.Fatalf("dormant transition: %v", err)
	}
	if dormant.Stage != contracts.OnboardingDormant || dormant.Status != contracts.OnboardingDormantStatus ||
		dormant.LastErrorCode != "pool_inactive" || dormant.NextAttemptAt != nil || dormant.LeaseOwner != "" {
		t.Fatalf("dormant=%+v", dormant)
	}
	if _, ok, err := st.ClaimOnboardingWorkflow(ctx, "should-not-claim", time.Minute); err != nil || ok {
		t.Fatalf("dormant was claimable: ok=%v err=%v", ok, err)
	}
	// Discovery only upserts active pools. The same desired state must still
	// wake a dormant workflow and advance its generation/version.
	woken, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: 12, InstanceID: "inst-dormant", PoolID: "pool-dormant", ConnectorID: "connector-dormant",
		DesiredFingerprint: "same-fingerprint",
	})
	if err != nil {
		t.Fatal(err)
	}
	if woken.ID != created.ID || woken.Stage != contracts.OnboardingCheckingGateway ||
		woken.Status != contracts.OnboardingPending || woken.DesiredGeneration != dormant.DesiredGeneration+1 ||
		woken.LastErrorCode != "" || woken.Attempts != 0 {
		t.Fatalf("woken=%+v", woken)
	}
}
