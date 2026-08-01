package store

import (
	"context"
	"reflect"
	"sort"
	"time"

	"e2m.local/contracts"
)

// MemoryStore implementations for append-only quality observations and scoped,
// bucketed snapshots. The isolation boundary is downstream instance + channel
// + model; current queries collapse history to the newest bucket per window.

func (s *MemoryStore) AppendChannelObservation(ctx context.Context, input contracts.ChannelObservation) (contracts.ChannelObservation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ChannelObservation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	obs := input
	if obs.ID != "" {
		for _, existing := range s.channelObs {
			if existing.ID != obs.ID {
				continue
			}
			if sameChannelObservation(existing, obs) {
				return existing, nil
			}
			return contracts.ChannelObservation{}, ErrConflict
		}
	}
	if obs.Source == "" {
		obs.Source = contracts.ObservationPassive
	}
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = s.now()
	} else {
		obs.ObservedAt = obs.ObservedAt.UTC().Truncate(time.Microsecond)
	}
	if obs.ID == "" {
		obs.ID = s.nextID("chobs")
	}
	s.channelObs = append(s.channelObs, obs)
	return obs, nil
}

// sameChannelObservation compares immutable facts while treating omitted
// source/time on a retry as "use the original store default". This lets a
// connector safely retry an event that Core timestamped on first receipt.
func sameChannelObservation(existing, retry contracts.ChannelObservation) bool {
	retry.ID = existing.ID
	if retry.Source == "" {
		retry.Source = existing.Source
	}
	existing.ObservedAt = existing.ObservedAt.UTC().Truncate(time.Microsecond)
	if retry.ObservedAt.IsZero() {
		retry.ObservedAt = existing.ObservedAt
	} else {
		retry.ObservedAt = retry.ObservedAt.UTC().Truncate(time.Microsecond)
	}
	return reflect.DeepEqual(existing, retry)
}

func (s *MemoryStore) ListChannelObservations(ctx context.Context, filter contracts.ChannelObservationFilter) ([]contracts.ChannelObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.ChannelObservation, 0, len(s.channelObs))
	for _, o := range s.channelObs {
		if (filter.ExactScope || filter.ChannelID != "") && o.ChannelID != filter.ChannelID {
			continue
		}
		if (filter.ExactScope || filter.InstanceID != "") && o.InstanceID != filter.InstanceID {
			continue
		}
		if filter.PoolID != "" && o.PoolID != filter.PoolID {
			continue
		}
		if (filter.ExactScope || filter.Model != "") && o.Model != filter.Model {
			continue
		}
		if (filter.ExactScope || filter.Capability != "") && o.Capability != filter.Capability {
			continue
		}
		if (filter.ExactScope || filter.EndpointPath != "") && o.EndpointPath != filter.EndpointPath {
			continue
		}
		if filter.Source != "" && o.Source != filter.Source {
			continue
		}
		if !filter.Since.IsZero() && o.ObservedAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && o.ObservedAt.After(filter.Until) {
			continue
		}
		out = append(out, o)
	}
	// Match PostgreSQL semantics even when observations arrive out of order.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ObservedAt.After(out[j].ObservedAt)
	})
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (s *MemoryStore) UpsertChannelHealthSnapshot(ctx context.Context, input contracts.ChannelHealthSnapshot) (contracts.ChannelHealthSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ChannelHealthSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, createdAtDefaulted := normalizeChannelHealthSnapshot(input, s.now())

	// A caller-supplied id is an immutable evidence identity. Reusing it with a
	// different payload must never rewrite (or alias) historical evidence.
	if snap.ID != "" {
		for _, existing := range s.channelSnapshots {
			if existing.ID != snap.ID {
				continue
			}
			if sameChannelHealthSnapshot(existing, snap, createdAtDefaulted) {
				return existing, nil
			}
			return contracts.ChannelHealthSnapshot{}, ErrConflict
		}
	}

	// Recomputations inside one bucket are immutable revisions. An exact retry
	// resolves to its existing revision; changed facts append a new id so an old
	// recommendation can continue to dereference the evidence it was built on.
	for _, existing := range s.channelSnapshots {
		if sameChannelHealthSnapshotScopeBucket(existing, snap) &&
			sameChannelHealthSnapshot(existing, snap, createdAtDefaulted) {
			return existing, nil
		}
	}
	if snap.ID == "" {
		snap.ID = s.nextID("chsnap")
	}
	s.channelSnapshots = append(s.channelSnapshots, snap)
	s.bumpUpstreamIntelligenceVersionForQualitySnapshotLocked(snap.ChannelID, snap.ID, s.now())
	return snap, nil
}

