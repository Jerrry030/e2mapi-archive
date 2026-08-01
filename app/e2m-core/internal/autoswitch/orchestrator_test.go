package autoswitch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/publish"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/strategy"
)

// fakeGateway is a minimal publish.Gateway that records calls and reflects
// scheduling toggles so a subsequent reconcile diff sees the new state. failOn
// forces an error for a given account id on SetSchedulable.
type fakeGateway struct {
	accounts []contracts.GatewayAccount
	failOn   map[string]bool
	deleted  []string
	created  []contracts.GatewayAccountSpec
	calls    []gatewayToggle
}

type gatewayToggle struct {
	instanceID string
	accountID  string
	enabled    bool
}

func (g *fakeGateway) ListAccounts(ctx context.Context, instanceID string) ([]contracts.GatewayAccount, error) {
	out := make([]contracts.GatewayAccount, len(g.accounts))
	copy(out, g.accounts)
	return out, nil
}

func (g *fakeGateway) SetSchedulable(ctx context.Context, instanceID, accountID string, schedulable bool, reason string) error {
	g.calls = append(g.calls, gatewayToggle{instanceID: instanceID, accountID: accountID, enabled: schedulable})
	if g.failOn[accountID] {
		return errors.New("gateway rejected toggle for " + accountID)
	}
	for i := range g.accounts {
		if g.accounts[i].ID == accountID {
			g.accounts[i].Schedulable = schedulable
			return nil
		}
	}
	return nil
}

func (g *fakeGateway) ProvisionAccount(ctx context.Context, instanceID string, spec contracts.GatewayAccountSpec, reason string) (contracts.GatewayProvisionResult, error) {
	g.created = append(g.created, spec)
	id := spec.RemoteID
	if id == "" {
		id = "prov-" + spec.ChannelID
	}
	g.accounts = append(g.accounts, contracts.GatewayAccount{ID: id, Schedulable: spec.Schedulable})
	return contracts.GatewayProvisionResult{RemoteID: id, Created: true}, nil
}

func (g *fakeGateway) UpdateAccount(ctx context.Context, instanceID string, spec contracts.GatewayAccountSpec, reason string) (contracts.GatewayProvisionResult, error) {
	return contracts.GatewayProvisionResult{RemoteID: spec.RemoteID}, nil
}

func (g *fakeGateway) DeleteAccount(ctx context.Context, instanceID, accountID, reason string) error {
	g.deleted = append(g.deleted, accountID)
	return nil
}

func (g *fakeGateway) schedulable(id string) bool {
	for _, a := range g.accounts {
		if a.ID == id {
			return a.Schedulable
		}
	}
	return false
}

// clock is an advanceable time source for the observation-window tests.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

// chanSeed describes one upstream channel to seed.
type chanSeed struct {
	id          string
	sourceID    string
	priority    int
	costHint    float64
	status      contracts.UpstreamChannelStatus
	remoteID    string // gateway account id; "" means no remote mapping
	live        bool   // seed an Active PublishedBinding (currently scheduled)
	onGateway   bool   // present on the gateway
	schedulable bool   // gateway scheduling state
}

type fixture struct {
	ctx  context.Context
	st   store.Store
	gw   *fakeGateway
	eng  *publish.Engine
	plan contracts.RoutePlan
	clk  *clock
}

type evalResult struct {
	decision *contracts.AutoSwitchDecision
	err      error
}

type captureDispatcher struct {
	events []notify.Event
}

func (d *captureDispatcher) DispatchAll(_ context.Context, event notify.Event, _ []contracts.NotificationRoute) {
	d.events = append(d.events, event)
}

