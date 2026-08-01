package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"e2m.local/contracts"
	"github.com/jackc/pgx/v5"
)

const hybridGatewayBindingColumns = `id,user_id,instance_id,resource_class,connector_id,credential_binding_id,
	remote_account_id,virtual_key_id,virtual_key_version,status,error_code,version,created_at,updated_at`

func scanHybridGatewayBinding(row rowScanner) (contracts.HybridGatewayBinding, error) {
	var binding contracts.HybridGatewayBinding
	err := row.Scan(&binding.ID, &binding.UserID, &binding.InstanceID, &binding.ResourceClass, &binding.ConnectorID,
		&binding.CredentialBindingID, &binding.RemoteAccountID, &binding.VirtualKeyID, &binding.VirtualKeyVersion,
		&binding.Status, &binding.ErrorCode, &binding.Version, &binding.CreatedAt, &binding.UpdatedAt)
	return binding, err
}

func (s *PostgresStore) GetHybridGatewayBinding(ctx context.Context, userID int64, instanceID string, class contracts.ResourceClass) (contracts.HybridGatewayBinding, error) {
	if userID <= 0 || strings.TrimSpace(instanceID) == "" || !class.IsPlatformSupply() {
		return contracts.HybridGatewayBinding{}, ErrInvalid
	}
	binding, err := scanHybridGatewayBinding(s.pool.QueryRow(ctx, `SELECT `+hybridGatewayBindingColumns+`
		FROM hybrid_gateway_bindings WHERE user_id=$1 AND instance_id=$2 AND resource_class=$3`, userID, strings.TrimSpace(instanceID), string(class)))
	if err != nil {
		return contracts.HybridGatewayBinding{}, mapNotFound(err)
	}
	return binding, nil
}

