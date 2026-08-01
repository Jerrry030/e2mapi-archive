package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"e2m.local/contracts"
)

const connectorCols = `connector_id, user_id, instance_id, name, status, token_hash,
	version, protocol_version, gateway_state, last_seen_at, revoked_at,
	created_at, updated_at`

// Migration 0074 deliberately preserves registered protocol-v2 identities so
// their existing token can authenticate exactly one real v3 handshake. New
// enrollments and every task lease/execution edge still require the current
// protocol. Do not broaden this closed set when a future protocol is added:
// each stored-version transition needs its own migration and handshake rules.
func validStoredConnectorProtocolVersion(version int) bool {
	return version == 2 || version == contracts.ConnectorProtocolVersion
}

func scanConnector(row rowScanner) (contracts.Connector, error) {
	var connector contracts.Connector
	var status string
	var gatewayState []byte
	if err := row.Scan(
		&connector.ID, &connector.UserID, &connector.InstanceID, &connector.Name,
		&status, &connector.TokenHash, &connector.Version, &connector.ProtocolVersion,
		&gatewayState, &connector.LastSeenAt, &connector.RevokedAt,
		&connector.CreatedAt, &connector.UpdatedAt,
	); err != nil {
		return contracts.Connector{}, err
	}
	connector.Status = contracts.ConnectorStatus(status)
	if !contracts.IsConnectorVersion(connector.Version) || !validStoredConnectorProtocolVersion(connector.ProtocolVersion) {
		return contracts.Connector{}, fmt.Errorf("store: connector has invalid protocol metadata")
	}
	if len(gatewayState) > 0 {
		_ = json.Unmarshal(gatewayState, &connector.Gateway)
	}
	connector.Gateway = contracts.SanitizeConnectorRuntimeState(connector.Gateway)
	// Sanitization normalizes a live runtime report to the current protocol.
	// A preserved v2 database identity has not performed that handshake yet, so
	// projecting its nested runtime as v3 would falsely claim an upgrade.
	connector.Gateway.ProtocolVersion = connector.ProtocolVersion
	return connector, nil
}

const connectorEnrollmentCols = `id, user_id, instance_id, connector_id, name,
	token_hash, expires_at, used_at, created_by, created_at`

func scanConnectorEnrollment(row rowScanner) (contracts.ConnectorEnrollment, error) {
	var enrollment contracts.ConnectorEnrollment
	if err := row.Scan(
		&enrollment.ID, &enrollment.UserID, &enrollment.InstanceID,
		&enrollment.ConnectorID, &enrollment.Name, &enrollment.TokenHash,
		&enrollment.ExpiresAt, &enrollment.UsedAt, &enrollment.CreatedBy,
		&enrollment.CreatedAt,
	); err != nil {
		return contracts.ConnectorEnrollment{}, err
	}
	return enrollment, nil
}

