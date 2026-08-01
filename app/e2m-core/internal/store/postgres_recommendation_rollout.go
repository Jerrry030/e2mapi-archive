package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"e2m.local/contracts"
)

const recommendationRolloutColumns = `id,user_id,plan_id,instance_id,recommendation_id,recommendation_plan_generation,
	recommendation_fingerprint,fact_version,evidence_ids,from_channel_id,to_channel_id,from_account_id,to_account_id,
	baseline_weights,baseline_fingerprint,scheduling_generation,status,stage,pending_stage,observation_seconds,
	recommendation_expires_at,started_at,stage_started_at,observe_until,last_after_evidence,rollback_reasons,
	version,last_operation_id,created_at,updated_at`

const recommendationRolloutOperationColumns = `id,rollout_id,user_id,plan_id,action,target_stage,status,attempts,error_code,
	version,lease_owner,lease_until,created_at,updated_at`

func prefixedRecommendationRolloutColumns(alias string) string {
	return prefixSQLColumns(alias, recommendationRolloutColumns)
}

func prefixedRecommendationRolloutOperationColumns(alias string) string {
	return prefixSQLColumns(alias, recommendationRolloutOperationColumns)
}

func prefixSQLColumns(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i := range parts {
		parts[i] = alias + "." + strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ",")
}

func scanRecommendationRollout(row rowScanner) (contracts.RecommendationRollout, error) {
	var rollout contracts.RecommendationRollout
	var evidence, baseline, lastAfter, rollbackReasons []byte
	var status string
	if err := row.Scan(
		&rollout.State.ID, &rollout.State.UserID, &rollout.State.PlanID, &rollout.InstanceID,
		&rollout.State.RecommendationID, &rollout.RecommendationPlanGeneration,
		&rollout.State.RecommendationFingerprint, &rollout.State.FactVersion, &evidence,
		&rollout.FromChannelID, &rollout.ToChannelID, &rollout.FromAccountID, &rollout.ToAccountID,
		&baseline, &rollout.State.BaselineFingerprint, &rollout.State.SchedulingGeneration,
		&status, &rollout.State.Stage, &rollout.State.PendingStage, &rollout.State.ObservationSeconds,
		&rollout.State.RecommendationExpiresAt, &rollout.State.StartedAt, &rollout.State.StageStartedAt,
		&rollout.State.ObserveUntil, &lastAfter, &rollbackReasons, &rollout.Version,
		&rollout.LastOperationID, &rollout.CreatedAt, &rollout.State.UpdatedAt,
	); err != nil {
		return contracts.RecommendationRollout{}, err
	}
	rollout.State.Status = contracts.RecommendationRolloutStatus(status)
	var baselineMap map[string]int
	if err := json.Unmarshal(evidence, &rollout.State.EvidenceIDs); err != nil ||
		json.Unmarshal(baseline, &baselineMap) != nil ||
		json.Unmarshal(rollbackReasons, &rollout.State.RollbackReasons) != nil {
		return contracts.RecommendationRollout{}, ErrInvalid
	}
	rollout.BaselineWeights = make([]contracts.RecommendationRolloutAccountWeight, 0, len(baselineMap))
	for accountID, weight := range baselineMap {
		rollout.BaselineWeights = append(rollout.BaselineWeights, contracts.RecommendationRolloutAccountWeight{AccountID: accountID, Weight: weight})
	}
	if len(lastAfter) > 0 && string(lastAfter) != "null" {
		var value contracts.RecommendationRolloutAfterEvidence
		if err := json.Unmarshal(lastAfter, &value); err != nil {
			return contracts.RecommendationRollout{}, ErrInvalid
		}
		rollout.State.LastAfterEvidence = &value
	}
	canonical, err := contracts.CanonicalRecommendationRolloutWeights(rollout.BaselineWeights)
	fingerprint, fingerprintErr := contracts.RecommendationRolloutBaselineFingerprint(canonical)
	if err != nil || fingerprintErr != nil || fingerprint != rollout.State.BaselineFingerprint || !validRecommendationRolloutState(rollout.State) {
		return contracts.RecommendationRollout{}, ErrInvalid
	}
	rollout.BaselineWeights = canonical
	return cloneRecommendationRollout(rollout), nil
}

