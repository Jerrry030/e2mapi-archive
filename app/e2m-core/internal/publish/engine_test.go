package publish

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/adapters"
	"e2m.local/core/internal/keyproof"
	"e2m.local/core/internal/store"
)

type deliveryKeyVerifierFunc func(context.Context, string, string) (keyproof.Verification, error)

func (f deliveryKeyVerifierFunc) Verify(ctx context.Context, channelID, instanceID string) (keyproof.Verification, error) {
	return f(ctx, channelID, instanceID)
}

// fakeGateway is an in-memory Gateway: it returns a fixed account list and
// records SetSchedulable calls so tests can assert side effects. An optional
// failOn set makes named accounts return an error on toggle.
type fakeGateway struct {
	accounts      []contracts.GatewayAccount
	failOn        map[string]bool
	calls         []schedCall
	updated       []contracts.GatewayAccountSpec
	updateErr     error
	deleted       []string
	provisioned   []contracts.GatewayAccountSpec
	failProvision map[string]bool
	lifecycle     bool
}

type fenceCall struct {
	accountID string
	fence     contracts.GatewaySchedulingFence
}

type fenceRecordingGateway struct {
	fakeGateway
	fences []fenceCall
}

func (g *fenceRecordingGateway) SetSchedulable(ctx context.Context, instanceID, accountID string, schedulable bool, reason string) error {
	fence, _ := contracts.GatewaySchedulingFenceFromContext(ctx)
	g.fences = append(g.fences, fenceCall{accountID: accountID, fence: fence})
	return g.fakeGateway.SetSchedulable(ctx, instanceID, accountID, schedulable, reason)
}

type schedCall struct {
	accountID   string
	schedulable bool
}

type failBindingStore struct {
	store.Store
}

type failQualityCircuitReadStore struct{ store.Store }

func (s failQualityCircuitReadStore) ListQualityCircuitRuntimes(context.Context, contracts.QualityCircuitRuntimeFilter) ([]contracts.QualityCircuitRuntime, error) {
	return nil, errors.New("quality circuit read failed")
}

func (s failBindingStore) UpsertPublishedBinding(ctx context.Context, input contracts.PublishedBinding) (contracts.PublishedBinding, error) {
	return contracts.PublishedBinding{}, errors.New("persist binding failed")
}

// staleAllocationStore simulates a reconcile racing with another user's key
// claim: its planning read misses global ownership, while the atomic upsert in
// the underlying store still sees and rejects the permanent allocation.
type staleAllocationStore struct {
	store.Store
}

// supersedeBeforeMutationStore advances the plan generation on the first
// ownership guard read after the caller has claimed its snapshot. It models a
// newer manual or automatic scheduling intent winning while the old worker is
// still assembling its diff.
type supersedeBeforeMutationStore struct {
	store.Store
	planID    string
	supersede bool
}

func (s *supersedeBeforeMutationStore) GetRoutePlan(ctx context.Context, id string) (contracts.RoutePlan, error) {
	if id == s.planID && !s.supersede {
		s.supersede = true
		if _, err := s.Store.ClaimRoutePlanScheduling(ctx, id, contracts.RoutePlanPublished, contracts.RoutePlanSuspended); err != nil {
			return contracts.RoutePlan{}, err
		}
	}
	return s.Store.GetRoutePlan(ctx, id)
}

func (s staleAllocationStore) ListPublishedBindings(ctx context.Context, planID string) ([]contracts.PublishedBinding, error) {
	if planID == "" {
		return nil, nil
	}
	return s.Store.ListPublishedBindings(ctx, planID)
}

func (g *fakeGateway) ListAccounts(ctx context.Context, instanceID string) ([]contracts.GatewayAccount, error) {
	return g.accounts, nil
}

func (g *fakeGateway) SetSchedulable(ctx context.Context, instanceID, accountID string, schedulable bool, reason string) error {
	g.calls = append(g.calls, schedCall{accountID: accountID, schedulable: schedulable})
	if g.failOn[accountID] {
		return errors.New("gateway rejected toggle")
	}
	// Reflect the toggle so a subsequent diff sees the new state.
	for i := range g.accounts {
		if g.accounts[i].ID == accountID {
			g.accounts[i].Schedulable = schedulable
		}
	}
	return nil
}

func (g *fakeGateway) ProvisionAccount(ctx context.Context, instanceID string, spec contracts.GatewayAccountSpec, reason string) (contracts.GatewayProvisionResult, error) {
	g.provisioned = append(g.provisioned, spec)
	if g.failProvision[spec.ChannelID] {
		return contracts.GatewayProvisionResult{}, errors.New("gateway rejected provision")
	}
	id := spec.RemoteID
	if id == "" {
		id = "prov-" + spec.ChannelID
	}
	g.upsertAccount(id, spec)
	return contracts.GatewayProvisionResult{RemoteID: id, Created: true}, nil
}

func (g *fakeGateway) UpdateAccount(ctx context.Context, instanceID string, spec contracts.GatewayAccountSpec, reason string) (contracts.GatewayProvisionResult, error) {
	g.updated = append(g.updated, spec)
	if g.updateErr != nil {
		return contracts.GatewayProvisionResult{}, g.updateErr
	}
	g.upsertAccount(spec.RemoteID, spec)
	return contracts.GatewayProvisionResult{RemoteID: spec.RemoteID, Created: false}, nil
}

func (g *fakeGateway) DeleteAccount(ctx context.Context, instanceID, accountID, reason string) error {
	g.deleted = append(g.deleted, accountID)
	for i := range g.accounts {
		if g.accounts[i].ID == accountID {
			g.accounts = append(g.accounts[:i], g.accounts[i+1:]...)
			return nil
		}
	}
	return nil
}

func (g *fakeGateway) SupportsLifecycleAction(context.Context, string, contracts.ReconcileActionType) bool {
	return g.lifecycle
}

func (g *fakeGateway) upsertAccount(id string, spec contracts.GatewayAccountSpec) {
	for i := range g.accounts {
		if g.accounts[i].ID == id {
			g.accounts[i].Schedulable = spec.Schedulable
			g.accounts[i].DisplayName = spec.DisplayName
			g.accounts[i].Platform = spec.Provider
			g.accounts[i].Type = spec.Type
			g.accounts[i].Priority = spec.Priority
			g.accounts[i].GroupIDs = spec.Groups
			return
		}
	}
	g.accounts = append(g.accounts, contracts.GatewayAccount{
		ID: id, Schedulable: spec.Schedulable, DisplayName: spec.DisplayName,
		Platform: spec.Provider, Type: spec.Type, Priority: spec.Priority, GroupIDs: spec.Groups,
	})
}

func newFixture(t *testing.T) (context.Context, store.Store) {
	t.Helper()
	return context.Background(), store.NewMemoryStore(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
}

// seedPlan creates a pool, its channels, and a route plan on an instance,
// returning the plan id. Channels are given as (id-suffix, status, remoteID).
func seedPlan(t *testing.T, ctx context.Context, st store.Store, planStatus contracts.RoutePlanStatus, poolStatus contracts.UpstreamPoolStatus, maxChannels int, chans []seedChan) contracts.RoutePlan {
	t.Helper()
	inst, err := st.CreateInstance(ctx, contracts.Instance{UserID: 101, Name: "gw", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "claude-stable", Status: poolStatus})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	if pool.Status != contracts.UpstreamPoolActive {
		active := pool
		active.Status = contracts.UpstreamPoolActive
		pool, err = st.UpdateUpstreamPool(ctx, active)
		if err != nil {
			t.Fatalf("activate pool for plan fixture: %v", err)
		}
	}
	for _, sc := range chans {
		ch := contracts.UpstreamChannel{
			ID:       sc.id,
			PoolID:   pool.ID,
			SourceID: sc.sourceID,
			Status:   sc.status,
			Priority: sc.priority,
		}
		if sc.remoteID != "" {
			ch.Labels = map[string]string{"remote_id": sc.remoteID}
		}
		if _, err := st.CreateUpstreamChannel(ctx, ch); err != nil {
			t.Fatalf("create channel: %v", err)
		}
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: inst.UserID, InstanceID: inst.ID, PoolID: pool.ID,
		Status: planStatus, MaxChannels: maxChannels,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if poolStatus != contracts.UpstreamPoolActive {
		if memory, ok := st.(*store.MemoryStore); ok && poolStatus == contracts.UpstreamPoolRetired {
			job, jobErr := memory.CreatePoolRetirementJob(ctx, pool.ID, 1)
			if jobErr != nil {
				t.Fatalf("create fixture retirement: %v", jobErr)
			}
			item, claimed, claimErr := memory.ClaimPoolRetirementItem(ctx, job.ID)
			if claimErr != nil || !claimed {
				t.Fatalf("claim fixture retirement: item=%+v claimed=%v err=%v", item, claimed, claimErr)
			}
			if _, completeErr := memory.CompletePoolRetirementItem(ctx, job.ID, item.PlanID, item.Attempts, ""); completeErr != nil {
				t.Fatalf("complete fixture retirement: %v", completeErr)
			}
			if _, finalizeErr := memory.FinalizePoolRetirementJob(ctx, job.ID); finalizeErr != nil {
				t.Fatalf("finalize fixture retirement: %v", finalizeErr)
			}
		} else {
			pool.Status = poolStatus
			if _, err := st.UpdateUpstreamPool(ctx, pool); err != nil {
				t.Fatalf("restore fixture pool status: %v", err)
			}
		}
	}
	return plan
}

type seedChan struct {
	id       string
	status   contracts.UpstreamChannelStatus
	remoteID string
	priority int
	sourceID string
}

func actionByChannel(p contracts.ReconcilePlan) map[string]contracts.ReconcileActionType {
	m := make(map[string]contracts.ReconcileActionType, len(p.Actions))
	for _, a := range p.Actions {
		m[a.ChannelID] = a.Type
	}
	return m
}

func TestRemoteIDHintIsScopedToItsOriginInstance(t *testing.T) {
	channel := contracts.UpstreamChannel{Labels: map[string]string{
		"remote_id": "7", "instance_id": "origin-instance",
	}}
	if got := resolveRemoteID(channel, nil, "other-instance"); got != "" {
		t.Fatalf("cross-instance remote hint = %q, want empty", got)
	}
	if got := resolveRemoteID(channel, nil, "origin-instance"); got != "7" {
		t.Fatalf("origin remote hint = %q, want 7", got)
	}
	binding := &contracts.PublishedBinding{RemoteID: "12"}
	if got := resolveRemoteID(channel, binding, "other-instance"); got != "12" {
		t.Fatalf("durable binding remote id = %q, want 12", got)
	}
}

func TestPlanPublishesOnlyHighestPriorityKeyPerSource(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "source-a-primary", sourceID: "source-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a-primary", priority: 1},
		{id: "source-a-spare", sourceID: "source-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a-spare", priority: 2},
		{id: "source-b", sourceID: "source-b", status: contracts.UpstreamChannelActive, remoteID: "acc-b", priority: 3},
	})
	gw := &fakeGateway{}

	got, err := New(st, gw).Plan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	actions := actionByChannel(got)
	if actions["source-a-primary"] != contracts.ReconcileCreate || actions["source-b"] != contracts.ReconcileCreate {
		t.Fatalf("expected one key from each source to be published, got %+v", got.Actions)
	}
	if _, ok := actions["source-a-spare"]; ok {
		t.Fatalf("lower-priority key from an already selected source must not be published: %+v", got.Actions)
	}
}

