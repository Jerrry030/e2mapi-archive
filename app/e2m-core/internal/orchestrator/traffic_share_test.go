package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/adapters"
	"e2m.local/core/internal/store"
)

type trafficShareAdapterFixture struct {
	accounts []contracts.GatewayAccount
	writes   []int
}

func (a *trafficShareAdapterFixture) Kind() contracts.InstanceKind {
	return contracts.InstanceKindNewAPI
}
func (a *trafficShareAdapterFixture) Capabilities() []contracts.AdapterCapability {
	return []contracts.AdapterCapability{{Name: contracts.CapabilitySetAccountTrafficShare, System: contracts.InstanceKindNewAPI, Supported: true}}
}
func (a *trafficShareAdapterFixture) ListAccounts(context.Context, contracts.Instance) ([]contracts.GatewayAccount, error) {
	return append([]contracts.GatewayAccount(nil), a.accounts...), nil
}
func (a *trafficShareAdapterFixture) SetSchedulable(context.Context, contracts.Instance, string, bool) error {
	return nil
}
func (a *trafficShareAdapterFixture) SetTrafficShare(_ context.Context, _ contracts.Instance, _ string, weight int) error {
	a.writes = append(a.writes, weight)
	return nil
}
func (a *trafficShareAdapterFixture) ProvisionAccount(context.Context, contracts.Instance, contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	return contracts.GatewayProvisionResult{}, nil
}
func (a *trafficShareAdapterFixture) UpdateAccount(context.Context, contracts.Instance, contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	return contracts.GatewayProvisionResult{}, nil
}
func (a *trafficShareAdapterFixture) DeleteAccount(context.Context, contracts.Instance, string) error {
	return nil
}

func TestSetTrafficSharePreservesZeroAndRequiresCurrentPlanFence(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now())
	user, err := st.CreateUser(ctx, contracts.User{Email: "traffic-share@example.com", Enabled: true, Roles: []contracts.UserRole{contracts.UserRoleClient}})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "gateway", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "pool", Status: contracts.UpstreamPoolActive})
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID, Status: contracts.RoutePlanPublished})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = st.ClaimRoutePlanScheduling(ctx, plan.ID, contracts.RoutePlanPublished)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{PlanID: plan.ID, InstanceID: instance.ID, ChannelID: "channel", RemoteID: "opaque-account", State: contracts.BindingActive, SchedulingGeneration: plan.SchedulingGeneration})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &trafficShareAdapterFixture{}
	orch := New(st, map[contracts.InstanceKind]adapters.GatewayAdapter{contracts.InstanceKindNewAPI: adapter})
	if err := orch.SetTrafficShare(ctx, instance.ID, "opaque-account", 0, "test"); err == nil {
		t.Fatal("unfenced traffic share unexpectedly dispatched")
	}
	fenced := contracts.WithGatewaySchedulingFence(ctx, contracts.GatewaySchedulingFence{Scope: "auto-switch/plan/" + plan.ID, Version: plan.SchedulingGeneration})
	if err := orch.SetTrafficShare(fenced, instance.ID, "opaque-account", 0, "test"); err != nil {
		t.Fatal(err)
	}
	if len(adapter.writes) != 1 || adapter.writes[0] != 0 {
		t.Fatalf("writes=%v", adapter.writes)
	}
	audits, err := st.ListAudits(ctx, user.ID)
	if err != nil || len(audits) < 2 || audits[len(audits)-1].Action != "account.traffic_share.set.0" {
		t.Fatalf("audits=%+v err=%v", audits, err)
	}
}

