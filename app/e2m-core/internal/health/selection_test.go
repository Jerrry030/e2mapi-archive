package health

import (
	"testing"

	"e2m.local/contracts"
)

func acc(id string, healthy, schedulable bool, groups ...string) contracts.GatewayAccount {
	status := ""
	if !healthy {
		status = "error"
	}
	return contracts.GatewayAccount{
		ID:          id,
		DisplayName: id,
		Status:      status,
		Schedulable: schedulable,
		GroupIDs:    groups,
	}
}

func withBalance(a contracts.GatewayAccount, bal float64) contracts.GatewayAccount {
	a.Balance = &bal
	return a
}

// A free (non-scheduled) healthy spare must beat an already-scheduled healthy
// account: we activate a spare rather than double-count a live one.
func TestSelectBackup_PrefersFreeSpare(t *testing.T) {
	accounts := []contracts.GatewayAccount{
		acc("live", true, true, "g1"),
		acc("spare", true, false, "g1"),
	}
	problem := accounts[0]
	pick := selectBackup(accounts, "bad", &problem, StrategyStability, nil)
	if pick == nil {
		t.Fatal("expected a backup, got nil")
	}
	if pick.account.ID != "spare" {
		t.Fatalf("expected free spare to win, got %s", pick.account.ID)
	}
}

// Group affinity dominates: a spare that serves the failing account's group is
// chosen over an equally-free spare that serves a different group.
func TestSelectBackup_GroupAffinity(t *testing.T) {
	problem := acc("bad", false, true, "gpt")
	accounts := []contracts.GatewayAccount{
		acc("wrong-group", true, false, "gemini"),
		acc("right-group", true, false, "gpt"),
	}
	pick := selectBackup(accounts, "bad", &problem, StrategyStability, nil)
	if pick == nil || pick.account.ID != "right-group" {
		t.Fatalf("expected group-matching spare, got %+v", pick)
	}
	if pick.reason() == "" {
		t.Fatal("expected a non-empty rationale")
	}
}

// A candidate with a live fail streak is penalised (flapping guard), so a
// steadier spare wins even if both serve the same group.
func TestSelectBackup_FlappingPenalty(t *testing.T) {
	problem := acc("bad", false, true, "g1")
	accounts := []contracts.GatewayAccount{
		acc("flaky", true, false, "g1"),
		acc("steady", true, false, "g1"),
	}
	signals := map[string]backupSignals{
		"flaky":  {failStreak: 3},
		"steady": {failStreak: 0},
	}
	pick := selectBackup(accounts, "bad", &problem, StrategyStability, signals)
	if pick == nil || pick.account.ID != "steady" {
		t.Fatalf("expected steady spare over flaky, got %+v", pick)
	}
}

// An account reporting exhausted balance must lose to one with headroom.
func TestSelectBackup_BalanceHeadroom(t *testing.T) {
	problem := acc("bad", false, true, "g1")
	accounts := []contracts.GatewayAccount{
		withBalance(acc("drained", true, false, "g1"), 0),
		withBalance(acc("funded", true, false, "g1"), 100),
	}
	pick := selectBackup(accounts, "bad", &problem, StrategyStability, nil)
	if pick == nil || pick.account.ID != "funded" {
		t.Fatalf("expected funded spare, got %+v", pick)
	}
}

// No healthy candidate -> nil (never switch into a broken account).
func TestSelectBackup_NoHealthy(t *testing.T) {
	problem := acc("bad", false, true, "g1")
	accounts := []contracts.GatewayAccount{
		acc("bad", false, true, "g1"),
		acc("also-bad", false, false, "g1"),
	}
	if pick := selectBackup(accounts, "bad", &problem, StrategyStability, nil); pick != nil {
		t.Fatalf("expected nil, got %+v", pick)
	}
}

// Performance strategy rewards higher priority (smaller Priority value).
func TestSelectBackup_PerformancePriority(t *testing.T) {
	problem := acc("bad", false, true, "g1")
	high := acc("high", true, false, "g1")
	high.Priority = 1
	low := acc("low", true, false, "g1")
	low.Priority = 20
	accounts := []contracts.GatewayAccount{low, high}
	pick := selectBackup(accounts, "bad", &problem, StrategyPerformance, nil)
	if pick == nil || pick.account.ID != "high" {
		t.Fatalf("expected high-priority spare, got %+v", pick)
	}
}

func TestNormalizeStrategy(t *testing.T) {
	cases := map[string]SwitchStrategy{
		"":             StrategyStability,
		"stability":    StrategyStability,
		"COST":         StrategyCost,
		" performance": StrategyPerformance,
		"nonsense":     StrategyStability,
	}
	for in, want := range cases {
		if got := normalizeStrategy(in); got != want {
			t.Errorf("normalizeStrategy(%q)=%q want %q", in, got, want)
		}
	}
}
