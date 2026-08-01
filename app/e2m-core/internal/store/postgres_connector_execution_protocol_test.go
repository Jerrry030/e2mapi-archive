package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
	"github.com/jackc/pgx/v5/pgconn"
)

// These tests exercise protocol v3 against a real PostgreSQL transaction
// manager. The memory-store tests cover the same state machine, but cannot
// prove the route-plan/task lock order, database trigger, or audit rollback.
func TestPostgresConnectorExecutionPermitSerializesGenerationBump(t *testing.T) {
	t.Run("execution commits before generation", func(t *testing.T) {
		fixture := newPostgresConnectorExecutionFixture(t)
		task := fixture.createFencedTask(t, "execution-first")
		leased := fixture.leaseTask(t, task.ID)

		executing, err := fixture.store.BeginConnectorTaskExecution(fixture.ctx, task.ID, contracts.ConnectorTaskExecutionRequest{
			ConnectorID: fixture.connector.ID,
			LeaseNonce:  leased.LeaseNonce,
		})
		if err != nil || executing.Status != contracts.ConnectorTaskExecuting {
			t.Fatalf("begin execution = %+v, err=%v", executing, err)
		}
		if _, err := fixture.bumpPlan(t); !isPostgresConnectorExecutionConflict(err) {
			t.Fatalf("generation bump after permit error=%v, want conflict", err)
		}

		persistedTask := fixture.getTask(t, task.ID)
		persistedPlan := fixture.getPlan(t)
		if persistedTask.Status != contracts.ConnectorTaskExecuting || persistedTask.LeaseNonce != leased.LeaseNonce {
			t.Fatalf("rejected generation bump changed execution permit: %+v", persistedTask)
		}
		if persistedPlan.SchedulingGeneration != fixture.plan.SchedulingGeneration {
			t.Fatalf("rejected generation bump committed generation=%d, want %d", persistedPlan.SchedulingGeneration, fixture.plan.SchedulingGeneration)
		}
	})

	t.Run("generation commits before execution", func(t *testing.T) {
		fixture := newPostgresConnectorExecutionFixture(t)
		task := fixture.createFencedTask(t, "generation-first")
		leased := fixture.leaseTask(t, task.ID)

		updated, err := fixture.bumpPlan(t)
		if err != nil {
			t.Fatalf("bump generation: %v", err)
		}
		if _, err := fixture.store.BeginConnectorTaskExecution(fixture.ctx, task.ID, contracts.ConnectorTaskExecutionRequest{
			ConnectorID: fixture.connector.ID,
			LeaseNonce:  leased.LeaseNonce,
		}); !isPostgresConnectorExecutionConflict(err) {
			t.Fatalf("begin after generation bump error=%v, want conflict", err)
		}

		persisted := fixture.getTask(t, task.ID)
		if persisted.Status != contracts.ConnectorTaskFailed || persisted.Error.Code != connectorTaskSupersededErrorCode ||
			persisted.LeaseOwner != "" || persisted.LeaseNonce != "" || persisted.LeaseUntil != nil {
			t.Fatalf("generation winner did not fail stale lease closed: %+v", persisted)
		}
		if updated.SchedulingGeneration != fixture.plan.SchedulingGeneration+1 {
			t.Fatalf("generation=%d, want %d", updated.SchedulingGeneration, fixture.plan.SchedulingGeneration+1)
		}
	})

	// Repetition is intentional: both transactions leave the start barrier at
	// the same time, and whichever PostgreSQL schedules first must be the sole
	// committer. A shared timeout also makes a lock-order regression fail as a
	// bounded test instead of hanging the suite.
	for iteration := 0; iteration < 12; iteration++ {
		t.Run("concurrent exactly one commits "+testIterationName(iteration), func(t *testing.T) {
			fixture := newPostgresConnectorExecutionFixture(t)
			task := fixture.createFencedTask(t, "concurrent")
			leased := fixture.leaseTask(t, task.ID)
			desired := fixture.plan
			desired.MaxChannels++

			type outcome struct {
				operation string
				err       error
			}
			start := make(chan struct{})
			outcomes := make(chan outcome, 2)
			raceCtx, cancel := context.WithTimeout(fixture.ctx, 5*time.Second)
			defer cancel()
			go func() {
				<-start
				_, err := fixture.store.BeginConnectorTaskExecution(raceCtx, task.ID, contracts.ConnectorTaskExecutionRequest{
					ConnectorID: fixture.connector.ID,
					LeaseNonce:  leased.LeaseNonce,
				})
				outcomes <- outcome{operation: "execution", err: err}
			}()
			go func() {
				<-start
				_, err := fixture.store.UpdateRoutePlan(raceCtx, desired)
				outcomes <- outcome{operation: "generation", err: err}
			}()
			close(start)

			got := make([]outcome, 0, 2)
			for len(got) < 2 {
				select {
				case result := <-outcomes:
					got = append(got, result)
				case <-raceCtx.Done():
					t.Fatalf("permit/generation race did not settle without deadlock: outcomes=%+v err=%v", got, raceCtx.Err())
				}
			}
			successes := 0
			winner := ""
			for _, result := range got {
				if result.err == nil {
					successes++
					winner = result.operation
					continue
				}
				if !isPostgresConnectorExecutionConflict(result.err) {
					t.Fatalf("%s loser error=%v, want conflict (not deadlock)", result.operation, result.err)
				}
			}
			if successes != 1 {
				t.Fatalf("race successes=%d, want exactly one: %+v", successes, got)
			}

			persistedTask := fixture.getTask(t, task.ID)
			persistedPlan := fixture.getPlan(t)
			switch winner {
			case "execution":
				if persistedTask.Status != contracts.ConnectorTaskExecuting || persistedTask.LeaseNonce != leased.LeaseNonce ||
					persistedPlan.SchedulingGeneration != fixture.plan.SchedulingGeneration {
					t.Fatalf("execution winner state: task=%+v plan=%+v", persistedTask, persistedPlan)
				}
			case "generation":
				if persistedTask.Status != contracts.ConnectorTaskFailed || persistedTask.Error.Code != connectorTaskSupersededErrorCode ||
					persistedPlan.SchedulingGeneration != fixture.plan.SchedulingGeneration+1 {
					t.Fatalf("generation winner state: task=%+v plan=%+v", persistedTask, persistedPlan)
				}
			default:
				t.Fatalf("unknown winner %q", winner)
			}
		})
	}
}

