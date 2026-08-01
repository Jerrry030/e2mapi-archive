package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"e2m.local/contracts"
	"e2m.local/core/internal/upstreamrecommendation"
)

func scanUpstreamShadowResult(row rowScanner) (contracts.UpstreamShadowResult, error) {
	var value contracts.UpstreamShadowResult
	var winner, ranking, evidence []byte
	if err := row.Scan(&value.ID, &value.UserID, &value.RecommendationID, &value.RecommendationFingerprint,
		&winner, &ranking, &evidence, &value.EvaluatedAt); err != nil {
		return contracts.UpstreamShadowResult{}, err
	}
	value.EvaluatedAt = normalizeUpstreamExperimentTime(value.EvaluatedAt)
	if json.Unmarshal(winner, &value.Winner) != nil || json.Unmarshal(ranking, &value.Ranking) != nil || json.Unmarshal(evidence, &value.EvidenceIDs) != nil || !validShadowResult(value) {
		return contracts.UpstreamShadowResult{}, ErrInvalid
	}
	return cloneShadowResult(value), nil
}

func (s *PostgresStore) AppendUpstreamShadowResult(ctx context.Context, value contracts.UpstreamShadowResult) (contracts.UpstreamShadowResult, error) {
	value = cloneShadowResult(value)
	value.EvaluatedAt = normalizeUpstreamExperimentTime(value.EvaluatedAt)
	if !validShadowResult(value) {
		return contracts.UpstreamShadowResult{}, ErrInvalid
	}
	winner, _ := json.Marshal(value.Winner)
	ranking, _ := json.Marshal(value.Ranking)
	evidence, _ := json.Marshal(value.EvidenceIDs)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.UpstreamShadowResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `INSERT INTO upstream_shadow_results
		(id,user_id,recommendation_id,recommendation_fingerprint,winner,ranking,evidence_ids,evaluated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (user_id,id) DO NOTHING`,
		value.ID, value.UserID, value.RecommendationID, value.RecommendationFingerprint, winner, ranking, evidence, value.EvaluatedAt)
	if err != nil {
		return contracts.UpstreamShadowResult{}, mapUpstreamWriteError(err)
	}
	stored, err := scanUpstreamShadowResult(tx.QueryRow(ctx, `SELECT id,user_id,recommendation_id,recommendation_fingerprint,winner,ranking,evidence_ids,evaluated_at
		FROM upstream_shadow_results WHERE user_id=$1 AND id=$2`, value.UserID, value.ID))
	if err != nil {
		return contracts.UpstreamShadowResult{}, err
	}
	if !reflectShadowEqual(stored, value) {
		return contracts.UpstreamShadowResult{}, ErrConflict
	}
	if command.RowsAffected() == 1 {
		if err := recordOperationalMetricTx(ctx, tx, "experiments", "shadow", 1); err != nil {
			return contracts.UpstreamShadowResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.UpstreamShadowResult{}, err
	}
	return stored, nil
}