func scanRecommendationRolloutOperation(row rowScanner) (contracts.RecommendationRolloutOperation, error) {
	var operation contracts.RecommendationRolloutOperation
	var action, status, errorCode string
	if err := row.Scan(&operation.ID, &operation.RolloutID, &operation.UserID, &operation.PlanID, &action,
		&operation.TargetStage, &status, &operation.Attempts, &errorCode, &operation.Version,
		&operation.LeaseOwner, &operation.LeaseUntil, &operation.CreatedAt, &operation.UpdatedAt); err != nil {
		return contracts.RecommendationRolloutOperation{}, err
	}
	operation.Action = contracts.RecommendationRolloutOperationAction(action)
	operation.Status = contracts.RecommendationRolloutOperationStatus(status)
	operation.ErrorCode = contracts.RecommendationRolloutOperationErrorCode(errorCode)
	if !validStoredRecommendationRolloutOperation(operation) {
		return contracts.RecommendationRolloutOperation{}, ErrInvalid
	}
	return cloneRecommendationRolloutOperation(operation), nil
}

func recommendationRolloutBindingsPostgres(ctx context.Context, tx pgx.Tx, planID string) ([]recommendationRolloutBindingReference, error) {
	rows, err := tx.Query(ctx, `SELECT binding.instance_id,binding.channel_id,binding.remote_id,binding.account_ownership,
		channel.account_ownership,binding.scheduling_generation,allocation.user_id
		FROM published_bindings AS binding
		LEFT JOIN upstream_channels AS channel ON channel.id=binding.channel_id
		LEFT JOIN upstream_channel_allocations AS allocation ON allocation.channel_id=binding.channel_id
		WHERE binding.plan_id=$1 AND binding.state<>'revoked' AND binding.remote_id<>''
		ORDER BY binding.channel_id,binding.id FOR SHARE OF binding`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bindings := make([]recommendationRolloutBindingReference, 0)
	for rows.Next() {
		var value recommendationRolloutBindingReference
		var bindingOwnership, channelOwnership *string
		var allocationUserID *int64
		if err := rows.Scan(&value.InstanceID, &value.ChannelID, &value.RemoteID, &bindingOwnership, &channelOwnership,
			&value.SchedulingGeneration, &allocationUserID); err != nil {
			return nil, err
		}
		if bindingOwnership != nil {
			value.BindingOwnership = contracts.GatewayAccountOwnership(*bindingOwnership).Normalize()
		}
		if channelOwnership != nil {
			value.ChannelOwnership = contracts.GatewayAccountOwnership(*channelOwnership).Normalize()
		}
		if allocationUserID != nil {
			value.AllocationUserID, value.Allocated = *allocationUserID, true
		}
		bindings = append(bindings, value)
	}
	return bindings, rows.Err()
}

func (s *PostgresStore) CreateRecommendationRollout(ctx context.Context, input contracts.RecommendationRolloutCreate) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, error) {
	rollout := cloneRecommendationRollout(input.Rollout)
	if !validRecommendationRolloutCreate(input, rollout) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentGeneration int64
	var planUserID int64
	var planInstanceID, planStatus string
	if err := tx.QueryRow(ctx, `SELECT user_id,instance_id,status,scheduling_generation FROM route_plans WHERE id=$1 FOR UPDATE`, rollout.State.PlanID).
		Scan(&planUserID, &planInstanceID, &planStatus, &currentGeneration); err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, mapNotFound(err)
	}
	if planUserID != rollout.State.UserID || planInstanceID != rollout.InstanceID || planStatus != string(contracts.RoutePlanPublished) ||
		currentGeneration != input.ExpectedPlanGeneration || rollout.RecommendationPlanGeneration != input.ExpectedPlanGeneration {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
	}
	var recommendationValid bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM upstream_recommendations
		WHERE user_id=$1 AND id=$2 AND fingerprint=$3 AND plan_generation=$4
		  AND from_channel_id=$5 AND to_channel_id=$6)`, rollout.State.UserID, rollout.State.RecommendationID,
		rollout.State.RecommendationFingerprint, rollout.RecommendationPlanGeneration, rollout.FromChannelID, rollout.ToChannelID).Scan(&recommendationValid); err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	bindings, err := recommendationRolloutBindingsPostgres(ctx, tx, rollout.State.PlanID)
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	if !recommendationValid || !recommendationRolloutReferencesValid(rollout, bindings) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
	}

	var newGeneration int64
	if err := tx.QueryRow(ctx, `UPDATE route_plans SET scheduling_generation=scheduling_generation+1,updated_at=statement_timestamp()
		WHERE id=$1 AND scheduling_generation=$2 RETURNING scheduling_generation`, rollout.State.PlanID, input.ExpectedPlanGeneration).Scan(&newGeneration); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
		}
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	if err := supersedeRoutePlanOwnersPostgres(ctx, tx, rollout.State.PlanID, "", newGeneration); err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	if rollout.State.ID == "" {
		rollout.State.ID = newID("rec-rollout")
	}
	rollout.State.SchedulingGeneration = newGeneration
	operationID := newID("rec-rollout-op")
	evidence, baseline, lastAfter, reasons, err := marshalRecommendationRolloutJSON(rollout)
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrInvalid
	}
	stored, err := scanRecommendationRollout(tx.QueryRow(ctx, `INSERT INTO recommendation_rollouts (
		id,user_id,plan_id,instance_id,recommendation_id,recommendation_plan_generation,recommendation_fingerprint,
		fact_version,evidence_ids,from_channel_id,to_channel_id,from_account_id,to_account_id,baseline_weights,
		baseline_fingerprint,scheduling_generation,status,stage,pending_stage,observation_seconds,
		recommendation_expires_at,started_at,stage_started_at,observe_until,last_after_evidence,rollback_reasons,
		version,last_operation_id,created_at,updated_at) VALUES (
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
		$21,$22,$23,$24,$25,$26,1,$27,statement_timestamp(),statement_timestamp()) RETURNING `+recommendationRolloutColumns,
		rollout.State.ID, rollout.State.UserID, rollout.State.PlanID, rollout.InstanceID, rollout.State.RecommendationID,
		rollout.RecommendationPlanGeneration, rollout.State.RecommendationFingerprint, rollout.State.FactVersion,
		evidence, rollout.FromChannelID, rollout.ToChannelID, rollout.FromAccountID, rollout.ToAccountID, baseline,
		rollout.State.BaselineFingerprint, rollout.State.SchedulingGeneration, string(rollout.State.Status), rollout.State.Stage,
		rollout.State.PendingStage, rollout.State.ObservationSeconds, rollout.State.RecommendationExpiresAt, rollout.State.StartedAt,
		rollout.State.StageStartedAt, rollout.State.ObserveUntil, lastAfter, reasons, operationID))
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, mapRecommendationRolloutWriteError(err)
	}
	operation, err := scanRecommendationRolloutOperation(tx.QueryRow(ctx, `INSERT INTO recommendation_rollout_operations
		(id,rollout_id,user_id,plan_id,action,target_stage,status,attempts,error_code,version,lease_owner,lease_until,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,'pending',0,'',1,'',NULL,statement_timestamp(),statement_timestamp())
		RETURNING `+recommendationRolloutOperationColumns,
		operationID, stored.State.ID, stored.State.UserID, stored.State.PlanID, string(input.FirstAction), input.FirstTargetStage))
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, mapRecommendationRolloutWriteError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	return stored, operation, nil
}

func (s *PostgresStore) GetRecommendationRollout(ctx context.Context, id string) (contracts.RecommendationRollout, error) {
	if strings.TrimSpace(id) == "" {
		return contracts.RecommendationRollout{}, ErrInvalid
	}
	rollout, err := scanRecommendationRollout(s.pool.QueryRow(ctx, `SELECT `+recommendationRolloutColumns+` FROM recommendation_rollouts WHERE id=$1`, strings.TrimSpace(id)))
	if err != nil {
		return contracts.RecommendationRollout{}, mapNotFound(err)
	}
	return rollout, nil
}

func (s *PostgresStore) ListRecommendationRollouts(ctx context.Context, filter contracts.RecommendationRolloutFilter) ([]contracts.RecommendationRollout, error) {
	if filter.UserID < 0 || filter.Status != "" && !contracts.IsRecommendationRolloutStatus(filter.Status) || filter.Limit < 0 {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT `+recommendationRolloutColumns+` FROM recommendation_rollouts
		WHERE ($1=0 OR user_id=$1) AND ($2='' OR status=$2) AND ($3='' OR plan_id=$3)
		ORDER BY updated_at DESC,id DESC LIMIT $4`, filter.UserID, string(filter.Status), strings.TrimSpace(filter.PlanID), normalizeRecommendationRolloutLimit(filter.Limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.RecommendationRollout, 0)
	for rows.Next() {
		rollout, scanErr := scanRecommendationRollout(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, rollout)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListRecommendationRolloutOperations(ctx context.Context, rolloutID string) ([]contracts.RecommendationRolloutOperation, error) {
	rolloutID = strings.TrimSpace(rolloutID)
	if rolloutID == "" {
		return nil, ErrInvalid
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recommendation_rollouts WHERE id=$1)`, rolloutID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT `+recommendationRolloutOperationColumns+` FROM recommendation_rollout_operations
		WHERE rollout_id=$1 ORDER BY created_at DESC,id DESC`, rolloutID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.RecommendationRolloutOperation, 0)
	for rows.Next() {
		operation, scanErr := scanRecommendationRolloutOperation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, operation)
	}
	return out, rows.Err()
}

