package contracts

import (
	"errors"
	"reflect"
	"testing"
)

func intPointer(value int) *int { return &value }

func TestCompileHybridAccountWeightsPreservesOwnerRatiosAndZeros(t *testing.T) {
	got, err := CompileHybridAccountWeights(map[ResourceClass]int{
		ResourceClassOwner: 80, ResourceClassEconomy: 20, ResourceClassStable: 0,
	}, []HybridWeightAccount{
		{AccountID: "owner-b", Class: ResourceClassOwner, CurrentWeight: intPointer(30), Schedulable: true},
		{AccountID: "stable", Class: ResourceClassStable, CurrentWeight: intPointer(90)},
		{AccountID: "economy", Class: ResourceClassEconomy, CurrentWeight: intPointer(10)},
		{AccountID: "owner-a", Class: ResourceClassOwner, CurrentWeight: intPointer(10), Schedulable: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []HybridAccountWeight{
		{AccountID: "economy", Class: ResourceClassEconomy, Weight: 0, Schedulable: true},
		{AccountID: "owner-a", Class: ResourceClassOwner, Weight: 3, Schedulable: true},
		{AccountID: "owner-b", Class: ResourceClassOwner, Weight: 17, Schedulable: true},
		{AccountID: "stable", Class: ResourceClassStable, Weight: 90, Schedulable: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("weights = %#v, want %#v", got, want)
	}
}

func TestCompileHybridAccountWeightsEqualFallbackAndLargestRemainder(t *testing.T) {
	got, err := CompileHybridAccountWeights(map[ResourceClass]int{
		ResourceClassOwner: 20, ResourceClassEconomy: 40, ResourceClassStable: 40,
	}, []HybridWeightAccount{
		{AccountID: "owner-c", Class: ResourceClassOwner, CurrentWeight: nil, Schedulable: true},
		{AccountID: "owner-b", Class: ResourceClassOwner, CurrentWeight: intPointer(0), Schedulable: true},
		{AccountID: "owner-a", Class: ResourceClassOwner, CurrentWeight: intPointer(100), Schedulable: true},
		{AccountID: "economy", Class: ResourceClassEconomy},
		{AccountID: "stable", Class: ResourceClassStable},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOwner := map[string]int{"owner-a": 0, "owner-b": 0, "owner-c": 0}
	for _, weight := range got {
		if want, owner := wantOwner[weight.AccountID]; owner && weight.Weight != want {
			t.Fatalf("%s weight = %d, want %d", weight.AccountID, weight.Weight, want)
		}
	}
	percent, ok := HybridAccountWeightPercent(got)
	if !ok || percent[ResourceClassOwner] != 20 || percent[ResourceClassEconomy] != 40 || percent[ResourceClassStable] != 40 {
		t.Fatalf("percent = %#v, ok=%t", percent, ok)
	}
}

func TestCompileHybridAccountWeightsFailsClosedForMissingAggregate(t *testing.T) {
	_, err := CompileHybridAccountWeights(map[ResourceClass]int{
		ResourceClassOwner: 100, ResourceClassEconomy: 0, ResourceClassStable: 0,
	}, []HybridWeightAccount{
		{AccountID: "owner", Class: ResourceClassOwner, CurrentWeight: intPointer(100)},
		{AccountID: "stable", Class: ResourceClassStable},
	})
	if !errors.Is(err, ErrHybridWeightsUnrepresentable) {
		t.Fatalf("error = %v", err)
	}
}

func TestActualHybridPercentRetainsExplicitZero(t *testing.T) {
	actual, err := ActualHybridPercent([]HybridWeightAccount{
		{AccountID: "owner", Class: ResourceClassOwner, CurrentWeight: intPointer(70), Schedulable: true},
		{AccountID: "economy", Class: ResourceClassEconomy, CurrentWeight: intPointer(10), Schedulable: true},
		{AccountID: "stable", Class: ResourceClassStable, CurrentWeight: intPointer(0)},
	})
	if err != nil || actual[ResourceClassStable] != 0 || len(actual) != 3 {
		t.Fatalf("actual = %#v, err = %v", actual, err)
	}
}

func TestCompileHybridAccountWeightsFailsClosedWhenNewAPICannotRepresentRatio(t *testing.T) {
	owners := make([]HybridWeightAccount, 10)
	for index := range owners {
		owners[index] = HybridWeightAccount{AccountID: string(rune('a' + index)), Class: ResourceClassOwner, CurrentWeight: intPointer(0), Schedulable: true}
	}
	accounts := append(owners,
		HybridWeightAccount{AccountID: "economy", Class: ResourceClassEconomy},
		HybridWeightAccount{AccountID: "stable", Class: ResourceClassStable},
	)
	_, err := CompileHybridAccountWeights(map[ResourceClass]int{
		ResourceClassOwner: 1, ResourceClassEconomy: 99, ResourceClassStable: 0,
	}, accounts)
	if !errors.Is(err, ErrHybridWeightsUnrepresentable) {
		t.Fatalf("error = %v", err)
	}
}

func TestCompileHybridAccountWeightsCanRestorePreviouslyDisabledOwnerPool(t *testing.T) {
	accounts := []HybridWeightAccount{
		{AccountID: "owner", Class: ResourceClassOwner, CurrentWeight: intPointer(40), Schedulable: false},
		{AccountID: "economy", Class: ResourceClassEconomy, CurrentWeight: intPointer(90), Schedulable: true},
		{AccountID: "stable", Class: ResourceClassStable, CurrentWeight: intPointer(0), Schedulable: false},
	}
	zeroOwner, err := CompileHybridAccountWeights(map[ResourceClass]int{
		ResourceClassOwner: 0, ResourceClassEconomy: 100, ResourceClassStable: 0,
	}, accounts)
	if err != nil || zeroOwner[1].Schedulable {
		t.Fatalf("zero owner weights=%#v err=%v", zeroOwner, err)
	}
	restored, err := CompileHybridAccountWeights(map[ResourceClass]int{
		ResourceClassOwner: 80, ResourceClassEconomy: 20, ResourceClassStable: 0,
	}, accounts)
	if err != nil {
		t.Fatal(err)
	}
	percent, ok := HybridAccountWeightPercent(restored)
	if !ok || !restored[1].Schedulable || percent[ResourceClassOwner] != 80 || percent[ResourceClassEconomy] != 20 {
		t.Fatalf("restored=%#v percent=%#v ok=%t", restored, percent, ok)
	}
}

func TestHybridAccountWeightsRejectConnectorAccountOverflow(t *testing.T) {
	accounts := make([]HybridWeightAccount, 0, MaxConnectorAccounts+1)
	accounts = append(accounts,
		HybridWeightAccount{AccountID: "economy", Class: ResourceClassEconomy},
		HybridWeightAccount{AccountID: "stable", Class: ResourceClassStable},
	)
	for index := 0; len(accounts) <= MaxConnectorAccounts; index++ {
		accounts = append(accounts, HybridWeightAccount{AccountID: "owner-" + string(rune(index+0x100)), Class: ResourceClassOwner})
	}
	if _, err := CompileHybridAccountWeights(map[ResourceClass]int{
		ResourceClassOwner: 100, ResourceClassEconomy: 0, ResourceClassStable: 0,
	}, accounts); !errors.Is(err, ErrHybridWeightsUnrepresentable) {
		t.Fatalf("compile error=%v", err)
	}
	values := make([]HybridAccountWeight, MaxConnectorAccounts+1)
	if ValidHybridAccountWeights(values) {
		t.Fatal("oversized persisted write set was accepted")
	}
}

func TestHybridRoutingFenceIdentity(t *testing.T) {
	scope := HybridRoutingFenceScope("instance-a", "account-7")
	instanceID, accountID, ok := GatewaySchedulingFenceHybridIdentity(GatewaySchedulingFence{Scope: scope, Version: 2, Sequence: 1})
	if !ok || instanceID != "instance-a" || accountID != "account-7" {
		t.Fatalf("identity = %q %q %t", instanceID, accountID, ok)
	}
	if _, _, ok := GatewaySchedulingFenceHybridIdentity(GatewaySchedulingFence{Scope: "hybrid-routing/instance/instance-a/account/account-7/extra", Version: 2, Sequence: 1}); ok {
		t.Fatal("malformed hybrid fence was accepted")
	}
}
