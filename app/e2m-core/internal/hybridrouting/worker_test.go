package hybridrouting

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/adapters"
	"e2m.local/core/internal/store"
)

type workerTestStore struct {
	mu                sync.Mutex
	now               time.Time
	instance          contracts.Instance
	allocation        contracts.HybridAllocation
	bindings          []contracts.HybridGatewayBinding
	keys              map[string]contracts.VirtualKey
	wallet            contracts.Wallet
	candidates        map[contracts.ResourceClass][]contracts.SupplyCandidate
	usage             map[string]contracts.SupplyDailyUsage
	execution         contracts.HybridRoutingExecution
	claimed           bool
	completeCalls     []contracts.HybridRoutingExecutionCompletion
	candidateListCall int
	published         []contracts.PublishedBinding
	publishedErr      error
	previous          []contracts.HybridRoutingExecution
	previousErr       error
}

var _ store.HybridRoutingStore = (*workerTestStore)(nil)

func (s *workerTestStore) GetInstance(context.Context, string) (contracts.Instance, error) {
	return s.instance, nil
}
func (s *workerTestStore) GetHybridAllocation(context.Context, string) (contracts.HybridAllocation, error) {
	return s.allocation, nil
}
func (s *workerTestStore) ListHybridGatewayBindings(context.Context, int64, string) ([]contracts.HybridGatewayBinding, error) {
	return append([]contracts.HybridGatewayBinding(nil), s.bindings...), nil
}
func (s *workerTestStore) ListHybridRoutingExecutions(context.Context, int64, string, int) ([]contracts.HybridRoutingExecution, error) {
	if s.previousErr != nil {
		return nil, s.previousErr
	}
	return append([]contracts.HybridRoutingExecution(nil), s.previous...), nil
}
func (s *workerTestStore) ListPublishedBindings(context.Context, string) ([]contracts.PublishedBinding, error) {
	if s.publishedErr != nil {
		return nil, s.publishedErr
	}
	return append([]contracts.PublishedBinding(nil), s.published...), nil
}
func (s *workerTestStore) GetVirtualKey(_ context.Context, _ int64, id string) (contracts.VirtualKey, error) {
	key, ok := s.keys[id]
	if !ok {
		return contracts.VirtualKey{}, store.ErrNotFound
	}
	return key, nil
}
func (s *workerTestStore) GetWallet(context.Context, int64, string) (contracts.Wallet, error) {
	return s.wallet, nil
}
func (s *workerTestStore) ListSupplyCandidates(_ context.Context, class contracts.ResourceClass, _ string) ([]contracts.SupplyCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.candidateListCall++
	return append([]contracts.SupplyCandidate(nil), s.candidates[class]...), nil
}
func (s *workerTestStore) GetSupplyDailyUsage(_ context.Context, _ int64, _, keyID, _ string) (contracts.SupplyDailyUsage, error) {
	return s.usage[keyID], nil
}
func (s *workerTestStore) ClaimHybridRoutingExecution(_ context.Context, workerID string, lease time.Duration) (contracts.HybridRoutingExecution, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed || s.execution.Status != contracts.HybridRoutingExecutionPending {
		return contracts.HybridRoutingExecution{}, false, nil
	}
	s.claimed = true
	s.execution.Status = contracts.HybridRoutingExecutionApplying
	s.execution.LeaseOwner = workerID
	until := s.now.Add(lease)
	s.execution.LeaseUntil = &until
	s.execution.Version++
	return s.execution, true, nil
}
func (s *workerTestStore) RenewHybridRoutingExecution(_ context.Context, id, workerID string, expectedVersion int64, lease time.Duration) (contracts.HybridRoutingExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.execution.ID || workerID != s.execution.LeaseOwner || expectedVersion != s.execution.Version {
		return contracts.HybridRoutingExecution{}, store.ErrConflict
	}
	until := s.now.Add(lease)
	s.execution.LeaseUntil = &until
	s.execution.Version++
	return s.execution, nil
}
func (s *workerTestStore) PlanHybridRoutingExecution(_ context.Context, input contracts.HybridRoutingExecutionPlan) (contracts.HybridRoutingExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.ID != s.execution.ID || input.WorkerID != s.execution.LeaseOwner || input.ExpectedVersion != s.execution.Version || len(s.execution.DesiredWeights) != 0 {
		return contracts.HybridRoutingExecution{}, store.ErrConflict
	}
	s.execution.Target = clonePercent(input.Target)
	s.execution.Effective = clonePercent(input.Effective)
	s.execution.DesiredWeights = append([]contracts.HybridAccountWeight(nil), input.DesiredWeights...)
	s.execution.AdjustmentCodes = append([]string(nil), input.AdjustmentCodes...)
	s.execution.Version++
	return s.execution, nil
}
func (s *workerTestStore) CompleteHybridRoutingExecution(_ context.Context, input contracts.HybridRoutingExecutionCompletion) (contracts.HybridRoutingExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.ID != s.execution.ID || input.WorkerID != s.execution.LeaseOwner || input.ExpectedVersion != s.execution.Version {
		return contracts.HybridRoutingExecution{}, store.ErrConflict
	}
	s.completeCalls = append(s.completeCalls, input)
	if input.Succeeded {
		s.execution.Status = contracts.HybridRoutingExecutionSucceeded
		s.execution.Actual, _ = contracts.HybridAccountWeightPercent(input.ReadBackWeights)
	} else {
		s.execution.Status = contracts.HybridRoutingExecutionFailed
		s.execution.ErrorCode = input.ErrorCode
	}
	s.execution.Version++
	return s.execution, nil
}

