package onboarding

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/keyproof"
	"e2m.local/core/internal/store"
)

type onboardingEvents struct {
	mu     sync.Mutex
	values []string
}

func (e *onboardingEvents) add(value string) {
	e.mu.Lock()
	e.values = append(e.values, value)
	e.mu.Unlock()
}

func (e *onboardingEvents) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.values...)
}

type onboardingGateway struct {
	events   *onboardingEvents
	accounts []contracts.GatewayAccount
	err      error
	calls    int
}

func (g *onboardingGateway) ListAccounts(context.Context, string) ([]contracts.GatewayAccount, error) {
	g.calls++
	if g.calls == 1 {
		g.events.add("gateway.preflight")
	} else {
		g.events.add("gateway.verify")
	}
	if g.err != nil {
		return nil, g.err
	}
	return append([]contracts.GatewayAccount(nil), g.accounts...), nil
}

type onboardingKeys struct {
	store              store.Store
	events             *onboardingEvents
	connectorID        string
	installErr         error
	installCalls       int
	verifyCalls        int
	deploymentRequired bool
}

func (k *onboardingKeys) InstallBinding(ctx context.Context, channelID, _ string) (contracts.ConnectorGatewayBindingInstallResult, error) {
	k.installCalls++
	k.events.add("key.install")
	if k.installErr != nil {
		return contracts.ConnectorGatewayBindingInstallResult{}, k.installErr
	}
	channel, err := k.store.GetUpstreamChannel(ctx, channelID)
	if err != nil {
		return contracts.ConnectorGatewayBindingInstallResult{}, err
	}
	delivery, err := k.store.GetUpstreamKeyDelivery(ctx, channelID)
	if err != nil {
		return contracts.ConnectorGatewayBindingInstallResult{}, err
	}
	return contracts.ConnectorGatewayBindingInstallResult{
		BindingID: channel.CredentialBindingID, ChannelID: channelID, KeyVersion: delivery.KeyVersion,
	}, nil
}

func (k *onboardingKeys) Verify(ctx context.Context, channelID, instanceID string) (keyproof.Verification, error) {
	k.verifyCalls++
	if k.verifyCalls == 1 {
		k.events.add("key.proof")
	} else {
		k.events.add("key.receipt")
	}
	delivery, err := k.store.GetUpstreamKeyDelivery(ctx, channelID)
	if err != nil {
		return keyproof.Verification{}, err
	}
	delivery.ProofStatus = contracts.DeliveryKeyProofVerified
	delivery.ProofConnectorID = k.connectorID
	return keyproof.Verification{
		Delivery: delivery,
		Proof: contracts.UpstreamKeyProofReceipt{
			ChannelID: channelID, InstanceID: instanceID, KeyVersion: delivery.KeyVersion,
			ConnectorID: k.connectorID, Status: contracts.DeliveryKeyProofVerified,
		},
		DeploymentRequired: k.deploymentRequired,
	}, nil
}

type onboardingPublisher struct {
	store              store.Store
	gateway            *onboardingGateway
	events             *onboardingEvents
	calls              int
	omitBindingReceipt bool
	hold               bool
}

