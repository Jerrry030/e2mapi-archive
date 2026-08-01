package store

import (
	"context"
	"sort"
	"time"

	"e2m.local/contracts"
)

const (
	DefaultUpstreamIntelligenceRetentionBatchSize = 100
	MaxUpstreamIntelligenceRetentionBatchSize     = 1_000
	DefaultUpstreamIntelligenceRetentionOwnerPage = 100
	MaxUpstreamIntelligenceRetentionOwnerPage     = 1_000
)

// UpstreamIntelligenceRetentionResult describes one owner-scoped, atomic
// history-pruning batch. Change events and absence state are intentionally not
// part of this result because the raw-history worker never deletes them.
type UpstreamIntelligenceRetentionResult struct {
	UserID            int64
	RunsDeleted       int
	BatchesDeleted    int
	WalletsDeleted    int
	OffersDeleted     int
	FinalizedDeleted  int
	ResultFactVersion int64
}

// UpstreamIntelligenceRetentionStore is separate from the ingest/read store so
// services that only need current intelligence do not inherit destructive
// history operations. Every prune call is scoped to exactly one owner and one
// bounded batch.
type UpstreamIntelligenceRetentionStore interface {
	ListUpstreamIntelligenceRetentionOwners(context.Context, time.Time, int64, int) ([]int64, error)
	PruneUpstreamIntelligenceHistory(context.Context, int64, time.Time, int) (UpstreamIntelligenceRetentionResult, error)
}

func AsUpstreamIntelligenceRetentionStore(st Store) (UpstreamIntelligenceRetentionStore, bool) {
	retention, ok := st.(UpstreamIntelligenceRetentionStore)
	return retention, ok
}

func normalizeUpstreamRetentionLimit(limit, defaultValue, maxValue int) int {
	if limit <= 0 {
		return defaultValue
	}
	if limit > maxValue {
		return maxValue
	}
	return limit
}

func validUpstreamRetentionCursor(cutoff time.Time, afterUserID int64) bool {
	return !cutoff.IsZero() && afterUserID >= 0
}