type gatewayWrite struct {
	accountID   string
	weight      int
	schedulable *bool
	identity    contracts.ConnectorExecutionIdentity
	fence       contracts.GatewaySchedulingFence
}

type workerTestGateway struct {
	mu              sync.Mutex
	accounts        []contracts.GatewayAccount
	writes          []gatewayWrite
	writeErr        error
	readErrAfter    int
	listCalls       int
	mutateInventory func([]contracts.GatewayAccount) []contracts.GatewayAccount
}

func (g *workerTestGateway) ListAccounts(context.Context, string) ([]contracts.GatewayAccount, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.listCalls++
	if g.readErrAfter > 0 && g.listCalls >= g.readErrAfter {
		return nil, errors.New("read unavailable")
	}
	accounts := cloneGatewayAccounts(g.accounts)
	if g.mutateInventory != nil {
		accounts = g.mutateInventory(accounts)
	}
	return accounts, nil
}
func (g *workerTestGateway) SetTrafficShare(ctx context.Context, _ string, accountID string, weight int, _ string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	write := gatewayWrite{accountID: accountID, weight: weight}
	write.identity, _ = contracts.ConnectorExecutionIdentityFromContext(ctx)
	write.fence, _ = contracts.GatewaySchedulingFenceFromContext(ctx)
	g.writes = append(g.writes, write)
	if g.writeErr != nil {
		return g.writeErr
	}
	for index := range g.accounts {
		if g.accounts[index].ID == accountID {
			value := weight
			g.accounts[index].CurrentWeight = &value
		}
	}
	return nil
}

func (g *workerTestGateway) SetSchedulable(ctx context.Context, _ string, accountID string, schedulable bool, _ string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	value := schedulable
	write := gatewayWrite{accountID: accountID, schedulable: &value}
	write.identity, _ = contracts.ConnectorExecutionIdentityFromContext(ctx)
	write.fence, _ = contracts.GatewaySchedulingFenceFromContext(ctx)
	g.writes = append(g.writes, write)
	if g.writeErr != nil {
		return g.writeErr
	}
	for index := range g.accounts {
		if g.accounts[index].ID == accountID {
			g.accounts[index].Schedulable = schedulable
		}
	}
	return nil
}

