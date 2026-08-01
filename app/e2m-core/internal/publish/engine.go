// Package publish is the declarative publish/reconcile engine for the
// platform-managed upstream layer. A RoutePlan is the desired state (a pool
// published onto one owner instance); the engine diffs that desired state
// against the instance's actual gateway accounts and produces a ReconcilePlan.
//
// dry-run (Plan) returns the diff without touching anything. apply (Apply)
// executes gateway lifecycle changes -- create/update/delete where supported,
// plus enable/disable/revoke scheduling -- and records a PublishedBinding paper
// trail for every evaluated channel. Gateway credentials and proxy values are
// resolved only by the customer-side Connector from opaque local binding IDs.
package publish

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/adapters"
	"e2m.local/core/internal/keyproof"
	"e2m.local/core/internal/store"
)

// Gateway is the narrow slice of the orchestrator the engine needs. It is
// satisfied by *orchestrator.Orchestrator, so the engine stays decoupled from
// adapter wiring and is trivial to fake in tests.
type Gateway interface {
	ListAccounts(ctx context.Context, instanceID string) ([]contracts.GatewayAccount, error)
	SetSchedulable(ctx context.Context, instanceID, accountID string, schedulable bool, reason string) error
	// ProvisionAccount creates (or adopts) a remote account/channel from a
	// managed spec and returns its gateway-native id. Used by the create path.
	ProvisionAccount(ctx context.Context, instanceID string, spec contracts.GatewayAccountSpec, reason string) (contracts.GatewayProvisionResult, error)
	UpdateAccount(ctx context.Context, instanceID string, spec contracts.GatewayAccountSpec, reason string) (contracts.GatewayProvisionResult, error)
	DeleteAccount(ctx context.Context, instanceID, accountID, reason string) error
}

type schedulingBarrierGateway interface {
	SchedulingBarrier(ctx context.Context, instanceID string) error
}

// LifecycleActionSupport is an optional capability surface for gateways that
// can execute account lifecycle actions. Gateways that do not implement it are
// treated conservatively as scheduling-only.
type LifecycleActionSupport interface {
	SupportsLifecycleAction(ctx context.Context, instanceID string, action contracts.ReconcileActionType) bool
}

// ErrUnsupportedLifecycle identifies an apply rejected because its complete
// diff contains account lifecycle actions the target gateway cannot execute.
// The rejection happens before any gateway call, binding write, or plan-status
// write.
var ErrUnsupportedLifecycle = errors.New("gateway account lifecycle is unsupported")

// UnsupportedLifecycleError lists the lifecycle action types that made an
// apply unsafe for the target gateway.
type UnsupportedLifecycleError struct {
	Actions []contracts.ReconcileActionType
}

func (e *UnsupportedLifecycleError) Error() string {
	actions := make([]string, 0, len(e.Actions))
	for _, action := range e.Actions {
		actions = append(actions, string(action))
	}
	return fmt.Sprintf("%s: %s; apply rejected before any changes", ErrUnsupportedLifecycle, strings.Join(actions, ", "))
}

func (e *UnsupportedLifecycleError) Unwrap() error { return ErrUnsupportedLifecycle }

// ExecutionError reports an apply that reached execution but did not fully
// succeed. SucceededActions distinguishes a complete failure from a partial
// remote mutation without exposing gateway-specific error payloads.
type ExecutionError struct {
	SucceededActions int
	Err              error
}

func (e *ExecutionError) Error() string { return e.Err.Error() }
func (e *ExecutionError) Unwrap() error { return e.Err }

func IsPartialExecution(err error) bool {
	var executionErr *ExecutionError
	return errors.As(err, &executionErr) && executionErr.SucceededActions > 0
}

// Engine reconciles route plans onto gateway instances. It never stores
// credentials; it toggles scheduling, provisions channels from the managed pool,
// and records bindings.
type Engine struct {
	store    store.Store
	gw       Gateway
	keyProof DeliveryKeyVerifier
	now      func() time.Time
}

type DeliveryKeyVerifier interface {
	Verify(context.Context, string, string) (keyproof.Verification, error)
}

type Option func(*Engine)

func WithDeliveryKeyVerifier(verifier DeliveryKeyVerifier) Option {
	return func(engine *Engine) { engine.keyProof = verifier }
}

// New builds an Engine with the default UTC clock.
func New(st store.Store, gw Gateway, options ...Option) *Engine {
	engine := &Engine{store: st, gw: gw, now: func() time.Time { return time.Now().UTC() }}
	for _, option := range options {
		option(engine)
	}
	return engine
}

// item is the fully-resolved reconcile decision for one channel (or one orphan
// binding). It carries everything both dry-run and apply need, so the diff is
// computed exactly once.
type item struct {
	action           contracts.ReconcileActionType
	requiredAction   contracts.ReconcileActionType // latent action used for capability preflight
	channelID        string
	remoteID         string
	remoteFound      bool
	schedulable      bool
	detail           string
	binding          *contracts.PublishedBinding // existing binding, if any
	targetState      contracts.PublishedBindingState
	writeBind        bool                       // whether apply should upsert a binding for this item
	channel          *contracts.UpstreamChannel // source channel, for provisioning
	newlyActive      bool                       // this item newly brings a channel into scheduling (rollout-gated)
	proofKeyVersion  int64
	proofConnectorID string
}

type applyMode uint8

const (
	applyReconcile applyMode = iota
	applyRollback
)

type applyOutcome struct {
	result           contracts.ReconcilePlan
	plan             contracts.RoutePlan
	succeededActions int
}

func (o applyOutcome) runStatus(runErr error) contracts.ReconcileRunStatus {
	switch {
	case runErr == nil:
		return contracts.ReconcileRunSucceeded
	case errors.Is(runErr, ErrUnsupportedLifecycle):
		return contracts.ReconcileRunFailed
	case o.succeededActions == 0:
		return contracts.ReconcileRunFailed
	default:
		return contracts.ReconcileRunPartial
	}
}

type executionOutcome struct {
	item              item
	err               error
	sideEffectApplied bool
}

// executionFailures keeps the stable, compact operator message while exposing
// every underlying cause to errors.Is/errors.As. Scheduling callers must be
// able to distinguish a lost generation from an ordinary gateway failure even
// when several reconcile items were evaluated together.
type executionFailures struct {
	message string
	errs    []error
}

func (e *executionFailures) Error() string   { return e.message }
func (e *executionFailures) Unwrap() []error { return e.errs }

func aggregateExecutionFailures(prefix string, errs []error) error {
	details := make([]string, 0, len(errs))
	for _, err := range errs {
		details = append(details, err.Error())
	}
	return &executionFailures{
		message: fmt.Sprintf("%s with %d error(s): %s", prefix, len(errs), strings.Join(details, "; ")),
		errs:    errs,
	}
}

// Plan computes the reconcile diff for a route plan without mutating anything.
// The dry-run is still recorded as a ReconcileRun so the history shows what the
// platform (or an automatic evaluator) previewed.
func (e *Engine) Plan(ctx context.Context, planID string) (contracts.ReconcilePlan, error) {
	started := e.now()
	plan, _, items, err := e.diff(ctx, planID, applyReconcile)
	if err != nil {
		e.recordRun(ctx, contracts.ReconcileRunDryRun, planID, plan, contracts.ReconcilePlan{}, started, contracts.ReconcileRunFailed, err)
		return contracts.ReconcilePlan{}, err
	}
	out := contracts.ReconcilePlan{
		InstanceID: plan.InstanceID,
		PlanID:     plan.ID,
		DryRun:     true,
		Actions:    actionsOf(items),
		CreatedAt:  e.now(),
	}
	e.recordRun(ctx, contracts.ReconcileRunDryRun, planID, plan, out, started, contracts.ReconcileRunSucceeded, nil)
	return out, nil
}

