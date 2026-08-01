package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
)

func hybridBindingMapKey(instanceID string, class contracts.ResourceClass) string {
	return strings.TrimSpace(instanceID) + ":" + string(class)
}

func copyHybridPercentMap(input map[contracts.ResourceClass]int) map[contracts.ResourceClass]int {
	if input == nil {
		return nil
	}
	out := make(map[contracts.ResourceClass]int, len(input))
	for class, value := range input {
		out[class] = value
	}
	return out
}

func copyHybridGatewayBinding(input contracts.HybridGatewayBinding) contracts.HybridGatewayBinding {
	return input
}

func copyHybridRoutingExecution(input contracts.HybridRoutingExecution) contracts.HybridRoutingExecution {
	out := input
	out.Target = copyHybridPercentMap(input.Target)
	out.Effective = copyHybridPercentMap(input.Effective)
	out.Actual = copyHybridPercentMap(input.Actual)
	out.DesiredWeights = append([]contracts.HybridAccountWeight(nil), input.DesiredWeights...)
	out.AdjustmentCodes = append([]string(nil), input.AdjustmentCodes...)
	out.LeaseUntil = copyTimePointer(input.LeaseUntil)
	out.CompletedAt = copyTimePointer(input.CompletedAt)
	return out
}

func (s *MemoryStore) GetHybridGatewayBinding(ctx context.Context, userID int64, instanceID string, class contracts.ResourceClass) (contracts.HybridGatewayBinding, error) {
	if err := ctx.Err(); err != nil {
		return contracts.HybridGatewayBinding{}, err
	}
	if userID <= 0 || strings.TrimSpace(instanceID) == "" || !class.IsPlatformSupply() {
		return contracts.HybridGatewayBinding{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	binding, ok := s.hybridGatewayBindings[hybridBindingMapKey(instanceID, class)]
	if !ok || binding.UserID != userID {
		return contracts.HybridGatewayBinding{}, ErrNotFound
	}
	return copyHybridGatewayBinding(binding), nil
}

func (s *MemoryStore) ListHybridGatewayBindings(ctx context.Context, userID int64, instanceID string) ([]contracts.HybridGatewayBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if userID <= 0 || strings.TrimSpace(instanceID) == "" {
		return nil, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.HybridGatewayBinding, 0, 2)
	for _, binding := range s.hybridGatewayBindings {
		if binding.UserID == userID && binding.InstanceID == instanceID {
			out = append(out, copyHybridGatewayBinding(binding))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ResourceClass < out[j].ResourceClass })
	return out, nil
}

func (s *MemoryStore) UpsertHybridGatewayBinding(ctx context.Context, input contracts.HybridGatewayBinding, expectedVersion int64) (contracts.HybridGatewayBinding, error) {
	if err := ctx.Err(); err != nil {
		return contracts.HybridGatewayBinding{}, err
	}
	input.InstanceID = strings.TrimSpace(input.InstanceID)
	input.ConnectorID = strings.TrimSpace(input.ConnectorID)
	input.CredentialBindingID = strings.TrimSpace(input.CredentialBindingID)
	input.RemoteAccountID = strings.TrimSpace(input.RemoteAccountID)
	input.VirtualKeyID = strings.TrimSpace(input.VirtualKeyID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.ID == "" {
		input.ID = s.nextID("hbind")
	}
	if expectedVersion < 0 || !contracts.ValidHybridGatewayBinding(input) {
		return contracts.HybridGatewayBinding{}, ErrInvalid
	}
	instanceOwned, connectorBound := false, false
	for _, instance := range s.instances {
		if instance.ID == input.InstanceID && instance.UserID == input.UserID {
			instanceOwned = true
			connectorBound = instance.ConnectorID == input.ConnectorID
			break
		}
	}
	key, virtualKeyFound := s.virtualKeys[input.VirtualKeyID]
	if !instanceOwned || !connectorBound || !virtualKeyFound || key.UserID != input.UserID || key.InstanceID != input.InstanceID ||
		key.ResourceClass != input.ResourceClass || key.KeyVersion != input.VirtualKeyVersion {
		return contracts.HybridGatewayBinding{}, ErrInvalid
	}
	mapKey := hybridBindingMapKey(input.InstanceID, input.ResourceClass)
	current, exists := s.hybridGatewayBindings[mapKey]
	if !exists && expectedVersion != 0 || exists && current.Version != expectedVersion {
		return contracts.HybridGatewayBinding{}, ErrConflict
	}
	now := s.now()
	if exists {
		if input.ID != current.ID || input.UserID != current.UserID || input.InstanceID != current.InstanceID || input.ResourceClass != current.ResourceClass {
			return contracts.HybridGatewayBinding{}, ErrConflict
		}
		input.Version = current.Version + 1
		input.CreatedAt = current.CreatedAt
	} else {
		input.Version = 1
		input.CreatedAt = now
	}
	input.UpdatedAt = now
	s.hybridGatewayBindings[mapKey] = copyHybridGatewayBinding(input)
	return copyHybridGatewayBinding(input), nil
}

func validHybridExecutionCreate(input contracts.HybridRoutingExecution) bool {
	return input.UserID > 0 && strings.TrimSpace(input.InstanceID) != "" && input.AllocationVersion > 0 &&
		contracts.ValidHybridRoutingModel(input.Model) &&
		(input.Status == "" || input.Status == contracts.HybridRoutingExecutionPending) &&
		input.Generation == 0 && input.Version == 0 && input.Attempts == 0 && input.LeaseOwner == "" &&
		input.LeaseUntil == nil && input.CompletedAt == nil && input.ErrorCode == "" &&
		len(input.Target) == 0 && len(input.Effective) == 0 && len(input.Actual) == 0 &&
		len(input.DesiredWeights) == 0 && len(input.AdjustmentCodes) == 0
}

func validHybridWorkerID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128 && !contracts.LooksLikeConnectorSensitiveValue(value)
}

func (s *MemoryStore) CreateHybridRoutingExecution(ctx context.Context, input contracts.HybridRoutingExecution) (contracts.HybridRoutingExecution, error) {
	if err := ctx.Err(); err != nil {
		return contracts.HybridRoutingExecution{}, err
	}
	input.InstanceID, input.Model = strings.TrimSpace(input.InstanceID), strings.TrimSpace(input.Model)
	if !validHybridExecutionCreate(input) {
		return contracts.HybridRoutingExecution{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	allocation, exists := s.hybridAllocations[input.InstanceID]
	if !exists || allocation.UserID != input.UserID || allocation.Version != input.AllocationVersion {
		return contracts.HybridRoutingExecution{}, ErrConflict
	}
	for _, execution := range s.hybridRoutingExecutions {
		if execution.InstanceID == input.InstanceID &&
			(execution.Status == contracts.HybridRoutingExecutionPending || execution.Status == contracts.HybridRoutingExecutionApplying) {
			return contracts.HybridRoutingExecution{}, ErrConflict
		}
	}
	for _, task := range s.connectorTasks {
		if task.InstanceID == input.InstanceID && task.ExecutionScope == contracts.HybridRoutingExecutionScope &&
			task.ExecutionGeneration == allocation.RoutingGeneration && task.Status == contracts.ConnectorTaskExecuting {
			return contracts.HybridRoutingExecution{}, ErrConflict
		}
	}
	allocation.RoutingGeneration++
	if allocation.RoutingGeneration <= 0 {
		allocation.RoutingGeneration = 1
	}
	allocation.UpdatedAt = s.now()
	s.hybridAllocations[input.InstanceID] = allocation
	if input.ID == "" {
		input.ID = s.nextID("hyexec")
	}
	input.Generation = allocation.RoutingGeneration
	input.Status = contracts.HybridRoutingExecutionPending
	input.Version = 1
	input.CreatedAt, input.UpdatedAt = allocation.UpdatedAt, allocation.UpdatedAt
	s.hybridRoutingExecutions = append(s.hybridRoutingExecutions, copyHybridRoutingExecution(input))
	return copyHybridRoutingExecution(input), nil
}

func (s *MemoryStore) GetHybridRoutingExecution(ctx context.Context, userID int64, id string) (contracts.HybridRoutingExecution, error) {
	if err := ctx.Err(); err != nil {
		return contracts.HybridRoutingExecution{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, execution := range s.hybridRoutingExecutions {
		if execution.ID == id && execution.UserID == userID {
			return copyHybridRoutingExecution(execution), nil
		}
	}
	return contracts.HybridRoutingExecution{}, ErrNotFound
}

func (s *MemoryStore) ListHybridRoutingExecutions(ctx context.Context, userID int64, instanceID string, limit int) ([]contracts.HybridRoutingExecution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if userID <= 0 || strings.TrimSpace(instanceID) == "" || limit < 0 {
		return nil, ErrInvalid
	}
	if limit == 0 || limit > 100 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.HybridRoutingExecution, 0, limit)
	for index := len(s.hybridRoutingExecutions) - 1; index >= 0 && len(out) < limit; index-- {
		execution := s.hybridRoutingExecutions[index]
		if execution.UserID == userID && execution.InstanceID == instanceID {
			out = append(out, copyHybridRoutingExecution(execution))
		}
	}
	return out, nil
}

func (s *MemoryStore) ClaimHybridRoutingExecution(ctx context.Context, workerID string, leaseDuration time.Duration) (contracts.HybridRoutingExecution, bool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.HybridRoutingExecution{}, false, err
	}
	workerID = strings.TrimSpace(workerID)
	if !validHybridWorkerID(workerID) || leaseDuration <= 0 {
		return contracts.HybridRoutingExecution{}, false, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now, selected := s.now(), -1
	for index, execution := range s.hybridRoutingExecutions {
		eligible := execution.Status == contracts.HybridRoutingExecutionPending ||
			execution.Status == contracts.HybridRoutingExecutionApplying && execution.LeaseUntil != nil && !execution.LeaseUntil.After(now)
		allocation, exists := s.hybridAllocations[execution.InstanceID]
		if !eligible || !exists || allocation.RoutingGeneration != execution.Generation {
			continue
		}
		if selected < 0 || execution.UpdatedAt.Before(s.hybridRoutingExecutions[selected].UpdatedAt) ||
			execution.UpdatedAt.Equal(s.hybridRoutingExecutions[selected].UpdatedAt) && execution.ID < s.hybridRoutingExecutions[selected].ID {
			selected = index
		}
	}
	if selected < 0 {
		return contracts.HybridRoutingExecution{}, false, nil
	}
	execution := s.hybridRoutingExecutions[selected]
	until := now.Add(leaseDuration)
	execution.Status = contracts.HybridRoutingExecutionApplying
	execution.LeaseOwner, execution.LeaseUntil = workerID, &until
	execution.Attempts++
	execution.Version++
	execution.UpdatedAt = now
	s.hybridRoutingExecutions[selected] = copyHybridRoutingExecution(execution)
	return copyHybridRoutingExecution(execution), true, nil
}

func (s *MemoryStore) RenewHybridRoutingExecution(ctx context.Context, id, workerID string, expectedVersion int64, leaseDuration time.Duration) (contracts.HybridRoutingExecution, error) {
	if err := ctx.Err(); err != nil {
		return contracts.HybridRoutingExecution{}, err
	}
	workerID = strings.TrimSpace(workerID)
	if id == "" || !validHybridWorkerID(workerID) || expectedVersion <= 0 || leaseDuration <= 0 {
		return contracts.HybridRoutingExecution{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for index, execution := range s.hybridRoutingExecutions {
		if execution.ID != id {
			continue
		}
		allocation, exists := s.hybridAllocations[execution.InstanceID]
		if execution.Status != contracts.HybridRoutingExecutionApplying || execution.LeaseOwner != workerID ||
			execution.Version != expectedVersion || execution.LeaseUntil == nil || !execution.LeaseUntil.After(now) ||
			!exists || allocation.RoutingGeneration != execution.Generation {
			return contracts.HybridRoutingExecution{}, ErrConflict
		}
		until := now.Add(leaseDuration)
		execution.LeaseUntil, execution.Version, execution.UpdatedAt = &until, execution.Version+1, now
		s.hybridRoutingExecutions[index] = copyHybridRoutingExecution(execution)
		return copyHybridRoutingExecution(execution), nil
	}
	return contracts.HybridRoutingExecution{}, ErrNotFound
}

func validHybridAdjustmentCodes(values []string) bool {
	if len(values) > 32 {
		return false
	}
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 64 || contracts.LooksLikeConnectorSensitiveValue(value) {
			return false
		}
	}
	return true
}

func (s *MemoryStore) PlanHybridRoutingExecution(ctx context.Context, input contracts.HybridRoutingExecutionPlan) (contracts.HybridRoutingExecution, error) {
	if err := ctx.Err(); err != nil {
		return contracts.HybridRoutingExecution{}, err
	}
	input.ID, input.WorkerID = strings.TrimSpace(input.ID), strings.TrimSpace(input.WorkerID)
	desiredPercent, desiredValid := contracts.HybridAccountWeightPercent(input.DesiredWeights)
	if input.ID == "" || !validHybridWorkerID(input.WorkerID) || input.ExpectedVersion <= 0 ||
		!contracts.ValidCompleteHybridPercentMap(input.Target) || !contracts.ValidCompleteHybridPercentMap(input.Effective) ||
		!desiredValid || !contracts.HybridPercentMapsEqual(desiredPercent, input.Effective) ||
		!validHybridAdjustmentCodes(input.AdjustmentCodes) {
		return contracts.HybridRoutingExecution{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for index, execution := range s.hybridRoutingExecutions {
		if execution.ID != input.ID {
			continue
		}
		allocation, exists := s.hybridAllocations[execution.InstanceID]
		if execution.Status != contracts.HybridRoutingExecutionApplying || execution.LeaseOwner != input.WorkerID ||
			execution.Version != input.ExpectedVersion || execution.LeaseUntil == nil || !execution.LeaseUntil.After(now) ||
			!exists || allocation.RoutingGeneration != execution.Generation || len(execution.DesiredWeights) != 0 {
			return contracts.HybridRoutingExecution{}, ErrConflict
		}
		execution.Target = copyHybridPercentMap(input.Target)
		execution.Effective = copyHybridPercentMap(input.Effective)
		execution.DesiredWeights = append([]contracts.HybridAccountWeight(nil), input.DesiredWeights...)
		execution.AdjustmentCodes = append([]string(nil), input.AdjustmentCodes...)
		execution.Version++
		execution.UpdatedAt = now
		s.hybridRoutingExecutions[index] = copyHybridRoutingExecution(execution)
		return copyHybridRoutingExecution(execution), nil
	}
	return contracts.HybridRoutingExecution{}, ErrNotFound
}

func (s *MemoryStore) CompleteHybridRoutingExecution(ctx context.Context, input contracts.HybridRoutingExecutionCompletion) (contracts.HybridRoutingExecution, error) {
	if err := ctx.Err(); err != nil {
		return contracts.HybridRoutingExecution{}, err
	}
	input.ID, input.WorkerID, input.ErrorCode = strings.TrimSpace(input.ID), strings.TrimSpace(input.WorkerID), strings.TrimSpace(input.ErrorCode)
	validCompletion := input.ID != "" && validHybridWorkerID(input.WorkerID) && input.ExpectedVersion > 0 &&
		len(input.ErrorCode) <= 64 && !contracts.LooksLikeConnectorSensitiveValue(input.ErrorCode)
	if input.Succeeded {
		validCompletion = validCompletion && input.ErrorCode == "" && contracts.ValidHybridAccountWeights(input.ReadBackWeights)
	} else {
		validCompletion = validCompletion && input.ErrorCode != "" && len(input.ReadBackWeights) == 0
	}
	if !validCompletion {
		return contracts.HybridRoutingExecution{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for index, execution := range s.hybridRoutingExecutions {
		if execution.ID != input.ID {
			continue
		}
		allocation, exists := s.hybridAllocations[execution.InstanceID]
		if execution.Status != contracts.HybridRoutingExecutionApplying || execution.LeaseOwner != input.WorkerID ||
			execution.Version != input.ExpectedVersion || execution.LeaseUntil == nil || !execution.LeaseUntil.After(now) ||
			!exists || allocation.RoutingGeneration != execution.Generation || input.Succeeded && len(execution.DesiredWeights) == 0 {
			return contracts.HybridRoutingExecution{}, ErrConflict
		}
		if input.Succeeded {
			actual, actualValid := contracts.HybridAccountWeightPercent(input.ReadBackWeights)
			if !actualValid || !contracts.HybridAccountWeightsEqual(input.ReadBackWeights, execution.DesiredWeights) {
				return contracts.HybridRoutingExecution{}, ErrInvalid
			}
			execution.Status = contracts.HybridRoutingExecutionSucceeded
			execution.Actual = copyHybridPercentMap(actual)
			execution.ErrorCode = ""
		} else {
			execution.Status = contracts.HybridRoutingExecutionFailed
			execution.Actual = nil
			execution.ErrorCode = input.ErrorCode
		}
		execution.LeaseOwner, execution.LeaseUntil = "", nil
		execution.Version++
		execution.UpdatedAt, execution.CompletedAt = now, &now
		s.hybridRoutingExecutions[index] = copyHybridRoutingExecution(execution)
		return copyHybridRoutingExecution(execution), nil
	}
	return contracts.HybridRoutingExecution{}, ErrNotFound
}