func TestWorkerAppliesExactHybridWeightsAndReadBack(t *testing.T) {
	st, gateway := newWorkerFixture(t, contracts.HybridAllocationRule{OwnerPercent: 80, EconomyPercent: 20, StablePercent: 0})
	worker := newTestWorker(t, st, gateway)
	worker.RunOnce(context.Background())

	if st.execution.Status != contracts.HybridRoutingExecutionSucceeded {
		t.Fatalf("execution=%+v", st.execution)
	}
	wantWeights := map[string]int{"owner-a": 13, "owner-b": 7}
	wantSchedulable := map[string]bool{"economy-account": true, "stable-account": false}
	if len(gateway.writes) != len(wantWeights)+len(wantSchedulable) {
		t.Fatalf("writes=%+v", gateway.writes)
	}
	for _, write := range gateway.writes {
		if write.schedulable == nil {
			if want, exists := wantWeights[write.accountID]; !exists || want != write.weight {
				t.Fatalf("weight write=%+v wants=%v", write, wantWeights)
			}
		} else if want, exists := wantSchedulable[write.accountID]; !exists || want != *write.schedulable {
			t.Fatalf("schedulable write=%+v wants=%v", write, wantSchedulable)
		}
		if write.identity.Scope != contracts.HybridRoutingExecutionScope || write.identity.ID != st.execution.ID || write.identity.Generation != st.execution.Generation {
			t.Fatalf("identity=%+v", write.identity)
		}
		if write.fence.Scope != contracts.HybridRoutingFenceScope(st.instance.ID, write.accountID) || write.fence.Version != st.execution.Generation {
			t.Fatalf("fence=%+v", write.fence)
		}
	}
	if len(st.completeCalls) != 1 || !st.completeCalls[0].Succeeded || len(st.completeCalls[0].ReadBackWeights) != 4 {
		t.Fatalf("completion=%+v", st.completeCalls)
	}
	readBack := map[string]int{}
	readBackSchedulable := map[string]bool{}
	for _, value := range st.completeCalls[0].ReadBackWeights {
		readBack[value.AccountID] = value.Weight
		readBackSchedulable[value.AccountID] = value.Schedulable
	}
	if weight, ok := readBack["stable-account"]; !ok || weight != 0 || readBackSchedulable["stable-account"] {
		t.Fatalf("disabled stable account missing from read-back: weights=%+v schedulable=%+v", readBack, readBackSchedulable)
	}
}

func TestWorkerFailsClosedBeforeWrites(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(*workerTestStore, *workerTestGateway)
		wantCode   string
		wantStatus contracts.HybridRoutingExecutionStatus
	}{
		{name: "model unrepresentable", wantCode: errorModelUnrepresentable, prepare: func(_ *workerTestStore, gateway *workerTestGateway) {
			gateway.accounts[0].Models = []string{"gpt-test", "gpt-other"}
		}},
		{name: "group unrepresentable", wantCode: errorModelUnrepresentable, prepare: func(_ *workerTestStore, gateway *workerTestGateway) {
			gateway.accounts[0].GroupIDs = []string{"vip"}
		}},
		{name: "multi group unrepresentable", wantCode: errorModelUnrepresentable, prepare: func(_ *workerTestStore, gateway *workerTestGateway) {
			gateway.accounts[0].GroupIDs = []string{"default", "vip"}
		}},
		{name: "priority unrepresentable", wantCode: errorModelUnrepresentable, prepare: func(_ *workerTestStore, gateway *workerTestGateway) {
			gateway.accounts[0].Priority = 10
		}},
		{name: "route plan ownership conflict", wantCode: errorSchedulingConflict, prepare: func(st *workerTestStore, _ *workerTestGateway) {
			st.published = []contracts.PublishedBinding{{InstanceID: st.instance.ID, RemoteID: "owner-a", AccountOwnership: contracts.GatewayAccountPlatformManaged, State: contracts.BindingActive}}
		}},
		{name: "route plan binding lookup failed", wantCode: errorBindingUnavailable, prepare: func(st *workerTestStore, _ *workerTestGateway) {
			st.publishedErr = errors.New("ownership unavailable")
		}},
		{name: "unallocated", wantCode: errorCapacityUnallocated, prepare: func(st *workerTestStore, _ *workerTestGateway) {
			st.allocation.DefaultRule = contracts.HybridAllocationRule{OwnerPercent: 0, EconomyPercent: 100, StablePercent: 0}
			st.candidates[contracts.ResourceClassEconomy] = nil
		}},
		{name: "wallet unavailable", wantCode: errorCapacityUnallocated, prepare: func(st *workerTestStore, _ *workerTestGateway) {
			st.allocation.DefaultRule = contracts.HybridAllocationRule{OwnerPercent: 0, EconomyPercent: 100, StablePercent: 0}
			st.wallet.AvailableMicros = 0
		}},
		{name: "price limited", wantCode: errorCapacityUnallocated, prepare: func(st *workerTestStore, _ *workerTestGateway) {
			st.allocation.DefaultRule = contracts.HybridAllocationRule{OwnerPercent: 0, EconomyPercent: 100, StablePercent: 0}
			st.allocation.MaxUnitPriceMicros = 1
		}},
		{name: "daily budget exhausted", wantCode: errorCapacityUnallocated, prepare: func(st *workerTestStore, _ *workerTestGateway) {
			st.allocation.DefaultRule = contracts.HybridAllocationRule{OwnerPercent: 0, EconomyPercent: 100, StablePercent: 0}
			st.allocation.DailyBudgetMicros = 100_000
			usage := st.usage["key-economy"]
			usage.InstanceReservedMicros = 100_000
			st.usage["key-economy"] = usage
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st, gateway := newWorkerFixture(t, contracts.HybridAllocationRule{OwnerPercent: 80, EconomyPercent: 20, StablePercent: 0})
			test.prepare(st, gateway)
			newTestWorker(t, st, gateway).RunOnce(context.Background())
			if len(gateway.writes) != 0 || st.execution.Status != contracts.HybridRoutingExecutionFailed || st.execution.ErrorCode != test.wantCode {
				t.Fatalf("writes=%+v execution=%+v", gateway.writes, st.execution)
			}
		})
	}
}

