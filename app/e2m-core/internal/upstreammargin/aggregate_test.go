package upstreammargin

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestAggregateBuildsExactMarginAndPreservesDerivedBreakdown(t *testing.T) {
	costs := []contracts.UpstreamCostFact{
		costFact(t, "exact", contracts.UpstreamCostExact, contracts.UpstreamCostPriceValid, "4", "USD"),
		costFact(t, "derived", contracts.UpstreamCostDerived, contracts.UpstreamCostPriceValid, "2", "USD"),
	}
	got, err := Aggregate(42, costs, []contracts.UpstreamRevenueFact{revenueFact(t, "10", "USD")})
	if err != nil {
		t.Fatal(err)
	}
	if got.AttributableCoverage != "1" || !got.CoverageGatePassed || got.Costs.Exact.FactCount != 2 || got.Costs.ExactFactCount != 1 || got.Costs.DerivedFactCount != 1 {
		t.Fatalf("exact coverage/breakdown wrong: %+v", got)
	}
	if len(got.Costs.Exact.Amounts) != 1 || got.Costs.Exact.Amounts[0].Amount != "6" {
		t.Fatalf("exact cost total wrong: %+v", got.Costs.Exact)
	}
	if got.Claim.Status != contracts.UpstreamMarginClaimExact || got.Claim.Currency != "USD" || decimalValue(got.Claim.Revenue) != "10" || decimalValue(got.Claim.PurchaseCost) != "6" || decimalValue(got.Claim.MarginAmount) != "4" || decimalValue(got.Claim.MarginRate) != "0.4" {
		t.Fatalf("exact margin wrong: %+v", got.Claim)
	}
}

func TestAggregateClassifiesExpiredBeforeAttributionAndFailsCoverageClosed(t *testing.T) {
	costs := make([]contracts.UpstreamCostFact, 0, 10)
	for index := 0; index < 8; index++ {
		costs = append(costs, costFact(t, "exact", contracts.UpstreamCostExact, contracts.UpstreamCostPriceValid, "1", "USD"))
	}
	unknown := costFact(t, contracts.UpstreamCostReasonPriceUnavailable, contracts.UpstreamCostUnknown, contracts.UpstreamCostPriceUnavailable, "", "")
	expired := costFact(t, contracts.UpstreamCostReasonPriceExpired, contracts.UpstreamCostExact, contracts.UpstreamCostPriceExpired, "", "")
	costs = append(costs, unknown, expired)

	got, err := Aggregate(42, costs, []contracts.UpstreamRevenueFact{revenueFact(t, "20", "USD")})
	if err != nil {
		t.Fatal(err)
	}
	if got.AttributableCoverage != "0.8" || got.CoverageGatePassed || got.Costs.Unknown.FactCount != 1 || got.Costs.Expired.FactCount != 1 {
		t.Fatalf("classification/coverage wrong: %+v", got)
	}
	if got.Costs.Expired.Reasons[contracts.UpstreamCostReasonPriceExpired] != 1 || got.Claim.Status != contracts.UpstreamMarginClaimBlocked || !reflect.DeepEqual(got.Claim.BlockedReasons, []contracts.UpstreamMarginBlockedReason{contracts.UpstreamMarginBlockedCoverageBelowGate}) {
		t.Fatalf("coverage did not fail closed: %+v", got.Claim)
	}
	assertClaimMoneyNil(t, got.Claim)
}

