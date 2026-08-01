package store

import (
	"context"
	"reflect"
	"sort"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/upstreamrecommendation"
)

// UpstreamRecommendationStore is deliberately narrower than Store. Advisory
// readers and UI-14 executors can depend on recommendation persistence without
// inheriting unrelated control-plane mutations.
type UpstreamRecommendationStore interface {
	CreateUpstreamRecommendation(context.Context, contracts.UpstreamRecommendation) (contracts.UpstreamRecommendation, error)
	GetUpstreamRecommendation(context.Context, int64, string) (contracts.UpstreamRecommendation, error)
	ListUpstreamRecommendations(context.Context, contracts.UpstreamRecommendationFilter) ([]contracts.UpstreamRecommendation, error)
	TransitionUpstreamRecommendation(context.Context, contracts.UpstreamRecommendation, contracts.UpstreamRecommendationStatus) (contracts.UpstreamRecommendation, error)
}

var (
	_ UpstreamRecommendationStore = (*MemoryStore)(nil)
	_ UpstreamRecommendationStore = (*PostgresStore)(nil)
)

func validRecommendationStatusFilter(status contracts.UpstreamRecommendationStatus) bool {
	return status == "" || contracts.IsUpstreamRecommendationStatus(status)
}

func cloneUpstreamRecommendation(value contracts.UpstreamRecommendation) contracts.UpstreamRecommendation {
	value.AffectedPlanIDs = append([]string(nil), value.AffectedPlanIDs...)
	value.AffectedDownstreams = append([]string(nil), value.AffectedDownstreams...)
	value.EvidenceIDs = append([]string(nil), value.EvidenceIDs...)
	value.Constraints = append([]contracts.UpstreamRecommendationConstraint(nil), value.Constraints...)
	for index := range value.Constraints {
		value.Constraints[index].EvidenceIDs = append([]string(nil), value.Constraints[index].EvidenceIDs...)
	}
	return value
}

func recommendationImmutableEqual(left, right contracts.UpstreamRecommendation) bool {
	// time.Time's location and monotonic metadata are representation details,
	// not part of the immutable recommendation identity. PostgreSQL may decode
	// the same timestamptz instant using the process-local location, so compare
	// instants before using DeepEqual for the remaining fields.
	if !left.CreatedAt.Equal(right.CreatedAt) || !left.ExpiresAt.Equal(right.ExpiresAt) {
		return false
	}
	left.Status, right.Status = "", ""
	left.DryRunID, right.DryRunID = "", ""
	left.CreatedAt, right.CreatedAt = left.CreatedAt.UTC(), right.CreatedAt.UTC()
	left.ExpiresAt, right.ExpiresAt = left.ExpiresAt.UTC(), right.ExpiresAt.UTC()
	return reflect.DeepEqual(left, right)
}

func (s *MemoryStore) CreateUpstreamRecommendation(ctx context.Context, input contracts.UpstreamRecommendation) (contracts.UpstreamRecommendation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	input = cloneUpstreamRecommendation(input)
	if err := upstreamrecommendation.Validate(input); err != nil {
		return contracts.UpstreamRecommendation{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.upstreamRecommendations {
		if existing.UserID == input.UserID && existing.ID == input.ID && existing.Fingerprint != input.Fingerprint {
			return contracts.UpstreamRecommendation{}, ErrConflict
		}
		if existing.UserID == input.UserID && existing.Fingerprint == input.Fingerprint {
			return cloneUpstreamRecommendation(existing), nil
		}
	}
	s.upstreamRecommendations = append(s.upstreamRecommendations, input)
	return cloneUpstreamRecommendation(input), nil
}

func (s *MemoryStore) GetUpstreamRecommendation(ctx context.Context, userID int64, id string) (contracts.UpstreamRecommendation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	if userID <= 0 || strings.TrimSpace(id) == "" {
		return contracts.UpstreamRecommendation{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getUpstreamRecommendationLocked(userID, id)
}

func (s *MemoryStore) getUpstreamRecommendationLocked(userID int64, id string) (contracts.UpstreamRecommendation, error) {
	for _, value := range s.upstreamRecommendations {
		if value.UserID == userID && value.ID == id {
			return cloneUpstreamRecommendation(value), nil
		}
	}
	return contracts.UpstreamRecommendation{}, ErrNotFound
}

func (s *MemoryStore) ListUpstreamRecommendations(ctx context.Context, filter contracts.UpstreamRecommendationFilter) ([]contracts.UpstreamRecommendation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filter.UserID <= 0 || !validRecommendationStatusFilter(filter.Status) {
		return nil, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]contracts.UpstreamRecommendation, 0)
	for _, value := range s.upstreamRecommendations {
		if value.UserID == filter.UserID && (filter.Status == "" || value.Status == filter.Status) {
			values = append(values, cloneUpstreamRecommendation(value))
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID > values[j].ID
		}
		return values[i].CreatedAt.After(values[j].CreatedAt)
	})
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	if len(values) > limit {
		values = values[:limit]
	}
	return values, nil
}

func (s *MemoryStore) TransitionUpstreamRecommendation(ctx context.Context, next contracts.UpstreamRecommendation, expectedStatus contracts.UpstreamRecommendationStatus) (contracts.UpstreamRecommendation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	next = cloneUpstreamRecommendation(next)
	if err := upstreamrecommendation.Validate(next); err != nil || !contracts.IsUpstreamRecommendationStatus(expectedStatus) {
		return contracts.UpstreamRecommendation{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, current := range s.upstreamRecommendations {
		if current.UserID != next.UserID || current.ID != next.ID {
			continue
		}
		if current.Status != expectedStatus || !recommendationImmutableEqual(current, next) {
			return contracts.UpstreamRecommendation{}, ErrConflict
		}
		s.upstreamRecommendations[index].Status = next.Status
		s.upstreamRecommendations[index].DryRunID = next.DryRunID
		return cloneUpstreamRecommendation(s.upstreamRecommendations[index]), nil
	}
	return contracts.UpstreamRecommendation{}, ErrNotFound
}