func TestPostgresConnectorExecutionCompletionProtocol(t *testing.T) {
	t.Run("executing completion ignores old lease deadline", func(t *testing.T) {
		fixture := newPostgresConnectorExecutionFixture(t)
		task := fixture.createFencedTask(t, "expired-audit-lease")
		leased := fixture.leaseTask(t, task.ID)
		if _, err := fixture.beginTask(t, leased); err != nil {
			t.Fatalf("begin execution: %v", err)
		}
		if _, err := fixture.store.pool.Exec(fixture.ctx,
			`UPDATE connector_tasks SET lease_until=statement_timestamp()-interval '1 hour' WHERE id=$1`, task.ID); err != nil {
			t.Fatalf("age audit lease deadline: %v", err)
		}

		completed, err := fixture.store.CompleteConnectorTask(fixture.ctx, task.ID, contracts.ConnectorTaskCompleteRequest{
			ConnectorID: fixture.connector.ID,
			LeaseNonce:  leased.LeaseNonce,
			Success:     true,
			Result:      fixture.appliedResult(t),
		})
		if err != nil || completed.Status != contracts.ConnectorTaskSucceeded {
			t.Fatalf("complete after old lease deadline = %+v, err=%v", completed, err)
		}
		if completed.LeaseOwner != "" || completed.LeaseNonce != "" || completed.LeaseUntil != nil {
			t.Fatalf("terminal completion retained execution lease: %+v", completed)
		}
	})

	t.Run("fenced leased task cannot complete directly", func(t *testing.T) {
		fixture := newPostgresConnectorExecutionFixture(t)
		task := fixture.createFencedTask(t, "leased-direct-complete")
		leased := fixture.leaseTask(t, task.ID)
		before := fixture.getTask(t, task.ID)
		_, err := fixture.store.CompleteConnectorTask(fixture.ctx, task.ID, contracts.ConnectorTaskCompleteRequest{
			ConnectorID: fixture.connector.ID,
			LeaseNonce:  leased.LeaseNonce,
			Success:     true,
			Result:      fixture.appliedResult(t),
		})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("direct fenced completion error=%v, want ErrConflict", err)
		}
		fixture.requireTaskUnchanged(t, before)
	})

	t.Run("manual task retains leased completion path", func(t *testing.T) {
		fixture := newPostgresConnectorExecutionFixture(t)
		task := fixture.createManualTask(t)
		leased := fixture.leaseTask(t, task.ID)
		before := fixture.getTask(t, task.ID)
		if _, err := fixture.store.BeginConnectorTaskExecution(fixture.ctx, task.ID, contracts.ConnectorTaskExecutionRequest{
			ConnectorID: fixture.connector.ID,
			LeaseNonce:  leased.LeaseNonce,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("manual task begin error=%v, want ErrConflict", err)
		}
		fixture.requireTaskUnchanged(t, before)

		completed, err := fixture.store.CompleteConnectorTask(fixture.ctx, task.ID, contracts.ConnectorTaskCompleteRequest{
			ConnectorID: fixture.connector.ID,
			LeaseNonce:  leased.LeaseNonce,
			Success:     true,
			Result:      json.RawMessage(`{"remote_id":"manual-account"}`),
		})
		if err != nil || completed.Status != contracts.ConnectorTaskSucceeded {
			t.Fatalf("manual leased completion = %+v, err=%v", completed, err)
		}
	})

	t.Run("retryable fenced completion preserves execution permit", func(t *testing.T) {
		fixture := newPostgresConnectorExecutionFixture(t)
		task := fixture.createFencedTask(t, "retryable-completion")
		leased := fixture.leaseTask(t, task.ID)
		if _, err := fixture.beginTask(t, leased); err != nil {
			t.Fatalf("begin execution: %v", err)
		}
		before := fixture.getTask(t, task.ID)
		_, err := fixture.store.CompleteConnectorTask(fixture.ctx, task.ID, contracts.ConnectorTaskCompleteRequest{
			ConnectorID: fixture.connector.ID,
			LeaseNonce:  leased.LeaseNonce,
			Error: contracts.ConnectorTaskError{
				Code:      "gateway_timeout",
				Retryable: true,
			},
		})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("retryable fenced completion error=%v, want ErrConflict", err)
		}
		fixture.requireTaskUnchanged(t, before)
	})
}

