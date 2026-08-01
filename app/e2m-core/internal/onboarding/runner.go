// Package onboarding turns every active shared pool into a durable RoutePlan
// on each eligible client gateway. It reuses the existing allocation,
// Connector task, key-proof, and publish engines instead of bypassing them.
package onboarding

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/keyproof"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/store"
)

const (
	DefaultInterval = 15 * time.Second
	// Connector-backed gateway calls wait for at most 30 seconds. A 75-second
	// lease leaves a full-call safety margin while bounding crash/restart
	// recovery instead of stranding work behind the old ten-minute lease.
	defaultLease        = 75 * time.Second
	defaultStepTimeout  = 60 * time.Second
	leaseReleaseTimeout = 2 * time.Second
	defaultBatchLimit   = 50
	connectorFreshness  = time.Minute
	defaultActiveCheck  = time.Minute

	auditActionOnboardingProgress       = "onboarding.workflow.progress"
	auditActionOnboardingCompleted      = "onboarding.workflow.completed"
	auditActionOnboardingVerified       = "onboarding.workflow.verified"
	auditActionOnboardingReconfigured   = "onboarding.workflow.reconfigured"
	auditActionOnboardingRepaired       = "onboarding.workflow.repaired"
	auditActionOnboardingRetryScheduled = "onboarding.workflow.retry_scheduled"
	auditActionOnboardingFailed         = "onboarding.workflow.failed"
	auditActionOnboardingPaused         = "onboarding.workflow.paused"
)

type Gateway interface {
	ListAccounts(context.Context, string) ([]contracts.GatewayAccount, error)
}

type DeliveryKeys interface {
	InstallBinding(context.Context, string, string) (contracts.ConnectorGatewayBindingInstallResult, error)
	Verify(context.Context, string, string) (keyproof.Verification, error)
}

type Publisher interface {
	Apply(context.Context, string) (contracts.ReconcilePlan, error)
}

// Dispatcher is the narrow notification boundary used by onboarding. The
// production adapter resolves the owner's routes and then applies their event
// thresholds; tests can capture business events without knowing any channel.
type Dispatcher interface {
	Dispatch(context.Context, int64, notify.Event)
}

type Option func(*Runner)

// WithNotifier publishes user-facing onboarding outcomes after their audit row
// is durably appended. It is optional so the workflow remains usable in tests
// and deployments without notification channels.
func WithNotifier(dispatcher Dispatcher) Option {
	return func(r *Runner) { r.notifier = dispatcher }
}

type Runner struct {
	store       store.Store
	gateway     Gateway
	keys        DeliveryKeys
	publisher   Publisher
	notifier    Dispatcher
	workerID    string
	interval    time.Duration
	lease       time.Duration
	stepTimeout time.Duration
	batchLimit  int
	activeCheck time.Duration
	now         func() time.Time
}

func New(st store.Store, gateway Gateway, keys DeliveryKeys, publisher Publisher, interval time.Duration, options ...Option) *Runner {
	if interval <= 0 {
		interval = DefaultInterval
	}
	runner := &Runner{
		store: st, gateway: gateway, keys: keys, publisher: publisher,
		workerID: newWorkerID(), interval: interval, lease: defaultLease, stepTimeout: defaultStepTimeout,
		batchLimit: defaultBatchLimit, activeCheck: defaultActiveCheck, now: time.Now,
	}
	for _, option := range options {
		if option != nil {
			option(runner)
		}
	}
	return runner
}

func (r *Runner) Run(ctx context.Context) {
	log.Printf("onboarding runner started (interval=%s)", r.interval)
	r.RunOnce(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("onboarding runner stopped: %v", ctx.Err())
			return
		case <-ticker.C:
			r.RunOnce(ctx)
		}
	}
}

// RunOnce discovers missing instance/pool workflows and drains a bounded set
// of due claims. It is exported so startup and real-gateway tests can run the
// exact production sweep without waiting for a ticker.
func (r *Runner) RunOnce(ctx context.Context) {
	if r == nil || r.store == nil || r.gateway == nil || r.keys == nil || r.publisher == nil {
		return
	}
	if err := r.discover(ctx); err != nil {
		log.Printf("onboarding: discovery failed (code=store_unavailable)")
		return
	}
	limit := r.batchLimit
	if limit <= 0 {
		limit = defaultBatchLimit
	}
	for i := 0; i < limit; i++ {
		workflow, claimed, err := r.store.ClaimOnboardingWorkflow(ctx, r.workerID, r.lease)
		if err != nil {
			log.Printf("onboarding: claim failed (code=store_unavailable)")
			return
		}
		if !claimed {
			return
		}
		if err := r.process(ctx, workflow); err != nil {
			code := errorCode(err)
			log.Printf("onboarding: workflow %s deferred (code=%s)", workflow.ID, code)
		}
	}
}

