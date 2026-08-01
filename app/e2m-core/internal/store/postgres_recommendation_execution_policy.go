package store

import (
	"context"
	"fmt"

	"e2m.local/contracts"
)

const recommendationExecutionPolicyColumns = `id,user_id,scope,plan_id,pool_id,enabled,kill_switch,
	daily_execution_cap,cooldown_seconds,minimum_savings::text,version,created_at,updated_at`

func scanRecommendationExecutionPolicy(row rowScanner) (contracts.RecommendationExecutionPolicy, error) {
	var policy contracts.RecommendationExecutionPolicy
	var scope, minimum string
	if err := row.Scan(
		&policy.ID, &policy.UserID, &scope, &policy.PlanID, &policy.PoolID,
		&policy.Enabled, &policy.KillSwitch, &policy.DailyExecutionCap,
		&policy.CooldownSeconds, &minimum, &policy.Version,
		&policy.CreatedAt, &policy.UpdatedAt,
	); err != nil {
		return contracts.RecommendationExecutionPolicy{}, err
	}
	decimal, err := contracts.CanonicalizeUpstreamDecimalText(minimum)
	if err != nil {
		return contracts.RecommendationExecutionPolicy{}, fmt.Errorf("store: invalid recommendation execution minimum savings: %w", err)
	}
	policy.Scope = contracts.RecommendationExecutionScope(scope)
	policy.MinimumSavings = decimal
	return policy, nil
}

func (s *PostgresStore) GetRecommendationExecutionPolicy(ctx context.Context, userID int64, scope contracts.RecommendationExecutionScope, targetID string) (contracts.RecommendationExecutionPolicy, error) {
	if _, valid := recommendationExecutionPolicyKey(userID, scope, targetID); !valid {
		return contracts.RecommendationExecutionPolicy{}, ErrInvalid
	}
	policy, err := scanRecommendationExecutionPolicy(s.pool.QueryRow(ctx,
		`SELECT `+recommendationExecutionPolicyColumns+` FROM recommendation_execution_policies
		 WHERE user_id=$1 AND scope=$2
		   AND (($2='plan' AND plan_id=$3 AND pool_id='') OR ($2='pool' AND pool_id=$3 AND plan_id=''))`,
		userID, string(scope), targetID))
	if err != nil {
		return contracts.RecommendationExecutionPolicy{}, mapNotFound(err)
	}
	return policy, nil
}

func (s *PostgresStore) ListRecommendationExecutionPolicies(ctx context.Context, userID int64) ([]contracts.RecommendationExecutionPolicy, error) {
	if userID <= 0 {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+recommendationExecutionPolicyColumns+` FROM recommendation_execution_policies
		 WHERE user_id=$1 ORDER BY scope,CASE scope WHEN 'plan' THEN plan_id ELSE pool_id END,id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.RecommendationExecutionPolicy, 0)
	for rows.Next() {
		policy, scanErr := scanRecommendationExecutionPolicy(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, policy)
	}
	return out, rows.Err()
}

// UpsertRecommendationExecutionPolicy uses a version compare-and-swap. The
// immutable owner/scope target is selected in the WHERE clause rather than by
// policy ID, preventing stale callers from moving an opt-in to another scope.
func (s *PostgresStore) UpsertRecommendationExecutionPolicy(ctx context.Context, input contracts.RecommendationExecutionPolicy, expectedVersion int64) (contracts.RecommendationExecutionPolicy, error) {
	if _, valid := recommendationExecutionPolicyInputKey(input); !validRecommendationExecutionPolicyInput(input, expectedVersion) || !valid {
		return contracts.RecommendationExecutionPolicy{}, ErrInvalid
	}
	if !s.postgresRecommendationExecutionTargetOwned(ctx, input) {
		return contracts.RecommendationExecutionPolicy{}, ErrConflict
	}
	if expectedVersion == 0 {
		id := input.ID
		if id == "" {
			id = newID("rec-policy")
		}
		policy, err := scanRecommendationExecutionPolicy(s.pool.QueryRow(ctx,
			`INSERT INTO recommendation_execution_policies
			 (id,user_id,scope,plan_id,pool_id,enabled,kill_switch,daily_execution_cap,cooldown_seconds,minimum_savings,version,created_at,updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,1,statement_timestamp(),statement_timestamp())
			 ON CONFLICT DO NOTHING
			 RETURNING `+recommendationExecutionPolicyColumns,
			id, input.UserID, string(input.Scope), input.PlanID, input.PoolID,
			input.Enabled, input.KillSwitch, input.DailyExecutionCap,
			input.CooldownSeconds, string(input.MinimumSavings)))
		if err != nil {
			if mapNotFound(err) == ErrNotFound || isUniqueViolation(err) {
				return contracts.RecommendationExecutionPolicy{}, ErrConflict
			}
			return contracts.RecommendationExecutionPolicy{}, mapUpstreamWriteError(err)
		}
		return policy, nil
	}

	policy, err := scanRecommendationExecutionPolicy(s.pool.QueryRow(ctx,
		`UPDATE recommendation_execution_policies SET
		   enabled=$5,kill_switch=$6,daily_execution_cap=$7,cooldown_seconds=$8,
		   minimum_savings=$9,version=version+1,updated_at=statement_timestamp()
		 WHERE user_id=$1 AND scope=$2 AND plan_id=$3 AND pool_id=$4 AND version=$10
		   AND ($11='' OR id=$11)
		 RETURNING `+recommendationExecutionPolicyColumns,
		input.UserID, string(input.Scope), input.PlanID, input.PoolID,
		input.Enabled, input.KillSwitch, input.DailyExecutionCap,
		input.CooldownSeconds, string(input.MinimumSavings), expectedVersion, input.ID))
	if err != nil {
		if mapNotFound(err) == ErrNotFound {
			return contracts.RecommendationExecutionPolicy{}, ErrConflict
		}
		return contracts.RecommendationExecutionPolicy{}, mapUpstreamWriteError(err)
	}
	return policy, nil
}

func (s *PostgresStore) postgresRecommendationExecutionTargetOwned(ctx context.Context, policy contracts.RecommendationExecutionPolicy) bool {
	var owned bool
	var err error
	switch policy.Scope {
	case contracts.RecommendationExecutionScopePlan:
		err = s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM route_plans WHERE id=$1 AND user_id=$2)`,
			policy.PlanID, policy.UserID).Scan(&owned)
	case contracts.RecommendationExecutionScopePool:
		err = s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM pool_rollout_targets WHERE pool_id=$1 AND user_id=$2 AND enabled)`,
			policy.PoolID, policy.UserID).Scan(&owned)
	default:
		return false
	}
	return err == nil && owned
}
