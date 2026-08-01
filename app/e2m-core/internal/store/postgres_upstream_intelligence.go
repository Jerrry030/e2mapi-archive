package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"e2m.local/contracts"
)

func jsonObject(value any) ([]byte, error) { return json.Marshal(value) }

func decimalText(value *contracts.CanonicalDecimal) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func scanDecimal(value *string) (*contracts.CanonicalDecimal, error) {
	if value == nil {
		return nil, nil
	}
	decimal, err := contracts.CanonicalizeUpstreamDecimalText(*value)
	if err != nil {
		return nil, fmt.Errorf("store: invalid upstream decimal from database: %w", err)
	}
	return &decimal, nil
}

func mapUpstreamWriteError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrDuplicate
		case "23503":
			return ErrNotFound
		case "23514", "22001", "22P02", "22003":
			return ErrInvalid
		}
	}
	return err
}

const upstreamSourceCols = `id,user_id,connector_id,instance_id,local_ref,mode,provider,display_name,currency,
	poll_interval_seconds,status,capability_balance,capability_groups,capability_rates,capability_prices,
	last_run_at,last_success_at,next_poll_at,last_coverage,last_error_code,created_at,updated_at`

func scanUpstreamSource(row rowScanner) (contracts.UpstreamIntelligenceSource, error) {
	var source contracts.UpstreamIntelligenceSource
	var mode, status, coverage string
	if err := row.Scan(&source.ID, &source.UserID, &source.ConnectorID, &source.InstanceID, &source.LocalRef,
		&mode, &source.Provider, &source.DisplayName, &source.Currency, &source.PollIntervalSeconds, &status,
		&source.Capabilities.Balance, &source.Capabilities.Groups, &source.Capabilities.Rates, &source.Capabilities.Prices,
		&source.LastRunAt, &source.LastSuccessAt, &source.NextPollAt, &coverage,
		&source.LastErrorCode, &source.CreatedAt, &source.UpdatedAt); err != nil {
		return contracts.UpstreamIntelligenceSource{}, err
	}
	source.Mode = contracts.UpstreamIntelligenceSourceMode(mode)
	source.Status = contracts.UpstreamIntelligenceSourceStatus(status)
	source.LastCoverage = contracts.UpstreamEvidenceCoverage(coverage)
	return source, nil
}

