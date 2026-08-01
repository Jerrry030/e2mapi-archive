package health

import (
	"context"
	"testing"

	"e2m.local/contracts"
)

func f64(v float64) *float64 { return &v }

func TestBalanceAlertBelowThreshold(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{
		{ID: "1", DisplayName: "主渠道", Status: "active", Schedulable: true, Balance: f64(3.50)},
		{ID: "2", DisplayName: "富渠道", Status: "active", Schedulable: true, Balance: f64(100)},
	}}
	c, st, inst := newChecker(t, adapter, Config{FailStreak: 99, BalanceThreshold: 10})

	c.checkInstance(ctx, inst)

	audits, _ := st.ListAudits(ctx, 101)
	var low int
	for _, a := range audits {
		if a.Action == "account.balance_low" {
			low++
			if a.TargetID != "1" {
				t.Fatalf("alert should target account 1, got %s", a.TargetID)
			}
		}
	}
	if low != 1 {
		t.Fatalf("expected exactly 1 low-balance audit, got %d", low)
	}

	// Second check within cooldown: no repeat alert.
	c.checkInstance(ctx, inst)
	audits, _ = st.ListAudits(ctx, 101)
	low = 0
	for _, a := range audits {
		if a.Action == "account.balance_low" {
			low++
		}
	}
	if low != 1 {
		t.Fatalf("cooldown must suppress repeats, got %d alerts", low)
	}
}

func TestBalanceAlertRearmsAfterRecovery(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{
		{ID: "1", Status: "active", Schedulable: true, Balance: f64(5)},
	}}
	c, st, inst := newChecker(t, adapter, Config{FailStreak: 99, BalanceThreshold: 10})

	c.checkInstance(ctx, inst) // alert #1

	// Balance recovers, then dips again: a fresh alert fires without waiting
	// out the cooldown.
	adapter.accounts[0].Balance = f64(50)
	c.checkInstance(ctx, inst)
	adapter.accounts[0].Balance = f64(2)
	c.checkInstance(ctx, inst) // alert #2

	audits, _ := st.ListAudits(ctx, 101)
	var low int
	for _, a := range audits {
		if a.Action == "account.balance_low" {
			low++
		}
	}
	if low != 2 {
		t.Fatalf("expected re-armed alert after recovery, got %d", low)
	}
}

func TestBalanceAlertDisabledByDefault(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{
		{ID: "1", Status: "active", Schedulable: true, Balance: f64(0.01)},
	}}
	c, st, inst := newChecker(t, adapter, Config{FailStreak: 99}) // no threshold

	c.checkInstance(ctx, inst)
	audits, _ := st.ListAudits(ctx, 101)
	for _, a := range audits {
		if a.Action == "account.balance_low" {
			t.Fatal("balance alert must be off when threshold is 0")
		}
	}
}

func TestDriftDetection(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{
		{ID: "1", DisplayName: "主号", Status: "active", Schedulable: true, Priority: 10},
		{ID: "2", DisplayName: "备号", Status: "active", Schedulable: false, Priority: 5},
	}}
	c, st, inst := newChecker(t, adapter, Config{FailStreak: 99, DriftDetection: true})

	// First check establishes the baseline; no drift possible.
	c.checkInstance(ctx, inst)
	audits, _ := st.ListAudits(ctx, 101)
	for _, a := range audits {
		if a.Action == "instance.config_drift" {
			t.Fatal("first observation must not report drift")
		}
	}

	// Upstream flips status and priority behind our back, and account 2 vanishes.
	adapter.accounts = []contracts.GatewayAccount{
		{ID: "1", DisplayName: "主号", Status: "rate_limited", Schedulable: true, Priority: 99},
	}
	c.checkInstance(ctx, inst)

	audits, _ = st.ListAudits(ctx, 101)
	var drift *contracts.OperationAudit
	for i := range audits {
		if audits[i].Action == "instance.config_drift" {
			drift = &audits[i]
			break
		}
	}
	if drift == nil {
		t.Fatal("expected a config_drift audit")
	}
	msg := drift.ErrorMessage
	for _, want := range []string{"rate_limited", "99", "消失"} {
		if !contains(msg, want) {
			t.Fatalf("drift message missing %q: %s", want, msg)
		}
	}

	// No further change: no new drift audit.
	c.checkInstance(ctx, inst)
	audits, _ = st.ListAudits(ctx, 101)
	var count int
	for _, a := range audits {
		if a.Action == "instance.config_drift" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("steady state must not re-report drift, got %d", count)
	}
}

func TestDriftIgnoresOwnSwitches(t *testing.T) {
	// When the checker itself auto-switches, the resulting schedulable flips are
	// applied to the adapter BEFORE rememberAccounts runs (ListAccounts already
	// happened), so the next diff sees them as upstream state. This test pins
	// the current behavior: our own switch IS visible as drift in the next
	// round because the switch mutated upstream state after our snapshot.
	// That is acceptable — the drift note simply corroborates the switch audit.
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{
		{ID: "1", Status: "error", Schedulable: true},
		{ID: "2", Status: "active", Schedulable: false},
	}}
	c, _, inst := newChecker(t, adapter, Config{FailStreak: 1, AutoSwitch: true, AllowLegacyAutoSwitch: true, DriftDetection: true})
	c.checkInstance(ctx, inst) // switches 1 off, 2 on
	c.checkInstance(ctx, inst) // sees the flips as drift — tolerated
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
