// Package autoswitch is the Phase 4 automatic-switch orchestrator. It closes the
// health-driven loop: read channel health snapshots, rank candidates with the
// pure strategy engine (Phase 3), turn the ranking into a concrete "drain the
// failing channel, let a healthy backup carry traffic" intent, and drive that
// intent through the EXISTING RoutePlan + reconcile engine (dry-run -> risk
// grade -> canary apply -> observe -> rollback). Every real gateway change still
// flows through publish/reconcile, so it is dry-runnable, audited via
// ReconcileRun, and rollbackable. The orchestrator never calls a gateway
// directly and never provisions or deletes. Runtime ejection is expressed as a
// plan-local PublishedBinding scheduling intent; the shared channel lifecycle
// remains unchanged for every other downstream instance.
//
// Safety invariants (docs/development/health-driven-auto-switching.md):
//   - Switch out fast, switch back slow: a failing channel is drained quickly,
//     and can only return after cooldown plus fresh active-probe evidence.
//   - Fail closed locally: if there is no eligible source, disable the failed
//     binding for this plan while leaving the shared channel catalog untouched.
//   - Never auto delete/deprovision: the first version only enables/disables.
//   - Idempotent per failure window: one active decision per (plan, from, to,
//     strategy) fingerprint; the same failure does not spawn duplicates.
//   - Dampened: minimum switch cooldown and a per-hour ceiling gate auto-apply.
package autoswitch

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/strategy"
)

// DefaultObservationWindow is how long a switch is observed before it is called
// done, when the strategy does not set its own recovery-observation window.
const DefaultObservationWindow = 15 * time.Minute

// autoSwitchApplyingLease bounds a distributed claim around reconcile. The
// gateway adapters have shorter request deadlines; this leaves room for the
// binding write and still lets a later sweep repair a crashed worker promptly.
const autoSwitchApplyingLease = 2 * time.Minute

// Applying work is cancelled well before its durable lease can expire. The
// remaining headroom lets adapters observe cancellation before another Core
// instance is allowed to take ownership.
const autoSwitchApplyingWorkTimeout = autoSwitchApplyingLease / 2

// decisionWindow is the health window the orchestrator decides on (the doc's
// primary 5m decision window).
const decisionWindow = contracts.Window5m

// replacementSnapshotMaxAge allows one missed aggregation refresh without
// admitting arbitrarily stale evidence as a healthy backup.
const replacementSnapshotMaxAge = 2*5*time.Minute + time.Minute

// autoActor attributes reconcile runs and audits to the automatic switcher so
// they are distinguishable from operator actions in the history.
var autoActor = contracts.Actor{Type: "workflow", ID: "auto-switch"}

// Reconciler is the slice of the publish engine the orchestrator drives. It is
// satisfied by *publish.Engine, so auto-switch stays decoupled from adapter
// wiring and is trivial to fake in tests. Plan is dry-run; Apply executes.
type Reconciler interface {
	PlanScheduling(ctx context.Context, planID string, desired map[string]bool) (contracts.ReconcilePlan, error)
	ApplyScheduling(ctx context.Context, planID string, desired map[string]bool) (contracts.ReconcilePlan, error)
}

// QualityProber obtains one real upstream measurement through the target
// instance's Connector. Implementations must keep gateway endpoints and
// credentials connector-local; Core supplies only stable resource identities.
type QualityProber interface {
	ProbeQuality(
		ctx context.Context,
		instanceID string,
		input contracts.ConnectorGatewayQualityProbeInput,
		identity string,
	) (contracts.ConnectorGatewayQualityProbeResult, error)
}

// Dispatcher delivers notifications. It is satisfied by *notify.Router; a nil
// dispatcher disables notification (delivery is always best-effort).
type Dispatcher interface {
	DispatchAll(ctx context.Context, ev notify.Event, routes []contracts.NotificationRoute)
}

// EventSink receives platform-internal auto-switch events (for example the
// console SSE feed). It is deliberately separate from notification routes so the
// console still sees decisions even when a user has not configured Feishu, QQ,
// or webhook delivery.
type EventSink func(ctx context.Context, decision contracts.AutoSwitchDecision)

// RecoveryEvent is the owner-safe projection of a recovery transition. It
// intentionally excludes pool, source, channel, plan and remote account IDs.
type RecoveryEvent struct {
	UserID     int64
	InstanceID string
	Status     string
	Stage      int
}

type RecoveryEventSink func(ctx context.Context, event RecoveryEvent)

// Orchestrator evaluates plans and drives automatic switches. It holds no
// cross-call state; all history lives in the store.
type Orchestrator struct {
	store             store.Store
	engine            Reconciler
	prober            QualityProber
	notifier          Dispatcher
	eventSink         EventSink
	recoveryEventSink RecoveryEventSink
	strategy          *contracts.RouteStrategy
	obsWindow         time.Duration
	now               func() time.Time
}

type orchestratorTimeContextKey struct{}

// Option configures an Orchestrator.
type Option func(*Orchestrator)

// WithNotifier wires a notification dispatcher (defaults to none).
func WithNotifier(d Dispatcher) Option { return func(o *Orchestrator) { o.notifier = d } }

// WithEventSink wires a platform event sink for console/live-stream updates.
func WithEventSink(s EventSink) Option { return func(o *Orchestrator) { o.eventSink = s } }

// WithRecoveryEventSink wires owner-safe recovery progress into the platform
// live event feed independently of configured notification routes.
func WithRecoveryEventSink(s RecoveryEventSink) Option {
	return func(o *Orchestrator) { o.recoveryEventSink = s }
}

// WithStrategy pins the strategy used for every plan (defaults to a
// stability-first, auto-applying strategy; per-plan resolution reads
// plan.Labels["strategy"] when no strategy is pinned).
func WithStrategy(s contracts.RouteStrategy) Option {
	return func(o *Orchestrator) { cp := s; o.strategy = &cp }
}

// WithObservationWindow overrides the observation window (defaults to the
// strategy's recovery-observation window, else DefaultObservationWindow).
func WithObservationWindow(d time.Duration) Option {
	return func(o *Orchestrator) {
		if d > 0 {
			o.obsWindow = d
		}
	}
}

// WithClock overrides the time source (tests inject a fixed/advanceable clock).
func WithClock(now func() time.Time) Option { return func(o *Orchestrator) { o.now = now } }

func (o *Orchestrator) withSchedulerTime(ctx context.Context, now time.Time) context.Context {
	return context.WithValue(ctx, orchestratorTimeContextKey{}, now)
}

func schedulerTime(ctx context.Context) time.Time {
	if value, ok := ctx.Value(orchestratorTimeContextKey{}).(time.Time); ok && !value.IsZero() {
		return value.UTC()
	}
	return time.Now().UTC()
}

// WithQualityProber enables active recovery probes. Without one, circuits stay
// open and the recovery runner only delays its next attempt.
func WithQualityProber(p QualityProber) Option { return func(o *Orchestrator) { o.prober = p } }

