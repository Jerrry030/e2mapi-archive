package health

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/adapters"
	"e2m.local/core/internal/orchestrator"
	"e2m.local/core/internal/store"
)

// scriptedAdapter returns a fixed account set and records SetSchedulable calls.
type scriptedAdapter struct {
	accounts []contracts.GatewayAccount
	calls    []struct {
		id  string
		val bool
	}
}

func (s *scriptedAdapter) Kind() contracts.InstanceKind                { return contracts.InstanceKindSub2API }
func (s *scriptedAdapter) Capabilities() []contracts.AdapterCapability { return nil }
func (s *scriptedAdapter) ListAccounts(context.Context, contracts.Instance) ([]contracts.GatewayAccount, error) {
	return s.accounts, nil
}
func (s *scriptedAdapter) SetSchedulable(_ context.Context, _ contracts.Instance, id string, val bool) error {
	s.calls = append(s.calls, struct {
		id  string
		val bool
	}{id, val})
	// reflect the change so subsequent reads see it
	for i := range s.accounts {
		if s.accounts[i].ID == id {
			s.accounts[i].Schedulable = val
		}
	}
	return nil
}
func (s *scriptedAdapter) ProvisionAccount(_ context.Context, _ contracts.Instance, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	id := spec.RemoteID
	if id == "" {
		id = "prov-" + spec.ChannelID
	}
	return contracts.GatewayProvisionResult{RemoteID: id, Created: true}, nil
}
func (s *scriptedAdapter) UpdateAccount(_ context.Context, _ contracts.Instance, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	return contracts.GatewayProvisionResult{RemoteID: spec.RemoteID}, nil
}
func (s *scriptedAdapter) DeleteAccount(_ context.Context, _ contracts.Instance, _ string) error {
	return nil
}

func newChecker(t *testing.T, adapter adapters.GatewayAdapter, cfg Config) (*Checker, store.Store, contracts.Instance) {
	t.Helper()
	ctx := context.Background()
	st := store.NewMemoryStore(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC))
	inst, err := st.CreateInstance(ctx, contracts.Instance{UserID: 101, Name: "s", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	orch := orchestrator.New(st, map[contracts.InstanceKind]adapters.GatewayAdapter{contracts.InstanceKindSub2API: adapter})
	c := New(cfg, st, orch, nil)
	return c, st, inst
}

func TestAutoSwitchAfterFailStreak(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{
		{ID: "1", DisplayName: "主号", Status: "error", Schedulable: true},   // unhealthy, live
		{ID: "2", DisplayName: "备用", Status: "active", Schedulable: false}, // healthy spare
	}}
	c, st, inst := newChecker(t, adapter, Config{FailStreak: 2, AutoSwitch: true, AllowLegacyAutoSwitch: true})

	// First check: streak becomes 1, below threshold -> no switch.
	c.checkInstance(ctx, inst)
	if len(adapter.calls) != 0 {
		t.Fatalf("should not switch on first bad check, calls=%v", adapter.calls)
	}

	// Second check: streak hits 2 -> auto-switch: disable 1, enable 2.
	c.checkInstance(ctx, inst)
	if len(adapter.calls) != 2 {
		t.Fatalf("expected disable+enable, got %v", adapter.calls)
	}
	if adapter.calls[0] != (struct {
		id  string
		val bool
	}{"1", false}) || adapter.calls[1] != (struct {
		id  string
		val bool
	}{"2", true}) {
		t.Fatalf("unexpected switch calls: %v", adapter.calls)
	}

	// Audit trail: disable + enable
	audits, _ := st.ListAudits(ctx, 101)
	var acct int
	for _, a := range audits {
		if a.TargetType == "account" {
			acct++
		}
	}
	if acct != 2 {
		t.Fatalf("expected 2 account audits, got %d", acct)
	}

	// Snapshot reflects the check.
	snaps := c.Snapshots(inst.ID)
	if len(snaps) != 1 || snaps[0].AutoSwitchNote == "" {
		t.Fatalf("expected a snapshot with auto-switch note, got %+v", snaps)
	}
}