func (s *PostgresStore) UpsertUpstreamIntelligenceSource(ctx context.Context, input contracts.UpstreamIntelligenceSource) (contracts.UpstreamIntelligenceSource, error) {
	if err := requireUpstreamOwner(input.UserID); err != nil || !validUpstreamSource(input) {
		return contracts.UpstreamIntelligenceSource{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.UpstreamIntelligenceSource{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	source, recommendationChanged, err := upsertUpstreamSourceTx(ctx, tx, input)
	if err != nil {
		return contracts.UpstreamIntelligenceSource{}, err
	}
	if recommendationChanged {
		if _, err := recordUpstreamIntelligenceFactMutationTx(ctx, tx, source.UserID, UpstreamIntelligenceFactMutationSource, source.ID); err != nil {
			return contracts.UpstreamIntelligenceSource{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.UpstreamIntelligenceSource{}, err
	}
	return source, nil
}

func (s *PostgresStore) GetUpstreamIntelligenceSource(ctx context.Context, userID int64, id string) (contracts.UpstreamIntelligenceSource, error) {
	if err := requireUpstreamOwner(userID); err != nil {
		return contracts.UpstreamIntelligenceSource{}, err
	}
	source, err := scanUpstreamSource(s.pool.QueryRow(ctx, `SELECT `+upstreamSourceCols+` FROM upstream_intelligence_sources WHERE user_id=$1 AND id=$2`, userID, id))
	return source, mapNotFound(err)
}

func (s *PostgresStore) ListUpstreamIntelligenceSources(ctx context.Context, filter contracts.UpstreamIntelligenceSourceFilter) ([]contracts.UpstreamIntelligenceSource, error) {
	if err := requireUpstreamOwner(filter.UserID); err != nil {
		return nil, err
	}
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	rows, err := s.pool.Query(ctx, `SELECT `+upstreamSourceCols+` FROM upstream_intelligence_sources
		WHERE user_id=$1 AND ($2='' OR connector_id=$2) AND ($3='' OR instance_id=$3) AND ($4='' OR status=$4)
		ORDER BY updated_at DESC LIMIT $5`, filter.UserID, filter.ConnectorID, filter.InstanceID, string(filter.Status), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamIntelligenceSource, 0)
	for rows.Next() {
		source, err := scanUpstreamSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, source)
	}
	return out, rows.Err()
}

const upstreamRunCols = `id,user_id,source_id,connector_id,trigger,status,coverage,started_at,observed_at,received_at,
	completed_at,snapshot_hash,manifest_hash,batch_count,fact_count,page_count,error_code,retryable,finalized_fact_version,created_at,updated_at`

func scanUpstreamRun(row rowScanner) (contracts.UpstreamCollectionRun, error) {
	var run contracts.UpstreamCollectionRun
	var trigger, status, coverage string
	if err := row.Scan(&run.ID, &run.UserID, &run.SourceID, &run.ConnectorID, &trigger, &status, &coverage,
		&run.StartedAt, &run.ObservedAt, &run.ReceivedAt, &run.CompletedAt, &run.SnapshotHash, &run.ManifestHash,
		&run.BatchCount, &run.FactCount, &run.PageCount, &run.ErrorCode, &run.Retryable, &run.FinalizedFactVersion,
		&run.CreatedAt, &run.UpdatedAt); err != nil {
		return contracts.UpstreamCollectionRun{}, err
	}
	run.Trigger = contracts.UpstreamCollectionTrigger(trigger)
	run.Status = contracts.UpstreamCollectionStatus(status)
	run.Coverage = contracts.UpstreamEvidenceCoverage(coverage)
	return run, nil
}

func (s *PostgresStore) CreateUpstreamCollectionRun(ctx context.Context, input contracts.UpstreamCollectionRun) (contracts.UpstreamCollectionRun, error) {
	input.ReceivedAt, input.CreatedAt, input.UpdatedAt, input.FinalizedFactVersion = time.Time{}, time.Time{}, time.Time{}, 0
	input.StartedAt, input.ObservedAt = normalizeUpstreamTime(input.StartedAt), normalizeUpstreamTime(input.ObservedAt)
	input.CompletedAt = normalizeUpstreamTimePtr(input.CompletedAt)
	if err := requireUpstreamOwner(input.UserID); err != nil || !validUpstreamCollectionRun(input) {
		return contracts.UpstreamCollectionRun{}, ErrInvalid
	}
	row := s.pool.QueryRow(ctx, `INSERT INTO upstream_collection_runs
		(id,user_id,source_id,connector_id,trigger,status,coverage,started_at,observed_at,received_at,completed_at,
		 snapshot_hash,manifest_hash,batch_count,fact_count,page_count,error_code,retryable)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,COALESCE(NULLIF($10::timestamptz,'epoch'::timestamptz),now()),$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (user_id,id) DO NOTHING RETURNING `+upstreamRunCols,
		input.ID, input.UserID, input.SourceID, input.ConnectorID, string(input.Trigger), string(input.Status), string(input.Coverage),
		input.StartedAt, input.ObservedAt, time.Time{}, input.CompletedAt, input.SnapshotHash, input.ManifestHash,
		input.BatchCount, input.FactCount, input.PageCount, input.ErrorCode, input.Retryable)
	run, err := scanUpstreamRun(row)
	if err == nil {
		return run, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.UpstreamCollectionRun{}, mapUpstreamWriteError(err)
	}
	existing, getErr := s.GetUpstreamCollectionRun(ctx, input.UserID, input.ID)
	if getErr != nil {
		return contracts.UpstreamCollectionRun{}, getErr
	}
	normalized := normalizeCollectionRun(input, existing.CreatedAt)
	if reflectUpstreamRunEqual(existing, normalized) {
		return existing, nil
	}
	return contracts.UpstreamCollectionRun{}, ErrConflict
}

func reflectUpstreamRunEqual(existing, retry contracts.UpstreamCollectionRun) bool {
	retry.CreatedAt, retry.UpdatedAt = existing.CreatedAt, existing.UpdatedAt
	retry.FinalizedFactVersion = existing.FinalizedFactVersion
	if retry.ReceivedAt.IsZero() {
		retry.ReceivedAt = existing.ReceivedAt
	}
	// pgx decodes timestamptz using the connection's configured location. The
	// same instant can therefore carry UTC on the request and Asia/Shanghai on
	// the stored row. Canonicalize every time before the comparable struct
	// equality so idempotency remains strict without depending on Location
	// pointer identity or monotonic clock metadata.
	existing = normalizeCollectionRunTimes(existing)
	retry = normalizeCollectionRunTimes(retry)
	if (existing.CompletedAt == nil) != (retry.CompletedAt == nil) {
		return false
	}
	if existing.CompletedAt != nil {
		if !existing.CompletedAt.Equal(*retry.CompletedAt) {
			return false
		}
		// CompletedAt is a pointer, so comparable struct equality would otherwise
		// compare allocation addresses instead of the already-verified instant.
		retry.CompletedAt = existing.CompletedAt
	}
	return existing == retry
}

func normalizeCollectionRunTimes(run contracts.UpstreamCollectionRun) contracts.UpstreamCollectionRun {
	run.StartedAt = normalizeUpstreamTime(run.StartedAt)
	run.ObservedAt = normalizeUpstreamTime(run.ObservedAt)
	run.ReceivedAt = normalizeUpstreamTime(run.ReceivedAt)
	run.CompletedAt = normalizeUpstreamTimePtr(run.CompletedAt)
	run.CreatedAt = normalizeUpstreamTime(run.CreatedAt)
	run.UpdatedAt = normalizeUpstreamTime(run.UpdatedAt)
	return run
}

func (s *PostgresStore) GetUpstreamCollectionRun(ctx context.Context, userID int64, id string) (contracts.UpstreamCollectionRun, error) {
	if err := requireUpstreamOwner(userID); err != nil {
		return contracts.UpstreamCollectionRun{}, err
	}
	run, err := scanUpstreamRun(s.pool.QueryRow(ctx, `SELECT `+upstreamRunCols+` FROM upstream_collection_runs WHERE user_id=$1 AND id=$2`, userID, id))
	return run, mapNotFound(err)
}

func (s *PostgresStore) ListUpstreamCollectionRuns(ctx context.Context, filter contracts.UpstreamCollectionRunFilter) ([]contracts.UpstreamCollectionRun, error) {
	if err := requireUpstreamOwner(filter.UserID); err != nil {
		return nil, err
	}
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	rows, err := s.pool.Query(ctx, `SELECT `+upstreamRunCols+` FROM upstream_collection_runs
		WHERE user_id=$1 AND ($2='' OR source_id=$2) AND ($3='' OR status=$3) AND ($4::timestamptz='epoch' OR observed_at >= $4)
		ORDER BY observed_at DESC LIMIT $5`, filter.UserID, filter.SourceID, string(filter.Status), filter.Since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamCollectionRun, 0)
	for rows.Next() {
		run, err := scanUpstreamRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func scanUpstreamBatch(row rowScanner) (UpstreamIntelligenceIngestBatch, error) {
	var batch UpstreamIntelligenceIngestBatch
	err := row.Scan(&batch.RunID, &batch.UserID, &batch.SourceID, &batch.BatchNo, &batch.BatchCount,
		&batch.PayloadHash, &batch.ManifestHash, &batch.WalletCount, &batch.OfferCount, &batch.ReceivedAt)
	return batch, err
}

const upstreamBatchCols = `run_id,user_id,source_id,batch_no,batch_count,payload_hash,manifest_hash,wallet_count,offer_count,received_at`

func (s *PostgresStore) UpsertUpstreamIntelligenceIngestBatch(ctx context.Context, input UpstreamIntelligenceIngestBatch) (UpstreamIntelligenceIngestBatch, bool, error) {
	if err := requireUpstreamOwner(input.UserID); err != nil || input.RunID == "" || input.SourceID == "" || input.BatchNo < 0 || input.BatchCount <= input.BatchNo ||
		!contracts.IsUpstreamIntelligenceSHA256(input.PayloadHash) || !contracts.IsUpstreamIntelligenceSHA256(input.ManifestHash) {
		return UpstreamIntelligenceIngestBatch{}, false, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return UpstreamIntelligenceIngestBatch{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `INSERT INTO upstream_ingest_batches
		(run_id,user_id,source_id,batch_no,batch_count,payload_hash,manifest_hash,wallet_count,offer_count,received_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,COALESCE(NULLIF($10::timestamptz,'epoch'::timestamptz),now()))
		ON CONFLICT (user_id,run_id,batch_no) DO NOTHING RETURNING `+upstreamBatchCols,
		input.RunID, input.UserID, input.SourceID, input.BatchNo, input.BatchCount, input.PayloadHash, input.ManifestHash,
		input.WalletCount, input.OfferCount, input.ReceivedAt)
	created, err := scanUpstreamBatch(row)
	if err == nil {
		if err := recordOperationalMetricTx(ctx, tx, "ingest_facts", "accepted", int64(created.WalletCount+created.OfferCount)); err != nil {
			return UpstreamIntelligenceIngestBatch{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return UpstreamIntelligenceIngestBatch{}, false, err
		}
		return created, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return UpstreamIntelligenceIngestBatch{}, false, mapUpstreamWriteError(err)
	}
	existing, err := scanUpstreamBatch(tx.QueryRow(ctx, `SELECT `+upstreamBatchCols+` FROM upstream_ingest_batches WHERE run_id=$1 AND batch_no=$2 AND user_id=$3 FOR UPDATE`, input.RunID, input.BatchNo, input.UserID))
	if err != nil {
		return UpstreamIntelligenceIngestBatch{}, false, mapNotFound(err)
	}
	if existing.SourceID != input.SourceID || existing.BatchCount != input.BatchCount || existing.PayloadHash != input.PayloadHash || existing.ManifestHash != input.ManifestHash || existing.WalletCount != input.WalletCount || existing.OfferCount != input.OfferCount {
		return UpstreamIntelligenceIngestBatch{}, false, ErrConflict
	}
	if err := recordOperationalMetricTx(ctx, tx, "ingest_facts", "duplicate", int64(existing.WalletCount+existing.OfferCount)); err != nil {
		return UpstreamIntelligenceIngestBatch{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return UpstreamIntelligenceIngestBatch{}, false, err
	}
	return existing, true, nil
}

func (s *PostgresStore) IngestUpstreamIntelligenceBatch(ctx context.Context, input UpstreamIntelligenceIngest) (contracts.UpstreamIntelligenceSource, contracts.UpstreamCollectionRun, UpstreamIntelligenceIngestBatch, bool, error) {
	emptySource, emptyRun, emptyBatch := contracts.UpstreamIntelligenceSource{}, contracts.UpstreamCollectionRun{}, UpstreamIntelligenceIngestBatch{}
	if err := ctx.Err(); err != nil {
		return emptySource, emptyRun, emptyBatch, false, err
	}
	if err := requireUpstreamOwner(input.Source.UserID); err != nil || !validUpstreamSource(input.Source) ||
		input.Run.UserID != input.Source.UserID || input.Run.ConnectorID != input.Source.ConnectorID ||
		input.Batch.UserID != input.Source.UserID || input.Batch.RunID != input.Run.ID ||
		input.Batch.WalletCount != len(input.Wallets) || input.Batch.OfferCount != len(input.Offers) {
		return emptySource, emptyRun, emptyBatch, false, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return emptySource, emptyRun, emptyBatch, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockUpstreamIntelligenceRunTx(ctx, tx, input.Source.UserID, input.Run.ID); err != nil {
		return emptySource, emptyRun, emptyBatch, false, err
	}

	source, sourceRecommendationChanged, err := upsertUpstreamSourceTx(ctx, tx, input.Source)
	if err != nil {
		return emptySource, emptyRun, emptyBatch, false, err
	}
	if err := bindUpstreamIngestSource(&input, source.ID); err != nil {
		return emptySource, emptyRun, emptyBatch, false, err
	}
	run, err := createUpstreamRunTx(ctx, tx, input.Run)
	if err != nil {
		return emptySource, emptyRun, emptyBatch, false, err
	}
	if input.Batch.BatchCount != run.BatchCount || input.Batch.ManifestHash != run.ManifestHash ||
		input.Batch.BatchNo < 0 || input.Batch.BatchCount <= input.Batch.BatchNo ||
		input.Batch.WalletCount+input.Batch.OfferCount > contracts.MaxUpstreamIntelligenceBatchFacts ||
		!contracts.IsUpstreamIntelligenceSHA256(input.Batch.PayloadHash) || !contracts.IsUpstreamIntelligenceSHA256(input.Batch.ManifestHash) {
		return emptySource, emptyRun, emptyBatch, false, ErrInvalid
	}
	for _, wallet := range input.Wallets {
		if _, err := appendUpstreamWalletTx(ctx, tx, wallet); err != nil {
			return emptySource, emptyRun, emptyBatch, false, err
		}
	}
	for _, offer := range input.Offers {
		if _, err := appendUpstreamOfferTx(ctx, tx, offer); err != nil {
			return emptySource, emptyRun, emptyBatch, false, err
		}
	}
	batch, duplicate, err := upsertUpstreamBatchTx(ctx, tx, input.Batch)
	if err != nil {
		return emptySource, emptyRun, emptyBatch, false, err
	}
	outcome := "accepted"
	if duplicate {
		outcome = "duplicate"
	}
	if err := recordOperationalMetricTx(ctx, tx, "ingest_facts", outcome, int64(batch.WalletCount+batch.OfferCount)); err != nil {
		return emptySource, emptyRun, emptyBatch, false, err
	}
	if sourceRecommendationChanged {
		if _, err := recordUpstreamIntelligenceFactMutationTx(ctx, tx, source.UserID, UpstreamIntelligenceFactMutationSource, source.ID); err != nil {
			return emptySource, emptyRun, emptyBatch, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return emptySource, emptyRun, emptyBatch, false, err
	}
	return source, run, batch, duplicate, nil
}

// lockUpstreamIntelligenceRunTx is the common first lock for ingest and
// finalization. Including the owner as hash seed keeps identical Connector run
// IDs in separate tenant lock namespaces. The transaction-scoped lock is
// released automatically on commit or rollback.
func lockUpstreamIntelligenceRunTx(ctx context.Context, tx pgx.Tx, userID int64, runID string) error {
	if userID <= 0 || runID == "" {
		return ErrInvalid
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($2::text, $1::bigint))`, userID, runID)
	return err
}

func bindUpstreamIngestSource(input *UpstreamIntelligenceIngest, sourceID string) error {
	if input == nil || sourceID == "" {
		return ErrInvalid
	}
	ids := []string{input.Run.SourceID, input.Batch.SourceID}
	for _, wallet := range input.Wallets {
		ids = append(ids, wallet.SourceID)
	}
	for _, offer := range input.Offers {
		ids = append(ids, offer.SourceID)
	}
	for _, id := range ids {
		if id != "" && id != sourceID {
			return ErrConflict
		}
	}
	input.Run.SourceID, input.Batch.SourceID = sourceID, sourceID
	for i := range input.Wallets {
		input.Wallets[i].SourceID = sourceID
	}
	for i := range input.Offers {
		input.Offers[i].SourceID = sourceID
	}
	return nil
}

func upsertUpstreamSourceTx(ctx context.Context, tx pgx.Tx, input contracts.UpstreamIntelligenceSource) (contracts.UpstreamIntelligenceSource, bool, error) {
	explicitID := input.ID != ""
	if input.PollIntervalSeconds == 0 {
		input.PollIntervalSeconds = 300
	}
	if input.Status == "" {
		input.Status = contracts.UpstreamSourceActive
	}
	existing, err := scanUpstreamSource(tx.QueryRow(ctx, `SELECT `+upstreamSourceCols+`
		FROM upstream_intelligence_sources
		WHERE user_id=$1 AND connector_id=$2 AND local_ref=$3
		FOR UPDATE`, input.UserID, input.ConnectorID, input.LocalRef))
	if err == nil {
		if explicitID && input.ID != existing.ID {
			return contracts.UpstreamIntelligenceSource{}, false, ErrConflict
		}
		input.ID = existing.ID
		input.LastRunAt, input.LastSuccessAt, input.NextPollAt = existing.LastRunAt, existing.LastSuccessAt, existing.NextPollAt
		input.LastCoverage, input.LastErrorCode = existing.LastCoverage, existing.LastErrorCode
		if upstreamIntelligenceSourceConfigurationEqual(existing, input) {
			return existing, false, nil
		}
		recommendationChanged := existing.Status != input.Status
		source, updateErr := scanUpstreamSource(tx.QueryRow(ctx, `UPDATE upstream_intelligence_sources SET
			mode=$4,provider=$5,display_name=$6,currency=$7,poll_interval_seconds=$8,status=$9,
			capability_balance=$10,capability_groups=$11,capability_rates=$12,capability_prices=$13,updated_at=now()
			WHERE user_id=$1 AND connector_id=$2 AND local_ref=$3 AND id=$14
			RETURNING `+upstreamSourceCols,
			input.UserID, input.ConnectorID, input.LocalRef, string(input.Mode), input.Provider, input.DisplayName,
			input.Currency, input.PollIntervalSeconds, string(input.Status), input.Capabilities.Balance,
			input.Capabilities.Groups, input.Capabilities.Rates, input.Capabilities.Prices, input.ID))
		if updateErr != nil {
			return contracts.UpstreamIntelligenceSource{}, false, mapUpstreamWriteError(updateErr)
		}
		return source, recommendationChanged, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.UpstreamIntelligenceSource{}, false, err
	}
	if input.ID == "" {
		input.ID = newID("uisrc")
	}
	source, err := scanUpstreamSource(tx.QueryRow(ctx, `INSERT INTO upstream_intelligence_sources
		(id,user_id,connector_id,instance_id,local_ref,mode,provider,display_name,currency,poll_interval_seconds,status,
		 capability_balance,capability_groups,capability_rates,capability_prices)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (user_id,connector_id,local_ref) DO NOTHING
		RETURNING `+upstreamSourceCols,
		input.ID, input.UserID, input.ConnectorID, input.InstanceID, input.LocalRef, string(input.Mode), input.Provider,
		input.DisplayName, input.Currency, input.PollIntervalSeconds, string(input.Status), input.Capabilities.Balance,
		input.Capabilities.Groups, input.Capabilities.Rates, input.Capabilities.Prices))
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent insert of the same natural key may have committed after
		// our first read. READ COMMITTED gives this statement a fresh snapshot;
		// lock the winner and apply the same strict identity/update rules.
		existing, getErr := scanUpstreamSource(tx.QueryRow(ctx, `SELECT `+upstreamSourceCols+`
			FROM upstream_intelligence_sources
			WHERE user_id=$1 AND connector_id=$2 AND local_ref=$3
			FOR UPDATE`, input.UserID, input.ConnectorID, input.LocalRef))
		if getErr != nil {
			return contracts.UpstreamIntelligenceSource{}, false, ErrConflict
		}
		if explicitID && input.ID != existing.ID {
			return contracts.UpstreamIntelligenceSource{}, false, ErrConflict
		}
		input.ID = existing.ID
		input.LastRunAt, input.LastSuccessAt, input.NextPollAt = existing.LastRunAt, existing.LastSuccessAt, existing.NextPollAt
		input.LastCoverage, input.LastErrorCode = existing.LastCoverage, existing.LastErrorCode
		if upstreamIntelligenceSourceConfigurationEqual(existing, input) {
			return existing, false, nil
		}
		recommendationChanged := existing.Status != input.Status
		updated, updateErr := scanUpstreamSource(tx.QueryRow(ctx, `UPDATE upstream_intelligence_sources SET
			mode=$4,provider=$5,display_name=$6,currency=$7,poll_interval_seconds=$8,status=$9,
			capability_balance=$10,capability_groups=$11,capability_rates=$12,capability_prices=$13,updated_at=now()
			WHERE user_id=$1 AND connector_id=$2 AND local_ref=$3 AND id=$14
			RETURNING `+upstreamSourceCols,
			input.UserID, input.ConnectorID, input.LocalRef, string(input.Mode), input.Provider, input.DisplayName,
			input.Currency, input.PollIntervalSeconds, string(input.Status), input.Capabilities.Balance,
			input.Capabilities.Groups, input.Capabilities.Rates, input.Capabilities.Prices, input.ID))
		if updateErr != nil {
			return contracts.UpstreamIntelligenceSource{}, false, mapUpstreamWriteError(updateErr)
		}
		return updated, recommendationChanged, nil
	}
	if errors.Is(mapUpstreamWriteError(err), ErrDuplicate) {
		return contracts.UpstreamIntelligenceSource{}, false, ErrConflict
	}
	return source, false, mapUpstreamWriteError(err)
}

func createUpstreamRunTx(ctx context.Context, tx pgx.Tx, input contracts.UpstreamCollectionRun) (contracts.UpstreamCollectionRun, error) {
	input.ReceivedAt, input.CreatedAt, input.UpdatedAt, input.FinalizedFactVersion = time.Time{}, time.Time{}, time.Time{}, 0
	input.StartedAt, input.ObservedAt = normalizeUpstreamTime(input.StartedAt), normalizeUpstreamTime(input.ObservedAt)
	input.CompletedAt = normalizeUpstreamTimePtr(input.CompletedAt)
	if !validUpstreamCollectionRun(input) {
		return contracts.UpstreamCollectionRun{}, ErrInvalid
	}
	run, err := scanUpstreamRun(tx.QueryRow(ctx, `INSERT INTO upstream_collection_runs
		(id,user_id,source_id,connector_id,trigger,status,coverage,started_at,observed_at,completed_at,
		 snapshot_hash,manifest_hash,batch_count,fact_count,page_count,error_code,retryable)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (user_id,id) DO NOTHING RETURNING `+upstreamRunCols,
		input.ID, input.UserID, input.SourceID, input.ConnectorID, string(input.Trigger), string(input.Status), string(input.Coverage),
		input.StartedAt, input.ObservedAt, input.CompletedAt, input.SnapshotHash, input.ManifestHash,
		input.BatchCount, input.FactCount, input.PageCount, input.ErrorCode, input.Retryable))
	if err == nil {
		return run, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.UpstreamCollectionRun{}, mapUpstreamWriteError(err)
	}
	existing, err := scanUpstreamRun(tx.QueryRow(ctx, `SELECT `+upstreamRunCols+` FROM upstream_collection_runs WHERE user_id=$1 AND id=$2 FOR UPDATE`, input.UserID, input.ID))
	if err != nil {
		return contracts.UpstreamCollectionRun{}, mapNotFound(err)
	}
	retry := normalizeCollectionRun(input, existing.CreatedAt)
	if !reflectUpstreamRunEqual(existing, retry) {
		return contracts.UpstreamCollectionRun{}, ErrConflict
	}
	return existing, nil
}

func upsertUpstreamBatchTx(ctx context.Context, tx pgx.Tx, input UpstreamIntelligenceIngestBatch) (UpstreamIntelligenceIngestBatch, bool, error) {
	input.ReceivedAt = time.Time{}
	created, err := scanUpstreamBatch(tx.QueryRow(ctx, `INSERT INTO upstream_ingest_batches
		(run_id,user_id,source_id,batch_no,batch_count,payload_hash,manifest_hash,wallet_count,offer_count)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (user_id,run_id,batch_no) DO NOTHING RETURNING `+upstreamBatchCols,
		input.RunID, input.UserID, input.SourceID, input.BatchNo, input.BatchCount, input.PayloadHash, input.ManifestHash,
		input.WalletCount, input.OfferCount))
	if err == nil {
		return created, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return UpstreamIntelligenceIngestBatch{}, false, mapUpstreamWriteError(err)
	}
	existing, err := scanUpstreamBatch(tx.QueryRow(ctx, `SELECT `+upstreamBatchCols+` FROM upstream_ingest_batches WHERE run_id=$1 AND batch_no=$2 AND user_id=$3 FOR UPDATE`, input.RunID, input.BatchNo, input.UserID))
	if err != nil {
		return UpstreamIntelligenceIngestBatch{}, false, mapNotFound(err)
	}
	if existing.SourceID != input.SourceID || existing.BatchCount != input.BatchCount || existing.PayloadHash != input.PayloadHash || existing.ManifestHash != input.ManifestHash || existing.WalletCount != input.WalletCount || existing.OfferCount != input.OfferCount {
		return UpstreamIntelligenceIngestBatch{}, false, ErrConflict
	}
	return existing, true, nil
}

func (s *PostgresStore) ListUpstreamIntelligenceIngestBatches(ctx context.Context, userID int64, runID string) ([]UpstreamIntelligenceIngestBatch, error) {
	if err := requireUpstreamOwner(userID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+upstreamBatchCols+` FROM upstream_ingest_batches WHERE user_id=$1 AND run_id=$2 ORDER BY batch_no`, userID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]UpstreamIntelligenceIngestBatch, 0)
	for rows.Next() {
		batch, err := scanUpstreamBatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, batch)
	}
	return out, rows.Err()
}

func scanWallet(row rowScanner) (contracts.UpstreamWalletObservation, error) {
	var observation contracts.UpstreamWalletObservation
	var balance, confidence *string
	var unit, accuracy, coverage string
	var missing []byte
	if err := row.Scan(&observation.RunID, &observation.ID, &observation.UserID, &observation.SourceID, &balance,
		&unit, &observation.Currency, &observation.ObservedAt, &observation.ReceivedAt, &observation.FreshUntil,
		&accuracy, &coverage, &confidence, &missing, &observation.ReasonCode); err != nil {
		return contracts.UpstreamWalletObservation{}, err
	}
	var err error
	if observation.BalanceAmount, err = scanDecimal(balance); err != nil {
		return contracts.UpstreamWalletObservation{}, err
	}
	if observation.Confidence, err = scanDecimal(confidence); err != nil {
		return contracts.UpstreamWalletObservation{}, err
	}
	observation.UnitKind = contracts.UpstreamWalletUnitKind(unit)
	observation.Accuracy = contracts.UpstreamEvidenceAccuracy(accuracy)
	observation.Coverage = contracts.UpstreamEvidenceCoverage(coverage)
	if err := json.Unmarshal(missing, &observation.MissingFields); err != nil {
		return contracts.UpstreamWalletObservation{}, err
	}
	return observation, nil
}

const walletCols = `run_id,id,user_id,source_id,balance_amount::text,unit_kind,currency,observed_at,received_at,fresh_until,
	accuracy,coverage,confidence::text,missing_fields,reason_code`

func (s *PostgresStore) AppendUpstreamWalletObservation(ctx context.Context, input contracts.UpstreamWalletObservation) (contracts.UpstreamWalletObservation, error) {
	if err := requireUpstreamOwner(input.UserID); err != nil || !validUpstreamWallet(input) {
		return contracts.UpstreamWalletObservation{}, ErrInvalid
	}
	return appendUpstreamWalletQuery(ctx, s.pool, input)
}

type upstreamQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func appendUpstreamWalletTx(ctx context.Context, tx pgx.Tx, input contracts.UpstreamWalletObservation) (contracts.UpstreamWalletObservation, error) {
	return appendUpstreamWalletQuery(ctx, tx, input)
}

func appendUpstreamWalletQuery(ctx context.Context, queryer upstreamQueryer, input contracts.UpstreamWalletObservation) (contracts.UpstreamWalletObservation, error) {
	input.ReceivedAt = time.Time{}
	input.ObservedAt, input.FreshUntil = normalizeUpstreamTime(input.ObservedAt), normalizeUpstreamTime(input.FreshUntil)
	input.MissingFields = normalizeUpstreamMissingFields(input.MissingFields)
	if !validUpstreamWallet(input) {
		return contracts.UpstreamWalletObservation{}, ErrInvalid
	}
	missing, err := jsonObject(input.MissingFields)
	if err != nil {
		return contracts.UpstreamWalletObservation{}, ErrInvalid
	}
	row := queryer.QueryRow(ctx, `INSERT INTO upstream_wallet_observations
		(run_id,id,user_id,source_id,balance_amount,unit_kind,currency,observed_at,received_at,fresh_until,accuracy,coverage,confidence,missing_fields,reason_code)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,COALESCE(NULLIF($9::timestamptz,'epoch'::timestamptz),now()),$10,$11,$12,$13,$14,$15)
		ON CONFLICT (user_id,run_id,id) DO NOTHING RETURNING `+walletCols,
		input.RunID, input.ID, input.UserID, input.SourceID, decimalText(input.BalanceAmount), string(input.UnitKind),
		input.Currency, input.ObservedAt, input.ReceivedAt, input.FreshUntil, string(input.Accuracy), string(input.Coverage),
		decimalText(input.Confidence), missing, input.ReasonCode)
	observation, err := scanWallet(row)
	if err == nil {
		return observation, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.UpstreamWalletObservation{}, mapUpstreamWriteError(err)
	}
	existing, err := scanWallet(queryer.QueryRow(ctx, `SELECT `+walletCols+` FROM upstream_wallet_observations WHERE run_id=$1 AND id=$2 AND user_id=$3`, input.RunID, input.ID, input.UserID))
	if err != nil {
		return contracts.UpstreamWalletObservation{}, mapNotFound(err)
	}
	input.ReceivedAt = existing.ReceivedAt
	if !upstreamWalletEqual(existing, input) {
		return contracts.UpstreamWalletObservation{}, ErrConflict
	}
	return existing, nil
}

func upstreamWalletEqual(a, b contracts.UpstreamWalletObservation) bool {
	a.ObservedAt = normalizeUpstreamTime(a.ObservedAt)
	a.ReceivedAt = normalizeUpstreamTime(a.ReceivedAt)
	a.FreshUntil = normalizeUpstreamTime(a.FreshUntil)
	b.ObservedAt = normalizeUpstreamTime(b.ObservedAt)
	b.ReceivedAt = normalizeUpstreamTime(b.ReceivedAt)
	b.FreshUntil = normalizeUpstreamTime(b.FreshUntil)
	return reflect.DeepEqual(a, b)
}

func (s *PostgresStore) ListUpstreamWalletObservations(ctx context.Context, filter contracts.UpstreamWalletObservationFilter) ([]contracts.UpstreamWalletObservation, error) {
	if err := requireUpstreamOwner(filter.UserID); err != nil {
		return nil, err
	}
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	rows, err := s.pool.Query(ctx, `SELECT `+walletCols+` FROM upstream_wallet_observations WHERE user_id=$1 AND ($2='' OR source_id=$2) AND ($3::timestamptz='epoch' OR observed_at >= $3) ORDER BY observed_at DESC LIMIT $4`, filter.UserID, filter.SourceID, filter.Since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamWalletObservation, 0)
	for rows.Next() {
		observation, err := scanWallet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, observation)
	}
	return out, rows.Err()
}

func scanOffer(row rowScanner) (contracts.UpstreamOfferObservation, error) {
	var observation contracts.UpstreamOfferObservation
	var groupMultiplier, rechargeYield, publishedPrice, effectiveMultiplier, effectiveCost, confidence *string
	var dimension, accuracy, coverage string
	var missing []byte
	if err := row.Scan(&observation.RunID, &observation.ID, &observation.UserID, &observation.SourceID,
		&observation.GroupKey, &observation.ModelKey, &dimension, &observation.SettlementCurrency,
		&groupMultiplier, &rechargeYield, &publishedPrice, &observation.PerTokens, &effectiveMultiplier,
		&effectiveCost, &observation.FormulaVersion, &accuracy, &coverage, &confidence, &observation.ObservedAt,
		&observation.EffectiveAt, &observation.ReceivedAt, &observation.FreshUntil, &observation.ValidUntil,
		&missing, &observation.ReasonCode, &observation.AdapterSchemaVersion, &observation.SourceRevision); err != nil {
		return contracts.UpstreamOfferObservation{}, err
	}
	var err error
	if observation.GroupMultiplier, err = scanDecimal(groupMultiplier); err != nil {
		return contracts.UpstreamOfferObservation{}, err
	}
	if observation.RechargeYield, err = scanDecimal(rechargeYield); err != nil {
		return contracts.UpstreamOfferObservation{}, err
	}
	if observation.PublishedUnitPrice, err = scanDecimal(publishedPrice); err != nil {
		return contracts.UpstreamOfferObservation{}, err
	}
	if observation.EffectiveMultiplier, err = scanDecimal(effectiveMultiplier); err != nil {
		return contracts.UpstreamOfferObservation{}, err
	}
	if observation.EffectiveUnitCost, err = scanDecimal(effectiveCost); err != nil {
		return contracts.UpstreamOfferObservation{}, err
	}
	if observation.Confidence, err = scanDecimal(confidence); err != nil {
		return contracts.UpstreamOfferObservation{}, err
	}
	observation.PriceDimension = contracts.UpstreamPriceDimension(dimension)
	observation.Accuracy = contracts.UpstreamEvidenceAccuracy(accuracy)
	observation.Coverage = contracts.UpstreamEvidenceCoverage(coverage)
	if err := json.Unmarshal(missing, &observation.MissingFields); err != nil {
		return contracts.UpstreamOfferObservation{}, err
	}
	return observation, nil
}

const offerCols = `run_id,id,user_id,source_id,group_key,model_key,price_dimension,settlement_currency,
	group_multiplier::text,recharge_yield::text,published_unit_price::text,per_tokens,effective_multiplier::text,
	effective_unit_cost::text,formula_version,accuracy,coverage,confidence::text,observed_at,effective_at,received_at,
	fresh_until,valid_until,missing_fields,reason_code,adapter_schema_version,source_revision`

func (s *PostgresStore) AppendUpstreamOfferObservation(ctx context.Context, input contracts.UpstreamOfferObservation) (contracts.UpstreamOfferObservation, error) {
	if err := requireUpstreamOwner(input.UserID); err != nil || !validUpstreamOffer(input) {
		return contracts.UpstreamOfferObservation{}, ErrInvalid
	}
	return appendUpstreamOfferQuery(ctx, s.pool, input)
}

func appendUpstreamOfferTx(ctx context.Context, tx pgx.Tx, input contracts.UpstreamOfferObservation) (contracts.UpstreamOfferObservation, error) {
	return appendUpstreamOfferQuery(ctx, tx, input)
}

func appendUpstreamOfferQuery(ctx context.Context, queryer upstreamQueryer, input contracts.UpstreamOfferObservation) (contracts.UpstreamOfferObservation, error) {
	input.ReceivedAt = time.Time{}
	input.ObservedAt, input.EffectiveAt, input.FreshUntil = normalizeUpstreamTime(input.ObservedAt), normalizeUpstreamTime(input.EffectiveAt), normalizeUpstreamTime(input.FreshUntil)
	input.ValidUntil = normalizeUpstreamTimePtr(input.ValidUntil)
	input.MissingFields = normalizeUpstreamMissingFields(input.MissingFields)
	if !validUpstreamOffer(input) {
		return contracts.UpstreamOfferObservation{}, ErrInvalid
	}
	missing, err := jsonObject(input.MissingFields)
	if err != nil {
		return contracts.UpstreamOfferObservation{}, ErrInvalid
	}
	row := queryer.QueryRow(ctx, `INSERT INTO upstream_offer_observations
		(run_id,id,user_id,source_id,group_key,model_key,price_dimension,settlement_currency,group_multiplier,recharge_yield,
		 published_unit_price,per_tokens,effective_multiplier,effective_unit_cost,formula_version,accuracy,coverage,confidence,
		 observed_at,effective_at,received_at,fresh_until,valid_until,missing_fields,reason_code,adapter_schema_version,source_revision)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
		 COALESCE(NULLIF($21::timestamptz,'epoch'::timestamptz),now()),$22,$23,$24,$25,$26,$27)
		ON CONFLICT DO NOTHING RETURNING `+offerCols,
		input.RunID, input.ID, input.UserID, input.SourceID, input.GroupKey, input.ModelKey, string(input.PriceDimension),
		input.SettlementCurrency, decimalText(input.GroupMultiplier), decimalText(input.RechargeYield), decimalText(input.PublishedUnitPrice),
		input.PerTokens, decimalText(input.EffectiveMultiplier), decimalText(input.EffectiveUnitCost), input.FormulaVersion,
		string(input.Accuracy), string(input.Coverage), decimalText(input.Confidence), input.ObservedAt, input.EffectiveAt,
		input.ReceivedAt, input.FreshUntil, input.ValidUntil, missing, input.ReasonCode, input.AdapterSchemaVersion, input.SourceRevision)
	observation, err := scanOffer(row)
	if err == nil {
		return observation, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.UpstreamOfferObservation{}, mapUpstreamWriteError(err)
	}
	existing, err := scanOffer(queryer.QueryRow(ctx, `SELECT `+offerCols+` FROM upstream_offer_observations
		WHERE run_id=$1 AND user_id=$2 AND (id=$3 OR (group_key=$4 AND model_key=$5 AND price_dimension=$6))`,
		input.RunID, input.UserID, input.ID, input.GroupKey, input.ModelKey, string(input.PriceDimension)))
	if err != nil {
		return contracts.UpstreamOfferObservation{}, mapNotFound(err)
	}
	input.ReceivedAt = existing.ReceivedAt
	if !upstreamOfferEqual(existing, input) {
		return contracts.UpstreamOfferObservation{}, ErrConflict
	}
	return existing, nil
}

func upstreamOfferEqual(a, b contracts.UpstreamOfferObservation) bool {
	a.ObservedAt = normalizeUpstreamTime(a.ObservedAt)
	a.EffectiveAt = normalizeUpstreamTime(a.EffectiveAt)
	a.ReceivedAt = normalizeUpstreamTime(a.ReceivedAt)
	a.FreshUntil = normalizeUpstreamTime(a.FreshUntil)
	a.ValidUntil = normalizeUpstreamTimePtr(a.ValidUntil)
	b.ObservedAt = normalizeUpstreamTime(b.ObservedAt)
	b.EffectiveAt = normalizeUpstreamTime(b.EffectiveAt)
	b.ReceivedAt = normalizeUpstreamTime(b.ReceivedAt)
	b.FreshUntil = normalizeUpstreamTime(b.FreshUntil)
	b.ValidUntil = normalizeUpstreamTimePtr(b.ValidUntil)
	if (a.ValidUntil == nil) != (b.ValidUntil == nil) {
		return false
	}
	if a.ValidUntil != nil {
		if !a.ValidUntil.Equal(*b.ValidUntil) {
			return false
		}
		b.ValidUntil = a.ValidUntil
	}
	return reflect.DeepEqual(a, b)
}

func (s *PostgresStore) ListUpstreamOfferObservations(ctx context.Context, filter contracts.UpstreamOfferObservationFilter) ([]contracts.UpstreamOfferObservation, error) {
	if err := requireUpstreamOwner(filter.UserID); err != nil {
		return nil, err
	}
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	rows, err := s.pool.Query(ctx, `SELECT `+offerCols+` FROM upstream_offer_observations
		WHERE user_id=$1 AND ($2='' OR source_id=$2) AND ($3='' OR group_key=$3) AND ($4='' OR model_key=$4)
		 AND ($5='' OR price_dimension=$5) AND ($6::timestamptz='epoch' OR observed_at >= $6)
		ORDER BY observed_at DESC LIMIT $7`, filter.UserID, filter.SourceID, filter.GroupKey, filter.ModelKey,
		string(filter.PriceDimension), filter.Since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamOfferObservation, 0)
	for rows.Next() {
		observation, err := scanOffer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, observation)
	}
	return out, rows.Err()
}

const absenceCols = `user_id,source_id,comparison_key,consecutive_complete_runs,last_present_observation_id,last_present_run_id,first_absent_at,last_absent_run_id,updated_at`

func scanAbsence(row rowScanner) (UpstreamSnapshotAbsence, error) {
	var absence UpstreamSnapshotAbsence
	err := row.Scan(&absence.UserID, &absence.SourceID, &absence.ComparisonKey, &absence.ConsecutiveCompleteRuns,
		&absence.LastPresentObservationID, &absence.LastPresentRunID, &absence.FirstAbsentAt, &absence.LastAbsentRunID, &absence.UpdatedAt)
	return absence, err
}

func (s *PostgresStore) UpsertUpstreamSnapshotAbsence(ctx context.Context, input UpstreamSnapshotAbsence) (UpstreamSnapshotAbsence, error) {
	if err := requireUpstreamOwner(input.UserID); err != nil || input.SourceID == "" || input.ComparisonKey == "" || input.ConsecutiveCompleteRuns < 0 {
		return UpstreamSnapshotAbsence{}, ErrInvalid
	}
	absence, err := scanAbsence(s.pool.QueryRow(ctx, `INSERT INTO upstream_snapshot_absences
		(user_id,source_id,comparison_key,consecutive_complete_runs,last_present_observation_id,last_present_run_id,first_absent_at,last_absent_run_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (source_id,comparison_key) DO UPDATE SET
		 user_id=EXCLUDED.user_id,consecutive_complete_runs=EXCLUDED.consecutive_complete_runs,
		 last_present_observation_id=EXCLUDED.last_present_observation_id,last_present_run_id=EXCLUDED.last_present_run_id,
		 first_absent_at=EXCLUDED.first_absent_at,last_absent_run_id=EXCLUDED.last_absent_run_id,updated_at=now()
		WHERE upstream_snapshot_absences.user_id=EXCLUDED.user_id RETURNING `+absenceCols,
		input.UserID, input.SourceID, input.ComparisonKey, input.ConsecutiveCompleteRuns, input.LastPresentObservationID,
		input.LastPresentRunID, input.FirstAbsentAt, input.LastAbsentRunID))
	if errors.Is(err, pgx.ErrNoRows) {
		return UpstreamSnapshotAbsence{}, ErrConflict
	}
	return absence, mapUpstreamWriteError(err)
}

func (s *PostgresStore) ListUpstreamSnapshotAbsences(ctx context.Context, userID int64, sourceID string) ([]UpstreamSnapshotAbsence, error) {
	if err := requireUpstreamOwner(userID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+absenceCols+` FROM upstream_snapshot_absences WHERE user_id=$1 AND ($2='' OR source_id=$2) ORDER BY comparison_key`, userID, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]UpstreamSnapshotAbsence, 0)
	for rows.Next() {
		absence, err := scanAbsence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, absence)
	}
	return out, rows.Err()
}

const linkCols = `id,user_id,intelligence_source_id,link_scope,upstream_source_identity,COALESCE(channel_id,''),price_dimension,status,verified_at,created_at,updated_at`

func scanLink(row rowScanner) (contracts.UpstreamIntelligenceLink, error) {
	var link contracts.UpstreamIntelligenceLink
	var scope, dimension, status string
	err := row.Scan(&link.ID, &link.UserID, &link.IntelligenceSourceID, &scope, &link.UpstreamSourceIdentity,
		&link.ChannelID, &dimension, &status, &link.VerifiedAt, &link.CreatedAt, &link.UpdatedAt)
	link.Scope, link.PriceDimension = contracts.UpstreamIntelligenceLinkScope(scope), contracts.UpstreamPriceDimension(dimension)
	link.Status = contracts.UpstreamIntelligenceLinkStatus(status)
	return link, err
}

func (s *PostgresStore) UpsertUpstreamIntelligenceLink(ctx context.Context, input contracts.UpstreamIntelligenceLink) (contracts.UpstreamIntelligenceLink, error) {
	input.VerifiedAt = normalizeUpstreamTimePtr(input.VerifiedAt)
	if err := requireUpstreamOwner(input.UserID); err != nil || !validUpstreamIntelligenceLink(input) {
		return contracts.UpstreamIntelligenceLink{}, ErrInvalid
	}
	if input.ID == "" {
		input.ID = newID("uilink")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.UpstreamIntelligenceLink{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Both target forms are durable allocation identities. A caller may not turn
	// an arbitrary source_id string into a trusted quality/cost join.
	var targetOwned bool
	if input.Scope == contracts.UpstreamLinkChannel {
		err = tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM upstream_channel_allocations WHERE channel_id=$1 AND user_id=$2
		)`, input.ChannelID, input.UserID).Scan(&targetOwned)
	} else {
		var targetCount int
		err = tx.QueryRow(ctx, `SELECT COUNT(*)
			FROM upstream_channel_allocations WHERE source_id=$1 AND user_id=$2`,
			input.UpstreamSourceIdentity, input.UserID).Scan(&targetCount)
		targetOwned = targetCount > 0
		if err == nil && input.Status == contracts.UpstreamLinkActive && targetCount != 1 {
			if targetCount == 0 {
				return contracts.UpstreamIntelligenceLink{}, ErrNotFound
			}
			return contracts.UpstreamIntelligenceLink{}, ErrConflict
		}
	}
	if err != nil {
		return contracts.UpstreamIntelligenceLink{}, err
	}
	if !targetOwned {
		return contracts.UpstreamIntelligenceLink{}, ErrNotFound
	}
	var existing contracts.UpstreamIntelligenceLink
	existingFound := false
	if input.ID != "" {
		existing, err = scanLink(tx.QueryRow(ctx, `SELECT `+linkCols+` FROM upstream_intelligence_links WHERE id=$1 AND user_id=$2 FOR UPDATE`, input.ID, input.UserID))
		if err == nil {
			existingFound = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return contracts.UpstreamIntelligenceLink{}, err
		}
	}
	if existingFound && existing.IntelligenceSourceID != input.IntelligenceSourceID {
		return contracts.UpstreamIntelligenceLink{}, ErrConflict
	}
	if existingFound && upstreamIntelligenceLinkBusinessEqual(existing, input) {
		if err := tx.Commit(ctx); err != nil {
			return contracts.UpstreamIntelligenceLink{}, err
		}
		return existing, nil
	}
	link, err := scanLink(tx.QueryRow(ctx, `INSERT INTO upstream_intelligence_links
		(id,user_id,intelligence_source_id,link_scope,upstream_source_identity,channel_id,price_dimension,status,verified_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (id) DO UPDATE SET
		 link_scope=EXCLUDED.link_scope,upstream_source_identity=EXCLUDED.upstream_source_identity,channel_id=EXCLUDED.channel_id,
		 price_dimension=EXCLUDED.price_dimension,status=EXCLUDED.status,verified_at=EXCLUDED.verified_at,updated_at=now()
		WHERE upstream_intelligence_links.user_id=EXCLUDED.user_id AND upstream_intelligence_links.intelligence_source_id=EXCLUDED.intelligence_source_id
		RETURNING `+linkCols, input.ID, input.UserID, input.IntelligenceSourceID, string(input.Scope), input.UpstreamSourceIdentity,
		nullableString(input.ChannelID), string(input.PriceDimension), string(input.Status), input.VerifiedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.UpstreamIntelligenceLink{}, ErrConflict
	}
	if err != nil {
		return contracts.UpstreamIntelligenceLink{}, mapUpstreamWriteError(err)
	}
	if _, err := recordUpstreamIntelligenceFactMutationTx(ctx, tx, input.UserID, UpstreamIntelligenceFactMutationLink, link.ID); err != nil {
		return contracts.UpstreamIntelligenceLink{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.UpstreamIntelligenceLink{}, err
	}
	return link, nil
}

func recordUpstreamIntelligenceFactMutationTx(ctx context.Context, tx pgx.Tx, userID int64, mutationKind UpstreamIntelligenceFactMutationKind, evidenceID string) (contracts.UpstreamIntelligenceFactVersion, error) {
	var version contracts.UpstreamIntelligenceFactVersion
	err := tx.QueryRow(ctx, `SELECT out_user_id,out_fact_version,out_updated_at
		FROM record_upstream_intelligence_fact_mutation($1,$2,$3)`, userID, mutationKind, nullableString(evidenceID)).
		Scan(&version.UserID, &version.FactVersion, &version.UpdatedAt)
	return version, err
}

func (s *PostgresStore) ListUpstreamIntelligenceLinks(ctx context.Context, filter contracts.UpstreamIntelligenceLinkFilter) ([]contracts.UpstreamIntelligenceLink, error) {
	if err := requireUpstreamOwner(filter.UserID); err != nil {
		return nil, err
	}
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	rows, err := s.pool.Query(ctx, `SELECT `+linkCols+` FROM upstream_intelligence_links
		WHERE user_id=$1 AND ($2='' OR intelligence_source_id=$2) AND ($3='' OR link_scope=$3) AND ($4='' OR status=$4)
		ORDER BY updated_at DESC LIMIT $5`, filter.UserID, filter.IntelligenceSourceID, string(filter.Scope), string(filter.Status), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamIntelligenceLink, 0)
	for rows.Next() {
		link, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, link)
	}
	return out, rows.Err()
}

const changeCols = `id,user_id,source_id,event_type,event_fingerprint,before_observation_id,after_observation_id,
	absolute_change::text,percentage_change::text,first_detected_at,confirmed_at,severity,impact_scope,group_key,model_key,price_dimension,created_at`

func scanChange(row rowScanner) (contracts.UpstreamChangeEvent, error) {
	var event contracts.UpstreamChangeEvent
	var eventType, severity, dimension string
	var absolute, percentage *string
	var impact []byte
	if err := row.Scan(&event.ID, &event.UserID, &event.SourceID, &eventType, &event.Fingerprint,
		&event.BeforeObservationID, &event.AfterObservationID, &absolute, &percentage, &event.FirstDetectedAt,
		&event.ConfirmedAt, &severity, &impact, &event.GroupKey, &event.ModelKey, &dimension, &event.CreatedAt); err != nil {
		return contracts.UpstreamChangeEvent{}, err
	}
	event.Type, event.Severity = contracts.UpstreamChangeEventType(eventType), contracts.UpstreamChangeSeverity(severity)
	event.PriceDimension = contracts.UpstreamPriceDimension(dimension)
	var err error
	if event.AbsoluteChange, err = scanDecimal(absolute); err != nil {
		return contracts.UpstreamChangeEvent{}, err
	}
	if event.PercentageChange, err = scanDecimal(percentage); err != nil {
		return contracts.UpstreamChangeEvent{}, err
	}
	if err := json.Unmarshal(impact, &event.ImpactScope); err != nil {
		return contracts.UpstreamChangeEvent{}, err
	}
	return event, nil
}

func (s *PostgresStore) AppendUpstreamChangeEvent(ctx context.Context, input contracts.UpstreamChangeEvent) (contracts.UpstreamChangeEvent, error) {
	if err := requireUpstreamOwner(input.UserID); err != nil || input.SourceID == "" || input.Fingerprint == "" {
		return contracts.UpstreamChangeEvent{}, ErrInvalid
	}
	if input.ID == "" {
		input.ID = newID("uichg")
	}
	impact, err := jsonObject(input.ImpactScope)
	if err != nil {
		return contracts.UpstreamChangeEvent{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.UpstreamChangeEvent{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	event, err := scanChange(tx.QueryRow(ctx, `INSERT INTO upstream_change_events
		(id,user_id,source_id,event_type,event_fingerprint,before_observation_id,after_observation_id,absolute_change,
		 percentage_change,first_detected_at,confirmed_at,severity,impact_scope,group_key,model_key,price_dimension)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (source_id,event_fingerprint) DO NOTHING RETURNING `+changeCols,
		input.ID, input.UserID, input.SourceID, string(input.Type), input.Fingerprint, input.BeforeObservationID,
		input.AfterObservationID, decimalText(input.AbsoluteChange), decimalText(input.PercentageChange),
		input.FirstDetectedAt, input.ConfirmedAt, string(input.Severity), impact, input.GroupKey, input.ModelKey, string(input.PriceDimension)))
	if err == nil {
		if err := recordOperationalMetricTx(ctx, tx, "change_events", string(event.Type), 1); err != nil {
			return contracts.UpstreamChangeEvent{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return contracts.UpstreamChangeEvent{}, err
		}
		return event, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.UpstreamChangeEvent{}, mapUpstreamWriteError(err)
	}
	existing, err := scanChange(tx.QueryRow(ctx, `SELECT `+changeCols+` FROM upstream_change_events WHERE source_id=$1 AND event_fingerprint=$2 AND user_id=$3`, input.SourceID, input.Fingerprint, input.UserID))
	if err != nil {
		return contracts.UpstreamChangeEvent{}, mapNotFound(err)
	}
	input.ID, input.CreatedAt = existing.ID, existing.CreatedAt
	if !upstreamChangeEqual(existing, input) {
		return contracts.UpstreamChangeEvent{}, ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.UpstreamChangeEvent{}, err
	}
	return existing, nil
}

func upstreamChangeEqual(a, b contracts.UpstreamChangeEvent) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

func (s *PostgresStore) ListUpstreamChangeEvents(ctx context.Context, filter contracts.UpstreamChangeEventFilter) ([]contracts.UpstreamChangeEvent, error) {
	if err := requireUpstreamOwner(filter.UserID); err != nil {
		return nil, err
	}
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	rows, err := s.pool.Query(ctx, `SELECT `+changeCols+` FROM upstream_change_events
		WHERE user_id=$1 AND ($2='' OR source_id=$2) AND ($3='' OR event_type=$3) AND ($4::timestamptz='epoch' OR confirmed_at >= $4)
		ORDER BY confirmed_at DESC LIMIT $5`, filter.UserID, filter.SourceID, string(filter.Type), filter.Since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamChangeEvent, 0)
	for rows.Next() {
		event, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetUpstreamIntelligenceFactVersion(ctx context.Context, userID int64) (contracts.UpstreamIntelligenceFactVersion, error) {
	if err := requireUpstreamOwner(userID); err != nil {
		return contracts.UpstreamIntelligenceFactVersion{}, err
	}
	var version contracts.UpstreamIntelligenceFactVersion
	err := s.pool.QueryRow(ctx, `SELECT user_id,fact_version,updated_at FROM upstream_intelligence_fact_versions WHERE user_id=$1`, userID).Scan(&version.UserID, &version.FactVersion, &version.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.UpstreamIntelligenceFactVersion{UserID: userID}, nil
	}
	return version, err
}

func (s *PostgresStore) FinalizeUpstreamCollectionRun(ctx context.Context, userID int64, runID string) (contracts.UpstreamCollectionRun, contracts.UpstreamIntelligenceFactVersion, error) {
	if err := requireUpstreamOwner(userID); err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockUpstreamIntelligenceRunTx(ctx, tx, userID, runID); err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	run, err := scanUpstreamRun(tx.QueryRow(ctx, `SELECT `+upstreamRunCols+` FROM upstream_collection_runs WHERE user_id=$1 AND id=$2 FOR UPDATE`, userID, runID))
	if err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, mapNotFound(err)
	}
	if run.Status == contracts.UpstreamCollectionRunning || run.CompletedAt == nil || run.BatchCount <= 0 || run.ManifestHash == "" {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, ErrConflict
	}
	if run.Status == contracts.UpstreamCollectionSucceeded && run.Coverage == contracts.UpstreamCoverageComplete {
		var incompleteFacts int
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT COUNT(*) FROM upstream_wallet_observations WHERE user_id=$1::bigint AND run_id=$2::text AND coverage<>'complete') +
			(SELECT COUNT(*) FROM upstream_offer_observations WHERE user_id=$1::bigint AND run_id=$2::text AND coverage<>'complete')`, userID, run.ID).Scan(&incompleteFacts); err != nil {
			return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
		}
		if incompleteFacts != 0 {
			return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, ErrConflict
		}
	}
	var batches, walletDeclared, offerDeclared int
	var manifestMismatch bool
	if err := tx.QueryRow(ctx, `SELECT COUNT(*),COALESCE(SUM(wallet_count),0),COALESCE(SUM(offer_count),0),
		COALESCE(bool_or(batch_count<>$3 OR manifest_hash<>$4),false) FROM upstream_ingest_batches WHERE user_id=$1 AND run_id=$2`,
		userID, run.ID, run.BatchCount, run.ManifestHash).Scan(&batches, &walletDeclared, &offerDeclared, &manifestMismatch); err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	batchRows, err := tx.Query(ctx, `SELECT batch_no,payload_hash FROM upstream_ingest_batches WHERE user_id=$1 AND run_id=$2 ORDER BY batch_no`, userID, run.ID)
	if err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	manifestBatches := make([]contracts.UpstreamIntelligenceManifestBatch, 0, run.BatchCount)
	for batchRows.Next() {
		var leaf contracts.UpstreamIntelligenceManifestBatch
		if err := batchRows.Scan(&leaf.BatchNo, &leaf.PayloadHash); err != nil {
			batchRows.Close()
			return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
		}
		manifestBatches = append(manifestBatches, leaf)
	}
	rowsErr := batchRows.Err()
	batchRows.Close()
	if rowsErr != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, rowsErr
	}
	manifestHash, manifestErr := contracts.CalculateUpstreamIntelligenceManifestHash(manifestBatches)
	if manifestErr != nil || manifestHash != run.ManifestHash {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, ErrConflict
	}
	var walletFacts, offerFacts int
	if err := tx.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM upstream_wallet_observations WHERE user_id=$1::bigint AND run_id=$2::text),
		(SELECT COUNT(*) FROM upstream_offer_observations WHERE user_id=$1::bigint AND run_id=$2::text)`, userID, run.ID).Scan(&walletFacts, &offerFacts); err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	if manifestMismatch || batches != run.BatchCount || walletDeclared != walletFacts || offerDeclared != offerFacts || walletFacts+offerFacts != run.FactCount {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, ErrConflict
	}
	// Lock the source in the same transaction so its current pointer cannot race
	// another finalization. The run's persisted version is the replay fence.
	var sourceID string
	if err := tx.QueryRow(ctx, `SELECT id FROM upstream_intelligence_sources WHERE id=$1 AND user_id=$2 FOR UPDATE`, run.SourceID, userID).Scan(&sourceID); err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, mapNotFound(err)
	}
	if run.FinalizedFactVersion > 0 {
		version := contracts.UpstreamIntelligenceFactVersion{UserID: userID, FactVersion: run.FinalizedFactVersion}
		err := tx.QueryRow(ctx, `SELECT updated_at FROM upstream_intelligence_fact_versions WHERE user_id=$1 AND fact_version >= $2`, userID, run.FinalizedFactVersion).Scan(&version.UpdatedAt)
		if err != nil {
			return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
		}
		return run, version, nil
	}
	if run.Status == contracts.UpstreamCollectionSucceeded && run.Coverage == contracts.UpstreamCoverageComplete {
		var newerComplete bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM upstream_collection_runs AS candidate
			WHERE candidate.user_id=$1 AND candidate.source_id=$2 AND candidate.id<>$3 AND
				candidate.finalized_fact_version>0 AND candidate.status='succeeded' AND candidate.coverage='complete' AND
				candidate.observed_at >= $4
		)`, userID, run.SourceID, run.ID, run.ObservedAt).Scan(&newerComplete); err != nil {
			return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
		}
		if !newerComplete {
			if err := reconcileCompleteSnapshotAbsencesTx(ctx, tx, run); err != nil {
				var invariantErr falseRemovalInvariantError
				if !errors.As(err, &invariantErr) {
					return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
				}
				// The business transaction must roll back, but the safety signal must
				// survive that rollback. Roll back explicitly before using a separate
				// connection; otherwise a single-connection pool can deadlock waiting
				// for the still-open transaction to release its connection.
				if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
					return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, errors.Join(err, rollbackErr)
				}
				if recordErr := s.RecordFalseRemovalInvariant(ctx); recordErr != nil {
					return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, errors.Join(err, recordErr)
				}
				return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
			}
		}
	}
	version, err := recordUpstreamIntelligenceFactMutationTx(ctx, tx, userID, UpstreamIntelligenceFactMutationCollection, run.ID)
	if err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE upstream_collection_runs SET finalized_fact_version=$3,updated_at=now() WHERE id=$1 AND user_id=$2`, run.ID, userID, version.FactVersion); err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	run.FinalizedFactVersion = version.FactVersion
	if err := recordOperationalMetricTx(ctx, tx, "collection_runs", string(run.Status), 1); err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	if err := recordOperationalMetricTx(ctx, tx, "collection_facts", string(run.Status), int64(run.FactCount)); err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	if err := recordOperationalMetricTx(ctx, tx, "collection_coverage", string(run.Coverage), 1); err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	if err := recordCollectionDurationTx(ctx, tx, string(run.Status), run.StartedAt, run.CompletedAt); err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	// Historical runs may arrive after a newer observation. They remain valid
	// facts and receive a fact version, but only the newest (observed_at, id)
	// tuple owns the source's current status pointer. Successful history advances
	// last_success_at monotonically even when it is not the latest run overall.
	_, err = tx.Exec(ctx, `UPDATE upstream_intelligence_sources AS source SET
		last_success_at=CASE
			WHEN $5='succeeded' AND $6='complete' AND (source.last_success_at IS NULL OR source.last_success_at < $3)
			THEN $3 ELSE source.last_success_at END,
		last_run_at=CASE WHEN NOT EXISTS (
			SELECT 1 FROM upstream_collection_runs AS candidate
			WHERE candidate.source_id=$1 AND candidate.user_id=$2 AND candidate.finalized_fact_version>0 AND
				(candidate.observed_at>$3 OR (candidate.observed_at=$3 AND candidate.id>$4))
		) THEN $3 ELSE source.last_run_at END,
		last_coverage=CASE WHEN NOT EXISTS (
			SELECT 1 FROM upstream_collection_runs AS candidate
			WHERE candidate.source_id=$1 AND candidate.user_id=$2 AND candidate.finalized_fact_version>0 AND
				(candidate.observed_at>$3 OR (candidate.observed_at=$3 AND candidate.id>$4))
		) THEN $6 ELSE source.last_coverage END,
		last_error_code=CASE WHEN NOT EXISTS (
			SELECT 1 FROM upstream_collection_runs AS candidate
			WHERE candidate.source_id=$1 AND candidate.user_id=$2 AND candidate.finalized_fact_version>0 AND
				(candidate.observed_at>$3 OR (candidate.observed_at=$3 AND candidate.id>$4))
		) THEN $7 ELSE source.last_error_code END,
		updated_at=CASE WHEN NOT EXISTS (
			SELECT 1 FROM upstream_collection_runs AS candidate
			WHERE candidate.source_id=$1 AND candidate.user_id=$2 AND candidate.finalized_fact_version>0 AND
				(candidate.observed_at>$3 OR (candidate.observed_at=$3 AND candidate.id>$4))
		) OR
			($5='succeeded' AND $6='complete' AND (source.last_success_at IS NULL OR source.last_success_at < $3))
			THEN now() ELSE source.updated_at END
		WHERE source.id=$1 AND source.user_id=$2`,
		run.SourceID, userID, run.ObservedAt, run.ID, string(run.Status), string(run.Coverage), run.ErrorCode)
	if err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	return run, version, nil
}

func reconcileCompleteSnapshotAbsencesTx(ctx context.Context, tx pgx.Tx, run contracts.UpstreamCollectionRun) error {
	offerRows, err := tx.Query(ctx, `SELECT `+offerCols+` FROM upstream_offer_observations
		WHERE user_id=$1 AND source_id=$2 AND run_id=$3 ORDER BY group_key,model_key,price_dimension FOR UPDATE`,
		run.UserID, run.SourceID, run.ID)
	if err != nil {
		return err
	}
	offers := make([]contracts.UpstreamOfferObservation, 0)
	for offerRows.Next() {
		offer, scanErr := scanOffer(offerRows)
		if scanErr != nil {
			offerRows.Close()
			return scanErr
		}
		offers = append(offers, offer)
	}
	rowsErr := offerRows.Err()
	offerRows.Close()
	if rowsErr != nil {
		return rowsErr
	}

	absenceRows, err := tx.Query(ctx, `SELECT `+absenceCols+` FROM upstream_snapshot_absences
		WHERE user_id=$1 AND source_id=$2 ORDER BY comparison_key FOR UPDATE`, run.UserID, run.SourceID)
	if err != nil {
		return err
	}
	previous := make([]UpstreamSnapshotAbsence, 0)
	for absenceRows.Next() {
		absence, scanErr := scanAbsence(absenceRows)
		if scanErr != nil {
			absenceRows.Close()
			return scanErr
		}
		previous = append(previous, absence)
	}
	rowsErr = absenceRows.Err()
	absenceRows.Close()
	if rowsErr != nil {
		return rowsErr
	}

	states, changes, err := reconcileCompleteSnapshotAbsences(run, offers, previous, nowUTC())
	if err != nil {
		return err
	}
	if invariantErr := validateRemovalEvents(run, states, changes); invariantErr != nil {
		return falseRemovalInvariantError{cause: invariantErr}
	}
	for _, state := range states {
		if _, err := tx.Exec(ctx, `INSERT INTO upstream_snapshot_absences
			(user_id,source_id,comparison_key,consecutive_complete_runs,last_present_observation_id,last_present_run_id,first_absent_at,last_absent_run_id,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (source_id,comparison_key) DO UPDATE SET
				user_id=EXCLUDED.user_id,consecutive_complete_runs=EXCLUDED.consecutive_complete_runs,
				last_present_observation_id=EXCLUDED.last_present_observation_id,last_present_run_id=EXCLUDED.last_present_run_id,
				first_absent_at=EXCLUDED.first_absent_at,last_absent_run_id=EXCLUDED.last_absent_run_id,updated_at=EXCLUDED.updated_at
			WHERE upstream_snapshot_absences.user_id=EXCLUDED.user_id`,
			state.UserID, state.SourceID, state.ComparisonKey, state.ConsecutiveCompleteRuns, state.LastPresentObservationID,
			state.LastPresentRunID, state.FirstAbsentAt, state.LastAbsentRunID, state.UpdatedAt); err != nil {
			return mapUpstreamWriteError(err)
		}
	}
	for _, event := range changes {
		impact, err := jsonObject(event.ImpactScope)
		if err != nil {
			return ErrInvalid
		}
		command, err := tx.Exec(ctx, `INSERT INTO upstream_change_events
			(id,user_id,source_id,event_type,event_fingerprint,before_observation_id,after_observation_id,absolute_change,
			 percentage_change,first_detected_at,confirmed_at,severity,impact_scope,group_key,model_key,price_dimension,created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,NULL,NULL,$8,$9,$10,$11,$12,$13,'',$14)
			ON CONFLICT (source_id,event_fingerprint) DO NOTHING`,
			newID("uichg"), event.UserID, event.SourceID, string(event.Type), event.Fingerprint,
			event.BeforeObservationID, event.AfterObservationID, event.FirstDetectedAt, event.ConfirmedAt,
			string(event.Severity), impact, event.GroupKey, event.ModelKey, event.CreatedAt)
		if err != nil {
			return mapUpstreamWriteError(err)
		}
		if command.RowsAffected() == 1 {
			if err := recordOperationalMetricTx(ctx, tx, "change_events", string(event.Type), 1); err != nil {
				return err
			}
		}
	}
	return nil
}
