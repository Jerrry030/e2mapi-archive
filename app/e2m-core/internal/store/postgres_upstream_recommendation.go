package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"e2m.local/contracts"
	"e2m.local/core/internal/upstreamrecommendation"
)

const upstreamRecommendationColumns = `id,user_id,status,intelligence_fact_version,cost_ledger_fact_version,
	link_fact_version,plan_generation,from_source_id,from_channel_id,from_group_key,to_source_id,to_channel_id,to_group_key,
	model_key,price_dimension,settlement_currency,per_tokens,affected_plan_ids,affected_downstreams,evidence_ids,constraints,
	from_cost_lower::text,from_cost_expected::text,from_cost_upper::text,to_cost_lower::text,to_cost_expected::text,to_cost_upper::text,
	savings_amount_lower::text,savings_amount_expected::text,savings_amount_upper::text,
	savings_percent_lower::text,savings_percent_expected::text,savings_percent_upper::text,
	formula_version,strategy_version,fingerprint,dry_run_id,created_at,expires_at`

func scanUpstreamRecommendation(row rowScanner) (contracts.UpstreamRecommendation, error) {
	var value contracts.UpstreamRecommendation
	var status, dimension string
	var plans, downstreams, evidence, constraints []byte
	var decimals [12]string
	if err := row.Scan(
		&value.ID, &value.UserID, &status, &value.IntelligenceFactVersion, &value.CostLedgerFactVersion,
		&value.LinkFactVersion, &value.PlanGeneration, &value.FromSourceID, &value.FromChannelID, &value.FromGroupKey,
		&value.ToSourceID, &value.ToChannelID, &value.ToGroupKey, &value.ModelKey, &dimension,
		&value.SettlementCurrency, &value.PerTokens, &plans, &downstreams, &evidence, &constraints,
		&decimals[0], &decimals[1], &decimals[2], &decimals[3], &decimals[4], &decimals[5],
		&decimals[6], &decimals[7], &decimals[8], &decimals[9], &decimals[10], &decimals[11],
		&value.FormulaVersion, &value.StrategyVersion, &value.Fingerprint, &value.DryRunID, &value.CreatedAt, &value.ExpiresAt,
	); err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	value.Status = contracts.UpstreamRecommendationStatus(status)
	value.PriceDimension = contracts.UpstreamPriceDimension(dimension)
	if err := json.Unmarshal(plans, &value.AffectedPlanIDs); err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	if err := json.Unmarshal(downstreams, &value.AffectedDownstreams); err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	if err := json.Unmarshal(evidence, &value.EvidenceIDs); err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	if err := json.Unmarshal(constraints, &value.Constraints); err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	parsed := make([]contracts.CanonicalDecimal, len(decimals))
	for index, raw := range decimals {
		decimal, err := contracts.CanonicalizeUpstreamDecimalText(raw)
		if err != nil {
			return contracts.UpstreamRecommendation{}, fmt.Errorf("store: invalid recommendation decimal: %w", err)
		}
		parsed[index] = decimal
	}
	value.FromCost = contracts.UpstreamRecommendationCostRange{Lower: parsed[0], Expected: parsed[1], Upper: parsed[2]}
	value.ToCost = contracts.UpstreamRecommendationCostRange{Lower: parsed[3], Expected: parsed[4], Upper: parsed[5]}
	value.Savings = contracts.UpstreamRecommendationSavingsRange{
		AmountLower: parsed[6], AmountExpected: parsed[7], AmountUpper: parsed[8],
		PercentLower: parsed[9], PercentExpected: parsed[10], PercentUpper: parsed[11],
	}
	value.CreatedAt = normalizeUpstreamTime(value.CreatedAt)
	value.ExpiresAt = normalizeUpstreamTime(value.ExpiresAt)
	return value, nil
}

func recommendationJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalid
	}
	return encoded, nil
}

