package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestUI17LiveStaleRolloutCAS(t *testing.T) {
	if strings.TrimSpace(os.Getenv("E2M_UI17_STALE_CAS_PROOF")) != "1" {
		t.Skip("set E2M_UI17_STALE_CAS_PROOF=1 to run the live PostgreSQL stale CAS proof")
	}

	dsn := ui17RequiredEnvironment(t, "E2M_TEST_POSTGRES_DSN")
	rolloutID := ui17RequiredEnvironment(t, "E2M_UI17_STALE_ROLLOUT_ID")
	operationID := ui17RequiredEnvironment(t, "E2M_UI17_STALE_OPERATION_ID")
	leaseOwner := ui17RequiredEnvironment(t, "E2M_UI17_STALE_LEASE_OWNER")
	operationVersion := ui17RequiredPositiveVersion(t, "E2M_UI17_STALE_OPERATION_VERSION")
	rolloutVersion := ui17RequiredPositiveVersion(t, "E2M_UI17_STALE_ROLLOUT_VERSION")
	if !validRecommendationRolloutWorkerID(leaseOwner) {
		t.Fatal("E2M_UI17_STALE_LEASE_OWNER is not a valid rollout worker identity")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal("open PostgreSQL store for stale CAS proof failed")
	}
	t.Cleanup(st.Close)

	before := ui17ReadStaleCASSnapshot(t, ctx, st, rolloutID, operationID)
	if before.operation.RolloutID != rolloutID {
		t.Fatal("stale operation does not belong to the expected rollout")
	}
	if before.operation.Status != contracts.RecommendationRolloutOperationSuperseded ||
		before.operation.LeaseOwner != "" || before.operation.LeaseUntil != nil {
		t.Fatal("stale operation is not superseded with its lease cleared")
	}
	if before.operation.Version != operationVersion+1 {
		t.Fatal("stale operation version does not prove exactly one takeover fence")
	}
	if before.rollout.Version <= rolloutVersion {
		t.Fatal("stale rollout version is not older than the current rollout")
	}
	if before.plan.ID != before.rollout.State.PlanID ||
		before.plan.SchedulingGeneration != before.rollout.State.SchedulingGeneration {
		t.Fatal("current plan and rollout scheduling generations are inconsistent")
	}
	if before.rollout.State.SchedulingGeneration <= 1 {
		t.Fatal("takeover did not advance the rollout scheduling generation")
	}
	active, rollback, auditCount := ui17ReadTakeoverCASSummary(t, ctx, st, rolloutID)
	expectedActive := 0
	if rollback.Status == contracts.RecommendationRolloutOperationPending || rollback.Status == contracts.RecommendationRolloutOperationRunning {
		expectedActive = 1
	}
	if active != expectedActive || rollback.Action != contracts.RecommendationRolloutOperationRollback ||
		(rollback.Status != contracts.RecommendationRolloutOperationPending && rollback.Status != contracts.RecommendationRolloutOperationRunning && rollback.Status != contracts.RecommendationRolloutOperationSucceeded) ||
		auditCount != 1 {
		t.Fatal("takeover did not leave exactly one rollback lineage and one accepted operator audit")
	}

	if _, err := st.RenewRecommendationRolloutOperation(ctx, operationID, leaseOwner, operationVersion, time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatal("stale operation renew did not return ErrConflict")
	}
	afterRenew := ui17ReadStaleCASSnapshot(t, ctx, st, rolloutID, operationID)
	ui17RequireUnchangedStaleCASSnapshot(t, before, afterRenew, "renew")

	if _, _, err := st.CompleteRecommendationRolloutOperation(ctx, contracts.RecommendationRolloutCompletion{
		OperationID:              operationID,
		WorkerID:                 leaseOwner,
		ExpectedOperationVersion: operationVersion,
		ExpectedRolloutVersion:   rolloutVersion,
		OperationStatus:          contracts.RecommendationRolloutOperationFailed,
		ErrorCode:                contracts.RecommendationRolloutOperationErrorWriteFailed,
		NextState:                before.rollout.State,
	}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale operation completion did not return ErrConflict")
	}
	afterComplete := ui17ReadStaleCASSnapshot(t, ctx, st, rolloutID, operationID)
	ui17RequireUnchangedStaleCASSnapshot(t, before, afterComplete, "complete")

	operationHash := sha256.Sum256([]byte(operationID))
	t.Logf("UI17_STALE_CAS_PASS operation_sha256=%x renew=conflict complete=conflict", operationHash)
}

