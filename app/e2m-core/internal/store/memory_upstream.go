package store

import (
	"context"
	"strconv"
	"time"

	"e2m.local/contracts"
)

// MemoryStore implementations for the platform-managed upstream layer:
// pools, channels, route plans (desired state), and published bindings
// (reconcile paper trail). Mirrors the concurrency/ID conventions of the rest
// of MemoryStore.

type upstreamChannelAllocation struct {
	UserID      int64
	SourceID    string
	FirstPlanID string
	CreatedAt   time.Time
}

func (s *MemoryStore) CreateUpstreamPool(ctx context.Context, input contracts.UpstreamPool) (contracts.UpstreamPool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamPool{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	p := input
	p.ResourceClass = contracts.NormalizePlatformResourceClass(p.ResourceClass)
	p.DeliveryMode = p.DeliveryMode.Normalize()
	if !p.ResourceClass.IsPlatformSupply() || !p.DeliveryMode.Valid() {
		return contracts.UpstreamPool{}, ErrInvalid
	}
	if p.ID == "" {
		p.ID = s.nextID("pool")
	}
	if p.Status == "" {
		p.Status = contracts.UpstreamPoolActive
	}
	p.CreatedAt, p.UpdatedAt = now, now
	s.upstreamPools = append(s.upstreamPools, p)
	return p, nil
}

func (s *MemoryStore) GetUpstreamPool(ctx context.Context, id string) (contracts.UpstreamPool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamPool{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.upstreamPools {
		if p.ID == id {
			return p, nil
		}
	}
	return contracts.UpstreamPool{}, ErrNotFound
}

func (s *MemoryStore) ListUpstreamPools(ctx context.Context) ([]contracts.UpstreamPool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.UpstreamPool, len(s.upstreamPools))
	copy(out, s.upstreamPools)
	return out, nil
}

func (s *MemoryStore) UpdateUpstreamPool(ctx context.Context, input contracts.UpstreamPool) (contracts.UpstreamPool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamPool{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := memoryLifecycleFor(s)
	state.jobsMu.Lock()
	defer state.jobsMu.Unlock()
	for i := range s.upstreamPools {
		if s.upstreamPools[i].ID == input.ID {
			input.ResourceClass = contracts.NormalizePlatformResourceClass(input.ResourceClass)
			input.DeliveryMode = input.DeliveryMode.Normalize()
			if !input.ResourceClass.IsPlatformSupply() || !input.DeliveryMode.Valid() {
				return contracts.UpstreamPool{}, ErrInvalid
			}
			if s.upstreamPools[i].Status != contracts.UpstreamPoolRetired && input.Status == contracts.UpstreamPoolRetired {
				return contracts.UpstreamPool{}, ErrConflict
			}
			if s.upstreamPools[i].Status == contracts.UpstreamPoolMaintenance && input.Status == contracts.UpstreamPoolActive {
				for _, job := range state.jobs {
					if job.PoolID == input.ID && job.Status != contracts.PoolRetirementCompleted {
						return contracts.UpstreamPool{}, ErrConflict
					}
				}
			}
			input.CreatedAt = s.upstreamPools[i].CreatedAt
			input.UpdatedAt = s.now()
			s.upstreamPools[i] = input
			return input, nil
		}
	}
	return contracts.UpstreamPool{}, ErrNotFound
}

func (s *MemoryStore) CreateUpstreamChannel(ctx context.Context, input contracts.UpstreamChannel) (contracts.UpstreamChannel, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamChannel{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	c := input
	if c.SourceID != "" && !contracts.IsUpstreamSourceIdentity(c.SourceID) {
		return contracts.UpstreamChannel{}, ErrInvalid
	}
	c.AccountOwnership = c.AccountOwnership.Normalize()
	if !c.AccountOwnership.Valid() {
		return contracts.UpstreamChannel{}, ErrConflict
	}
	if c.ID == "" {
		c.ID = s.nextID("uchan")
	}
	if c.Status == "" {
		c.Status = contracts.UpstreamChannelActive
	}
	c.CreatedAt, c.UpdatedAt = now, now
	s.upstreamChannels = append(s.upstreamChannels, c)
	return c, nil
}

func (s *MemoryStore) GetUpstreamChannel(ctx context.Context, id string) (contracts.UpstreamChannel, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamChannel{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.upstreamChannels {
		if c.ID == id {
			return c, nil
		}
	}
	return contracts.UpstreamChannel{}, ErrNotFound
}

func (s *MemoryStore) ListUpstreamChannels(ctx context.Context, poolID string) ([]contracts.UpstreamChannel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.UpstreamChannel, 0, len(s.upstreamChannels))
	for _, c := range s.upstreamChannels {
		if poolID == "" || c.PoolID == poolID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *MemoryStore) UpdateUpstreamChannel(ctx context.Context, input contracts.UpstreamChannel) (contracts.UpstreamChannel, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamChannel{}, err
	}
	if input.SourceID != "" && !contracts.IsUpstreamSourceIdentity(input.SourceID) {
		return contracts.UpstreamChannel{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.upstreamChannels {
		if s.upstreamChannels[i].ID == input.ID {
			current := s.upstreamChannels[i]
			input.AccountOwnership = input.AccountOwnership.Normalize()
			if !input.AccountOwnership.Valid() || input.AccountOwnership != current.AccountOwnership.Normalize() {
				return contracts.UpstreamChannel{}, ErrConflict
			}
			if _, allocated := s.channelAllocations[input.ID]; allocated {
				if current.SourceIdentity() != input.SourceIdentity() ||
					current.PoolID != input.PoolID ||
					input.InventoryState == contracts.UpstreamInventoryRetired {
					return contracts.UpstreamChannel{}, ErrConflict
				}
			}
			if upstreamChannelSemanticEqual(current, input) {
				return current, nil
			}
			affectedPlanIDs := make(map[string]struct{})
			if _, allocated := s.channelAllocations[input.ID]; allocated {
				for _, binding := range s.publishedBindings {
					if binding.ChannelID == input.ID {
						affectedPlanIDs[binding.PlanID] = struct{}{}
					}
				}
			}
			if s.anyRoutePlanHasExecutingConnectorTaskLocked(affectedPlanIDs) {
				return contracts.UpstreamChannel{}, ErrConflict
			}
			input.CreatedAt = current.CreatedAt
			input.UpdatedAt = s.now().UTC()
			s.upstreamChannels[i] = input
			if _, allocated := s.channelAllocations[input.ID]; allocated {
				for planIndex := range s.routePlans {
					plan := &s.routePlans[planIndex]
					bindingFound := false
					for _, binding := range s.publishedBindings {
						if binding.PlanID == plan.ID && binding.ChannelID == input.ID {
							bindingFound = true
							break
						}
					}
					if !bindingFound {
						continue
					}
					s.advanceRoutePlanGenerationLocked(plan, input.UpdatedAt, "")
				}
			}
			return input, nil
		}
	}
	return contracts.UpstreamChannel{}, ErrNotFound
}

func upstreamChannelSemanticEqual(left, right contracts.UpstreamChannel) bool {
	return left.PoolID == right.PoolID && left.SourceID == right.SourceID && left.AccountOwnership.Normalize() == right.AccountOwnership.Normalize() &&
		left.DisplayName == right.DisplayName && left.Provider == right.Provider && left.ProbeCapability == right.ProbeCapability &&
		left.ProbeEndpointPath == right.ProbeEndpointPath && left.CredentialBindingID == right.CredentialBindingID &&
		left.ProxyBindingID == right.ProxyBindingID && left.Priority == right.Priority && left.Weight == right.Weight &&
		left.CostHint == right.CostHint && left.Status == right.Status && left.InventoryState == right.InventoryState &&
		stringSlicesEqual(left.Models, right.Models) && stringSlicesEqual(left.Groups, right.Groups) && stringMapEqual(left.Labels, right.Labels)
}

func stringSlicesEqual(left, right []string) bool {
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

func stringMapEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func (s *MemoryStore) CreateRoutePlan(ctx context.Context, input contracts.RoutePlan) (contracts.RoutePlan, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RoutePlan{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	p := input
	if p.InstanceID != "" && p.PoolID != "" {
		for _, existing := range s.routePlans {
			if existing.InstanceID == p.InstanceID && existing.PoolID == p.PoolID {
				return contracts.RoutePlan{}, ErrDuplicate
			}
		}
		for _, pool := range s.upstreamPools {
			if pool.ID == p.PoolID && pool.Status != contracts.UpstreamPoolActive {
				return contracts.RoutePlan{}, ErrConflict
			}
		}
	}
	if p.ID == "" {
		p.ID = s.nextID("plan")
	}
	if p.Status == "" {
		p.Status = contracts.RoutePlanDraft
	}
	p.CreatedAt, p.UpdatedAt = now, now
	s.routePlans = append(s.routePlans, p)
	return p, nil
}

func (s *MemoryStore) GetRoutePlan(ctx context.Context, id string) (contracts.RoutePlan, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RoutePlan{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.routePlans {
		if p.ID == id {
			return p, nil
		}
	}
	return contracts.RoutePlan{}, ErrNotFound
}

func (s *MemoryStore) ListRoutePlans(ctx context.Context, userID int64) ([]contracts.RoutePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.RoutePlan, 0, len(s.routePlans))
	for _, p := range s.routePlans {
		if userID == 0 || p.UserID == userID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *MemoryStore) UpdateRoutePlan(ctx context.Context, input contracts.RoutePlan) (contracts.RoutePlan, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RoutePlan{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.routePlans {
		if s.routePlans[i].ID == input.ID {
			current := s.routePlans[i]
			if input.SchedulingGeneration != current.SchedulingGeneration ||
				input.UserID != current.UserID || input.InstanceID != current.InstanceID || input.PoolID != current.PoolID {
				return contracts.RoutePlan{}, ErrConflict
			}
			if routePlanDesiredStateEqual(current, input) {
				return current, nil
			}
			if s.routePlanHasExecutingConnectorTaskLocked(current.ID) {
				return contracts.RoutePlan{}, ErrConflict
			}
			input.CreatedAt = current.CreatedAt
			input.SchedulingGeneration = current.SchedulingGeneration
			input.UpdatedAt = s.now().UTC()
			s.routePlans[i] = input
			s.advanceRoutePlanGenerationLocked(&s.routePlans[i], input.UpdatedAt, "")
			return s.routePlans[i], nil
		}
	}
	return contracts.RoutePlan{}, ErrNotFound
}

func (s *MemoryStore) CompleteRoutePlanPublish(ctx context.Context, id string, expectedSchedulingGeneration int64) (contracts.RoutePlan, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RoutePlan{}, err
	}
	if id == "" || expectedSchedulingGeneration <= 0 {
		return contracts.RoutePlan{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.routePlans {
		plan := s.routePlans[i]
		if plan.ID != id {
			continue
		}
		if plan.Status != contracts.RoutePlanDraft || plan.SchedulingGeneration != expectedSchedulingGeneration {
			return contracts.RoutePlan{}, ErrConflict
		}
		plan.Status = contracts.RoutePlanPublished
		plan.UpdatedAt = s.now().UTC()
		s.routePlans[i] = plan
		return plan, nil
	}
	return contracts.RoutePlan{}, ErrNotFound
}

func routePlanDesiredStateEqual(left, right contracts.RoutePlan) bool {
	if effectiveRoutePlanRollout(left.Rollout) != effectiveRoutePlanRollout(right.Rollout) ||
		left.Tier != right.Tier || left.Status != right.Status || left.MaxChannels != right.MaxChannels ||
		left.RolloutBatchSize != right.RolloutBatchSize || left.RolloutCanaryCount != right.RolloutCanaryCount ||
		len(left.Labels) != len(right.Labels) {
		return false
	}
	for key, value := range left.Labels {
		if right.Labels[key] != value {
			return false
		}
	}
	return true
}

func effectiveRoutePlanRollout(value contracts.RolloutMode) contracts.RolloutMode {
	if value == "" {
		return contracts.RolloutImmediate
	}
	return value
}

func (s *MemoryStore) ClaimRoutePlanScheduling(ctx context.Context, id string, allowedStatuses ...contracts.RoutePlanStatus) (contracts.RoutePlan, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RoutePlan{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.routePlans {
		if s.routePlans[i].ID != id {
			continue
		}
		if !routePlanStatusAllowed(s.routePlans[i].Status, allowedStatuses) {
			return contracts.RoutePlan{}, ErrConflict
		}
		if s.routePlanHasExecutingConnectorTaskLocked(s.routePlans[i].ID) {
			return contracts.RoutePlan{}, ErrConflict
		}
		now := s.now().UTC()
		s.advanceRoutePlanGenerationLocked(&s.routePlans[i], now, "")
		return s.routePlans[i], nil
	}
	return contracts.RoutePlan{}, ErrNotFound
}

func (s *MemoryStore) TransitionRoutePlanScheduling(ctx context.Context, id string, expected, target contracts.RoutePlanStatus) (contracts.RoutePlan, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RoutePlan{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.routePlans {
		if s.routePlans[i].ID != id {
			continue
		}
		if s.routePlans[i].Status != expected {
			return contracts.RoutePlan{}, ErrConflict
		}
		if s.routePlanHasExecutingConnectorTaskLocked(s.routePlans[i].ID) {
			return contracts.RoutePlan{}, ErrConflict
		}
		s.routePlans[i].Status = target
		now := s.now().UTC()
		s.advanceRoutePlanGenerationLocked(&s.routePlans[i], now, "")
		return s.routePlans[i], nil
	}
	return contracts.RoutePlan{}, ErrNotFound
}

func (s *MemoryStore) ClaimPlanChannels(ctx context.Context, planID string) ([]contracts.UpstreamChannel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var plan contracts.RoutePlan
	planFound := false
	for _, candidate := range s.routePlans {
		if candidate.ID == planID {
			plan, planFound = candidate, true
			break
		}
	}
	if !planFound {
		return nil, ErrNotFound
	}
	poolActive := false
	poolFound := false
	for _, pool := range s.upstreamPools {
		if pool.ID == plan.PoolID {
			poolFound = true
			poolActive = pool.Status == contracts.UpstreamPoolActive
			break
		}
	}
	if !poolFound {
		return nil, ErrNotFound
	}
	if !poolActive {
		return nil, ErrConflict
	}

	channels := make([]contracts.UpstreamChannel, 0)
	for _, channel := range s.upstreamChannels {
		if channel.PoolID == plan.PoolID {
			channels = append(channels, channel)
		}
	}
	view := planChannelAllocationView{
		owners:     make(map[string]int64, len(s.channelAllocations)),
		userSource: make(map[string]struct{}),
	}
	for channelID, allocation := range s.channelAllocations {
		view.owners[channelID] = allocation.UserID
		if allocation.UserID == plan.UserID {
			view.userSource[allocation.SourceID] = struct{}{}
		}
	}
	selected := selectClaimablePlanChannels(channels, plan.MaxChannels, plan.UserID, view)
	now := s.now()
	for _, channel := range selected {
		if _, exists := s.channelAllocations[channel.ID]; !exists {
			s.channelAllocations[channel.ID] = upstreamChannelAllocation{
				UserID: plan.UserID, SourceID: channel.SourceIdentity(), FirstPlanID: plan.ID, CreatedAt: now,
			}
		}
		bindingExists := false
		for _, binding := range s.publishedBindings {
			if binding.PlanID == plan.ID && binding.ChannelID == channel.ID {
				bindingExists = true
				break
			}
		}
		if bindingExists {
			continue
		}
		s.publishedBindings = append(s.publishedBindings, contracts.PublishedBinding{
			ID: s.nextID("bind"), PlanID: plan.ID, InstanceID: plan.InstanceID,
			ChannelID: channel.ID, AccountOwnership: channel.AccountOwnership.Normalize(),
			State: contracts.BindingPending, SchedulingGeneration: plan.SchedulingGeneration,
			CreatedAt: now, UpdatedAt: now,
		})
	}
	return selected, nil
}

func routePlanStatusAllowed(status contracts.RoutePlanStatus, allowed []contracts.RoutePlanStatus) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if status == candidate {
			return true
		}
	}
	return false
}

func (s *MemoryStore) UpsertPublishedBinding(ctx context.Context, input contracts.PublishedBinding) (contracts.PublishedBinding, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PublishedBinding{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ownerID int64
	var planGeneration int64
	var planInstanceID string
	planFound := false
	for _, plan := range s.routePlans {
		if plan.ID == input.PlanID {
			ownerID = plan.UserID
			planGeneration = plan.SchedulingGeneration
			planInstanceID = plan.InstanceID
			planFound = true
			break
		}
	}
	if !planFound {
		return contracts.PublishedBinding{}, ErrNotFound
	}
	// Legacy/manual seed writes may omit a generation before any scheduling
	// operation has claimed the plan. Once a plan has a current owner, every
	// binding fact must come from exactly that generation.
	if planGeneration > 0 && input.SchedulingGeneration != planGeneration {
		return contracts.PublishedBinding{}, ErrConflict
	}
	if input.InstanceID == "" {
		input.InstanceID = planInstanceID
	} else if planInstanceID != "" && input.InstanceID != planInstanceID {
		return contracts.PublishedBinding{}, ErrConflict
	}
	sourceID := input.ChannelID
	for _, channel := range s.upstreamChannels {
		if channel.ID == input.ChannelID {
			sourceID = channel.SourceIdentity()
			input.AccountOwnership = channel.AccountOwnership.Normalize()
			break
		}
	}
	if !input.AccountOwnership.Valid() {
		return contracts.PublishedBinding{}, ErrConflict
	}
	if allocation, exists := s.channelAllocations[input.ChannelID]; exists && allocation.UserID != ownerID {
		return contracts.PublishedBinding{}, ErrDuplicate
	}
	if _, exists := s.channelAllocations[input.ChannelID]; !exists {
		for channelID, allocation := range s.channelAllocations {
			if channelID != input.ChannelID && allocation.UserID == ownerID && allocation.SourceID == sourceID {
				return contracts.PublishedBinding{}, ErrDuplicate
			}
		}
	}
	now := s.now()
	// A caller that supplies an existing binding ID may not relocate that
	// execution identity to another plan or channel. Callers that address the
	// stable (plan, channel) key may omit the generated ID on idempotent writes.
	if input.ID != "" {
		for _, binding := range s.publishedBindings {
			if binding.ID == input.ID && (binding.PlanID != input.PlanID || binding.ChannelID != input.ChannelID) {
				return contracts.PublishedBinding{}, ErrConflict
			}
		}
	}
	// Identity is (plan_id, channel_id): one binding per channel per plan.
	for i := range s.publishedBindings {
		b := s.publishedBindings[i]
		if b.PlanID == input.PlanID && b.ChannelID == input.ChannelID {
			if input.InstanceID == "" {
				input.InstanceID = b.InstanceID
			}
			if input.ID != "" && input.ID != b.ID || input.InstanceID != b.InstanceID {
				return contracts.PublishedBinding{}, ErrConflict
			}
			if input.SchedulingGeneration < b.SchedulingGeneration {
				return contracts.PublishedBinding{}, ErrConflict
			}
			remoteChanged := input.RemoteID != b.RemoteID
			if remoteChanged && (input.RemoteID == "" ||
				input.SchedulingGeneration == b.SchedulingGeneration && b.RemoteID != "") {
				return contracts.PublishedBinding{}, ErrConflict
			}
			input.ID = b.ID
			input.CreatedAt = b.CreatedAt
			// Replacing a concrete remote under a newer scheduling owner creates a
			// new execution identity. It must earn callability again; proof from the
			// prior remote can never cross the generation boundary. Establishing an
			// initially-empty remote in the same generation may carry an explicit
			// awaiting-first-request state from the successful publisher.
			if remoteChanged && input.SchedulingGeneration > b.SchedulingGeneration {
				input.VerificationStatus = contracts.BindingVerificationPublishedPending
				input.VerificationSource = contracts.BindingVerificationSourcePublish
				input.VerifiedAt = nil
				input.VerificationErrorCode = ""
			} else if remoteChanged && input.VerificationStatus == "" {
				input.VerificationStatus = contracts.BindingVerificationPublishedPending
				input.VerificationSource = contracts.BindingVerificationSourcePublish
				input.VerifiedAt = nil
				input.VerificationErrorCode = ""
				// Scheduling/lifecycle writers normally omit verification fields. Keep
				// the independent request-evidence ledger unless this write explicitly
				// resets or advances it (for example after provisioning a new remote).
			} else if input.VerificationStatus == "" {
				input.VerificationStatus = b.VerificationStatus
				input.VerificationSource = b.VerificationSource
				input.VerifiedAt = b.VerifiedAt
				input.VerificationErrorCode = b.VerificationErrorCode
			}
			input.UpdatedAt = now
			s.publishedBindings[i] = input
			return input, nil
		}
	}
	b := input
	if b.ID == "" {
		b.ID = s.nextID("bind")
	}
	if b.State == "" {
		b.State = contracts.BindingPending
	}
	if b.VerificationStatus == "" {
		b.VerificationStatus = contracts.BindingVerificationPublishedPending
		b.VerificationSource = contracts.BindingVerificationSourcePublish
	}
	b.CreatedAt, b.UpdatedAt = now, now
	if _, exists := s.channelAllocations[b.ChannelID]; !exists {
		s.channelAllocations[b.ChannelID] = upstreamChannelAllocation{
			UserID: ownerID, SourceID: sourceID, FirstPlanID: b.PlanID, CreatedAt: now,
		}
	}
	s.publishedBindings = append(s.publishedBindings, b)
	return b, nil
}

func (s *MemoryStore) RecordPublishedBindingVerification(ctx context.Context, planID, channelID string, status contracts.PublishedBindingVerificationStatus, source contracts.PublishedBindingVerificationSource, observedAt time.Time, errorCode string) (contracts.PublishedBinding, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PublishedBinding{}, err
	}
	if !validBindingVerification(status, source) {
		return contracts.PublishedBinding{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.publishedBindings {
		b := &s.publishedBindings[i]
		if b.PlanID != planID || b.ChannelID != channelID {
			continue
		}
		if isBindingVerified(b.VerificationStatus) && !isBindingVerified(status) {
			return *b, nil
		}
		if observedAt.IsZero() {
			observedAt = s.now()
		}
		observedAt = observedAt.UTC()
		b.VerificationStatus = status
		b.VerificationSource = source
		b.VerificationErrorCode = errorCode
		b.UpdatedAt = s.now()
		if isBindingVerified(status) {
			verifiedAt := observedAt
			b.VerifiedAt = &verifiedAt
			b.VerificationErrorCode = ""
		} else {
			b.VerifiedAt = nil
		}
		return *b, nil
	}
	return contracts.PublishedBinding{}, ErrNotFound
}

func validBindingVerification(status contracts.PublishedBindingVerificationStatus, source contracts.PublishedBindingVerificationSource) bool {
	switch status {
	case contracts.BindingVerificationPublishedPending, contracts.BindingVerificationAwaitingFirstRequest:
		return source == contracts.BindingVerificationSourcePublish
	case contracts.BindingVerificationProbeVerified:
		return source == contracts.BindingVerificationSourceProbe
	case contracts.BindingVerificationPassiveVerified:
		return source == contracts.BindingVerificationSourcePassive
	case contracts.BindingVerificationFailed:
		return source == contracts.BindingVerificationSourceProbe || source == contracts.BindingVerificationSourcePassive
	default:
		return false
	}
}

func isBindingVerified(status contracts.PublishedBindingVerificationStatus) bool {
	return status == contracts.BindingVerificationProbeVerified || status == contracts.BindingVerificationPassiveVerified
}

func (s *MemoryStore) ListPublishedBindings(ctx context.Context, planID string) ([]contracts.PublishedBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.PublishedBinding, 0, len(s.publishedBindings))
	for _, b := range s.publishedBindings {
		if planID == "" || b.PlanID == planID {
			out = append(out, b)
		}
	}
	return out, nil
}

// AppendReconcileRun records one publish/reconcile execution. Runs are
// append-only history; ListReconcileRuns returns them newest-first.
func (s *MemoryStore) AppendReconcileRun(ctx context.Context, input contracts.ReconcileRun) (contracts.ReconcileRun, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ReconcileRun{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run := input
	if run.ID == "" {
		run.ID = s.nextID("rcrun")
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = s.now()
	}
	if run.FinishedAt.IsZero() {
		run.FinishedAt = s.now()
	}
	s.reconcileRuns = append(s.reconcileRuns, run)
	s.recordOperationalMetricLocked("reconcile_runs", string(run.Kind), 1)
	return run, nil
}

func (s *MemoryStore) ListReconcileRuns(ctx context.Context, planID string, limit int) ([]contracts.ReconcileRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.ReconcileRun, 0, len(s.reconcileRuns))
	// Newest first: iterate in reverse insertion order.
	for i := len(s.reconcileRuns) - 1; i >= 0; i-- {
		r := s.reconcileRuns[i]
		if planID == "" || r.PlanID == planID {
			out = append(out, r)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// --- Auto-switch decisions (Phase 4) ---

// CreateAutoSwitchDecision records a new decision. Decisions are append-only
// history; a decision's later lifecycle is tracked via UpdateAutoSwitchDecision.
func (s *MemoryStore) CreateAutoSwitchDecision(ctx context.Context, input contracts.AutoSwitchDecision) (contracts.AutoSwitchDecision, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.Status.IsActive() {
		for _, existing := range s.autoSwitchDecs {
			if existing.PlanID == input.PlanID && input.Fingerprint != "" && existing.Fingerprint == input.Fingerprint && existing.Status.IsActive() {
				return contracts.AutoSwitchDecision{}, ErrDuplicate
			}
		}
	}
	dec := input
	if dec.ID == "" {
		dec.ID = s.nextID("aswitch")
	}
	now := s.now()
	if dec.CreatedAt.IsZero() {
		dec.CreatedAt = now
	}
	if dec.SchedulingGeneration == 0 {
		if planIndex := s.routePlanIndex(dec.PlanID); planIndex >= 0 {
			dec.SchedulingGeneration = s.routePlans[planIndex].SchedulingGeneration
		}
	}
	dec.UpdatedAt = now
	dec.LeaseUntil = copyTimePointer(dec.LeaseUntil)
	s.autoSwitchDecs = append(s.autoSwitchDecs, dec)
	return dec, nil
}

// ClaimAutoSwitchDecision atomically reserves one failure fingerprint before
// the orchestrator mutates channel state or invokes reconcile. A losing caller
// receives the already-active decision and must not execute any side effects.
func (s *MemoryStore) ClaimAutoSwitchDecision(ctx context.Context, input contracts.AutoSwitchDecision) (contracts.AutoSwitchDecision, bool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	leaseDuration := autoSwitchLeaseDuration(input.LeaseUntil, input.CreatedAt)
	if _, err := autoSwitchLeaseMicros(leaseDuration); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	storeNow := s.now().UTC()
	planIndex := s.routePlanIndex(input.PlanID)
	if planIndex < 0 {
		return contracts.AutoSwitchDecision{}, false, ErrNotFound
	}
	if s.routePlans[planIndex].Status != contracts.RoutePlanPublished {
		return contracts.AutoSwitchDecision{}, false, ErrConflict
	}
	for i := len(s.autoSwitchDecs) - 1; i >= 0; i-- {
		existing := s.autoSwitchDecs[i]
		if existing.PlanID == input.PlanID && existing.Fingerprint == input.Fingerprint && existing.Status.IsActive() &&
			existing.SchedulingGeneration == s.routePlans[planIndex].SchedulingGeneration {
			return existing, false, nil
		}
	}
	if s.routePlanHasExecutingConnectorTaskLocked(input.PlanID) {
		return contracts.AutoSwitchDecision{}, false, ErrConflict
	}
	dec := input
	dec.Status = contracts.AutoSwitchApplying
	dec.LeaseVersion = 1
	s.advanceRoutePlanGenerationLocked(&s.routePlans[planIndex], storeNow, "")
	dec.SchedulingGeneration = s.routePlans[planIndex].SchedulingGeneration
	if dec.ID == "" {
		dec.ID = s.nextID("aswitch")
	}
	if dec.CreatedAt.IsZero() {
		dec.CreatedAt = storeNow
	}
	dec.UpdatedAt = storeNow
	leaseUntil := storeNow.Add(leaseDuration)
	dec.LeaseUntil = &leaseUntil
	s.autoSwitchDecs = append(s.autoSwitchDecs, dec)
	return dec, true, nil
}

// ClaimApprovedAutoSwitchDecision is the operator-execution counterpart of an
// automatic claim. The decision transition, plan generation advance, and lease
// installation share one lock, so concurrent execute requests have one owner.
func (s *MemoryStore) ClaimApprovedAutoSwitchDecision(ctx context.Context, id string, leaseDuration time.Duration) (contracts.AutoSwitchDecision, bool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	if _, err := autoSwitchLeaseMicros(leaseDuration); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	storeNow := s.now().UTC()
	for i := range s.autoSwitchDecs {
		current := s.autoSwitchDecs[i]
		if current.ID != id {
			continue
		}
		if current.Status != contracts.AutoSwitchApproved {
			return current, false, nil
		}
		planIndex := s.routePlanIndex(current.PlanID)
		if planIndex < 0 {
			return contracts.AutoSwitchDecision{}, false, ErrNotFound
		}
		if s.routePlans[planIndex].Status != contracts.RoutePlanPublished ||
			s.routePlans[planIndex].SchedulingGeneration != current.SchedulingGeneration {
			return contracts.AutoSwitchDecision{}, false, ErrConflict
		}
		if s.routePlanHasExecutingConnectorTaskLocked(current.PlanID) {
			return contracts.AutoSwitchDecision{}, false, ErrConflict
		}
		claimed := current
		claimed.Status = contracts.AutoSwitchApplying
		claimed.UpdatedAt = storeNow
		claimed.LeaseVersion = current.LeaseVersion + 1
		s.advanceRoutePlanGenerationLocked(&s.routePlans[planIndex], storeNow, current.ID)
		claimed.SchedulingGeneration = s.routePlans[planIndex].SchedulingGeneration
		leaseUntil := storeNow.Add(leaseDuration)
		claimed.LeaseUntil = &leaseUntil
		s.autoSwitchDecs[i] = claimed
		return claimed, true, nil
	}
	return contracts.AutoSwitchDecision{}, false, ErrNotFound
}

func (s *MemoryStore) ClaimAutoSwitchObservation(ctx context.Context, input contracts.AutoSwitchDecision, leaseDuration time.Duration) (contracts.AutoSwitchDecision, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	if _, err := autoSwitchLeaseMicros(leaseDuration); err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.autoSwitchDecs {
		current := s.autoSwitchDecs[i]
		if current.ID != input.ID {
			continue
		}
		if current.Status != contracts.AutoSwitchObserving {
			return contracts.AutoSwitchDecision{}, ErrConflict
		}
		planIndex := s.routePlanIndex(current.PlanID)
		if planIndex < 0 {
			return contracts.AutoSwitchDecision{}, ErrNotFound
		}
		if s.routePlans[planIndex].Status != contracts.RoutePlanPublished ||
			s.routePlans[planIndex].SchedulingGeneration != current.SchedulingGeneration {
			return contracts.AutoSwitchDecision{}, ErrConflict
		}
		if s.routePlanHasExecutingConnectorTaskLocked(current.PlanID) {
			return contracts.AutoSwitchDecision{}, ErrConflict
		}
		storeNow := s.now().UTC()
		claimed := current
		claimed.Status = contracts.AutoSwitchApplying
		claimed.UpdatedAt = storeNow
		claimed.LeaseVersion = current.LeaseVersion + 1
		s.advanceRoutePlanGenerationLocked(&s.routePlans[planIndex], storeNow, current.ID)
		claimed.SchedulingGeneration = s.routePlans[planIndex].SchedulingGeneration
		leaseUntil := storeNow.Add(leaseDuration)
		claimed.LeaseUntil = &leaseUntil
		s.autoSwitchDecs[i] = claimed
		return claimed, nil
	}
	return contracts.AutoSwitchDecision{}, ErrNotFound
}

func (s *MemoryStore) ClaimExpiredAutoSwitchDecision(
	ctx context.Context,
	id string,
	now, legacyStaleBefore, leaseUntil time.Time,
) (contracts.AutoSwitchDecision, bool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	leaseDuration := leaseUntil.Sub(now)
	legacyStaleAge := now.Sub(legacyStaleBefore)
	if _, err := autoSwitchLeaseMicros(leaseDuration); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	if legacyStaleAge < 0 {
		return contracts.AutoSwitchDecision{}, false, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	storeNow := s.now().UTC()
	for i := range s.autoSwitchDecs {
		current := s.autoSwitchDecs[i]
		if current.ID != id {
			continue
		}
		if current.Status != contracts.AutoSwitchApplying {
			return current, false, nil
		}
		planIndex := s.routePlanIndex(current.PlanID)
		if planIndex < 0 {
			return contracts.AutoSwitchDecision{}, false, ErrNotFound
		}
		if !routePlanStatusAllowed(s.routePlans[planIndex].Status, []contracts.RoutePlanStatus{contracts.RoutePlanPublished, contracts.RoutePlanSuspended}) ||
			s.routePlans[planIndex].SchedulingGeneration != current.SchedulingGeneration {
			return current, false, nil
		}
		expired := current.LeaseUntil != nil && !current.LeaseUntil.After(storeNow)
		legacyExpired := current.LeaseUntil == nil && !current.UpdatedAt.After(storeNow.Add(-legacyStaleAge))
		if !expired && !legacyExpired {
			return current, false, nil
		}
		if s.routePlanHasExecutingConnectorTaskLocked(current.PlanID) {
			return contracts.AutoSwitchDecision{}, false, ErrConflict
		}
		renewedUntil := storeNow.Add(leaseDuration)
		current.LeaseUntil = &renewedUntil
		current.LeaseVersion++
		s.advanceRoutePlanGenerationLocked(&s.routePlans[planIndex], storeNow, current.ID)
		current.SchedulingGeneration = s.routePlans[planIndex].SchedulingGeneration
		current.UpdatedAt = storeNow
		s.autoSwitchDecs[i] = current
		return current, true, nil
	}
	return contracts.AutoSwitchDecision{}, false, ErrNotFound
}

func (s *MemoryStore) RenewAutoSwitchDecisionLease(ctx context.Context, id string, leaseVersion int64, leaseDuration time.Duration) (contracts.AutoSwitchDecision, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	if _, err := autoSwitchLeaseMicros(leaseDuration); err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	storeNow := s.now().UTC()
	for i := range s.autoSwitchDecs {
		current := s.autoSwitchDecs[i]
		if current.ID != id {
			continue
		}
		if current.Status != contracts.AutoSwitchApplying || current.LeaseVersion != leaseVersion ||
			current.LeaseUntil == nil || !current.LeaseUntil.After(storeNow) {
			return contracts.AutoSwitchDecision{}, ErrConflict
		}
		planIndex := s.routePlanIndex(current.PlanID)
		if planIndex < 0 || s.routePlans[planIndex].SchedulingGeneration != current.SchedulingGeneration {
			return contracts.AutoSwitchDecision{}, ErrConflict
		}
		leaseUntil := storeNow.Add(leaseDuration)
		current.LeaseUntil = &leaseUntil
		current.UpdatedAt = storeNow
		s.autoSwitchDecs[i] = current
		return current, nil
	}
	return contracts.AutoSwitchDecision{}, ErrNotFound
}

func (s *MemoryStore) routePlanIndex(id string) int {
	for i := range s.routePlans {
		if s.routePlans[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *MemoryStore) supersedeActiveAutoSwitchDecisions(planID, keepDecisionID string, generation int64, now time.Time) {
	for i := range s.autoSwitchDecs {
		decision := &s.autoSwitchDecs[i]
		if decision.PlanID != planID || decision.ID == keepDecisionID || !decision.Status.IsActive() ||
			decision.SchedulingGeneration >= generation {
			continue
		}
		supersedeAutoSwitchDecision(decision, generation, now)
	}
}

func (s *MemoryStore) ReleaseAutoSwitchDecisionLease(ctx context.Context, id string, leaseVersion int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	storeNow := s.now().UTC()
	for i := range s.autoSwitchDecs {
		current := s.autoSwitchDecs[i]
		if current.ID != id {
			continue
		}
		if current.Status != contracts.AutoSwitchApplying || current.LeaseVersion != leaseVersion ||
			current.LeaseUntil == nil || !current.LeaseUntil.After(storeNow) {
			return ErrConflict
		}
		current.LeaseUntil = copyTimePointer(&storeNow)
		current.UpdatedAt = storeNow
		s.autoSwitchDecs[i] = current
		return nil
	}
	return ErrNotFound
}

func (s *MemoryStore) GetAutoSwitchDecision(ctx context.Context, id string) (contracts.AutoSwitchDecision, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.autoSwitchDecs {
		if d.ID == id {
			d.LeaseUntil = copyTimePointer(d.LeaseUntil)
			return d, nil
		}
	}
	return contracts.AutoSwitchDecision{}, ErrNotFound
}

func (s *MemoryStore) UpdateAutoSwitchDecision(ctx context.Context, input contracts.AutoSwitchDecision) (contracts.AutoSwitchDecision, error) {
	return contracts.AutoSwitchDecision{}, ErrConflict
}

// TransitionAutoSwitchDecision advances a decision only while it remains in
// expected. This is the in-memory compare-and-swap counterpart of the
// PostgreSQL conditional UPDATE.
func (s *MemoryStore) TransitionAutoSwitchDecision(ctx context.Context, input contracts.AutoSwitchDecision, expected contracts.AutoSwitchStatus) (contracts.AutoSwitchDecision, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.Status == contracts.AutoSwitchApplying {
		return contracts.AutoSwitchDecision{}, ErrConflict
	}
	storeNow := s.now().UTC()
	for i := range s.autoSwitchDecs {
		if s.autoSwitchDecs[i].ID != input.ID {
			continue
		}
		if s.autoSwitchDecs[i].Status != expected ||
			expected == contracts.AutoSwitchApplying && (s.autoSwitchDecs[i].LeaseVersion != input.LeaseVersion ||
				s.autoSwitchDecs[i].LeaseUntil == nil || !s.autoSwitchDecs[i].LeaseUntil.After(storeNow) ||
				s.routePlanGeneration(s.autoSwitchDecs[i].PlanID) != s.autoSwitchDecs[i].SchedulingGeneration) {
			return contracts.AutoSwitchDecision{}, ErrConflict
		}
		updated := input
		updated.CreatedAt = s.autoSwitchDecs[i].CreatedAt
		updated.LeaseVersion = s.autoSwitchDecs[i].LeaseVersion
		updated.SchedulingGeneration = s.autoSwitchDecs[i].SchedulingGeneration
		updated.UpdatedAt = storeNow
		updated.LeaseUntil = copyTimePointer(updated.LeaseUntil)
		s.autoSwitchDecs[i] = updated
		return updated, nil
	}
	return contracts.AutoSwitchDecision{}, ErrNotFound
}

func (s *MemoryStore) routePlanGeneration(id string) int64 {
	if index := s.routePlanIndex(id); index >= 0 {
		return s.routePlans[index].SchedulingGeneration
	}
	return -1
}

func (s *MemoryStore) ListAutoSwitchDecisions(ctx context.Context, filter contracts.AutoSwitchDecisionFilter) ([]contracts.AutoSwitchDecision, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.AutoSwitchDecision, 0, len(s.autoSwitchDecs))
	// Newest first: iterate in reverse insertion order.
	for i := len(s.autoSwitchDecs) - 1; i >= 0; i-- {
		d := s.autoSwitchDecs[i]
		if !filter.Matches(d) {
			continue
		}
		out = append(out, d)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

// FindActiveAutoSwitchDecisionByFingerprint returns the most recent still-active
// (non-terminal) decision for a plan with the given fingerprint, or ErrNotFound.
// This is the idempotency guard: the same failure window must not create a second
// equivalent live decision.
func (s *MemoryStore) FindActiveAutoSwitchDecisionByFingerprint(ctx context.Context, planID, fingerprint string) (contracts.AutoSwitchDecision, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	planIndex := s.routePlanIndex(planID)
	if planIndex < 0 {
		return contracts.AutoSwitchDecision{}, ErrNotFound
	}
	generation := s.routePlans[planIndex].SchedulingGeneration
	for i := len(s.autoSwitchDecs) - 1; i >= 0; i-- {
		d := s.autoSwitchDecs[i]
		if d.PlanID == planID && d.Fingerprint == fingerprint && d.Status.IsActive() && d.SchedulingGeneration == generation {
			return d, nil
		}
	}
	return contracts.AutoSwitchDecision{}, ErrNotFound
}

// --- Route strategies (Phase 5) ---

// strategyOwnerID returns the id that a strategy's scope binds to, so upsert can
// key by (scope, owner) and keep exactly one strategy per scoped target.
func strategyOwnerID(s contracts.RouteStrategy) string {
	switch s.Scope.Normalize() {
	case contracts.StrategyScopePlan:
		return s.PlanID
	case contracts.StrategyScopePool:
		return s.PoolID
	default:
		return strconv.FormatInt(s.UserID, 10)
	}
}

// UpsertRouteStrategy creates or replaces the strategy for a (scope, owner)
// target. An explicit ID updates that row; otherwise a matching scope+owner is
// replaced in place so re-saving a plan's strategy never accumulates duplicates.
func (s *MemoryStore) UpsertRouteStrategy(ctx context.Context, input contracts.RouteStrategy) (contracts.RouteStrategy, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RouteStrategy{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	rec := normalizeRouteStrategyRecord(input)
	owner := strategyOwnerID(rec)
	for i := range s.routeStrategies {
		match := (rec.ID != "" && s.routeStrategies[i].ID == rec.ID) ||
			(rec.ID == "" && s.routeStrategies[i].Scope == rec.Scope && strategyOwnerID(s.routeStrategies[i]) == owner)
		if match {
			rec.ID = s.routeStrategies[i].ID
			rec.CreatedAt = s.routeStrategies[i].CreatedAt
			rec.UpdatedAt = now
			s.routeStrategies[i] = rec
			return rec, nil
		}
	}
	if rec.ID == "" {
		rec.ID = s.nextID("strategy")
	}
	rec.CreatedAt, rec.UpdatedAt = now, now
	s.routeStrategies = append(s.routeStrategies, rec)
	return rec, nil
}

func (s *MemoryStore) GetRouteStrategy(ctx context.Context, id string) (contracts.RouteStrategy, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RouteStrategy{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, st := range s.routeStrategies {
		if st.ID == id {
			return st, nil
		}
	}
	return contracts.RouteStrategy{}, ErrNotFound
}

func (s *MemoryStore) ListRouteStrategies(ctx context.Context, filter contracts.RouteStrategyFilter) ([]contracts.RouteStrategy, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.RouteStrategy, 0, len(s.routeStrategies))
	for _, st := range s.routeStrategies {
		if filter.Matches(st) {
			out = append(out, st)
		}
	}
	return out, nil
}

func (s *MemoryStore) DeleteRouteStrategy(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.routeStrategies {
		if s.routeStrategies[i].ID == id {
			s.routeStrategies = append(s.routeStrategies[:i], s.routeStrategies[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}