// blockingClaimStore pauses the winning evaluator after its atomic claim is
// durable, but before Evaluate can mutate channel state or call reconcile.
type blockingClaimStore struct {
	store.Store
	claimed chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingClaimStore) ClaimAutoSwitchDecision(ctx context.Context, input contracts.AutoSwitchDecision) (contracts.AutoSwitchDecision, bool, error) {
	d, claimed, err := s.Store.ClaimAutoSwitchDecision(ctx, input)
	if err == nil && claimed {
		s.once.Do(func() { close(s.claimed) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return contracts.AutoSwitchDecision{}, false, ctx.Err()
		}
	}
	return d, claimed, err
}

// racingObserveStore lets two observers read the same observing snapshot, then
// pauses the CAS winner so the loser must observe the applying claim and exit.
type racingObserveStore struct {
	store.Store
	decisionID string
	readCount  atomic.Int32
	readsReady chan struct{}
	claimed    chan struct{}
	release    chan struct{}
	readsOnce  sync.Once
	claimOnce  sync.Once
}

// blockingTransitionStore pauses final persistence after all Evaluate work has
// completed, proving notification is emitted only by the claim owner and only
// after applying transitions to its externally meaningful state.
type blockingTransitionStore struct {
	store.Store
	beforeSave chan struct{}
	release    chan struct{}
	once       sync.Once
}

type cancelAfterObservationClaimStore struct {
	store.Store
	cancel context.CancelFunc
}

type failGlobalCohortStore struct{ store.Store }

func (s *failGlobalCohortStore) ListUpstreamChannels(ctx context.Context, poolID string) ([]contracts.UpstreamChannel, error) {
	if poolID == "" {
		return nil, errors.New("global cohort unavailable")
	}
	return s.Store.ListUpstreamChannels(ctx, poolID)
}

// reclaimBeforeSecondSideEffectStore lets the first gateway item run, then
// expires and reclaims the applying lease before the second item. It models a
// stalled worker whose generation is superseded by another Core instance.
type reclaimBeforeSecondSideEffectStore struct {
	store.Store
	renews    atomic.Int32
	reclaimed chan contracts.AutoSwitchDecision
}

func (s *reclaimBeforeSecondSideEffectStore) RenewAutoSwitchDecisionLease(
	ctx context.Context,
	id string,
	leaseVersion int64,
	leaseDuration time.Duration,
) (contracts.AutoSwitchDecision, error) {
	if s.renews.Add(1) == 2 {
		if err := s.Store.ReleaseAutoSwitchDecisionLease(ctx, id, leaseVersion); err != nil {
			return contracts.AutoSwitchDecision{}, err
		}
		now := time.Now().UTC()
		claimed, ok, err := s.Store.ClaimExpiredAutoSwitchDecision(
			ctx, id, now, now.Add(-leaseDuration), now.Add(leaseDuration),
		)
		if err != nil {
			return contracts.AutoSwitchDecision{}, err
		}
		if !ok {
			return contracts.AutoSwitchDecision{}, errors.New("replacement worker did not reclaim expired lease")
		}
		s.reclaimed <- claimed
	}
	return s.Store.RenewAutoSwitchDecisionLease(ctx, id, leaseVersion, leaseDuration)
}

func (s *cancelAfterObservationClaimStore) ClaimAutoSwitchObservation(ctx context.Context, input contracts.AutoSwitchDecision, leaseDuration time.Duration) (contracts.AutoSwitchDecision, error) {
	d, err := s.Store.ClaimAutoSwitchObservation(ctx, input, leaseDuration)
	if err == nil {
		s.cancel()
	}
	return d, err
}

func (s *blockingTransitionStore) TransitionAutoSwitchDecision(ctx context.Context, input contracts.AutoSwitchDecision, expected contracts.AutoSwitchStatus) (contracts.AutoSwitchDecision, error) {
	if expected == contracts.AutoSwitchApplying && input.Status != contracts.AutoSwitchApplying {
		s.once.Do(func() { close(s.beforeSave) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return contracts.AutoSwitchDecision{}, ctx.Err()
		}
	}
	return s.Store.TransitionAutoSwitchDecision(ctx, input, expected)
}

func (s *racingObserveStore) GetAutoSwitchDecision(ctx context.Context, id string) (contracts.AutoSwitchDecision, error) {
	d, err := s.Store.GetAutoSwitchDecision(ctx, id)
	if err != nil || id != s.decisionID {
		return d, err
	}
	n := s.readCount.Add(1)
	if n <= 2 {
		if n == 2 {
			s.readsOnce.Do(func() { close(s.readsReady) })
		}
		select {
		case <-s.readsReady:
		case <-ctx.Done():
			return contracts.AutoSwitchDecision{}, ctx.Err()
		}
	}
	return d, nil
}

func (s *racingObserveStore) ClaimAutoSwitchObservation(ctx context.Context, input contracts.AutoSwitchDecision, leaseDuration time.Duration) (contracts.AutoSwitchDecision, error) {
	d, err := s.Store.ClaimAutoSwitchObservation(ctx, input, leaseDuration)
	if err == nil && input.ID == s.decisionID {
		s.claimOnce.Do(func() { close(s.claimed) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return contracts.AutoSwitchDecision{}, ctx.Err()
		}
	}
	return d, err
}

// seedFixture builds an instance, pool, channels, plan, bindings, and gateway
// accounts for a single plan/pool closed loop.
func seedFixture(t *testing.T, maxChannels int, chans []chanSeed) fixture {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	st := store.NewMemoryStore(base)

	user, err := st.CreateUser(ctx, contracts.User{
		Email: "fixture-101@local.dev", DisplayName: "Fixture Owner",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	inst, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "gw", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "claude-stable", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	gw := &fakeGateway{failOn: map[string]bool{}}
	for _, c := range chans {
		labels := map[string]string{}
		if c.remoteID != "" {
			labels["remote_id"] = c.remoteID
		}
		ch := contracts.UpstreamChannel{
			ID: c.id, PoolID: pool.ID, DisplayName: c.id, Provider: "anthropic",
			SourceID: c.sourceID, Priority: c.priority, CostHint: c.costHint, Status: c.status, Labels: labels,
		}
		if _, err := st.CreateUpstreamChannel(ctx, ch); err != nil {
			t.Fatalf("create channel %s: %v", c.id, err)
		}
		if c.onGateway && c.remoteID != "" {
			gw.accounts = append(gw.accounts, contracts.GatewayAccount{ID: c.remoteID, Schedulable: c.schedulable})
		}
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: user.ID, InstanceID: inst.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanPublished, MaxChannels: maxChannels,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	for _, c := range chans {
		if c.remoteID == "" && !c.live {
			continue
		}
		state := contracts.BindingDisabled
		if c.live {
			state = contracts.BindingActive
		}
		if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
			PlanID: plan.ID, InstanceID: inst.ID, ChannelID: c.id, RemoteID: c.remoteID, State: state,
		}); err != nil {
			t.Fatalf("seed binding %s: %v", c.id, err)
		}
	}
	eng := publish.New(st, gw)
	return fixture{ctx: ctx, st: st, gw: gw, eng: eng, plan: plan, clk: &clock{t: base}}
}

// seedSnapshot upserts a 5m health snapshot for a channel.
func seedSnapshot(t *testing.T, st store.Store, channelID string, success float64, state contracts.HealthState) {
	t.Helper()
	score := 95.0
	if state != contracts.HealthHealthy {
		score = 20.0
	}
	instanceID := ""
	if plans, listErr := st.ListRoutePlans(context.Background(), 0); listErr == nil && len(plans) > 0 {
		instanceID = plans[0].InstanceID
	}
	_, err := st.UpsertChannelHealthSnapshot(context.Background(), contracts.ChannelHealthSnapshot{
		ChannelID: channelID, InstanceID: instanceID, Window: contracts.Window5m, SampleCount: 20,
		SuccessRate: success, ErrorRate: 1 - success,
		QualitySampleCount: 20, QualitySuccessRate: success, QualityErrorRate: 1 - success,
		UpstreamErrorRate: 1 - success, TTFTP95: 600, DurationP95: 3000,
		SuccessScore: score, TTFTScore: score, DurationScore: score, StabilityScore: score, CostScore: score,
		HealthState: state,
	})
	if err != nil {
		t.Fatalf("seed snapshot %s: %v", channelID, err)
	}
}

func seedPreferenceSnapshot(t *testing.T, st store.Store, instanceID, channelID string, success, ttftP95, durationP95, successScore, ttftScore, durationScore, stabilityScore float64) {
	t.Helper()
	snapshot := contracts.ChannelHealthSnapshot{}
	snapshot.ChannelID = channelID
	snapshot.InstanceID = instanceID
	snapshot.Window = contracts.Window5m
	snapshot.SampleCount = 20
	snapshot.SuccessRate = success
	snapshot.ErrorRate = 1 - success
	snapshot.QualitySampleCount = 20
	snapshot.QualitySuccessRate = success
	snapshot.QualityErrorRate = 1 - success
	snapshot.UpstreamErrorRate = 1 - success
	snapshot.TTFTP95 = ttftP95
	snapshot.DurationP95 = durationP95
	snapshot.SuccessScore = successScore
	snapshot.TTFTScore = ttftScore
	snapshot.DurationScore = durationScore
	snapshot.StabilityScore = stabilityScore
	snapshot.CostScore = 50
	snapshot.HealthState = contracts.HealthHealthy
	if _, err := st.UpsertChannelHealthSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
}

func seedSnapshotForInstance(
	t *testing.T,
	st store.Store,
	channelID, instanceID string,
	bucket time.Time,
	success float64,
	state contracts.HealthState,
) {
	t.Helper()
	_, err := st.UpsertChannelHealthSnapshot(context.Background(), contracts.ChannelHealthSnapshot{
		ChannelID: channelID, InstanceID: instanceID, Window: contracts.Window5m,
		BucketStart: bucket, CreatedAt: bucket, SampleCount: 20,
		SuccessRate: success, ErrorRate: 1 - success,
		QualitySampleCount: 20, QualitySuccessRate: success, QualityErrorRate: 1 - success,
		UpstreamErrorRate: 1 - success, TTFTP95: 600, DurationP95: 3000,
		HealthState: state,
	})
	if err != nil {
		t.Fatalf("seed scoped snapshot %s/%s: %v", instanceID, channelID, err)
	}
}

func healthyBackup() chanSeed {
	return chanSeed{id: "ch-backup", priority: 2, status: contracts.UpstreamChannelActive, remoteID: "acc-backup", onGateway: true, schedulable: false}
}

func livePrimary() chanSeed {
	return chanSeed{id: "ch-primary", priority: 1, status: contracts.UpstreamChannelActive, remoteID: "acc-primary", live: true, onGateway: true, schedulable: true}
}

// TestEvaluateLowRiskSwitchAppliesAndObserves: a failing live channel with a
// healthy backup is auto-switched (canary apply) and enters observation.
func TestEvaluateLowRiskSwitchAppliesAndObserves(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)

	o := New(f.st, f.eng, WithClock(f.clk.now))
	dec, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if dec == nil {
		t.Fatal("expected a decision")
	}
	if dec.Status != contracts.AutoSwitchObserving {
		t.Fatalf("status = %q, want observing (risk=%s reason=%s)", dec.Status, dec.RiskLevel, dec.RiskReason)
	}
	if dec.RiskLevel != contracts.RiskLevelL1 {
		t.Fatalf("risk = %q, want L1", dec.RiskLevel)
	}
	if dec.FromChannelID != "ch-primary" || dec.ToChannelID != "ch-backup" {
		t.Fatalf("from/to = %s/%s, want ch-primary/ch-backup", dec.FromChannelID, dec.ToChannelID)
	}
	if !dec.AutoApplied || dec.AppliedAt == nil || dec.ObserveUntil == nil {
		t.Fatalf("expected auto-applied with applied/observe timestamps: %+v", dec)
	}
	if f.gw.schedulable("acc-primary") {
		t.Fatal("primary should be drained out of scheduling")
	}
	if !f.gw.schedulable("acc-backup") {
		t.Fatal("backup should be scheduling after switch")
	}
	if len(f.gw.deleted) != 0 {
		t.Fatalf("switch must never delete: %+v", f.gw.deleted)
	}
	// A dry-run and an apply run should both be recorded with the auto trigger.
	runs, _ := f.st.ListReconcileRuns(f.ctx, f.plan.ID, 0)
	var sawAutoApply bool
	for _, r := range runs {
		if r.Kind == contracts.ReconcileRunApply && r.Trigger == contracts.ReconcileTriggerAuto {
			sawAutoApply = true
		}
	}
	if !sawAutoApply {
		t.Fatalf("expected an auto-triggered apply run, got %+v", runs)
	}
}

func TestEvaluateSkipsHigherScoredKeyFromFailingSource(t *testing.T) {
	channels := map[string]contracts.UpstreamChannel{
		"primary":          {ID: "primary", SourceID: "source-a"},
		"same-source":      {ID: "same-source", SourceID: "source-a"},
		"different-source": {ID: "different-source", SourceID: "source-b"},
	}
	eligible := []strategy.ScoredCandidate{
		{ChannelID: "same-source", Score: 100},
		{ChannelID: "different-source", Score: 95},
	}
	if got := bestDifferentSource("primary", eligible, channels); got != "different-source" {
		t.Fatalf("best different source = %q, want different-source", got)
	}
}

func TestEvaluateDoesNotSelectUnpublishedZeroSampleChannel(t *testing.T) {
	primary := livePrimary()
	primary.sourceID = "source-a"
	backup := healthyBackup()
	backup.sourceID = "source-b"
	unpublished := chanSeed{
		id: "ch-unpublished", sourceID: "source-c", status: contracts.UpstreamChannelActive,
	}

	f := seedFixture(t, 1, []chanSeed{primary, backup, unpublished})
	seedSnapshot(t, f.st, primary.id, 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, backup.id, 0.95, contracts.HealthHealthy)

	dec, err := New(f.st, f.eng, WithClock(f.clk.now)).Evaluate(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if dec == nil || dec.ToChannelID != backup.id {
		t.Fatalf("unpublished zero-sample channel must not outrank an assigned key, got %+v", dec)
	}
	if len(f.gw.created) != 0 {
		t.Fatalf("quality switching must not allocate a new key: %+v", f.gw.created)
	}
}

// TestEvaluateNoBackupFailsClosedLocally: a failing channel with no eligible
// backup is drained from this plan instead of continuing bad traffic.
// TestEvaluatePublishesEventWithoutNotificationRoutes: console/internal events
// are independent from Feishu/QQ/webhook route configuration. A user with no
// notification routes should still see upstream.auto_switch in the SSE feed.
func TestEvaluatePublishesEventWithoutNotificationRoutes(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)

	var events []contracts.AutoSwitchDecision
	o := New(f.st, f.eng, WithClock(f.clk.now), WithEventSink(func(ctx context.Context, d contracts.AutoSwitchDecision) {
		events = append(events, d)
	}))
	dec, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if dec == nil || dec.Status != contracts.AutoSwitchObserving {
		t.Fatalf("expected observing decision, got %+v", dec)
	}
	if len(events) != 1 {
		t.Fatalf("expected one platform event, got %d", len(events))
	}
	if events[0].ID != dec.ID || events[0].Status != contracts.AutoSwitchObserving {
		t.Fatalf("event decision mismatch: event=%+v decision=%+v", events[0], dec)
	}
}

func TestUserNotificationContainsOnlyAnonymousOutcomeAndFacts(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	if _, err := f.st.CreateNotificationRoute(f.ctx, contracts.NotificationRoute{
		UserID: f.plan.UserID, Name: "capture", Channel: contracts.NotificationChannelWebhook,
		TargetRef: "credential_ref:user/1/notification/capture", Enabled: true,
	}); err != nil {
		t.Fatalf("create route: %v", err)
	}
	dispatcher := &captureDispatcher{}
	decision, err := New(f.st, f.eng, WithClock(f.clk.now), WithNotifier(dispatcher)).Evaluate(f.ctx, f.plan.ID)
	if err != nil || decision == nil || len(dispatcher.events) != 1 {
		t.Fatalf("notification setup: decision=%+v events=%d err=%v", decision, len(dispatcher.events), err)
	}
	event := dispatcher.events[0]
	for _, secret := range []string{decision.ID, decision.PlanID, decision.InstanceID, decision.FromChannelID, decision.ToChannelID,
		decision.TriggerReason, decision.RiskReason, "ch-primary", "ch-backup", "plan-", "inst-", "aswitch-"} {
		if secret != "" && (strings.Contains(event.Text, secret) || strings.Contains(event.Title, secret)) {
			t.Fatalf("notification leaked %q: title=%q text=%q", secret, event.Title, event.Text)
		}
		for key, value := range event.Fields {
			if strings.Contains(key, "Id") || secret != "" && strings.Contains(value, secret) {
				t.Fatalf("notification fields leaked identity: %+v", event.Fields)
			}
		}
	}
	if event.InstanceID != "" || event.Fields["status"] == "" || event.Fields["autoApplied"] != "true" ||
		event.EventLevel != autoSwitchEventLevel(decision.Status) ||
		event.Result != autoSwitchNotificationResult(decision.Status) {
		t.Fatalf("notification must expose anonymous outcome only: %+v", event)
	}
}

func TestEvaluateNoBackupFailsClosedLocally(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary()})
	seedSnapshot(t, f.st, "ch-primary", 0.40, contracts.HealthUnhealthy)

	o := New(f.st, f.eng, WithClock(f.clk.now))
	dec, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if dec == nil || dec.Status != contracts.AutoSwitchCompleted {
		t.Fatalf("expected completed fail-closed decision, got %+v", dec)
	}
	if dec.RiskLevel != contracts.RiskLevelL3 {
		t.Fatalf("risk = %q, want L3", dec.RiskLevel)
	}
	if dec.ToChannelID != "" {
		t.Fatalf("expected no backup, got to=%s", dec.ToChannelID)
	}
	if f.gw.schedulable("acc-primary") {
		t.Fatal("unhealthy primary must be locally drained when no healthy source remains")
	}
	ch, _ := f.st.GetUpstreamChannel(f.ctx, "ch-primary")
	if ch.Status != contracts.UpstreamChannelActive {
		t.Fatalf("fail-closed changed shared channel lifecycle: %q", ch.Status)
	}
}

func TestEvaluateNoBackupBypassesSwitchCooldown(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary()})
	seedSnapshot(t, f.st, "ch-primary", 0.40, contracts.HealthUnhealthy)
	priorAt := f.clk.now().Add(-time.Minute)
	if _, err := f.st.CreateAutoSwitchDecision(f.ctx, contracts.AutoSwitchDecision{
		PlanID: f.plan.ID, InstanceID: f.plan.InstanceID, PoolID: f.plan.PoolID,
		Status: contracts.AutoSwitchCompleted, AutoApplied: true, AppliedAt: &priorAt,
	}); err != nil {
		t.Fatalf("seed recent switch: %v", err)
	}

	o := New(f.st, f.eng, WithClock(f.clk.now), WithStrategy(contracts.RouteStrategy{
		Type: contracts.StrategyStabilityFirst, AutoApply: true, CooldownSeconds: 600,
	}))
	dec, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil || dec == nil || dec.Status != contracts.AutoSwitchCompleted {
		t.Fatalf("fail-closed was blocked by cooldown: decision=%+v err=%v", dec, err)
	}
	if f.gw.schedulable("acc-primary") {
		t.Fatal("no-backup failure must be drained despite switch cooldown")
	}
}

