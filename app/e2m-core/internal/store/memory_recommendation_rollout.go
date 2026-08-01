package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
)

const maxRecommendationRolloutListLimit = 500

func (s *MemoryStore) CreateRecommendationRollout(ctx context.Context, input contracts.RecommendationRolloutCreate) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	rollout := cloneRecommendationRollout(input.Rollout)
	if !validRecommendationRolloutCreate(input, rollout) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	planIndex := -1
	for i := range s.routePlans {
		if s.routePlans[i].ID == rollout.State.PlanID {
			planIndex = i
			break
		}
	}
	if planIndex < 0 {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrNotFound
	}
	plan := s.routePlans[planIndex]
	if plan.UserID != rollout.State.UserID || plan.InstanceID != rollout.InstanceID || plan.Status != contracts.RoutePlanPublished ||
		plan.SchedulingGeneration != input.ExpectedPlanGeneration || rollout.RecommendationPlanGeneration != input.ExpectedPlanGeneration {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
	}
	if !memoryRecommendationRolloutReferencesLocked(s, rollout) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
	}
	for _, current := range s.recommendationRollouts {
		if current.State.ID == rollout.State.ID || recommendationRolloutActive(current.State.Status) && current.State.PlanID == rollout.State.PlanID {
			return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
		}
	}
	if s.routePlanHasExecutingConnectorTaskLocked(plan.ID) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
	}

	now := normalizeRecommendationRolloutTime(s.now())
	s.routePlans[planIndex] = plan
	s.advanceRoutePlanGenerationLocked(&s.routePlans[planIndex], now, "")
	plan = s.routePlans[planIndex]

	if rollout.State.ID == "" {
		rollout.State.ID = s.nextID("rec-rollout")
	}
	rollout.State.SchedulingGeneration = plan.SchedulingGeneration
	rollout.State.UpdatedAt = now
	rollout.Version = 1
	rollout.CreatedAt = now
	operation := newMemoryRecommendationRolloutOperationLocked(s, rollout, input.FirstAction, input.FirstTargetStage, now)
	rollout.LastOperationID = operation.ID
	s.recommendationRollouts = append(s.recommendationRollouts, cloneRecommendationRollout(rollout))
	s.recommendationRolloutOperations = append(s.recommendationRolloutOperations, cloneRecommendationRolloutOperation(operation))
	return cloneRecommendationRollout(rollout), cloneRecommendationRolloutOperation(operation), nil
}

