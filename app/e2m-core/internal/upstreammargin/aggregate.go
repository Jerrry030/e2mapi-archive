// Package upstreammargin builds a read-only gross-margin view from immutable
// purchase-cost and revenue facts. It performs no store lookups and never
// invents FX, revenue, or missing purchase cost.
package upstreammargin

import (
	"errors"
	"math/big"
	"sort"
	"strings"

	"e2m.local/contracts"
)

var ErrInvalidInput = errors.New("upstream margin: invalid input")

var minimumAttributableCoverage = big.NewRat(9, 10)

// Aggregate builds one deterministic, owner-scoped margin view. Inputs must
// already be the immutable facts selected for one reporting window. No revenue
// facts means revenue is unavailable, not zero. No FX conversion is attempted.
func Aggregate(userID int64, costs []contracts.UpstreamCostFact, revenues []contracts.UpstreamRevenueFact) (contracts.UpstreamMarginReadModel, error) {
	model := emptyModel(userID)
	if userID <= 0 {
		return contracts.UpstreamMarginReadModel{}, ErrInvalidInput
	}

	costTotals := newMoneyTotals()
	for _, fact := range costs {
		bucket, amount, currency, err := classifyCost(userID, fact)
		if err != nil {
			return contracts.UpstreamMarginReadModel{}, err
		}
		column := costColumn(&model.Costs, bucket)
		column.FactCount++
		if reason := strings.TrimSpace(fact.ReasonCode); reason != "" {
			if column.Reasons == nil {
				column.Reasons = make(map[string]int)
			}
			column.Reasons[reason]++
		}
		if amount != nil {
			costTotals.add(bucket, currency, amount)
		}
		switch bucket {
		case contracts.UpstreamMarginCostExact:
			model.AttributableCostFactCount++
			if fact.Attribution == contracts.UpstreamCostDerived {
				model.Costs.DerivedFactCount++
			} else {
				model.Costs.ExactFactCount++
			}
		case contracts.UpstreamMarginCostEstimated:
			model.AttributableCostFactCount++
		}
	}

	model.TotalCostFactCount = len(costs)
	model.UncoveredCostFactCount = model.TotalCostFactCount - model.AttributableCostFactCount
	coverage, err := ratio(model.AttributableCostFactCount, model.TotalCostFactCount)
	if err != nil {
		return contracts.UpstreamMarginReadModel{}, err
	}
	model.AttributableCoverage = coverage
	coverageRat, _ := coverage.Rat()
	model.CoverageGatePassed = model.TotalCostFactCount > 0 && coverageRat.Cmp(minimumAttributableCoverage) >= 0

	model.Costs.Exact.Amounts, err = costTotals.amountsFor(contracts.UpstreamMarginCostExact)
	if err != nil {
		return contracts.UpstreamMarginReadModel{}, err
	}
	model.Costs.Estimated.Amounts, err = costTotals.amountsFor(contracts.UpstreamMarginCostEstimated)
	if err != nil {
		return contracts.UpstreamMarginReadModel{}, err
	}

	revenueTotals := make(map[string]*big.Rat)
	for _, fact := range revenues {
		amount, currency, err := validateRevenue(userID, fact)
		if err != nil {
			return contracts.UpstreamMarginReadModel{}, err
		}
		addMoney(revenueTotals, currency, amount)
	}
	model.Revenue, err = moneySlice(revenueTotals)
	if err != nil {
		return contracts.UpstreamMarginReadModel{}, err
	}

	model.Claim, err = buildClaim(model, costTotals.attributable, revenueTotals)
	if err != nil {
		return contracts.UpstreamMarginReadModel{}, err
	}
	return model, nil
}

type bucketMoneyTotals struct {
	byBucket     map[contracts.UpstreamMarginCostBucket]map[string]*big.Rat
	attributable map[string]*big.Rat
}

func newMoneyTotals() *bucketMoneyTotals {
	return &bucketMoneyTotals{
		byBucket: map[contracts.UpstreamMarginCostBucket]map[string]*big.Rat{
			contracts.UpstreamMarginCostExact:     {},
			contracts.UpstreamMarginCostEstimated: {},
		},
		attributable: make(map[string]*big.Rat),
	}
}

func (totals *bucketMoneyTotals) add(bucket contracts.UpstreamMarginCostBucket, currency string, amount *big.Rat) {
	addMoney(totals.byBucket[bucket], currency, amount)
	addMoney(totals.attributable, currency, amount)
}

