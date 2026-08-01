package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

func newUpstreamStore() *MemoryStore {
	return NewMemoryStore(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
}

func TestUpstreamPoolDefaultsStatusAndID(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()

	pool, err := s.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "claude-stable"})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	if pool.ID == "" {
		t.Fatal("expected generated pool id")
	}
	if pool.Status != contracts.UpstreamPoolActive {
		t.Fatalf("expected default active status, got %q", pool.Status)
	}
	if pool.CreatedAt.IsZero() || pool.UpdatedAt.IsZero() {
		t.Fatal("expected timestamps set")
	}

	got, err := s.GetUpstreamPool(ctx, pool.ID)
	if err != nil {
		t.Fatalf("get pool: %v", err)
	}
	if got.Name != "claude-stable" {
		t.Fatalf("round-trip name mismatch: %q", got.Name)
	}
}

func TestGetUpstreamPoolNotFound(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	if _, err := s.GetUpstreamPool(ctx, "missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateUpstreamPoolPreservesCreatedAt(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	pool, _ := s.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "p"})

	pool.Name = "renamed"
	pool.Status = contracts.UpstreamPoolMaintenance
	updated, err := s.UpdateUpstreamPool(ctx, pool)
	if err != nil {
		t.Fatalf("update pool: %v", err)
	}
	if updated.Name != "renamed" || updated.Status != contracts.UpstreamPoolMaintenance {
		t.Fatalf("update not applied: %+v", updated)
	}
	if !updated.CreatedAt.Equal(pool.CreatedAt) {
		t.Fatalf("CreatedAt changed: was %v now %v", pool.CreatedAt, updated.CreatedAt)
	}
}

func TestUpdateUpstreamPoolCannotReopenActiveRetirement(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	pool, err := s.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "retiring"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePoolRetirementJob(ctx, pool.ID, 7); err != nil {
		t.Fatal(err)
	}
	pool.Status = contracts.UpstreamPoolActive
	if _, err := s.UpdateUpstreamPool(ctx, pool); !errors.Is(err, ErrConflict) {
		t.Fatalf("reopen active retirement error=%v, want ErrConflict", err)
	}
	got, err := s.GetUpstreamPool(ctx, pool.ID)
	if err != nil || got.Status != contracts.UpstreamPoolMaintenance {
		t.Fatalf("retiring pool changed: pool=%+v err=%v", got, err)
	}
}

func TestUpdateUpstreamPoolNotFound(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	if _, err := s.UpdateUpstreamPool(ctx, contracts.UpstreamPool{ID: "nope", Name: "x"}); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListUpstreamChannelsFilteredByPool(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	p1, _ := s.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "p1"})
	p2, _ := s.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "p2"})
	if _, err := s.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: p1.ID, DisplayName: "a"}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := s.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: p1.ID, DisplayName: "b"}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := s.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: p2.ID, DisplayName: "c"}); err != nil {
		t.Fatalf("create channel: %v", err)
	}

	p1chans, err := s.ListUpstreamChannels(ctx, p1.ID)
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	if len(p1chans) != 2 {
		t.Fatalf("expected 2 channels in p1, got %d", len(p1chans))
	}
	for _, c := range p1chans {
		if c.Status != contracts.UpstreamChannelActive {
			t.Fatalf("expected default active channel status, got %q", c.Status)
		}
	}

	all, err := s.ListUpstreamChannels(ctx, "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 channels total, got %d", len(all))
	}
}

func TestUpstreamChannelSourceIDRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	created, err := s.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: "pool-a", DisplayName: "key-a", SourceID: "upstream-channel-a",
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	got, err := s.GetUpstreamChannel(ctx, created.ID)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}
	if got.SourceID != "upstream-channel-a" {
		t.Fatalf("source identity lost on create: %+v", got)
	}
	got.SourceID = "upstream-channel-b"
	updated, err := s.UpdateUpstreamChannel(ctx, got)
	if err != nil {
		t.Fatalf("update channel: %v", err)
	}
	if updated.SourceID != "upstream-channel-b" {
		t.Fatalf("source identity lost on update: %+v", updated)
	}
}

func TestRoutePlanDefaultsAndScope(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	plan, err := s.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: 101, InstanceID: "i-a", PoolID: "p-a"})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if plan.Status != contracts.RoutePlanDraft {
		t.Fatalf("expected default draft status, got %q", plan.Status)
	}
	if _, err := s.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: 102, InstanceID: "i-b", PoolID: "p-b"}); err != nil {
		t.Fatalf("create plan: %v", err)
	}

	scoped, err := s.ListRoutePlans(ctx, 101)
	if err != nil {
		t.Fatalf("list plans: %v", err)
	}
	if len(scoped) != 1 || scoped[0].UserID != 101 {
		t.Fatalf("expected only t-a plan, got %+v", scoped)
	}
	all, _ := s.ListRoutePlans(ctx, 0)
	if len(all) != 2 {
		t.Fatalf("expected 2 plans total, got %d", len(all))
	}
}