// New builds an Orchestrator over a store and reconcile engine.
func New(st store.Store, engine Reconciler, opts ...Option) *Orchestrator {
	o := &Orchestrator{
		store:  st,
		engine: engine,
		now:    func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// Evaluate inspects one plan and, if a currently-scheduled channel has failed,
// produces an AutoSwitchDecision that drains it in favour of the best healthy
// backup. It returns (nil, nil) when nothing needs switching. A returned
// decision may be proposed, skipped, observing, or failed depending on risk,
// dampening, and apply outcome.
func (o *Orchestrator) Evaluate(ctx context.Context, planID string) (*contracts.AutoSwitchDecision, error) {
	plan, err := o.store.GetRoutePlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	// Only a published plan is part of live scheduling. Draft and suspended
	// plans are operator-owned states and must not start an automatic decision.
	if plan.Status != contracts.RoutePlanPublished {
		return nil, nil
	}

	channels, err := o.store.ListUpstreamChannels(ctx, plan.PoolID)
	if err != nil {
		return nil, err
	}
	bindings, err := o.store.ListPublishedBindings(ctx, plan.ID)
	if err != nil {
		return nil, err
	}
	// Reconcile and circuit persistence cannot share one transaction with a
	// remote gateway. Repair the narrow crash window from the durable decision
	// before ranking so an ejected source cannot re-enter on missing samples.
	if err := o.repairDecisionCircuitFallbacks(ctx, plan.ID, bindings); err != nil {
		return nil, err
	}
	if err := o.repairExpiredApplyingDecisions(ctx, plan, bindings); err != nil {
		return nil, err
	}
	// An expired-decision repair may have reconciled gateway facts back into the
	// binding paper trail. Rank from that repaired state, never the pre-repair
	// snapshot loaded above.
	bindings, err = o.store.ListPublishedBindings(ctx, plan.ID)
	if err != nil {
		return nil, err
	}
	// A restore-pending binding is already enabled and only needs the recovery
	// worker to finish its durable close. It must not be classified as a live
	// half-open failure and ejected again in this evaluation pass.
	pendingRestore := make(map[string]bool)
	recoveredAfter := make(map[string]time.Time)
	if runtimes, listErr := o.store.ListQualityCircuitRuntimes(ctx, contracts.QualityCircuitRuntimeFilter{PlanID: plan.ID}); listErr != nil {
		return nil, listErr
	} else {
		for _, runtime := range runtimes {
			pendingRestore[runtime.ChannelID] = runtime.RestorePending
			if watermark := recoveryEvidenceWatermark(runtime); !watermark.IsZero() {
				recoveredAfter[runtime.ChannelID] = watermark
			}
		}
	}
	live := make(map[string]bool)
	for _, b := range bindings {
		if b.State == contracts.BindingActive && !pendingRestore[b.ChannelID] {
			live[b.ChannelID] = true
		}
	}

	strat := o.strategyFor(ctx, plan)
	circuitStates, err := o.qualityCircuitStates(ctx, plan.ID)
	if err != nil {
		return nil, err
	}
	cands := o.candidates(ctx, plan, channels, bindings, circuitStates, recoveredAfter)
	ranking := strategy.RankByPenalty(strat, cands)
	admitted := admitHealthyReplacements(ranking.Eligible, o.now())
	preferenceEligible := strategy.RankEligibleByPreference(strat, cands, admitted)

	// The trigger is a currently-live channel that the strategy now gates out.
	// Optimization-only switches (moving to a marginally better healthy channel)
	// are intentionally out of scope for the MVP: switch out fast on failure,
	// switch back slow and deliberately.
	excludedReason := make(map[string]strategy.Reason, len(ranking.Excluded))
	for _, ex := range ranking.Excluded {
		excludedReason[ex.ChannelID] = ex.Reason
	}
	from := firstLiveExcluded(ranking.Excluded, live)
	if from == "" {
		return nil, nil // no failing live channel; nothing to do
	}

	channelByID := make(map[string]contracts.UpstreamChannel, len(channels))
	for _, channel := range channels {
		channelByID[channel.ID] = channel
	}
	to := bestDifferentSource(from, preferenceEligible, channelByID)
	fromEval, fromFound := penaltyEvaluationFor(ranking, from)
	toEval, toFound := penaltyEvaluationFor(ranking, to)
	hardFailure := fromFound && fromEval.HardFailure

	now := o.now()
	fp := fingerprint(plan.ID, from, to, strat.Type.Normalize())

	// Idempotency: one active decision per failure fingerprint.
	if existing, ok := o.activeByFingerprint(ctx, plan.ID, fp); ok {
		return existing, nil
	}

	base := contracts.AutoSwitchDecision{
		UserID:        plan.UserID,
		PlanID:        plan.ID,
		InstanceID:    plan.InstanceID,
		PoolID:        plan.PoolID,
		Strategy:      strat.Type.Normalize(),
		Trigger:       contracts.ReconcileTriggerAuto,
		TriggerReason: penaltyTriggerText(from, excludedReason[from], fromEval, fromFound),
		FromChannelID: from,
		ToChannelID:   to,
		Fingerprint:   fp,
		CreatedAt:     now,
	}
	leaseUntil := now.Add(autoSwitchApplyingLease)
	base.LeaseUntil = &leaseUntil
	claimed, ownsClaim, err := o.store.ClaimAutoSwitchDecision(ctx, base)
	if err != nil {
		return nil, err
	}
	if !ownsClaim {
		return &claimed, nil
	}
	base = claimed
	// Once a claim is durable, finish its state machine even if an HTTP caller
	// disconnects or the runner is stopping. Abandoning it at applying would
	// block the active fingerprint indefinitely.
	workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), autoSwitchApplyingWorkTimeout)
	defer cancel()
	ctx = o.withApplyingSideEffectGuard(workCtx, base)

	// Soft quality degradation is rolled out by a stable plan/channel cohort.
	// A source-capacity event can hit many downstreams at once; applying the
	// same public verdict everywhere would create a self-amplifying stampede.
	// Definitive instance-scoped failures bypass this guard.
	if fromFound && !hardFailure && to != "" {
		badWindows := o.consecutiveBadWindows(ctx, plan, strat, from)
		percentage := strategy.QualityEjectionPercentage(badWindows)
		selected, cohortKnown := o.sourceQualityCohort(ctx, channelByID[from].SourceIdentity(), percentage)
		if cohortKnown && selected[plan.ID] {
			// This downstream is in the current progressive batch.
		} else {
			reason := fmt.Sprintf("质量分 %.1f 已连续 %d 个窗口低于摘除线，进入 %d%% 稳定灰度；本下游不在当前批次，保持原调度", fromEval.Score, badWindows, percentage)
			if !cohortKnown {
				reason = "无法确认全局来源灰度成员，保留当前下游作为观察组并等待下轮评估"
			}
			return o.finalize(ctx, base, contracts.AutoSwitchSkipped, contracts.RiskLevelL1,
				reason, nil)
		}
	}

	// Dampening protects ordinary replacement churn. It must never keep a
	// definitively broken credential online or defeat fail-closed when no healthy
	// source exists.
	if to != "" && !hardFailure {
		lastSwitch, recent := o.switchHistory(ctx, plan.ID, now)
		if damp := strategy.AllowSwitch(strat, lastSwitch, recent, now); !damp.Allowed {
			return o.finalize(ctx, base, contracts.AutoSwitchSkipped, contracts.RiskLevelL1, damp.Reason.Text, nil)
		}
	}

	// No eligible source: fail closed for this plan only. Continuing to send
	// traffic to a source whose quality crossed the ejection line hides errors
	// from the user; the disabled binding can rejoin only through fresh probes.
	if to == "" {
		runCtx := o.autoCtx(ctx)
		dry, planErr := o.engine.PlanScheduling(runCtx, plan.ID, map[string]bool{from: false})
		if planErr != nil {
			return o.finalize(ctx, base, contracts.AutoSwitchFailed, contracts.RiskLevelL3,
				"局部摘除 dry-run 失败: "+planErr.Error(), nil)
		}
		base.DryRunResult = dry
		if len(dry.Actions) == 0 {
			return o.finalize(ctx, base, contracts.AutoSwitchSkipped, contracts.RiskLevelL3,
				"无健康来源且当前绑定已停止调度", nil)
		}
		applied, applyErr := o.engine.ApplyScheduling(runCtx, plan.ID, map[string]bool{from: false})
		base.DryRunResult = applied
		if applyErr != nil {
			return o.finalize(ctx, base, contracts.AutoSwitchFailed, contracts.RiskLevelL3,
				"无健康来源，局部摘除 apply 失败: "+applyErr.Error(), nil)
		}
		base.AutoApplied = true
		appliedAt := now
		base.AppliedAt = &appliedAt
		if leaseErr := o.renewApplyingLease(ctx, base); leaseErr != nil {
			return nil, leaseErr
		}
		if _, circuitErr := o.openQualityCircuit(ctx, plan.ID, from, fromEval, appliedAt); circuitErr != nil {
			return o.finalizeNote(ctx, base, contracts.AutoSwitchFailed, contracts.RiskLevelL3,
				"local ejection succeeded but circuit persistence failed: "+circuitErr.Error(),
				"the durable auto-switch decision will repair the circuit on the next sweep", nil)
		}
		return o.finalizeNote(ctx, base, contracts.AutoSwitchCompleted, contracts.RiskLevelL3,
			"无健康替代来源，已仅对当前下游摘除故障渠道",
			"当前下游进入 fail-closed；等待冷却后主动探测恢复", nil)
	}

	// Express the switch as a sparse plan-local scheduling intent. No shared
	// UpstreamChannel state is changed, so a capacity issue observed by this
	// instance cannot drain the source from unrelated downstream plans.
	intent := map[string]bool{to: true, from: false}
	runCtx := o.autoCtx(ctx)
	dry, err := o.engine.PlanScheduling(runCtx, plan.ID, intent)
	if err != nil {
		return o.finalize(ctx, base, contracts.AutoSwitchFailed, contracts.RiskLevelL3,
			"reconcile dry-run 失败: "+err.Error(), nil)
	}
	base.DryRunResult = dry

	// A switch that changes nothing on the gateway is a no-op skip.
	if len(dry.Actions) == 0 {
		return o.finalize(ctx, base, contracts.AutoSwitchSkipped, contracts.RiskLevelL1,
			"当前网关状态无需变更", nil)
	}

	// Canary / anti-stranding safety. The low-risk auto-apply is only safe when
	// the chosen backup actually comes online in this same apply and the change
	// stays canary-sized. Two dangers are gated here:
	//   1. The rollout policy holds the backup back (ReconcileHold on `to`).
	//      Applying would drain the failing channel without promoting the backup.
	//      If the failing channel is the only live one, that strands traffic, so
	//      we skip and alert; otherwise other channels still serve, so we only
	//      propose it for a human instead of silently half-switching.
	//   2. The switch would bring more than one new channel online, which is
	//      broader than a single-channel canary; that is proposed, not
	//      auto-applied.
	toHeld, onlineCount := analyzeSwitchRollout(dry.Actions, to)
	onlyLive := len(live) == 1 && live[from]
	if toHeld {
		if hardFailure {
			return o.failClosedHardBinding(ctx, base, plan, fromEval, now,
				"replacement is held by rollout policy; hard-failed binding was isolated")
		}
		if onlyLive {
			return o.finalize(ctx, base, contracts.AutoSwitchSkipped, contracts.RiskLevelL3,
				"备用渠道被灰度发布策略暂缓上线，自动切换会导致服务空档，暂不切换，已通知人工介入", nil)
		}
		return o.finalize(ctx, base, contracts.AutoSwitchProposed, contracts.RiskLevelL2,
			"备用渠道被灰度发布策略暂缓上线，转为人工确认后再切换", nil)
	}
	if onlineCount > 1 {
		if hardFailure {
			return o.failClosedHardBinding(ctx, base, plan, fromEval, now,
				"replacement cannot be admitted safely; hard-failed binding was isolated")
		}
		return o.finalize(ctx, base, contracts.AutoSwitchProposed, contracts.RiskLevelL2,
			"本次切换将同时上线多个新渠道，超出灰度(canary)范围，转为人工确认", nil)
	}

	risk, riskReason := gradeRisk(dry.Actions, to != "")
	if fromFound {
		riskReason = penaltyRiskText(riskReason, fromEval, toEval, toFound)
	}
	base.RiskLevel = risk
	base.RiskReason = riskReason

	// Governance gates: approval required, auto-apply disabled, or mid-risk all
	// leave the desired state untouched and only propose the switch to a human.
	if !hardFailure && (strat.ApprovalRequired || !strat.AutoApply || risk == contracts.RiskLevelL2) {
		return o.finalize(ctx, base, contracts.AutoSwitchProposed, risk, riskReason, nil)
	}
	// High risk never auto-executes; it alerts and leaves state unchanged.
	if !hardFailure && risk == contracts.RiskLevelL3 {
		return o.finalize(ctx, base, contracts.AutoSwitchSkipped, risk, riskReason, nil)
	}

	// Low risk + auto-apply: execute the switch (canary of one backup) and enter
	// the observation window.
	applied, applyErr := o.engine.ApplyScheduling(runCtx, plan.ID, intent)
	if applyErr != nil {
		// Best-effort drain the attempted replacement, but never re-enable the
		// unhealthy source here. The scoped engine admits the replacement before
		// draining the source, so an admission failure already leaves the source
		// online; if the source was drained before a later persistence failure,
		// fail closed until active probes prove it safe.
		note := ""
		_, replacementDrainErr := o.engine.ApplyScheduling(runCtx, plan.ID, map[string]bool{to: false})
		if reErr := replacementDrainErr; reErr != nil {
			note = "基线恢复 apply 也失败: " + reErr.Error()
		} else {
			note = "已撤回替代渠道；故障来源未被自动恢复"
		}
		if replacementDrainErr == nil || o.bindingDisabled(ctx, plan.ID, to) {
			replacementEval := ejectionEvaluation(to, "replacement_apply_failed", "replacement failed during scheduling apply", scoreOrZero(toEval, toFound))
			if leaseErr := o.renewApplyingLease(ctx, base); leaseErr != nil {
				note += "; applying lease lost before replacement circuit: " + leaseErr.Error()
			} else if _, circuitErr := o.openQualityCircuit(ctx, plan.ID, to, replacementEval, now); circuitErr != nil {
				note += "; replacement circuit persistence failed: " + circuitErr.Error()
			}
		}
		if hardFailure && !o.bindingDisabled(ctx, plan.ID, from) {
			_, sourceDrainErr := o.engine.ApplyScheduling(runCtx, plan.ID, map[string]bool{from: false})
			if sourceDrainErr != nil {
				note += "; hard-failed source drain failed: " + sourceDrainErr.Error()
			} else {
				note += "; hard-failed source was locally drained after replacement admission failed"
			}
		}
		if o.bindingDisabled(ctx, plan.ID, from) {
			base.AutoApplied = true
			appliedAt := now
			base.AppliedAt = &appliedAt
			if leaseErr := o.renewApplyingLease(ctx, base); leaseErr != nil {
				note += "; applying lease lost before source circuit: " + leaseErr.Error()
			} else if _, circuitErr := o.openQualityCircuit(ctx, plan.ID, from, fromEval, appliedAt); circuitErr != nil {
				note += "; source circuit persistence failed: " + circuitErr.Error()
			}
		}
		base.DryRunResult = applied
		return o.finalizeNote(ctx, base, contracts.AutoSwitchFailed, contracts.RiskLevelL3,
			"reconcile apply 失败: "+applyErr.Error(), note, nil)
	}

	base.AutoApplied = true
	appliedAt := now
	observeUntil := now.Add(o.observationWindow(strat))
	base.AppliedAt = &appliedAt
	base.ObserveUntil = &observeUntil
	if leaseErr := o.renewApplyingLease(ctx, base); leaseErr != nil {
		return nil, leaseErr
	}
	if _, circuitErr := o.openQualityCircuit(ctx, plan.ID, from, fromEval, appliedAt); circuitErr != nil {
		return o.finalizeNote(ctx, base, contracts.AutoSwitchFailed, contracts.RiskLevelL3,
			"switch succeeded but source circuit persistence failed: "+circuitErr.Error(),
			"the replacement remains serving; the durable decision will repair source isolation on the next sweep", nil)
	}
	return o.finalize(ctx, base, contracts.AutoSwitchObserving, risk,
		"已自动切换并进入观察期", nil)
}

