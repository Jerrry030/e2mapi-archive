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

const onboardingColumns = `id, user_id, instance_id, pool_id, connector_id,
	stage, status, attempts, next_attempt_at, last_error_code, plan_id,
	key_version_summary, desired_fingerprint, desired_generation,
	last_ready_generation, last_ready_at, version, lease_owner, lease_until, created_at, updated_at`

func scanOnboardingWorkflow(row rowScanner) (contracts.OnboardingWorkflow, error) {
	var workflow contracts.OnboardingWorkflow
	var stage, status string
	var versions []byte
	if err := row.Scan(
		&workflow.ID, &workflow.UserID, &workflow.InstanceID, &workflow.PoolID,
		&workflow.ConnectorID, &stage, &status, &workflow.Attempts,
		&workflow.NextAttemptAt, &workflow.LastErrorCode, &workflow.PlanID,
		&versions, &workflow.DesiredFingerprint, &workflow.DesiredGeneration,
		&workflow.LastReadyGeneration, &workflow.LastReadyAt,
		&workflow.Version, &workflow.LeaseOwner,
		&workflow.LeaseUntil, &workflow.CreatedAt, &workflow.UpdatedAt,
	); err != nil {
		return contracts.OnboardingWorkflow{}, err
	}
	workflow.Stage = contracts.OnboardingStage(stage)
	workflow.Status = contracts.OnboardingStatus(status)
	if len(versions) > 0 {
		_ = json.Unmarshal(versions, &workflow.KeyVersionSummary)
	}
	return workflow, nil
}

func marshalKeyVersionSummary(summary map[string]int64) ([]byte, error) {
	if len(summary) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(summary)
}