func TestRoutePlanHasOneOwnerPerInstancePool(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	if _, err := s.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-a", UserID: 101, InstanceID: "inst-a", PoolID: "pool-a"}); err != nil {
		t.Fatalf("create owner plan: %v", err)
	}
	if _, err := s.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-b", UserID: 101, InstanceID: "inst-a", PoolID: "pool-a"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate instance/pool plan error=%v, want ErrDuplicate", err)
	}
	if _, err := s.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-c", UserID: 101, InstanceID: "inst-b", PoolID: "pool-a"}); err != nil {
		t.Fatalf("same pool on another instance: %v", err)
	}
	if _, err := s.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "legacy-a", UserID: 101}); err != nil {
		t.Fatalf("create legacy plan: %v", err)
	}
	if _, err := s.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "legacy-b", UserID: 202}); err != nil {
		t.Fatalf("legacy empty scope must remain compatible: %v", err)
	}
}

func TestRoutePlanDesiredStateChangeAdvancesGenerationAndPublishCompletionDoesNot(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	plan, err := s.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: "plan-generation-fence", UserID: 101, InstanceID: "instance-a", PoolID: "pool-a",
		Status: contracts.RoutePlanDraft, SchedulingGeneration: 7, Rollout: contracts.RolloutImmediate,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan.MaxChannels = 2
	changed, err := s.UpdateRoutePlan(ctx, plan)
	if err != nil || changed.SchedulingGeneration != 8 || changed.MaxChannels != 2 {
		t.Fatalf("changed plan=%+v err=%v", changed, err)
	}
	if _, err := s.UpdateRoutePlan(ctx, plan); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale desired-state update error=%v, want ErrConflict", err)
	}
	replayed, err := s.UpdateRoutePlan(ctx, changed)
	if err != nil || replayed.SchedulingGeneration != changed.SchedulingGeneration {
		t.Fatalf("idempotent replay=%+v err=%v", replayed, err)
	}
	published, err := s.CompleteRoutePlanPublish(ctx, changed.ID, changed.SchedulingGeneration)
	if err != nil || published.Status != contracts.RoutePlanPublished || published.SchedulingGeneration != changed.SchedulingGeneration {
		t.Fatalf("publish completion=%+v err=%v", published, err)
	}
	if _, err := s.CompleteRoutePlanPublish(ctx, changed.ID, changed.SchedulingGeneration); !errors.Is(err, ErrConflict) {
		t.Fatalf("publish replay error=%v, want ErrConflict", err)
	}
	identityChange := published
	identityChange.InstanceID = "instance-b"
	if _, err := s.UpdateRoutePlan(ctx, identityChange); !errors.Is(err, ErrConflict) {
		t.Fatalf("identity change error=%v, want ErrConflict", err)
	}
}

func TestRoutePlanDesiredStateChangeFencesRunningRecommendationRollout(t *testing.T) {
	ctx := context.Background()
	st, input := newMemoryRecommendationRolloutFixture(t)
	created, _, err := st.CreateRecommendationRollout(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	_, running, claimed, err := st.ClaimRecommendationRolloutOperation(ctx, "plan-change-worker", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim operation=%+v claimed=%v err=%v", running, claimed, err)
	}
	plan, err := st.GetRoutePlan(ctx, created.State.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	plan.MaxChannels++
	updated, err := st.UpdateRoutePlan(ctx, plan)
	if err != nil || updated.SchedulingGeneration != created.State.SchedulingGeneration+1 {
		t.Fatalf("updated plan=%+v err=%v", updated, err)
	}
	if _, err := st.RenewRecommendationRolloutOperation(ctx, running.ID, "plan-change-worker", running.Version, time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale rollout renewal error=%v, want ErrConflict", err)
	}
}

// UpsertPublishedBinding is keyed on (plan_id, channel_id): a second upsert for
// the same pair updates in place rather than inserting a duplicate, preserving
// the original id and created_at.
func TestUpsertPublishedBindingIdentity(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	if _, err := s.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-1", UserID: 101}); err != nil {
		t.Fatalf("create plan: %v", err)
	}

	first, err := s.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: "plan-1", InstanceID: "inst-1", ChannelID: "ch-1", RemoteID: "acc-1", State: contracts.BindingActive,
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.ID == "" {
		t.Fatal("expected generated binding id")
	}

	second, err := s.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: "plan-1", InstanceID: "inst-1", ChannelID: "ch-1", RemoteID: "acc-1", State: contracts.BindingDisabled,
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected same id on update, got %q vs %q", second.ID, first.ID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("CreatedAt changed on update")
	}
	if second.State != contracts.BindingDisabled {
		t.Fatalf("state not updated: %q", second.State)
	}

	all, _ := s.ListPublishedBindings(ctx, "plan-1")
	if len(all) != 1 {
		t.Fatalf("expected one binding after upsert-in-place, got %d", len(all))
	}
}

