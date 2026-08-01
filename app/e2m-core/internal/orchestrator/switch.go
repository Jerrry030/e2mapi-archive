// Package orchestrator drives control-plane actions across gateway adapters,
// applying RiskLevel gating and writing an OperationAudit for every write.
package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/adapters"
	"e2m.local/core/internal/store"
)

// Orchestrator coordinates account operations against a managed instance.
type Orchestrator struct {
	store    store.Store
	adapters map[contracts.InstanceKind]adapters.GatewayAdapter
}

// ErrManagedAccountSchedulingFence is returned when a direct gateway
// scheduling write targets an account owned by a RoutePlan without carrying
// that plan's current scheduling generation. Managed accounts must be changed
// through publish/auto-switch so delayed Connector work remains ordered.
var ErrManagedAccountSchedulingFence = errors.New(strings.TrimSuffix(contracts.LegacyManagedAccountSchedulingFencePrefix, ": account "))

func New(st store.Store, adapterSet map[contracts.InstanceKind]adapters.GatewayAdapter) *Orchestrator {
	return &Orchestrator{store: st, adapters: adapterSet}
}

func (o *Orchestrator) adapterFor(kind contracts.InstanceKind) (adapters.GatewayAdapter, error) {
	a, ok := o.adapters[kind]
	if !ok {
		return nil, fmt.Errorf("orchestrator: no adapter for kind %q", kind)
	}
	return a, nil
}

// ListAccounts returns the upstream accounts of an instance.
func (o *Orchestrator) ListAccounts(ctx context.Context, instanceID string) ([]contracts.GatewayAccount, error) {
	inst, err := o.store.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	a, err := o.adapterFor(inst.Kind)
	if err != nil {
		return nil, err
	}
	return a.ListAccounts(ctx, inst)
}

// ProbeQuality asks the instance's Connector to make one real, account-scoped
// upstream request. The task contains no URL or credential; those stay local to
// the Connector. Adapters that do not implement the optional probe capability
// fail closed rather than synthesizing recovery evidence.
func (o *Orchestrator) ProbeQuality(
	ctx context.Context,
	instanceID string,
	input contracts.ConnectorGatewayQualityProbeInput,
	identity string,
) (contracts.ConnectorGatewayQualityProbeResult, error) {
	inst, err := o.store.GetInstance(ctx, instanceID)
	if err != nil {
		return contracts.ConnectorGatewayQualityProbeResult{}, err
	}
	a, err := o.adapterFor(inst.Kind)
	if err != nil {
		return contracts.ConnectorGatewayQualityProbeResult{}, err
	}
	probe, ok := a.(adapters.QualityProbeAdapter)
	if !ok {
		return contracts.ConnectorGatewayQualityProbeResult{}, fmt.Errorf("orchestrator: quality probe is unsupported for kind %q", inst.Kind)
	}
	return probe.ProbeQuality(ctx, inst, input, identity)
}

// ProveBinding asks the instance's Connector for a challenge-bound HMAC over
// one local credential binding. Core receives no binding plaintext.
func (o *Orchestrator) ProveBinding(
	ctx context.Context,
	instanceID string,
	input contracts.ConnectorGatewayBindingProofInput,
) (contracts.ConnectorGatewayBindingProofResult, error) {
	inst, err := o.store.GetInstance(ctx, instanceID)
	if err != nil {
		return contracts.ConnectorGatewayBindingProofResult{}, err
	}
	a, err := o.adapterFor(inst.Kind)
	if err != nil {
		return contracts.ConnectorGatewayBindingProofResult{}, err
	}
	prover, ok := a.(adapters.BindingProofAdapter)
	if !ok {
		return contracts.ConnectorGatewayBindingProofResult{}, fmt.Errorf("orchestrator: binding proof is unsupported for kind %q", inst.Kind)
	}
	return prover.ProveBinding(ctx, inst, input)
}