func TestWorkerModelExecutionIgnoresUnrelatedCohortMetadata(t *testing.T) {
	st, gateway := newWorkerFixture(t, contracts.HybridAllocationRule{OwnerPercent: 80, EconomyPercent: 20, StablePercent: 0})
	st.allocation.ModelOverrides = []contracts.HybridModelAllocation{{Model: "gpt-test", Rule: st.allocation.DefaultRule}}
	st.execution.Model = "gpt-test"
	gateway.accounts = append(gateway.accounts, contracts.GatewayAccount{
		ID: "unrelated-vip", Models: []string{"gpt-other"}, GroupIDs: []string{"vip"}, Priority: 10, Schedulable: true, CurrentWeight: intPtr(100),
	})
	newTestWorker(t, st, gateway).RunOnce(context.Background())
	if st.execution.Status != contracts.HybridRoutingExecutionSucceeded {
		t.Fatalf("execution=%+v", st.execution)
	}
	for _, write := range gateway.writes {
		if write.accountID == "unrelated-vip" {
			t.Fatalf("unrelated account was mutated: %+v", write)
		}
	}
}

func TestWorkerRestoresOwnerAccountsAfterZeroAllocation(t *testing.T) {
	st, gateway := newWorkerFixture(t, contracts.HybridAllocationRule{OwnerPercent: 80, EconomyPercent: 20, StablePercent: 0})
	st.previous = []contracts.HybridRoutingExecution{{
		ID: "previous", UserID: st.instance.UserID, InstanceID: st.instance.ID, Status: contracts.HybridRoutingExecutionSucceeded,
		DesiredWeights: []contracts.HybridAccountWeight{
			{AccountID: "owner-a", Class: contracts.ResourceClassOwner, Weight: 60, Schedulable: false},
			{AccountID: "owner-b", Class: contracts.ResourceClassOwner, Weight: 40, Schedulable: false},
		},
	}}
	for index := range gateway.accounts {
		if gateway.accounts[index].ID == "owner-a" || gateway.accounts[index].ID == "owner-b" {
			gateway.accounts[index].Schedulable = false
		}
	}
	newTestWorker(t, st, gateway).RunOnce(context.Background())
	if st.execution.Status != contracts.HybridRoutingExecutionSucceeded {
		t.Fatalf("execution=%+v", st.execution)
	}
	for _, ownerID := range []string{"owner-a", "owner-b"} {
		found := false
		for _, account := range gateway.accounts {
			if account.ID == ownerID {
				found = account.Schedulable
			}
		}
		if !found {
			t.Fatalf("owner %s was not restored: %+v", ownerID, gateway.accounts)
		}
	}
}

