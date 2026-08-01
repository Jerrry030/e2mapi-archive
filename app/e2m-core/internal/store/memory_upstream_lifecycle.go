package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"e2m.local/contracts"
)

type memoryRotationRecord struct {
	currentRef      string
	previousRef     string
	previousMask    string
	previousVersion int64
	status          contracts.KeyRotationStatus
	resumeStatus    contracts.KeyRotationStatus
	startedAt       time.Time
}

type memoryLifecycleState struct {
	mu         sync.Mutex
	jobsMu     sync.Mutex
	inventory  map[string]contracts.UpstreamInventoryState
	safety     map[string]int
	rotations  map[string]memoryRotationRecord
	migrations []contracts.UpstreamChannelMigration
	jobs       map[string]contracts.PoolRetirementJob
	jobOrder   []string
}

var memoryLifecycleStates sync.Map // *MemoryStore -> *memoryLifecycleState

func memoryLifecycleFor(s *MemoryStore) *memoryLifecycleState {
	if state, ok := memoryLifecycleStates.Load(s); ok {
		return state.(*memoryLifecycleState)
	}
	created := &memoryLifecycleState{
		inventory: make(map[string]contracts.UpstreamInventoryState),
		safety:    make(map[string]int), rotations: make(map[string]memoryRotationRecord),
		jobs: make(map[string]contracts.PoolRetirementJob),
	}
	actual, _ := memoryLifecycleStates.LoadOrStore(s, created)
	return actual.(*memoryLifecycleState)
}

func (s *MemoryStore) SetUpstreamPoolSafetyStock(ctx context.Context, poolID string, threshold int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if threshold < 0 {
		return ErrInvalid
	}
	if _, err := s.GetUpstreamPool(ctx, poolID); err != nil {
		return err
	}
	state := memoryLifecycleFor(s)
	state.mu.Lock()
	state.safety[poolID] = threshold
	state.mu.Unlock()
	pool, err := s.GetUpstreamPool(ctx, poolID)
	if err == nil {
		pool.SafetyStockThreshold = threshold
		_, _ = s.UpdateUpstreamPool(ctx, pool)
	}
	return nil
}

func (s *MemoryStore) SetUpstreamInventoryState(ctx context.Context, channelID string, value contracts.UpstreamInventoryState) (contracts.UpstreamInventoryStateRecord, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamInventoryStateRecord{}, err
	}
	if !value.Valid() {
		return contracts.UpstreamInventoryStateRecord{}, ErrInvalid
	}
	channel, err := s.GetUpstreamChannel(ctx, channelID)
	if err != nil {
		return contracts.UpstreamInventoryStateRecord{}, err
	}
	if value == contracts.UpstreamInventoryRetired {
		allocations, allocErr := inventoryAllocations(ctx, s)
		if allocErr != nil {
			return contracts.UpstreamInventoryStateRecord{}, allocErr
		}
		if _, allocated := allocations[channelID]; allocated {
			return contracts.UpstreamInventoryStateRecord{}, ErrConflict
		}
	}
	now := s.now().UTC()
	state := memoryLifecycleFor(s)
	state.mu.Lock()
	state.inventory[channelID] = value
	state.mu.Unlock()
	channel.InventoryState = value
	s.mu.Lock()
	for i := range s.upstreamChannels {
		if s.upstreamChannels[i].ID == channelID {
			s.upstreamChannels[i].InventoryState = value
			s.upstreamChannels[i].UpdatedAt = now
			break
		}
	}
	s.mu.Unlock()
	return contracts.UpstreamInventoryStateRecord{ChannelID: channelID, State: value, UpdatedAt: now}, nil
}

