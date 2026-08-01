package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

type memoryHybridRoutingFixture struct {
	store      *MemoryStore
	instance   contracts.Instance
	connector  contracts.Connector
	allocation contracts.HybridAllocation
	execution  contracts.HybridRoutingExecution
}

func newMemoryHybridRoutingFixture(t *testing.T) memoryHybridRoutingFixture {
	t.Helper()
	ctx := context.Background()
	st, _, instance, connector := newMemoryConnectorTaskFixture(t, "connector-hybrid-routing")
	allocation, err := st.UpsertHybridAllocation(ctx, contracts.HybridAllocation{
		UserID: instance.UserID, InstanceID: instance.ID,
		DefaultRule: contracts.HybridAllocationRule{OwnerPercent: 80, EconomyPercent: 20, StablePercent: 0},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := st.CreateHybridRoutingExecution(ctx, contracts.HybridRoutingExecution{
		UserID: instance.UserID, InstanceID: instance.ID, AllocationVersion: allocation.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = execution
	claimed, ok, err := st.ClaimHybridRoutingExecution(ctx, "hybrid-worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	return memoryHybridRoutingFixture{store: st, instance: instance, connector: connector, allocation: allocation, execution: claimed}
}

func (fixture memoryHybridRoutingFixture) createTrafficTask(t *testing.T, suffix string) contracts.ConnectorTask {
	t.Helper()
	fence := contracts.GatewaySchedulingFence{
		Scope:   contracts.HybridRoutingFenceScope(fixture.instance.ID, "owner-account"),
		Version: fixture.execution.Generation, Sequence: 1,
	}
	raw, err := json.Marshal(contracts.ConnectorGatewayTrafficShareSetInput{AccountID: "owner-account", Weight: 80, Fence: fence})
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.CreateConnectorTask(context.Background(), contracts.ConnectorTask{
		UserID: fixture.instance.UserID, InstanceID: fixture.instance.ID, ConnectorID: fixture.connector.ID,
		ExecutionScope: contracts.HybridRoutingExecutionScope, ExecutionID: fixture.execution.ID,
		ExecutionGeneration: fixture.execution.Generation, Type: contracts.ConnectorTaskGatewayTrafficShareSet,
		Input: raw, IdempotencyKey: "hybrid-routing-" + suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestMemoryHybridRoutingExecutionStateMachine(t *testing.T) {
	fixture := newMemoryHybridRoutingFixture(t)
	plan := contracts.HybridRoutingExecutionPlan{
		ID: fixture.execution.ID, WorkerID: fixture.execution.LeaseOwner, ExpectedVersion: fixture.execution.Version,
		Target:    map[contracts.ResourceClass]int{contracts.ResourceClassOwner: 80, contracts.ResourceClassEconomy: 20, contracts.ResourceClassStable: 0},
		Effective: map[contracts.ResourceClass]int{contracts.ResourceClassOwner: 80, contracts.ResourceClassEconomy: 20, contracts.ResourceClassStable: 0},
		DesiredWeights: []contracts.HybridAccountWeight{
			{AccountID: "economy", Class: contracts.ResourceClassEconomy, Weight: 10, Schedulable: true},
			{AccountID: "owner-account", Class: contracts.ResourceClassOwner, Weight: 70, Schedulable: true},
			{AccountID: "stable", Class: contracts.ResourceClassStable, Weight: 0, Schedulable: false},
		},
	}
	planned, err := fixture.store.PlanHybridRoutingExecution(context.Background(), plan)
	if err != nil || len(planned.DesiredWeights) != 3 {
		t.Fatalf("planned=%+v err=%v", planned, err)
	}
	completed, err := fixture.store.CompleteHybridRoutingExecution(context.Background(), contracts.HybridRoutingExecutionCompletion{
		ID: planned.ID, WorkerID: planned.LeaseOwner, ExpectedVersion: planned.Version, Succeeded: true,
		ReadBackWeights: append([]contracts.HybridAccountWeight(nil), plan.DesiredWeights...),
	})
	if err != nil || completed.Status != contracts.HybridRoutingExecutionSucceeded || completed.Actual[contracts.ResourceClassStable] != 0 {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func TestMemoryHybridRoutingExecutionRejectsMismatchedWriteSetAndActual(t *testing.T) {
	t.Run("plan", func(t *testing.T) {
		fixture := newMemoryHybridRoutingFixture(t)
		_, err := fixture.store.PlanHybridRoutingExecution(context.Background(), contracts.HybridRoutingExecutionPlan{
			ID: fixture.execution.ID, WorkerID: fixture.execution.LeaseOwner, ExpectedVersion: fixture.execution.Version,
			Target:    map[contracts.ResourceClass]int{contracts.ResourceClassOwner: 80, contracts.ResourceClassEconomy: 20, contracts.ResourceClassStable: 0},
			Effective: map[contracts.ResourceClass]int{contracts.ResourceClassOwner: 80, contracts.ResourceClassEconomy: 20, contracts.ResourceClassStable: 0},
			DesiredWeights: []contracts.HybridAccountWeight{
				{AccountID: "economy", Class: contracts.ResourceClassEconomy, Weight: 0, Schedulable: true},
				{AccountID: "owner-account", Class: contracts.ResourceClassOwner, Weight: 90, Schedulable: true},
				{AccountID: "stable", Class: contracts.ResourceClassStable, Weight: 0, Schedulable: false},
			},
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("actual", func(t *testing.T) {
		fixture := newMemoryHybridRoutingFixture(t)
		planned, err := fixture.store.PlanHybridRoutingExecution(context.Background(), contracts.HybridRoutingExecutionPlan{
			ID: fixture.execution.ID, WorkerID: fixture.execution.LeaseOwner, ExpectedVersion: fixture.execution.Version,
			Target:    map[contracts.ResourceClass]int{contracts.ResourceClassOwner: 80, contracts.ResourceClassEconomy: 20, contracts.ResourceClassStable: 0},
			Effective: map[contracts.ResourceClass]int{contracts.ResourceClassOwner: 80, contracts.ResourceClassEconomy: 20, contracts.ResourceClassStable: 0},
			DesiredWeights: []contracts.HybridAccountWeight{
				{AccountID: "economy", Class: contracts.ResourceClassEconomy, Weight: 10, Schedulable: true},
				{AccountID: "owner-account", Class: contracts.ResourceClassOwner, Weight: 70, Schedulable: true},
				{AccountID: "stable", Class: contracts.ResourceClassStable, Weight: 0, Schedulable: false},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = fixture.store.CompleteHybridRoutingExecution(context.Background(), contracts.HybridRoutingExecutionCompletion{
			ID: planned.ID, WorkerID: planned.LeaseOwner, ExpectedVersion: planned.Version, Succeeded: true,
			ReadBackWeights: []contracts.HybridAccountWeight{
				{AccountID: "economy", Class: contracts.ResourceClassEconomy, Weight: 10, Schedulable: true},
				{AccountID: "owner-account", Class: contracts.ResourceClassOwner, Weight: 70, Schedulable: false},
				{AccountID: "stable", Class: contracts.ResourceClassStable, Weight: 0, Schedulable: false},
			},
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestMemoryHybridConnectorPermitLifecycle(t *testing.T) {
	fixture := newMemoryHybridRoutingFixture(t)
	task := fixture.createTrafficTask(t, "permit")
	leased, err := fixture.store.LeaseConnectorTasks(context.Background(), contracts.ConnectorTaskLeaseRequest{
		ConnectorID: fixture.connector.ID, LeaseSeconds: 60,
	})
	if err != nil || len(leased) != 1 || leased[0].ID != task.ID {
		t.Fatalf("leased=%+v err=%v", leased, err)
	}
	executing, err := fixture.store.BeginConnectorTaskExecution(context.Background(), task.ID, contracts.ConnectorTaskExecutionRequest{
		ConnectorID: fixture.connector.ID, LeaseNonce: leased[0].LeaseNonce,
	})
	if err != nil || executing.Status != contracts.ConnectorTaskExecuting {
		t.Fatalf("executing=%+v err=%v", executing, err)
	}
	input := contracts.ConnectorGatewayTrafficShareSetInput{}
	if err := json.Unmarshal(task.Input, &input); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(contracts.ConnectorGatewayTrafficShareSetResult{AccountID: input.AccountID, Weight: input.Weight, Fence: input.Fence})
	completed, err := fixture.store.CompleteConnectorTask(context.Background(), task.ID, contracts.ConnectorTaskCompleteRequest{
		ConnectorID: fixture.connector.ID, LeaseNonce: leased[0].LeaseNonce, Success: true, Result: result,
	})
	if err != nil || completed.Status != contracts.ConnectorTaskSucceeded {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func TestMemoryHybridGenerationCannotAdvanceDuringConnectorExecution(t *testing.T) {
	fixture := newMemoryHybridRoutingFixture(t)
	task := fixture.createTrafficTask(t, "freeze")
	leased, err := fixture.store.LeaseConnectorTasks(context.Background(), contracts.ConnectorTaskLeaseRequest{ConnectorID: fixture.connector.ID})
	if err != nil || len(leased) != 1 {
		t.Fatalf("leased=%+v err=%v", leased, err)
	}
	if _, err := fixture.store.BeginConnectorTaskExecution(context.Background(), task.ID, contracts.ConnectorTaskExecutionRequest{
		ConnectorID: fixture.connector.ID, LeaseNonce: leased[0].LeaseNonce,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.store.CreateHybridRoutingExecution(context.Background(), contracts.HybridRoutingExecution{
		UserID: fixture.instance.UserID, InstanceID: fixture.instance.ID, AllocationVersion: fixture.allocation.Version,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("generation advance error=%v", err)
	}
}

func TestMemoryHybridFenceRejectsMixedOrMissingIdentity(t *testing.T) {
	fixture := newMemoryHybridRoutingFixture(t)
	task := fixture.createTrafficTask(t, "base")
	for name, mutate := range map[string]func(*contracts.ConnectorTask){
		"missing": func(task *contracts.ConnectorTask) { task.ExecutionID = "" },
		"mixed": func(task *contracts.ConnectorTask) {
			task.PlanID, task.SchedulingGeneration = "plan", task.ExecutionGeneration
		},
		"generation": func(task *contracts.ConnectorTask) { task.ExecutionGeneration++ },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := task
			candidate.ID, candidate.IdempotencyKey = "", "hybrid-invalid-"+name
			mutate(&candidate)
			if _, err := fixture.store.CreateConnectorTask(context.Background(), candidate); !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
