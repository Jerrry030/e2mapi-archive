package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

func seedMemoryHybridSupply(t *testing.T) (*MemoryStore, contracts.User, contracts.Instance, contracts.SupplyChannelEndpoint, string) {
	t.Helper()
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	owner, err := st.CreateUser(ctx, contracts.User{Email: "hybrid@example.com", DisplayName: "Hybrid", PasswordHash: "hash", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: owner.ID, Name: "NewAPI", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "Economy", Models: []string{"gpt-test"}, ResourceClass: contracts.ResourceClassEconomy, DeliveryMode: contracts.UpstreamDeliverySupplyGateway})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: pool.ID, DisplayName: "Economy A", Models: []string{"gpt-test"}, AccountOwnership: contracts.GatewayAccountPlatformManaged, InventoryState: contracts.UpstreamInventoryReady})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := st.UpsertSupplyChannelEndpoint(ctx, contracts.SupplyChannelEndpoint{
		ChannelID: channel.ID, BaseURL: "https://upstream.example/v1", SecretRef: "credential_ref:supply/test", MaskedValue: "sk-***",
		Currency: "CNY", InputPriceMicrosPerMillion: 1_000_000, OutputPriceMicrosPerMillion: 2_000_000,
		InputSupplierMicrosPerMillion: 500_000, OutputSupplierMicrosPerMillion: 1_000_000,
		MaxRequestMicros: 100_000, MaxConcurrency: 10, CapacityPercent: 100, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	allocation, err := st.UpsertHybridAllocation(ctx, contracts.HybridAllocation{
		UserID: owner.ID, InstanceID: instance.ID,
		DefaultRule:       contracts.HybridAllocationRule{OwnerPercent: 80, EconomyPercent: 20, StablePercent: 0},
		DailyBudgetMicros: 1_000_000, MaxUnitPriceMicros: 100_000,
	}, 0)
	if err != nil || allocation.Version != 1 {
		t.Fatalf("allocation=%+v err=%v", allocation, err)
	}
	plaintext := "e2m_v1_test_key"
	_, err = st.CreateVirtualKey(ctx, contracts.VirtualKey{UserID: owner.ID, InstanceID: instance.ID, Name: "economy", ResourceClass: contracts.ResourceClassEconomy, Prefix: "e2m_v1_", TokenHash: contracts.HashVirtualKey(plaintext), SecretRef: "credential_ref:virtual/test"})
	if err != nil {
		t.Fatal(err)
	}
	return st, owner, instance, endpoint, plaintext
}

func TestMemoryHybridAllocationCASAndOwnerKeyBoundary(t *testing.T) {
	st, owner, instance, _, _ := seedMemoryHybridSupply(t)
	ctx := context.Background()
	allocation, err := st.GetHybridAllocation(ctx, instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	allocation.DefaultRule = contracts.HybridAllocationRule{OwnerPercent: 50, EconomyPercent: 25, StablePercent: 25}
	updated, err := st.UpsertHybridAllocation(ctx, allocation, allocation.Version)
	if err != nil || updated.Version != 2 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if _, err := st.UpsertHybridAllocation(ctx, allocation, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS error=%v", err)
	}
	_, err = st.CreateVirtualKey(ctx, contracts.VirtualKey{UserID: owner.ID, InstanceID: instance.ID, Name: "owner", ResourceClass: contracts.ResourceClassOwner, TokenHash: contracts.HashVirtualKey("owner"), SecretRef: "credential_ref:virtual/owner"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("owner key error=%v", err)
	}
}

func TestMemorySupplyReserveSettleAndReleaseAreBalanced(t *testing.T) {
	st, owner, _, _, plaintext := seedMemoryHybridSupply(t)
	ctx := context.Background()
	st.mu.Lock()
	st.wallets[walletMapKey(owner.ID, "CNY")] = contracts.Wallet{UserID: owner.ID, Currency: "CNY", AvailableMicros: 500_000, Version: 1, UpdatedAt: st.now()}
	st.mu.Unlock()
	reserved, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "request-1", "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.Wallet.AvailableMicros != 400_000 || reserved.Wallet.ReservedMicros != 100_000 || reserved.Reservation.ChannelID == "" {
		t.Fatalf("reserved=%+v", reserved)
	}
	if reserved.Key.LastUsedAt == nil {
		t.Fatal("successful reservation did not update key last_used_at")
	}
	settled, err := st.SettleSupplyRequest(ctx, reserved.Reservation.ID, 1_000, 2_000, contracts.SupplyTelemetry{})
	if err != nil {
		t.Fatal(err)
	}
	if settled.ChargedMicros != 5_000 || settled.SupplierMicros != 2_500 || settled.ReleasedMicros != 95_000 || settled.Wallet.AvailableMicros != 495_000 || settled.Wallet.ReservedMicros != 0 {
		t.Fatalf("settled=%+v", settled)
	}
	duplicate, err := st.SettleSupplyRequest(ctx, reserved.Reservation.ID, 99, 99, contracts.SupplyTelemetry{})
	if err != nil || duplicate.ChargedMicros != 5_000 || duplicate.Wallet.AvailableMicros != 495_000 {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	reserved2, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "request-2", "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatal(err)
	}
	released, err := st.ReleaseSupplyRequest(ctx, reserved2.Reservation.ID, "upstream_failed", contracts.SupplyTelemetry{})
	if err != nil || released.ReleasedMicros != 100_000 || released.Wallet.AvailableMicros != 495_000 || released.Wallet.ReservedMicros != 0 {
		t.Fatalf("released=%+v err=%v", released, err)
	}
	journals, err := st.ListWalletJournals(ctx, owner.ID, 100)
	if err != nil || len(journals) != 5 {
		t.Fatalf("journals=%d err=%v", len(journals), err)
	}
	for _, journal := range journals {
		if !journal.Balanced() {
			t.Fatalf("unbalanced journal: %+v", journal)
		}
	}
	var settleJournal contracts.WalletJournal
	for _, journal := range journals {
		if journal.Kind == contracts.WalletJournalSettle {
			settleJournal = journal
			break
		}
	}
	if settleJournal.Kind != contracts.WalletJournalSettle || len(settleJournal.Entries) != 3 {
		t.Fatalf("settle journal=%+v", settleJournal)
	}
	credits := map[contracts.WalletAccountCode]int64{}
	for _, entry := range settleJournal.Entries {
		if entry.Direction == contracts.WalletEntryCredit {
			credits[entry.Account] += entry.AmountMicros
		}
	}
	if credits[contracts.WalletAccountUpstreamPayable] != 2_500 || credits[contracts.WalletAccountPlatformRevenue] != 2_500 {
		t.Fatalf("settlement credits=%+v", credits)
	}
}

// setWallet replaces the wallet balance for a settlement-boundary test.
func setWallet(t *testing.T, st *MemoryStore, userID int64, availableMicros int64) {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.wallets[walletMapKey(userID, "CNY")] = contracts.Wallet{
		UserID: userID, Currency: "CNY", AvailableMicros: availableMicros, Version: 1, UpdatedAt: st.now(),
	}
}

func TestMemoryWalletSmallerThanHoldCanStillSpend(t *testing.T) {
	st, owner, _, _, plaintext := seedMemoryHybridSupply(t)
	ctx := context.Background()
	// The endpoint holds 100_000 per request; this wallet has less than that.
	setWallet(t, st, owner.ID, 40_000)

	reserved, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "small-balance-1", "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatalf("a wallet below the per-request hold must still be usable: %v", err)
	}
	if reserved.Reservation.ReservedMicros != 40_000 {
		t.Fatalf("hold must shrink to the available balance, got %d", reserved.Reservation.ReservedMicros)
	}
	if reserved.Wallet.AvailableMicros != 0 || reserved.Wallet.ReservedMicros != 40_000 {
		t.Fatalf("unexpected wallet after hold: %+v", reserved.Wallet)
	}

	// An empty wallet is still refused: the balance, not the cap, is the limit.
	setWallet(t, st, owner.ID, 0)
	if _, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "small-balance-2", "gpt-test", "CNY", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("an empty wallet must be refused, got %v", err)
	}
}

