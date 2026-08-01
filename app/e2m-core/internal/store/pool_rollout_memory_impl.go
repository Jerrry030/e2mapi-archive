package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
)

func normalizePoolRolloutTarget(input contracts.PoolRolloutTarget) contracts.PoolRolloutTarget {
	input.PoolID = strings.TrimSpace(input.PoolID)
	input.InstanceID = strings.TrimSpace(input.InstanceID)
	input.Note = strings.TrimSpace(input.Note)
	if input.Rollout == "" {
		input.Rollout = contracts.RolloutImmediate
	}
	if input.Scope == contracts.PoolRolloutScopeUser {
		input.InstanceID = ""
	}
	return input
}

func validPoolRolloutTarget(input contracts.PoolRolloutTarget) bool {
	if input.PoolID == "" || !input.Scope.Valid() || input.UserID <= 0 || input.RolloutBatchSize < 0 || input.RolloutCanaryCount < 0 {
		return false
	}
	switch input.Rollout {
	case contracts.RolloutImmediate, contracts.RolloutCanary, contracts.RolloutBatched:
	default:
		return false
	}
	if input.Scope == contracts.PoolRolloutScopeUser {
		return input.InstanceID == ""
	}
	return input.Scope == contracts.PoolRolloutScopeInstance && input.InstanceID != ""
}

func poolRolloutTargetMatches(target contracts.PoolRolloutTarget, scope contracts.PoolRolloutScope, userID int64, instanceID string) bool {
	if target.Scope != scope || target.UserID != userID {
		return false
	}
	return scope == contracts.PoolRolloutScopeUser || target.InstanceID == instanceID
}

func rolloutResolution(target contracts.PoolRolloutTarget, userID int64, instanceID string) contracts.PoolRolloutResolution {
	return contracts.PoolRolloutResolution{
		PoolID: target.PoolID, UserID: userID, InstanceID: instanceID,
		Enabled: target.Enabled, Source: target.Scope, TargetID: target.ID,
		TargetUpdatedAt: timePointer(target.UpdatedAt), DesiredUpdatedAt: timePointer(target.UpdatedAt),
		Rollout: target.Rollout, RolloutBatchSize: target.RolloutBatchSize,
		RolloutCanaryCount: target.RolloutCanaryCount,
	}
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copyValue := value
	return &copyValue
}

