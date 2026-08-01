package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func supersedeActiveAutoSwitchDecisionsPostgres(
	ctx context.Context,
	tx pgx.Tx,
	planID, keepDecisionID string,
	generation int64,
) error {
	reason := autoSwitchSupersededReason(generation)
	_, err := tx.Exec(ctx,
		`UPDATE auto_switch_decisions
		 SET status='failed', error=$4, observation_note=$4, lease_until=NULL,
		     resolved_at=statement_timestamp(), updated_at=statement_timestamp()
		 WHERE plan_id=$1 AND ($2='' OR id<>$2)
		   AND status IN ('proposed','approved','applying','observing')
		   AND scheduling_generation < $3`,
		planID, keepDecisionID, generation, reason)
	return err
}