// PlanScheduling previews a plan-local scheduling change. Only channels named
// in desired are considered; the shared UpstreamChannel lifecycle is never
// mutated or used to express this runtime decision. This is the narrow entry
// point used by health-driven ejection and recovery.
func (e *Engine) PlanScheduling(ctx context.Context, planID string, desired map[string]bool) (contracts.ReconcilePlan, error) {
	started := e.now()
	plan, items, err := e.diffScheduling(ctx, planID, desired)
	if err != nil {
		e.recordRun(ctx, contracts.ReconcileRunDryRun, planID, plan, contracts.ReconcilePlan{}, started, contracts.ReconcileRunFailed, err)
		return contracts.ReconcilePlan{}, err
	}
	out := contracts.ReconcilePlan{
		InstanceID: plan.InstanceID,
		PlanID:     plan.ID,
		DryRun:     true,
		Actions:    actionsOf(items),
		CreatedAt:  e.now(),
	}
	e.recordRun(ctx, contracts.ReconcileRunDryRun, planID, plan, out, started, contracts.ReconcileRunSucceeded, nil)
	return out, nil
}

// Apply computes the diff then executes the safe subset, recording binding
// state for every evaluated channel and a ReconcileRun history entry. It returns
// the executed plan (with action details reflecting outcomes) and an aggregated
// error if any action failed; successful actions are still persisted.
func (e *Engine) Apply(ctx context.Context, planID string) (contracts.ReconcilePlan, error) {
	started := e.now()
	var plan contracts.RoutePlan
	var fenceErr error
	// A suspended plan is still a valid target for normal reconciliation. It
	// can never make a channel active (diffForPlan treats suspended as drained),
	// but normal reconcile must remain able to retire platform-managed accounts
	// after the plan was suspended. Rollback deliberately remains revoke-only.
	ctx, plan, fenceErr = e.withSchedulingFence(ctx, planID, contracts.RoutePlanDraft, contracts.RoutePlanPublished, contracts.RoutePlanSuspended)
	if fenceErr != nil {
		e.recordRun(ctx, contracts.ReconcileRunApply, planID, plan, contracts.ReconcilePlan{}, started, contracts.ReconcileRunFailed, fenceErr)
		return contracts.ReconcilePlan{}, fenceErr
	}
	outcome, runErr := e.apply(ctx, plan, applyReconcile)
	e.recordRun(ctx, contracts.ReconcileRunApply, planID, outcome.plan, outcome.result, started, outcome.runStatus(runErr), runErr)
	return outcome.result, runErr
}

// ApplyScheduling applies a plan-local scheduling change. Enable operations are
// completed before disables; if an enable fails, disables are not attempted.
// That ordering keeps a degraded source online when its replacement could not
// be admitted and prevents a scoped switch from stranding traffic.
func (e *Engine) ApplyScheduling(ctx context.Context, planID string, desired map[string]bool) (contracts.ReconcilePlan, error) {
	started := e.now()
	var plan contracts.RoutePlan
	var fenceErr error
	ctx, plan, fenceErr = e.withSchedulingFence(ctx, planID, contracts.RoutePlanPublished, contracts.RoutePlanSuspended)
	if fenceErr != nil {
		e.recordRun(ctx, contracts.ReconcileRunApply, planID, plan, contracts.ReconcilePlan{}, started, contracts.ReconcileRunFailed, fenceErr)
		return contracts.ReconcilePlan{}, fenceErr
	}
	outcome, runErr := e.applyScheduling(ctx, plan, desired)
	e.recordRun(ctx, contracts.ReconcileRunApply, planID, outcome.plan, outcome.result, started, outcome.runStatus(runErr), runErr)
	return outcome.result, runErr
}

// Rollback suspends a plan and reconciles it, which drains every published
// channel out of scheduling on the gateway (revoke). It is the reversible
// "pull this managed switch back" action: remote accounts are not deleted, so
// re-publishing restores the previous state. Centralising suspend+apply here
// (instead of the HTTP handler) keeps the run history complete for background /
// automatic callers too, and labels the run kind rollback.
func (e *Engine) Rollback(ctx context.Context, planID string) (contracts.ReconcilePlan, error) {
	started := e.now()
	plan, err := e.store.GetRoutePlan(ctx, planID)
	if err != nil {
		e.recordRun(ctx, contracts.ReconcileRunRollback, planID, contracts.RoutePlan{}, contracts.ReconcilePlan{}, started, contracts.ReconcileRunFailed, err)
		return contracts.ReconcilePlan{}, err
	}
	if plan.Status == contracts.RoutePlanPublished {
		plan, err = e.store.TransitionRoutePlanScheduling(ctx, planID, contracts.RoutePlanPublished, contracts.RoutePlanSuspended)
	} else if plan.Status == contracts.RoutePlanSuspended {
		plan, err = e.store.ClaimRoutePlanScheduling(ctx, planID, contracts.RoutePlanSuspended)
	} else {
		err = store.ErrConflict
	}
	if err != nil {
		e.recordRun(ctx, contracts.ReconcileRunRollback, planID, plan, contracts.ReconcilePlan{}, started, contracts.ReconcileRunFailed, err)
		return contracts.ReconcilePlan{}, err
	}
	ctx = contracts.WithGatewaySchedulingFence(ctx, contracts.GatewaySchedulingFence{
		Scope: "auto-switch/plan/" + planID, Version: plan.SchedulingGeneration,
	})
	outcome, runErr := e.apply(ctx, plan, applyRollback)
	e.recordRun(ctx, contracts.ReconcileRunRollback, planID, outcome.plan, outcome.result, started, outcome.runStatus(runErr), runErr)
	return outcome.result, runErr
}

// withSchedulingFence gives every real scheduling apply a durable plan-scoped
// generation. Automatic decisions attach their persisted generation before
// entering the engine; operator publish/rollback and quality recovery allocate
// one here. Consequently every newer intent supersedes delayed Connector work
// from every older path, rather than only work from the same decision.
func (e *Engine) withSchedulingFence(ctx context.Context, planID string, allowedStatuses ...contracts.RoutePlanStatus) (context.Context, contracts.RoutePlan, error) {
	if fence, ok := contracts.GatewaySchedulingFenceFromContext(ctx); ok {
		if fence.Scope != "auto-switch/plan/"+planID {
			return ctx, contracts.RoutePlan{}, fmt.Errorf("gateway scheduling fence scope %q does not match plan %s", fence.Scope, planID)
		}
		plan, err := e.store.GetRoutePlan(ctx, planID)
		if err != nil {
			return ctx, contracts.RoutePlan{}, err
		}
		if plan.SchedulingGeneration != fence.Version || !routePlanStatusAllowed(plan.Status, allowedStatuses) {
			return ctx, plan, store.ErrConflict
		}
		return ctx, plan, nil
	}
	plan, err := e.store.ClaimRoutePlanScheduling(ctx, planID, allowedStatuses...)
	if err != nil {
		return ctx, contracts.RoutePlan{}, fmt.Errorf("claim route plan scheduling: %w", err)
	}
	return contracts.WithGatewaySchedulingFence(ctx, contracts.GatewaySchedulingFence{
		Scope: "auto-switch/plan/" + planID, Version: plan.SchedulingGeneration,
	}), plan, nil
}

func routePlanStatusAllowed(status contracts.RoutePlanStatus, allowed []contracts.RoutePlanStatus) bool {
	for _, candidate := range allowed {
		if status == candidate {
			return true
		}
	}
	return len(allowed) == 0
}