func (s *PostgresStore) UpsertOnboardingWorkflow(ctx context.Context, input contracts.OnboardingWorkflow) (contracts.OnboardingWorkflow, error) {
	input = normalizeNewOnboardingWorkflow(input)
	if !validOnboardingWorkflow(input) {
		return contracts.OnboardingWorkflow{}, ErrConflict
	}
	if input.ID == "" {
		input.ID = newID("onboard")
	}
	versions, err := marshalKeyVersionSummary(input.KeyVersionSummary)
	if err != nil {
		return contracts.OnboardingWorkflow{}, err
	}
	workflow, err := scanOnboardingWorkflow(s.pool.QueryRow(ctx,
		`INSERT INTO onboarding_workflows
		 (id, user_id, instance_id, pool_id, connector_id, stage, status,
		  attempts, next_attempt_at, last_error_code, plan_id,
		  key_version_summary, desired_fingerprint, desired_generation,
		  last_ready_generation, last_ready_at, version, lease_owner, lease_until,
		  created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,1,0,NULL,1,'',NULL,
		         statement_timestamp(),statement_timestamp())
		 ON CONFLICT (instance_id, pool_id) DO UPDATE SET
		   connector_id=EXCLUDED.connector_id,
		   desired_fingerprint=EXCLUDED.desired_fingerprint,
		   desired_generation=CASE WHEN onboarding_workflows.status='dormant'
		                                OR onboarding_workflows.connector_id IS DISTINCT FROM EXCLUDED.connector_id
		                                OR onboarding_workflows.desired_fingerprint IS DISTINCT FROM EXCLUDED.desired_fingerprint
		                           THEN onboarding_workflows.desired_generation+1 ELSE onboarding_workflows.desired_generation END,
		   stage=CASE WHEN onboarding_workflows.connector_id IS DISTINCT FROM EXCLUDED.connector_id THEN 'waiting_connector'
		              WHEN onboarding_workflows.status='dormant'
		                OR onboarding_workflows.desired_fingerprint IS DISTINCT FROM EXCLUDED.desired_fingerprint THEN 'checking_gateway'
		              ELSE onboarding_workflows.stage END,
		   status=CASE WHEN onboarding_workflows.status='dormant'
		                    OR onboarding_workflows.connector_id IS DISTINCT FROM EXCLUDED.connector_id
		                    OR onboarding_workflows.desired_fingerprint IS DISTINCT FROM EXCLUDED.desired_fingerprint
		               THEN 'pending' ELSE onboarding_workflows.status END,
		   attempts=CASE WHEN onboarding_workflows.status='dormant'
		                      OR onboarding_workflows.connector_id IS DISTINCT FROM EXCLUDED.connector_id
		                      OR onboarding_workflows.desired_fingerprint IS DISTINCT FROM EXCLUDED.desired_fingerprint
		                 THEN 0 ELSE onboarding_workflows.attempts END,
		   next_attempt_at=CASE WHEN onboarding_workflows.status='dormant'
		                             OR onboarding_workflows.connector_id IS DISTINCT FROM EXCLUDED.connector_id
		                             OR onboarding_workflows.desired_fingerprint IS DISTINCT FROM EXCLUDED.desired_fingerprint
		                        THEN NULL ELSE onboarding_workflows.next_attempt_at END,
		   last_error_code=CASE WHEN onboarding_workflows.status='dormant'
		                             OR onboarding_workflows.connector_id IS DISTINCT FROM EXCLUDED.connector_id
		                             OR onboarding_workflows.desired_fingerprint IS DISTINCT FROM EXCLUDED.desired_fingerprint
		                        THEN '' ELSE onboarding_workflows.last_error_code END,
		   key_version_summary=CASE WHEN onboarding_workflows.status='dormant'
		                                 OR onboarding_workflows.connector_id IS DISTINCT FROM EXCLUDED.connector_id
		                                 OR onboarding_workflows.desired_fingerprint IS DISTINCT FROM EXCLUDED.desired_fingerprint
		                            THEN '{}'::jsonb ELSE onboarding_workflows.key_version_summary END,
		   lease_owner=CASE WHEN onboarding_workflows.status='dormant'
		                         OR onboarding_workflows.connector_id IS DISTINCT FROM EXCLUDED.connector_id
		                         OR onboarding_workflows.desired_fingerprint IS DISTINCT FROM EXCLUDED.desired_fingerprint
		                    THEN '' ELSE onboarding_workflows.lease_owner END,
		   lease_until=CASE WHEN onboarding_workflows.status='dormant'
		                         OR onboarding_workflows.connector_id IS DISTINCT FROM EXCLUDED.connector_id
		                         OR onboarding_workflows.desired_fingerprint IS DISTINCT FROM EXCLUDED.desired_fingerprint
		                    THEN NULL ELSE onboarding_workflows.lease_until END,
		   version=CASE WHEN onboarding_workflows.status='dormant'
		                      OR onboarding_workflows.connector_id IS DISTINCT FROM EXCLUDED.connector_id
		                      OR onboarding_workflows.desired_fingerprint IS DISTINCT FROM EXCLUDED.desired_fingerprint
		                THEN onboarding_workflows.version+1 ELSE onboarding_workflows.version END,
		   updated_at=CASE WHEN onboarding_workflows.status='dormant'
		                         OR onboarding_workflows.connector_id IS DISTINCT FROM EXCLUDED.connector_id
		                         OR onboarding_workflows.desired_fingerprint IS DISTINCT FROM EXCLUDED.desired_fingerprint
		                   THEN statement_timestamp() ELSE onboarding_workflows.updated_at END
		 WHERE onboarding_workflows.user_id=EXCLUDED.user_id
		 RETURNING `+onboardingColumns,
		input.ID, input.UserID, input.InstanceID, input.PoolID, input.ConnectorID,
		string(input.Stage), string(input.Status), input.Attempts, input.NextAttemptAt,
		input.LastErrorCode, input.PlanID, versions, input.DesiredFingerprint))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || isUniqueViolation(err) {
			return contracts.OnboardingWorkflow{}, ErrConflict
		}
		return contracts.OnboardingWorkflow{}, err
	}
	return workflow, nil
}

func (s *PostgresStore) GetOnboardingWorkflow(ctx context.Context, id string) (contracts.OnboardingWorkflow, error) {
	workflow, err := scanOnboardingWorkflow(s.pool.QueryRow(ctx,
		`SELECT `+onboardingColumns+` FROM onboarding_workflows WHERE id=$1`, id))
	if err != nil {
		return contracts.OnboardingWorkflow{}, mapNotFound(err)
	}
	return workflow, nil
}