func (s *PostgresStore) CreateUpstreamRecommendation(ctx context.Context, input contracts.UpstreamRecommendation) (contracts.UpstreamRecommendation, error) {
	input = cloneUpstreamRecommendation(input)
	if err := upstreamrecommendation.Validate(input); err != nil {
		return contracts.UpstreamRecommendation{}, ErrInvalid
	}
	plans, err := recommendationJSON(input.AffectedPlanIDs)
	if err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	downstreams, err := recommendationJSON(input.AffectedDownstreams)
	if err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	evidence, err := recommendationJSON(input.EvidenceIDs)
	if err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	constraints, err := recommendationJSON(input.Constraints)
	if err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	stored, err := scanUpstreamRecommendation(s.pool.QueryRow(ctx, `INSERT INTO upstream_recommendations (
		id,user_id,status,intelligence_fact_version,cost_ledger_fact_version,link_fact_version,plan_generation,
		from_source_id,from_channel_id,from_group_key,to_source_id,to_channel_id,to_group_key,model_key,price_dimension,
		settlement_currency,per_tokens,affected_plan_ids,affected_downstreams,evidence_ids,constraints,
		from_cost_lower,from_cost_expected,from_cost_upper,to_cost_lower,to_cost_expected,to_cost_upper,
		savings_amount_lower,savings_amount_expected,savings_amount_upper,savings_percent_lower,savings_percent_expected,savings_percent_upper,
		formula_version,strategy_version,fingerprint,dry_run_id,created_at,expires_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39)
	ON CONFLICT (user_id,fingerprint) DO NOTHING RETURNING `+upstreamRecommendationColumns,
		input.ID, input.UserID, string(input.Status), input.IntelligenceFactVersion, input.CostLedgerFactVersion,
		input.LinkFactVersion, input.PlanGeneration, input.FromSourceID, input.FromChannelID, input.FromGroupKey,
		input.ToSourceID, input.ToChannelID, input.ToGroupKey, input.ModelKey, string(input.PriceDimension), input.SettlementCurrency,
		input.PerTokens, plans, downstreams, evidence, constraints,
		string(input.FromCost.Lower), string(input.FromCost.Expected), string(input.FromCost.Upper),
		string(input.ToCost.Lower), string(input.ToCost.Expected), string(input.ToCost.Upper),
		string(input.Savings.AmountLower), string(input.Savings.AmountExpected), string(input.Savings.AmountUpper),
		string(input.Savings.PercentLower), string(input.Savings.PercentExpected), string(input.Savings.PercentUpper),
		input.FormulaVersion, input.StrategyVersion, input.Fingerprint, input.DryRunID, input.CreatedAt, input.ExpiresAt))
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		if isUniqueViolation(err) {
			return contracts.UpstreamRecommendation{}, ErrConflict
		}
		return contracts.UpstreamRecommendation{}, mapUpstreamWriteError(err)
	}
	stored, err = scanUpstreamRecommendation(s.pool.QueryRow(ctx, `SELECT `+upstreamRecommendationColumns+`
		FROM upstream_recommendations WHERE user_id=$1 AND fingerprint=$2`, input.UserID, input.Fingerprint))
	if err == nil {
		return stored, nil
	}
	return contracts.UpstreamRecommendation{}, mapNotFound(err)
}

func (s *PostgresStore) GetUpstreamRecommendation(ctx context.Context, userID int64, id string) (contracts.UpstreamRecommendation, error) {
	if userID <= 0 || id == "" {
		return contracts.UpstreamRecommendation{}, ErrInvalid
	}
	return queryUpstreamRecommendation(ctx, s.pool, userID, id)
}

func queryUpstreamRecommendation(ctx context.Context, queryer upstreamReadQueryer, userID int64, id string) (contracts.UpstreamRecommendation, error) {
	value, err := scanUpstreamRecommendation(queryer.QueryRow(ctx, `SELECT `+upstreamRecommendationColumns+`
		FROM upstream_recommendations WHERE user_id=$1 AND id=$2`, userID, id))
	if err != nil {
		return contracts.UpstreamRecommendation{}, mapNotFound(err)
	}
	return value, nil
}

func (s *PostgresStore) ListUpstreamRecommendations(ctx context.Context, filter contracts.UpstreamRecommendationFilter) ([]contracts.UpstreamRecommendation, error) {
	if filter.UserID <= 0 || !validRecommendationStatusFilter(filter.Status) {
		return nil, ErrInvalid
	}
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	rows, err := s.pool.Query(ctx, `SELECT `+upstreamRecommendationColumns+` FROM upstream_recommendations
		WHERE user_id=$1 AND ($2='' OR status=$2) ORDER BY created_at DESC,id DESC LIMIT $3`,
		filter.UserID, string(filter.Status), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]contracts.UpstreamRecommendation, 0)
	for rows.Next() {
		value, scanErr := scanUpstreamRecommendation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *PostgresStore) TransitionUpstreamRecommendation(ctx context.Context, next contracts.UpstreamRecommendation, expectedStatus contracts.UpstreamRecommendationStatus) (contracts.UpstreamRecommendation, error) {
	if err := upstreamrecommendation.Validate(next); err != nil || !contracts.IsUpstreamRecommendationStatus(expectedStatus) {
		return contracts.UpstreamRecommendation{}, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanUpstreamRecommendation(tx.QueryRow(ctx, `SELECT `+upstreamRecommendationColumns+`
		FROM upstream_recommendations WHERE user_id=$1 AND id=$2 FOR UPDATE`, next.UserID, next.ID))
	if err != nil {
		return contracts.UpstreamRecommendation{}, mapNotFound(err)
	}
	if current.Status != expectedStatus || !recommendationImmutableEqual(current, next) {
		return contracts.UpstreamRecommendation{}, ErrConflict
	}
	updated, err := scanUpstreamRecommendation(tx.QueryRow(ctx, `UPDATE upstream_recommendations SET status=$3,dry_run_id=$4
		WHERE user_id=$1 AND id=$2 AND status=$5 RETURNING `+upstreamRecommendationColumns,
		next.UserID, next.ID, string(next.Status), next.DryRunID, string(expectedStatus)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.UpstreamRecommendation{}, ErrConflict
		}
		return contracts.UpstreamRecommendation{}, mapUpstreamWriteError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	return updated, nil
}
