package store

import (
	"context"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
)

func requireUpstreamOwner(userID int64) error {
	if userID <= 0 {
		return ErrInvalid
	}
	return nil
}

func normalizeUpstreamTime(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.UTC().Truncate(time.Microsecond)
}

func normalizeUpstreamTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := normalizeUpstreamTime(*value)
	return &normalized
}

func (s *MemoryStore) UpsertUpstreamIntelligenceSource(ctx context.Context, input contracts.UpstreamIntelligenceSource) (contracts.UpstreamIntelligenceSource, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamIntelligenceSource{}, err
	}
	if err := requireUpstreamOwner(input.UserID); err != nil || !validUpstreamSource(input) {
		return contracts.UpstreamIntelligenceSource{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	connectorFound := false
	for _, connector := range s.connectors {
		if connector.ID == input.ConnectorID && connector.UserID == input.UserID && connector.InstanceID == input.InstanceID {
			connectorFound = true
			break
		}
	}
	if !connectorFound {
		return contracts.UpstreamIntelligenceSource{}, ErrNotFound
	}
	if input.PollIntervalSeconds == 0 {
		input.PollIntervalSeconds = 300
	}
	if input.Status == "" {
		input.Status = contracts.UpstreamSourceActive
	}
	for i, existing := range s.upstreamIntelSources {
		if existing.UserID == input.UserID && existing.ConnectorID == input.ConnectorID && existing.LocalRef == input.LocalRef {
			if input.ID != "" && input.ID != existing.ID {
				return contracts.UpstreamIntelligenceSource{}, ErrConflict
			}
			input.ID, input.CreatedAt = existing.ID, existing.CreatedAt
			input.UpdatedAt = s.now()
			input.LastRunAt = normalizeUpstreamTimePtr(input.LastRunAt)
			input.LastSuccessAt = normalizeUpstreamTimePtr(input.LastSuccessAt)
			input.NextPollAt = normalizeUpstreamTimePtr(input.NextPollAt)
			input.LastRunAt, input.LastSuccessAt, input.NextPollAt = existing.LastRunAt, existing.LastSuccessAt, existing.NextPollAt
			input.LastCoverage, input.LastErrorCode = existing.LastCoverage, existing.LastErrorCode
			if upstreamIntelligenceSourceConfigurationEqual(existing, input) {
				return cloneUpstreamIntelligenceSource(existing), nil
			}
			recommendationChanged := existing.Status != input.Status
			s.upstreamIntelSources[i] = input
			if recommendationChanged {
				s.bumpUpstreamIntelligenceFactVersionLocked(input.UserID, input.UpdatedAt, UpstreamIntelligenceFactMutationSource, input.ID)
			}
			return input, nil
		}
		if input.ID != "" && existing.ID == input.ID {
			return contracts.UpstreamIntelligenceSource{}, ErrConflict
		}
	}
	if input.ID == "" {
		input.ID = s.nextID("uisrc")
	}
	now := s.now()
	input.LastRunAt, input.LastSuccessAt, input.NextPollAt = nil, nil, nil
	input.LastCoverage, input.LastErrorCode = "", ""
	input.CreatedAt, input.UpdatedAt = now, now
	s.upstreamIntelSources = append(s.upstreamIntelSources, input)
	return input, nil
}

// Source configuration comparison prevents timestamp churn on exact replay.
// Only Status currently participates in recommendation callability and gets a
// source mutation; the other fields remain ordinary source metadata.
func upstreamIntelligenceSourceConfigurationEqual(left, right contracts.UpstreamIntelligenceSource) bool {
	return left.ID == right.ID && left.UserID == right.UserID && left.ConnectorID == right.ConnectorID &&
		left.InstanceID == right.InstanceID && left.LocalRef == right.LocalRef && left.Mode == right.Mode &&
		left.Provider == right.Provider && left.DisplayName == right.DisplayName && left.Currency == right.Currency &&
		left.PollIntervalSeconds == right.PollIntervalSeconds && left.Status == right.Status &&
		left.Capabilities == right.Capabilities
}

func validUpstreamSource(input contracts.UpstreamIntelligenceSource) bool {
	if strings.TrimSpace(input.ConnectorID) == "" || strings.TrimSpace(input.InstanceID) == "" || strings.TrimSpace(input.LocalRef) == "" || strings.TrimSpace(input.DisplayName) == "" || input.Provider != "sub2api" {
		return false
	}
	if input.PollIntervalSeconds != 0 && (input.PollIntervalSeconds < 60 || input.PollIntervalSeconds > 3600) {
		return false
	}
	if input.Mode != contracts.UpstreamSourceOwned && input.Mode != contracts.UpstreamSourceExternal {
		return false
	}
	return input.Status == "" || input.Status == contracts.UpstreamSourceActive || input.Status == contracts.UpstreamSourcePaused || input.Status == contracts.UpstreamSourceDisconnected
}

func (s *MemoryStore) GetUpstreamIntelligenceSource(ctx context.Context, userID int64, id string) (contracts.UpstreamIntelligenceSource, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamIntelligenceSource{}, err
	}
	if err := requireUpstreamOwner(userID); err != nil {
		return contracts.UpstreamIntelligenceSource{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, source := range s.upstreamIntelSources {
		if source.UserID == userID && source.ID == id {
			return source, nil
		}
	}
	return contracts.UpstreamIntelligenceSource{}, ErrNotFound
}

func (s *MemoryStore) ListUpstreamIntelligenceSources(ctx context.Context, filter contracts.UpstreamIntelligenceSourceFilter) ([]contracts.UpstreamIntelligenceSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := requireUpstreamOwner(filter.UserID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.UpstreamIntelligenceSource, 0)
	for _, source := range s.upstreamIntelSources {
		if source.UserID != filter.UserID || filter.ConnectorID != "" && source.ConnectorID != filter.ConnectorID || filter.InstanceID != "" && source.InstanceID != filter.InstanceID || filter.Status != "" && source.Status != filter.Status {
			continue
		}
		out = append(out, source)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return limitSources(out, contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)), nil
}

func limitSources(values []contracts.UpstreamIntelligenceSource, limit int) []contracts.UpstreamIntelligenceSource {
	if limit > 0 && len(values) > limit {
		return values[:limit]
	}
	return values
}

func (s *MemoryStore) CreateUpstreamCollectionRun(ctx context.Context, input contracts.UpstreamCollectionRun) (contracts.UpstreamCollectionRun, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamCollectionRun{}, err
	}
	input.ReceivedAt, input.CreatedAt, input.UpdatedAt, input.FinalizedFactVersion = time.Time{}, time.Time{}, time.Time{}, 0
	if err := requireUpstreamOwner(input.UserID); err != nil || !validUpstreamCollectionRun(input) {
		return contracts.UpstreamCollectionRun{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sourceFound := false
	for _, source := range s.upstreamIntelSources {
		if source.ID == input.SourceID && source.UserID == input.UserID && source.ConnectorID == input.ConnectorID {
			sourceFound = true
			break
		}
	}
	if !sourceFound {
		return contracts.UpstreamCollectionRun{}, ErrNotFound
	}
	for _, existing := range s.upstreamIntelRuns {
		if existing.UserID != input.UserID || existing.ID != input.ID {
			continue
		}
		retry := normalizeCollectionRun(input, existing.CreatedAt)
		retry.CreatedAt, retry.UpdatedAt = existing.CreatedAt, existing.UpdatedAt
		retry.FinalizedFactVersion = existing.FinalizedFactVersion
		if input.ReceivedAt.IsZero() {
			retry.ReceivedAt = existing.ReceivedAt
		}
		if reflect.DeepEqual(existing, retry) {
			return existing, nil
		}
		return contracts.UpstreamCollectionRun{}, ErrConflict
	}
	input = normalizeCollectionRun(input, s.now())
	s.upstreamIntelRuns = append(s.upstreamIntelRuns, input)
	return input, nil
}

func normalizeCollectionRun(run contracts.UpstreamCollectionRun, now time.Time) contracts.UpstreamCollectionRun {
	run.StartedAt, run.ObservedAt = normalizeUpstreamTime(run.StartedAt), normalizeUpstreamTime(run.ObservedAt)
	run.CompletedAt = normalizeUpstreamTimePtr(run.CompletedAt)
	if run.ReceivedAt.IsZero() {
		run.ReceivedAt = now
	} else {
		run.ReceivedAt = normalizeUpstreamTime(run.ReceivedAt)
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.CreatedAt
	}
	return run
}

func (s *MemoryStore) GetUpstreamCollectionRun(ctx context.Context, userID int64, id string) (contracts.UpstreamCollectionRun, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamCollectionRun{}, err
	}
	if err := requireUpstreamOwner(userID); err != nil {
		return contracts.UpstreamCollectionRun{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, run := range s.upstreamIntelRuns {
		if run.UserID == userID && run.ID == id {
			return run, nil
		}
	}
	return contracts.UpstreamCollectionRun{}, ErrNotFound
}

func (s *MemoryStore) ListUpstreamCollectionRuns(ctx context.Context, filter contracts.UpstreamCollectionRunFilter) ([]contracts.UpstreamCollectionRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := requireUpstreamOwner(filter.UserID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.UpstreamCollectionRun, 0)
	for _, run := range s.upstreamIntelRuns {
		if run.UserID != filter.UserID || filter.SourceID != "" && run.SourceID != filter.SourceID || filter.Status != "" && run.Status != filter.Status || !filter.Since.IsZero() && run.ObservedAt.Before(filter.Since) {
			continue
		}
		out = append(out, run)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ObservedAt.After(out[j].ObservedAt) })
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) UpsertUpstreamIntelligenceIngestBatch(ctx context.Context, input UpstreamIntelligenceIngestBatch) (UpstreamIntelligenceIngestBatch, bool, error) {
	if err := ctx.Err(); err != nil {
		return UpstreamIntelligenceIngestBatch{}, false, err
	}
	if err := requireUpstreamOwner(input.UserID); err != nil || input.RunID == "" || input.SourceID == "" || input.BatchNo < 0 || input.BatchCount <= input.BatchNo ||
		!contracts.IsUpstreamIntelligenceSHA256(input.PayloadHash) || !contracts.IsUpstreamIntelligenceSHA256(input.ManifestHash) {
		return UpstreamIntelligenceIngestBatch{}, false, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	runFound := false
	for _, run := range s.upstreamIntelRuns {
		if run.ID == input.RunID && run.UserID == input.UserID && run.SourceID == input.SourceID {
			runFound = true
			break
		}
	}
	if !runFound {
		return UpstreamIntelligenceIngestBatch{}, false, ErrNotFound
	}
	for _, existing := range s.upstreamIntelBatches {
		if existing.UserID != input.UserID || existing.RunID != input.RunID || existing.BatchNo != input.BatchNo {
			continue
		}
		if existing.UserID == input.UserID && existing.SourceID == input.SourceID && existing.PayloadHash == input.PayloadHash && existing.ManifestHash == input.ManifestHash && existing.BatchCount == input.BatchCount && existing.WalletCount == input.WalletCount && existing.OfferCount == input.OfferCount {
			s.recordOperationalMetricLocked("ingest_facts", "duplicate", int64(existing.WalletCount+existing.OfferCount))
			return existing, true, nil
		}
		return UpstreamIntelligenceIngestBatch{}, false, ErrConflict
	}
	if input.ReceivedAt.IsZero() {
		input.ReceivedAt = s.now()
	} else {
		input.ReceivedAt = normalizeUpstreamTime(input.ReceivedAt)
	}
	s.upstreamIntelBatches = append(s.upstreamIntelBatches, input)
	s.recordOperationalMetricLocked("ingest_facts", "accepted", int64(input.WalletCount+input.OfferCount))
	return input, false, nil
}

func (s *MemoryStore) IngestUpstreamIntelligenceBatch(ctx context.Context, input UpstreamIntelligenceIngest) (contracts.UpstreamIntelligenceSource, contracts.UpstreamCollectionRun, UpstreamIntelligenceIngestBatch, bool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamIntelligenceSource{}, contracts.UpstreamCollectionRun{}, UpstreamIntelligenceIngestBatch{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	originalSourceSequence, sequenceExisted := s.seq["uisrc"]
	committed := false
	defer func() {
		if committed {
			return
		}
		if sequenceExisted {
			s.seq["uisrc"] = originalSourceSequence
		} else {
			delete(s.seq, "uisrc")
		}
	}()

	source, sourceIndex, sourceNew, err := s.prepareMemoryUpstreamSource(input.Source)
	if err != nil {
		return contracts.UpstreamIntelligenceSource{}, contracts.UpstreamCollectionRun{}, UpstreamIntelligenceIngestBatch{}, false, err
	}
	if err := bindUpstreamIngestSource(&input, source.ID); err != nil {
		return contracts.UpstreamIntelligenceSource{}, contracts.UpstreamCollectionRun{}, UpstreamIntelligenceIngestBatch{}, false, err
	}
	if input.Run.UserID != source.UserID || input.Run.ConnectorID != source.ConnectorID ||
		input.Batch.UserID != source.UserID || input.Batch.RunID != input.Run.ID ||
		input.Batch.WalletCount != len(input.Wallets) || input.Batch.OfferCount != len(input.Offers) {
		return contracts.UpstreamIntelligenceSource{}, contracts.UpstreamCollectionRun{}, UpstreamIntelligenceIngestBatch{}, false, ErrInvalid
	}
	now := s.now()
	run, runIndex, runNew, err := prepareMemoryUpstreamRun(s.upstreamIntelRuns, input.Run, now)
	if err != nil {
		return contracts.UpstreamIntelligenceSource{}, contracts.UpstreamCollectionRun{}, UpstreamIntelligenceIngestBatch{}, false, err
	}
	batch, duplicate, err := prepareMemoryUpstreamBatch(s.upstreamIntelBatches, run, input.Batch, now)
	if err != nil {
		return contracts.UpstreamIntelligenceSource{}, contracts.UpstreamCollectionRun{}, UpstreamIntelligenceIngestBatch{}, false, err
	}
	preparedWallets, preparedOffers, err := prepareMemoryUpstreamFacts(s.upstreamIntelWallets, s.upstreamIntelOffers, run, input.Wallets, input.Offers, now)
	if err != nil {
		return contracts.UpstreamIntelligenceSource{}, contracts.UpstreamCollectionRun{}, UpstreamIntelligenceIngestBatch{}, false, err
	}
	sourceRecommendationChanged := !sourceNew && s.upstreamIntelSources[sourceIndex].Status != source.Status
	if sourceNew {
		s.upstreamIntelSources = append(s.upstreamIntelSources, source)
	} else {
		s.upstreamIntelSources[sourceIndex] = source
	}
	if runNew {
		s.upstreamIntelRuns = append(s.upstreamIntelRuns, run)
	} else {
		s.upstreamIntelRuns[runIndex] = run
	}
	s.upstreamIntelWallets = append(s.upstreamIntelWallets, preparedWallets...)
	s.upstreamIntelOffers = append(s.upstreamIntelOffers, preparedOffers...)
	if duplicate {
		s.recordOperationalMetricLocked("ingest_facts", "duplicate", int64(batch.WalletCount+batch.OfferCount))
		if sourceRecommendationChanged {
			s.bumpUpstreamIntelligenceFactVersionLocked(source.UserID, source.UpdatedAt, UpstreamIntelligenceFactMutationSource, source.ID)
		}
		committed = true
		return source, run, batch, true, nil
	}
	// Receipt is deliberately last: a visible receipt always means every fact
	// in the declared payload has already been durably accepted.
	s.upstreamIntelBatches = append(s.upstreamIntelBatches, batch)
	s.recordOperationalMetricLocked("ingest_facts", "accepted", int64(batch.WalletCount+batch.OfferCount))
	if sourceRecommendationChanged {
		s.bumpUpstreamIntelligenceFactVersionLocked(source.UserID, source.UpdatedAt, UpstreamIntelligenceFactMutationSource, source.ID)
	}
	committed = true
	return source, run, batch, false, nil
}

func (s *MemoryStore) prepareMemoryUpstreamSource(input contracts.UpstreamIntelligenceSource) (contracts.UpstreamIntelligenceSource, int, bool, error) {
	if err := requireUpstreamOwner(input.UserID); err != nil || !validUpstreamSource(input) {
		return contracts.UpstreamIntelligenceSource{}, -1, false, ErrInvalid
	}
	connectorFound := false
	for _, connector := range s.connectors {
		if connector.ID == input.ConnectorID && connector.UserID == input.UserID && connector.InstanceID == input.InstanceID {
			connectorFound = true
			break
		}
	}
	if !connectorFound {
		return contracts.UpstreamIntelligenceSource{}, -1, false, ErrNotFound
	}
	for i, existing := range s.upstreamIntelSources {
		if existing.UserID == input.UserID && existing.ConnectorID == input.ConnectorID && existing.LocalRef == input.LocalRef {
			if input.ID != "" && input.ID != existing.ID {
				return contracts.UpstreamIntelligenceSource{}, -1, false, ErrConflict
			}
			input.ID, input.CreatedAt, input.UpdatedAt = existing.ID, existing.CreatedAt, s.now()
			input.LastRunAt, input.LastSuccessAt, input.NextPollAt = existing.LastRunAt, existing.LastSuccessAt, existing.NextPollAt
			input.LastCoverage, input.LastErrorCode = existing.LastCoverage, existing.LastErrorCode
			if input.PollIntervalSeconds == 0 {
				input.PollIntervalSeconds = 300
			}
			if input.Status == "" {
				input.Status = contracts.UpstreamSourceActive
			}
			return input, i, false, nil
		}
		if input.ID != "" && existing.ID == input.ID {
			return contracts.UpstreamIntelligenceSource{}, -1, false, ErrConflict
		}
	}
	if input.ID == "" {
		input.ID = s.nextID("uisrc")
	}
	if input.PollIntervalSeconds == 0 {
		input.PollIntervalSeconds = 300
	}
	if input.Status == "" {
		input.Status = contracts.UpstreamSourceActive
	}
	now := s.now()
	input.LastRunAt, input.LastSuccessAt, input.NextPollAt = nil, nil, nil
	input.LastCoverage, input.LastErrorCode = "", ""
	input.CreatedAt, input.UpdatedAt = now, now
	return input, -1, true, nil
}

func prepareMemoryUpstreamRun(existingRuns []contracts.UpstreamCollectionRun, input contracts.UpstreamCollectionRun, now time.Time) (contracts.UpstreamCollectionRun, int, bool, error) {
	input.ReceivedAt, input.CreatedAt, input.UpdatedAt, input.FinalizedFactVersion = time.Time{}, time.Time{}, time.Time{}, 0
	if !validUpstreamCollectionRun(input) {
		return contracts.UpstreamCollectionRun{}, -1, false, ErrInvalid
	}
	for i, existing := range existingRuns {
		if existing.UserID != input.UserID || existing.ID != input.ID {
			continue
		}
		retry := normalizeCollectionRun(input, existing.CreatedAt)
		retry.ReceivedAt, retry.CreatedAt, retry.UpdatedAt, retry.FinalizedFactVersion = existing.ReceivedAt, existing.CreatedAt, existing.UpdatedAt, existing.FinalizedFactVersion
		if reflect.DeepEqual(existing, retry) {
			return existing, i, false, nil
		}
		return contracts.UpstreamCollectionRun{}, -1, false, ErrConflict
	}
	return normalizeCollectionRun(input, now), -1, true, nil
}

func prepareMemoryUpstreamBatch(existingBatches []UpstreamIntelligenceIngestBatch, run contracts.UpstreamCollectionRun, input UpstreamIntelligenceIngestBatch, now time.Time) (UpstreamIntelligenceIngestBatch, bool, error) {
	if input.UserID != run.UserID || input.RunID != run.ID || input.SourceID != run.SourceID || input.BatchNo < 0 || input.BatchCount <= input.BatchNo ||
		input.BatchCount != run.BatchCount || input.ManifestHash != run.ManifestHash || input.WalletCount < 0 || input.OfferCount < 0 ||
		input.WalletCount+input.OfferCount > contracts.MaxUpstreamIntelligenceBatchFacts ||
		!contracts.IsUpstreamIntelligenceSHA256(input.PayloadHash) || !contracts.IsUpstreamIntelligenceSHA256(input.ManifestHash) {
		return UpstreamIntelligenceIngestBatch{}, false, ErrInvalid
	}
	for _, existing := range existingBatches {
		if existing.UserID != input.UserID || existing.RunID != input.RunID || existing.BatchNo != input.BatchNo {
			continue
		}
		if existing.UserID == input.UserID && existing.SourceID == input.SourceID && existing.PayloadHash == input.PayloadHash && existing.ManifestHash == input.ManifestHash && existing.BatchCount == input.BatchCount && existing.WalletCount == input.WalletCount && existing.OfferCount == input.OfferCount {
			return existing, true, nil
		}
		return UpstreamIntelligenceIngestBatch{}, false, ErrConflict
	}
	input.ReceivedAt = now
	return input, false, nil
}

func prepareMemoryUpstreamFacts(existingWallets []contracts.UpstreamWalletObservation, existingOffers []contracts.UpstreamOfferObservation, run contracts.UpstreamCollectionRun, wallets []contracts.UpstreamWalletObservation, offers []contracts.UpstreamOfferObservation, now time.Time) ([]contracts.UpstreamWalletObservation, []contracts.UpstreamOfferObservation, error) {
	preparedWallets := make([]contracts.UpstreamWalletObservation, 0, len(wallets))
	preparedOffers := make([]contracts.UpstreamOfferObservation, 0, len(offers))
	seenWallets := make(map[string]struct{}, len(wallets))
	for _, input := range wallets {
		if _, duplicate := seenWallets[input.ID]; duplicate {
			return nil, nil, ErrInvalid
		}
		seenWallets[input.ID] = struct{}{}
		input.ReceivedAt = now
		input.ObservedAt, input.FreshUntil = normalizeUpstreamTime(input.ObservedAt), normalizeUpstreamTime(input.FreshUntil)
		input.MissingFields = normalizeUpstreamMissingFields(input.MissingFields)
		if input.UserID != run.UserID || input.RunID != run.ID || input.SourceID != run.SourceID || !validUpstreamWallet(input) {
			return nil, nil, ErrInvalid
		}
		found := false
		for _, existing := range existingWallets {
			if existing.UserID == input.UserID && existing.RunID == input.RunID && existing.ID == input.ID {
				input.ReceivedAt = existing.ReceivedAt
				if !reflect.DeepEqual(existing, input) {
					return nil, nil, ErrConflict
				}
				found = true
				break
			}
		}
		if !found {
			preparedWallets = append(preparedWallets, input)
		}
	}
	seenOffers := make(map[string]struct{}, len(offers))
	seenOfferKeys := make(map[string]struct{}, len(offers))
	for _, input := range offers {
		key := input.GroupKey + "\x00" + input.ModelKey + "\x00" + string(input.PriceDimension)
		if _, duplicate := seenOffers[input.ID]; duplicate {
			return nil, nil, ErrInvalid
		}
		if _, duplicate := seenOfferKeys[key]; duplicate {
			return nil, nil, ErrInvalid
		}
		seenOffers[input.ID], seenOfferKeys[key] = struct{}{}, struct{}{}
		input.ReceivedAt = now
		input.ObservedAt, input.EffectiveAt, input.FreshUntil = normalizeUpstreamTime(input.ObservedAt), normalizeUpstreamTime(input.EffectiveAt), normalizeUpstreamTime(input.FreshUntil)
		input.ValidUntil = normalizeUpstreamTimePtr(input.ValidUntil)
		input.MissingFields = normalizeUpstreamMissingFields(input.MissingFields)
		if input.UserID != run.UserID || input.RunID != run.ID || input.SourceID != run.SourceID || !validUpstreamOffer(input) {
			return nil, nil, ErrInvalid
		}
		found := false
		for _, existing := range existingOffers {
			if existing.UserID == input.UserID && existing.RunID == input.RunID && (existing.ID == input.ID || existing.GroupKey == input.GroupKey && existing.ModelKey == input.ModelKey && existing.PriceDimension == input.PriceDimension) {
				input.ReceivedAt = existing.ReceivedAt
				if !reflect.DeepEqual(existing, input) {
					return nil, nil, ErrConflict
				}
				found = true
				break
			}
		}
		if !found {
			preparedOffers = append(preparedOffers, input)
		}
	}
	return preparedWallets, preparedOffers, nil
}

func (s *MemoryStore) ListUpstreamIntelligenceIngestBatches(ctx context.Context, userID int64, runID string) ([]UpstreamIntelligenceIngestBatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := requireUpstreamOwner(userID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]UpstreamIntelligenceIngestBatch, 0)
	for _, batch := range s.upstreamIntelBatches {
		if batch.UserID == userID && batch.RunID == runID {
			out = append(out, batch)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].BatchNo < out[j].BatchNo })
	return out, nil
}

func (s *MemoryStore) AppendUpstreamWalletObservation(ctx context.Context, input contracts.UpstreamWalletObservation) (contracts.UpstreamWalletObservation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamWalletObservation{}, err
	}
	if err := requireUpstreamOwner(input.UserID); err != nil {
		return contracts.UpstreamWalletObservation{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !memoryRunOwned(s.upstreamIntelRuns, input.UserID, input.SourceID, input.RunID) {
		return contracts.UpstreamWalletObservation{}, ErrNotFound
	}
	input.ObservedAt, input.FreshUntil = normalizeUpstreamTime(input.ObservedAt), normalizeUpstreamTime(input.FreshUntil)
	input.MissingFields = normalizeUpstreamMissingFields(input.MissingFields)
	for _, existing := range s.upstreamIntelWallets {
		if existing.UserID == input.UserID && existing.RunID == input.RunID && existing.ID == input.ID {
			input.ReceivedAt = existing.ReceivedAt
			if reflect.DeepEqual(existing, input) {
				return existing, nil
			}
			return contracts.UpstreamWalletObservation{}, ErrConflict
		}
	}
	input.ReceivedAt = s.now()
	if !validUpstreamWallet(input) {
		return contracts.UpstreamWalletObservation{}, ErrInvalid
	}
	s.upstreamIntelWallets = append(s.upstreamIntelWallets, input)
	return input, nil
}

func memoryRunOwned(runs []contracts.UpstreamCollectionRun, userID int64, sourceID, runID string) bool {
	for _, run := range runs {
		if run.ID == runID && run.UserID == userID && run.SourceID == sourceID {
			return true
		}
	}
	return false
}

func (s *MemoryStore) ListUpstreamWalletObservations(ctx context.Context, filter contracts.UpstreamWalletObservationFilter) ([]contracts.UpstreamWalletObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := requireUpstreamOwner(filter.UserID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.UpstreamWalletObservation, 0)
	for _, observation := range s.upstreamIntelWallets {
		if observation.UserID != filter.UserID || filter.SourceID != "" && observation.SourceID != filter.SourceID || !filter.Since.IsZero() && observation.ObservedAt.Before(filter.Since) {
			continue
		}
		out = append(out, observation)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ObservedAt.After(out[j].ObservedAt) })
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) AppendUpstreamOfferObservation(ctx context.Context, input contracts.UpstreamOfferObservation) (contracts.UpstreamOfferObservation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamOfferObservation{}, err
	}
	if err := requireUpstreamOwner(input.UserID); err != nil {
		return contracts.UpstreamOfferObservation{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !memoryRunOwned(s.upstreamIntelRuns, input.UserID, input.SourceID, input.RunID) {
		return contracts.UpstreamOfferObservation{}, ErrNotFound
	}
	input.ObservedAt, input.EffectiveAt, input.FreshUntil = normalizeUpstreamTime(input.ObservedAt), normalizeUpstreamTime(input.EffectiveAt), normalizeUpstreamTime(input.FreshUntil)
	input.ValidUntil = normalizeUpstreamTimePtr(input.ValidUntil)
	input.MissingFields = normalizeUpstreamMissingFields(input.MissingFields)
	for _, existing := range s.upstreamIntelOffers {
		if existing.UserID == input.UserID && existing.RunID == input.RunID && (existing.ID == input.ID || existing.GroupKey == input.GroupKey && existing.ModelKey == input.ModelKey && existing.PriceDimension == input.PriceDimension) {
			input.ReceivedAt = existing.ReceivedAt
			if reflect.DeepEqual(existing, input) {
				return existing, nil
			}
			return contracts.UpstreamOfferObservation{}, ErrConflict
		}
	}
	input.ReceivedAt = s.now()
	if !validUpstreamOffer(input) {
		return contracts.UpstreamOfferObservation{}, ErrInvalid
	}
	s.upstreamIntelOffers = append(s.upstreamIntelOffers, input)
	return input, nil
}

func (s *MemoryStore) ListUpstreamOfferObservations(ctx context.Context, filter contracts.UpstreamOfferObservationFilter) ([]contracts.UpstreamOfferObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := requireUpstreamOwner(filter.UserID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.UpstreamOfferObservation, 0)
	for _, observation := range s.upstreamIntelOffers {
		if observation.UserID != filter.UserID || filter.SourceID != "" && observation.SourceID != filter.SourceID || filter.GroupKey != "" && observation.GroupKey != filter.GroupKey || filter.ModelKey != "" && observation.ModelKey != filter.ModelKey || filter.PriceDimension != "" && observation.PriceDimension != filter.PriceDimension || !filter.Since.IsZero() && observation.ObservedAt.Before(filter.Since) {
			continue
		}
		out = append(out, observation)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ObservedAt.After(out[j].ObservedAt) })
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) UpsertUpstreamSnapshotAbsence(ctx context.Context, input UpstreamSnapshotAbsence) (UpstreamSnapshotAbsence, error) {
	if err := ctx.Err(); err != nil {
		return UpstreamSnapshotAbsence{}, err
	}
	if err := requireUpstreamOwner(input.UserID); err != nil || input.SourceID == "" || input.ComparisonKey == "" || input.ConsecutiveCompleteRuns < 0 {
		return UpstreamSnapshotAbsence{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !memorySourceOwned(s.upstreamIntelSources, input.UserID, input.SourceID) {
		return UpstreamSnapshotAbsence{}, ErrNotFound
	}
	input.FirstAbsentAt = normalizeUpstreamTimePtr(input.FirstAbsentAt)
	input.UpdatedAt = s.now()
	for i := range s.upstreamIntelAbsences {
		if s.upstreamIntelAbsences[i].SourceID == input.SourceID && s.upstreamIntelAbsences[i].ComparisonKey == input.ComparisonKey {
			s.upstreamIntelAbsences[i] = input
			return input, nil
		}
	}
	s.upstreamIntelAbsences = append(s.upstreamIntelAbsences, input)
	return input, nil
}

func memorySourceOwned(sources []contracts.UpstreamIntelligenceSource, userID int64, sourceID string) bool {
	for _, source := range sources {
		if source.ID == sourceID && source.UserID == userID {
			return true
		}
	}
	return false
}

func (s *MemoryStore) ListUpstreamSnapshotAbsences(ctx context.Context, userID int64, sourceID string) ([]UpstreamSnapshotAbsence, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := requireUpstreamOwner(userID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]UpstreamSnapshotAbsence, 0)
	for _, absence := range s.upstreamIntelAbsences {
		if absence.UserID == userID && (sourceID == "" || absence.SourceID == sourceID) {
			out = append(out, absence)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ComparisonKey < out[j].ComparisonKey })
	return out, nil
}

func (s *MemoryStore) UpsertUpstreamIntelligenceLink(ctx context.Context, input contracts.UpstreamIntelligenceLink) (contracts.UpstreamIntelligenceLink, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamIntelligenceLink{}, err
	}
	input.VerifiedAt = normalizeUpstreamTimePtr(input.VerifiedAt)
	if err := requireUpstreamOwner(input.UserID); err != nil || !validUpstreamIntelligenceLink(input) {
		return contracts.UpstreamIntelligenceLink{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !memorySourceOwned(s.upstreamIntelSources, input.UserID, input.IntelligenceSourceID) {
		return contracts.UpstreamIntelligenceLink{}, ErrNotFound
	}
	if input.Scope == contracts.UpstreamLinkChannel {
		allocation, ok := s.channelAllocations[input.ChannelID]
		if !ok || allocation.UserID != input.UserID {
			return contracts.UpstreamIntelligenceLink{}, ErrNotFound
		}
	} else {
		matches := memoryUpstreamSourceIdentityAllocatedChannels(s.channelAllocations, input.UserID, input.UpstreamSourceIdentity)
		if len(matches) == 0 {
			return contracts.UpstreamIntelligenceLink{}, ErrNotFound
		}
		if input.Status == contracts.UpstreamLinkActive && len(matches) != 1 {
			return contracts.UpstreamIntelligenceLink{}, ErrConflict
		}
	}
	for _, existing := range s.upstreamIntelLinks {
		if existing.ID != input.ID && input.Status == contracts.UpstreamLinkActive && existing.Status == contracts.UpstreamLinkActive && existing.UserID == input.UserID && existing.Scope == input.Scope && existing.PriceDimension == input.PriceDimension && (input.Scope == contracts.UpstreamLinkChannel && existing.ChannelID == input.ChannelID || input.Scope == contracts.UpstreamLinkSourceIdentity && existing.UpstreamSourceIdentity == input.UpstreamSourceIdentity) {
			return contracts.UpstreamIntelligenceLink{}, ErrDuplicate
		}
	}
	now := normalizeUpstreamTime(s.now())
	for i, existing := range s.upstreamIntelLinks {
		if existing.ID == input.ID {
			if existing.UserID != input.UserID {
				return contracts.UpstreamIntelligenceLink{}, ErrNotFound
			}
			if existing.IntelligenceSourceID != input.IntelligenceSourceID {
				return contracts.UpstreamIntelligenceLink{}, ErrConflict
			}
			if upstreamIntelligenceLinkBusinessEqual(existing, input) {
				return cloneUpstreamLink(existing), nil
			}
			input.CreatedAt, input.UpdatedAt = existing.CreatedAt, now
			s.upstreamIntelLinks[i] = input
			s.bumpUpstreamIntelligenceFactVersionLocked(input.UserID, now, UpstreamIntelligenceFactMutationLink, input.ID)
			return cloneUpstreamLink(input), nil
		}
	}
	if input.ID == "" {
		input.ID = s.nextID("uilink")
	}
	input.CreatedAt, input.UpdatedAt = now, now
	s.upstreamIntelLinks = append(s.upstreamIntelLinks, input)
	s.bumpUpstreamIntelligenceFactVersionLocked(input.UserID, now, UpstreamIntelligenceFactMutationLink, input.ID)
	return cloneUpstreamLink(input), nil
}

func validUpstreamIntelligenceLink(input contracts.UpstreamIntelligenceLink) bool {
	if strings.TrimSpace(input.IntelligenceSourceID) == "" {
		return false
	}
	if input.Status != contracts.UpstreamLinkActive && input.Status != contracts.UpstreamLinkInactive {
		return false
	}
	if input.Status == contracts.UpstreamLinkActive &&
		(input.VerifiedAt == nil || input.VerifiedAt.IsZero() || input.PriceDimension == "") {
		return false
	}
	switch input.PriceDimension {
	case "", contracts.UpstreamPriceInput, contracts.UpstreamPriceOutput, contracts.UpstreamPriceCachedInput, contracts.UpstreamPriceRequest:
	default:
		return false
	}
	switch input.Scope {
	case contracts.UpstreamLinkChannel:
		return strings.TrimSpace(input.ChannelID) != "" && input.UpstreamSourceIdentity == ""
	case contracts.UpstreamLinkSourceIdentity:
		return contracts.IsUpstreamSourceIdentity(input.UpstreamSourceIdentity) && input.ChannelID == ""
	default:
		return false
	}
}

func memoryUpstreamSourceIdentityAllocatedChannels(allocations map[string]upstreamChannelAllocation, userID int64, sourceIdentity string) []string {
	channels := make([]string, 0, 1)
	for channelID, allocation := range allocations {
		if allocation.UserID == userID && allocation.SourceID == sourceIdentity {
			channels = append(channels, channelID)
		}
	}
	sort.Strings(channels)
	return channels
}

func upstreamIntelligenceLinkBusinessEqual(left, right contracts.UpstreamIntelligenceLink) bool {
	return left.ID == right.ID && left.UserID == right.UserID &&
		left.IntelligenceSourceID == right.IntelligenceSourceID && left.Scope == right.Scope &&
		left.UpstreamSourceIdentity == right.UpstreamSourceIdentity && left.ChannelID == right.ChannelID &&
		left.PriceDimension == right.PriceDimension && left.Status == right.Status &&
		upstreamTimePtrEqual(left.VerifiedAt, right.VerifiedAt)
}

func upstreamTimePtrEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func (s *MemoryStore) bumpUpstreamIntelligenceFactVersionLocked(userID int64, at time.Time, kind UpstreamIntelligenceFactMutationKind, evidenceID string) contracts.UpstreamIntelligenceFactVersion {
	version := s.upstreamIntelVersions[userID]
	if _, initialized := s.upstreamIntelLineageWatermarks[userID]; !initialized {
		// Existing versions can predate mutation lineage (for example a local
		// fixture restored from an older schema). Anchor the first managed
		// mutation at that exact version; callers must fail closed across it.
		s.upstreamIntelLineageWatermarks[userID] = version.FactVersion
	}
	version.UserID = userID
	version.FactVersion++
	version.UpdatedAt = normalizeUpstreamTime(at)
	s.upstreamIntelVersions[userID] = version
	s.upstreamIntelFactMutations[userID] = append(s.upstreamIntelFactMutations[userID], UpstreamIntelligenceFactMutation{
		UserID: userID, FactVersion: version.FactVersion, Kind: kind, EvidenceID: evidenceID, CreatedAt: version.UpdatedAt,
	})
	return version
}

func (s *MemoryStore) ListUpstreamIntelligenceLinks(ctx context.Context, filter contracts.UpstreamIntelligenceLinkFilter) ([]contracts.UpstreamIntelligenceLink, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := requireUpstreamOwner(filter.UserID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.UpstreamIntelligenceLink, 0)
	for _, link := range s.upstreamIntelLinks {
		if link.UserID != filter.UserID || filter.IntelligenceSourceID != "" && link.IntelligenceSourceID != filter.IntelligenceSourceID || filter.Scope != "" && link.Scope != filter.Scope || filter.Status != "" && link.Status != filter.Status {
			continue
		}
		out = append(out, link)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) AppendUpstreamChangeEvent(ctx context.Context, input contracts.UpstreamChangeEvent) (contracts.UpstreamChangeEvent, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamChangeEvent{}, err
	}
	if err := requireUpstreamOwner(input.UserID); err != nil || input.SourceID == "" || input.Fingerprint == "" {
		return contracts.UpstreamChangeEvent{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !memorySourceOwned(s.upstreamIntelSources, input.UserID, input.SourceID) {
		return contracts.UpstreamChangeEvent{}, ErrNotFound
	}
	input.FirstDetectedAt, input.ConfirmedAt = normalizeUpstreamTime(input.FirstDetectedAt), normalizeUpstreamTime(input.ConfirmedAt)
	if input.CreatedAt.IsZero() {
		input.CreatedAt = s.now()
	}
	for _, existing := range s.upstreamIntelChanges {
		if existing.SourceID == input.SourceID && existing.Fingerprint == input.Fingerprint {
			if reflect.DeepEqual(existing, input) {
				return existing, nil
			}
			return contracts.UpstreamChangeEvent{}, ErrConflict
		}
	}
	if input.ID == "" {
		input.ID = s.nextID("uichg")
	}
	s.upstreamIntelChanges = append(s.upstreamIntelChanges, input)
	s.recordOperationalMetricLocked("change_events", string(input.Type), 1)
	return input, nil
}

func (s *MemoryStore) ListUpstreamChangeEvents(ctx context.Context, filter contracts.UpstreamChangeEventFilter) ([]contracts.UpstreamChangeEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := requireUpstreamOwner(filter.UserID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.UpstreamChangeEvent, 0)
	for _, event := range s.upstreamIntelChanges {
		if event.UserID != filter.UserID || filter.SourceID != "" && event.SourceID != filter.SourceID || filter.Type != "" && event.Type != filter.Type || !filter.Since.IsZero() && event.ConfirmedAt.Before(filter.Since) {
			continue
		}
		out = append(out, event)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ConfirmedAt.After(out[j].ConfirmedAt) })
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) GetUpstreamIntelligenceFactVersion(ctx context.Context, userID int64) (contracts.UpstreamIntelligenceFactVersion, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamIntelligenceFactVersion{}, err
	}
	if err := requireUpstreamOwner(userID); err != nil {
		return contracts.UpstreamIntelligenceFactVersion{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if version, ok := s.upstreamIntelVersions[userID]; ok {
		return version, nil
	}
	return contracts.UpstreamIntelligenceFactVersion{UserID: userID}, nil
}

func (s *MemoryStore) FinalizeUpstreamCollectionRun(ctx context.Context, userID int64, runID string) (contracts.UpstreamCollectionRun, contracts.UpstreamIntelligenceFactVersion, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	if err := requireUpstreamOwner(userID); err != nil {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	runIndex := -1
	for i := range s.upstreamIntelRuns {
		if s.upstreamIntelRuns[i].ID == runID && s.upstreamIntelRuns[i].UserID == userID {
			runIndex = i
			break
		}
	}
	if runIndex < 0 {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, ErrNotFound
	}
	run := s.upstreamIntelRuns[runIndex]
	if run.Status == contracts.UpstreamCollectionRunning || run.CompletedAt == nil || run.BatchCount <= 0 || run.ManifestHash == "" {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, ErrConflict
	}
	batchSeen := make(map[int]struct{}, run.BatchCount)
	manifestBatches := make([]contracts.UpstreamIntelligenceManifestBatch, 0, run.BatchCount)
	walletCount, offerCount := 0, 0
	for _, batch := range s.upstreamIntelBatches {
		if batch.UserID != userID || batch.RunID != run.ID {
			continue
		}
		if batch.BatchCount != run.BatchCount || batch.ManifestHash != run.ManifestHash {
			return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, ErrConflict
		}
		batchSeen[batch.BatchNo] = struct{}{}
		manifestBatches = append(manifestBatches, contracts.UpstreamIntelligenceManifestBatch{BatchNo: batch.BatchNo, PayloadHash: batch.PayloadHash})
		walletCount += batch.WalletCount
		offerCount += batch.OfferCount
	}
	if len(batchSeen) != run.BatchCount || walletCount+offerCount != run.FactCount {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, ErrConflict
	}
	manifestHash, err := contracts.CalculateUpstreamIntelligenceManifestHash(manifestBatches)
	if err != nil || manifestHash != run.ManifestHash {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, ErrConflict
	}
	facts := 0
	for _, wallet := range s.upstreamIntelWallets {
		if wallet.UserID == userID && wallet.RunID == run.ID {
			if run.Status == contracts.UpstreamCollectionSucceeded && run.Coverage == contracts.UpstreamCoverageComplete && wallet.Coverage != contracts.UpstreamCoverageComplete {
				return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, ErrConflict
			}
			facts++
		}
	}
	for _, offer := range s.upstreamIntelOffers {
		if offer.UserID == userID && offer.RunID == run.ID {
			if run.Status == contracts.UpstreamCollectionSucceeded && run.Coverage == contracts.UpstreamCoverageComplete && offer.Coverage != contracts.UpstreamCoverageComplete {
				return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, ErrConflict
			}
			facts++
		}
	}
	if facts != run.FactCount {
		return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, ErrConflict
	}
	finalizationKey := memoryUpstreamFinalizationKey(userID, run.ID)
	if finalized, ok := s.upstreamIntelFinalized[finalizationKey]; ok {
		current := s.upstreamIntelVersions[userID]
		if finalized.UserID != userID || current.FactVersion < finalized.FactVersion {
			return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, ErrConflict
		}
		run.FinalizedFactVersion = finalized.FactVersion
		return run, contracts.UpstreamIntelligenceFactVersion{UserID: userID, FactVersion: finalized.FactVersion, UpdatedAt: current.UpdatedAt}, nil
	}
	now := s.now()
	if run.Status == contracts.UpstreamCollectionSucceeded && run.Coverage == contracts.UpstreamCoverageComplete &&
		isStrictlyNewestCompleteUpstreamRun(s.upstreamIntelRuns, run) {
		offers := make([]contracts.UpstreamOfferObservation, 0)
		for _, offer := range s.upstreamIntelOffers {
			if offer.UserID == userID && offer.SourceID == run.SourceID && offer.RunID == run.ID {
				offers = append(offers, offer)
			}
		}
		previous := make([]UpstreamSnapshotAbsence, 0)
		for _, absence := range s.upstreamIntelAbsences {
			if absence.UserID == userID && absence.SourceID == run.SourceID {
				previous = append(previous, absence)
			}
		}
		absences, changes, changeErr := reconcileCompleteSnapshotAbsences(run, offers, previous, now)
		if changeErr != nil {
			return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, changeErr
		}
		if invariantErr := validateRemovalEvents(run, absences, changes); invariantErr != nil {
			s.operationalEventCounters[operationalEventFalseRemovalInvariant]++
			return contracts.UpstreamCollectionRun{}, contracts.UpstreamIntelligenceFactVersion{}, invariantErr
		}
		s.upstreamIntelAbsences = replaceMemoryUpstreamAbsences(s.upstreamIntelAbsences, userID, run.SourceID, absences)
		for index := range changes {
			changes[index].ID = s.nextID("uichg")
			s.upstreamIntelChanges = append(s.upstreamIntelChanges, changes[index])
			s.recordOperationalMetricLocked("change_events", string(changes[index].Type), 1)
		}
	}
	version := s.bumpUpstreamIntelligenceFactVersionLocked(userID, now, UpstreamIntelligenceFactMutationCollection, run.ID)
	run.FinalizedFactVersion = version.FactVersion
	s.upstreamIntelRuns[runIndex].FinalizedFactVersion = version.FactVersion
	s.upstreamIntelFinalized[finalizationKey] = memoryUpstreamFinalization{RunID: run.ID, UserID: userID, FactVersion: version.FactVersion}
	s.recordOperationalMetricLocked("collection_runs", string(run.Status), 1)
	s.recordOperationalMetricLocked("collection_facts", string(run.Status), int64(run.FactCount))
	s.recordOperationalMetricLocked("collection_coverage", string(run.Coverage), 1)
	s.recordCollectionDurationLocked(string(run.Status), run.StartedAt, run.CompletedAt)
	for i := range s.upstreamIntelSources {
		if s.upstreamIntelSources[i].ID != run.SourceID || s.upstreamIntelSources[i].UserID != userID {
			continue
		}
		// A delayed older run remains valid history and receives its own fact
		// version, but it must never move the source's current pointer backward.
		// Run IDs provide a deterministic tie-break when two observations share
		// the same timestamp.
		observedAt := run.ObservedAt
		if run.Status == contracts.UpstreamCollectionSucceeded && run.Coverage == contracts.UpstreamCoverageComplete {
			if successAt := s.upstreamIntelSources[i].LastSuccessAt; successAt == nil || observedAt.After(*successAt) {
				s.upstreamIntelSources[i].LastSuccessAt = &observedAt
			}
		}
		if currentAt := s.upstreamIntelSources[i].LastRunAt; currentAt != nil &&
			(run.ObservedAt.Before(*currentAt) || run.ObservedAt.Equal(*currentAt) && run.ID <= latestFinalizedUpstreamRunID(s.upstreamIntelRuns, userID, run.SourceID, *currentAt, run.ID)) {
			break
		}
		s.upstreamIntelSources[i].LastRunAt = &observedAt
		s.upstreamIntelSources[i].LastCoverage = run.Coverage
		s.upstreamIntelSources[i].LastErrorCode = run.ErrorCode
		s.upstreamIntelSources[i].UpdatedAt = now
		break
	}
	return run, version, nil
}

func isStrictlyNewestCompleteUpstreamRun(runs []contracts.UpstreamCollectionRun, run contracts.UpstreamCollectionRun) bool {
	for _, candidate := range runs {
		if candidate.UserID != run.UserID || candidate.SourceID != run.SourceID || candidate.ID == run.ID ||
			candidate.FinalizedFactVersion <= 0 || candidate.Status != contracts.UpstreamCollectionSucceeded ||
			candidate.Coverage != contracts.UpstreamCoverageComplete {
			continue
		}
		if candidate.ObservedAt.After(run.ObservedAt) || candidate.ObservedAt.Equal(run.ObservedAt) {
			return false
		}
	}
	return true
}

func replaceMemoryUpstreamAbsences(all []UpstreamSnapshotAbsence, userID int64, sourceID string, replacement []UpstreamSnapshotAbsence) []UpstreamSnapshotAbsence {
	out := make([]UpstreamSnapshotAbsence, 0, len(all)+len(replacement))
	for _, absence := range all {
		if absence.UserID != userID || absence.SourceID != sourceID {
			out = append(out, absence)
		}
	}
	return append(out, replacement...)
}

func memoryUpstreamFinalizationKey(userID int64, runID string) string {
	return strconv.FormatInt(userID, 10) + "\x00" + runID
}

func latestFinalizedUpstreamRunID(runs []contracts.UpstreamCollectionRun, userID int64, sourceID string, observedAt time.Time, exceptID string) string {
	latest := ""
	for _, candidate := range runs {
		if candidate.UserID == userID && candidate.SourceID == sourceID && candidate.ID != exceptID &&
			candidate.FinalizedFactVersion > 0 && candidate.ObservedAt.Equal(observedAt) && candidate.ID > latest {
			latest = candidate.ID
		}
	}
	return latest
}
