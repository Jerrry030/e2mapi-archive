package health

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestEmergencyEnableSpareWhenPoolEmpty(t *testing.T) {
	ctx := context.Background()
	// Gateway self-disabled the bad account (new-api style): it is unhealthy AND
	// non-schedulable, so the normal disable path has nothing to do — but the
	// pool now has zero healthy scheduled accounts.
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{
		{ID: "1", DisplayName: "主渠道", Status: "error", Schedulable: false},
		{ID: "2", DisplayName: "备用", Status: "active", Schedulable: false},
	}}
	c, _, inst := newChecker(t, adapter, Config{FailStreak: 2, AutoSwitch: true, AllowLegacyAutoSwitch: true, Cooldown: time.Hour})

	// Debounce: first check observes the empty pool but must not act yet.
	c.checkInstance(ctx, inst)
	if len(adapter.calls) != 0 {
		t.Fatalf("emergency must be debounced, got %v", adapter.calls)
	}
	// Second consecutive empty check crosses FailStreak -> act.
	c.checkInstance(ctx, inst)
	if len(adapter.calls) != 1 {
		t.Fatalf("expected exactly one emergency enable, got %v", adapter.calls)
	}
	if adapter.calls[0].id != "2" || !adapter.calls[0].val {
		t.Fatalf("should enable spare 2, got %+v", adapter.calls[0])
	}

	// Snapshot carries the note.
	snaps := c.Snapshots(inst.ID)
	if len(snaps) != 1 || snaps[0].AutoSwitchNote == "" {
		t.Fatalf("expected emergency note in snapshot, got %+v", snaps)
	}

	// Cooldown: a second check must not enable anything else.
	adapter.accounts[1].Schedulable = false // pretend it got disabled again
	c.checkInstance(ctx, inst)
	if len(adapter.calls) != 1 {
		t.Fatalf("emergency path must be cooldown-limited, calls=%v", adapter.calls)
	}
}

func TestNoEmergencyWhenPoolStillServing(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{
		{ID: "1", Status: "error", Schedulable: false},  // dead, already out
		{ID: "2", Status: "active", Schedulable: true},  // healthy, serving
		{ID: "3", Status: "active", Schedulable: false}, // spare
	}}
	c, _, inst := newChecker(t, adapter, Config{FailStreak: 1, AutoSwitch: true, AllowLegacyAutoSwitch: true})
	c.checkInstance(ctx, inst)
	if len(adapter.calls) != 0 {
		t.Fatalf("pool is serving; no emergency action expected, got %v", adapter.calls)
	}
}

func TestLegacyGlobalGateBlocksEmergencyEnable(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{
		{ID: "1", Status: "error", Schedulable: false},
		{ID: "2", Status: "active", Schedulable: false},
	}}
	c, _, inst := newChecker(t, adapter, Config{FailStreak: 1, AutoSwitch: true})
	policy := contracts.DefaultInstanceMonitorPolicy(inst.ID, inst.UserID)
	policy.AutoSwitch = true
	policy.FailStreak = 1

	snap := c.checkInstance(ctx, inst, policy)
	if len(adapter.calls) != 0 {
		t.Fatalf("disabled legacy writer must not emergency-enable a spare: %v", adapter.calls)
	}
	if snap.TotalAccounts != 2 || snap.AutoSwitchNote != "" {
		t.Fatalf("monitoring should continue without emergency writes: %+v", snap)
	}
}
