// Package upstreamrecommendation builds evidence-bound advice and evaluates
// its lifecycle. It is pure domain code: no store, HTTP, reconcile, or gateway
// dependency is permitted here.
package upstreamrecommendation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"e2m.local/contracts"
)

var (
	ErrInvalidCandidate  = errors.New("upstream recommendation: invalid candidate")
	ErrUnsafeCandidate   = errors.New("upstream recommendation: constraints did not pass")
	ErrInvalidTransition = errors.New("upstream recommendation: invalid transition")
)

const fingerprintDomain = "e2m.upstream-recommendation.v1"

// Build validates all evidence and constraints, computes a stable fingerprint,
// and returns an open recommendation. Unknown and blocked constraints fail
// closed and cannot be encoded as executable advice.
func Build(id string, input contracts.UpstreamRecommendationCandidate) (contracts.UpstreamRecommendation, error) {
	input = normalizeCandidate(input)
	if strings.TrimSpace(id) == "" || !validCandidate(input) {
		return contracts.UpstreamRecommendation{}, ErrInvalidCandidate
	}
	if !constraintsPass(input.Constraints) {
		return contracts.UpstreamRecommendation{}, ErrUnsafeCandidate
	}
	fingerprint, err := Fingerprint(input)
	if err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	savings, err := calculateSavings(input.FromCost, input.ToCost)
	if err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	return contracts.UpstreamRecommendation{
		ID: id, UserID: input.UserID, Status: contracts.UpstreamRecommendationOpen,
		IntelligenceFactVersion: input.IntelligenceFactVersion, CostLedgerFactVersion: input.CostLedgerFactVersion,
		LinkFactVersion: input.LinkFactVersion, PlanGeneration: input.PlanGeneration,
		FromSourceID: input.FromSourceID, FromChannelID: input.FromChannelID, FromGroupKey: input.FromGroupKey,
		ToSourceID: input.ToSourceID, ToChannelID: input.ToChannelID, ToGroupKey: input.ToGroupKey, ModelKey: input.ModelKey,
		PriceDimension: input.PriceDimension, SettlementCurrency: input.SettlementCurrency, PerTokens: input.PerTokens,
		AffectedPlanIDs: append([]string(nil), input.AffectedPlanIDs...), AffectedDownstreams: append([]string(nil), input.AffectedDownstreams...),
		EvidenceIDs: append([]string(nil), input.EvidenceIDs...), Constraints: cloneConstraints(input.Constraints),
		FromCost: input.FromCost, ToCost: input.ToCost, Savings: savings,
		FormulaVersion: input.FormulaVersion, StrategyVersion: input.StrategyVersion, Fingerprint: fingerprint,
		CreatedAt: input.CreatedAt.UTC(), ExpiresAt: input.ExpiresAt.UTC(),
	}, nil
}

// Validate verifies the complete immutable recommendation, including its
// fingerprint and derived savings interval. Persistence and execution callers
// use this exported boundary so a tampered object cannot enter durable state.
func Validate(value contracts.UpstreamRecommendation) error {
	if !validRecommendation(value) {
		return ErrInvalidCandidate
	}
	return nil
}

// Fingerprint is domain-separated SHA-256 over canonical, sorted decision
// fields. Display prose, generated ID, timestamps, status, and slice order do
// not influence identity.
func Fingerprint(input contracts.UpstreamRecommendationCandidate) (string, error) {
	input = normalizeCandidate(input)
	if !validCandidate(input) {
		return "", ErrInvalidCandidate
	}
	parts := []string{
		fingerprintDomain, strconv.FormatInt(input.UserID, 10), strconv.FormatInt(input.IntelligenceFactVersion, 10),
		strconv.FormatInt(input.CostLedgerFactVersion, 10), strconv.FormatInt(input.LinkFactVersion, 10), strconv.FormatInt(input.PlanGeneration, 10),
		input.FromSourceID, input.FromChannelID, input.FromGroupKey, input.ToSourceID, input.ToChannelID, input.ToGroupKey,
		input.ModelKey, string(input.PriceDimension), input.SettlementCurrency, strconv.FormatInt(input.PerTokens, 10),
		strings.Join(input.AffectedPlanIDs, "\x1f"), strings.Join(input.AffectedDownstreams, "\x1f"),
		strings.Join(input.EvidenceIDs, "\x1f"), input.FormulaVersion, input.StrategyVersion,
		string(input.FromCost.Lower), string(input.FromCost.Expected), string(input.FromCost.Upper),
		string(input.ToCost.Lower), string(input.ToCost.Expected), string(input.ToCost.Upper),
	}
	for _, constraint := range input.Constraints {
		parts = append(parts, string(constraint.Kind), string(constraint.Status), constraint.ReasonCode, strings.Join(constraint.EvidenceIDs, "\x1f"))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:]), nil
}