func (s *MemoryStore) GetRecommendationRollout(ctx context.Context, id string) (contracts.RecommendationRollout, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RecommendationRollout{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return contracts.RecommendationRollout{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, rollout := range s.recommendationRollouts {
		if rollout.State.ID == id {
			return cloneRecommendationRollout(rollout), nil
		}
	}
	return contracts.RecommendationRollout{}, ErrNotFound
}

func (s *MemoryStore) ListRecommendationRollouts(ctx context.Context, filter contracts.RecommendationRolloutFilter) ([]contracts.RecommendationRollout, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filter.UserID < 0 || filter.Status != "" && !contracts.IsRecommendationRolloutStatus(filter.Status) || filter.Limit < 0 {
		return nil, ErrInvalid
	}
	limit := normalizeRecommendationRolloutLimit(filter.Limit)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.RecommendationRollout, 0)
	for _, rollout := range s.recommendationRollouts {
		if filter.UserID > 0 && rollout.State.UserID != filter.UserID || filter.Status != "" && rollout.State.Status != filter.Status ||
			strings.TrimSpace(filter.PlanID) != "" && rollout.State.PlanID != strings.TrimSpace(filter.PlanID) {
			continue
		}
		out = append(out, cloneRecommendationRollout(rollout))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].State.UpdatedAt.Equal(out[j].State.UpdatedAt) {
			return out[i].State.UpdatedAt.After(out[j].State.UpdatedAt)
		}
		return out[i].State.ID > out[j].State.ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) ListRecommendationRolloutOperations(ctx context.Context, rolloutID string) ([]contracts.RecommendationRolloutOperation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rolloutID = strings.TrimSpace(rolloutID)
	if rolloutID == "" {
		return nil, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	found := false
	for _, rollout := range s.recommendationRollouts {
		found = found || rollout.State.ID == rolloutID
	}
	if !found {
		return nil, ErrNotFound
	}
	out := make([]contracts.RecommendationRolloutOperation, 0)
	for _, operation := range s.recommendationRolloutOperations {
		if operation.RolloutID == rolloutID {
			out = append(out, cloneRecommendationRolloutOperation(operation))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// TransitionRecommendationRolloutState records a side-effect-free observation
// result. Remote mutations must always use the durable operation path below;
// deliberately keeping this CAS narrow prevents callers from fabricating an
// applied stage, a rollback, or a completed rollout without gateway evidence.
func (s *MemoryStore) TransitionRecommendationRolloutState(ctx context.Context, rolloutID string, expectedVersion int64, next contracts.RecommendationRolloutState) (contracts.RecommendationRollout, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RecommendationRollout{}, err
	}
	rolloutID = strings.TrimSpace(rolloutID)
	if rolloutID == "" || expectedVersion <= 0 || !validRecommendationRolloutState(next) {
		return contracts.RecommendationRollout{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := memoryRecommendationRolloutIndex(s, rolloutID)
	if index < 0 {
		return contracts.RecommendationRollout{}, ErrNotFound
	}
	current := s.recommendationRollouts[index]
	if current.Version != expectedVersion || !validRecommendationRolloutObservationTransition(current.State, next) ||
		!memoryRecommendationRolloutGenerationOwnedLocked(s, current) || memoryRecommendationRolloutHasActiveOperationLocked(s, rolloutID) {
		return contracts.RecommendationRollout{}, ErrConflict
	}
	current.State = cloneRecommendationRolloutState(next)
	current.State.UpdatedAt = normalizeRecommendationRolloutTime(s.now())
	current.Version++
	s.recommendationRollouts[index] = cloneRecommendationRollout(current)
	return cloneRecommendationRollout(current), nil
}

func (s *MemoryStore) EnqueueRecommendationRolloutOperation(ctx context.Context, rolloutID string, expectedVersion int64, next contracts.RecommendationRolloutState, action contracts.RecommendationRolloutOperationAction, target contracts.RecommendationRolloutStage) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	if strings.TrimSpace(rolloutID) == "" || expectedVersion <= 0 || !validRecommendationRolloutOperationShape(action, target) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := memoryRecommendationRolloutIndex(s, rolloutID)
	if index < 0 {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrNotFound
	}
	current := s.recommendationRollouts[index]
	if current.Version != expectedVersion || !validRecommendationRolloutEnqueueTransition(current.State, next, action, target) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
	}
	activeOperationIndexes := make([]int, 0, 1)
	for operationIndex := range s.recommendationRolloutOperations {
		operation := s.recommendationRolloutOperations[operationIndex]
		if operation.RolloutID != rolloutID || operation.Status != contracts.RecommendationRolloutOperationPending && operation.Status != contracts.RecommendationRolloutOperationRunning {
			continue
		}
		if action != contracts.RecommendationRolloutOperationRollback || operation.Action != contracts.RecommendationRolloutOperationApplyStage {
			return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
		}
		activeOperationIndexes = append(activeOperationIndexes, operationIndex)
	}
	planIndex := memoryRecommendationRolloutPlanIndexLocked(s, current)
	if planIndex < 0 || !recommendationRolloutPlanCanClaimOperation(s.routePlans[planIndex], current, action) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
	}
	if s.routePlanHasExecutingConnectorTaskLocked(s.routePlans[planIndex].ID) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
	}
	now := normalizeRecommendationRolloutTime(s.now())
	for _, operationIndex := range activeOperationIndexes {
		operation := s.recommendationRolloutOperations[operationIndex]
		// Rollback is the sole preemptive operation. Superseding and clearing a
		// forward lease under this same lock, after every fail-closed check has
		// passed, means its old worker can neither renew nor complete after the
		// generation takeover below and a rejected enqueue leaves no partial write.
		operation.Status = contracts.RecommendationRolloutOperationSuperseded
		operation.ErrorCode = ""
		operation.Version++
		operation.LeaseOwner = ""
		operation.LeaseUntil = nil
		operation.UpdatedAt = now
		s.recommendationRolloutOperations[operationIndex] = cloneRecommendationRolloutOperation(operation)
	}
	plan := s.routePlans[planIndex]
	s.routePlans[planIndex] = plan
	s.advanceRoutePlanGenerationLocked(&s.routePlans[planIndex], now, "")
	plan = s.routePlans[planIndex]
	current.State = cloneRecommendationRolloutState(next)
	current.State.SchedulingGeneration = plan.SchedulingGeneration
	if current.State.LastAfterEvidence != nil {
		current.State.LastAfterEvidence.SchedulingGeneration = plan.SchedulingGeneration
	}
	current.State.UpdatedAt = now
	current.Version++
	operation := newMemoryRecommendationRolloutOperationLocked(s, current, action, target, now)
	current.LastOperationID = operation.ID
	s.recommendationRollouts[index] = cloneRecommendationRollout(current)
	s.recommendationRolloutOperations = append(s.recommendationRolloutOperations, cloneRecommendationRolloutOperation(operation))
	return cloneRecommendationRollout(current), cloneRecommendationRolloutOperation(operation), nil
}

func (s *MemoryStore) ClaimRecommendationRolloutOperation(ctx context.Context, workerID string, lease time.Duration) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, bool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, false, err
	}
	workerID = strings.TrimSpace(workerID)
	if !validRecommendationRolloutWorkerID(workerID) || lease <= 0 {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, false, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := normalizeRecommendationRolloutTime(s.now())
	best := -1
	for i, operation := range s.recommendationRolloutOperations {
		eligible := operation.Status == contracts.RecommendationRolloutOperationPending ||
			operation.Status == contracts.RecommendationRolloutOperationRunning && (operation.LeaseUntil == nil || !operation.LeaseUntil.After(now))
		if !eligible || memoryRecommendationRolloutIndex(s, operation.RolloutID) < 0 {
			continue
		}
		if best < 0 || operation.UpdatedAt.Before(s.recommendationRolloutOperations[best].UpdatedAt) ||
			operation.UpdatedAt.Equal(s.recommendationRolloutOperations[best].UpdatedAt) && operation.ID < s.recommendationRolloutOperations[best].ID {
			best = i
		}
	}
	if best < 0 {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, false, nil
	}
	operation := s.recommendationRolloutOperations[best]
	rolloutIndex := memoryRecommendationRolloutIndex(s, operation.RolloutID)
	rollout := s.recommendationRollouts[rolloutIndex]
	if !memoryRecommendationRolloutGenerationOwnedLocked(s, rollout) {
		operation.Status = contracts.RecommendationRolloutOperationSuperseded
		operation.ErrorCode = ""
		operation.Version++
		operation.LeaseOwner = ""
		operation.LeaseUntil = nil
		operation.UpdatedAt = now
		s.recommendationRolloutOperations[best] = operation
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, false, nil
	}
	until := now.Add(lease)
	operation.Status = contracts.RecommendationRolloutOperationRunning
	operation.Attempts++
	operation.ErrorCode = ""
	operation.Version++
	operation.LeaseOwner = workerID
	operation.LeaseUntil = &until
	operation.UpdatedAt = now
	s.recommendationRolloutOperations[best] = operation
	return cloneRecommendationRollout(rollout), cloneRecommendationRolloutOperation(operation), true, nil
}

func (s *MemoryStore) RenewRecommendationRolloutOperation(ctx context.Context, id, workerID string, expectedVersion int64, lease time.Duration) (contracts.RecommendationRolloutOperation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RecommendationRolloutOperation{}, err
	}
	if strings.TrimSpace(id) == "" || !validRecommendationRolloutWorkerID(strings.TrimSpace(workerID)) || expectedVersion <= 0 || lease <= 0 {
		return contracts.RecommendationRolloutOperation{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := normalizeRecommendationRolloutTime(s.now())
	for i := range s.recommendationRolloutOperations {
		operation := s.recommendationRolloutOperations[i]
		if operation.ID != id {
			continue
		}
		rolloutIndex := memoryRecommendationRolloutIndex(s, operation.RolloutID)
		if operation.Status != contracts.RecommendationRolloutOperationRunning || operation.LeaseOwner != strings.TrimSpace(workerID) || operation.Version != expectedVersion ||
			operation.LeaseUntil == nil || !operation.LeaseUntil.After(now) || rolloutIndex < 0 || !memoryRecommendationRolloutGenerationOwnedLocked(s, s.recommendationRollouts[rolloutIndex]) {
			return contracts.RecommendationRolloutOperation{}, ErrConflict
		}
		until := now.Add(lease)
		operation.Version++
		operation.LeaseUntil = &until
		operation.UpdatedAt = now
		s.recommendationRolloutOperations[i] = operation
		return cloneRecommendationRolloutOperation(operation), nil
	}
	return contracts.RecommendationRolloutOperation{}, ErrNotFound
}

func (s *MemoryStore) CompleteRecommendationRolloutOperation(ctx context.Context, input contracts.RecommendationRolloutCompletion) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, err
	}
	if !validRecommendationRolloutCompletion(input) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := normalizeRecommendationRolloutTime(s.now())
	opIndex := -1
	for i := range s.recommendationRolloutOperations {
		if s.recommendationRolloutOperations[i].ID == input.OperationID {
			opIndex = i
			break
		}
	}
	if opIndex < 0 {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrNotFound
	}
	operation := s.recommendationRolloutOperations[opIndex]
	rolloutIndex := memoryRecommendationRolloutIndex(s, operation.RolloutID)
	if rolloutIndex < 0 {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
	}
	rollout := s.recommendationRollouts[rolloutIndex]
	if operation.Status != contracts.RecommendationRolloutOperationRunning || operation.LeaseOwner != strings.TrimSpace(input.WorkerID) ||
		operation.Version != input.ExpectedOperationVersion || operation.LeaseUntil == nil || !operation.LeaseUntil.After(now) ||
		rollout.Version != input.ExpectedRolloutVersion || !validRecommendationRolloutCompletionTransition(rollout.State, operation, input) ||
		!memoryRecommendationRolloutGenerationOwnedLocked(s, rollout) {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, ErrConflict
	}
	operation.Status = input.OperationStatus
	operation.ErrorCode = input.ErrorCode
	operation.Version++
	operation.LeaseOwner = ""
	operation.LeaseUntil = nil
	operation.UpdatedAt = now
	rollout.State = cloneRecommendationRolloutState(input.NextState)
	rollout.State.UpdatedAt = now
	rollout.Version++
	rollout.LastOperationID = operation.ID
	s.recommendationRolloutOperations[opIndex] = cloneRecommendationRolloutOperation(operation)
	s.recommendationRollouts[rolloutIndex] = cloneRecommendationRollout(rollout)
	return cloneRecommendationRollout(rollout), cloneRecommendationRolloutOperation(operation), nil
}

