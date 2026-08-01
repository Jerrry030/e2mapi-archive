package recommendationexecution

import (
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestEvaluateAllowsExactOptedInScope(t *testing.T) {
	policy, input := validFixture()
	got := Evaluate(policy, input)
	if !got.Allowed || len(got.Reasons) != 0 {
		t.Fatalf("valid opt-in blocked: %+v", got)
	}
}

func TestEvaluateZeroValueFailsClosed(t *testing.T) {
	got := Evaluate(contracts.RecommendationExecutionPolicy{}, contracts.RecommendationExecutionContext{})
	if got.Allowed {
		t.Fatal("zero value authorized execution")
	}
	for _, reason := range []contracts.RecommendationExecutionBlockReason{
		contracts.RecommendationExecutionBlockedDisabled,
		contracts.RecommendationExecutionBlockedInvalidPolicy,
		contracts.RecommendationExecutionBlockedInvalidContext,
		contracts.RecommendationExecutionBlockedOwnerMismatch,
		contracts.RecommendationExecutionBlockedScopeMismatch,
		contracts.RecommendationExecutionBlockedDailyCap,
		contracts.RecommendationExecutionBlockedSavingsUnavailable,
	} {
		assertReason(t, got, reason)
	}
}

func TestEvaluateIndependentAuthorizationGates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*contracts.RecommendationExecutionPolicy, *contracts.RecommendationExecutionContext)
		reason contracts.RecommendationExecutionBlockReason
	}{
		{"disabled", func(p *contracts.RecommendationExecutionPolicy, _ *contracts.RecommendationExecutionContext) {
			p.Enabled = false
		}, contracts.RecommendationExecutionBlockedDisabled},
		{"kill switch", func(p *contracts.RecommendationExecutionPolicy, _ *contracts.RecommendationExecutionContext) {
			p.KillSwitch = true
		}, contracts.RecommendationExecutionBlockedKillSwitch},
		{"owner", func(_ *contracts.RecommendationExecutionPolicy, i *contracts.RecommendationExecutionContext) {
			i.UserID++
		}, contracts.RecommendationExecutionBlockedOwnerMismatch},
		{"scope", func(_ *contracts.RecommendationExecutionPolicy, i *contracts.RecommendationExecutionContext) {
			i.PlanID = "other"
		}, contracts.RecommendationExecutionBlockedScopeMismatch},
		{"daily cap", func(p *contracts.RecommendationExecutionPolicy, i *contracts.RecommendationExecutionContext) {
			i.DailyExecutionCount = p.DailyExecutionCap
		}, contracts.RecommendationExecutionBlockedDailyCap},
		{"cooldown", func(_ *contracts.RecommendationExecutionPolicy, i *contracts.RecommendationExecutionContext) {
			recent := i.Now.Add(-time.Minute)
			i.LastExecutedAt = &recent
		}, contracts.RecommendationExecutionBlockedCooldown},
		{"minimum", func(_ *contracts.RecommendationExecutionPolicy, i *contracts.RecommendationExecutionContext) {
			i.ExpectedSavings = "0.099999999999999999"
		}, contracts.RecommendationExecutionBlockedSavingsBelowMinimum},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, input := validFixture()
			test.mutate(&policy, &input)
			got := Evaluate(policy, input)
			if got.Allowed {
				t.Fatalf("gate %s authorized execution", test.name)
			}
			assertReason(t, got, test.reason)
		})
	}
}

func TestEvaluateMinimumSavingsBoundaryUsesExactDecimal(t *testing.T) {
	policy, input := validFixture()
	input.ExpectedSavings = policy.MinimumSavings
	if got := Evaluate(policy, input); !got.Allowed {
		t.Fatalf("equal exact decimal should pass: %+v", got)
	}
	input.ExpectedSavings = contracts.CanonicalDecimal("0.1")
	policy.MinimumSavings = contracts.CanonicalDecimal("0.100000000000000001")
	if got := Evaluate(policy, input); got.Allowed {
		t.Fatalf("sub-ulp difference was rounded through float: %+v", got)
	}
}

func TestEvaluateCooldownBoundaryAndFutureTimestamp(t *testing.T) {
	policy, input := validFixture()
	last := input.Now.Add(-time.Duration(policy.CooldownSeconds) * time.Second)
	input.LastExecutedAt = &last
	if got := Evaluate(policy, input); !got.Allowed {
		t.Fatalf("cooldown boundary should pass: %+v", got)
	}
	future := input.Now.Add(time.Second)
	input.LastExecutedAt = &future
	got := Evaluate(policy, input)
	if got.Allowed {
		t.Fatalf("future timestamp authorized execution: %+v", got)
	}
	assertReason(t, got, contracts.RecommendationExecutionBlockedInvalidContext)
}

func TestEvaluatePoolScopeOptInDoesNotAuthorizeOtherPool(t *testing.T) {
	policy, input := validFixture()
	policy.Scope, policy.PlanID, policy.PoolID = contracts.RecommendationExecutionScopePool, "", input.PoolID
	if got := Evaluate(policy, input); !got.Allowed {
		t.Fatalf("matching pool blocked: %+v", got)
	}
	input.PoolID = "pool-other"
	if got := Evaluate(policy, input); got.Allowed {
		t.Fatal("pool policy authorized another pool")
	}
}

func TestEvaluateRejectsMalformedScopeShapeAndMalformedDecimals(t *testing.T) {
	policy, input := validFixture()
	policy.PoolID = "also-set"
	assertReason(t, Evaluate(policy, input), contracts.RecommendationExecutionBlockedInvalidPolicy)

	policy, input = validFixture()
	input.ExpectedSavings = "0.20"
	assertReason(t, Evaluate(policy, input), contracts.RecommendationExecutionBlockedSavingsUnavailable)
}

func validFixture() (contracts.RecommendationExecutionPolicy, contracts.RecommendationExecutionContext) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	return contracts.RecommendationExecutionPolicy{
			ID: "policy-1", UserID: 42, Scope: contracts.RecommendationExecutionScopePlan,
			PlanID: "plan-1", Enabled: true, DailyExecutionCap: 3,
			CooldownSeconds: 300, MinimumSavings: "0.1",
		}, contracts.RecommendationExecutionContext{
			UserID: 42, PlanID: "plan-1", PoolID: "pool-1", ExpectedSavings: "0.2", Now: now,
		}
}

func assertReason(t *testing.T, got contracts.RecommendationExecutionAuthorization, want contracts.RecommendationExecutionBlockReason) {
	t.Helper()
	for _, reason := range got.Reasons {
		if reason == want {
			return
		}
	}
	t.Fatalf("missing reason %q in %+v", want, got)
}