// InstallBinding queues a Core-created ciphertext envelope for the selected
// instance. The envelope is already encrypted to that Connector's public key.
func (o *Orchestrator) InstallBinding(
	ctx context.Context,
	instanceID string,
	input contracts.ConnectorGatewayBindingInstallInput,
) (contracts.ConnectorGatewayBindingInstallResult, error) {
	inst, err := o.store.GetInstance(ctx, instanceID)
	if err != nil {
		return contracts.ConnectorGatewayBindingInstallResult{}, err
	}
	a, err := o.adapterFor(inst.Kind)
	if err != nil {
		return contracts.ConnectorGatewayBindingInstallResult{}, err
	}
	installer, ok := a.(adapters.BindingInstaller)
	if !ok {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("orchestrator: binding install is unsupported for kind %q", inst.Kind)
	}
	return installer.InstallBinding(ctx, inst, input)
}

// SetSchedulable toggles one account's scheduling participation. This is the L1
// primitive behind manual switching; every call is audited.
func (o *Orchestrator) SetSchedulable(ctx context.Context, instanceID, accountID string, schedulable bool, reason string) error {
	inst, err := o.store.GetInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	if err := o.validateSchedulableAccounts(ctx, inst, []string{accountID}); err != nil {
		o.audit(ctx, inst, accountID, actionSchedulable(schedulable), reason, err)
		return err
	}
	a, err := o.adapterFor(inst.Kind)
	if err != nil {
		return err
	}
	err = a.SetSchedulable(ctx, inst, accountID, schedulable)
	o.audit(ctx, inst, accountID, actionSchedulable(schedulable), reason, err)
	return err
}

// SetTrafficShare applies one exact gateway-native integer weight. It is
// deliberately unavailable on adapters without the native capability and
// repeats the managed-account generation fence immediately before dispatch.
// Every attempted side effect is written as an L1 audit with an opaque account
// target and a numerical (including explicit zero) weight action.
func (o *Orchestrator) SetTrafficShare(ctx context.Context, instanceID, accountID string, weight int, reason string) error {
	inst, err := o.store.GetInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || weight < 0 || weight > 100 {
		err = errors.New("orchestrator: traffic share input is invalid")
		o.audit(ctx, inst, accountID, actionTrafficShare(weight), reason, err)
		return err
	}
	if inst.Kind != contracts.InstanceKindNewAPI {
		err = fmt.Errorf("orchestrator: traffic share is unsupported for kind %q", inst.Kind)
		o.audit(ctx, inst, accountID, actionTrafficShare(weight), reason, err)
		return err
	}
	if err = o.validateSchedulableAccounts(ctx, inst, []string{accountID}); err != nil {
		o.audit(ctx, inst, accountID, actionTrafficShare(weight), reason, err)
		return err
	}
	a, adapterErr := o.adapterFor(inst.Kind)
	if adapterErr != nil {
		o.audit(ctx, inst, accountID, actionTrafficShare(weight), reason, adapterErr)
		return adapterErr
	}
	weighted, ok := a.(adapters.TrafficShareAdapter)
	if !ok {
		err = fmt.Errorf("orchestrator: traffic share is unsupported for kind %q", inst.Kind)
		o.audit(ctx, inst, accountID, actionTrafficShare(weight), reason, err)
		return err
	}
	err = weighted.SetTrafficShare(ctx, inst, accountID, weight)
	o.audit(ctx, inst, accountID, actionTrafficShare(weight), reason, err)
	return err
}

// ValidateSchedulableAccounts preflights a batch of gateway scheduling writes.
// Approval uses it both before persisting a request and again after claiming
// execution. SetSchedulable repeats the same check at the side-effect boundary
// to close the submit/approve ownership race.
func (o *Orchestrator) ValidateSchedulableAccounts(ctx context.Context, instanceID string, accountIDs []string) error {
	inst, err := o.store.GetInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	return o.validateSchedulableAccounts(ctx, inst, accountIDs)
}