func TestMemorySettlementChargesTrueCostAndCarriesDebt(t *testing.T) {
	ctx := context.Background()

	// Funded wallet: the true cost exceeds the hold and is charged in full.
	st, owner, _, _, plaintext := seedMemoryHybridSupply(t)
	setWallet(t, st, owner.ID, 500_000)
	reserved, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "over-hold-1", "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.Reservation.ReservedMicros != 100_000 {
		t.Fatalf("expected the configured hold, got %d", reserved.Reservation.ReservedMicros)
	}
	// 150_000 prompt tokens at 1_000_000 micros per million = 150_000 micros,
	// which is 1.5x the hold.
	settled, err := st.SettleSupplyRequest(ctx, reserved.Reservation.ID, 150_000, 0, contracts.SupplyTelemetry{})
	if err != nil {
		t.Fatalf("settling above the hold must succeed when funds remain: %v", err)
	}
	if settled.ChargedMicros != 150_000 {
		t.Fatalf("the true cost must be charged, got %d", settled.ChargedMicros)
	}
	if settled.Wallet.AvailableMicros != 350_000 || settled.Wallet.ReservedMicros != 0 {
		t.Fatalf("unexpected wallet after settling above the hold: %+v", settled.Wallet)
	}

	// Wallet that cannot cover the true cost: the full cost is still charged
	// and the shortfall is carried as debt.
	st2, owner2, _, _, plaintext2 := seedMemoryHybridSupply(t)
	setWallet(t, st2, owner2.ID, 120_000)
	reserved2, err := st2.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext2), "over-hold-2", "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatal(err)
	}
	settled2, err := st2.SettleSupplyRequest(ctx, reserved2.Reservation.ID, 500_000, 0, contracts.SupplyTelemetry{})
	if err != nil {
		t.Fatalf("settling beyond the balance must still settle: %v", err)
	}
	if settled2.ChargedMicros != 500_000 {
		t.Fatalf("the true cost must be charged even without funds, got %d", settled2.ChargedMicros)
	}
	// 120_000 held, 500_000 charged -> 380_000 of debt.
	if settled2.Wallet.AvailableMicros != -380_000 || settled2.Wallet.ReservedMicros != 0 {
		t.Fatalf("the shortfall must be carried as debt: %+v", settled2.Wallet)
	}

	// A wallet in debt cannot start new requests.
	if _, err := st2.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext2), "in-debt", "gpt-test", "CNY", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("a wallet in debt must be refused, got %v", err)
	}

	// A partial credit is accepted and reduces the debt without clearing it.
	partial, _, err := st2.AdjustWalletBalance(ctx, owner2.ID, "CNY", 80_000, "debt-partial", "partial repayment")
	if err != nil {
		t.Fatalf("a credit onto a wallet in debt must be accepted: %v", err)
	}
	if partial.AvailableMicros != -300_000 {
		t.Fatalf("the credit must offset the debt, got %d", partial.AvailableMicros)
	}
	if _, err := st2.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext2), "still-in-debt", "gpt-test", "CNY", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("a still-negative wallet must stay refused, got %v", err)
	}

	// Clearing the debt restores service.
	cleared, _, err := st2.AdjustWalletBalance(ctx, owner2.ID, "CNY", 400_000, "debt-clear", "top up")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.AvailableMicros != 100_000 {
		t.Fatalf("the remainder must land in available funds, got %d", cleared.AvailableMicros)
	}
	if _, err := st2.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext2), "after-clear", "gpt-test", "CNY", nil); err != nil {
		t.Fatalf("a cleared wallet must be usable again: %v", err)
	}

	// The ledger still balances once the charge spans both debit sources.
	journals, err := st.ListWalletJournals(ctx, owner.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, journal := range journals {
		if journal.Kind != contracts.WalletJournalSettle {
			continue
		}
		debit, credit := int64(0), int64(0)
		for _, entry := range journal.Entries {
			if entry.Direction == contracts.WalletEntryDebit {
				debit += entry.AmountMicros
			} else {
				credit += entry.AmountMicros
			}
		}
		if debit != credit || debit != journal.AmountMicros {
			t.Fatalf("settlement journal is unbalanced: debit=%d credit=%d amount=%d", debit, credit, journal.AmountMicros)
		}
	}
}