func TestApplyAssignsDifferentKeyFromSameSourceToSecondUser(t *testing.T) {
	ctx, st := newFixture(t)
	ownerPlan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "key-1", sourceID: "source-a", status: contracts.UpstreamChannelActive, remoteID: "acc-key-1", priority: 1},
		{id: "key-2", sourceID: "source-a", status: contracts.UpstreamChannelActive, remoteID: "acc-key-2", priority: 2},
	})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: ownerPlan.ID, InstanceID: ownerPlan.InstanceID, ChannelID: "key-1",
		RemoteID: "acc-key-1", State: contracts.BindingDisabled,
	}); err != nil {
		t.Fatalf("claim key for first user: %v", err)
	}
	key2, err := st.GetUpstreamChannel(ctx, "key-2")
	if err != nil {
		t.Fatalf("load second key: %v", err)
	}
	key2.CredentialBindingID = "credential-key-2"
	if _, err := st.UpdateUpstreamChannel(ctx, key2); err != nil {
		t.Fatalf("configure second key: %v", err)
	}
	otherInstance, err := st.CreateInstance(ctx, contracts.Instance{UserID: 202, Name: "other-gw", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatalf("create other instance: %v", err)
	}
	otherPlan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: 202, InstanceID: otherInstance.ID, PoolID: ownerPlan.PoolID, Status: contracts.RoutePlanPublished,
	})
	if err != nil {
		t.Fatalf("create other plan: %v", err)
	}
	gw := &fakeGateway{lifecycle: true}

	got, err := New(st, gw).Apply(ctx, otherPlan.ID)
	if err != nil {
		t.Fatalf("apply second user: %v", err)
	}
	if actions := actionByChannel(got); len(actions) != 1 || actions["key-2"] != contracts.ReconcileCreate {
		t.Fatalf("second user must receive the next key from source-a: %+v", got.Actions)
	}
	bindings, listErr := st.ListPublishedBindings(ctx, otherPlan.ID)
	if listErr != nil {
		t.Fatalf("list conflicting plan bindings: %v", listErr)
	}
	if len(bindings) != 1 || bindings[0].ChannelID != "key-2" {
		t.Fatalf("second user received wrong key: %+v", bindings)
	}
}

func TestApplyConcurrentOwnershipConflictFailsBeforeGatewayMutation(t *testing.T) {
	ctx, st := newFixture(t)
	ownerPlan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "key-1", sourceID: "source-a", status: contracts.UpstreamChannelActive, remoteID: "acc-key-1"},
	})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: ownerPlan.ID, InstanceID: ownerPlan.InstanceID, ChannelID: "key-1",
		RemoteID: "acc-key-1", State: contracts.BindingDisabled,
	}); err != nil {
		t.Fatalf("claim key for first user: %v", err)
	}
	otherPlan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: 202, InstanceID: "other-instance", PoolID: ownerPlan.PoolID, Status: contracts.RoutePlanPublished,
	})
	if err != nil {
		t.Fatalf("create other plan: %v", err)
	}
	gw := &fakeGateway{lifecycle: true}

	_, err = New(staleAllocationStore{Store: st}, gw).Apply(ctx, otherPlan.ID)
	if err == nil || !strings.Contains(err.Error(), store.ErrDuplicate.Error()) {
		t.Fatalf("racing claim error = %v, want ownership duplicate", err)
	}
	if len(gw.provisioned) != 0 || len(gw.calls) != 0 {
		t.Fatalf("atomic claim must fail before gateway mutation: %+v", gw)
	}
}

func TestDesiredActiveChannelsTreatsEmptySourceAsChannelIdentity(t *testing.T) {
	got := desiredActiveChannels([]contracts.UpstreamChannel{
		{ID: "ch-a", Status: contracts.UpstreamChannelActive, Priority: 1},
		{ID: "ch-b", Status: contracts.UpstreamChannelActive, Priority: 2},
	}, 0)
	if len(got) != 2 {
		t.Fatalf("legacy channels without source_id must remain independent, got %+v", got)
	}
}

func TestDesiredActiveChannelsDoesNotReplaceInactiveOwnedKeyFromSameSource(t *testing.T) {
	got := desiredActiveChannelsForUser([]contracts.UpstreamChannel{
		{ID: "owned-key", SourceID: "source-a", Status: contracts.UpstreamChannelMaintenance, Priority: 1},
		{ID: "unallocated-key", SourceID: "source-a", Status: contracts.UpstreamChannelActive, Priority: 2},
	}, 0, 101, map[string]int64{"owned-key": 101})
	if len(got) != 0 {
		t.Fatalf("inactive permanent allocation must not cause a second key assignment: %+v", got)
	}
}

func TestApplySchedulingIsPlanLocalAndKeepsSharedCatalogActive(t *testing.T) {
	ctx, st := newFixture(t)
	planA := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
		{id: "ch-b", status: contracts.UpstreamChannelActive, remoteID: "acc-b"},
	})
	planB, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: planA.UserID, InstanceID: "instance-b", PoolID: planA.PoolID, Status: contracts.RoutePlanPublished,
	})
	if err != nil {
		t.Fatalf("create second plan: %v", err)
	}
	for _, seed := range []contracts.PublishedBinding{
		{PlanID: planA.ID, InstanceID: planA.InstanceID, ChannelID: "ch-a", RemoteID: "acc-a", State: contracts.BindingActive},
		{PlanID: planA.ID, InstanceID: planA.InstanceID, ChannelID: "ch-b", RemoteID: "acc-b", State: contracts.BindingDisabled},
		{PlanID: planB.ID, InstanceID: planB.InstanceID, ChannelID: "ch-a", RemoteID: "acc-b-a", State: contracts.BindingActive},
	} {
		if _, err := st.UpsertPublishedBinding(ctx, seed); err != nil {
			t.Fatalf("seed binding: %v", err)
		}
	}
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{
		{ID: "acc-a", Schedulable: true}, {ID: "acc-b", Schedulable: false},
		{ID: "acc-b-a", Schedulable: true},
	}}

	result, err := New(st, gw).ApplyScheduling(ctx, planA.ID, map[string]bool{"ch-a": false, "ch-b": true})
	if err != nil {
		t.Fatalf("ApplyScheduling: %v", err)
	}
	if actions := actionByChannel(result); actions["ch-b"] != contracts.ReconcileEnable || actions["ch-a"] != contracts.ReconcileDisable {
		t.Fatalf("scoped actions=%+v", result.Actions)
	}
	if !gw.accounts[2].Schedulable {
		t.Fatal("plan B shared-source account was changed by plan A ejection")
	}
	channel, err := st.GetUpstreamChannel(ctx, "ch-a")
	if err != nil || channel.Status != contracts.UpstreamChannelActive {
		t.Fatalf("shared catalog lifecycle changed: channel=%+v err=%v", channel, err)
	}
	bindingsB, _ := st.ListPublishedBindings(ctx, planB.ID)
	if len(bindingsB) != 1 || bindingsB[0].State != contracts.BindingActive {
		t.Fatalf("plan B binding changed: %+v", bindingsB)
	}
}