// ValidateCurrent compares the exact facts used at creation with a fresh,
// owner-scoped resolution. now >= expires_at is expired.
func ValidateCurrent(recommendation contracts.UpstreamRecommendation, current contracts.UpstreamRecommendationCurrentFacts) contracts.UpstreamRecommendationValidity {
	reasons := make([]contracts.UpstreamRecommendationStaleReason, 0)
	add := func(reason contracts.UpstreamRecommendationStaleReason) {
		for _, existing := range reasons {
			if existing == reason {
				return
			}
		}
		reasons = append(reasons, reason)
	}
	if !validRecommendation(recommendation) || !validCurrent(current) {
		add(contracts.UpstreamRecommendationStaleInvalidCurrentFacts)
	}
	if current.Now.IsZero() || recommendation.ExpiresAt.IsZero() || !current.Now.Before(recommendation.ExpiresAt) {
		add(contracts.UpstreamRecommendationStaleExpired)
	}
	if current.UserID != recommendation.UserID {
		add(contracts.UpstreamRecommendationStaleOwner)
	}
	if current.IntelligenceFactVersion != recommendation.IntelligenceFactVersion {
		add(contracts.UpstreamRecommendationStaleIntelligenceVersion)
	}
	if current.CostLedgerFactVersion != recommendation.CostLedgerFactVersion {
		add(contracts.UpstreamRecommendationStaleCostVersion)
	}
	if current.LinkFactVersion != recommendation.LinkFactVersion {
		add(contracts.UpstreamRecommendationStaleLinkVersion)
	}
	if current.PlanGeneration != recommendation.PlanGeneration {
		add(contracts.UpstreamRecommendationStalePlanGeneration)
	}
	if current.FromSourceID != recommendation.FromSourceID || current.FromChannelID != recommendation.FromChannelID || current.FromGroupKey != recommendation.FromGroupKey ||
		current.ToSourceID != recommendation.ToSourceID || current.ToChannelID != recommendation.ToChannelID || current.ToGroupKey != recommendation.ToGroupKey {
		add(contracts.UpstreamRecommendationStaleMapping)
	}
	if current.ModelKey != recommendation.ModelKey || current.PriceDimension != recommendation.PriceDimension ||
		current.SettlementCurrency != recommendation.SettlementCurrency || current.PerTokens != recommendation.PerTokens {
		add(contracts.UpstreamRecommendationStaleDimension)
	}
	if !sameUniqueIDs(current.EvidenceIDs, recommendation.EvidenceIDs) {
		add(contracts.UpstreamRecommendationStaleEvidence)
	}
	if !sameUniqueIDs(current.AffectedPlanIDs, recommendation.AffectedPlanIDs) || !sameUniqueIDs(current.AffectedDownstreams, recommendation.AffectedDownstreams) {
		add(contracts.UpstreamRecommendationStaleMapping)
	}
	if current.FormulaVersion != recommendation.FormulaVersion {
		add(contracts.UpstreamRecommendationStaleFormula)
	}
	if current.StrategyVersion != recommendation.StrategyVersion {
		add(contracts.UpstreamRecommendationStaleStrategy)
	}
	sort.Slice(reasons, func(i, j int) bool { return reasons[i] < reasons[j] })
	return contracts.UpstreamRecommendationValidity{Current: len(reasons) == 0, Reasons: reasons}
}