func (o *Orchestrator) failClosedHardBinding(
	ctx context.Context,
	base contracts.AutoSwitchDecision,
	plan contracts.RoutePlan,
	evaluation strategy.PenaltyEvaluation,
	now time.Time,
	note string,
) (*contracts.AutoSwitchDecision, error) {
	applied, err := o.engine.ApplyScheduling(o.autoCtx(ctx), plan.ID, map[string]bool{base.FromChannelID: false})
	base.DryRunResult = applied
	if err != nil {
		return o.finalizeNote(ctx, base, contracts.AutoSwitchFailed, contracts.RiskLevelL3,
			"hard-failed binding could not be isolated: "+err.Error(), note, nil)
	}
	base.AutoApplied = true
	base.AppliedAt = timePointer(now)
	if leaseErr := o.renewApplyingLease(ctx, base); leaseErr != nil {
		return nil, leaseErr
	}
	if _, circuitErr := o.openQualityCircuit(ctx, plan.ID, base.FromChannelID, evaluation, now); circuitErr != nil {
		return o.finalizeNote(ctx, base, contracts.AutoSwitchFailed, contracts.RiskLevelL3,
			"hard-failed binding was isolated but circuit persistence failed: "+circuitErr.Error(), note, nil)
	}
	return o.finalizeNote(ctx, base, contracts.AutoSwitchCompleted, contracts.RiskLevelL3,
		"hard credential failure was isolated locally", note, nil)
}

// bestDifferentSource returns the highest-scored assigned key whose upstream
// source differs from the failing key. The permanent allocation constraint
// normally prevents same-user duplicate sources; this remains a scheduling
// defense for legacy/imported rows and any future candidate providers.
func bestDifferentSource(from string, eligible []strategy.ScoredCandidate, channels map[string]contracts.UpstreamChannel) string {
	fromChannel, ok := channels[from]
	if !ok {
		return ""
	}
	fromSource := fromChannel.SourceIdentity()
	for _, candidate := range eligible {
		channel, exists := channels[candidate.ChannelID]
		if exists && candidate.ChannelID != from && channel.SourceIdentity() != fromSource {
			return candidate.ChannelID
		}
	}
	return ""
}

