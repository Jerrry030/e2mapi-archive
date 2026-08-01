package store

import (
	"context"

	"e2m.local/contracts"
)

func (s *MemoryStore) GetInstanceMonitorPolicy(ctx context.Context, instanceID string) (contracts.InstanceMonitorPolicy, error) {
	if err := ctx.Err(); err != nil {
		return contracts.InstanceMonitorPolicy{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, policy := range s.monitorPolicies {
		if policy.InstanceID == instanceID {
			return policy, nil
		}
	}
	for _, instance := range s.instances {
		if instance.ID == instanceID {
			return contracts.DefaultInstanceMonitorPolicy(instance.ID, instance.UserID), nil
		}
	}
	return contracts.InstanceMonitorPolicy{}, ErrNotFound
}

func (s *MemoryStore) UpsertInstanceMonitorPolicy(ctx context.Context, input contracts.InstanceMonitorPolicy) (contracts.InstanceMonitorPolicy, error) {
	if err := ctx.Err(); err != nil {
		return contracts.InstanceMonitorPolicy{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var instance *contracts.Instance
	for i := range s.instances {
		if s.instances[i].ID == input.InstanceID {
			instance = &s.instances[i]
			break
		}
	}
	if instance == nil {
		return contracts.InstanceMonitorPolicy{}, ErrNotFound
	}
	if input.UserID != 0 && input.UserID != instance.UserID {
		return contracts.InstanceMonitorPolicy{}, ErrConflict
	}
	input.UserID = instance.UserID
	updatedAt := s.now().UTC()
	input.UpdatedAt = &updatedAt
	for i := range s.monitorPolicies {
		if s.monitorPolicies[i].InstanceID == input.InstanceID {
			s.monitorPolicies[i] = input
			return input, nil
		}
	}
	s.monitorPolicies = append(s.monitorPolicies, input)
	return input, nil
}