func (s *PostgresStore) ListOnboardingWorkflows(ctx context.Context, filter contracts.OnboardingWorkflowFilter) ([]contracts.OnboardingWorkflow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+onboardingColumns+`
		 FROM onboarding_workflows
		 WHERE ($1=0 OR user_id=$1) AND ($2='' OR instance_id=$2)
		   AND ($3='' OR pool_id=$3) AND ($4='' OR connector_id=$4)
		   AND (cardinality($5::text[])=0 OR stage=ANY($5::text[]))
		   AND (cardinality($6::text[])=0 OR status=ANY($6::text[]))
		 ORDER BY updated_at DESC, id
		 LIMIT CASE WHEN $7>0 THEN $7 ELSE 2147483647 END`,
		filter.UserID, filter.InstanceID, filter.PoolID, filter.ConnectorID,
		onboardingStagesToStrings(filter.Stages), onboardingStatusesToStrings(filter.Statuses), filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.OnboardingWorkflow, 0)
	for rows.Next() {
		workflow, scanErr := scanOnboardingWorkflow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, workflow)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ClaimOnboardingWorkflow(ctx context.Context, workerID string, leaseDuration time.Duration) (contracts.OnboardingWorkflow, bool, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || leaseDuration <= 0 {
		return contracts.OnboardingWorkflow{}, false, ErrConflict
	}
	leaseMicros := leaseDuration.Microseconds()
	if leaseMicros <= 0 {
		return contracts.OnboardingWorkflow{}, false, ErrConflict
	}
	workflow, err := scanOnboardingWorkflow(s.pool.QueryRow(ctx,
		`WITH candidate AS (
		   SELECT id FROM onboarding_workflows
		    WHERE connector_id<>'' AND
		          ((status IN ('pending','retryable')
		             AND (next_attempt_at IS NULL OR next_attempt_at<=statement_timestamp()))
		       OR (status='active' AND next_attempt_at IS NOT NULL
		             AND next_attempt_at<=statement_timestamp())
		       OR (status='running'
		             AND (lease_until IS NULL OR lease_until<=statement_timestamp())))
		    ORDER BY updated_at, id
		    FOR UPDATE SKIP LOCKED
		    LIMIT 1
		 )
		 UPDATE onboarding_workflows w
		    SET status='running', attempts=w.attempts+1, next_attempt_at=NULL,
		        lease_owner=$1,
		        lease_until=statement_timestamp()+($2::bigint * interval '1 microsecond'),
		        version=w.version+1, updated_at=statement_timestamp()
		   FROM candidate
		  WHERE w.id=candidate.id
		 RETURNING `+prefixedOnboardingColumns("w"), workerID, leaseMicros))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.OnboardingWorkflow{}, false, nil
		}
		return contracts.OnboardingWorkflow{}, false, err
	}
	return workflow, true, nil
}

func (s *PostgresStore) RenewOnboardingWorkflowLease(
	ctx context.Context,
	id, workerID string,
	expectedVersion int64,
	leaseDuration time.Duration,
) (contracts.OnboardingWorkflow, error) {
	id = strings.TrimSpace(id)
	workerID = strings.TrimSpace(workerID)
	leaseMicros := leaseDuration.Microseconds()
	if id == "" || workerID == "" || expectedVersion <= 0 || leaseMicros <= 0 {
		return contracts.OnboardingWorkflow{}, ErrConflict
	}
	workflow, err := scanOnboardingWorkflow(s.pool.QueryRow(ctx,
		`UPDATE onboarding_workflows
		    SET lease_until=statement_timestamp()+($4::bigint * interval '1 microsecond'),
		        version=version+1, updated_at=statement_timestamp()
		  WHERE id=$1 AND version=$2 AND status='running' AND lease_owner=$3
		    AND lease_until IS NOT NULL AND lease_until>statement_timestamp()
		  RETURNING `+onboardingColumns,
		id, expectedVersion, workerID, leaseMicros))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.OnboardingWorkflow{}, s.onboardingConflictOrNotFound(ctx, id)
		}
		return contracts.OnboardingWorkflow{}, err
	}
	return workflow, nil
}

