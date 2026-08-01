// Package recommendationexecution evaluates the independent, explicitly
// opted-in authorization boundary for intelligence-driven optimization.
// It has no store, HTTP, autoswitch, or gateway dependency.
package recommendationexecution

import (
	"math/big"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
)

const maxGuardSeconds = 365 * 24 * 60 * 60

// Evaluate is fail-closed. Every malformed or missing input produces a blocked
// result; authorization requires all gates to pass at the same evaluation time.
func Evaluate(policy contracts.RecommendationExecutionPolicy, input contracts.RecommendationExecutionContext) contracts.RecommendationExecutionAuthorization {
	reasons := make([]contracts.RecommendationExecutionBlockReason, 0, 4)
	add := func(reason contracts.RecommendationExecutionBlockReason) {
		for _, existing := range reasons {
			if existing == reason {
				return
			}
		}
		reasons = append(reasons, reason)
	}

	minimum, policyValid := validatePolicy(policy)
	if !policyValid {
		add(contracts.RecommendationExecutionBlockedInvalidPolicy)
	}
	if !policy.Enabled {
		add(contracts.RecommendationExecutionBlockedDisabled)
	}
	if policy.KillSwitch {
		add(contracts.RecommendationExecutionBlockedKillSwitch)
	}
	if !validContext(input) {
		add(contracts.RecommendationExecutionBlockedInvalidContext)
	}
	if policy.UserID <= 0 || input.UserID <= 0 || policy.UserID != input.UserID {
		add(contracts.RecommendationExecutionBlockedOwnerMismatch)
	}
	if !scopeMatches(policy, input) {
		add(contracts.RecommendationExecutionBlockedScopeMismatch)
	}
	if policy.DailyExecutionCap <= 0 || input.DailyExecutionCount < 0 || input.DailyExecutionCount >= policy.DailyExecutionCap {
		add(contracts.RecommendationExecutionBlockedDailyCap)
	}
	if cooldownActive(policy, input) {
		add(contracts.RecommendationExecutionBlockedCooldown)
	}

	savings, err := input.ExpectedSavings.Rat()
	if err != nil || savings.Sign() < 0 {
		add(contracts.RecommendationExecutionBlockedSavingsUnavailable)
	} else if !policyValid || minimum == nil || savings.Cmp(minimum) < 0 {
		add(contracts.RecommendationExecutionBlockedSavingsBelowMinimum)
	}

	sort.Slice(reasons, func(i, j int) bool { return reasons[i] < reasons[j] })
	return contracts.RecommendationExecutionAuthorization{Allowed: len(reasons) == 0, Reasons: reasons}
}

func validatePolicy(policy contracts.RecommendationExecutionPolicy) (*big.Rat, bool) {
	minimum, err := policy.MinimumSavings.Rat()
	if err != nil || minimum.Sign() < 0 || policy.UserID <= 0 || strings.TrimSpace(policy.ID) == "" ||
		!contracts.IsRecommendationExecutionScope(policy.Scope) || policy.DailyExecutionCap <= 0 ||
		policy.CooldownSeconds < 0 || policy.CooldownSeconds > maxGuardSeconds {
		return nil, false
	}
	switch policy.Scope {
	case contracts.RecommendationExecutionScopePlan:
		if strings.TrimSpace(policy.PlanID) == "" || strings.TrimSpace(policy.PoolID) != "" {
			return nil, false
		}
	case contracts.RecommendationExecutionScopePool:
		if strings.TrimSpace(policy.PoolID) == "" || strings.TrimSpace(policy.PlanID) != "" {
			return nil, false
		}
	default:
		return nil, false
	}
	return minimum, true
}

func validContext(input contracts.RecommendationExecutionContext) bool {
	if input.UserID <= 0 || strings.TrimSpace(input.PlanID) == "" || strings.TrimSpace(input.PoolID) == "" ||
		input.Now.IsZero() || input.DailyExecutionCount < 0 {
		return false
	}
	if input.LastExecutedAt != nil && (input.LastExecutedAt.IsZero() || input.LastExecutedAt.After(input.Now)) {
		return false
	}
	return true
}

func scopeMatches(policy contracts.RecommendationExecutionPolicy, input contracts.RecommendationExecutionContext) bool {
	switch policy.Scope {
	case contracts.RecommendationExecutionScopePlan:
		return strings.TrimSpace(policy.PlanID) != "" && policy.PlanID == input.PlanID
	case contracts.RecommendationExecutionScopePool:
		return strings.TrimSpace(policy.PoolID) != "" && policy.PoolID == input.PoolID
	default:
		return false
	}
}

func cooldownActive(policy contracts.RecommendationExecutionPolicy, input contracts.RecommendationExecutionContext) bool {
	if policy.CooldownSeconds < 0 || input.LastExecutedAt == nil || input.Now.IsZero() || input.LastExecutedAt.IsZero() {
		return false
	}
	next := input.LastExecutedAt.Add(time.Duration(policy.CooldownSeconds) * time.Second)
	return input.Now.Before(next)
}