func (o *Orchestrator) validateSchedulableAccounts(ctx context.Context, inst contracts.Instance, accountIDs []string) error {
	if o.store == nil {
		return errors.New("orchestrator: store is not configured")
	}
	targets := make(map[string]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID = strings.TrimSpace(accountID); accountID != "" {
			targets[accountID] = struct{}{}
		}
	}
	if len(targets) == 0 {
		return nil
	}

	bindings, err := o.store.ListPublishedBindings(ctx, "")
	if err != nil {
		return fmt.Errorf("orchestrator: list managed account bindings: %w", err)
	}
	fence, fenced := contracts.GatewaySchedulingFenceFromContext(ctx)
	// Hybrid aggregate accounts are managed by their own execution generation,
	// not by RoutePlan published bindings. Always classify persisted aggregate
	// IDs, including ordinary/manual calls, so omitting the Hybrid identity can
	// never turn a managed platform account into an unfenced account.
	hybridBindings, bindingErr := o.store.ListHybridGatewayBindings(ctx, inst.UserID, inst.ID)
	if bindingErr != nil {
		return fmt.Errorf("orchestrator: list hybrid gateway bindings: %w", bindingErr)
	}
	hybridExecutions, executionListErr := o.store.ListHybridRoutingExecutions(ctx, inst.UserID, inst.ID, 100)
	if executionListErr != nil {
		return fmt.Errorf("orchestrator: list hybrid routing executions: %w", executionListErr)
	}
	activeHybridExecutions := make([]contracts.HybridRoutingExecution, 0, 1)
	for _, execution := range hybridExecutions {
		if execution.Status == contracts.HybridRoutingExecutionPending || execution.Status == contracts.HybridRoutingExecutionApplying {
			activeHybridExecutions = append(activeHybridExecutions, execution)
		}
	}
	identity, hasIdentity := contracts.ConnectorExecutionIdentityFromContext(ctx)
	var hybridExecution contracts.HybridRoutingExecution
	hybridExecutionLoaded := false
	for targetAccountID := range targets {
		hybridManaged := false
		for _, binding := range hybridBindings {
			if strings.TrimSpace(binding.RemoteAccountID) == targetAccountID {
				hybridManaged = true
				break
			}
		}
		for _, execution := range activeHybridExecutions {
			if hybridExecutionOwnsAccount(execution, targetAccountID) {
				hybridManaged = true
				break
			}
		}
		if !hybridManaged {
			continue
		}
		// The context carries a generation-level base fence. Its per-task
		// sequence is allocated later by the Connector adapter, so validate the
		// Hybrid scope with a synthetic positive sequence without changing the
		// fence that will be dispatched.
		identityFence := fence
		identityFence.Sequence = 1
		instanceID, accountID, ok := contracts.GatewaySchedulingFenceHybridIdentity(identityFence)
		if !hasIdentity || !contracts.ValidHybridRoutingExecutionIdentity(identity) || !fenced || !ok ||
			instanceID != inst.ID || accountID != targetAccountID || fence.Version != identity.Generation {
			return fmt.Errorf("orchestrator: hybrid account %s requires its current applying execution identity and fence", targetAccountID)
		}
		if !hybridExecutionLoaded {
			hybridExecution, err = o.store.GetHybridRoutingExecution(ctx, inst.UserID, identity.ID)
			if err != nil {
				return fmt.Errorf("orchestrator: load hybrid routing execution %s: %w", identity.ID, err)
			}
			hybridExecutionLoaded = true
		}
		activeExecutionMatch := false
		for _, execution := range activeHybridExecutions {
			if execution.ID == hybridExecution.ID && execution.Generation == hybridExecution.Generation {
				activeExecutionMatch = true
				break
			}
		}
		if hybridExecution.Status != contracts.HybridRoutingExecutionApplying || hybridExecution.UserID != inst.UserID ||
			hybridExecution.InstanceID != inst.ID || hybridExecution.Generation != identity.Generation ||
			!activeExecutionMatch ||
			!hybridExecutionOwnsAccount(hybridExecution, targetAccountID) {
			return fmt.Errorf("orchestrator: hybrid account %s requires its current applying execution identity and fence", targetAccountID)
		}
	}
	loadedPlans := make(map[string]contracts.RoutePlan)
	for _, binding := range bindings {
		remoteID := strings.TrimSpace(binding.RemoteID)
		if _, targeted := targets[remoteID]; !targeted || remoteID == "" {
			continue
		}
		if binding.InstanceID != "" && binding.InstanceID != inst.ID {
			continue
		}
		if !binding.RequiresSchedulingFence() {
			continue
		}

		plan, loaded := loadedPlans[binding.PlanID]
		if !loaded {
			plan, err = o.store.GetRoutePlan(ctx, binding.PlanID)
			if err != nil {
				return fmt.Errorf("orchestrator: load managed account route plan %s: %w", binding.PlanID, err)
			}
			loadedPlans[binding.PlanID] = plan
		}
		if plan.InstanceID != inst.ID {
			if binding.InstanceID == inst.ID {
				return fmt.Errorf("orchestrator: managed account binding %s has inconsistent instance ownership", binding.ID)
			}
			continue
		}

		expectedScope := "auto-switch/plan/" + plan.ID
		if !fenced || fence.Scope != expectedScope || fence.Version != plan.SchedulingGeneration {
			return fmt.Errorf("%w: account %s belongs to route plan %s at generation %d",
				ErrManagedAccountSchedulingFence, remoteID, plan.ID, plan.SchedulingGeneration)
		}
	}
	return nil
}

