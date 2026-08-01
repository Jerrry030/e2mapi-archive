package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"e2m.local/contracts"
)

// A route-plan generation owns every queued mutation for that plan. Exercise
// both ways its semantics can change: a direct desired-state edit and an edit
// to an allocated channel used by the plan.
func TestMemoryConnectorTaskSemanticGenerationFence(t *testing.T) {
	type semanticMutation struct {
		bump   func(*testing.T, *memoryConnectorPlanFenceFixture) contracts.RoutePlan
		replay func(*testing.T, *memoryConnectorPlanFenceFixture) contracts.RoutePlan
	}
	mutations := map[string]semanticMutation{
		"route plan": {
			bump: func(t *testing.T, fixture *memoryConnectorPlanFenceFixture) contracts.RoutePlan {
				t.Helper()
				plan, err := fixture.store.GetRoutePlan(fixture.ctx, fixture.plan.ID)
				if err != nil {
					t.Fatal(err)
				}
				plan.MaxChannels++
				updated, err := fixture.store.UpdateRoutePlan(fixture.ctx, plan)
				if err != nil {
					t.Fatalf("update route plan semantics: %v", err)
				}
				fixture.plan = updated
				return updated
			},
			replay: func(t *testing.T, fixture *memoryConnectorPlanFenceFixture) contracts.RoutePlan {
				t.Helper()
				plan, err := fixture.store.GetRoutePlan(fixture.ctx, fixture.plan.ID)
				if err != nil {
					t.Fatal(err)
				}
				replayed, err := fixture.store.UpdateRoutePlan(fixture.ctx, plan)
				if err != nil {
					t.Fatalf("replay identical route plan: %v", err)
				}
				fixture.plan = replayed
				return replayed
			},
		},
		"allocated channel": {
			bump: func(t *testing.T, fixture *memoryConnectorPlanFenceFixture) contracts.RoutePlan {
				t.Helper()
				channel, err := fixture.store.GetUpstreamChannel(fixture.ctx, fixture.channel.ID)
				if err != nil {
					t.Fatal(err)
				}
				channel.DisplayName += " changed"
				updated, err := fixture.store.UpdateUpstreamChannel(fixture.ctx, channel)
				if err != nil {
					t.Fatalf("update allocated channel semantics: %v", err)
				}
				fixture.channel = updated
				plan, err := fixture.store.GetRoutePlan(fixture.ctx, fixture.plan.ID)
				if err != nil {
					t.Fatal(err)
				}
				fixture.plan = plan
				return plan
			},
			replay: func(t *testing.T, fixture *memoryConnectorPlanFenceFixture) contracts.RoutePlan {
				t.Helper()
				channel, err := fixture.store.GetUpstreamChannel(fixture.ctx, fixture.channel.ID)
				if err != nil {
					t.Fatal(err)
				}
				replayed, err := fixture.store.UpdateUpstreamChannel(fixture.ctx, channel)
				if err != nil {
					t.Fatalf("replay identical allocated channel: %v", err)
				}
				fixture.channel = replayed
				plan, err := fixture.store.GetRoutePlan(fixture.ctx, fixture.plan.ID)
				if err != nil {
					t.Fatal(err)
				}
				fixture.plan = plan
				return plan
			},
		},
	}

	for name, mutation := range mutations {
		mutation := mutation
		t.Run(name, func(t *testing.T) {
			t.Run("old pending task cannot lease", func(t *testing.T) {
				fixture := newMemoryConnectorPlanFenceFixture(t)
				oldGeneration := fixture.plan.SchedulingGeneration
				task := fixture.createSchedulingTask(t, "pending")

				current := mutation.bump(t, fixture)
				if current.SchedulingGeneration != oldGeneration+1 {
					t.Fatalf("semantic bump generation=%d, want %d", current.SchedulingGeneration, oldGeneration+1)
				}
				assertMemoryConnectorTaskSuperseded(t, fixture, task.ID, 0)
				leased, err := fixture.store.LeaseConnectorTasks(fixture.ctx, contracts.ConnectorTaskLeaseRequest{
					ConnectorID: fixture.connector.ID, MaxTasks: 10,
				})
				if err != nil || len(leased) != 0 {
					t.Fatalf("stale pending task was leaseable: tasks=%+v err=%v", leased, err)
				}
			})

			t.Run("old leased completion conflicts", func(t *testing.T) {
				fixture := newMemoryConnectorPlanFenceFixture(t)
				oldGeneration := fixture.plan.SchedulingGeneration
				task := fixture.createSchedulingTask(t, "leased")
				leased, err := fixture.store.LeaseConnectorTasks(fixture.ctx, contracts.ConnectorTaskLeaseRequest{
					ConnectorID: fixture.connector.ID, LeaseSeconds: 60,
				})
				if err != nil || len(leased) != 1 || leased[0].ID != task.ID || leased[0].LeaseNonce == "" {
					t.Fatalf("lease fenced task: tasks=%+v err=%v", leased, err)
				}

				current := mutation.bump(t, fixture)
				if current.SchedulingGeneration != oldGeneration+1 {
					t.Fatalf("semantic bump generation=%d, want %d", current.SchedulingGeneration, oldGeneration+1)
				}
				assertMemoryConnectorTaskSuperseded(t, fixture, task.ID, 1)
				if _, err := fixture.store.CompleteConnectorTask(fixture.ctx, task.ID, contracts.ConnectorTaskCompleteRequest{
					ConnectorID: fixture.connector.ID,
					LeaseNonce:  leased[0].LeaseNonce,
					Success:     true,
					Result:      json.RawMessage(`{"remote_id":"must-not-commit"}`),
				}); !errors.Is(err, ErrConflict) {
					t.Fatalf("stale leased completion error=%v, want ErrConflict", err)
				}
				assertMemoryConnectorTaskSuperseded(t, fixture, task.ID, 1)
			})

			t.Run("identical replay does not supersede", func(t *testing.T) {
				fixture := newMemoryConnectorPlanFenceFixture(t)
				oldGeneration := fixture.plan.SchedulingGeneration
				task := fixture.createSchedulingTask(t, "same-value-replay")

				current := mutation.replay(t, fixture)
				if current.SchedulingGeneration != oldGeneration {
					t.Fatalf("same-value replay advanced generation: before=%d after=%d", oldGeneration, current.SchedulingGeneration)
				}
				stored, err := fixture.store.GetConnectorTask(fixture.ctx, task.ID)
				if err != nil || stored.Status != contracts.ConnectorTaskPending || stored.Error.Code != "" || stored.Attempts != 0 {
					t.Fatalf("same-value replay superseded pending task: task=%+v err=%v", stored, err)
				}
				leased, err := fixture.store.LeaseConnectorTasks(fixture.ctx, contracts.ConnectorTaskLeaseRequest{
					ConnectorID: fixture.connector.ID, LeaseSeconds: 60,
				})
				if err != nil || len(leased) != 1 || leased[0].ID != task.ID {
					t.Fatalf("current task was not leaseable after replay: tasks=%+v err=%v", leased, err)
				}
				executing, err := fixture.store.BeginConnectorTaskExecution(fixture.ctx, task.ID, contracts.ConnectorTaskExecutionRequest{
					ConnectorID: fixture.connector.ID, LeaseNonce: leased[0].LeaseNonce,
				})
				if err != nil || executing.Status != contracts.ConnectorTaskExecuting {
					t.Fatalf("current task could not acquire execution permit after replay: task=%+v err=%v", executing, err)
				}
				completed, err := fixture.store.CompleteConnectorTask(fixture.ctx, task.ID, contracts.ConnectorTaskCompleteRequest{
					ConnectorID: fixture.connector.ID, LeaseNonce: leased[0].LeaseNonce, Success: true,
					Result: json.RawMessage(`{"remote_id":"account-plan-fence"}`),
				})
				if err != nil || completed.Status != contracts.ConnectorTaskSucceeded || completed.Error.Code != "" {
					t.Fatalf("current task could not complete after replay: task=%+v err=%v", completed, err)
				}
			})
		})
	}
}

