package store

import (
	"context"
	"encoding/json"

	"e2m.local/contracts"
	"github.com/jackc/pgx/v5"
)

// supersedeConnectorTasksForPlanPostgres is called in the same transaction as
// every route-plan generation advance. Clearing the lease nonce makes a task
// that was already handed to Connector unable to record a late success.
func supersedeConnectorTasksForPlanPostgres(ctx context.Context, tx pgx.Tx, planID string, generation int64) error {
	if planID == "" || generation < 0 {
		return ErrInvalid
	}
	// A newly created route plan legitimately starts at generation zero. It
	// cannot own a valid fenced task (fence versions are strictly positive), so
	// the pre-claim cleanup is a no-op until the writer advances it to one.
	if generation == 0 {
		return nil
	}
	// An executing task owns the plan's durable remote-side-effect permit. All
	// generation writers hold the route-plan row before reaching this helper,
	// so an execution permit and a generation advance cannot both commit. The
	// caller must roll its entire transaction back and leave the executing task
	// for explicit completion or operator resolution.
	var executing bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM connector_tasks WHERE plan_id=$1 AND status=$2)`,
		planID, string(contracts.ConnectorTaskExecuting),
	).Scan(&executing); err != nil {
		return err
	}
	if executing {
		return ErrConflict
	}
	errorPayload, err := json.Marshal(contracts.ConnectorTaskError{Code: connectorTaskSupersededErrorCode})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`UPDATE connector_tasks
		 SET status=$3,result='null'::jsonb,error=$4::jsonb,
		     lease_owner='',lease_nonce='',lease_until=NULL,updated_at=statement_timestamp()
		 WHERE plan_id=$1 AND scheduling_generation<$2 AND status IN ($5,$6)`,
		planID, generation, string(contracts.ConnectorTaskFailed), string(errorPayload),
		string(contracts.ConnectorTaskPending), string(contracts.ConnectorTaskLeased))
	return err
}

func supersedeRoutePlanOwnersPostgres(
	ctx context.Context,
	tx pgx.Tx,
	planID, keepDecisionID string,
	generation int64,
) error {
	if err := supersedeActiveAutoSwitchDecisionsPostgres(ctx, tx, planID, keepDecisionID, generation); err != nil {
		return err
	}
	return supersedeConnectorTasksForPlanPostgres(ctx, tx, planID, generation)
}