func hybridExecutionOwnsAccount(execution contracts.HybridRoutingExecution, accountID string) bool {
	for _, value := range execution.DesiredWeights {
		if value.AccountID == accountID {
			return true
		}
	}
	return false
}

// SchedulingBarrier advances the Connector's durable plan watermark without
// changing any gateway account. Publish calls it before reading gateway facts
// so older queued scheduling tasks cannot land after a newer no-op decision.
func (o *Orchestrator) SchedulingBarrier(ctx context.Context, instanceID string) error {
	inst, err := o.store.GetInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	a, err := o.adapterFor(inst.Kind)
	if err != nil {
		return err
	}
	barrier, ok := a.(adapters.SchedulingBarrierAdapter)
	if !ok {
		return fmt.Errorf("orchestrator: scheduling barrier is unsupported for kind %q", inst.Kind)
	}
	return barrier.SchedulingBarrier(ctx, inst)
}

// SwitchUpstream performs a manual switch: take the problem account out of
// scheduling, then (optionally) bring a backup in. Both steps are audited. If
// the disable succeeds but the enable fails, the error is surfaced so the
// operator knows the pool is in a partial state.
func (o *Orchestrator) SwitchUpstream(ctx context.Context, sw contracts.AccountSwitch) error {
	inst, err := o.store.GetInstance(ctx, sw.InstanceID)
	if err != nil {
		return err
	}
	if err := o.validateSchedulableAccounts(ctx, inst, []string{sw.DisableAccountID, sw.EnableAccountID}); err != nil {
		return err
	}
	if sw.DisableAccountID != "" {
		derr := o.SetSchedulable(ctx, inst.ID, sw.DisableAccountID, false, sw.Reason)
		if derr != nil {
			return fmt.Errorf("switch: disable %s failed: %w", sw.DisableAccountID, derr)
		}
	}

	if sw.EnableAccountID != "" {
		eerr := o.SetSchedulable(ctx, inst.ID, sw.EnableAccountID, true, sw.Reason)
		if eerr != nil {
			return fmt.Errorf("switch: disabled %s but enabling backup %s failed: %w",
				sw.DisableAccountID, sw.EnableAccountID, eerr)
		}
	}

	return nil
}

// ProvisionAccount creates (or adopts) a remote account/channel on an instance
// from a platform-managed spec, auditing the write at L2 (create is
// higher-risk than a schedule toggle). Returns the gateway-native id.
func (o *Orchestrator) ProvisionAccount(ctx context.Context, instanceID string, spec contracts.GatewayAccountSpec, reason string) (contracts.GatewayProvisionResult, error) {
	inst, err := o.store.GetInstance(ctx, instanceID)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	a, err := o.adapterFor(inst.Kind)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	res, err := a.ProvisionAccount(ctx, inst, spec)
	target := res.RemoteID
	if target == "" {
		target = spec.ChannelID
	}
	o.auditLevel(ctx, inst, target, "account.provision", reason, contracts.RiskLevelL2, err)
	return res, err
}