func (s *PostgresStore) ReleaseOnboardingWorkflowLease(
	ctx context.Context,
	id, workerID string,
	expectedVersion int64,
) error {
	id = strings.TrimSpace(id)
	workerID = strings.TrimSpace(workerID)
	if id == "" || workerID == "" || expectedVersion <= 0 {
		return ErrConflict
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE onboarding_workflows
		    SET lease_until=statement_timestamp(), version=version+1,
		        updated_at=statement_timestamp()
		  WHERE id=$1 AND version=$2 AND status='running' AND lease_owner=$3
		    AND lease_until IS NOT NULL AND lease_until>statement_timestamp()`,
		id, expectedVersion, workerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return s.onboardingConflictOrNotFound(ctx, id)
	}
	return nil
}

func (s *PostgresStore) onboardingConflictOrNotFound(ctx context.Context, id string) error {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM onboarding_workflows WHERE id=$1)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return ErrConflict
}

func (s *PostgresStore) TransitionOnboardingWorkflow(ctx context.Context, input contracts.OnboardingWorkflow, expectedVersion int64) (contracts.OnboardingWorkflow, error) {
	input = normalizeNewOnboardingWorkflow(input)
	if expectedVersion <= 0 || !validOnboardingWorkflow(input) || strings.TrimSpace(input.LeaseOwner) == "" {
		return contracts.OnboardingWorkflow{}, ErrConflict
	}
	versions, err := marshalKeyVersionSummary(input.KeyVersionSummary)
	if err != nil {
		return contracts.OnboardingWorkflow{}, err
	}
	keepLease := input.Status == contracts.OnboardingRunning
	workflow, err := scanOnboardingWorkflow(s.pool.QueryRow(ctx,
		`UPDATE onboarding_workflows SET
		   stage=$4, status=$5,
		   next_attempt_at=CASE WHEN $10 THEN NULL::timestamptz ELSE $6::timestamptz END,
		   last_error_code=$7,
		   plan_id=$8, key_version_summary=$9,
		   last_ready_generation=$11, last_ready_at=$12,
		   lease_owner=CASE WHEN $10 THEN lease_owner ELSE '' END,
		   lease_until=CASE WHEN $10 THEN lease_until ELSE NULL END,
		   version=version+1, updated_at=statement_timestamp()
		 WHERE id=$1 AND version=$2 AND status='running' AND lease_owner=$3
		   AND lease_until IS NOT NULL AND lease_until>statement_timestamp()
		   AND user_id=$13 AND instance_id=$14 AND pool_id=$15 AND connector_id=$16
		 RETURNING `+onboardingColumns,
		input.ID, expectedVersion, input.LeaseOwner, string(input.Stage), string(input.Status),
		input.NextAttemptAt, input.LastErrorCode, input.PlanID, versions, keepLease,
		input.LastReadyGeneration, input.LastReadyAt,
		input.UserID, input.InstanceID, input.PoolID, input.ConnectorID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if existsErr := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM onboarding_workflows WHERE id=$1)`, input.ID).Scan(&exists); existsErr != nil {
				return contracts.OnboardingWorkflow{}, existsErr
			}
			if !exists {
				return contracts.OnboardingWorkflow{}, ErrNotFound
			}
			return contracts.OnboardingWorkflow{}, ErrConflict
		}
		return contracts.OnboardingWorkflow{}, err
	}
	return workflow, nil
}

func prefixedOnboardingColumns(alias string) string {
	columns := []string{
		"id", "user_id", "instance_id", "pool_id", "connector_id", "stage", "status",
		"attempts", "next_attempt_at", "last_error_code", "plan_id", "key_version_summary",
		"desired_fingerprint", "desired_generation", "last_ready_generation", "last_ready_at",
		"version", "lease_owner", "lease_until", "created_at", "updated_at",
	}
	for i := range columns {
		columns[i] = alias + "." + columns[i]
	}
	return strings.Join(columns, ", ")
}

func onboardingStagesToStrings(stages []contracts.OnboardingStage) []string {
	out := make([]string, len(stages))
	for i, stage := range stages {
		out[i] = string(stage)
	}
	return out
}

func onboardingStatusesToStrings(statuses []contracts.OnboardingStatus) []string {
	out := make([]string, len(statuses))
	for i, status := range statuses {
		out[i] = string(status)
	}
	return out
}
