package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPostgresOnboardingWorkflowClaimCASAndRepair(t *testing.T) {
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

	suffix := newID("onboarding-pg")
	user, err := st.CreateUser(ctx, contracts.User{
		Email: "onboarding-" + suffix + "@example.com",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID, Name: "Onboarding", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	poolID := "pool-" + suffix
	if _, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: "Onboarding"}); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM onboarding_workflows WHERE instance_id=$1`, instance.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_pools WHERE id=$1`, poolID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM instances WHERE id=$1`, instance.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user.ID)
	})

	created, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: user.ID, InstanceID: instance.ID, PoolID: poolID,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if created.ConnectorID != "" || created.Stage != contracts.OnboardingWaitingConnector || created.Version != 1 {
		t.Fatalf("created defaults: %+v", created)
	}
	idempotent, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: user.ID, InstanceID: instance.ID, PoolID: poolID,
	})
	if err != nil || idempotent.ID != created.ID || idempotent.Version != created.Version {
		t.Fatalf("idempotent upsert=%+v err=%v", idempotent, err)
	}
	if _, claimed, err := st.ClaimOnboardingWorkflow(ctx, "pg-waiting", time.Minute); err != nil || claimed {
		t.Fatalf("workflow without connector was claimable: claimed=%v err=%v", claimed, err)
	}
	created, err = st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: user.ID, InstanceID: instance.ID, PoolID: poolID, ConnectorID: "connector-" + suffix,
	})
	if err != nil {
		t.Fatalf("bind connector: %v", err)
	}

	results := make(chan contracts.OnboardingWorkflow, 6)
	errs := make(chan error, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			workflow, claimed, claimErr := st.ClaimOnboardingWorkflow(ctx, "pg-worker-"+string(rune('a'+worker)), time.Minute)
			if claimErr != nil {
				errs <- claimErr
				return
			}
			if claimed {
				results <- workflow
			}
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for claimErr := range errs {
		t.Fatalf("concurrent claim: %v", claimErr)
	}
	var owner contracts.OnboardingWorkflow
	winners := 0
	for result := range results {
		owner = result
		winners++
	}
	if winners != 1 || owner.Attempts != 1 || owner.Version != 3 {
		t.Fatalf("claim winners=%d owner=%+v", winners, owner)
	}
	if _, claimed, err := st.ClaimOnboardingWorkflow(ctx, "pg-live-contender", time.Minute); err != nil || claimed {
		t.Fatalf("live lease was stolen: claimed=%v err=%v", claimed, err)
	}
	var dbBefore time.Time
	if err := st.pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&dbBefore); err != nil {
		t.Fatalf("read database clock before renew: %v", err)
	}
	renewed, err := st.RenewOnboardingWorkflowLease(ctx, owner.ID, owner.LeaseOwner, owner.Version, 2*time.Minute)
	var dbAfter time.Time
	if clockErr := st.pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&dbAfter); clockErr != nil {
		t.Fatalf("read database clock after renew: %v", clockErr)
	}
	if err != nil || renewed.Version != owner.Version+1 || renewed.LeaseUntil == nil ||
		renewed.LeaseUntil.Before(dbBefore.Add(2*time.Minute)) || renewed.LeaseUntil.After(dbAfter.Add(2*time.Minute)) {
		t.Fatalf("renewed workflow=%+v err=%v", renewed, err)
	}
	if _, err := st.RenewOnboardingWorkflowLease(ctx, owner.ID, owner.LeaseOwner, owner.Version, time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale renew error=%v, want ErrConflict", err)
	}
	if err := st.ReleaseOnboardingWorkflowLease(ctx, owner.ID, owner.LeaseOwner, owner.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale release error=%v, want ErrConflict", err)
	}
	owner = renewed

	owner.Stage = contracts.OnboardingCheckingGateway
	advanced, err := st.TransitionOnboardingWorkflow(ctx, owner, owner.Version)
	if err != nil {
		t.Fatalf("advance stage: %v", err)
	}
	if _, err := st.TransitionOnboardingWorkflow(ctx, owner, owner.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS error=%v, want ErrConflict", err)
	}
	if _, err := st.pool.Exec(ctx,
		`UPDATE onboarding_workflows SET lease_until=statement_timestamp()-interval '1 second' WHERE id=$1`, created.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	repaired, claimed, err := st.ClaimOnboardingWorkflow(ctx, "pg-repair", 2*time.Minute)
	if err != nil || !claimed {
		t.Fatalf("repair=%+v claimed=%v err=%v", repaired, claimed, err)
	}
	if repaired.ID != created.ID || repaired.Attempts != 2 || repaired.Version != advanced.Version+1 || repaired.LeaseOwner != "pg-repair" {
		t.Fatalf("repair fields: %+v", repaired)
	}
	if _, err := st.TransitionOnboardingWorkflow(ctx, advanced, advanced.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("old lease transition error=%v, want ErrConflict", err)
	}

	repaired.Stage = contracts.OnboardingActive
	repaired.Status = contracts.OnboardingReady
	repaired.KeyVersionSummary = map[string]int64{"channel-version-only": 9}
	activeCheckAt := time.Now().UTC().Add(time.Minute)
	repaired.NextAttemptAt = &activeCheckAt
	active, err := st.TransitionOnboardingWorkflow(ctx, repaired, repaired.Version)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if active.LeaseOwner != "" || active.LeaseUntil != nil || active.KeyVersionSummary["channel-version-only"] != 9 {
		t.Fatalf("active fields: %+v", active)
	}
	if _, claimed, err := st.ClaimOnboardingWorkflow(ctx, "pg-unused", time.Minute); err != nil || claimed {
		t.Fatalf("active claim claimed=%v err=%v", claimed, err)
	}

	reset, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: user.ID, InstanceID: instance.ID, PoolID: poolID, ConnectorID: "replacement-connector",
	})
	if err != nil {
		t.Fatalf("connector reset: %v", err)
	}
	if reset.ID != created.ID || reset.Stage != contracts.OnboardingWaitingConnector ||
		reset.Status != contracts.OnboardingPending || reset.Attempts != 0 || len(reset.KeyVersionSummary) != 0 {
		t.Fatalf("reset fields: %+v", reset)
	}
}

func TestPostgresOnboardingWorkflowReleaseAllowsImmediateTakeover(t *testing.T) {
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

	suffix := newID("onboarding-release-pg")
	user, err := st.CreateUser(ctx, contracts.User{
		Email: suffix + "@example.com", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "Release", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatal(err)
	}
	poolID := "pool-" + suffix
	if _, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: "Release"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM onboarding_workflows WHERE instance_id=$1`, instance.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_pools WHERE id=$1`, poolID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM instances WHERE id=$1`, instance.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user.ID)
	})
	created, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
		UserID: user.ID, InstanceID: instance.ID, PoolID: poolID, ConnectorID: "connector-" + suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	owner, claimed, err := st.ClaimOnboardingWorkflow(ctx, "old-pg-process", 10*time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim=%+v claimed=%v err=%v", owner, claimed, err)
	}
	if err := st.ReleaseOnboardingWorkflowLease(ctx, owner.ID, owner.LeaseOwner, owner.Version); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := st.TransitionOnboardingWorkflow(ctx, owner, owner.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("released transition error=%v, want ErrConflict", err)
	}
	replacement, claimed, err := st.ClaimOnboardingWorkflow(ctx, "new-pg-process", time.Minute)
	if err != nil || !claimed || replacement.ID != created.ID || replacement.LeaseOwner != "new-pg-process" {
		t.Fatalf("replacement=%+v claimed=%v err=%v", replacement, claimed, err)
	}
}

