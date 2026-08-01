package autoswitch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/strategy"
)

type scriptedQualityProber struct {
	mu      sync.Mutex
	results []contracts.ConnectorGatewayQualityProbeResult
	err     error
	calls   []qualityProbeCall
}

type conflictOnCloseStore struct {
	store.Store
	failClose bool
}

func (s *conflictOnCloseStore) UpsertQualityCircuitRuntime(
	ctx context.Context,
	input contracts.QualityCircuitRuntime,
	expectedVersion int64,
) (contracts.QualityCircuitRuntime, error) {
	if s.failClose && input.State == contracts.QualityCircuitClosed && !input.RestorePending {
		s.failClose = false
		return contracts.QualityCircuitRuntime{}, store.ErrConflict
	}
	return s.Store.UpsertQualityCircuitRuntime(ctx, input, expectedVersion)
}

type qualityProbeCall struct {
	instanceID string
	input      contracts.ConnectorGatewayQualityProbeInput
	identity   string
}

func (p *scriptedQualityProber) ProbeQuality(
	ctx context.Context,
	instanceID string,
	input contracts.ConnectorGatewayQualityProbeInput,
	identity string,
) (contracts.ConnectorGatewayQualityProbeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, qualityProbeCall{instanceID: instanceID, input: input, identity: identity})
	if p.err != nil {
		return contracts.ConnectorGatewayQualityProbeResult{}, p.err
	}
	if len(p.results) == 0 {
		return contracts.ConnectorGatewayQualityProbeResult{}, errors.New("unexpected quality probe")
	}
	result := p.results[0]
	p.results = p.results[1:]
	if result.Capability == "" {
		result.Capability = input.Capability
	}
	if result.EndpointPath == "" {
		result.EndpointPath = input.EndpointPath
	}
	return result, nil
}

func (p *scriptedQualityProber) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func TestRecoveryRequiresThreeActiveProbesAndKeepsCurrentSource(t *testing.T) {
	f := seedRecoveryFixture(t)
	prober := &scriptedQualityProber{results: []contracts.ConnectorGatewayQualityProbeResult{
		{Success: true, Status: 200, FirstTokenMS: 300, TotalMS: 1000},
		{Success: true, Status: 200, FirstTokenMS: 250, TotalMS: 900},
		{Success: true, Status: 200, FirstTokenMS: 200, TotalMS: 800},
	}}
	o := New(f.st, f.eng, WithClock(f.clk.now), WithQualityProber(prober), WithStrategy(contracts.RouteStrategy{
		Type: contracts.StrategyStabilityFirst, AutoApply: true,
	}))
	openCircuitForRecovery(t, f, o)

	for attempt := 1; attempt <= 3; attempt++ {
		if err := o.RecoverDueCircuits(f.ctx); err != nil {
			t.Fatalf("recovery attempt %d: %v", attempt, err)
		}
		runtime, err := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
		if err != nil {
			t.Fatalf("get runtime: %v", err)
		}
		if attempt < 3 {
			if runtime.State != contracts.QualityCircuitHalfOpen || runtime.ConsecutiveProbeSuccesses != attempt {
				t.Fatalf("attempt %d runtime=%+v", attempt, runtime)
			}
			if f.gw.schedulable("acc-primary") {
				t.Fatalf("attempt %d restored traffic before all probes", attempt)
			}
			f.clk.t = *runtime.ProbeAfter
		} else if runtime.State != contracts.QualityCircuitClosed || !runtime.RecoveryReady || runtime.RecoveryStage != 10 {
			t.Fatalf("third strong probe did not enter guarded recovery: %+v", runtime)
		}
	}
	if prober.callCount() != 3 {
		t.Fatalf("probe calls=%d, want 3", prober.callCount())
	}
	if !f.gw.schedulable("acc-primary") || !f.gw.schedulable("acc-backup") {
		t.Fatal("recovery must enable the original source without disabling the serving source")
	}
}