func (p *onboardingPublisher) Apply(ctx context.Context, planID string) (contracts.ReconcilePlan, error) {
	p.calls++
	p.events.add("publish")
	actor, actorOK := contracts.ActorFromContext(ctx)
	trigger, triggerOK := contracts.ReconcileTriggerFromContext(ctx)
	if !actorOK || actor.Type != "system" || actor.ID != "e2m-onboarding" ||
		!triggerOK || trigger != contracts.ReconcileTriggerAuto {
		return contracts.ReconcilePlan{}, errors.New("publish context is not automatic system work")
	}
	if err := contracts.RunReconcileSideEffectGuard(ctx); err != nil {
		return contracts.ReconcilePlan{}, err
	}
	plan, err := p.store.ClaimRoutePlanScheduling(ctx, planID, contracts.RoutePlanDraft, contracts.RoutePlanPublished, contracts.RoutePlanSuspended)
	if err != nil {
		return contracts.ReconcilePlan{}, err
	}
	if plan.Status == contracts.RoutePlanDraft {
		plan, err = p.store.CompleteRoutePlanPublish(ctx, plan.ID, plan.SchedulingGeneration)
		if err != nil {
			return contracts.ReconcilePlan{}, err
		}
	}
	bindings, err := p.store.ListPublishedBindings(ctx, planID)
	if err != nil {
		return contracts.ReconcilePlan{}, err
	}
	if p.omitBindingReceipt {
		return contracts.ReconcilePlan{PlanID: planID, InstanceID: plan.InstanceID}, nil
	}
	for _, binding := range bindings {
		binding.RemoteID = "remote-" + binding.ChannelID
		binding.State = contracts.BindingActive
		binding.LastError = ""
		binding.SchedulingGeneration = plan.SchedulingGeneration
		if _, err := p.store.UpsertPublishedBinding(ctx, binding); err != nil {
			return contracts.ReconcilePlan{}, err
		}
		p.gateway.upsertAccount(contracts.GatewayAccount{ID: binding.RemoteID, Schedulable: true})
	}
	result := contracts.ReconcilePlan{PlanID: planID, InstanceID: plan.InstanceID}
	if p.hold {
		result.Actions = append(result.Actions, contracts.ReconcileAction{Type: contracts.ReconcileHold, ChannelID: "held-channel"})
	}
	return result, nil
}

func (g *onboardingGateway) upsertAccount(account contracts.GatewayAccount) {
	for i := range g.accounts {
		if g.accounts[i].ID == account.ID {
			g.accounts[i] = account
			return
		}
	}
	g.accounts = append(g.accounts, account)
}

type onboardingFixture struct {
	ctx       context.Context
	store     store.Store
	instance  contracts.Instance
	connector contracts.Connector
	pool      contracts.UpstreamPool
	channel   contracts.UpstreamChannel
	events    *onboardingEvents
	gateway   *onboardingGateway
	keys      *onboardingKeys
	publisher *onboardingPublisher
	runner    *Runner
}

