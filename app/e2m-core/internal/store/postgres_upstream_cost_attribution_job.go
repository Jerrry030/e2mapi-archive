package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"e2m.local/contracts"
)

const upstreamCostJobCols = `usage_observation_id,user_id,channel_id,instance_id,model_key,group_key,
	input_tokens,output_tokens,cached_input_tokens,request_count,occurred_at,calculation_version,status,
	attempts,next_attempt_at,last_error_code,lease_owner,lease_until,lease_version,created_at,updated_at,completed_at`

func scanUpstreamCostAttributionJob(row rowScanner) (UpstreamCostAttributionJob, error) {
	var job UpstreamCostAttributionJob
	var status string
	err := row.Scan(&job.UsageObservationID, &job.UserID, &job.ChannelID, &job.InstanceID, &job.ModelKey,
		&job.GroupKey, &job.InputTokens, &job.OutputTokens, &job.CachedInputTokens, &job.RequestCount,
		&job.OccurredAt, &job.CalculationVersion, &status, &job.Attempts, &job.NextAttemptAt,
		&job.LastErrorCode, &job.LeaseOwner, &job.LeaseUntil, &job.LeaseVersion, &job.CreatedAt,
		&job.UpdatedAt, &job.CompletedAt)
	job.Status = UpstreamCostAttributionJobStatus(status)
	if err != nil {
		return UpstreamCostAttributionJob{}, mapNotFound(err)
	}
	return cloneUpstreamCostJob(job), nil
}

