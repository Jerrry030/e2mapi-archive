package hybridrouting

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/adapters"
	"e2m.local/core/internal/store"
)

const (
	DefaultWorkerInterval = time.Second
	DefaultWorkerLease    = 2 * time.Minute
)

const (
	errorAllocationStale        = "allocation_stale"
	errorBindingUnavailable     = "binding_unavailable"
	errorGatewayReadFailed      = "gateway_read_failed"
	errorModelUnrepresentable   = "model_unrepresentable"
	errorSchedulingConflict     = "scheduling_conflict"
	errorRoutingUnrepresentable = "routing_unrepresentable"
	errorCapacityUnallocated    = "capacity_unallocated"
	errorWriteFailed            = "write_failed"
	reasonOwnerUnavailable      = "owner_unavailable"
	reasonWalletUnavailable     = "platform_wallet_unavailable"
	reasonPlatformUnavailable   = "platform_supply_unavailable"
	reasonVirtualKeyUnavailable = "platform_virtual_key_unavailable"
	reasonPlatformPriceLimited  = "platform_price_limited"
	reasonPlatformBudgetLimited = "platform_budget_limited"
)

// Gateway is the exact account/weight surface. Orchestrator implements it and
// retains Connector permit, persisted binding classification and L1 audit.
type Gateway interface {
	ListAccounts(context.Context, string) ([]contracts.GatewayAccount, error)
	SetSchedulable(context.Context, string, string, bool, string) error
	SetTrafficShare(context.Context, string, string, int, string) error
}

type Worker struct {
	store    store.HybridRoutingStore
	gateway  Gateway
	workerID string
	interval time.Duration
	lease    time.Duration
	now      func() time.Time
}