// UpdateAccount pushes a changed spec onto an existing remote account.
func (o *Orchestrator) UpdateAccount(ctx context.Context, instanceID string, spec contracts.GatewayAccountSpec, reason string) (contracts.GatewayProvisionResult, error) {
	inst, err := o.store.GetInstance(ctx, instanceID)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	a, err := o.adapterFor(inst.Kind)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	res, err := a.UpdateAccount(ctx, inst, spec)
	o.auditLevel(ctx, inst, spec.RemoteID, "account.update", reason, contracts.RiskLevelL2, err)
	return res, err
}

// DeleteAccount removes a remote account/channel the platform created.
func (o *Orchestrator) DeleteAccount(ctx context.Context, instanceID, accountID, reason string) error {
	inst, err := o.store.GetInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	a, err := o.adapterFor(inst.Kind)
	if err != nil {
		return err
	}
	err = a.DeleteAccount(ctx, inst, accountID)
	o.auditLevel(ctx, inst, accountID, "account.deprovision", reason, contracts.RiskLevelL2, err)
	return err
}

// SupportsLifecycleAction reports protocol-v2 lifecycle support to the
// publish preflight. The action-specific ownership guard remains in the
// reconcile engine and Connector task contract.
func (o *Orchestrator) SupportsLifecycleAction(ctx context.Context, instanceID string, action contracts.ReconcileActionType) bool {
	inst, err := o.store.GetInstance(ctx, instanceID)
	if err != nil {
		return false
	}
	a, err := o.adapterFor(inst.Kind)
	if err != nil {
		return false
	}
	for _, capability := range a.Capabilities() {
		if !capability.Supported {
			continue
		}
		switch action {
		case contracts.ReconcileCreate:
			if capability.Name == contracts.CapabilityCreateAccount {
				return true
			}
		case contracts.ReconcileUpdate:
			if capability.Name == contracts.CapabilityUpdateAccount {
				return true
			}
		case contracts.ReconcileDeprovision:
			if capability.Name == contracts.CapabilityDeleteAccount {
				return true
			}
		}
	}
	return false
}

func (o *Orchestrator) audit(ctx context.Context, inst contracts.Instance, accountID, action, reason string, opErr error) {
	o.auditLevel(ctx, inst, accountID, action, reason, contracts.RiskLevelL1, opErr)
}

func (o *Orchestrator) auditLevel(ctx context.Context, inst contracts.Instance, accountID, action, reason string, level contracts.RiskLevel, opErr error) {
	result := "accepted"
	errMsg := ""
	if opErr != nil {
		result = "failed"
		errMsg = opErr.Error()
	}
	actor := contracts.Actor{Type: "operator", ID: "console"}
	if a, ok := contracts.ActorFromContext(ctx); ok {
		actor = a
	}
	// Audit failures must not mask the original error; ignore the append error.
	_, _ = o.store.AppendAudit(ctx, contracts.OperationAudit{
		UserID:       inst.UserID,
		InstanceID:   inst.ID,
		ActorType:    actor.Type,
		ActorID:      actor.ID,
		Action:       action,
		RiskLevel:    level,
		TargetType:   "account",
		TargetID:     accountID,
		Result:       result,
		ErrorMessage: errMsg,
		RequestHash:  auditReasonHash(reason),
	})
}

func auditReasonHash(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(reason))
	return hex.EncodeToString(sum[:])
}

func actionSchedulable(schedulable bool) string {
	if schedulable {
		return "account.enable_schedulable"
	}
	return "account.disable_schedulable"
}

func actionTrafficShare(weight int) string {
	return fmt.Sprintf("account.traffic_share.set.%d", weight)
}

// ErrUnsupportedKind is returned when no adapter is registered for an instance kind.
var ErrUnsupportedKind = errors.New("orchestrator: unsupported instance kind")