func TestUpsertPublishedBindingExecutionIdentityCannotDrift(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	plan, err := s.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: "plan-binding-identity", UserID: 101, InstanceID: "instance-1", SchedulingGeneration: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "channel-1", RemoteID: "remote-1",
		State: contracts.BindingActive, SchedulingGeneration: plan.SchedulingGeneration,
	})
	if err != nil {
		t.Fatal(err)
	}
	verifiedAt := time.Date(2026, 7, 5, 0, 1, 0, 0, time.UTC)
	first, err = s.RecordPublishedBindingVerification(ctx, plan.ID, first.ChannelID,
		contracts.BindingVerificationPassiveVerified, contracts.BindingVerificationSourcePassive, verifiedAt, "")
	if err != nil || !first.IsCallable() {
		t.Fatalf("verify binding=%+v err=%v", first, err)
	}

	for name, mutate := range map[string]func(*contracts.PublishedBinding){
		"id":       func(value *contracts.PublishedBinding) { value.ID = "binding-replaced" },
		"instance": func(value *contracts.PublishedBinding) { value.InstanceID = "instance-2" },
		"remote":   func(value *contracts.PublishedBinding) { value.RemoteID = "remote-2" },
		"clear":    func(value *contracts.PublishedBinding) { value.RemoteID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := first
			mutate(&candidate)
			if _, err := s.UpsertPublishedBinding(ctx, candidate); !errors.Is(err, ErrConflict) {
				t.Fatalf("same-generation identity drift error=%v, want ErrConflict", err)
			}
		})
	}
	stored, _ := s.ListPublishedBindings(ctx, plan.ID)
	if len(stored) != 1 || stored[0].ID != first.ID || stored[0].InstanceID != first.InstanceID ||
		stored[0].RemoteID != first.RemoteID || !stored[0].IsCallable() || stored[0].VerifiedAt == nil {
		t.Fatalf("rejected drift mutated binding: %+v", stored)
	}
}

