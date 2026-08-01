package store

import (
	"context"
	"encoding/json"

	"e2m.local/contracts"
)

// PostgresStore implementation for persisted route strategies (Phase 5). The
// thresholds/weights blobs are stored as JSONB; scope + owner columns let the
// orchestrator resolve a plan's effective strategy by precedence. Upsert keys on
// the (scope, owner) unique index so re-saving a scope's strategy replaces it.

func marshalThresholds(v contracts.StrategyThresholds) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func unmarshalThresholds(raw []byte) contracts.StrategyThresholds {
	var out contracts.StrategyThresholds
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func marshalWeights(v contracts.StrategyWeights) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func unmarshalWeights(raw []byte) contracts.StrategyWeights {
	var out contracts.StrategyWeights
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

const routeStrategyColumns = `id, name, type, scope, plan_id, pool_id, user_id,
	thresholds, weights, auto_apply, approval_required,
	cooldown_seconds, recovery_observation_seconds, max_auto_switches_per_hour,
	created_at, updated_at`

func scanRouteStrategy(row rowScanner) (contracts.RouteStrategy, error) {
	var s contracts.RouteStrategy
	var typ, scope string
	var thresholds, weights []byte
	if err := row.Scan(&s.ID, &s.Name, &typ, &scope, &s.PlanID, &s.PoolID, &s.UserID,
		&thresholds, &weights, &s.AutoApply, &s.ApprovalRequired,
		&s.CooldownSeconds, &s.RecoveryObservationSeconds, &s.MaxAutoSwitchesPerHour,
		&s.CreatedAt, &s.UpdatedAt); err != nil {
		return contracts.RouteStrategy{}, err
	}
	s.Type = contracts.RouteStrategyType(typ)
	s.Scope = contracts.StrategyScope(scope)
	s.Thresholds = unmarshalThresholds(thresholds)
	s.Weights = unmarshalWeights(weights)
	return s, nil
}

func (s *PostgresStore) UpsertRouteStrategy(ctx context.Context, input contracts.RouteStrategy) (contracts.RouteStrategy, error) {
	rec := normalizeRouteStrategyRecord(input)
	if rec.ID == "" {
		rec.ID = newID("strategy")
	}
	now := nowUTC()
	rec.CreatedAt, rec.UpdatedAt = now, now
	// Conflict target is the (scope, owner) unique index: re-saving a scope's
	// strategy replaces it in place and keeps the original created_at.
	_, err := s.pool.Exec(ctx,
		`INSERT INTO route_strategies
		 (id, name, type, scope, plan_id, pool_id, user_id, thresholds, weights,
		  auto_apply, approval_required, cooldown_seconds, recovery_observation_seconds,
		  max_auto_switches_per_hour, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		 ON CONFLICT (scope, plan_id, pool_id, user_id) DO UPDATE SET
		   name=EXCLUDED.name, type=EXCLUDED.type, thresholds=EXCLUDED.thresholds,
		   weights=EXCLUDED.weights, auto_apply=EXCLUDED.auto_apply,
		   approval_required=EXCLUDED.approval_required,
		   cooldown_seconds=EXCLUDED.cooldown_seconds,
		   recovery_observation_seconds=EXCLUDED.recovery_observation_seconds,
		   max_auto_switches_per_hour=EXCLUDED.max_auto_switches_per_hour,
		   updated_at=EXCLUDED.updated_at`,
		rec.ID, rec.Name, string(rec.Type), string(rec.Scope), rec.PlanID, rec.PoolID, rec.UserID,
		marshalThresholds(rec.Thresholds), marshalWeights(rec.Weights),
		rec.AutoApply, rec.ApprovalRequired, rec.CooldownSeconds, rec.RecoveryObservationSeconds,
		rec.MaxAutoSwitchesPerHour, rec.CreatedAt, rec.UpdatedAt)
	if err != nil {
		return contracts.RouteStrategy{}, err
	}
	// Read back so the returned row reflects the persisted id/created_at even when
	// the upsert updated an existing (scope, owner) row.
	return s.getRouteStrategyByScope(ctx, rec.Scope, rec.PlanID, rec.PoolID, rec.UserID)
}

func (s *PostgresStore) getRouteStrategyByScope(ctx context.Context, scope contracts.StrategyScope, planID, poolID string, userID int64) (contracts.RouteStrategy, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+routeStrategyColumns+` FROM route_strategies
		 WHERE scope=$1 AND plan_id=$2 AND pool_id=$3 AND user_id=$4`,
		string(scope), planID, poolID, userID)
	st, err := scanRouteStrategy(row)
	if err != nil {
		return contracts.RouteStrategy{}, mapNotFound(err)
	}
	return st, nil
}

func (s *PostgresStore) GetRouteStrategy(ctx context.Context, id string) (contracts.RouteStrategy, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+routeStrategyColumns+` FROM route_strategies WHERE id=$1`, id)
	st, err := scanRouteStrategy(row)
	if err != nil {
		return contracts.RouteStrategy{}, mapNotFound(err)
	}
	return st, nil
}

func (s *PostgresStore) ListRouteStrategies(ctx context.Context, filter contracts.RouteStrategyFilter) ([]contracts.RouteStrategy, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+routeStrategyColumns+` FROM route_strategies
		 WHERE ($1='' OR scope=$1)
		   AND ($2='' OR plan_id=$2)
		   AND ($3='' OR pool_id=$3)
		   AND ($4=0 OR user_id=$4)
		 ORDER BY created_at DESC`,
		string(filter.Scope), filter.PlanID, filter.PoolID, filter.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.RouteStrategy
	for rows.Next() {
		st, err := scanRouteStrategy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *PostgresStore) DeleteRouteStrategy(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM route_strategies WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