func memoryRecommendationRolloutReferencesLocked(s *MemoryStore, rollout contracts.RecommendationRollout) bool {
	recommendationFound := false
	for _, recommendation := range s.upstreamRecommendations {
		if recommendation.UserID == rollout.State.UserID && recommendation.ID == rollout.State.RecommendationID &&
			recommendation.Fingerprint == rollout.State.RecommendationFingerprint && recommendation.PlanGeneration == rollout.RecommendationPlanGeneration &&
			recommendation.FromChannelID == rollout.FromChannelID && recommendation.ToChannelID == rollout.ToChannelID {
			recommendationFound = true
			break
		}
	}
	if !recommendationFound {
		return false
	}
	bindings := make([]recommendationRolloutBindingReference, 0)
	for _, binding := range s.publishedBindings {
		if binding.PlanID == rollout.State.PlanID && binding.State != contracts.BindingRevoked && binding.RemoteID != "" {
			allocation, allocated := s.channelAllocations[binding.ChannelID]
			channelOwnership := contracts.GatewayAccountOwnership("")
			for _, channel := range s.upstreamChannels {
				if channel.ID == binding.ChannelID {
					channelOwnership = channel.AccountOwnership.Normalize()
					break
				}
			}
			bindings = append(bindings, recommendationRolloutBindingReference{
				InstanceID: binding.InstanceID, ChannelID: binding.ChannelID, RemoteID: binding.RemoteID,
				BindingOwnership: binding.AccountOwnership.Normalize(), ChannelOwnership: channelOwnership,
				SchedulingGeneration: binding.SchedulingGeneration, AllocationUserID: allocation.UserID, Allocated: allocated,
			})
		}
	}
	return recommendationRolloutReferencesValid(rollout, bindings)
}