func (s *PostgresStore) TransitionRecommendationRolloutState(ctx context.Context, rolloutID string, expectedVersion int64, next contracts.RecommendationRolloutState) (contracts.RecommendationRollout, error) {
	rolloutID = strings.TrimSpace(rolloutID)
	if rolloutID == "" || expectedVersion <= 0 || !validRecommendationRolloutState(next) {
		return contracts.RecommendationRollout{}, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return contracts.RecommendationRollout{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanRecommendationRollout(tx.QueryRow(ctx, `SELECT `+recommendationRolloutColumns+` FROM recommendation_rollouts WHERE id=$1 FOR UPDATE`, rolloutID))
	if err != nil {
		return contracts.RecommendationRollout{}, mapNotFound(err)
	}
	if current.Version != expectedVersion || !validRecommendationRolloutObservationTransition(current.State, next) {
		return contracts.RecommendationRollout{}, ErrConflict
	}
	if err := verifyRecommendationRolloutGenerationPostgres(ctx, tx, current); err != nil {
		return contracts.RecommendationRollout{}, err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recommendation_rollout_operations WHERE rollout_id=$1 AND status IN ('pending','running'))`, rolloutID).Scan(&active); err != nil {
		return contracts.RecommendationRollout{}, err
	}
	if active {
		return contracts.RecommendationRollout{}, ErrConflict
	}
	updated, err := updateRecommendationRolloutStatePostgres(ctx, tx, current, next, current.LastOperationID)
	if err != nil {
		return contracts.RecommendationRollout{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.RecommendationRollout{}, err
	}
	return updated, nil
}

func (s *PostgresStore) EnqueueRecommendationRolloutOperation(ctx context.Context, rolloutID string, expectedVersion int64, next contracts.RecommendationRolloutState, action contracts.RecommendationRolloutOperationAction, target contracts.RecommendationRolloutStage) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, error) {
	if strings.TrimSpace(rolloutID) == "" || expectedVersion <= 0 || !validRecommendationRolloutOperationShape(action, target) || !validRecommendationRolloutState(next) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanRecommendationRollout(tx.QueryRow(ctx, `SELECT `+recommendationRolloutColumns+` FROM recommendation_rollouts WHERE id=$1 FOR UPDATE`, rolloutID))
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, mapNotFound(err)
	}
	if current.Version != expectedVersion || !validRecommendationRolloutEnqueueTransition(current.State, next, action, target) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
	}
	var active, activeForwardOnly bool
	if err := tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM recommendation_rollout_operations WHERE rollout_id=$1 AND status IN ('pending','running')),
		NOT EXISTS(SELECT 1 FROM recommendation_rollout_operations WHERE rollout_id=$1 AND status IN ('pending','running') AND action<>'apply_stage')`, rolloutID).Scan(&active, &activeForwardOnly); err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	if active && (action != contracts.RecommendationRolloutOperationRollback || !activeForwardOnly) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
	}
	if active {
		// This transaction owns the rollout row. Supersede every active forward
		// operation before taking a new plan generation so stale workers lose both
		// their lease identity and their rollout/generation CAS in one commit.
		if _, err := tx.Exec(ctx, `UPDATE recommendation_rollout_operations SET status='superseded',error_code='',
			version=version+1,lease_owner='',lease_until=NULL,updated_at=statement_timestamp()
			WHERE rollout_id=$1 AND action='apply_stage' AND status IN ('pending','running')`, rolloutID); err != nil {
			return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
		}
	}
	newGeneration, err := claimRecommendationRolloutGenerationPostgres(ctx, tx, current, action)
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	next.SchedulingGeneration = newGeneration
	if next.LastAfterEvidence != nil {
		next.LastAfterEvidence.SchedulingGeneration = newGeneration
	}
	operationID := newID("rec-rollout-op")
	updated, err := updateRecommendationRolloutStateAndGenerationPostgres(ctx, tx, current, next, operationID)
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	operation, err := scanRecommendationRolloutOperation(tx.QueryRow(ctx, `INSERT INTO recommendation_rollout_operations
		(id,rollout_id,user_id,plan_id,action,target_stage,status,attempts,error_code,version,lease_owner,lease_until,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,'pending',0,'',1,'',NULL,statement_timestamp(),statement_timestamp()) RETURNING `+recommendationRolloutOperationColumns,
		operationID, rolloutID, current.State.UserID, current.State.PlanID, string(action), target))
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, mapRecommendationRolloutWriteError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	return updated, operation, nil
}