// Transition enforces open -> shadowing -> ready_for_dry_run -> dry_running ->
// dry_run_passed/blocked, plus terminal dismissed/expired. It never mutates its
// input and checks expiry before every non-expire transition.
func Transition(current contracts.UpstreamRecommendation, event contracts.UpstreamRecommendationEvent) (contracts.UpstreamRecommendation, error) {
	next := cloneRecommendation(current)
	if !validRecommendation(current) || event.UserID != current.UserID || event.Now.IsZero() {
		return contracts.UpstreamRecommendation{}, ErrInvalidTransition
	}
	if event.Type != contracts.UpstreamRecommendationEventExpire && !event.Now.Before(current.ExpiresAt) {
		return contracts.UpstreamRecommendation{}, ErrInvalidTransition
	}
	switch event.Type {
	case contracts.UpstreamRecommendationEventStartShadow:
		if current.Status != contracts.UpstreamRecommendationOpen {
			return contracts.UpstreamRecommendation{}, ErrInvalidTransition
		}
		next.Status = contracts.UpstreamRecommendationShadowing
	case contracts.UpstreamRecommendationEventShadowPassed:
		if current.Status != contracts.UpstreamRecommendationShadowing {
			return contracts.UpstreamRecommendation{}, ErrInvalidTransition
		}
		next.Status = contracts.UpstreamRecommendationReadyForDryRun
	case contracts.UpstreamRecommendationEventShadowBlocked:
		if current.Status != contracts.UpstreamRecommendationShadowing {
			return contracts.UpstreamRecommendation{}, ErrInvalidTransition
		}
		next.Status = contracts.UpstreamRecommendationOpen
	case contracts.UpstreamRecommendationEventStartDryRun:
		if current.Status != contracts.UpstreamRecommendationReadyForDryRun || strings.TrimSpace(event.DryRunID) == "" {
			return contracts.UpstreamRecommendation{}, ErrInvalidTransition
		}
		next.Status, next.DryRunID = contracts.UpstreamRecommendationDryRunning, strings.TrimSpace(event.DryRunID)
	case contracts.UpstreamRecommendationEventDryRunPassed:
		if current.Status != contracts.UpstreamRecommendationDryRunning || current.DryRunID == "" || event.DryRunID != current.DryRunID {
			return contracts.UpstreamRecommendation{}, ErrInvalidTransition
		}
		next.Status = contracts.UpstreamRecommendationDryRunPassed
	case contracts.UpstreamRecommendationEventDryRunBlocked:
		if current.Status != contracts.UpstreamRecommendationDryRunning || current.DryRunID == "" || event.DryRunID != current.DryRunID {
			return contracts.UpstreamRecommendation{}, ErrInvalidTransition
		}
		next.Status = contracts.UpstreamRecommendationDryRunBlocked
	case contracts.UpstreamRecommendationEventDismiss:
		if terminal(current.Status) {
			return contracts.UpstreamRecommendation{}, ErrInvalidTransition
		}
		next.Status = contracts.UpstreamRecommendationDismissed
	case contracts.UpstreamRecommendationEventExpire:
		if terminal(current.Status) || event.Now.Before(current.ExpiresAt) {
			return contracts.UpstreamRecommendation{}, ErrInvalidTransition
		}
		next.Status = contracts.UpstreamRecommendationExpired
	default:
		return contracts.UpstreamRecommendation{}, ErrInvalidTransition
	}
	return next, nil
}

func validCandidate(input contracts.UpstreamRecommendationCandidate) bool {
	if input.UserID <= 0 || input.IntelligenceFactVersion <= 0 || input.CostLedgerFactVersion <= 0 || input.LinkFactVersion <= 0 || input.PlanGeneration <= 0 ||
		!nonempty(input.FromSourceID, input.FromChannelID, input.FromGroupKey, input.ToSourceID, input.ToChannelID, input.ToGroupKey, input.ModelKey,
			input.SettlementCurrency, input.FormulaVersion, input.StrategyVersion) || input.FromSourceID == input.ToSourceID || input.FromChannelID == input.ToChannelID ||
		!validPriceDimension(input.PriceDimension) || input.PerTokens <= 0 || !validCurrency(input.SettlementCurrency) ||
		len(input.AffectedPlanIDs) == 0 || len(input.AffectedDownstreams) == 0 || len(input.EvidenceIDs) == 0 ||
		!uniqueNonempty(input.AffectedPlanIDs) || !uniqueNonempty(input.AffectedDownstreams) || !uniqueNonempty(input.EvidenceIDs) ||
		input.CreatedAt.IsZero() || input.ExpiresAt.IsZero() || !input.ExpiresAt.After(input.CreatedAt) || !validCostRange(input.FromCost) || !validCostRange(input.ToCost) {
		return false
	}
	_, err := calculateSavings(input.FromCost, input.ToCost)
	return err == nil && validConstraints(input.Constraints)
}