func TestHybridSchedulingWriteRequiresCurrentApplyingExecution(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now())
	user, err := st.CreateUser(ctx, contracts.User{Email: "hybrid-fence@example.com", Enabled: true, Roles: []contracts.UserRole{contracts.UserRoleClient}})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "hybrid-newapi", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatal(err)
	}
	instance.ConnectorID = "connector-hybrid"
	instance, err = st.UpdateInstance(ctx, instance)
	if err != nil {
		t.Fatal(err)
	}
	allocation, err := st.UpsertHybridAllocation(ctx, contracts.HybridAllocation{
		UserID: user.ID, InstanceID: instance.ID,
		DefaultRule: contracts.HybridAllocationRule{OwnerPercent: 0, EconomyPercent: 100, StablePercent: 0},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.CreateVirtualKey(ctx, contracts.VirtualKey{
		UserID: user.ID, InstanceID: instance.ID, Name: "economy", ResourceClass: contracts.ResourceClassEconomy,
		TokenHash: contracts.HashVirtualKey("e2m_v1_hybrid_fence"), SecretRef: "credential_ref:hybrid/fence", Models: []string{"gpt-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertHybridGatewayBinding(ctx, contracts.HybridGatewayBinding{
		ID: "hybrid-binding", UserID: user.ID, InstanceID: instance.ID, ResourceClass: contracts.ResourceClassEconomy,
		ConnectorID: instance.ConnectorID, CredentialBindingID: "credential-binding", RemoteAccountID: "economy-account",
		VirtualKeyID: key.ID, VirtualKeyVersion: key.KeyVersion, Status: contracts.HybridGatewayBindingReady,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := st.CreateHybridRoutingExecution(ctx, contracts.HybridRoutingExecution{
		ID: "hybrid-execution", UserID: user.ID, InstanceID: instance.ID, AllocationVersion: allocation.Version, Model: "gpt-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, claimed, err := st.ClaimHybridRoutingExecution(ctx, "hybrid-test-worker", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("execution=%+v claimed=%v err=%v", execution, claimed, err)
	}
	planned, err := st.PlanHybridRoutingExecution(ctx, contracts.HybridRoutingExecutionPlan{
		ID: execution.ID, WorkerID: "hybrid-test-worker", ExpectedVersion: execution.Version,
		Target:    map[contracts.ResourceClass]int{contracts.ResourceClassOwner: 0, contracts.ResourceClassEconomy: 100, contracts.ResourceClassStable: 0},
		Effective: map[contracts.ResourceClass]int{contracts.ResourceClassOwner: 0, contracts.ResourceClassEconomy: 100, contracts.ResourceClassStable: 0},
		DesiredWeights: []contracts.HybridAccountWeight{
			{AccountID: "economy-account", Class: contracts.ResourceClassEconomy, Weight: 0, Schedulable: true},
			{AccountID: "owner-account", Class: contracts.ResourceClassOwner, Weight: 40, Schedulable: false},
			{AccountID: "stable-account", Class: contracts.ResourceClassStable, Weight: 0, Schedulable: false},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &trafficShareAdapterFixture{}
	orch := New(st, map[contracts.InstanceKind]adapters.GatewayAdapter{contracts.InstanceKindNewAPI: adapter})
	validContext := contracts.WithConnectorExecutionIdentity(ctx, contracts.ConnectorExecutionIdentity{
		Scope: contracts.HybridRoutingExecutionScope, ID: planned.ID, Generation: planned.Generation,
	})
	validContext = contracts.WithGatewaySchedulingFence(validContext, contracts.GatewaySchedulingFence{
		Scope: contracts.HybridRoutingFenceScope(instance.ID, "economy-account"), Version: planned.Generation,
	})
	if err := orch.SetTrafficShare(validContext, instance.ID, "economy-account", 0, "hybrid apply"); err != nil {
		t.Fatalf("current applying execution rejected: %v", err)
	}
	if len(adapter.writes) != 1 {
		t.Fatalf("writes=%v", adapter.writes)
	}

	tests := []struct {
		name string
		ctx  context.Context
	}{
		{name: "manual call", ctx: ctx},
		{name: "unknown execution", ctx: hybridWriteContext(ctx, instance.ID, "economy-account", "unknown-execution", planned.Generation)},
		{name: "stale generation", ctx: hybridWriteContext(ctx, instance.ID, "economy-account", planned.ID, planned.Generation+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := len(adapter.writes)
			if err := orch.SetTrafficShare(test.ctx, instance.ID, "economy-account", 1, "invalid hybrid write"); err == nil {
				t.Fatal("invalid hybrid write unexpectedly dispatched")
			}
			if len(adapter.writes) != before {
				t.Fatalf("writes=%v", adapter.writes)
			}
		})
	}
	if err := orch.SetTrafficShare(ctx, instance.ID, "owner-account", 0, "manual owner write"); err == nil {
		t.Fatal("manual write penetrated applying Hybrid owner account")
	}
	ownerContext := hybridWriteContext(ctx, instance.ID, "owner-account", planned.ID, planned.Generation)
	if err := orch.SetTrafficShare(ownerContext, instance.ID, "owner-account", 0, "hybrid owner write"); err != nil {
		t.Fatalf("fenced Hybrid owner write rejected: %v", err)
	}
	if len(adapter.writes) != 2 {
		t.Fatalf("owner writes=%v", adapter.writes)
	}
	lookupErr := errors.New("execution store unavailable")
	failingOrchestrator := New(hybridExecutionOverrideStore{Store: st, err: lookupErr}, map[contracts.InstanceKind]adapters.GatewayAdapter{
		contracts.InstanceKindNewAPI: adapter,
	})
	if err := failingOrchestrator.SetTrafficShare(validContext, instance.ID, "economy-account", 1, "lookup failure"); !errors.Is(err, lookupErr) {
		t.Fatalf("lookup error=%v, want %v", err, lookupErr)
	}
	if len(adapter.writes) != 2 {
		t.Fatalf("lookup failure reached adapter: %v", adapter.writes)
	}
	completed, err := st.CompleteHybridRoutingExecution(ctx, contracts.HybridRoutingExecutionCompletion{
		ID: planned.ID, WorkerID: "hybrid-test-worker", ExpectedVersion: planned.Version, Succeeded: true,
		ReadBackWeights: planned.DesiredWeights,
	})
	if err != nil || completed.Status != contracts.HybridRoutingExecutionSucceeded {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	if err := orch.SetTrafficShare(validContext, instance.ID, "economy-account", 2, "terminal hybrid write"); err == nil {
		t.Fatal("terminal hybrid execution unexpectedly dispatched")
	}
	if len(adapter.writes) != 2 {
		t.Fatalf("terminal execution reached adapter: %v", adapter.writes)
	}
}

type hybridExecutionOverrideStore struct {
	store.Store
	err error
}

func (s hybridExecutionOverrideStore) GetHybridRoutingExecution(context.Context, int64, string) (contracts.HybridRoutingExecution, error) {
	return contracts.HybridRoutingExecution{}, s.err
}

type hybridExecutionHistoryStore struct {
	store.Store
	executions []contracts.HybridRoutingExecution
}

func (s hybridExecutionHistoryStore) ListHybridRoutingExecutions(context.Context, int64, string, int) ([]contracts.HybridRoutingExecution, error) {
	return append([]contracts.HybridRoutingExecution(nil), s.executions...), nil
}

func TestHybridSchedulingWriteFindsOlderApplyingExecutionBehindTerminalHistory(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now())
	user, _ := st.CreateUser(ctx, contracts.User{Email: "history@example.com", Enabled: true, Roles: []contracts.UserRole{contracts.UserRoleClient}})
	instance, _ := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "newapi-history", Kind: contracts.InstanceKindNewAPI})
	applying := contracts.HybridRoutingExecution{
		ID: "older-applying", UserID: user.ID, InstanceID: instance.ID, Generation: 3,
		Status:         contracts.HybridRoutingExecutionApplying,
		DesiredWeights: []contracts.HybridAccountWeight{{AccountID: "owner-account", Class: contracts.ResourceClassOwner, Schedulable: false}},
	}
	history := hybridExecutionHistoryStore{Store: st, executions: []contracts.HybridRoutingExecution{
		{ID: "newer-terminal", UserID: user.ID, InstanceID: instance.ID, Generation: 4, Status: contracts.HybridRoutingExecutionSucceeded},
		applying,
	}}
	adapter := &trafficShareAdapterFixture{}
	orch := New(history, map[contracts.InstanceKind]adapters.GatewayAdapter{contracts.InstanceKindNewAPI: adapter})
	if err := orch.SetTrafficShare(ctx, instance.ID, "owner-account", 0, "manual write"); err == nil {
		t.Fatal("older applying execution was hidden behind terminal history")
	}
	if len(adapter.writes) != 0 {
		t.Fatalf("manual write reached adapter: %v", adapter.writes)
	}
}

func hybridWriteContext(parent context.Context, instanceID, accountID, executionID string, generation int64) context.Context {
	ctx := contracts.WithConnectorExecutionIdentity(parent, contracts.ConnectorExecutionIdentity{
		Scope: contracts.HybridRoutingExecutionScope, ID: executionID, Generation: generation,
	})
	return contracts.WithGatewaySchedulingFence(ctx, contracts.GatewaySchedulingFence{
		Scope: contracts.HybridRoutingFenceScope(instanceID, accountID), Version: generation,
	})
}

var _ adapters.TrafficShareAdapter = (*trafficShareAdapterFixture)(nil)
