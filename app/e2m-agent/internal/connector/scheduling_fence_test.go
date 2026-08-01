package connector

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

type recordingSchedulingGateway struct {
	mu      sync.Mutex
	calls   []bool
	deletes []string
}

func (g *recordingSchedulingGateway) ProvisionAccount(context.Context, contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	return contracts.GatewayProvisionResult{}, nil
}
func (g *recordingSchedulingGateway) UpdateAccount(context.Context, contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	return contracts.GatewayProvisionResult{}, nil
}
func (g *recordingSchedulingGateway) DeleteAccount(_ context.Context, id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deletes = append(g.deletes, id)
	return nil
}

type blockingSchedulingGateway struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   []bool
}

func (g *blockingSchedulingGateway) Health(context.Context) (contracts.ConnectorGatewayHealthResult, error) {
	return contracts.ConnectorGatewayHealthResult{}, nil
}

func (g *blockingSchedulingGateway) ListAccounts(context.Context) ([]contracts.GatewayAccount, error) {
	return nil, nil
}

func (g *blockingSchedulingGateway) ProbeQuality(context.Context, contracts.ConnectorGatewayQualityProbeInput) (contracts.ConnectorGatewayQualityProbeResult, error) {
	return contracts.ConnectorGatewayQualityProbeResult{}, nil
}

func (g *blockingSchedulingGateway) SetSchedulable(_ context.Context, _ string, value bool) error {
	g.mu.Lock()
	g.calls = append(g.calls, value)
	call := len(g.calls)
	g.mu.Unlock()
	if call == 1 {
		close(g.started)
		<-g.release
	}
	return nil
}

func (g *blockingSchedulingGateway) values() []bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]bool(nil), g.calls...)
}

func (g *recordingSchedulingGateway) Health(context.Context) (contracts.ConnectorGatewayHealthResult, error) {
	return contracts.ConnectorGatewayHealthResult{}, nil
}

func (g *recordingSchedulingGateway) ListAccounts(context.Context) ([]contracts.GatewayAccount, error) {
	return nil, nil
}

func (g *recordingSchedulingGateway) ProbeQuality(context.Context, contracts.ConnectorGatewayQualityProbeInput) (contracts.ConnectorGatewayQualityProbeResult, error) {
	return contracts.ConnectorGatewayQualityProbeResult{}, nil
}

func (g *recordingSchedulingGateway) SetSchedulable(_ context.Context, _ string, value bool) error {
	g.mu.Lock()
	g.calls = append(g.calls, value)
	g.mu.Unlock()
	return nil
}

func (g *recordingSchedulingGateway) values() []bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]bool(nil), g.calls...)
}

func TestSchedulingFencePersistsTupleAndRejectsLateTaskBeforeGateway(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	gateway := &recordingSchedulingGateway{}
	newConnector := func() *Connector {
		conn := New(Config{ConnectorID: testConnectorID, InstanceID: testInstanceID, ConfigStore: store})
		conn.gatewayForTask = func() (gateways.Gateway, error) { return gateway, nil }
		return conn
	}
	newFencedTask := func(id string, version, sequence int64, enabled bool) contracts.ConnectorTask {
		task := newTask(t, id, contracts.ConnectorTaskGatewaySchedulableSet, contracts.ConnectorGatewaySchedulableSetInput{
			AccountID: "account-a", Schedulable: enabled,
			Fence: &contracts.GatewaySchedulingFence{
				Scope: "auto-switch/plan/plan-a", Version: version, Sequence: sequence,
			},
		})
		task.IdempotencyKey = "intent-" + id
		return task
	}

	first := newFencedTask("generation-1-sequence-2", 1, 2, false)
	if result := newConnector().execute(t.Context(), first); !result.success {
		t.Fatalf("first fenced task failed: %+v", result)
	}

	// Constructing a new Connector proves the watermark is read from disk, not
	// retained only by the first process's memory.
	restarted := newConnector()
	stale := newFencedTask("generation-1-sequence-1", 1, 1, true)
	if result := restarted.execute(t.Context(), stale); result.success || result.taskErr.Code != "scheduling_fence_stale" || result.taskErr.Retryable {
		t.Fatalf("stale task result = %+v", result)
	}

	conflict := newFencedTask("generation-1-sequence-2-conflict", 1, 2, true)
	if result := restarted.execute(t.Context(), conflict); result.success || result.taskErr.Code != "scheduling_fence_conflict" || result.taskErr.Retryable {
		t.Fatalf("conflicting task result = %+v", result)
	}

	// An exact logical replay is answered from the durable write receipt after
	// process restart and does not repeat the gateway mutation.
	replay := first
	replay.ID = "generation-1-sequence-2-replay"
	replay.LeaseNonce = "lease-replay"
	if result := restarted.execute(t.Context(), replay); !result.success {
		t.Fatalf("same-intent replay failed: %+v", result)
	}

	newer := newFencedTask("generation-2-sequence-1", 2, 1, true)
	if result := restarted.execute(t.Context(), newer); !result.success {
		t.Fatalf("new generation failed: %+v", result)
	}

	got := gateway.values()
	if len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("gateway writes = %v, want [false true]", got)
	}
}