func (totals *bucketMoneyTotals) amountsFor(bucket contracts.UpstreamMarginCostBucket) ([]contracts.UpstreamMarginMoney, error) {
	return moneySlice(totals.byBucket[bucket])
}

func emptyModel(userID int64) contracts.UpstreamMarginReadModel {
	return contracts.UpstreamMarginReadModel{
		UserID: userID,
		Costs: contracts.UpstreamMarginCostBreakdown{
			Exact:        emptyColumn(),
			Estimated:    emptyColumn(),
			Unknown:      emptyColumn(),
			Unattributed: emptyColumn(),
			Expired:      emptyColumn(),
		},
		Revenue:                     []contracts.UpstreamMarginMoney{},
		AttributableCoverage:        contracts.CanonicalDecimal("0"),
		MinimumAttributableCoverage: contracts.UpstreamMarginDefaultMinimumAttributableCoverage,
		Claim: contracts.UpstreamMarginClaim{
			Status:         contracts.UpstreamMarginClaimBlocked,
			BlockedReasons: []contracts.UpstreamMarginBlockedReason{},
		},
	}
}

func emptyColumn() contracts.UpstreamMarginCostColumn {
	return contracts.UpstreamMarginCostColumn{Amounts: []contracts.UpstreamMarginMoney{}}
}

func classifyCost(userID int64, fact contracts.UpstreamCostFact) (contracts.UpstreamMarginCostBucket, *big.Rat, string, error) {
	if fact.UserID != userID || !contracts.IsUpstreamCostAttribution(fact.Attribution) || !contracts.IsUpstreamCostPriceStatus(fact.PriceStatus) {
		return "", nil, "", ErrInvalidInput
	}
	// Expiry wins over attribution: an exact-looking fact whose price interval
	// did not cover the usage event is never allowed into attributable cost.
	if fact.PriceStatus == contracts.UpstreamCostPriceExpired {
		return contracts.UpstreamMarginCostExpired, nil, "", nil
	}
	if fact.Attribution == contracts.UpstreamCostUnattributed {
		return contracts.UpstreamMarginCostUnattributed, nil, "", nil
	}
	if fact.PriceStatus != contracts.UpstreamCostPriceValid || fact.Attribution == contracts.UpstreamCostUnknown {
		return contracts.UpstreamMarginCostUnknown, nil, "", nil
	}

	var bucket contracts.UpstreamMarginCostBucket
	switch fact.Attribution {
	case contracts.UpstreamCostExact, contracts.UpstreamCostDerived:
		bucket = contracts.UpstreamMarginCostExact
	case contracts.UpstreamCostEstimated:
		bucket = contracts.UpstreamMarginCostEstimated
	default:
		return "", nil, "", ErrInvalidInput
	}
	amount, currency, err := validMoney(fact.Amount, fact.Currency)
	if err != nil {
		return "", nil, "", err
	}
	return bucket, amount, currency, nil
}

func validateRevenue(userID int64, fact contracts.UpstreamRevenueFact) (*big.Rat, string, error) {
	if fact.UserID != userID || strings.TrimSpace(fact.RevenueObservationID) == "" || strings.TrimSpace(fact.CalculationVersion) == "" || fact.OccurredAt.IsZero() {
		return nil, "", ErrInvalidInput
	}
	amount := fact.Amount
	return validMoney(&amount, fact.Currency)
}

func validMoney(value *contracts.CanonicalDecimal, currency string) (*big.Rat, string, error) {
	if value == nil || !validCurrency(currency) {
		return nil, "", ErrInvalidInput
	}
	amount, err := value.Rat()
	if err != nil || amount.Sign() < 0 {
		return nil, "", ErrInvalidInput
	}
	return amount, currency, nil
}

func validCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func costColumn(costs *contracts.UpstreamMarginCostBreakdown, bucket contracts.UpstreamMarginCostBucket) *contracts.UpstreamMarginCostColumn {
	switch bucket {
	case contracts.UpstreamMarginCostExact:
		return &costs.Exact
	case contracts.UpstreamMarginCostEstimated:
		return &costs.Estimated
	case contracts.UpstreamMarginCostUnknown:
		return &costs.Unknown
	case contracts.UpstreamMarginCostUnattributed:
		return &costs.Unattributed
	case contracts.UpstreamMarginCostExpired:
		return &costs.Expired
	default:
		panic("unreachable margin cost bucket")
	}
}