// An unassigned catalog key is not a quality-switch candidate. With no already
// assigned backup, the failing binding is locally failed closed without
// provisioning or changing key ownership.
func TestEvaluateUnassignedBackupFailsClosedWithoutProvisioning(t *testing.T) {
	backup := chanSeed{id: "ch-backup", priority: 2, status: contracts.UpstreamChannelActive} // no remote, not on gateway
	f := seedFixture(t, 1, []chanSeed{livePrimary(), backup})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)

	o := New(f.st, f.eng, WithClock(f.clk.now))
	dec, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if dec == nil || dec.Status != contracts.AutoSwitchCompleted || dec.ToChannelID != "" {
		t.Fatalf("expected fail-closed decision without replacement, got %+v", dec)
	}
	if dec.RiskLevel != contracts.RiskLevelL3 {
		t.Fatalf("risk = %q want L3 (reason=%s)", dec.RiskLevel, dec.RiskReason)
	}
	if actions := dec.DryRunResult.Actions; len(actions) != 1 || actions[0].Type != contracts.ReconcileDisable {
		t.Fatalf("expected only the failed binding to be disabled, got %+v", actions)
	}
	// The shared catalog stays active; only this plan's binding is isolated.
	ch, _ := f.st.GetUpstreamChannel(f.ctx, "ch-primary")
	if ch.Status != contracts.UpstreamChannelActive {
		t.Fatalf("primary status = %q, want reverted to active", ch.Status)
	}
	if len(f.gw.created) != 0 {
		t.Fatalf("high-risk switch must not provision: %+v", f.gw.created)
	}
}

