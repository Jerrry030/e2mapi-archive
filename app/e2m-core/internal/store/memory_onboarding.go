package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
)

func (s *MemoryStore) UpsertOnboardingWorkflow(ctx context.Context, input contracts.OnboardingWorkflow) (contracts.OnboardingWorkflow, error) {
	if err := ctx.Err(); err != nil {
		return contracts.OnboardingWorkflow{}, err
	}
	input = normalizeNewOnboardingWorkflow(input)
	if !validOnboardingWorkflow(input) {
		return contracts.OnboardingWorkflow{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for i, current := range s.onboardingFlows {
		if current.InstanceID != input.InstanceID || current.PoolID != input.PoolID {
			continue
		}
		if current.UserID != input.UserID {
			return contracts.OnboardingWorkflow{}, ErrConflict
		}
		connectorChanged := current.ConnectorID != input.ConnectorID
		desiredChanged := current.DesiredFingerprint != input.DesiredFingerprint
		// Upsert is called only for pools that discovery has just observed as
		// active. Wake a dormant row even when its desired state is unchanged.
		wakeDormant := current.Status == contracts.OnboardingDormantStatus
		if !connectorChanged && !desiredChanged && !wakeDormant {
			return copyOnboardingWorkflow(current), nil
		}
		current.ConnectorID = input.ConnectorID
		current.DesiredFingerprint = input.DesiredFingerprint
		if current.DesiredGeneration <= 0 {
			current.DesiredGeneration = 1
		} else {
			current.DesiredGeneration++
		}
		current.Stage = contracts.OnboardingCheckingGateway
		if connectorChanged {
			current.Stage = contracts.OnboardingWaitingConnector
		}
		current.Status = contracts.OnboardingPending
		current.Attempts = 0
		current.NextAttemptAt = nil
		current.LastErrorCode = ""
		current.KeyVersionSummary = nil
		current.LeaseOwner = ""
		current.LeaseUntil = nil
		current.Version++
		current.UpdatedAt = now
		s.onboardingFlows[i] = current
		return copyOnboardingWorkflow(current), nil
	}

	created := copyOnboardingWorkflow(input)
	created.DesiredGeneration = 1
	if created.ID == "" {
		created.ID = s.nextID("onboard")
	} else {
		for _, current := range s.onboardingFlows {
			if current.ID == created.ID {
				return contracts.OnboardingWorkflow{}, ErrDuplicate
			}
		}
	}
	created.Version = 1
	created.LeaseOwner = ""
	created.LeaseUntil = nil
	created.CreatedAt = now
	created.UpdatedAt = now
	s.onboardingFlows = append(s.onboardingFlows, created)
	return copyOnboardingWorkflow(created), nil
}

func (s *MemoryStore) GetOnboardingWorkflow(ctx context.Context, id string) (contracts.OnboardingWorkflow, error) {
	if err := ctx.Err(); err != nil {
		return contracts.OnboardingWorkflow{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, workflow := range s.onboardingFlows {
		if workflow.ID == id {
			return copyOnboardingWorkflow(workflow), nil
		}
	}
	return contracts.OnboardingWorkflow{}, ErrNotFound
}

func (s *MemoryStore) ListOnboardingWorkflows(ctx context.Context, filter contracts.OnboardingWorkflowFilter) ([]contracts.OnboardingWorkflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.OnboardingWorkflow, 0, len(s.onboardingFlows))
	for _, workflow := range s.onboardingFlows {
		if filter.Matches(workflow) {
			out = append(out, copyOnboardingWorkflow(workflow))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (s *MemoryStore) ClaimOnboardingWorkflow(ctx context.Context, workerID string, leaseDuration time.Duration) (contracts.OnboardingWorkflow, bool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.OnboardingWorkflow{}, false, err
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || leaseDuration <= 0 {
		return contracts.OnboardingWorkflow{}, false, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	index := -1
	for i := range s.onboardingFlows {
		candidate := s.onboardingFlows[i]
		if !onboardingClaimDue(candidate, now) {
			continue
		}
		if index < 0 || candidate.UpdatedAt.Before(s.onboardingFlows[index].UpdatedAt) ||
			(candidate.UpdatedAt.Equal(s.onboardingFlows[index].UpdatedAt) && candidate.ID < s.onboardingFlows[index].ID) {
			index = i
		}
	}
	if index < 0 {
		return contracts.OnboardingWorkflow{}, false, nil
	}
	claimed := s.onboardingFlows[index]
	leaseUntil := now.Add(leaseDuration)
	claimed.Status = contracts.OnboardingRunning
	claimed.Attempts++
	claimed.NextAttemptAt = nil
	claimed.LeaseOwner = workerID
	claimed.LeaseUntil = &leaseUntil
	claimed.Version++
	claimed.UpdatedAt = now
	s.onboardingFlows[index] = claimed
	return copyOnboardingWorkflow(claimed), true, nil
}

func (s *MemoryStore) RenewOnboardingWorkflowLease(
	ctx context.Context,
	id, workerID string,
	expectedVersion int64,
	leaseDuration time.Duration,
) (contracts.OnboardingWorkflow, error) {
	if err := ctx.Err(); err != nil {
		return contracts.OnboardingWorkflow{}, err
	}
	workerID = strings.TrimSpace(workerID)
	if strings.TrimSpace(id) == "" || workerID == "" || expectedVersion <= 0 || leaseDuration.Microseconds() <= 0 {
		return contracts.OnboardingWorkflow{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for i, current := range s.onboardingFlows {
		if current.ID != id {
			continue
		}
		if current.Status != contracts.OnboardingRunning || current.Version != expectedVersion ||
			current.LeaseOwner != workerID || current.LeaseUntil == nil || !current.LeaseUntil.After(now) {
			return contracts.OnboardingWorkflow{}, ErrConflict
		}
		leaseUntil := now.Add(leaseDuration)
		current.LeaseUntil = &leaseUntil
		current.Version++
		current.UpdatedAt = now
		s.onboardingFlows[i] = current
		return copyOnboardingWorkflow(current), nil
	}
	return contracts.OnboardingWorkflow{}, ErrNotFound
}

func (s *MemoryStore) ReleaseOnboardingWorkflowLease(
	ctx context.Context,
	id, workerID string,
	expectedVersion int64,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	workerID = strings.TrimSpace(workerID)
	if strings.TrimSpace(id) == "" || workerID == "" || expectedVersion <= 0 {
		return ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for i, current := range s.onboardingFlows {
		if current.ID != id {
			continue
		}
		if current.Status != contracts.OnboardingRunning || current.Version != expectedVersion ||
			current.LeaseOwner != workerID || current.LeaseUntil == nil || !current.LeaseUntil.After(now) {
			return ErrConflict
		}
		current.LeaseUntil = &now
		current.Version++
		current.UpdatedAt = now
		s.onboardingFlows[i] = current
		return nil
	}
	return ErrNotFound
}

func (s *MemoryStore) TransitionOnboardingWorkflow(ctx context.Context, input contracts.OnboardingWorkflow, expectedVersion int64) (contracts.OnboardingWorkflow, error) {
	if err := ctx.Err(); err != nil {
		return contracts.OnboardingWorkflow{}, err
	}
	input = normalizeNewOnboardingWorkflow(input)
	if expectedVersion <= 0 || !validOnboardingWorkflow(input) || strings.TrimSpace(input.LeaseOwner) == "" {
		return contracts.OnboardingWorkflow{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for i, current := range s.onboardingFlows {
		if current.ID != input.ID {
			continue
		}
		if current.Version != expectedVersion || current.Status != contracts.OnboardingRunning ||
			current.LeaseOwner != input.LeaseOwner || current.LeaseUntil == nil || !current.LeaseUntil.After(now) ||
			current.UserID != input.UserID || current.InstanceID != input.InstanceID ||
			current.PoolID != input.PoolID || current.ConnectorID != input.ConnectorID {
			return contracts.OnboardingWorkflow{}, ErrConflict
		}
		updated := copyOnboardingWorkflow(input)
		updated.Attempts = current.Attempts
		updated.Version = current.Version + 1
		updated.CreatedAt = current.CreatedAt
		updated.UpdatedAt = now
		if updated.Status == contracts.OnboardingRunning {
			updated.LeaseOwner = current.LeaseOwner
			updated.LeaseUntil = current.LeaseUntil
			updated.NextAttemptAt = nil
		} else {
			updated.LeaseOwner = ""
			updated.LeaseUntil = nil
		}
		s.onboardingFlows[i] = updated
		return copyOnboardingWorkflow(updated), nil
	}
	return contracts.OnboardingWorkflow{}, ErrNotFound
}