func (s *PostgresStore) ClaimRecommendationRolloutOperation(ctx context.Context, workerID string, lease time.Duration) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, bool, error) {
	workerID = strings.TrimSpace(workerID)
	if !validRecommendationRolloutWorkerID(workerID) || lease <= 0 {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, false, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation, err := scanRecommendationRolloutOperation(tx.QueryRow(ctx, `WITH candidate AS (
		SELECT operation.id FROM recommendation_rollout_operations operation
		JOIN recommendation_rollouts rollout ON rollout.id=operation.rollout_id
		JOIN route_plans plan ON plan.id=rollout.plan_id
		WHERE (operation.status='pending' OR (operation.status='running' AND operation.lease_until<=statement_timestamp()))
		  AND plan.scheduling_generation=rollout.scheduling_generation
		  AND ((operation.action='apply_stage' AND plan.status='published') OR
		       (operation.action='rollback' AND plan.status IN ('published','suspended')))
		ORDER BY operation.updated_at,operation.id FOR UPDATE OF operation SKIP LOCKED LIMIT 1
	) UPDATE recommendation_rollout_operations operation SET status='running',attempts=operation.attempts+1,error_code='',
		version=operation.version+1,lease_owner=$1,lease_until=statement_timestamp()+($2::bigint * interval '1 microsecond'),
		updated_at=statement_timestamp() FROM candidate WHERE operation.id=candidate.id
	RETURNING `+prefixedRecommendationRolloutOperationColumns("operation"), workerID, lease.Microseconds()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Commit(ctx)
			return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, false, nil
		}
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, false, err
	}
	rollout, err := scanRecommendationRollout(tx.QueryRow(ctx, `SELECT `+recommendationRolloutColumns+` FROM recommendation_rollouts WHERE id=$1 FOR SHARE`, operation.RolloutID))
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, false, err
	}
	return rollout, operation, true, nil
}

