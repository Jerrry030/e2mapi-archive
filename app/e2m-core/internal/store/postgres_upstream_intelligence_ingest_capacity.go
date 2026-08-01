package store

import (
	"context"
	"time"
)

func (s *PostgresStore) AdmitUpstreamIntelligenceIngest(ctx context.Context, input UpstreamIntelligenceIngestCapacityRequest) (UpstreamIntelligenceIngestCapacityResult, error) {
	input.Limit = NormalizeUpstreamIntelligenceIngestCapacityLimit(input.Limit)
	if !validUpstreamIntelligenceIngestCapacityRequest(input) {
		return UpstreamIntelligenceIngestCapacityResult{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return UpstreamIntelligenceIngestCapacityResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var result UpstreamIntelligenceIngestCapacityResult
	if err := tx.QueryRow(ctx, `SELECT window_start,window_end,batches_used,facts_used,admitted,replay
		FROM admit_upstream_intelligence_ingest($1,$2,$3,$4,$5,$6,$7,$8)`,
		input.UserID, input.RunID, input.BatchNo, input.PayloadHash, input.FactCount,
		int64(input.Limit.Window/time.Second), input.Limit.MaxBatches, input.Limit.MaxFacts,
	).Scan(&result.WindowStart, &result.WindowEnd, &result.BatchesUsed, &result.FactsUsed, &result.Admitted, &result.Replay); err != nil {
		return UpstreamIntelligenceIngestCapacityResult{}, mapUpstreamWriteError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return UpstreamIntelligenceIngestCapacityResult{}, err
	}
	if !result.Admitted {
		return result, ErrUpstreamIntelligenceIngestQuotaExceeded
	}
	return result, nil
}
