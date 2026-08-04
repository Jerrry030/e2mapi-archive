package walletalert

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

func lowBalanceAudits(t *testing.T, st store.Store, userID int64) int {
	t.Helper()
	all, err := st.ListAudits(context.Background(), userID)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	count := 0
	for _, audit := range all {
		if audit.Action == "platform.wallet.balance_low" {
			count++
		}
	}
	return count
}

func TestWalletAlertIsEdgeTriggered(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC))
	user, err := st.CreateUser(ctx, contracts.User{Email: "low@example.com", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, _, err := st.AdjustWalletBalance(ctx, user.ID, "CNY", 2_000_000, "seed-1", "seed"); err != nil {
		t.Fatalf("seed wallet: %v", err)
	}

	runner := New(st, nil, "CNY", 5_000_000, time.Hour)

	// Below threshold: exactly one alert even across repeated sweeps.
	runner.RunOnce(ctx)
	runner.RunOnce(ctx)
	if got := lowBalanceAudits(t, st, user.ID); got != 1 {
		t.Fatalf("expected one low-balance audit, got %d", got)
	}

	// Recovery above the threshold re-arms the alert.
	if _, _, err := st.AdjustWalletBalance(ctx, user.ID, "CNY", 10_000_000, "seed-2", "topup"); err != nil {
		t.Fatalf("top up: %v", err)
	}
	runner.RunOnce(ctx)
	if _, _, err := st.AdjustWalletBalance(ctx, user.ID, "CNY", -9_000_000, "seed-3", "spend"); err != nil {
		t.Fatalf("spend: %v", err)
	}
	runner.RunOnce(ctx)
	if got := lowBalanceAudits(t, st, user.ID); got != 2 {
		t.Fatalf("expected a second alert after recovery and re-drop, got %d", got)
	}
}