func (s *PostgresStore) AppendChannelObservationWithCostJob(ctx context.Context, input contracts.ChannelObservation, jobInput *UpstreamCostAttributionJob) (contracts.ChannelObservation, error) {
	if jobInput == nil || strings.TrimSpace(input.ID) == "" {
		return contracts.ChannelObservation{}, ErrInvalid
	}
	obs := input
	if obs.Source == "" {
		obs.Source = contracts.ObservationPassive
	}
	if obs.ObservedAt.IsZero() {
		return contracts.ChannelObservation{}, ErrInvalid
	}
	obs.ObservedAt = normalizeUpstreamTime(obs.ObservedAt)
	job := *jobInput
	job.OccurredAt = obs.ObservedAt
	normalized, err := normalizeUpstreamCostJob(job)
	if err != nil || normalized.UsageObservationID != obs.ID ||
		normalized.ChannelID != obs.ChannelID || normalized.InstanceID != obs.InstanceID ||
		normalized.ModelKey != obs.Model {
		return contracts.ChannelObservation{}, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return contracts.ChannelObservation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ownerMatches bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM upstream_channel_allocations WHERE user_id=$1 AND channel_id=$2
	)`, normalized.UserID, normalized.ChannelID).Scan(&ownerMatches); err != nil {
		return contracts.ChannelObservation{}, err
	}
	if !ownerMatches {
		return contracts.ChannelObservation{}, ErrNotFound
	}
	saved, err := appendChannelObservationQuery(ctx, tx, obs)
	if err != nil {
		return contracts.ChannelObservation{}, err
	}
	storedJob, err := scanUpstreamCostAttributionJob(tx.QueryRow(ctx, `INSERT INTO upstream_cost_attribution_jobs
		(usage_observation_id,user_id,channel_id,instance_id,model_key,group_key,input_tokens,output_tokens,
		 cached_input_tokens,request_count,occurred_at,calculation_version,status,next_attempt_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'pending',statement_timestamp())
		ON CONFLICT (usage_observation_id) DO NOTHING RETURNING `+upstreamCostJobCols,
		normalized.UsageObservationID, normalized.UserID, normalized.ChannelID, normalized.InstanceID,
		normalized.ModelKey, normalized.GroupKey, normalized.InputTokens, normalized.OutputTokens,
		normalized.CachedInputTokens, normalized.RequestCount, normalized.OccurredAt, normalized.CalculationVersion))
	if err == ErrNotFound {
		storedJob, err = scanUpstreamCostAttributionJob(tx.QueryRow(ctx,
			`SELECT `+upstreamCostJobCols+` FROM upstream_cost_attribution_jobs WHERE usage_observation_id=$1`,
			normalized.UsageObservationID))
		if err == nil && !sameUpstreamCostJobPayload(storedJob, normalized) {
			err = ErrConflict
		}
	}
	if err != nil {
		return contracts.ChannelObservation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.ChannelObservation{}, err
	}
	return saved, nil
}

func appendChannelObservationQuery(ctx context.Context, queryer upstreamQueryer, obs contracts.ChannelObservation) (contracts.ChannelObservation, error) {
	row := queryer.QueryRow(ctx,
		`INSERT INTO channel_observations
		   (id,channel_id,instance_id,pool_id,model,capability,endpoint_path,success,status_code,error_type,
		    first_token_ms,total_ms,input_tokens,output_tokens,estimated_cost,source,observed_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		 ON CONFLICT (id) DO NOTHING
		 RETURNING id,channel_id,instance_id,pool_id,model,capability,endpoint_path,success,status_code,error_type,
		           first_token_ms,total_ms,input_tokens,output_tokens,estimated_cost,source,observed_at`,
		obs.ID, obs.ChannelID, obs.InstanceID, obs.PoolID, obs.Model, string(obs.Capability), obs.EndpointPath,
		obs.Success, obs.StatusCode, string(obs.ErrorType), obs.FirstTokenMS, obs.TotalMS, obs.InputTokens,
		obs.OutputTokens, obs.EstimatedCost, string(obs.Source), obs.ObservedAt)
	saved, err := scanChannelObservation(row)
	if err == nil {
		return saved, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.ChannelObservation{}, err
	}
	existing, err := scanChannelObservation(queryer.QueryRow(ctx,
		`SELECT id,channel_id,instance_id,pool_id,model,capability,endpoint_path,success,status_code,error_type,
		        first_token_ms,total_ms,input_tokens,output_tokens,estimated_cost,source,observed_at
		   FROM channel_observations WHERE id=$1`, obs.ID))
	if err != nil {
		return contracts.ChannelObservation{}, err
	}
	if !sameChannelObservation(existing, obs) {
		return contracts.ChannelObservation{}, ErrConflict
	}
	return existing, nil
}

func (s *PostgresStore) ClaimUpstreamCostAttributionJob(ctx context.Context, workerID string, leaseDuration time.Duration) (UpstreamCostAttributionJob, bool, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || leaseDuration <= 0 {
		return UpstreamCostAttributionJob{}, false, ErrInvalid
	}
	job, err := scanUpstreamCostAttributionJob(s.pool.QueryRow(ctx, `WITH candidate AS (
		SELECT usage_observation_id FROM upstream_cost_attribution_jobs
		 WHERE ((status IN ('pending','retrying') AND next_attempt_at <= statement_timestamp())
		        OR (status='processing' AND lease_until <= statement_timestamp()))
		 ORDER BY next_attempt_at,created_at
		 FOR UPDATE SKIP LOCKED LIMIT 1
	)
	UPDATE upstream_cost_attribution_jobs j
	   SET status='processing',attempts=j.attempts+1,lease_version=j.lease_version+1,lease_owner=$1,
	       lease_until=statement_timestamp()+($2 * interval '1 microsecond'),updated_at=statement_timestamp()
	  FROM candidate WHERE j.usage_observation_id=candidate.usage_observation_id
	RETURNING `+prefixedUpstreamCostJobCols("j"), workerID, leaseDuration.Microseconds()))
	if err == ErrNotFound {
		return UpstreamCostAttributionJob{}, false, nil
	}
	return job, err == nil, err
}

func prefixedUpstreamCostJobCols(alias string) string {
	parts := strings.Split(upstreamCostJobCols, ",")
	for i := range parts {
		parts[i] = alias + "." + strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ",")
}

func (s *PostgresStore) LoadUpstreamCostAttributionEvidence(ctx context.Context, job UpstreamCostAttributionJob) ([]contracts.UpstreamIntelligenceLink, []contracts.UpstreamOfferObservation, error) {
	normalized, err := normalizeUpstreamCostJob(job)
	if err != nil {
		return nil, nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	linkRows, err := tx.Query(ctx, `SELECT `+linkCols+` FROM upstream_intelligence_links
		WHERE user_id=$1 AND link_scope='channel' AND channel_id=$2 AND status='active'
		  AND verified_at IS NOT NULL ORDER BY id`, normalized.UserID, normalized.ChannelID)
	if err != nil {
		return nil, nil, err
	}
	links := make([]contracts.UpstreamIntelligenceLink, 0, 4)
	sources := make([]string, 0, 4)
	for linkRows.Next() {
		link, scanErr := scanLink(linkRows)
		if scanErr != nil {
			linkRows.Close()
			return nil, nil, scanErr
		}
		links = append(links, link)
		sources = append(sources, link.IntelligenceSourceID)
	}
	if err := linkRows.Err(); err != nil {
		linkRows.Close()
		return nil, nil, err
	}
	linkRows.Close()
	if len(sources) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, nil, err
		}
		return links, nil, nil
	}
	offerRows, err := tx.Query(ctx, `WITH relevant AS (
		SELECT o.*,
		       max(o.effective_at) FILTER (WHERE o.effective_at <= $4) OVER
		         (PARTITION BY o.source_id,o.price_dimension) AS latest_before,
		       min(o.effective_at) FILTER (WHERE o.effective_at > $4) OVER
		         (PARTITION BY o.source_id,o.price_dimension) AS earliest_after
		  FROM upstream_offer_observations o
		  JOIN upstream_collection_runs r
		    ON r.user_id=o.user_id AND r.id=o.run_id AND r.source_id=o.source_id
		 WHERE o.user_id=$1 AND o.source_id=ANY($2::text[]) AND o.group_key=$3
		   AND o.model_key=$5 AND r.finalized_fact_version > 0
	)
	SELECT `+offerCols+` FROM relevant
	 WHERE effective_at=latest_before OR effective_at=earliest_after
	 ORDER BY effective_at,id`, normalized.UserID, sources, normalized.GroupKey, normalized.OccurredAt, normalized.ModelKey)
	if err != nil {
		return nil, nil, err
	}
	offers := make([]contracts.UpstreamOfferObservation, 0)
	for offerRows.Next() {
		offer, scanErr := scanOffer(offerRows)
		if scanErr != nil {
			offerRows.Close()
			return nil, nil, scanErr
		}
		offers = append(offers, offer)
	}
	if err := offerRows.Err(); err != nil {
		offerRows.Close()
		return nil, nil, err
	}
	offerRows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return links, offers, nil
}

func (s *PostgresStore) CompleteUpstreamCostAttributionJob(ctx context.Context, claim UpstreamCostAttributionJob, facts []contracts.UpstreamCostFact) ([]contracts.UpstreamCostFact, contracts.UpstreamCostFactVersion, error) {
	prepared, err := prepareUpstreamCostBatch(facts)
	if err != nil || !upstreamCostFactsMatchJob(prepared, claim) {
		return nil, contracts.UpstreamCostFactVersion{}, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanUpstreamCostAttributionJob(tx.QueryRow(ctx, `SELECT `+upstreamCostJobCols+`
		FROM upstream_cost_attribution_jobs WHERE usage_observation_id=$1 FOR UPDATE`, claim.UsageObservationID))
	if err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	var leaseCurrent bool
	if err := tx.QueryRow(ctx, `SELECT lease_until > statement_timestamp() FROM upstream_cost_attribution_jobs
		WHERE usage_observation_id=$1`, claim.UsageObservationID).Scan(&leaseCurrent); err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	if current.Status != UpstreamCostJobProcessing || current.LeaseOwner != claim.LeaseOwner ||
		current.LeaseVersion != claim.LeaseVersion || !leaseCurrent {
		return nil, contracts.UpstreamCostFactVersion{}, ErrConflict
	}
	saved, version, err := appendUpstreamCostFactsTx(ctx, tx, prepared)
	if err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE upstream_cost_attribution_jobs
		SET status='succeeded',last_error_code='',lease_owner='',lease_until=NULL,
		    completed_at=statement_timestamp(),updated_at=statement_timestamp()
		WHERE usage_observation_id=$1 AND status='processing' AND lease_owner=$2 AND lease_version=$3
		  AND lease_until > statement_timestamp()`, claim.UsageObservationID, claim.LeaseOwner, claim.LeaseVersion)
	if err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	if tag.RowsAffected() != 1 {
		return nil, contracts.UpstreamCostFactVersion{}, ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	return saved, version, nil
}

func (s *PostgresStore) RetryUpstreamCostAttributionJob(ctx context.Context, claim UpstreamCostAttributionJob, errorCode string, delay time.Duration) (UpstreamCostAttributionJob, error) {
	errorCode = strings.TrimSpace(errorCode)
	if !retryableUpstreamCostJobErrorCode(errorCode) || delay < 0 {
		return UpstreamCostAttributionJob{}, ErrInvalid
	}
	job, err := scanUpstreamCostAttributionJob(s.pool.QueryRow(ctx, `UPDATE upstream_cost_attribution_jobs
		SET status='retrying',last_error_code=$4,next_attempt_at=statement_timestamp()+($5 * interval '1 microsecond'),
		    lease_owner='',lease_until=NULL,updated_at=statement_timestamp()
		WHERE usage_observation_id=$1 AND status='processing' AND lease_owner=$2 AND lease_version=$3
		  AND lease_until > statement_timestamp()
		RETURNING `+upstreamCostJobCols, claim.UsageObservationID, claim.LeaseOwner, claim.LeaseVersion,
		errorCode, delay.Microseconds()))
	if err == ErrNotFound {
		if _, getErr := scanUpstreamCostAttributionJob(s.pool.QueryRow(ctx, `SELECT `+upstreamCostJobCols+`
			FROM upstream_cost_attribution_jobs WHERE usage_observation_id=$1`, claim.UsageObservationID)); getErr == nil {
			return UpstreamCostAttributionJob{}, ErrConflict
		}
	}
	return job, err
}