func TestPostgresConnectorExecutingTaskRetainsIdempotencyAfterExpiry(t *testing.T) {
	fixture := newPostgresConnectorExecutionFixture(t)
	task := fixture.createFencedTask(t, "executing-idempotency")
	leased := fixture.leaseTask(t, task.ID)
	executing, err := fixture.beginTask(t, leased)
	if err != nil || executing.Status != contracts.ConnectorTaskExecuting {
		t.Fatalf("begin execution = %+v, err=%v", executing, err)
	}
	if _, err := fixture.store.pool.Exec(fixture.ctx,
		`UPDATE connector_tasks SET expires_at=statement_timestamp()-interval '1 hour' WHERE id=$1`, task.ID); err != nil {
		t.Fatalf("age executing task expiry: %v", err)
	}

	_, err = fixture.store.CreateConnectorTask(fixture.ctx, contracts.ConnectorTask{
		UserID: task.UserID, InstanceID: task.InstanceID, ConnectorID: task.ConnectorID,
		PlanID: task.PlanID, SchedulingGeneration: task.SchedulingGeneration,
		Type: task.Type, SchemaVersion: task.SchemaVersion, Input: append(json.RawMessage(nil), task.Input...),
		IdempotencyKey: task.IdempotencyKey,
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expired executing duplicate error=%v, want ErrDuplicate", err)
	}
	var count int
	if err := fixture.store.pool.QueryRow(fixture.ctx,
		`SELECT COUNT(*) FROM connector_tasks WHERE connector_id=$1 AND idempotency_key=$2`,
		task.ConnectorID, task.IdempotencyKey,
	).Scan(&count); err != nil {
		t.Fatalf("count executing task identity: %v", err)
	}
	if count != 1 {
		t.Fatalf("connector task identity count=%d, want 1", count)
	}
	persisted := fixture.getTask(t, task.ID)
	if persisted.Status != contracts.ConnectorTaskExecuting || persisted.LeaseNonce != leased.LeaseNonce {
		t.Fatalf("duplicate attempt changed executing task: %+v", persisted)
	}
}

func TestPostgresResolveConnectorTaskExecutionClosedSetAndReplay(t *testing.T) {
	tests := []struct {
		name       string
		resolution contracts.ConnectorTaskExecutionResolution
		revoke     bool
		wantStatus contracts.ConnectorTaskStatus
		wantError  string
		wantResult bool
	}{
		{name: "confirmed applied", resolution: contracts.ConnectorTaskExecutionConfirmedApplied, wantStatus: contracts.ConnectorTaskSucceeded, wantResult: true},
		{name: "confirmed not applied", resolution: contracts.ConnectorTaskExecutionConfirmedNotApplied, wantStatus: contracts.ConnectorTaskFailed, wantError: "execution_abandoned"},
		{name: "revoked unverifiable", resolution: contracts.ConnectorTaskExecutionRevokedUnverifiable, revoke: true, wantStatus: contracts.ConnectorTaskFailed, wantError: "execution_outcome_unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPostgresConnectorExecutionFixture(t)
			task := fixture.createFencedTask(t, "resolve-"+strings.ReplaceAll(test.name, " ", "-"))
			leased := fixture.leaseTask(t, task.ID)
			executing, err := fixture.beginTask(t, leased)
			if err != nil {
				t.Fatalf("begin execution: %v", err)
			}
			req := contracts.ConnectorTaskExecutionResolveRequest{
				LeaseNonce:   executing.LeaseNonce,
				Resolution:   test.resolution,
				EvidenceNote: "Independent gateway readback confirmed the mutation outcome.",
			}
			if test.wantResult {
				req.Result = fixture.appliedResult(t)
			}
			audit := fixture.resolutionAudit(executing, req)

			if test.revoke {
				before := fixture.getTask(t, task.ID)
				if _, err := fixture.store.ResolveConnectorTaskExecution(fixture.ctx, task.ID, req, audit); !errors.Is(err, ErrConflict) {
					t.Fatalf("unrevoked unverifiable resolution error=%v, want ErrConflict", err)
				}
				fixture.requireTaskUnchanged(t, before)
				fixture.requireAuditCount(t, task.ID, 0)
				if _, err := fixture.store.RevokeConnector(fixture.ctx, fixture.connector.ID); err != nil {
					t.Fatalf("revoke connector: %v", err)
				}
			}

			resolved, err := fixture.store.ResolveConnectorTaskExecution(fixture.ctx, task.ID, req, audit)
			if err != nil {
				t.Fatalf("resolve execution: %v", err)
			}
			if resolved.Status != test.wantStatus || resolved.Error.Code != test.wantError ||
				resolved.LeaseOwner != "" || resolved.LeaseNonce != "" || resolved.LeaseUntil != nil {
				t.Fatalf("terminal resolution state: %+v", resolved)
			}
			if test.wantResult != (len(resolved.Result) > 0) {
				t.Fatalf("resolution result presence=%t, want %t: %s", len(resolved.Result) > 0, test.wantResult, resolved.Result)
			}
			fixture.requireAuditCount(t, task.ID, 1)

			terminalBeforeReplay := fixture.getTask(t, task.ID)
			if _, err := fixture.store.ResolveConnectorTaskExecution(fixture.ctx, task.ID, req, audit); !errors.Is(err, ErrConflict) {
				t.Fatalf("replayed resolution error=%v, want ErrConflict", err)
			}
			fixture.requireTaskUnchanged(t, terminalBeforeReplay)
			fixture.requireAuditCount(t, task.ID, 1)
		})
	}
}