// admitHealthyReplacements is the boundary between platform safety and owner
// preference. A preset may reorder only fresh, well-sampled snapshots that the
// metrics layer explicitly classified as healthy. Unknown, degraded, thin, or
// stale channels remain visible to health monitoring but cannot automatically
// receive traffic after a failure.
func admitHealthyReplacements(eligible []strategy.ScoredCandidate, now time.Time) []strategy.ScoredCandidate {
	out := make([]strategy.ScoredCandidate, 0, len(eligible))
	for _, candidate := range eligible {
		snapshot := candidate.Snapshot
		if candidate.Confidence < 1 || snapshot.HealthState != contracts.HealthHealthy || snapshot.CreatedAt.IsZero() {
			continue
		}
		if snapshot.CreatedAt.Before(now.Add(-replacementSnapshotMaxAge)) {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

// Observe advances one observing decision past its window: if the backup proved
// healthy the switch completes; otherwise it is rolled back to the baseline and
// escalated. It is safe to call repeatedly and on non-observing decisions.
func (o *Orchestrator) Observe(ctx context.Context, decisionID string) (*contracts.AutoSwitchDecision, error) {
	d, err := o.store.GetAutoSwitchDecision(ctx, decisionID)
	if err != nil {
		return nil, err
	}
	if d.Status != contracts.AutoSwitchObserving {
		return &d, nil
	}
	now := o.now()
	if d.ObserveUntil != nil && now.Before(*d.ObserveUntil) {
		return &d, nil // still inside the observation window
	}

	// Claim the due observation before any channel/reconcile/notification side
	// effect. A concurrent observer that loses this CAS returns the winner's
	// current state and does no work.
	claim := d
	claim.Status = contracts.AutoSwitchApplying
	leaseUntil := now.Add(autoSwitchApplyingLease)
	claim.LeaseUntil = &leaseUntil
	d, err = o.store.ClaimAutoSwitchObservation(ctx, claim, autoSwitchApplyingLease)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			latest, getErr := o.store.GetAutoSwitchDecision(ctx, decisionID)
			if getErr != nil {
				return nil, getErr
			}
			return &latest, nil
		}
		return nil, err
	}
	// As with Evaluate, a durable observation claim must reach observing again
	// or a terminal status independently of the initiating request lifetime.
	workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), autoSwitchApplyingWorkTimeout)
	defer cancel()
	ctx = o.withApplyingSideEffectGuard(workCtx, d)

	plan, err := o.store.GetRoutePlan(ctx, d.PlanID)
	if err != nil {
		return nil, o.restoreObservationClaim(ctx, d, err)
	}
	strat := o.strategyFor(ctx, plan)

	if o.channelHealthy(ctx, strat, plan, d.ToChannelID) {
		resolved := now
		d.Status = contracts.AutoSwitchCompleted
		d.LeaseUntil = nil
		d.ResolvedAt = &resolved
		d.ObservationNote = "观察期通过，切换生效，备用渠道服务正常"
		if uerr := o.renewApplyingLease(ctx, d); uerr != nil {
			return nil, uerr
		}
		saved, uerr := o.store.TransitionAutoSwitchDecision(ctx, d, contracts.AutoSwitchApplying)
		if uerr != nil {
			return nil, uerr
		}
		o.notifyDecision(ctx, saved, contracts.RiskLevelL1, "自动切换完成")
		return &saved, nil
	}

	// Observation failed: drain the replacement locally. The original source
	// remains quarantined; restoring it here would bypass cooldown and active
	// probe evidence. The plan therefore fails closed until another healthy
	// source is selected or recoverBindings proves the original safe.
	rollback := map[string]bool{}
	if d.ToChannelID != "" {
		rollback[d.ToChannelID] = false
	}
	runCtx := o.autoCtx(ctx)
	rolled, applyErr := o.engine.ApplyScheduling(runCtx, plan.ID, rollback)
	if d.ToChannelID != "" && (applyErr == nil || o.bindingDisabled(ctx, plan.ID, d.ToChannelID)) {
		toEval := ejectionEvaluation(d.ToChannelID, "replacement_observation_failed", "replacement failed its observation window", 0)
		if snap := o.lookupSnapshot(ctx, plan, d.ToChannelID); snap != nil {
			if channel, channelErr := o.store.GetUpstreamChannel(ctx, d.ToChannelID); channelErr == nil {
				toEval = strategy.EvaluatePenalty(strat, strategy.Candidate{Channel: channel, Snapshot: *snap, State: snap.HealthState})
				toEval.Eject = true
				if toEval.Reason.Code == "" {
					toEval.Reason = strategy.Reason{Code: "replacement_observation_failed", Text: "replacement failed its observation window"}
				}
			}
		}
		if leaseErr := o.renewApplyingLease(ctx, d); leaseErr != nil {
			if applyErr == nil {
				applyErr = leaseErr
			} else {
				applyErr = fmt.Errorf("%v; renew applying lease: %w", applyErr, leaseErr)
			}
		} else if _, circuitErr := o.openQualityCircuit(ctx, plan.ID, d.ToChannelID, toEval, now); circuitErr != nil {
			if applyErr == nil {
				applyErr = fmt.Errorf("persist replacement quality circuit: %w", circuitErr)
			} else {
				applyErr = fmt.Errorf("%v; persist replacement quality circuit: %w", applyErr, circuitErr)
			}
		}
	}

	resolved := now
	d.Status = contracts.AutoSwitchRolledBack
	d.LeaseUntil = nil
	d.ResolvedAt = &resolved
	d.DryRunResult = rolled
	if applyErr != nil {
		d.Error = "回滚 apply 失败: " + applyErr.Error()
		d.ObservationNote = "观察期未达标，回滚执行出错，请立即人工介入"
	} else {
		d.ObservationNote = "替代渠道观察未达标，已局部摘除；原渠道仍隔离，当前下游 fail-closed，等待主动探测或其他健康来源"
	}
	if uerr := o.renewApplyingLease(ctx, d); uerr != nil {
		return nil, uerr
	}
	saved, uerr := o.store.TransitionAutoSwitchDecision(ctx, d, contracts.AutoSwitchApplying)
	if uerr != nil {
		return nil, uerr
	}
	o.notifyDecision(ctx, saved, contracts.RiskLevelL3, "自动切换回滚")
	return &saved, nil
}

// ObservePending sweeps every observing decision for a plan (or all plans when
// planID is empty) and advances any whose window has elapsed. It is the entry
// point a background ticker calls.
func (o *Orchestrator) ObservePending(ctx context.Context, planID string) ([]contracts.AutoSwitchDecision, error) {
	decs, err := o.store.ListAutoSwitchDecisions(ctx, contracts.AutoSwitchDecisionFilter{
		PlanID:   planID,
		Statuses: []contracts.AutoSwitchStatus{contracts.AutoSwitchObserving},
	})
	if err != nil {
		return nil, err
	}
	var out []contracts.AutoSwitchDecision
	for _, d := range decs {
		res, err := o.Observe(ctx, d.ID)
		if err != nil {
			return out, err
		}
		if res != nil {
			out = append(out, *res)
		}
	}
	return out, nil
}

// --- internals ---

// strategyFor resolves the effective strategy for a plan. A pinned strategy
// (WithStrategy, used by tests and single-strategy deployments) always wins.
// Otherwise it resolves a persisted strategy by precedence
// plan > pool > user, and finally falls back to the managed default (the
// plan.Labels["strategy"] type under a conservative auto-apply policy). This is
// what lets the platform ship an account-wide default while a single high-value
// plan runs stability-first and a cost-sensitive one runs cost-first.
func (o *Orchestrator) strategyFor(ctx context.Context, plan contracts.RoutePlan) contracts.RouteStrategy {
	if o.strategy != nil {
		return *o.strategy
	}
	if s, ok := o.resolvePersistedStrategy(ctx, plan); ok {
		return s
	}
	typ := contracts.RouteStrategyType(plan.Labels["strategy"])
	if typ == "" {
		typ = contracts.StrategyBalanced
	} else {
		typ = typ.Normalize()
	}
	// Default managed behaviour: smart_auto/balanced with low-risk auto-apply
	// under a conservative cooldown. Owners can override only the preset.

	return contracts.RouteStrategy{
		Type:                   typ,
		AutoApply:              true,
		CooldownSeconds:        600,
		MaxAutoSwitchesPerHour: 6,
	}
}

// resolvePersistedStrategy looks up a stored strategy for the plan by precedence
// plan > pool > user, returning the first match. A store error or no match
// yields ok=false so the caller falls back to the managed default.
func (o *Orchestrator) resolvePersistedStrategy(ctx context.Context, plan contracts.RoutePlan) (contracts.RouteStrategy, bool) {
	lookups := []contracts.RouteStrategyFilter{
		{Scope: contracts.StrategyScopePlan, PlanID: plan.ID},
		{Scope: contracts.StrategyScopePool, PoolID: plan.PoolID},
		{Scope: contracts.StrategyScopeUser, UserID: plan.UserID},
	}
	for _, f := range lookups {
		// Skip a lookup whose owner id is empty (e.g. a plan with no pool).
		if f.PlanID == "" && f.PoolID == "" && f.UserID == 0 {
			continue
		}
		found, err := o.store.ListRouteStrategies(ctx, f)
		if err != nil || len(found) == 0 {
			continue
		}
		return found[0], true
	}
	return contracts.RouteStrategy{}, false
}

func (o *Orchestrator) candidates(ctx context.Context, plan contracts.RoutePlan, channels []contracts.UpstreamChannel, bindings []contracts.PublishedBinding, circuitStates map[string]contracts.QualityCircuitState, recoveredAfter map[string]time.Time) []strategy.Candidate {
	// Compute the decision-window snapshots once, then derive the event-based
	// severe signals (auth/balance/consecutive-failures) and a provider-wide
	// outage flag from the raw observations. The strategy engine hard-gates on
	// these, but a numeric snapshot cannot express them on its own, so without
	// this derivation those gates would never fire.
	snaps := make([]contracts.ChannelHealthSnapshot, len(channels))
	for i := range channels {
		snaps[i] = o.latestSnapshotAfter(ctx, plan, channels[i].ID, recoveredAfter[channels[i].ID])
	}
	signals := o.deriveSignalsForInstanceAfter(ctx, plan.InstanceID, channels, snaps, recoveredAfter)
	bindingState := make(map[string]contracts.PublishedBindingState, len(bindings))
	assigned := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		bindingState[binding.ChannelID] = binding.State
		assigned[binding.ChannelID] = binding.RemoteID != "" &&
			(binding.State == contracts.BindingActive || binding.State == contracts.BindingDisabled)
	}
	locallyEjected := o.locallyEjectedChannels(ctx, plan.ID)
	out := make([]strategy.Candidate, 0, len(channels))
	for i := range channels {
		// Quality scheduling only rotates keys already assigned to this user. An
		// unbound catalog entry has no traffic evidence and must not be allocated
		// opportunistically just because an empty window starts at 100 points.
		if !assigned[channels[i].ID] {
			continue
		}
		sig := signals[channels[i].ID]
		state := snaps[i].HealthState
		// Durable circuit state is the authoritative scheduling gate. Passive
		// snapshots cannot make an open source eligible, and half-open remains
		// probe-only until the recovery FSM closes it.
		switch circuitStates[channels[i].ID] {
		case contracts.QualityCircuitOpen:
			state = contracts.HealthQuarantined
		case contracts.QualityCircuitHalfOpen:
			state = contracts.HealthRecovering
		}
		// A disabled binding is an open circuit. Missing traffic after ejection
		// must not make it look like a fresh 100-point candidate; only
		// recoverBindings may re-admit it after strict active probes.
		if !qualityCircuitBlocksScheduling(circuitStates[channels[i].ID]) && bindingState[channels[i].ID] == contracts.BindingDisabled && locallyEjected[channels[i].ID] {
			state = contracts.HealthQuarantined
		}
		out = append(out, strategy.Candidate{
			Channel:             channels[i],
			Snapshot:            snaps[i],
			State:               state,
			AuthFailure:         sig.authFailure,
			InsufficientBalance: sig.insufficientBalance,
			ConsecutiveFailures: sig.consecutiveFailures,
			ProviderDown:        sig.providerDown,
		})
	}
	return out
}

func (o *Orchestrator) locallyEjectedChannels(ctx context.Context, planID string) map[string]bool {
	out := map[string]bool{}
	decisions, err := o.store.ListAutoSwitchDecisions(ctx, contracts.AutoSwitchDecisionFilter{PlanID: planID, Limit: 100})
	if err != nil {
		return out
	}
	for _, decision := range decisions {
		if decision.AutoApplied && decision.AppliedAt != nil && decision.FromChannelID != "" {
			out[decision.FromChannelID] = true
		}
	}
	return out
}

