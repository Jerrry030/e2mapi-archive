package contracts

import (
	"encoding/json"
	"strings"
	"testing"
)

func trafficShareFence() GatewaySchedulingFence {
	return GatewaySchedulingFence{Scope: "recommendation-rollout/plan-1", Version: 7, Sequence: 3}
}

func TestGatewayAccountCurrentWeightJSONIsPresenceSafe(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantNil    bool
		wantWeight int
	}{
		{name: "missing", raw: `{"id":"account-1"}`, wantNil: true},
		{name: "null", raw: `{"id":"account-1","current_weight":null}`, wantNil: true},
		{name: "zero", raw: `{"id":"account-1","current_weight":0}`, wantWeight: 0},
		{name: "one hundred", raw: `{"id":"account-1","current_weight":100}`, wantWeight: 100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var account GatewayAccount
			if err := json.Unmarshal([]byte(test.raw), &account); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if test.wantNil {
				if account.CurrentWeight != nil {
					t.Fatalf("current_weight = %v, want unknown", *account.CurrentWeight)
				}
			} else if account.CurrentWeight == nil || *account.CurrentWeight != test.wantWeight {
				t.Fatalf("current_weight = %v, want %d", account.CurrentWeight, test.wantWeight)
			}

			normalized, issue := NormalizeConnectorGatewayAccounts([]GatewayAccount{account})
			if issue != "" {
				t.Fatalf("normalization rejected valid current_weight: %s", issue)
			}
			if !test.wantNil && (normalized[0].CurrentWeight == nil || *normalized[0].CurrentWeight != test.wantWeight) {
				t.Fatalf("normalized current_weight = %v, want %d", normalized[0].CurrentWeight, test.wantWeight)
			}
		})
	}
	unknown, err := json.Marshal(GatewayAccount{ID: "account-1"})
	if err != nil || strings.Contains(string(unknown), "current_weight") {
		t.Fatalf("unknown current_weight must be omitted: raw=%s err=%v", unknown, err)
	}
	zero := 0
	explicitZero, err := json.Marshal(GatewayAccount{ID: "account-1", CurrentWeight: &zero})
	if err != nil || !strings.Contains(string(explicitZero), `"current_weight":0`) {
		t.Fatalf("explicit zero current_weight was lost: raw=%s err=%v", explicitZero, err)
	}
}

func TestGatewayAccountCurrentWeightRejectsOutOfRangeAndDoesNotAlias(t *testing.T) {
	for _, weight := range []int{-1, 101} {
		if _, issue := NormalizeConnectorGatewayAccounts([]GatewayAccount{{ID: "account-1", CurrentWeight: &weight}}); issue == "" {
			t.Fatalf("out-of-range current_weight %d was accepted", weight)
		}
	}
	weight := 25
	normalized, issue := NormalizeConnectorGatewayAccounts([]GatewayAccount{{ID: "account-1", CurrentWeight: &weight}})
	if issue != "" {
		t.Fatal(issue)
	}
	weight = 99
	if normalized[0].CurrentWeight == nil || *normalized[0].CurrentWeight != 25 {
		t.Fatal("normalization retained a caller-owned current_weight pointer")
	}
}

func TestTrafficShareTaskIsClosedNumericL1AndRequiresFence(t *testing.T) {
	if ConnectorTaskGatewayTrafficShareSet != ConnectorTaskType("gateway.account.traffic_share.set") ||
		!IsConnectorCapability(ConnectorTaskGatewayTrafficShareSet) {
		t.Fatal("traffic-share set must be an exact closed connector task")
	}
	if got := ConnectorTaskGatewayTrafficShareSet.RiskLevel(); got != RiskLevelL1 {
		t.Fatalf("traffic-share risk = %q, want L1", got)
	}

	valid := ConnectorGatewayTrafficShareSetInput{AccountID: "account-1", Weight: 0, Fence: trafficShareFence()}
	if !ValidateConnectorGatewayTrafficShareSetInput(valid) {
		t.Fatal("explicit zero traffic share with a complete fence was rejected")
	}
	valid.Weight = 100
	if !ValidateConnectorGatewayTrafficShareSetInput(valid) {
		t.Fatal("100% traffic share was rejected")
	}

	for name, input := range map[string]ConnectorGatewayTrafficShareSetInput{
		"weight below zero":       {AccountID: "account-1", Weight: -1, Fence: trafficShareFence()},
		"weight over one hundred": {AccountID: "account-1", Weight: 101, Fence: trafficShareFence()},
		"missing account":         {Weight: 10, Fence: trafficShareFence()},
		"account url":             {AccountID: "https://gateway.example/account/1", Weight: 10, Fence: trafficShareFence()},
		"missing fence":           {AccountID: "account-1", Weight: 10},
		"missing generation":      {AccountID: "account-1", Weight: 10, Fence: GatewaySchedulingFence{Scope: "rollout", Sequence: 1}},
		"missing sequence":        {AccountID: "account-1", Weight: 10, Fence: GatewaySchedulingFence{Scope: "rollout", Version: 1}},
		"sensitive scope":         {AccountID: "account-1", Weight: 10, Fence: GatewaySchedulingFence{Scope: "https://secret.example", Version: 1, Sequence: 1}},
		"credential scope":        {AccountID: "account-1", Weight: 10, Fence: GatewaySchedulingFence{Scope: "sk-secret", Version: 1, Sequence: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			if ValidateConnectorGatewayTrafficShareSetInput(input) {
				t.Fatalf("invalid input was accepted: %+v", input)
			}
		})
	}
}