func (s *PostgresStore) RenewRecommendationRolloutOperation(ctx context.Context, id, workerID string, expectedVersion int64, lease time.Duration) (contracts.RecommendationRolloutOperation, error) {
	if strings.TrimSpace(id) == "" || !validRecommendationRolloutWorkerID(strings.TrimSpace(workerID)) || expectedVersion <= 0 || lease <= 0 {
		return contracts.RecommendationRolloutOperation{}, ErrInvalid
	}
	operation, err := scanRecommendationRolloutOperation(s.pool.QueryRow(ctx, `UPDATE recommendation_rollout_operations operation
		SET version=operation.version+1,lease_until=statement_timestamp()+($4::bigint * interval '1 microsecond'),updated_at=statement_timestamp()
		FROM recommendation_rollouts rollout,route_plans plan
		WHERE operation.id=$1 AND operation.status='running' AND operation.lease_owner=$2 AND operation.version=$3
		  AND operation.lease_until>statement_timestamp() AND rollout.id=operation.rollout_id AND plan.id=rollout.plan_id
		  AND plan.scheduling_generation=rollout.scheduling_generation
		  AND ((operation.action='apply_stage' AND plan.status='published') OR
		       (operation.action='rollback' AND plan.status IN ('published','suspended')))
	RETURNING `+prefixedRecommendationRolloutOperationColumns("operation"), id, strings.TrimSpace(workerID), expectedVersion, lease.Microseconds()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.RecommendationRolloutOperation{}, ErrConflict
		}
		return contracts.RecommendationRolloutOperation{}, err
	}
	return operation, nil
}