func TestManagedAccountsStayOutOfLegacyOwnerActionsAndNotifications(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{
		{ID: "managed-remote-key", DisplayName: "managed secret", Status: "error", Schedulable: true, Balance: f64(1)},
		{ID: "owner-account", DisplayName: "owner account", Status: "active", Schedulable: true, Balance: f64(100)},
	}}
	c, st, inst := newChecker(t, adapter, Config{
		FailStreak: 1, AutoSwitch: true, AllowLegacyAutoSwitch: true,
		BalanceThreshold: 10, DriftDetection: true,
	})
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "managed", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	channel, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, DisplayName: "source", Status: contracts.UpstreamChannelActive,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: inst.UserID, InstanceID: inst.ID, PoolID: pool.ID, Status: contracts.RoutePlanPublished,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: inst.ID, ChannelID: channel.ID,
		RemoteID: "managed-remote-key", State: contracts.BindingDisabled,
	}); err != nil {
		t.Fatalf("create managed binding: %v", err)
	}

	var emitted []string
	c.SetEventSink(func(eventType string, _ int64, payload any) {
		emitted = append(emitted, eventType+":"+fmt.Sprint(payload))
	})
	c.checkInstance(ctx, inst)
	adapter.accounts[0].Priority = 99
	c.checkInstance(ctx, inst)

	if len(adapter.calls) != 0 {
		t.Fatalf("legacy switch mutated a managed account: %+v", adapter.calls)
	}
	audits, err := st.ListAudits(ctx, inst.UserID)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	for _, audit := range audits {
		if audit.TargetID == "managed-remote-key" || strings.Contains(audit.ErrorMessage, "managed secret") {
			t.Fatalf("legacy owner audit leaked managed identity: %+v", audit)
		}
		if audit.Action == "account.balance_low" || audit.Action == "instance.config_drift" {
			t.Fatalf("managed account produced a legacy owner alert: %+v", audit)
		}
	}
	for _, event := range emitted {
		if strings.HasPrefix(event, "account.balance_low:") || strings.HasPrefix(event, "instance.config_drift:") ||
			strings.HasPrefix(event, "health.auto_switch:") {
			t.Fatalf("managed account produced a legacy owner event: %s", event)
		}
	}
}

func TestCooldownPreventsRepeatSwitch(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{
		{ID: "1", Status: "error", Schedulable: true},
		{ID: "2", Status: "active", Schedulable: false},
	}}
	c, _, inst := newChecker(t, adapter, Config{FailStreak: 1, AutoSwitch: true, AllowLegacyAutoSwitch: true, Cooldown: time.Hour})

	c.checkInstance(ctx, inst) // streak 1 >= 1 -> switch (disable 1, enable 2)
	first := len(adapter.calls)
	if first == 0 {
		t.Fatal("expected a switch")
	}
	// account 1 is still error but now non-schedulable; re-mark it schedulable to
	// simulate it being re-enabled, and check cooldown blocks a second switch.
	adapter.accounts[0].Schedulable = true
	c.checkInstance(ctx, inst)
	if len(adapter.calls) != first {
		t.Fatalf("cooldown should block a repeat switch, calls grew to %d", len(adapter.calls))
	}
}

func TestHealthyAccountNotSwitched(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{
		{ID: "1", Status: "active", Schedulable: true},
		{ID: "2", Status: "active", Schedulable: true},
	}}
	c, _, inst := newChecker(t, adapter, Config{FailStreak: 1, AutoSwitch: true, AllowLegacyAutoSwitch: true})
	c.checkInstance(ctx, inst)
	c.checkInstance(ctx, inst)
	if len(adapter.calls) != 0 {
		t.Fatalf("healthy accounts must not be switched, calls=%v", adapter.calls)
	}
	snaps := c.Snapshots(inst.ID)
	if snaps[0].HealthyCount != 2 {
		t.Fatalf("expected 2 healthy, got %d", snaps[0].HealthyCount)
	}
}