func (s *PostgresStore) CompleteUpstreamShadow(ctx context.Context, expected contracts.UpstreamRecommendation, value contracts.UpstreamShadowResult) (contracts.UpstreamRecommendation, contracts.UpstreamShadowResult, error) {
	expected = cloneUpstreamRecommendation(expected)
	value = cloneShadowResult(value)
	value.EvaluatedAt = normalizeUpstreamExperimentTime(value.EvaluatedAt)
	if expected.Status != contracts.UpstreamRecommendationOpen || upstreamrecommendation.Validate(expected) != nil || !validShadowResult(value) || !shadowResultMatchesRecommendation(value, expected) {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, ErrInvalid
	}
	next, err := completedShadowRecommendation(expected, value.EvaluatedAt)
	if err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanUpstreamRecommendation(tx.QueryRow(ctx, `SELECT `+upstreamRecommendationColumns+`
		FROM upstream_recommendations WHERE user_id=$1 AND id=$2 FOR UPDATE`, expected.UserID, expected.ID))
	if err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, mapNotFound(err)
	}
	existing, existingErr := scanUpstreamShadowResult(tx.QueryRow(ctx, `SELECT id,user_id,recommendation_id,recommendation_fingerprint,winner,ranking,evidence_ids,evaluated_at
		FROM upstream_shadow_results WHERE user_id=$1 AND id=$2`, value.UserID, value.ID))
	if existingErr == nil {
		if !reflectShadowEqual(existing, value) || current.Status != next.Status || current.DryRunID != next.DryRunID || !recommendationImmutableEqual(current, expected) {
			return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, ErrConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, err
		}
		return current, existing, nil
	}
	if !errors.Is(existingErr, pgx.ErrNoRows) {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, existingErr
	}
	if current.Status != expected.Status || !recommendationImmutableEqual(current, expected) {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, ErrConflict
	}
	winner, _ := json.Marshal(value.Winner)
	ranking, _ := json.Marshal(value.Ranking)
	evidence, _ := json.Marshal(value.EvidenceIDs)
	if _, err := tx.Exec(ctx, `INSERT INTO upstream_shadow_results
		(id,user_id,recommendation_id,recommendation_fingerprint,winner,ranking,evidence_ids,evaluated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, value.ID, value.UserID, value.RecommendationID,
		value.RecommendationFingerprint, winner, ranking, evidence, value.EvaluatedAt); err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, mapUpstreamWriteError(err)
	}
	if err := recordOperationalMetricTx(ctx, tx, "experiments", "shadow", 1); err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, err
	}
	updated, err := scanUpstreamRecommendation(tx.QueryRow(ctx, `UPDATE upstream_recommendations SET status=$3,dry_run_id=$4
		WHERE user_id=$1 AND id=$2 AND status=$5 RETURNING `+upstreamRecommendationColumns,
		next.UserID, next.ID, string(next.Status), next.DryRunID, string(expected.Status)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, ErrConflict
		}
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, mapUpstreamWriteError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, err
	}
	return updated, value, nil
}

func reflectShadowEqual(left, right contracts.UpstreamShadowResult) bool {
	l, _ := json.Marshal(left)
	r, _ := json.Marshal(right)
	return string(l) == string(r)
}

func (s *PostgresStore) GetUpstreamShadowResult(ctx context.Context, userID int64, id string) (contracts.UpstreamShadowResult, error) {
	if userID <= 0 || strings.TrimSpace(id) == "" {
		return contracts.UpstreamShadowResult{}, ErrInvalid
	}
	value, err := scanUpstreamShadowResult(s.pool.QueryRow(ctx, `SELECT id,user_id,recommendation_id,recommendation_fingerprint,winner,ranking,evidence_ids,evaluated_at
		FROM upstream_shadow_results WHERE user_id=$1 AND id=$2`, userID, id))
	if err != nil {
		return contracts.UpstreamShadowResult{}, mapNotFound(err)
	}
	return value, nil
}

func (s *PostgresStore) ListUpstreamShadowResults(ctx context.Context, userID int64, recommendationID string, limit int) ([]contracts.UpstreamShadowResult, error) {
	if userID <= 0 {
		return nil, ErrInvalid
	}
	limit = contracts.NormalizeUpstreamIntelligenceListLimit(limit)
	rows, err := s.pool.Query(ctx, `SELECT id,user_id,recommendation_id,recommendation_fingerprint,winner,ranking,evidence_ids,evaluated_at
		FROM upstream_shadow_results WHERE user_id=$1 AND ($2='' OR recommendation_id=$2) ORDER BY evaluated_at DESC,id DESC LIMIT $3`, userID, recommendationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]contracts.UpstreamShadowResult, 0)
	for rows.Next() {
		value, err := scanUpstreamShadowResult(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func scanUpstreamDryRunResult(row rowScanner) (contracts.UpstreamDryRunResult, error) {
	var value contracts.UpstreamDryRunResult
	var desired, plan []byte
	var reconcileKind string
	if err := row.Scan(&value.ID, &value.UserID, &value.RecommendationID, &value.RecommendationFingerprint,
		&value.IntelligenceFactVersion, &value.CostLedgerFactVersion, &value.LinkFactVersion, &value.PlanGeneration,
		&value.PlanID, &value.FromChannelID, &value.ToChannelID, &desired, &reconcileKind, &plan,
		&value.ActionHashVersion, &value.ActionSetHash, &value.CreatedAt); err != nil {
		return contracts.UpstreamDryRunResult{}, err
	}
	value.CreatedAt = normalizeUpstreamExperimentTime(value.CreatedAt)
	value.ReconcileKind = contracts.ReconcileRunKind(reconcileKind)
	if json.Unmarshal(desired, &value.DesiredScheduling) != nil || json.Unmarshal(plan, &value.Plan) != nil || !validDryRunResult(value) {
		return contracts.UpstreamDryRunResult{}, ErrInvalid
	}
	return cloneDryRunResult(value), nil
}

func (s *PostgresStore) AppendUpstreamDryRunResult(ctx context.Context, value contracts.UpstreamDryRunResult) (contracts.UpstreamDryRunResult, error) {
	value = cloneDryRunResult(value)
	value = normalizeDryRunResultTimes(value)
	if !validDryRunResult(value) {
		return contracts.UpstreamDryRunResult{}, ErrInvalid
	}
	desired, _ := json.Marshal(value.DesiredScheduling)
	plan, _ := json.Marshal(value.Plan)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.UpstreamDryRunResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `INSERT INTO upstream_dry_run_results
		(id,user_id,recommendation_id,recommendation_fingerprint,intelligence_fact_version,cost_ledger_fact_version,link_fact_version,plan_generation,
		 plan_id,from_channel_id,to_channel_id,desired_scheduling,reconcile_kind,reconcile_plan,action_hash_version,action_set_hash,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) ON CONFLICT (user_id,id) DO NOTHING`,
		value.ID, value.UserID, value.RecommendationID, value.RecommendationFingerprint, value.IntelligenceFactVersion, value.CostLedgerFactVersion,
		value.LinkFactVersion, value.PlanGeneration, value.PlanID, value.FromChannelID, value.ToChannelID, desired, string(value.ReconcileKind), plan,
		value.ActionHashVersion, value.ActionSetHash, value.CreatedAt)
	if err != nil {
		return contracts.UpstreamDryRunResult{}, mapUpstreamWriteError(err)
	}
	stored, err := scanUpstreamDryRunResult(tx.QueryRow(ctx, `SELECT id,user_id,recommendation_id,recommendation_fingerprint,intelligence_fact_version,cost_ledger_fact_version,link_fact_version,plan_generation,
		plan_id,from_channel_id,to_channel_id,desired_scheduling,reconcile_kind,reconcile_plan,action_hash_version,action_set_hash,created_at
		FROM upstream_dry_run_results WHERE user_id=$1 AND id=$2`, value.UserID, value.ID))
	if err != nil {
		return contracts.UpstreamDryRunResult{}, err
	}
	l, _ := json.Marshal(stored)
	r, _ := json.Marshal(value)
	if string(l) != string(r) {
		return contracts.UpstreamDryRunResult{}, ErrConflict
	}
	if command.RowsAffected() == 1 {
		if err := recordOperationalMetricTx(ctx, tx, "experiments", "dry_run", 1); err != nil {
			return contracts.UpstreamDryRunResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.UpstreamDryRunResult{}, err
	}
	return stored, nil
}

func (s *PostgresStore) CompleteUpstreamDryRun(ctx context.Context, expected contracts.UpstreamRecommendation, value contracts.UpstreamDryRunResult) (contracts.UpstreamRecommendation, contracts.UpstreamDryRunResult, error) {
	expected = cloneUpstreamRecommendation(expected)
	value = cloneDryRunResult(value)
	value = normalizeDryRunResultTimes(value)
	if expected.Status != contracts.UpstreamRecommendationReadyForDryRun || upstreamrecommendation.Validate(expected) != nil || !validDryRunResult(value) || !dryRunResultMatchesRecommendation(value, expected) {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, ErrInvalid
	}
	next, err := completedDryRunRecommendation(expected, value.ID, value.CreatedAt)
	if err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanUpstreamRecommendation(tx.QueryRow(ctx, `SELECT `+upstreamRecommendationColumns+`
		FROM upstream_recommendations WHERE user_id=$1 AND id=$2 FOR UPDATE`, expected.UserID, expected.ID))
	if err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, mapNotFound(err)
	}
	existing, existingErr := scanUpstreamDryRunResult(tx.QueryRow(ctx, `SELECT id,user_id,recommendation_id,recommendation_fingerprint,intelligence_fact_version,cost_ledger_fact_version,link_fact_version,plan_generation,
		plan_id,from_channel_id,to_channel_id,desired_scheduling,reconcile_kind,reconcile_plan,action_hash_version,action_set_hash,created_at
		FROM upstream_dry_run_results WHERE user_id=$1 AND id=$2`, value.UserID, value.ID))
	if existingErr == nil {
		left, _ := json.Marshal(existing)
		right, _ := json.Marshal(value)
		if string(left) != string(right) || current.Status != next.Status || current.DryRunID != next.DryRunID || !recommendationImmutableEqual(current, expected) {
			return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, ErrConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, err
		}
		return current, existing, nil
	}
	if !errors.Is(existingErr, pgx.ErrNoRows) {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, existingErr
	}
	if current.Status != expected.Status || !recommendationImmutableEqual(current, expected) {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, ErrConflict
	}
	desired, _ := json.Marshal(value.DesiredScheduling)
	plan, _ := json.Marshal(value.Plan)
	if _, err := tx.Exec(ctx, `INSERT INTO upstream_dry_run_results
		(id,user_id,recommendation_id,recommendation_fingerprint,intelligence_fact_version,cost_ledger_fact_version,link_fact_version,plan_generation,
		 plan_id,from_channel_id,to_channel_id,desired_scheduling,reconcile_kind,reconcile_plan,action_hash_version,action_set_hash,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		value.ID, value.UserID, value.RecommendationID, value.RecommendationFingerprint, value.IntelligenceFactVersion, value.CostLedgerFactVersion,
		value.LinkFactVersion, value.PlanGeneration, value.PlanID, value.FromChannelID, value.ToChannelID, desired, string(value.ReconcileKind), plan,
		value.ActionHashVersion, value.ActionSetHash, value.CreatedAt); err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, mapUpstreamWriteError(err)
	}
	if err := recordOperationalMetricTx(ctx, tx, "experiments", "dry_run", 1); err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, err
	}
	updated, err := scanUpstreamRecommendation(tx.QueryRow(ctx, `UPDATE upstream_recommendations SET status=$3,dry_run_id=$4
		WHERE user_id=$1 AND id=$2 AND status=$5 RETURNING `+upstreamRecommendationColumns,
		next.UserID, next.ID, string(next.Status), next.DryRunID, string(expected.Status)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, ErrConflict
		}
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, mapUpstreamWriteError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, err
	}
	return updated, value, nil
}