func (s *PostgresStore) CompleteRecommendationRolloutOperation(ctx context.Context, input contracts.RecommendationRolloutCompletion) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, error) {
	if !validRecommendationRolloutCompletion(input) || !validRecommendationRolloutState(input.NextState) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation, err := scanRecommendationRolloutOperation(tx.QueryRow(ctx, `SELECT `+recommendationRolloutOperationColumns+` FROM recommendation_rollout_operations WHERE id=$1 FOR UPDATE`, input.OperationID))
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, mapNotFound(err)
	}
	rollout, err := scanRecommendationRollout(tx.QueryRow(ctx, `SELECT `+recommendationRolloutColumns+` FROM recommendation_rollouts WHERE id=$1 FOR UPDATE`, operation.RolloutID))
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	var leaseLive bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE(lease_until>statement_timestamp(),false) FROM recommendation_rollout_operations WHERE id=$1`, operation.ID).Scan(&leaseLive); err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	if operation.Status != contracts.RecommendationRolloutOperationRunning || operation.LeaseOwner != strings.TrimSpace(input.WorkerID) ||
		operation.Version != input.ExpectedOperationVersion || !leaseLive || rollout.Version != input.ExpectedRolloutVersion ||
		!validRecommendationRolloutCompletionTransition(rollout.State, operation, input) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
	}
	if err := verifyRecommendationRolloutGenerationPostgres(ctx, tx, rollout); err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	updated, err := updateRecommendationRolloutStatePostgres(ctx, tx, rollout, input.NextState, operation.ID)
	if err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	completed, err := scanRecommendationRolloutOperation(tx.QueryRow(ctx, `UPDATE recommendation_rollout_operations SET
		status=$4,error_code=$5,version=version+1,lease_owner='',lease_until=NULL,updated_at=statement_timestamp()
		WHERE id=$1 AND lease_owner=$2 AND version=$3 RETURNING `+recommendationRolloutOperationColumns,
		input.OperationID, strings.TrimSpace(input.WorkerID), input.ExpectedOperationVersion, string(input.OperationStatus), string(input.ErrorCode)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
		}
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	return updated, completed, nil
}

func updateRecommendationRolloutStatePostgres(ctx context.Context, tx pgx.Tx, current contracts.RecommendationRollout, next contracts.RecommendationRolloutState, operationID string) (contracts.RecommendationRollout, error) {
	lastAfter, err := json.Marshal(next.LastAfterEvidence)
	if err != nil {
		return contracts.RecommendationRollout{}, ErrInvalid
	}
	reasons, err := marshalRecommendationRolloutBlockReasons(next.RollbackReasons)
	if err != nil {
		return contracts.RecommendationRollout{}, ErrInvalid
	}
	updated, err := scanRecommendationRollout(tx.QueryRow(ctx, `UPDATE recommendation_rollouts SET
		status=$3,stage=$4,pending_stage=$5,stage_started_at=$6,observe_until=$7,last_after_evidence=$8,
		rollback_reasons=$9,version=version+1,last_operation_id=$10,updated_at=statement_timestamp()
		WHERE id=$1 AND version=$2 RETURNING `+recommendationRolloutColumns,
		current.State.ID, current.Version, string(next.Status), next.Stage, next.PendingStage, next.StageStartedAt,
		next.ObserveUntil, lastAfter, reasons, operationID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.RecommendationRollout{}, ErrConflict
		}
		return contracts.RecommendationRollout{}, mapRecommendationRolloutWriteError(err)
	}
	return updated, nil
}

func updateRecommendationRolloutStateAndGenerationPostgres(ctx context.Context, tx pgx.Tx, current contracts.RecommendationRollout, next contracts.RecommendationRolloutState, operationID string) (contracts.RecommendationRollout, error) {
	lastAfter, err := json.Marshal(next.LastAfterEvidence)
	if err != nil {
		return contracts.RecommendationRollout{}, ErrInvalid
	}
	reasons, err := marshalRecommendationRolloutBlockReasons(next.RollbackReasons)
	if err != nil {
		return contracts.RecommendationRollout{}, ErrInvalid
	}
	updated, err := scanRecommendationRollout(tx.QueryRow(ctx, `UPDATE recommendation_rollouts SET
		scheduling_generation=$3,status=$4,stage=$5,pending_stage=$6,stage_started_at=$7,observe_until=$8,
		last_after_evidence=$9,rollback_reasons=$10,version=version+1,last_operation_id=$11,updated_at=statement_timestamp()
		WHERE id=$1 AND version=$2 RETURNING `+recommendationRolloutColumns,
		current.State.ID, current.Version, next.SchedulingGeneration, string(next.Status), next.Stage, next.PendingStage,
		next.StageStartedAt, next.ObserveUntil, lastAfter, reasons, operationID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.RecommendationRollout{}, ErrConflict
		}
		return contracts.RecommendationRollout{}, mapRecommendationRolloutWriteError(err)
	}
	return updated, nil
}

func claimRecommendationRolloutGenerationPostgres(ctx context.Context, tx pgx.Tx, rollout contracts.RecommendationRollout, action contracts.RecommendationRolloutOperationAction) (int64, error) {
	var generation int64
	query := `UPDATE route_plans SET scheduling_generation=scheduling_generation+1,updated_at=statement_timestamp()
		WHERE id=$1 AND user_id=$2 AND instance_id=$3 AND status='published' AND scheduling_generation=$4
		RETURNING scheduling_generation`
	args := []any{rollout.State.PlanID, rollout.State.UserID, rollout.InstanceID, rollout.State.SchedulingGeneration}
	if action == contracts.RecommendationRolloutOperationRollback {
		query = `UPDATE route_plans SET scheduling_generation=scheduling_generation+1,updated_at=statement_timestamp()
			WHERE id=$1 AND user_id=$2 AND instance_id=$3 AND status IN ('published','suspended')
			RETURNING scheduling_generation`
		args = []any{rollout.State.PlanID, rollout.State.UserID, rollout.InstanceID}
	}
	if err := tx.QueryRow(ctx, query, args...).Scan(&generation); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrConflict
		}
		return 0, err
	}
	if err := supersedeRoutePlanOwnersPostgres(ctx, tx, rollout.State.PlanID, "", generation); err != nil {
		return 0, err
	}
	return generation, nil
}

func verifyRecommendationRolloutGenerationPostgres(ctx context.Context, tx pgx.Tx, rollout contracts.RecommendationRollout) error {
	var owned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM route_plans WHERE id=$1 AND user_id=$2 AND instance_id=$3 AND status='published' AND scheduling_generation=$4)`,
		rollout.State.PlanID, rollout.State.UserID, rollout.InstanceID, rollout.State.SchedulingGeneration).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return ErrConflict
	}
	return nil
}