func (r *Runner) discover(ctx context.Context) error {
	pools, err := r.store.ListUpstreamPools(ctx)
	if err != nil {
		return err
	}
	instances, err := r.store.ListInstances(ctx, 0)
	if err != nil {
		return err
	}
	users, err := r.store.ListUsers(ctx)
	if err != nil {
		return err
	}
	eligible := make(map[int64]bool, len(users))
	for _, user := range users {
		eligible[user.ID] = user.Enabled && hasRole(user.Roles, contracts.UserRoleClient)
	}
	deliveries, err := r.store.ListUpstreamKeyDeliveries(ctx)
	if err != nil {
		return err
	}
	deliveryVersions := make(map[string]int64, len(deliveries))
	for _, delivery := range deliveries {
		deliveryVersions[delivery.ChannelID] = delivery.KeyVersion
	}
	channelsByPool := make(map[string][]contracts.UpstreamChannel, len(pools))
	for _, pool := range pools {
		channels, listErr := r.store.ListUpstreamChannels(ctx, pool.ID)
		if listErr != nil {
			return listErr
		}
		channelsByPool[pool.ID] = channels
	}
	existingWorkflows, err := r.store.ListOnboardingWorkflows(ctx, contracts.OnboardingWorkflowFilter{})
	if err != nil {
		return err
	}
	workflowByScope := make(map[string]contracts.OnboardingWorkflow, len(existingWorkflows))
	for _, workflow := range existingWorkflows {
		workflowByScope[workflow.InstanceID+"\x00"+workflow.PoolID] = workflow
	}
	for _, instance := range instances {
		if !eligible[instance.UserID] {
			continue
		}
		var connector contracts.Connector
		if strings.TrimSpace(instance.ConnectorID) != "" {
			connector, err = r.store.GetConnector(ctx, instance.ConnectorID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		for _, pool := range pools {
			if pool.Status != contracts.UpstreamPoolActive {
				continue
			}
			resolution, resolveErr := r.resolvePoolRollout(ctx, pool.ID, instance.UserID, instance.ID)
			if resolveErr != nil {
				return resolveErr
			}
			if !resolution.Enabled {
				continue
			}
			fingerprint, fingerprintErr := desiredFingerprint(
				pool, channelsByPool[pool.ID], deliveryVersions, instance, connector, resolution,
			)
			if fingerprintErr != nil {
				return fingerprintErr
			}
			// A canary/batched apply that intentionally held later channels is
			// not a technical retry. Keep it paused until the operator changes
			// the approved rollout cap/mode (which changes this fingerprint), so
			// the ordinary 15-second discovery loop cannot widen it repeatedly.
			if current, ok := workflowByScope[instance.ID+"\x00"+pool.ID]; ok &&
				current.Status == contracts.OnboardingDormantStatus &&
				current.LastErrorCode == "rollout_observation_pending" &&
				current.DesiredFingerprint == fingerprint {
				continue
			}
			if _, err := r.store.UpsertOnboardingWorkflow(ctx, contracts.OnboardingWorkflow{
				UserID: instance.UserID, InstanceID: instance.ID, PoolID: pool.ID,
				ConnectorID: strings.TrimSpace(instance.ConnectorID), DesiredFingerprint: fingerprint,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runner) process(ctx context.Context, workflow contracts.OnboardingWorkflow) error {
	// Graceful cancellation expires only the exact generation this process still
	// owns. A crash relies on the short TTL, and a stale release cannot disturb a
	// replacement worker because renew/claim always advance Version.
	defer func() {
		if ctx.Err() == nil || workflow.Status != contracts.OnboardingRunning {
			return
		}
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseReleaseTimeout)
		defer cancel()
		_ = r.store.ReleaseOnboardingWorkflowLease(
			releaseCtx, workflow.ID, workflow.LeaseOwner, workflow.Version,
		)
	}()

	wasActive := workflow.Stage == contracts.OnboardingActive
	workflow, err := r.advance(ctx, workflow, contracts.OnboardingCheckingGateway)
	if err != nil {
		return err
	}
	if err := r.ensureEligible(ctx, workflow); err != nil {
		if errorCode(err) == "pool_inactive" || errorCode(err) == "pool_rollout_disabled" {
			return r.dormantWorkflow(ctx, workflow, err)
		}
		return r.deferWorkflow(ctx, workflow, err, false)
	}
	// Attempts is reset whenever discovery finds a new desired generation and
	// incremented when that generation is first claimed. Record one clear
	// "started" event for each eligible onboarding/reconfiguration cycle, while
	// keeping retries and periodic ready-state verification quiet.
	if workflow.Attempts == 1 {
		r.audit(ctx, workflow, auditActionOnboardingProgress, "running", "", contracts.EventLevelInfo)
	}
	instance, connector, err := r.readyConnector(ctx, workflow)
	if err != nil {
		return r.deferWorkflow(ctx, workflow, err, true)
	}
	stepCtx, cancel, err := r.externalStepContext(ctx, &workflow)
	if err != nil {
		return err
	}
	_, err = r.gateway.ListAccounts(stepCtx, instance.ID)
	cancel()
	if err != nil {
		return r.deferWorkflow(ctx, workflow, stepError("gateway_unavailable", err), false)
	}

	var plan contracts.RoutePlan
	var channels []contracts.UpstreamChannel
	if wasActive && strings.TrimSpace(workflow.PlanID) != "" {
		plan, err = r.store.GetRoutePlan(ctx, workflow.PlanID)
		if err == nil && plan.UserID == workflow.UserID && plan.InstanceID == workflow.InstanceID &&
			plan.PoolID == workflow.PoolID && plan.Status == contracts.RoutePlanPublished {
			channels, err = r.store.ClaimPlanChannels(ctx, plan.ID)
			if err == nil {
				err = r.requireCompleteSourceCoverage(ctx, plan, channels)
			}
			if err == nil {
				err = r.verifyActive(ctx, &workflow, plan.ID, instance, connector, channels, workflow.KeyVersionSummary)
			}
			if err == nil {
				return r.completeActive(ctx, workflow, true)
			}
		}
		// A stale active receipt is repaired through the normal idempotent path.
		plan = contracts.RoutePlan{}
		channels = nil
	}

	plan, err = r.ensurePlan(ctx, workflow, instance)
	if err != nil {
		return r.deferWorkflow(ctx, workflow, err, false)
	}
	workflow.PlanID = plan.ID
	workflow, err = r.advance(ctx, workflow, contracts.OnboardingAssigningKeys)
	if err != nil {
		return err
	}
	channels, err = r.store.ClaimPlanChannels(ctx, plan.ID)
	if err != nil {
		return r.deferWorkflow(ctx, workflow, stepError("key_assignment_failed", err), false)
	}
	if err := r.requireCompleteSourceCoverage(ctx, plan, channels); err != nil {
		return r.deferWorkflow(ctx, workflow, err, false)
	}

	workflow, err = r.advance(ctx, workflow, contracts.OnboardingDeliveringBindings)
	if err != nil {
		return err
	}
	versions := make(map[string]int64)
	for _, channel := range channels {
		if channel.AccountOwnership.Normalize() != contracts.GatewayAccountPlatformManaged {
			continue
		}
		stepCtx, cancel, renewErr := r.externalStepContext(ctx, &workflow)
		if renewErr != nil {
			return renewErr
		}
		installed, installErr := r.keys.InstallBinding(stepCtx, channel.ID, instance.ID)
		cancel()
		if installErr != nil {
			return r.deferWorkflow(ctx, workflow, stepError("binding_delivery_failed", installErr), false)
		}
		if installed.ChannelID != channel.ID || installed.BindingID != strings.TrimSpace(channel.CredentialBindingID) || installed.KeyVersion <= 0 {
			return r.deferWorkflow(ctx, workflow, stepError("binding_delivery_invalid", nil), false)
		}
		stepCtx, cancel, renewErr = r.externalStepContext(ctx, &workflow)
		if renewErr != nil {
			return renewErr
		}
		verification, verifyErr := r.keys.Verify(stepCtx, channel.ID, instance.ID)
		cancel()
		if verifyErr != nil || verification.Delivery.KeyVersion != installed.KeyVersion ||
			verification.Proof.ChannelID != channel.ID || verification.Proof.InstanceID != instance.ID ||
			verification.Proof.KeyVersion != installed.KeyVersion ||
			verification.Proof.Status != contracts.DeliveryKeyProofVerified ||
			verification.Proof.ConnectorID != connector.ID {
			return r.deferWorkflow(ctx, workflow, stepError("binding_proof_failed", verifyErr), false)
		}
		versions[channel.ID] = installed.KeyVersion
	}
	workflow.KeyVersionSummary = versions
	workflow, err = r.advance(ctx, workflow, contracts.OnboardingPublishing)
	if err != nil {
		return err
	}
	publishCtx := contracts.WithActor(ctx, contracts.Actor{Type: "system", ID: "e2m-onboarding"})
	publishCtx = contracts.WithReconcileTrigger(publishCtx, contracts.ReconcileTriggerAuto)
	// Publish can contain several gateway mutations. Its engine invokes this
	// guard immediately before each mutation, renewing the same workflow claim
	// and advancing the version carried by this process.
	publishCtx = contracts.WithReconcileSideEffectGuard(publishCtx, func(guardCtx context.Context) error {
		return r.renewWorkflowLease(guardCtx, &workflow)
	})
	stepCtx, cancel, err = r.externalStepContext(publishCtx, &workflow)
	if err != nil {
		return err
	}
	result, err := r.publisher.Apply(stepCtx, plan.ID)
	cancel()
	if err != nil {
		return r.deferWorkflow(ctx, workflow, stepError("publish_failed", err), false)
	}
	if rolloutHeld(result) {
		return r.dormantWorkflow(ctx, workflow, stepError("rollout_observation_pending", nil))
	}

	workflow, err = r.advance(ctx, workflow, contracts.OnboardingVerifying)
	if err != nil {
		return err
	}
	if err := r.verifyActive(ctx, &workflow, plan.ID, instance, connector, channels, versions); err != nil {
		return r.deferWorkflow(ctx, workflow, err, false)
	}
	return r.completeActive(ctx, workflow, false)
}

func rolloutHeld(result contracts.ReconcilePlan) bool {
	for _, action := range result.Actions {
		if action.Type == contracts.ReconcileHold {
			return true
		}
	}
	return false
}

func (r *Runner) completeActive(ctx context.Context, workflow contracts.OnboardingWorkflow, verifiedWithoutRepair bool) error {
	action := auditActionOnboardingCompleted
	level := contracts.EventLevelNotice
	switch {
	case workflow.LastReadyGeneration == 0:
		action = auditActionOnboardingCompleted
	case verifiedWithoutRepair:
		action = auditActionOnboardingVerified
		level = contracts.EventLevelInfo
	case workflow.LastReadyGeneration < workflow.DesiredGeneration:
		action = auditActionOnboardingReconfigured
	default:
		action = auditActionOnboardingRepaired
	}
	workflow.Stage = contracts.OnboardingActive
	workflow.Status = contracts.OnboardingReady
	workflow.LastErrorCode = ""
	readyAt := r.now().UTC()
	workflow.LastReadyGeneration = workflow.DesiredGeneration
	workflow.LastReadyAt = &readyAt
	interval := r.activeCheck
	if interval <= 0 {
		interval = defaultActiveCheck
	}
	next := readyAt.Add(interval)
	workflow.NextAttemptAt = &next
	active, err := r.store.TransitionOnboardingWorkflow(ctx, workflow, workflow.Version)
	if err != nil {
		return err
	}
	r.audit(ctx, active, action, onboardingResult(action), "", level)
	return nil
}

func onboardingResult(action string) string {
	if action == auditActionOnboardingVerified {
		return "verified"
	}
	return "accepted"
}
func (r *Runner) ensureEligible(ctx context.Context, workflow contracts.OnboardingWorkflow) error {
	user, err := r.store.GetUser(ctx, workflow.UserID)
	if err != nil || !user.Enabled || !hasRole(user.Roles, contracts.UserRoleClient) {
		return stepError("user_ineligible", err)
	}
	pool, err := r.store.GetUpstreamPool(ctx, workflow.PoolID)
	if err != nil {
		return stepError("pool_unavailable", err)
	}
	if pool.Status != contracts.UpstreamPoolActive {
		return stepError("pool_inactive", nil)
	}
	resolution, err := r.resolvePoolRollout(ctx, workflow.PoolID, workflow.UserID, workflow.InstanceID)
	if err != nil {
		return stepError("pool_rollout_unavailable", err)
	}
	if !resolution.Enabled {
		return stepError("pool_rollout_disabled", nil)
	}
	return nil
}

func (r *Runner) resolvePoolRollout(ctx context.Context, poolID string, userID int64, instanceID string) (contracts.PoolRolloutResolution, error) {
	rolloutStore, ok := store.AsPoolRolloutStore(r.store)
	if !ok {
		return contracts.PoolRolloutLegacyCompatible(poolID, userID, instanceID), nil
	}
	return rolloutStore.ResolvePoolRollout(ctx, poolID, userID, instanceID)
}

func (r *Runner) readyConnector(ctx context.Context, workflow contracts.OnboardingWorkflow) (contracts.Instance, contracts.Connector, error) {
	instance, err := r.store.GetInstance(ctx, workflow.InstanceID)
	if err != nil || instance.UserID != workflow.UserID || instance.ID == "" {
		return contracts.Instance{}, contracts.Connector{}, stepError("instance_unavailable", err)
	}
	if instance.ConnectorID == "" || instance.ConnectorID != workflow.ConnectorID {
		return instance, contracts.Connector{}, stepError("connector_unavailable", nil)
	}
	connector, err := r.store.GetConnector(ctx, instance.ConnectorID)
	now := r.now().UTC()
	if err != nil || connector.Status != contracts.ConnectorStatusOnline ||
		connector.UserID != instance.UserID || connector.InstanceID != instance.ID ||
		connector.ProtocolVersion != contracts.ConnectorProtocolVersion ||
		connector.LastSeenAt == nil || connector.LastSeenAt.Before(now.Add(-connectorFreshness)) {
		return instance, connector, stepError("connector_unavailable", err)
	}
	runtime := connector.Gateway
	if !runtime.GatewayConfigured || runtime.GatewayStatus != "ok" ||
		runtime.GatewayKind != string(instance.Kind) ||
		!contracts.IsConnectorBindingEncryptionPublicKey(runtime.BindingEncryptionPublicKey) {
		return instance, connector, stepError("connector_gateway_not_ready", nil)
	}
	for _, capability := range []contracts.ConnectorTaskType{
		contracts.ConnectorTaskGatewayAccountsList,
		contracts.ConnectorTaskGatewayBindingInstall,
		contracts.ConnectorTaskGatewayBindingProof,
		contracts.ConnectorTaskGatewaySchedulingBarrier,
		contracts.ConnectorTaskGatewayAccountCreate,
		contracts.ConnectorTaskGatewayAccountUpdate,
	} {
		if !hasCapability(runtime.Capabilities, capability) {
			return instance, connector, stepError("connector_capability_missing", nil)
		}
	}
	return instance, connector, nil
}

func (r *Runner) ensurePlan(ctx context.Context, workflow contracts.OnboardingWorkflow, instance contracts.Instance) (contracts.RoutePlan, error) {
	resolution, err := r.resolvePoolRollout(ctx, workflow.PoolID, workflow.UserID, workflow.InstanceID)
	if err != nil {
		return contracts.RoutePlan{}, stepError("pool_rollout_unavailable", err)
	}
	if !resolution.Enabled {
		return contracts.RoutePlan{}, stepError("pool_rollout_disabled", nil)
	}
	plans, err := r.store.ListRoutePlans(ctx, instance.UserID)
	if err != nil {
		return contracts.RoutePlan{}, stepError("route_plan_store_failed", err)
	}
	for _, plan := range plans {
		if plan.InstanceID != instance.ID || plan.PoolID != workflow.PoolID {
			continue
		}
		if plan.Status == contracts.RoutePlanSuspended {
			// A suspended managed plan was previously drained. Re-admission must
			// use the explicit rollout policy instead of remaining permanently
			// stuck in a manual state.
			if plan.Labels["managed_by"] != "e2m-onboarding" {
				return contracts.RoutePlan{}, stepError("route_plan_suspended", nil)
			}
			plan.Status = contracts.RoutePlanDraft
		}
		plan.Rollout = resolution.Rollout
		plan.RolloutBatchSize = resolution.RolloutBatchSize
		plan.RolloutCanaryCount = resolution.RolloutCanaryCount
		updated, updateErr := r.store.UpdateRoutePlan(ctx, plan)
		if updateErr != nil {
			return contracts.RoutePlan{}, stepError("route_plan_update_failed", updateErr)
		}
		return updated, nil
	}
	plan, err := r.store.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: instance.UserID, InstanceID: instance.ID, PoolID: workflow.PoolID,
		Tier: "stability", Status: contracts.RoutePlanDraft, Rollout: resolution.Rollout,
		RolloutBatchSize: resolution.RolloutBatchSize, RolloutCanaryCount: resolution.RolloutCanaryCount,
		Labels: map[string]string{"managed_by": "e2m-onboarding"},
	})
	if err == nil {
		return plan, nil
	}
	if !errors.Is(err, store.ErrDuplicate) {
		return contracts.RoutePlan{}, stepError("route_plan_create_failed", err)
	}
	plans, listErr := r.store.ListRoutePlans(ctx, instance.UserID)
	if listErr != nil {
		return contracts.RoutePlan{}, stepError("route_plan_store_failed", listErr)
	}
	for _, plan := range plans {
		if plan.InstanceID == instance.ID && plan.PoolID == workflow.PoolID && plan.Status != contracts.RoutePlanSuspended {
			return plan, nil
		}
	}
	return contracts.RoutePlan{}, stepError("route_plan_create_conflict", err)
}

func (r *Runner) requireCompleteSourceCoverage(ctx context.Context, plan contracts.RoutePlan, selected []contracts.UpstreamChannel) error {
	catalog, err := r.store.ListUpstreamChannels(ctx, plan.PoolID)
	if err != nil {
		return stepError("key_catalog_unavailable", err)
	}
	sources := make(map[string]struct{})
	for _, channel := range catalog {
		if channel.Status == contracts.UpstreamChannelActive {
			sources[channel.SourceIdentity()] = struct{}{}
		}
	}
	want := len(sources)
	if plan.MaxChannels > 0 && want > plan.MaxChannels {
		want = plan.MaxChannels
	}
	selectedSources := make(map[string]struct{}, len(selected))
	for _, channel := range selected {
		selectedSources[channel.SourceIdentity()] = struct{}{}
	}
	if want == 0 || len(selectedSources) < want {
		return stepError("key_capacity_unavailable", nil)
	}
	return nil
}

func (r *Runner) verifyActive(
	ctx context.Context,
	workflow *contracts.OnboardingWorkflow,
	planID string,
	instance contracts.Instance,
	connector contracts.Connector,
	channels []contracts.UpstreamChannel,
	versions map[string]int64,
) error {
	plan, err := r.store.GetRoutePlan(ctx, planID)
	if err != nil || plan.Status != contracts.RoutePlanPublished {
		return stepError("publish_not_active", err)
	}
	bindings, err := r.store.ListPublishedBindings(ctx, planID)
	if err != nil {
		return stepError("binding_receipt_unavailable", err)
	}
	byChannel := make(map[string]contracts.PublishedBinding, len(bindings))
	for _, binding := range bindings {
		byChannel[binding.ChannelID] = binding
	}
	stepCtx, cancel, err := r.externalStepContext(ctx, workflow)
	if err != nil {
		return err
	}
	accounts, err := r.gateway.ListAccounts(stepCtx, instance.ID)
	cancel()
	if err != nil {
		return stepError("gateway_verification_failed", err)
	}
	accountByID := make(map[string]contracts.GatewayAccount, len(accounts))
	for _, account := range accounts {
		accountByID[account.ID] = account
	}
	for _, channel := range channels {
		binding, ok := byChannel[channel.ID]
		account, accountOK := accountByID[binding.RemoteID]
		if !ok || binding.State != contracts.BindingActive || binding.RemoteID == "" || !accountOK || !account.Schedulable {
			return stepError("gateway_binding_not_active", nil)
		}
		if channel.AccountOwnership.Normalize() != contracts.GatewayAccountPlatformManaged {
			continue
		}
		stepCtx, cancel, renewErr := r.externalStepContext(ctx, workflow)
		if renewErr != nil {
			return renewErr
		}
		verification, verifyErr := r.keys.Verify(stepCtx, channel.ID, instance.ID)
		cancel()
		if verifyErr != nil || verification.DeploymentRequired ||
			verification.Delivery.KeyVersion != versions[channel.ID] ||
			verification.Proof.ChannelID != channel.ID || verification.Proof.InstanceID != instance.ID ||
			verification.Proof.KeyVersion != versions[channel.ID] ||
			verification.Proof.Status != contracts.DeliveryKeyProofVerified ||
			verification.Proof.ConnectorID != connector.ID {
			return stepError("delivery_receipt_unverified", verifyErr)
		}
	}
	return nil
}

func (r *Runner) renewWorkflowLease(ctx context.Context, workflow *contracts.OnboardingWorkflow) error {
	if workflow == nil || workflow.Status != contracts.OnboardingRunning {
		return store.ErrConflict
	}
	lease := r.lease
	if lease <= 0 {
		lease = defaultLease
	}
	renewed, err := r.store.RenewOnboardingWorkflowLease(
		ctx, workflow.ID, workflow.LeaseOwner, workflow.Version, lease,
	)
	if err != nil {
		return err
	}
	*workflow = renewed
	return nil
}

func (r *Runner) externalStepContext(
	ctx context.Context,
	workflow *contracts.OnboardingWorkflow,
) (context.Context, context.CancelFunc, error) {
	if err := r.renewWorkflowLease(ctx, workflow); err != nil {
		return nil, nil, err
	}
	timeout := r.stepTimeout
	if timeout <= 0 {
		timeout = defaultStepTimeout
	}
	lease := r.lease
	if lease <= 0 {
		lease = defaultLease
	}
	// Never let an external call reach the lease boundary. Custom short leases
	// used by tests or operators receive the same conservative half-TTL cap.
	if max := lease / 2; max > 0 && timeout > max {
		timeout = max
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	return stepCtx, cancel, nil
}

func (r *Runner) advance(ctx context.Context, workflow contracts.OnboardingWorkflow, stage contracts.OnboardingStage) (contracts.OnboardingWorkflow, error) {
	workflow.Stage = stage
	workflow.Status = contracts.OnboardingRunning
	workflow.NextAttemptAt = nil
	workflow.LastErrorCode = ""
	return r.store.TransitionOnboardingWorkflow(ctx, workflow, workflow.Version)
}

func (r *Runner) deferWorkflow(ctx context.Context, workflow contracts.OnboardingWorkflow, cause error, waiting bool) error {
	code := errorCode(cause)
	next := r.now().UTC().Add(retryDelay(workflow.Attempts, waiting))
	if waiting {
		workflow.Stage = contracts.OnboardingWaitingConnector
		workflow.Status = contracts.OnboardingPending
	} else {
		workflow.Stage = contracts.OnboardingFailedRetryable
		workflow.Status = contracts.OnboardingRetryable
	}
	workflow.NextAttemptAt = &next
	workflow.LastErrorCode = code
	updated, err := r.store.TransitionOnboardingWorkflow(ctx, workflow, workflow.Version)
	if err != nil {
		return err
	}
	action := auditActionOnboardingRetryScheduled
	level := contracts.EventLevelNotice
	if updated.Attempts >= 3 {
		action = auditActionOnboardingFailed
		level = contracts.EventLevelWarning
	}
	r.audit(ctx, updated, action, "retrying", code, level)
	return cause
}

func (r *Runner) dormantWorkflow(ctx context.Context, workflow contracts.OnboardingWorkflow, cause error) error {
	code := errorCode(cause)
	workflow.Stage = contracts.OnboardingDormant
	workflow.Status = contracts.OnboardingDormantStatus
	workflow.NextAttemptAt = nil
	workflow.LastErrorCode = code
	updated, err := r.store.TransitionOnboardingWorkflow(ctx, workflow, workflow.Version)
	if err != nil {
		return err
	}
	r.audit(ctx, updated, auditActionOnboardingPaused, "paused", code, contracts.EventLevelNotice)
	return nil
}

func (r *Runner) audit(ctx context.Context, workflow contracts.OnboardingWorkflow, action, result, code string, level contracts.EventLevel) {
	details := map[string]string{
		"pool_id":            workflow.PoolID,
		"attempts":           strconv.Itoa(workflow.Attempts),
		"delivered_keys":     strconv.Itoa(len(workflow.KeyVersionSummary)),
		"desired_generation": strconv.FormatInt(workflow.DesiredGeneration, 10),
	}
	if code != "" {
		details["reason_code"] = code
	}
	if workflow.NextAttemptAt != nil {
		details["next_attempt_at"] = workflow.NextAttemptAt.UTC().Format(time.RFC3339)
	}
	if instance, err := r.store.GetInstance(ctx, workflow.InstanceID); err == nil {
		details["instance_name"] = instance.Name
	}
	if pool, err := r.store.GetUpstreamPool(ctx, workflow.PoolID); err == nil {
		details["pool_name"] = pool.Name
	}
	if _, err := r.store.AppendAudit(ctx, contracts.OperationAudit{
		UserID: workflow.UserID, InstanceID: workflow.InstanceID,
		ActorType: "system", ActorID: "e2m-onboarding", Action: action,
		RiskLevel: contracts.RiskLevelL2, EventLevel: level,
		TargetType: "onboarding_workflow", TargetID: workflow.ID,
		Result: result, ErrorMessage: code, WorkflowRunID: workflow.ID, Details: details,
	}); err == nil {
		r.notifyAudit(ctx, workflow, action, result, code, level, details)
	}
}

func (r *Runner) notifyAudit(
	ctx context.Context,
	workflow contracts.OnboardingWorkflow,
	action, result, code string,
	level contracts.EventLevel,
	details map[string]string,
) {
	if r.notifier == nil || !notifiableOnboardingAction(action) {
		return
	}
	title, text := onboardingNotification(action, code, details)
	r.notifier.Dispatch(ctx, workflow.UserID, notify.Event{
		UserID: workflow.UserID, InstanceID: workflow.InstanceID,
		EventLevel: level, RiskLevel: contracts.RiskLevelL2, Result: result,
		Title: title, Text: text, Fields: copyNotificationFields(details),
	})
}

func notifiableOnboardingAction(action string) bool {
	switch action {
	case auditActionOnboardingCompleted,
		auditActionOnboardingReconfigured,
		auditActionOnboardingRepaired,
		auditActionOnboardingRetryScheduled,
		auditActionOnboardingFailed,
		auditActionOnboardingPaused:
		return true
	default:
		return false
	}
}

func onboardingNotification(action, code string, details map[string]string) (string, string) {
	instance := displayOr(details["instance_name"], "未命名实例")
	pool := displayOr(details["pool_name"], "未命名上游池")
	switch action {
	case auditActionOnboardingCompleted:
		return "首次上游接入已完成", fmt.Sprintf(
			"上游池「%s」已部署到实例「%s」，账号和密钥交付已完成；正在等待首次真实请求或主动探测确认可调用。", pool, instance,
		)
	case auditActionOnboardingReconfigured:
		return "上游配置更新已完成", fmt.Sprintf(
			"上游池「%s」在实例「%s」中的配置已部署；正在等待真实请求或主动探测确认可调用。", pool, instance,
		)
	case auditActionOnboardingRepaired:
		return "上游配置已自动修复", fmt.Sprintf(
			"实例「%s」的上游池「%s」配置已重新部署；正在等待真实请求或主动探测确认可调用。", instance, pool,
		)
	case auditActionOnboardingFailed:
		return "上游接入连续失败", fmt.Sprintf(
			"上游池「%s」接入实例「%s」已连续失败 %s 次；原因：%s。系统将在 %s 再次重试。",
			pool, instance, displayOr(details["attempts"], "多"), onboardingReason(code),
			displayOr(details["next_attempt_at"], "稍后"),
		)
	case auditActionOnboardingRetryScheduled:
		return "上游接入暂未完成", fmt.Sprintf(
			"上游池「%s」暂未接入实例「%s」；原因：%s。系统将在 %s 自动重试。",
			pool, instance, onboardingReason(code), displayOr(details["next_attempt_at"], "稍后"),
		)
	default:
		return "上游自动接入已暂停", fmt.Sprintf(
			"上游池「%s」已停用，实例「%s」的自动接入已暂停。", pool, instance,
		)
	}
}

func onboardingReason(code string) string {
	switch code {
	case "connector_unavailable":
		return "连接器当前不可用"
	case "connector_gateway_not_ready":
		return "连接器中的网关配置尚未就绪"
	case "connector_capability_missing":
		return "连接器缺少完成接入所需的能力"
	case "gateway_unavailable", "gateway_verification_failed":
		return "实例网关当前不可用"
	case "binding_delivery_failed", "binding_delivery_invalid":
		return "上游密钥未能安全下发"
	case "binding_proof_failed", "delivery_receipt_unverified":
		return "上游密钥核验未通过"
	case "gateway_binding_not_active":
		return "上游账号尚未在实例中生效"
	case "key_assignment_failed", "key_capacity_unavailable", "key_catalog_unavailable":
		return "当前没有可用的上游账号或密钥"
	case "publish_failed", "publish_not_active", "binding_receipt_unavailable":
		return "上游账号和流量调度配置尚未生效"
	case "route_plan_create_conflict", "route_plan_create_failed", "route_plan_store_failed", "route_plan_suspended":
		return "实例的上游发布计划尚未就绪"
	case "instance_unavailable":
		return "目标实例当前不可用"
	case "pool_unavailable", "pool_inactive":
		return "上游池当前不可用"
	case "user_ineligible":
		return "当前账号不符合自动接入条件"
	default:
		return "接入流程暂时未完成"
	}
}

func displayOr(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func copyNotificationFields(details map[string]string) map[string]string {
	fields := map[string]string{}
	for source, target := range map[string]string{
		"instance_name":   "instanceName",
		"pool_name":       "poolName",
		"attempts":        "attempts",
		"next_attempt_at": "nextAttemptAt",
		"reason_code":     "reasonCode",
	} {
		if value := strings.TrimSpace(details[source]); value != "" {
			fields[target] = value
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

type codedError struct {
	code string
	err  error
}

func (e *codedError) Error() string {
	if e.err != nil {
		return e.code + ": " + e.err.Error()
	}
	return e.code
}

func (e *codedError) Unwrap() error { return e.err }

func stepError(code string, err error) error { return &codedError{code: code, err: err} }

func errorCode(err error) string {
	var coded *codedError
	if errors.As(err, &coded) && coded.code != "" {
		return coded.code
	}
	return "onboarding_failed"
}

func retryDelay(attempts int, waiting bool) time.Duration {
	if waiting {
		return 30 * time.Second
	}
	if attempts < 1 {
		attempts = 1
	}
	shift := attempts - 1
	if shift > 5 {
		shift = 5
	}
	delay := 15 * time.Second * time.Duration(1<<shift)
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func hasCapability(values []contracts.ConnectorTaskType, want contracts.ConnectorTaskType) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasRole(values []contracts.UserRole, want contracts.UserRole) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type fingerprintChannel struct {
	ID                  string                            `json:"id"`
	SourceID            string                            `json:"source_id"`
	Ownership           contracts.GatewayAccountOwnership `json:"ownership"`
	DisplayName         string                            `json:"display_name"`
	Provider            string                            `json:"provider"`
	Models              []string                          `json:"models"`
	Groups              []string                          `json:"groups"`
	CredentialBindingID string                            `json:"credential_binding_id"`
	ProxyBindingID      string                            `json:"proxy_binding_id"`
	Priority            int                               `json:"priority"`
	Weight              int                               `json:"weight"`
	DeliveryKeyVersion  int64                             `json:"delivery_key_version"`
	Labels              [][2]string                       `json:"labels"`
}

type fingerprintDesiredState struct {
	PoolID             string                       `json:"pool_id"`
	PoolStatus         contracts.UpstreamPoolStatus `json:"pool_status"`
	InstanceID         string                       `json:"instance_id"`
	InstanceKind       contracts.InstanceKind       `json:"instance_kind"`
	ConnectorID        string                       `json:"connector_id"`
	ConnectorKey       string                       `json:"connector_key"`
	Channels           []fingerprintChannel         `json:"channels"`
	Rollout            contracts.RolloutMode        `json:"rollout"`
	RolloutBatchSize   int                          `json:"rollout_batch_size"`
	RolloutCanaryCount int                          `json:"rollout_canary_count"`
	RolloutTargetID    string                       `json:"rollout_target_id"`
}

// desiredFingerprint intentionally excludes secret refs, masked values, and
// plaintext. Only fields that can change the gateway desired state are hashed.
func desiredFingerprint(
	pool contracts.UpstreamPool,
	channels []contracts.UpstreamChannel,
	deliveryVersions map[string]int64,
	instance contracts.Instance,
	connector contracts.Connector,
	rollout contracts.PoolRolloutResolution,
) (string, error) {
	desired := fingerprintDesiredState{
		PoolID: pool.ID, PoolStatus: pool.Status, InstanceID: instance.ID,
		InstanceKind: instance.Kind, ConnectorID: strings.TrimSpace(instance.ConnectorID),
		ConnectorKey: connector.Gateway.BindingEncryptionPublicKey,
		Rollout:      rollout.Rollout, RolloutBatchSize: rollout.RolloutBatchSize,
		RolloutCanaryCount: rollout.RolloutCanaryCount, RolloutTargetID: rollout.TargetID,
	}
	for _, channel := range channels {
		if channel.Status != contracts.UpstreamChannelActive {
			continue
		}
		item := fingerprintChannel{
			ID: channel.ID, SourceID: channel.SourceIdentity(), Ownership: channel.AccountOwnership.Normalize(),
			DisplayName: channel.DisplayName, Provider: channel.Provider,
			Models: append([]string(nil), channel.Models...), Groups: append([]string(nil), channel.Groups...),
			CredentialBindingID: strings.TrimSpace(channel.CredentialBindingID),
			ProxyBindingID:      strings.TrimSpace(channel.ProxyBindingID), Priority: channel.Priority, Weight: channel.Weight,
		}
		if item.Ownership == contracts.GatewayAccountPlatformManaged {
			item.DeliveryKeyVersion = deliveryVersions[channel.ID]
		}
		sort.Strings(item.Models)
		sort.Strings(item.Groups)
		for key, value := range channel.Labels {
			item.Labels = append(item.Labels, [2]string{key, value})
		}
		sort.Slice(item.Labels, func(i, j int) bool {
			if item.Labels[i][0] != item.Labels[j][0] {
				return item.Labels[i][0] < item.Labels[j][0]
			}
			return item.Labels[i][1] < item.Labels[j][1]
		})
		desired.Channels = append(desired.Channels, item)
	}
	sort.Slice(desired.Channels, func(i, j int) bool { return desired.Channels[i].ID < desired.Channels[j].ID })
	raw, err := json.Marshal(desired)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func newWorkerID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err == nil {
		return "onboarding-" + hex.EncodeToString(raw)
	}
	return fmt.Sprintf("onboarding-%d", time.Now().UnixNano())
}