func TestFullApplyCannotReenableOpenQualityCircuit(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", sourceID: "source-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-a",
		RemoteID: "acc-a", State: contracts.BindingDisabled,
	}); err != nil {
		t.Fatalf("seed disabled binding: %v", err)
	}
	now := time.Now().UTC()
	if _, err := st.UpsertQualityCircuitRuntime(ctx, contracts.QualityCircuitRuntime{
		PlanID: plan.ID, ChannelID: "ch-a", State: contracts.QualityCircuitOpen,
		OpenedAt: &now, ProbeAfter: &now, LastTransitionAt: &now, OpenCount: 1, LastScore: 40,
	}, 0); err != nil {
		t.Fatalf("seed quality circuit: %v", err)
	}
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: false}}}
	eng := New(st, gw)

	planned, err := eng.Plan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("plan isolated full reconcile: %v", err)
	}
	if len(planned.Actions) != 0 {
		t.Fatalf("full reconcile planned an action for already-isolated binding: %+v", planned.Actions)
	}
	applied, err := eng.Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("apply isolated full reconcile: %v", err)
	}
	if len(applied.Actions) != 0 || len(gw.calls) != 0 || len(gw.updated) != 0 || len(gw.provisioned) != 0 {
		t.Fatalf("full reconcile bypassed quality circuit: applied=%+v gateway=%+v", applied.Actions, gw)
	}
	bindings, err := st.ListPublishedBindings(ctx, plan.ID)
	if err != nil || len(bindings) != 1 || bindings[0].State != contracts.BindingDisabled {
		t.Fatalf("quality-isolated binding changed: %+v err=%v", bindings, err)
	}
}

func TestFullApplyDrainsGatewayAccountWhenOpenQualityCircuitFactIsStale(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", sourceID: "source-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-a",
		RemoteID: "acc-a", State: contracts.BindingActive,
	}); err != nil {
		t.Fatalf("seed active binding: %v", err)
	}
	now := time.Now().UTC()
	if _, err := st.UpsertQualityCircuitRuntime(ctx, contracts.QualityCircuitRuntime{
		PlanID: plan.ID, ChannelID: "ch-a", State: contracts.QualityCircuitHalfOpen,
		OpenedAt: &now, ProbeAfter: &now, HalfOpenSince: &now, LastTransitionAt: &now,
		OpenCount: 1, ConsecutiveProbeSuccesses: 2, LastScore: 90,
	}, 0); err != nil {
		t.Fatalf("seed half-open quality circuit: %v", err)
	}
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: true}}}
	result, err := New(st, gw).Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("apply stale quality isolation: %v", err)
	}
	if actions := actionByChannel(result); actions["ch-a"] != contracts.ReconcileDisable {
		t.Fatalf("full reconcile did not drain half-open binding: %+v", result.Actions)
	}
	if len(gw.calls) != 1 || gw.calls[0].accountID != "acc-a" || gw.calls[0].schedulable {
		t.Fatalf("unexpected gateway drain: %+v", gw.calls)
	}
}

func TestFullApplyFailsClosedWhenQualityCircuitsCannotBeRead(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	gw := &fakeGateway{lifecycle: true}
	_, err := New(failQualityCircuitReadStore{Store: st}, gw).Apply(ctx, plan.ID)
	if err == nil || !strings.Contains(err.Error(), "quality circuit read failed") {
		t.Fatalf("quality circuit read error=%v", err)
	}
	if len(gw.calls) != 0 || len(gw.updated) != 0 || len(gw.provisioned) != 0 {
		t.Fatalf("failed circuit read reached gateway: %+v", gw)
	}
}

func TestApplySchedulingAllocatesPlanScopedGenerationAndPreservesProvidedFence(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-a", RemoteID: "acc-a", State: contracts.BindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	gw := &fenceRecordingGateway{fakeGateway: fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: true}}}}
	eng := New(st, gw)
	if _, err := eng.ApplyScheduling(ctx, plan.ID, map[string]bool{"ch-a": false}); err != nil {
		t.Fatalf("manual scheduling apply: %v", err)
	}
	if len(gw.fences) != 1 || gw.fences[0].fence.Scope != "auto-switch/plan/"+plan.ID || gw.fences[0].fence.Version <= 0 {
		t.Fatalf("manual fence calls=%+v", gw.fences)
	}
	// A caller that already owns a durable decision generation keeps that exact
	// generation; the engine must not replace it with an unrelated one.
	gw.accounts[0].Schedulable = false
	claimedPlan, err := st.ClaimRoutePlanScheduling(ctx, plan.ID, contracts.RoutePlanPublished)
	if err != nil {
		t.Fatalf("claim provided generation: %v", err)
	}
	provided := contracts.WithGatewaySchedulingFence(ctx, contracts.GatewaySchedulingFence{
		Scope: "auto-switch/plan/" + plan.ID, Version: claimedPlan.SchedulingGeneration,
	})
	if _, err := eng.ApplyScheduling(provided, plan.ID, map[string]bool{"ch-a": true}); err != nil {
		t.Fatalf("fenced scheduling apply: %v", err)
	}
	if len(gw.fences) != 2 || gw.fences[1].fence.Version != claimedPlan.SchedulingGeneration {
		t.Fatalf("provided fence calls=%+v", gw.fences)
	}
}

func TestApplySchedulingDoesNotDrainWhenAdmissionFails(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
		{id: "ch-b", status: contracts.UpstreamChannelActive, remoteID: "acc-b"},
	})
	for _, seed := range []contracts.PublishedBinding{
		{PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-a", RemoteID: "acc-a", State: contracts.BindingActive},
		{PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-b", RemoteID: "acc-b", State: contracts.BindingDisabled},
	} {
		_, _ = st.UpsertPublishedBinding(ctx, seed)
	}
	gw := &fakeGateway{
		accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: true}, {ID: "acc-b", Schedulable: false}},
		failOn:   map[string]bool{"acc-b": true},
	}
	if _, err := New(st, gw).ApplyScheduling(ctx, plan.ID, map[string]bool{"ch-a": false, "ch-b": true}); err == nil {
		t.Fatal("expected admission failure")
	}
	if !gw.accounts[0].Schedulable {
		t.Fatal("current source was drained after replacement admission failed")
	}
	if len(gw.calls) != 1 || gw.calls[0].accountID != "acc-b" || !gw.calls[0].schedulable {
		t.Fatalf("unexpected gateway calls: %+v", gw.calls)
	}
}

func TestApplySchedulingLosingGenerationBeforeMutationDoesNotTouchGatewayOrBindings(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-a", RemoteID: "acc-a", State: contracts.BindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	gate := &supersedeBeforeMutationStore{Store: st, planID: plan.ID}
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: true}}}

	_, err := New(gate, gw).ApplyScheduling(ctx, plan.ID, map[string]bool{"ch-a": false})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale scheduling apply error=%v, want conflict", err)
	}
	if len(gw.calls) != 0 || !gw.accounts[0].Schedulable {
		t.Fatalf("stale generation reached gateway: calls=%+v accounts=%+v", gw.calls, gw.accounts)
	}
	bindings, listErr := st.ListPublishedBindings(ctx, plan.ID)
	if listErr != nil || len(bindings) != 1 || bindings[0].State != contracts.BindingActive || bindings[0].SchedulingGeneration != 0 {
		t.Fatalf("stale generation changed binding facts: %+v err=%v", bindings, listErr)
	}
}

func TestApplySchedulingAllowsOnlyDrainForSuspendedPlan(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanSuspended, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
		{id: "ch-b", status: contracts.UpstreamChannelActive, remoteID: "acc-b"},
	})
	for _, seed := range []contracts.PublishedBinding{
		{PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-a", RemoteID: "acc-a", State: contracts.BindingActive},
		{PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-b", RemoteID: "acc-b", State: contracts.BindingActive},
	} {
		if _, err := st.UpsertPublishedBinding(ctx, seed); err != nil {
			t.Fatalf("seed binding: %v", err)
		}
	}
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{
		{ID: "acc-a", Schedulable: true}, {ID: "acc-b", Schedulable: true},
	}}

	result, err := New(st, gw).ApplyScheduling(ctx, plan.ID, map[string]bool{"ch-a": false, "ch-b": false})
	if err != nil {
		t.Fatalf("ApplyScheduling suspended drain: %v", err)
	}
	if actions := actionByChannel(result); actions["ch-a"] != contracts.ReconcileDisable || actions["ch-b"] != contracts.ReconcileDisable {
		t.Fatalf("suspended drain actions=%+v", result.Actions)
	}
	for _, call := range gw.calls {
		if call.schedulable {
			t.Fatalf("suspended drain enabled an account: %+v", gw.calls)
		}
	}
	updated, err := st.GetRoutePlan(ctx, plan.ID)
	if err != nil || updated.Status != contracts.RoutePlanSuspended {
		t.Fatalf("scheduling drain changed plan status: %+v err=%v", updated, err)
	}

	if _, err := New(st, gw).ApplyScheduling(ctx, plan.ID, map[string]bool{"ch-a": true}); err == nil {
		t.Fatal("suspended plan accepted an enable scheduling intent")
	}
	if len(gw.calls) != 2 {
		t.Fatalf("rejected suspended enable reached gateway: %+v", gw.calls)
	}
}