type recommendationRolloutBindingReference struct {
	InstanceID           string
	ChannelID            string
	RemoteID             string
	BindingOwnership     contracts.GatewayAccountOwnership
	ChannelOwnership     contracts.GatewayAccountOwnership
	SchedulingGeneration int64
	AllocationUserID     int64
	Allocated            bool
}

func recommendationRolloutReferencesValid(rollout contracts.RecommendationRollout, bindings []recommendationRolloutBindingReference) bool {
	baselineAccounts := make(map[string]struct{}, len(rollout.BaselineWeights))
	for _, weight := range rollout.BaselineWeights {
		baselineAccounts[weight.AccountID] = struct{}{}
	}
	if len(bindings) != len(baselineAccounts) {
		return false
	}
	bindingAccounts := make(map[string]string, len(bindings))
	remoteAccounts := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if binding.InstanceID != rollout.InstanceID || binding.SchedulingGeneration != rollout.RecommendationPlanGeneration ||
			!binding.Allocated || binding.AllocationUserID != rollout.State.UserID || !binding.BindingOwnership.Valid() ||
			binding.ChannelOwnership.Normalize() != binding.BindingOwnership.Normalize() {
			return false
		}
		if _, duplicate := bindingAccounts[binding.ChannelID]; duplicate {
			return false
		}
		if _, duplicate := remoteAccounts[binding.RemoteID]; duplicate {
			return false
		}
		bindingAccounts[binding.ChannelID] = binding.RemoteID
		remoteAccounts[binding.RemoteID] = struct{}{}
	}
	if bindingAccounts[rollout.FromChannelID] != rollout.FromAccountID || bindingAccounts[rollout.ToChannelID] != rollout.ToAccountID {
		return false
	}
	for accountID := range remoteAccounts {
		if _, exists := baselineAccounts[accountID]; !exists {
			return false
		}
	}
	return true
}

func memoryRecommendationRolloutGenerationOwnedLocked(s *MemoryStore, rollout contracts.RecommendationRollout) bool {
	for _, plan := range s.routePlans {
		if plan.ID == rollout.State.PlanID {
			return plan.Status == contracts.RoutePlanPublished && plan.UserID == rollout.State.UserID && plan.InstanceID == rollout.InstanceID && plan.SchedulingGeneration == rollout.State.SchedulingGeneration
		}
	}
	return false
}

func memoryRecommendationRolloutPlanIndexLocked(s *MemoryStore, rollout contracts.RecommendationRollout) int {
	for index := range s.routePlans {
		plan := s.routePlans[index]
		if plan.ID == rollout.State.PlanID && plan.UserID == rollout.State.UserID && plan.InstanceID == rollout.InstanceID {
			return index
		}
	}
	return -1
}

