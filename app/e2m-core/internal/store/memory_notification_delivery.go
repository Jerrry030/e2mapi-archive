package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
)

func normalizeNotificationDelivery(input contracts.NotificationDelivery) (contracts.NotificationDelivery, error) {
	d := input
	if d.UserID <= 0 || strings.TrimSpace(d.RouteID) == "" || strings.TrimSpace(d.RouteName) == "" {
		return contracts.NotificationDelivery{}, ErrInvalid
	}
	if d.Kind != contracts.NotificationDeliveryKindEvent && d.Kind != contracts.NotificationDeliveryKindTest {
		return contracts.NotificationDelivery{}, ErrInvalid
	}
	if d.Status == "" {
		d.Status = contracts.NotificationDeliveryPending
	}
	if !d.Status.Valid() {
		return contracts.NotificationDelivery{}, ErrInvalid
	}
	if d.MaxAttempts <= 0 {
		d.MaxAttempts = 5
	}
	if d.NextAttemptAt.IsZero() {
		d.NextAttemptAt = time.Now().UTC()
	}
	return d, nil
}

func (s *MemoryStore) CreateNotificationDelivery(ctx context.Context, input contracts.NotificationDelivery) (contracts.NotificationDelivery, error) {
	if err := ctx.Err(); err != nil {
		return contracts.NotificationDelivery{}, err
	}
	d, err := normalizeNotificationDelivery(input)
	if err != nil {
		return contracts.NotificationDelivery{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	if d.ID == "" {
		d.ID = s.nextID("delivery")
	}
	if input.NextAttemptAt.IsZero() {
		d.NextAttemptAt = now
	}
	d.CreatedAt, d.UpdatedAt = now, now
	s.notificationDeliveries = append(s.notificationDeliveries, d)
	return d, nil
}

func (s *MemoryStore) GetNotificationDelivery(ctx context.Context, id string) (contracts.NotificationDelivery, error) {
	if err := ctx.Err(); err != nil {
		return contracts.NotificationDelivery{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.notificationDeliveries {
		if d.ID == id {
			return d, nil
		}
	}
	return contracts.NotificationDelivery{}, ErrNotFound
}

func (s *MemoryStore) ListNotificationDeliveries(ctx context.Context, filter contracts.NotificationDeliveryFilter) ([]contracts.NotificationDelivery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	out := make([]contracts.NotificationDelivery, 0, len(s.notificationDeliveries))
	for _, d := range s.notificationDeliveries {
		if filter.UserID != 0 && d.UserID != filter.UserID {
			continue
		}
		if filter.RouteID != "" && d.RouteID != filter.RouteID {
			continue
		}
		if filter.TargetRef != "" && d.TargetRef != filter.TargetRef {
			continue
		}
		if filter.Status != "" && d.Status != filter.Status {
			continue
		}
		if filter.Channel != "" && d.Channel != filter.Channel {
			continue
		}
		out = append(out, d)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) ClaimNotificationDelivery(ctx context.Context, workerID string, leaseDuration time.Duration) (contracts.NotificationDelivery, bool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.NotificationDelivery{}, false, err
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || leaseDuration <= 0 {
		return contracts.NotificationDelivery{}, false, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	best := -1
	for i, d := range s.notificationDeliveries {
		if d.Status == contracts.NotificationDeliveryProcessing && d.LeaseUntil != nil && !d.LeaseUntil.After(now) && d.Attempts >= d.MaxAttempts {
			d.Status = contracts.NotificationDeliveryFailed
			d.LastErrorCode = "lease_expired"
			d.LastErrorMessage = "notification delivery lease expired after the final attempt"
			d.LeaseOwner, d.LeaseUntil = "", nil
			d.UpdatedAt = now
			s.notificationDeliveries[i] = d
			continue
		}
		eligible := d.Attempts < d.MaxAttempts &&
			(d.Status == contracts.NotificationDeliveryPending || d.Status == contracts.NotificationDeliveryRetrying) &&
			!d.NextAttemptAt.After(now)
		if d.Status == contracts.NotificationDeliveryProcessing && d.LeaseUntil != nil && !d.LeaseUntil.After(now) {
			eligible = true
		}
		if !eligible {
			continue
		}
		if best < 0 || d.NextAttemptAt.Before(s.notificationDeliveries[best].NextAttemptAt) {
			best = i
		}
	}
	if best < 0 {
		return contracts.NotificationDelivery{}, false, nil
	}
	d := s.notificationDeliveries[best]
	d.Status = contracts.NotificationDeliveryProcessing
	d.Attempts++
	d.LeaseVersion++
	d.LeaseOwner = workerID
	leaseUntil := now.Add(leaseDuration)
	d.LeaseUntil = &leaseUntil
	d.UpdatedAt = now
	s.notificationDeliveries[best] = d
	return d, true, nil
}

func (s *MemoryStore) CompleteNotificationDelivery(ctx context.Context, id, workerID string, expectedLeaseVersion int64, succeeded bool, errorCode, errorMessage string, nextAttemptAt time.Time) (contracts.NotificationDelivery, error) {
	if err := ctx.Err(); err != nil {
		return contracts.NotificationDelivery{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	for i, d := range s.notificationDeliveries {
		if d.ID != id {
			continue
		}
		if d.Status != contracts.NotificationDeliveryProcessing || d.LeaseOwner != workerID || d.LeaseVersion != expectedLeaseVersion || d.LeaseUntil == nil || !d.LeaseUntil.After(now) {
			return contracts.NotificationDelivery{}, ErrConflict
		}
		d.LeaseOwner, d.LeaseUntil = "", nil
		d.LastErrorCode = strings.TrimSpace(errorCode)
		d.LastErrorMessage = strings.TrimSpace(errorMessage)
		if succeeded {
			d.Status = contracts.NotificationDeliverySucceeded
			d.LastErrorCode, d.LastErrorMessage = "", ""
			d.SentAt = &now
		} else if d.Attempts >= d.MaxAttempts || nextAttemptAt.IsZero() {
			d.Status = contracts.NotificationDeliveryFailed
		} else {
			d.Status = contracts.NotificationDeliveryRetrying
			d.NextAttemptAt = nextAttemptAt.UTC()
		}
		d.UpdatedAt = now
		s.notificationDeliveries[i] = d
		return d, nil
	}
	return contracts.NotificationDelivery{}, ErrNotFound
}

func (s *MemoryStore) RetryNotificationDelivery(ctx context.Context, id string) (contracts.NotificationDelivery, error) {
	if err := ctx.Err(); err != nil {
		return contracts.NotificationDelivery{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	for _, existing := range s.notificationDeliveries {
		if existing.RetriedFromID == id {
			return contracts.NotificationDelivery{}, ErrConflict
		}
	}
	for _, d := range s.notificationDeliveries {
		if d.ID != id {
			continue
		}
		if d.Status != contracts.NotificationDeliveryFailed {
			return contracts.NotificationDelivery{}, ErrConflict
		}
		d.ID = s.nextID("delivery")
		d.RetriedFromID = id
		d.Status = contracts.NotificationDeliveryPending
		d.Attempts = 0
		d.NextAttemptAt = now
		d.LastErrorCode, d.LastErrorMessage = "", ""
		d.LeaseOwner, d.LeaseUntil, d.SentAt = "", nil, nil
		d.LeaseVersion = 0
		d.UpdatedAt = now
		d.CreatedAt = now
		s.notificationDeliveries = append(s.notificationDeliveries, d)
		return d, nil
	}
	return contracts.NotificationDelivery{}, ErrNotFound
}