func TestWorkerDoesNotReviveUnmanagedDisabledOwner(t *testing.T) {
	st, gateway := newWorkerFixture(t, contracts.HybridAllocationRule{OwnerPercent: 80, EconomyPercent: 20, StablePercent: 0})
	for index := range gateway.accounts {
		if gateway.accounts[index].ID == "owner-b" {
			gateway.accounts[index].Schedulable = false
		}
	}
	newTestWorker(t, st, gateway).RunOnce(context.Background())
	if st.execution.Status != contracts.HybridRoutingExecutionSucceeded {
		t.Fatalf("execution=%+v", st.execution)
	}
	for _, write := range gateway.writes {
		if write.accountID == "owner-b" {
			t.Fatalf("unmanaged disabled owner was mutated: %+v", write)
		}
	}
}

func TestWorkerRetainsApplyingOnUncertainWriteOrReadBack(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*workerTestGateway)
	}{
		{name: "write uncertainty", prepare: func(g *workerTestGateway) { g.writeErr = errors.New("timeout") }},
		{name: "readback uncertainty", prepare: func(g *workerTestGateway) { g.readErrAfter = 3 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, gateway := newWorkerFixture(t, contracts.HybridAllocationRule{OwnerPercent: 80, EconomyPercent: 20, StablePercent: 0})
			test.prepare(gateway)
			newTestWorker(t, st, gateway).RunOnce(context.Background())
			if st.execution.Status != contracts.HybridRoutingExecutionApplying || len(st.completeCalls) != 0 || len(st.execution.DesiredWeights) == 0 {
				t.Fatalf("execution=%+v completions=%+v", st.execution, st.completeCalls)
			}
		})
	}
}

func TestWorkerRecordsPreDispatchWriteFailure(t *testing.T) {
	st, gateway := newWorkerFixture(t, contracts.HybridAllocationRule{OwnerPercent: 80, EconomyPercent: 20, StablePercent: 0})
	gateway.writeErr = adapters.ErrGatewayMutationNotDispatched
	newTestWorker(t, st, gateway).RunOnce(context.Background())
	if st.execution.Status != contracts.HybridRoutingExecutionFailed || st.execution.ErrorCode != errorWriteFailed {
		t.Fatalf("execution=%+v", st.execution)
	}
}

func TestWorkerReclaimUsesPersistedWriteSetWithoutCapacityRecompile(t *testing.T) {
	st, gateway := newWorkerFixture(t, contracts.HybridAllocationRule{OwnerPercent: 80, EconomyPercent: 20, StablePercent: 0})
	st.execution.DesiredWeights = []contracts.HybridAccountWeight{
		{AccountID: "economy-account", Class: contracts.ResourceClassEconomy, Weight: 0, Schedulable: true},
		{AccountID: "owner-a", Class: contracts.ResourceClassOwner, Weight: 14, Schedulable: true},
		{AccountID: "owner-b", Class: contracts.ResourceClassOwner, Weight: 6, Schedulable: true},
		{AccountID: "stable-account", Class: contracts.ResourceClassStable, Weight: 0, Schedulable: false},
	}
	st.execution.Target = percentMap(80, 20, 0)
	st.execution.Effective = percentMap(80, 20, 0)
	st.candidates = nil
	newTestWorker(t, st, gateway).RunOnce(context.Background())
	if st.execution.Status != contracts.HybridRoutingExecutionSucceeded || st.candidateListCall != 0 {
		t.Fatalf("execution=%+v candidate_calls=%d", st.execution, st.candidateListCall)
	}
}

func TestWorkerReclaimRetainsOwnerDisabledByCurrentExecution(t *testing.T) {
	st, gateway := newWorkerFixture(t, contracts.HybridAllocationRule{OwnerPercent: 50, EconomyPercent: 50, StablePercent: 0})
	st.execution.DesiredWeights = []contracts.HybridAccountWeight{
		{AccountID: "economy-account", Class: contracts.ResourceClassEconomy, Weight: 10, Schedulable: true},
		{AccountID: "owner-a", Class: contracts.ResourceClassOwner, Weight: 10, Schedulable: true},
		{AccountID: "owner-b", Class: contracts.ResourceClassOwner, Weight: 40, Schedulable: false},
		{AccountID: "stable-account", Class: contracts.ResourceClassStable, Weight: 0, Schedulable: false},
	}
	st.execution.Target = percentMap(50, 50, 0)
	st.execution.Effective = percentMap(50, 50, 0)
	st.previous = []contracts.HybridRoutingExecution{st.execution}
	for index := range gateway.accounts {
		if gateway.accounts[index].ID == "owner-b" {
			// Simulate a crash after this account was disabled but before its
			// dormant weight and execution result were committed.
			gateway.accounts[index].Schedulable = false
		}
	}

	newTestWorker(t, st, gateway).RunOnce(context.Background())
	if st.execution.Status != contracts.HybridRoutingExecutionSucceeded {
		t.Fatalf("execution=%+v writes=%+v", st.execution, gateway.writes)
	}
	for _, account := range gateway.accounts {
		if account.ID == "owner-b" && (account.Schedulable || account.CurrentWeight == nil || *account.CurrentWeight != 40) {
			t.Fatalf("reclaimed owner state=%+v", account)
		}
	}
}

