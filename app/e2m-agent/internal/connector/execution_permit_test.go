package connector

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

type executionPermitGateway struct {
	mu     sync.Mutex
	calls  int
	err    error
	onCall func()
}

func (g *executionPermitGateway) Health(context.Context) (contracts.ConnectorGatewayHealthResult, error) {
	return contracts.ConnectorGatewayHealthResult{}, errors.New("unexpected health call")
}
func (g *executionPermitGateway) ListAccounts(context.Context) ([]contracts.GatewayAccount, error) {
	return nil, errors.New("unexpected list call")
}
func (g *executionPermitGateway) ProbeQuality(context.Context, contracts.ConnectorGatewayQualityProbeInput) (contracts.ConnectorGatewayQualityProbeResult, error) {
	return contracts.ConnectorGatewayQualityProbeResult{}, errors.New("unexpected probe call")
}
func (g *executionPermitGateway) SetSchedulable(context.Context, string, bool) error {
	g.mu.Lock()
	g.calls++
	onCall := g.onCall
	g.mu.Unlock()
	if onCall != nil {
		onCall()
	}
	return g.err
}
func (g *executionPermitGateway) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

type executionPermitCore struct {
	server          *httptest.Server
	mu              sync.Mutex
	task            contracts.ConnectorTask
	executeStatus   int
	executeResponse *contracts.ConnectorTask
	events          []string
	completions     int
	permitChecks    func()
}

