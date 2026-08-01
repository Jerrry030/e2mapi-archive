package autoswitch

import (
	"context"
	"fmt"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// newOrch builds an orchestrator over a fresh memory store with a fixed clock,
// for the pure-derivation and strategy-resolution tests that do not need the
// full gateway fixture.
func newOrch(t *testing.T) (*Orchestrator, store.Store, time.Time) {
	t.Helper()
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	st := store.NewMemoryStore(base)
	o := New(st, nil, WithClock(func() time.Time { return base }))
	return o, st, base
}

func failObs(channelID string, et contracts.ObservationErrorType, at time.Time) contracts.ChannelObservation {
	return contracts.ChannelObservation{ChannelID: channelID, Success: false, ErrorType: et, ObservedAt: at}
}

func okObs(channelID string, at time.Time) contracts.ChannelObservation {
	return contracts.ChannelObservation{ChannelID: channelID, Success: true, ObservedAt: at}
}

// TestDeriveSignalsAuthAndStreak: a leading run of failures ending in an auth
// error yields AuthFailure and the streak length; an older success outside the
// leading run does not reset it.
func TestDeriveSignalsAuthAndStreak(t *testing.T) {
	o, st, base := newOrch(t)
	ctx := context.Background()
	// Insert oldest-first; the store returns newest-first, so the last appended
	// (auth failure) leads the run.
	_, _ = st.AppendChannelObservation(ctx, okObs("ch-1", base.Add(-4*time.Minute)))
	_, _ = st.AppendChannelObservation(ctx, failObs("ch-1", contracts.ErrorServer, base.Add(-3*time.Minute)))
	_, _ = st.AppendChannelObservation(ctx, failObs("ch-1", contracts.ErrorAuth, base.Add(-1*time.Minute)))

	channels := []contracts.UpstreamChannel{{ID: "ch-1", Provider: "p1", Status: contracts.UpstreamChannelActive}}
	snaps := []contracts.ChannelHealthSnapshot{{ChannelID: "ch-1", HealthState: contracts.HealthUnhealthy}}
	sig := o.deriveSignals(ctx, channels, snaps)["ch-1"]
	if !sig.authFailure {
		t.Fatal("expected authFailure from leading auth error")
	}
	if sig.consecutiveFailures != 1 {
		t.Fatalf("consecutiveFailures = %d, want 1 upstream-responsibility failure", sig.consecutiveFailures)
	}
}

// TestDeriveSignalsRecentSuccessClearsStreak: when the most recent observation is
// a success, the leading failure streak is zero (the channel recovered).
func TestDeriveSignalsRecentSuccessClearsStreak(t *testing.T) {
	o, st, base := newOrch(t)
	ctx := context.Background()
	_, _ = st.AppendChannelObservation(ctx, failObs("ch-1", contracts.ErrorAuth, base.Add(-3*time.Minute)))
	_, _ = st.AppendChannelObservation(ctx, okObs("ch-1", base.Add(-1*time.Minute)))

	channels := []contracts.UpstreamChannel{{ID: "ch-1", Provider: "p1", Status: contracts.UpstreamChannelActive}}
	snaps := []contracts.ChannelHealthSnapshot{{ChannelID: "ch-1", HealthState: contracts.HealthHealthy}}
	sig := o.deriveSignals(ctx, channels, snaps)["ch-1"]
	if sig.authFailure || sig.consecutiveFailures != 0 {
		t.Fatalf("recovered channel should have no severe signals: %+v", sig)
	}
}

// TestDeriveSignalsProviderDown: when every decided channel of a provider is
// failing (and there are at least two), all of them are marked ProviderDown.
func TestDeriveSignalsProviderDown(t *testing.T) {
	o, _, _ := newOrch(t)
	ctx := context.Background()
	channels := []contracts.UpstreamChannel{
		{ID: "a", Provider: "openai", Status: contracts.UpstreamChannelActive},
		{ID: "b", Provider: "openai", Status: contracts.UpstreamChannelActive},
	}
	snaps := []contracts.ChannelHealthSnapshot{
		{ChannelID: "a", HealthState: contracts.HealthUnhealthy},
		{ChannelID: "b", HealthState: contracts.HealthUnhealthy},
	}
	got := o.deriveSignals(ctx, channels, snaps)
	if !got["a"].providerDown || !got["b"].providerDown {
		t.Fatalf("expected both channels marked providerDown: %+v", got)
	}
}

// TestDeriveSignalsProviderNotDownWhenOneHealthy: a provider with a healthy
// channel is not a provider-wide outage.
func TestDeriveSignalsProviderNotDownWhenOneHealthy(t *testing.T) {
	o, _, _ := newOrch(t)
	ctx := context.Background()
	channels := []contracts.UpstreamChannel{
		{ID: "a", Provider: "openai", Status: contracts.UpstreamChannelActive},
		{ID: "b", Provider: "openai", Status: contracts.UpstreamChannelActive},
	}
	snaps := []contracts.ChannelHealthSnapshot{
		{ChannelID: "a", HealthState: contracts.HealthUnhealthy},
		{ChannelID: "b", HealthState: contracts.HealthHealthy},
	}
	got := o.deriveSignals(ctx, channels, snaps)
	if got["a"].providerDown || got["b"].providerDown {
		t.Fatalf("a mixed-health provider must not be marked down: %+v", got)
	}
}

func TestDeriveSignalsClientFailuresCannotMarkProviderDown(t *testing.T) {
	o, st, base := newOrch(t)
	ctx := context.Background()
	channels := []contracts.UpstreamChannel{
		{ID: "a", Provider: "openai", Status: contracts.UpstreamChannelActive},
		{ID: "b", Provider: "openai", Status: contracts.UpstreamChannelActive},
	}
	for _, channel := range channels {
		for i := 0; i < 5; i++ {
			_, _ = st.AppendChannelObservation(ctx, failObs(channel.ID, contracts.ErrorClient, base.Add(-time.Duration(i)*time.Second)))
		}
	}
	// Factual success is zero, but quality has no attributable samples and must
	// remain unknown. Provider aggregation may not upgrade downstream mistakes
	// into a source-wide outage.
	snaps := []contracts.ChannelHealthSnapshot{
		{ChannelID: "a", SampleCount: 5, SuccessRate: 0, ErrorRate: 1, HealthState: contracts.HealthUnknown},
		{ChannelID: "b", SampleCount: 5, SuccessRate: 0, ErrorRate: 1, HealthState: contracts.HealthUnknown},
	}
	got := o.deriveSignals(ctx, channels, snaps)
	if got["a"].providerDown || got["b"].providerDown || got["a"].consecutiveFailures != 0 || got["b"].consecutiveFailures != 0 {
		t.Fatalf("client failures polluted provider signals: %+v", got)
	}
}

func TestDeriveSignalsPlatformErrorBreaksUpstreamFailureStreak(t *testing.T) {
	o, st, base := newOrch(t)
	ctx := context.Background()
	_, _ = st.AppendChannelObservation(ctx, failObs("ch-1", contracts.ErrorServer, base.Add(-3*time.Minute)))
	_, _ = st.AppendChannelObservation(ctx, failObs("ch-1", contracts.ErrorPlatform, base.Add(-2*time.Minute)))
	_, _ = st.AppendChannelObservation(ctx, failObs("ch-1", contracts.ErrorServer, base.Add(-time.Minute)))
	channels := []contracts.UpstreamChannel{{ID: "ch-1", Provider: "p1", Status: contracts.UpstreamChannelActive}}
	snaps := []contracts.ChannelHealthSnapshot{{ChannelID: "ch-1", HealthState: contracts.HealthUnhealthy}}
	if got := o.deriveSignals(ctx, channels, snaps)["ch-1"].consecutiveFailures; got != 1 {
		t.Fatalf("platform error must terminate the upstream streak, got %d", got)
	}
}

// TestAnalyzeSwitchRollout covers the canary-safety classifier.
func TestAnalyzeSwitchRollout(t *testing.T) {
	held, online := analyzeSwitchRollout([]contracts.ReconcileAction{
		{Type: contracts.ReconcileDisable, ChannelID: "from"},
		{Type: contracts.ReconcileHold, ChannelID: "to"},
	}, "to")
	if !held || online != 0 {
		t.Fatalf("held backup: held=%v online=%d", held, online)
	}

	held, online = analyzeSwitchRollout([]contracts.ReconcileAction{
		{Type: contracts.ReconcileEnable, ChannelID: "to"},
		{Type: contracts.ReconcileEnable, ChannelID: "other"},
	}, "to")
	if held || online != 2 {
		t.Fatalf("two newly online: held=%v online=%d", held, online)
	}

	held, online = analyzeSwitchRollout([]contracts.ReconcileAction{
		{Type: contracts.ReconcileEnable, ChannelID: "to"},
		{Type: contracts.ReconcileDisable, ChannelID: "from"},
	}, "to")
	if held || online != 1 {
		t.Fatalf("clean canary: held=%v online=%d", held, online)
	}
}

func TestStrategyForDefaultsToBalancedSmartAuto(t *testing.T) {
	o, _, _ := newOrch(t)
	got := o.strategyFor(context.Background(), contracts.RoutePlan{})
	if got.Type != contracts.StrategyBalanced {
		t.Fatalf("default strategy = %s, want balanced", got.Type)
	}
}

// TestResolvePersistedStrategyPrecedence: plan-scoped beats pool-scoped beats
// user-scoped; with none persisted, resolution falls back to the default.
func TestResolvePersistedStrategyPrecedence(t *testing.T) {
	o, st, _ := newOrch(t)
	ctx := context.Background()
	plan := contracts.RoutePlan{ID: "plan-1", PoolID: "pool-1", UserID: 101}

	if _, ok := o.resolvePersistedStrategy(ctx, plan); ok {
		t.Fatal("no strategy persisted yet; expected fallback")
	}

	if _, err := st.UpsertRouteStrategy(ctx, contracts.RouteStrategy{
		Scope: contracts.StrategyScopeUser, UserID: 101, Type: contracts.StrategyCostFirst,
	}); err != nil {
		t.Fatalf("seed user strategy: %v", err)
	}
	got, ok := o.resolvePersistedStrategy(ctx, plan)
	if !ok || got.Type != contracts.StrategyCostFirst {
		t.Fatalf("user scope: ok=%v type=%s", ok, got.Type)
	}

	if _, err := st.UpsertRouteStrategy(ctx, contracts.RouteStrategy{
		Scope: contracts.StrategyScopePool, PoolID: "pool-1", Type: contracts.StrategyLatencyFirst,
	}); err != nil {
		t.Fatalf("seed pool strategy: %v", err)
	}
	got, _ = o.resolvePersistedStrategy(ctx, plan)
	if got.Type != contracts.StrategyLatencyFirst {
		t.Fatalf("pool scope should win over user: %s", got.Type)
	}

	if _, err := st.UpsertRouteStrategy(ctx, contracts.RouteStrategy{
		Scope: contracts.StrategyScopePlan, PlanID: "plan-1", Type: contracts.StrategyStabilityFirst,
	}); err != nil {
		t.Fatalf("seed plan strategy: %v", err)
	}
	got, _ = o.resolvePersistedStrategy(ctx, plan)
	if got.Type != contracts.StrategyStabilityFirst {
		t.Fatalf("plan scope should win: %s", got.Type)
	}
}

// TestEvaluateAuthFailureTriggersSwitch: an auth-failing live channel (derived
// from raw observations, not just the snapshot) is switched out for a healthy
// backup even though its snapshot alone would not hard-gate it.
func TestEvaluateAuthFailureTriggersSwitch(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	// Primary snapshot reads borderline-OK on numbers, so only the derived auth
	// signal should gate it.
	seedSnapshot(t, f.st, "ch-primary", 0.97, contracts.HealthHealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	now := f.clk.now()
	auth := failObs("ch-primary", contracts.ErrorAuth, now.Add(-1*time.Minute))
	auth.InstanceID = f.plan.InstanceID
	_, _ = f.st.AppendChannelObservation(f.ctx, auth)

	o := New(f.st, f.eng, WithClock(f.clk.now))
	dec, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if dec == nil || dec.FromChannelID != "ch-primary" || dec.ToChannelID != "ch-backup" {
		t.Fatalf("expected auth-driven switch primary->backup, got %+v", dec)
	}
}

func TestHardCredentialFailureBypassesApprovalAndAutoApplyGates(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.99, contracts.HealthHealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	auth := failObs("ch-primary", contracts.ErrorAuth, f.clk.now().Add(-time.Minute))
	auth.InstanceID = f.plan.InstanceID
	_, _ = f.st.AppendChannelObservation(f.ctx, auth)
	o := New(f.st, f.eng, WithClock(f.clk.now), WithStrategy(contracts.RouteStrategy{
		Type: contracts.StrategyStabilityFirst, AutoApply: false, ApprovalRequired: true,
	}))
	decision, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil || decision == nil || decision.Status != contracts.AutoSwitchObserving || !decision.AutoApplied {
		t.Fatalf("hard credential failure was held for approval: decision=%+v err=%v", decision, err)
	}
	if f.gw.schedulable("acc-primary") || !f.gw.schedulable("acc-backup") {
		t.Fatalf("hard credential failure did not switch locally: %+v", f.gw.calls)
	}
}

func TestHardCredentialFailureFailsClosedWhenBackupAdmissionFails(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.99, contracts.HealthHealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	auth := failObs("ch-primary", contracts.ErrorAuth, f.clk.now().Add(-time.Minute))
	auth.InstanceID = f.plan.InstanceID
	_, _ = f.st.AppendChannelObservation(f.ctx, auth)
	f.gw.failOn["acc-backup"] = true
	decision, err := New(f.st, f.eng, WithClock(f.clk.now)).Evaluate(f.ctx, f.plan.ID)
	if err != nil || decision == nil || decision.Status != contracts.AutoSwitchFailed {
		t.Fatalf("hard failed admission result: decision=%+v err=%v", decision, err)
	}
	if f.gw.schedulable("acc-primary") {
		t.Fatalf("hard-failed binding remained scheduled after backup admission failed: %+v", f.gw.calls)
	}
}

func TestHardCredentialFailureIsolatesOnlyAffectedDownstream(t *testing.T) {
	const sharedSource = "shared-upstream"
	f := seedFixture(t, 1, []chanSeed{
		{id: "affected-primary", sourceID: sharedSource, priority: 1, status: contracts.UpstreamChannelActive, remoteID: "affected-account", live: true, onGateway: true, schedulable: true},
		{id: "affected-backup", sourceID: "affected-backup-source", priority: 2, status: contracts.UpstreamChannelActive, remoteID: "affected-backup-account", onGateway: true},
	})

	otherUser, err := f.st.CreateUser(f.ctx, contracts.User{
		Email: "unaffected-owner@local.dev", DisplayName: "Unaffected Owner",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherInstance, err := f.st.CreateInstance(f.ctx, contracts.Instance{
		UserID: otherUser.ID, Name: "unaffected-gateway", Kind: contracts.InstanceKindNewAPI,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherPool, err := f.st.CreateUpstreamPool(f.ctx, contracts.UpstreamPool{
		Name: "unaffected-pool", Status: contracts.UpstreamPoolActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range []contracts.UpstreamChannel{
		{ID: "unaffected-primary", PoolID: otherPool.ID, DisplayName: "unaffected-primary", SourceID: sharedSource, Priority: 1, Status: contracts.UpstreamChannelActive},
		{ID: "unaffected-backup", PoolID: otherPool.ID, DisplayName: "unaffected-backup", SourceID: "unaffected-backup-source", Priority: 2, Status: contracts.UpstreamChannelActive},
	} {
		if _, err := f.st.CreateUpstreamChannel(f.ctx, channel); err != nil {
			t.Fatal(err)
		}
	}
	otherPlan, err := f.st.CreateRoutePlan(f.ctx, contracts.RoutePlan{
		UserID: otherUser.ID, InstanceID: otherInstance.ID, PoolID: otherPool.ID,
		Status: contracts.RoutePlanPublished, MaxChannels: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range []contracts.PublishedBinding{
		{PlanID: otherPlan.ID, InstanceID: otherInstance.ID, ChannelID: "unaffected-primary", RemoteID: "unaffected-account", State: contracts.BindingActive},
		{PlanID: otherPlan.ID, InstanceID: otherInstance.ID, ChannelID: "unaffected-backup", RemoteID: "unaffected-backup-account", State: contracts.BindingDisabled},
	} {
		if _, err := f.st.UpsertPublishedBinding(f.ctx, binding); err != nil {
			t.Fatal(err)
		}
	}
	f.gw.accounts = append(f.gw.accounts,
		contracts.GatewayAccount{ID: "unaffected-account", Schedulable: true},
		contracts.GatewayAccount{ID: "unaffected-backup-account", Schedulable: false},
	)

	seedSnapshotForInstance(t, f.st, "affected-primary", f.plan.InstanceID, f.clk.now(), .99, contracts.HealthHealthy)
	seedSnapshotForInstance(t, f.st, "affected-backup", f.plan.InstanceID, f.clk.now(), .99, contracts.HealthHealthy)
	seedSnapshotForInstance(t, f.st, "unaffected-primary", otherPlan.InstanceID, f.clk.now(), .99, contracts.HealthHealthy)
	seedSnapshotForInstance(t, f.st, "unaffected-backup", otherPlan.InstanceID, f.clk.now(), .99, contracts.HealthHealthy)
	auth := failObs("affected-primary", contracts.ErrorAuth, f.clk.now().Add(-time.Minute))
	auth.ID = "affected-auth-failure"
	auth.InstanceID = f.plan.InstanceID
	if _, err := f.st.AppendChannelObservation(f.ctx, auth); err != nil {
		t.Fatal(err)
	}

	o := New(f.st, f.eng, WithClock(f.clk.now), WithStrategy(contracts.RouteStrategy{
		Type: contracts.StrategyStabilityFirst, AutoApply: false, ApprovalRequired: true,
	}))
	decision, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil || decision == nil || decision.Status != contracts.AutoSwitchObserving || !decision.AutoApplied {
		t.Fatalf("affected downstream was not isolated immediately: decision=%+v err=%v", decision, err)
	}
	if decision.FromChannelID != "affected-primary" || decision.ToChannelID != "affected-backup" {
		t.Fatalf("unexpected affected switch: %+v", decision)
	}
	if f.gw.schedulable("affected-account") || !f.gw.schedulable("affected-backup-account") {
		t.Fatalf("affected gateway did not switch locally: %+v", f.gw.calls)
	}
	if !f.gw.schedulable("unaffected-account") || f.gw.schedulable("unaffected-backup-account") {
		t.Fatalf("credential failure spread to another downstream: %+v", f.gw.calls)
	}
	for _, call := range f.gw.calls {
		if call.instanceID == otherPlan.InstanceID {
			t.Fatalf("unaffected gateway received scheduling command: %+v", call)
		}
	}
	otherBindings, err := f.st.ListPublishedBindings(f.ctx, otherPlan.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range otherBindings {
		want := contracts.BindingDisabled
		if binding.ChannelID == "unaffected-primary" {
			want = contracts.BindingActive
		}
		if binding.State != want {
			t.Fatalf("unaffected binding %s state=%s, want %s", binding.ChannelID, binding.State, want)
		}
	}
	if next, err := o.Evaluate(f.ctx, otherPlan.ID); err != nil || next != nil {
		t.Fatalf("unaffected downstream produced a decision: decision=%+v err=%v", next, err)
	}
	decisions, err := f.st.ListAutoSwitchDecisions(f.ctx, contracts.AutoSwitchDecisionFilter{PlanID: otherPlan.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 0 {
		t.Fatalf("unaffected downstream has decisions: %s", fmt.Sprint(decisions))
	}
}