// severeFailureStreak is the leading consecutive-failure count at which a channel
// is treated as failing for provider-outage aggregation (mirrors the strategy
// default ConsecutiveFailureLimit; the strategy still applies its own limit for
// the hard gate).
const severeFailureStreak = 3

// signalWindow is how far back severe-signal derivation reads raw observations.
// It matches the primary 5m decision window.
const signalWindow = 5 * time.Minute

// channelSignals holds the event-based signals derived for one channel.
type channelSignals struct {
	authFailure         bool
	insufficientBalance bool
	consecutiveFailures int
	providerDown        bool
}

// deriveSignals reads each channel's recent raw observations and derives the
// severe-failure signals the strategy engine gates on, plus a provider-wide
// outage flag. The consecutive-failure streak and auth/balance flags are taken
// from the leading (most-recent) run of failures so a channel that already
// recovered is not gated. A provider is marked down only when it has at least
// two decided channels and every one of them is failing, so a single failing
// channel is never mistaken for a provider-wide outage.
func (o *Orchestrator) deriveSignals(ctx context.Context, channels []contracts.UpstreamChannel, snaps []contracts.ChannelHealthSnapshot) map[string]channelSignals {
	return o.deriveSignalsForInstance(ctx, "", channels, snaps)
}

func (o *Orchestrator) deriveSignalsForInstance(ctx context.Context, instanceID string, channels []contracts.UpstreamChannel, snaps []contracts.ChannelHealthSnapshot) map[string]channelSignals {
	return o.deriveSignalsForInstanceAfter(ctx, instanceID, channels, snaps, nil)
}

func (o *Orchestrator) deriveSignalsForInstanceAfter(ctx context.Context, instanceID string, channels []contracts.UpstreamChannel, snaps []contracts.ChannelHealthSnapshot, recoveredAfter map[string]time.Time) map[string]channelSignals {
	windowStart := o.now().Add(-signalWindow)
	out := make(map[string]channelSignals, len(channels))
	type provAgg struct{ decided, failing int }
	prov := make(map[string]*provAgg)

	for i := range channels {
		ch := channels[i]
		sig := channelSignals{}
		since := windowStart
		if watermark := recoveredAfter[ch.ID]; watermark.After(since) {
			since = watermark
		}
		obs, err := o.store.ListChannelObservations(ctx, contracts.ChannelObservationFilter{
			ChannelID: ch.ID, InstanceID: instanceID, Since: since, Limit: 200,
		})
		if err == nil && len(obs) > 0 {
			// The store returns observations newest-first; walk the leading run
			// of failures to get the live streak and any severe error in it.
		observationLoop:
			for _, ob := range obs {
				if ob.Success {
					break
				}
				switch ob.ErrorType {
				case contracts.ErrorAuth:
					sig.authFailure = true
				case contracts.ErrorInsufficientBalance:
					sig.insufficientBalance = true
				case contracts.ErrorTimeout, contracts.ErrorRateLimit, contracts.ErrorServer,
					contracts.ErrorNetwork, contracts.ErrorUnknown:
					sig.consecutiveFailures++
				default:
					// Client/cancelled failures are not upstream quality. They
					// terminate the leading upstream-failure streak without
					// making the binding look broken.
					break observationLoop
				}
			}
		}
		out[ch.ID] = sig

		// Provider aggregation: a channel counts once it has a decided verdict
		// (a non-unknown snapshot state or a live failure streak). "Failing" is a
		// severe signal, an unhealthy snapshot, or a full sampled failure streak.
		decided := snaps[i].HealthState != contracts.HealthUnknown && snaps[i].HealthState != "" ||
			sig.consecutiveFailures > 0
		if ch.Provider == "" || !decided {
			continue
		}
		p := prov[ch.Provider]
		if p == nil {
			p = &provAgg{}
			prov[ch.Provider] = p
		}
		p.decided++
		failing := sig.authFailure || sig.insufficientBalance ||
			sig.consecutiveFailures >= severeFailureStreak ||
			snaps[i].HealthState == contracts.HealthUnhealthy
		if failing {
			p.failing++
		}
	}

	// Second pass: stamp provider-wide outage onto every channel of a provider
	// whose decided channels are all failing (and there are at least two).
	for i := range channels {
		ch := channels[i]
		if ch.Provider == "" {
			continue
		}
		if p := prov[ch.Provider]; p != nil && p.decided >= 2 && p.failing == p.decided {
			sig := out[ch.ID]
			sig.providerDown = true
			out[ch.ID] = sig
		}
	}
	return out
}

func (o *Orchestrator) latestSnapshot(ctx context.Context, plan contracts.RoutePlan, channelID string) contracts.ChannelHealthSnapshot {
	return o.latestSnapshotAfter(ctx, plan, channelID, time.Time{})
}

// recoveryEvidenceWatermark returns the earliest traffic timestamp that may
// contribute to a post-recovery quality verdict. Guarded automatic recovery
// keeps its existing probe/stage boundary. Manual recovery has no probe proof,
// so its durable close transition is only a freshness boundary: it makes no
// claim that the source is healthy and fresh passive failures still count.
func recoveryEvidenceWatermark(runtime contracts.QualityCircuitRuntime) time.Time {
	if runtime.State != contracts.QualityCircuitClosed {
		return time.Time{}
	}
	if runtime.RecoveryReady && runtime.RecoveryStageStartedAt != nil {
		return runtime.RecoveryStageStartedAt.UTC()
	}
	switch runtime.LastReason.Code {
	case strategy.CircuitReasonRestored:
		if runtime.LastProbeAt != nil {
			return runtime.LastProbeAt.UTC()
		}
	case "manual_recovery_completed":
		if runtime.LastTransitionAt != nil {
			return runtime.LastTransitionAt.UTC()
		}
	}
	return time.Time{}
}

// latestSnapshotAfter ignores every aggregate bucket whose underlying rolling
// window can still contain pre-recovery traffic. A restored source therefore
// starts with unknown quality and can only be deducted again from fresh facts.
func (o *Orchestrator) latestSnapshotAfter(ctx context.Context, plan contracts.RoutePlan, channelID string, watermark time.Time) contracts.ChannelHealthSnapshot {
	filter := contracts.ChannelHealthSnapshotFilter{
		ChannelID: channelID, InstanceID: plan.InstanceID,
		Window: decisionWindow,
	}
	if !watermark.IsZero() {
		filter.Since = watermark.Add(decisionWindow.Duration())
	}
	snaps, err := o.store.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{
		ChannelID: filter.ChannelID, InstanceID: filter.InstanceID,
		Window: filter.Window, Since: filter.Since,
	})
	if err != nil || len(snaps) == 0 {
		return contracts.ChannelHealthSnapshot{ChannelID: channelID, InstanceID: plan.InstanceID, Window: decisionWindow, HealthState: contracts.HealthUnknown}
	}
	return conservativeSnapshot(snaps)
}

// channelHealthy reports whether a channel's latest decision-window snapshot is
// eligible under the strategy AND reads healthy (not merely "unknown"). The
// observation bar is deliberately strict: a switch is only confirmed when the
// backup is provably serving well.
func (o *Orchestrator) channelHealthy(ctx context.Context, strat contracts.RouteStrategy, plan contracts.RoutePlan, channelID string) bool {
	if channelID == "" {
		return false
	}
	ch, err := o.store.GetUpstreamChannel(ctx, channelID)
	if err != nil {
		return false
	}
	snap := o.latestSnapshot(ctx, plan, channelID)
	r := strategy.RankByPenalty(strat, []strategy.Candidate{{Channel: ch, Snapshot: snap, State: snap.HealthState}})
	if len(r.Eligible) == 0 {
		return false
	}
	return snap.HealthState == contracts.HealthHealthy
}

func (o *Orchestrator) autoCtx(ctx context.Context) context.Context {
	ctx = contracts.WithReconcileTrigger(ctx, contracts.ReconcileTriggerAuto)
	return contracts.WithActor(ctx, autoActor)
}

func (o *Orchestrator) renewApplyingLease(ctx context.Context, decision contracts.AutoSwitchDecision) error {
	_, err := o.store.RenewAutoSwitchDecisionLease(
		ctx, decision.ID, decision.LeaseVersion, autoSwitchApplyingLease,
	)
	return err
}

func (o *Orchestrator) withApplyingSideEffectGuard(ctx context.Context, decision contracts.AutoSwitchDecision) context.Context {
	ctx = contracts.WithGatewaySchedulingFence(ctx, contracts.GatewaySchedulingFence{
		Scope: "auto-switch/plan/" + decision.PlanID, Version: decision.SchedulingGeneration,
	})
	return contracts.WithReconcileSideEffectGuard(ctx, func(guardCtx context.Context) error {
		plan, err := o.store.GetRoutePlan(guardCtx, decision.PlanID)
		if err != nil {
			return err
		}
		if plan.Status != contracts.RoutePlanPublished || plan.SchedulingGeneration != decision.SchedulingGeneration {
			return store.ErrConflict
		}
		return o.renewApplyingLease(guardCtx, decision)
	})
}

func (o *Orchestrator) observationWindow(strat contracts.RouteStrategy) time.Duration {
	if o.obsWindow > 0 {
		return o.obsWindow
	}
	if strat.RecoveryObservationSeconds > 0 {
		return time.Duration(strat.RecoveryObservationSeconds) * time.Second
	}
	return DefaultObservationWindow
}