func TestPreviouslyManagedOwnerIDsScansAllMatchingExecutions(t *testing.T) {
	current := contracts.HybridRoutingExecution{ID: "current", Model: "gpt-test"}
	managed := previouslyManagedOwnerIDs(current, []contracts.HybridRoutingExecution{
		{ID: "newer", Model: "gpt-test", Status: contracts.HybridRoutingExecutionSucceeded,
			DesiredWeights: []contracts.HybridAccountWeight{{AccountID: "owner-a", Class: contracts.ResourceClassOwner, Schedulable: false}}},
		{ID: "older", Model: "gpt-test", Status: contracts.HybridRoutingExecutionSucceeded,
			DesiredWeights: []contracts.HybridAccountWeight{{AccountID: "owner-b", Class: contracts.ResourceClassOwner, Schedulable: false}}},
	})
	for _, accountID := range []string{"owner-a", "owner-b"} {
		if _, ok := managed[accountID]; !ok {
			t.Fatalf("managed owners=%v, missing %s", managed, accountID)
		}
	}
}

func TestManagedRoutePlanAccountIDsSharesCanonicalNoDispatchRules(t *testing.T) {
	bindings := []contracts.PublishedBinding{
		{InstanceID: "instance", RemoteID: "explicit", AccountOwnership: contracts.GatewayAccountOwnerProvided,
			State: contracts.BindingFailed, LastError: contracts.OwnerMetadataUpdateNotDispatchedMarker + ": validation failed"},
		{InstanceID: "instance", RemoteID: "legacy", AccountOwnership: contracts.GatewayAccountOwnerProvided,
			State: contracts.BindingFailed, LastError: contracts.LegacyManagedAccountSchedulingFencePrefix + "legacy belongs to route plan plan-1"},
		{InstanceID: "instance", RemoteID: "timeout", AccountOwnership: contracts.GatewayAccountOwnerProvided,
			State: contracts.BindingFailed, LastError: "context deadline exceeded"},
		{InstanceID: "instance", RemoteID: "platform", AccountOwnership: contracts.GatewayAccountPlatformManaged,
			State: contracts.BindingFailed, LastError: contracts.OwnerMetadataUpdateNotDispatchedMarker + ": ignored"},
	}
	managed := managedRoutePlanAccountIDs("instance", bindings)
	for _, accountID := range []string{"explicit", "legacy"} {
		if _, exists := managed[accountID]; exists {
			t.Fatalf("proved pre-dispatch account %s remained managed: %v", accountID, managed)
		}
	}
	for _, accountID := range []string{"timeout", "platform"} {
		if _, exists := managed[accountID]; !exists {
			t.Fatalf("uncertain account %s lost its fence: %v", accountID, managed)
		}
	}
}