func TestMemoryReserveEnforcesUserPlatformLimits(t *testing.T) {
	st, owner, _, _, plaintext := seedMemoryHybridSupply(t)
	ctx := context.Background()
	tokenHash := contracts.HashVirtualKey(plaintext)
	setState := func(concurrency, rpm int) {
		st.mu.Lock()
		st.wallets[walletMapKey(owner.ID, "CNY")] = contracts.Wallet{UserID: owner.ID, Currency: "CNY", AvailableMicros: 500_000, Version: 1, UpdatedAt: st.now()}
		for i := range st.users {
			if st.users[i].ID == owner.ID {
				st.users[i].PlatformConcurrency = concurrency
				st.users[i].PlatformRPM = rpm
			}
		}
		st.mu.Unlock()
	}

	setState(1, 0)
	first, err := st.ReserveSupplyRequest(ctx, tokenHash, "limit-request-1", "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatalf("first reservation must pass: %v", err)
	}
	if _, err := st.ReserveSupplyRequest(ctx, tokenHash, "limit-request-2", "gpt-test", "CNY", nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second concurrent reservation must be rate limited, got %v", err)
	}
	// The idempotent replay of an active reservation never counts against the cap.
	if _, err := st.ReserveSupplyRequest(ctx, tokenHash, "limit-request-1", "gpt-test", "CNY", nil); err != nil {
		t.Fatalf("replay of the active reservation must pass: %v", err)
	}
	if _, err := st.SettleSupplyRequest(ctx, first.Reservation.ID, 100, 100, contracts.SupplyTelemetry{}); err != nil {
		t.Fatalf("settle: %v", err)
	}

	setState(0, 2)
	if _, err := st.ReserveSupplyRequest(ctx, tokenHash, "limit-request-3", "gpt-test", "CNY", nil); err != nil {
		t.Fatalf("reservation under the RPM cap must pass: %v", err)
	}
	if _, err := st.ReserveSupplyRequest(ctx, tokenHash, "limit-request-4", "gpt-test", "CNY", nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("reservation over the RPM cap must be rate limited, got %v", err)
	}
}

