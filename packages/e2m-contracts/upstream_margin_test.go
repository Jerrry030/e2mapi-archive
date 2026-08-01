package contracts

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUpstreamMarginContractKeepsBlockedMarginNullable(t *testing.T) {
	model := UpstreamMarginReadModel{
		UserID:                      42,
		AttributableCoverage:        CanonicalDecimal("0"),
		MinimumAttributableCoverage: UpstreamMarginDefaultMinimumAttributableCoverage,
		Claim: UpstreamMarginClaim{
			Status:         UpstreamMarginClaimBlocked,
			BlockedReasons: []UpstreamMarginBlockedReason{UpstreamMarginBlockedRevenueUnavailable},
		},
	}
	payload, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"attributable_coverage":"0"`, `"minimum_attributable_coverage":"0.9"`, `"margin_amount":null`, `"margin_rate":null`} {
		if !strings.Contains(string(payload), want) {
			t.Fatalf("missing %s in %s", want, payload)
		}
	}
}

func TestUpstreamMarginEnumsAreClosed(t *testing.T) {
	for _, bucket := range []UpstreamMarginCostBucket{
		UpstreamMarginCostExact, UpstreamMarginCostEstimated, UpstreamMarginCostUnknown,
		UpstreamMarginCostUnattributed, UpstreamMarginCostExpired,
	} {
		if !IsUpstreamMarginCostBucket(bucket) {
			t.Fatalf("known bucket rejected: %q", bucket)
		}
	}
	if IsUpstreamMarginCostBucket("derived") {
		t.Fatal("derived leaked into the five-column presentation")
	}
	for _, status := range []UpstreamMarginClaimStatus{UpstreamMarginClaimExact, UpstreamMarginClaimEstimated, UpstreamMarginClaimBlocked} {
		if !IsUpstreamMarginClaimStatus(status) {
			t.Fatalf("known status rejected: %q", status)
		}
	}
	if IsUpstreamMarginClaimStatus("true") || IsUpstreamMarginBlockedReason("best_effort") {
		t.Fatal("unknown margin vocabulary accepted")
	}
}