func recommendationRolloutPlanCanClaimOperation(plan contracts.RoutePlan, rollout contracts.RecommendationRollout, action contracts.RecommendationRolloutOperationAction) bool {
	if plan.ID != rollout.State.PlanID || plan.UserID != rollout.State.UserID || plan.InstanceID != rollout.InstanceID {
		return false
	}
	if action == contracts.RecommendationRolloutOperationRollback {
		return plan.Status == contracts.RoutePlanPublished || plan.Status == contracts.RoutePlanSuspended
	}
	return plan.Status == contracts.RoutePlanPublished && plan.SchedulingGeneration == rollout.State.SchedulingGeneration
}

func memoryRecommendationRolloutIndex(s *MemoryStore, id string) int {
	for i := range s.recommendationRollouts {
		if s.recommendationRollouts[i].State.ID == id {
			return i
		}
	}
	return -1
}

func memoryRecommendationRolloutHasActiveOperationLocked(s *MemoryStore, rolloutID string) bool {
	for _, operation := range s.recommendationRolloutOperations {
		if operation.RolloutID == rolloutID && (operation.Status == contracts.RecommendationRolloutOperationPending || operation.Status == contracts.RecommendationRolloutOperationRunning) {
			return true
		}
	}
	return false
}

func newMemoryRecommendationRolloutOperationLocked(s *MemoryStore, rollout contracts.RecommendationRollout, action contracts.RecommendationRolloutOperationAction, target contracts.RecommendationRolloutStage, now time.Time) contracts.RecommendationRolloutOperation {
	return contracts.RecommendationRolloutOperation{
		ID: s.nextID("rec-rollout-op"), RolloutID: rollout.State.ID, UserID: rollout.State.UserID, PlanID: rollout.State.PlanID,
		Action: action, TargetStage: target, Status: contracts.RecommendationRolloutOperationPending, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func validRecommendationRolloutCreate(input contracts.RecommendationRolloutCreate, rollout contracts.RecommendationRollout) bool {
	baseline, err := contracts.CanonicalRecommendationRolloutWeights(rollout.BaselineWeights)
	fingerprint, fingerprintErr := contracts.RecommendationRolloutBaselineFingerprint(baseline)
	return err == nil && fingerprintErr == nil && fingerprint == rollout.State.BaselineFingerprint && input.ExpectedPlanGeneration > 0 &&
		rollout.RecommendationPlanGeneration == input.ExpectedPlanGeneration && rollout.State.SchedulingGeneration == 0 &&
		rollout.Version == 0 && rollout.CreatedAt.IsZero() && rollout.LastOperationID == "" &&
		strings.TrimSpace(rollout.InstanceID) != "" && strings.TrimSpace(rollout.FromChannelID) != "" && strings.TrimSpace(rollout.ToChannelID) != "" &&
		strings.TrimSpace(rollout.FromAccountID) != "" && strings.TrimSpace(rollout.ToAccountID) != "" && rollout.FromAccountID != rollout.ToAccountID &&
		validRecommendationRolloutOperationShape(input.FirstAction, input.FirstTargetStage) && validRecommendationRolloutStateForCreate(rollout.State)
}

func validRecommendationRolloutStateForCreate(state contracts.RecommendationRolloutState) bool {
	copyState := cloneRecommendationRolloutState(state)
	copyState.SchedulingGeneration = 1
	return validRecommendationRolloutState(copyState) && state.Status == contracts.RecommendationRolloutApplying && state.Stage == contracts.RecommendationRolloutStageNone && state.PendingStage == contracts.RecommendationRolloutStage10
}

func validRecommendationRolloutState(state contracts.RecommendationRolloutState) bool {
	if strings.TrimSpace(state.ID) == "" || state.UserID <= 0 || strings.TrimSpace(state.PlanID) == "" || strings.TrimSpace(state.RecommendationID) == "" ||
		len(state.RecommendationFingerprint) != 64 || state.FactVersion <= 0 || len(state.EvidenceIDs) == 0 || len(state.BaselineFingerprint) != 64 ||
		state.SchedulingGeneration <= 0 || !contracts.IsRecommendationRolloutStatus(state.Status) || !contracts.IsRecommendationRolloutStage(state.Stage) ||
		!contracts.IsRecommendationRolloutStage(state.PendingStage) || state.ObservationSeconds <= 0 || state.ObservationSeconds > 604800 ||
		state.RecommendationExpiresAt.IsZero() || state.StartedAt.IsZero() || !state.RecommendationExpiresAt.After(state.StartedAt) {
		return false
	}
	switch state.Status {
	case contracts.RecommendationRolloutApplying:
		return state.ObserveUntil == nil && nextRecommendationRolloutStage(state.Stage) == state.PendingStage && state.PendingStage != 0
	case contracts.RecommendationRolloutObserving:
		return state.PendingStage == 0 && state.Stage != 0 && state.StageStartedAt != nil && state.ObserveUntil != nil && state.ObserveUntil.After(*state.StageStartedAt)
	case contracts.RecommendationRolloutCompleted:
		return state.Stage == 100 && state.PendingStage == 0
	case contracts.RecommendationRolloutRolledBack:
		return state.Stage == 0 && state.PendingStage == 0
	case contracts.RecommendationRolloutReady, contracts.RecommendationRolloutRollbackRequired, contracts.RecommendationRolloutBlocked:
		return state.PendingStage == 0
	default:
		return false
	}
}

func validRecommendationRolloutOperationShape(action contracts.RecommendationRolloutOperationAction, target contracts.RecommendationRolloutStage) bool {
	return action == contracts.RecommendationRolloutOperationApplyStage && (target == 10 || target == 25 || target == 50 || target == 100) || action == contracts.RecommendationRolloutOperationRollback && target == 0
}

func validRecommendationRolloutCompletion(input contracts.RecommendationRolloutCompletion) bool {
	if strings.TrimSpace(input.OperationID) == "" || !validRecommendationRolloutWorkerID(strings.TrimSpace(input.WorkerID)) || input.ExpectedOperationVersion <= 0 || input.ExpectedRolloutVersion <= 0 ||
		input.OperationStatus != contracts.RecommendationRolloutOperationSucceeded && input.OperationStatus != contracts.RecommendationRolloutOperationFailed && input.OperationStatus != contracts.RecommendationRolloutOperationSuperseded ||
		!contracts.IsRecommendationRolloutOperationErrorCode(input.ErrorCode) {
		return false
	}
	return input.OperationStatus == contracts.RecommendationRolloutOperationFailed && input.ErrorCode != "" || input.OperationStatus != contracts.RecommendationRolloutOperationFailed && input.ErrorCode == ""
}

func validRecommendationRolloutWorkerID(value string) bool {
	return value != "" && len(value) <= 128 && !contracts.LooksLikeConnectorSensitiveValue(value)
}

func nextRecommendationRolloutStage(stage contracts.RecommendationRolloutStage) contracts.RecommendationRolloutStage {
	switch stage {
	case 0:
		return 10
	case 10:
		return 25
	case 25:
		return 50
	case 50:
		return 100
	default:
		return 0
	}
}

func recommendationRolloutActive(status contracts.RecommendationRolloutStatus) bool {
	return status == contracts.RecommendationRolloutReady || status == contracts.RecommendationRolloutApplying || status == contracts.RecommendationRolloutObserving || status == contracts.RecommendationRolloutRollbackRequired || status == contracts.RecommendationRolloutBlocked
}

func sameRecommendationRolloutIdentity(left, right contracts.RecommendationRolloutState) bool {
	return left.ID == right.ID && left.UserID == right.UserID && left.PlanID == right.PlanID && left.RecommendationID == right.RecommendationID &&
		left.RecommendationFingerprint == right.RecommendationFingerprint && left.FactVersion == right.FactVersion && left.BaselineFingerprint == right.BaselineFingerprint &&
		left.SchedulingGeneration == right.SchedulingGeneration && left.StartedAt.Equal(right.StartedAt) && left.RecommendationExpiresAt.Equal(right.RecommendationExpiresAt)
}

func sameRecommendationRolloutImmutableIdentity(left, right contracts.RecommendationRolloutState) bool {
	return left.ID == right.ID && left.UserID == right.UserID && left.PlanID == right.PlanID && left.RecommendationID == right.RecommendationID &&
		left.RecommendationFingerprint == right.RecommendationFingerprint && left.FactVersion == right.FactVersion && left.BaselineFingerprint == right.BaselineFingerprint &&
		left.ObservationSeconds == right.ObservationSeconds && left.StartedAt.Equal(right.StartedAt) && left.RecommendationExpiresAt.Equal(right.RecommendationExpiresAt) &&
		sameRecommendationRolloutIDs(left.EvidenceIDs, right.EvidenceIDs)
}

func validRecommendationRolloutEnqueueTransition(current, next contracts.RecommendationRolloutState, action contracts.RecommendationRolloutOperationAction, target contracts.RecommendationRolloutStage) bool {
	if !validRecommendationRolloutState(next) || !sameRecommendationRolloutImmutableIdentity(current, next) {
		return false
	}
	if action == contracts.RecommendationRolloutOperationApplyStage {
		base := next.Status == contracts.RecommendationRolloutApplying && target == nextRecommendationRolloutStage(current.Stage) &&
			next.Stage == current.Stage && next.PendingStage == target && next.SchedulingGeneration == current.SchedulingGeneration &&
			sameRecommendationRolloutReasons(current.RollbackReasons, next.RollbackReasons)
		if !base {
			return false
		}
		if current.Status == contracts.RecommendationRolloutReady {
			return sameRecommendationRolloutAfterEvidence(current.LastAfterEvidence, next.LastAfterEvidence)
		}
		// Observe+Evaluate may be persisted with the next operation in one CAS,
		// eliminating a ready-state crash window between those two pure steps.
		return current.Status == contracts.RecommendationRolloutObserving && current.Stage != contracts.RecommendationRolloutStage100 &&
			validRecommendationRolloutSuccessfulAfterEvidence(current, next.LastAfterEvidence, next.UpdatedAt) && next.ObserveUntil == nil
	}
	if action != contracts.RecommendationRolloutOperationRollback || target != contracts.RecommendationRolloutStageNone {
		return false
	}
	switch current.Status {
	case contracts.RecommendationRolloutReady, contracts.RecommendationRolloutApplying, contracts.RecommendationRolloutObserving, contracts.RecommendationRolloutBlocked,
		contracts.RecommendationRolloutCompleted, contracts.RecommendationRolloutRollbackRequired:
	default:
		return false
	}
	return next.Status == contracts.RecommendationRolloutRollbackRequired && next.Stage == current.Stage &&
		next.PendingStage == contracts.RecommendationRolloutStageNone && next.ObserveUntil == nil &&
		len(next.RollbackReasons) > 0 && next.SchedulingGeneration == current.SchedulingGeneration
}

func validRecommendationRolloutCompletionTransition(current contracts.RecommendationRolloutState, operation contracts.RecommendationRolloutOperation, input contracts.RecommendationRolloutCompletion) bool {
	next := input.NextState
	if !validRecommendationRolloutState(next) || !sameRecommendationRolloutIdentity(current, next) || !sameRecommendationRolloutImmutableIdentity(current, next) {
		return false
	}
	if input.OperationStatus == contracts.RecommendationRolloutOperationSuperseded {
		return statesEqualForRecommendationRolloutStore(current, next)
	}
	if operation.Action == contracts.RecommendationRolloutOperationApplyStage {
		if current.Status != contracts.RecommendationRolloutApplying || current.PendingStage != operation.TargetStage {
			return false
		}
		if input.OperationStatus == contracts.RecommendationRolloutOperationSucceeded {
			return next.Status == contracts.RecommendationRolloutObserving && next.Stage == operation.TargetStage && next.PendingStage == 0 &&
				next.StageStartedAt != nil && next.ObserveUntil != nil && next.ObserveUntil.After(*next.StageStartedAt)
		}
		return input.OperationStatus == contracts.RecommendationRolloutOperationFailed && next.Status == contracts.RecommendationRolloutRollbackRequired &&
			next.Stage == current.Stage && next.PendingStage == 0 && next.ObserveUntil == nil && len(next.RollbackReasons) > 0
	}
	if operation.Action != contracts.RecommendationRolloutOperationRollback || current.Status != contracts.RecommendationRolloutRollbackRequired {
		return false
	}
	if input.OperationStatus == contracts.RecommendationRolloutOperationFailed {
		return next.Status == contracts.RecommendationRolloutRollbackRequired && next.Stage == current.Stage && next.PendingStage == 0 && len(next.RollbackReasons) > 0
	}
	if input.OperationStatus != contracts.RecommendationRolloutOperationSucceeded || next.Status != contracts.RecommendationRolloutRolledBack || next.Stage != 0 || next.PendingStage != 0 {
		return false
	}
	after := next.LastAfterEvidence
	return after != nil && after.Stage == 0 && after.BaselineFingerprint == current.BaselineFingerprint &&
		after.RecommendationFingerprint == current.RecommendationFingerprint && after.SchedulingGeneration == current.SchedulingGeneration &&
		validRecommendationRolloutRollbackWeightEvidence(after.EvidenceIDs, current.BaselineFingerprint) &&
		after.Callability == contracts.RecommendationRolloutGateUnknown && after.Quality == contracts.RecommendationRolloutGateUnknown &&
		!after.ObservedAt.IsZero() && !after.FreshUntil.IsZero() && !after.FreshUntil.Before(after.ObservedAt)
}

func validRecommendationRolloutRollbackWeightEvidence(values []string, fingerprint string) bool {
	return len(values) == 1 && values[0] == "weight-set-sha256:"+fingerprint
}

func statesEqualForRecommendationRolloutStore(left, right contracts.RecommendationRolloutState) bool {
	return left.Status == right.Status && left.Stage == right.Stage && left.PendingStage == right.PendingStage &&
		sameRecommendationRolloutTime(left.StageStartedAt, right.StageStartedAt) && sameRecommendationRolloutTime(left.ObserveUntil, right.ObserveUntil) &&
		sameRecommendationRolloutReasons(left.RollbackReasons, right.RollbackReasons) && left.LastAfterEvidence == nil && right.LastAfterEvidence == nil
}

func validRecommendationRolloutSuccessfulAfterEvidence(current contracts.RecommendationRolloutState, after *contracts.RecommendationRolloutAfterEvidence, now time.Time) bool {
	return after != nil && after.Stage == current.Stage && after.RecommendationFingerprint == current.RecommendationFingerprint &&
		after.BaselineFingerprint == "" && after.SchedulingGeneration == current.SchedulingGeneration && len(after.EvidenceIDs) > 0 &&
		sameRecommendationRolloutIDs(after.EvidenceIDs, append([]string(nil), after.EvidenceIDs...)) &&
		after.Callability == contracts.RecommendationRolloutGatePassed && after.Quality == contracts.RecommendationRolloutGatePassed &&
		!after.ObservedAt.IsZero() && !after.FreshUntil.IsZero() && !after.FreshUntil.Before(after.ObservedAt) &&
		!now.IsZero() && !now.Before(after.ObservedAt) && now.Before(after.FreshUntil)
}

func sameRecommendationRolloutAfterEvidence(left, right *contracts.RecommendationRolloutAfterEvidence) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Stage == right.Stage && left.RecommendationFingerprint == right.RecommendationFingerprint &&
		left.BaselineFingerprint == right.BaselineFingerprint && left.SchedulingGeneration == right.SchedulingGeneration &&
		left.ObservedAt.Equal(right.ObservedAt) && left.FreshUntil.Equal(right.FreshUntil) && left.Callability == right.Callability &&
		left.Quality == right.Quality && sameRecommendationRolloutIDs(left.EvidenceIDs, right.EvidenceIDs)
}

func validRecommendationRolloutObservationTransition(current, next contracts.RecommendationRolloutState) bool {
	if current.Status != contracts.RecommendationRolloutObserving || current.Stage == contracts.RecommendationRolloutStageNone ||
		!sameRecommendationRolloutIdentity(current, next) || current.Stage != next.Stage || next.PendingStage != contracts.RecommendationRolloutStageNone ||
		current.ObservationSeconds != next.ObservationSeconds || !sameRecommendationRolloutIDs(current.EvidenceIDs, next.EvidenceIDs) ||
		!sameRecommendationRolloutTime(current.StageStartedAt, next.StageStartedAt) || next.ObserveUntil != nil ||
		!sameRecommendationRolloutReasons(current.RollbackReasons, next.RollbackReasons) {
		return false
	}
	if current.Stage == contracts.RecommendationRolloutStage100 {
		if next.Status != contracts.RecommendationRolloutCompleted {
			return false
		}
	} else if next.Status != contracts.RecommendationRolloutReady {
		return false
	}
	return validRecommendationRolloutSuccessfulAfterEvidence(current, next.LastAfterEvidence, next.UpdatedAt)
}

func sameRecommendationRolloutIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]struct{}, len(left))
	for _, value := range left {
		value = strings.TrimSpace(value)
		if value == "" {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := seen[strings.TrimSpace(value)]; !ok {
			return false
		}
	}
	return true
}