func TestAggregateAcceptsNinetyPercentOnlyAsEstimatedClaim(t *testing.T) {
	costs := make([]contracts.UpstreamCostFact, 0, 10)
	for index := 0; index < 9; index++ {
		costs = append(costs, costFact(t, "exact", contracts.UpstreamCostExact, contracts.UpstreamCostPriceValid, "1", "USD"))
	}
	costs = append(costs, costFact(t, "missing", contracts.UpstreamCostUnattributed, contracts.UpstreamCostPriceUnavailable, "", ""))

	got, err := Aggregate(42, costs, []contracts.UpstreamRevenueFact{revenueFact(t, "12", "USD")})
	if err != nil {
		t.Fatal(err)
	}
	if got.AttributableCoverage != "0.9" || !got.CoverageGatePassed || got.UncoveredCostFactCount != 1 || got.Claim.Status != contracts.UpstreamMarginClaimEstimated {
		t.Fatalf("90%% boundary wrong: %+v", got)
	}
	if decimalValue(got.Claim.PurchaseCost) != "9" || decimalValue(got.Claim.MarginAmount) != "3" || decimalValue(got.Claim.MarginRate) != "0.25" {
		t.Fatalf("estimated claim arithmetic wrong: %+v", got.Claim)
	}
}

func TestAggregateEstimatedCostNeverBecomesExactMargin(t *testing.T) {
	got, err := Aggregate(42,
		[]contracts.UpstreamCostFact{costFact(t, "estimated", contracts.UpstreamCostEstimated, contracts.UpstreamCostPriceValid, "3", "USD")},
		[]contracts.UpstreamRevenueFact{revenueFact(t, "5", "USD")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.AttributableCoverage != "1" || got.Claim.Status != contracts.UpstreamMarginClaimEstimated || got.Costs.Estimated.FactCount != 1 {
		t.Fatalf("estimated cost was overstated: %+v", got)
	}
}

func TestAggregateWithoutRevenueNeverClaimsMargin(t *testing.T) {
	got, err := Aggregate(42, []contracts.UpstreamCostFact{costFact(t, "exact", contracts.UpstreamCostExact, contracts.UpstreamCostPriceValid, "3", "USD")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Claim.Status != contracts.UpstreamMarginClaimBlocked || !reflect.DeepEqual(got.Claim.BlockedReasons, []contracts.UpstreamMarginBlockedReason{contracts.UpstreamMarginBlockedRevenueUnavailable}) {
		t.Fatalf("missing revenue did not block: %+v", got.Claim)
	}
	assertClaimMoneyNil(t, got.Claim)
}

func TestAggregateNeverCombinesCurrenciesWithoutFX(t *testing.T) {
	costs := []contracts.UpstreamCostFact{
		costFact(t, "usd", contracts.UpstreamCostExact, contracts.UpstreamCostPriceValid, "2", "USD"),
		costFact(t, "cny", contracts.UpstreamCostExact, contracts.UpstreamCostPriceValid, "7", "CNY"),
	}
	revenues := []contracts.UpstreamRevenueFact{revenueFact(t, "4", "USD"), revenueFact(t, "14", "CNY")}
	got, err := Aggregate(42, costs, revenues)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Costs.Exact.Amounts) != 2 || got.Costs.Exact.Amounts[0].Currency != "CNY" || got.Costs.Exact.Amounts[1].Currency != "USD" || len(got.Revenue) != 2 {
		t.Fatalf("currency-local totals lost: %+v", got)
	}
	if got.Claim.Status != contracts.UpstreamMarginClaimBlocked || !reflect.DeepEqual(got.Claim.BlockedReasons, []contracts.UpstreamMarginBlockedReason{contracts.UpstreamMarginBlockedCrossCurrency}) {
		t.Fatalf("cross-currency claim was not blocked: %+v", got.Claim)
	}
	assertClaimMoneyNil(t, got.Claim)
}

func TestAggregateZeroRevenueHasMarginAmountButNoUndefinedRate(t *testing.T) {
	got, err := Aggregate(42,
		[]contracts.UpstreamCostFact{costFact(t, "exact", contracts.UpstreamCostExact, contracts.UpstreamCostPriceValid, "2", "USD")},
		[]contracts.UpstreamRevenueFact{revenueFact(t, "0", "USD")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Claim.Status != contracts.UpstreamMarginClaimExact || decimalValue(got.Claim.MarginAmount) != "-2" || got.Claim.MarginRate != nil {
		t.Fatalf("zero revenue semantics wrong: %+v", got.Claim)
	}
}

func TestAggregateRejectsCrossOwnerAndMalformedMoney(t *testing.T) {
	crossOwner := costFact(t, "exact", contracts.UpstreamCostExact, contracts.UpstreamCostPriceValid, "1", "USD")
	crossOwner.UserID = 7
	if _, err := Aggregate(42, []contracts.UpstreamCostFact{crossOwner}, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-owner fact accepted: %v", err)
	}
	malformed := costFact(t, "exact", contracts.UpstreamCostExact, contracts.UpstreamCostPriceValid, "1", "USD")
	bad := contracts.CanonicalDecimal("1.00")
	malformed.Amount = &bad
	if _, err := Aggregate(42, []contracts.UpstreamCostFact{malformed}, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("malformed amount accepted: %v", err)
	}
}

func TestAggregateIsDeterministicAcrossInputOrder(t *testing.T) {
	firstCost := costFact(t, "first", contracts.UpstreamCostExact, contracts.UpstreamCostPriceValid, "1.25", "USD")
	secondCost := costFact(t, "second", contracts.UpstreamCostEstimated, contracts.UpstreamCostPriceValid, "2.75", "USD")
	firstRevenue := revenueFact(t, "5", "USD")
	secondRevenue := revenueFact(t, "3", "USD")
	forward, err := Aggregate(42, []contracts.UpstreamCostFact{firstCost, secondCost}, []contracts.UpstreamRevenueFact{firstRevenue, secondRevenue})
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := Aggregate(42, []contracts.UpstreamCostFact{secondCost, firstCost}, []contracts.UpstreamRevenueFact{secondRevenue, firstRevenue})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("input order changed aggregate: forward=%+v reverse=%+v", forward, reverse)
	}
}

func TestAggregateRejectsAggregateNumericOverflowWithoutPanicking(t *testing.T) {
	maximum := "99999999999999999999"
	costs := []contracts.UpstreamCostFact{
		costFact(t, "first", contracts.UpstreamCostExact, contracts.UpstreamCostPriceValid, maximum, "USD"),
		costFact(t, "second", contracts.UpstreamCostExact, contracts.UpstreamCostPriceValid, maximum, "USD"),
	}
	if _, err := Aggregate(42, costs, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("aggregate overflow was not rejected: %v", err)
	}
}

func costFact(t *testing.T, reason string, attribution contracts.UpstreamCostAttribution, status contracts.UpstreamCostPriceStatus, amount, currency string) contracts.UpstreamCostFact {
	t.Helper()
	fact := contracts.UpstreamCostFact{UserID: 42, Attribution: attribution, PriceStatus: status, ReasonCode: reason}
	if amount != "" {
		value, err := contracts.ParseCanonicalDecimal(amount)
		if err != nil {
			t.Fatal(err)
		}
		fact.Amount = &value
		fact.Currency = currency
	}
	return fact
}

func revenueFact(t *testing.T, amount, currency string) contracts.UpstreamRevenueFact {
	t.Helper()
	value, err := contracts.ParseCanonicalDecimal(amount)
	if err != nil {
		t.Fatal(err)
	}
	return contracts.UpstreamRevenueFact{
		UserID: 42, RevenueObservationID: "revenue", Amount: value, Currency: currency,
		CalculationVersion: "v1", OccurredAt: time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC),
	}
}

func decimalValue(value *contracts.CanonicalDecimal) contracts.CanonicalDecimal {
	if value == nil {
		return ""
	}
	return *value
}

func assertClaimMoneyNil(t *testing.T, claim contracts.UpstreamMarginClaim) {
	t.Helper()
	if claim.Revenue != nil || claim.PurchaseCost != nil || claim.MarginAmount != nil || claim.MarginRate != nil || claim.Currency != "" {
		t.Fatalf("blocked claim leaked money: %+v", claim)
	}
}