func TestRecoveryRolloutRequiresFreshTrafficAndRegressionReopens(t *testing.T) {
	f := seedRecoveryFixture(t)
	prober := &scriptedQualityProber{results: []contracts.ConnectorGatewayQualityProbeResult{
		{Success: true, Status: 200, FirstTokenMS: 200, TotalMS: 800},
		{Success: true, Status: 200, FirstTokenMS: 200, TotalMS: 800},
		{Success: true, Status: 200, FirstTokenMS: 200, TotalMS: 800},
	}}
	o := New(f.st, f.eng, WithClock(f.clk.now), WithQualityProber(prober))
	openCircuitForRecovery(t, f, o)
	for i := 0; i < 3; i++ {
		if err := o.RecoverDueCircuits(f.ctx); err != nil {
			t.Fatalf("probe %d: %v", i+1, err)
		}
		runtime, _ := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
		if runtime.ProbeAfter != nil {
			f.clk.t = *runtime.ProbeAfter
		}
	}
	runtime, _ := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if runtime.State != contracts.QualityCircuitClosed || runtime.RecoveryStage != 10 || runtime.RecoveryObserveAfter == nil {
		t.Fatalf("canary was not admitted: %+v", runtime)
	}

	// Time alone is never recovery proof.
	f.clk.t = runtime.RecoveryObserveAfter.Add(time.Minute)
	if err := o.RecoverDueCircuits(f.ctx); err != nil {
		t.Fatalf("hold thin evidence: %v", err)
	}
	runtime, _ = f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if runtime.RecoveryStage != 10 || runtime.State != contracts.QualityCircuitClosed {
		t.Fatalf("empty evidence advanced recovery: %+v", runtime)
	}

	for i := 0; i < 5; i++ {
		_, err := f.st.AppendChannelObservation(f.ctx, contracts.ChannelObservation{
			ID: fmt.Sprintf("rollout-bad-%d", i), ChannelID: "ch-primary", InstanceID: f.plan.InstanceID,
			Success: false, StatusCode: 503, ErrorType: contracts.ErrorServer,
			Source: contracts.ObservationPassive, ObservedAt: f.clk.now().Add(-time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := o.RecoverDueCircuits(f.ctx); err != nil {
		t.Fatalf("reopen regressed canary: %v", err)
	}
	runtime, _ = f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if runtime.State != contracts.QualityCircuitOpen || runtime.RecoveryReady || runtime.RecoveryStage != 0 || f.gw.schedulable("acc-primary") {
		t.Fatalf("regressed canary remained live: %+v", runtime)
	}
}

func TestRecoveryRolloutAdvancesFromAdmittedCohortWithoutHeldOutTraffic(t *testing.T) {
	const sourceID = "shared-recovery-source"
	f := seedFixture(t, 1, []chanSeed{
		{id: "ch-primary", sourceID: sourceID, status: contracts.UpstreamChannelActive, remoteID: "acc-primary", onGateway: true},
		healthyBackup(),
	})
	primary, err := f.st.GetUpstreamChannel(f.ctx, "ch-primary")
	if err != nil {
		t.Fatal(err)
	}

	type recoveryMember struct {
		plan      contracts.RoutePlan
		channelID string
		accountID string
	}
	members := []recoveryMember{{plan: f.plan, channelID: primary.ID, accountID: "acc-primary"}}
	for index := 1; index < 10; index++ {
		user, err := f.st.CreateUser(f.ctx, contracts.User{
			Email: fmt.Sprintf("recovery-cohort-%d@local.dev", index), DisplayName: "Recovery Cohort",
			Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		instance, err := f.st.CreateInstance(f.ctx, contracts.Instance{
			UserID: user.ID, Name: fmt.Sprintf("recovery-gateway-%d", index), Kind: contracts.InstanceKindNewAPI,
		})
		if err != nil {
			t.Fatal(err)
		}
		pool, err := f.st.CreateUpstreamPool(f.ctx, contracts.UpstreamPool{
			Name: fmt.Sprintf("recovery-pool-%d", index), Status: contracts.UpstreamPoolActive,
		})
		if err != nil {
			t.Fatal(err)
		}
		channelID := fmt.Sprintf("recovery-channel-%d", index)
		accountID := fmt.Sprintf("recovery-account-%d", index)
		if _, err := f.st.CreateUpstreamChannel(f.ctx, contracts.UpstreamChannel{
			ID: channelID, PoolID: pool.ID, SourceID: sourceID, DisplayName: channelID,
			Status: contracts.UpstreamChannelActive,
		}); err != nil {
			t.Fatal(err)
		}
		plan, err := f.st.CreateRoutePlan(f.ctx, contracts.RoutePlan{
			UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID,
			Status: contracts.RoutePlanPublished, MaxChannels: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.UpsertPublishedBinding(f.ctx, contracts.PublishedBinding{
			PlanID: plan.ID, InstanceID: instance.ID, ChannelID: channelID,
			RemoteID: accountID, State: contracts.BindingDisabled,
		}); err != nil {
			t.Fatal(err)
		}
		f.gw.accounts = append(f.gw.accounts, contracts.GatewayAccount{ID: accountID, Schedulable: false})
		members = append(members, recoveryMember{plan: plan, channelID: channelID, accountID: accountID})
	}

	stageStarted := f.clk.now().Add(-recoveryRolloutObservationWindow - time.Minute)
	observeAfter := stageStarted.Add(recoveryRolloutObservationWindow)
	for _, member := range members {
		if _, err := f.st.UpsertQualityCircuitRuntime(f.ctx, contracts.QualityCircuitRuntime{
			PlanID: member.plan.ID, ChannelID: member.channelID, State: contracts.QualityCircuitHalfOpen,
			OpenCount: 1, ConsecutiveProbeSuccesses: 3, LastScore: 95, RecoveryReady: true,
			RecoveryStage: 10, RecoveryStageStartedAt: &stageStarted, RecoveryObserveAfter: &observeAfter,
		}, 0); err != nil {
			t.Fatal(err)
		}
	}

	o := New(f.st, f.eng, WithClock(f.clk.now))
	if err := o.advanceRecoveryRollouts(f.ctx, f.clk.now()); err != nil {
		t.Fatalf("admit initial cohort: %v", err)
	}
	assertStage := func(stage, wantActive int) []recoveryMember {
		t.Helper()
		active := make([]recoveryMember, 0, wantActive)
		for _, member := range members {
			runtime, err := f.st.GetQualityCircuitRuntime(f.ctx, member.plan.ID, member.channelID)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.RecoveryStage != stage || !runtime.RecoveryReady {
				t.Fatalf("member %s runtime=%+v, want ready stage %d", member.channelID, runtime, stage)
			}
			if runtime.State == contracts.QualityCircuitClosed {
				if runtime.RecoveryObserveAfter == nil || runtime.RecoveryStageStartedAt == nil {
					t.Fatalf("active member %s has no durable observation window: %+v", member.channelID, runtime)
				}
				active = append(active, member)
			}
		}
		if len(active) != wantActive {
			t.Fatalf("%d%% recovery stage has %d active members, want %d", stage, len(active), wantActive)
		}
		return active
	}
	appendHealthyStageEvidence := func(stage int, active []recoveryMember) {
		t.Helper()
		var observeAfter time.Time
		for _, member := range active {
			runtime, err := f.st.GetQualityCircuitRuntime(f.ctx, member.plan.ID, member.channelID)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.RecoveryObserveAfter != nil && runtime.RecoveryObserveAfter.After(observeAfter) {
				observeAfter = *runtime.RecoveryObserveAfter
			}
		}
		f.clk.t = observeAfter.Add(time.Second)
		for memberIndex, member := range active {
			for sample := 0; sample < 5; sample++ {
				if _, err := f.st.AppendChannelObservation(f.ctx, contracts.ChannelObservation{
					ID:        fmt.Sprintf("recovery-stage-%d-member-%d-sample-%d", stage, memberIndex, sample),
					ChannelID: member.channelID, InstanceID: member.plan.InstanceID,
					Success: true, StatusCode: 200, FirstTokenMS: 250, TotalMS: 800,
					Source:     contracts.ObservationPassive,
					ObservedAt: f.clk.now().Add(-time.Duration(sample) * time.Second),
				}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	advance := func(fromStage, nextStage, wantActive int, active []recoveryMember) []recoveryMember {
		t.Helper()
		appendHealthyStageEvidence(fromStage, active)
		if err := o.advanceRecoveryRollouts(f.ctx, f.clk.now()); err != nil {
			t.Fatalf("expand recovery cohort from %d%%: %v", fromStage, err)
		}
		return assertStage(nextStage, wantActive)
	}

	active := assertStage(10, 1)
	active = advance(10, 25, 3, active)
	active = advance(25, 50, 5, active)
	active = advance(50, 100, 10, active)
	appendHealthyStageEvidence(100, active)
	if err := o.advanceRecoveryRollouts(f.ctx, f.clk.now()); err != nil {
		t.Fatalf("complete recovery rollout: %v", err)
	}
	for _, member := range members {
		runtime, err := f.st.GetQualityCircuitRuntime(f.ctx, member.plan.ID, member.channelID)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.State != contracts.QualityCircuitClosed || runtime.RecoveryReady || runtime.RecoveryStage != 0 ||
			runtime.RecoveryStageStartedAt != nil || runtime.RecoveryObserveAfter != nil ||
			runtime.LastReason.Code != strategy.CircuitReasonRestored {
			t.Fatalf("member %s did not finish recovery cleanup: %+v", member.channelID, runtime)
		}
		if !f.gw.schedulable(member.accountID) {
			t.Fatalf("member %s was not restored on gateway", member.channelID)
		}
	}
}

func TestRecoveryRolloutSourceRegressionReisolatesEveryAdmittedMember(t *testing.T) {
	const sourceID = "shared-regression-source"
	f := seedFixture(t, 1, []chanSeed{{
		id: "ch-primary", sourceID: sourceID, status: contracts.UpstreamChannelActive,
		remoteID: "acc-primary", live: true, onGateway: true, schedulable: true,
	}})

	otherUser, err := f.st.CreateUser(f.ctx, contracts.User{
		Email: "recovery-regression@local.dev", DisplayName: "Recovery Regression",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherInstance, err := f.st.CreateInstance(f.ctx, contracts.Instance{
		UserID: otherUser.ID, Name: "recovery-regression-gateway", Kind: contracts.InstanceKindNewAPI,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherPool, err := f.st.CreateUpstreamPool(f.ctx, contracts.UpstreamPool{
		Name: "recovery-regression-pool", Status: contracts.UpstreamPoolActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.CreateUpstreamChannel(f.ctx, contracts.UpstreamChannel{
		ID: "regression-peer", PoolID: otherPool.ID, SourceID: sourceID,
		DisplayName: "regression-peer", Status: contracts.UpstreamChannelActive,
	}); err != nil {
		t.Fatal(err)
	}
	otherPlan, err := f.st.CreateRoutePlan(f.ctx, contracts.RoutePlan{
		UserID: otherUser.ID, InstanceID: otherInstance.ID, PoolID: otherPool.ID,
		Status: contracts.RoutePlanPublished, MaxChannels: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.UpsertPublishedBinding(f.ctx, contracts.PublishedBinding{
		PlanID: otherPlan.ID, InstanceID: otherInstance.ID, ChannelID: "regression-peer",
		RemoteID: "regression-peer-account", State: contracts.BindingActive,
	}); err != nil {
		t.Fatal(err)
	}
	f.gw.accounts = append(f.gw.accounts, contracts.GatewayAccount{ID: "regression-peer-account", Schedulable: true})

	type member struct {
		plan      contracts.RoutePlan
		channelID string
		accountID string
	}
	members := []member{
		{plan: f.plan, channelID: "ch-primary", accountID: "acc-primary"},
		{plan: otherPlan, channelID: "regression-peer", accountID: "regression-peer-account"},
	}
	stageStarted := f.clk.now().Add(-recoveryRolloutObservationWindow - time.Minute)
	observeAfter := stageStarted.Add(recoveryRolloutObservationWindow)
	for _, member := range members {
		if _, err := f.st.UpsertQualityCircuitRuntime(f.ctx, contracts.QualityCircuitRuntime{
			PlanID: member.plan.ID, ChannelID: member.channelID, State: contracts.QualityCircuitClosed,
			OpenCount: 1, ConsecutiveProbeSuccesses: 3, LastScore: 95, RecoveryReady: true,
			RecoveryStage: 100, RecoveryStageStartedAt: &stageStarted, RecoveryObserveAfter: &observeAfter,
		}, 0); err != nil {
			t.Fatal(err)
		}
	}
	f.clk.t = observeAfter.Add(time.Second)
	for memberIndex, member := range members {
		for sample := 0; sample < 5; sample++ {
			success := memberIndex == 0
			observation := contracts.ChannelObservation{
				ID:        fmt.Sprintf("recovery-regression-%d-%d", memberIndex, sample),
				ChannelID: member.channelID, InstanceID: member.plan.InstanceID,
				Success: success, StatusCode: 200, FirstTokenMS: 250, TotalMS: 800,
				Source:     contracts.ObservationPassive,
				ObservedAt: f.clk.now().Add(-time.Duration(sample) * time.Second),
			}
			if !success {
				observation.StatusCode = 503
				observation.ErrorType = contracts.ErrorServer
				observation.FirstTokenMS = 0
				observation.TotalMS = 0
			}
			if _, err := f.st.AppendChannelObservation(f.ctx, observation); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := New(f.st, f.eng, WithClock(f.clk.now)).advanceRecoveryRollouts(f.ctx, f.clk.now()); err != nil {
		t.Fatalf("reopen regressed source cohort: %v", err)
	}
	for _, member := range members {
		runtime, err := f.st.GetQualityCircuitRuntime(f.ctx, member.plan.ID, member.channelID)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.State != contracts.QualityCircuitOpen || runtime.RecoveryReady || runtime.RecoveryStage != 0 ||
			runtime.LastReason.Code != "recovery_regressed" {
			t.Fatalf("member %s was not re-isolated after source regression: %+v", member.channelID, runtime)
		}
		if f.gw.schedulable(member.accountID) {
			t.Fatalf("member %s remained schedulable after source regression", member.channelID)
		}
		bindings, err := f.st.ListPublishedBindings(f.ctx, member.plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(bindings) != 1 || bindings[0].State != contracts.BindingDisabled {
			t.Fatalf("member %s binding was not disabled: %+v", member.channelID, bindings)
		}
	}
}

func TestRecoveryProbeFailureReopensWithBackoff(t *testing.T) {
	f := seedRecoveryFixture(t)
	prober := &scriptedQualityProber{results: []contracts.ConnectorGatewayQualityProbeResult{
		{Success: true, Status: 200, FirstTokenMS: 200, TotalMS: 800},
		{Success: false, Status: 503, ErrorType: contracts.ErrorServer},
	}}
	o := New(f.st, f.eng, WithClock(f.clk.now), WithQualityProber(prober))
	openCircuitForRecovery(t, f, o)
	if err := o.RecoverDueCircuits(f.ctx); err != nil {
		t.Fatalf("first probe: %v", err)
	}
	runtime, _ := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	f.clk.t = *runtime.ProbeAfter
	if err := o.RecoverDueCircuits(f.ctx); err != nil {
		t.Fatalf("failed probe transition: %v", err)
	}
	runtime, _ = f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if runtime.State != contracts.QualityCircuitOpen || runtime.OpenCount != 2 || runtime.ConsecutiveProbeSuccesses != 0 {
		t.Fatalf("failed half-open probe must reopen: %+v", runtime)
	}
	if runtime.ProbeAfter == nil || !runtime.ProbeAfter.After(f.clk.now().Add(5*time.Minute)) {
		t.Fatalf("second open did not increase cooldown: %+v", runtime.ProbeAfter)
	}
	if f.gw.schedulable("acc-primary") {
		t.Fatal("failed recovery probe restored traffic")
	}
}

func TestRecoveryRejectsContradictorySuccessfulProbe(t *testing.T) {
	f := seedRecoveryFixture(t)
	prober := &scriptedQualityProber{results: []contracts.ConnectorGatewayQualityProbeResult{{
		Success: true, Status: 500,
	}}}
	o := New(f.st, f.eng, WithClock(f.clk.now), WithQualityProber(prober))
	openCircuitForRecovery(t, f, o)
	if err := o.RecoverDueCircuits(f.ctx); err != nil {
		t.Fatalf("contradictory probe: %v", err)
	}
	runtime, _ := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if runtime.State != contracts.QualityCircuitOpen || runtime.OpenCount != 2 || f.gw.schedulable("acc-primary") {
		t.Fatalf("contradictory success restored scheduling: %+v", runtime)
	}
}

func TestRecoveryRejectsSuccessfulProbeWithoutCompleteLatencyEvidence(t *testing.T) {
	f := seedRecoveryFixture(t)
	prober := &scriptedQualityProber{results: []contracts.ConnectorGatewayQualityProbeResult{{
		Success: true, Status: 200, TotalMS: 800,
	}}}
	o := New(f.st, f.eng, WithClock(f.clk.now), WithQualityProber(prober))
	openCircuitForRecovery(t, f, o)
	if err := o.RecoverDueCircuits(f.ctx); err != nil {
		t.Fatalf("recover incomplete probe: %v", err)
	}
	runtime, err := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if err != nil {
		t.Fatalf("get circuit: %v", err)
	}
	if runtime.State != contracts.QualityCircuitOpen || runtime.ConsecutiveProbeSuccesses != 0 || f.gw.schedulable("acc-primary") {
		t.Fatalf("incomplete quality evidence restored scheduling: %+v", runtime)
	}
}

func TestRecoveryPlatformErrorNeverCountsAsHealthyProbe(t *testing.T) {
	f := seedRecoveryFixture(t)
	prober := &scriptedQualityProber{results: []contracts.ConnectorGatewayQualityProbeResult{{
		Success: false, Status: 500, ErrorType: contracts.ErrorPlatform,
	}}}
	o := New(f.st, f.eng, WithClock(f.clk.now), WithQualityProber(prober))
	openCircuitForRecovery(t, f, o)
	before, _ := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if err := o.RecoverDueCircuits(f.ctx); err != nil {
		t.Fatalf("platform probe result: %v", err)
	}
	runtime, _ := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if runtime.State != contracts.QualityCircuitOpen || runtime.ConsecutiveProbeSuccesses != 0 ||
		runtime.LastScore != before.LastScore || runtime.LastReason.Code != "probe_platform_error" {
		t.Fatalf("platform error advanced recovery: before=%+v after=%+v", before, runtime)
	}
}

func TestRecoveryUnsupportedConnectorStaysOpenAndDefers(t *testing.T) {
	f := seedRecoveryFixture(t)
	setConnectorRuntime(t, f, false)
	prober := &scriptedQualityProber{}
	o := New(f.st, f.eng, WithClock(f.clk.now), WithQualityProber(prober))
	openCircuitForRecovery(t, f, o)
	if err := o.RecoverDueCircuits(f.ctx); err != nil {
		t.Fatalf("unsupported probe: %v", err)
	}
	runtime, _ := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if runtime.State != contracts.QualityCircuitOpen || runtime.LastReason.Code != "probe_unsupported" ||
		runtime.ConsecutiveProbeSuccesses != 0 || runtime.OpenCount != 2 ||
		runtime.ProbeAfter == nil || !runtime.ProbeAfter.After(f.clk.now().Add(qualityProbeRetryDelay)) {
		t.Fatalf("unsupported probe must remain isolated and defer: %+v", runtime)
	}
	if prober.callCount() != 0 || f.gw.schedulable("acc-primary") {
		t.Fatal("unsupported connector must not execute or restore a probe")
	}
}

func TestRecoveryInfrastructureFailureBreaksSuccessStreakAndBacksOffExponentially(t *testing.T) {
	f := seedRecoveryFixture(t)
	prober := &scriptedQualityProber{results: []contracts.ConnectorGatewayQualityProbeResult{{
		Success: true, Status: 200, FirstTokenMS: 200, TotalMS: 800,
	}}}
	o := New(f.st, f.eng, WithClock(f.clk.now), WithQualityProber(prober))
	openCircuitForRecovery(t, f, o)
	if err := o.RecoverDueCircuits(f.ctx); err != nil {
		t.Fatalf("successful probe: %v", err)
	}
	runtime, _ := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if runtime.ConsecutiveProbeSuccesses != 1 {
		t.Fatalf("setup success streak=%d", runtime.ConsecutiveProbeSuccesses)
	}
	f.clk.t = *runtime.ProbeAfter
	prober.err = errors.New("connector task failed")
	failedAt := f.clk.now()
	if err := o.RecoverDueCircuits(f.ctx); err != nil {
		t.Fatalf("failed task: %v", err)
	}
	runtime, _ = f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if runtime.State != contracts.QualityCircuitOpen || runtime.ConsecutiveProbeSuccesses != 0 || runtime.OpenCount != 2 {
		t.Fatalf("infrastructure failure retained recovery progress: %+v", runtime)
	}
	if runtime.ProbeAfter == nil || !runtime.ProbeAfter.After(failedAt.Add(qualityProbeRetryDelay)) {
		t.Fatalf("failure used fixed retry instead of exponential backoff: %+v", runtime.ProbeAfter)
	}
}

func TestRecoveryStaleConnectorStaysOpen(t *testing.T) {
	f := seedRecoveryFixture(t)
	instance, _ := f.st.GetInstance(f.ctx, f.plan.InstanceID)
	connector, _ := f.st.GetConnector(f.ctx, instance.ConnectorID)
	stale := f.clk.now().Add(-2 * time.Minute)
	connector.LastSeenAt = &stale
	// Wrap the store so GetConnector exposes a stale heartbeat without mutating
	// the connector task fixture's other state.
	wrapped := &staleConnectorStore{Store: f.st, connector: connector}
	prober := &scriptedQualityProber{}
	o := New(wrapped, f.eng, WithClock(f.clk.now), WithQualityProber(prober))
	openCircuitForRecovery(t, f, o)
	if err := o.RecoverDueCircuits(f.ctx); err != nil {
		t.Fatalf("stale connector recovery: %v", err)
	}
	runtime, _ := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if runtime.State != contracts.QualityCircuitOpen || runtime.LastReason.Code != "probe_unsupported" || prober.callCount() != 0 {
		t.Fatalf("stale connector must not receive a probe: %+v calls=%d", runtime, prober.callCount())
	}
}

type staleConnectorStore struct {
	store.Store
	connector contracts.Connector
}

func (s *staleConnectorStore) GetConnector(context.Context, string) (contracts.Connector, error) {
	return s.connector, nil
}

func TestRecoveryProbeCASAllowsOneRunner(t *testing.T) {
	f := seedRecoveryFixture(t)
	prober := &scriptedQualityProber{results: []contracts.ConnectorGatewayQualityProbeResult{
		{Success: true, Status: 200, FirstTokenMS: 200, TotalMS: 800},
	}}
	o := New(f.st, f.eng, WithClock(f.clk.now), WithQualityProber(prober))
	openCircuitForRecovery(t, f, o)

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errCh <- o.RecoverDueCircuits(f.ctx)
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent recovery: %v", err)
		}
	}
	if prober.callCount() != 1 {
		t.Fatalf("CAS claim allowed %d probe side effects, want 1", prober.callCount())
	}
}

func TestRecoveryRepairsCrashAfterGatewayRestore(t *testing.T) {
	f := seedRecoveryFixture(t)
	wrapped := &conflictOnCloseStore{Store: f.st, failClose: true}
	prober := &scriptedQualityProber{results: []contracts.ConnectorGatewayQualityProbeResult{
		{Success: true, Status: 200, FirstTokenMS: 200, TotalMS: 800},
		{Success: true, Status: 200, FirstTokenMS: 200, TotalMS: 800},
		{Success: true, Status: 200, FirstTokenMS: 200, TotalMS: 800},
	}}
	o := New(wrapped, f.eng, WithClock(f.clk.now), WithQualityProber(prober))
	openCircuitForRecovery(t, f, o)
	for i := 0; i < 3; i++ {
		if err := o.RecoverDueCircuits(f.ctx); err != nil {
			t.Fatalf("probe %d: %v", i+1, err)
		}
		runtime, _ := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
		if runtime.ProbeAfter != nil {
			f.clk.t = *runtime.ProbeAfter
		}
	}
	runtime, _ := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if !runtime.RestorePending || runtime.State != contracts.QualityCircuitHalfOpen || !f.gw.schedulable("acc-primary") {
		t.Fatalf("simulated crash must leave durable restore intent plus active binding: %+v", runtime)
	}
	if err := o.RecoverDueCircuits(f.ctx); err != nil {
		t.Fatalf("repair pending restore: %v", err)
	}
	runtime, _ = f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if runtime.State != contracts.QualityCircuitClosed || runtime.RestorePending {
		t.Fatalf("pending restore was not completed idempotently: %+v", runtime)
	}
	if prober.callCount() != 3 {
		t.Fatalf("repair issued another real quality probe: %d", prober.callCount())
	}
}

func TestEvaluateRepairsLegacyActiveHalfOpenRestore(t *testing.T) {
	f := seedRecoveryFixture(t)
	o := New(f.st, f.eng, WithClock(f.clk.now))
	if _, err := f.eng.ApplyScheduling(o.autoCtx(f.ctx), f.plan.ID, map[string]bool{
		"ch-backup": true, "ch-primary": true,
	}); err != nil {
		t.Fatalf("seed active binding: %v", err)
	}
	now := f.clk.now()
	if _, err := f.st.UpsertQualityCircuitRuntime(f.ctx, contracts.QualityCircuitRuntime{
		PlanID: f.plan.ID, ChannelID: "ch-primary", State: contracts.QualityCircuitHalfOpen,
		OpenedAt: &now, ProbeAfter: &now, HalfOpenSince: &now,
		OpenCount: 1, ConsecutiveProbeSuccesses: 3, LastScore: 95,
	}, 0); err != nil {
		t.Fatalf("seed legacy runtime: %v", err)
	}
	if _, err := o.Evaluate(f.ctx, f.plan.ID); err != nil {
		t.Fatalf("evaluate repair: %v", err)
	}
	runtime, _ := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if !runtime.RestorePending || runtime.State != contracts.QualityCircuitHalfOpen {
		t.Fatalf("legacy active half-open was not marked pending: %+v", runtime)
	}
	if !f.gw.schedulable("acc-primary") {
		t.Fatal("repair path ejected an already-restored binding")
	}
	f.clk.t = *runtime.ProbeAfter
	if err := o.RecoverDueCircuits(f.ctx); err != nil {
		t.Fatalf("complete repaired restore: %v", err)
	}
	runtime, _ = f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if runtime.State != contracts.QualityCircuitClosed || runtime.RestorePending {
		t.Fatalf("legacy repair did not close circuit: %+v", runtime)
	}
}

func seedRecoveryFixture(t *testing.T) fixture {
	t.Helper()
	f := seedFixture(t, 1, []chanSeed{livePrimary(), healthyBackup()})
	channel, err := f.st.GetUpstreamChannel(f.ctx, "ch-primary")
	if err != nil {
		t.Fatal(err)
	}
	channel.Models = []string{"model-a"}
	channel.ProbeCapability = contracts.QualityProbeTextStream
	channel.ProbeEndpointPath = contracts.QualityProbeEndpointResponses
	if _, err := f.st.UpdateUpstreamChannel(f.ctx, channel); err != nil {
		t.Fatalf("add probe model: %v", err)
	}
	setConnectorRuntime(t, f, true)
	return f
}

func setConnectorRuntime(t *testing.T, f fixture, supportsProbe bool) {
	t.Helper()
	instance, err := f.st.GetInstance(f.ctx, f.plan.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if instance.ConnectorID == "" {
		expiresAt := time.Now().UTC().Add(time.Hour)
		enrollment, err := f.st.CreateConnectorEnrollment(f.ctx, contracts.ConnectorEnrollment{
			ID: "enroll-recovery", UserID: instance.UserID, InstanceID: instance.ID,
			ConnectorID: "connector-recovery", TokenHash: "enroll-token-recovery", ExpiresAt: expiresAt,
		})
		if err != nil {
			t.Fatalf("create connector enrollment: %v", err)
		}
		connector, _, err := f.st.UseConnectorEnrollment(f.ctx, enrollment.TokenHash, contracts.Connector{
			ID: enrollment.ConnectorID, UserID: instance.UserID, InstanceID: instance.ID,
			Version: "0.2.0", ProtocolVersion: contracts.ConnectorProtocolVersion, TokenHash: "connector-token-recovery",
		})
		if err != nil {
			t.Fatalf("enroll connector: %v", err)
		}
		if _, err := f.st.UpdateInstanceConnector(f.ctx, instance.ID, connector.ID); err != nil {
			t.Fatalf("bind connector: %v", err)
		}
		instance.ConnectorID = connector.ID
	}
	capabilities := []contracts.ConnectorTaskType(nil)
	var qualityProbe *contracts.ConnectorQualityProbeCapabilities
	if supportsProbe {
		capabilities = []contracts.ConnectorTaskType{contracts.ConnectorTaskGatewayQualityProbe}
		qualityProbe = &contracts.ConnectorQualityProbeCapabilities{
			RecoveryMode: contracts.QualityProbeRecoveryAutomatic, Enabled: true,
			Capabilities:  []contracts.QualityProbeCapability{contracts.QualityProbeTextStream},
			EndpointPaths: []string{contracts.QualityProbeEndpointResponses},
			FirstTokenMS:  true, TotalMS: true, MaxRequestsPerHour: 12, MinIntervalSeconds: 60,
		}
	}
	if _, err := f.st.RecordConnectorSeen(f.ctx, instance.ConnectorID, "0.2.0", contracts.ConnectorRuntimeState{
		ProtocolVersion: contracts.ConnectorProtocolVersion, GatewayConfigured: true,
		GatewayKind: string(instance.Kind), GatewayStatus: "ok", Capabilities: capabilities, QualityProbe: qualityProbe,
	}); err != nil {
		t.Fatalf("record connector runtime: %v", err)
	}
}

func openCircuitForRecovery(t *testing.T, f fixture, o *Orchestrator) {
	t.Helper()
	if _, err := f.eng.ApplyScheduling(o.autoCtx(f.ctx), f.plan.ID, map[string]bool{
		"ch-backup": true, "ch-primary": false,
	}); err != nil {
		t.Fatalf("eject primary: %v", err)
	}
	if _, err := o.openQualityCircuit(f.ctx, f.plan.ID, "ch-primary", strategy.PenaltyEvaluation{
		ChannelID: "ch-primary", Score: 40, Evidence: 1, Eject: true,
		Reason: strategy.Reason{Code: strategy.GatePenaltyThreshold, Text: "quality below threshold"},
	}, f.clk.now()); err != nil {
		t.Fatalf("open circuit: %v", err)
	}
	runtime, err := f.st.GetQualityCircuitRuntime(f.ctx, f.plan.ID, "ch-primary")
	if err != nil || runtime.ProbeAfter == nil {
		t.Fatalf("get opened circuit: %+v err=%v", runtime, err)
	}
	f.clk.t = *runtime.ProbeAfter
}