func TestTrafficShareReceiptMustExactlyMatchRequest(t *testing.T) {
	input := ConnectorGatewayTrafficShareSetInput{AccountID: "account-1", Weight: 25, Fence: trafficShareFence()}
	result := ConnectorGatewayTrafficShareSetResult{AccountID: input.AccountID, Weight: input.Weight, Fence: input.Fence}
	if !ValidateConnectorGatewayTrafficShareSetResult(input, result) {
		t.Fatal("matching traffic-share receipt was rejected")
	}

	wrongAccount := result
	wrongAccount.AccountID = "account-2"
	wrongWeight := result
	wrongWeight.Weight = 50
	wrongFence := result
	wrongFence.Fence.Sequence++
	for _, mismatched := range []ConnectorGatewayTrafficShareSetResult{wrongAccount, wrongWeight, wrongFence} {
		if ValidateConnectorGatewayTrafficShareSetResult(input, mismatched) {
			t.Fatalf("mismatched receipt was accepted: %+v", mismatched)
		}
	}
}

func TestTrafficShareSummaryRetainsZeroWithoutLeakingRawInput(t *testing.T) {
	input := ConnectorGatewayTrafficShareSetInput{AccountID: "account-1", Weight: 0, Fence: trafficShareFence()}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	summary := SummarizeConnectorTask(ConnectorTask{
		ID: "task-1", Type: ConnectorTaskGatewayTrafficShareSet, RiskLevel: RiskLevelL1, Input: raw,
	})
	if summary.TargetAccountID != input.AccountID || summary.TargetTrafficShare == nil || *summary.TargetTrafficShare != 0 ||
		summary.SchedulingFence == nil || *summary.SchedulingFence != input.Fence {
		t.Fatalf("traffic-share summary = %+v", summary)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"input", "credential", "secret", "url", "authorization"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("summary exposed forbidden raw/sensitive field %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"target_traffic_share":0`) {
		t.Fatalf("summary lost explicit numeric zero: %s", encoded)
	}

	invalid := input
	invalid.Fence.Scope = "rollout/private\nsecret"
	raw, _ = json.Marshal(invalid)
	invalidSummary := SummarizeConnectorTask(ConnectorTask{Type: ConnectorTaskGatewayTrafficShareSet, Input: raw})
	if invalidSummary.TargetAccountID != "" || invalidSummary.TargetTrafficShare != nil || invalidSummary.SchedulingFence != nil {
		t.Fatalf("invalid traffic-share input was partially projected: %+v", invalidSummary)
	}
}

func TestTrafficShareCapabilityIsAdvertisedOnlyForNewAPI(t *testing.T) {
	seen := 0
	for _, capability := range ExecutableGatewayCapabilities() {
		if capability.Name != CapabilitySetAccountTrafficShare {
			continue
		}
		seen++
		if capability.System != InstanceKindNewAPI || capability.Mode != CapabilityModeWrite ||
			capability.RiskLevel != RiskLevelL1 || !capability.Supported {
			t.Fatalf("unexpected traffic-share capability: %+v", capability)
		}
	}
	if seen != 1 {
		t.Fatalf("traffic-share capability count = %d, want exactly one NewAPI declaration", seen)
	}
}