func TestSchedulingFenceRejectsCachedSuccessAfterNewerTuple(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	gateway := &recordingSchedulingGateway{}
	conn := New(Config{ConnectorID: testConnectorID, InstanceID: testInstanceID, ConfigStore: store})
	conn.gatewayForTask = func() (gateways.Gateway, error) { return gateway, nil }
	newFencedTask := func(id string, sequence int64, enabled bool) contracts.ConnectorTask {
		task := newTask(t, id, contracts.ConnectorTaskGatewaySchedulableSet, contracts.ConnectorGatewaySchedulableSetInput{
			AccountID: "account-a", Schedulable: enabled,
			Fence: &contracts.GatewaySchedulingFence{
				Scope: "auto-switch/plan/plan-a", Version: 1, Sequence: sequence,
			},
		})
		task.IdempotencyKey = "intent-" + id
		return task
	}

	old := newFencedTask("old", 1, false)
	if result := conn.execute(t.Context(), old); !result.success {
		t.Fatalf("old task failed: %+v", result)
	}
	if result := conn.execute(t.Context(), newFencedTask("new", 2, true)); !result.success {
		t.Fatalf("new task failed: %+v", result)
	}
	if result := conn.execute(t.Context(), old); result.success || result.taskErr.Code != "scheduling_fence_stale" {
		t.Fatalf("cached stale task result = %+v", result)
	}
	if got := gateway.values(); len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("gateway writes = %v, want [false true]", got)
	}
}

func TestSchedulingFenceRequiresDurableLocalStoreBeforeGateway(t *testing.T) {
	gateway := &recordingSchedulingGateway{}
	conn := New(Config{ConnectorID: testConnectorID, InstanceID: testInstanceID})
	conn.gatewayForTask = func() (gateways.Gateway, error) { return gateway, nil }
	task := newTask(t, "fenced-without-store", contracts.ConnectorTaskGatewaySchedulableSet, contracts.ConnectorGatewaySchedulableSetInput{
		AccountID: "account-a", Schedulable: false,
		Fence: &contracts.GatewaySchedulingFence{
			Scope: "auto-switch/plan/plan-a", Version: 1, Sequence: 1,
		},
	})

	result := conn.execute(t.Context(), task)
	if result.localErr == nil || !strings.Contains(result.localErr.Error(), "persistence is not configured") {
		t.Fatalf("fenced task local error = %v", result.localErr)
	}
	if got := gateway.values(); len(got) != 0 {
		t.Fatalf("fenced task without durable store reached gateway: %v", got)
	}
}