// apply is the shared execution core for Apply and Rollback. It executes the
// diff's safe subset and records binding state, but does not write a
// ReconcileRun; the caller labels the run with the correct kind.
func (e *Engine) apply(ctx context.Context, plan contracts.RoutePlan, mode applyMode) (applyOutcome, error) {
	if err := e.advanceSchedulingBarrier(ctx, plan); err != nil {
		return applyOutcome{plan: plan}, err
	}
	_, items, err := e.diffForPlan(ctx, plan, mode)
	outcome := applyOutcome{plan: plan}
	if err != nil {
		return outcome, err
	}
	if mode == applyReconcile {
		if err := e.preflightDeliveryKeys(ctx, plan, items, true); err != nil {
			return outcome, err
		}
	}
	outcome.result = contracts.ReconcilePlan{
		InstanceID: plan.InstanceID,
		PlanID:     plan.ID,
		DryRun:     false,
		Actions:    actionsOf(items),
		CreatedAt:  e.now(),
	}
	if unsupported := e.unsupportedLifecycleActions(ctx, plan.InstanceID, items); len(unsupported) > 0 {
		return outcome, &UnsupportedLifecycleError{Actions: unsupported}
	}

	if mode == applyRollback {
		// Rollback atomically suspended and claimed the plan before diffing.
		// Validate that this execution still owns that exact generation.
		fence, ok := contracts.GatewaySchedulingFenceFromContext(ctx)
		if !ok || fence.Version != plan.SchedulingGeneration {
			return outcome, store.ErrConflict
		}
	}

	var errs []error
	executed := make([]item, 0, len(items))
	for i := range items {
		execution := e.execute(ctx, plan, items[i])
		it := execution.item
		if execution.err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", it.action, it.channelID, execution.err))
		}
		if execution.sideEffectApplied || execution.err == nil && it.action != contracts.ReconcileNoop {
			outcome.succeededActions++
		}
		if it.action == contracts.ReconcileNoop {
			// Keep noops out of the executed action list, but their binding
			// paper trail is still persisted inside execute, and any persist
			// error has already been captured above.
			continue
		}
		executed = append(executed, it)
	}

	outcome.result = contracts.ReconcilePlan{
		InstanceID: plan.InstanceID,
		PlanID:     plan.ID,
		DryRun:     false,
		Actions:    actionsOf(executed),
		CreatedAt:  e.now(),
	}
	if len(errs) > 0 {
		return outcome, &ExecutionError{
			SucceededActions: outcome.succeededActions,
			Err:              aggregateExecutionFailures("reconcile applied", errs),
		}
	}

	// Draft is an execution state, not client optimism: publish only after every
	// gateway action and binding write has succeeded.
	if plan.Status == contracts.RoutePlanDraft {
		updated, uerr := e.store.CompleteRoutePlanPublish(ctx, plan.ID, plan.SchedulingGeneration)
		if uerr != nil {
			return outcome, &ExecutionError{
				SucceededActions: outcome.succeededActions,
				Err:              fmt.Errorf("update plan status: %w", uerr),
			}
		}
		plan = updated
		outcome.plan = updated
	}
	return outcome, nil
}

// applyScheduling is the scheduling-only counterpart of apply. It deliberately
// cannot create, update, revoke, or delete accounts: a health decision may
// change one plan's use of an existing binding, never the shared pool catalog.
func (e *Engine) applyScheduling(ctx context.Context, plan contracts.RoutePlan, desired map[string]bool) (applyOutcome, error) {
	if err := e.advanceSchedulingBarrier(ctx, plan); err != nil {
		return applyOutcome{plan: plan}, err
	}
	items, err := e.diffSchedulingForPlan(ctx, plan, desired)
	outcome := applyOutcome{plan: plan}
	if err != nil {
		return outcome, err
	}
	if err := e.preflightDeliveryKeys(ctx, plan, items, false); err != nil {
		return outcome, err
	}
	outcome.result = contracts.ReconcilePlan{
		InstanceID: plan.InstanceID,
		PlanID:     plan.ID,
		DryRun:     false,
		Actions:    actionsOf(items),
		CreatedAt:  e.now(),
	}

	for _, it := range items {
		switch it.action {
		case contracts.ReconcileEnable, contracts.ReconcileDisable, contracts.ReconcileNoop:
			// Allowed plan-local scheduling actions.
		default:
			return outcome, fmt.Errorf("plan-local scheduling requires an existing remote binding for channel %s (would require %s)", it.channelID, it.action)
		}
	}

	var errs []error
	executed := make([]item, 0, len(items))
	enableFailed := false
	for i := range items {
		it := items[i]
		// diffScheduling orders desired-active items first. Once admission of a
		// replacement fails, do not proceed to any drain in this operation.
		if it.targetState == contracts.BindingDisabled && enableFailed {
			continue
		}
		execution := e.execute(ctx, plan, it)
		it = execution.item
		if execution.err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", it.action, it.channelID, execution.err))
			if it.targetState == contracts.BindingActive {
				enableFailed = true
			}
		}
		if execution.sideEffectApplied || execution.err == nil && it.action != contracts.ReconcileNoop {
			outcome.succeededActions++
		}
		if it.action != contracts.ReconcileNoop {
			executed = append(executed, it)
		}
	}
	outcome.result.Actions = actionsOf(executed)
	if len(errs) > 0 {
		return outcome, &ExecutionError{
			SucceededActions: outcome.succeededActions,
			Err:              aggregateExecutionFailures("scoped scheduling applied", errs),
		}
	}
	return outcome, nil
}

// preflightDeliveryKeys verifies every platform-managed channel that this
// operation would leave active. It runs before binding claims and gateway
// mutations. A newly verified key must be pushed through an account update;
// scheduling-only recovery refuses that refresh and waits for a full publish.
func (e *Engine) preflightDeliveryKeys(ctx context.Context, plan contracts.RoutePlan, items []item, allowRefresh bool) error {
	if e.keyProof == nil {
		return nil
	}
	for i := range items {
		it := &items[i]
		if it.targetState != contracts.BindingActive || it.channel == nil ||
			it.channel.AccountOwnership.Normalize() != contracts.GatewayAccountPlatformManaged {
			continue
		}
		switch it.action {
		case contracts.ReconcileCreate, contracts.ReconcileUpdate, contracts.ReconcileEnable, contracts.ReconcileNoop:
		default:
			continue
		}
		verification, err := e.keyProof.Verify(ctx, it.channelID, plan.InstanceID)
		if err != nil {
			return fmt.Errorf("verify delivery key for channel %s: %w", it.channelID, err)
		}
		it.proofKeyVersion = verification.Delivery.KeyVersion
		if verification.Proof.Status != contracts.DeliveryKeyProofVerified ||
			verification.Proof.ChannelID != it.channelID || verification.Proof.InstanceID != plan.InstanceID ||
			verification.Proof.KeyVersion != verification.Delivery.KeyVersion || verification.Proof.ConnectorID == "" {
			return fmt.Errorf("verify delivery key for channel %s: current instance proof receipt is invalid", it.channelID)
		}
		it.proofConnectorID = verification.Proof.ConnectorID
		if !verification.DeploymentRequired || it.action == contracts.ReconcileCreate || it.action == contracts.ReconcileUpdate {
			continue
		}
		if !allowRefresh {
			return fmt.Errorf("verify delivery key for channel %s: newly verified key requires a full publish before scheduling", it.channelID)
		}
		it.action = contracts.ReconcileUpdate
		it.detail = "delivery key verified; refresh remote credential"
	}
	return nil
}