func TestMemoryDisabledUserCannotReserveWithExistingPlatformKey(t *testing.T) {
	st, owner, _, _, plaintext := seedMemoryHybridSupply(t)
	ctx := context.Background()
	st.mu.Lock()
	for i := range st.users {
		if st.users[i].ID == owner.ID {
			st.users[i].Enabled = false
		}
	}
	st.wallets[walletMapKey(owner.ID, "CNY")] = contracts.Wallet{UserID: owner.ID, Currency: "CNY", AvailableMicros: 500_000, Version: 1, UpdatedAt: st.now()}
	st.mu.Unlock()

	if _, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "disabled-user-request", "gpt-test", "CNY", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled user platform key remained usable: %v", err)
	}
}

func TestMemoryDisabledUserCannotCreatePlatformKey(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	user, err := st.CreateUser(ctx, contracts.User{Email: "disabled-platform-key@example.com", PasswordHash: "hash", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	group, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "Stable", Models: []string{"gpt-test"}, ResourceClass: contracts.ResourceClassStable, DeliveryMode: contracts.UpstreamDeliverySupplyGateway})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateVirtualKey(ctx, contracts.VirtualKey{UserID: user.ID, GroupID: group.ID, Name: "blocked", ResourceClass: contracts.ResourceClassStable, TokenHash: contracts.HashVirtualKey("e2m_v1_blocked"), SecretRef: "credential_ref:virtual/blocked", Models: []string{"gpt-test"}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("disabled user created a platform key: %v", err)
	}
}

func TestMemorySupplierWithoutCustomerRoleCannotCreateOrUsePlatformKey(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	supplier, err := st.CreateUser(ctx, contracts.User{Email: "supplier-platform-key@example.com", PasswordHash: "hash", Roles: []contracts.UserRole{contracts.UserRoleSupplier}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	group, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "Stable", Models: []string{"gpt-test"}, ResourceClass: contracts.ResourceClassStable, DeliveryMode: contracts.UpstreamDeliverySupplyGateway})
	if err != nil {
		t.Fatal(err)
	}
	input := contracts.VirtualKey{UserID: supplier.ID, GroupID: group.ID, Name: "blocked", ResourceClass: contracts.ResourceClassStable, TokenHash: contracts.HashVirtualKey("e2m_v1_supplier_blocked"), SecretRef: "credential_ref:virtual/supplier-blocked", Models: []string{"gpt-test"}}
	if _, err = st.CreateVirtualKey(ctx, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("supplier-only user created a platform key: %v", err)
	}

	// Simulate a legacy key that predates the role fence. The data-plane check
	// must still invalidate it after the customer role has been removed.
	input.ID, input.Enabled = "vkey_legacy_supplier", true
	st.mu.Lock()
	st.virtualKeys[input.ID] = input
	st.wallets[walletMapKey(supplier.ID, "CNY")] = contracts.Wallet{UserID: supplier.ID, Currency: "CNY", AvailableMicros: 500_000, Version: 1, UpdatedAt: st.now()}
	st.mu.Unlock()
	if _, err = st.ReserveSupplyRequest(ctx, input.TokenHash, "supplier-role-request", "gpt-test", "CNY", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("supplier-only user's legacy platform key remained usable: %v", err)
	}
}

func TestMemoryPlatformKeyStaysInsideItsGroupAndSnapshotsPrices(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	owner, err := st.CreateUser(ctx, contracts.User{Email: "platform-key@example.com", PasswordHash: "hash", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	groupA, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "Stable A", Models: []string{"gpt-test"}, ResourceClass: contracts.ResourceClassStable, DeliveryMode: contracts.UpstreamDeliverySupplyGateway})
	groupB, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "Stable B", Models: []string{"gpt-test"}, ResourceClass: contracts.ResourceClassStable, DeliveryMode: contracts.UpstreamDeliverySupplyGateway})
	createChannel := func(group contracts.UpstreamPool, name string, priority int) (contracts.UpstreamChannel, contracts.SupplyChannelEndpoint) {
		channel, createErr := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: group.ID, DisplayName: name, Models: []string{"gpt-test"}, Priority: priority, Weight: 100, AccountOwnership: contracts.GatewayAccountPlatformManaged, InventoryState: contracts.UpstreamInventoryReady})
		if createErr != nil {
			t.Fatal(createErr)
		}
		endpoint, endpointErr := st.UpsertSupplyChannelEndpoint(ctx, contracts.SupplyChannelEndpoint{ChannelID: channel.ID, BaseURL: "https://upstream.example/v1", SecretRef: "credential_ref:" + channel.ID, Currency: "CNY", InputPriceMicrosPerMillion: 2_000_000, OutputPriceMicrosPerMillion: 3_000_000, InputSupplierMicrosPerMillion: 1_000_000, OutputSupplierMicrosPerMillion: 1_500_000, MaxRequestMicros: 100_000, MaxConcurrency: 10, CapacityPercent: 100, Enabled: true})
		if endpointErr != nil {
			t.Fatal(endpointErr)
		}
		return channel, endpoint
	}
	channelA1, endpointA1 := createChannel(groupA, "A1", 1)
	channelA2, _ := createChannel(groupA, "A2", 2)
	_, _ = createChannel(groupB, "B1", 0)
	plaintext := "e2m_v1_platform_group"
	key, err := st.CreateVirtualKey(ctx, contracts.VirtualKey{UserID: owner.ID, GroupID: groupA.ID, Name: "production", ResourceClass: contracts.ResourceClassStable, Prefix: "e2m_v1_", TokenHash: contracts.HashVirtualKey(plaintext), SecretRef: "credential_ref:virtual/platform", Models: []string{"gpt-test"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.AdjustWalletBalance(ctx, owner.ID, "CNY", 500_000, "seed-platform-wallet", "test"); err != nil {
		t.Fatal(err)
	}
	reserved, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "platform-request-1", "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.Key.ID != key.ID || reserved.Candidate.Pool.ID != groupA.ID || reserved.Candidate.Channel.ID != channelA1.ID || reserved.Usage.GroupID != groupA.ID || reserved.Usage.InstanceID != "" {
		t.Fatalf("reservation escaped group: %+v", reserved)
	}
	endpointA1.InputPriceMicrosPerMillion = 99_000_000
	endpointA1.OutputPriceMicrosPerMillion = 99_000_000
	if _, err = st.UpsertSupplyChannelEndpoint(ctx, endpointA1); err != nil {
		t.Fatal(err)
	}
	settled, err := st.SettleSupplyRequest(ctx, reserved.Reservation.ID, 1_000, 1_000, contracts.SupplyTelemetry{})
	if err != nil || settled.ChargedMicros != 5_000 {
		t.Fatalf("price snapshot was not used: %+v err=%v", settled, err)
	}
	reserved, err = st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "platform-request-2", "gpt-test", "CNY", []string{channelA1.ID})
	if err != nil || reserved.Candidate.Channel.ID != channelA2.ID {
		t.Fatalf("excluded channel was not failed over: %+v err=%v", reserved.Candidate, err)
	}
	usage, err := st.ListSupplyUsage(ctx, contracts.SupplyUsageFilter{UserID: owner.ID, GroupID: groupA.ID, Limit: 10})
	if err != nil || len(usage) != 2 {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
}

func TestMemoryConfirmRechargePaymentIsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	owner, err := st.CreateUser(ctx, contracts.User{Email: "payer@example.com", PasswordHash: "hash", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := st.CreatePaymentProvider(ctx, contracts.PaymentProvider{ProviderKey: contracts.PaymentProviderStripe, Name: "Stripe", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	order, err := st.CreatePaymentOrder(ctx, contracts.PaymentOrder{UserID: owner.ID, Amount: "10", PayAmount: "10", FeeRate: "0", Currency: "CNY", PaymentType: "stripe", OutTradeNo: "recharge_1", OrderType: contracts.PaymentOrderBalance, ProviderInstanceID: provider.ID, ProviderKey: provider.ProviderKey, ProviderName: provider.Name, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	notification := contracts.PaymentNotification{ProviderInstanceID: provider.ID, ProviderKey: provider.ProviderKey, EventID: "evt-1", ProviderOrderID: "cs-1", OutTradeNo: order.OutTradeNo, PaymentTradeNo: "pi-1", PaidAmountMicros: 10_000_000, Currency: "CNY", PaidAt: time.Now()}
	confirmed, wallet, credited, err := st.ConfirmRechargePayment(ctx, notification, "body-hash")
	if err != nil || !credited || confirmed.Status != contracts.PaymentOrderCompleted || wallet.AvailableMicros != 10_000_000 {
		t.Fatalf("confirmed=%+v wallet=%+v credited=%v err=%v", confirmed, wallet, credited, err)
	}
	_, wallet, credited, err = st.ConfirmRechargePayment(ctx, notification, "body-hash")
	if err != nil || credited || wallet.AvailableMicros != 10_000_000 {
		t.Fatalf("duplicate wallet=%+v credited=%v err=%v", wallet, credited, err)
	}
	if _, _, _, err = st.ConfirmRechargePayment(ctx, notification, "different-body-hash"); !errors.Is(err, ErrConflict) {
		t.Fatalf("same event with different body hash error=%v", err)
	}
	journals, _ := st.ListWalletJournals(ctx, owner.ID, 10)
	if len(journals) != 1 || !journals[0].Balanced() {
		t.Fatalf("journals=%+v", journals)
	}
}

func TestMemoryRejectedPaymentCallbackIsDurableAndHashBound(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	provider, err := st.CreatePaymentProvider(ctx, contracts.PaymentProvider{ProviderKey: contracts.PaymentProviderStripe, Name: "Stripe", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	event := contracts.PaymentCallbackEvent{
		ProviderInstanceID: provider.ID, ProviderKey: provider.ProviderKey, EventID: "evt-orphan",
		BodyHash: "body-hash", ErrorCode: "unknown_order",
	}
	if err := st.RecordRejectedPaymentCallback(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRejectedPaymentCallback(ctx, event); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	event.BodyHash = "different-hash"
	if err := st.RecordRejectedPaymentCallback(ctx, event); !errors.Is(err, ErrConflict) {
		t.Fatalf("hash mismatch error=%v", err)
	}
	stored, ok := st.paymentCallbackEvents[provider.ID+":"+event.EventID]
	if !ok || stored.Accepted || stored.ErrorCode != "unknown_order" || stored.OrderID != "" {
		t.Fatalf("stored=%+v ok=%v", stored, ok)
	}
}