func (s *MemoryStore) ListUpstreamIntelligenceRetentionOwners(ctx context.Context, cutoff time.Time, afterUserID int64, limit int) ([]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validUpstreamRetentionCursor(cutoff, afterUserID) {
		return nil, ErrInvalid
	}
	cutoff = normalizeUpstreamTime(cutoff)
	limit = normalizeUpstreamRetentionLimit(limit, DefaultUpstreamIntelligenceRetentionOwnerPage, MaxUpstreamIntelligenceRetentionOwnerPage)
	s.mu.RLock()
	defer s.mu.RUnlock()

	owners := make(map[int64]struct{})
	for _, run := range s.upstreamIntelRuns {
		if run.UserID > afterUserID && run.ReceivedAt.Before(cutoff) {
			owners[run.UserID] = struct{}{}
		}
	}
	ordered := make([]int64, 0, len(owners))
	for userID := range owners {
		ordered = append(ordered, userID)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered, nil
}

func (s *MemoryStore) PruneUpstreamIntelligenceHistory(ctx context.Context, userID int64, cutoff time.Time, limit int) (UpstreamIntelligenceRetentionResult, error) {
	result := UpstreamIntelligenceRetentionResult{UserID: userID}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := requireUpstreamOwner(userID); err != nil || cutoff.IsZero() {
		return result, ErrInvalid
	}
	cutoff = normalizeUpstreamTime(cutoff)
	limit = normalizeUpstreamRetentionLimit(limit, DefaultUpstreamIntelligenceRetentionBatchSize, MaxUpstreamIntelligenceRetentionBatchSize)
	s.mu.Lock()
	defer s.mu.Unlock()

	protected := memoryUpstreamRetentionProtectedRuns(s, userID)
	candidates := make([]contracts.UpstreamCollectionRun, 0)
	for _, run := range s.upstreamIntelRuns {
		if run.UserID != userID || !run.ReceivedAt.Before(cutoff) {
			continue
		}
		if _, keep := protected[run.ID]; keep {
			continue
		}
		candidates = append(candidates, run)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ReceivedAt.Equal(candidates[j].ReceivedAt) {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].ReceivedAt.Before(candidates[j].ReceivedAt)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	if len(candidates) == 0 {
		return result, nil
	}

	deletedRuns := make(map[string]struct{}, len(candidates))
	for _, run := range candidates {
		deletedRuns[run.ID] = struct{}{}
	}
	result.RunsDeleted = len(deletedRuns)
	s.upstreamIntelRuns = retainUpstreamRuns(s.upstreamIntelRuns, userID, deletedRuns)
	s.upstreamIntelBatches, result.BatchesDeleted = retainUpstreamBatches(s.upstreamIntelBatches, userID, deletedRuns)
	s.upstreamIntelWallets, result.WalletsDeleted = retainUpstreamWallets(s.upstreamIntelWallets, userID, deletedRuns)
	s.upstreamIntelOffers, result.OffersDeleted = retainUpstreamOffers(s.upstreamIntelOffers, userID, deletedRuns)
	for runID := range deletedRuns {
		key := memoryUpstreamFinalizationKey(userID, runID)
		if _, exists := s.upstreamIntelFinalized[key]; exists {
			delete(s.upstreamIntelFinalized, key)
			result.FinalizedDeleted++
		}
	}
	version := s.bumpUpstreamIntelligenceFactVersionLocked(userID, s.now(), UpstreamIntelligenceFactMutationRetention, "")
	result.ResultFactVersion = version.FactVersion
	return result, nil
}

func memoryUpstreamRetentionProtectedRuns(s *MemoryStore, userID int64) map[string]struct{} {
	protected := make(map[string]struct{})
	latestFinalized := make(map[string]contracts.UpstreamCollectionRun)
	latestComplete := make(map[string]contracts.UpstreamCollectionRun)
	latestWallet := make(map[string]contracts.UpstreamWalletObservation)
	latestOffers := make(map[string]contracts.UpstreamOfferObservation)
	for _, run := range s.upstreamIntelRuns {
		if run.UserID != userID || run.FinalizedFactVersion <= 0 {
			continue
		}
		if current, exists := latestFinalized[run.SourceID]; !exists || newerUpstreamRetentionRun(run, current) {
			latestFinalized[run.SourceID] = run
		}
		if run.Status == contracts.UpstreamCollectionSucceeded && run.Coverage == contracts.UpstreamCoverageComplete {
			if current, exists := latestComplete[run.SourceID]; !exists || newerUpstreamRetentionRun(run, current) {
				latestComplete[run.SourceID] = run
			}
		}
	}
	for _, run := range latestFinalized {
		protected[run.ID] = struct{}{}
	}
	for _, run := range latestComplete {
		protected[run.ID] = struct{}{}
	}
	// Current wallet/offer rows are part of the materialized read model. Keep
	// the finalized run that owns each current identity even when that run is
	// older than the latest complete snapshot; pruning it would make a current
	// fact disappear. Unfinalized observations never become current facts.
	for _, wallet := range s.upstreamIntelWallets {
		if wallet.UserID != userID || !memoryRetentionRunFinalized(s.upstreamIntelRuns, userID, wallet.RunID) {
			continue
		}
		if current, exists := latestWallet[wallet.SourceID]; !exists || newerUpstreamRetentionWallet(wallet, current) {
			latestWallet[wallet.SourceID] = wallet
		}
	}
	for _, wallet := range latestWallet {
		protected[wallet.RunID] = struct{}{}
	}
	for _, offer := range s.upstreamIntelOffers {
		if offer.UserID != userID || !memoryRetentionRunFinalized(s.upstreamIntelRuns, userID, offer.RunID) {
			continue
		}
		key := offer.SourceID + "\x00" + offer.GroupKey + "\x00" + offer.ModelKey + "\x00" + string(offer.PriceDimension)
		if current, exists := latestOffers[key]; !exists || newerUpstreamRetentionOffer(offer, current) {
			latestOffers[key] = offer
		}
	}
	for _, offer := range latestOffers {
		protected[offer.RunID] = struct{}{}
	}
	for _, absence := range s.upstreamIntelAbsences {
		if absence.UserID == userID && absence.LastPresentRunID != "" {
			protected[absence.LastPresentRunID] = struct{}{}
		}
	}

	changeReferences := make(map[string]struct{})
	for _, event := range s.upstreamIntelChanges {
		if event.UserID != userID {
			continue
		}
		if event.BeforeObservationID != "" {
			changeReferences[event.SourceID+"\x00"+event.BeforeObservationID] = struct{}{}
		}
		if event.AfterObservationID != "" {
			changeReferences[event.SourceID+"\x00"+event.AfterObservationID] = struct{}{}
		}
	}
	for _, wallet := range s.upstreamIntelWallets {
		if wallet.UserID == userID {
			if _, referenced := changeReferences[wallet.SourceID+"\x00"+wallet.ID]; referenced {
				protected[wallet.RunID] = struct{}{}
			}
		}
	}
	for _, offer := range s.upstreamIntelOffers {
		if offer.UserID == userID {
			if _, referenced := changeReferences[offer.SourceID+"\x00"+offer.ID]; referenced {
				protected[offer.RunID] = struct{}{}
			}
		}
	}
	return protected
}

func memoryRetentionRunFinalized(runs []contracts.UpstreamCollectionRun, userID int64, runID string) bool {
	for _, run := range runs {
		if run.UserID == userID && run.ID == runID {
			return run.FinalizedFactVersion > 0
		}
	}
	return false
}

func newerUpstreamRetentionWallet(candidate, current contracts.UpstreamWalletObservation) bool {
	return candidate.ObservedAt.After(current.ObservedAt) || candidate.ObservedAt.Equal(current.ObservedAt) &&
		(candidate.RunID > current.RunID || candidate.RunID == current.RunID && candidate.ID > current.ID)
}

func newerUpstreamRetentionOffer(candidate, current contracts.UpstreamOfferObservation) bool {
	return candidate.ObservedAt.After(current.ObservedAt) || candidate.ObservedAt.Equal(current.ObservedAt) &&
		(candidate.RunID > current.RunID || candidate.RunID == current.RunID && candidate.ID > current.ID)
}

func newerUpstreamRetentionRun(candidate, current contracts.UpstreamCollectionRun) bool {
	return candidate.ObservedAt.After(current.ObservedAt) ||
		(candidate.ObservedAt.Equal(current.ObservedAt) && candidate.ID > current.ID)
}

func retainUpstreamRuns(values []contracts.UpstreamCollectionRun, userID int64, deleted map[string]struct{}) []contracts.UpstreamCollectionRun {
	out := values[:0]
	for _, value := range values {
		if value.UserID == userID {
			if _, remove := deleted[value.ID]; remove {
				continue
			}
		}
		out = append(out, value)
	}
	return out
}

func retainUpstreamBatches(values []UpstreamIntelligenceIngestBatch, userID int64, deleted map[string]struct{}) ([]UpstreamIntelligenceIngestBatch, int) {
	out, removed := values[:0], 0
	for _, value := range values {
		if value.UserID == userID {
			if _, remove := deleted[value.RunID]; remove {
				removed++
				continue
			}
		}
		out = append(out, value)
	}
	return out, removed
}

func retainUpstreamWallets(values []contracts.UpstreamWalletObservation, userID int64, deleted map[string]struct{}) ([]contracts.UpstreamWalletObservation, int) {
	out, removed := values[:0], 0
	for _, value := range values {
		if value.UserID == userID {
			if _, remove := deleted[value.RunID]; remove {
				removed++
				continue
			}
		}
		out = append(out, value)
	}
	return out, removed
}

func retainUpstreamOffers(values []contracts.UpstreamOfferObservation, userID int64, deleted map[string]struct{}) ([]contracts.UpstreamOfferObservation, int) {
	out, removed := values[:0], 0
	for _, value := range values {
		if value.UserID == userID {
			if _, remove := deleted[value.RunID]; remove {
				removed++
				continue
			}
		}
		out = append(out, value)
	}
	return out, removed
}

func (s *PostgresStore) ListUpstreamIntelligenceRetentionOwners(ctx context.Context, cutoff time.Time, afterUserID int64, limit int) ([]int64, error) {
	if !validUpstreamRetentionCursor(cutoff, afterUserID) {
		return nil, ErrInvalid
	}
	cutoff = normalizeUpstreamTime(cutoff)
	limit = normalizeUpstreamRetentionLimit(limit, DefaultUpstreamIntelligenceRetentionOwnerPage, MaxUpstreamIntelligenceRetentionOwnerPage)
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT user_id FROM upstream_collection_runs
		WHERE user_id>$1 AND received_at<$2 ORDER BY user_id LIMIT $3`, afterUserID, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := make([]int64, 0)
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		owners = append(owners, userID)
	}
	return owners, rows.Err()
}

// postgresUpstreamRetentionCandidates mirrors the current-read ordering: a
// finalized run is retained when it owns the newest finalized wallet for its
// source or offer for its full source/group/model/price-dimension identity.
// This is evidence required by the materialized read model, not expendable
// history. Newer unfinalized observations do not displace that evidence.
const postgresUpstreamRetentionCandidates = `SELECT run.id
	FROM upstream_collection_runs AS run
	WHERE run.user_id=$1 AND run.received_at<$2
	  AND NOT (
		run.finalized_fact_version>0 AND NOT EXISTS (
			SELECT 1 FROM upstream_collection_runs AS newer
			WHERE newer.user_id=run.user_id AND newer.source_id=run.source_id AND newer.finalized_fact_version>0
			  AND (newer.observed_at>run.observed_at OR (newer.observed_at=run.observed_at AND newer.id>run.id))
		)
	  )
	  AND NOT (
		run.finalized_fact_version>0 AND run.status='succeeded' AND run.coverage='complete' AND NOT EXISTS (
			SELECT 1 FROM upstream_collection_runs AS newer
			WHERE newer.user_id=run.user_id AND newer.source_id=run.source_id AND newer.finalized_fact_version>0
			  AND newer.status='succeeded' AND newer.coverage='complete'
			  AND (newer.observed_at>run.observed_at OR (newer.observed_at=run.observed_at AND newer.id>run.id))
		)
	  )
	  AND NOT EXISTS (
		SELECT 1 FROM upstream_snapshot_absences AS absence
		WHERE absence.user_id=run.user_id AND absence.source_id=run.source_id AND absence.last_present_run_id=run.id
	  )
	  AND NOT EXISTS (
		SELECT 1 FROM upstream_wallet_observations AS current_wallet
		WHERE current_wallet.user_id=run.user_id AND current_wallet.source_id=run.source_id AND current_wallet.run_id=run.id
		  AND run.finalized_fact_version>0
		  AND NOT EXISTS (
			SELECT 1 FROM upstream_wallet_observations AS newer_wallet
			JOIN upstream_collection_runs AS newer_wallet_run
			  ON newer_wallet_run.user_id=newer_wallet.user_id AND newer_wallet_run.id=newer_wallet.run_id
			WHERE newer_wallet.user_id=current_wallet.user_id AND newer_wallet.source_id=current_wallet.source_id
			  AND newer_wallet_run.finalized_fact_version>0
			  AND (newer_wallet.observed_at>current_wallet.observed_at OR
			       (newer_wallet.observed_at=current_wallet.observed_at AND
			        (newer_wallet.run_id>current_wallet.run_id OR
			         (newer_wallet.run_id=current_wallet.run_id AND newer_wallet.id>current_wallet.id))))
		  )
	  )
	  AND NOT EXISTS (
		SELECT 1 FROM upstream_offer_observations AS current_offer
		WHERE current_offer.user_id=run.user_id AND current_offer.source_id=run.source_id AND current_offer.run_id=run.id
		  AND run.finalized_fact_version>0
		  AND NOT EXISTS (
			SELECT 1 FROM upstream_offer_observations AS newer_offer
			JOIN upstream_collection_runs AS newer_offer_run
			  ON newer_offer_run.user_id=newer_offer.user_id AND newer_offer_run.id=newer_offer.run_id
			WHERE newer_offer.user_id=current_offer.user_id AND newer_offer.source_id=current_offer.source_id
			  AND newer_offer.group_key=current_offer.group_key AND newer_offer.model_key=current_offer.model_key
			  AND newer_offer.price_dimension=current_offer.price_dimension AND newer_offer_run.finalized_fact_version>0
			  AND (newer_offer.observed_at>current_offer.observed_at OR
			       (newer_offer.observed_at=current_offer.observed_at AND
			        (newer_offer.run_id>current_offer.run_id OR
			         (newer_offer.run_id=current_offer.run_id AND newer_offer.id>current_offer.id))))
		  )
	  )
	  AND NOT EXISTS (
		SELECT 1 FROM upstream_wallet_observations AS observation
		WHERE observation.user_id=run.user_id AND observation.source_id=run.source_id AND observation.run_id=run.id
		  AND EXISTS (
			SELECT 1 FROM upstream_change_events AS event
			WHERE event.user_id=run.user_id AND event.source_id=run.source_id
			  AND (event.before_observation_id=observation.id OR event.after_observation_id=observation.id)
		  )
	  )
	  AND NOT EXISTS (
		SELECT 1 FROM upstream_offer_observations AS observation
		WHERE observation.user_id=run.user_id AND observation.source_id=run.source_id AND observation.run_id=run.id
		  AND EXISTS (
			SELECT 1 FROM upstream_change_events AS event
			WHERE event.user_id=run.user_id AND event.source_id=run.source_id
			  AND (event.before_observation_id=observation.id OR event.after_observation_id=observation.id)
		  )
	  )
	ORDER BY run.received_at,run.id
	LIMIT $3
	FOR UPDATE OF run SKIP LOCKED`

func (s *PostgresStore) PruneUpstreamIntelligenceHistory(ctx context.Context, userID int64, cutoff time.Time, limit int) (UpstreamIntelligenceRetentionResult, error) {
	result := UpstreamIntelligenceRetentionResult{UserID: userID}
	if err := requireUpstreamOwner(userID); err != nil || cutoff.IsZero() {
		return result, ErrInvalid
	}
	cutoff = normalizeUpstreamTime(cutoff)
	limit = normalizeUpstreamRetentionLimit(limit, DefaultUpstreamIntelligenceRetentionBatchSize, MaxUpstreamIntelligenceRetentionBatchSize)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, postgresUpstreamRetentionCandidates, userID, cutoff, limit)
	if err != nil {
		return result, err
	}
	runIDs := make([]string, 0, limit)
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return result, err
		}
		runIDs = append(runIDs, runID)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return result, rowsErr
	}
	if len(runIDs) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return result, err
		}
		return result, nil
	}

	if err := tx.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM upstream_ingest_batches WHERE user_id=$1 AND run_id=ANY($2::text[])),
		(SELECT COUNT(*) FROM upstream_wallet_observations WHERE user_id=$1 AND run_id=ANY($2::text[])),
		(SELECT COUNT(*) FROM upstream_offer_observations WHERE user_id=$1 AND run_id=ANY($2::text[])),
		(SELECT COUNT(*) FROM upstream_collection_runs WHERE user_id=$1 AND id=ANY($2::text[]) AND finalized_fact_version>0)`,
		userID, runIDs).Scan(&result.BatchesDeleted, &result.WalletsDeleted, &result.OffersDeleted, &result.FinalizedDeleted); err != nil {
		return result, err
	}
	command, err := tx.Exec(ctx, `DELETE FROM upstream_collection_runs WHERE user_id=$1 AND id=ANY($2::text[])`, userID, runIDs)
	if err != nil {
		return result, err
	}
	result.RunsDeleted = int(command.RowsAffected())
	if result.RunsDeleted != len(runIDs) {
		return UpstreamIntelligenceRetentionResult{UserID: userID}, ErrConflict
	}
	version, err := recordUpstreamIntelligenceFactMutationTx(ctx, tx, userID, UpstreamIntelligenceFactMutationRetention, "")
	if err != nil {
		return result, err
	}
	result.ResultFactVersion = version.FactVersion
	if err := tx.Commit(ctx); err != nil {
		return UpstreamIntelligenceRetentionResult{UserID: userID}, err
	}
	return result, nil
}

var (
	_ UpstreamIntelligenceRetentionStore = (*MemoryStore)(nil)
	_ UpstreamIntelligenceRetentionStore = (*PostgresStore)(nil)
)