func (e *Engine) advanceSchedulingBarrier(ctx context.Context, plan contracts.RoutePlan) error {
	fence, ok := contracts.GatewaySchedulingFenceFromContext(ctx)
	if !ok || fence.Scope != "auto-switch/plan/"+plan.ID || fence.Version != plan.SchedulingGeneration {
		return store.ErrConflict
	}
	barrier, ok := e.gw.(schedulingBarrierGateway)
	if !ok {
		// In-process test/legacy gateways have no durable task queue that can
		// deliver an older command after this call. Production Connector-backed
		// gateways implement the barrier and fail closed on delivery failure.
		return nil
	}
	if err := e.guardSchedulingMutation(ctx, plan); err != nil {
		return fmt.Errorf("guard gateway scheduling barrier: %w", err)
	}
	if err := barrier.SchedulingBarrier(ctx, plan.InstanceID); err != nil {
		return fmt.Errorf("advance gateway scheduling barrier: %w", err)
	}
	return nil
}

// guardSchedulingMutation verifies both durable plan ownership and any
// caller-specific lease immediately before a gateway or binding mutation. The
// plan snapshot is the one returned by the atomic scheduling claim; a later
// generation or lifecycle transition supersedes it.
func (e *Engine) guardSchedulingMutation(ctx context.Context, plan contracts.RoutePlan) error {
	fence, ok := contracts.GatewaySchedulingFenceFromContext(ctx)
	if !ok || fence.Scope != "auto-switch/plan/"+plan.ID || fence.Version != plan.SchedulingGeneration {
		return store.ErrConflict
	}
	current, err := e.store.GetRoutePlan(ctx, plan.ID)
	if err != nil {
		return err
	}
	if current.SchedulingGeneration != fence.Version || current.Status != plan.Status {
		return store.ErrConflict
	}
	return contracts.RunReconcileSideEffectGuard(ctx)
}

func (e *Engine) unsupportedLifecycleActions(ctx context.Context, instanceID string, items []item) []contracts.ReconcileActionType {
	support, hasSupport := e.gw.(LifecycleActionSupport)
	seen := make(map[contracts.ReconcileActionType]bool)
	var unsupported []contracts.ReconcileActionType
	for _, it := range items {
		action := it.action
		if it.requiredAction != "" {
			action = it.requiredAction
		}
		if !isLifecycleAction(action) || seen[action] {
			continue
		}
		seen[action] = true
		// Owner-provided remote accounts are update-only. Reject the complete
		// reconcile before the pending binding claim, not merely at the adapter
		// call, so a forbidden create cannot consume permanent allocation state.
		if action == contracts.ReconcileCreate && it.channel != nil &&
			it.channel.AccountOwnership.Normalize() == contracts.GatewayAccountOwnerProvided {
			unsupported = append(unsupported, action)
			continue
		}
		if hasSupport && support.SupportsLifecycleAction(ctx, instanceID, action) {
			continue
		}
		unsupported = append(unsupported, action)
	}
	sort.Slice(unsupported, func(i, j int) bool { return unsupported[i] < unsupported[j] })
	return unsupported
}

func isLifecycleAction(action contracts.ReconcileActionType) bool {
	switch action {
	case contracts.ReconcileCreate, contracts.ReconcileUpdate, contracts.ReconcileDeprovision:
		return true
	default:
		return false
	}
}

// recordRun appends a ReconcileRun history entry. It is best-effort: recording
// history must never fail or cancel the reconcile itself, so a background
// context is used (a cancelled request still leaves a record) and the write
// error is ignored. Trigger/actor are read from ctx so an automatic health-
// driven switch (Phase 4) is distinguishable from an operator action.
func (e *Engine) recordRun(ctx context.Context, kind contracts.ReconcileRunKind, planID string, plan contracts.RoutePlan, result contracts.ReconcilePlan, started time.Time, status contracts.ReconcileRunStatus, runErr error) {
	if e.store == nil {
		return
	}
	trigger := contracts.ReconcileTriggerManual
	if t, ok := contracts.ReconcileTriggerFromContext(ctx); ok && t != "" {
		trigger = t
	}
	run := contracts.ReconcileRun{
		PlanID:     planID,
		InstanceID: plan.InstanceID,
		UserID:     plan.UserID,
		Kind:       kind,
		Trigger:    trigger,
		Status:     status,
		Actions:    result.Actions,
		StartedAt:  started,
		FinishedAt: e.now(),
	}
	if a, ok := contracts.ActorFromContext(ctx); ok {
		run.ActorType, run.ActorID = a.Type, a.ID
	}
	if runErr != nil {
		run.Error = runErr.Error()
	}
	_, _ = e.store.AppendReconcileRun(context.Background(), run)
}

// diff loads desired + actual state and produces the per-channel decisions.
// Rollback is evaluated against a virtual suspended plan; persistence happens
// later, after capability preflight.
func (e *Engine) diff(ctx context.Context, planID string, mode applyMode) (contracts.RoutePlan, contracts.UpstreamPool, []item, error) {
	plan, err := e.store.GetRoutePlan(ctx, planID)
	if err != nil {
		return contracts.RoutePlan{}, contracts.UpstreamPool{}, nil, err
	}
	pool, items, err := e.diffForPlan(ctx, plan, mode)
	return plan, pool, items, err
}