func newExecutionPermitCore(t *testing.T, task contracts.ConnectorTask) *executionPermitCore {
	t.Helper()
	core := &executionPermitCore{task: task, executeStatus: http.StatusOK}
	core.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testConnectorToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/connectors/tasks/lease":
			core.record("lease")
			_ = json.NewEncoder(w).Encode(contracts.ConnectorTaskLeaseResponse{
				ProtocolVersion: contracts.ConnectorProtocolVersion, Tasks: []contracts.ConnectorTask{task},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/connectors/tasks/"+task.ID+"/execute":
			var request contracts.ConnectorTaskExecutionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.ConnectorID != task.ConnectorID || request.LeaseNonce != task.LeaseNonce {
				t.Errorf("execute request = %+v err=%v", request, err)
			}
			if core.permitChecks != nil {
				core.permitChecks()
			}
			core.record("execute")
			if core.executeStatus != http.StatusOK {
				w.WriteHeader(core.executeStatus)
				_, _ = w.Write([]byte(`{"code":"task_execution_conflict"}`))
				return
			}
			response := task
			response.Status = contracts.ConnectorTaskExecuting
			if core.executeResponse != nil {
				response = *core.executeResponse
			}
			_ = json.NewEncoder(w).Encode(response)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/connectors/tasks/"+task.ID+"/complete":
			core.record("complete")
			core.mu.Lock()
			core.completions++
			core.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(core.server.Close)
	return core
}

func (c *executionPermitCore) record(event string) {
	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()
}
func (c *executionPermitCore) snapshot() ([]string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.events...), c.completions
}

func executionPermitTask(t *testing.T, taskType contracts.ConnectorTaskType) contracts.ConnectorTask {
	t.Helper()
	fence := contracts.GatewaySchedulingFence{Scope: "auto-switch/plan/plan-permit", Version: 7, Sequence: 1}
	var input any = contracts.ConnectorGatewaySchedulableSetInput{AccountID: "account-permit", Schedulable: false, Fence: &fence}
	if taskType == contracts.ConnectorTaskGatewaySchedulingBarrier {
		input = contracts.ConnectorGatewaySchedulingBarrierInput{Fence: fence}
	}
	task := newTask(t, "permit-task", taskType, input)
	task.UserID = 1
	task.PlanID = "plan-permit"
	task.SchedulingGeneration = 7
	return task
}

func executionPermitConnector(t *testing.T, core *executionPermitCore, gateway gateways.Gateway) *Connector {
	t.Helper()
	store := NewLocalConfigStore(t.TempDir())
	conn := New(Config{
		CoreURL: core.server.URL, Token: testConnectorToken, ConnectorID: testConnectorID,
		InstanceID: testInstanceID, ConfigStore: store, Version: "0.1.0-test",
	})
	conn.gatewayForTask = func() (gateways.Gateway, error) { return gateway, nil }
	return conn
}

func TestExecutionPermitPrecedesGatewayAndCompletion(t *testing.T) {
	task := executionPermitTask(t, contracts.ConnectorTaskGatewaySchedulableSet)
	core := newExecutionPermitCore(t, task)
	gateway := &executionPermitGateway{onCall: func() { core.record("gateway") }}
	conn := executionPermitConnector(t, core, gateway)
	core.permitChecks = func() {
		if conn.schedulingFence.mu.TryLock() {
			conn.schedulingFence.mu.Unlock()
			t.Error("scheduling fence lock was not held before permit")
		}
		if conn.writeReceipts.mu.TryLock() {
			conn.writeReceipts.mu.Unlock()
			t.Error("write receipt lock was not held before permit")
		}
	}
	if err := conn.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	events, completions := core.snapshot()
	if gateway.count() != 1 || completions != 1 || !reflect.DeepEqual(events, []string{"lease", "execute", "gateway", "complete"}) {
		t.Fatalf("events=%v gateway=%d completions=%d", events, gateway.count(), completions)
	}
}

func TestExecutionPermitConflictHasNoGatewayOrCompletion(t *testing.T) {
	task := executionPermitTask(t, contracts.ConnectorTaskGatewaySchedulableSet)
	core := newExecutionPermitCore(t, task)
	core.executeStatus = http.StatusConflict
	gateway := &executionPermitGateway{}
	err := executionPermitConnector(t, core, gateway).PollOnce(t.Context())
	_, completions := core.snapshot()
	if err == nil || gateway.count() != 0 || completions != 0 {
		t.Fatalf("err=%v gateway=%d completions=%d", err, gateway.count(), completions)
	}
}

func TestExecutionPermitIdentityMismatchHasNoGatewayOrCompletion(t *testing.T) {
	mutations := map[string]func(*contracts.ConnectorTask){
		"user":        func(task *contracts.ConnectorTask) { task.UserID++ },
		"instance":    func(task *contracts.ConnectorTask) { task.InstanceID += "-other" },
		"connector":   func(task *contracts.ConnectorTask) { task.ConnectorID += "-other" },
		"type":        func(task *contracts.ConnectorTask) { task.Type = contracts.ConnectorTaskGatewaySwitch },
		"schema":      func(task *contracts.ConnectorTask) { task.SchemaVersion++ },
		"input":       func(task *contracts.ConnectorTask) { task.Input = append(json.RawMessage(nil), []byte(`{}`)...) },
		"idempotency": func(task *contracts.ConnectorTask) { task.IdempotencyKey += "-other" },
		"plan":        func(task *contracts.ConnectorTask) { task.PlanID += "-other" },
		"generation":  func(task *contracts.ConnectorTask) { task.SchedulingGeneration++ },
		"lease nonce": func(task *contracts.ConnectorTask) { task.LeaseNonce += "-other" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			task := executionPermitTask(t, contracts.ConnectorTaskGatewaySchedulableSet)
			core := newExecutionPermitCore(t, task)
			mismatch := task
			mismatch.Status = contracts.ConnectorTaskExecuting
			mutate(&mismatch)
			core.executeResponse = &mismatch
			gateway := &executionPermitGateway{}
			err := executionPermitConnector(t, core, gateway).PollOnce(t.Context())
			_, completions := core.snapshot()
			if err == nil || gateway.count() != 0 || completions != 0 {
				t.Fatalf("err=%v gateway=%d completions=%d", err, gateway.count(), completions)
			}
		})
	}
}

