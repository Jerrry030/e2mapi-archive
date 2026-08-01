package healthmetrics

import (
	"context"
	"sort"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// Service is the store-backed entry point for the metrics layer. It records
// observations (the passive-observation intake, and later the active-probe
// intake) and recomputes windowed snapshots from them. It holds no cross-call
// state of its own; all history lives in the store, so it is safe to construct
// per request or once at startup.
type Service struct {
	store      store.Store
	thresholds Thresholds
	windows    []contracts.HealthWindow
	bucketSize time.Duration
	now        func() time.Time
}

// Option configures a Service.
type Option func(*Service)

// WithThresholds overrides the default aggregation thresholds.
func WithThresholds(t Thresholds) Option { return func(s *Service) { s.thresholds = t } }

// WithWindows overrides which windows Recompute produces. Defaults to 1m and 5m
// (the first-version windows from the design doc).
func WithWindows(w ...contracts.HealthWindow) Option {
	return func(s *Service) {
		if len(w) > 0 {
			s.windows = w
		}
	}
}

// WithClock overrides the time source (tests inject a fixed clock).
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithSnapshotBucketSize controls how often a new historical snapshot row is
// created. Recomputes inside one bucket update in place. The production default
// is one minute; this option mainly makes cadence-specific tests explicit.
func WithSnapshotBucketSize(size time.Duration) Option {
	return func(s *Service) {
		if size > 0 {
			s.bucketSize = size
		}
	}
}

// NewService builds a metrics service over the given store.
func NewService(st store.Store, opts ...Option) *Service {
	s := &Service{
		store:      st,
		thresholds: DefaultThresholds(),
		windows:    []contracts.HealthWindow{contracts.Window1m, contracts.Window5m},
		bucketSize: time.Minute,
		now:        func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Windows reports the windows this service recomputes, in declaration order.
func (s *Service) Windows() []contracts.HealthWindow {
	out := make([]contracts.HealthWindow, len(s.windows))
	copy(out, s.windows)
	return out
}

// RecordObservation persists one measured outcome. The store owns the receive
// timestamp for caller-supplied IDs so an omitted timestamp remains stable on
// retry. Legacy internal observations without an ID retain the service clock.
func (s *Service) RecordObservation(ctx context.Context, obs contracts.ChannelObservation) (contracts.ChannelObservation, error) {
	if obs.Source == "" {
		obs.Source = contracts.ObservationPassive
	}
	if obs.ObservedAt.IsZero() && obs.ID == "" {
		obs.ObservedAt = s.now()
	}
	return s.store.AppendChannelObservation(ctx, obs)
}

// RecomputeChannel discovers every downstream-instance/model scope observed for
// one channel and recomputes each independently. Existing scopes are retained
// after their traffic becomes idle so their current snapshot decays to unknown.
// Legacy observations without instance/model form their own compatibility scope
// and are never mixed into an explicitly-scoped downstream.
func (s *Service) RecomputeChannel(ctx context.Context, channelID string) ([]contracts.ChannelHealthSnapshot, error) {
	now := s.now()
	maxWindow := longestWindow(s.windows)
	recent, err := s.store.ListChannelObservations(ctx, contracts.ChannelObservationFilter{
		ChannelID: channelID,
		Since:     now.Add(-maxWindow),
		Until:     now,
	})
	if err != nil {
		return nil, err
	}
	current, err := s.store.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{
		ChannelID: channelID,
	})
	if err != nil {
		return nil, err
	}
	scopes := discoverScopes(channelID, recent, current)
	if len(scopes) == 0 && len(current) == 0 {
		scopes = []contracts.ChannelHealthScope{{ChannelID: channelID}}
	}

	out := make([]contracts.ChannelHealthSnapshot, 0, len(scopes)*len(s.windows))
	for _, scope := range scopes {
		snaps, err := s.RecomputeScopeAt(ctx, scope, now)
		if err != nil {
			return nil, err
		}
		out = append(out, snaps...)
	}
	return out, nil
}

// RecomputeScopeAt refreshes one exact downstream/channel/model scope at a
// fixed time. It is exported for ingestion paths that already know their scope
// and avoids scanning unrelated downstreams.
func (s *Service) RecomputeScopeAt(ctx context.Context, scope contracts.ChannelHealthScope, now time.Time) ([]contracts.ChannelHealthSnapshot, error) {
	out := make([]contracts.ChannelHealthSnapshot, 0, len(s.windows))
	for _, w := range s.windows {
		since := now.Add(-w.Duration())
		obs, err := s.store.ListChannelObservations(ctx, contracts.ChannelObservationFilter{
			ChannelID:    scope.ChannelID,
			InstanceID:   scope.InstanceID,
			Model:        scope.Model,
			Capability:   scope.Capability,
			EndpointPath: scope.EndpointPath,
			Since:        since,
			Until:        now,
			ExactScope:   true,
		})
		if err != nil {
			return nil, err
		}
		snap := AggregateScope(scope, w, obs, s.thresholds)
		snap.BucketStart = now.UTC().Truncate(s.bucketSize)
		snap.CreatedAt = now
		saved, err := s.store.UpsertChannelHealthSnapshot(ctx, snap)
		if err != nil {
			return nil, err
		}
		out = append(out, saved)
	}
	return out, nil
}

func longestWindow(windows []contracts.HealthWindow) time.Duration {
	var longest time.Duration
	for _, window := range windows {
		if duration := window.Duration(); duration > longest {
			longest = duration
		}
	}
	return longest
}

func discoverScopes(channelID string, observations []contracts.ChannelObservation, snapshots []contracts.ChannelHealthSnapshot) []contracts.ChannelHealthScope {
	byKey := make(map[string]contracts.ChannelHealthScope)
	add := func(scope contracts.ChannelHealthScope) {
		key := scope.InstanceID + "\x00" + scope.Model + "\x00" + string(scope.Capability) + "\x00" + scope.EndpointPath
		if existing, ok := byKey[key]; ok {
			if existing.PoolID == "" && scope.PoolID != "" {
				existing.PoolID = scope.PoolID
				byKey[key] = existing
			}
			return
		}
		scope.ChannelID = channelID
		byKey[key] = scope
	}
	for _, observation := range observations {
		add(contracts.ChannelHealthScope{
			ChannelID: observation.ChannelID, InstanceID: observation.InstanceID,
			PoolID: observation.PoolID, Model: observation.Model,
			Capability: observation.Capability, EndpointPath: observation.EndpointPath,
		})
	}
	for _, snapshot := range snapshots {
		// Once an idle scope has decayed to unknown, do not write another
		// identical historical row every minute. Fresh observations add it back.
		if snapshot.HealthState == contracts.HealthUnknown {
			continue
		}
		add(contracts.ChannelHealthScope{
			ChannelID: snapshot.ChannelID, InstanceID: snapshot.InstanceID,
			PoolID: snapshot.PoolID, Model: snapshot.Model,
			Capability: snapshot.Capability, EndpointPath: snapshot.EndpointPath,
		})
	}
	out := make([]contracts.ChannelHealthScope, 0, len(byKey))
	for _, scope := range byKey {
		out = append(out, scope)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].InstanceID == out[j].InstanceID {
			return out[i].Model < out[j].Model
		}
		return out[i].InstanceID < out[j].InstanceID
	})
	return out
}

// RecomputePool recomputes snapshots for every channel in a pool. It is the
// cadence entry point a background ticker (or the health checker) calls so the
// whole pool's snapshots stay fresh for the strategy engine. Per-channel errors
// abort so a partial recompute never silently hides a store failure.
func (s *Service) RecomputePool(ctx context.Context, poolID string) ([]contracts.ChannelHealthSnapshot, error) {
	channels, err := s.store.ListUpstreamChannels(ctx, poolID)
	if err != nil {
		return nil, err
	}
	var out []contracts.ChannelHealthSnapshot
	for i := range channels {
		snaps, err := s.RecomputeChannel(ctx, channels[i].ID)
		if err != nil {
			return nil, err
		}
		out = append(out, snaps...)
	}
	return out, nil
}