// diffForPlan evaluates the exact plan generation claimed by the caller. This
// keeps execution metadata stable even if a newer scheduling owner takes over
// while the (read-only) diff is being assembled; mutation guards reject the old
// owner before it can act.
func (e *Engine) diffForPlan(ctx context.Context, plan contracts.RoutePlan, mode applyMode) (contracts.UpstreamPool, []item, error) {
	if fence, ok := contracts.GatewaySchedulingFenceFromContext(ctx); ok && plan.SchedulingGeneration != fence.Version {
		return contracts.UpstreamPool{}, nil, store.ErrConflict
	}
	if mode == applyRollback && plan.Status != contracts.RoutePlanSuspended {
		return contracts.UpstreamPool{}, nil, store.ErrConflict
	}
	pool, err := e.store.GetUpstreamPool(ctx, plan.PoolID)
	if err != nil {
		return contracts.UpstreamPool{}, nil, fmt.Errorf("load pool %s: %w", plan.PoolID, err)
	}
	channels, err := e.store.ListUpstreamChannels(ctx, pool.ID)
	if err != nil {
		return pool, nil, fmt.Errorf("list channels: %w", err)
	}
	bindings, err := e.store.ListPublishedBindings(ctx, plan.ID)
	if err != nil {
		return pool, nil, fmt.Errorf("list bindings: %w", err)
	}
	qualityCircuits, err := e.store.ListQualityCircuitRuntimes(ctx, contracts.QualityCircuitRuntimeFilter{PlanID: plan.ID})
	if err != nil {
		// A full reconcile must never interpret an unreadable quality gate as
		// permission to create, update, or enable an isolated upstream binding.
		return pool, nil, fmt.Errorf("list quality circuits: %w", err)
	}
	qualityIsolated := make(map[string]bool, len(qualityCircuits))
	for _, runtime := range qualityCircuits {
		qualityIsolated[runtime.ChannelID] = runtime.State == contracts.QualityCircuitOpen ||
			runtime.State == contracts.QualityCircuitHalfOpen
	}
	allBindings, err := e.store.ListPublishedBindings(ctx, "")
	if err != nil {
		return pool, nil, fmt.Errorf("list key allocations: %w", err)
	}
	allPlans, err := e.store.ListRoutePlans(ctx, 0)
	if err != nil {
		return pool, nil, fmt.Errorf("list allocation owners: %w", err)
	}
	accounts, err := e.gw.ListAccounts(ctx, plan.InstanceID)
	if err != nil {
		return pool, nil, fmt.Errorf("list gateway accounts: %w", err)
	}

	accByID := make(map[string]contracts.GatewayAccount, len(accounts))
	for _, a := range accounts {
		accByID[a.ID] = a
	}
	bindByChannel := make(map[string]*contracts.PublishedBinding, len(bindings))
	for i := range bindings {
		bindByChannel[bindings[i].ChannelID] = &bindings[i]
	}
	channelByID := make(map[string]contracts.UpstreamChannel, len(channels))
	for _, c := range channels {
		channelByID[c.ID] = c
	}

	planActive := plan.Status != contracts.RoutePlanSuspended
	poolRetired := pool.Status == contracts.UpstreamPoolRetired
	poolMaintenance := pool.Status == contracts.UpstreamPoolMaintenance

	desiredActive := desiredActiveChannelsForUser(channels, plan.MaxChannels, plan.UserID, channelOwners(allPlans, allBindings))
	desiredSet := make(map[string]bool, len(desiredActive))
	for _, c := range desiredActive {
		desiredSet[c.ID] = true
	}

	var items []item

	for i := range channels {
		ch := channels[i]
		chRef := &channels[i]
		b := bindByChannel[ch.ID]
		remoteID := resolveRemoteID(ch, b, plan.InstanceID)
		acc, found := accByID[remoteID]
		schedulable := found && acc.Schedulable

		wantActive := planActive && !poolRetired && desiredSet[ch.ID] &&
			ch.Status == contracts.UpstreamChannelActive && !qualityIsolated[ch.ID]
		wantDisabled := planActive && !poolRetired &&
			(ch.Status == contracts.UpstreamChannelMaintenance || qualityIsolated[ch.ID])

		switch {
		case wantActive && !poolMaintenance:
			switch {
			case remoteID == "" || !found:
				// No live remote account for a desired channel. Connector-backed
				// gateways provision it from opaque local binding IDs; maintenance
				// pools keep the intent pending without dispatching a task.
				detail := "no remote account mapped; gateway/operator must provision"
				if remoteID != "" {
					detail = "mapped remote account not found on gateway"
				}
				detail = "provision managed channel onto gateway"
				if ch.AccountOwnership.Normalize() == contracts.GatewayAccountOwnerProvided {
					detail = "owner-provided account must already exist; create is forbidden"
				}
				items = append(items, item{
					action: contracts.ReconcileCreate, channelID: ch.ID, remoteID: remoteID,
					detail: detail, binding: b,
					targetState: contracts.BindingActive, writeBind: true,
					channel: chRef, newlyActive: true,
				})
			case needsUpdate(ch, acc):
				items = append(items, item{
					action: contracts.ReconcileUpdate, channelID: ch.ID, remoteID: remoteID,
					remoteFound: true, schedulable: schedulable, detail: "push managed channel config",
					binding: b, targetState: contracts.BindingActive, writeBind: true,
					channel: chRef, newlyActive: !schedulable,
				})
			case schedulable:
				items = append(items, item{
					action: contracts.ReconcileNoop, channelID: ch.ID, remoteID: remoteID,
					remoteFound: true, schedulable: true,
					binding: b, targetState: contracts.BindingActive, writeBind: true, channel: chRef,
				})
			default:
				items = append(items, item{
					action: contracts.ReconcileEnable, channelID: ch.ID, remoteID: remoteID,
					remoteFound: true,
					binding:     b, targetState: contracts.BindingActive, writeBind: true,
					channel: chRef, newlyActive: true,
				})
			}
		case wantDisabled && !poolMaintenance:
			detail := "channel in maintenance"
			if qualityIsolated[ch.ID] {
				detail = "binding held outside scheduling by quality circuit"
			}
			if found && schedulable {
				items = append(items, item{
					action: contracts.ReconcileDisable, channelID: ch.ID, remoteID: remoteID,
					remoteFound: true, schedulable: true, detail: detail,
					binding: b, targetState: contracts.BindingDisabled, writeBind: true, channel: chRef,
				})
			} else {
				items = append(items, item{
					action: contracts.ReconcileNoop, channelID: ch.ID, remoteID: remoteID,
					remoteFound: found, detail: detail,
					binding: b, targetState: contracts.BindingDisabled, writeBind: b != nil, channel: chRef,
				})
			}
		default:
			// Not desired. Only act if there is a binding to withdraw.
			if b == nil {
				continue
			}
			action := contracts.ReconcileRevoke
			detail := revokeReason(plan, pool, ch)
			if mode != applyRollback && remoteID != "" && (poolRetired || ch.Status == contracts.UpstreamChannelRetired) &&
				ch.AccountOwnership.Normalize() == contracts.GatewayAccountPlatformManaged {
				action = contracts.ReconcileDeprovision
				detail = "delete retired managed channel"
			}
			items = append(items, item{
				action: action, channelID: ch.ID, remoteID: remoteID,
				remoteFound: found, schedulable: schedulable, detail: detail,
				binding: b, targetState: contracts.BindingRevoked, writeBind: true, channel: chRef,
			})
		}
	}

	// Orphan bindings are deprovisioned during normal reconcile. Rollback only
	// withdraws them from scheduling so it remains reversible.
	for i := range bindings {
		b := &bindings[i]
		if _, ok := channelByID[b.ChannelID]; ok {
			continue
		}
		acc, found := accByID[b.RemoteID]
		action := contracts.ReconcileRevoke
		if mode != applyRollback && b.RemoteID != "" && b.AccountOwnership.Normalize() == contracts.GatewayAccountPlatformManaged {
			action = contracts.ReconcileDeprovision
		}
		items = append(items, item{
			action: action, channelID: b.ChannelID, remoteID: b.RemoteID,
			remoteFound: found, schedulable: found && acc.Schedulable,
			detail: "channel removed from pool", binding: b, targetState: contracts.BindingRevoked, writeBind: true,
		})
	}

	sort.SliceStable(items, func(a, b int) bool { return items[a].channelID < items[b].channelID })

	// Gray rollout: hold back newly-activated channels beyond the plan's rollout
	// budget so an upstream switch lands gradually instead of all at once. Items
	// already scheduling (noop/disable/revoke/update) are never held.
	applyRolloutGate(plan, items)

	return pool, items, nil
}

// diffScheduling resolves an explicit per-plan scheduling intent against the
// target instance. The map is intentionally sparse: channels not named by the
// caller are left exactly as they are. This makes an ejection local to one
// PublishedBinding instead of changing the lifecycle of a shared channel.
func (e *Engine) diffScheduling(ctx context.Context, planID string, desired map[string]bool) (contracts.RoutePlan, []item, error) {
	plan, err := e.store.GetRoutePlan(ctx, planID)
	if err != nil {
		return contracts.RoutePlan{}, nil, err
	}
	items, err := e.diffSchedulingForPlan(ctx, plan, desired)
	return plan, items, err
}