// PoolRolloutOperationFingerprint identifies the complete effective rollout
// decision that one durable operation is allowed to execute. The worker
// recomputes it after claim and at every side-effect fence so an in-place
// policy update cannot make an older operation apply the newer policy.
func PoolRolloutOperationFingerprint(resolution contracts.PoolRolloutResolution, action contracts.PoolRolloutOperationAction, planID string) string {
	updated := "default"
	if resolution.DesiredUpdatedAt != nil {
		updated = resolution.DesiredUpdatedAt.UTC().Format(time.RFC3339Nano)
	} else if resolution.TargetUpdatedAt != nil {
		updated = resolution.TargetUpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	raw := strings.Join([]string{
		resolution.PoolID, resolution.InstanceID, resolution.TargetID, updated,
		string(action), planID, string(resolution.Rollout),
		strconv.Itoa(resolution.RolloutBatchSize), strconv.Itoa(resolution.RolloutCanaryCount),
	}, "\x00")
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func (s *MemoryStore) UpsertPoolRolloutTarget(ctx context.Context, input contracts.PoolRolloutTarget) (contracts.PoolRolloutTarget, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRolloutTarget{}, err
	}
	input = normalizePoolRolloutTarget(input)
	if !validPoolRolloutTarget(input) {
		return contracts.PoolRolloutTarget{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	poolExists := false
	for _, pool := range s.upstreamPools {
		poolExists = poolExists || pool.ID == input.PoolID
	}
	if !poolExists {
		return contracts.PoolRolloutTarget{}, ErrNotFound
	}
	if input.Scope == contracts.PoolRolloutScopeInstance {
		instanceExists := false
		for _, instance := range s.instances {
			if instance.ID == input.InstanceID && instance.UserID == input.UserID {
				instanceExists = true
				break
			}
		}
		if !instanceExists {
			return contracts.PoolRolloutTarget{}, ErrInvalid
		}
	}
	now := s.now().UTC()
	for i, target := range s.poolRolloutTargets {
		if target.PoolID == input.PoolID && poolRolloutTargetMatches(target, input.Scope, input.UserID, input.InstanceID) {
			input.ID = target.ID
			input.CreatedAt = target.CreatedAt
			input.UpdatedAt = now
			s.poolRolloutTargets[i] = input
			return input, nil
		}
	}
	input.ID = s.nextID("rollout")
	input.CreatedAt, input.UpdatedAt = now, now
	s.poolRolloutTargets = append(s.poolRolloutTargets, input)
	return input, nil
}

func (s *MemoryStore) DeletePoolRolloutTarget(ctx context.Context, poolID string, scope contracts.PoolRolloutScope, userID int64, instanceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	poolID, instanceID = strings.TrimSpace(poolID), strings.TrimSpace(instanceID)
	if poolID == "" || !scope.Valid() || userID <= 0 {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, target := range s.poolRolloutTargets {
		if target.PoolID == poolID && poolRolloutTargetMatches(target, scope, userID, instanceID) {
			s.poolRolloutTargets = append(s.poolRolloutTargets[:i], s.poolRolloutTargets[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) ListPoolRolloutTargets(ctx context.Context, poolID string) ([]contracts.PoolRolloutTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.PoolRolloutTarget, 0)
	for _, target := range s.poolRolloutTargets {
		if poolID == "" || target.PoolID == poolID {
			out = append(out, target)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		if out[i].UserID != out[j].UserID {
			return out[i].UserID < out[j].UserID
		}
		return out[i].InstanceID < out[j].InstanceID
	})
	return out, nil
}

func (s *MemoryStore) ResolvePoolRollout(ctx context.Context, poolID string, userID int64, instanceID string) (contracts.PoolRolloutResolution, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRolloutResolution{}, err
	}
	poolID, instanceID = strings.TrimSpace(poolID), strings.TrimSpace(instanceID)
	if poolID == "" || userID <= 0 || instanceID == "" {
		return contracts.PoolRolloutResolution{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := contracts.PoolRolloutResolution{
		PoolID: poolID, UserID: userID, InstanceID: instanceID, Enabled: false, Rollout: contracts.RolloutImmediate,
	}
	eligible := false
	for _, pool := range s.upstreamPools {
		if pool.ID == poolID {
			eligible = pool.Status == contracts.UpstreamPoolActive
			break
		}
	}
	if !eligible {
		return result, nil
	}
	var userDeactivationRequestedAt *time.Time
	for _, user := range s.users {
		if user.ID == userID {
			eligible = user.Enabled && userHasRole(user.Roles, contracts.UserRoleClient)
			userDeactivationRequestedAt = user.DeactivationRequestedAt
			break
		}
	}
	for _, target := range s.poolRolloutTargets {
		if target.PoolID == poolID && target.Scope == contracts.PoolRolloutScopeUser && target.UserID == userID {
			result = rolloutResolution(target, userID, instanceID)
			result.Enabled = result.Enabled && eligible
			setPoolRolloutDesiredUpdatedAt(&result, userDeactivationRequestedAt)
		}
	}
	for _, target := range s.poolRolloutTargets {
		if target.PoolID == poolID && target.Scope == contracts.PoolRolloutScopeInstance && target.UserID == userID && target.InstanceID == instanceID {
			result = rolloutResolution(target, userID, instanceID)
			result.Enabled = result.Enabled && eligible
			setPoolRolloutDesiredUpdatedAt(&result, userDeactivationRequestedAt)
			return result, nil
		}
	}
	setPoolRolloutDesiredUpdatedAt(&result, userDeactivationRequestedAt)
	return result, nil
}

func setPoolRolloutDesiredUpdatedAt(result *contracts.PoolRolloutResolution, candidate *time.Time) {
	if candidate != nil && (result.DesiredUpdatedAt == nil || candidate.After(*result.DesiredUpdatedAt)) {
		value := *candidate
		result.DesiredUpdatedAt = &value
	}
}

func (s *MemoryStore) EnsurePoolRolloutOperations(ctx context.Context, poolID string) ([]contracts.PoolRolloutOperation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	poolID = strings.TrimSpace(poolID)
	if poolID == "" {
		return nil, ErrInvalid
	}
	pool, err := s.GetUpstreamPool(ctx, poolID)
	if err != nil {
		return nil, err
	}
	instances, err := s.ListInstances(ctx, 0)
	if err != nil {
		return nil, err
	}
	plans, err := s.ListRoutePlans(ctx, 0)
	if err != nil {
		return nil, err
	}
	planByInstance := make(map[string]contracts.RoutePlan)
	for _, plan := range plans {
		if plan.PoolID == poolID {
			planByInstance[plan.InstanceID] = plan
		}
	}
	created := make([]contracts.PoolRolloutOperation, 0)
	for _, instance := range instances {
		resolution, resolveErr := s.ResolvePoolRollout(ctx, poolID, instance.UserID, instance.ID)
		if resolveErr != nil {
			return nil, resolveErr
		}
		plan := planByInstance[instance.ID]
		action := contracts.PoolRolloutOperationPublish
		planID := plan.ID
		if pool.Status != contracts.UpstreamPoolActive || !resolution.Enabled {
			if plan.ID == "" {
				continue
			}
			if plan.Status == contracts.RoutePlanSuspended {
				bindings, bindingErr := s.ListPublishedBindings(ctx, plan.ID)
				if bindingErr != nil {
					return nil, bindingErr
				}
				allRevoked := true
				for _, binding := range bindings {
					allRevoked = allRevoked && binding.State == contracts.BindingRevoked
				}
				if allRevoked {
					continue
				}
			}
			action = contracts.PoolRolloutOperationDrain
		} else if plan.ID == "" {
			// Onboarding owns plan creation and the first publish. Do not create a
			// permanently retryable operation while that durable workflow is still
			// preparing the plan.
			continue
		} else if plan.Labels["managed_by"] == "e2m-onboarding" {
			// Onboarding is the sole publish owner for managed plans, including
			// later rollout-policy changes after the first ready generation. The
			// pool-rollout worker retains only the disabled/drain path.
			continue
		}
		if action == contracts.PoolRolloutOperationPublish && plan.ID != "" && plan.Status == contracts.RoutePlanPublished &&
			plan.Rollout == resolution.Rollout && plan.RolloutBatchSize == resolution.RolloutBatchSize &&
			plan.RolloutCanaryCount == resolution.RolloutCanaryCount {
			continue
		}
		fingerprint := PoolRolloutOperationFingerprint(resolution, action, planID)
		s.mu.Lock()
		exists := false
		for _, operation := range s.poolRolloutOps {
			if operation.DesiredFingerprint == fingerprint {
				exists = true
				break
			}
		}
		if exists {
			if action == contracts.PoolRolloutOperationDrain {
				now := s.now().UTC()
				for i := range s.poolRolloutOps {
					operation := &s.poolRolloutOps[i]
					if operation.DesiredFingerprint != fingerprint || operation.Status != contracts.PoolRolloutOperationFailed {
						continue
					}
					operation.Status = contracts.PoolRolloutOperationPending
					operation.LastError = ""
					operation.Version++
					operation.LeaseOwner = ""
					operation.LeaseUntil = nil
					operation.UpdatedAt = now
				}
			}
			s.mu.Unlock()
			continue
		}
		now := s.now().UTC()
		if action == contracts.PoolRolloutOperationPublish {
			active := false
			for _, currentPool := range s.upstreamPools {
				active = active || currentPool.ID == poolID && currentPool.Status == contracts.UpstreamPoolActive
			}
			if !active {
				s.mu.Unlock()
				continue
			}
		}
		operation := contracts.PoolRolloutOperation{
			ID: s.nextID("rolloutop"), PoolID: poolID, UserID: instance.UserID,
			InstanceID: instance.ID, PlanID: planID, TargetID: resolution.TargetID,
			Action: action, Status: contracts.PoolRolloutOperationPending,
			DesiredFingerprint: fingerprint, Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		for i := range s.poolRolloutOps {
			current := &s.poolRolloutOps[i]
			if current.PoolID == poolID && current.InstanceID == instance.ID &&
				(current.Status == contracts.PoolRolloutOperationPending || current.Status == contracts.PoolRolloutOperationFailed) {
				current.Status = contracts.PoolRolloutOperationSuperseded
				current.Version++
				current.UpdatedAt = now
			}
		}
		s.poolRolloutOps = append(s.poolRolloutOps, operation)
		s.mu.Unlock()
		created = append(created, operation)
	}
	return created, nil
}

// GuardPoolRolloutPublish atomically renews a claimed publish only while its
// pool remains active. Pool retirement supersedes the row in the same lock
// that moves the pool to maintenance, so a claimed worker cannot mutate its
// plan or gateway after retirement has taken ownership.
func (s *MemoryStore) GuardPoolRolloutPublish(ctx context.Context, id, workerID string, expectedVersion int64, lease time.Duration) (contracts.PoolRolloutOperation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRolloutOperation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if lease <= 0 {
		return contracts.PoolRolloutOperation{}, ErrInvalid
	}
	for i, operation := range s.poolRolloutOps {
		if operation.ID != id {
			continue
		}
		if operation.Action != contracts.PoolRolloutOperationPublish || operation.Status != contracts.PoolRolloutOperationRunning ||
			operation.Version != expectedVersion || operation.LeaseOwner != workerID || operation.LeaseUntil == nil ||
			!operation.LeaseUntil.After(s.now().UTC()) {
			return contracts.PoolRolloutOperation{}, ErrConflict
		}
		for _, pool := range s.upstreamPools {
			if pool.ID == operation.PoolID {
				if pool.Status != contracts.UpstreamPoolActive {
					return contracts.PoolRolloutOperation{}, ErrConflict
				}
				leaseUntil := s.now().UTC().Add(lease)
				operation.Version++
				operation.LeaseUntil = &leaseUntil
				operation.UpdatedAt = s.now().UTC()
				s.poolRolloutOps[i] = operation
				return operation, nil
			}
		}
		return contracts.PoolRolloutOperation{}, ErrNotFound
	}
	return contracts.PoolRolloutOperation{}, ErrNotFound
}

func (s *MemoryStore) ClaimPoolRolloutOperation(ctx context.Context, workerID string, lease time.Duration) (contracts.PoolRolloutOperation, bool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRolloutOperation{}, false, err
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || lease <= 0 {
		return contracts.PoolRolloutOperation{}, false, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	index := -1
	for i, operation := range s.poolRolloutOps {
		due := operation.Status == contracts.PoolRolloutOperationPending || operation.Status == contracts.PoolRolloutOperationFailed ||
			(operation.Status == contracts.PoolRolloutOperationRunning && (operation.LeaseUntil == nil || !operation.LeaseUntil.After(now)))
		if due && operation.Action == contracts.PoolRolloutOperationPublish {
			active := false
			for _, pool := range s.upstreamPools {
				active = active || pool.ID == operation.PoolID && pool.Status == contracts.UpstreamPoolActive
			}
			due = active
		}
		if due && (index < 0 || operation.UpdatedAt.Before(s.poolRolloutOps[index].UpdatedAt)) {
			index = i
		}
	}
	if index < 0 {
		return contracts.PoolRolloutOperation{}, false, nil
	}
	operation := s.poolRolloutOps[index]
	leaseUntil := now.Add(lease)
	operation.Status = contracts.PoolRolloutOperationRunning
	operation.Attempts++
	operation.Version++
	operation.LeaseOwner = workerID
	operation.LeaseUntil = &leaseUntil
	operation.UpdatedAt = now
	s.poolRolloutOps[index] = operation
	return operation, true, nil
}

func (s *MemoryStore) GetPoolRolloutOperation(ctx context.Context, id string) (contracts.PoolRolloutOperation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRolloutOperation{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, operation := range s.poolRolloutOps {
		if operation.ID == id {
			return operation, nil
		}
	}
	return contracts.PoolRolloutOperation{}, ErrNotFound
}

func (s *MemoryStore) RenewPoolRolloutOperation(ctx context.Context, id, workerID string, expectedVersion int64, lease time.Duration) (contracts.PoolRolloutOperation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRolloutOperation{}, err
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(workerID) == "" || expectedVersion <= 0 || lease <= 0 {
		return contracts.PoolRolloutOperation{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	for i, operation := range s.poolRolloutOps {
		if operation.ID != id {
			continue
		}
		if operation.Status != contracts.PoolRolloutOperationRunning || operation.Version != expectedVersion ||
			operation.LeaseOwner != workerID || operation.LeaseUntil == nil || !operation.LeaseUntil.After(now) {
			return contracts.PoolRolloutOperation{}, ErrConflict
		}
		leaseUntil := now.Add(lease)
		operation.LeaseUntil = &leaseUntil
		operation.Version++
		operation.UpdatedAt = now
		s.poolRolloutOps[i] = operation
		return operation, nil
	}
	return contracts.PoolRolloutOperation{}, ErrNotFound
}

func (s *MemoryStore) CompletePoolRolloutOperation(ctx context.Context, id, workerID string, expectedVersion int64, status contracts.PoolRolloutOperationStatus, lastError string) (contracts.PoolRolloutOperation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRolloutOperation{}, err
	}
	if status != contracts.PoolRolloutOperationSucceeded && status != contracts.PoolRolloutOperationFailed && status != contracts.PoolRolloutOperationSuperseded {
		return contracts.PoolRolloutOperation{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, operation := range s.poolRolloutOps {
		if operation.ID != id {
			continue
		}
		if operation.Status != contracts.PoolRolloutOperationRunning || operation.Version != expectedVersion || operation.LeaseOwner != workerID ||
			operation.LeaseUntil == nil || !operation.LeaseUntil.After(s.now().UTC()) {
			return contracts.PoolRolloutOperation{}, ErrConflict
		}
		operation.Status = status
		operation.LastError = strings.TrimSpace(lastError)
		operation.Version++
		operation.LeaseOwner = ""
		operation.LeaseUntil = nil
		operation.UpdatedAt = s.now().UTC()
		s.poolRolloutOps[i] = operation
		return operation, nil
	}
	return contracts.PoolRolloutOperation{}, ErrNotFound
}

func (s *MemoryStore) ListPoolRolloutOperations(ctx context.Context, poolID string) ([]contracts.PoolRolloutOperation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.PoolRolloutOperation, 0)
	for _, operation := range s.poolRolloutOps {
		if poolID == "" || operation.PoolID == poolID {
			out = append(out, operation)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}
