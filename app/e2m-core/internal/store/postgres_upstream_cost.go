package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"e2m.local/contracts"
)

const upstreamCostFactCols = `id,idempotency_key,user_id,fact_version,usage_observation_id,channel_id,instance_id,
	intelligence_source_id,model_key,group_key,price_dimension,quantity,per_tokens,price_observation_id,
	price_effective_at,price_valid_until,unit_cost::text,amount::text,currency,attribution,price_status,
	calculation_version,reason_code,missing_fields,occurred_at,created_at`

func scanUpstreamCostFact(row rowScanner) (contracts.UpstreamCostFact, error) {
	var fact contracts.UpstreamCostFact
	var dimension, attribution, priceStatus string
	var unitCost, amount *string
	var missing []byte
	if err := row.Scan(
		&fact.ID, &fact.IdempotencyKey, &fact.UserID, &fact.FactVersion, &fact.UsageObservationID,
		&fact.ChannelID, &fact.InstanceID, &fact.IntelligenceSourceID, &fact.ModelKey, &fact.GroupKey,
		&dimension, &fact.Quantity, &fact.PerTokens, &fact.PriceObservationID, &fact.PriceEffectiveAt,
		&fact.PriceValidUntil, &unitCost, &amount, &fact.Currency, &attribution, &priceStatus,
		&fact.CalculationVersion, &fact.ReasonCode, &missing, &fact.OccurredAt, &fact.CreatedAt,
	); err != nil {
		return contracts.UpstreamCostFact{}, err
	}
	var err error
	if fact.UnitCost, err = scanDecimal(unitCost); err != nil {
		return contracts.UpstreamCostFact{}, err
	}
	if fact.Amount, err = scanDecimal(amount); err != nil {
		return contracts.UpstreamCostFact{}, err
	}
	if err := json.Unmarshal(missing, &fact.MissingFields); err != nil {
		return contracts.UpstreamCostFact{}, err
	}
	fact.PriceDimension = contracts.UpstreamPriceDimension(dimension)
	fact.Attribution = contracts.UpstreamCostAttribution(attribution)
	fact.PriceStatus = contracts.UpstreamCostPriceStatus(priceStatus)
	return normalizeUpstreamCostFact(fact), nil
}

func (s *PostgresStore) AppendUpstreamCostFacts(ctx context.Context, facts []contracts.UpstreamCostFact) ([]contracts.UpstreamCostFact, contracts.UpstreamCostFactVersion, error) {
	prepared, err := prepareUpstreamCostBatch(facts)
	if err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inserted, version, err := appendUpstreamCostFactsTx(ctx, tx, prepared)
	if err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	return inserted, version, nil
}