// switchHistory returns the last auto-applied switch time and the applied times
// within the trailing hour, for dampening.
func (o *Orchestrator) switchHistory(ctx context.Context, planID string, now time.Time) (time.Time, []time.Time) {
	decs, err := o.store.ListAutoSwitchDecisions(ctx, contracts.AutoSwitchDecisionFilter{PlanID: planID, Limit: 100})
	if err != nil {
		return time.Time{}, nil
	}
	var last time.Time
	var recent []time.Time
	hourAgo := now.Add(-time.Hour)
	for _, d := range decs {
		if !d.AutoApplied || d.AppliedAt == nil {
			continue
		}
		if d.AppliedAt.After(last) {
			last = *d.AppliedAt
		}
		if d.AppliedAt.After(hourAgo) {
			recent = append(recent, *d.AppliedAt)
		}
	}
	return last, recent
}

func (o *Orchestrator) activeByFingerprint(ctx context.Context, planID, fp string) (*contracts.AutoSwitchDecision, bool) {
	d, err := o.store.FindActiveAutoSwitchDecisionByFingerprint(ctx, planID, fp)
	if err != nil {
		return nil, false
	}
	return &d, true
}

// restoreObservationClaim releases an observation claim when execution failed
// before reconcile was attempted, allowing the runner to retry safely. Once an
// Apply has been attempted, Observe records a terminal rolled_back decision
// instead and never releases the claim.
func (o *Orchestrator) restoreObservationClaim(ctx context.Context, d contracts.AutoSwitchDecision, cause error) error {
	d.Status = contracts.AutoSwitchObserving
	d.LeaseUntil = nil
	if err := o.renewApplyingLease(ctx, d); err != nil {
		return fmt.Errorf("%w (renew observation claim: %v)", cause, err)
	}
	if _, err := o.store.TransitionAutoSwitchDecision(ctx, d, contracts.AutoSwitchApplying); err != nil {
		return fmt.Errorf("%w (restore observation claim: %v)", cause, err)
	}
	return cause
}

// finalize persists a terminal/observing decision and dispatches a notification.
func (o *Orchestrator) finalize(ctx context.Context, base contracts.AutoSwitchDecision, status contracts.AutoSwitchStatus, risk contracts.RiskLevel, reason string, prior map[string]contracts.UpstreamChannelStatus) (*contracts.AutoSwitchDecision, error) {
	return o.finalizeNote(ctx, base, status, risk, reason, "", prior)
}

func (o *Orchestrator) finalizeNote(ctx context.Context, base contracts.AutoSwitchDecision, status contracts.AutoSwitchStatus, risk contracts.RiskLevel, reason, note string, prior map[string]contracts.UpstreamChannelStatus) (*contracts.AutoSwitchDecision, error) {
	base.Status = status
	base.LeaseUntil = nil
	if base.RiskLevel == "" {
		base.RiskLevel = risk
	}
	if base.RiskReason == "" {
		base.RiskReason = reason
	}
	if note != "" {
		base.ObservationNote = note
	}
	if status == contracts.AutoSwitchFailed {
		base.Error = reason
	}
	if isTerminal(status) && base.ResolvedAt == nil {
		now := o.now()
		base.ResolvedAt = &now
	}
	if err := o.renewApplyingLease(ctx, base); err != nil {
		return nil, err
	}
	saved, err := o.store.TransitionAutoSwitchDecision(ctx, base, contracts.AutoSwitchApplying)
	if err != nil {
		return nil, err
	}
	o.notifyDecision(ctx, saved, notifyRisk(status, risk), notifyTitle(status))
	return &saved, nil
}

func (o *Orchestrator) notifyDecision(ctx context.Context, d contracts.AutoSwitchDecision, risk contracts.RiskLevel, title string) {
	if o.eventSink != nil {
		o.eventSink(ctx, d)
	}
	if o.notifier == nil {
		return
	}
	routes, err := o.store.ListNotificationRoutes(ctx, d.UserID)
	if err != nil || len(routes) == 0 {
		return
	}
	// Owner notifications deliberately carry no internal identity or catalog
	// names. Only anonymous before/after facts and the lifecycle outcome leave
	// the control plane. Event sinks receive the decision and are responsible
	// for applying an audience-appropriate projection before publishing it.
	plan, _ := o.store.GetRoutePlan(ctx, d.PlanID)
	fromSnap := o.lookupSnapshot(ctx, plan, d.FromChannelID)
	toSnap := o.lookupSnapshot(ctx, plan, d.ToChannelID)
	notice := notify.AutoSwitchNotice{
		Status:          strings.TrimPrefix(title, "自动切换"),
		StrategyName:    "平台质量调度",
		FromChannel:     "原调度来源",
		ToChannel:       anonymousTarget(d),
		ObservationNote: anonymousOutcome(d),
	}
	fillNoticeMetrics(&notice, fromSnap, toSnap)
	ev := notify.Event{
		UserID:     d.UserID,
		EventLevel: autoSwitchEventLevel(d.Status),
		RiskLevel:  risk,
		Result:     autoSwitchNotificationResult(d.Status),
		Title:      notify.AutoSwitchTitle(strings.TrimSpace(notice.Status)),
		Text:       notify.BuildAutoSwitchText(notice),
		Fields: map[string]string{
			"status":      string(d.Status),
			"autoApplied": fmt.Sprintf("%t", d.AutoApplied),
			"riskLevel":   string(risk),
		},
	}
	o.notifier.DispatchAll(ctx, ev, routes)
}

func anonymousTarget(d contracts.AutoSwitchDecision) string {
	if d.ToChannelID == "" {
		return "无可用替代来源"
	}
	return "替代调度来源"
}

func anonymousOutcome(d contracts.AutoSwitchDecision) string {
	switch d.Status {
	case contracts.AutoSwitchCompleted:
		return "质量调度已完成"
	case contracts.AutoSwitchObserving:
		return "已自动执行，正在观察恢复结果"
	case contracts.AutoSwitchRolledBack:
		return "观察未通过，已执行局部隔离"
	case contracts.AutoSwitchSkipped:
		return "本轮未执行调度变更"
	case contracts.AutoSwitchProposed:
		return "已记录质量风险，暂未执行调度变更"
	case contracts.AutoSwitchApproved:
		return "管理员已批准该调度变更，等待安全执行"
	case contracts.AutoSwitchRejected:
		return "管理员已拒绝该调度变更，本次未改变线路"
	default:
		return "质量调度执行失败"
	}
}

// instanceName resolves a friendly instance name for notifications, falling back to
// the instance id (and never failing the alert on a store miss).
func (o *Orchestrator) instanceName(ctx context.Context, instanceID string) string {
	if instanceID == "" {
		return ""
	}
	inst, err := o.store.GetInstance(ctx, instanceID)
	if err != nil || inst.Name == "" {
		return instanceID
	}
	return inst.Name
}

// lookupChannel returns a channel by id, or nil on any miss (notification
// rendering is best-effort and must never fail the alert).
func (o *Orchestrator) lookupChannel(ctx context.Context, id string) *contracts.UpstreamChannel {
	if id == "" {
		return nil
	}
	ch, err := o.store.GetUpstreamChannel(ctx, id)
	if err != nil {
		return nil
	}
	return &ch
}

// lookupSnapshot returns a channel's latest decision-window snapshot, or nil
// when there is no id or no snapshot exists yet.
func (o *Orchestrator) lookupSnapshot(ctx context.Context, plan contracts.RoutePlan, id string) *contracts.ChannelHealthSnapshot {
	if id == "" {
		return nil
	}
	snap := o.latestSnapshot(ctx, plan, id)
	if snap.SampleCount == 0 && snap.HealthState == contracts.HealthUnknown {
		return nil
	}
	return &snap
}

// fillNoticeMetrics copies before/after quality metrics from the from/to
// snapshots into the notice. A nil snapshot leaves those fields nil ("unknown").
func fillNoticeMetrics(n *notify.AutoSwitchNotice, from, to *contracts.ChannelHealthSnapshot) {
	if from != nil {
		n.FromSuccessRate = ptrFloat(from.SuccessRate)
		n.FromTTFTP95 = ptrFloat(from.TTFTP95)
		n.FromDurationP95 = ptrFloat(from.DurationP95)
	}
	if to != nil {
		n.ToSuccessRate = ptrFloat(to.SuccessRate)
		n.ToTTFTP95 = ptrFloat(to.TTFTP95)
		n.ToDurationP95 = ptrFloat(to.DurationP95)
	}
}

func ptrFloat(v float64) *float64 { return &v }

// strategyDisplayName maps a strategy type onto its human (Chinese) name.
func strategyDisplayName(t contracts.RouteStrategyType) string {
	switch t.Normalize() {
	case contracts.StrategyCostFirst:
		return "成本优先"
	case contracts.StrategyLatencyFirst:
		return "延迟优先"
	case contracts.StrategyBalanced:
		return "均衡"
	default:
		return "稳定优先"
	}
}

// channelDisplayName prefers a channel's display name, falling back to its id.
func channelDisplayName(ch *contracts.UpstreamChannel) string {
	if ch == nil {
		return ""
	}
	if ch.DisplayName != "" {
		return ch.DisplayName
	}
	return ch.ID
}

// --- pure helpers ---