func newTestWorker(t *testing.T, st *workerTestStore, gateway *workerTestGateway) *Worker {
	t.Helper()
	worker, err := NewWorker(st, gateway, "hybrid-test-worker", time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	worker.now = func() time.Time { return st.now }
	return worker
}

func newWorkerFixture(t *testing.T, rule contracts.HybridAllocationRule) (*workerTestStore, *workerTestGateway) {
	t.Helper()
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	instance := contracts.Instance{ID: "instance-newapi", UserID: 7, Name: "owner gateway", Kind: contracts.InstanceKindNewAPI, ConnectorID: "connector-owner"}
	allocation := contracts.HybridAllocation{UserID: instance.UserID, InstanceID: instance.ID, Basis: contracts.HybridAllocationBasisRequests, DefaultRule: rule.Normalize(), DailyBudgetMicros: 10_000_000, MaxUnitPriceMicros: 100_000, RoutingGeneration: 1, Version: 1}
	models := []string{"gpt-test"}
	keys := map[string]contracts.VirtualKey{}
	bindings := make([]contracts.HybridGatewayBinding, 0, 2)
	for _, item := range []struct {
		class     contracts.ResourceClass
		keyID     string
		accountID string
	}{
		{contracts.ResourceClassEconomy, "key-economy", "economy-account"},
		{contracts.ResourceClassStable, "key-stable", "stable-account"},
	} {
		keys[item.keyID] = contracts.VirtualKey{ID: item.keyID, UserID: instance.UserID, InstanceID: instance.ID, Name: string(item.class), ResourceClass: item.class, KeyVersion: 1, Enabled: true, Models: append([]string(nil), models...), DailyLimitMicros: 10_000_000}
		bindings = append(bindings, contracts.HybridGatewayBinding{ID: "binding-" + string(item.class), UserID: instance.UserID, InstanceID: instance.ID, ResourceClass: item.class, ConnectorID: instance.ConnectorID, CredentialBindingID: "credential-" + string(item.class), RemoteAccountID: item.accountID, VirtualKeyID: item.keyID, VirtualKeyVersion: 1, Status: contracts.HybridGatewayBindingReady, Version: 1})
	}
	candidates := map[contracts.ResourceClass][]contracts.SupplyCandidate{}
	for _, class := range []contracts.ResourceClass{contracts.ResourceClassEconomy, contracts.ResourceClassStable} {
		candidates[class] = []contracts.SupplyCandidate{{Endpoint: contracts.SupplyChannelEndpoint{ChannelID: "channel-" + string(class), Currency: "CNY", MaxRequestMicros: 100_000, CapacityPercent: 100}}}
	}
	usage := map[string]contracts.SupplyDailyUsage{}
	for keyID := range keys {
		usage[keyID] = contracts.SupplyDailyUsage{UserID: instance.UserID, InstanceID: instance.ID, VirtualKeyID: keyID, Currency: "CNY", DayStart: now.Truncate(24 * time.Hour)}
	}
	st := &workerTestStore{now: now, instance: instance, allocation: allocation, bindings: bindings, keys: keys, wallet: contracts.Wallet{UserID: instance.UserID, Currency: "CNY", AvailableMicros: 10_000_000}, candidates: candidates, usage: usage, execution: contracts.HybridRoutingExecution{ID: "execution-1", UserID: instance.UserID, InstanceID: instance.ID, AllocationVersion: allocation.Version, Generation: allocation.RoutingGeneration, Status: contracts.HybridRoutingExecutionPending, Version: 1}}
	gateway := &workerTestGateway{accounts: []contracts.GatewayAccount{
		{ID: "owner-a", Models: append([]string(nil), models...), GroupIDs: []string{"default"}, Schedulable: true, CurrentWeight: intPtr(60)},
		{ID: "owner-b", Models: append([]string(nil), models...), GroupIDs: []string{"default"}, Schedulable: true, CurrentWeight: intPtr(40)},
		{ID: "economy-account", Models: append([]string(nil), models...), GroupIDs: []string{"default"}, CurrentWeight: intPtr(0)},
		{ID: "stable-account", Models: append([]string(nil), models...), GroupIDs: []string{"default"}, Schedulable: true, CurrentWeight: intPtr(0)},
	}}
	return st, gateway
}

func intPtr(value int) *int { return &value }

func clonePercent(input map[contracts.ResourceClass]int) map[contracts.ResourceClass]int {
	out := make(map[contracts.ResourceClass]int, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func percentMap(owner, economy, stable int) map[contracts.ResourceClass]int {
	return map[contracts.ResourceClass]int{contracts.ResourceClassOwner: owner, contracts.ResourceClassEconomy: economy, contracts.ResourceClassStable: stable}
}

func cloneGatewayAccounts(input []contracts.GatewayAccount) []contracts.GatewayAccount {
	out := append([]contracts.GatewayAccount(nil), input...)
	for index := range out {
		out[index].Models = append([]string(nil), input[index].Models...)
		out[index].GroupIDs = append([]string(nil), input[index].GroupIDs...)
		if input[index].CurrentWeight != nil {
			value := *input[index].CurrentWeight
			out[index].CurrentWeight = &value
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