func (e *Engine) diffSchedulingForPlan(ctx context.Context, plan contracts.RoutePlan, desired map[string]bool) ([]item, error) {
	if fence, ok := contracts.GatewaySchedulingFenceFromContext(ctx); ok && plan.SchedulingGeneration != fence.Version {
		return nil, store.ErrConflict
	}
	suspendedDrain := plan.Status == contracts.RoutePlanSuspended && len(desired) > 0
	for _, active := range desired {
		if active {
			suspendedDrain = false
			break
		}
	}
	if plan.Status != contracts.RoutePlanPublished && !suspendedDrain {
		return nil, fmt.Errorf("plan-local scheduling requires a published route plan")
	}
	pool, err := e.store.GetUpstreamPool(ctx, plan.PoolID)
	if err != nil {
		return nil, fmt.Errorf("load pool %s: %w", plan.PoolID, err)
	}
	if pool.Status != contracts.UpstreamPoolActive && !suspendedDrain {
		return nil, fmt.Errorf("plan-local scheduling requires an active pool")
	}
	channels, err := e.store.ListUpstreamChannels(ctx, plan.PoolID)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	bindings, err := e.store.ListPublishedBindings(ctx, plan.ID)
	if err != nil {
		return nil, fmt.Errorf("list bindings: %w", err)
	}
	accounts, err := e.gw.ListAccounts(ctx, plan.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("list gateway accounts: %w", err)
	}

	channelByID := make(map[string]*contracts.UpstreamChannel, len(channels))
	for i := range channels {
		channelByID[channels[i].ID] = &channels[i]
	}
	bindByChannel := make(map[string]*contracts.PublishedBinding, len(bindings))
	for i := range bindings {
		bindByChannel[bindings[i].ChannelID] = &bindings[i]
	}
	accountByID := make(map[string]contracts.GatewayAccount, len(accounts))
	for _, account := range accounts {
		accountByID[account.ID] = account
	}

	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]item, 0, len(ids))
	for _, id := range ids {
		wantActive := desired[id]
		channel := channelByID[id]
		if channel == nil {
			return nil, fmt.Errorf("channel %s is not part of route plan pool %s", id, plan.PoolID)
		}
		binding := bindByChannel[id]
		if !wantActive && binding == nil {
			continue
		}
		if wantActive && channel.Status != contracts.UpstreamChannelActive {
			return nil, fmt.Errorf("channel %s is not active in the shared catalog", id)
		}
		remoteID := resolveRemoteID(*channel, binding, plan.InstanceID)
		account, found := accountByID[remoteID]
		schedulable := found && account.Schedulable

		it := item{
			channelID: id, remoteID: remoteID, remoteFound: found,
			schedulable: schedulable, binding: binding, channel: channel,
			writeBind: binding != nil,
		}
		if wantActive {
			it.targetState = contracts.BindingActive
			switch {
			case binding == nil || remoteID == "" || !found:
				it.action = contracts.ReconcileCreate
				it.detail = "plan-local admission requires a provisioned binding"
			case schedulable:
				it.action = contracts.ReconcileNoop
				it.detail = "binding already participates in this plan"
			default:
				it.action = contracts.ReconcileEnable
				it.detail = "admit binding into this plan's scheduling pool"
			}
		} else {
			it.targetState = contracts.BindingDisabled
			if schedulable {
				it.action = contracts.ReconcileDisable
				it.detail = "eject binding from this plan's scheduling pool"
			} else {
				it.action = contracts.ReconcileNoop
				it.detail = "binding already ejected from this plan"
			}
		}
		items = append(items, it)
	}

	// Admit replacements before draining sources. Stable channel ordering keeps
	// the preview deterministic inside each phase.
	sort.SliceStable(items, func(i, j int) bool {
		iActive := items[i].targetState == contracts.BindingActive
		jActive := items[j].targetState == contracts.BindingActive
		if iActive != jActive {
			return iActive
		}
		return items[i].channelID < items[j].channelID
	})
	return items, nil
}

// applyRolloutGate converts the excess "newly active" items (create/enable) into
// ReconcileHold actions, according to the plan's rollout policy. The budget
// counts channels that are already active/being-activated as consumed, so
// re-applying the plan widens the rollout wave by wave until fully rolled out.
func applyRolloutGate(plan contracts.RoutePlan, items []item) {
	budget, unlimited := rolloutBudget(plan)
	if unlimited {
		return
	}
	// Count channels already scheduling (noop-active) as consumed capacity, so a
	// canary that already shipped does not get re-held on the next apply.
	consumed := 0
	for i := range items {
		if items[i].action == contracts.ReconcileNoop && items[i].targetState == contracts.BindingActive {
			consumed++
		}
	}
	for i := range items {
		if !items[i].newlyActive {
			continue
		}
		if consumed < budget {
			consumed++
			continue
		}
		// Over budget: hold this channel back this wave.
		items[i].requiredAction = items[i].action
		items[i].action = contracts.ReconcileHold
		items[i].newlyActive = false
		items[i].detail = "held by rollout policy (" + string(rolloutModeOf(plan)) + "); pending action: " + string(items[i].requiredAction)
		// A held channel that already has a remote id keeps its prior binding
		// state; a brand-new one is recorded pending so operators can see it is
		// queued.
		if items[i].remoteID == "" {
			items[i].targetState = contracts.BindingPending
		} else if items[i].binding != nil {
			items[i].targetState = items[i].binding.State
		} else {
			items[i].targetState = contracts.BindingPending
		}
	}
}

// rolloutModeOf returns the effective rollout mode (default immediate).
func rolloutModeOf(plan contracts.RoutePlan) contracts.RolloutMode {
	if plan.Rollout == "" {
		return contracts.RolloutImmediate
	}
	return plan.Rollout
}

// rolloutBudget returns how many newly-activated channels one apply may bring
// in. unlimited=true means no gating (immediate mode).
func rolloutBudget(plan contracts.RoutePlan) (budget int, unlimited bool) {
	switch rolloutModeOf(plan) {
	case contracts.RolloutCanary:
		n := plan.RolloutCanaryCount
		if n <= 0 {
			n = 1
		}
		return n, false
	case contracts.RolloutBatched:
		n := plan.RolloutBatchSize
		if n <= 0 {
			n = 1
		}
		return n, false
	default:
		return 0, true
	}
}

