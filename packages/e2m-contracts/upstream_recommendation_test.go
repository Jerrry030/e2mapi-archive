package contracts

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUpstreamRecommendationContractKeepsMoneyAsDecimalStrings(t *testing.T) {
	value := UpstreamRecommendation{
		UserID: 42, Status: UpstreamRecommendationOpen,
		FromCost: UpstreamRecommendationCostRange{Lower: "8", Expected: "10", Upper: "12"},
		ToCost:   UpstreamRecommendationCostRange{Lower: "7", Expected: "7", Upper: "7"},
		Savings: UpstreamRecommendationSavingsRange{
			AmountLower: "1", AmountExpected: "3", AmountUpper: "5",
			PercentLower: "0.083333333333333333", PercentExpected: "0.3", PercentUpper: "0.625",
		},
	}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"amount_lower":"1"`, `"percent_expected":"0.3"`, `"expected":"10"`} {
		if !strings.Contains(string(payload), want) {
			t.Fatalf("missing %s in %s", want, payload)
		}
	}
}

func TestUpstreamRecommendationVocabularyIsClosed(t *testing.T) {
	if !IsUpstreamRecommendationStatus(UpstreamRecommendationDryRunning) || IsUpstreamRecommendationStatus("executing") {
		t.Fatal("status vocabulary is not closed")
	}
	if !IsUpstreamRecommendationConstraintKind(UpstreamRecommendationConstraintCapacity) || IsUpstreamRecommendationConstraintKind("cheapest") {
		t.Fatal("constraint vocabulary is not closed")
	}
	if !IsUpstreamRecommendationConstraintStatus(UpstreamRecommendationConstraintUnknown) || IsUpstreamRecommendationConstraintStatus("ignored") {
		t.Fatal("constraint status vocabulary is not closed")
	}
}