// A desired-active channel whose remote account exists but is not scheduling
// must produce an "enable" action, and dry-run must not touch the gateway.
func TestPlanEnablesDisabledRemote(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: false}}}
	eng := New(st, gw)

	got, err := eng.Plan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !got.DryRun {
		t.Errorf("expected DryRun=true")
	}
	if a := actionByChannel(got); a["ch-a"] != contracts.ReconcileEnable {
		t.Errorf("ch-a action = %q, want enable", a["ch-a"])
	}
	if len(gw.calls) != 0 {
		t.Errorf("dry-run must not call gateway, got %d calls", len(gw.calls))
	}
	// dry-run must not persist bindings either.
	if bs, _ := st.ListPublishedBindings(ctx, plan.ID); len(bs) != 0 {
		t.Errorf("dry-run persisted %d bindings, want 0", len(bs))
	}
}

// Apply on an enable action toggles the gateway and records an active binding.
func TestApplyEnableTogglesAndBinds(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanDraft, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: false}}}
	eng := New(st, gw)

	got, err := eng.Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got.DryRun {
		t.Errorf("expected DryRun=false")
	}
	if len(gw.calls) != 1 || gw.calls[0].accountID != "acc-a" || !gw.calls[0].schedulable {
		t.Errorf("expected one enable(acc-a) call, got %+v", gw.calls)
	}
	bs, _ := st.ListPublishedBindings(ctx, plan.ID)
	if len(bs) != 1 || bs[0].State != contracts.BindingActive || bs[0].RemoteID != "acc-a" {
		t.Errorf("expected one active binding for acc-a, got %+v", bs)
	}
	// Draft plan is promoted to published on apply.
	updated, _ := st.GetRoutePlan(ctx, plan.ID)
	if updated.Status != contracts.RoutePlanPublished {
		t.Errorf("plan status = %q, want published", updated.Status)
	}
}

// An already-scheduling desired channel is a noop: no action, no gateway call,
// but the binding is still recorded as active (paper trail).
func TestApplyNoopWhenAlreadyActive(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: true}}}
	eng := New(st, gw)

	got, err := eng.Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got.Actions) != 0 {
		t.Errorf("expected no actions, got %+v", got.Actions)
	}
	if len(gw.calls) != 0 {
		t.Errorf("expected no gateway calls, got %+v", gw.calls)
	}
	bs, _ := st.ListPublishedBindings(ctx, plan.ID)
	if len(bs) != 1 || bs[0].State != contracts.BindingActive {
		t.Errorf("expected active binding paper trail, got %+v", bs)
	}
}

// Connector lifecycle tasks require an opaque local credential binding. Core
// rejects an unbound provision before calling the gateway and records the
// channel as failed so the missing local configuration is visible.
func TestApplyRejectsCreateWithoutCredentialBinding(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive},
	})
	gw := &fakeGateway{lifecycle: true}
	eng := New(st, gw)

	got, err := eng.Apply(ctx, plan.ID)
	if err == nil || !strings.Contains(err.Error(), "connector credential binding is required") {
		t.Fatalf("expected missing credential binding error, got %v", err)
	}
	if a := actionByChannel(got); a["ch-a"] != contracts.ReconcileCreate {
		t.Errorf("ch-a action = %q, want create", a["ch-a"])
	}
	if len(gw.provisioned) != 0 {
		t.Errorf("unbound create must not call gateway, got %+v", gw.provisioned)
	}
	bs, _ := st.ListPublishedBindings(ctx, plan.ID)
	if len(bs) != 1 || bs[0].State != contracts.BindingFailed || !strings.Contains(bs[0].LastError, "credential binding") {
		t.Errorf("expected failed binding with validation error, got %+v", bs)
	}
}

func TestOwnerProvidedMissingRemoteIsRejectedBeforeBindingClaim(t *testing.T) {
	ctx, st := newFixture(t)
	inst, err := st.CreateInstance(ctx, contracts.Instance{UserID: 101, Name: "owner gateway", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	pool, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "owner accounts", Status: contracts.UpstreamPoolActive})
	channel, _ := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, SourceID: "owner-source", DisplayName: "existing owner account",
		AccountOwnership:    contracts.GatewayAccountOwnerProvided,
		CredentialBindingID: "owner-binding", Status: contracts.UpstreamChannelActive,
	})
	plan, _ := st.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: inst.UserID, InstanceID: inst.ID, PoolID: pool.ID, Status: contracts.RoutePlanDraft})
	gw := &fakeGateway{lifecycle: true}
	_, err = New(st, gw).Apply(ctx, plan.ID)
	if !errors.Is(err, ErrUnsupportedLifecycle) {
		t.Fatalf("apply error = %v", err)
	}
	if len(gw.provisioned) != 0 {
		t.Fatalf("owner account was provisioned: %+v", gw.provisioned)
	}
	bindings, _ := st.ListPublishedBindings(ctx, plan.ID)
	if len(bindings) != 0 {
		t.Fatalf("forbidden create claimed binding: %+v channel=%s", bindings, channel.ID)
	}
}

func TestOwnerProvidedExistingRemoteIsUpdatedWithoutCreateOrDelete(t *testing.T) {
	ctx, st := newFixture(t)
	inst, err := st.CreateInstance(ctx, contracts.Instance{UserID: 101, Name: "owner gateway", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "owner accounts", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	channel, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, SourceID: "owner-source", DisplayName: "desired owner account",
		Provider: "openai", Priority: 10, AccountOwnership: contracts.GatewayAccountOwnerProvided,
		Status: contracts.UpstreamChannelActive,
		Labels: map[string]string{"remote_id": "owner-remote", "instance_id": inst.ID},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: inst.UserID, InstanceID: inst.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanDraft, Rollout: contracts.RolloutImmediate,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	gw := &fakeGateway{lifecycle: true, accounts: []contracts.GatewayAccount{{
		ID: "owner-remote", DisplayName: "old owner account", Platform: "openai",
		Priority: 10, Schedulable: true,
	}}}

	result, err := New(st, gw).Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if actions := actionByChannel(result); actions[channel.ID] != contracts.ReconcileUpdate {
		t.Fatalf("action = %q, want update", actions[channel.ID])
	}
	if len(gw.updated) != 1 || gw.updated[0].Ownership != contracts.GatewayAccountOwnerProvided ||
		gw.updated[0].RemoteID != "owner-remote" || gw.updated[0].CredentialBindingID != "" ||
		gw.updated[0].ProxyBindingID != "" {
		t.Fatalf("owner update = %+v", gw.updated)
	}
	if len(gw.provisioned) != 0 || len(gw.deleted) != 0 {
		t.Fatalf("owner update crossed lifecycle boundary: creates=%+v deletes=%+v", gw.provisioned, gw.deleted)
	}
	bindings, err := st.ListPublishedBindings(ctx, plan.ID)
	if err != nil || len(bindings) != 1 || bindings[0].RemoteID != "owner-remote" ||
		bindings[0].AccountOwnership != contracts.GatewayAccountOwnerProvided || bindings[0].State != contracts.BindingActive {
		t.Fatalf("owner binding = %+v err=%v", bindings, err)
	}
}

func TestOwnerProvidedPreDispatchFailurePersistsFenceExemptionProof(t *testing.T) {
	ctx, st := newFixture(t)
	inst, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: 101, Name: "owner gateway", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{
		Name: "owner accounts", Status: contracts.UpstreamPoolActive,
	})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	channel, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, SourceID: "owner-source", DisplayName: "desired owner account",
		Provider: "openai", Priority: 10, AccountOwnership: contracts.GatewayAccountOwnerProvided,
		Status: contracts.UpstreamChannelActive,
		Labels: map[string]string{"remote_id": "owner-remote", "instance_id": inst.ID},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: inst.UserID, InstanceID: inst.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanDraft, Rollout: contracts.RolloutImmediate,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	gw := &fakeGateway{
		lifecycle: true,
		accounts: []contracts.GatewayAccount{{
			ID: "owner-remote", DisplayName: "old owner account", Platform: "openai",
			Priority: 10, Schedulable: true,
		}},
		updateErr: adapters.ErrGatewayMutationNotDispatched,
	}

	if _, err := New(st, gw).Apply(ctx, plan.ID); !errors.Is(err, adapters.ErrGatewayMutationNotDispatched) {
		t.Fatalf("apply error = %v, want no-dispatch sentinel", err)
	}
	bindings, err := st.ListPublishedBindings(ctx, plan.ID)
	if err != nil || len(bindings) != 1 || bindings[0].ChannelID != channel.ID ||
		bindings[0].State != contracts.BindingFailed ||
		!strings.HasPrefix(bindings[0].LastError, contracts.OwnerMetadataUpdateNotDispatchedMarker+":") {
		t.Fatalf("owner no-dispatch binding = %+v err=%v", bindings, err)
	}
}

// A maintenance channel that is currently scheduling must be disabled.
func TestApplyDisableMaintenanceChannel(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelMaintenance, remoteID: "acc-a"},
	})
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: true}}}
	eng := New(st, gw)

	got, err := eng.Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if a := actionByChannel(got); a["ch-a"] != contracts.ReconcileDisable {
		t.Errorf("ch-a action = %q, want disable", a["ch-a"])
	}
	if len(gw.calls) != 1 || gw.calls[0].schedulable {
		t.Errorf("expected one disable call, got %+v", gw.calls)
	}
	bs, _ := st.ListPublishedBindings(ctx, plan.ID)
	if len(bs) != 1 || bs[0].State != contracts.BindingDisabled {
		t.Errorf("expected disabled binding, got %+v", bs)
	}
}

