package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryConnectorTaskExecutionPermitLifecycle(t *testing.T) {
	t.Run("exact permit retry is idempotent and executing ignores lease deadline", func(t *testing.T) {
		fixture := newMemoryConnectorPlanFenceFixture(t)
		task := fixture.createSchedulingTask(t, "permit-lifecycle")
		leased := leaseMemoryFencedTask(t, fixture, task.ID)

		request := contracts.ConnectorTaskExecutionRequest{ConnectorID: fixture.connector.ID, LeaseNonce: leased.LeaseNonce}
		first, err := fixture.store.BeginConnectorTaskExecution(fixture.ctx, task.ID, request)
		if err != nil || first.Status != contracts.ConnectorTaskExecuting {
			t.Fatalf("begin execution = %+v err=%v", first, err)
		}
		second, err := fixture.store.BeginConnectorTaskExecution(fixture.ctx, task.ID, request)
		if err != nil || second.Status != contracts.ConnectorTaskExecuting || second.LeaseNonce != first.LeaseNonce || second.Attempts != first.Attempts {
			t.Fatalf("idempotent begin = %+v err=%v", second, err)
		}
		fixture.store.now = func() time.Time { return leased.LeaseUntil.Add(time.Hour) }
		completed, err := fixture.store.CompleteConnectorTask(fixture.ctx, task.ID, contracts.ConnectorTaskCompleteRequest{
			ConnectorID: fixture.connector.ID, LeaseNonce: leased.LeaseNonce, Success: true,
			Result: json.RawMessage(`{"remote_id":"account-plan-fence"}`),
		})
		if err != nil || completed.Status != contracts.ConnectorTaskSucceeded {
			t.Fatalf("complete after audit lease deadline = %+v err=%v", completed, err)
		}
	})

	t.Run("expired leased permit is rejected", func(t *testing.T) {
		fixture := newMemoryConnectorPlanFenceFixture(t)
		task := fixture.createSchedulingTask(t, "expired-permit")
		leased := leaseMemoryFencedTask(t, fixture, task.ID)
		fixture.store.now = func() time.Time { return leased.LeaseUntil.Add(time.Nanosecond) }
		_, err := fixture.store.BeginConnectorTaskExecution(fixture.ctx, task.ID, contracts.ConnectorTaskExecutionRequest{
			ConnectorID: fixture.connector.ID, LeaseNonce: leased.LeaseNonce,
		})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("expired begin error=%v, want ErrConflict", err)
		}
	})

	t.Run("fenced completion requires executing and retryable cannot release permit", func(t *testing.T) {
		fixture := newMemoryConnectorPlanFenceFixture(t)
		task := fixture.createSchedulingTask(t, "completion-state")
		leased := leaseMemoryFencedTask(t, fixture, task.ID)
		completion := contracts.ConnectorTaskCompleteRequest{
			ConnectorID: fixture.connector.ID, LeaseNonce: leased.LeaseNonce, Success: true,
			Result: json.RawMessage(`{"remote_id":"account-plan-fence"}`),
		}
		if _, err := fixture.store.CompleteConnectorTask(fixture.ctx, task.ID, completion); !errors.Is(err, ErrConflict) {
			t.Fatalf("leased fenced completion error=%v, want ErrConflict", err)
		}
		if _, err := fixture.store.BeginConnectorTaskExecution(fixture.ctx, task.ID, contracts.ConnectorTaskExecutionRequest{
			ConnectorID: fixture.connector.ID, LeaseNonce: leased.LeaseNonce,
		}); err != nil {
			t.Fatal(err)
		}
		completion.Success = false
		completion.Result = nil
		completion.Error = contracts.ConnectorTaskError{Code: "gateway_timeout", Retryable: true}
		if _, err := fixture.store.CompleteConnectorTask(fixture.ctx, task.ID, completion); !errors.Is(err, ErrConflict) {
			t.Fatalf("retryable executing completion error=%v, want ErrConflict", err)
		}
		persisted, err := fixture.store.GetConnectorTask(fixture.ctx, task.ID)
		if err != nil || persisted.Status != contracts.ConnectorTaskExecuting || persisted.LeaseNonce != leased.LeaseNonce {
			t.Fatalf("rejected retry released permit: %+v err=%v", persisted, err)
		}
	})

	t.Run("malformed success receipt cannot release permit", func(t *testing.T) {
		fixture := newMemoryConnectorPlanFenceFixture(t)
		task := fixture.createSchedulingTask(t, "invalid-success-receipt")
		leased := leaseMemoryFencedTask(t, fixture, task.ID)
		if _, err := fixture.store.BeginConnectorTaskExecution(fixture.ctx, task.ID, contracts.ConnectorTaskExecutionRequest{
			ConnectorID: fixture.connector.ID, LeaseNonce: leased.LeaseNonce,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.CompleteConnectorTask(fixture.ctx, task.ID, contracts.ConnectorTaskCompleteRequest{
			ConnectorID: fixture.connector.ID, LeaseNonce: leased.LeaseNonce, Success: true,
			Result: json.RawMessage(`{"unexpected":"receipt"}`),
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("invalid success receipt error=%v, want ErrConflict", err)
		}
		persisted, err := fixture.store.GetConnectorTask(fixture.ctx, task.ID)
		if err != nil || persisted.Status != contracts.ConnectorTaskExecuting || persisted.LeaseNonce != leased.LeaseNonce || len(persisted.Result) != 0 {
			t.Fatalf("invalid receipt released permit: %+v err=%v", persisted, err)
		}
	})
}

func TestMemoryConnectorExecutingTaskRetainsIdempotencyAfterExpiry(t *testing.T) {
	fixture := newMemoryConnectorPlanFenceFixture(t)
	task := fixture.createSchedulingTask(t, "executing-idempotency")
	leased := leaseMemoryFencedTask(t, fixture, task.ID)
	executing, err := fixture.store.BeginConnectorTaskExecution(fixture.ctx, task.ID, contracts.ConnectorTaskExecutionRequest{
		ConnectorID: fixture.connector.ID, LeaseNonce: leased.LeaseNonce,
	})
	if err != nil || executing.Status != contracts.ConnectorTaskExecuting {
		t.Fatalf("begin execution = %+v err=%v", executing, err)
	}
	fixture.store.now = func() time.Time { return task.ExpiresAt.Add(time.Hour) }
	_, err = fixture.store.CreateConnectorTask(fixture.ctx, contracts.ConnectorTask{
		UserID: task.UserID, InstanceID: task.InstanceID, ConnectorID: task.ConnectorID,
		PlanID: task.PlanID, SchedulingGeneration: task.SchedulingGeneration,
		Type: task.Type, Input: append(json.RawMessage(nil), task.Input...), IdempotencyKey: task.IdempotencyKey,
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expired executing duplicate error=%v, want ErrDuplicate", err)
	}
	if got := len(fixture.store.connectorTasks); got != 1 {
		t.Fatalf("connector tasks=%d, want one durable executing intent", got)
	}
	persisted, err := fixture.store.GetConnectorTask(fixture.ctx, task.ID)
	if err != nil || persisted.Status != contracts.ConnectorTaskExecuting {
		t.Fatalf("executing task changed after duplicate: %+v err=%v", persisted, err)
	}
}

func TestMemoryConnectorExecutionPermitSerializesGenerationBump(t *testing.T) {
	t.Run("execution commits before generation bump", func(t *testing.T) {
		fixture, task, leased, plan := newMemoryConnectorExecutionGenerationRace(t, "execution-first")
		_, beginErr := fixture.store.BeginConnectorTaskExecution(fixture.ctx, task.ID, contracts.ConnectorTaskExecutionRequest{
			ConnectorID: fixture.connector.ID, LeaseNonce: leased.LeaseNonce,
		})
		_, bumpErr := fixture.store.UpdateRoutePlan(fixture.ctx, plan)

		assertMemoryConnectorExecutionGenerationOutcome(t, "execution first", fixture, task.ID, beginErr, bumpErr)
	})

	t.Run("generation bump commits before execution", func(t *testing.T) {
		fixture, task, leased, plan := newMemoryConnectorExecutionGenerationRace(t, "generation-first")
		_, bumpErr := fixture.store.UpdateRoutePlan(fixture.ctx, plan)
		_, beginErr := fixture.store.BeginConnectorTaskExecution(fixture.ctx, task.ID, contracts.ConnectorTaskExecutionRequest{
			ConnectorID: fixture.connector.ID, LeaseNonce: leased.LeaseNonce,
		})

		assertMemoryConnectorExecutionGenerationOutcome(t, "generation first", fixture, task.ID, beginErr, bumpErr)
	})

	t.Run("synchronized contenders have exactly one winner", func(t *testing.T) {
		const rounds = 100
		for round := 0; round < rounds; round++ {
			fixture, task, leased, plan := newMemoryConnectorExecutionGenerationRace(t, fmt.Sprintf("concurrent-%03d", round))
			start := make(chan struct{})
			beginResult := make(chan error, 1)
			bumpResult := make(chan error, 1)

			go func() {
				<-start
				_, err := fixture.store.BeginConnectorTaskExecution(fixture.ctx, task.ID, contracts.ConnectorTaskExecutionRequest{
					ConnectorID: fixture.connector.ID, LeaseNonce: leased.LeaseNonce,
				})
				beginResult <- err
			}()
			go func() {
				<-start
				_, err := fixture.store.UpdateRoutePlan(fixture.ctx, plan)
				bumpResult <- err
			}()

			close(start)
			beginErr, bumpErr := <-beginResult, <-bumpResult
			assertMemoryConnectorExecutionGenerationOutcome(
				t, fmt.Sprintf("synchronized round %d", round), fixture, task.ID, beginErr, bumpErr,
			)
		}
	})
}

func newMemoryConnectorExecutionGenerationRace(t *testing.T, suffix string) (*memoryConnectorPlanFenceFixture, contracts.ConnectorTask, contracts.ConnectorTask, contracts.RoutePlan) {
	t.Helper()
	fixture := newMemoryConnectorPlanFenceFixture(t)
	task := fixture.createSchedulingTask(t, "race-"+suffix)
	leased := leaseMemoryFencedTask(t, fixture, task.ID)
	plan, err := fixture.store.GetRoutePlan(fixture.ctx, fixture.plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan.MaxChannels++
	return fixture, task, leased, plan
}

func assertMemoryConnectorExecutionGenerationOutcome(t *testing.T, name string, fixture *memoryConnectorPlanFenceFixture, taskID string, beginErr, bumpErr error) {
	t.Helper()
	beginWon, bumpWon := beginErr == nil, bumpErr == nil
	if beginWon == bumpWon {
		t.Fatalf("%s: winners begin=%v bump=%v, want exactly one nil error", name, beginErr, bumpErr)
	}
	if beginWon && !errors.Is(bumpErr, ErrConflict) {
		t.Fatalf("%s: execution won but generation bump error=%v, want ErrConflict", name, bumpErr)
	}
	if bumpWon && !errors.Is(beginErr, ErrConflict) {
		t.Fatalf("%s: generation bump won but execution error=%v, want ErrConflict", name, beginErr)
	}

	persistedTask, err := fixture.store.GetConnectorTask(fixture.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	persistedPlan, err := fixture.store.GetRoutePlan(fixture.ctx, fixture.plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if beginWon {
		if persistedTask.Status != contracts.ConnectorTaskExecuting ||
			persistedPlan.SchedulingGeneration != fixture.plan.SchedulingGeneration {
			t.Fatalf("%s: execution winner state task=%s generation=%d, want executing/%d",
				name, persistedTask.Status, persistedPlan.SchedulingGeneration, fixture.plan.SchedulingGeneration)
		}
		return
	}
	if persistedTask.Status != contracts.ConnectorTaskFailed ||
		persistedTask.Error.Code != connectorTaskSupersededErrorCode ||
		persistedPlan.SchedulingGeneration != fixture.plan.SchedulingGeneration+1 {
		t.Fatalf("%s: generation winner state task=%s error=%s generation=%d, want failed/%s/%d",
			name, persistedTask.Status, persistedTask.Error.Code, persistedPlan.SchedulingGeneration,
			connectorTaskSupersededErrorCode, fixture.plan.SchedulingGeneration+1)
	}
}

func leaseMemoryFencedTask(t *testing.T, fixture *memoryConnectorPlanFenceFixture, taskID string) contracts.ConnectorTask {
	t.Helper()
	leased, err := fixture.store.LeaseConnectorTasks(context.Background(), contracts.ConnectorTaskLeaseRequest{
		ConnectorID: fixture.connector.ID, LeaseSeconds: 60,
	})
	if err != nil || len(leased) != 1 || leased[0].ID != taskID {
		t.Fatalf("lease task=%s tasks=%+v err=%v", taskID, leased, err)
	}
	return leased[0]
}