func sameRecommendationRolloutTime(left, right *time.Time) bool {
	return left == nil && right == nil || left != nil && right != nil && left.Equal(*right)
}

func sameRecommendationRolloutReasons(left, right []contracts.RecommendationRolloutBlockReason) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneRecommendationRollout(input contracts.RecommendationRollout) contracts.RecommendationRollout {
	input.State = cloneRecommendationRolloutState(input.State)
	input.BaselineWeights = append([]contracts.RecommendationRolloutAccountWeight(nil), input.BaselineWeights...)
	return input
}

func cloneRecommendationRolloutState(input contracts.RecommendationRolloutState) contracts.RecommendationRolloutState {
	input.EvidenceIDs = append([]string(nil), input.EvidenceIDs...)
	input.RollbackReasons = append([]contracts.RecommendationRolloutBlockReason(nil), input.RollbackReasons...)
	input.StageStartedAt = cloneRecommendationRolloutTime(input.StageStartedAt)
	input.ObserveUntil = cloneRecommendationRolloutTime(input.ObserveUntil)
	if input.LastAfterEvidence != nil {
		copyEvidence := *input.LastAfterEvidence
		copyEvidence.EvidenceIDs = append([]string(nil), input.LastAfterEvidence.EvidenceIDs...)
		input.LastAfterEvidence = &copyEvidence
	}
	return input
}

func cloneRecommendationRolloutOperation(input contracts.RecommendationRolloutOperation) contracts.RecommendationRolloutOperation {
	input.LeaseUntil = cloneRecommendationRolloutTime(input.LeaseUntil)
	return input
}

func cloneRecommendationRolloutTime(input *time.Time) *time.Time {
	if input == nil {
		return nil
	}
	copyTime := *input
	return &copyTime
}

func normalizeRecommendationRolloutTime(input time.Time) time.Time {
	return input.UTC().Truncate(time.Microsecond)
}

func normalizeRecommendationRolloutLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > maxRecommendationRolloutListLimit {
		return maxRecommendationRolloutListLimit
	}
	return limit
}