// firstLiveExcluded returns the deterministically-first live channel that the
// strategy gated out, or "" if none.
func firstLiveExcluded(excluded []strategy.ExcludedCandidate, live map[string]bool) string {
	ids := make([]string, 0, len(excluded))
	for _, ex := range excluded {
		if live[ex.ChannelID] {
			ids = append(ids, ex.ChannelID)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return ids[0]
}

// penaltyEvaluationFor projects the ranking's persisted explanation back into
// the penalty form used by the orchestrator. Excluded entries retain the full
// deduction breakdown; eligible entries supply the replacement score.
func penaltyEvaluationFor(r strategy.Ranking, channelID string) (strategy.PenaltyEvaluation, bool) {
	if channelID == "" {
		return strategy.PenaltyEvaluation{}, false
	}
	for _, ex := range r.Excluded {
		if ex.ChannelID != channelID {
			continue
		}
		eval := strategy.PenaltyEvaluation{
			ChannelID: ex.ChannelID, Score: ex.Score, Eject: true,
			HardFailure: ex.HardFailure, Reason: ex.Reason, Snapshot: ex.Snapshot,
		}
		if ex.Penalties != nil {
			eval.Penalties = *ex.Penalties
		}
		return eval, true
	}
	for _, eligible := range r.Eligible {
		if eligible.ChannelID != channelID {
			continue
		}
		eval := strategy.PenaltyEvaluation{
			ChannelID: eligible.ChannelID, Score: eligible.Score,
			Evidence: eligible.Confidence, Snapshot: eligible.Snapshot,
		}
		if eligible.Penalties != nil {
			eval.Penalties = *eligible.Penalties
		}
		return eval, true
	}
	return strategy.PenaltyEvaluation{}, false
}

func scoreOrZero(eval strategy.PenaltyEvaluation, ok bool) float64 {
	if !ok {
		return 0
	}
	return eval.Score
}

func penaltyTriggerText(channelID string, fallback strategy.Reason, eval strategy.PenaltyEvaluation, ok bool) string {
	if !ok {
		return triggerText(channelID, fallback)
	}
	p := eval.Penalties
	return fmt.Sprintf(
		"渠道 %s 质量分 %.1f = 100 - %.1f（上游错误扣 %.1f，首字耗时扣 %.1f，总耗时扣 %.1f）；%s",
		channelID, eval.Score, p.TotalPenalty, p.ErrorPenalty, p.TTFTPenalty, p.DurationPenalty, fallback.Text,
	)
}

func penaltyRiskText(base string, from, to strategy.PenaltyEvaluation, hasTo bool) string {
	if hasTo {
		return fmt.Sprintf("%s；来源质量分 %.1f，目标质量分 %.1f", base, from.Score, to.Score)
	}
	return fmt.Sprintf("%s；来源质量分 %.1f", base, from.Score)
}

// qualityEjectionPercentage progressively limits collateral impact from a soft
// quality event as consecutive bad metric buckets accumulate. A single sweep
// never selects every downstream.
func (o *Orchestrator) consecutiveBadWindows(ctx context.Context, plan contracts.RoutePlan, strat contracts.RouteStrategy, channelID string) int {
	snaps, err := o.store.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{
		ChannelID: channelID, InstanceID: plan.InstanceID, Window: decisionWindow,
		IncludeHistory: true, Since: o.now().Add(-30 * time.Minute),
	})
	if err != nil || len(snaps) == 0 {
		return 1
	}
	channel, err := o.store.GetUpstreamChannel(ctx, channelID)
	if err != nil {
		return 1
	}
	streak := 0
	for _, group := range strategy.IndependentWindowBuckets(snaps, decisionWindow.Duration()) {
		snap := conservativeSnapshot(group)
		eval := strategy.EvaluatePenalty(strat, strategy.Candidate{Channel: channel, Snapshot: snap, State: snap.HealthState})
		if !eval.Eject {
			break
		}
		streak++
	}
	if streak == 0 {
		return 1
	}
	return streak
}

// SourceQualityCohort returns the execution cohort for one stable source. Only
// published active bindings whose latest instance-scoped 5m score has crossed
// the soft penalty threshold can join a new ejection batch. Healthy/unknown
// active bindings remain real traffic observers without consuming a slot, while
// durable quality-isolated bindings keep earlier batches stable across sweeps
// and restarts. Suspended and manually-disabled bindings are excluded.
//
// known=false is fail-safe: a caller must not apply a soft source-wide action
// when catalog, plan, strategy, snapshot, circuit, or decision facts could not
// be enumerated consistently.
func (o *Orchestrator) SourceQualityCohort(ctx context.Context, sourceID string, percentage int) (map[string]bool, bool) {
	if strings.TrimSpace(sourceID) == "" {
		return nil, false
	}
	channels, err := o.store.ListUpstreamChannels(ctx, "")
	if err != nil {
		return nil, false
	}
	sameSource := make(map[string]contracts.UpstreamChannel)
	for _, channel := range channels {
		if channel.SourceIdentity() == sourceID {
			sameSource[channel.ID] = channel
		}
	}
	plans, err := o.store.ListRoutePlans(ctx, 0)
	if err != nil {
		return nil, false
	}
	affectedPlanIDs := make([]string, 0, len(plans))
	observerPlanIDs := make([]string, 0, len(plans))
	isolatedPlanIDs := make([]string, 0, len(plans))
	for _, candidate := range plans {
		if candidate.Status != contracts.RoutePlanPublished {
			continue
		}
		runtimes, runtimeErr := o.store.ListQualityCircuitRuntimes(ctx, contracts.QualityCircuitRuntimeFilter{PlanID: candidate.ID})
		if runtimeErr != nil {
			return nil, false
		}
		runtimeByChannel := make(map[string]contracts.QualityCircuitRuntime, len(runtimes))
		for _, runtime := range runtimes {
			runtimeByChannel[runtime.ChannelID] = runtime
		}
		bindings, listErr := o.store.ListPublishedBindings(ctx, candidate.ID)
		if listErr != nil {
			return nil, false
		}
		activeChannelIDs := make([]string, 0, 1)
		isolated := false
		var decisions []contracts.AutoSwitchDecision
		decisionsLoaded := false
		for _, binding := range bindings {
			if _, matchesSource := sameSource[binding.ChannelID]; !matchesSource || strings.TrimSpace(binding.RemoteID) == "" {
				continue
			}
			runtime, hasRuntime := runtimeByChannel[binding.ChannelID]
			if hasRuntime && qualityCircuitBlocksScheduling(runtime.State) {
				isolated = true
				continue
			}
			switch {
			case binding.State == contracts.BindingActive:
				activeChannelIDs = append(activeChannelIDs, binding.ChannelID)
			case binding.State == contracts.BindingDisabled:
				// Reconcile and circuit persistence straddle a remote side effect.
				// During that narrow crash window the durable decision proves this
				// disabled binding belongs to the incident, rather than to a manual
				// operator action. Use the same freshness rule as circuit repair.
				if !decisionsLoaded {
					decisions, listErr = o.store.ListAutoSwitchDecisions(ctx, contracts.AutoSwitchDecisionFilter{PlanID: candidate.ID, Limit: 100})
					if listErr != nil {
						return nil, false
					}
					decisionsLoaded = true
				}
				_, ejectedAt, hasDecision := fallbackEjectionDecision(decisions, binding.ChannelID)
				if !hasDecision {
					continue
				}
				if hasRuntime {
					transitionedAt := runtime.UpdatedAt
					if runtime.LastTransitionAt != nil {
						transitionedAt = *runtime.LastTransitionAt
					}
					if !transitionedAt.Before(ejectedAt) {
						continue
					}
				}
				isolated = true
			}
		}
		if len(activeChannelIDs) > 0 {
			strat, strategyErr := o.strategyForQualityCohort(ctx, candidate)
			if strategyErr != nil {
				return nil, false
			}
			affected := false
			for _, channelID := range activeChannelIDs {
				runtime := runtimeByChannel[channelID]
				watermark := recoveryEvidenceWatermark(runtime)
				snapshot, snapshotErr := o.latestSnapshotAfterForQualityCohort(ctx, candidate, channelID, watermark)
				if snapshotErr != nil {
					return nil, false
				}
				evaluation := strategy.EvaluatePenalty(strat, strategy.Candidate{
					Channel: sameSource[channelID], Snapshot: snapshot, State: snapshot.HealthState,
				})
				switch {
				case evaluation.Eject && !evaluation.HardFailure && evaluation.Reason.Code == strategy.GatePenaltyThreshold:
					affected = true
				case evaluation.Eject:
					// Credential/lifecycle gates are not a soft source incident.
					// Let their binding-local or operator-owned path resolve first.
					return nil, false
				}
			}
			if affected {
				affectedPlanIDs = append(affectedPlanIDs, candidate.ID)
			} else {
				observerPlanIDs = append(observerPlanIDs, candidate.ID)
			}
		} else if isolated {
			isolatedPlanIDs = append(isolatedPlanIDs, candidate.ID)
		}
	}
	return strategy.StableQualityAffectedIncidentCohortPlanIDs(
		affectedPlanIDs, observerPlanIDs, isolatedPlanIDs, sourceID, percentage,
	), true
}

func (o *Orchestrator) sourceQualityCohort(ctx context.Context, sourceID string, percentage int) (map[string]bool, bool) {
	return o.SourceQualityCohort(ctx, sourceID, percentage)
}

func (o *Orchestrator) strategyForQualityCohort(ctx context.Context, plan contracts.RoutePlan) (contracts.RouteStrategy, error) {
	if o.strategy != nil {
		return *o.strategy, nil
	}
	lookups := []contracts.RouteStrategyFilter{
		{Scope: contracts.StrategyScopePlan, PlanID: plan.ID},
		{Scope: contracts.StrategyScopePool, PoolID: plan.PoolID},
		{Scope: contracts.StrategyScopeUser, UserID: plan.UserID},
	}
	for _, filter := range lookups {
		if filter.PlanID == "" && filter.PoolID == "" && filter.UserID == 0 {
			continue
		}
		found, err := o.store.ListRouteStrategies(ctx, filter)
		if err != nil {
			return contracts.RouteStrategy{}, err
		}
		if len(found) > 0 {
			return found[0], nil
		}
	}
	return contracts.RouteStrategy{
		Type:                   contracts.RouteStrategyType(plan.Labels["strategy"]).Normalize(),
		AutoApply:              true,
		CooldownSeconds:        600,
		MaxAutoSwitchesPerHour: 6,
	}, nil
}

func (o *Orchestrator) latestSnapshotAfterForQualityCohort(
	ctx context.Context,
	plan contracts.RoutePlan,
	channelID string,
	watermark time.Time,
) (contracts.ChannelHealthSnapshot, error) {
	filter := contracts.ChannelHealthSnapshotFilter{
		ChannelID: channelID, InstanceID: plan.InstanceID, Window: decisionWindow,
	}
	if !watermark.IsZero() {
		filter.Since = watermark.Add(decisionWindow.Duration())
	}
	snapshots, err := o.store.ListChannelHealthSnapshots(ctx, filter)
	if err != nil {
		return contracts.ChannelHealthSnapshot{}, err
	}
	if len(snapshots) == 0 {
		return contracts.ChannelHealthSnapshot{
			ChannelID: channelID, InstanceID: plan.InstanceID,
			Window: decisionWindow, HealthState: contracts.HealthUnknown,
		}, nil
	}
	return conservativeSnapshot(snapshots), nil
}

// conservativeSnapshot combines the current per-model rows for one concrete
// instance/channel. RoutePlan has no model selector yet, so selecting the newest
// arbitrary model could hide a bad model. The aggregate preserves the newest
// timestamp while taking the worst quality dimension across models.
func conservativeSnapshot(snaps []contracts.ChannelHealthSnapshot) contracts.ChannelHealthSnapshot {
	out := snaps[0]
	latest := snaps[0].CreatedAt
	out.Model = ""
	for i := 1; i < len(snaps); i++ {
		snap := snaps[i]
		if snap.InstanceID != out.InstanceID {
			continue
		}
		out.SampleCount += snap.SampleCount
		out.QualitySampleCount += snap.QualitySampleCount
		out.UpstreamFailureCount += snap.UpstreamFailureCount
		out.AuthFailureCount += snap.AuthFailureCount
		out.InsufficientBalanceCount += snap.InsufficientBalanceCount
		out.SuccessRate = minFloat(out.SuccessRate, snap.SuccessRate)
		out.QualitySuccessRate = minFloat(out.QualitySuccessRate, snap.QualitySuccessRate)
		out.QualityErrorRate = maxFloat(out.QualityErrorRate, snap.QualityErrorRate)
		out.UpstreamErrorRate = maxFloat(out.UpstreamErrorRate, snap.UpstreamErrorRate)
		out.ErrorRate = maxFloat(out.ErrorRate, snap.ErrorRate)
		out.TimeoutRate = maxFloat(out.TimeoutRate, snap.TimeoutRate)
		out.RateLimitRate = maxFloat(out.RateLimitRate, snap.RateLimitRate)
		out.TTFTP50 = maxFloat(out.TTFTP50, snap.TTFTP50)
		out.TTFTP95 = maxFloat(out.TTFTP95, snap.TTFTP95)
		out.DurationP50 = maxFloat(out.DurationP50, snap.DurationP50)
		out.DurationP95 = maxFloat(out.DurationP95, snap.DurationP95)
		out.SuccessScore = minFloat(out.SuccessScore, snap.SuccessScore)
		out.TTFTScore = minFloat(out.TTFTScore, snap.TTFTScore)
		out.DurationScore = minFloat(out.DurationScore, snap.DurationScore)
		out.StabilityScore = minFloat(out.StabilityScore, snap.StabilityScore)
		out.HealthScore = minFloat(out.HealthScore, snap.HealthScore)
		out.QualityScore = minFloat(out.QualityScore, snap.QualityScore)
		out.RiskScore = maxFloat(out.RiskScore, snap.RiskScore)
		out.HealthState = worseHealthState(out.HealthState, snap.HealthState)
		if snap.CreatedAt.After(latest) {
			latest = snap.CreatedAt
			out.CreatedAt = snap.CreatedAt
			out.BucketStart = snap.BucketStart
		}
	}
	return out
}

func minFloat(a, b float64) float64 {
	if b < a {
		return b
	}
	return a
}

func maxFloat(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}

func worseHealthState(a, b contracts.HealthState) contracts.HealthState {
	rank := map[contracts.HealthState]int{
		contracts.HealthUnknown: 0, contracts.HealthHealthy: 1, contracts.HealthDegraded: 2,
		contracts.HealthRecovering: 3, contracts.HealthUnhealthy: 4, contracts.HealthQuarantined: 5,
	}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// analyzeSwitchRollout inspects a dry-run action set for canary safety. It
// reports whether the intended backup (to) was held back by the rollout policy,
// and how many channels this apply would newly bring into scheduling (enable or
// create). A held backup means the switch would drain the failing channel
// without promoting the backup; more than one newly-online channel means the
// apply is broader than a single-channel canary.
func analyzeSwitchRollout(actions []contracts.ReconcileAction, to string) (toHeld bool, onlineCount int) {
	for _, a := range actions {
		switch a.Type {
		case contracts.ReconcileEnable, contracts.ReconcileCreate:
			onlineCount++
		case contracts.ReconcileHold:
			if a.ChannelID == to {
				toHeld = true
			}
		}
	}
	return toHeld, onlineCount
}

// gradeRisk maps a dry-run action set + backup availability onto a RiskLevel.
// create/deprovision or a missing backup are high; update/revoke are mid; a pure
// enable/disable/hold/noop switch is low and auto-appliable.
func gradeRisk(actions []contracts.ReconcileAction, hasBackup bool) (contracts.RiskLevel, string) {
	if !hasBackup {
		return contracts.RiskLevelL3, "无可用备用渠道"
	}
	mid := false
	for _, a := range actions {
		switch a.Type {
		case contracts.ReconcileCreate, contracts.ReconcileDeprovision:
			return contracts.RiskLevelL3, "变更包含新建/删除渠道等高风险动作，需人工处理"
		case contracts.ReconcileUpdate, contracts.ReconcileRevoke:
			mid = true
		}
	}
	if mid {
		return contracts.RiskLevelL2, "变更包含渠道配置或撤销，需人工确认"
	}
	return contracts.RiskLevelL1, "仅包含启用/停用等低风险动作，可自动灰度执行"
}

func fingerprint(planID, from, to string, typ contracts.RouteStrategyType) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s", planID, from, to, typ)
	return hex.EncodeToString(h.Sum(nil))
}

func triggerText(from string, r strategy.Reason) string {
	if r.Text == "" {
		return fmt.Sprintf("渠道 %s 健康度下降，触发自动切换", from)
	}
	return fmt.Sprintf("渠道 %s %s", from, r.Text)
}

func isTerminal(s contracts.AutoSwitchStatus) bool {
	switch s {
	case contracts.AutoSwitchCompleted, contracts.AutoSwitchRolledBack, contracts.AutoSwitchSkipped,
		contracts.AutoSwitchRejected, contracts.AutoSwitchFailed:
		return true
	default:
		return false
	}
}

// notifyRisk lifts a decision onto the notify risk scale: terminal failures and
// rollbacks alert loudest; proposals need a human; the rest are informational.
func notifyRisk(status contracts.AutoSwitchStatus, risk contracts.RiskLevel) contracts.RiskLevel {
	switch status {
	case contracts.AutoSwitchFailed, contracts.AutoSwitchRolledBack:
		return contracts.RiskLevelL3
	case contracts.AutoSwitchProposed, contracts.AutoSwitchApproved:
		if risk == contracts.RiskLevelL3 {
			return contracts.RiskLevelL3
		}
		return contracts.RiskLevelL2
	case contracts.AutoSwitchSkipped:
		return risk
	default:
		return contracts.RiskLevelL1
	}
}

func autoSwitchEventLevel(status contracts.AutoSwitchStatus) contracts.EventLevel {
	switch status {
	case contracts.AutoSwitchFailed, contracts.AutoSwitchRolledBack:
		return contracts.EventLevelCritical
	case contracts.AutoSwitchProposed, contracts.AutoSwitchApproved:
		return contracts.EventLevelWarning
	case contracts.AutoSwitchObserving:
		return contracts.EventLevelNotice
	default:
		return contracts.EventLevelInfo
	}
}

func autoSwitchNotificationResult(status contracts.AutoSwitchStatus) string {
	switch status {
	case contracts.AutoSwitchFailed:
		return "failed"
	case contracts.AutoSwitchRolledBack:
		return "rejected"
	case contracts.AutoSwitchObserving:
		return "running"
	case contracts.AutoSwitchProposed:
		return "paused"
	case contracts.AutoSwitchApproved:
		return "accepted"
	case contracts.AutoSwitchRejected:
		return "rejected"
	default:
		return "accepted"
	}
}

func notifyTitle(status contracts.AutoSwitchStatus) string {
	switch status {
	case contracts.AutoSwitchObserving:
		return "自动切换已执行"
	case contracts.AutoSwitchProposed:
		return "自动切换待审批"
	case contracts.AutoSwitchApproved:
		return "自动切换已批准"
	case contracts.AutoSwitchRejected:
		return "自动切换已拒绝"
	case contracts.AutoSwitchSkipped:
		return "自动切换已跳过"
	case contracts.AutoSwitchFailed:
		return "自动切换失败"
	default:
		return "自动切换"
	}
}