func TestPostgresResolveConnectorTaskExecutionRejectsWrongOldAndStaleIdentity(t *testing.T) {
	t.Run("wrong nonce", func(t *testing.T) {
		fixture := newPostgresConnectorExecutionFixture(t)
		task := fixture.createFencedTask(t, "wrong-nonce")
		leased := fixture.leaseTask(t, task.ID)
		executing, err := fixture.beginTask(t, leased)
		if err != nil {
			t.Fatalf("begin execution: %v", err)
		}
		req := fixture.notAppliedResolution(executing.LeaseNonce)
		audit := fixture.resolutionAudit(executing, req)
		req.LeaseNonce = "wrong-execution-nonce"
		before := fixture.getTask(t, task.ID)
		if _, err := fixture.store.ResolveConnectorTaskExecution(fixture.ctx, task.ID, req, audit); !errors.Is(err, ErrConflict) {
			t.Fatalf("wrong nonce resolution error=%v, want ErrConflict", err)
		}
		fixture.requireTaskUnchanged(t, before)
		fixture.requireAuditCount(t, task.ID, 0)
	})

	t.Run("old lease nonce", func(t *testing.T) {
		fixture := newPostgresConnectorExecutionFixture(t)
		task := fixture.createFencedTask(t, "old-nonce")
		firstLease := fixture.leaseTask(t, task.ID)
		if _, err := fixture.store.pool.Exec(fixture.ctx,
			`UPDATE connector_tasks SET lease_until=statement_timestamp()-interval '1 second' WHERE id=$1`, task.ID); err != nil {
			t.Fatalf("expire first audit lease: %v", err)
		}
		secondLease := fixture.leaseTask(t, task.ID)
		if secondLease.LeaseNonce == firstLease.LeaseNonce {
			t.Fatal("re-leased task reused its old nonce")
		}
		executing, err := fixture.beginTask(t, secondLease)
		if err != nil {
			t.Fatalf("begin second execution identity: %v", err)
		}
		req := fixture.notAppliedResolution(firstLease.LeaseNonce)
		audit := fixture.resolutionAudit(executing, req)
		before := fixture.getTask(t, task.ID)
		if _, err := fixture.store.ResolveConnectorTaskExecution(fixture.ctx, task.ID, req, audit); !errors.Is(err, ErrConflict) {
			t.Fatalf("old nonce resolution error=%v, want ErrConflict", err)
		}
		fixture.requireTaskUnchanged(t, before)
		fixture.requireAuditCount(t, task.ID, 0)
	})

	t.Run("stale generation", func(t *testing.T) {
		fixture := newPostgresConnectorExecutionFixture(t)
		task := fixture.createFencedTask(t, "stale-generation")
		leased := fixture.leaseTask(t, task.ID)
		if _, err := fixture.bumpPlan(t); err != nil {
			t.Fatalf("bump generation: %v", err)
		}
		req := fixture.notAppliedResolution(leased.LeaseNonce)
		audit := fixture.resolutionAudit(leased, req)
		before := fixture.getTask(t, task.ID)
		if _, err := fixture.store.ResolveConnectorTaskExecution(fixture.ctx, task.ID, req, audit); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale generation resolution error=%v, want ErrConflict", err)
		}
		fixture.requireTaskUnchanged(t, before)
		fixture.requireAuditCount(t, task.ID, 0)
	})

	t.Run("terminal completion is stale for resolution", func(t *testing.T) {
		fixture := newPostgresConnectorExecutionFixture(t)
		task := fixture.createFencedTask(t, "terminal-stale")
		leased := fixture.leaseTask(t, task.ID)
		executing, err := fixture.beginTask(t, leased)
		if err != nil {
			t.Fatalf("begin execution: %v", err)
		}
		req := fixture.notAppliedResolution(executing.LeaseNonce)
		audit := fixture.resolutionAudit(executing, req)
		if _, err := fixture.store.CompleteConnectorTask(fixture.ctx, task.ID, contracts.ConnectorTaskCompleteRequest{
			ConnectorID: fixture.connector.ID,
			LeaseNonce:  executing.LeaseNonce,
			Success:     true,
			Result:      fixture.appliedResult(t),
		}); err != nil {
			t.Fatalf("complete execution: %v", err)
		}
		before := fixture.getTask(t, task.ID)
		if _, err := fixture.store.ResolveConnectorTaskExecution(fixture.ctx, task.ID, req, audit); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale terminal resolution error=%v, want ErrConflict", err)
		}
		fixture.requireTaskUnchanged(t, before)
		fixture.requireAuditCount(t, task.ID, 0)
	})
}