func validRecommendation(value contracts.UpstreamRecommendation) bool {
	if strings.TrimSpace(value.ID) == "" || !contracts.IsUpstreamRecommendationStatus(value.Status) || len(value.Fingerprint) != 64 {
		return false
	}
	input := contracts.UpstreamRecommendationCandidate{
		UserID: value.UserID, IntelligenceFactVersion: value.IntelligenceFactVersion, CostLedgerFactVersion: value.CostLedgerFactVersion,
		LinkFactVersion: value.LinkFactVersion, PlanGeneration: value.PlanGeneration,
		FromSourceID: value.FromSourceID, FromChannelID: value.FromChannelID, FromGroupKey: value.FromGroupKey,
		ToSourceID: value.ToSourceID, ToChannelID: value.ToChannelID, ToGroupKey: value.ToGroupKey, ModelKey: value.ModelKey,
		PriceDimension: value.PriceDimension, SettlementCurrency: value.SettlementCurrency, PerTokens: value.PerTokens,
		AffectedPlanIDs: value.AffectedPlanIDs, AffectedDownstreams: value.AffectedDownstreams, EvidenceIDs: value.EvidenceIDs,
		Constraints: value.Constraints, FromCost: value.FromCost, ToCost: value.ToCost,
		FormulaVersion: value.FormulaVersion, StrategyVersion: value.StrategyVersion,
		CreatedAt: value.CreatedAt, ExpiresAt: value.ExpiresAt,
	}
	input = normalizeCandidate(input)
	if !validCandidate(input) {
		return false
	}
	fingerprint, err := Fingerprint(input)
	if err != nil || fingerprint != value.Fingerprint {
		return false
	}
	savings, err := calculateSavings(input.FromCost, input.ToCost)
	return err == nil && savings == value.Savings
}

func validCurrent(value contracts.UpstreamRecommendationCurrentFacts) bool {
	return value.UserID > 0 && value.IntelligenceFactVersion > 0 && value.CostLedgerFactVersion > 0 && value.LinkFactVersion > 0 && value.PlanGeneration > 0 &&
		nonempty(value.FromSourceID, value.FromChannelID, value.FromGroupKey, value.ToSourceID, value.ToChannelID, value.ToGroupKey,
			value.ModelKey, value.SettlementCurrency, value.FormulaVersion, value.StrategyVersion) && validPriceDimension(value.PriceDimension) &&
		value.PerTokens > 0 && validCurrency(value.SettlementCurrency) && uniqueNonempty(value.AffectedPlanIDs) &&
		uniqueNonempty(value.AffectedDownstreams) && uniqueNonempty(value.EvidenceIDs) && !value.Now.IsZero()
}

func validConstraints(values []contracts.UpstreamRecommendationConstraint) bool {
	if len(values) != len(contracts.UpstreamRecommendationRequiredConstraints()) {
		return false
	}
	seen := make(map[contracts.UpstreamRecommendationConstraintKind]bool, len(values))
	for _, value := range values {
		if !contracts.IsUpstreamRecommendationConstraintKind(value.Kind) || !contracts.IsUpstreamRecommendationConstraintStatus(value.Status) || seen[value.Kind] ||
			len(value.EvidenceIDs) == 0 || !uniqueNonempty(value.EvidenceIDs) || value.Status != contracts.UpstreamRecommendationConstraintPassed && strings.TrimSpace(value.ReasonCode) == "" {
			return false
		}
		seen[value.Kind] = true
	}
	for _, required := range contracts.UpstreamRecommendationRequiredConstraints() {
		if !seen[required] {
			return false
		}
	}
	return true
}

func constraintsPass(values []contracts.UpstreamRecommendationConstraint) bool {
	if !validConstraints(values) {
		return false
	}
	for _, value := range values {
		if value.Status != contracts.UpstreamRecommendationConstraintPassed {
			return false
		}
	}
	return true
}

func validCostRange(value contracts.UpstreamRecommendationCostRange) bool {
	_, ok := orderedNonnegative(value.Lower, value.Expected, value.Upper)
	return ok
}