// A suspended plan withdraws every previously published channel.
func TestApplySuspendedPlanRevokes(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanSuspended, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	// Pre-existing active binding so there is something to revoke.
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-a", RemoteID: "acc-a", State: contracts.BindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: true}}}
	eng := New(st, gw)

	got, err := eng.Rollback(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if a := actionByChannel(got); a["ch-a"] != contracts.ReconcileRevoke {
		t.Errorf("ch-a action = %q, want revoke", a["ch-a"])
	}
	if len(gw.calls) != 1 || gw.calls[0].schedulable {
		t.Errorf("expected one disable(revoke) call, got %+v", gw.calls)
	}
	bs, _ := st.ListPublishedBindings(ctx, plan.ID)
	if len(bs) != 1 || bs[0].State != contracts.BindingRevoked {
		t.Errorf("expected revoked binding, got %+v", bs)
	}
	// A suspended plan must NOT be promoted to published.
	updated, _ := st.GetRoutePlan(ctx, plan.ID)
	if updated.Status != contracts.RoutePlanSuspended {
		t.Errorf("plan status = %q, want suspended (unchanged)", updated.Status)
	}
}

func TestApplySuspendedRetiredPlanDeprovisionsAtCurrentGeneration(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanSuspended, contracts.UpstreamPoolRetired, 0, []seedChan{
		{id: "ch-retired", status: contracts.UpstreamChannelRetired, remoteID: "acc-retired"},
	})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-retired", RemoteID: "acc-retired",
		State: contracts.BindingRevoked, AccountOwnership: contracts.GatewayAccountPlatformManaged,
	}); err != nil {
		t.Fatalf("seed retired binding: %v", err)
	}
	gw := &fakeGateway{lifecycle: true, accounts: []contracts.GatewayAccount{{ID: "acc-retired", Schedulable: false}}}

	result, err := New(st, gw).Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Apply suspended retired plan: %v", err)
	}
	if actions := actionByChannel(result); actions["ch-retired"] != contracts.ReconcileDeprovision {
		t.Fatalf("suspended retired reconcile actions=%+v, want deprovision", result.Actions)
	}
	if len(gw.calls) != 1 || gw.calls[0].accountID != "acc-retired" || gw.calls[0].schedulable {
		t.Fatalf("deprovision must only drain before delete: %+v", gw.calls)
	}
	if len(gw.deleted) != 1 || gw.deleted[0] != "acc-retired" || len(gw.provisioned) != 0 || len(gw.updated) != 0 {
		t.Fatalf("unexpected lifecycle calls: deleted=%+v provisioned=%+v updated=%+v", gw.deleted, gw.provisioned, gw.updated)
	}
	updated, err := st.GetRoutePlan(ctx, plan.ID)
	if err != nil || updated.Status != contracts.RoutePlanSuspended || updated.SchedulingGeneration <= plan.SchedulingGeneration {
		t.Fatalf("final plan=%+v err=%v", updated, err)
	}
	bindings, err := st.ListPublishedBindings(ctx, plan.ID)
	if err != nil || len(bindings) != 1 || bindings[0].State != contracts.BindingRevoked ||
		bindings[0].SchedulingGeneration != updated.SchedulingGeneration {
		t.Fatalf("final bindings=%+v err=%v, plan generation=%d", bindings, err, updated.SchedulingGeneration)
	}
}

func TestApplySuspendedRetiredPlanQueuesDeleteWhenRemoteAlreadyAbsent(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanSuspended, contracts.UpstreamPoolRetired, 0, []seedChan{
		{id: "ch-retired", status: contracts.UpstreamChannelRetired, remoteID: "acc-retired"},
	})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-retired", RemoteID: "acc-retired",
		State: contracts.BindingRevoked, AccountOwnership: contracts.GatewayAccountPlatformManaged,
	}); err != nil {
		t.Fatal(err)
	}
	gw := &fakeGateway{lifecycle: true}
	result, err := New(st, gw).Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Apply absent retired account: %v", err)
	}
	if actions := actionByChannel(result); actions["ch-retired"] != contracts.ReconcileDeprovision {
		t.Fatalf("actions=%+v, want deprovision", result.Actions)
	}
	if len(gw.calls) != 0 || len(gw.deleted) != 1 || gw.deleted[0] != "acc-retired" {
		t.Fatalf("absent cleanup calls=%+v deletes=%+v", gw.calls, gw.deleted)
	}
}

func TestApplySuspendedActivePlanNeverReenablesOrMutatesLifecycle(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanSuspended, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-active", status: contracts.UpstreamChannelActive, remoteID: "acc-active"},
		{id: "ch-unbound", status: contracts.UpstreamChannelActive},
	})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-active", RemoteID: "acc-active",
		State: contracts.BindingRevoked, AccountOwnership: contracts.GatewayAccountPlatformManaged,
	}); err != nil {
		t.Fatalf("seed active binding: %v", err)
	}
	gw := &fakeGateway{lifecycle: true, accounts: []contracts.GatewayAccount{{ID: "acc-active", Schedulable: false}}}

	result, err := New(st, gw).Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Apply suspended active plan: %v", err)
	}
	if actions := actionByChannel(result); actions["ch-active"] != contracts.ReconcileRevoke {
		t.Fatalf("suspended active reconcile actions=%+v, want revoke only", result.Actions)
	}
	if len(gw.calls) != 0 || len(gw.deleted) != 0 || len(gw.provisioned) != 0 || len(gw.updated) != 0 {
		t.Fatalf("suspended reconcile re-enabled or mutated lifecycle: %+v", gw)
	}
}

func TestApplyMaintenancePoolFailsClosedWithoutCreateOrEnable(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolMaintenance, 0, []seedChan{
		{id: "ch-existing", status: contracts.UpstreamChannelActive, remoteID: "acc-existing"},
		{id: "ch-missing", status: contracts.UpstreamChannelActive},
	})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-existing", RemoteID: "acc-existing",
		State: contracts.BindingActive, AccountOwnership: contracts.GatewayAccountPlatformManaged,
	}); err != nil {
		t.Fatal(err)
	}
	gw := &fakeGateway{lifecycle: true, accounts: []contracts.GatewayAccount{{ID: "acc-existing", Schedulable: false}}}
	result, err := New(st, gw).Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Apply maintenance pool: %v", err)
	}
	if actions := actionByChannel(result); actions["ch-existing"] != contracts.ReconcileRevoke {
		t.Fatalf("maintenance actions=%+v, want existing binding revoked", result.Actions)
	}
	if len(gw.provisioned) != 0 || len(gw.updated) != 0 {
		t.Fatalf("maintenance pool mutated lifecycle: %+v", gw)
	}
	for _, call := range gw.calls {
		if call.schedulable {
			t.Fatalf("maintenance pool enabled an account: %+v", gw.calls)
		}
	}
}

func TestApplySuspendedRetiredPlanLosingGenerationDoesNotTouchGatewayOrBinding(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanSuspended, contracts.UpstreamPoolRetired, 0, []seedChan{
		{id: "ch-retired", status: contracts.UpstreamChannelRetired, remoteID: "acc-retired"},
	})
	seed, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-retired", RemoteID: "acc-retired",
		State: contracts.BindingRevoked, AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	if err != nil {
		t.Fatalf("seed retired binding: %v", err)
	}
	gate := &supersedeBeforeMutationStore{Store: st, planID: plan.ID}
	gw := &fakeGateway{lifecycle: true, accounts: []contracts.GatewayAccount{{ID: "acc-retired", Schedulable: false}}}

	_, err = New(gate, gw).Apply(ctx, plan.ID)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale suspended cleanup error=%v, want conflict", err)
	}
	if len(gw.calls) != 0 || len(gw.deleted) != 0 {
		t.Fatalf("stale suspended cleanup reached gateway: calls=%+v deleted=%+v", gw.calls, gw.deleted)
	}
	bindings, listErr := st.ListPublishedBindings(ctx, plan.ID)
	if listErr != nil || len(bindings) != 1 || bindings[0].SchedulingGeneration != seed.SchedulingGeneration || bindings[0].State != seed.State {
		t.Fatalf("stale suspended cleanup changed binding: %+v err=%v", bindings, listErr)
	}
}

// maxChannels caps how many active channels are published; the capped-out
// channel that was previously bound is revoked.
func TestMaxChannelsCapRevokesExtra(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 1, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a", priority: 1},
		{id: "ch-b", status: contracts.UpstreamChannelActive, remoteID: "acc-b", priority: 2},
	})
	// ch-b was published before the cap tightened.
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-b", RemoteID: "acc-b", State: contracts.BindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{
		{ID: "acc-a", Schedulable: false},
		{ID: "acc-b", Schedulable: true},
	}}
	eng := New(st, gw)

	got, err := eng.Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	a := actionByChannel(got)
	if a["ch-a"] != contracts.ReconcileEnable {
		t.Errorf("ch-a action = %q, want enable (priority winner)", a["ch-a"])
	}
	if a["ch-b"] != contracts.ReconcileRevoke {
		t.Errorf("ch-b action = %q, want revoke (capped out)", a["ch-b"])
	}
}

// A binding whose channel no longer exists in the pool is an orphan and must be
// revoked.
func TestApplyOrphanBindingRevoked(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-gone", RemoteID: "acc-gone", State: contracts.BindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	gw := &fakeGateway{lifecycle: true, accounts: []contracts.GatewayAccount{
		{ID: "acc-a", Schedulable: true},
		{ID: "acc-gone", Schedulable: true},
	}}
	eng := New(st, gw)

	got, err := eng.Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if a := actionByChannel(got); a["ch-gone"] != contracts.ReconcileDeprovision {
		t.Errorf("ch-gone action = %q, want deprovision", a["ch-gone"])
	}
}