func (s *PostgresStore) ListHybridGatewayBindings(ctx context.Context, userID int64, instanceID string) ([]contracts.HybridGatewayBinding, error) {
	if userID <= 0 || strings.TrimSpace(instanceID) == "" {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT `+hybridGatewayBindingColumns+`
		FROM hybrid_gateway_bindings WHERE user_id=$1 AND instance_id=$2 ORDER BY resource_class`, userID, strings.TrimSpace(instanceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contracts.HybridGatewayBinding{}
	for rows.Next() {
		binding, err := scanHybridGatewayBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, binding)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpsertHybridGatewayBinding(ctx context.Context, input contracts.HybridGatewayBinding, expectedVersion int64) (contracts.HybridGatewayBinding, error) {
	input.InstanceID = strings.TrimSpace(input.InstanceID)
	input.ConnectorID = strings.TrimSpace(input.ConnectorID)
	input.CredentialBindingID = strings.TrimSpace(input.CredentialBindingID)
	input.RemoteAccountID = strings.TrimSpace(input.RemoteAccountID)
	input.VirtualKeyID = strings.TrimSpace(input.VirtualKeyID)
	if input.ID == "" {
		input.ID = newID("hbind")
	}
	if expectedVersion < 0 || !contracts.ValidHybridGatewayBinding(input) {
		return contracts.HybridGatewayBinding{}, ErrInvalid
	}
	var binding contracts.HybridGatewayBinding
	var err error
	if expectedVersion == 0 {
		binding, err = scanHybridGatewayBinding(s.pool.QueryRow(ctx, `INSERT INTO hybrid_gateway_bindings
			(id,user_id,instance_id,resource_class,connector_id,credential_binding_id,remote_account_id,virtual_key_id,
			 virtual_key_version,status,error_code,version)
			SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,1
			FROM instances i JOIN connectors c ON c.connector_id=$5 AND c.user_id=i.user_id AND c.instance_id=i.id
			JOIN virtual_keys k ON k.id=$8 AND k.user_id=i.user_id AND k.instance_id=i.id AND k.resource_class=$4
			WHERE i.id=$3 AND i.user_id=$2 AND i.connector_id=$5 AND k.key_version=$9
			ON CONFLICT(instance_id,resource_class) DO NOTHING RETURNING `+hybridGatewayBindingColumns,
			input.ID, input.UserID, input.InstanceID, string(input.ResourceClass), input.ConnectorID, input.CredentialBindingID,
			input.RemoteAccountID, input.VirtualKeyID, input.VirtualKeyVersion, string(input.Status), input.ErrorCode))
	} else {
		binding, err = scanHybridGatewayBinding(s.pool.QueryRow(ctx, `UPDATE hybrid_gateway_bindings b SET
			connector_id=$5,credential_binding_id=$6,remote_account_id=$7,virtual_key_id=$8,virtual_key_version=$9,
			status=$10,error_code=$11,version=b.version+1,updated_at=statement_timestamp()
			FROM instances i,connectors c,virtual_keys k
			WHERE b.id=$1 AND b.user_id=$2 AND b.instance_id=$3 AND b.resource_class=$4 AND b.version=$12
			  AND i.id=b.instance_id AND i.user_id=b.user_id AND i.connector_id=$5
			  AND c.connector_id=$5 AND c.user_id=b.user_id AND c.instance_id=b.instance_id
			  AND k.id=$8 AND k.user_id=b.user_id AND k.instance_id=b.instance_id AND k.resource_class=b.resource_class AND k.key_version=$9
			RETURNING `+prefixedHybridGatewayBindingColumns("b"), input.ID, input.UserID, input.InstanceID, string(input.ResourceClass),
			input.ConnectorID, input.CredentialBindingID, input.RemoteAccountID, input.VirtualKeyID, input.VirtualKeyVersion,
			string(input.Status), input.ErrorCode, expectedVersion))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.HybridGatewayBinding{}, ErrConflict
	}
	if isUniqueViolation(err) {
		return contracts.HybridGatewayBinding{}, ErrDuplicate
	}
	return binding, err
}

func prefixedHybridGatewayBindingColumns(alias string) string {
	parts := strings.Split(hybridGatewayBindingColumns, ",")
	for index := range parts {
		parts[index] = alias + "." + strings.TrimSpace(parts[index])
	}
	return strings.Join(parts, ",")
}

const hybridRoutingExecutionColumns = `id,user_id,instance_id,allocation_version,generation,model,status,target,effective,actual,
	desired_weights,adjustment_codes,error_code,lease_owner,lease_until,attempts,version,created_at,updated_at,completed_at`

func scanHybridRoutingExecution(row rowScanner) (contracts.HybridRoutingExecution, error) {
	var execution contracts.HybridRoutingExecution
	var target, effective, actual, weights, codes []byte
	err := row.Scan(&execution.ID, &execution.UserID, &execution.InstanceID, &execution.AllocationVersion, &execution.Generation,
		&execution.Model, &execution.Status, &target, &effective, &actual, &weights, &codes, &execution.ErrorCode,
		&execution.LeaseOwner, &execution.LeaseUntil, &execution.Attempts, &execution.Version, &execution.CreatedAt,
		&execution.UpdatedAt, &execution.CompletedAt)
	if err != nil {
		return contracts.HybridRoutingExecution{}, err
	}
	for raw, destination := range map[*[]byte]any{&target: &execution.Target, &effective: &execution.Effective, &actual: &execution.Actual,
		&weights: &execution.DesiredWeights, &codes: &execution.AdjustmentCodes} {
		if len(*raw) > 0 && string(*raw) != "null" {
			if err := json.Unmarshal(*raw, destination); err != nil {
				return contracts.HybridRoutingExecution{}, err
			}
		}
	}
	return execution, nil
}

func prefixedHybridRoutingExecutionColumns(alias string) string {
	parts := strings.Split(hybridRoutingExecutionColumns, ",")
	for index := range parts {
		parts[index] = alias + "." + strings.TrimSpace(parts[index])
	}
	return strings.Join(parts, ",")
}

func (s *PostgresStore) CreateHybridRoutingExecution(ctx context.Context, input contracts.HybridRoutingExecution) (contracts.HybridRoutingExecution, error) {
	input.InstanceID, input.Model = strings.TrimSpace(input.InstanceID), strings.TrimSpace(input.Model)
	if !validHybridExecutionCreate(input) {
		return contracts.HybridRoutingExecution{}, ErrInvalid
	}
	if input.ID == "" {
		input.ID = newID("hyexec")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return contracts.HybridRoutingExecution{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var allocationVersion, generation int64
	if err := tx.QueryRow(ctx, `SELECT version,routing_generation FROM hybrid_allocations
		WHERE instance_id=$1 AND user_id=$2 FOR UPDATE`, input.InstanceID, input.UserID).Scan(&allocationVersion, &generation); err != nil {
		return contracts.HybridRoutingExecution{}, mapNotFound(err)
	}
	if allocationVersion != input.AllocationVersion {
		return contracts.HybridRoutingExecution{}, ErrConflict
	}
	var executing bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM connector_tasks WHERE execution_scope=$1 AND
		instance_id=$2 AND execution_generation=$3 AND status='executing')`, contracts.HybridRoutingExecutionScope,
		input.InstanceID, generation).Scan(&executing); err != nil {
		return contracts.HybridRoutingExecution{}, err
	}
	if executing {
		return contracts.HybridRoutingExecution{}, ErrConflict
	}
	generation++
	if _, err := tx.Exec(ctx, `UPDATE hybrid_allocations SET routing_generation=$2,updated_at=statement_timestamp()
		WHERE instance_id=$1`, input.InstanceID, generation); err != nil {
		return contracts.HybridRoutingExecution{}, err
	}
	execution, err := scanHybridRoutingExecution(tx.QueryRow(ctx, `INSERT INTO hybrid_routing_executions
		(id,user_id,instance_id,allocation_version,generation,model,status,target,effective,actual,desired_weights,
		 adjustment_codes,error_code,lease_owner,lease_until,attempts,version)
		VALUES ($1,$2,$3,$4,$5,$6,'pending','{}'::jsonb,'{}'::jsonb,'{}'::jsonb,'[]'::jsonb,'[]'::jsonb,'','',NULL,0,1)
		RETURNING `+hybridRoutingExecutionColumns, input.ID, input.UserID, input.InstanceID, input.AllocationVersion, generation, input.Model))
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.HybridRoutingExecution{}, ErrConflict
		}
		return contracts.HybridRoutingExecution{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.HybridRoutingExecution{}, err
	}
	return execution, nil
}

