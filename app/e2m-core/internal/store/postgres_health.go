package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"e2m.local/contracts"
)

// PostgresStore implementations for the channel health-metrics layer.
// Observations are append-only; snapshots are scoped by downstream instance,
// channel and model, then retained per recompute bucket. "window" is a SQL
// keyword, so it is always double-quoted in these statements.

const channelHealthSnapshotCols = `id,channel_id,pool_id,instance_id,model,capability,endpoint_path,"window",bucket_start,sample_count,
	success_rate,ttft_p50,ttft_p95,duration_p50,duration_p95,
	error_rate,quality_sample_count,quality_success_rate,quality_error_rate,
	timeout_rate,rate_limit_rate,upstream_error_rate,
	upstream_failure_count,auth_failure_count,insufficient_balance_count,
	estimated_cost_per_1k_tokens,
	health_score,quality_score,success_score,ttft_score,duration_score,
	stability_score,cost_score,risk_score,health_state,created_at`

func (s *PostgresStore) AppendChannelObservation(ctx context.Context, input contracts.ChannelObservation) (contracts.ChannelObservation, error) {
	obs := input
	if obs.Source == "" {
		obs.Source = contracts.ObservationPassive
	}
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = nowUTC()
	} else {
		obs.ObservedAt = obs.ObservedAt.UTC().Truncate(time.Microsecond)
	}
	if obs.ID == "" {
		obs.ID = newID("chobs")
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO channel_observations
		   (id, channel_id, instance_id, pool_id, model, capability, endpoint_path, success, status_code, error_type,
		    first_token_ms, total_ms, input_tokens, output_tokens, estimated_cost, source, observed_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		 ON CONFLICT (id) DO NOTHING
		 RETURNING id, channel_id, instance_id, pool_id, model, capability, endpoint_path, success, status_code, error_type,
		           first_token_ms, total_ms, input_tokens, output_tokens, estimated_cost, source, observed_at`,
		obs.ID, obs.ChannelID, obs.InstanceID, obs.PoolID, obs.Model, string(obs.Capability), obs.EndpointPath, obs.Success, obs.StatusCode, string(obs.ErrorType),
		obs.FirstTokenMS, obs.TotalMS, obs.InputTokens, obs.OutputTokens, obs.EstimatedCost, string(obs.Source), obs.ObservedAt)
	saved, err := scanChannelObservation(row)
	if err == nil {
		return saved, nil
	}
	if !errors.Is(err, pgxNoRows()) {
		return contracts.ChannelObservation{}, err
	}
	existing, err := scanChannelObservation(s.pool.QueryRow(ctx,
		`SELECT id, channel_id, instance_id, pool_id, model, capability, endpoint_path, success, status_code, error_type,
		        first_token_ms, total_ms, input_tokens, output_tokens, estimated_cost, source, observed_at
		 FROM channel_observations WHERE id=$1`, obs.ID))
	if err != nil {
		return contracts.ChannelObservation{}, err
	}
	if !sameChannelObservation(existing, obs) {
		return contracts.ChannelObservation{}, ErrConflict
	}
	return existing, nil
}

func scanChannelObservation(row rowScanner) (contracts.ChannelObservation, error) {
	var o contracts.ChannelObservation
	var errType, source string
	if err := row.Scan(&o.ID, &o.ChannelID, &o.InstanceID, &o.PoolID, &o.Model, &o.Capability, &o.EndpointPath, &o.Success, &o.StatusCode, &errType,
		&o.FirstTokenMS, &o.TotalMS, &o.InputTokens, &o.OutputTokens, &o.EstimatedCost, &source, &o.ObservedAt); err != nil {
		return contracts.ChannelObservation{}, err
	}
	o.ErrorType = contracts.ObservationErrorType(errType)
	o.Source = contracts.ObservationSource(source)
	return o, nil
}

func (s *PostgresStore) ListChannelObservations(ctx context.Context, filter contracts.ChannelObservationFilter) ([]contracts.ChannelObservation, error) {
	// Dynamic AND-filter: only non-zero fields constrain the query, mirroring the
	// memory store's filter semantics.
	conds := []string{"1=1"}
	args := []any{}
	add := func(clause string, val any) {
		args = append(args, val)
		conds = append(conds, strings.Replace(clause, "?", "$"+strconv.Itoa(len(args)), 1))
	}
	if filter.ChannelID != "" {
		add("channel_id = ?", filter.ChannelID)
	}
	if filter.InstanceID != "" {
		add("instance_id = ?", filter.InstanceID)
	}
	if filter.PoolID != "" {
		add("pool_id = ?", filter.PoolID)
	}
	if filter.Model != "" {
		add("model = ?", filter.Model)
	}
	if filter.Capability != "" {
		add("capability = ?", string(filter.Capability))
	}
	if filter.EndpointPath != "" {
		add("endpoint_path = ?", filter.EndpointPath)
	}
	if filter.ExactScope {
		// ExactScope includes empty values; the non-empty predicates above may be
		// repeated harmlessly to keep this branch straightforward.
		add("channel_id = ?", filter.ChannelID)
		add("instance_id = ?", filter.InstanceID)
		add("model = ?", filter.Model)
		add("capability = ?", string(filter.Capability))
		add("endpoint_path = ?", filter.EndpointPath)
	}
	if filter.Source != "" {
		add("source = ?", string(filter.Source))
	}
	if !filter.Since.IsZero() {
		add("observed_at >= ?", filter.Since)
	}
	if !filter.Until.IsZero() {
		add("observed_at <= ?", filter.Until)
	}
	query := `SELECT id, channel_id, instance_id, pool_id, model, capability, endpoint_path, success, status_code, error_type,
	                 first_token_ms, total_ms, input_tokens, output_tokens, estimated_cost, source, observed_at
	          FROM channel_observations WHERE ` + strings.Join(conds, " AND ") + ` ORDER BY observed_at DESC`
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		query += " LIMIT $" + strconv.Itoa(len(args))
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.ChannelObservation
	for rows.Next() {
		o, err := scanChannelObservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpsertChannelHealthSnapshot(ctx context.Context, input contracts.ChannelHealthSnapshot) (contracts.ChannelHealthSnapshot, error) {
	snap, createdAtDefaulted := normalizeChannelHealthSnapshot(input, nowUTC())
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return contracts.ChannelHealthSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The removed scope+bucket UNIQUE constraint used to serialize rolling
	// overwrites. Retain that serialization boundary with a transaction-scoped
	// advisory lock so concurrent identical retries cannot append duplicates.
	lockKey := channelHealthSnapshotScopeLockKey(snap)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 710071))`, lockKey); err != nil {
		return contracts.ChannelHealthSnapshot{}, err
	}

	if snap.ID != "" {
		existing, lookupErr := scanChannelHealthSnapshot(tx.QueryRow(ctx,
			`SELECT `+channelHealthSnapshotCols+` FROM channel_health_snapshots WHERE id=$1`, snap.ID))
		if lookupErr == nil {
			if !sameChannelHealthSnapshot(existing, snap, createdAtDefaulted) {
				return contracts.ChannelHealthSnapshot{}, ErrConflict
			}
			if err := tx.Commit(ctx); err != nil {
				return contracts.ChannelHealthSnapshot{}, err
			}
			return existing, nil
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return contracts.ChannelHealthSnapshot{}, lookupErr
		}
	}

	rows, err := tx.Query(ctx, `SELECT `+channelHealthSnapshotCols+` FROM channel_health_snapshots
		WHERE instance_id=$1 AND channel_id=$2 AND model=$3 AND capability=$4 AND endpoint_path=$5
		  AND "window"=$6 AND bucket_start=$7
		ORDER BY created_at DESC,id DESC`, snap.InstanceID, snap.ChannelID, snap.Model,
		string(snap.Capability), snap.EndpointPath, string(snap.Window), snap.BucketStart)
	if err != nil {
		return contracts.ChannelHealthSnapshot{}, err
	}
	for rows.Next() {
		existing, scanErr := scanChannelHealthSnapshot(rows)
		if scanErr != nil {
			rows.Close()
			return contracts.ChannelHealthSnapshot{}, scanErr
		}
		if sameChannelHealthSnapshot(existing, snap, createdAtDefaulted) {
			rows.Close()
			if err := tx.Commit(ctx); err != nil {
				return contracts.ChannelHealthSnapshot{}, err
			}
			return existing, nil
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return contracts.ChannelHealthSnapshot{}, err
	}
	rows.Close()

	if snap.ID == "" {
		snap.ID = newID("chsnap")
	}
	saved, err := scanChannelHealthSnapshot(tx.QueryRow(ctx,
		`INSERT INTO channel_health_snapshots
		   (id, channel_id, pool_id, instance_id, model, capability, endpoint_path, "window", bucket_start, sample_count,
		    success_rate, ttft_p50, ttft_p95, duration_p50, duration_p95,
		    error_rate, quality_sample_count, quality_success_rate, quality_error_rate,
		    timeout_rate, rate_limit_rate, upstream_error_rate,
		    upstream_failure_count, auth_failure_count, insufficient_balance_count,
		    estimated_cost_per_1k_tokens,
		    health_score, quality_score, success_score, ttft_score, duration_score,
		    stability_score, cost_score, risk_score, health_state, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36)
		 RETURNING `+channelHealthSnapshotCols,
		snap.ID, snap.ChannelID, snap.PoolID, snap.InstanceID, snap.Model, string(snap.Capability), snap.EndpointPath, string(snap.Window), snap.BucketStart, snap.SampleCount,
		snap.SuccessRate, snap.TTFTP50, snap.TTFTP95, snap.DurationP50, snap.DurationP95,
		snap.ErrorRate, snap.QualitySampleCount, snap.QualitySuccessRate, snap.QualityErrorRate,
		snap.TimeoutRate, snap.RateLimitRate, snap.UpstreamErrorRate,
		snap.UpstreamFailureCount, snap.AuthFailureCount, snap.InsufficientBalanceCount,
		snap.EstimatedCostPer1KTokens,
		snap.HealthScore, snap.QualityScore, snap.SuccessScore, snap.TTFTScore, snap.DurationScore,
		snap.StabilityScore, snap.CostScore, snap.RiskScore, string(snap.HealthState), snap.CreatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.ChannelHealthSnapshot{}, ErrConflict
		}
		return contracts.ChannelHealthSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.ChannelHealthSnapshot{}, err
	}
	return saved, nil
}

// channelHealthSnapshotScopeLockKey is length-prefixed rather than delimiter
// separated. It is unambiguous even when a scope component contains punctuation
// and, critically, never introduces the NUL byte PostgreSQL rejects in text.
func channelHealthSnapshotScopeLockKey(snap contracts.ChannelHealthSnapshot) string {
	parts := []string{
		snap.InstanceID, snap.ChannelID, snap.Model, string(snap.Capability),
		snap.EndpointPath, string(snap.Window), snap.BucketStart.Format(time.RFC3339Nano),
	}
	var key strings.Builder
	for _, part := range parts {
		key.WriteString(strconv.Itoa(len(part)))
		key.WriteByte(':')
		for _, value := range []byte(part) {
			const hex = "0123456789abcdef"
			key.WriteByte(hex[value>>4])
			key.WriteByte(hex[value&0x0f])
		}
	}
	return key.String()
}

func scanChannelHealthSnapshot(row rowScanner) (contracts.ChannelHealthSnapshot, error) {
	var snap contracts.ChannelHealthSnapshot
	var window, state string
	if err := row.Scan(&snap.ID, &snap.ChannelID, &snap.PoolID, &snap.InstanceID, &snap.Model, &snap.Capability, &snap.EndpointPath, &window, &snap.BucketStart, &snap.SampleCount,
		&snap.SuccessRate, &snap.TTFTP50, &snap.TTFTP95, &snap.DurationP50, &snap.DurationP95,
		&snap.ErrorRate, &snap.QualitySampleCount, &snap.QualitySuccessRate, &snap.QualityErrorRate,
		&snap.TimeoutRate, &snap.RateLimitRate, &snap.UpstreamErrorRate,
		&snap.UpstreamFailureCount, &snap.AuthFailureCount, &snap.InsufficientBalanceCount,
		&snap.EstimatedCostPer1KTokens,
		&snap.HealthScore, &snap.QualityScore, &snap.SuccessScore, &snap.TTFTScore, &snap.DurationScore,
		&snap.StabilityScore, &snap.CostScore, &snap.RiskScore, &state, &snap.CreatedAt); err != nil {
		return contracts.ChannelHealthSnapshot{}, err
	}
	snap.Window = contracts.HealthWindow(window)
	snap.HealthState = contracts.HealthState(state)
	return snap, nil
}

func (s *PostgresStore) ListChannelHealthSnapshots(ctx context.Context, filter contracts.ChannelHealthSnapshotFilter) ([]contracts.ChannelHealthSnapshot, error) {
	conds := []string{"1=1"}
	args := []any{}
	add := func(clause string, val any) {
		args = append(args, val)
		conds = append(conds, strings.Replace(clause, "?", "$"+strconv.Itoa(len(args)), 1))
	}
	if filter.ChannelID != "" {
		add("channel_id = ?", filter.ChannelID)
	}
	if filter.InstanceID != "" {
		add("instance_id = ?", filter.InstanceID)
	}
	if filter.PoolID != "" {
		add("pool_id = ?", filter.PoolID)
	}
	if filter.Model != "" {
		add("model = ?", filter.Model)
	}
	if filter.Capability != "" {
		add("capability = ?", string(filter.Capability))
	}
	if filter.EndpointPath != "" {
		add("endpoint_path = ?", filter.EndpointPath)
	}
	if filter.ExactScope {
		add("channel_id = ?", filter.ChannelID)
		add("instance_id = ?", filter.InstanceID)
		add("model = ?", filter.Model)
		add("capability = ?", string(filter.Capability))
		add("endpoint_path = ?", filter.EndpointPath)
	}
	if filter.Window != "" {
		add(`"window" = ?`, string(filter.Window))
	}
	if !filter.BucketStart.IsZero() {
		add("bucket_start = ?", filter.BucketStart)
	}
	if !filter.Since.IsZero() {
		add("bucket_start >= ?", filter.Since)
	}
	var query string
	if filter.IncludeHistory {
		query = `SELECT ` + channelHealthSnapshotCols + ` FROM channel_health_snapshots WHERE ` + strings.Join(conds, " AND ") +
			` ORDER BY bucket_start DESC, created_at DESC, id DESC`
	} else {
		query = `SELECT ` + channelHealthSnapshotCols + ` FROM (
			SELECT DISTINCT ON (instance_id, channel_id, model, capability, endpoint_path, "window") ` + channelHealthSnapshotCols + `
			FROM channel_health_snapshots WHERE ` + strings.Join(conds, " AND ") + `
			ORDER BY instance_id, channel_id, model, capability, endpoint_path, "window", bucket_start DESC, created_at DESC, id DESC
		) AS current_snapshots ORDER BY bucket_start DESC, created_at DESC, id DESC`
	}
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		query += " LIMIT $" + strconv.Itoa(len(args))
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.ChannelHealthSnapshot
	for rows.Next() {
		snap, err := scanChannelHealthSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}
