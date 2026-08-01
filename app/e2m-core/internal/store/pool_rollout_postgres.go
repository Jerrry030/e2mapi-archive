package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"e2m.local/contracts"
)

const poolRolloutColumns = `id, pool_id, scope, user_id, instance_id, enabled,
	rollout, rollout_batch_size, rollout_canary_count, note, created_at, updated_at`

func scanPoolRolloutTarget(row rowScanner) (contracts.PoolRolloutTarget, error) {
	var target contracts.PoolRolloutTarget
	var scope, rollout string
	var instanceID *string
	if err := row.Scan(
		&target.ID, &target.PoolID, &scope, &target.UserID, &instanceID,
		&target.Enabled, &rollout, &target.RolloutBatchSize, &target.RolloutCanaryCount,
		&target.Note, &target.CreatedAt, &target.UpdatedAt,
	); err != nil {
		return contracts.PoolRolloutTarget{}, err
	}
	target.Scope = contracts.PoolRolloutScope(scope)
	target.Rollout = contracts.RolloutMode(rollout)
	if instanceID != nil {
		target.InstanceID = *instanceID
	}
	return target, nil
}

func (s *PostgresStore) UpsertPoolRolloutTarget(ctx context.Context, input contracts.PoolRolloutTarget) (contracts.PoolRolloutTarget, error) {
	input = normalizePoolRolloutTarget(input)
	if !validPoolRolloutTarget(input) {
		return contracts.PoolRolloutTarget{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.PoolRolloutTarget{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if input.Scope == contracts.PoolRolloutScopeInstance {
		var owned bool
		if err := tx.QueryRow(ctx,
			`SELECT TRUE FROM instances WHERE id=$1 AND user_id=$2 FOR SHARE`,
			input.InstanceID, input.UserID,
		).Scan(&owned); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return contracts.PoolRolloutTarget{}, ErrInvalid
			}
			return contracts.PoolRolloutTarget{}, err
		}
		if !owned {
			return contracts.PoolRolloutTarget{}, ErrInvalid
		}
	}
	if input.ID == "" {
		input.ID = newID("rollout")
	}
	instanceID := any(input.InstanceID)
	conflict := `(pool_id,instance_id) WHERE scope='instance'`
	if input.Scope == contracts.PoolRolloutScopeUser {
		instanceID = nil
		conflict = `(pool_id,user_id) WHERE scope='user'`
	}
	target, err := scanPoolRolloutTarget(tx.QueryRow(ctx,
		`INSERT INTO pool_rollout_targets
		 (id,pool_id,scope,user_id,instance_id,enabled,rollout,rollout_batch_size,rollout_canary_count,note,created_at,updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,statement_timestamp(),statement_timestamp())
		 ON CONFLICT `+conflict+` DO UPDATE SET
		   enabled=EXCLUDED.enabled, rollout=EXCLUDED.rollout,
		   rollout_batch_size=EXCLUDED.rollout_batch_size,
		   rollout_canary_count=EXCLUDED.rollout_canary_count,
		   note=EXCLUDED.note, updated_at=statement_timestamp()
		 RETURNING `+poolRolloutColumns,
		input.ID, input.PoolID, string(input.Scope), input.UserID, instanceID,
		input.Enabled, string(input.Rollout), input.RolloutBatchSize,
		input.RolloutCanaryCount, input.Note))
	if err != nil {
		if isPoolRolloutForeignKeyViolation(err) {
			return contracts.PoolRolloutTarget{}, ErrInvalid
		}
		return contracts.PoolRolloutTarget{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.PoolRolloutTarget{}, err
	}
	return target, nil
}

func isPoolRolloutForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func (s *PostgresStore) DeletePoolRolloutTarget(ctx context.Context, poolID string, scope contracts.PoolRolloutScope, userID int64, instanceID string) error {
	poolID, instanceID = strings.TrimSpace(poolID), strings.TrimSpace(instanceID)
	if poolID == "" || !scope.Valid() || userID <= 0 {
		return ErrInvalid
	}
	result, err := s.pool.Exec(ctx,
		`DELETE FROM pool_rollout_targets WHERE pool_id=$1 AND scope=$2 AND user_id=$3
		   AND (($2='user' AND instance_id IS NULL) OR ($2='instance' AND instance_id=$4))`,
		poolID, string(scope), userID, instanceID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListPoolRolloutTargets(ctx context.Context, poolID string) ([]contracts.PoolRolloutTarget, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+poolRolloutColumns+` FROM pool_rollout_targets
		 WHERE ($1='' OR pool_id=$1) ORDER BY scope,user_id,instance_id`, strings.TrimSpace(poolID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.PoolRolloutTarget, 0)
	for rows.Next() {
		target, scanErr := scanPoolRolloutTarget(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, target)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ResolvePoolRollout(ctx context.Context, poolID string, userID int64, instanceID string) (contracts.PoolRolloutResolution, error) {
	poolID, instanceID = strings.TrimSpace(poolID), strings.TrimSpace(instanceID)
	if poolID == "" || userID <= 0 || instanceID == "" {
		return contracts.PoolRolloutResolution{}, ErrInvalid
	}
	var userDeactivationRequestedAt *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT deactivation_requested_at FROM users WHERE id=$1`, userID).Scan(&userDeactivationRequestedAt); err != nil {
		return contracts.PoolRolloutResolution{}, mapNotFound(err)
	}
	target, err := scanPoolRolloutTarget(s.pool.QueryRow(ctx,
		`SELECT target.id,target.pool_id,target.scope,target.user_id,target.instance_id,
		        (target.enabled AND owner.enabled AND 'client'=ANY(owner.roles) AND pool.status='active'),
		        target.rollout,target.rollout_batch_size,target.rollout_canary_count,
		        target.note,target.created_at,target.updated_at
		 FROM pool_rollout_targets target
		 JOIN users owner ON owner.id=target.user_id
		 JOIN upstream_pools pool ON pool.id=target.pool_id
		 WHERE target.pool_id=$1 AND target.user_id=$2 AND
		       ((scope='instance' AND instance_id=$3) OR (scope='user' AND instance_id IS NULL))
		 ORDER BY CASE scope WHEN 'instance' THEN 0 ELSE 1 END LIMIT 1`,
		poolID, userID, instanceID))
	if err == nil {
		resolved := rolloutResolution(target, userID, instanceID)
		setPoolRolloutDesiredUpdatedAt(&resolved, userDeactivationRequestedAt)
		return resolved, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.PoolRolloutResolution{}, err
	}
	resolved := contracts.PoolRolloutResolution{
		PoolID: poolID, UserID: userID, InstanceID: instanceID,
		Enabled: false, Rollout: contracts.RolloutImmediate,
	}
	setPoolRolloutDesiredUpdatedAt(&resolved, userDeactivationRequestedAt)
	return resolved, nil
}

const poolRolloutOperationColumns = `id,pool_id,user_id,instance_id,plan_id,target_id,action,status,
	desired_fingerprint,attempts,last_error,version,lease_owner,lease_until,created_at,updated_at`

func scanPoolRolloutOperation(row rowScanner) (contracts.PoolRolloutOperation, error) {
	var operation contracts.PoolRolloutOperation
	var action, status string
	if err := row.Scan(
		&operation.ID, &operation.PoolID, &operation.UserID, &operation.InstanceID,
		&operation.PlanID, &operation.TargetID, &action, &status,
		&operation.DesiredFingerprint, &operation.Attempts, &operation.LastError,
		&operation.Version, &operation.LeaseOwner, &operation.LeaseUntil,
		&operation.CreatedAt, &operation.UpdatedAt,
	); err != nil {
		return contracts.PoolRolloutOperation{}, err
	}
	operation.Action = contracts.PoolRolloutOperationAction(action)
	operation.Status = contracts.PoolRolloutOperationStatus(status)
	return operation, nil
}

func (s *PostgresStore) EnsurePoolRolloutOperations(ctx context.Context, poolID string) ([]contracts.PoolRolloutOperation, error) {
	poolID = strings.TrimSpace(poolID)
	if poolID == "" {
		return nil, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var poolStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM upstream_pools WHERE id=$1 FOR SHARE`, poolID).Scan(&poolStatus); err != nil {
		return nil, mapNotFound(err)
	}
	rows, err := tx.Query(ctx,
		`SELECT i.id,i.user_id,
		        COALESCE(p.id,''),COALESCE(p.status,''),COALESCE(p.rollout,'immediate'),
		        COALESCE(p.rollout_batch_size,0),COALESCE(p.rollout_canary_count,0),
		        COALESCE(p.labels->>'managed_by',''),
		        COALESCE(t.id,''),(COALESCE(t.enabled,FALSE) AND u.enabled AND 'client'=ANY(u.roles) AND $2='active'),
		        COALESCE(t.rollout,'immediate'),COALESCE(t.rollout_batch_size,0),
		        COALESCE(t.rollout_canary_count,0),t.updated_at,u.deactivation_requested_at
		 FROM instances i
		 JOIN users u ON u.id=i.user_id
		 LEFT JOIN route_plans p ON p.instance_id=i.id AND p.pool_id=$1
		 LEFT JOIN LATERAL (
		   SELECT target.* FROM pool_rollout_targets target
		    WHERE target.pool_id=$1 AND target.user_id=i.user_id
		      AND ((target.scope='instance' AND target.instance_id=i.id) OR target.scope='user')
		    ORDER BY CASE target.scope WHEN 'instance' THEN 0 ELSE 1 END LIMIT 1
		 ) t ON TRUE
		 ORDER BY i.id`, poolID, poolStatus)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		instanceID                      string
		userID                          int64
		planID, planStatus, planRollout string
		planBatch, planCanary           int
		planManagedBy                   string
		targetID                        string
		enabled                         bool
		rollout                         string
		batch, canary                   int
		targetUpdated                   *time.Time
		userDeactivationRequested       *time.Time
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.instanceID, &c.userID, &c.planID, &c.planStatus, &c.planRollout,
			&c.planBatch, &c.planCanary, &c.planManagedBy,
			&c.targetID, &c.enabled, &c.rollout, &c.batch, &c.canary, &c.targetUpdated,
			&c.userDeactivationRequested); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	created := make([]contracts.PoolRolloutOperation, 0)
	for _, c := range candidates {
		resolution := contracts.PoolRolloutResolution{
			PoolID: poolID, UserID: c.userID, InstanceID: c.instanceID,
			Enabled: c.enabled, TargetID: c.targetID, TargetUpdatedAt: c.targetUpdated,
			Rollout: contracts.RolloutMode(c.rollout), RolloutBatchSize: c.batch, RolloutCanaryCount: c.canary,
		}
		resolution.DesiredUpdatedAt = c.targetUpdated
		setPoolRolloutDesiredUpdatedAt(&resolution, c.userDeactivationRequested)
		action := contracts.PoolRolloutOperationPublish
		if !c.enabled {
			if c.planID == "" {
				continue
			}
			if c.planStatus == string(contracts.RoutePlanSuspended) {
				var unrevoked bool
				if err := tx.QueryRow(ctx,
					`SELECT EXISTS (SELECT 1 FROM published_bindings WHERE plan_id=$1 AND state<>$2)`,
					c.planID, string(contracts.BindingRevoked)).Scan(&unrevoked); err != nil {
					return nil, err
				}
				if !unrevoked {
					continue
				}
			}
			action = contracts.PoolRolloutOperationDrain
		} else if c.planID == "" {
			// Onboarding creates the plan and owns its first publish. Waiting here
			// avoids a failed operation being reclaimed every worker interval.
			continue
		} else if c.planManagedBy == "e2m-onboarding" {
			continue
		} else if c.planID != "" && c.planStatus == string(contracts.RoutePlanPublished) &&
			c.planRollout == c.rollout && c.planBatch == c.batch && c.planCanary == c.canary {
			continue
		}
		if action == contracts.PoolRolloutOperationPublish && poolStatus != string(contracts.UpstreamPoolActive) {
			continue
		}
		fingerprint := PoolRolloutOperationFingerprint(resolution, action, c.planID)
		operation, insertErr := scanPoolRolloutOperation(tx.QueryRow(ctx,
			`INSERT INTO pool_rollout_operations
			 (id,pool_id,user_id,instance_id,plan_id,target_id,action,status,desired_fingerprint,
			  attempts,last_error,version,lease_owner,lease_until,created_at,updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,'pending',$8,0,'',1,'',NULL,
			         statement_timestamp(),statement_timestamp())
			 ON CONFLICT (desired_fingerprint) DO NOTHING
			 RETURNING `+poolRolloutOperationColumns,
			newID("rolloutop"), poolID, c.userID, c.instanceID, c.planID, c.targetID, string(action), fingerprint))
		if insertErr != nil {
			if errors.Is(insertErr, pgx.ErrNoRows) {
				if action == contracts.PoolRolloutOperationDrain {
					if _, err := tx.Exec(ctx,
						`UPDATE pool_rollout_operations
						 SET status='pending', last_error='', version=version+1,
						     lease_owner='', lease_until=NULL, updated_at=statement_timestamp()
						 WHERE desired_fingerprint=$1 AND status='failed'`, fingerprint); err != nil {
						return nil, err
					}
				}
				continue
			}
			return nil, insertErr
		}
		if _, err := tx.Exec(ctx,
			`UPDATE pool_rollout_operations SET status='superseded',version=version+1,
			        lease_owner='',lease_until=NULL,updated_at=statement_timestamp()
			  WHERE pool_id=$1 AND instance_id=$2 AND id<>$3 AND status IN ('pending','failed')`,
			poolID, c.instanceID, operation.ID); err != nil {
			return nil, err
		}
		created = append(created, operation)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return created, nil
}

func (s *PostgresStore) GuardPoolRolloutPublish(ctx context.Context, id, workerID string, expectedVersion int64, lease time.Duration) (contracts.PoolRolloutOperation, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(workerID) == "" || expectedVersion <= 0 || lease <= 0 {
		return contracts.PoolRolloutOperation{}, ErrInvalid
	}
	operation, err := scanPoolRolloutOperation(s.pool.QueryRow(ctx,
		`UPDATE pool_rollout_operations op SET version=op.version+1,
		        lease_until=statement_timestamp()+($4::bigint * interval '1 microsecond'),
		        updated_at=statement_timestamp()
		  FROM upstream_pools pool
		 WHERE op.id=$1 AND op.status='running' AND op.action='publish'
		   AND op.lease_owner=$2 AND op.version=$3 AND op.lease_until>statement_timestamp()
		   AND pool.id=op.pool_id AND pool.status='active'
		 RETURNING `+prefixedPoolRolloutOperationColumns("op"),
		id, workerID, expectedVersion, lease.Microseconds()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.PoolRolloutOperation{}, ErrConflict
		}
		return contracts.PoolRolloutOperation{}, err
	}
	return operation, nil
}

func (s *PostgresStore) ClaimPoolRolloutOperation(ctx context.Context, workerID string, lease time.Duration) (contracts.PoolRolloutOperation, bool, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || lease <= 0 {
		return contracts.PoolRolloutOperation{}, false, ErrInvalid
	}
	operation, err := scanPoolRolloutOperation(s.pool.QueryRow(ctx,
		`WITH candidate AS (
		   SELECT op.id FROM pool_rollout_operations op
		   JOIN upstream_pools pool ON pool.id=op.pool_id
		    WHERE (op.status IN ('pending','failed') OR
		          (op.status='running' AND (op.lease_until IS NULL OR op.lease_until<=statement_timestamp())))
		      AND (op.action<>'publish' OR pool.status='active')
		    ORDER BY op.updated_at,op.id FOR UPDATE OF op SKIP LOCKED LIMIT 1
		 )
		 UPDATE pool_rollout_operations op SET status='running',attempts=op.attempts+1,
		        version=op.version+1,lease_owner=$1,
		        lease_until=statement_timestamp()+($2::bigint * interval '1 microsecond'),
		        updated_at=statement_timestamp()
		 FROM candidate WHERE op.id=candidate.id RETURNING `+prefixedPoolRolloutOperationColumns("op"),
		workerID, lease.Microseconds()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.PoolRolloutOperation{}, false, nil
		}
		return contracts.PoolRolloutOperation{}, false, err
	}
	return operation, true, nil
}

func (s *PostgresStore) GetPoolRolloutOperation(ctx context.Context, id string) (contracts.PoolRolloutOperation, error) {
	operation, err := scanPoolRolloutOperation(s.pool.QueryRow(ctx,
		`SELECT `+poolRolloutOperationColumns+` FROM pool_rollout_operations WHERE id=$1`, strings.TrimSpace(id)))
	if err != nil {
		return contracts.PoolRolloutOperation{}, mapNotFound(err)
	}
	return operation, nil
}

func (s *PostgresStore) RenewPoolRolloutOperation(ctx context.Context, id, workerID string, expectedVersion int64, lease time.Duration) (contracts.PoolRolloutOperation, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(workerID) == "" || expectedVersion <= 0 || lease <= 0 {
		return contracts.PoolRolloutOperation{}, ErrInvalid
	}
	operation, err := scanPoolRolloutOperation(s.pool.QueryRow(ctx,
		`UPDATE pool_rollout_operations SET version=version+1,
		        lease_until=statement_timestamp()+($4::bigint * interval '1 microsecond'),
		        updated_at=statement_timestamp()
		  WHERE id=$1 AND status='running' AND lease_owner=$2 AND version=$3
		    AND lease_until>statement_timestamp()
		  RETURNING `+poolRolloutOperationColumns,
		id, workerID, expectedVersion, lease.Microseconds()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.PoolRolloutOperation{}, ErrConflict
		}
		return contracts.PoolRolloutOperation{}, err
	}
	return operation, nil
}

func prefixedPoolRolloutOperationColumns(alias string) string {
	parts := strings.Split(poolRolloutOperationColumns, ",")
	for i := range parts {
		parts[i] = alias + "." + strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ",")
}

func (s *PostgresStore) CompletePoolRolloutOperation(ctx context.Context, id, workerID string, expectedVersion int64, status contracts.PoolRolloutOperationStatus, lastError string) (contracts.PoolRolloutOperation, error) {
	if status != contracts.PoolRolloutOperationSucceeded && status != contracts.PoolRolloutOperationFailed && status != contracts.PoolRolloutOperationSuperseded {
		return contracts.PoolRolloutOperation{}, ErrInvalid
	}
	operation, err := scanPoolRolloutOperation(s.pool.QueryRow(ctx,
		`UPDATE pool_rollout_operations SET status=$4,last_error=$5,version=version+1,
		        lease_owner='',lease_until=NULL,updated_at=statement_timestamp()
		  WHERE id=$1 AND status='running' AND lease_owner=$2 AND version=$3
		    AND lease_until>statement_timestamp()
		  RETURNING `+poolRolloutOperationColumns,
		id, workerID, expectedVersion, string(status), strings.TrimSpace(lastError)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.PoolRolloutOperation{}, ErrConflict
		}
		return contracts.PoolRolloutOperation{}, err
	}
	return operation, nil
}

func (s *PostgresStore) ListPoolRolloutOperations(ctx context.Context, poolID string) ([]contracts.PoolRolloutOperation, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+poolRolloutOperationColumns+` FROM pool_rollout_operations
		 WHERE ($1='' OR pool_id=$1) ORDER BY updated_at DESC,id`, strings.TrimSpace(poolID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.PoolRolloutOperation, 0)
	for rows.Next() {
		operation, scanErr := scanPoolRolloutOperation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, operation)
	}
	return out, rows.Err()
}