type memoryConnectorPlanFenceFixture struct {
	ctx       context.Context
	store     *MemoryStore
	connector contracts.Connector
	channel   contracts.UpstreamChannel
	plan      contracts.RoutePlan
}

func newMemoryConnectorPlanFenceFixture(t *testing.T) *memoryConnectorPlanFenceFixture {
	t.Helper()
	ctx := context.Background()
	st, _, instance, connector := newMemoryConnectorTaskFixture(t, "connector-plan-generation-fence")
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{
		ID: "pool-plan-generation-fence", Name: "plan generation fence", Status: contracts.UpstreamPoolActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		ID: "channel-plan-generation-fence", PoolID: pool.ID, SourceID: "source-plan-generation-fence",
		DisplayName: "channel", CredentialBindingID: "binding-plan-generation-fence",
		Status: contracts.UpstreamChannelActive, AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: "plan-generation-fence", UserID: 1, InstanceID: instance.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanPublished, SchedulingGeneration: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: instance.ID, ChannelID: channel.ID, RemoteID: "account-plan-fence",
		State: contracts.BindingActive, SchedulingGeneration: plan.SchedulingGeneration,
	}); err != nil {
		t.Fatal(err)
	}
	return &memoryConnectorPlanFenceFixture{ctx: ctx, store: st, connector: connector, channel: channel, plan: plan}
}

func (fixture *memoryConnectorPlanFenceFixture) createSchedulingTask(t *testing.T, suffix string) contracts.ConnectorTask {
	t.Helper()
	fence := contracts.GatewaySchedulingFence{
		Scope: "auto-switch/plan/" + fixture.plan.ID, Version: fixture.plan.SchedulingGeneration, Sequence: 1,
	}
	input, err := json.Marshal(contracts.ConnectorGatewaySchedulableSetInput{
		AccountID: "account-plan-fence", Schedulable: false, Fence: &fence,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.CreateConnectorTask(fixture.ctx, contracts.ConnectorTask{
		UserID: fixture.plan.UserID, InstanceID: fixture.plan.InstanceID, ConnectorID: fixture.connector.ID,
		PlanID: fixture.plan.ID, SchedulingGeneration: fixture.plan.SchedulingGeneration,
		Type: contracts.ConnectorTaskGatewaySchedulableSet, Input: input,
		IdempotencyKey: "plan-generation-fence-" + suffix,
	})
	if err != nil {
		t.Fatalf("create scheduling task: %v", err)
	}
	if task.PlanID != fixture.plan.ID || task.SchedulingGeneration != fixture.plan.SchedulingGeneration {
		t.Fatalf("stored task fence mismatch: %+v", task)
	}
	return task
}

func assertMemoryConnectorTaskSuperseded(t *testing.T, fixture *memoryConnectorPlanFenceFixture, taskID string, attempts int) {
	t.Helper()
	task, err := fixture.store.GetConnectorTask(fixture.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != contracts.ConnectorTaskFailed || task.Error.Code != "scheduling_fence_stale" || task.Error.Retryable ||
		task.LeaseOwner != "" || task.LeaseNonce != "" || task.LeaseUntil != nil || task.Attempts != attempts || len(task.Result) != 0 {
		t.Fatalf("stale task was not fail-closed: %+v", task)
	}
}