// Savings is derived conservatively from two uncertain cost intervals:
// lower=from.lower-to.upper, upper=from.upper-to.lower. The lower bound must be
// strictly positive; otherwise the opportunity is not proven and fails closed.
func calculateSavings(from, to contracts.UpstreamRecommendationCostRange) (contracts.UpstreamRecommendationSavingsRange, error) {
	fromLower, ok := orderedNonnegative(from.Lower, from.Expected, from.Upper)
	if !ok {
		return contracts.UpstreamRecommendationSavingsRange{}, ErrInvalidCandidate
	}
	fromExpected, _ := from.Expected.Rat()
	fromUpper, _ := from.Upper.Rat()
	toLower, ok := orderedNonnegative(to.Lower, to.Expected, to.Upper)
	if !ok {
		return contracts.UpstreamRecommendationSavingsRange{}, ErrInvalidCandidate
	}
	toExpected, _ := to.Expected.Rat()
	toUpper, _ := to.Upper.Rat()
	amountLower := new(big.Rat).Sub(fromLower, toUpper)
	amountExpected := new(big.Rat).Sub(fromExpected, toExpected)
	amountUpper := new(big.Rat).Sub(fromUpper, toLower)
	if amountLower.Sign() <= 0 || amountLower.Cmp(amountExpected) > 0 || amountExpected.Cmp(amountUpper) > 0 || fromLower.Sign() <= 0 || fromExpected.Sign() <= 0 || fromUpper.Sign() <= 0 {
		return contracts.UpstreamRecommendationSavingsRange{}, ErrUnsafeCandidate
	}
	// Conservative percentage bounds use the largest baseline denominator for
	// the lower bound and the smallest denominator for the upper bound.
	percentLower := new(big.Rat).Quo(amountLower, fromUpper)
	percentExpected := new(big.Rat).Quo(amountExpected, fromExpected)
	percentUpper := new(big.Rat).Quo(amountUpper, fromLower)
	values := []*big.Rat{amountLower, amountExpected, amountUpper, percentLower, percentExpected, percentUpper}
	decimals := make([]contracts.CanonicalDecimal, len(values))
	for index, value := range values {
		decimal, err := contracts.QuantizeCanonicalDecimal(value, contracts.UpstreamDecimalMaxScale)
		if err != nil {
			return contracts.UpstreamRecommendationSavingsRange{}, ErrInvalidCandidate
		}
		decimals[index] = decimal
	}
	return contracts.UpstreamRecommendationSavingsRange{
		AmountLower: decimals[0], AmountExpected: decimals[1], AmountUpper: decimals[2],
		PercentLower: decimals[3], PercentExpected: decimals[4], PercentUpper: decimals[5],
	}, nil
}

func orderedNonnegative(lower, expected, upper contracts.CanonicalDecimal) (*big.Rat, bool) {
	l, errL := lower.Rat()
	e, errE := expected.Rat()
	u, errU := upper.Rat()
	if errL != nil || errE != nil || errU != nil || l.Sign() < 0 || e.Sign() < 0 || u.Sign() < 0 || l.Cmp(e) > 0 || e.Cmp(u) > 0 {
		return nil, false
	}
	return l, true
}

func normalizeCandidate(input contracts.UpstreamRecommendationCandidate) contracts.UpstreamRecommendationCandidate {
	input.AffectedPlanIDs = normalizeIDs(input.AffectedPlanIDs)
	input.AffectedDownstreams = normalizeIDs(input.AffectedDownstreams)
	input.EvidenceIDs = normalizeIDs(input.EvidenceIDs)
	input.Constraints = cloneConstraints(input.Constraints)
	for index := range input.Constraints {
		input.Constraints[index].EvidenceIDs = normalizeIDs(input.Constraints[index].EvidenceIDs)
	}
	sort.Slice(input.Constraints, func(i, j int) bool { return input.Constraints[i].Kind < input.Constraints[j].Kind })
	return input
}

func normalizeIDs(values []string) []string {
	result := append([]string(nil), values...)
	for index := range result {
		result[index] = strings.TrimSpace(result[index])
	}
	sort.Strings(result)
	return result
}

func uniqueNonempty(values []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func sameUniqueIDs(left, right []string) bool {
	if !uniqueNonempty(left) || !uniqueNonempty(right) || len(left) != len(right) {
		return false
	}
	l, r := normalizeIDs(left), normalizeIDs(right)
	for index := range l {
		if l[index] != r[index] {
			return false
		}
	}
	return true
}

func validPriceDimension(value contracts.UpstreamPriceDimension) bool {
	switch value {
	case contracts.UpstreamPriceInput, contracts.UpstreamPriceOutput, contracts.UpstreamPriceCachedInput, contracts.UpstreamPriceRequest:
		return true
	default:
		return false
	}
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

func nonempty(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func terminal(status contracts.UpstreamRecommendationStatus) bool {
	return status == contracts.UpstreamRecommendationDryRunPassed || status == contracts.UpstreamRecommendationDryRunBlocked ||
		status == contracts.UpstreamRecommendationDismissed || status == contracts.UpstreamRecommendationExpired
}

func cloneConstraints(values []contracts.UpstreamRecommendationConstraint) []contracts.UpstreamRecommendationConstraint {
	result := append([]contracts.UpstreamRecommendationConstraint(nil), values...)
	for index := range result {
		result[index].EvidenceIDs = append([]string(nil), result[index].EvidenceIDs...)
	}
	return result
}

func cloneRecommendation(value contracts.UpstreamRecommendation) contracts.UpstreamRecommendation {
	value.AffectedPlanIDs = append([]string(nil), value.AffectedPlanIDs...)
	value.AffectedDownstreams = append([]string(nil), value.AffectedDownstreams...)
	value.EvidenceIDs = append([]string(nil), value.EvidenceIDs...)
	value.Constraints = cloneConstraints(value.Constraints)
	return value
}