func TestPostgresResolveConnectorTaskExecutionAuditIsAtomic(t *testing.T) {
	fixture := newPostgresConnectorExecutionFixture(t)
	task := fixture.createFencedTask(t, "audit-atomic")
	leased := fixture.leaseTask(t, task.ID)
	executing, err := fixture.beginTask(t, leased)
	if err != nil {
		t.Fatalf("begin execution: %v", err)
	}
	req := fixture.notAppliedResolution(executing.LeaseNonce)
	validAudit := fixture.resolutionAudit(executing, req)
	before := fixture.getTask(t, task.ID)

	invalidAudits := []struct {
		name   string
		mutate func(*contracts.OperationAudit)
	}{
		{name: "actor type", mutate: func(a *contracts.OperationAudit) { a.ActorType = "connector" }},
		{name: "actor id", mutate: func(a *contracts.OperationAudit) { a.ActorID = "" }},
		{name: "action", mutate: func(a *contracts.OperationAudit) { a.Action = "connector_task.complete" }},
		{name: "risk", mutate: func(a *contracts.OperationAudit) { a.RiskLevel = contracts.RiskLevelL2 }},
		{name: "event", mutate: func(a *contracts.OperationAudit) { a.EventLevel = contracts.EventLevelWarning }},
		{name: "result", mutate: func(a *contracts.OperationAudit) { a.Result = "succeeded" }},
		{name: "nonce digest", mutate: func(a *contracts.OperationAudit) { a.Details["lease_nonce_sha256"] = "sha256:wrong" }},
		{name: "extra detail", mutate: func(a *contracts.OperationAudit) { a.Details["raw_response"] = "unexpected" }},
	}
	for _, test := range invalidAudits {
		t.Run("invalid "+test.name+" changes nothing", func(t *testing.T) {
			audit := cloneConnectorExecutionAudit(validAudit)
			test.mutate(&audit)
			if _, err := fixture.store.ResolveConnectorTaskExecution(fixture.ctx, task.ID, req, audit); !errors.Is(err, ErrConflict) {
				t.Fatalf("invalid audit error=%v, want ErrConflict", err)
			}
			fixture.requireTaskUnchanged(t, before)
			fixture.requireAuditCount(t, task.ID, 0)
		})
	}

	// The audit is semantically valid, so the task UPDATE runs first. PostgreSQL
	// rejects NUL in a text field during the following INSERT; observing the
	// executing task unchanged proves both writes share one transaction.
	insertFailureAudit := cloneConnectorExecutionAudit(validAudit)
	insertFailureAudit.RequestHash = "sha256:\x00"
	if _, err := fixture.store.ResolveConnectorTaskExecution(fixture.ctx, task.ID, req, insertFailureAudit); err == nil {
		t.Fatal("audit insertion with a NUL text value unexpectedly succeeded")
	}
	fixture.requireTaskUnchanged(t, before)
	fixture.requireAuditCount(t, task.ID, 0)

	resolved, err := fixture.store.ResolveConnectorTaskExecution(fixture.ctx, task.ID, req, validAudit)
	if err != nil {
		t.Fatalf("valid atomic resolution: %v", err)
	}
	if resolved.Status != contracts.ConnectorTaskFailed || resolved.Error.Code != "execution_abandoned" {
		t.Fatalf("valid resolution did not terminalize task: %+v", resolved)
	}
	fixture.requireAuditCount(t, task.ID, 1)
}