// TestEvaluateApplyFailureRecordsFailed: when the reconcile apply errors, the
// decision is failed and the orchestrator restores the baseline.
func TestEvaluateApplyFailureRecordsFailed(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	f.gw.failOn["acc-primary"] = true // draining the failing channel errors

	o := New(f.st, f.eng, WithClock(f.clk.now))
	dec, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if dec == nil || dec.Status != contracts.AutoSwitchFailed {
		t.Fatalf("expected failed decision, got %+v", dec)
	}
	if !strings.Contains(dec.Error, "apply") {
		t.Fatalf("error should mention apply failure, got %q", dec.Error)
	}
	if len(f.gw.deleted) != 0 {
		t.Fatalf("apply failure must never delete: %+v", f.gw.deleted)
	}
}

// TestObserveSuccessCompletes: a switch whose backup stays healthy through the
// observation window completes.
func TestObserveSuccessCompletes(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)

	o := New(f.st, f.eng, WithClock(f.clk.now), WithObservationWindow(15*time.Minute))
	dec, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil || dec == nil || dec.Status != contracts.AutoSwitchObserving {
		t.Fatalf("setup switch failed: dec=%+v err=%v", dec, err)
	}
	f.clk.add(20 * time.Minute) // past the observation window
	res, err := o.Observe(f.ctx, dec.ID)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if res.Status != contracts.AutoSwitchCompleted {
		t.Fatalf("status = %q, want completed", res.Status)
	}
	if res.ResolvedAt == nil {
		t.Fatal("completed decision should carry ResolvedAt")
	}
}

// TestObserveFailureRollsBack: a switch whose backup goes bad in the observation
// window is rolled back to the original channel via reconcile.
func TestObserveFailureRollsBack(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)

	o := New(f.st, f.eng, WithClock(f.clk.now), WithObservationWindow(15*time.Minute))
	dec, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil || dec == nil || dec.Status != contracts.AutoSwitchObserving {
		t.Fatalf("setup switch failed: dec=%+v err=%v", dec, err)
	}
	if !f.gw.schedulable("acc-backup") || f.gw.schedulable("acc-primary") {
		t.Fatal("precondition: expected backup live, primary drained after switch")
	}

	// The backup now degrades during observation.
	seedSnapshot(t, f.st, "ch-backup", 0.30, contracts.HealthUnhealthy)
	// Even if passive data now looks healthy, the original remains quarantined
	// until active recovery probes pass.
	seedSnapshot(t, f.st, "ch-primary", 0.99, contracts.HealthHealthy)
	f.clk.add(20 * time.Minute)

	res, err := o.Observe(f.ctx, dec.ID)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if res.Status != contracts.AutoSwitchRolledBack {
		t.Fatalf("status = %q, want rolled_back", res.Status)
	}
	if f.gw.schedulable("acc-primary") {
		t.Fatal("failed replacement must not restore the original without active probes")
	}
	if f.gw.schedulable("acc-backup") {
		t.Fatal("rollback should drain the unhealthy backup")
	}
	if len(f.gw.deleted) != 0 {
		t.Fatalf("rollback must never delete: %+v", f.gw.deleted)
	}
}

// TestEvaluateIdempotentPerWindow: two evaluations of the same failure window
// produce exactly one decision (fingerprint dedup). Uses a non-auto-applying
// strategy so the state is unchanged between calls and the same trigger recurs.
func TestEvaluateIdempotentPerWindow(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)

	o := New(f.st, f.eng, WithClock(f.clk.now),
		WithStrategy(contracts.RouteStrategy{Type: contracts.StrategyStabilityFirst, AutoApply: false}))

	first, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil || first == nil {
		t.Fatalf("first evaluate: dec=%+v err=%v", first, err)
	}
	if first.Status != contracts.AutoSwitchProposed {
		t.Fatalf("first status = %q, want proposed", first.Status)
	}
	second, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("second evaluate: %v", err)
	}
	if second == nil || second.ID != first.ID {
		t.Fatalf("second evaluate should return the same active decision, got %+v", second)
	}
	all, _ := f.st.ListAutoSwitchDecisions(f.ctx, contracts.AutoSwitchDecisionFilter{PlanID: f.plan.ID})
	if len(all) != 1 {
		t.Fatalf("expected exactly one decision for the window, got %d", len(all))
	}
}

// TestEvaluateConcurrentClaimRunsSideEffectsOnce proves the fingerprint claim
// is acquired before dry-run/apply/event side effects, not merely deduplicated
// after both evaluators have already executed them.
func TestEvaluateConcurrentClaimRunsSideEffectsOnce(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)

	gate := &blockingClaimStore{
		Store: f.st, claimed: make(chan struct{}), release: make(chan struct{}),
	}
	var events atomic.Int32
	o := New(gate, f.eng, WithClock(f.clk.now), WithEventSink(func(context.Context, contracts.AutoSwitchDecision) {
		events.Add(1)
	}))

	firstCh := make(chan evalResult, 1)
	go func() {
		d, err := o.Evaluate(f.ctx, f.plan.ID)
		firstCh <- evalResult{decision: d, err: err}
	}()
	select {
	case <-gate.claimed:
	case <-time.After(2 * time.Second):
		t.Fatal("first evaluator did not acquire the fingerprint claim")
	}

	secondCh := make(chan evalResult, 1)
	go func() {
		d, err := o.Evaluate(f.ctx, f.plan.ID)
		secondCh <- evalResult{decision: d, err: err}
	}()
	var second evalResult
	select {
	case second = <-secondCh:
	case <-time.After(2 * time.Second):
		close(gate.release)
		t.Fatal("losing evaluator did not return the active claim")
	}
	if second.err != nil || second.decision == nil || second.decision.Status != contracts.AutoSwitchApplying {
		close(gate.release)
		t.Fatalf("losing evaluator = %+v, want applying owner", second)
	}
	close(gate.release)
	first := <-firstCh
	if first.err != nil || first.decision == nil || first.decision.Status != contracts.AutoSwitchObserving {
		t.Fatalf("winning evaluator = %+v, want observing", first)
	}
	if second.decision.ID != first.decision.ID {
		t.Fatalf("concurrent decisions differ: winner=%s loser=%s", first.decision.ID, second.decision.ID)
	}

	runs, err := f.st.ListReconcileRuns(f.ctx, f.plan.ID, 0)
	if err != nil {
		t.Fatalf("list reconcile runs: %v", err)
	}
	var dryRuns, applies int
	for _, run := range runs {
		switch run.Kind {
		case contracts.ReconcileRunDryRun:
			dryRuns++
		case contracts.ReconcileRunApply:
			applies++
		}
	}
	if dryRuns != 1 || applies != 1 || events.Load() != 1 {
		t.Fatalf("side effects: dry-run=%d apply=%d events=%d, want one each", dryRuns, applies, events.Load())
	}
}