// A retired pool withdraws all channels regardless of channel status.
func TestApplyRetiredPoolRevokesAll(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolRetired, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-a", RemoteID: "acc-a", State: contracts.BindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	gw := &fakeGateway{lifecycle: true, accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: true}}}
	eng := New(st, gw)

	got, err := eng.Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if a := actionByChannel(got); a["ch-a"] != contracts.ReconcileDeprovision {
		t.Errorf("ch-a action = %q, want deprovision", a["ch-a"])
	}
}

// When the only gateway toggle fails, apply returns an aggregated error,
// records the binding as failed, and marks the run failed rather than partial.
func TestApplyGatewayErrorIsSurfaced(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	gw := &fakeGateway{
		accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: false}},
		failOn:   map[string]bool{"acc-a": true},
	}
	eng := New(st, gw)

	got, err := eng.Apply(ctx, plan.ID)
	if err == nil {
		t.Fatalf("expected aggregated error, got nil")
	}
	if len(got.Actions) != 1 {
		t.Errorf("expected plan with 1 failed action, got %+v", got.Actions)
	}
	bs, _ := st.ListPublishedBindings(ctx, plan.ID)
	if len(bs) != 1 || bs[0].State != contracts.BindingFailed || bs[0].LastError == "" {
		t.Errorf("expected failed binding with error, got %+v", bs)
	}
	runs, err := st.ListReconcileRuns(ctx, plan.ID, 0)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != contracts.ReconcileRunFailed {
		t.Fatalf("zero successful actions should record a failed run, got %+v", runs)
	}
}

func TestApplyMixedExecutionOutcomeRecordsPartial(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
		{id: "ch-b", status: contracts.UpstreamChannelActive, remoteID: "acc-b"},
	})
	gw := &fakeGateway{
		accounts: []contracts.GatewayAccount{
			{ID: "acc-a", Schedulable: false},
			{ID: "acc-b", Schedulable: false},
		},
		failOn: map[string]bool{"acc-b": true},
	}

	result, err := New(st, gw).Apply(ctx, plan.ID)
	if err == nil {
		t.Fatal("expected mixed apply error")
	}
	if actions := actionByChannel(result); actions["ch-a"] != contracts.ReconcileEnable || actions["ch-b"] != contracts.ReconcileEnable {
		t.Fatalf("expected both enable outcomes, got %+v", result.Actions)
	}
	runs, listErr := st.ListReconcileRuns(ctx, plan.ID, 0)
	if listErr != nil {
		t.Fatalf("list runs: %v", listErr)
	}
	if len(runs) != 1 || runs[0].Status != contracts.ReconcileRunPartial {
		t.Fatalf("one success plus one failure should record a partial run, got %+v", runs)
	}
}

// A draft remains a draft when an otherwise supported scheduling action fails.
func TestApplyFailureDoesNotPublishDraft(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanDraft, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	gw := &fakeGateway{
		accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: false}},
		failOn:   map[string]bool{"acc-a": true},
	}

	if _, err := New(st, gw).Apply(ctx, plan.ID); err == nil {
		t.Fatal("expected apply failure")
	}
	updated, err := st.GetRoutePlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("get route plan: %v", err)
	}
	if updated.Status != contracts.RoutePlanDraft {
		t.Fatalf("failed apply published draft: status=%q", updated.Status)
	}
}

// Unsupported lifecycle is preflighted across the complete diff. A mixed
// create+enable plan must not execute the otherwise supported enable or persist
// any binding/status change.
func TestApplyUnsupportedLifecycleRejectsMixedPlanWithoutPartialWrites(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanDraft, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-create", status: contracts.UpstreamChannelActive},
		{id: "ch-enable", status: contracts.UpstreamChannelActive, remoteID: "acc-enable"},
	})
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-enable", Schedulable: false}}}
	eng := New(st, gw)

	dry, err := eng.Plan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if actions := actionByChannel(dry); actions["ch-create"] != contracts.ReconcileCreate || actions["ch-enable"] != contracts.ReconcileEnable {
		t.Fatalf("dry-run must expose create+enable, got %+v", dry.Actions)
	}

	got, err := eng.Apply(ctx, plan.ID)
	if !errors.Is(err, ErrUnsupportedLifecycle) {
		t.Fatalf("Apply error = %v, want ErrUnsupportedLifecycle", err)
	}
	var unsupported *UnsupportedLifecycleError
	if !errors.As(err, &unsupported) || len(unsupported.Actions) != 1 || unsupported.Actions[0] != contracts.ReconcileCreate {
		t.Fatalf("typed unsupported actions = %+v", unsupported)
	}
	if actions := actionByChannel(got); actions["ch-create"] != contracts.ReconcileCreate || actions["ch-enable"] != contracts.ReconcileEnable {
		t.Fatalf("rejected apply should return the complete diff, got %+v", got.Actions)
	}
	if len(gw.calls) != 0 || len(gw.provisioned) != 0 || len(gw.updated) != 0 || len(gw.deleted) != 0 {
		t.Fatalf("unsupported preflight allowed gateway writes: sched=%+v provision=%+v update=%+v delete=%+v", gw.calls, gw.provisioned, gw.updated, gw.deleted)
	}
	if bindings, _ := st.ListPublishedBindings(ctx, plan.ID); len(bindings) != 0 {
		t.Fatalf("unsupported preflight persisted bindings: %+v", bindings)
	}
	updated, err := st.GetRoutePlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("get route plan: %v", err)
	}
	if updated.Status != contracts.RoutePlanDraft {
		t.Fatalf("unsupported apply published draft: status=%q", updated.Status)
	}
	runs, err := st.ListReconcileRuns(ctx, plan.ID, 0)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 2 || runs[0].Kind != contracts.ReconcileRunApply || runs[0].Status != contracts.ReconcileRunFailed {
		t.Fatalf("unsupported apply should record a failed, non-partial run: %+v", runs)
	}
}

func TestApplyUnsupportedLifecycleRejectsCreateHiddenByRolloutHold(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanDraft, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-enable", status: contracts.UpstreamChannelActive, remoteID: "acc-enable"},
		{id: "ch-held-create", status: contracts.UpstreamChannelActive},
	})
	plan.Rollout = contracts.RolloutCanary
	plan.RolloutCanaryCount = 1
	if _, err := st.UpdateRoutePlan(ctx, plan); err != nil {
		t.Fatalf("update rollout: %v", err)
	}
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-enable", Schedulable: false}}}

	result, err := New(st, gw).Apply(ctx, plan.ID)
	if !errors.Is(err, ErrUnsupportedLifecycle) {
		t.Fatalf("Apply error = %v, want ErrUnsupportedLifecycle", err)
	}
	if actions := actionByChannel(result); actions["ch-enable"] != contracts.ReconcileEnable || actions["ch-held-create"] != contracts.ReconcileHold {
		t.Fatalf("expected enable + held create in returned diff, got %+v", result.Actions)
	}
	if len(gw.calls) != 0 {
		t.Fatalf("held unsupported create allowed enable side effect: %+v", gw.calls)
	}
	if bindings, _ := st.ListPublishedBindings(ctx, plan.ID); len(bindings) != 0 {
		t.Fatalf("held unsupported create persisted bindings: %+v", bindings)
	}
}

func TestApplyMaintenanceMissingRemoteFailsClosedWithoutWrites(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanDraft, contracts.UpstreamPoolMaintenance, 0, []seedChan{
		{id: "ch-missing", status: contracts.UpstreamChannelActive, remoteID: "acc-missing"},
	})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-missing", RemoteID: "acc-missing",
		State: contracts.BindingActive, LastError: "preserve me",
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	gw := &fakeGateway{}

	result, err := New(st, gw).Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Apply maintenance drain: %v", err)
	}
	if actions := actionByChannel(result); actions["ch-missing"] != contracts.ReconcileRevoke {
		t.Fatalf("maintenance actions=%+v, want revoke", result.Actions)
	}
	if len(gw.calls) != 0 || len(gw.provisioned) != 0 || len(gw.updated) != 0 || len(gw.deleted) != 0 {
		t.Fatalf("unsupported hidden create allowed gateway writes: %+v", gw)
	}
	bindings, listErr := st.ListPublishedBindings(ctx, plan.ID)
	if listErr != nil {
		t.Fatalf("list bindings: %v", listErr)
	}
	if len(bindings) != 1 || bindings[0].State != contracts.BindingRevoked {
		t.Fatalf("maintenance drain binding: %+v", bindings)
	}
	updated, getErr := st.GetRoutePlan(ctx, plan.ID)
	if getErr != nil {
		t.Fatalf("get plan: %v", getErr)
	}
	if updated.Status != contracts.RoutePlanPublished {
		t.Fatalf("successful maintenance drain plan status=%q", updated.Status)
	}
	runs, runErr := st.ListReconcileRuns(ctx, plan.ID, 0)
	if runErr != nil {
		t.Fatalf("list runs: %v", runErr)
	}
	if len(runs) != 1 || runs[0].Status != contracts.ReconcileRunSucceeded {
		t.Fatalf("maintenance drain run=%+v", runs)
	}
}