func (s *PostgresStore) GetHybridRoutingExecution(ctx context.Context, userID int64, id string) (contracts.HybridRoutingExecution, error) {
	execution, err := scanHybridRoutingExecution(s.pool.QueryRow(ctx, `SELECT `+hybridRoutingExecutionColumns+`
		FROM hybrid_routing_executions WHERE id=$1 AND user_id=$2`, strings.TrimSpace(id), userID))
	if err != nil {
		return contracts.HybridRoutingExecution{}, mapNotFound(err)
	}
	return execution, nil
}

func (s *PostgresStore) ListHybridRoutingExecutions(ctx context.Context, userID int64, instanceID string, limit int) ([]contracts.HybridRoutingExecution, error) {
	if userID <= 0 || strings.TrimSpace(instanceID) == "" || limit < 0 {
		return nil, ErrInvalid
	}
	if limit == 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT `+hybridRoutingExecutionColumns+` FROM hybrid_routing_executions
		WHERE user_id=$1 AND instance_id=$2 ORDER BY created_at DESC,id DESC LIMIT $3`, userID, strings.TrimSpace(instanceID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contracts.HybridRoutingExecution{}
	for rows.Next() {
		execution, err := scanHybridRoutingExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, execution)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ClaimHybridRoutingExecution(ctx context.Context, workerID string, leaseDuration time.Duration) (contracts.HybridRoutingExecution, bool, error) {
	workerID = strings.TrimSpace(workerID)
	if !validHybridWorkerID(workerID) || leaseDuration <= 0 {
		return contracts.HybridRoutingExecution{}, false, ErrInvalid
	}
	execution, err := scanHybridRoutingExecution(s.pool.QueryRow(ctx, `WITH candidate AS (
		SELECT execution.id FROM hybrid_routing_executions execution
		JOIN hybrid_allocations allocation ON allocation.instance_id=execution.instance_id
		WHERE (execution.status='pending' OR (execution.status='applying' AND execution.lease_until<=statement_timestamp()))
		  AND allocation.routing_generation=execution.generation
		ORDER BY execution.updated_at,execution.id FOR UPDATE OF execution SKIP LOCKED LIMIT 1
	) UPDATE hybrid_routing_executions execution SET status='applying',attempts=execution.attempts+1,
		lease_owner=$1,lease_until=statement_timestamp()+($2::bigint * interval '1 microsecond'),
		version=execution.version+1,updated_at=statement_timestamp()
	FROM candidate WHERE execution.id=candidate.id RETURNING `+prefixedHybridRoutingExecutionColumns("execution"), workerID, leaseDuration.Microseconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.HybridRoutingExecution{}, false, nil
	}
	return execution, err == nil, err
}

func (s *PostgresStore) RenewHybridRoutingExecution(ctx context.Context, id, workerID string, expectedVersion int64, leaseDuration time.Duration) (contracts.HybridRoutingExecution, error) {
	workerID = strings.TrimSpace(workerID)
	if strings.TrimSpace(id) == "" || !validHybridWorkerID(workerID) || expectedVersion <= 0 || leaseDuration <= 0 {
		return contracts.HybridRoutingExecution{}, ErrInvalid
	}
	execution, err := scanHybridRoutingExecution(s.pool.QueryRow(ctx, `UPDATE hybrid_routing_executions execution SET
		lease_until=statement_timestamp()+($4::bigint * interval '1 microsecond'),version=execution.version+1,updated_at=statement_timestamp()
	FROM hybrid_allocations allocation WHERE execution.id=$1 AND execution.status='applying' AND execution.lease_owner=$2
		AND execution.version=$3 AND execution.lease_until>statement_timestamp() AND allocation.instance_id=execution.instance_id
		AND allocation.routing_generation=execution.generation RETURNING `+prefixedHybridRoutingExecutionColumns("execution"),
		strings.TrimSpace(id), workerID, expectedVersion, leaseDuration.Microseconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.HybridRoutingExecution{}, ErrConflict
	}
	return execution, err
}

func (s *PostgresStore) PlanHybridRoutingExecution(ctx context.Context, input contracts.HybridRoutingExecutionPlan) (contracts.HybridRoutingExecution, error) {
	input.ID, input.WorkerID = strings.TrimSpace(input.ID), strings.TrimSpace(input.WorkerID)
	desiredPercent, desiredValid := contracts.HybridAccountWeightPercent(input.DesiredWeights)
	if input.ID == "" || !validHybridWorkerID(input.WorkerID) || input.ExpectedVersion <= 0 ||
		!contracts.ValidCompleteHybridPercentMap(input.Target) || !contracts.ValidCompleteHybridPercentMap(input.Effective) ||
		!desiredValid || !contracts.HybridPercentMapsEqual(desiredPercent, input.Effective) ||
		!validHybridAdjustmentCodes(input.AdjustmentCodes) {
		return contracts.HybridRoutingExecution{}, ErrInvalid
	}
	target, _ := json.Marshal(input.Target)
	effective, _ := json.Marshal(input.Effective)
	weights, _ := json.Marshal(input.DesiredWeights)
	codes, _ := json.Marshal(input.AdjustmentCodes)
	execution, err := scanHybridRoutingExecution(s.pool.QueryRow(ctx, `UPDATE hybrid_routing_executions execution SET
		target=$4::jsonb,effective=$5::jsonb,desired_weights=$6::jsonb,adjustment_codes=$7::jsonb,
		version=execution.version+1,updated_at=statement_timestamp()
	FROM hybrid_allocations allocation WHERE execution.id=$1 AND execution.status='applying' AND execution.lease_owner=$2
		AND execution.version=$3 AND execution.lease_until>statement_timestamp() AND execution.desired_weights='[]'::jsonb
		AND allocation.instance_id=execution.instance_id AND allocation.routing_generation=execution.generation
	RETURNING `+prefixedHybridRoutingExecutionColumns("execution"), input.ID, input.WorkerID, input.ExpectedVersion,
		string(target), string(effective), string(weights), string(codes)))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.HybridRoutingExecution{}, ErrConflict
	}
	return execution, err
}

func (s *PostgresStore) CompleteHybridRoutingExecution(ctx context.Context, input contracts.HybridRoutingExecutionCompletion) (contracts.HybridRoutingExecution, error) {
	input.ID, input.WorkerID, input.ErrorCode = strings.TrimSpace(input.ID), strings.TrimSpace(input.WorkerID), strings.TrimSpace(input.ErrorCode)
	valid := input.ID != "" && validHybridWorkerID(input.WorkerID) && input.ExpectedVersion > 0 && len(input.ErrorCode) <= 64 &&
		!contracts.LooksLikeConnectorSensitiveValue(input.ErrorCode)
	if input.Succeeded {
		valid = valid && input.ErrorCode == "" && contracts.ValidHybridAccountWeights(input.ReadBackWeights)
	} else {
		valid = valid && input.ErrorCode != "" && len(input.ReadBackWeights) == 0
	}
	if !valid {
		return contracts.HybridRoutingExecution{}, ErrInvalid
	}
	actual := map[contracts.ResourceClass]int(nil)
	if input.Succeeded {
		var persistedWeights []contracts.HybridAccountWeight
		var raw []byte
		err := s.pool.QueryRow(ctx, `SELECT desired_weights FROM hybrid_routing_executions
			WHERE id=$1 AND status='applying' AND lease_owner=$2 AND version=$3 AND lease_until>statement_timestamp()`,
			input.ID, input.WorkerID, input.ExpectedVersion).Scan(&raw)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return contracts.HybridRoutingExecution{}, ErrConflict
			}
			return contracts.HybridRoutingExecution{}, err
		}
		if err := json.Unmarshal(raw, &persistedWeights); err != nil {
			return contracts.HybridRoutingExecution{}, err
		}
		var actualValid bool
		actual, actualValid = contracts.HybridAccountWeightPercent(input.ReadBackWeights)
		if !actualValid || !contracts.HybridAccountWeightsEqual(input.ReadBackWeights, persistedWeights) {
			return contracts.HybridRoutingExecution{}, ErrInvalid
		}
	}
	actualJSON, _ := json.Marshal(actual)
	status := contracts.HybridRoutingExecutionFailed
	if input.Succeeded {
		status = contracts.HybridRoutingExecutionSucceeded
	}
	execution, err := scanHybridRoutingExecution(s.pool.QueryRow(ctx, `UPDATE hybrid_routing_executions execution SET
		status=$4,actual=$5::jsonb,error_code=$6,lease_owner='',lease_until=NULL,version=execution.version+1,
		updated_at=statement_timestamp(),completed_at=statement_timestamp()
	FROM hybrid_allocations allocation WHERE execution.id=$1 AND execution.status='applying' AND execution.lease_owner=$2
		AND execution.version=$3 AND execution.lease_until>statement_timestamp()
		AND ($4<>'succeeded' OR execution.desired_weights<>'[]'::jsonb)
		AND allocation.instance_id=execution.instance_id AND allocation.routing_generation=execution.generation
	RETURNING `+prefixedHybridRoutingExecutionColumns("execution"), input.ID, input.WorkerID, input.ExpectedVersion,
		string(status), string(actualJSON), input.ErrorCode))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.HybridRoutingExecution{}, ErrConflict
	}
	return execution, err
}