func normalizeChannelHealthSnapshot(input contracts.ChannelHealthSnapshot, defaultNow time.Time) (contracts.ChannelHealthSnapshot, bool) {
	snap := input
	createdAtDefaulted := snap.CreatedAt.IsZero()
	if createdAtDefaulted {
		snap.CreatedAt = defaultNow
	}
	snap.CreatedAt = snap.CreatedAt.UTC().Truncate(time.Microsecond)
	if snap.BucketStart.IsZero() {
		snap.BucketStart = snap.CreatedAt.Truncate(time.Minute)
	} else {
		snap.BucketStart = snap.BucketStart.UTC().Truncate(time.Microsecond)
	}
	return snap, createdAtDefaulted
}

func sameChannelHealthSnapshotScopeBucket(a, b contracts.ChannelHealthSnapshot) bool {
	return a.ChannelID == b.ChannelID &&
		a.InstanceID == b.InstanceID &&
		a.Model == b.Model &&
		a.Capability == b.Capability &&
		a.EndpointPath == b.EndpointPath &&
		a.Window == b.Window &&
		a.BucketStart.Equal(b.BucketStart)
}

// sameChannelHealthSnapshot compares one immutable revision payload. ID is an
// identity rather than a fact, so a retry which generated a fresh candidate id
// still resolves to the original revision. Omitted CreatedAt has the same
// semantics as AppendChannelObservation: use the timestamp Core assigned on
// first receipt.
func sameChannelHealthSnapshot(existing, retry contracts.ChannelHealthSnapshot, retryCreatedAtDefaulted bool) bool {
	retry.ID = existing.ID
	existing.CreatedAt = existing.CreatedAt.UTC().Truncate(time.Microsecond)
	existing.BucketStart = existing.BucketStart.UTC().Truncate(time.Microsecond)
	if retryCreatedAtDefaulted {
		retry.CreatedAt = existing.CreatedAt
	} else {
		retry.CreatedAt = retry.CreatedAt.UTC().Truncate(time.Microsecond)
	}
	retry.BucketStart = retry.BucketStart.UTC().Truncate(time.Microsecond)
	return reflect.DeepEqual(existing, retry)
}

// bumpUpstreamIntelligenceVersionForQualitySnapshotLocked mirrors migration
// 0060's PostgreSQL trigger. Ownership is derived exclusively from the durable
// channel allocation; a snapshot never supplies or guesses an owner id.
func (s *MemoryStore) bumpUpstreamIntelligenceVersionForQualitySnapshotLocked(channelID, evidenceID string, at time.Time) {
	allocation, allocated := s.channelAllocations[channelID]
	if !allocated || allocation.UserID <= 0 {
		return
	}
	s.bumpUpstreamIntelligenceFactVersionLocked(allocation.UserID, at, UpstreamIntelligenceFactMutationQuality, evidenceID)
}

func (s *MemoryStore) ListChannelHealthSnapshots(ctx context.Context, filter contracts.ChannelHealthSnapshotFilter) ([]contracts.ChannelHealthSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	matches := make([]contracts.ChannelHealthSnapshot, 0, len(s.channelSnapshots))
	for _, snap := range s.channelSnapshots {
		if (filter.ExactScope || filter.ChannelID != "") && snap.ChannelID != filter.ChannelID {
			continue
		}
		if (filter.ExactScope || filter.InstanceID != "") && snap.InstanceID != filter.InstanceID {
			continue
		}
		if filter.PoolID != "" && snap.PoolID != filter.PoolID {
			continue
		}
		if (filter.ExactScope || filter.Model != "") && snap.Model != filter.Model {
			continue
		}
		if (filter.ExactScope || filter.Capability != "") && snap.Capability != filter.Capability {
			continue
		}
		if (filter.ExactScope || filter.EndpointPath != "") && snap.EndpointPath != filter.EndpointPath {
			continue
		}
		if filter.Window != "" && snap.Window != filter.Window {
			continue
		}
		if !filter.BucketStart.IsZero() && !snap.BucketStart.Equal(filter.BucketStart) {
			continue
		}
		if !filter.Since.IsZero() && snap.BucketStart.Before(filter.Since) {
			continue
		}
		matches = append(matches, snap)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].BucketStart.Equal(matches[j].BucketStart) {
			if matches[i].CreatedAt.Equal(matches[j].CreatedAt) {
				return matches[i].ID > matches[j].ID
			}
			return matches[i].CreatedAt.After(matches[j].CreatedAt)
		}
		return matches[i].BucketStart.After(matches[j].BucketStart)
	})

	out := matches
	if !filter.IncludeHistory {
		seen := make(map[string]struct{}, len(matches))
		out = make([]contracts.ChannelHealthSnapshot, 0, len(matches))
		for _, snap := range matches {
			key := snap.InstanceID + "\x00" + snap.ChannelID + "\x00" + snap.Model + "\x00" + string(snap.Capability) + "\x00" + snap.EndpointPath + "\x00" + string(snap.Window)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, snap)
		}
	}
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}