func newOnboardingFixture(t *testing.T, runtime contracts.ConnectorRuntimeState, withDeliveryKey bool) *onboardingFixture {
	t.Helper()
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now().UTC())
	user, err := st.CreateUser(ctx, contracts.User{
		Email: "onboarding@example.test", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID, Name: "Gateway", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: "pool-a", Name: "Shared", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPoolRolloutTarget(ctx, contracts.PoolRolloutTarget{
		PoolID: pool.ID, Scope: contracts.PoolRolloutScopeInstance, UserID: user.ID,
		InstanceID: instance.ID, Enabled: true, Rollout: contracts.RolloutImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	channel, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		ID: "channel-a", PoolID: pool.ID, SourceID: "source-a", DisplayName: "Source A",
		CredentialBindingID: "binding-a", AccountOwnership: contracts.GatewayAccountPlatformManaged,
		Status: contracts.UpstreamChannelActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if withDeliveryKey {
		if _, err := st.UpsertUpstreamKeyDelivery(ctx, contracts.UpstreamKeyDelivery{
			ChannelID: channel.ID, SecretRef: "credential_ref:test/channel-a", MaskedValue: "********",
		}); err != nil {
			t.Fatal(err)
		}
	}
	enrollment, err := st.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{
		UserID: user.ID, InstanceID: instance.ID, ConnectorID: "connector-a",
		TokenHash: "enrollment-a", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	connector, _, err := st.UseConnectorEnrollment(ctx, enrollment.TokenHash, contracts.Connector{
		ID: enrollment.ConnectorID, InstanceID: instance.ID, Version: "0.1.0-test",
		ProtocolVersion: contracts.ConnectorProtocolVersion, TokenHash: "connector-token", Gateway: runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, err = st.GetInstance(ctx, instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	events := &onboardingEvents{}
	gateway := &onboardingGateway{events: events}
	keys := &onboardingKeys{store: st, events: events, connectorID: connector.ID}
	publisher := &onboardingPublisher{store: st, gateway: gateway, events: events}
	runner := New(st, gateway, keys, publisher, time.Hour)
	runner.batchLimit = 1
	return &onboardingFixture{
		ctx: ctx, store: st, instance: instance, connector: connector, pool: pool, channel: channel,
		events: events, gateway: gateway, keys: keys, publisher: publisher, runner: runner,
	}
}

func readyOnboardingRuntime(kind string) contracts.ConnectorRuntimeState {
	publicKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, contracts.ConnectorBindingEncryptionPublicKeySize))
	return contracts.ConnectorRuntimeState{
		ProtocolVersion: contracts.ConnectorProtocolVersion, GatewayConfigured: true,
		GatewayKind: kind, GatewayStatus: "ok", BindingEncryptionPublicKey: publicKey,
		Capabilities: []contracts.ConnectorTaskType{
			contracts.ConnectorTaskGatewayAccountsList,
			contracts.ConnectorTaskGatewayBindingInstall,
			contracts.ConnectorTaskGatewayBindingProof,
			contracts.ConnectorTaskGatewaySchedulingBarrier,
			contracts.ConnectorTaskGatewayAccountCreate,
			contracts.ConnectorTaskGatewayAccountUpdate,
		},
	}
}

func TestRunnerWaitsForConnectorReadiness(t *testing.T) {
	runtime := readyOnboardingRuntime("sub2api")
	runtime.GatewayStatus = "configured"
	fixture := newOnboardingFixture(t, runtime, true)
	fixture.runner.RunOnce(fixture.ctx)
	workflow := onlyOnboardingWorkflow(t, fixture)
	if workflow.Stage != contracts.OnboardingWaitingConnector || workflow.Status != contracts.OnboardingPending ||
		workflow.LastErrorCode != "connector_gateway_not_ready" || workflow.NextAttemptAt == nil {
		t.Fatalf("workflow = %+v", workflow)
	}
	assertNoOnboardingSideEffects(t, fixture)
	assertOnboardingAudits(t, fixture,
		onboardingAuditWant{action: auditActionOnboardingProgress, result: "running"},
		onboardingAuditWant{action: auditActionOnboardingRetryScheduled, result: "retrying", errorCode: "connector_gateway_not_ready"},
	)
}

func TestRunnerRejectsWrongGatewayKindBeforePreflight(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("newapi"), true)
	fixture.runner.RunOnce(fixture.ctx)
	workflow := onlyOnboardingWorkflow(t, fixture)
	if workflow.Stage != contracts.OnboardingWaitingConnector || workflow.Status != contracts.OnboardingPending ||
		workflow.LastErrorCode != "connector_gateway_not_ready" {
		t.Fatalf("workflow = %+v", workflow)
	}
	assertNoOnboardingSideEffects(t, fixture)
}

func TestRunnerOrdersInstallProofPublishAndRequiresActiveReceipts(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	fixture.runner.RunOnce(fixture.ctx)
	workflow := onlyOnboardingWorkflow(t, fixture)
	if workflow.Stage != contracts.OnboardingActive || workflow.Status != contracts.OnboardingReady ||
		workflow.KeyVersionSummary[fixture.channel.ID] != 1 {
		t.Fatalf("workflow = %+v", workflow)
	}
	want := []string{"gateway.preflight", "key.install", "key.proof", "publish", "gateway.verify", "key.receipt"}
	if got := fixture.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if fixture.publisher.calls != 1 || fixture.keys.installCalls != 1 || fixture.keys.verifyCalls != 2 {
		t.Fatalf("calls: publish=%d install=%d verify=%d", fixture.publisher.calls, fixture.keys.installCalls, fixture.keys.verifyCalls)
	}
	assertOnboardingAudits(t, fixture,
		onboardingAuditWant{action: auditActionOnboardingProgress, result: "running"},
		onboardingAuditWant{action: auditActionOnboardingCompleted, result: "accepted"},
	)
}

func TestRunnerMissingDeliveryKeyIsRetryableAndNeverPublishes(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), false)
	fixture.runner.RunOnce(fixture.ctx)
	workflow := onlyOnboardingWorkflow(t, fixture)
	if workflow.Stage != contracts.OnboardingFailedRetryable || workflow.Status != contracts.OnboardingRetryable ||
		workflow.LastErrorCode != "binding_delivery_failed" || workflow.NextAttemptAt == nil {
		t.Fatalf("workflow = %+v", workflow)
	}
	if fixture.keys.installCalls != 1 || fixture.keys.verifyCalls != 0 || fixture.publisher.calls != 0 {
		t.Fatalf("calls: install=%d verify=%d publish=%d", fixture.keys.installCalls, fixture.keys.verifyCalls, fixture.publisher.calls)
	}
	assertOnboardingAudits(t, fixture,
		onboardingAuditWant{action: auditActionOnboardingProgress, result: "running"},
		onboardingAuditWant{action: auditActionOnboardingRetryScheduled, result: "retrying", errorCode: "binding_delivery_failed"},
	)
}

func TestRunnerDoesNotBecomeActiveWithoutDeploymentReceipt(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	fixture.keys.deploymentRequired = true
	fixture.runner.RunOnce(fixture.ctx)
	workflow := onlyOnboardingWorkflow(t, fixture)
	if workflow.Stage != contracts.OnboardingFailedRetryable || workflow.Status != contracts.OnboardingRetryable ||
		workflow.LastErrorCode != "delivery_receipt_unverified" || workflow.NextAttemptAt == nil {
		t.Fatalf("workflow = %+v", workflow)
	}
	if fixture.publisher.calls != 1 || fixture.keys.verifyCalls != 2 {
		t.Fatalf("calls: publish=%d verify=%d", fixture.publisher.calls, fixture.keys.verifyCalls)
	}
	plan, err := fixture.store.GetRoutePlan(fixture.ctx, workflow.PlanID)
	if err != nil || plan.Status != contracts.RoutePlanPublished {
		t.Fatalf("published plan = %+v, %v", plan, err)
	}
}

func TestRunnerDoesNotBecomeActiveFromPublishSuccessWithoutBindingReceipt(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	fixture.publisher.omitBindingReceipt = true
	fixture.runner.RunOnce(fixture.ctx)
	workflow := onlyOnboardingWorkflow(t, fixture)
	if workflow.Stage != contracts.OnboardingFailedRetryable || workflow.Status != contracts.OnboardingRetryable ||
		workflow.LastErrorCode != "gateway_binding_not_active" || workflow.NextAttemptAt == nil {
		t.Fatalf("workflow = %+v", workflow)
	}
	if fixture.publisher.calls != 1 || fixture.keys.verifyCalls != 1 {
		t.Fatalf("calls: publish=%d verify=%d", fixture.publisher.calls, fixture.keys.verifyCalls)
	}
	bindings, err := fixture.store.ListPublishedBindings(fixture.ctx, workflow.PlanID)
	if err != nil || len(bindings) != 1 || bindings[0].State != contracts.BindingPending {
		t.Fatalf("binding receipts=%+v err=%v", bindings, err)
	}
}

func TestRunnerKeyRotationResetsActiveWorkflowAndRedelivers(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	fixture.runner.RunOnce(fixture.ctx)
	first := onlyOnboardingWorkflow(t, fixture)
	if first.Status != contracts.OnboardingReady || first.KeyVersionSummary[fixture.channel.ID] != 1 {
		t.Fatalf("first workflow=%+v", first)
	}
	if _, err := fixture.store.UpsertUpstreamKeyDelivery(fixture.ctx, contracts.UpstreamKeyDelivery{
		ChannelID: fixture.channel.ID, SecretRef: "credential_ref:test/channel-a-v2", MaskedValue: "********v2",
	}); err != nil {
		t.Fatal(err)
	}

	fixture.runner.RunOnce(fixture.ctx)
	rotated := onlyOnboardingWorkflow(t, fixture)
	if rotated.Status != contracts.OnboardingReady || rotated.Stage != contracts.OnboardingActive ||
		rotated.KeyVersionSummary[fixture.channel.ID] != 2 ||
		rotated.DesiredGeneration != first.DesiredGeneration+1 {
		t.Fatalf("rotated workflow=%+v", rotated)
	}
	if fixture.keys.installCalls != 2 || fixture.publisher.calls != 2 {
		t.Fatalf("rotation calls install=%d publish=%d", fixture.keys.installCalls, fixture.publisher.calls)
	}
}

func TestRunnerNewSourceResetsActiveWorkflowAndPublishesIt(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	fixture.runner.RunOnce(fixture.ctx)
	first := onlyOnboardingWorkflow(t, fixture)
	second, err := fixture.store.CreateUpstreamChannel(fixture.ctx, contracts.UpstreamChannel{
		ID: "channel-b", PoolID: fixture.pool.ID, SourceID: "source-b", DisplayName: "Source B",
		CredentialBindingID: "binding-b", AccountOwnership: contracts.GatewayAccountPlatformManaged,
		Status: contracts.UpstreamChannelActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.UpsertUpstreamKeyDelivery(fixture.ctx, contracts.UpstreamKeyDelivery{
		ChannelID: second.ID, SecretRef: "credential_ref:test/channel-b", MaskedValue: "********",
	}); err != nil {
		t.Fatal(err)
	}

	fixture.runner.RunOnce(fixture.ctx)
	updated := onlyOnboardingWorkflow(t, fixture)
	if updated.Status != contracts.OnboardingReady || updated.KeyVersionSummary[second.ID] != 1 ||
		updated.DesiredGeneration != first.DesiredGeneration+1 {
		t.Fatalf("new source workflow=%+v", updated)
	}
	bindings, err := fixture.store.ListPublishedBindings(fixture.ctx, updated.PlanID)
	if err != nil || len(bindings) != 2 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
}

func TestRunnerHealthyActiveRecheckDoesNotRepublish(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	fixture.runner.activeCheck = 40 * time.Millisecond
	fixture.runner.RunOnce(fixture.ctx)
	first := onlyOnboardingWorkflow(t, fixture)
	if first.Status != contracts.OnboardingReady || first.NextAttemptAt == nil {
		t.Fatalf("first workflow=%+v", first)
	}
	time.Sleep(80 * time.Millisecond)

	fixture.runner.RunOnce(fixture.ctx)
	rechecked := onlyOnboardingWorkflow(t, fixture)
	if rechecked.Status != contracts.OnboardingReady || rechecked.NextAttemptAt == nil ||
		!rechecked.NextAttemptAt.After(*first.NextAttemptAt) {
		t.Fatalf("rechecked workflow=%+v", rechecked)
	}
	if fixture.publisher.calls != 1 || fixture.keys.installCalls != 1 {
		t.Fatalf("healthy recheck republished: publish=%d install=%d", fixture.publisher.calls, fixture.keys.installCalls)
	}
	assertOnboardingAudits(t, fixture,
		onboardingAuditWant{action: auditActionOnboardingProgress, result: "running"},
		onboardingAuditWant{action: auditActionOnboardingCompleted, result: "accepted"},
		onboardingAuditWant{action: auditActionOnboardingVerified, result: "verified"},
	)
}

func TestRunnerRechecksUserEligibilityBeforeAnySideEffect(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	if err := fixture.runner.discover(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	user, err := fixture.store.GetUser(fixture.ctx, fixture.instance.UserID)
	if err != nil {
		t.Fatal(err)
	}
	user.Enabled = false
	if _, err := fixture.store.UpdateUser(fixture.ctx, user); err != nil {
		t.Fatal(err)
	}

	fixture.runner.RunOnce(fixture.ctx)
	workflow := onlyOnboardingWorkflow(t, fixture)
	if workflow.Status != contracts.OnboardingRetryable || workflow.LastErrorCode != "user_ineligible" {
		t.Fatalf("disabled user workflow=%+v", workflow)
	}
	assertNoOnboardingSideEffects(t, fixture)
	assertOnboardingAudits(t, fixture, onboardingAuditWant{
		action: auditActionOnboardingRetryScheduled, result: "retrying", errorCode: "user_ineligible",
	})
}

func TestRunnerDormantsInactivePoolWithoutRetryNoise(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	// Discover while active, then retire before the pending workflow is claimed.
	// This also proves eligibility is rechecked under the claimed lease.
	if err := fixture.runner.discover(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := fixture.store.GetUpstreamPool(fixture.ctx, fixture.pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Maintenance is the reversible inactive state. Retirement now requires the
	// explicit durable retirement workflow and cannot be toggled directly.
	pool.Status = contracts.UpstreamPoolMaintenance
	if _, err := fixture.store.UpdateUpstreamPool(fixture.ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture.runner.RunOnce(fixture.ctx)
	dormant := onlyOnboardingWorkflow(t, fixture)
	if dormant.Status != contracts.OnboardingDormantStatus || dormant.Stage != contracts.OnboardingDormant ||
		dormant.LastErrorCode != "pool_inactive" || dormant.NextAttemptAt != nil {
		t.Fatalf("dormant workflow=%+v", dormant)
	}
	if _, ok, err := fixture.store.ClaimOnboardingWorkflow(fixture.ctx, "noise-check", time.Minute); err != nil || ok {
		t.Fatalf("dormant claim ok=%v err=%v", ok, err)
	}
	assertNoOnboardingSideEffects(t, fixture)
	assertOnboardingAudits(t, fixture, onboardingAuditWant{
		action: auditActionOnboardingPaused, result: "paused", errorCode: "pool_inactive",
	})
	// Re-enable the pool. Discovery's active-pool upsert must wake the exact
	// same row even though its desired fingerprint is unchanged.
	pool.Status = contracts.UpstreamPoolActive
	if _, err := fixture.store.UpdateUpstreamPool(fixture.ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := fixture.runner.discover(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	woken := onlyOnboardingWorkflow(t, fixture)
	if woken.Status != contracts.OnboardingPending || woken.Stage != contracts.OnboardingCheckingGateway ||
		woken.DesiredGeneration <= dormant.DesiredGeneration {
		t.Fatalf("woken workflow=%+v", woken)
	}
}

type interruptTransitionStore struct {
	store.Store
	mu        sync.Mutex
	failStage contracts.OnboardingStage
	failed    bool
}

func (s *interruptTransitionStore) TransitionOnboardingWorkflow(ctx context.Context, input contracts.OnboardingWorkflow, expectedVersion int64) (contracts.OnboardingWorkflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.failed && input.Stage == s.failStage {
		s.failed = true
		return contracts.OnboardingWorkflow{}, errors.New("simulated process interruption")
	}
	return s.Store.TransitionOnboardingWorkflow(ctx, input, expectedVersion)
}

type onboardingLeaseStore struct {
	store.Store
	mu       sync.Mutex
	renewals []int64
	releases []int64
}

func (s *onboardingLeaseStore) RenewOnboardingWorkflowLease(
	ctx context.Context,
	id, workerID string,
	expectedVersion int64,
	leaseDuration time.Duration,
) (contracts.OnboardingWorkflow, error) {
	renewed, err := s.Store.RenewOnboardingWorkflowLease(ctx, id, workerID, expectedVersion, leaseDuration)
	if err == nil {
		s.mu.Lock()
		s.renewals = append(s.renewals, renewed.Version)
		s.mu.Unlock()
	}
	return renewed, err
}

func (s *onboardingLeaseStore) ReleaseOnboardingWorkflowLease(
	ctx context.Context,
	id, workerID string,
	expectedVersion int64,
) error {
	err := s.Store.ReleaseOnboardingWorkflowLease(ctx, id, workerID, expectedVersion)
	if err == nil {
		s.mu.Lock()
		s.releases = append(s.releases, expectedVersion)
		s.mu.Unlock()
	}
	return err
}

func (s *onboardingLeaseStore) snapshot() ([]int64, []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.renewals...), append([]int64(nil), s.releases...)
}

type blockingOnboardingGateway struct {
	started chan struct{}
	once    sync.Once
}

func (g *blockingOnboardingGateway) ListAccounts(ctx context.Context, _ string) ([]contracts.GatewayAccount, error) {
	g.once.Do(func() { close(g.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRunnerRenewsLeaseBeforeEveryExternalStep(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	tracked := &onboardingLeaseStore{Store: fixture.store}
	fixture.runner.store = tracked
	fixture.keys.store = tracked
	fixture.publisher.store = tracked
	fixture.runner.RunOnce(fixture.ctx)

	workflow := onlyOnboardingWorkflow(t, fixture)
	if workflow.Status != contracts.OnboardingReady {
		t.Fatalf("workflow=%+v", workflow)
	}
	renewals, releases := tracked.snapshot()
	// Preflight, install, proof, publish entry, publish mutation guard, final
	// account list, and final proof each fence ownership independently.
	if len(renewals) != 7 {
		t.Fatalf("lease renewals=%v, want 7 guarded external steps", renewals)
	}
	for i := 1; i < len(renewals); i++ {
		if renewals[i] <= renewals[i-1] {
			t.Fatalf("renewal versions are not strictly increasing: %v", renewals)
		}
	}
	if len(releases) != 0 {
		t.Fatalf("completed workflow unexpectedly released a lease: %v", releases)
	}
}

func TestRunnerRestartWaitsForLiveLeaseThenReclaimsExpiredLease(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	if err := fixture.runner.discover(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	oldClaim, claimed, err := fixture.store.ClaimOnboardingWorkflow(fixture.ctx, "old-core", 60*time.Millisecond)
	if err != nil || !claimed {
		t.Fatalf("old claim=%+v claimed=%v err=%v", oldClaim, claimed, err)
	}

	restarted := New(fixture.store, fixture.gateway, fixture.keys, fixture.publisher, time.Hour)
	restarted.batchLimit = 1
	restarted.lease = 200 * time.Millisecond
	restarted.stepTimeout = 50 * time.Millisecond
	restarted.RunOnce(fixture.ctx)
	if got := fixture.events.snapshot(); len(got) != 0 {
		t.Fatalf("new Core stole a live old lease: events=%v", got)
	}
	live := onlyOnboardingWorkflow(t, fixture)
	if live.LeaseOwner != "old-core" || live.Version != oldClaim.Version {
		t.Fatalf("live claim changed before expiry: %+v", live)
	}

	time.Sleep(90 * time.Millisecond)
	restarted.RunOnce(fixture.ctx)
	recovered := onlyOnboardingWorkflow(t, fixture)
	if recovered.Status != contracts.OnboardingReady || recovered.Stage != contracts.OnboardingActive {
		t.Fatalf("expired restart was not recovered: %+v", recovered)
	}
	if recovered.Attempts != oldClaim.Attempts+1 {
		t.Fatalf("recovered attempts=%d, want %d", recovered.Attempts, oldClaim.Attempts+1)
	}
	oldClaim.Stage = contracts.OnboardingCheckingGateway
	if _, err := fixture.store.TransitionOnboardingWorkflow(fixture.ctx, oldClaim, oldClaim.Version); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("old Core was not fenced after restart: %v", err)
	}
}

func TestRunnerCancellationReleasesExactLeaseForImmediateRestart(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	tracked := &onboardingLeaseStore{Store: fixture.store}
	blocking := &blockingOnboardingGateway{started: make(chan struct{})}
	fixture.runner.store = tracked
	fixture.runner.gateway = blocking
	fixture.keys.store = tracked
	fixture.publisher.store = tracked
	fixture.runner.lease = 10 * time.Minute
	fixture.runner.stepTimeout = time.Minute

	ctx, cancel := context.WithCancel(fixture.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		fixture.runner.RunOnce(ctx)
	}()
	select {
	case <-blocking.started:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway preflight did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop after cancellation")
	}

	renewals, releases := tracked.snapshot()
	if len(renewals) != 1 || len(releases) != 1 || releases[0] != renewals[0] {
		t.Fatalf("renewals=%v releases=%v, want exact current generation release", renewals, releases)
	}
	replacement, claimed, err := fixture.store.ClaimOnboardingWorkflow(fixture.ctx, "new-core", time.Minute)
	if err != nil || !claimed || replacement.LeaseOwner != "new-core" {
		t.Fatalf("immediate replacement=%+v claimed=%v err=%v", replacement, claimed, err)
	}
	if err := fixture.store.ReleaseOnboardingWorkflowLease(
		fixture.ctx, replacement.ID, fixture.runner.workerID, replacement.Version,
	); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale process released replacement lease: %v", err)
	}
}

func TestRunnerResumesExpiredInterruptedWorkflowWithoutDuplicatePlan(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	interrupted := &interruptTransitionStore{Store: fixture.store, failStage: contracts.OnboardingVerifying}
	fixture.runner.store = interrupted
	fixture.keys.store = interrupted
	fixture.publisher.store = interrupted
	fixture.runner.lease = 40 * time.Millisecond
	fixture.runner.RunOnce(fixture.ctx)
	workflow := onlyOnboardingWorkflow(t, fixture)
	if workflow.Status != contracts.OnboardingRunning || workflow.Stage != contracts.OnboardingPublishing || fixture.publisher.calls != 1 {
		t.Fatalf("interrupted workflow=%+v publish=%d", workflow, fixture.publisher.calls)
	}
	time.Sleep(100 * time.Millisecond)
	fixture.runner.RunOnce(fixture.ctx)
	workflow = onlyOnboardingWorkflow(t, fixture)
	if workflow.Status != contracts.OnboardingReady || workflow.Stage != contracts.OnboardingActive {
		t.Fatalf("resumed workflow = %+v", workflow)
	}
	plans, err := fixture.store.ListRoutePlans(fixture.ctx, fixture.connector.UserID)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%+v err=%v", plans, err)
	}
	if fixture.publisher.calls != 2 || fixture.keys.installCalls != 2 {
		t.Fatalf("idempotent replay calls: publish=%d install=%d", fixture.publisher.calls, fixture.keys.installCalls)
	}
}

func onlyOnboardingWorkflow(t *testing.T, fixture *onboardingFixture) contracts.OnboardingWorkflow {
	t.Helper()
	workflows, err := fixture.store.ListOnboardingWorkflows(fixture.ctx, contracts.OnboardingWorkflowFilter{
		InstanceID: fixture.instance.ID, PoolID: fixture.pool.ID,
	})
	if err != nil || len(workflows) != 1 {
		t.Fatalf("workflows=%+v err=%v", workflows, err)
	}
	return workflows[0]
}

func assertNoOnboardingSideEffects(t *testing.T, fixture *onboardingFixture) {
	t.Helper()
	if fixture.gateway.calls != 0 || fixture.keys.installCalls != 0 || fixture.keys.verifyCalls != 0 || fixture.publisher.calls != 0 {
		t.Fatalf("unexpected calls: gateway=%d install=%d verify=%d publish=%d",
			fixture.gateway.calls, fixture.keys.installCalls, fixture.keys.verifyCalls, fixture.publisher.calls)
	}
	plans, err := fixture.store.ListRoutePlans(fixture.ctx, fixture.connector.UserID)
	if err != nil || len(plans) != 0 {
		t.Fatalf("unexpected plans=%+v err=%v", plans, err)
	}
}

type onboardingAuditWant struct {
	action    string
	result    string
	errorCode string
}

func assertOnboardingAudits(t *testing.T, fixture *onboardingFixture, want ...onboardingAuditWant) {
	t.Helper()
	audits, err := fixture.store.ListAudits(fixture.ctx, fixture.connector.UserID)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]onboardingAuditWant, 0, len(audits))
	for _, audit := range audits {
		if audit.ActorType != "system" || audit.ActorID != "e2m-onboarding" {
			continue
		}
		got = append(got, onboardingAuditWant{
			action: audit.Action, result: audit.Result, errorCode: audit.ErrorMessage,
		})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("onboarding audits=%+v want=%+v", got, want)
	}
}
