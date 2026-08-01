package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPublishedBindingExecutionIdentityMigrationIsDatabaseEnforced(t *testing.T) {
	up, err := migrationsFS.ReadFile("migrations/0072_published_binding_execution_identity.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(up))
	for _, required := range []string{
		"create or replace function enforce_published_binding_execution_identity",
		"new.id is distinct from old.id",
		"new.plan_id is distinct from old.plan_id",
		"new.instance_id is distinct from old.instance_id",
		"new.channel_id is distinct from old.channel_id",
		"new.scheduling_generation < old.scheduling_generation",
		"new.remote_id is distinct from old.remote_id",
		"new.scheduling_generation = old.scheduling_generation",
		"new.verification_status := 'published_pending'",
		"new.verification_source := 'publish'",
		"new.verified_at := null",
		"before update on published_bindings",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("0072 up migration lacks %q", required)
		}
	}
	down, err := migrationsFS.ReadFile("migrations/0072_published_binding_execution_identity.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := strings.ToLower(string(down))
	if !strings.Contains(downSQL, "drop trigger if exists trg_enforce_published_binding_execution_identity") ||
		!strings.Contains(downSQL, "drop function if exists enforce_published_binding_execution_identity()") {
		t.Fatal("0072 down migration does not reverse its trigger and function")
	}
}

func TestPostgresPublishedBindingExecutionIdentityLive(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("E2M_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL store: %v", err)
	}
	t.Cleanup(st.Close)

	suffix := newID("binding-identity-live")
	poolID, planID := "pool-"+suffix, "plan-"+suffix
	channelID := "channel-" + suffix
	instanceID := "instance-" + suffix
	if _, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: suffix}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		ID: channelID, PoolID: poolID, SourceID: "source-" + suffix,
		AccountOwnership: contracts.GatewayAccountPlatformManaged,
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: planID, UserID: 1, InstanceID: instanceID, PoolID: poolID, SchedulingGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM published_bindings WHERE plan_id=$1`, planID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_channel_allocations WHERE channel_id=$1`, channelID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM route_plans WHERE id=$1`, planID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_channels WHERE id=$1`, channelID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_pools WHERE id=$1`, poolID)
	})

	pending, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: instanceID, ChannelID: channelID,
		State: contracts.BindingPending, SchedulingGeneration: plan.SchedulingGeneration,
	})
	if err != nil {
		t.Fatal(err)
	}
	pending.RemoteID = "remote-1-" + suffix
	pending.State = contracts.BindingActive
	pending.VerificationStatus = contracts.BindingVerificationAwaitingFirstRequest
	pending.VerificationSource = contracts.BindingVerificationSourcePublish
	first, err := st.UpsertPublishedBinding(ctx, pending)
	if err != nil || first.RemoteID != pending.RemoteID {
		t.Fatalf("establish remote=%+v err=%v", first, err)
	}
	verifiedAt := time.Now().UTC().Truncate(time.Microsecond)
	first, err = st.RecordPublishedBindingVerification(ctx, plan.ID, channelID,
		contracts.BindingVerificationPassiveVerified, contracts.BindingVerificationSourcePassive, verifiedAt, "")
	if err != nil || !first.IsCallable() {
		t.Fatalf("verify binding=%+v err=%v", first, err)
	}

	for name, mutate := range map[string]func(*contracts.PublishedBinding){
		"id":       func(value *contracts.PublishedBinding) { value.ID = "binding-replaced-" + suffix },
		"instance": func(value *contracts.PublishedBinding) { value.InstanceID = "foreign-instance-" + suffix },
		"remote":   func(value *contracts.PublishedBinding) { value.RemoteID = "same-generation-replacement-" + suffix },
		"clear":    func(value *contracts.PublishedBinding) { value.RemoteID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := first
			mutate(&candidate)
			if _, err := st.UpsertPublishedBinding(ctx, candidate); !errors.Is(err, ErrConflict) {
				t.Fatalf("same-generation drift error=%v, want ErrConflict", err)
			}
		})
	}

	plan, err = st.ClaimRoutePlanScheduling(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	sameRemote := first
	sameRemote.SchedulingGeneration = plan.SchedulingGeneration
	preserved, err := st.UpsertPublishedBinding(ctx, sameRemote)
	if err != nil || !preserved.IsCallable() || preserved.VerifiedAt == nil || !preserved.VerifiedAt.Equal(verifiedAt) {
		t.Fatalf("same remote lost verification: %+v err=%v", preserved, err)
	}

	plan, err = st.ClaimRoutePlanScheduling(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement := preserved
	replacement.RemoteID = "remote-2-" + suffix
	replacement.SchedulingGeneration = plan.SchedulingGeneration
	replaced, err := st.UpsertPublishedBinding(ctx, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.VerificationStatus != contracts.BindingVerificationPublishedPending ||
		replaced.VerificationSource != contracts.BindingVerificationSourcePublish || replaced.VerifiedAt != nil ||
		replaced.VerificationErrorCode != "" || replaced.IsCallable() {
		t.Fatalf("replacement inherited old callability: %+v", replaced)
	}

	// The database trigger is the final boundary: a direct writer cannot move
	// either the stable local identity or the remote execution identity.
	for name, query := range map[string]string{
		"id":       `UPDATE published_bindings SET id='direct-id-drift-' || id WHERE id=$1`,
		"plan":     `UPDATE published_bindings SET plan_id='direct-plan-drift-' || plan_id WHERE id=$1`,
		"instance": `UPDATE published_bindings SET instance_id='direct-instance-drift-' || instance_id WHERE id=$1`,
		"channel":  `UPDATE published_bindings SET channel_id='direct-channel-drift-' || channel_id WHERE id=$1`,
		"remote": `UPDATE published_bindings SET remote_id='direct-remote-drift-' || remote_id,
			verification_status='passive_verified',verification_source='passive',verified_at=statement_timestamp()
			WHERE id=$1`,
	} {
		t.Run("direct_sql_"+name, func(t *testing.T) {
			if _, err := st.pool.Exec(ctx, query, replaced.ID); err == nil {
				t.Fatalf("direct SQL %s identity drift was accepted", name)
			}
		})
	}
	got, err := st.ListPublishedBindings(ctx, plan.ID)
	if err != nil || len(got) != 1 || got[0].ID != replaced.ID || got[0].PlanID != replaced.PlanID ||
		got[0].InstanceID != replaced.InstanceID || got[0].ChannelID != replaced.ChannelID ||
		got[0].RemoteID != replaced.RemoteID || got[0].IsCallable() {
		t.Fatalf("direct rejected drift mutated binding: %+v err=%v", got, err)
	}
}

func TestPostgresPublishedBindingConcurrentFirstRemoteIdentityHasOneWinnerLive(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("E2M_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL store: %v", err)
	}
	t.Cleanup(st.Close)

	suffix := newID("binding-first-remote-race")
	poolID, planID := "pool-"+suffix, "plan-"+suffix
	channelID, instanceID := "channel-"+suffix, "instance-"+suffix
	if _, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: suffix}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		ID: channelID, PoolID: poolID, SourceID: "source-" + suffix,
		AccountOwnership: contracts.GatewayAccountPlatformManaged,
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: planID, UserID: 1, InstanceID: instanceID, PoolID: poolID, SchedulingGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM published_bindings WHERE plan_id=$1`, planID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_channel_allocations WHERE channel_id=$1`, channelID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM route_plans WHERE id=$1`, planID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_channels WHERE id=$1`, channelID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_pools WHERE id=$1`, poolID)
	})

	pending, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: instanceID, ChannelID: channelID,
		State: contracts.BindingPending, SchedulingGeneration: plan.SchedulingGeneration,
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	type result struct {
		binding contracts.PublishedBinding
		err     error
	}
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, remoteID := range []string{"remote-a-" + suffix, "remote-b-" + suffix} {
		remoteID := remoteID
		go func() {
			ready.Done()
			<-start
			candidate := pending
			candidate.RemoteID = remoteID
			candidate.State = contracts.BindingActive
			candidate.VerificationStatus = contracts.BindingVerificationAwaitingFirstRequest
			candidate.VerificationSource = contracts.BindingVerificationSourcePublish
			binding, err := st.UpsertPublishedBinding(ctx, candidate)
			results <- result{binding: binding, err: err}
		}()
	}
	ready.Wait()
	close(start)

	var winner contracts.PublishedBinding
	successes, conflicts := 0, 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			successes++
			winner = result.binding
		case errors.Is(result.err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent writer error: %v", result.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent first identity outcomes successes=%d conflicts=%d", successes, conflicts)
	}
	bindings, err := st.ListPublishedBindings(ctx, plan.ID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	got := bindings[0]
	if got.RemoteID != winner.RemoteID || got.ID != pending.ID || got.SchedulingGeneration != plan.SchedulingGeneration ||
		got.VerificationStatus != contracts.BindingVerificationAwaitingFirstRequest || got.VerifiedAt != nil || got.IsCallable() {
		t.Fatalf("winning execution identity was not isolated: winner=%+v stored=%+v", winner, got)
	}
}