type postgresConnectorExecutionFixture struct {
	ctx       context.Context
	cancel    context.CancelFunc
	store     *PostgresStore
	user      contracts.User
	instance  contracts.Instance
	connector contracts.Connector
	pool      contracts.UpstreamPool
	plan      contracts.RoutePlan
}

func newPostgresConnectorExecutionFixture(t *testing.T) *postgresConnectorExecutionFixture {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("E2M_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	store, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		cancel()
		t.Fatalf("open postgres store: %v", err)
	}
	fixture := &postgresConnectorExecutionFixture{ctx: ctx, cancel: cancel, store: store}
	fixture.user, err = store.CreateUser(ctx, contracts.User{
		Email:   "connector-execution-" + newID("test") + "@example.com",
		Roles:   []contracts.UserRole{contracts.UserRoleClient},
		Enabled: true,
	})
	if err != nil {
		store.Close()
		cancel()
		t.Fatalf("create user: %v", err)
	}
	fixture.instance, err = store.CreateInstance(ctx, contracts.Instance{
		UserID: fixture.user.ID,
		Name:   "Protocol v3 execution test",
		Kind:   contracts.InstanceKindNewAPI,
	})
	if err != nil {
		store.Close()
		cancel()
		t.Fatalf("create instance: %v", err)
	}
	connectorID := newID("conn-execution")
	enrollment, err := store.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{
		UserID:      fixture.user.ID,
		InstanceID:  fixture.instance.ID,
		ConnectorID: connectorID,
		TokenHash:   newID("enrollment-token"),
	})
	if err != nil {
		store.Close()
		cancel()
		t.Fatalf("create connector enrollment: %v", err)
	}
	fixture.connector, _, err = store.UseConnectorEnrollment(ctx, enrollment.TokenHash, contracts.Connector{
		ID:              connectorID,
		InstanceID:      fixture.instance.ID,
		Version:         "0.3.0",
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		TokenHash:       newID("connector-token"),
	})
	if err != nil {
		store.Close()
		cancel()
		t.Fatalf("use connector enrollment: %v", err)
	}
	fixture.pool, err = store.CreateUpstreamPool(ctx, contracts.UpstreamPool{
		ID:     newID("pool-execution"),
		Name:   "Protocol v3 execution pool",
		Status: contracts.UpstreamPoolActive,
	})
	if err != nil {
		store.Close()
		cancel()
		t.Fatalf("create upstream pool: %v", err)
	}
	fixture.plan, err = store.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID:                   newID("plan-execution"),
		UserID:               fixture.user.ID,
		InstanceID:           fixture.instance.ID,
		PoolID:               fixture.pool.ID,
		Status:               contracts.RoutePlanPublished,
		SchedulingGeneration: 1,
	})
	if err != nil {
		store.Close()
		cancel()
		t.Fatalf("create route plan: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM operation_audits WHERE target_type='connector_task' AND target_id IN (SELECT id FROM connector_tasks WHERE connector_id=$1)`, fixture.connector.ID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM connector_tasks WHERE connector_id=$1`, fixture.connector.ID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM route_plans WHERE id=$1`, fixture.plan.ID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM upstream_pools WHERE id=$1`, fixture.pool.ID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM connectors WHERE connector_id=$1`, fixture.connector.ID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM connector_enrollments WHERE connector_id=$1`, fixture.connector.ID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM instances WHERE id=$1`, fixture.instance.ID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, fixture.user.ID)
		store.Close()
		cancel()
	})
	return fixture
}