func TestUpsertPublishedBindingRemoteReplacementRequiresNewProof(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	plan, err := s.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: "plan-binding-replacement", UserID: 101, InstanceID: "instance-1", SchedulingGeneration: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "channel-1",
		State: contracts.BindingPending, SchedulingGeneration: plan.SchedulingGeneration,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Provisioning establishes the initially-empty remote in the same generation.
	pending.RemoteID = "remote-1"
	pending.State = contracts.BindingActive
	pending.VerificationStatus = contracts.BindingVerificationAwaitingFirstRequest
	pending.VerificationSource = contracts.BindingVerificationSourcePublish
	first, err := s.UpsertPublishedBinding(ctx, pending)
	if err != nil || first.RemoteID != "remote-1" || first.VerificationStatus != contracts.BindingVerificationAwaitingFirstRequest {
		t.Fatalf("establish first remote=%+v err=%v", first, err)
	}
	verifiedAt := time.Date(2026, 7, 5, 0, 2, 0, 0, time.UTC)
	first, err = s.RecordPublishedBindingVerification(ctx, plan.ID, first.ChannelID,
		contracts.BindingVerificationProbeVerified, contracts.BindingVerificationSourceProbe, verifiedAt, "")
	if err != nil {
		t.Fatal(err)
	}

	plan, err = s.ClaimRoutePlanScheduling(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	sameRemote := first
	sameRemote.SchedulingGeneration = plan.SchedulingGeneration
	preserved, err := s.UpsertPublishedBinding(ctx, sameRemote)
	if err != nil || !preserved.IsCallable() || preserved.VerifiedAt == nil || !preserved.VerifiedAt.Equal(verifiedAt) {
		t.Fatalf("same remote lost verification: %+v err=%v", preserved, err)
	}

	plan, err = s.ClaimRoutePlanScheduling(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement := preserved
	replacement.RemoteID = "remote-2"
	replacement.SchedulingGeneration = plan.SchedulingGeneration
	// An attempted carry-over must be discarded by the store.
	replaced, err := s.UpsertPublishedBinding(ctx, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.RemoteID != "remote-2" || replaced.VerificationStatus != contracts.BindingVerificationPublishedPending ||
		replaced.VerificationSource != contracts.BindingVerificationSourcePublish || replaced.VerifiedAt != nil ||
		replaced.VerificationErrorCode != "" || replaced.IsCallable() {
		t.Fatalf("replacement inherited old callability: %+v", replaced)
	}
}

func TestUpsertPublishedBindingDefaultsState(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	if _, err := s.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "p", UserID: 101}); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	b, err := s.UpsertPublishedBinding(ctx, contracts.PublishedBinding{PlanID: "p", ChannelID: "c"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if b.State != contracts.BindingPending {
		t.Fatalf("expected default pending state, got %q", b.State)
	}
}

func TestListPublishedBindingsFilteredByPlan(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	if _, err := s.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-1", UserID: 101}); err != nil {
		t.Fatalf("create plan 1: %v", err)
	}
	if _, err := s.CreateRoutePlan(ctx, contracts.RoutePlan{ID: "plan-2", UserID: 202}); err != nil {
		t.Fatalf("create plan 2: %v", err)
	}
	if _, err := s.UpsertPublishedBinding(ctx, contracts.PublishedBinding{PlanID: "plan-1", ChannelID: "c1"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := s.UpsertPublishedBinding(ctx, contracts.PublishedBinding{PlanID: "plan-2", ChannelID: "c2"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.ListPublishedBindings(ctx, "plan-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].PlanID != "plan-1" {
		t.Fatalf("expected only plan-1 bindings, got %+v", got)
	}
}

func TestUpstreamChannelAllocationIsPermanentPerUser(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	for _, channel := range []contracts.UpstreamChannel{
		{ID: "key-1", PoolID: "pool-a", SourceID: "source-a"},
		{ID: "key-2", PoolID: "pool-a", SourceID: "source-a"},
		{ID: "key-3", PoolID: "pool-a", SourceID: "source-b"},
	} {
		if _, err := s.CreateUpstreamChannel(ctx, channel); err != nil {
			t.Fatalf("create channel %s: %v", channel.ID, err)
		}
	}
	for _, plan := range []contracts.RoutePlan{
		{ID: "owner-plan-a", UserID: 101},
		{ID: "owner-plan-b", UserID: 101},
		{ID: "other-plan", UserID: 202},
	} {
		if _, err := s.CreateRoutePlan(ctx, plan); err != nil {
			t.Fatalf("create plan %s: %v", plan.ID, err)
		}
	}
	first, err := s.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: "owner-plan-a", ChannelID: "key-1", State: contracts.BindingDisabled,
	})
	if err != nil {
		t.Fatalf("first owner claim: %v", err)
	}
	if _, err := s.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: "owner-plan-b", ChannelID: "key-1", State: contracts.BindingActive,
	}); err != nil {
		t.Fatalf("same user must be allowed on another plan: %v", err)
	}
	if _, err := s.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: "owner-plan-b", ChannelID: "key-2", State: contracts.BindingActive,
	}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second key from same source error = %v, want ErrDuplicate", err)
	}
	if _, err := s.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: "owner-plan-b", ChannelID: "key-3", State: contracts.BindingActive,
	}); err != nil {
		t.Fatalf("same user must be allowed one key from another source: %v", err)
	}
	if _, err := s.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: "other-plan", ChannelID: "key-1", State: contracts.BindingActive,
	}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("other user claim error = %v, want ErrDuplicate", err)
	}
	allocation := s.channelAllocations["key-1"]
	if allocation.UserID != 101 || allocation.FirstPlanID != first.PlanID {
		t.Fatalf("permanent allocation changed: %+v", allocation)
	}
	bindings, err := s.ListPublishedBindings(ctx, "other-plan")
	if err != nil {
		t.Fatalf("list other bindings: %v", err)
	}
	if len(bindings) != 0 {
		t.Fatalf("rejected claim must not leave a partial binding: %+v", bindings)
	}
	key1, err := s.GetUpstreamChannel(ctx, "key-1")
	if err != nil {
		t.Fatalf("get allocated key: %v", err)
	}
	key1.SourceID = "source-changed"
	if _, err := s.UpdateUpstreamChannel(ctx, key1); !errors.Is(err, ErrConflict) {
		t.Fatalf("allocated source identity update error=%v, want ErrConflict", err)
	}
}

func TestUpstreamChannelOwnershipIsImmutable(t *testing.T) {
	ctx := context.Background()
	s := newUpstreamStore()
	channel, err := s.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		ID: "owner-key", PoolID: "pool-a", SourceID: "source-a",
		AccountOwnership: contracts.GatewayAccountOwnerProvided,
	})
	if err != nil || channel.AccountOwnership != contracts.GatewayAccountOwnerProvided {
		t.Fatalf("create ownership = %+v err=%v", channel, err)
	}
	channel.AccountOwnership = contracts.GatewayAccountPlatformManaged
	if _, err := s.UpdateUpstreamChannel(ctx, channel); !errors.Is(err, ErrConflict) {
		t.Fatalf("ownership change error = %v", err)
	}
	stored, _ := s.GetUpstreamChannel(ctx, channel.ID)
	if stored.AccountOwnership != contracts.GatewayAccountOwnerProvided {
		t.Fatalf("stored ownership = %q", stored.AccountOwnership)
	}
}