func TestPostgresOnboardingDormantIsNotClaimedAndSameFingerprintWakes(t *testing.T) {
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
	suffix := newID("onboarding-dormant-pg")
	user, err := st.CreateUser(ctx, contracts.User{Email: suffix + "@example.com", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "Dormant", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatal(err)
	}
	poolID := "pool-" + suffix
	if _, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: "Dormant", Status: contracts.UpstreamPoolActive}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM onboarding_workflows WHERE instance_id=$1`, instance.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_pools WHERE id=$1`, poolID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM instances WHERE id=$1`, instance.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user.ID)
	})
	created, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{UserID: user.ID, InstanceID: instance.ID, PoolID: poolID, ConnectorID: "connector-" + suffix, DesiredFingerprint: "same"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := st.ClaimOnboardingWorkflow(ctx, "dormant-worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	claimed.Stage, claimed.Status = contracts.OnboardingDormant, contracts.OnboardingDormantStatus
	claimed.LastErrorCode, claimed.NextAttemptAt = "pool_inactive", nil
	dormant, err := st.TransitionOnboardingWorkflow(ctx, claimed, claimed.Version)
	if err != nil {
		t.Fatalf("transition dormant: %v", err)
	}
	if _, ok, err := st.ClaimOnboardingWorkflow(ctx, "noise-worker", time.Minute); err != nil || ok {
		t.Fatalf("dormant claim ok=%v err=%v", ok, err)
	}
	woken, err := st.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{UserID: user.ID, InstanceID: instance.ID, PoolID: poolID, ConnectorID: claimed.ConnectorID, DesiredFingerprint: "same"})
	if err != nil {
		t.Fatal(err)
	}
	if woken.ID != created.ID || woken.Status != contracts.OnboardingPending || woken.Stage != contracts.OnboardingCheckingGateway ||
		woken.DesiredGeneration <= dormant.DesiredGeneration || woken.LastErrorCode != "" {
		t.Fatalf("woken=%+v dormant=%+v", woken, dormant)
	}
}

func TestPostgresClaimPlanChannelsConcurrentUsersAndIdempotency(t *testing.T) {
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

	suffix := newID("plan-channel-pg")
	poolID := "pool-" + suffix
	sourceID := "source-" + suffix
	channelIDs := []string{"key-slow-" + suffix, "key-best-" + suffix}
	users := make([]contracts.User, 2)
	instances := make([]contracts.Instance, 2)
	plans := make([]contracts.RoutePlan, 2)
	for i := range users {
		users[i], err = st.CreateUser(ctx, contracts.User{
			Email: "claim-" + string(rune('a'+i)) + "-" + suffix + "@example.com",
			Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
		})
		if err != nil {
			t.Fatalf("create user %d: %v", i, err)
		}
		instances[i], err = st.CreateInstance(ctx, contracts.Instance{
			UserID: users[i].ID, Name: "Claim", Kind: contracts.InstanceKindSub2API,
		})
		if err != nil {
			t.Fatalf("create instance %d: %v", i, err)
		}
	}
	if _, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: "Claim"}); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	for i, channelID := range channelIDs {
		if _, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
			ID: channelID, PoolID: poolID, SourceID: sourceID,
			DisplayName: channelID, Priority: 10 - i*9,
			AccountOwnership: contracts.GatewayAccountPlatformManaged,
		}); err != nil {
			t.Fatalf("create channel: %v", err)
		}
	}
	for i := range plans {
		userID := users[0].ID
		if i == 1 {
			userID = users[1].ID
		}
		plans[i], err = st.CreateRoutePlan(ctx, contracts.RoutePlan{
			ID:     "plan-" + string(rune('a'+i)) + "-" + suffix,
			UserID: userID, InstanceID: instances[i].ID, PoolID: poolID,
		})
		if err != nil {
			t.Fatalf("create plan %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		planIDs := []string{plans[0].ID, plans[1].ID, "plan-reuse-" + suffix}
		instanceIDs := []string{instances[0].ID, instances[1].ID}
		userIDs := []int64{users[0].ID, users[1].ID}
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM published_bindings WHERE plan_id=ANY($1)`, planIDs)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_channel_allocations WHERE channel_id=ANY($1)`, channelIDs)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM route_plans WHERE id=ANY($1)`, planIDs)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_channels WHERE id=ANY($1)`, channelIDs)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_pools WHERE id=$1`, poolID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM instances WHERE id=ANY($1)`, instanceIDs)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=ANY($1)`, userIDs)
	})

	type claimResult struct {
		planIndex int
		channels  []contracts.UpstreamChannel
		err       error
	}
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			channels, claimErr := st.ClaimPlanChannels(ctx, plans[i].ID)
			results <- claimResult{planIndex: i, channels: channels, err: claimErr}
		}()
	}
	wg.Wait()
	close(results)
	winner := -1
	selectedIDs := make(map[string]struct{})
	for result := range results {
		if result.err != nil {
			t.Fatalf("claim plan %d: %v", result.planIndex, result.err)
		}
		if len(result.channels) == 1 {
			if _, duplicate := selectedIDs[result.channels[0].ID]; duplicate {
				t.Fatalf("both users received channel %s", result.channels[0].ID)
			}
			selectedIDs[result.channels[0].ID] = struct{}{}
			if result.channels[0].ID != channelIDs[1] {
				continue
			}
			winner = result.planIndex
		} else if len(result.channels) != 0 {
			t.Fatalf("unexpected selection: %+v", result.channels)
		}
	}
	if winner < 0 || len(selectedIDs) != 2 {
		t.Fatalf("selections=%v winner=%d, want two distinct keys including highest priority", selectedIDs, winner)
	}

	repeated, err := st.ClaimPlanChannels(ctx, plans[winner].ID)
	if err != nil || len(repeated) != 1 || repeated[0].ID != channelIDs[1] {
		t.Fatalf("idempotent claim=%+v err=%v", repeated, err)
	}
	bindings, err := st.ListPublishedBindings(ctx, plans[winner].ID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("winner bindings=%+v err=%v", bindings, err)
	}
	bindings[0].State = contracts.BindingRevoked
	if _, err := st.UpsertPublishedBinding(ctx, bindings[0]); err != nil {
		t.Fatalf("revoke binding: %v", err)
	}
	repeated, err = st.ClaimPlanChannels(ctx, plans[winner].ID)
	if err != nil || len(repeated) != 1 || repeated[0].ID != channelIDs[1] {
		t.Fatalf("claim after revoke=%+v err=%v", repeated, err)
	}
	bindings, _ = st.ListPublishedBindings(ctx, plans[winner].ID)
	if bindings[0].State != contracts.BindingRevoked {
		t.Fatalf("claim rewrote revoked binding: %+v", bindings[0])
	}

	// A second plan for the winning user reuses that exact Key, never the other
	// credential from the same source.
	sameOwnerPlan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: "plan-reuse-" + suffix, UserID: users[winner].ID,
		InstanceID: "instance-reuse-" + suffix, PoolID: poolID,
	})
	if err != nil {
		t.Fatalf("create same owner plan: %v", err)
	}
	sameOwner, err := st.ClaimPlanChannels(ctx, sameOwnerPlan.ID)
	if err != nil || len(sameOwner) != 1 || sameOwner[0].ID != channelIDs[1] {
		t.Fatalf("same owner claim=%+v err=%v", sameOwner, err)
	}
	var allocationCount int
	if err := st.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM upstream_channel_allocations WHERE channel_id=ANY($1)`, channelIDs).Scan(&allocationCount); err != nil {
		t.Fatalf("count allocations: %v", err)
	}
	if allocationCount != 2 {
		t.Fatalf("allocations=%d, want 2 distinct permanent keys", allocationCount)
	}
}