func (fixture *postgresConnectorExecutionFixture) createFencedTask(t *testing.T, suffix string) contracts.ConnectorTask {
	t.Helper()
	input := contracts.ConnectorGatewayTrafficShareSetInput{
		AccountID: "account-protocol-v3",
		Weight:    25,
		Fence: contracts.GatewaySchedulingFence{
			Scope:    "auto-switch/plan/" + fixture.plan.ID,
			Version:  fixture.plan.SchedulingGeneration,
			Sequence: 1,
		},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.CreateConnectorTask(fixture.ctx, contracts.ConnectorTask{
		UserID:         fixture.user.ID,
		InstanceID:     fixture.instance.ID,
		ConnectorID:    fixture.connector.ID,
		Type:           contracts.ConnectorTaskGatewayTrafficShareSet,
		SchemaVersion:  1,
		Input:          raw,
		IdempotencyKey: newID("protocol-v3-" + suffix),
		ExpiresAt:      time.Now().UTC().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create fenced task: %v", err)
	}
	return task
}

func (fixture *postgresConnectorExecutionFixture) createManualTask(t *testing.T) contracts.ConnectorTask {
	t.Helper()
	raw, err := json.Marshal(contracts.ConnectorGatewaySchedulableSetInput{
		AccountID:   "manual-account",
		Schedulable: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.CreateConnectorTask(fixture.ctx, contracts.ConnectorTask{
		UserID:         fixture.user.ID,
		InstanceID:     fixture.instance.ID,
		ConnectorID:    fixture.connector.ID,
		Type:           contracts.ConnectorTaskGatewaySchedulableSet,
		SchemaVersion:  1,
		Input:          raw,
		IdempotencyKey: newID("manual-protocol"),
		ExpiresAt:      time.Now().UTC().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create manual task: %v", err)
	}
	return task
}

func (fixture *postgresConnectorExecutionFixture) leaseTask(t *testing.T, taskID string) contracts.ConnectorTask {
	t.Helper()
	tasks, err := fixture.store.LeaseConnectorTasks(fixture.ctx, contracts.ConnectorTaskLeaseRequest{
		ConnectorID:     fixture.connector.ID,
		MaxTasks:        1,
		LeaseSeconds:    60,
		ProtocolVersion: contracts.ConnectorProtocolVersion,
	})
	if err != nil || len(tasks) != 1 || tasks[0].ID != taskID {
		t.Fatalf("lease task %s: tasks=%+v err=%v", taskID, tasks, err)
	}
	return tasks[0]
}

func (fixture *postgresConnectorExecutionFixture) beginTask(t *testing.T, leased contracts.ConnectorTask) (contracts.ConnectorTask, error) {
	t.Helper()
	return fixture.store.BeginConnectorTaskExecution(fixture.ctx, leased.ID, contracts.ConnectorTaskExecutionRequest{
		ConnectorID: fixture.connector.ID,
		LeaseNonce:  leased.LeaseNonce,
	})
}

func (fixture *postgresConnectorExecutionFixture) bumpPlan(t *testing.T) (contracts.RoutePlan, error) {
	t.Helper()
	desired := fixture.plan
	desired.MaxChannels++
	return fixture.store.UpdateRoutePlan(fixture.ctx, desired)
}

func (fixture *postgresConnectorExecutionFixture) getTask(t *testing.T, taskID string) contracts.ConnectorTask {
	t.Helper()
	task, err := fixture.store.GetConnectorTask(fixture.ctx, taskID)
	if err != nil {
		t.Fatalf("get task %s: %v", taskID, err)
	}
	return task
}

func (fixture *postgresConnectorExecutionFixture) getPlan(t *testing.T) contracts.RoutePlan {
	t.Helper()
	plan, err := fixture.store.GetRoutePlan(fixture.ctx, fixture.plan.ID)
	if err != nil {
		t.Fatalf("get route plan: %v", err)
	}
	return plan
}

func (fixture *postgresConnectorExecutionFixture) appliedResult(t *testing.T) json.RawMessage {
	t.Helper()
	input := contracts.ConnectorGatewayTrafficShareSetInput{
		AccountID: "account-protocol-v3",
		Weight:    25,
		Fence: contracts.GatewaySchedulingFence{
			Scope:    "auto-switch/plan/" + fixture.plan.ID,
			Version:  fixture.plan.SchedulingGeneration,
			Sequence: 1,
		},
	}
	raw, err := json.Marshal(contracts.ConnectorGatewayTrafficShareSetResult(input))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (fixture *postgresConnectorExecutionFixture) notAppliedResolution(nonce string) contracts.ConnectorTaskExecutionResolveRequest {
	return contracts.ConnectorTaskExecutionResolveRequest{
		LeaseNonce:   nonce,
		Resolution:   contracts.ConnectorTaskExecutionConfirmedNotApplied,
		EvidenceNote: "Independent gateway readback confirmed that no mutation was applied.",
	}
}

func (fixture *postgresConnectorExecutionFixture) resolutionAudit(task contracts.ConnectorTask, req contracts.ConnectorTaskExecutionResolveRequest) contracts.OperationAudit {
	return contracts.OperationAudit{
		UserID:       task.UserID,
		InstanceID:   task.InstanceID,
		ActorType:    "user",
		ActorID:      "ui17-platform-admin",
		Action:       "connector_task.resolve_execution",
		RiskLevel:    contracts.RiskLevelL3,
		EventLevel:   contracts.EventLevelCritical,
		TargetType:   "connector_task",
		TargetID:     task.ID,
		Result:       string(req.Resolution),
		ErrorMessage: "",
		Details: map[string]string{
			"resolution":         string(req.Resolution),
			"evidence_note":      strings.TrimSpace(req.EvidenceNote),
			"connector_id":       task.ConnectorID,
			"lease_nonce_sha256": ConnectorTaskLeaseNonceAuditHash(task.LeaseNonce),
		},
	}
}

func (fixture *postgresConnectorExecutionFixture) requireTaskUnchanged(t *testing.T, before contracts.ConnectorTask) {
	t.Helper()
	after := fixture.getTask(t, before.ID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected operation changed task:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func (fixture *postgresConnectorExecutionFixture) requireAuditCount(t *testing.T, taskID string, want int) {
	t.Helper()
	audits, err := fixture.store.ListAuditsByTarget(fixture.ctx, "connector_task", taskID)
	if err != nil {
		t.Fatalf("list task audits: %v", err)
	}
	if len(audits) != want {
		t.Fatalf("task audit count=%d, want %d: %+v", len(audits), want, audits)
	}
	if want > 0 && audits[0].Details["lease_nonce"] != "" {
		t.Fatalf("resolution audit leaked raw nonce: %+v", audits[0].Details)
	}
}

func cloneConnectorExecutionAudit(input contracts.OperationAudit) contracts.OperationAudit {
	clone := input
	clone.Details = make(map[string]string, len(input.Details))
	for key, value := range input.Details {
		clone.Details[key] = value
	}
	return clone
}

func isPostgresConnectorExecutionConflict(err error) bool {
	if errors.Is(err, ErrConflict) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

func testIterationName(iteration int) string {
	const digits = "0123456789"
	if iteration < len(digits) {
		return string(digits[iteration])
	}
	return "1" + string(digits[iteration-len(digits)])
}
