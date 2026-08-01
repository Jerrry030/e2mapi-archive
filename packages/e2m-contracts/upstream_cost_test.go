package contracts

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUpstreamCostIdempotencyKeyIsStableAndScoped(t *testing.T) {
	first, err := UpstreamCostIdempotencyKey(42, "usage-1", UpstreamPriceInput, UpstreamCostCalculationVersionV1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := UpstreamCostIdempotencyKey(42, "usage-1", UpstreamPriceInput, UpstreamCostCalculationVersionV1)
	if err != nil || first != second || len(first) != 64 {
		t.Fatalf("unstable keys %q %q: %v", first, second, err)
	}
	other, err := UpstreamCostIdempotencyKey(42, "usage-1", UpstreamPriceOutput, UpstreamCostCalculationVersionV1)
	if err != nil || other == first {
		t.Fatalf("dimension did not scope key %q: %v", other, err)
	}
	if _, err := UpstreamCostIdempotencyKey(0, "usage", UpstreamPriceInput, "v1"); err == nil {
		t.Fatal("zero owner accepted")
	}
}

func TestUpstreamCostUsagePreservesMissingVersusObservedZero(t *testing.T) {
	zero := int64(0)
	present, _ := json.Marshal(UpstreamCostUsage{InputTokens: &zero})
	missing, _ := json.Marshal(UpstreamCostUsage{})
	if !strings.Contains(string(present), `"input_tokens":0`) || !strings.Contains(string(missing), `"input_tokens":null`) {
		t.Fatalf("quantity presence lost: present=%s missing=%s", present, missing)
	}
}

func TestUpstreamCostDerivedIsKnownButEstimatedIsNot(t *testing.T) {
	if !UpstreamCostAttributionIsKnown(UpstreamCostDerived) || !UpstreamCostAttributionIsKnown(UpstreamCostExact) {
		t.Fatal("exact and deterministic derived evidence must be known")
	}
	if UpstreamCostAttributionIsKnown(UpstreamCostEstimated) || UpstreamCostAttributionIsKnown(UpstreamCostUnknown) {
		t.Fatal("estimated or unknown evidence cannot enter the known column")
	}
}