func TestEvaluateNotifiesOnlyAfterClaimIsFinalized(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)

	gate := &blockingTransitionStore{
		Store: f.st, beforeSave: make(chan struct{}), release: make(chan struct{}),
	}
	var events atomic.Int32
	o := New(gate, f.eng, WithClock(f.clk.now), WithEventSink(func(context.Context, contracts.AutoSwitchDecision) {
		events.Add(1)
	}))
	result := make(chan evalResult, 1)
	go func() {
		d, err := o.Evaluate(f.ctx, f.plan.ID)
		result <- evalResult{decision: d, err: err}
	}()
	select {
	case <-gate.beforeSave:
	case <-time.After(2 * time.Second):
		t.Fatal("evaluate did not reach final transition")
	}
	if events.Load() != 0 {
		close(gate.release)
		t.Fatalf("event emitted while decision was still applying: %d", events.Load())
	}
	close(gate.release)
	res := <-result
	if res.err != nil || res.decision == nil || res.decision.Status != contracts.AutoSwitchObserving {
		t.Fatalf("evaluate result=%+v, want observing", res)
	}
	if events.Load() != 1 {
		t.Fatalf("events=%d after final transition, want 1", events.Load())
	}
}

func TestStaleApplyingOwnerCannotContinueOrCompensateGatewaySwitch(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)

	gate := &reclaimBeforeSecondSideEffectStore{
		Store: f.st, reclaimed: make(chan contracts.AutoSwitchDecision, 1),
	}
	o := New(gate, f.eng, WithClock(f.clk.now))
	decision, err := o.Evaluate(f.ctx, f.plan.ID)
	if !errors.Is(err, store.ErrConflict) || decision != nil {
		t.Fatalf("stale evaluator result: decision=%+v err=%v, want lease conflict", decision, err)
	}

	replacementOwner := <-gate.reclaimed
	if replacementOwner.LeaseVersion <= 1 || replacementOwner.Status != contracts.AutoSwitchApplying {
		t.Fatalf("replacement owner=%+v, want newer applying generation", replacementOwner)
	}
	if len(f.gw.calls) != 1 || f.gw.calls[0].accountID != "acc-backup" || !f.gw.calls[0].enabled {
		t.Fatalf("stale owner gateway calls=%+v, want only initial replacement enable", f.gw.calls)
	}
	if !f.gw.schedulable("acc-primary") || !f.gw.schedulable("acc-backup") {
		t.Fatal("stale owner drained the source or reversed the admitted replacement")
	}
	current, getErr := f.st.GetAutoSwitchDecision(f.ctx, replacementOwner.ID)
	if getErr != nil || current.Status != contracts.AutoSwitchApplying || current.LeaseVersion != replacementOwner.LeaseVersion {
		t.Fatalf("stale owner overwrote replacement decision: current=%+v err=%v", current, getErr)
	}
}

// TestObserveConcurrentClaimRunsRollbackOnce forces both observers to read the
// same due decision. The observing->applying CAS permits only one rollback and
// one event.
func TestObserveConcurrentClaimRunsRollbackOnce(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)

	setup := New(f.st, f.eng, WithClock(f.clk.now), WithObservationWindow(15*time.Minute))
	dec, err := setup.Evaluate(f.ctx, f.plan.ID)
	if err != nil || dec == nil || dec.Status != contracts.AutoSwitchObserving {
		t.Fatalf("setup switch: decision=%+v err=%v", dec, err)
	}
	seedSnapshot(t, f.st, "ch-backup", 0.30, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-primary", 0.99, contracts.HealthHealthy)
	f.clk.add(20 * time.Minute)

	gate := &racingObserveStore{
		Store: f.st, decisionID: dec.ID, readsReady: make(chan struct{}),
		claimed: make(chan struct{}), release: make(chan struct{}),
	}
	var events atomic.Int32
	o := New(gate, f.eng, WithClock(f.clk.now), WithEventSink(func(context.Context, contracts.AutoSwitchDecision) {
		events.Add(1)
	}))
	results := make(chan evalResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			d, observeErr := o.Observe(f.ctx, dec.ID)
			results <- evalResult{decision: d, err: observeErr}
		}()
	}
	select {
	case <-gate.claimed:
	case <-time.After(2 * time.Second):
		t.Fatal("no observer acquired the applying claim")
	}

	var loser evalResult
	select {
	case loser = <-results:
	case <-time.After(2 * time.Second):
		close(gate.release)
		t.Fatal("losing observer did not return while winner held the claim")
	}
	if loser.err != nil || loser.decision == nil || loser.decision.Status != contracts.AutoSwitchApplying {
		close(gate.release)
		t.Fatalf("losing observer = %+v, want applying owner", loser)
	}
	close(gate.release)
	winner := <-results
	if winner.err != nil || winner.decision == nil || winner.decision.Status != contracts.AutoSwitchRolledBack {
		t.Fatalf("winning observer = %+v, want rolled_back", winner)
	}
	if events.Load() != 1 {
		t.Fatalf("observe events=%d, want 1", events.Load())
	}
	runs, err := f.st.ListReconcileRuns(f.ctx, f.plan.ID, 0)
	if err != nil {
		t.Fatalf("list reconcile runs: %v", err)
	}
	var applies int
	for _, run := range runs {
		if run.Kind == contracts.ReconcileRunApply {
			applies++
		}
	}
	if applies != 2 { // initial switch plus exactly one rollback
		t.Fatalf("apply runs=%d, want initial switch + one rollback", applies)
	}
}

func TestObserveCancellationAfterClaimStillFinalizes(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)

	setup := New(f.st, f.eng, WithClock(f.clk.now), WithObservationWindow(15*time.Minute))
	dec, err := setup.Evaluate(f.ctx, f.plan.ID)
	if err != nil || dec == nil || dec.Status != contracts.AutoSwitchObserving {
		t.Fatalf("setup switch: decision=%+v err=%v", dec, err)
	}
	f.clk.add(20 * time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	gate := &cancelAfterObservationClaimStore{Store: f.st, cancel: cancel}
	o := New(gate, f.eng, WithClock(f.clk.now))
	got, err := o.Observe(ctx, dec.ID)
	if err != nil {
		t.Fatalf("observe after request cancellation: %v", err)
	}
	if got.Status != contracts.AutoSwitchCompleted {
		t.Fatalf("status=%q after cancellation, want completed", got.Status)
	}
}

// TestEvaluateNoFailureReturnsNil: a healthy live channel produces no decision.
func TestEvaluateNoFailureReturnsNil(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.99, contracts.HealthHealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)

	o := New(f.st, f.eng, WithClock(f.clk.now))
	dec, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if dec != nil {
		t.Fatalf("expected no decision for a healthy plan, got %+v", dec)
	}
}

func TestQualityEjectionCohortNeverSelectsEveryPlanForSoftFailure(t *testing.T) {
	for _, windows := range []int{1, 2, 3, 10} {
		percentage := strategy.QualityEjectionPercentage(windows)
		if percentage >= 100 {
			t.Fatalf("soft streak %d selected %d%%; soft quality must never drain every downstream in one sweep", windows, percentage)
		}
		selected := 0
		for i := 0; i < 1000; i++ {
			if strategy.InStableQualityCohort(fmt.Sprintf("plan-%d", i), "ch-a", percentage) {
				selected++
			}
		}
		if selected == 0 || selected == 1000 {
			t.Fatalf("streak %d selected %d/1000 plans; expected a stable partial cohort", windows, selected)
		}
	}
}