func TestSchedulingFenceSerializesSharedDataDirectoryThroughGatewaySideEffect(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	gateway := &blockingSchedulingGateway{started: make(chan struct{}), release: make(chan struct{})}
	newConnector := func() *Connector {
		conn := New(Config{ConnectorID: testConnectorID, InstanceID: testInstanceID, ConfigStore: store})
		conn.gatewayForTask = func() (gateways.Gateway, error) { return gateway, nil }
		return conn
	}
	newFencedTask := func(id string, sequence int64, enabled bool) contracts.ConnectorTask {
		return newTask(t, id, contracts.ConnectorTaskGatewaySchedulableSet, contracts.ConnectorGatewaySchedulableSetInput{
			AccountID: "account-a", Schedulable: enabled,
			Fence: &contracts.GatewaySchedulingFence{
				Scope: "auto-switch/plan/plan-a", Version: 1, Sequence: sequence,
			},
		})
	}

	firstDone := make(chan taskResult, 1)
	go func() { firstDone <- newConnector().execute(t.Context(), newFencedTask("first", 1, false)) }()
	<-gateway.started
	secondDone := make(chan taskResult, 1)
	go func() { secondDone <- newConnector().execute(t.Context(), newFencedTask("second", 2, true)) }()

	select {
	case result := <-secondDone:
		t.Fatalf("newer tuple crossed in-flight older gateway write: %+v", result)
	case <-time.After(100 * time.Millisecond):
		// The shared ledger lock must remain held through the first side effect.
	}
	close(gateway.release)
	if result := <-firstDone; !result.success {
		t.Fatalf("first task failed: %+v", result)
	}
	if result := <-secondDone; !result.success {
		t.Fatalf("second task failed: %+v", result)
	}
	if got := gateway.values(); len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("serialized gateway writes = %v, want [false true]", got)
	}
}

func TestSchedulingFenceCorruptLedgerFailsClosed(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	if err := os.WriteFile(filepath.Join(filepath.Dir(store.path), "scheduling-fences.json"), []byte(`{"version":1,"scopes":`), 0600); err != nil {
		t.Fatal(err)
	}
	gateway := &recordingSchedulingGateway{}
	conn := New(Config{ConnectorID: testConnectorID, InstanceID: testInstanceID, ConfigStore: store})
	conn.gatewayForTask = func() (gateways.Gateway, error) { return gateway, nil }
	task := newTask(t, "corrupt-ledger", contracts.ConnectorTaskGatewaySchedulableSet, contracts.ConnectorGatewaySchedulableSetInput{
		AccountID: "account-a", Schedulable: true,
		Fence: &contracts.GatewaySchedulingFence{
			Scope: "auto-switch/plan/plan-a", Version: 1, Sequence: 1,
		},
	})
	result := conn.execute(t.Context(), task)
	if result.localErr == nil || !strings.Contains(result.localErr.Error(), "decode scheduling fence ledger") {
		t.Fatalf("corrupt ledger error = %v", result.localErr)
	}
	if got := gateway.values(); len(got) != 0 {
		t.Fatalf("corrupt ledger task reached gateway: %v", got)
	}
}

func TestManualSchedulingRemainsCompatibleWithoutFence(t *testing.T) {
	gateway := &recordingSchedulingGateway{}
	conn := New(Config{
		ConnectorID: testConnectorID,
		InstanceID:  testInstanceID,
		ConfigStore: NewLocalConfigStore(t.TempDir()),
	})
	conn.gatewayForTask = func() (gateways.Gateway, error) { return gateway, nil }
	task := newTask(t, "manual", contracts.ConnectorTaskGatewaySchedulableSet, contracts.ConnectorGatewaySchedulableSetInput{
		AccountID: "account-a", Schedulable: true,
	})
	if result := conn.execute(t.Context(), task); !result.success {
		t.Fatalf("manual task failed: %+v", result)
	}
	if got := gateway.values(); len(got) != 1 || !got[0] {
		t.Fatalf("manual gateway writes = %v", got)
	}
}

func TestSchedulingFenceRejectsMissingSequenceBeforeGateway(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	gateway := &recordingSchedulingGateway{}
	conn := New(Config{ConnectorID: testConnectorID, InstanceID: testInstanceID, ConfigStore: store})
	conn.gatewayForTask = func() (gateways.Gateway, error) { return gateway, nil }
	task := newTask(t, "missing-sequence", contracts.ConnectorTaskGatewaySchedulableSet, contracts.ConnectorGatewaySchedulableSetInput{
		AccountID: "account-a", Schedulable: true,
		Fence: &contracts.GatewaySchedulingFence{Scope: "auto-switch/plan/plan-a", Version: 1},
	})
	result := conn.execute(t.Context(), task)
	if result.success || result.taskErr.Code != "invalid_task_input" {
		t.Fatalf("missing sequence result = %+v", result)
	}
	if got := gateway.values(); len(got) != 0 {
		t.Fatalf("invalid fence reached gateway: %v", got)
	}
}