func marshalRecommendationRolloutJSON(rollout contracts.RecommendationRollout) ([]byte, []byte, []byte, []byte, error) {
	evidenceIDs := rollout.State.EvidenceIDs
	if evidenceIDs == nil {
		evidenceIDs = []string{}
	}
	evidence, err := json.Marshal(evidenceIDs)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	baselineMap := make(map[string]int, len(rollout.BaselineWeights))
	for _, weight := range rollout.BaselineWeights {
		baselineMap[weight.AccountID] = weight.Weight
	}
	baseline, err := json.Marshal(baselineMap)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	lastAfter, err := json.Marshal(rollout.State.LastAfterEvidence)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	reasons, err := marshalRecommendationRolloutBlockReasons(rollout.State.RollbackReasons)
	return evidence, baseline, lastAfter, reasons, err
}

func marshalRecommendationRolloutBlockReasons(reasons []contracts.RecommendationRolloutBlockReason) ([]byte, error) {
	if reasons == nil {
		reasons = []contracts.RecommendationRolloutBlockReason{}
	}
	return json.Marshal(reasons)
}

func validStoredRecommendationRolloutOperation(operation contracts.RecommendationRolloutOperation) bool {
	if strings.TrimSpace(operation.ID) == "" || strings.TrimSpace(operation.RolloutID) == "" || operation.UserID <= 0 || strings.TrimSpace(operation.PlanID) == "" ||
		!validRecommendationRolloutOperationShape(operation.Action, operation.TargetStage) || !contracts.IsRecommendationRolloutOperationStatus(operation.Status) ||
		!contracts.IsRecommendationRolloutOperationErrorCode(operation.ErrorCode) || operation.Attempts < 0 || operation.Version <= 0 || operation.CreatedAt.IsZero() || operation.UpdatedAt.IsZero() {
		return false
	}
	if operation.Status == contracts.RecommendationRolloutOperationRunning {
		return validRecommendationRolloutWorkerID(operation.LeaseOwner) && operation.LeaseUntil != nil && operation.ErrorCode == ""
	}
	return operation.LeaseOwner == "" && operation.LeaseUntil == nil && (operation.Status == contracts.RecommendationRolloutOperationFailed) == (operation.ErrorCode != "")
}

func mapRecommendationRolloutWriteError(err error) error {
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

var _ RecommendationRolloutStore = (*PostgresStore)(nil)