func (s *MemoryStore) ImportUpstreamInventory(ctx context.Context, poolID string, entries []contracts.UpstreamInventoryImportEntry) (contracts.UpstreamInventoryImportResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamInventoryImportResult{}, err
	}
	poolID = strings.TrimSpace(poolID)
	if poolID == "" || len(entries) == 0 {
		return contracts.UpstreamInventoryImportResult{}, ErrInvalid
	}
	if _, err := s.GetUpstreamPool(ctx, poolID); err != nil {
		return contracts.UpstreamInventoryImportResult{}, err
	}
	for _, entry := range entries {
		channel := entry.Channel
		if channel.PoolID != "" && channel.PoolID != poolID || !contracts.IsUpstreamSourceIdentity(channel.SourceID) ||
			strings.TrimSpace(channel.DisplayName) == "" || channel.AccountOwnership.Normalize() != contracts.GatewayAccountPlatformManaged ||
			strings.TrimSpace(entry.SecretRef) == "" || strings.TrimSpace(entry.MaskedValue) == "" {
			return contracts.UpstreamInventoryImportResult{}, ErrInvalid
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	result := contracts.UpstreamInventoryImportResult{Channels: make([]contracts.UpstreamInventoryImportedChannel, 0, len(entries))}
	state := memoryLifecycleFor(s)
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, entry := range entries {
		channel := entry.Channel
		channel.PoolID = poolID
		channel.AccountOwnership = contracts.GatewayAccountPlatformManaged
		channel.Status = contracts.UpstreamChannelMaintenance
		channel.InventoryState = contracts.UpstreamInventoryDraft
		if channel.ID == "" {
			channel.ID = s.nextID("uchan")
		}
		for _, existing := range s.upstreamChannels {
			if existing.ID == channel.ID {
				return contracts.UpstreamInventoryImportResult{}, ErrDuplicate
			}
		}
		channel.CreatedAt, channel.UpdatedAt = now, now
		s.upstreamChannels = append(s.upstreamChannels, channel)
		s.keyDeliveries[channel.ID] = contracts.UpstreamKeyDelivery{
			ID: s.nextID("keydel"), ChannelID: channel.ID, SecretRef: entry.SecretRef,
			MaskedValue: entry.MaskedValue, KeyVersion: 1, ProofStatus: contracts.DeliveryKeyProofUnverified,
			CreatedAt: now, UpdatedAt: now,
		}
		state.inventory[channel.ID] = contracts.UpstreamInventoryDraft
		result.Channels = append(result.Channels, contracts.SafeImportedUpstreamChannel(channel))
	}
	result.Imported = len(result.Channels)
	return result, nil
}

func (s *MemoryStore) MigrateUpstreamChannel(ctx context.Context, channelID, targetPoolID, reason string, _ int64) (contracts.UpstreamChannelMigration, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return contracts.UpstreamChannelMigration{}, ErrInvalid
	}
	channel, err := s.GetUpstreamChannel(ctx, channelID)
	if err != nil {
		return contracts.UpstreamChannelMigration{}, err
	}
	if channel.PoolID == targetPoolID {
		return contracts.UpstreamChannelMigration{}, ErrConflict
	}
	if _, err := s.GetUpstreamPool(ctx, targetPoolID); err != nil {
		return contracts.UpstreamChannelMigration{}, err
	}
	from := channel.PoolID
	now := s.now().UTC()
	s.mu.Lock()
	found := false
	for i := range s.upstreamChannels {
		if s.upstreamChannels[i].ID == channelID {
			s.upstreamChannels[i].PoolID = targetPoolID
			s.upstreamChannels[i].UpdatedAt = now
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return contracts.UpstreamChannelMigration{}, ErrNotFound
	}
	migration := contracts.UpstreamChannelMigration{ChannelID: channelID, FromPoolID: from, ToPoolID: targetPoolID, Reason: reason, MigratedAt: now}
	state := memoryLifecycleFor(s)
	state.mu.Lock()
	state.migrations = append(state.migrations, migration)
	state.mu.Unlock()
	return migration, nil
}

func (s *MemoryStore) StartUpstreamKeyRotation(ctx context.Context, channelID, secretRef, maskedValue string) (contracts.UpstreamKeyRotation, error) {
	if strings.TrimSpace(secretRef) == "" || strings.TrimSpace(maskedValue) == "" {
		return contracts.UpstreamKeyRotation{}, ErrInvalid
	}
	state := memoryLifecycleFor(s)
	state.mu.Lock()
	defer state.mu.Unlock()
	if active, ok := state.rotations[channelID]; ok && active.status != contracts.KeyRotationStable {
		return contracts.UpstreamKeyRotation{}, ErrConflict
	}
	previous, err := s.GetUpstreamKeyDelivery(ctx, channelID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return contracts.UpstreamKeyRotation{}, err
	}
	next, err := s.UpsertUpstreamKeyDelivery(ctx, contracts.UpstreamKeyDelivery{ChannelID: channelID, SecretRef: secretRef, MaskedValue: maskedValue})
	if err != nil {
		return contracts.UpstreamKeyRotation{}, err
	}
	if errors.Is(err, ErrNotFound) || previous.ID == "" {
		return s.rotationView(ctx, next, memoryRotationRecord{currentRef: secretRef, status: contracts.KeyRotationStable})
	}
	now := s.now().UTC()
	record := memoryRotationRecord{
		currentRef: secretRef, previousRef: previous.SecretRef, previousMask: previous.MaskedValue,
		previousVersion: previous.KeyVersion, status: contracts.KeyRotationDeploying, startedAt: now,
	}
	state.rotations[channelID] = record
	return s.rotationView(ctx, next, record)
}

func (s *MemoryStore) GetUpstreamKeyRotation(ctx context.Context, channelID string) (contracts.UpstreamKeyRotation, error) {
	delivery, err := s.GetUpstreamKeyDelivery(ctx, channelID)
	if err != nil {
		return contracts.UpstreamKeyRotation{}, err
	}
	state := memoryLifecycleFor(s)
	state.mu.Lock()
	record, ok := state.rotations[channelID]
	state.mu.Unlock()
	if !ok {
		record = memoryRotationRecord{currentRef: delivery.SecretRef, status: contracts.KeyRotationStable}
	}
	return s.rotationView(ctx, delivery, record)
}

func (s *MemoryStore) rotationView(ctx context.Context, delivery contracts.UpstreamKeyDelivery, record memoryRotationRecord) (contracts.UpstreamKeyRotation, error) {
	view := contracts.UpstreamKeyRotation{
		ChannelID: delivery.ChannelID, CurrentKeyVersion: delivery.KeyVersion,
		CurrentMaskedValue: delivery.MaskedValue, PreviousKeyVersion: record.previousVersion,
		PreviousMaskedValue: record.previousMask, Status: record.status, UpdatedAt: delivery.UpdatedAt,
		CanRollback: record.previousRef != "" && (record.status == contracts.KeyRotationDeploying || record.status == contracts.KeyRotationRollingBack),
	}
	if !record.startedAt.IsZero() {
		started := record.startedAt
		view.StartedAt = &started
	}
	targets, err := rotationTargetInstances(ctx, s, delivery.ChannelID)
	if err != nil {
		return contracts.UpstreamKeyRotation{}, err
	}
	view.TargetInstances = len(targets)
	for _, instanceID := range targets {
		proof, proofErr := s.GetUpstreamKeyProofReceipt(ctx, delivery.ChannelID, instanceID)
		deployment, deploymentErr := s.GetUpstreamKeyDeployment(ctx, delivery.ChannelID, instanceID)
		if proofErr == nil && deploymentErr == nil && proof.KeyVersion == delivery.KeyVersion &&
			proof.Status == contracts.DeliveryKeyProofVerified && deployment.KeyVersion == delivery.KeyVersion &&
			deployment.Status == contracts.DeliveryKeyDeploymentDeployed {
			view.ConfirmedInstances++
		} else {
			view.PendingInstances = append(view.PendingInstances, instanceID)
		}
	}
	view.CanFinalize = record.previousRef != "" && view.ConfirmedInstances == view.TargetInstances &&
		(record.status == contracts.KeyRotationDeploying || record.status == contracts.KeyRotationRollingBack || record.status == contracts.KeyRotationFinalizing)
	return view, nil
}

func (s *MemoryStore) BeginUpstreamKeyRotationRollback(ctx context.Context, channelID string) (contracts.KeyRotationSecrets, error) {
	state := memoryLifecycleFor(s)
	state.mu.Lock()
	defer state.mu.Unlock()
	record, ok := state.rotations[channelID]
	if !ok || record.previousRef == "" || (record.status != contracts.KeyRotationDeploying && record.status != contracts.KeyRotationRollingBack) {
		return contracts.KeyRotationSecrets{}, ErrConflict
	}
	current, err := s.GetUpstreamKeyDelivery(ctx, channelID)
	if err != nil {
		return contracts.KeyRotationSecrets{}, err
	}
	reverted, err := s.UpsertUpstreamKeyDelivery(ctx, contracts.UpstreamKeyDelivery{ChannelID: channelID, SecretRef: record.previousRef, MaskedValue: record.previousMask})
	if err != nil {
		return contracts.KeyRotationSecrets{}, err
	}
	now := s.now().UTC()
	nextRecord := memoryRotationRecord{
		currentRef: record.previousRef, previousRef: current.SecretRef, previousMask: current.MaskedValue,
		previousVersion: current.KeyVersion, status: contracts.KeyRotationRollingBack, startedAt: now,
	}
	state.rotations[channelID] = nextRecord
	view, err := s.rotationView(ctx, reverted, nextRecord)
	return contracts.KeyRotationSecrets{Rotation: view, CurrentSecretRef: nextRecord.currentRef, PreviousSecretRef: nextRecord.previousRef}, err
}

func (s *MemoryStore) BeginUpstreamKeyRotationFinalize(ctx context.Context, channelID string) (contracts.KeyRotationSecrets, error) {
	view, err := s.GetUpstreamKeyRotation(ctx, channelID)
	if err != nil {
		return contracts.KeyRotationSecrets{}, err
	}
	if !view.CanFinalize {
		return contracts.KeyRotationSecrets{}, ErrConflict
	}
	state := memoryLifecycleFor(s)
	state.mu.Lock()
	defer state.mu.Unlock()
	record, ok := state.rotations[channelID]
	if !ok || record.previousRef == "" {
		return contracts.KeyRotationSecrets{}, ErrConflict
	}
	if record.status != contracts.KeyRotationFinalizing {
		if record.status != contracts.KeyRotationDeploying && record.status != contracts.KeyRotationRollingBack {
			return contracts.KeyRotationSecrets{}, ErrConflict
		}
		record.resumeStatus = record.status
		record.status = contracts.KeyRotationFinalizing
		state.rotations[channelID] = record
	}
	view.Status = contracts.KeyRotationFinalizing
	return contracts.KeyRotationSecrets{Rotation: view, CurrentSecretRef: record.currentRef, PreviousSecretRef: record.previousRef}, nil
}

func (s *MemoryStore) CompleteUpstreamKeyRotationFinalize(ctx context.Context, channelID string, expectedVersion int64) (contracts.UpstreamKeyRotation, error) {
	delivery, err := s.GetUpstreamKeyDelivery(ctx, channelID)
	if err != nil {
		return contracts.UpstreamKeyRotation{}, err
	}
	if delivery.KeyVersion != expectedVersion {
		return contracts.UpstreamKeyRotation{}, ErrConflict
	}
	state := memoryLifecycleFor(s)
	state.mu.Lock()
	record, ok := state.rotations[channelID]
	if !ok || record.status != contracts.KeyRotationFinalizing {
		state.mu.Unlock()
		return contracts.UpstreamKeyRotation{}, ErrConflict
	}
	delete(state.rotations, channelID)
	state.mu.Unlock()
	return s.rotationView(ctx, delivery, memoryRotationRecord{currentRef: delivery.SecretRef, status: contracts.KeyRotationStable})
}

func (s *MemoryStore) AbortUpstreamKeyRotationFinalize(ctx context.Context, channelID string, expectedVersion int64) error {
	delivery, err := s.GetUpstreamKeyDelivery(ctx, channelID)
	if err != nil {
		return err
	}
	if delivery.KeyVersion != expectedVersion {
		return ErrConflict
	}
	state := memoryLifecycleFor(s)
	state.mu.Lock()
	defer state.mu.Unlock()
	record, ok := state.rotations[channelID]
	if !ok || record.status != contracts.KeyRotationFinalizing {
		return ErrConflict
	}
	if record.resumeStatus != contracts.KeyRotationDeploying && record.resumeStatus != contracts.KeyRotationRollingBack {
		return ErrConflict
	}
	record.status = record.resumeStatus
	record.resumeStatus = ""
	state.rotations[channelID] = record
	return nil
}

func rotationTargetInstances(ctx context.Context, st Store, channelID string) ([]string, error) {
	plans, err := st.ListRoutePlans(ctx, 0)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{})
	for _, plan := range plans {
		bindings, err := st.ListPublishedBindings(ctx, plan.ID)
		if err != nil {
			return nil, err
		}
		for _, binding := range bindings {
			if binding.ChannelID == channelID && binding.State != contracts.BindingRevoked {
				instanceID := strings.TrimSpace(binding.InstanceID)
				if instanceID == "" {
					instanceID = strings.TrimSpace(plan.InstanceID)
				}
				if instanceID != "" {
					set[instanceID] = struct{}{}
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func (s *MemoryStore) GetUpstreamInventory(ctx context.Context, poolID string) (contracts.UpstreamInventorySnapshot, error) {
	channels, err := s.ListUpstreamChannels(ctx, poolID)
	if err != nil {
		return contracts.UpstreamInventorySnapshot{}, err
	}
	pools, err := s.ListUpstreamPools(ctx)
	if err != nil {
		return contracts.UpstreamInventorySnapshot{}, err
	}
	deliveries, err := s.ListUpstreamKeyDeliveries(ctx)
	if err != nil {
		return contracts.UpstreamInventorySnapshot{}, err
	}
	deliveryByChannel := make(map[string]contracts.UpstreamKeyDelivery, len(deliveries))
	for _, delivery := range deliveries {
		deliveryByChannel[delivery.ChannelID] = delivery
	}
	state := memoryLifecycleFor(s)
	state.mu.Lock()
	inventoryStates := make(map[string]contracts.UpstreamInventoryState, len(state.inventory))
	for id, value := range state.inventory {
		inventoryStates[id] = value
	}
	safety := make(map[string]int, len(state.safety))
	for id, value := range state.safety {
		safety[id] = value
	}
	state.mu.Unlock()

	allocations, err := inventoryAllocations(ctx, s)
	if err != nil {
		return contracts.UpstreamInventorySnapshot{}, err
	}
	snapshot := contracts.UpstreamInventorySnapshot{AsOf: s.now().UTC()}
	for _, pool := range pools {
		if poolID != "" && pool.ID != poolID {
			continue
		}
		threshold := pool.SafetyStockThreshold
		if override, ok := safety[pool.ID]; ok {
			threshold = override
		}
		summary := contracts.UpstreamPoolInventorySummary{PoolID: pool.ID, SafetyStockThreshold: threshold}
		snapshot.Pools = append(snapshot.Pools, summary)
	}
	// Build pointers only after the slice has reached its final length. Taking
	// element addresses while append is still allowed can leave the map pointing
	// at an old backing array after a capacity growth.
	summaryByPool := make(map[string]*contracts.UpstreamPoolInventorySummary, len(snapshot.Pools))
	for i := range snapshot.Pools {
		summaryByPool[snapshot.Pools[i].PoolID] = &snapshot.Pools[i]
	}
	for _, channel := range channels {
		invState := channel.InventoryState
		if override := inventoryStates[channel.ID]; override != "" {
			invState = override
		}
		if invState == "" {
			invState = contracts.UpstreamInventoryReady
		}
		item := contracts.UpstreamInventoryItem{Channel: channel, InventoryState: invState}
		if allocation, ok := allocations[channel.ID]; ok {
			item.Allocated, item.AllocatedUserID, item.FirstPlanID = true, allocation.userID, allocation.planID
			allocatedAt := allocation.createdAt
			item.AllocatedAt = &allocatedAt
		}
		summary := summaryByPool[channel.PoolID]
		if summary == nil {
			// A channel without its pool is invalid catalog state. Keep the
			// projection safe instead of dereferencing a missing summary.
			continue
		}
		if delivery, ok := deliveryByChannel[channel.ID]; ok {
			safe := delivery
			safe.SecretRef = ""
			item.Delivery = &safe
			if delivery.ProofStatus == contracts.DeliveryKeyProofUnverified {
				summary.ProofUnverified++
			}
			if delivery.ProofStatus == contracts.DeliveryKeyProofMismatch {
				summary.ProofMismatch++
			}
		}
		targets, targetErr := rotationTargetInstances(ctx, s, channel.ID)
		if targetErr != nil {
			return contracts.UpstreamInventorySnapshot{}, targetErr
		}
		item.TargetInstances = len(targets)
		for _, instanceID := range targets {
			if receipt, receiptErr := s.GetUpstreamKeyProofReceipt(ctx, channel.ID, instanceID); receiptErr == nil {
				if receipt.Status == contracts.DeliveryKeyProofVerified {
					item.ProofVerified++
				}
				if receipt.Status == contracts.DeliveryKeyProofMismatch {
					item.ProofMismatch++
				}
			}
			if deployment, deploymentErr := s.GetUpstreamKeyDeployment(ctx, channel.ID, instanceID); deploymentErr == nil {
				switch deployment.Status {
				case contracts.DeliveryKeyDeploymentDeployed:
					item.DeploymentsDeployed++
				case contracts.DeliveryKeyDeploymentPending:
					item.DeploymentsPending++
				case contracts.DeliveryKeyDeploymentFailed:
					item.DeploymentsFailed++
				}
			}
		}
		summary.Total++
		if item.Allocated {
			summary.Allocated++
		}
		summary.DeploymentsFailed += item.DeploymentsFailed
		switch invState {
		case contracts.UpstreamInventoryDraft:
			summary.Draft++
		case contracts.UpstreamInventoryTesting:
			summary.Testing++
		case contracts.UpstreamInventoryReady:
			summary.Ready++
			if !item.Allocated && channel.Status == contracts.UpstreamChannelActive &&
				channel.AccountOwnership.Normalize() == contracts.GatewayAccountPlatformManaged {
				summary.Available++
			}
		case contracts.UpstreamInventoryQuarantined:
			summary.Quarantined++
		case contracts.UpstreamInventoryRetired:
			summary.Retired++
		}
		snapshot.Items = append(snapshot.Items, item)
	}
	for i := range snapshot.Pools {
		summary := &snapshot.Pools[i]
		summary.BelowSafetyStock = summary.SafetyStockThreshold > 0 && summary.Available < summary.SafetyStockThreshold
		if summary.BelowSafetyStock {
			snapshot.Alerts = append(snapshot.Alerts, contracts.UpstreamInventoryAlert{
				PoolID: summary.PoolID, Code: "safety_stock_low", Available: summary.Available,
				Threshold: summary.SafetyStockThreshold,
				Message:   fmt.Sprintf("available inventory %d is below safety stock %d", summary.Available, summary.SafetyStockThreshold),
			})
		}
	}
	return snapshot, nil
}

type inventoryAllocation struct {
	userID    int64
	planID    string
	createdAt time.Time
}

func inventoryAllocations(ctx context.Context, st Store) (map[string]inventoryAllocation, error) {
	plans, err := st.ListRoutePlans(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make(map[string]inventoryAllocation)
	for _, plan := range plans {
		bindings, err := st.ListPublishedBindings(ctx, plan.ID)
		if err != nil {
			return nil, err
		}
		for _, binding := range bindings {
			current, exists := out[binding.ChannelID]
			if !exists || binding.CreatedAt.Before(current.createdAt) {
				out[binding.ChannelID] = inventoryAllocation{userID: plan.UserID, planID: plan.ID, createdAt: binding.CreatedAt}
			}
		}
	}
	return out, nil
}

func (s *MemoryStore) CreatePoolRetirementJob(ctx context.Context, poolID string, createdBy int64) (contracts.PoolRetirementJob, error) {
	state := memoryLifecycleFor(s)
	now := s.now().UTC()
	s.mu.Lock()
	state.jobsMu.Lock()
	defer state.jobsMu.Unlock()
	defer s.mu.Unlock()
	for _, existing := range state.jobs {
		if existing.PoolID == poolID && existing.Status != contracts.PoolRetirementCompleted {
			return contracts.PoolRetirementJob{}, ErrConflict
		}
	}
	job := contracts.PoolRetirementJob{ID: s.nextID("poolret"), PoolID: poolID, Status: contracts.PoolRetirementPending, CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now}
	poolFound := false
	for i := range s.upstreamPools {
		if s.upstreamPools[i].ID != poolID || s.upstreamPools[i].Status == contracts.UpstreamPoolRetired {
			continue
		}
		s.upstreamPools[i].Status = contracts.UpstreamPoolMaintenance
		s.upstreamPools[i].UpdatedAt = now
		poolFound = true
		break
	}
	if !poolFound {
		return contracts.PoolRetirementJob{}, ErrNotFound
	}
	for i := range s.poolRolloutOps {
		operation := &s.poolRolloutOps[i]
		if operation.PoolID == poolID && operation.Action == contracts.PoolRolloutOperationPublish &&
			(operation.Status == contracts.PoolRolloutOperationPending || operation.Status == contracts.PoolRolloutOperationFailed || operation.Status == contracts.PoolRolloutOperationRunning) {
			operation.Status = contracts.PoolRolloutOperationSuperseded
			operation.LastError = "pool retirement started"
			operation.Version++
			operation.LeaseOwner = ""
			operation.LeaseUntil = nil
			operation.UpdatedAt = now
		}
	}
	for _, plan := range s.routePlans {
		if plan.PoolID == poolID {
			job.Items = append(job.Items, contracts.PoolRetirementItem{
				JobID: job.ID, PlanID: plan.ID, InstanceID: plan.InstanceID,
				Status: contracts.PoolRetirementItemPending, CleanupStatus: contracts.PoolRetirementCleanupPending,
				UpdatedAt: now,
			})
		}
	}
	job.TotalPlans = len(job.Items)
	if job.TotalPlans == 0 {
		job.Status = contracts.PoolRetirementFinalizing
	}
	state.jobs[job.ID] = job
	state.jobOrder = append(state.jobOrder, job.ID)
	return job, nil
}

func (s *MemoryStore) GetPoolRetirementJob(ctx context.Context, id string) (contracts.PoolRetirementJob, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	state := memoryLifecycleFor(s)
	state.jobsMu.Lock()
	defer state.jobsMu.Unlock()
	job, ok := state.jobs[id]
	if !ok {
		return contracts.PoolRetirementJob{}, ErrNotFound
	}
	job.Items = append([]contracts.PoolRetirementItem(nil), job.Items...)
	return job, nil
}

func (s *MemoryStore) ListPoolRetirementJobs(ctx context.Context, poolID string) ([]contracts.PoolRetirementJob, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state := memoryLifecycleFor(s)
	state.jobsMu.Lock()
	defer state.jobsMu.Unlock()
	out := make([]contracts.PoolRetirementJob, 0)
	for i := len(state.jobOrder) - 1; i >= 0; i-- {
		job := state.jobs[state.jobOrder[i]]
		if poolID == "" || job.PoolID == poolID {
			job.Items = append([]contracts.PoolRetirementItem(nil), job.Items...)
			out = append(out, job)
		}
	}
	return out, nil
}

func (s *MemoryStore) ClaimPoolRetirementItem(ctx context.Context, jobID string) (contracts.PoolRetirementItem, bool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRetirementItem{}, false, err
	}
	state := memoryLifecycleFor(s)
	state.jobsMu.Lock()
	defer state.jobsMu.Unlock()
	job, ok := state.jobs[jobID]
	if !ok {
		return contracts.PoolRetirementItem{}, false, ErrNotFound
	}
	now := s.now().UTC()
	for i := range job.Items {
		claimable := job.Items[i].Status == contracts.PoolRetirementItemPending ||
			job.Items[i].Status == contracts.PoolRetirementItemFailed ||
			(job.Items[i].Status == contracts.PoolRetirementItemRunning &&
				(job.Items[i].LeaseUntil == nil || !job.Items[i].LeaseUntil.After(now)))
		if claimable {
			job.Items[i].Status = contracts.PoolRetirementItemRunning
			job.Items[i].LastError = ""
			job.Items[i].Attempts++
			leaseUntil := now.Add(2 * time.Minute)
			job.Items[i].LeaseUntil = &leaseUntil
			job.Items[i].UpdatedAt = now
			job.Status = contracts.PoolRetirementRunning
			job.UpdatedAt = job.Items[i].UpdatedAt
			state.jobs[jobID] = job
			return job.Items[i], true, nil
		}
	}
	return contracts.PoolRetirementItem{}, false, nil
}

func (s *MemoryStore) RenewPoolRetirementItem(ctx context.Context, jobID, planID string, expectedAttempts int, lease time.Duration) (contracts.PoolRetirementItem, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRetirementItem{}, err
	}
	if strings.TrimSpace(jobID) == "" || strings.TrimSpace(planID) == "" || expectedAttempts <= 0 || lease <= 0 {
		return contracts.PoolRetirementItem{}, ErrInvalid
	}
	state := memoryLifecycleFor(s)
	state.jobsMu.Lock()
	defer state.jobsMu.Unlock()
	job, ok := state.jobs[jobID]
	if !ok {
		return contracts.PoolRetirementItem{}, ErrNotFound
	}
	now := s.now().UTC()
	for i := range job.Items {
		item := &job.Items[i]
		if item.PlanID != planID {
			continue
		}
		if item.Status != contracts.PoolRetirementItemRunning || item.Attempts != expectedAttempts ||
			item.LeaseUntil == nil || !item.LeaseUntil.After(now) {
			return contracts.PoolRetirementItem{}, ErrConflict
		}
		leaseUntil := now.Add(lease)
		item.LeaseUntil = &leaseUntil
		item.UpdatedAt = now
		job.UpdatedAt = now
		state.jobs[jobID] = job
		return *item, nil
	}
	return contracts.PoolRetirementItem{}, ErrNotFound
}

func (s *MemoryStore) CompletePoolRetirementItem(ctx context.Context, jobID, planID string, expectedAttempts int, errorMessage string) (contracts.PoolRetirementJob, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	if strings.TrimSpace(jobID) == "" || strings.TrimSpace(planID) == "" || expectedAttempts <= 0 {
		return contracts.PoolRetirementJob{}, ErrInvalid
	}
	state := memoryLifecycleFor(s)
	state.jobsMu.Lock()
	defer state.jobsMu.Unlock()
	job, ok := state.jobs[jobID]
	if !ok {
		return contracts.PoolRetirementJob{}, ErrNotFound
	}
	found := false
	now := s.now().UTC()
	for i := range job.Items {
		if job.Items[i].PlanID == planID {
			if job.Items[i].Status != contracts.PoolRetirementItemRunning ||
				job.Items[i].Attempts != expectedAttempts || job.Items[i].LeaseUntil == nil ||
				!job.Items[i].LeaseUntil.After(now) {
				return contracts.PoolRetirementJob{}, ErrConflict
			}
			found = true
			job.Items[i].UpdatedAt = now
			job.Items[i].LastError = strings.TrimSpace(errorMessage)
			job.Items[i].LeaseUntil = nil
			if job.Items[i].LastError == "" {
				job.Items[i].Status = contracts.PoolRetirementItemCompleted
			} else {
				job.Items[i].Status = contracts.PoolRetirementItemFailed
			}
			break
		}
	}
	if !found {
		return contracts.PoolRetirementJob{}, ErrNotFound
	}
	job.CompletedPlans, job.FailedPlans, job.LastError = 0, 0, ""
	pending := 0
	for _, item := range job.Items {
		switch item.Status {
		case contracts.PoolRetirementItemCompleted:
			job.CompletedPlans++
		case contracts.PoolRetirementItemFailed:
			job.FailedPlans++
			job.LastError = item.LastError
		default:
			pending++
		}
	}
	job.UpdatedAt = now
	if pending > 0 {
		job.Status = contracts.PoolRetirementRunning
	} else if job.FailedPlans > 0 {
		job.Status = contracts.PoolRetirementPartial
	} else {
		job.Status = contracts.PoolRetirementFinalizing
	}
	state.jobs[jobID] = job
	return job, nil
}

func (s *MemoryStore) FinalizePoolRetirementJob(ctx context.Context, jobID string) (contracts.PoolRetirementJob, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	state := memoryLifecycleFor(s)
	state.jobsMu.Lock()
	job, ok := state.jobs[jobID]
	if !ok {
		state.jobsMu.Unlock()
		return contracts.PoolRetirementJob{}, ErrNotFound
	}
	if job.Status != contracts.PoolRetirementFinalizing || job.FailedPlans != 0 ||
		(job.TotalPlans > 0 && job.CompletedPlans != job.TotalPlans) {
		state.jobsMu.Unlock()
		return contracts.PoolRetirementJob{}, ErrConflict
	}
	state.jobsMu.Unlock()
	now := s.now().UTC()
	s.mu.Lock()
	found := false
	for i := range s.upstreamPools {
		if s.upstreamPools[i].ID == job.PoolID {
			s.upstreamPools[i].Status = contracts.UpstreamPoolRetired
			s.upstreamPools[i].UpdatedAt = now
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return contracts.PoolRetirementJob{}, ErrNotFound
	}
	state.jobsMu.Lock()
	job = state.jobs[jobID]
	job.Status = contracts.PoolRetirementCleanup
	job.CompletedAt = nil
	if job.TotalPlans == 0 {
		job.Status = contracts.PoolRetirementCompleted
		job.CompletedAt = &now
	}
	job.UpdatedAt = now
	state.jobs[jobID] = job
	state.jobsMu.Unlock()
	return job, nil
}

func (s *MemoryStore) ClaimPoolRetirementCleanupItem(ctx context.Context, jobID string) (contracts.PoolRetirementItem, bool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRetirementItem{}, false, err
	}
	state := memoryLifecycleFor(s)
	state.jobsMu.Lock()
	defer state.jobsMu.Unlock()
	job, ok := state.jobs[jobID]
	if !ok {
		return contracts.PoolRetirementItem{}, false, ErrNotFound
	}
	if job.Status != contracts.PoolRetirementCleanup {
		return contracts.PoolRetirementItem{}, false, nil
	}
	now := s.now().UTC()
	for i := range job.Items {
		claimable := job.Items[i].Status == contracts.PoolRetirementItemCompleted &&
			(job.Items[i].CleanupStatus == contracts.PoolRetirementCleanupPending ||
				job.Items[i].CleanupStatus == contracts.PoolRetirementCleanupFailed ||
				(job.Items[i].CleanupStatus == contracts.PoolRetirementCleanupRunning &&
					(job.Items[i].CleanupLeaseUntil == nil || !job.Items[i].CleanupLeaseUntil.After(now))))
		if !claimable {
			continue
		}
		job.Items[i].CleanupStatus = contracts.PoolRetirementCleanupRunning
		job.Items[i].CleanupLastError = ""
		job.Items[i].CleanupAttempts++
		leaseUntil := now.Add(2 * time.Minute)
		job.Items[i].CleanupLeaseUntil = &leaseUntil
		job.Items[i].UpdatedAt = now
		job.UpdatedAt = now
		state.jobs[jobID] = job
		return job.Items[i], true, nil
	}
	return contracts.PoolRetirementItem{}, false, nil
}

func (s *MemoryStore) RenewPoolRetirementCleanupItem(ctx context.Context, jobID, planID string, expectedAttempts int, lease time.Duration) (contracts.PoolRetirementItem, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRetirementItem{}, err
	}
	if strings.TrimSpace(jobID) == "" || strings.TrimSpace(planID) == "" || expectedAttempts <= 0 || lease <= 0 {
		return contracts.PoolRetirementItem{}, ErrInvalid
	}
	state := memoryLifecycleFor(s)
	state.jobsMu.Lock()
	defer state.jobsMu.Unlock()
	job, ok := state.jobs[jobID]
	if !ok {
		return contracts.PoolRetirementItem{}, ErrNotFound
	}
	now := s.now().UTC()
	for i := range job.Items {
		item := &job.Items[i]
		if item.PlanID != planID {
			continue
		}
		if job.Status != contracts.PoolRetirementCleanup || item.Status != contracts.PoolRetirementItemCompleted ||
			item.CleanupStatus != contracts.PoolRetirementCleanupRunning || item.CleanupAttempts != expectedAttempts ||
			item.CleanupLeaseUntil == nil || !item.CleanupLeaseUntil.After(now) {
			return contracts.PoolRetirementItem{}, ErrConflict
		}
		leaseUntil := now.Add(lease)
		item.CleanupLeaseUntil = &leaseUntil
		item.UpdatedAt = now
		job.UpdatedAt = now
		state.jobs[jobID] = job
		return *item, nil
	}
	return contracts.PoolRetirementItem{}, ErrNotFound
}

func (s *MemoryStore) CompletePoolRetirementCleanupItem(ctx context.Context, jobID, planID string, expectedAttempts int, errorMessage string) (contracts.PoolRetirementJob, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	if strings.TrimSpace(jobID) == "" || strings.TrimSpace(planID) == "" || expectedAttempts <= 0 {
		return contracts.PoolRetirementJob{}, ErrInvalid
	}
	state := memoryLifecycleFor(s)
	state.jobsMu.Lock()
	defer state.jobsMu.Unlock()
	job, ok := state.jobs[jobID]
	if !ok {
		return contracts.PoolRetirementJob{}, ErrNotFound
	}
	if job.Status != contracts.PoolRetirementCleanup {
		return contracts.PoolRetirementJob{}, ErrConflict
	}
	now := s.now().UTC()
	found := false
	for i := range job.Items {
		if job.Items[i].PlanID != planID {
			continue
		}
		if job.Items[i].Status != contracts.PoolRetirementItemCompleted ||
			job.Items[i].CleanupStatus != contracts.PoolRetirementCleanupRunning ||
			job.Items[i].CleanupAttempts != expectedAttempts || job.Items[i].CleanupLeaseUntil == nil ||
			!job.Items[i].CleanupLeaseUntil.After(now) {
			return contracts.PoolRetirementJob{}, ErrConflict
		}
		found = true
		job.Items[i].CleanupLastError = strings.TrimSpace(errorMessage)
		job.Items[i].CleanupLeaseUntil = nil
		job.Items[i].UpdatedAt = now
		if job.Items[i].CleanupLastError == "" {
			job.Items[i].CleanupStatus = contracts.PoolRetirementCleanupCompleted
		} else {
			job.Items[i].CleanupStatus = contracts.PoolRetirementCleanupFailed
		}
		break
	}
	if !found {
		return contracts.PoolRetirementJob{}, ErrNotFound
	}
	job.CleanupCompletedPlans, job.CleanupFailedPlans, job.LastError = 0, 0, ""
	pending := 0
	for _, item := range job.Items {
		switch item.CleanupStatus {
		case contracts.PoolRetirementCleanupCompleted:
			job.CleanupCompletedPlans++
		case contracts.PoolRetirementCleanupFailed:
			job.CleanupFailedPlans++
			job.LastError = item.CleanupLastError
		default:
			pending++
		}
	}
	job.Status = contracts.PoolRetirementCleanup
	job.CompletedAt = nil
	if pending == 0 && job.CleanupFailedPlans == 0 && job.CleanupCompletedPlans == job.TotalPlans {
		job.Status = contracts.PoolRetirementCompleted
		job.CompletedAt = &now
	}
	job.UpdatedAt = now
	state.jobs[jobID] = job
	return job, nil
}