func TestSchedulingBarrierAdvancesDurableLedgerWithoutCallingGateway(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	gateway := &recordingSchedulingGateway{}
	gatewayLoads := 0
	conn := New(Config{ConnectorID: testConnectorID, InstanceID: testInstanceID, ConfigStore: store})
	conn.gatewayForTask = func() (gateways.Gateway, error) {
		gatewayLoads++
		return gateway, nil
	}
	barrier := newTask(t, "barrier", contracts.ConnectorTaskGatewaySchedulingBarrier, contracts.ConnectorGatewaySchedulingBarrierInput{
		Fence: contracts.GatewaySchedulingFence{
			Scope: "auto-switch/plan/plan-a", Version: 2, Sequence: 1,
		},
	})
	if result := conn.execute(t.Context(), barrier); !result.success {
		t.Fatalf("barrier failed: %+v", result)
	}
	if gatewayLoads != 0 || len(gateway.values()) != 0 {
		t.Fatalf("barrier reached gateway: loads=%d writes=%v", gatewayLoads, gateway.values())
	}

	stale := newTask(t, "stale-after-barrier", contracts.ConnectorTaskGatewaySchedulableSet, contracts.ConnectorGatewaySchedulableSetInput{
		AccountID: "account-a", Schedulable: false,
		Fence: &contracts.GatewaySchedulingFence{
			Scope: "auto-switch/plan/plan-a", Version: 1, Sequence: 99,
		},
	})
	if result := conn.execute(t.Context(), stale); result.success || result.taskErr.Code != "scheduling_fence_stale" {
		t.Fatalf("stale task result = %+v", result)
	}
	if gatewayLoads != 0 || len(gateway.values()) != 0 {
		t.Fatalf("stale task after barrier reached gateway: loads=%d writes=%v", gatewayLoads, gateway.values())
	}

	newer := newTask(t, "new-after-barrier", contracts.ConnectorTaskGatewaySchedulableSet, contracts.ConnectorGatewaySchedulableSetInput{
		AccountID: "account-a", Schedulable: true,
		Fence: &contracts.GatewaySchedulingFence{
			Scope: "auto-switch/plan/plan-a", Version: 2, Sequence: 2,
		},
	})
	if result := conn.execute(t.Context(), newer); !result.success {
		t.Fatalf("newer task failed: %+v", result)
	}
	if gatewayLoads != 1 {
		t.Fatalf("newer task gateway loads=%d, want 1", gatewayLoads)
	}
	if got := gateway.values(); len(got) != 1 || !got[0] {
		t.Fatalf("newer task gateway writes=%v, want [true]", got)
	}
}

func TestSchedulingBarrierRequiresValidFence(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	conn := New(Config{ConnectorID: testConnectorID, InstanceID: testInstanceID, ConfigStore: store})
	gatewayLoads := 0
	conn.gatewayForTask = func() (gateways.Gateway, error) {
		gatewayLoads++
		return &recordingSchedulingGateway{}, nil
	}
	for name, fence := range map[string]contracts.GatewaySchedulingFence{
		"missing scope":    {Version: 1, Sequence: 1},
		"missing version":  {Scope: "auto-switch/plan/plan-a", Sequence: 1},
		"missing sequence": {Scope: "auto-switch/plan/plan-a", Version: 1},
	} {
		t.Run(name, func(t *testing.T) {
			task := newTask(t, name, contracts.ConnectorTaskGatewaySchedulingBarrier, contracts.ConnectorGatewaySchedulingBarrierInput{Fence: fence})
			result := conn.execute(t.Context(), task)
			if result.success || result.taskErr.Code != "invalid_task_input" {
				t.Fatalf("invalid barrier result = %+v", result)
			}
		})
	}
	if gatewayLoads != 0 {
		t.Fatalf("invalid barriers loaded gateway %d times", gatewayLoads)
	}
}