func TestSourceQualityCohortAggregatesDifferentKeysAcrossPlansAndPools(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{{
		id: "ch-primary", sourceID: "source-shared", priority: 1,
		status: contracts.UpstreamChannelActive, remoteID: "acc-primary", live: true, onGateway: true, schedulable: true,
	}})
	user, err := f.st.CreateUser(f.ctx, contracts.User{Email: "cohort-2@local.dev", DisplayName: "Cohort 2", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := f.st.CreateInstance(f.ctx, contracts.Instance{UserID: user.ID, Name: "gw-2", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := f.st.CreateUpstreamPool(f.ctx, contracts.UpstreamPool{Name: "pool-2", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.CreateUpstreamChannel(f.ctx, contracts.UpstreamChannel{
		ID: "ch-other-key", PoolID: pool.ID, SourceID: "source-shared", DisplayName: "other", Status: contracts.UpstreamChannelActive,
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := f.st.CreateRoutePlan(f.ctx, contracts.RoutePlan{
		UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID, Status: contracts.RoutePlanPublished,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.UpsertPublishedBinding(f.ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: instance.ID, ChannelID: "ch-other-key", RemoteID: "acc-other", State: contracts.BindingActive,
	}); err != nil {
		t.Fatal(err)
	}
	o := New(f.st, f.eng, WithClock(f.clk.now))
	seedSnapshotForInstance(t, f.st, "ch-primary", f.plan.InstanceID, f.clk.now(), .4, contracts.HealthUnhealthy)
	seedSnapshotForInstance(t, f.st, "ch-other-key", instance.ID, f.clk.now(), .4, contracts.HealthUnhealthy)
	selected, known := o.sourceQualityCohort(f.ctx, "source-shared", 75)
	if !known || len(selected) != 1 || selected[f.plan.ID] == selected[plan.ID] {
		t.Fatalf("cross-key affected source cohort=%v known=%v", selected, known)
	}
}

func TestNoHealthyBackupFailsClosedEvenOutsideSoftQualityCohort(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{{
		id: "ch-primary", sourceID: "source-shared", priority: 1,
		status: contracts.UpstreamChannelActive, remoteID: "acc-primary", live: true, onGateway: true, schedulable: true,
	}})
	user, err := f.st.CreateUser(f.ctx, contracts.User{
		Email: "fail-closed-cohort-2@local.dev", DisplayName: "Fail Closed 2",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := f.st.CreateInstance(f.ctx, contracts.Instance{UserID: user.ID, Name: "gw-2", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := f.st.CreateUpstreamPool(f.ctx, contracts.UpstreamPool{Name: "pool-2", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := f.st.CreateUpstreamChannel(f.ctx, contracts.UpstreamChannel{
		ID: "ch-other-key", PoolID: pool.ID, SourceID: "source-shared", DisplayName: "other",
		Status: contracts.UpstreamChannelActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.st.CreateRoutePlan(f.ctx, contracts.RoutePlan{
		UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID, Status: contracts.RoutePlanPublished,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.UpsertPublishedBinding(f.ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: instance.ID, ChannelID: channel.ID,
		RemoteID: "acc-other", State: contracts.BindingActive,
	}); err != nil {
		t.Fatal(err)
	}
	f.gw.accounts = append(f.gw.accounts, contracts.GatewayAccount{ID: "acc-other", Schedulable: true})

	seedBad := func(channelID, instanceID string) {
		t.Helper()
		if _, err := f.st.UpsertChannelHealthSnapshot(f.ctx, contracts.ChannelHealthSnapshot{
			ChannelID: channelID, InstanceID: instanceID, Window: contracts.Window5m,
			BucketStart: f.clk.now(), CreatedAt: f.clk.now(),
			SampleCount: 20, SuccessRate: .4, ErrorRate: .6,
			QualitySampleCount: 20, QualitySuccessRate: .4, QualityErrorRate: .6,
			UpstreamErrorRate: .6, TTFTP95: 600, DurationP95: 3000,
			HealthState: contracts.HealthUnhealthy,
		}); err != nil {
			t.Fatalf("seed bad snapshot: %v", err)
		}
	}
	seedBad("ch-primary", f.plan.InstanceID)
	seedBad(channel.ID, instance.ID)

	o := New(f.st, f.eng, WithClock(f.clk.now))
	selected, known := o.sourceQualityCohort(f.ctx, "source-shared", 25)
	if !known || len(selected) != 1 {
		t.Fatalf("test needs one selected plan and one holdout: selected=%v known=%v", selected, known)
	}
	targetPlan, targetAccount, targetChannel := f.plan, "acc-primary", "ch-primary"
	if selected[f.plan.ID] {
		targetPlan, targetAccount, targetChannel = plan, "acc-other", channel.ID
	}
	if selected[targetPlan.ID] {
		t.Fatalf("test target must be outside the soft cohort: target=%s selected=%v", targetPlan.ID, selected)
	}

	decision, err := o.Evaluate(f.ctx, targetPlan.ID)
	if err != nil {
		t.Fatalf("evaluate holdout without backup: %v", err)
	}
	if decision == nil || decision.Status != contracts.AutoSwitchCompleted || !decision.AutoApplied || decision.ToChannelID != "" {
		t.Fatalf("no-backup holdout did not fail closed: %+v", decision)
	}
	if f.gw.schedulable(targetAccount) {
		t.Fatalf("failed source %s remained schedulable without a healthy backup", targetAccount)
	}
	runtime, err := f.st.GetQualityCircuitRuntime(f.ctx, targetPlan.ID, targetChannel)
	if err != nil || runtime.State != contracts.QualityCircuitOpen {
		t.Fatalf("fail-closed circuit missing: runtime=%+v err=%v", runtime, err)
	}
}

func TestSourceQualityCohortCountsOnlyPublishedActiveBindings(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{{
		id: "ch-primary", sourceID: "source-shared", status: contracts.UpstreamChannelActive,
		remoteID: "acc-primary", live: true, onGateway: true, schedulable: true,
	}})
	for index, state := range []contracts.PublishedBindingState{contracts.BindingActive, contracts.BindingDisabled} {
		user, err := f.st.CreateUser(f.ctx, contracts.User{
			Email: fmt.Sprintf("inactive-cohort-%d@local.dev", index), DisplayName: "Inactive",
			Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		instance, _ := f.st.CreateInstance(f.ctx, contracts.Instance{UserID: user.ID, Name: fmt.Sprintf("gw-%d", index), Kind: contracts.InstanceKindNewAPI})
		planStatus := contracts.RoutePlanSuspended
		if state == contracts.BindingDisabled {
			planStatus = contracts.RoutePlanPublished
		}
		channel, err := f.st.CreateUpstreamChannel(f.ctx, contracts.UpstreamChannel{
			ID: fmt.Sprintf("ch-inactive-%d", index), PoolID: f.plan.PoolID,
			SourceID: "source-shared", DisplayName: "inactive key", Status: contracts.UpstreamChannelActive,
		})
		if err != nil {
			t.Fatal(err)
		}
		plan, _ := f.st.CreateRoutePlan(f.ctx, contracts.RoutePlan{
			UserID: user.ID, InstanceID: instance.ID, PoolID: f.plan.PoolID, Status: planStatus,
		})
		_, err = f.st.UpsertPublishedBinding(f.ctx, contracts.PublishedBinding{
			PlanID: plan.ID, InstanceID: instance.ID, ChannelID: channel.ID,
			RemoteID: fmt.Sprintf("inactive-%d", index), State: state,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	seedSnapshotForInstance(t, f.st, "ch-primary", f.plan.InstanceID, f.clk.now(), .4, contracts.HealthUnhealthy)
	selected, known := New(f.st, f.eng, WithClock(f.clk.now)).sourceQualityCohort(f.ctx, "source-shared", 25)
	if !known || len(selected) != 1 || !selected[f.plan.ID] {
		t.Fatalf("inactive plans became false holdouts: selected=%v known=%v", selected, known)
	}
}

func TestSourceQualityCohortIncludesDecisionBackedCircuitCrashWindow(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{{
		id: "ch-primary", sourceID: "source-shared", status: contracts.UpstreamChannelActive,
		remoteID: "acc-primary", live: true, onGateway: true, schedulable: true,
	}})
	otherUser, err := f.st.CreateUser(f.ctx, contracts.User{
		Email: "circuit-crash-holdout@local.dev", DisplayName: "Holdout",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherInstance, _ := f.st.CreateInstance(f.ctx, contracts.Instance{UserID: otherUser.ID, Name: "holdout", Kind: contracts.InstanceKindNewAPI})
	otherPool, _ := f.st.CreateUpstreamPool(f.ctx, contracts.UpstreamPool{Name: "holdout", Status: contracts.UpstreamPoolActive})
	otherChannel, _ := f.st.CreateUpstreamChannel(f.ctx, contracts.UpstreamChannel{
		ID: "ch-holdout", PoolID: otherPool.ID, SourceID: "source-shared", DisplayName: "holdout", Status: contracts.UpstreamChannelActive,
	})
	otherPlan, _ := f.st.CreateRoutePlan(f.ctx, contracts.RoutePlan{
		UserID: otherUser.ID, InstanceID: otherInstance.ID, PoolID: otherPool.ID, Status: contracts.RoutePlanPublished,
	})
	if _, err := f.st.UpsertPublishedBinding(f.ctx, contracts.PublishedBinding{
		PlanID: otherPlan.ID, InstanceID: otherInstance.ID, ChannelID: otherChannel.ID,
		RemoteID: "acc-holdout", State: contracts.BindingActive,
	}); err != nil {
		t.Fatal(err)
	}

	// Model a crash after gateway/binding disable and durable decision update,
	// but before the quality circuit row was written.
	if _, err := f.st.UpsertPublishedBinding(f.ctx, contracts.PublishedBinding{
		PlanID: f.plan.ID, InstanceID: f.plan.InstanceID, ChannelID: "ch-primary",
		RemoteID: "acc-primary", State: contracts.BindingDisabled,
	}); err != nil {
		t.Fatal(err)
	}
	appliedAt := f.clk.now()
	if _, err := f.st.CreateAutoSwitchDecision(f.ctx, contracts.AutoSwitchDecision{
		PlanID: f.plan.ID, UserID: f.plan.UserID, InstanceID: f.plan.InstanceID, PoolID: f.plan.PoolID,
		FromChannelID: "ch-primary", Status: contracts.AutoSwitchCompleted,
		AutoApplied: true, AppliedAt: &appliedAt, CreatedAt: appliedAt, ResolvedAt: &appliedAt,
	}); err != nil {
		t.Fatal(err)
	}
	selected, known := New(f.st, f.eng, WithClock(f.clk.now)).sourceQualityCohort(f.ctx, "source-shared", 25)
	if !known || len(selected) != 1 || !selected[f.plan.ID] || selected[otherPlan.ID] {
		t.Fatalf("decision-backed circuit crash rotated cohort: selected=%v known=%v", selected, known)
	}
}

func TestSoftFailureKeepsObservationGroupWhenGlobalCohortUnavailable(t *testing.T) {
	f := seedFixture(t, 2, []chanSeed{
		{id: "ch-primary", sourceID: "source-shared", priority: 1, status: contracts.UpstreamChannelActive, remoteID: "acc-primary", live: true, onGateway: true, schedulable: true},
		{id: "ch-backup", sourceID: "source-backup", priority: 2, status: contracts.UpstreamChannelActive, remoteID: "acc-backup", onGateway: true},
	})
	seedSnapshot(t, f.st, "ch-primary", .4, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", .99, contracts.HealthHealthy)
	wrapped := &failGlobalCohortStore{Store: f.st}
	o := New(wrapped, f.eng, WithClock(f.clk.now))
	decision, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("evaluate with unavailable cohort: %v", err)
	}
	if decision == nil || decision.Status != contracts.AutoSwitchSkipped || decision.AutoApplied {
		t.Fatalf("unknown cohort must fail safe without a soft switch: %+v", decision)
	}
	if !f.gw.schedulable("acc-primary") || f.gw.schedulable("acc-backup") {
		t.Fatal("unknown global cohort changed gateway scheduling")
	}
}

func TestAffectedSourceMemberIsSelectedWhenHealthyMemberHashesFirst(t *testing.T) {
	f := seedFixture(t, 2, []chanSeed{
		{id: "bad-key", sourceID: "source-shared", priority: 1, status: contracts.UpstreamChannelActive, remoteID: "acc-bad", live: true, onGateway: true, schedulable: true},
		{id: "bad-backup", sourceID: "source-backup", priority: 2, status: contracts.UpstreamChannelActive, remoteID: "acc-bad-backup", onGateway: true},
	})
	user, err := f.st.CreateUser(f.ctx, contracts.User{
		Email: "healthy-source-member@local.dev", DisplayName: "Healthy Observer",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, _ := f.st.CreateInstance(f.ctx, contracts.Instance{UserID: user.ID, Name: "healthy", Kind: contracts.InstanceKindNewAPI})
	pool, _ := f.st.CreateUpstreamPool(f.ctx, contracts.UpstreamPool{Name: "healthy", Status: contracts.UpstreamPoolActive})
	for _, channel := range []contracts.UpstreamChannel{
		{ID: "healthy-key", PoolID: pool.ID, SourceID: "source-shared", DisplayName: "healthy-key", Priority: 1, Status: contracts.UpstreamChannelActive},
		{ID: "healthy-backup", PoolID: pool.ID, SourceID: "healthy-backup-source", DisplayName: "healthy-backup", Priority: 2, Status: contracts.UpstreamChannelActive},
	} {
		if _, err := f.st.CreateUpstreamChannel(f.ctx, channel); err != nil {
			t.Fatal(err)
		}
	}
	plan, _ := f.st.CreateRoutePlan(f.ctx, contracts.RoutePlan{
		UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID, Status: contracts.RoutePlanPublished, MaxChannels: 2,
	})
	for _, binding := range []contracts.PublishedBinding{
		{PlanID: plan.ID, InstanceID: instance.ID, ChannelID: "healthy-key", RemoteID: "acc-healthy", State: contracts.BindingActive},
		{PlanID: plan.ID, InstanceID: instance.ID, ChannelID: "healthy-backup", RemoteID: "acc-healthy-backup", State: contracts.BindingDisabled},
	} {
		if _, err := f.st.UpsertPublishedBinding(f.ctx, binding); err != nil {
			t.Fatal(err)
		}
	}
	f.gw.accounts = append(f.gw.accounts,
		contracts.GatewayAccount{ID: "acc-healthy", Schedulable: true},
		contracts.GatewayAccount{ID: "acc-healthy-backup", Schedulable: false},
	)
	seedSnapshotForInstance(t, f.st, "bad-key", f.plan.InstanceID, f.clk.now(), .4, contracts.HealthUnhealthy)
	seedSnapshotForInstance(t, f.st, "bad-backup", f.plan.InstanceID, f.clk.now(), .99, contracts.HealthHealthy)
	seedSnapshotForInstance(t, f.st, "healthy-key", instance.ID, f.clk.now(), .99, contracts.HealthHealthy)
	seedSnapshotForInstance(t, f.st, "healthy-backup", instance.ID, f.clk.now(), .99, contracts.HealthHealthy)

	// The legacy all-active algorithm could choose either plan. The affected-only
	// cohort must choose the actually-bad plan regardless of relative hash order.
	o := New(f.st, f.eng, WithClock(f.clk.now))
	selected, known := o.SourceQualityCohort(f.ctx, "source-shared", 25)
	if !known || len(selected) != 1 || !selected[f.plan.ID] || selected[plan.ID] {
		t.Fatalf("healthy source member consumed the incident slot: selected=%v known=%v", selected, known)
	}
	decision, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("evaluate affected member: %v", err)
	}
	if decision == nil || decision.Status != contracts.AutoSwitchObserving || !decision.AutoApplied {
		t.Fatalf("affected member was not switched: %+v", decision)
	}
	if f.gw.schedulable("acc-bad") || !f.gw.schedulable("acc-bad-backup") || !f.gw.schedulable("acc-healthy") {
		t.Fatalf("unexpected gateway scheduling after affected switch: %+v", f.gw.accounts)
	}
}

func TestSourceQualityCohortDoesNotRotateAcrossRepeatedSweeps(t *testing.T) {
	f := seedFixture(t, 2, []chanSeed{
		{id: "key-0", sourceID: "source-shared", priority: 1, status: contracts.UpstreamChannelActive, remoteID: "acc-key-0", live: true, onGateway: true, schedulable: true},
		{id: "backup-0", sourceID: "backup-source", priority: 2, status: contracts.UpstreamChannelActive, remoteID: "acc-backup-0", onGateway: true},
	})
	type member struct {
		plan      contracts.RoutePlan
		channelID string
		backupID  string
	}
	members := []member{{plan: f.plan, channelID: "key-0", backupID: "backup-0"}}
	for index := 1; index < 4; index++ {
		user, err := f.st.CreateUser(f.ctx, contracts.User{
			Email: fmt.Sprintf("stable-incident-%d@local.dev", index), DisplayName: "Stable Incident",
			Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		instance, err := f.st.CreateInstance(f.ctx, contracts.Instance{UserID: user.ID, Name: fmt.Sprintf("gw-%d", index), Kind: contracts.InstanceKindNewAPI})
		if err != nil {
			t.Fatal(err)
		}
		pool, err := f.st.CreateUpstreamPool(f.ctx, contracts.UpstreamPool{Name: fmt.Sprintf("pool-%d", index), Status: contracts.UpstreamPoolActive})
		if err != nil {
			t.Fatal(err)
		}
		channelID := fmt.Sprintf("key-%d", index)
		backupID := fmt.Sprintf("backup-%d", index)
		for _, channel := range []contracts.UpstreamChannel{
			{ID: channelID, PoolID: pool.ID, SourceID: "source-shared", DisplayName: channelID, Priority: 1, Status: contracts.UpstreamChannelActive},
			{ID: backupID, PoolID: pool.ID, SourceID: fmt.Sprintf("backup-source-%d", index), DisplayName: backupID, Priority: 2, Status: contracts.UpstreamChannelActive},
		} {
			if _, err := f.st.CreateUpstreamChannel(f.ctx, channel); err != nil {
				t.Fatal(err)
			}
		}
		plan, err := f.st.CreateRoutePlan(f.ctx, contracts.RoutePlan{
			UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID, Status: contracts.RoutePlanPublished, MaxChannels: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, binding := range []contracts.PublishedBinding{
			{PlanID: plan.ID, InstanceID: instance.ID, ChannelID: channelID, RemoteID: "acc-" + channelID, State: contracts.BindingActive},
			{PlanID: plan.ID, InstanceID: instance.ID, ChannelID: backupID, RemoteID: "acc-" + backupID, State: contracts.BindingDisabled},
		} {
			if _, err := f.st.UpsertPublishedBinding(f.ctx, binding); err != nil {
				t.Fatal(err)
			}
		}
		f.gw.accounts = append(f.gw.accounts,
			contracts.GatewayAccount{ID: "acc-" + channelID, Schedulable: true},
			contracts.GatewayAccount{ID: "acc-" + backupID, Schedulable: false},
		)
		members = append(members, member{plan: plan, channelID: channelID, backupID: backupID})
	}
	seedMemberSnapshot := func(member member, bucket time.Time, bad bool) {
		t.Helper()
		success := .99
		state := contracts.HealthHealthy
		if bad {
			success = .4
			state = contracts.HealthUnhealthy
		}
		for _, channelID := range []string{member.channelID, member.backupID} {
			channelSuccess, channelState := success, state
			if channelID == member.backupID {
				channelSuccess, channelState = .99, contracts.HealthHealthy
			}
			if _, err := f.st.UpsertChannelHealthSnapshot(f.ctx, contracts.ChannelHealthSnapshot{
				ChannelID: channelID, InstanceID: member.plan.InstanceID, Window: contracts.Window5m,
				BucketStart: bucket, CreatedAt: bucket, SampleCount: 20,
				SuccessRate: channelSuccess, ErrorRate: 1 - channelSuccess,
				QualitySampleCount: 20, QualitySuccessRate: channelSuccess, QualityErrorRate: 1 - channelSuccess,
				UpstreamErrorRate: 1 - channelSuccess, TTFTP95: 600, DurationP95: 3000,
				HealthState: channelState,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, member := range members {
		seedMemberSnapshot(member, f.clk.now(), true)
	}
	o := New(f.st, f.eng, WithClock(f.clk.now))
	initial, known := o.sourceQualityCohort(f.ctx, "source-shared", 25)
	if !known || len(initial) != 1 {
		t.Fatalf("initial incident cohort=%v known=%v", initial, known)
	}
	for _, member := range members {
		if _, err := o.Evaluate(f.ctx, member.plan.ID); err != nil {
			t.Fatalf("first sweep plan %s: %v", member.plan.ID, err)
		}
	}
	for sweep := 0; sweep < 3; sweep++ {
		selected, known := o.sourceQualityCohort(f.ctx, "source-shared", 25)
		if !known || !mapsEqual(selected, initial) {
			t.Fatalf("same bad window rotated on sweep %d: initial=%v selected=%v", sweep+2, initial, selected)
		}
		for _, member := range members {
			if _, err := o.Evaluate(f.ctx, member.plan.ID); err != nil {
				t.Fatalf("repeat sweep %d plan %s: %v", sweep+2, member.plan.ID, err)
			}
		}
	}
	active := 0
	for _, member := range members {
		bindings, err := f.st.ListPublishedBindings(f.ctx, member.plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, binding := range bindings {
			if binding.ChannelID == member.channelID && binding.State == contracts.BindingActive {
				active++
			}
		}
	}
	if active != 3 {
		t.Fatalf("same bad window left %d active source bindings, want three holdouts", active)
	}

	// A second independent bad window expands the same incident to 50%, rather
	// than replacing the first selected member.
	f.clk.add(5 * time.Minute)
	for _, member := range members {
		seedMemberSnapshot(member, f.clk.now(), true)
	}
	expanded, known := o.sourceQualityCohort(f.ctx, "source-shared", 50)
	if !known || len(expanded) != 2 {
		t.Fatalf("second-window cohort=%v known=%v", expanded, known)
	}
	for planID := range initial {
		if !expanded[planID] {
			t.Fatalf("second window replaced the first selected plan: initial=%v expanded=%v", initial, expanded)
		}
	}
}

func mapsEqual(left, right map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func TestHardBindingFailureBypassesQualityCohort(t *testing.T) {
	// The orchestrator's hard-failure branch does not call inStableCohort at
	// all; assert the soft cohort itself would exclude some plans so this test
	// would catch accidentally routing hard failures through it.
	excluded := false
	for i := 0; i < 100; i++ {
		if !strategy.InStableQualityCohort(fmt.Sprintf("plan-%d", i), "ch-a", 25) {
			excluded = true
			break
		}
	}
	if !excluded {
		t.Fatal("test precondition: expected soft cohort to exclude at least one plan")
	}
}

func TestDisabledEjectedBindingCannotReenterWithoutRecoveryProbes(t *testing.T) {
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	seedSnapshot(t, f.st, "ch-primary", 0.50, contracts.HealthUnhealthy)
	seedSnapshot(t, f.st, "ch-backup", 0.99, contracts.HealthHealthy)
	o := New(f.st, f.eng, WithClock(f.clk.now), WithObservationWindow(time.Minute), WithStrategy(contracts.RouteStrategy{
		Type: contracts.StrategyStabilityFirst, AutoApply: true, RecoveryObservationSeconds: 60,
	}))
	decision, err := o.Evaluate(f.ctx, f.plan.ID)
	if err != nil || decision == nil || decision.Status != contracts.AutoSwitchObserving {
		t.Fatalf("initial ejection: decision=%+v err=%v", decision, err)
	}
	f.clk.add(2 * time.Minute)
	completed, err := o.Observe(f.ctx, decision.ID)
	if err != nil || completed.Status != contracts.AutoSwitchCompleted {
		t.Fatalf("complete switch: decision=%+v err=%v", completed, err)
	}
	// No traffic makes the old source look sample-free/perfect. It must remain
	// quarantined and cannot displace the current healthy source.
	if next, err := o.Evaluate(f.ctx, f.plan.ID); err != nil || next != nil {
		t.Fatalf("disabled source reentered without probes: decision=%+v err=%v", next, err)
	}
	if f.gw.schedulable("acc-primary") {
		t.Fatal("old source was enabled without recovery probes")
	}
}
