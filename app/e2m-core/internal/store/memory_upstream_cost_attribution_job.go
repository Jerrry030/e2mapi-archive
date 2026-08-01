package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
)

func (s *MemoryStore) AppendChannelObservationWithCostJob(ctx context.Context, input contracts.ChannelObservation, jobInput *UpstreamCostAttributionJob) (contracts.ChannelObservation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ChannelObservation{}, err
	}
	if jobInput == nil || strings.TrimSpace(input.ID) == "" {
		return contracts.ChannelObservation{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	obs := input
	if obs.Source == "" {
		obs.Source = contracts.ObservationPassive
	}
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = normalizeUpstreamTime(s.now())
	} else {
		obs.ObservedAt = normalizeUpstreamTime(obs.ObservedAt)
	}
	job := *jobInput
	job.OccurredAt = obs.ObservedAt
	normalized, err := normalizeUpstreamCostJob(job)
	if err != nil || normalized.UsageObservationID != obs.ID ||
		normalized.ChannelID != obs.ChannelID || normalized.InstanceID != obs.InstanceID ||
		normalized.ModelKey != obs.Model {
		return contracts.ChannelObservation{}, ErrInvalid
	}
	allocation, allocated := s.channelAllocations[normalized.ChannelID]
	if !allocated || allocation.UserID != normalized.UserID {
		return contracts.ChannelObservation{}, ErrNotFound
	}

	observationFound := false
	for _, existing := range s.channelObs {
		if existing.ID != obs.ID {
			continue
		}
		if !sameChannelObservation(existing, obs) {
			return contracts.ChannelObservation{}, ErrConflict
		}
		obs, observationFound = existing, true
		break
	}
	jobFound := false
	for _, existing := range s.upstreamCostJobs {
		if existing.UsageObservationID != normalized.UsageObservationID {
			continue
		}
		if !sameUpstreamCostJobPayload(existing, normalized) {
			return contracts.ChannelObservation{}, ErrConflict
		}
		jobFound = true
		break
	}

	now := normalizeUpstreamTime(s.now())
	if !observationFound {
		s.channelObs = append(s.channelObs, obs)
	}
	if !jobFound {
		normalized.Status = UpstreamCostJobPending
		normalized.NextAttemptAt = now
		normalized.CreatedAt, normalized.UpdatedAt = now, now
		s.upstreamCostJobs = append(s.upstreamCostJobs, cloneUpstreamCostJob(normalized))
	}
	return obs, nil
}