func (s *PostgresStore) CreateConnectorEnrollment(ctx context.Context, input contracts.ConnectorEnrollment) (contracts.ConnectorEnrollment, error) {
	enrollment := input
	if enrollment.InstanceID == "" || enrollment.UserID == 0 {
		return contracts.ConnectorEnrollment{}, ErrConflict
	}
	if enrollment.ID == "" {
		enrollment.ID = newID("cenroll")
	}
	if enrollment.ConnectorID == "" {
		enrollment.ConnectorID = newID("conn")
	}
	now := nowUTC()
	if enrollment.ExpiresAt.IsZero() {
		enrollment.ExpiresAt = now.Add(24 * time.Hour)
	}
	if !enrollment.ExpiresAt.After(now) {
		return contracts.ConnectorEnrollment{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.ConnectorEnrollment{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The instance lock serializes enrollment creation for its unique slot;
	// the unique connector index remains the final guard across instances.
	var ownerID int64
	var boundConnector string
	if err := tx.QueryRow(ctx,
		`SELECT user_id, COALESCE(connector_id, '') FROM instances WHERE id=$1 FOR UPDATE`,
		enrollment.InstanceID,
	).Scan(&ownerID, &boundConnector); err != nil {
		return contracts.ConnectorEnrollment{}, mapNotFound(err)
	}
	if ownerID != enrollment.UserID || boundConnector != "" && boundConnector != enrollment.ConnectorID {
		return contracts.ConnectorEnrollment{}, ErrConflict
	}
	// The plaintext enrollment token is shown only once. Reissuing an install
	// guide for the same owned instance safely invalidates its previous unused
	// token instead of trapping the user until the old enrollment expires.
	if _, err := tx.Exec(ctx,
		`DELETE FROM connector_enrollments
		 WHERE used_at IS NULL
		   AND ((instance_id=$2 AND user_id=$4)
		        OR (expires_at <= $1 AND connector_id=$3))`,
		now, enrollment.InstanceID, enrollment.ConnectorID, enrollment.UserID); err != nil {
		return contracts.ConnectorEnrollment{}, err
	}
	enrollment.CreatedAt = now
	_, err = tx.Exec(ctx,
		`INSERT INTO connector_enrollments
		 (id, user_id, instance_id, connector_id, name, token_hash, expires_at, used_at, created_by, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		enrollment.ID, enrollment.UserID, enrollment.InstanceID, enrollment.ConnectorID,
		enrollment.Name, enrollment.TokenHash, enrollment.ExpiresAt, enrollment.UsedAt,
		enrollment.CreatedBy, enrollment.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.ConnectorEnrollment{}, ErrDuplicate
		}
		return contracts.ConnectorEnrollment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.ConnectorEnrollment{}, err
	}
	return enrollment, nil
}

func (s *PostgresStore) UseConnectorEnrollment(ctx context.Context, tokenHash string, input contracts.Connector) (contracts.Connector, contracts.ConnectorEnrollment, error) {
	if !contracts.IsConnectorVersion(input.Version) || input.ProtocolVersion != contracts.ConnectorProtocolVersion {
		return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.Connector{}, contracts.ConnectorEnrollment{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	enrollment, err := scanConnectorEnrollment(tx.QueryRow(ctx,
		`SELECT `+connectorEnrollmentCols+` FROM connector_enrollments WHERE token_hash=$1 FOR UPDATE`,
		tokenHash))
	if err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrNotFound
		}
		return contracts.Connector{}, contracts.ConnectorEnrollment{}, err
	}
	now := nowUTC()
	if enrollment.UsedAt != nil || !enrollment.ExpiresAt.After(now) {
		return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrConflict
	}
	if input.ID != "" && input.ID != enrollment.ConnectorID ||
		input.InstanceID != "" && input.InstanceID != enrollment.InstanceID {
		return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrConflict
	}
	var ownerEnabled bool
	var ownerRoles []string
	if err := tx.QueryRow(ctx, `SELECT enabled, roles FROM users WHERE id=$1 FOR UPDATE`, enrollment.UserID).Scan(&ownerEnabled, &ownerRoles); err != nil {
		return contracts.Connector{}, contracts.ConnectorEnrollment{}, mapNotFound(err)
	}
	if !ownerEnabled || !userHasRole(userRolesFromStrings(ownerRoles), contracts.UserRoleClient) {
		return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrConflict
	}
	var ownerID int64
	var boundConnector string
	if err := tx.QueryRow(ctx,
		`SELECT user_id, COALESCE(connector_id, '') FROM instances WHERE id=$1 FOR UPDATE`,
		enrollment.InstanceID,
	).Scan(&ownerID, &boundConnector); err != nil {
		return contracts.Connector{}, contracts.ConnectorEnrollment{}, mapNotFound(err)
	}
	if ownerID != enrollment.UserID || boundConnector != "" && boundConnector != enrollment.ConnectorID {
		return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrConflict
	}

	connector := input
	connector.ID = enrollment.ConnectorID
	connector.UserID = enrollment.UserID
	connector.InstanceID = enrollment.InstanceID
	if connector.Name == "" {
		connector.Name = enrollment.Name
	}
	connector.Gateway = contracts.SanitizeConnectorRuntimeState(connector.Gateway)
	connector.Status = contracts.ConnectorStatusOnline
	connector.LastSeenAt = &now
	connector.RevokedAt = nil
	connector.CreatedAt = now
	connector.UpdatedAt = now

	connector, err = scanConnector(tx.QueryRow(ctx,
		`INSERT INTO connectors
		 (connector_id, user_id, instance_id, name, status, token_hash, version,
		  protocol_version, gateway_state, last_seen_at, revoked_at, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		 ON CONFLICT (connector_id) DO UPDATE SET
		   user_id=EXCLUDED.user_id, instance_id=EXCLUDED.instance_id,
		   name=EXCLUDED.name, status=EXCLUDED.status, token_hash=EXCLUDED.token_hash,
		   version=EXCLUDED.version, protocol_version=EXCLUDED.protocol_version,
		   gateway_state=EXCLUDED.gateway_state,
		   last_seen_at=EXCLUDED.last_seen_at, revoked_at=NULL, updated_at=EXCLUDED.updated_at
		 WHERE connectors.user_id=EXCLUDED.user_id AND connectors.instance_id=EXCLUDED.instance_id
		 RETURNING `+connectorCols,
		connector.ID, connector.UserID, connector.InstanceID, connector.Name,
		string(connector.Status), connector.TokenHash, connector.Version,
		connector.ProtocolVersion, marshalConnectorRuntimeState(connector.Gateway), connector.LastSeenAt,
		connector.RevokedAt, connector.CreatedAt, connector.UpdatedAt))
	if err != nil {
		if isUniqueViolation(err) || errors.Is(err, pgxNoRows()) {
			return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrConflict
		}
		return contracts.Connector{}, contracts.ConnectorEnrollment{}, err
	}
	usedAt := now
	enrollment.UsedAt = &usedAt
	if _, err := tx.Exec(ctx, `UPDATE connector_enrollments SET used_at=$2 WHERE id=$1`, enrollment.ID, usedAt); err != nil {
		return contracts.Connector{}, contracts.ConnectorEnrollment{}, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE instances SET connector_id=$2, updated_at=$3 WHERE id=$1`,
		enrollment.InstanceID, connector.ID, now); err != nil {
		return contracts.Connector{}, contracts.ConnectorEnrollment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.Connector{}, contracts.ConnectorEnrollment{}, err
	}
	return connector, enrollment, nil
}

func (s *PostgresStore) ListConnectors(ctx context.Context, filter contracts.ConnectorFilter) ([]contracts.Connector, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+connectorCols+` FROM connectors
		 WHERE ($1=0 OR user_id=$1)
		   AND ($2='' OR instance_id=$2)
		   AND ($3='' OR status=$3)
		 ORDER BY created_at DESC`,
		filter.UserID, filter.InstanceID, string(filter.Status))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.Connector
	for rows.Next() {
		connector, err := scanConnector(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, connector)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetConnector(ctx context.Context, id string) (contracts.Connector, error) {
	connector, err := scanConnector(s.pool.QueryRow(ctx,
		`SELECT `+connectorCols+` FROM connectors WHERE connector_id=$1`, id))
	if err != nil {
		return contracts.Connector{}, mapNotFound(err)
	}
	return connector, nil
}

func (s *PostgresStore) GetConnectorByTokenHash(ctx context.Context, tokenHash string) (contracts.Connector, error) {
	connector, err := scanConnector(s.pool.QueryRow(ctx,
		`SELECT `+connectorCols+` FROM connectors
		 WHERE token_hash=$1 AND status<>$2
		   AND EXISTS (
		     SELECT 1 FROM users WHERE id=connectors.user_id
		       AND ((enabled AND 'client'=ANY(roles)) OR deactivation_status IN ('draining','failed'))
		   )`,
		tokenHash, string(contracts.ConnectorStatusRevoked)))
	if err != nil {
		return contracts.Connector{}, mapNotFound(err)
	}
	return connector, nil
}

func (s *PostgresStore) RecordConnectorSeen(ctx context.Context, id, version string, runtime contracts.ConnectorRuntimeState) (contracts.Connector, error) {
	if !contracts.IsConnectorVersion(version) || runtime.ProtocolVersion != contracts.ConnectorProtocolVersion {
		return contracts.Connector{}, ErrConflict
	}
	now := nowUTC()
	runtime = contracts.SanitizeConnectorRuntimeState(runtime)
	connector, err := scanConnector(s.pool.QueryRow(ctx,
		`WITH updated AS (
		   UPDATE connectors
		   SET status=$2,
		       version=$3,
		       protocol_version=$4,
		       gateway_state=$5,
		       last_seen_at=$6,
		       updated_at=$6
		   WHERE connector_id=$1 AND status<>$7
		     AND EXISTS (
		       SELECT 1 FROM users WHERE id=connectors.user_id
		         AND ((enabled AND 'client'=ANY(roles)) OR deactivation_status IN ('draining','failed'))
		     )
		     AND (status<>$2 OR version<>$3 OR protocol_version<>$4
		          OR gateway_state<>$5::jsonb OR last_seen_at IS NULL
		          OR last_seen_at <= ($6::timestamptz - interval '15 seconds'))
		   RETURNING `+connectorCols+`
		 )
		 SELECT * FROM updated
		 UNION ALL
		 SELECT `+connectorCols+` FROM connectors
		 WHERE connector_id=$1 AND status<>$7 AND NOT EXISTS (SELECT 1 FROM updated)
		   AND EXISTS (
		     SELECT 1 FROM users WHERE id=connectors.user_id
		       AND ((enabled AND 'client'=ANY(roles)) OR deactivation_status IN ('draining','failed'))
		   )
		 LIMIT 1`,
		id, string(contracts.ConnectorStatusOnline), version, runtime.ProtocolVersion,
		marshalConnectorRuntimeState(runtime), now, string(contracts.ConnectorStatusRevoked)))
	if err != nil {
		return contracts.Connector{}, mapNotFound(err)
	}
	return connector, nil
}

func marshalConnectorRuntimeState(input contracts.ConnectorRuntimeState) []byte {
	raw, err := json.Marshal(contracts.SanitizeConnectorRuntimeState(input))
	if err != nil {
		return []byte("{}")
	}
	return raw
}

func (s *PostgresStore) UpdateConnectorToken(ctx context.Context, id, tokenHash string) (contracts.Connector, error) {
	connector, err := scanConnector(s.pool.QueryRow(ctx,
		`UPDATE connectors SET token_hash=$2, status=$3, updated_at=now()
		 WHERE connector_id=$1 AND status<>$4
		   AND EXISTS (SELECT 1 FROM users u WHERE u.id=connectors.user_id
		               AND u.enabled AND 'client'=ANY(u.roles) AND u.deactivation_status='none')
		 RETURNING `+connectorCols,
		id, tokenHash, string(contracts.ConnectorStatusOffline), string(contracts.ConnectorStatusRevoked)))
	if err != nil {
		return contracts.Connector{}, mapNotFound(err)
	}
	return connector, nil
}

func (s *PostgresStore) RevokeConnector(ctx context.Context, id string) (contracts.Connector, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.Connector{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	connector, err := scanConnector(tx.QueryRow(ctx,
		`UPDATE connectors SET status=$2, token_hash='', revoked_at=now(), updated_at=now()
		 WHERE connector_id=$1
		   AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id=connectors.user_id
		                   AND u.deactivation_status IN ('draining','failed'))
		 RETURNING `+connectorCols,
		id, string(contracts.ConnectorStatusRevoked)))
	if err != nil {
		return contracts.Connector{}, mapNotFound(err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE instances SET connector_id=NULL, updated_at=now()
		 WHERE id=$1 AND connector_id=$2`, connector.InstanceID, connector.ID); err != nil {
		return contracts.Connector{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.Connector{}, err
	}
	return connector, nil
}

const connectorTaskLeaseNonceBytes = 32

func newConnectorTaskLeaseNonce() (string, error) {
	raw := make([]byte, connectorTaskLeaseNonceBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

const connectorTaskCols = `id, user_id, instance_id, connector_id, COALESCE(plan_id,''), COALESCE(scheduling_generation,0),
	COALESCE(execution_scope,''), COALESCE(execution_id,''), COALESCE(execution_generation,0), type,
	schema_version, risk_level, status, input, result, error, idempotency_key,
	lease_owner, lease_nonce, lease_until, attempts, max_attempts, available_at,
	expires_at, created_at, updated_at`

func validRawJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 || !json.Valid(raw) {
		return []byte("null")
	}
	return raw
}

func marshalTaskError(input contracts.ConnectorTaskError) []byte {
	raw, err := json.Marshal(input)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

func scanConnectorTask(row rowScanner) (contracts.ConnectorTask, error) {
	var task contracts.ConnectorTask
	var taskType, risk, status string
	var input, result, taskError []byte
	if err := row.Scan(
		&task.ID, &task.UserID, &task.InstanceID, &task.ConnectorID, &task.PlanID, &task.SchedulingGeneration,
		&task.ExecutionScope, &task.ExecutionID, &task.ExecutionGeneration, &taskType,
		&task.SchemaVersion, &risk, &status, &input, &result, &taskError,
		&task.IdempotencyKey, &task.LeaseOwner, &task.LeaseNonce, &task.LeaseUntil, &task.Attempts,
		&task.MaxAttempts, &task.AvailableAt, &task.ExpiresAt, &task.CreatedAt, &task.UpdatedAt,
	); err != nil {
		return contracts.ConnectorTask{}, err
	}
	task.Type = contracts.ConnectorTaskType(taskType)
	task.RiskLevel = contracts.RiskLevel(risk)
	task.Status = contracts.ConnectorTaskStatus(status)
	if string(input) != "null" {
		task.Input = append(json.RawMessage(nil), input...)
	}
	if string(result) != "null" {
		task.Result = append(json.RawMessage(nil), result...)
	}
	if len(taskError) > 0 {
		_ = json.Unmarshal(taskError, &task.Error)
	}
	if !validStoredConnectorTaskError(task.Error) {
		return contracts.ConnectorTask{}, fmt.Errorf("store: connector task has invalid error code")
	}
	return task, nil
}

func (s *PostgresStore) CreateConnectorTask(ctx context.Context, input contracts.ConnectorTask) (contracts.ConnectorTask, error) {
	task := input
	if task.ConnectorID == "" || task.InstanceID == "" || task.UserID == 0 ||
		task.Type.RiskLevel() == contracts.RiskLevelL3 || !validStoredConnectorTaskError(task.Error) {
		return contracts.ConnectorTask{}, ErrConflict
	}
	if err := normalizeConnectorTaskPlanFence(&task); err != nil {
		return contracts.ConnectorTask{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.ConnectorTask{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var connectorUserID int64
	var connectorInstanceID, connectorStatus, deactivationStatus string
	var userEnabled, userClient bool
	if err := tx.QueryRow(ctx,
		`SELECT c.user_id, c.instance_id, c.status, u.enabled, 'client'=ANY(u.roles), u.deactivation_status
		 FROM connectors c JOIN users u ON u.id=c.user_id WHERE c.connector_id=$1 FOR SHARE OF c, u`,
		task.ConnectorID,
	).Scan(&connectorUserID, &connectorInstanceID, &connectorStatus, &userEnabled, &userClient, &deactivationStatus); err != nil {
		return contracts.ConnectorTask{}, mapNotFound(err)
	}
	if connectorUserID != task.UserID || connectorInstanceID != task.InstanceID || connectorStatus == string(contracts.ConnectorStatusRevoked) {
		return contracts.ConnectorTask{}, ErrConflict
	}
	if task.PlanID != "" {
		plan, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planCols+` FROM route_plans WHERE id=$1 FOR SHARE`, task.PlanID))
		if err != nil {
			return contracts.ConnectorTask{}, mapNotFound(err)
		}
		if !connectorTaskMatchesCurrentPlan(task, plan) {
			return contracts.ConnectorTask{}, ErrConflict
		}
	} else if task.ExecutionScope != "" {
		execution, err := scanHybridRoutingExecution(tx.QueryRow(ctx, `SELECT `+hybridRoutingExecutionColumns+`
			FROM hybrid_routing_executions WHERE id=$1 FOR SHARE`, task.ExecutionID))
		if err != nil {
			return contracts.ConnectorTask{}, mapNotFound(err)
		}
		if !connectorTaskMatchesCurrentHybridExecution(task, execution) {
			return contracts.ConnectorTask{}, ErrConflict
		}
	}
	deactivating := contracts.UserDeactivationStatus(deactivationStatus).InProgress()
	if !(userEnabled && userClient) && (!deactivating || !connectorTaskAllowedDuringUserDeactivation(task)) {
		return contracts.ConnectorTask{}, ErrConflict
	}
	if task.ID == "" {
		task.ID = newID("ctask")
	}
	if task.SchemaVersion <= 0 {
		task.SchemaVersion = 1
	}
	task.RiskLevel = task.Type.RiskLevel()
	if task.Status == "" {
		task.Status = contracts.ConnectorTaskPending
	}
	if task.MaxAttempts <= 0 {
		task.MaxAttempts = 3
	}
	now := nowUTC()
	if task.AvailableAt.IsZero() {
		task.AvailableAt = now
	}
	if task.IdempotencyKey != "" {
		_, err := tx.Exec(ctx,
			`UPDATE connector_tasks
			 SET status=$3, lease_owner='', lease_nonce='', lease_until=NULL, updated_at=$4,
			     result='null'::jsonb, error='{"code":"expired"}'::jsonb
			 WHERE connector_id=$1 AND idempotency_key=$2 AND status IN ($5,$6) AND expires_at <= $4`,
			task.ConnectorID, task.IdempotencyKey, string(contracts.ConnectorTaskExpired), now,
			string(contracts.ConnectorTaskPending), string(contracts.ConnectorTaskLeased))
		if err != nil {
			return contracts.ConnectorTask{}, err
		}
	}
	if task.ExpiresAt.IsZero() {
		task.ExpiresAt = now.Add(10 * time.Minute)
	}
	task.CreatedAt, task.UpdatedAt = now, now
	_, err = tx.Exec(ctx,
		`INSERT INTO connector_tasks
		 (id, user_id, instance_id, connector_id, plan_id, scheduling_generation, execution_scope, execution_id,
		  execution_generation, type, schema_version, risk_level,
		  status, input, result, error, idempotency_key, lease_owner, lease_nonce, lease_until,
		  attempts, max_attempts, available_at, expires_at, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,0),NULLIF($7,''),NULLIF($8,''),NULLIF($9,0),$10,$11,$12,$13,
		 $14::jsonb,$15::jsonb,$16::jsonb,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)`,
		task.ID, task.UserID, task.InstanceID, task.ConnectorID, task.PlanID, task.SchedulingGeneration,
		task.ExecutionScope, task.ExecutionID, task.ExecutionGeneration, string(task.Type),
		task.SchemaVersion, string(task.RiskLevel), string(task.Status),
		string(validRawJSON(task.Input)), string(validRawJSON(task.Result)),
		string(marshalTaskError(task.Error)), task.IdempotencyKey, task.LeaseOwner,
		task.LeaseNonce, task.LeaseUntil, task.Attempts, task.MaxAttempts, task.AvailableAt, task.ExpiresAt,
		task.CreatedAt, task.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.ConnectorTask{}, ErrDuplicate
		}
		return contracts.ConnectorTask{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.ConnectorTask{}, err
	}
	return task, nil
}

func (s *PostgresStore) GetConnectorTask(ctx context.Context, id string) (contracts.ConnectorTask, error) {
	if _, err := s.pool.Exec(ctx,
		`UPDATE connector_tasks
		 SET status=$2, result='null'::jsonb, error='{"code":"expired"}'::jsonb,
		     lease_owner='', lease_nonce='', lease_until=NULL, updated_at=statement_timestamp()
		 WHERE id=$1 AND status IN ($3,$4) AND expires_at <= statement_timestamp()`,
		id, string(contracts.ConnectorTaskExpired), string(contracts.ConnectorTaskPending),
		string(contracts.ConnectorTaskLeased)); err != nil {
		return contracts.ConnectorTask{}, err
	}
	task, err := scanConnectorTask(s.pool.QueryRow(ctx,
		`SELECT `+connectorTaskCols+` FROM connector_tasks WHERE id=$1`, id))
	if err != nil {
		return contracts.ConnectorTask{}, mapNotFound(err)
	}
	return task, nil
}

func (s *PostgresStore) ListConnectorTasks(ctx context.Context, filter contracts.ConnectorTaskFilter) ([]contracts.ConnectorTask, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	taskTypes := make([]string, 0, len(filter.Types))
	for _, taskType := range filter.Types {
		taskTypes = append(taskTypes, string(taskType))
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+connectorTaskCols+` FROM connector_tasks
		 WHERE ($1=0 OR user_id=$1)
		   AND ($2='' OR instance_id=$2)
		   AND ($3='' OR connector_id=$3)
		   AND ($4='' OR status=$4)
		   AND (COALESCE(cardinality($5::text[]), 0)=0 OR type=ANY($5::text[]))
		 ORDER BY created_at DESC LIMIT $6`,
		filter.UserID, filter.InstanceID, filter.ConnectorID, string(filter.Status), taskTypes, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.ConnectorTask
	for rows.Next() {
		task, err := scanConnectorTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

func (s *PostgresStore) LeaseConnectorTasks(ctx context.Context, req contracts.ConnectorTaskLeaseRequest) ([]contracts.ConnectorTask, error) {
	if req.ConnectorID == "" || req.ProtocolVersion != 0 && req.ProtocolVersion != contracts.ConnectorProtocolVersion {
		return nil, ErrNotFound
	}
	maxTasks := req.MaxTasks
	if maxTasks <= 0 || maxTasks > 10 {
		maxTasks = 1
	}
	leaseSeconds := req.LeaseSeconds
	if leaseSeconds <= 0 {
		leaseSeconds = 30
	}
	if leaseSeconds > 300 {
		leaseSeconds = 300
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var connectorEnabled, connectorDeactivating bool
	if err := tx.QueryRow(ctx,
		`SELECT
		   EXISTS(SELECT 1 FROM connectors c JOIN users u ON u.id=c.user_id
		          WHERE c.connector_id=$1 AND c.status<>$2
		            AND c.protocol_version=$3
		            AND ((u.enabled AND 'client'=ANY(u.roles)) OR u.deactivation_status IN ('draining','failed'))),
		   EXISTS(SELECT 1 FROM connectors c JOIN users u ON u.id=c.user_id
		          WHERE c.connector_id=$1 AND c.status<>$2
		            AND c.protocol_version=$3
		            AND u.deactivation_status IN ('draining','failed'))`,
		req.ConnectorID, string(contracts.ConnectorStatusRevoked), contracts.ConnectorProtocolVersion).Scan(&connectorEnabled, &connectorDeactivating); err != nil {
		return nil, err
	}
	if !connectorEnabled {
		return nil, ErrConflict
	}
	now := nowUTC()
	requestedLeaseUntil := now.Add(time.Duration(leaseSeconds) * time.Second)
	if _, err := tx.Exec(ctx,
		`UPDATE connector_tasks
		 SET status=$2, lease_owner='', lease_nonce='', lease_until=NULL, updated_at=$3,
		     result='null'::jsonb, error='{"code":"expired"}'::jsonb
		 WHERE connector_id=$1 AND status IN ($4,$5) AND expires_at <= $3`,
		req.ConnectorID, string(contracts.ConnectorTaskExpired), now,
		string(contracts.ConnectorTaskPending), string(contracts.ConnectorTaskLeased)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE connector_tasks
		 SET status=$2, lease_owner='', lease_nonce='', lease_until=NULL, updated_at=$3,
			     error='{"code":"max_attempts_exceeded"}'::jsonb
		 WHERE connector_id=$1 AND expires_at > $3 AND attempts >= max_attempts
		   AND (status=$4 OR (status=$5 AND (lease_until IS NULL OR lease_until <= $3)))`,
		req.ConnectorID, string(contracts.ConnectorTaskFailed), now,
		string(contracts.ConnectorTaskPending), string(contracts.ConnectorTaskLeased)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE connector_tasks AS task
		 SET status=$2, result='null'::jsonb, error=$3::jsonb,
		     lease_owner='', lease_nonce='', lease_until=NULL, updated_at=$4
		 WHERE task.connector_id=$1 AND task.plan_id IS NOT NULL
		   AND task.status IN ($5,$6)
		   AND NOT EXISTS (
		     SELECT 1 FROM route_plans AS plan
		     WHERE plan.id=task.plan_id AND plan.user_id=task.user_id
		       AND plan.instance_id=task.instance_id
		       AND plan.scheduling_generation=task.scheduling_generation
		   )`, req.ConnectorID, string(contracts.ConnectorTaskFailed),
		string(marshalTaskError(contracts.ConnectorTaskError{Code: connectorTaskSupersededErrorCode})), now,
		string(contracts.ConnectorTaskPending), string(contracts.ConnectorTaskLeased)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE connector_tasks AS task
		 SET status=$2,result='null'::jsonb,error=$3::jsonb,
		     lease_owner='',lease_nonce='',lease_until=NULL,updated_at=$4
		 WHERE task.connector_id=$1 AND task.execution_scope=$5
		   AND task.status IN ($6,$7)
		   AND NOT EXISTS (
		     SELECT 1 FROM hybrid_routing_executions execution
		     JOIN hybrid_allocations allocation ON allocation.instance_id=execution.instance_id
		     WHERE execution.id=task.execution_id AND execution.user_id=task.user_id
		       AND execution.instance_id=task.instance_id AND execution.generation=task.execution_generation
		       AND execution.status='applying' AND allocation.routing_generation=task.execution_generation
		   )`, req.ConnectorID, string(contracts.ConnectorTaskFailed),
		string(marshalTaskError(contracts.ConnectorTaskError{Code: connectorTaskSupersededErrorCode})), now,
		contracts.HybridRoutingExecutionScope, string(contracts.ConnectorTaskPending), string(contracts.ConnectorTaskLeased)); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx,
		`SELECT `+connectorTaskCols+` FROM connector_tasks
		 WHERE connector_id=$1
		   AND (status=$2 OR (status=$3 AND (lease_until IS NULL OR lease_until <= $4)))
		   AND available_at <= $4 AND expires_at > $4 AND attempts < max_attempts
		   AND (plan_id IS NULL OR EXISTS (
		     SELECT 1 FROM route_plans AS plan
		     WHERE plan.id=connector_tasks.plan_id AND plan.user_id=connector_tasks.user_id
		       AND plan.instance_id=connector_tasks.instance_id
		       AND plan.scheduling_generation=connector_tasks.scheduling_generation
		   ))
		   AND (execution_scope IS NULL OR EXISTS (
		     SELECT 1 FROM hybrid_routing_executions execution
		     JOIN hybrid_allocations allocation ON allocation.instance_id=execution.instance_id
		     WHERE execution.id=connector_tasks.execution_id AND execution.user_id=connector_tasks.user_id
		       AND execution.instance_id=connector_tasks.instance_id AND execution.generation=connector_tasks.execution_generation
		       AND execution.status='applying' AND allocation.routing_generation=connector_tasks.execution_generation
		   ))
		   AND (NOT $6 OR type IN ('gateway.accounts.list','gateway.scheduling.barrier') OR
		        (type='gateway.account.schedulable.set'
		         AND input->'schedulable'='false'::jsonb
		         AND COALESCE(btrim(input->>'account_id'),'')<>''))
		 ORDER BY created_at LIMIT $5 FOR UPDATE SKIP LOCKED`,
		req.ConnectorID, string(contracts.ConnectorTaskPending),
		string(contracts.ConnectorTaskLeased), now, maxTasks, connectorDeactivating)
	if err != nil {
		return nil, err
	}
	var tasks []contracts.ConnectorTask
	for rows.Next() {
		task, err := scanConnectorTask(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if connectorDeactivating && !connectorTaskAllowedDuringUserDeactivation(task) {
			continue
		}
		leaseNonce, err := newConnectorTaskLeaseNonce()
		if err != nil {
			rows.Close()
			return nil, err
		}
		task.Status = contracts.ConnectorTaskLeased
		task.LeaseOwner = req.ConnectorID
		task.LeaseNonce = leaseNonce
		leaseUntil := requestedLeaseUntil
		if task.ExpiresAt.Before(leaseUntil) {
			leaseUntil = task.ExpiresAt
		}
		task.LeaseUntil = &leaseUntil
		task.Attempts++
		task.UpdatedAt = now
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range tasks {
		task := &tasks[i]
		if _, err := tx.Exec(ctx,
			`UPDATE connector_tasks SET status=$2, lease_owner=$3, lease_nonce=$4, lease_until=$5,
			 attempts=$6, updated_at=$7 WHERE id=$1`,
			task.ID, string(task.Status), task.LeaseOwner, task.LeaseNonce, task.LeaseUntil,
			task.Attempts, task.UpdatedAt); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return tasks, nil
}

// BeginConnectorTaskExecution exchanges a live lease for the durable permit
// required by a RoutePlan- or Hybrid-scoped gateway mutation. The owning
// generation row is locked before the task, matching every generation writer.
func (s *PostgresStore) BeginConnectorTaskExecution(ctx context.Context, id string, req contracts.ConnectorTaskExecutionRequest) (contracts.ConnectorTask, error) {
	if id == "" || req.ConnectorID == "" || req.LeaseNonce == "" {
		return contracts.ConnectorTask{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.ConnectorTask{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// This first read deliberately takes no row lock. Execution identity is
	// immutable at the database boundary; its only purpose is to discover the
	// plan whose lock must precede the task lock below.
	var planID, executionScope, executionID string
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(plan_id,''),COALESCE(execution_scope,''),COALESCE(execution_id,'')
		 FROM connector_tasks WHERE id=$1`, id,
	).Scan(&planID, &executionScope, &executionID); err != nil {
		return contracts.ConnectorTask{}, mapNotFound(err)
	}
	if planID == "" && executionScope == "" || planID != "" && executionScope != "" {
		return contracts.ConnectorTask{}, ErrConflict
	}
	var plan contracts.RoutePlan
	var hybridExecution contracts.HybridRoutingExecution
	if planID != "" {
		plan, err = scanPlan(tx.QueryRow(ctx, `SELECT `+planCols+` FROM route_plans WHERE id=$1 FOR SHARE`, planID))
	} else if executionScope == contracts.HybridRoutingExecutionScope {
		hybridExecution, err = scanHybridRoutingExecution(tx.QueryRow(ctx, `SELECT `+hybridRoutingExecutionColumns+`
			FROM hybrid_routing_executions WHERE id=$1 FOR SHARE`, executionID))
	} else {
		return contracts.ConnectorTask{}, ErrConflict
	}
	if err != nil {
		return contracts.ConnectorTask{}, mapNotFound(err)
	}
	task, err := scanConnectorTask(tx.QueryRow(ctx,
		`SELECT `+connectorTaskCols+` FROM connector_tasks
		 WHERE id=$1 AND EXISTS (
		   SELECT 1 FROM connectors c JOIN users u ON u.id=c.user_id
		   WHERE c.connector_id=connector_tasks.connector_id AND c.status<>$2
		     AND c.protocol_version=$3
		     AND ((u.enabled AND 'client'=ANY(u.roles)) OR u.deactivation_status IN ('draining','failed'))
		 ) FOR UPDATE`, id, string(contracts.ConnectorStatusRevoked), contracts.ConnectorProtocolVersion))
	if err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.ConnectorTask{}, ErrNotFound
		}
		return contracts.ConnectorTask{}, err
	}
	now := nowUTC()
	identityMatches := task.PlanID == planID
	if planID != "" {
		identityMatches = identityMatches && connectorTaskMatchesCurrentPlan(task, plan)
	} else {
		identityMatches = identityMatches && task.ExecutionScope == executionScope && task.ExecutionID == executionID &&
			connectorTaskMatchesCurrentHybridExecution(task, hybridExecution)
		if identityMatches {
			var currentGeneration int64
			if err := tx.QueryRow(ctx, `SELECT routing_generation FROM hybrid_allocations WHERE instance_id=$1 FOR SHARE`, task.InstanceID).Scan(&currentGeneration); err != nil {
				return contracts.ConnectorTask{}, mapNotFound(err)
			}
			identityMatches = currentGeneration == task.ExecutionGeneration
		}
	}
	if !identityMatches ||
		task.ConnectorID != req.ConnectorID || task.LeaseOwner != req.ConnectorID ||
		task.LeaseNonce != req.LeaseNonce {
		return contracts.ConnectorTask{}, ErrConflict
	}
	// A retry after Core committed the permit but its response was lost is
	// idempotent for the exact immutable task and lease identity. executing is
	// intentionally not governed by the old lease deadline.
	if task.Status == contracts.ConnectorTaskExecuting {
		if err := tx.Commit(ctx); err != nil {
			return contracts.ConnectorTask{}, err
		}
		return task, nil
	}
	if task.Status != contracts.ConnectorTaskLeased || task.LeaseUntil == nil ||
		!task.LeaseUntil.After(now) || !task.ExpiresAt.After(now) {
		return contracts.ConnectorTask{}, ErrConflict
	}
	var deactivating bool
	if err := tx.QueryRow(ctx,
		`SELECT deactivation_status IN ('draining','failed') FROM users WHERE id=$1`, task.UserID,
	).Scan(&deactivating); err != nil {
		return contracts.ConnectorTask{}, mapNotFound(err)
	}
	if deactivating && !connectorTaskAllowedDuringUserDeactivation(task) {
		return contracts.ConnectorTask{}, ErrConflict
	}
	task.Status = contracts.ConnectorTaskExecuting
	task.UpdatedAt = now
	if _, err := tx.Exec(ctx,
		`UPDATE connector_tasks SET status=$2,updated_at=$3
		 WHERE id=$1 AND status=$4 AND lease_owner=$5 AND lease_nonce=$6`,
		task.ID, string(task.Status), task.UpdatedAt, string(contracts.ConnectorTaskLeased),
		req.ConnectorID, req.LeaseNonce); err != nil {
		return contracts.ConnectorTask{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.ConnectorTask{}, err
	}
	return task, nil
}

func (s *PostgresStore) CompleteConnectorTask(ctx context.Context, id string, req contracts.ConnectorTaskCompleteRequest) (contracts.ConnectorTask, error) {
	if !validConnectorCompletionError(req) {
		return contracts.ConnectorTask{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.ConnectorTask{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Discover the immutable execution identity without taking the task lock,
	// then acquire plan -> task. The previous task -> plan order could deadlock
	// with a generation writer, which always owns the plan before invalidating
	// queued tasks.
	var discoveredPlanID, discoveredExecutionScope, discoveredExecutionID string
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(plan_id,''),COALESCE(execution_scope,''),COALESCE(execution_id,'')
		 FROM connector_tasks WHERE id=$1`, id,
	).Scan(&discoveredPlanID, &discoveredExecutionScope, &discoveredExecutionID); err != nil {
		return contracts.ConnectorTask{}, mapNotFound(err)
	}
	var lockedPlan contracts.RoutePlan
	var lockedHybridExecution contracts.HybridRoutingExecution
	if discoveredPlanID != "" {
		lockedPlan, err = scanPlan(tx.QueryRow(ctx,
			`SELECT `+planCols+` FROM route_plans WHERE id=$1 FOR SHARE`, discoveredPlanID))
		if err != nil {
			return contracts.ConnectorTask{}, mapNotFound(err)
		}
	} else if discoveredExecutionScope != "" {
		if discoveredExecutionScope != contracts.HybridRoutingExecutionScope {
			return contracts.ConnectorTask{}, ErrConflict
		}
		lockedHybridExecution, err = scanHybridRoutingExecution(tx.QueryRow(ctx, `SELECT `+hybridRoutingExecutionColumns+`
			FROM hybrid_routing_executions WHERE id=$1 FOR SHARE`, discoveredExecutionID))
		if err != nil {
			return contracts.ConnectorTask{}, mapNotFound(err)
		}
	}
	task, err := scanConnectorTask(tx.QueryRow(ctx,
		`SELECT `+connectorTaskCols+` FROM connector_tasks
		 WHERE id=$1 AND EXISTS (
		   SELECT 1 FROM connectors c JOIN users u ON u.id=c.user_id
		   WHERE c.connector_id=connector_tasks.connector_id AND c.status<>$2
		     AND c.protocol_version=$3
		     AND ((u.enabled AND 'client'=ANY(u.roles)) OR u.deactivation_status IN ('draining','failed'))
		 ) FOR UPDATE`, id, string(contracts.ConnectorStatusRevoked), contracts.ConnectorProtocolVersion))
	if err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.ConnectorTask{}, ErrNotFound
		}
		return contracts.ConnectorTask{}, err
	}
	if task.ConnectorID != req.ConnectorID || task.PlanID != discoveredPlanID ||
		task.ExecutionScope != discoveredExecutionScope || task.ExecutionID != discoveredExecutionID {
		return contracts.ConnectorTask{}, ErrConflict
	}
	if task.PlanID != "" && !connectorTaskMatchesCurrentPlan(task, lockedPlan) {
		return contracts.ConnectorTask{}, ErrConflict
	}
	if task.ExecutionScope != "" {
		if !connectorTaskMatchesCurrentHybridExecution(task, lockedHybridExecution) {
			return contracts.ConnectorTask{}, ErrConflict
		}
		var currentGeneration int64
		if err := tx.QueryRow(ctx, `SELECT routing_generation FROM hybrid_allocations WHERE instance_id=$1 FOR SHARE`, task.InstanceID).Scan(&currentGeneration); err != nil {
			return contracts.ConnectorTask{}, mapNotFound(err)
		}
		if currentGeneration != task.ExecutionGeneration {
			return contracts.ConnectorTask{}, ErrConflict
		}
	}
	var deactivating bool
	if err := tx.QueryRow(ctx,
		`SELECT u.deactivation_status IN ('draining','failed')
		 FROM users u WHERE u.id=$1`, task.UserID).Scan(&deactivating); err != nil {
		return contracts.ConnectorTask{}, mapNotFound(err)
	}
	if deactivating && !connectorTaskAllowedDuringUserDeactivation(task) {
		return contracts.ConnectorTask{}, ErrConflict
	}
	now := nowUTC()
	if !connectorTaskRequiresExecutionPermit(task) && !task.ExpiresAt.After(now) {
		if _, err := tx.Exec(ctx,
			`UPDATE connector_tasks
			 SET status=$2, result='null'::jsonb, error='{"code":"expired"}'::jsonb,
			     lease_owner='', lease_nonce='', lease_until=NULL, updated_at=$3
			 WHERE id=$1`, task.ID, string(contracts.ConnectorTaskExpired), now); err != nil {
			return contracts.ConnectorTask{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return contracts.ConnectorTask{}, err
		}
		return contracts.ConnectorTask{}, ErrConflict
	}
	expectedStatus := contracts.ConnectorTaskLeased
	if connectorTaskRequiresExecutionPermit(task) {
		expectedStatus = contracts.ConnectorTaskExecuting
		// A retryable completion would release the execution permit and permit a
		// newer generation even though a timeout or broken response cannot prove
		// that the remote mutation did not happen. Such outcomes remain executing
		// for explicit reconciliation instead of entering the automatic retry
		// queue.
		if !req.Success && req.Error.Retryable {
			return contracts.ConnectorTask{}, ErrConflict
		}
		// HTTP canonicalizes typed receipts, but the Store is also a security
		// boundary used by workers and tests. Never let a direct caller release a
		// plan permit with a malformed or mismatched acknowledgement.
		if req.Success && !validResolvedConnectorTaskResult(task, req.Result) {
			return contracts.ConnectorTask{}, ErrConflict
		}
	}
	if req.LeaseNonce == "" || task.Status != expectedStatus ||
		task.LeaseOwner != req.ConnectorID || task.LeaseNonce != req.LeaseNonce ||
		task.LeaseUntil == nil || !connectorTaskRequiresExecutionPermit(task) && !task.LeaseUntil.After(now) {
		return contracts.ConnectorTask{}, ErrConflict
	}
	task.Result = append(json.RawMessage(nil), req.Result...)
	task.Error = req.Error
	task.LeaseOwner = ""
	task.LeaseNonce = ""
	task.LeaseUntil = nil
	task.UpdatedAt = now
	if req.Success {
		task.Status = contracts.ConnectorTaskSucceeded
	} else if !req.Error.Retryable || task.Attempts >= task.MaxAttempts {
		task.Result = nil
		task.Status = contracts.ConnectorTaskFailed
	} else {
		task.Result = nil
		task.Status = contracts.ConnectorTaskPending
		task.AvailableAt = now.Add(connectorTaskRetryDelay(task.Attempts))
	}
	if _, err := tx.Exec(ctx,
		`UPDATE connector_tasks SET status=$2, result=$3::jsonb, error=$4::jsonb,
		 lease_owner=$5, lease_nonce=$6, lease_until=$7, available_at=$8, updated_at=$9 WHERE id=$1`,
		task.ID, string(task.Status), string(validRawJSON(task.Result)),
		string(marshalTaskError(task.Error)), task.LeaseOwner, task.LeaseNonce, task.LeaseUntil,
		task.AvailableAt, task.UpdatedAt); err != nil {
		return contracts.ConnectorTask{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.ConnectorTask{}, err
	}
	return task, nil
}

// ResolveConnectorTaskExecution is the only recovery edge for a fenced task
// whose remote outcome could not be inferred. It takes the route-plan lock
// before the task lock, matching permit acquisition and generation writers,
// and commits the terminal transition together with its mandatory L3 audit.
// It intentionally never returns the task to pending.
func (s *PostgresStore) ResolveConnectorTaskExecution(
	ctx context.Context,
	id string,
	req contracts.ConnectorTaskExecutionResolveRequest,
	audit contracts.OperationAudit,
) (contracts.ConnectorTask, error) {
	if id == "" {
		return contracts.ConnectorTask{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.ConnectorTask{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Execution identity is immutable under migration 0074. This unlocked read
	// only discovers which ordering domain must be locked first.
	var planID, executionScope, executionID string
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(plan_id,''),COALESCE(execution_scope,''),COALESCE(execution_id,'')
		 FROM connector_tasks WHERE id=$1`, id,
	).Scan(&planID, &executionScope, &executionID); err != nil {
		return contracts.ConnectorTask{}, mapNotFound(err)
	}
	if planID == "" && executionScope == "" || planID != "" && executionScope != "" {
		return contracts.ConnectorTask{}, ErrConflict
	}
	if planID != "" {
		if _, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planCols+` FROM route_plans WHERE id=$1 FOR SHARE`, planID)); err != nil {
			return contracts.ConnectorTask{}, mapNotFound(err)
		}
	} else if executionScope == contracts.HybridRoutingExecutionScope {
		if _, err := scanHybridRoutingExecution(tx.QueryRow(ctx, `SELECT `+hybridRoutingExecutionColumns+`
			FROM hybrid_routing_executions WHERE id=$1 FOR SHARE`, executionID)); err != nil {
			return contracts.ConnectorTask{}, mapNotFound(err)
		}
	} else {
		return contracts.ConnectorTask{}, ErrConflict
	}
	task, err := scanConnectorTask(tx.QueryRow(ctx,
		`SELECT `+connectorTaskCols+` FROM connector_tasks WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return contracts.ConnectorTask{}, mapNotFound(err)
	}
	if task.PlanID != planID || task.ExecutionScope != executionScope || task.ExecutionID != executionID ||
		!connectorTaskRequiresExecutionPermit(task) ||
		!validConnectorTaskExecutionResolution(task, req) ||
		!validConnectorTaskResolutionAudit(task, req, audit) {
		return contracts.ConnectorTask{}, ErrConflict
	}
	if req.Resolution == contracts.ConnectorTaskExecutionRevokedUnverifiable {
		var revoked bool
		if err := tx.QueryRow(ctx,
			`SELECT status=$2 FROM connectors WHERE connector_id=$1 FOR SHARE`,
			task.ConnectorID, string(contracts.ConnectorStatusRevoked),
		).Scan(&revoked); err != nil {
			return contracts.ConnectorTask{}, mapNotFound(err)
		}
		if !revoked {
			return contracts.ConnectorTask{}, ErrConflict
		}
	}

	switch req.Resolution {
	case contracts.ConnectorTaskExecutionConfirmedApplied:
		task.Status = contracts.ConnectorTaskSucceeded
		task.Result = append(json.RawMessage(nil), req.Result...)
		task.Error = contracts.ConnectorTaskError{}
	case contracts.ConnectorTaskExecutionConfirmedNotApplied:
		task.Status = contracts.ConnectorTaskFailed
		task.Result = nil
		task.Error = contracts.ConnectorTaskError{Code: "execution_abandoned"}
	case contracts.ConnectorTaskExecutionRevokedUnverifiable:
		task.Status = contracts.ConnectorTaskFailed
		task.Result = nil
		task.Error = contracts.ConnectorTaskError{Code: "execution_outcome_unknown"}
	default:
		return contracts.ConnectorTask{}, ErrConflict
	}
	now := nowUTC()
	task.LeaseOwner = ""
	task.LeaseNonce = ""
	task.LeaseUntil = nil
	task.UpdatedAt = now
	tag, err := tx.Exec(ctx,
		`UPDATE connector_tasks
		 SET status=$2, result=$3::jsonb, error=$4::jsonb,
		     lease_owner='', lease_nonce='', lease_until=NULL, updated_at=$5
		 WHERE id=$1 AND status=$6`,
		task.ID, string(task.Status), string(validRawJSON(task.Result)),
		string(marshalTaskError(task.Error)), task.UpdatedAt, string(contracts.ConnectorTaskExecuting))
	if err != nil {
		return contracts.ConnectorTask{}, err
	}
	if tag.RowsAffected() != 1 {
		return contracts.ConnectorTask{}, ErrConflict
	}

	// IDs and timestamps are server-authored so a caller cannot collide with or
	// backdate another audit record. All identity fields were checked above.
	audit.ID = newID("audit")
	audit.CreatedAt = now
	details, err := json.Marshal(audit.Details)
	if err != nil {
		return contracts.ConnectorTask{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operation_audits
		(id,user_id,instance_id,actor_type,actor_id,action,risk_level,event_level,target_type,target_id,
		 request_payload_hash,result,error_message,approval_id,workflow_run_id,details,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::jsonb,$17)`,
		audit.ID, audit.UserID, audit.InstanceID, audit.ActorType, audit.ActorID, audit.Action,
		string(audit.RiskLevel), string(audit.EventLevel), audit.TargetType, audit.TargetID,
		audit.RequestHash, audit.Result, audit.ErrorMessage, audit.ApprovalID,
		audit.WorkflowRunID, string(details), audit.CreatedAt); err != nil {
		return contracts.ConnectorTask{}, fmt.Errorf("append connector execution resolution audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.ConnectorTask{}, err
	}
	return task, nil
}
