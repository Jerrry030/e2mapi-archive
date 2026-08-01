package contracts

import "testing"

func TestHybridAllocationValidationAndModelOverride(t *testing.T) {
	allocation := HybridAllocation{
		UserID: 7, InstanceID: "instance-1",
		DefaultRule: HybridAllocationRule{OwnerPercent: 80, EconomyPercent: 5, StablePercent: 15},
		ModelOverrides: []HybridModelAllocation{{
			Model: "gpt-5", Rule: HybridAllocationRule{
				OwnerPercent: 20, EconomyPercent: 30, StablePercent: 50,
				OwnerBurstMax: 40, EconomyBurstMax: 50, StableBurstMax: 80,
			},
		}},
	}
	if !ValidHybridAllocation(allocation) {
		t.Fatal("valid allocation was rejected")
	}
	if got := allocation.Normalize().Basis; got != HybridAllocationBasisRequests {
		t.Fatalf("basis = %q", got)
	}
	if got := allocation.RuleForModel("GPT-5"); got.EconomyPercent != 30 || got.StableBurstMax != 80 {
		t.Fatalf("override = %+v", got)
	}
	if got := allocation.RuleForModel("claude"); got.OwnerBurstMax != 80 || got.StableBurstMax != 15 {
		t.Fatalf("normalized default = %+v", got)
	}

	invalid := allocation
	invalid.DefaultRule.StablePercent = 14
	if ValidHybridAllocation(invalid) {
		t.Fatal("allocation whose target does not total 100 was accepted")
	}
	invalid = allocation
	invalid.ModelOverrides = append(invalid.ModelOverrides, invalid.ModelOverrides[0])
	invalid.ModelOverrides[1].Model = "GPT-5"
	if ValidHybridAllocation(invalid) {
		t.Fatal("case-insensitive duplicate model override was accepted")
	}
}

func TestCompileEffectiveAllocationRespectsElasticCeilings(t *testing.T) {
	rule := HybridAllocationRule{
		OwnerPercent: 80, EconomyPercent: 5, StablePercent: 15,
		OwnerBurstMax: 90, EconomyBurstMax: 10, StableBurstMax: 30,
	}
	got := CompileEffectiveAllocation(rule, []AllocationResourceState{
		{Class: ResourceClassOwner, Available: true, Capacity: 100},
		{Class: ResourceClassEconomy, Available: false, Capacity: 0, ReasonCode: "economy_budget_exhausted"},
		{Class: ResourceClassStable, Available: true, Capacity: 100},
	})
	if got.Effective[ResourceClassOwner] != 80 || got.Effective[ResourceClassEconomy] != 0 || got.Effective[ResourceClassStable] != 20 || got.Unallocated != 0 {
		t.Fatalf("effective allocation = %+v", got)
	}
	if len(got.AdjustmentCodes) != 2 || got.AdjustmentCodes[0] != "economy_budget_exhausted" || got.AdjustmentCodes[1] != "stable_burst_used" {
		t.Fatalf("adjustment codes = %#v", got.AdjustmentCodes)
	}

	got = CompileEffectiveAllocation(rule, []AllocationResourceState{
		{Class: ResourceClassOwner, Available: false},
		{Class: ResourceClassEconomy, Available: false},
		{Class: ResourceClassStable, Available: true, Capacity: 25},
	})
	if got.Effective[ResourceClassStable] != 25 || got.Unallocated != 75 {
		t.Fatalf("allocation exceeded a capacity/elasticity boundary: %+v", got)
	}
}

func TestPlatformResourceAndVirtualKeyBoundaries(t *testing.T) {
	if got := NormalizePlatformResourceClass(""); got != ResourceClassStable {
		t.Fatalf("legacy class = %q", got)
	}
	if ResourceClassOwner.IsPlatformSupply() {
		t.Fatal("owner resource was classified as E2M supply")
	}
	for _, class := range []ResourceClass{ResourceClassEconomy, ResourceClassStable} {
		if !class.IsPlatformSupply() {
			t.Fatalf("%q is not platform supply", class)
		}
		key := VirtualKey{UserID: 1, InstanceID: "inst", Name: "key", ResourceClass: class}
		if !key.Valid() {
			t.Fatalf("valid %q virtual key rejected", class)
		}
	}
	if (VirtualKey{UserID: 1, InstanceID: "inst", Name: "key", ResourceClass: ResourceClassOwner}).Valid() {
		t.Fatal("owner virtual key was accepted")
	}
	if HashVirtualKey("e2m_test") == HashVirtualKey("e2m_other") || HashVirtualKey("e2m_test") == "e2m_test" {
		t.Fatal("virtual-key hashing boundary is broken")
	}
}

func TestWalletJournalBalanced(t *testing.T) {
	journal := WalletJournal{
		ID: "journal-1", Currency: "CNY", AmountMicros: 1_000_000,
		Entries: []WalletEntry{
			{JournalID: "journal-1", Account: WalletAccountPlatformCash, Direction: WalletEntryDebit, AmountMicros: 1_000_000, Currency: "CNY"},
			{JournalID: "journal-1", Account: WalletAccountUserAvailable, Direction: WalletEntryCredit, AmountMicros: 1_000_000, Currency: "CNY"},
		},
	}
	if !journal.Balanced() {
		t.Fatal("balanced journal rejected")
	}
	journal.Entries[1].AmountMicros--
	if journal.Balanced() {
		t.Fatal("unbalanced journal accepted")
	}
}