func (s *MemoryStore) ClaimUpstreamCostAttributionJob(ctx context.Context, workerID string, leaseDuration time.Duration) (UpstreamCostAttributionJob, bool, error) {
	if err := ctx.Err(); err != nil {
		return UpstreamCostAttributionJob{}, false, err
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || leaseDuration <= 0 {
		return UpstreamCostAttributionJob{}, false, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := normalizeUpstreamTime(s.now())
	best := -1
	for i, job := range s.upstreamCostJobs {
		eligible := (job.Status == UpstreamCostJobPending || job.Status == UpstreamCostJobRetrying) && !job.NextAttemptAt.After(now)
		if job.Status == UpstreamCostJobProcessing && job.LeaseUntil != nil && !job.LeaseUntil.After(now) {
			eligible = true
		}
		if !eligible {
			continue
		}
		if best < 0 || job.NextAttemptAt.Before(s.upstreamCostJobs[best].NextAttemptAt) ||
			job.NextAttemptAt.Equal(s.upstreamCostJobs[best].NextAttemptAt) && job.CreatedAt.Before(s.upstreamCostJobs[best].CreatedAt) {
			best = i
		}
	}
	if best < 0 {
		return UpstreamCostAttributionJob{}, false, nil
	}
	job := s.upstreamCostJobs[best]
	job.Status = UpstreamCostJobProcessing
	job.Attempts++
	job.LeaseVersion++
	job.LeaseOwner = workerID
	leaseUntil := now.Add(leaseDuration)
	job.LeaseUntil = &leaseUntil
	job.UpdatedAt = now
	s.upstreamCostJobs[best] = cloneUpstreamCostJob(job)
	return cloneUpstreamCostJob(job), true, nil
}

func (s *MemoryStore) LoadUpstreamCostAttributionEvidence(ctx context.Context, job UpstreamCostAttributionJob) ([]contracts.UpstreamIntelligenceLink, []contracts.UpstreamOfferObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	normalized, err := normalizeUpstreamCostJob(job)
	if err != nil {
		return nil, nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	links := make([]contracts.UpstreamIntelligenceLink, 0, 4)
	sources := make(map[string]struct{}, 4)
	for _, link := range s.upstreamIntelLinks {
		if link.UserID != normalized.UserID || link.Scope != contracts.UpstreamLinkChannel ||
			link.ChannelID != normalized.ChannelID || link.Status != contracts.UpstreamLinkActive ||
			link.VerifiedAt == nil || link.VerifiedAt.IsZero() {
			continue
		}
		links = append(links, cloneUpstreamLink(link))
		sources[link.IntelligenceSourceID] = struct{}{}
	}
	type evidenceKey struct {
		source    string
		dimension contracts.UpstreamPriceDimension
	}
	candidates := make(map[evidenceKey][]contracts.UpstreamOfferObservation)
	for _, offer := range s.upstreamIntelOffers {
		if offer.UserID != normalized.UserID || offer.GroupKey != normalized.GroupKey ||
			offer.ModelKey != normalized.ModelKey {
			continue
		}
		if _, linked := sources[offer.SourceID]; !linked {
			continue
		}
		if _, finalized := s.upstreamIntelFinalized[memoryUpstreamFinalizationKey(offer.UserID, offer.RunID)]; !finalized {
			continue
		}
		key := evidenceKey{source: offer.SourceID, dimension: offer.PriceDimension}
		candidates[key] = append(candidates[key], cloneUpstreamOffer(offer))
	}
	offers := make([]contracts.UpstreamOfferObservation, 0, len(candidates)*2)
	for _, values := range candidates {
		var latestBefore, earliestAfter time.Time
		for _, offer := range values {
			if !offer.EffectiveAt.After(normalized.OccurredAt) {
				if latestBefore.IsZero() || offer.EffectiveAt.After(latestBefore) {
					latestBefore = offer.EffectiveAt
				}
			} else if earliestAfter.IsZero() || offer.EffectiveAt.Before(earliestAfter) {
				earliestAfter = offer.EffectiveAt
			}
		}
		for _, offer := range values {
			if !latestBefore.IsZero() && offer.EffectiveAt.Equal(latestBefore) ||
				!earliestAfter.IsZero() && offer.EffectiveAt.Equal(earliestAfter) {
				offers = append(offers, offer)
			}
		}
	}
	sort.SliceStable(links, func(i, j int) bool { return links[i].ID < links[j].ID })
	sort.SliceStable(offers, func(i, j int) bool {
		if offers[i].EffectiveAt.Equal(offers[j].EffectiveAt) {
			return offers[i].ID < offers[j].ID
		}
		return offers[i].EffectiveAt.Before(offers[j].EffectiveAt)
	})
	return links, offers, nil
}

func (s *MemoryStore) CompleteUpstreamCostAttributionJob(ctx context.Context, claim UpstreamCostAttributionJob, facts []contracts.UpstreamCostFact) ([]contracts.UpstreamCostFact, contracts.UpstreamCostFactVersion, error) {
	if err := ctx.Err(); err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	prepared, err := prepareUpstreamCostBatch(facts)
	if err != nil || !upstreamCostFactsMatchJob(prepared, claim) {
		return nil, contracts.UpstreamCostFactVersion{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := normalizeUpstreamTime(s.now())
	index := -1
	for i := range s.upstreamCostJobs {
		if s.upstreamCostJobs[i].UsageObservationID == claim.UsageObservationID {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, contracts.UpstreamCostFactVersion{}, ErrNotFound
	}
	if err := validateClaimedUpstreamCostJob(s.upstreamCostJobs[index], claim.LeaseOwner, now); err != nil ||
		s.upstreamCostJobs[index].LeaseVersion != claim.LeaseVersion {
		return nil, contracts.UpstreamCostFactVersion{}, ErrConflict
	}
	saved, version, err := s.appendUpstreamCostFactsLocked(prepared, now)
	if err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	job := s.upstreamCostJobs[index]
	job.Status = UpstreamCostJobSucceeded
	job.LastErrorCode, job.LeaseOwner, job.LeaseUntil = "", "", nil
	job.CompletedAt, job.UpdatedAt = &now, now
	s.upstreamCostJobs[index] = cloneUpstreamCostJob(job)
	return saved, version, nil
}

func (s *MemoryStore) RetryUpstreamCostAttributionJob(ctx context.Context, claim UpstreamCostAttributionJob, errorCode string, delay time.Duration) (UpstreamCostAttributionJob, error) {
	if err := ctx.Err(); err != nil {
		return UpstreamCostAttributionJob{}, err
	}
	errorCode = strings.TrimSpace(errorCode)
	if !retryableUpstreamCostJobErrorCode(errorCode) || delay < 0 {
		return UpstreamCostAttributionJob{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := normalizeUpstreamTime(s.now())
	for i, job := range s.upstreamCostJobs {
		if job.UsageObservationID != claim.UsageObservationID {
			continue
		}
		if err := validateClaimedUpstreamCostJob(job, claim.LeaseOwner, now); err != nil ||
			job.LeaseVersion != claim.LeaseVersion {
			return UpstreamCostAttributionJob{}, ErrConflict
		}
		job.Status = UpstreamCostJobRetrying
		job.LastErrorCode = errorCode
		job.NextAttemptAt = now.Add(delay)
		job.LeaseOwner, job.LeaseUntil = "", nil
		job.UpdatedAt = now
		s.upstreamCostJobs[i] = cloneUpstreamCostJob(job)
		return cloneUpstreamCostJob(job), nil
	}
	return UpstreamCostAttributionJob{}, ErrNotFound
}
