package store

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
)

func walletMapKey(userID int64, currency string) string {
	return strings.ToUpper(strings.TrimSpace(currency)) + ":" + strconv.FormatInt(userID, 10)
}

func validCurrency(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for index := range currency {
		if currency[index] < 'A' || currency[index] > 'Z' {
			return false
		}
	}
	return true
}

func (s *MemoryStore) GetHybridAllocation(ctx context.Context, instanceID string) (contracts.HybridAllocation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.HybridAllocation{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	allocation, ok := s.hybridAllocations[strings.TrimSpace(instanceID)]
	if !ok {
		return contracts.HybridAllocation{}, ErrNotFound
	}
	return copyHybridAllocation(allocation), nil
}

func (s *MemoryStore) UpsertHybridAllocation(ctx context.Context, input contracts.HybridAllocation, expectedVersion int64) (contracts.HybridAllocation, error) {
	if err := ctx.Err(); err != nil {
		return contracts.HybridAllocation{}, err
	}
	input = input.Normalize()
	if expectedVersion < 0 || !contracts.ValidHybridAllocation(input) {
		return contracts.HybridAllocation{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var instanceFound bool
	for _, instance := range s.instances {
		if instance.ID == input.InstanceID {
			if instance.UserID != input.UserID {
				return contracts.HybridAllocation{}, ErrInvalid
			}
			instanceFound = true
			break
		}
	}
	if !instanceFound {
		return contracts.HybridAllocation{}, ErrInvalid
	}
	current, exists := s.hybridAllocations[input.InstanceID]
	if !exists && expectedVersion != 0 || exists && current.Version != expectedVersion {
		return contracts.HybridAllocation{}, ErrConflict
	}
	now := s.now()
	if exists {
		// Allocation edits never advance or rewind the routing generation; only
		// CreateHybridRoutingExecution owns that generation transition.
		input.RoutingGeneration = current.RoutingGeneration
		input.CreatedAt = current.CreatedAt
		input.Version = current.Version + 1
	} else {
		input.CreatedAt = now
		input.Version = 1
	}
	input.UpdatedAt = now
	s.hybridAllocations[input.InstanceID] = copyHybridAllocation(input)
	return copyHybridAllocation(input), nil
}

func copyHybridAllocation(input contracts.HybridAllocation) contracts.HybridAllocation {
	out := input
	out.ModelOverrides = append([]contracts.HybridModelAllocation(nil), input.ModelOverrides...)
	return out
}

func (s *MemoryStore) ListWalletsBelow(ctx context.Context, currency string, thresholdMicros int64) ([]contracts.Wallet, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !validCurrency(currency) || thresholdMicros <= 0 {
		return nil, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []contracts.Wallet{}
	for _, wallet := range s.wallets {
		if wallet.Currency == currency && wallet.AvailableMicros < thresholdMicros {
			out = append(out, wallet)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

func (s *MemoryStore) GetWallet(ctx context.Context, userID int64, currency string) (contracts.Wallet, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Wallet{}, err
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if userID <= 0 || !validCurrency(currency) {
		return contracts.Wallet{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	wallet, ok := s.wallets[walletMapKey(userID, currency)]
	if !ok {
		return contracts.Wallet{UserID: userID, Currency: currency}, nil
	}
	return wallet, nil
}

func (s *MemoryStore) ListWalletJournals(ctx context.Context, userID int64, limit int) ([]contracts.WalletJournal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if userID <= 0 || limit < 0 {
		return nil, ErrInvalid
	}
	if limit == 0 || limit > 100 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.WalletJournal, 0, limit)
	for index := len(s.walletJournals) - 1; index >= 0 && len(out) < limit; index-- {
		if s.walletJournals[index].UserID == userID {
			out = append(out, copyWalletJournal(s.walletJournals[index]))
		}
	}
	return out, nil
}

func copyWalletJournal(input contracts.WalletJournal) contracts.WalletJournal {
	out := input
	out.Entries = append([]contracts.WalletEntry(nil), input.Entries...)
	return out
}

func (s *MemoryStore) AdjustWalletBalance(ctx context.Context, userID int64, currency string, deltaMicros int64, idempotencyKey, note string) (contracts.Wallet, contracts.WalletJournal, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Wallet{}, contracts.WalletJournal{}, err
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	idempotencyKey, note = strings.TrimSpace(idempotencyKey), strings.TrimSpace(note)
	if userID <= 0 || !validCurrency(currency) || deltaMicros == 0 || idempotencyKey == "" || len(idempotencyKey) > 128 || len(note) > 500 {
		return contracts.Wallet{}, contracts.WalletJournal{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, journal := range s.walletJournals {
		if journal.UserID == userID && journal.IdempotencyKey == idempotencyKey {
			return s.wallets[walletMapKey(userID, currency)], copyWalletJournal(journal), nil
		}
	}
	if _, exists := func() (contracts.User, bool) {
		for _, user := range s.users {
			if user.ID == userID && user.Enabled {
				return user, true
			}
		}
		return contracts.User{}, false
	}(); !exists {
		return contracts.Wallet{}, contracts.WalletJournal{}, ErrNotFound
	}
	walletKey := walletMapKey(userID, currency)
	wallet := s.wallets[walletKey]
	// A credit is always accepted, including a partial repayment that leaves
	// the wallet still in debt. Only a debit is refused when it would create
	// or deepen debt: debt may only arise from metered settlement.
	if deltaMicros < 0 && wallet.AvailableMicros < -deltaMicros {
		return contracts.Wallet{}, contracts.WalletJournal{}, ErrConflict
	}
	now := s.now()
	wallet.UserID, wallet.Currency = userID, currency
	wallet.AvailableMicros += deltaMicros
	wallet.Version++
	wallet.UpdatedAt = now
	s.wallets[walletKey] = wallet
	amount := deltaMicros
	debit, credit := contracts.WalletAccountPlatformCash, contracts.WalletAccountUserAvailable
	if amount < 0 {
		amount = -amount
		debit, credit = contracts.WalletAccountUserAvailable, contracts.WalletAccountPlatformCash
	}
	journal := s.appendWalletJournalLocked(userID, contracts.WalletJournalAdjustment, currency, amount, idempotencyKey, "admin_balance_adjustment", note, debit, credit, now)
	return wallet, copyWalletJournal(journal), nil
}

func copyVirtualKey(input contracts.VirtualKey) contracts.VirtualKey {
	out := input
	out.Models = append([]string(nil), input.Models...)
	out.ExpiresAt = copyTimePointer(input.ExpiresAt)
	out.LastUsedAt = copyTimePointer(input.LastUsedAt)
	return out
}

func (s *MemoryStore) CreateVirtualKey(ctx context.Context, input contracts.VirtualKey) (contracts.VirtualKey, error) {
	if err := ctx.Err(); err != nil {
		return contracts.VirtualKey{}, err
	}
	input.Name = strings.TrimSpace(input.Name)
	input.InstanceID = strings.TrimSpace(input.InstanceID)
	input.GroupID = strings.TrimSpace(input.GroupID)
	if !input.Valid() || strings.TrimSpace(input.TokenHash) == "" || strings.TrimSpace(input.SecretRef) == "" {
		return contracts.VirtualKey{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	userEligible := false
	for _, user := range s.users {
		if user.ID == input.UserID {
			userEligible = user.Enabled && (userHasRole(user.Roles, contracts.UserRoleClient) || userHasRole(user.Roles, contracts.UserRoleAdmin))
			break
		}
	}
	if !userEligible {
		return contracts.VirtualKey{}, ErrInvalid
	}
	scopeExists := false
	if input.GroupID != "" {
		for _, pool := range s.upstreamPools {
			if pool.ID == input.GroupID && pool.DeliveryMode == contracts.UpstreamDeliverySupplyGateway && pool.Status != contracts.UpstreamPoolRetired {
				scopeExists = true
				break
			}
		}
	} else {
		for _, instance := range s.instances {
			if instance.ID == input.InstanceID && instance.UserID == input.UserID {
				scopeExists = true
				break
			}
		}
	}
	if !scopeExists {
		return contracts.VirtualKey{}, ErrInvalid
	}
	for _, key := range s.virtualKeys {
		if key.TokenHash == input.TokenHash || key.SecretRef == input.SecretRef ||
			(key.InstanceID == input.InstanceID && key.GroupID == input.GroupID && key.ResourceClass == input.ResourceClass && key.Name == input.Name) {
			return contracts.VirtualKey{}, ErrDuplicate
		}
	}
	if input.ID == "" {
		input.ID = s.nextID("vkey")
	}
	if input.KeyVersion == 0 {
		input.KeyVersion = 1
	}
	now := s.now()
	input.Enabled = true
	input.CreatedAt, input.UpdatedAt = now, now
	s.virtualKeys[input.ID] = copyVirtualKey(input)
	return copyVirtualKey(input), nil
}

func (s *MemoryStore) GetVirtualKey(ctx context.Context, userID int64, id string) (contracts.VirtualKey, error) {
	if err := ctx.Err(); err != nil {
		return contracts.VirtualKey{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	key, ok := s.virtualKeys[id]
	if !ok || key.UserID != userID {
		return contracts.VirtualKey{}, ErrNotFound
	}
	return copyVirtualKey(key), nil
}

func (s *MemoryStore) GetVirtualKeyByHash(ctx context.Context, tokenHash string) (contracts.VirtualKey, error) {
	if err := ctx.Err(); err != nil {
		return contracts.VirtualKey{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, key := range s.virtualKeys {
		if key.TokenHash == tokenHash {
			return copyVirtualKey(key), nil
		}
	}
	return contracts.VirtualKey{}, ErrNotFound
}

func (s *MemoryStore) ListVirtualKeys(ctx context.Context, userID int64) ([]contracts.VirtualKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []contracts.VirtualKey{}
	for _, key := range s.virtualKeys {
		if key.UserID == userID {
			out = append(out, copyVirtualKey(key))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *MemoryStore) UpdateVirtualKey(ctx context.Context, input contracts.VirtualKey) (contracts.VirtualKey, error) {
	if err := ctx.Err(); err != nil {
		return contracts.VirtualKey{}, err
	}
	input.Name = strings.TrimSpace(input.Name)
	if !input.Valid() {
		return contracts.VirtualKey{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.virtualKeys[input.ID]
	if !ok || current.UserID != input.UserID {
		return contracts.VirtualKey{}, ErrNotFound
	}
	if current.InstanceID != input.InstanceID || current.GroupID != input.GroupID || current.ResourceClass != input.ResourceClass ||
		current.TokenHash != input.TokenHash || current.SecretRef != input.SecretRef || current.KeyVersion != input.KeyVersion {
		return contracts.VirtualKey{}, ErrConflict
	}
	for _, key := range s.virtualKeys {
		if key.ID != input.ID && key.InstanceID == input.InstanceID && key.GroupID == input.GroupID && key.ResourceClass == input.ResourceClass && key.Name == input.Name {
			return contracts.VirtualKey{}, ErrDuplicate
		}
	}
	input.CreatedAt = current.CreatedAt
	input.UpdatedAt = s.now()
	s.virtualKeys[input.ID] = copyVirtualKey(input)
	return copyVirtualKey(input), nil
}

func (s *MemoryStore) DeleteVirtualKey(ctx context.Context, userID int64, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.virtualKeys[id]
	if !ok || key.UserID != userID {
		return ErrNotFound
	}
	for _, reservation := range s.walletReservations {
		if reservation.VirtualKeyID == id && reservation.Status == contracts.WalletReservationActive {
			return ErrConflict
		}
	}
	delete(s.virtualKeys, id)
	return nil
}

func (s *MemoryStore) UpsertSupplyChannelEndpoint(ctx context.Context, input contracts.SupplyChannelEndpoint) (contracts.SupplyChannelEndpoint, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SupplyChannelEndpoint{}, err
	}
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	if !input.Valid() || strings.TrimSpace(input.SecretRef) == "" {
		return contracts.SupplyChannelEndpoint{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var channelFound bool
	for _, channel := range s.upstreamChannels {
		if channel.ID == input.ChannelID {
			for _, pool := range s.upstreamPools {
				if pool.ID == channel.PoolID && pool.DeliveryMode == contracts.UpstreamDeliverySupplyGateway {
					channelFound = true
				}
			}
		}
	}
	if !channelFound {
		return contracts.SupplyChannelEndpoint{}, ErrInvalid
	}
	for id, endpoint := range s.supplyEndpoints {
		if id != input.ChannelID && endpoint.SecretRef == input.SecretRef {
			return contracts.SupplyChannelEndpoint{}, ErrDuplicate
		}
	}
	now := s.now()
	if current, ok := s.supplyEndpoints[input.ChannelID]; ok {
		input.CreatedAt = current.CreatedAt
	} else {
		input.CreatedAt = now
	}
	input.UpdatedAt = now
	s.supplyEndpoints[input.ChannelID] = input
	return input, nil
}

func (s *MemoryStore) GetSupplyChannelEndpoint(ctx context.Context, channelID string) (contracts.SupplyChannelEndpoint, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SupplyChannelEndpoint{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	endpoint, ok := s.supplyEndpoints[channelID]
	if !ok {
		return contracts.SupplyChannelEndpoint{}, ErrNotFound
	}
	return endpoint, nil
}

func (s *MemoryStore) ListSupplyCandidates(ctx context.Context, class contracts.ResourceClass, model string) ([]contracts.SupplyCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	model = strings.TrimSpace(model)
	if !class.IsPlatformSupply() || model == "" {
		return nil, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listSupplyCandidatesLocked(class, model), nil
}

func (s *MemoryStore) ListSupplyUsage(ctx context.Context, filter contracts.SupplyUsageFilter) ([]contracts.SupplyUsageRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filter.UserID < 0 || filter.Limit < 0 || filter.Status != "" && filter.Status != contracts.SupplyUsageReserved && filter.Status != contracts.SupplyUsageSettled && filter.Status != contracts.SupplyUsageReleased {
		return nil, ErrInvalid
	}
	if filter.Limit == 0 || filter.Limit > 200 {
		filter.Limit = 200
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.SupplyUsageRecord, 0, filter.Limit)
	for _, usage := range s.supplyUsage {
		if filter.UserID > 0 && usage.UserID != filter.UserID || filter.GroupID != "" && usage.GroupID != filter.GroupID ||
			filter.VirtualKeyID != "" && usage.VirtualKeyID != filter.VirtualKeyID || filter.Status != "" && usage.Status != filter.Status {
			continue
		}
		out = append(out, usage)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (s *MemoryStore) GetSupplyDailyUsage(ctx context.Context, userID int64, instanceID, virtualKeyID, currency string) (contracts.SupplyDailyUsage, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SupplyDailyUsage{}, err
	}
	instanceID, virtualKeyID = strings.TrimSpace(instanceID), strings.TrimSpace(virtualKeyID)
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if userID <= 0 || instanceID == "" || virtualKeyID == "" || !validCurrency(currency) {
		return contracts.SupplyDailyUsage{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	key, exists := s.virtualKeys[virtualKeyID]
	if !exists || key.UserID != userID || key.InstanceID != instanceID {
		return contracts.SupplyDailyUsage{}, ErrNotFound
	}
	dayStart := s.now().UTC().Truncate(24 * time.Hour)
	result := contracts.SupplyDailyUsage{UserID: userID, InstanceID: instanceID, VirtualKeyID: virtualKeyID, Currency: currency, DayStart: dayStart}
	for _, usage := range s.supplyUsage {
		if usage.UserID != userID || usage.InstanceID != instanceID || usage.CreatedAt.Before(dayStart) || usage.Status == contracts.SupplyUsageReleased {
			continue
		}
		reservation, ok := s.walletReservations[usage.ReservationID]
		if !ok || reservation.Currency != currency {
			continue
		}
		result.InstanceReservedMicros += usage.ReservedMicros
		if usage.VirtualKeyID == virtualKeyID {
			result.KeyReservedMicros += usage.ReservedMicros
		}
	}
	return result, nil
}

func (s *MemoryStore) listSupplyCandidatesLocked(class contracts.ResourceClass, model string) []contracts.SupplyCandidate {
	out := []contracts.SupplyCandidate{}
	for _, pool := range s.upstreamPools {
		if pool.ResourceClass != class || pool.DeliveryMode != contracts.UpstreamDeliverySupplyGateway || pool.Status != contracts.UpstreamPoolActive || !containsModel(pool.Models, model) {
			continue
		}
		for _, channel := range s.upstreamChannels {
			endpoint, ok := s.supplyEndpoints[channel.ID]
			if channel.PoolID != pool.ID || !ok || channel.Status != contracts.UpstreamChannelActive || !channel.IsInventoryReady() ||
				!endpoint.Enabled || endpoint.CapacityPercent == 0 || !containsModel(channel.Models, model) {
				continue
			}
			active := 0
			for _, reservation := range s.walletReservations {
				if reservation.ChannelID == channel.ID && reservation.Status == contracts.WalletReservationActive {
					active++
				}
			}
			if endpoint.MaxConcurrency > 0 && active >= endpoint.MaxConcurrency {
				continue
			}
			out = append(out, contracts.SupplyCandidate{Pool: pool, Channel: channel, Endpoint: endpoint, Active: active})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Channel.Priority != out[j].Channel.Priority {
			return out[i].Channel.Priority < out[j].Channel.Priority
		}
		if out[i].Active == out[j].Active {
			if out[i].Channel.Weight != out[j].Channel.Weight {
				return out[i].Channel.Weight > out[j].Channel.Weight
			}
			return out[i].Channel.ID < out[j].Channel.ID
		}
		return out[i].Active < out[j].Active
	})
	return out
}

// ListSupplyModels mirrors the Postgres projection. It walks the same pools,
// channels and endpoints listSupplyCandidatesLocked walks, so the catalog it
// returns matches what this backend's ReserveSupplyRequest would accept.
func (s *MemoryStore) ListSupplyModels(ctx context.Context, tokenHash, currency string) (contracts.SupplyModelCatalog, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SupplyModelCatalog{}, err
	}
	tokenHash = strings.TrimSpace(tokenHash)
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if tokenHash == "" || !validCurrency(currency) {
		return contracts.SupplyModelCatalog{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var key contracts.VirtualKey
	var found bool
	for _, current := range s.virtualKeys {
		if current.TokenHash == tokenHash {
			key, found = current, true
			break
		}
	}
	if !found || !key.Enabled || key.ExpiresAt != nil && !s.now().Before(*key.ExpiresAt) {
		return contracts.SupplyModelCatalog{}, ErrNotFound
	}
	userEligible := false
	for _, user := range s.users {
		if user.ID == key.UserID {
			userEligible = user.Enabled && (userHasRole(user.Roles, contracts.UserRoleClient) || userHasRole(user.Roles, contracts.UserRoleAdmin))
			break
		}
	}
	if !userEligible {
		return contracts.SupplyModelCatalog{}, ErrNotFound
	}

	// Models group case-insensitively but are reported with a DECLARED
	// spelling: the upstream is the authority on a model id, and a folded id
	// both misses the channel's case-sensitive model mapping and can be
	// rejected outright by the upstream. Picking the lexicographic minimum
	// matches the Postgres backend's min(m.value).
	type aggregate struct {
		declared  string
		firstSeen time.Time
		channels  map[string]bool
	}
	served := map[string]*aggregate{}
	var wildcard contracts.SupplyModelEntry
	wildcardChannels := map[string]bool{}
	record := func(bucket map[string]*aggregate, model string, channel contracts.UpstreamChannel) {
		model = strings.TrimSpace(model)
		lowered := strings.ToLower(model)
		if lowered == "" {
			return
		}
		entry, ok := bucket[lowered]
		if !ok {
			entry = &aggregate{declared: model, firstSeen: channel.CreatedAt, channels: map[string]bool{}}
			bucket[lowered] = entry
		}
		if model < entry.declared {
			entry.declared = model
		}
		if channel.CreatedAt.Before(entry.firstSeen) {
			entry.firstSeen = channel.CreatedAt
		}
		entry.channels[channel.ID] = true
	}
	for _, pool := range s.upstreamPools {
		if pool.ResourceClass != key.ResourceClass || pool.DeliveryMode != contracts.UpstreamDeliverySupplyGateway ||
			pool.Status != contracts.UpstreamPoolActive || key.GroupID != "" && pool.ID != key.GroupID {
			continue
		}
		for _, channel := range s.upstreamChannels {
			endpoint, ok := s.supplyEndpoints[channel.ID]
			if channel.PoolID != pool.ID || !ok || channel.Status != contracts.UpstreamChannelActive ||
				!channel.IsInventoryReady() || !endpoint.Enabled || endpoint.CapacityPercent == 0 ||
				endpoint.Currency != currency {
				continue
			}
			// Both arrays empty means this pair accepts any model, so it can
			// never be enumerated — only counted.
			if len(pool.Models) == 0 && len(channel.Models) == 0 {
				wildcardChannels[channel.ID] = true
				if wildcard.CreatedAt.IsZero() || channel.CreatedAt.Before(wildcard.CreatedAt) {
					wildcard.CreatedAt = channel.CreatedAt
				}
				continue
			}
			// Otherwise the pair serves the intersection of whichever arrays
			// are declared, matching the two independent gates in the
			// Postgres candidate query.
			declared := pool.Models
			if len(declared) == 0 {
				declared = channel.Models
			}
			for _, model := range declared {
				if containsModel(pool.Models, model) && containsModel(channel.Models, model) {
					record(served, model, channel)
				}
			}
		}
	}
	wildcard.Channels = len(wildcardChannels)
	entries := make([]contracts.SupplyModelEntry, 0, len(served))
	for _, entry := range served {
		entries = append(entries, contracts.SupplyModelEntry{
			Model: entry.declared, CreatedAt: entry.firstSeen, Channels: len(entry.channels),
		})
	}
	// Order case-insensitively to match the Postgres backend's ORDER BY lower().
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Model) < strings.ToLower(entries[j].Model)
	})
	return buildSupplyModelCatalog(key.Models, entries, wildcard), nil
}

func containsModel(models []string, target string) bool {
	if len(models) == 0 {
		return true
	}
	for _, model := range models {
		if strings.EqualFold(model, target) {
			return true
		}
	}
	return false
}

func (s *MemoryStore) ReserveSupplyRequest(ctx context.Context, tokenHash, requestID, model, currency string, excludedChannelIDs []string) (contracts.SupplyReservationResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SupplyReservationResult{}, err
	}
	tokenHash, requestID, model = strings.TrimSpace(tokenHash), strings.TrimSpace(requestID), strings.TrimSpace(model)
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if tokenHash == "" || requestID == "" || model == "" || !validCurrency(currency) {
		return contracts.SupplyReservationResult{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var key contracts.VirtualKey
	var found bool
	for _, current := range s.virtualKeys {
		if current.TokenHash == tokenHash {
			key, found = current, true
			break
		}
	}
	if !found || !key.Enabled || key.ExpiresAt != nil && !s.now().Before(*key.ExpiresAt) || !containsModel(key.Models, model) {
		return contracts.SupplyReservationResult{}, ErrNotFound
	}
	userEligible := false
	var platformUser contracts.User
	for _, user := range s.users {
		if user.ID == key.UserID {
			platformUser = user
			userEligible = user.Enabled && (userHasRole(user.Roles, contracts.UserRoleClient) || userHasRole(user.Roles, contracts.UserRoleAdmin))
			break
		}
	}
	if !userEligible {
		return contracts.SupplyReservationResult{}, ErrNotFound
	}
	for _, reservation := range s.walletReservations {
		if reservation.VirtualKeyID == key.ID && reservation.RequestID == requestID {
			usage := s.supplyUsage[reservation.ID]
			candidate, ok := s.supplyCandidateByChannelLocked(key.ResourceClass, model, reservation.ChannelID)
			if !ok || key.GroupID != "" && candidate.Pool.ID != key.GroupID {
				return contracts.SupplyReservationResult{}, ErrConflict
			}
			return contracts.SupplyReservationResult{Key: copyVirtualKey(key), Candidate: candidate, Wallet: s.wallets[walletMapKey(key.UserID, currency)], Reservation: reservation, Usage: usage}, nil
		}
	}
	// Per-user platform throttles run after the idempotent-replay branch so a
	// retried request never counts against itself.
	if platformUser.PlatformConcurrency > 0 {
		active := 0
		for _, reservation := range s.walletReservations {
			if reservation.UserID == key.UserID && reservation.Status == contracts.WalletReservationActive {
				active++
			}
		}
		if active >= platformUser.PlatformConcurrency {
			return contracts.SupplyReservationResult{}, ErrRateLimited
		}
	}
	if platformUser.PlatformRPM > 0 {
		minuteAgo := s.now().Add(-time.Minute)
		recent := 0
		for _, usage := range s.supplyUsage {
			if usage.UserID == key.UserID && !usage.CreatedAt.Before(minuteAgo) {
				recent++
			}
		}
		if recent >= platformUser.PlatformRPM {
			return contracts.SupplyReservationResult{}, ErrRateLimited
		}
	}

	candidates := s.listSupplyCandidatesLocked(key.ResourceClass, model)
	excluded := make(map[string]bool, len(excludedChannelIDs))
	for _, id := range excludedChannelIDs {
		excluded[strings.TrimSpace(id)] = true
	}
	eligible := candidates[:0]
	for _, candidate := range candidates {
		if key.GroupID != "" && candidate.Pool.ID != key.GroupID || excluded[candidate.Channel.ID] {
			continue
		}
		eligible = append(eligible, candidate)
	}
	candidates = eligible
	if len(candidates) == 0 {
		return contracts.SupplyReservationResult{}, ErrNoSupply
	}
	s.rankSupplyCandidatesLocked(candidates, key.RoutingPreference)
	candidate := candidates[0]
	if candidate.Endpoint.Currency != currency {
		return contracts.SupplyReservationResult{}, ErrInvalid
	}
	reserve := candidate.Endpoint.MaxRequestMicros
	allocation := s.hybridAllocations[key.InstanceID]
	if allocation.MaxUnitPriceMicros > 0 && reserve > allocation.MaxUnitPriceMicros {
		return contracts.SupplyReservationResult{}, ErrConflict
	}
	walletKey := walletMapKey(key.UserID, currency)
	wallet := s.wallets[walletKey]
	// The hold is a ceiling, not an entry fee: a wallet holding less than the
	// configured per-request cap reserves everything it has instead of being
	// locked out. Settlement still charges the true cost and may draw the
	// difference from whatever remains, so the balance is the real limit.
	if wallet.AvailableMicros <= 0 {
		return contracts.SupplyReservationResult{}, ErrConflict
	}
	if reserve > wallet.AvailableMicros {
		reserve = wallet.AvailableMicros
	}
	instanceSpentToday, keySpentToday := int64(0), int64(0)
	dayStart := s.now().UTC().Truncate(24 * time.Hour)
	for _, usage := range s.supplyUsage {
		reservation, ok := s.walletReservations[usage.ReservationID]
		if usage.UserID == key.UserID && usage.InstanceID == key.InstanceID && usage.GroupID == key.GroupID && !usage.CreatedAt.Before(dayStart) &&
			usage.Status != contracts.SupplyUsageReleased && ok && reservation.Currency == currency {
			instanceSpentToday += usage.ReservedMicros
			if usage.VirtualKeyID == key.ID {
				keySpentToday += usage.ReservedMicros
			}
		}
	}
	if allocation.DailyBudgetMicros > 0 && instanceSpentToday+reserve > allocation.DailyBudgetMicros ||
		key.DailyLimitMicros > 0 && keySpentToday+reserve > key.DailyLimitMicros {
		return contracts.SupplyReservationResult{}, ErrConflict
	}
	now := s.now()
	key.LastUsedAt = &now
	key.UpdatedAt = now
	s.virtualKeys[key.ID] = copyVirtualKey(key)
	reservation := contracts.WalletReservation{ID: s.nextID("wres"), UserID: key.UserID, VirtualKeyID: key.ID, ChannelID: candidate.Channel.ID, RequestID: requestID, Currency: currency, ReservedMicros: reserve, Status: contracts.WalletReservationActive, CreatedAt: now, UpdatedAt: now}
	usage := contracts.SupplyUsageRecord{ID: s.nextID("usage"), RequestID: requestID, ReservationID: reservation.ID, UserID: key.UserID, GroupID: key.GroupID, InstanceID: key.InstanceID, VirtualKeyID: key.ID, ResourceClass: key.ResourceClass, ChannelID: candidate.Channel.ID, Model: model, InputPriceMicrosPerMillion: candidate.Endpoint.InputPriceMicrosPerMillion, OutputPriceMicrosPerMillion: candidate.Endpoint.OutputPriceMicrosPerMillion, InputSupplierMicrosPerMillion: candidate.Endpoint.InputSupplierMicrosPerMillion, OutputSupplierMicrosPerMillion: candidate.Endpoint.OutputSupplierMicrosPerMillion, ReservedMicros: reserve, Status: contracts.SupplyUsageReserved, CreatedAt: now}
	wallet.UserID, wallet.Currency = key.UserID, currency
	wallet.AvailableMicros -= reserve
	wallet.ReservedMicros += reserve
	wallet.Version++
	wallet.UpdatedAt = now
	s.wallets[walletKey] = wallet
	s.walletReservations[reservation.ID] = reservation
	s.supplyUsage[reservation.ID] = usage
	s.appendWalletJournalLocked(key.UserID, contracts.WalletJournalReserve, currency, reserve, "reserve:"+reservation.ID, "supply_reservation", reservation.ID, contracts.WalletAccountUserAvailable, contracts.WalletAccountUserReserved, now)
	return contracts.SupplyReservationResult{Key: copyVirtualKey(key), Candidate: candidate, Wallet: wallet, Reservation: reservation, Usage: usage}, nil
}

func (s *MemoryStore) supplyCandidateByChannelLocked(class contracts.ResourceClass, model, channelID string) (contracts.SupplyCandidate, bool) {
	for _, candidate := range s.listSupplyCandidatesLocked(class, model) {
		if candidate.Channel.ID == channelID {
			return candidate, true
		}
	}
	return contracts.SupplyCandidate{}, false
}

func tokenCharge(price int64, tokens int64) int64 {
	if price <= 0 || tokens <= 0 {
		return 0
	}
	return (price*tokens + 999_999) / 1_000_000
}

func (s *MemoryStore) SettleSupplyRequest(ctx context.Context, reservationID string, promptTokens, completionTokens int64, telemetry contracts.SupplyTelemetry) (contracts.SupplySettlementResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SupplySettlementResult{}, err
	}
	if promptTokens < 0 || completionTokens < 0 {
		return contracts.SupplySettlementResult{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settleSupplyLocked(reservationID, promptTokens, completionTokens, false, false, "metered", telemetry)
}

func (s *MemoryStore) SettleSupplyRequestConservatively(ctx context.Context, reservationID, reasonCode string, telemetry contracts.SupplyTelemetry) (contracts.SupplySettlementResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SupplySettlementResult{}, err
	}
	reasonCode = strings.TrimSpace(reasonCode)
	if reasonCode == "" || len(reasonCode) > 64 {
		return contracts.SupplySettlementResult{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settleSupplyLocked(reservationID, 0, 0, false, true, reasonCode, telemetry)
}

func (s *MemoryStore) ReleaseSupplyRequest(ctx context.Context, reservationID, reasonCode string, telemetry contracts.SupplyTelemetry) (contracts.SupplySettlementResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SupplySettlementResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settleSupplyLocked(reservationID, 0, 0, true, false, strings.TrimSpace(reasonCode), telemetry)
}

func (s *MemoryStore) settleSupplyLocked(reservationID string, promptTokens, completionTokens int64, release, conservative bool, reasonCode string, telemetry contracts.SupplyTelemetry) (contracts.SupplySettlementResult, error) {
	reservation, ok := s.walletReservations[reservationID]
	if !ok {
		return contracts.SupplySettlementResult{}, ErrNotFound
	}
	usage := s.supplyUsage[reservationID]
	walletKey := walletMapKey(reservation.UserID, reservation.Currency)
	wallet := s.wallets[walletKey]
	if reservation.Status != contracts.WalletReservationActive {
		return contracts.SupplySettlementResult{Wallet: wallet, Reservation: reservation, Usage: usage, ChargedMicros: reservation.SettledMicros, ReleasedMicros: reservation.ReservedMicros - reservation.SettledMicros}, nil
	}
	charged, supplier := int64(0), int64(0)
	if conservative {
		charged, supplier = reservation.ReservedMicros, reservation.ReservedMicros
	} else if !release {
		charged = tokenCharge(usage.InputPriceMicrosPerMillion, promptTokens) + tokenCharge(usage.OutputPriceMicrosPerMillion, completionTokens)
		supplier = tokenCharge(usage.InputSupplierMicrosPerMillion, promptTokens) + tokenCharge(usage.OutputSupplierMicrosPerMillion, completionTokens)
		if supplier > charged {
			return contracts.SupplySettlementResult{}, ErrConflict
		}
		// A request is charged what it actually cost, even when that exceeds
		// the hold and the funds left. The shortfall is carried as debt (a
		// negative available balance) and is offset by the next credit; a
		// wallet at or below zero cannot start further requests.
	}
	released := reservation.ReservedMicros - charged
	now := s.now()
	wallet.ReservedMicros -= reservation.ReservedMicros
	wallet.AvailableMicros += released
	wallet.Version++
	wallet.UpdatedAt = now
	reservation.SettledMicros = charged
	reservation.UpdatedAt = now
	usage.PromptTokens, usage.CompletionTokens = promptTokens, completionTokens
	usage.SettledMicros = charged
	usage.SettlementReason = reasonCode
	usage.CompletedAt = &now
	usage.FirstTokenMS = max(telemetry.FirstTokenMS, 0)
	usage.DurationMS = max(telemetry.DurationMS, 0)
	s.recordSupplyChannelStatsLocked(usage.ChannelID, telemetry, now)
	if release {
		reservation.Status = contracts.WalletReservationReleased
		usage.Status = contracts.SupplyUsageReleased
		s.appendWalletJournalLocked(reservation.UserID, contracts.WalletJournalRelease, reservation.Currency, reservation.ReservedMicros, "release:"+reservation.ID, "supply_reservation", reservation.ID, contracts.WalletAccountUserReserved, contracts.WalletAccountUserAvailable, now)
	} else {
		reservation.Status = contracts.WalletReservationSettled
		usage.Status = contracts.SupplyUsageSettled
		if charged > 0 {
			fromReserved := reservation.ReservedMicros
			if charged < fromReserved {
				fromReserved = charged
			}
			s.appendSupplySettlementJournalLocked(reservation.UserID, reservation.Currency, charged, supplier, fromReserved, "settle:"+reservation.ID, "supply_reservation", reservation.ID, now)
		}
		if released > 0 {
			s.appendWalletJournalLocked(reservation.UserID, contracts.WalletJournalRelease, reservation.Currency, released, "settle-release:"+reservation.ID, "supply_reservation", reservation.ID, contracts.WalletAccountUserReserved, contracts.WalletAccountUserAvailable, now)
		}
	}
	s.wallets[walletKey] = wallet
	s.walletReservations[reservation.ID] = reservation
	s.supplyUsage[reservation.ID] = usage
	return contracts.SupplySettlementResult{Wallet: wallet, Reservation: reservation, Usage: usage, ChargedMicros: charged, SupplierMicros: supplier, ReleasedMicros: released}, nil
}

// rankSupplyCandidatesLocked mirrors supplyPreferenceOrderBy: a stable sort
// on the preference metric alone, so equally-ranked channels keep the
// platform-curated default order the caller already applied. Hard gates ran
// before this — the preference can only reorder, never admit or exclude.
func (s *MemoryStore) rankSupplyCandidatesLocked(candidates []contracts.SupplyCandidate, preference contracts.SupplyRoutingPreference) {
	if len(candidates) < 2 {
		return
	}
	var metric func(contracts.SupplyCandidate) float64
	switch preference {
	case contracts.SupplyRoutingPriceFirst:
		metric = func(candidate contracts.SupplyCandidate) float64 {
			return float64(candidate.Endpoint.InputPriceMicrosPerMillion*2 + candidate.Endpoint.OutputPriceMicrosPerMillion)
		}
	case contracts.SupplyRoutingSpeedFirst:
		since := s.now().Add(-supplyRankingWindow)
		metric = func(candidate contracts.SupplyCandidate) float64 {
			_, _, ttftSum, ttftSamples := s.supplyChannelWindowLocked(candidate.Channel.ID, since)
			return (float64(ttftSum) + supplyRankTTFTPriorMS*supplyRankTTFTPseudoSamples) / (float64(ttftSamples) + supplyRankTTFTPseudoSamples)
		}
	case contracts.SupplyRoutingSuccessFirst:
		since := s.now().Add(-supplyRankingWindow)
		metric = func(candidate contracts.SupplyCandidate) float64 {
			requests, failures, _, _ := s.supplyChannelWindowLocked(candidate.Channel.ID, since)
			return (float64(failures) + supplyRankFailurePseudoFailures) / (float64(requests) + supplyRankFailurePseudoRequests)
		}
	default:
		return
	}
	scores := make(map[string]float64, len(candidates))
	for _, candidate := range candidates {
		scores[candidate.Channel.ID] = metric(candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return scores[candidates[i].Channel.ID] < scores[candidates[j].Channel.ID]
	})
}

// supplyChannelWindowLocked sums a channel's reliability buckets from since
// onward. Callers hold s.mu.
func (s *MemoryStore) supplyChannelWindowLocked(channelID string, since time.Time) (requests, failures, ttftSum, ttftSamples int64) {
	for bucketStart, bucket := range s.supplyChannelStats[channelID] {
		if bucketStart.Before(since) {
			continue
		}
		requests += bucket.Requests
		failures += bucket.Failures
		ttftSum += bucket.TTFTSumMS
		ttftSamples += bucket.TTFTSamples
	}
	return requests, failures, ttftSum, ttftSamples
}

// recordSupplyChannelStatsLocked mirrors recordSupplyChannelStatsTx: one
// sample per success/failure outcome into the current five-minute bucket,
// plus a per-channel retention prune. Neutral or empty outcomes write nothing.
func (s *MemoryStore) recordSupplyChannelStatsLocked(channelID string, telemetry contracts.SupplyTelemetry, now time.Time) {
	if !telemetry.Outcome.CountsAsSample() || strings.TrimSpace(channelID) == "" {
		return
	}
	buckets := s.supplyChannelStats[channelID]
	if buckets == nil {
		buckets = make(map[time.Time]contracts.SupplyChannelStatsBucket)
		s.supplyChannelStats[channelID] = buckets
	}
	start := contracts.SupplyStatsBucketStart(now)
	bucket := buckets[start]
	bucket.ChannelID, bucket.BucketStart = channelID, start
	bucket.Requests++
	if telemetry.Outcome == contracts.SupplyOutcomeFailure {
		bucket.Failures++
	}
	if telemetry.FirstTokenMS > 0 {
		bucket.TTFTSumMS += telemetry.FirstTokenMS
		bucket.TTFTSamples++
	}
	if telemetry.DurationMS > 0 {
		bucket.DurationSumMS += telemetry.DurationMS
		bucket.DurationSamples++
	}
	buckets[start] = bucket
	cutoff := now.Add(-supplyStatsRetention)
	for bucketStart := range buckets {
		if bucketStart.Before(cutoff) {
			delete(buckets, bucketStart)
		}
	}
}

func (s *MemoryStore) ListSupplyChannelStats(ctx context.Context, channelID string, since time.Time) ([]contracts.SupplyChannelStatsBucket, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]contracts.SupplyChannelStatsBucket, 0)
	for bucketStart, bucket := range s.supplyChannelStats[channelID] {
		if !bucketStart.Before(since) {
			out = append(out, bucket)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BucketStart.Before(out[j].BucketStart) })
	return out, nil
}

func (s *MemoryStore) appendSupplySettlementJournalLocked(userID int64, currency string, charged, supplier, fromReserved int64, idempotencyKey, referenceType, referenceID string, now time.Time) contracts.WalletJournal {
	journal := contracts.WalletJournal{ID: s.nextID("wjnl"), UserID: userID, Kind: contracts.WalletJournalSettle, Currency: currency, AmountMicros: charged, IdempotencyKey: idempotencyKey, ReferenceType: referenceType, ReferenceID: referenceID, CreatedAt: now}
	if fromReserved > 0 {
		journal.Entries = append(journal.Entries, contracts.WalletEntry{ID: s.nextID("went"), JournalID: journal.ID, Account: contracts.WalletAccountUserReserved, Direction: contracts.WalletEntryDebit, AmountMicros: fromReserved, Currency: currency, CreatedAt: now})
	}
	// Anything charged beyond the hold comes straight out of available funds,
	// so the ledger records two debit sources rather than overdrawing the
	// reserved account.
	if extra := charged - fromReserved; extra > 0 {
		journal.Entries = append(journal.Entries, contracts.WalletEntry{ID: s.nextID("went"), JournalID: journal.ID, Account: contracts.WalletAccountUserAvailable, Direction: contracts.WalletEntryDebit, AmountMicros: extra, Currency: currency, CreatedAt: now})
	}
	if supplier > 0 {
		journal.Entries = append(journal.Entries, contracts.WalletEntry{ID: s.nextID("went"), JournalID: journal.ID, Account: contracts.WalletAccountUpstreamPayable, Direction: contracts.WalletEntryCredit, AmountMicros: supplier, Currency: currency, CreatedAt: now})
	}
	if margin := charged - supplier; margin > 0 {
		journal.Entries = append(journal.Entries, contracts.WalletEntry{ID: s.nextID("went"), JournalID: journal.ID, Account: contracts.WalletAccountPlatformRevenue, Direction: contracts.WalletEntryCredit, AmountMicros: margin, Currency: currency, CreatedAt: now})
	}
	s.walletJournals = append(s.walletJournals, journal)
	return journal
}

func (s *MemoryStore) appendWalletJournalLocked(userID int64, kind contracts.WalletJournalKind, currency string, amount int64, idempotencyKey, referenceType, referenceID string, debit, credit contracts.WalletAccountCode, now time.Time) contracts.WalletJournal {
	journal := contracts.WalletJournal{ID: s.nextID("wjnl"), UserID: userID, Kind: kind, Currency: currency, AmountMicros: amount, IdempotencyKey: idempotencyKey, ReferenceType: referenceType, ReferenceID: referenceID, CreatedAt: now}
	journal.Entries = []contracts.WalletEntry{
		{ID: s.nextID("went"), JournalID: journal.ID, Account: debit, Direction: contracts.WalletEntryDebit, AmountMicros: amount, Currency: currency, CreatedAt: now},
		{ID: s.nextID("went"), JournalID: journal.ID, Account: credit, Direction: contracts.WalletEntryCredit, AmountMicros: amount, Currency: currency, CreatedAt: now},
	}
	s.walletJournals = append(s.walletJournals, journal)
	return journal
}