func TestInstancePolicyDisablesScheduledChecks(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{{
		ID: "1", Status: "active", Schedulable: true,
	}}}
	c, st, inst := newChecker(t, adapter, Config{})
	policy := contracts.DefaultInstanceMonitorPolicy(inst.ID, inst.UserID)
	policy.Enabled = false
	if _, err := st.UpsertInstanceMonitorPolicy(ctx, policy); err != nil {
		t.Fatalf("disable monitoring: %v", err)
	}
	c.checkAll(ctx)
	if got := c.Snapshots(inst.ID); len(got) != 0 {
		t.Fatalf("disabled policy produced snapshots: %+v", got)
	}
	if _, err := c.CheckNow(ctx, inst.ID); err != nil {
		t.Fatalf("manual check should work while scheduling is disabled: %v", err)
	}
	if got := c.Snapshots(inst.ID); len(got) != 1 {
		t.Fatalf("manual check did not produce a snapshot: %+v", got)
	}
}

func TestInstancePolicyControlsCadence(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{{
		ID: "1", Status: "active", Schedulable: true,
	}}}
	c, st, inst := newChecker(t, adapter, Config{})
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	policy := contracts.DefaultInstanceMonitorPolicy(inst.ID, inst.UserID)
	policy.CheckIntervalSeconds = 30
	if _, err := st.UpsertInstanceMonitorPolicy(ctx, policy); err != nil {
		t.Fatalf("save monitoring policy: %v", err)
	}
	c.checkAll(ctx)
	firstChecked := c.Snapshots(inst.ID)[0].CheckedAt
	now = now.Add(29 * time.Second)
	c.checkAll(ctx)
	if got := c.Snapshots(inst.ID)[0].CheckedAt; !got.Equal(firstChecked) {
		t.Fatalf("check ran before interval: got %v want %v", got, firstChecked)
	}
	now = now.Add(time.Second)
	c.checkAll(ctx)
	if got := c.Snapshots(inst.ID)[0].CheckedAt; !got.Equal(now) {
		t.Fatalf("check did not run when interval elapsed: got %v want %v", got, now)
	}
}

func TestDefaultInstancePolicyDoesNotAutoSwitch(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{
		{ID: "1", Status: "error", Schedulable: true},
		{ID: "2", Status: "active", Schedulable: false},
	}}
	c, _, inst := newChecker(t, adapter, Config{FailStreak: 1, AutoSwitch: true, AllowLegacyAutoSwitch: true})
	policy := contracts.DefaultInstanceMonitorPolicy(inst.ID, inst.UserID)
	c.checkInstance(ctx, inst, policy)
	c.checkInstance(ctx, inst, policy)
	if len(adapter.calls) != 0 {
		t.Fatalf("default policy must not auto-switch: %v", adapter.calls)
	}
}

func TestLegacyGlobalGateOverridesEnabledInstancePolicy(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{accounts: []contracts.GatewayAccount{
		{ID: "1", Status: "error", Schedulable: true},
		{ID: "2", Status: "active", Schedulable: false},
	}}
	c, st, inst := newChecker(t, adapter, Config{FailStreak: 1, AutoSwitch: true})
	policy := contracts.DefaultInstanceMonitorPolicy(inst.ID, inst.UserID)
	policy.AutoSwitch = true
	policy.FailStreak = 1

	snap := c.checkInstance(ctx, inst, policy)
	if len(adapter.calls) != 0 {
		t.Fatalf("instance policy must not bypass the disabled legacy writer: %v", adapter.calls)
	}
	if snap.TotalAccounts != 2 || snap.AutoSwitchNote != "" {
		t.Fatalf("monitoring should continue without a write or switch note: %+v", snap)
	}
	audits, err := st.ListAudits(ctx, inst.UserID)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	for _, audit := range audits {
		if audit.TargetType == "account" {
			t.Fatalf("disabled legacy writer produced an account audit: %+v", audit)
		}
	}
}

func TestInstanceCheckGuardPreventsOverlap(t *testing.T) {
	c, _, inst := newChecker(t, &scriptedAdapter{}, Config{})
	if !c.startCheck(inst.ID) {
		t.Fatal("first check did not acquire the guard")
	}
	if c.startCheck(inst.ID) {
		t.Fatal("overlapping check acquired the same instance guard")
	}
	c.finishCheck(inst.ID)
	if !c.startCheck(inst.ID) {
		t.Fatal("guard was not released")
	}
	c.finishCheck(inst.ID)
}