// execute performs one gateway side effect (if any) and persists its binding.
// The returned error describes the whole item outcome, including persistence.
func (e *Engine) execute(ctx context.Context, plan contracts.RoutePlan, it item) executionOutcome {
	reason := fmt.Sprintf("reconcile plan %s: %s channel %s", plan.ID, it.action, it.channelID)
	var opErr error
	sideEffectApplied := false
	// Claim permanent key ownership before the first gateway mutation. The
	// store resolves the plan owner and atomically rejects a key already owned
	// by another user, so a conflicting publish cannot leak the key remotely and
	// only discover the conflict while persisting its paper trail afterward.
	if it.writeBind && it.binding == nil {
		if err := e.guardSchedulingMutation(ctx, plan); err != nil {
			it.detail = "error:binding claim fence: " + err.Error()
			return executionOutcome{item: it, err: fmt.Errorf("binding claim fence: %w", err)}
		}
		claimed, err := e.store.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
			PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: it.channelID,
			RemoteID: it.remoteID, State: contracts.BindingPending,
			SchedulingGeneration: plan.SchedulingGeneration,
		})
		if err != nil {
			it.detail = "error:persist binding claim: " + err.Error()
			return executionOutcome{item: it, err: fmt.Errorf("persist binding claim: %w", err)}
		}
		it.binding = &claimed
	}
	if it.action != contracts.ReconcileNoop && it.action != contracts.ReconcileHold {
		if err := e.guardSchedulingMutation(ctx, plan); err != nil {
			it.detail = "error:side effect fence: " + err.Error()
			return executionOutcome{item: it, err: fmt.Errorf("side effect fence: %w", err)}
		}
	}

	switch it.action {
	case contracts.ReconcileEnable:
		opErr = e.gw.SetSchedulable(ctx, plan.InstanceID, it.remoteID, true, reason)
		sideEffectApplied = opErr == nil
	case contracts.ReconcileDisable:
		opErr = e.gw.SetSchedulable(ctx, plan.InstanceID, it.remoteID, false, reason)
		sideEffectApplied = opErr == nil
	case contracts.ReconcileRevoke:
		if it.remoteFound && it.schedulable {
			opErr = e.gw.SetSchedulable(ctx, plan.InstanceID, it.remoteID, false, reason)
			sideEffectApplied = opErr == nil
		}
	case contracts.ReconcileCreate:
		if it.targetState == contracts.BindingActive && it.channel != nil {
			if it.channel.AccountOwnership.Normalize() == contracts.GatewayAccountOwnerProvided {
				opErr = errors.New("owner-provided accounts are update-only")
				break
			}
			// Provisioning carries opaque local binding IDs. Connector resolves
			// their values without exposing plaintext to Core.
			var res contracts.GatewayProvisionResult
			res, opErr = e.provision(ctx, plan, &it, reason)
			if opErr == nil {
				it.remoteID = res.RemoteID
				sideEffectApplied = true
			}
		}
		// Otherwise: pending create intent, no side effect (recorded below).
	case contracts.ReconcileUpdate:
		if it.channel != nil {
			var res contracts.GatewayProvisionResult
			res, opErr = e.update(ctx, plan, &it, reason)
			if opErr == nil && res.RemoteID != "" {
				it.remoteID = res.RemoteID
			}
			sideEffectApplied = opErr == nil
		}
	case contracts.ReconcileDeprovision:
		if it.remoteID != "" {
			ownership := contracts.GatewayAccountPlatformManaged
			if it.channel != nil {
				ownership = it.channel.AccountOwnership.Normalize()
			} else if it.binding != nil {
				ownership = it.binding.AccountOwnership.Normalize()
			}
			if ownership == contracts.GatewayAccountOwnerProvided {
				opErr = errors.New("owner-provided accounts are update-only")
			} else {
				// Deprovision is two-phase in protocol v2: immediately drain, then
				// enqueue a durable, generation-fenced delete for 30 minutes later.
				// If the remote account is already absent (for example, a prior
				// generation's delayed delete completed before Core recovered), skip
				// the drain call and enqueue the idempotent delete receipt directly.
				if it.remoteFound {
					opErr = e.gw.SetSchedulable(ctx, plan.InstanceID, it.remoteID, false, reason+"; drain before deferred delete")
					if opErr == nil {
						sideEffectApplied = true
					}
				}
				if opErr == nil {
					sideEffectApplied = true
					opErr = e.gw.DeleteAccount(ctx, plan.InstanceID, it.remoteID, reason)
				}
			}
		}
	case contracts.ReconcileHold, contracts.ReconcileNoop:
		// Hold: intentionally deferred by the rollout policy. Noop: no side
		// effect. Neither touches the gateway.
	}

	if (it.action == contracts.ReconcileCreate || it.action == contracts.ReconcileUpdate) &&
		it.proofKeyVersion > 0 && it.proofConnectorID != "" {
		deploymentStatus := contracts.DeliveryKeyDeploymentDeployed
		if opErr != nil {
			deploymentStatus = contracts.DeliveryKeyDeploymentFailed
		}
		_, deploymentErr := e.store.UpsertUpstreamKeyDeployment(ctx, contracts.UpstreamKeyDeployment{
			ChannelID: it.channelID, InstanceID: plan.InstanceID,
			KeyVersion: it.proofKeyVersion, ConnectorID: it.proofConnectorID,
			Status: deploymentStatus,
		})
		if deploymentErr != nil && opErr == nil {
			opErr = fmt.Errorf("persist delivery key deployment acknowledgement: %w", deploymentErr)
		}
	}

	if opErr != nil {
		it.detail = "error:" + opErr.Error()
	}
	if !it.writeBind {
		return executionOutcome{item: it, err: opErr, sideEffectApplied: sideEffectApplied}
	}
	if err := e.guardSchedulingMutation(ctx, plan); err != nil {
		if opErr == nil {
			opErr = fmt.Errorf("binding persist fence: %w", err)
			it.detail = "error:" + opErr.Error()
		}
		return executionOutcome{item: it, err: opErr, sideEffectApplied: sideEffectApplied}
	}

	state := it.targetState
	lastErr := ""
	switch {
	case opErr != nil:
		state = contracts.BindingFailed
		lastErr = opErr.Error()
		if it.action == contracts.ReconcileUpdate && it.channel != nil &&
			it.channel.AccountOwnership.Normalize() == contracts.GatewayAccountOwnerProvided &&
			errors.Is(opErr, adapters.ErrGatewayMutationNotDispatched) {
			lastErr = contracts.OwnerMetadataUpdateNotDispatchedMarker + ": " + lastErr
		}
	case it.action == contracts.ReconcileCreate && state == contracts.BindingPending:
		// Creation intent recorded as pending; surface the reason as last_error
		// so operators see why it is not active yet.
		lastErr = it.detail
	case it.action == contracts.ReconcileHold:
		// Held by rollout: record why it is queued, without failing.
		lastErr = it.detail
	}

	bind := contracts.PublishedBinding{
		PlanID:               plan.ID,
		InstanceID:           plan.InstanceID,
		ChannelID:            it.channelID,
		RemoteID:             it.remoteID,
		State:                state,
		LastError:            lastErr,
		SchedulingGeneration: plan.SchedulingGeneration,
	}
	// A successful create proves publication, not model callability. Reset the
	// evidence ledger for the new remote identity and wait for an active probe or
	// the first real successful request. Scheduling-only changes preserve the
	// previous verification fields through Store.UpsertPublishedBinding.
	if it.action == contracts.ReconcileCreate && opErr == nil && state == contracts.BindingActive {
		bind.VerificationStatus = contracts.BindingVerificationAwaitingFirstRequest
		bind.VerificationSource = contracts.BindingVerificationSourcePublish
	}
	if it.binding != nil {
		bind.ID = it.binding.ID
		if it.remoteID == "" {
			bind.RemoteID = it.binding.RemoteID
		}
	}
	if _, err := e.store.UpsertPublishedBinding(ctx, bind); err != nil && opErr == nil {
		opErr = fmt.Errorf("persist binding: %w", err)
		it.detail = "error:" + opErr.Error()
	}
	return executionOutcome{item: it, err: opErr, sideEffectApplied: sideEffectApplied}
}

// provision sends opaque local binding IDs to the Connector.
func (e *Engine) provision(ctx context.Context, plan contracts.RoutePlan, it *item, reason string) (contracts.GatewayProvisionResult, error) {
	spec, err := e.specFor(*it.channel, it.remoteID)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	spec.Schedulable = true
	return e.gw.ProvisionAccount(ctx, plan.InstanceID, spec, reason)
}

func (e *Engine) update(ctx context.Context, plan contracts.RoutePlan, it *item, reason string) (contracts.GatewayProvisionResult, error) {
	spec, err := e.specFor(*it.channel, it.remoteID)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	spec.Schedulable = true
	return e.gw.UpdateAccount(ctx, plan.InstanceID, spec, reason)
}

// specFor maps a managed UpstreamChannel onto a gateway-neutral provision spec.
// Ref values are opaque Connector-local binding IDs, never Core vault refs.
func (e *Engine) specFor(ch contracts.UpstreamChannel, remoteID string) (contracts.GatewayAccountSpec, error) {
	spec := contracts.GatewayAccountSpec{
		Ownership:           ch.AccountOwnership.Normalize(),
		ChannelID:           ch.ID,
		RemoteID:            remoteID,
		DisplayName:         ch.DisplayName,
		Provider:            ch.Provider,
		Models:              ch.Models,
		Groups:              ch.Groups,
		Priority:            ch.Priority,
		Weight:              ch.Weight,
		CredentialBindingID: strings.TrimSpace(ch.CredentialBindingID),
		ProxyBindingID:      strings.TrimSpace(ch.ProxyBindingID),
	}
	if ch.Labels != nil {
		if v := strings.TrimSpace(ch.Labels["type"]); v != "" {
			spec.Type = v
		}
		if spec.Provider == "" {
			spec.Provider = strings.TrimSpace(ch.Labels["provider"])
		}
	}
	// Platform-managed lifecycle work must resolve a Connector-local credential
	// binding. Owner-provided updates are deliberately credential-blind: the
	// adapter patches only explicitly managed metadata on an existing remote
	// account, so requiring a binding here would both contradict update-only
	// ownership and tempt callers to register an owner secret in E2M.
	if spec.Ownership == contracts.GatewayAccountPlatformManaged && spec.CredentialBindingID == "" {
		return spec, errors.New("connector credential binding is required")
	}
	if !spec.Ownership.Valid() {
		return spec, errors.New("account ownership is invalid")
	}
	if spec.Ownership == contracts.GatewayAccountOwnerProvided {
		spec.CredentialBindingID = ""
		spec.ProxyBindingID = ""
	}
	return spec, nil
}