func TestFencedMutationCannotBypassPermitWithMissingMetadata(t *testing.T) {
	for _, test := range []struct {
		name       string
		planID     string
		generation int64
	}{
		{name: "both missing"},
		{name: "plan only", planID: "plan-permit"},
		{name: "generation only", generation: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			task := executionPermitTask(t, contracts.ConnectorTaskGatewaySchedulableSet)
			task.PlanID = test.planID
			task.SchedulingGeneration = test.generation
			core := newExecutionPermitCore(t, task)
			gateway := &executionPermitGateway{}
			err := executionPermitConnector(t, core, gateway).PollOnce(t.Context())
			events, completions := core.snapshot()
			if err == nil || gateway.count() != 0 || completions != 0 || !reflect.DeepEqual(events, []string{"lease"}) {
				t.Fatalf("err=%v events=%v gateway=%d completions=%d", err, events, gateway.count(), completions)
			}
		})
	}
}

func TestExecutionPermitUncertainGatewayOutcomeIsNotCompleted(t *testing.T) {
	task := executionPermitTask(t, contracts.ConnectorTaskGatewaySchedulableSet)
	core := newExecutionPermitCore(t, task)
	gateway := &executionPermitGateway{err: &gateways.Error{Code: "gateway_timeout", Message: "timeout", Retryable: true}}
	err := executionPermitConnector(t, core, gateway).PollOnce(t.Context())
	_, completions := core.snapshot()
	if err == nil || !strings.Contains(err.Error(), "outcome is uncertain") || gateway.count() != 1 || completions != 0 {
		t.Fatalf("err=%v gateway=%d completions=%d", err, gateway.count(), completions)
	}
}

func TestExecutionPermitCachedReceiptStillBeginsBeforeCompletion(t *testing.T) {
	task := executionPermitTask(t, contracts.ConnectorTaskGatewaySchedulableSet)
	core := newExecutionPermitCore(t, task)
	gateway := &executionPermitGateway{}
	conn := executionPermitConnector(t, core, gateway)
	leaseResult := gatewayResult(contracts.ConnectorGatewayMutationResult{}, nil)
	_, found, lease, err := conn.writeReceipts.begin(task.IdempotencyKey, writeTaskPayloadSignature(task))
	if err != nil || found || lease == nil {
		t.Fatalf("seed receipt: found=%v lease=%v err=%v", found, lease != nil, err)
	}
	if err := lease.commit(leaseResult); err != nil {
		t.Fatal(err)
	}
	if err := lease.release(); err != nil {
		t.Fatal(err)
	}
	if err := conn.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	events, completions := core.snapshot()
	if gateway.count() != 0 || completions != 1 || !reflect.DeepEqual(events, []string{"lease", "execute", "complete"}) {
		t.Fatalf("events=%v gateway=%d completions=%d", events, gateway.count(), completions)
	}
}

func TestExecutionPermitBarrierBeginsBeforeReceiptCommitAndCompletion(t *testing.T) {
	task := executionPermitTask(t, contracts.ConnectorTaskGatewaySchedulingBarrier)
	core := newExecutionPermitCore(t, task)
	conn := executionPermitConnector(t, core, &executionPermitGateway{})
	core.permitChecks = func() {
		if conn.schedulingFence.mu.TryLock() {
			conn.schedulingFence.mu.Unlock()
			t.Error("scheduling fence lock was not held before barrier permit")
		}
		if conn.writeReceipts.mu.TryLock() {
			conn.writeReceipts.mu.Unlock()
			t.Error("write receipt lock was not held before barrier permit")
		}
		state, err := conn.writeReceipts.loadLocked()
		if err != nil || len(state.Entries) != 0 {
			t.Errorf("barrier receipt committed before permit: entries=%v err=%v", state.Entries, err)
		}
	}
	if err := conn.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, found, lease, err := conn.writeReceipts.begin(task.IdempotencyKey, writeTaskPayloadSignature(task))
	if err != nil || !found || lease == nil {
		t.Fatalf("barrier receipt missing: found=%v lease=%v err=%v", found, lease != nil, err)
	}
	_ = lease.release()
	events, completions := core.snapshot()
	if completions != 1 || !reflect.DeepEqual(events, []string{"lease", "execute", "complete"}) {
		t.Fatalf("events=%v completions=%d", events, completions)
	}
}