func (s *PostgresStore) GetUpstreamDryRunResult(ctx context.Context, userID int64, id string) (contracts.UpstreamDryRunResult, error) {
	if userID <= 0 || strings.TrimSpace(id) == "" {
		return contracts.UpstreamDryRunResult{}, ErrInvalid
	}
	value, err := scanUpstreamDryRunResult(s.pool.QueryRow(ctx, `SELECT id,user_id,recommendation_id,recommendation_fingerprint,intelligence_fact_version,cost_ledger_fact_version,link_fact_version,plan_generation,
		plan_id,from_channel_id,to_channel_id,desired_scheduling,reconcile_kind,reconcile_plan,action_hash_version,action_set_hash,created_at
		FROM upstream_dry_run_results WHERE user_id=$1 AND id=$2`, userID, id))
	if err != nil {
		return contracts.UpstreamDryRunResult{}, mapNotFound(err)
	}
	return value, nil
}

func (s *PostgresStore) ListUpstreamDryRunResults(ctx context.Context, userID int64, recommendationID string, limit int) ([]contracts.UpstreamDryRunResult, error) {
	if userID <= 0 {
		return nil, ErrInvalid
	}
	limit = contracts.NormalizeUpstreamIntelligenceListLimit(limit)
	rows, err := s.pool.Query(ctx, `SELECT id,user_id,recommendation_id,recommendation_fingerprint,intelligence_fact_version,cost_ledger_fact_version,link_fact_version,plan_generation,
		plan_id,from_channel_id,to_channel_id,desired_scheduling,reconcile_kind,reconcile_plan,action_hash_version,action_set_hash,created_at
		FROM upstream_dry_run_results WHERE user_id=$1 AND ($2='' OR recommendation_id=$2) ORDER BY created_at DESC,id DESC LIMIT $3`, userID, recommendationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]contracts.UpstreamDryRunResult, 0)
	for rows.Next() {
		value, err := scanUpstreamDryRunResult(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

var _ = errors.Is
var _ = pgx.ErrNoRows