func needsUpdate(ch contracts.UpstreamChannel, acc contracts.GatewayAccount) bool {
	if ch.DisplayName != "" && acc.DisplayName != "" && ch.DisplayName != acc.DisplayName {
		return true
	}
	if ch.Provider != "" && acc.Platform != "" && ch.Provider != acc.Platform {
		return true
	}
	if ch.Priority > 0 && acc.Priority > 0 && ch.Priority != acc.Priority {
		return true
	}
	if ch.Labels != nil {
		if typ := strings.TrimSpace(ch.Labels["type"]); typ != "" && acc.Type != "" && typ != acc.Type {
			return true
		}
	}
	if len(ch.Groups) > 0 && len(acc.GroupIDs) > 0 && strings.Join(ch.Groups, ",") != strings.Join(acc.GroupIDs, ",") {
		return true
	}
	return false
}

// desiredActiveChannels returns at most one active key per upstream source,
// deterministically ordered and capped by maxChannels (0 = no cap).
func desiredActiveChannels(channels []contracts.UpstreamChannel, maxChannels int) []contracts.UpstreamChannel {
	return desiredActiveChannelsForUser(channels, maxChannels, 0, nil)
}

// desiredActiveChannelsForUser selects one concrete key per source. A user's
// permanent allocation wins within that source (including across instances);
// otherwise the highest-priority unallocated key is selected. Keys owned by a
// different user are never exposed through this plan's desired state.
func desiredActiveChannelsForUser(channels []contracts.UpstreamChannel, maxChannels int, userID int64, owners map[string]int64) []contracts.UpstreamChannel {
	active := make([]contracts.UpstreamChannel, 0, len(channels))
	ownedSources := make(map[string]struct{})
	for _, c := range channels {
		if ownerID, allocated := owners[c.ID]; allocated && ownerID == userID {
			ownedSources[c.SourceIdentity()] = struct{}{}
		}
		if c.Status == contracts.UpstreamChannelActive {
			active = append(active, c)
		}
	}
	sort.SliceStable(active, func(a, b int) bool {
		pa, pb := effPriority(active[a].Priority), effPriority(active[b].Priority)
		if pa != pb {
			return pa < pb
		}
		if active[a].Weight != active[b].Weight {
			return active[a].Weight > active[b].Weight
		}
		return active[a].ID < active[b].ID
	})
	selected := make([]contracts.UpstreamChannel, 0, len(active))
	seenSources := make(map[string]struct{}, len(active))
	// First retain this user's existing key for each source, even if a newer
	// unallocated key has a better catalog priority.
	for _, channel := range active {
		owner, allocated := owners[channel.ID]
		if !allocated || owner != userID {
			continue
		}
		sourceID := channel.SourceIdentity()
		if _, exists := seenSources[sourceID]; exists {
			continue
		}
		seenSources[sourceID] = struct{}{}
		selected = append(selected, channel)
	}
	// Fill sources the user does not own from unallocated inventory. Allocated
	// keys belonging to another user are deliberately skipped.
	for _, channel := range active {
		if _, allocated := owners[channel.ID]; allocated {
			continue
		}
		sourceID := channel.SourceIdentity()
		if _, owned := ownedSources[sourceID]; owned {
			continue
		}
		if _, exists := seenSources[sourceID]; exists {
			continue
		}
		seenSources[sourceID] = struct{}{}
		selected = append(selected, channel)
	}
	sort.SliceStable(selected, func(a, b int) bool {
		pa, pb := effPriority(selected[a].Priority), effPriority(selected[b].Priority)
		if pa != pb {
			return pa < pb
		}
		if selected[a].Weight != selected[b].Weight {
			return selected[a].Weight > selected[b].Weight
		}
		return selected[a].ID < selected[b].ID
	})
	if maxChannels > 0 && len(selected) > maxChannels {
		selected = selected[:maxChannels]
	}
	return selected
}

// channelOwners derives permanent allocation ownership from the append-only
// binding paper trail. Disabled/revoked rows still count, so isolation never
// makes a key reusable. Conflicting legacy ownership is marked unavailable to
// every user; migration 0028 rejects that state in PostgreSQL.
func channelOwners(plans []contracts.RoutePlan, bindings []contracts.PublishedBinding) map[string]int64 {
	userByPlan := make(map[string]int64, len(plans))
	for _, plan := range plans {
		userByPlan[plan.ID] = plan.UserID
	}
	owners := make(map[string]int64, len(bindings))
	conflicted := make(map[string]bool)
	for _, binding := range bindings {
		userID, ok := userByPlan[binding.PlanID]
		if !ok || conflicted[binding.ChannelID] {
			continue
		}
		if ownerID, exists := owners[binding.ChannelID]; exists && ownerID != userID {
			delete(owners, binding.ChannelID)
			conflicted[binding.ChannelID] = true
			continue
		}
		owners[binding.ChannelID] = userID
	}
	for channelID := range conflicted {
		owners[channelID] = -1
	}
	return owners
}

// effPriority treats a non-positive Priority as "unset" and sorts it last, so
// explicitly prioritised channels win the cap.
func effPriority(p int) int {
	if p <= 0 {
		return 1 << 30
	}
	return p
}

// resolveRemoteID finds the gateway account id a channel maps to: an existing
// binding's RemoteID wins, else a channel label hint ("remote_id").
func resolveRemoteID(ch contracts.UpstreamChannel, b *contracts.PublishedBinding, instanceID string) string {
	if b != nil && b.RemoteID != "" {
		return b.RemoteID
	}
	if ch.Labels != nil {
		// Legacy catalog rows may carry an adoption hint for the gateway that
		// originally supplied the account. A native remote ID is only meaningful
		// inside that one instance; reusing it on another user's gateway could
		// update an unrelated account that happens to have the same numeric ID.
		if scoped := strings.TrimSpace(ch.Labels["instance_id"]); scoped != "" && scoped != strings.TrimSpace(instanceID) {
			return ""
		}
		if v := strings.TrimSpace(ch.Labels["remote_id"]); v != "" {
			return v
		}
	}
	return ""
}

func revokeReason(plan contracts.RoutePlan, pool contracts.UpstreamPool, ch contracts.UpstreamChannel) string {
	switch {
	case plan.Status == contracts.RoutePlanSuspended:
		return "route plan suspended"
	case pool.Status == contracts.UpstreamPoolRetired:
		return "pool retired"
	case ch.Status == contracts.UpstreamChannelRetired:
		return "channel retired"
	default:
		return "channel not in plan (capped out)"
	}
}

// actionsOf projects items into contract actions, dropping noops so the plan
// shows only real changes.
func actionsOf(items []item) []contracts.ReconcileAction {
	out := make([]contracts.ReconcileAction, 0, len(items))
	for _, it := range items {
		if it.action == contracts.ReconcileNoop {
			continue
		}
		out = append(out, contracts.ReconcileAction{
			Type:      it.action,
			ChannelID: it.channelID,
			RemoteID:  it.remoteID,
			Detail:    it.detail,
		})
	}
	return out
}