// A noop still records the desired binding state. If that write fails, apply
// should report the error even though no gateway call was needed.
func TestApplyNoopBindingPersistErrorIsSurfaced(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: true}}}
	eng := New(failBindingStore{Store: st}, gw)

	got, err := eng.Apply(ctx, plan.ID)
	if err == nil || !strings.Contains(err.Error(), "persist binding failed") {
		t.Fatalf("expected binding persist error, got %v", err)
	}
	if len(got.Actions) != 0 {
		t.Errorf("noop persist failure should not surface as an action, got %+v", got.Actions)
	}
	if len(gw.calls) != 0 {
		t.Errorf("noop should not call gateway, got %+v", gw.calls)
	}
	runs, listErr := st.ListReconcileRuns(ctx, plan.ID, 0)
	if listErr != nil {
		t.Fatalf("list runs: %v", listErr)
	}
	if len(runs) != 1 || runs[0].Status != contracts.ReconcileRunFailed {
		t.Fatalf("noop persistence error with no successful action should fail the run, got %+v", runs)
	}
}

// A successful gateway mutation followed by a binding persistence failure is a
// partial outcome: the remote state changed even though Core could not record
// the binding.
func TestApplyGatewaySuccessThenBindingFailureRecordsPartial(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: false}}}
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-a", RemoteID: "acc-a", State: contracts.BindingDisabled,
	}); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	if _, err := New(failBindingStore{Store: st}, gw).Apply(ctx, plan.ID); err == nil || !strings.Contains(err.Error(), "persist binding failed") {
		t.Fatalf("expected binding persistence error, got %v", err)
	}
	if len(gw.calls) != 1 || gw.calls[0].accountID != "acc-a" || !gw.calls[0].schedulable {
		t.Fatalf("gateway mutation did not succeed before persistence failed: %+v", gw.calls)
	}
	runs, err := st.ListReconcileRuns(ctx, plan.ID, 0)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != contracts.ReconcileRunPartial {
		t.Fatalf("successful gateway write plus persistence failure should be partial, got %+v", runs)
	}
}

// Reconciling a missing plan returns store.ErrNotFound.
func TestPlanNotFound(t *testing.T) {
	ctx, st := newFixture(t)
	eng := New(st, &fakeGateway{})
	if _, err := eng.Plan(ctx, "plan-missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// Applying twice is idempotent: the second apply is all noops with no gateway
// calls, and the binding stays active.
func TestApplyIdempotent(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: false}}}
	eng := New(st, gw)

	if _, err := eng.Apply(ctx, plan.ID); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	callsAfterFirst := len(gw.calls)
	got, err := eng.Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(got.Actions) != 0 {
		t.Errorf("second apply should be noop, got %+v", got.Actions)
	}
	if len(gw.calls) != callsAfterFirst {
		t.Errorf("second apply made extra gateway calls: %+v", gw.calls[callsAfterFirst:])
	}
	bs, _ := st.ListPublishedBindings(ctx, plan.ID)
	if len(bs) != 1 || bs[0].State != contracts.BindingActive {
		t.Errorf("binding should stay active, got %+v", bs)
	}
}
func TestApplyProvisionsMissingRemoteWithLocalBinding(t *testing.T) {
	ctx, st := newFixture(t)
	inst, err := st.CreateInstance(ctx, contracts.Instance{UserID: 101, Name: "gw", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "managed", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	if _, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		ID: "ch-a", PoolID: pool.ID, DisplayName: "managed-a", Provider: "anthropic",
		Models: []string{"claude-3-5-sonnet"}, Groups: []string{"default"},
		CredentialBindingID: "credential-binding-a", ProxyBindingID: "proxy-binding-a", Priority: 10, Weight: 2,
		Status: contracts.UpstreamChannelActive,
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: inst.UserID, InstanceID: inst.ID, PoolID: pool.ID, Status: contracts.RoutePlanPublished,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	gw := &fakeGateway{lifecycle: true}
	eng := New(st, gw)

	got, err := eng.Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got.Actions) != 1 || got.Actions[0].Type != contracts.ReconcileCreate || got.Actions[0].RemoteID != "prov-ch-a" {
		t.Fatalf("expected one create with remote id, got %+v", got.Actions)
	}
	if len(gw.provisioned) != 1 {
		t.Fatalf("expected one provision call, got %d", len(gw.provisioned))
	}
	spec := gw.provisioned[0]
	if spec.CredentialBindingID != "credential-binding-a" || spec.ProxyBindingID != "proxy-binding-a" || !spec.Schedulable || spec.DisplayName != "managed-a" {
		t.Fatalf("provision spec did not preserve opaque local bindings: %+v", spec)
	}
	bindings, err := st.ListPublishedBindings(ctx, plan.ID)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	if len(bindings) != 1 || bindings[0].State != contracts.BindingActive || bindings[0].RemoteID != "prov-ch-a" {
		t.Fatalf("expected active binding with remote id, got %+v", bindings)
	}
}

func TestApplyDeliveryKeyProofFailsBeforeLifecycleAndBindingWrites(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive},
	})
	channel, _ := st.GetUpstreamChannel(ctx, "ch-a")
	channel.CredentialBindingID = "binding-a"
	if _, err := st.UpdateUpstreamChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	gw := &fakeGateway{lifecycle: true}
	verifier := deliveryKeyVerifierFunc(func(context.Context, string, string) (keyproof.Verification, error) {
		return keyproof.Verification{}, keyproof.ErrUnverified
	})
	if _, err := New(st, gw, WithDeliveryKeyVerifier(verifier)).Apply(ctx, plan.ID); !errors.Is(err, keyproof.ErrUnverified) {
		t.Fatalf("apply error = %v", err)
	}
	if len(gw.provisioned) != 0 || len(gw.updated) != 0 || len(gw.calls) != 0 {
		t.Fatalf("unverified key reached gateway: %+v", gw)
	}
	bindings, _ := st.ListPublishedBindings(ctx, plan.ID)
	if len(bindings) != 0 {
		t.Fatalf("unverified key claimed a binding: %+v", bindings)
	}
}

func TestApplyDeploymentRequiredForcesUpdateAndPersistsAcknowledgement(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	channel, _ := st.GetUpstreamChannel(ctx, "ch-a")
	channel.CredentialBindingID = "binding-a"
	if _, err := st.UpdateUpstreamChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: channel.ID,
		RemoteID: "acc-a", State: contracts.BindingActive,
	}); err != nil {
		t.Fatal(err)
	}
	delivery, err := st.UpsertUpstreamKeyDelivery(ctx, contracts.UpstreamKeyDelivery{
		ChannelID: channel.ID, SecretRef: "ref:key", MaskedValue: "********-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier := deliveryKeyVerifierFunc(func(context.Context, string, string) (keyproof.Verification, error) {
		return keyproof.Verification{Delivery: contracts.UpstreamKeyDelivery{
			ChannelID: channel.ID, KeyVersion: delivery.KeyVersion,
			ProofStatus: contracts.DeliveryKeyProofVerified, ProofConnectorID: "connector-a",
		}, Proof: contracts.UpstreamKeyProofReceipt{
			ChannelID: channel.ID, InstanceID: plan.InstanceID, KeyVersion: delivery.KeyVersion,
			ConnectorID: "connector-a", Status: contracts.DeliveryKeyProofVerified,
		}, DeploymentRequired: true}, nil
	})
	gw := &fakeGateway{lifecycle: true, accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: true}}}
	result, err := New(st, gw, WithDeliveryKeyVerifier(verifier)).Apply(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if actions := actionByChannel(result); actions[channel.ID] != contracts.ReconcileUpdate || len(gw.updated) != 1 {
		t.Fatalf("current key was not forced through update: result=%+v gateway=%+v", result.Actions, gw.updated)
	}
	deployed, err := st.GetUpstreamKeyDeployment(ctx, channel.ID, plan.InstanceID)
	if err != nil || deployed.Status != contracts.DeliveryKeyDeploymentDeployed || deployed.KeyVersion != delivery.KeyVersion {
		t.Fatalf("deployment acknowledgement = %+v, %v", deployed, err)
	}
}

func TestRolloutBatchedHoldsExcessNewlyActiveChannels(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
		{id: "ch-b", status: contracts.UpstreamChannelActive},
		{id: "ch-c", status: contracts.UpstreamChannelActive},
	})
	plan.Rollout = contracts.RolloutBatched
	plan.RolloutBatchSize = 1
	if _, err := st.UpdateRoutePlan(ctx, plan); err != nil {
		t.Fatalf("update plan rollout: %v", err)
	}
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: false}}}
	eng := New(st, gw)

	got, err := eng.Plan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	actions := actionByChannel(got)
	if actions["ch-a"] != contracts.ReconcileEnable {
		t.Fatalf("first newly-active channel should consume the batch as enable, got %+v", got.Actions)
	}
	if actions["ch-b"] != contracts.ReconcileHold || actions["ch-c"] != contracts.ReconcileHold {
		t.Fatalf("excess channels should be held, got %+v", got.Actions)
	}
}
func TestApplyUpdatesDriftedManagedRemote(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a", priority: 10},
	})
	channels, _ := st.ListUpstreamChannels(ctx, "pool-1")
	_ = channels
	// Load and update through the store so the existing seed helper stays simple.
	chs, err := st.ListUpstreamChannels(ctx, plan.PoolID)
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	ch := chs[0]
	ch.DisplayName = "managed-a"
	ch.Provider = "anthropic"
	ch.CredentialBindingID = "credential-binding-a"
	ch.ProxyBindingID = "proxy-binding-a"
	ch.Groups = []string{"default"}
	ch.Labels["type"] = "oauth"
	if _, err := st.UpdateUpstreamChannel(ctx, ch); err != nil {
		t.Fatalf("update channel: %v", err)
	}
	gw := &fakeGateway{lifecycle: true, accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: true, DisplayName: "old", Platform: "anthropic", Type: "oauth", Priority: 10, GroupIDs: []string{"default"}}}}
	eng := New(st, gw)

	got, err := eng.Apply(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if a := actionByChannel(got); a["ch-a"] != contracts.ReconcileUpdate {
		t.Fatalf("expected update action, got %+v", got.Actions)
	}
	if len(gw.updated) != 1 || gw.updated[0].DisplayName != "managed-a" ||
		gw.updated[0].CredentialBindingID != "credential-binding-a" || gw.updated[0].ProxyBindingID != "proxy-binding-a" {
		t.Fatalf("expected update spec pushed, got %+v", gw.updated)
	}
}