func buildClaim(model contracts.UpstreamMarginReadModel, costs, revenues map[string]*big.Rat) (contracts.UpstreamMarginClaim, error) {
	claim := contracts.UpstreamMarginClaim{
		Status:         contracts.UpstreamMarginClaimBlocked,
		BlockedReasons: []contracts.UpstreamMarginBlockedReason{},
	}
	if model.TotalCostFactCount == 0 {
		claim.BlockedReasons = append(claim.BlockedReasons, contracts.UpstreamMarginBlockedNoCostFacts)
	} else if !model.CoverageGatePassed {
		claim.BlockedReasons = append(claim.BlockedReasons, contracts.UpstreamMarginBlockedCoverageBelowGate)
	}
	if len(revenues) == 0 {
		claim.BlockedReasons = append(claim.BlockedReasons, contracts.UpstreamMarginBlockedRevenueUnavailable)
	}
	currencies := make(map[string]struct{}, len(costs)+len(revenues))
	for currency := range costs {
		currencies[currency] = struct{}{}
	}
	for currency := range revenues {
		currencies[currency] = struct{}{}
	}
	if len(currencies) > 1 {
		claim.BlockedReasons = append(claim.BlockedReasons, contracts.UpstreamMarginBlockedCrossCurrency)
	}
	if len(claim.BlockedReasons) > 0 || len(currencies) != 1 {
		return claim, nil
	}

	for currency := range currencies {
		claim.Currency = currency
	}
	revenue := cloneRat(revenues[claim.Currency])
	if revenue == nil {
		return claim, nil
	}
	cost := cloneRat(costs[claim.Currency])
	if cost == nil {
		cost = new(big.Rat)
	}
	margin := new(big.Rat).Sub(revenue, cost)
	var err error
	if claim.Revenue, err = decimalPtr(revenue); err != nil {
		return contracts.UpstreamMarginClaim{}, err
	}
	if claim.PurchaseCost, err = decimalPtr(cost); err != nil {
		return contracts.UpstreamMarginClaim{}, err
	}
	if claim.MarginAmount, err = decimalPtr(margin); err != nil {
		return contracts.UpstreamMarginClaim{}, err
	}
	if revenue.Sign() > 0 {
		if claim.MarginRate, err = decimalPtr(new(big.Rat).Quo(margin, revenue)); err != nil {
			return contracts.UpstreamMarginClaim{}, err
		}
	}
	claim.Status = contracts.UpstreamMarginClaimExact
	if model.UncoveredCostFactCount > 0 || model.Costs.Estimated.FactCount > 0 {
		claim.Status = contracts.UpstreamMarginClaimEstimated
	}
	return claim, nil
}

func ratio(numerator, denominator int) (contracts.CanonicalDecimal, error) {
	if numerator < 0 || denominator < 0 || numerator > denominator {
		return "", ErrInvalidInput
	}
	if denominator == 0 {
		return contracts.CanonicalDecimal("0"), nil
	}
	value, err := contracts.QuantizeCanonicalDecimal(new(big.Rat).SetFrac64(int64(numerator), int64(denominator)), contracts.UpstreamDecimalMaxScale)
	if err != nil {
		return "", ErrInvalidInput
	}
	return value, nil
}

func addMoney(target map[string]*big.Rat, currency string, amount *big.Rat) {
	if current := target[currency]; current != nil {
		current.Add(current, amount)
		return
	}
	target[currency] = cloneRat(amount)
}

func moneySlice(values map[string]*big.Rat) ([]contracts.UpstreamMarginMoney, error) {
	currencies := make([]string, 0, len(values))
	for currency := range values {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	result := make([]contracts.UpstreamMarginMoney, 0, len(currencies))
	for _, currency := range currencies {
		amount, err := contracts.QuantizeCanonicalDecimal(values[currency], contracts.UpstreamDecimalMaxScale)
		if err != nil {
			return nil, ErrInvalidInput
		}
		result = append(result, contracts.UpstreamMarginMoney{Currency: currency, Amount: amount})
	}
	return result, nil
}

func decimalPtr(value *big.Rat) (*contracts.CanonicalDecimal, error) {
	decimal, err := contracts.QuantizeCanonicalDecimal(value, contracts.UpstreamDecimalMaxScale)
	if err != nil {
		return nil, ErrInvalidInput
	}
	return &decimal, nil
}

func cloneRat(value *big.Rat) *big.Rat {
	if value == nil {
		return nil
	}
	return new(big.Rat).Set(value)
}