func appendUpstreamCostFactsTx(ctx context.Context, tx pgx.Tx, prepared []contracts.UpstreamCostFact) ([]contracts.UpstreamCostFact, contracts.UpstreamCostFactVersion, error) {
	// The owner row is both the monotonic version and the serialization lock.
	// Replays take the same lock but do not advance it.
	if _, err := tx.Exec(ctx, `INSERT INTO upstream_cost_fact_versions (user_id,fact_version,updated_at)
		VALUES ($1,0,statement_timestamp()) ON CONFLICT (user_id) DO NOTHING`, prepared[0].UserID); err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, mapUpstreamWriteError(err)
	}
	var current contracts.UpstreamCostFactVersion
	if err := tx.QueryRow(ctx, `SELECT user_id,fact_version,updated_at FROM upstream_cost_fact_versions WHERE user_id=$1 FOR UPDATE`, prepared[0].UserID).
		Scan(&current.UserID, &current.FactVersion, &current.UpdatedAt); err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, mapNotFound(err)
	}

	existing := make([]contracts.UpstreamCostFact, 0, len(prepared))
	for _, fact := range prepared {
		stored, scanErr := scanUpstreamCostFact(tx.QueryRow(ctx, `SELECT `+upstreamCostFactCols+`
			FROM upstream_cost_facts WHERE user_id=$1 AND idempotency_key=$2`, fact.UserID, fact.IdempotencyKey))
		if scanErr == nil {
			if !costFactsEqualIgnoringServerFields(stored, fact) {
				return nil, contracts.UpstreamCostFactVersion{}, ErrConflict
			}
			existing = append(existing, stored)
			continue
		}
		if !errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, contracts.UpstreamCostFactVersion{}, scanErr
		}
	}
	if len(existing) != 0 {
		if len(existing) != len(prepared) {
			return nil, contracts.UpstreamCostFactVersion{}, ErrConflict
		}
		version := contracts.UpstreamCostFactVersion{UserID: existing[0].UserID, FactVersion: existing[0].FactVersion, UpdatedAt: existing[0].CreatedAt}
		return cloneUpstreamCostFacts(existing), version, nil
	}

	var version contracts.UpstreamCostFactVersion
	if err := tx.QueryRow(ctx, `UPDATE upstream_cost_fact_versions
		SET fact_version=fact_version+1,updated_at=statement_timestamp() WHERE user_id=$1
		RETURNING user_id,fact_version,updated_at`, prepared[0].UserID).
		Scan(&version.UserID, &version.FactVersion, &version.UpdatedAt); err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	inserted := make([]contracts.UpstreamCostFact, 0, len(prepared))
	for _, fact := range prepared {
		missing, err := jsonObject(fact.MissingFields)
		if err != nil {
			return nil, contracts.UpstreamCostFactVersion{}, ErrInvalid
		}
		fact.ID, fact.FactVersion = newID("ucost"), version.FactVersion
		stored, err := scanUpstreamCostFact(tx.QueryRow(ctx, `INSERT INTO upstream_cost_facts
			(id,idempotency_key,user_id,fact_version,usage_observation_id,channel_id,instance_id,intelligence_source_id,
			 model_key,group_key,price_dimension,quantity,per_tokens,price_observation_id,price_effective_at,price_valid_until,
			 unit_cost,amount,currency,attribution,price_status,calculation_version,reason_code,missing_fields,occurred_at,created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)
			RETURNING `+upstreamCostFactCols,
			fact.ID, fact.IdempotencyKey, fact.UserID, fact.FactVersion, fact.UsageObservationID, fact.ChannelID,
			fact.InstanceID, fact.IntelligenceSourceID, fact.ModelKey, fact.GroupKey, string(fact.PriceDimension), fact.Quantity,
			fact.PerTokens, fact.PriceObservationID, fact.PriceEffectiveAt, fact.PriceValidUntil, decimalText(fact.UnitCost),
			decimalText(fact.Amount), fact.Currency, string(fact.Attribution), string(fact.PriceStatus), fact.CalculationVersion,
			fact.ReasonCode, missing, fact.OccurredAt, version.UpdatedAt))
		if err != nil {
			return nil, contracts.UpstreamCostFactVersion{}, mapUpstreamWriteError(err)
		}
		inserted = append(inserted, stored)
	}
	return inserted, version, nil
}

func (s *PostgresStore) ListUpstreamCostFacts(ctx context.Context, filter contracts.UpstreamCostFactFilter) ([]contracts.UpstreamCostFact, error) {
	if err := requireUpstreamOwner(filter.UserID); err != nil {
		return nil, err
	}
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	rows, err := s.pool.Query(ctx, `SELECT `+upstreamCostFactCols+` FROM upstream_cost_facts
		WHERE user_id=$1 AND ($2='' OR channel_id=$2) AND ($3='' OR intelligence_source_id=$3)
		 AND ($4='' OR model_key=$4) AND ($5='' OR price_dimension=$5) AND ($6='' OR attribution=$6)
		 AND ($7::timestamptz='epoch' OR occurred_at >= $7) AND ($8::timestamptz='epoch' OR occurred_at < $8)
		ORDER BY occurred_at DESC,id DESC LIMIT $9`, filter.UserID, filter.ChannelID, filter.SourceID, filter.ModelKey,
		string(filter.PriceDimension), string(filter.Attribution), filter.Since, filter.Until, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamCostFact, 0)
	for rows.Next() {
		fact, err := scanUpstreamCostFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, fact)
	}
	return out, rows.Err()
}