// The engine must record a ReconcileRun in its unified execution layer for
// dry-run, apply, and rollback, so background/automatic callers (Phase 4) are
// audited without relying on the HTTP handler. Trigger and actor come from ctx.
func TestReconcileRunsRecordedInExecutionLayer(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: false}}}
	eng := New(st, gw)

	// Attach an auto trigger + actor, as a health-driven switch would.
	runCtx := contracts.WithReconcileTrigger(ctx, contracts.ReconcileTriggerAuto)
	runCtx = contracts.WithActor(runCtx, contracts.Actor{Type: "workflow", ID: "health-evaluator"})

	if _, err := eng.Plan(runCtx, plan.ID); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := eng.Apply(runCtx, plan.ID); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	runs, err := st.ListReconcileRuns(ctx, plan.ID, 0)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs (dry-run + apply), got %d: %+v", len(runs), runs)
	}
	// Newest-first: apply then dry-run.
	apply, dry := runs[0], runs[1]
	if dry.Kind != contracts.ReconcileRunDryRun || apply.Kind != contracts.ReconcileRunApply {
		t.Fatalf("unexpected run kinds: %q then %q", apply.Kind, dry.Kind)
	}
	if apply.Trigger != contracts.ReconcileTriggerAuto || apply.ActorID != "health-evaluator" {
		t.Fatalf("apply run should carry ctx trigger/actor, got %+v", apply)
	}
	if apply.Status != contracts.ReconcileRunSucceeded {
		t.Fatalf("apply run should be succeeded, got %q", apply.Status)
	}
	if apply.PlanID != plan.ID || apply.InstanceID != plan.InstanceID {
		t.Fatalf("apply run should carry plan/instance ids, got %+v", apply)
	}
	if len(apply.Actions) != 1 || apply.Actions[0].Type != contracts.ReconcileEnable {
		t.Fatalf("apply run should record the enable action, got %+v", apply.Actions)
	}
	if !apply.FinishedAt.After(apply.StartedAt) && !apply.FinishedAt.Equal(apply.StartedAt) {
		t.Fatalf("finished_at should be >= started_at, got start=%v finish=%v", apply.StartedAt, apply.FinishedAt)
	}
}

func TestRollbackOnlyRevokesWithoutLifecycleMutations(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-missing", status: contracts.UpstreamChannelActive},
		{id: "ch-drift", status: contracts.UpstreamChannelActive, remoteID: "acc-drift"},
		{id: "ch-retired", status: contracts.UpstreamChannelRetired, remoteID: "acc-retired"},
	})
	channels, err := st.ListUpstreamChannels(ctx, plan.PoolID)
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	for _, channel := range channels {
		if channel.ID == "ch-drift" {
			channel.DisplayName = "managed-name"
			if _, err := st.UpdateUpstreamChannel(ctx, channel); err != nil {
				t.Fatalf("make channel drift: %v", err)
			}
		}
	}
	for _, binding := range []contracts.PublishedBinding{
		{PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-drift", RemoteID: "acc-drift", State: contracts.BindingActive},
		{PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-retired", RemoteID: "acc-retired", State: contracts.BindingActive},
		{PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-orphan", RemoteID: "acc-orphan", State: contracts.BindingActive},
	} {
		if _, err := st.UpsertPublishedBinding(ctx, binding); err != nil {
			t.Fatalf("seed binding %s: %v", binding.ChannelID, err)
		}
	}
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{
		{ID: "acc-drift", Schedulable: true, DisplayName: "stale-name"},
		{ID: "acc-retired", Schedulable: true},
		{ID: "acc-orphan", Schedulable: true},
	}}

	result, err := New(st, gw).Rollback(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	actions := actionByChannel(result)
	if len(actions) != 3 || actions["ch-drift"] != contracts.ReconcileRevoke ||
		actions["ch-retired"] != contracts.ReconcileRevoke || actions["ch-orphan"] != contracts.ReconcileRevoke {
		t.Fatalf("rollback should only revoke existing bindings, got %+v", result.Actions)
	}
	if _, ok := actions["ch-missing"]; ok {
		t.Fatalf("rollback must not create an unbound missing channel: %+v", result.Actions)
	}
	if len(gw.calls) != 3 {
		t.Fatalf("expected three scheduling revokes, got %+v", gw.calls)
	}
	for _, call := range gw.calls {
		if call.schedulable {
			t.Fatalf("rollback scheduling call enabled an account: %+v", gw.calls)
		}
	}
	if len(gw.provisioned) != 0 || len(gw.updated) != 0 || len(gw.deleted) != 0 {
		t.Fatalf("rollback performed lifecycle mutations: provision=%+v update=%+v delete=%+v", gw.provisioned, gw.updated, gw.deleted)
	}
	bindings, err := st.ListPublishedBindings(ctx, plan.ID)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	if len(bindings) != 3 {
		t.Fatalf("expected three bindings, got %+v", bindings)
	}
	for _, binding := range bindings {
		if binding.State != contracts.BindingRevoked {
			t.Fatalf("binding %s was not revoked: %+v", binding.ChannelID, binding)
		}
	}
}

func TestRollbackRetiredPoolRevokesInsteadOfDeprovisioning(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolRetired, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-a", RemoteID: "acc-a", State: contracts.BindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: true}}}

	result, err := New(st, gw).Rollback(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if actions := actionByChannel(result); actions["ch-a"] != contracts.ReconcileRevoke {
		t.Fatalf("retired pool rollback should revoke, got %+v", result.Actions)
	}
	if len(gw.calls) != 1 || gw.calls[0].schedulable || len(gw.deleted) != 0 {
		t.Fatalf("retired pool rollback should only disable scheduling: calls=%+v deleted=%+v", gw.calls, gw.deleted)
	}
}

// Rollback runs through the engine so it is recorded with kind=rollback and the
// plan is left suspended (drained, not deleted).
func TestRollbackRecordsRunAndSuspendsPlan(t *testing.T) {
	ctx, st := newFixture(t)
	plan := seedPlan(t, ctx, st, contracts.RoutePlanPublished, contracts.UpstreamPoolActive, 0, []seedChan{
		{id: "ch-a", status: contracts.UpstreamChannelActive, remoteID: "acc-a"},
	})
	// Seed an active binding so rollback has something to revoke.
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "ch-a", RemoteID: "acc-a", State: contracts.BindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	gw := &fakeGateway{accounts: []contracts.GatewayAccount{{ID: "acc-a", Schedulable: true}}}
	eng := New(st, gw)

	result, err := eng.Rollback(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if a := actionByChannel(result); a["ch-a"] != contracts.ReconcileRevoke {
		t.Fatalf("rollback should revoke ch-a, got %+v", result.Actions)
	}
	if gw.accounts[0].Schedulable {
		t.Fatal("rollback should drain the remote account out of scheduling")
	}
	updated, err := st.GetRoutePlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("get plan: %v", err)
	}
	if updated.Status != contracts.RoutePlanSuspended {
		t.Fatalf("plan should be suspended after rollback, got %q", updated.Status)
	}
	runs, err := st.ListReconcileRuns(ctx, plan.ID, 0)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Kind != contracts.ReconcileRunRollback {
		t.Fatalf("expected one rollback run, got %+v", runs)
	}
	// No trigger attached -> defaults to manual.
	if runs[0].Trigger != contracts.ReconcileTriggerManual {
		t.Fatalf("rollback run should default to manual trigger, got %q", runs[0].Trigger)
	}
	if runs[0].Status != contracts.ReconcileRunSucceeded {
		t.Fatalf("rollback run should be succeeded, got %q", runs[0].Status)
	}
}

// A load failure (missing plan) is recorded as a failed run, not silently
// dropped, and does not panic the executor.
func TestApplyRecordsFailedRunOnLoadError(t *testing.T) {
	ctx, st := newFixture(t)
	eng := New(st, &fakeGateway{})

	if _, err := eng.Apply(ctx, "does-not-exist"); err == nil {
		t.Fatal("expected apply error for missing plan")
	}
	runs, err := st.ListReconcileRuns(ctx, "does-not-exist", 0)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != contracts.ReconcileRunFailed || runs[0].Kind != contracts.ReconcileRunApply {
		t.Fatalf("expected one failed apply run, got %+v", runs)
	}
	if runs[0].Error == "" {
		t.Fatal("failed run should carry the error message")
	}
}