func ui17ReadTakeoverCASSummary(t *testing.T, ctx context.Context, st *PostgresStore, rolloutID string) (int, contracts.RecommendationRolloutOperation, int) {
	t.Helper()
	operations, err := st.ListRecommendationRolloutOperations(ctx, rolloutID)
	if err != nil {
		t.Fatal("read takeover operations failed")
	}
	active := 0
	var rollback contracts.RecommendationRolloutOperation
	for _, operation := range operations {
		if operation.Status == contracts.RecommendationRolloutOperationPending || operation.Status == contracts.RecommendationRolloutOperationRunning {
			active++
		}
		if operation.Action == contracts.RecommendationRolloutOperationRollback {
			if rollback.ID != "" {
				t.Fatal("takeover created more than one rollback operation")
			}
			rollback = operation
		}
	}
	var auditCount int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM operation_audits
		WHERE target_id=$1 AND action='upstream_recommendation.rollout.rollback' AND result='accepted'`, rolloutID).Scan(&auditCount); err != nil {
		t.Fatal("read takeover audit failed")
	}
	return active, rollback, auditCount
}

type ui17StaleCASSnapshot struct {
	operation contracts.RecommendationRolloutOperation
	rollout   contracts.RecommendationRollout
	plan      contracts.RoutePlan
}

func ui17RequiredEnvironment(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required for the live stale CAS proof", name)
	}
	return value
}

func ui17RequiredPositiveVersion(t *testing.T, name string) int64 {
	t.Helper()
	value := ui17RequiredEnvironment(t, name)
	version, err := strconv.ParseInt(value, 10, 64)
	if err != nil || version <= 0 {
		t.Fatalf("%s must be a positive integer", name)
	}
	return version
}

func ui17ReadStaleCASSnapshot(t *testing.T, ctx context.Context, st *PostgresStore, rolloutID, operationID string) ui17StaleCASSnapshot {
	t.Helper()
	rollout, err := st.GetRecommendationRollout(ctx, rolloutID)
	if err != nil {
		t.Fatal("read rollout snapshot for stale CAS proof failed")
	}
	operations, err := st.ListRecommendationRolloutOperations(ctx, rolloutID)
	if err != nil {
		t.Fatal("read operation snapshot for stale CAS proof failed")
	}
	var operation contracts.RecommendationRolloutOperation
	for i := range operations {
		if operations[i].ID == operationID {
			operation = operations[i]
			break
		}
	}
	if operation.ID == "" {
		t.Fatal("expected stale operation is absent")
	}
	plan, err := st.GetRoutePlan(ctx, rollout.State.PlanID)
	if err != nil {
		t.Fatal("read route plan snapshot for stale CAS proof failed")
	}
	return ui17StaleCASSnapshot{operation: operation, rollout: rollout, plan: plan}
}

func ui17RequireUnchangedStaleCASSnapshot(t *testing.T, before, after ui17StaleCASSnapshot, action string) {
	t.Helper()
	if !reflect.DeepEqual(before.operation, after.operation) ||
		!reflect.DeepEqual(before.rollout, after.rollout) ||
		!reflect.DeepEqual(before.plan, after.plan) {
		t.Fatalf("stale %s CAS mutated operation, rollout, or route plan state", action)
	}
}
