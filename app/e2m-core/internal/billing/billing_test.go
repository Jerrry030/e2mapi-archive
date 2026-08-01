package billing

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

func TestStatementCountsInstancesAndDispositions(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))

	user, err := st.CreateUser(ctx, contracts.User{
		Email:       "owner-a@example.com",
		DisplayName: "Owner A",
		Roles:       []contracts.UserRole{contracts.UserRoleOwner},
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "i1", Kind: contracts.InstanceKindSub2API}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "i2", Kind: contracts.InstanceKindSub2API}); err != nil {
		t.Fatal(err)
	}

	period := time.Now().UTC().Format("2006-01")
	inPeriod := time.Now().UTC()

	// two accepted dispositions + one failed + one out-of-scope target
	mk := func(action, result, target string, at time.Time) {
		if _, err := st.AppendAudit(ctx, contracts.OperationAudit{
			UserID: user.ID, Action: action, Result: result,
			TargetType: target, RiskLevel: contracts.RiskLevelL1, CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("account.disable_schedulable", "accepted", "account", inPeriod)
	mk("account.enable_schedulable", "accepted", "account", inPeriod)
	mk("account.disable_schedulable", "failed", "account", inPeriod)                     // not billed
	mk("notification_route.create", "accepted", "notification_route", inPeriod)          // not billed
	mk("account.disable_schedulable", "accepted", "account", inPeriod.AddDate(0, -2, 0)) // out of period

	calc := New(st, Pricing{InstanceMonthlyCents: 19900, DispositionCents: 100, Currency: "CNY"})
	s, err := calc.Statement(ctx, user.ID, period)
	if err != nil {
		t.Fatal(err)
	}

	if s.InstanceCount != 2 {
		t.Fatalf("instances: want 2, got %d", s.InstanceCount)
	}
	if s.DispositionCount != 2 {
		t.Fatalf("dispositions: want 2, got %d", s.DispositionCount)
	}
	if s.Total != "400.00" { // 2*199.00 + 2*1.00
		t.Fatalf("total: want 400.00, got %s", s.Total)
	}
	if s.UserEmail != "owner-a@example.com" || s.Currency != "CNY" || len(s.Lines) != 2 {
		t.Fatalf("statement fields wrong: %+v", s)
	}
}

func TestParsePeriodRejectsGarbage(t *testing.T) {
	if _, _, err := ParsePeriod("2026-13"); err == nil {
		t.Fatal("expected error for month 13")
	}
	if _, _, err := ParsePeriod("junk"); err == nil {
		t.Fatal("expected error for junk")
	}
	start, end, err := ParsePeriod("2026-07")
	if err != nil {
		t.Fatal(err)
	}
	if start.Month() != time.July || end.Month() != time.August {
		t.Fatalf("bounds wrong: %v %v", start, end)
	}
}

func TestCentsRendering(t *testing.T) {
	for v, want := range map[int64]string{0: "0.00", 5: "0.05", 19900: "199.00", 100: "1.00"} {
		if got := cents(v); got != want {
			t.Fatalf("cents(%d): want %s got %s", v, want, got)
		}
	}
}