func NewWorker(st store.HybridRoutingStore, gateway Gateway, workerID string, interval, lease time.Duration) (*Worker, error) {
	workerID = strings.TrimSpace(workerID)
	if st == nil || gateway == nil || workerID == "" || len(workerID) > 128 || contracts.LooksLikeConnectorSensitiveValue(workerID) {
		return nil, errors.New("hybrid routing worker: invalid dependency or worker id")
	}
	if interval <= 0 {
		interval = DefaultWorkerInterval
	}
	if lease <= 0 {
		lease = DefaultWorkerLease
	}
	return &Worker{store: st, gateway: gateway, workerID: workerID, interval: interval, lease: lease, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (w *Worker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	w.RunOnce(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) {
	for ctx.Err() == nil {
		execution, claimed, err := w.store.ClaimHybridRoutingExecution(ctx, w.workerID, w.lease)
		if err != nil {
			log.Printf("hybrid routing worker: claim failed: %v", err)
			return
		}
		if !claimed {
			return
		}
		w.process(ctx, execution)
	}
}

func (w *Worker) process(ctx context.Context, execution contracts.HybridRoutingExecution) {
	version := execution.Version
	renew := func() error {
		owned, err := w.store.RenewHybridRoutingExecution(ctx, execution.ID, w.workerID, version, w.lease)
		if err == nil {
			version = owned.Version
		}
		return err
	}
	fail := func(code string) {
		_, err := w.store.CompleteHybridRoutingExecution(ctx, contracts.HybridRoutingExecutionCompletion{
			ID: execution.ID, WorkerID: w.workerID, ExpectedVersion: version, ErrorCode: code,
		})
		if err != nil && ctx.Err() == nil {
			log.Printf("hybrid routing worker: record %s for %s failed: %v", code, execution.ID, err)
		}
	}

	allocation, err := w.store.GetHybridAllocation(ctx, execution.InstanceID)
	if err != nil || allocation.UserID != execution.UserID || allocation.Version != execution.AllocationVersion ||
		allocation.RoutingGeneration != execution.Generation {
		fail(errorAllocationStale)
		return
	}
	instance, err := w.store.GetInstance(ctx, execution.InstanceID)
	if err != nil || instance.UserID != execution.UserID || instance.Kind != contracts.InstanceKindNewAPI || strings.TrimSpace(instance.ConnectorID) == "" {
		fail(errorBindingUnavailable)
		return
	}
	bindings, err := w.store.ListHybridGatewayBindings(ctx, execution.UserID, execution.InstanceID)
	if err != nil {
		fail(errorBindingUnavailable)
		return
	}
	accounts, err := w.gateway.ListAccounts(ctx, execution.InstanceID)
	if err != nil {
		fail(errorGatewayReadFailed)
		return
	}
	published, err := w.store.ListPublishedBindings(ctx, "")
	if err != nil {
		fail(errorBindingUnavailable)
		return
	}
	previous, err := w.store.ListHybridRoutingExecutions(ctx, execution.UserID, execution.InstanceID, 100)
	if err != nil {
		fail(errorBindingUnavailable)
		return
	}
	managedOwners := previouslyManagedOwnerIDs(execution, previous)
	inventory, code := w.buildInventory(ctx, instance, allocation, execution.Model, bindings, published, managedOwners, accounts)
	if code != "" {
		fail(code)
		return
	}
	desired := append([]contracts.HybridAccountWeight(nil), execution.DesiredWeights...)
	effectivePercent := execution.Effective
	if len(desired) == 0 {
		states := w.resourceStates(ctx, allocation, inventory)
		effective := contracts.CompileEffectiveAllocation(allocation.RuleForModel(execution.Model), states)
		if effective.Unallocated != 0 || !contracts.ValidCompleteHybridPercentMap(effective.Effective) {
			fail(errorCapacityUnallocated)
			return
		}
		desired, err = contracts.CompileHybridAccountWeights(effective.Effective, inventory.Accounts)
		if err != nil {
			fail(errorRoutingUnrepresentable)
			return
		}
		planned, planErr := w.store.PlanHybridRoutingExecution(ctx, contracts.HybridRoutingExecutionPlan{
			ID: execution.ID, WorkerID: w.workerID, ExpectedVersion: version,
			Target: effective.Target, Effective: effective.Effective, DesiredWeights: desired,
			AdjustmentCodes: effective.AdjustmentCodes,
		})
		if planErr != nil {
			return
		}
		version = planned.Version
		effectivePercent = effective.Effective
	} else {
		desiredPercent, valid := contracts.HybridAccountWeightPercent(desired)
		if !valid || !contracts.HybridPercentMapsEqual(desiredPercent, execution.Effective) ||
			!sameHybridAccountClasses(inventory.Accounts, desired) {
			fail(errorRoutingUnrepresentable)
			return
		}
	}

	identity := contracts.ConnectorExecutionIdentity{
		Scope: contracts.HybridRoutingExecutionScope, ID: execution.ID, Generation: execution.Generation,
	}
	for _, value := range desired {
		current, readErr := w.readDesiredStates(ctx, execution.InstanceID, execution.Model, inventory, desired)
		if readErr != nil {
			// The exact plan is already durable. A transient read failure cannot
			// prove the outcome of earlier writes, so retain applying for reclaim.
			return
		}
		state, exists := current[value.AccountID]
		if !exists || state.CurrentWeight == nil {
			return
		}
		if *state.CurrentWeight == value.Weight && state.Schedulable == value.Schedulable {
			continue
		}
		writeCtx := contracts.WithConnectorExecutionIdentity(ctx, identity)
		writeCtx = contracts.WithGatewaySchedulingFence(writeCtx, contracts.GatewaySchedulingFence{
			Scope: contracts.HybridRoutingFenceScope(execution.InstanceID, value.AccountID), Version: execution.Generation,
		})
		reason := fmt.Sprintf("hybrid-routing:%s:%d", execution.ID, execution.Generation)
		applyWeight := func() bool {
			if err := renew(); err != nil {
				return false
			}
			if err := w.gateway.SetTrafficShare(writeCtx, execution.InstanceID, value.AccountID, value.Weight, reason); err != nil {
				// A timed-out Connector mutation may already be executing. Its durable
				// permit freezes this execution; do not falsely terminalize it here.
				if errors.Is(err, adapters.ErrGatewayMutationNotDispatched) {
					fail(errorWriteFailed)
				}
				return false
			}
			after, err := w.readDesiredStates(writeCtx, execution.InstanceID, execution.Model, inventory, desired)
			got, ok := after[value.AccountID]
			return err == nil && ok && got.CurrentWeight != nil && *got.CurrentWeight == value.Weight
		}
		applySchedulable := func(schedulable bool) bool {
			if err := renew(); err != nil {
				return false
			}
			if err := w.gateway.SetSchedulable(writeCtx, execution.InstanceID, value.AccountID, schedulable, reason); err != nil {
				if errors.Is(err, adapters.ErrGatewayMutationNotDispatched) {
					fail(errorWriteFailed)
				}
				return false
			}
			after, err := w.readDesiredStates(writeCtx, execution.InstanceID, execution.Model, inventory, desired)
			got, ok := after[value.AccountID]
			return err == nil && ok && got.Schedulable == schedulable
		}

		// Removing an account from sampling happens before changing its dormant
		// weight. Adding one does the reverse, so an old/default weight can never
		// receive live traffic between the two operations.
		if !value.Schedulable && state.Schedulable {
			if !applySchedulable(false) {
				return
			}
			state.Schedulable = false
		}
		if *state.CurrentWeight != value.Weight {
			if !applyWeight() {
				return
			}
		}
		if value.Schedulable && !state.Schedulable {
			if !applySchedulable(true) {
				return
			}
		}
		final, readErr := w.readDesiredStates(writeCtx, execution.InstanceID, execution.Model, inventory, desired)
		got, ok := final[value.AccountID]
		if readErr != nil || !ok || got.CurrentWeight == nil || *got.CurrentWeight != value.Weight || got.Schedulable != value.Schedulable {
			return
		}
	}
	if err := renew(); err != nil {
		return
	}
	readback, err := w.readHybridAccounts(ctx, execution.InstanceID, execution.Model, inventory, desired)
	if err != nil {
		return
	}
	actual, err := contracts.ActualHybridPercent(readback)
	if err != nil || !contracts.HybridPercentMapsEqual(actual, effectivePercent) {
		return
	}
	readBackWeights := make([]contracts.HybridAccountWeight, 0, len(readback))
	for _, account := range readback {
		if account.CurrentWeight == nil {
			return
		}
		readBackWeights = append(readBackWeights, contracts.HybridAccountWeight{
			AccountID: account.AccountID, Class: account.Class, Weight: *account.CurrentWeight, Schedulable: account.Schedulable,
		})
	}
	if _, err := w.store.CompleteHybridRoutingExecution(ctx, contracts.HybridRoutingExecutionCompletion{
		ID: execution.ID, WorkerID: w.workerID, ExpectedVersion: version, Succeeded: true, ReadBackWeights: readBackWeights,
	}); err != nil && ctx.Err() == nil {
		log.Printf("hybrid routing worker: record success for %s failed: %v", execution.ID, err)
	}
}

type routingInventory struct {
	Accounts          []contracts.HybridWeightAccount
	Models            []string
	Keys              map[contracts.ResourceClass]contracts.VirtualKey
	HasModelOverrides bool
}

func (w *Worker) buildInventory(ctx context.Context, instance contracts.Instance, allocation contracts.HybridAllocation, model string,
	bindings []contracts.HybridGatewayBinding, published []contracts.PublishedBinding, managedOwners map[string]struct{}, accounts []contracts.GatewayAccount,
) (routingInventory, string) {
	relevant, models, ok := representableAccounts(model, len(allocation.ModelOverrides) > 0, accounts)
	if !ok {
		return routingInventory{}, errorModelUnrepresentable
	}
	byID := make(map[string]contracts.GatewayAccount, len(accounts))
	for _, account := range relevant {
		id := strings.TrimSpace(account.ID)
		if id == "" {
			return routingInventory{}, errorGatewayReadFailed
		}
		if _, duplicate := byID[id]; duplicate {
			return routingInventory{}, errorGatewayReadFailed
		}
		byID[id] = account
	}
	managedIDs := managedRoutePlanAccountIDs(instance.ID, published)
	platformIDs := map[string]contracts.ResourceClass{}
	classified := make([]contracts.HybridWeightAccount, 0, len(relevant))
	keys := make(map[contracts.ResourceClass]contracts.VirtualKey, 2)
	for _, class := range []contracts.ResourceClass{contracts.ResourceClassEconomy, contracts.ResourceClassStable} {
		var binding *contracts.HybridGatewayBinding
		for index := range bindings {
			if bindings[index].ResourceClass == class {
				if binding != nil {
					return routingInventory{}, errorBindingUnavailable
				}
				binding = &bindings[index]
			}
		}
		if binding == nil || binding.Status != contracts.HybridGatewayBindingReady || !contracts.ValidHybridGatewayBinding(*binding) ||
			binding.ConnectorID != instance.ConnectorID || strings.TrimSpace(binding.RemoteAccountID) == "" {
			return routingInventory{}, errorBindingUnavailable
		}
		remote, exists := byID[binding.RemoteAccountID]
		if !exists {
			return routingInventory{}, errorBindingUnavailable
		}
		if _, conflict := managedIDs[remote.ID]; conflict {
			return routingInventory{}, errorSchedulingConflict
		}
		key, err := w.store.GetVirtualKey(ctx, allocation.UserID, binding.VirtualKeyID)
		if err != nil || !key.Enabled || key.InstanceID != allocation.InstanceID || key.ResourceClass != class ||
			key.KeyVersion != binding.VirtualKeyVersion || key.ExpiresAt != nil && !w.now().Before(*key.ExpiresAt) ||
			!sameStringSet(canonicalModels(key.Models), models) {
			return routingInventory{}, errorBindingUnavailable
		}
		if _, duplicate := platformIDs[remote.ID]; duplicate {
			return routingInventory{}, errorBindingUnavailable
		}
		classified = append(classified, contracts.HybridWeightAccount{AccountID: remote.ID, Class: class, CurrentWeight: remote.CurrentWeight, Schedulable: remote.Schedulable})
		platformIDs[remote.ID] = class
		keys[class] = key
	}
	for _, account := range relevant {
		if _, platform := platformIDs[strings.TrimSpace(account.ID)]; platform {
			continue
		}
		if _, conflict := managedIDs[strings.TrimSpace(account.ID)]; conflict {
			// A NewAPI account in the same model/group/priority cohort still
			// participates in sampling. Silently excluding it would make the
			// compiled and observed three-pool proportions untrue, while writing
			// it would cross the RoutePlan fence. Require an explicit ownership
			// migration before Hybrid takes over this cohort.
			return routingInventory{}, errorSchedulingConflict
		}
		if !account.Schedulable {
			if _, previouslyManaged := managedOwners[strings.TrimSpace(account.ID)]; !previouslyManaged {
				// Disabled accounts without a prior Hybrid plan may be unhealthy or
				// manually drained. Do not reinterpret them as members that Hybrid is
				// authorized to re-enable.
				continue
			}
		}
		classified = append(classified, contracts.HybridWeightAccount{AccountID: strings.TrimSpace(account.ID), Class: contracts.ResourceClassOwner, CurrentWeight: account.CurrentWeight, Schedulable: account.Schedulable})
	}
	sort.Slice(classified, func(i, j int) bool { return classified[i].AccountID < classified[j].AccountID })
	return routingInventory{
		Accounts: classified, Models: models, Keys: keys, HasModelOverrides: len(allocation.ModelOverrides) > 0,
	}, ""
}

func previouslyManagedOwnerIDs(current contracts.HybridRoutingExecution, executions []contracts.HybridRoutingExecution) map[string]struct{} {
	out := make(map[string]struct{})
	// Once the exact write set is durable, the current execution owns every
	// owner account it planned to drain. A reclaimed worker must retain those
	// accounts in inventory even when an earlier partial apply already disabled
	// them; otherwise it cannot replay or verify the persisted plan.
	for _, value := range current.DesiredWeights {
		if value.Class == contracts.ResourceClassOwner && !value.Schedulable {
			out[value.AccountID] = struct{}{}
		}
	}
	for _, execution := range executions {
		if execution.ID == current.ID || execution.Status != contracts.HybridRoutingExecutionSucceeded ||
			!strings.EqualFold(strings.TrimSpace(execution.Model), strings.TrimSpace(current.Model)) {
			continue
		}
		for _, value := range execution.DesiredWeights {
			if value.Class == contracts.ResourceClassOwner && !value.Schedulable {
				out[value.AccountID] = struct{}{}
			}
		}
	}
	return out
}

func managedRoutePlanAccountIDs(instanceID string, bindings []contracts.PublishedBinding) map[string]struct{} {
	out := make(map[string]struct{})
	for _, binding := range bindings {
		remoteID := strings.TrimSpace(binding.RemoteID)
		if remoteID == "" || binding.InstanceID != "" && binding.InstanceID != instanceID || !binding.RequiresSchedulingFence() {
			continue
		}
		out[remoteID] = struct{}{}
	}
	return out
}

func (w *Worker) resourceStates(ctx context.Context, allocation contracts.HybridAllocation, inventory routingInventory) []contracts.AllocationResourceState {
	states := make([]contracts.AllocationResourceState, 0, 3)
	ownerAvailable := false
	for _, account := range inventory.Accounts {
		ownerAvailable = ownerAvailable || account.Class == contracts.ResourceClassOwner
	}
	states = append(states, contracts.AllocationResourceState{Class: contracts.ResourceClassOwner, Available: ownerAvailable,
		Capacity: boolPercent(ownerAvailable), ReasonCode: reasonOwnerUnavailable})
	for _, class := range []contracts.ResourceClass{contracts.ResourceClassEconomy, contracts.ResourceClassStable} {
		key := inventory.Keys[class]
		capacity, maxReserve, currency := 100, int64(0), ""
		available, reason := true, ""
		for _, model := range inventory.Models {
			candidates, err := w.store.ListSupplyCandidates(ctx, class, model)
			if err != nil || len(candidates) == 0 {
				available, reason = false, reasonPlatformUnavailable
				break
			}
			first := candidates[0]
			if currency == "" {
				currency = first.Endpoint.Currency
			}
			if first.Endpoint.Currency != currency {
				available, reason = false, reasonPlatformUnavailable
				break
			}
			if allocation.MaxUnitPriceMicros > 0 && first.Endpoint.MaxRequestMicros > allocation.MaxUnitPriceMicros {
				available, reason = false, reasonPlatformPriceLimited
				break
			}
			if first.Endpoint.MaxRequestMicros > maxReserve {
				maxReserve = first.Endpoint.MaxRequestMicros
			}
			for _, candidate := range candidates {
				if candidate.Endpoint.Currency != currency {
					available, reason = false, reasonPlatformUnavailable
					break
				}
				if candidate.Endpoint.CapacityPercent < capacity {
					capacity = candidate.Endpoint.CapacityPercent
				}
			}
			if !available {
				break
			}
		}
		if available {
			wallet, err := w.store.GetWallet(ctx, allocation.UserID, currency)
			if err != nil || wallet.AvailableMicros < maxReserve {
				available, reason = false, reasonWalletUnavailable
			}
		}
		if available {
			usage, err := w.store.GetSupplyDailyUsage(ctx, allocation.UserID, allocation.InstanceID, key.ID, currency)
			if err != nil || allocation.DailyBudgetMicros > 0 && usage.InstanceReservedMicros+maxReserve > allocation.DailyBudgetMicros ||
				key.DailyLimitMicros > 0 && usage.KeyReservedMicros+maxReserve > key.DailyLimitMicros {
				available, reason = false, reasonPlatformBudgetLimited
			}
		}
		if !available {
			capacity = 0
		}
		states = append(states, contracts.AllocationResourceState{Class: class, Available: available, Capacity: capacity, ReasonCode: reason})
	}
	return states
}

func (w *Worker) readDesiredStates(ctx context.Context, instanceID, model string, inventory routingInventory,
	desired []contracts.HybridAccountWeight) (map[string]contracts.HybridWeightAccount, error) {
	accounts, err := w.readHybridAccounts(ctx, instanceID, model, inventory, desired)
	if err != nil {
		return nil, err
	}
	out := make(map[string]contracts.HybridWeightAccount, len(accounts))
	for _, account := range accounts {
		out[account.AccountID] = account
	}
	return out, nil
}

func (w *Worker) readHybridAccounts(ctx context.Context, instanceID, model string, inventory routingInventory,
	desired []contracts.HybridAccountWeight) ([]contracts.HybridWeightAccount, error) {
	accounts, err := w.gateway.ListAccounts(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	relevant, _, ok := representableAccounts(model, inventory.HasModelOverrides, accounts)
	if !ok {
		return nil, contracts.ErrHybridWeightsUnrepresentable
	}
	byID := make(map[string]contracts.GatewayAccount, len(accounts))
	desiredIDs := make(map[string]struct{}, len(desired))
	for _, value := range desired {
		desiredIDs[value.AccountID] = struct{}{}
	}
	for _, account := range relevant {
		id := strings.TrimSpace(account.ID)
		if id == "" {
			return nil, contracts.ErrHybridWeightsUnrepresentable
		}
		if _, duplicate := byID[id]; duplicate {
			return nil, contracts.ErrHybridWeightsUnrepresentable
		}
		if _, expected := desiredIDs[id]; !expected {
			// Unmanaged accounts in the same representable cohort are safe only
			// while they remain disabled. They contribute no NewAPI sampling
			// units and Hybrid has no authority to change them.
			if account.Schedulable {
				return nil, contracts.ErrHybridWeightsUnrepresentable
			}
			continue
		}
		byID[id] = account
	}
	if !sameHybridAccountClasses(inventory.Accounts, desired) || len(byID) != len(desired) {
		return nil, contracts.ErrHybridWeightsUnrepresentable
	}
	out := make([]contracts.HybridWeightAccount, 0, len(desired))
	for _, value := range desired {
		account, ok := byID[value.AccountID]
		if !ok {
			return nil, contracts.ErrHybridWeightsUnrepresentable
		}
		out = append(out, contracts.HybridWeightAccount{AccountID: value.AccountID, Class: value.Class, CurrentWeight: account.CurrentWeight, Schedulable: account.Schedulable})
	}
	return out, nil
}

func representableAccounts(model string, hasOverrides bool, accounts []contracts.GatewayAccount) ([]contracts.GatewayAccount, []string, bool) {
	model = strings.TrimSpace(model)
	if len(accounts) == 0 || len(accounts) > contracts.MaxConnectorAccounts || model == "" && hasOverrides {
		return nil, nil, false
	}
	relevant := make([]contracts.GatewayAccount, 0, len(accounts))
	var common []string
	for _, account := range accounts {
		models := canonicalModels(account.Models)
		if models == nil {
			return nil, nil, false
		}
		if model != "" {
			if !containsFold(models, strings.ToLower(model)) {
				continue
			}
			if account.Priority != 0 || len(account.GroupIDs) != 1 || account.GroupIDs[0] != "default" {
				return nil, nil, false
			}
			if len(models) != 1 || !strings.EqualFold(models[0], model) {
				return nil, nil, false
			}
			relevant = append(relevant, account)
			continue
		}
		if account.Priority != 0 || len(account.GroupIDs) != 1 || account.GroupIDs[0] != "default" {
			return nil, nil, false
		}
		if len(models) == 0 {
			return nil, nil, false
		}
		if common == nil {
			common = models
		} else if !sameStringSet(common, models) {
			return nil, nil, false
		}
		relevant = append(relevant, account)
	}
	if len(relevant) == 0 {
		return nil, nil, false
	}
	if model != "" {
		common = []string{strings.ToLower(model)}
	}
	return relevant, common, true
}

func canonicalModels(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !contracts.ValidHybridRoutingModel(value) || value == "" {
			return nil
		}
		value = strings.ToLower(value)
		if _, duplicate := seen[value]; duplicate {
			return nil
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !strings.EqualFold(left[index], right[index]) {
			return false
		}
	}
	return true
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func sameHybridAccountClasses(accounts []contracts.HybridWeightAccount, desired []contracts.HybridAccountWeight) bool {
	if len(accounts) != len(desired) {
		return false
	}
	classes := make(map[string]contracts.ResourceClass, len(accounts))
	for _, account := range accounts {
		if _, duplicate := classes[account.AccountID]; duplicate {
			return false
		}
		classes[account.AccountID] = account.Class
	}
	for _, value := range desired {
		if class, exists := classes[value.AccountID]; !exists || class != value.Class {
			return false
		}
	}
	return true
}

func boolPercent(value bool) int {
	if value {
		return 100
	}
	return 0
}
