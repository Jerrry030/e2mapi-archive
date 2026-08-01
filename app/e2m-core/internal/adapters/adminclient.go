package adapters

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"e2m.local/contracts"
)

const (
	connectorTaskSchemaVersion = 1
	// Callers wait synchronously for at most timeout, but queued work must live
	// long enough for all declared lease attempts and their retry backoffs.
	connectorTaskExecutionWindow = 3 * time.Minute
)

const (
	deferredAccountDeleteDelay   = 30 * time.Minute
	deferredAccountDeleteExpiry  = 365 * 24 * time.Hour
	deferredAccountDeleteRetries = 12
)

// ErrGatewayMutationNotDispatched identifies validation failures returned
// before a Connector task is created. Callers may use it as durable proof that
// this attempt could not have changed the remote gateway.
var ErrGatewayMutationNotDispatched = errors.New("connector gateway: mutation was not dispatched")

type gatewayMutationNotDispatchedError struct{ err error }

func (e *gatewayMutationNotDispatchedError) Error() string { return e.err.Error() }
func (e *gatewayMutationNotDispatchedError) Unwrap() []error {
	return []error{ErrGatewayMutationNotDispatched, e.err}
}

func mutationNotDispatched(err error) error {
	if err == nil {
		return nil
	}
	return &gatewayMutationNotDispatchedError{err: err}
}

// ConnectorTaskStore is the persistence surface needed by the typed gateway
// client. Core only queues domain operations; gateway HTTP details live in the
// connector runtime.
type ConnectorTaskStore interface {
	CreateConnectorTask(context.Context, contracts.ConnectorTask) (contracts.ConnectorTask, error)
	GetConnectorTask(context.Context, string) (contracts.ConnectorTask, error)
}

type connectorTaskLister interface {
	ListConnectorTasks(context.Context, contracts.ConnectorTaskFilter) ([]contracts.ConnectorTask, error)
}

// ConnectorGatewayClient implements GatewayAdapter by creating typed connector
// tasks and waiting for their typed results.
type ConnectorGatewayClient struct {
	store   ConnectorTaskStore
	kind    contracts.InstanceKind
	timeout time.Duration
	poll    time.Duration
	now     func() time.Time
}

var _ GatewayAdapter = (*ConnectorGatewayClient)(nil)
var _ SchedulingBarrierAdapter = (*ConnectorGatewayClient)(nil)
var _ TrafficShareAdapter = (*ConnectorGatewayClient)(nil)
var _ BindingProofAdapter = (*ConnectorGatewayClient)(nil)
var _ BindingInstaller = (*ConnectorGatewayClient)(nil)

func NewConnectorGatewayClient(st ConnectorTaskStore, kind contracts.InstanceKind) *ConnectorGatewayClient {
	return &ConnectorGatewayClient{
		store:   st,
		kind:    kind,
		timeout: 30 * time.Second,
		poll:    250 * time.Millisecond,
		now:     time.Now,
	}
}

func (c *ConnectorGatewayClient) Kind() contracts.InstanceKind { return c.kind }

func (c *ConnectorGatewayClient) Capabilities() []contracts.AdapterCapability {
	return gatewayCapabilities(c.kind)
}

func (c *ConnectorGatewayClient) ListAccounts(ctx context.Context, instance contracts.Instance) ([]contracts.GatewayAccount, error) {
	var result contracts.ConnectorGatewayAccountsListResult
	err := c.runTask(
		ctx,
		instance,
		contracts.ConnectorTaskGatewayAccountsList,
		contracts.ConnectorGatewayAccountsListInput{},
		&result,
	)
	if err != nil {
		return nil, err
	}
	if result.Accounts == nil {
		return []contracts.GatewayAccount{}, nil
	}
	return result.Accounts, nil
}

// ProbeQuality queues a connector-local probe for one published account. The
// identity should be stable for one logical probe attempt (for example a
// circuit transition id) and distinct for later samples.
func (c *ConnectorGatewayClient) ProbeQuality(
	ctx context.Context,
	instance contracts.Instance,
	input contracts.ConnectorGatewayQualityProbeInput,
	identity string,
) (contracts.ConnectorGatewayQualityProbeResult, error) {
	input.AccountID = strings.TrimSpace(input.AccountID)
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	input.Model = strings.TrimSpace(input.Model)
	input.EndpointPath = strings.TrimSpace(input.EndpointPath)
	identity = strings.TrimSpace(identity)
	if !contracts.IsConnectorQualityProbeField(input.AccountID) ||
		!contracts.IsConnectorQualityProbeField(input.ChannelID) ||
		!contracts.IsConnectorQualityProbeField(input.Model) ||
		!contracts.IsQualityProbeCapability(input.Capability) ||
		!contracts.IsQualityProbeEndpointPath(input.EndpointPath) {
		return contracts.ConnectorGatewayQualityProbeResult{}, fmt.Errorf("connector gateway: quality probe requires account_id, channel_id, model, capability, and endpoint_path")
	}
	if identity == "" {
		return contracts.ConnectorGatewayQualityProbeResult{}, fmt.Errorf("connector gateway: quality probe identity is required")
	}
	var result contracts.ConnectorGatewayQualityProbeResult
	err := c.runTaskWithIdentity(ctx, instance, contracts.ConnectorTaskGatewayQualityProbe, input, identity, &result)
	if err == nil && (result.Capability != input.Capability || result.EndpointPath != input.EndpointPath) {
		return contracts.ConnectorGatewayQualityProbeResult{}, fmt.Errorf("connector gateway: quality probe result scope does not match request")
	}
	return result, err
}

func (c *ConnectorGatewayClient) ProveBinding(
	ctx context.Context,
	instance contracts.Instance,
	input contracts.ConnectorGatewayBindingProofInput,
) (contracts.ConnectorGatewayBindingProofResult, error) {
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	input.BindingID = strings.TrimSpace(input.BindingID)
	if !contracts.IsConnectorQualityProbeField(input.ChannelID) ||
		!contracts.IsConnectorQualityProbeField(input.BindingID) ||
		len(input.Challenge) != contracts.ConnectorGatewayBindingProofChallengeHexLength ||
		strings.ToLower(input.Challenge) != input.Challenge {
		return contracts.ConnectorGatewayBindingProofResult{}, fmt.Errorf("connector gateway: binding proof input is invalid")
	}
	if challenge, err := hex.DecodeString(input.Challenge); err != nil || len(challenge) != contracts.ConnectorGatewayBindingProofChallengeHexLength/2 {
		return contracts.ConnectorGatewayBindingProofResult{}, fmt.Errorf("connector gateway: binding proof challenge is invalid")
	}
	var result contracts.ConnectorGatewayBindingProofResult
	if err := c.runTask(ctx, instance, contracts.ConnectorTaskGatewayBindingProof, input, &result); err != nil {
		return contracts.ConnectorGatewayBindingProofResult{}, err
	}
	if proof, err := hex.DecodeString(result.Proof); err != nil || len(proof) != sha256.Size || strings.ToLower(result.Proof) != result.Proof {
		return contracts.ConnectorGatewayBindingProofResult{}, fmt.Errorf("connector gateway: binding proof result is invalid")
	}
	return result, nil
}

func (c *ConnectorGatewayClient) InstallBinding(
	ctx context.Context,
	instance contracts.Instance,
	input contracts.ConnectorGatewayBindingInstallInput,
) (contracts.ConnectorGatewayBindingInstallResult, error) {
	input.BindingID = strings.TrimSpace(input.BindingID)
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	if !contracts.IsConnectorQualityProbeField(input.BindingID) ||
		!contracts.IsConnectorQualityProbeField(input.ChannelID) || input.KeyVersion <= 0 ||
		!contracts.IsConnectorBindingCiphertext(input.Ciphertext) {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("connector gateway: binding install input is invalid")
	}
	stableInput, err := json.Marshal(struct {
		BindingID  string `json:"binding_id"`
		ChannelID  string `json:"channel_id"`
		KeyVersion int64  `json:"key_version"`
	}{input.BindingID, input.ChannelID, input.KeyVersion})
	if err != nil {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("connector gateway: encode binding install identity: %w", err)
	}
	idempotencyKey := connectorTaskIdempotencyKey(c.kind, instance, contracts.ConnectorTaskGatewayBindingInstall, stableInput, "binding-install-v1")
	var result contracts.ConnectorGatewayBindingInstallResult
	if err := c.runTaskAtWithKey(ctx, instance, contracts.ConnectorTaskGatewayBindingInstall, input, "", time.Time{}, time.Time{}, idempotencyKey, &result); err != nil {
		return contracts.ConnectorGatewayBindingInstallResult{}, err
	}
	if result.BindingID != input.BindingID || result.ChannelID != input.ChannelID || result.KeyVersion != input.KeyVersion {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("connector gateway: binding install result does not match request")
	}
	return result, nil
}

func (c *ConnectorGatewayClient) SetSchedulable(ctx context.Context, instance contracts.Instance, accountID string, schedulable bool) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fmt.Errorf("connector gateway: account_id is required for schedulable update")
	}
	var result contracts.ConnectorGatewayMutationResult
	return c.runTask(
		ctx,
		instance,
		contracts.ConnectorTaskGatewaySchedulableSet,
		contracts.ConnectorGatewaySchedulableSetInput{
			AccountID:   accountID,
			Schedulable: schedulable,
		},
		&result,
	)
}

// SetTrafficShare queues a fenced native integer weight mutation. The ability
// is intentionally NewAPI-only; callers cannot accidentally approximate a
// staged percentage through the legacy boolean schedulable operation.
func (c *ConnectorGatewayClient) SetTrafficShare(ctx context.Context, instance contracts.Instance, accountID string, weight int) error {
	accountID = strings.TrimSpace(accountID)
	if !supportsAdapterCapability(c.Capabilities(), contracts.CapabilitySetAccountTrafficShare) {
		return mutationNotDispatched(fmt.Errorf("connector gateway: traffic share is unsupported for %s", c.kind))
	}
	if accountID == "" || weight < 0 || weight > 100 {
		return mutationNotDispatched(fmt.Errorf("connector gateway: traffic share input is invalid"))
	}
	fence, ok := contracts.NextGatewaySchedulingFence(ctx)
	if !ok {
		return mutationNotDispatched(fmt.Errorf("connector gateway: traffic share requires a scheduling fence"))
	}
	input := contracts.ConnectorGatewayTrafficShareSetInput{AccountID: accountID, Weight: weight, Fence: fence}
	if !contracts.ValidateConnectorGatewayTrafficShareSetInput(input) {
		return mutationNotDispatched(fmt.Errorf("connector gateway: traffic share input is invalid"))
	}
	var result contracts.ConnectorGatewayTrafficShareSetResult
	if err := c.runTask(ctx, instance, contracts.ConnectorTaskGatewayTrafficShareSet, input, &result); err != nil {
		return err
	}
	if !contracts.ValidateConnectorGatewayTrafficShareSetResult(input, result) {
		return fmt.Errorf("connector gateway: traffic share result does not match request")
	}
	return nil
}

func supportsAdapterCapability(capabilities []contracts.AdapterCapability, name contracts.CapabilityName) bool {
	for _, capability := range capabilities {
		if capability.Name == name && capability.Supported {
			return true
		}
	}
	return false
}

// SchedulingBarrier advances the Connector's durable scheduling watermark.
// It is only valid inside an automatic scheduling operation carrying a fence.
func (c *ConnectorGatewayClient) SchedulingBarrier(ctx context.Context, instance contracts.Instance) error {
	fence, ok := contracts.NextGatewaySchedulingFence(ctx)
	if !ok {
		return fmt.Errorf("connector gateway: scheduling barrier requires a scheduling fence")
	}
	var result contracts.ConnectorGatewayMutationResult
	return c.runTask(
		ctx,
		instance,
		contracts.ConnectorTaskGatewaySchedulingBarrier,
		contracts.ConnectorGatewaySchedulingBarrierInput{Fence: fence},
		&result,
	)
}

func (c *ConnectorGatewayClient) ProvisionAccount(ctx context.Context, instance contracts.Instance, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	spec.Ownership = spec.Ownership.Normalize()
	if !spec.Ownership.Valid() {
		return contracts.GatewayProvisionResult{}, fmt.Errorf("connector gateway: invalid account ownership")
	}
	if spec.Ownership != contracts.GatewayAccountPlatformManaged {
		return contracts.GatewayProvisionResult{}, fmt.Errorf("connector gateway: owner-provided accounts are update-only")
	}
	if err := validateLifecycleSpec(spec, false); err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	var result contracts.ConnectorGatewayMutationResult
	err := c.runTask(ctx, instance, contracts.ConnectorTaskGatewayAccountCreate,
		contracts.ConnectorGatewayAccountCreateInput{Spec: spec}, &result)
	return contracts.GatewayProvisionResult{RemoteID: result.RemoteID, Created: result.Created}, err
}

func (c *ConnectorGatewayClient) UpdateAccount(ctx context.Context, instance contracts.Instance, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	spec.Ownership = spec.Ownership.Normalize()
	if !spec.Ownership.Valid() {
		return contracts.GatewayProvisionResult{}, mutationNotDispatched(fmt.Errorf("connector gateway: invalid account ownership"))
	}
	if err := validateLifecycleSpec(spec, true); err != nil {
		return contracts.GatewayProvisionResult{}, mutationNotDispatched(err)
	}
	var result contracts.ConnectorGatewayMutationResult
	err := c.runTask(ctx, instance, contracts.ConnectorTaskGatewayAccountUpdate,
		contracts.ConnectorGatewayAccountUpdateInput{Spec: spec}, &result)
	return contracts.GatewayProvisionResult{RemoteID: result.RemoteID, Created: result.Created}, err
}

func (c *ConnectorGatewayClient) DeleteAccount(ctx context.Context, instance contracts.Instance, accountID string) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fmt.Errorf("connector gateway: account_id is required for delete")
	}
	fence, ok := contracts.NextGatewaySchedulingFence(ctx)
	if !ok {
		return fmt.Errorf("connector gateway: account delete requires a scheduling fence")
	}
	now := c.nowTime()
	input := contracts.ConnectorGatewayAccountDeleteInput{
		AccountID: accountID, Ownership: contracts.GatewayAccountPlatformManaged, Fence: &fence,
	}
	var result contracts.ConnectorGatewayMutationResult
	return c.runTaskAt(ctx, instance, contracts.ConnectorTaskGatewayAccountDelete, input,
		"delete:"+fence.Scope+fmt.Sprintf(":%d:%d", fence.Version, fence.Sequence),
		now.Add(deferredAccountDeleteDelay), now.Add(deferredAccountDeleteExpiry), &result)
}

func validateLifecycleSpec(spec contracts.GatewayAccountSpec, requireRemote bool) error {
	if strings.TrimSpace(spec.ChannelID) == "" {
		return fmt.Errorf("connector gateway: channel_id is required")
	}
	if spec.Ownership.Normalize() == contracts.GatewayAccountOwnerProvided &&
		(strings.TrimSpace(spec.CredentialBindingID) != "" || strings.TrimSpace(spec.ProxyBindingID) != "") {
		return fmt.Errorf("connector gateway: owner-provided update must be credential-blind")
	}
	if spec.Ownership.Normalize() == contracts.GatewayAccountPlatformManaged &&
		strings.TrimSpace(spec.CredentialBindingID) == "" {
		return fmt.Errorf("connector gateway: credential_binding_id is required for platform-managed lifecycle")
	}
	if requireRemote && strings.TrimSpace(spec.RemoteID) == "" {
		return fmt.Errorf("connector gateway: remote_id is required for update")
	}
	return nil
}

func (c *ConnectorGatewayClient) runTask(
	ctx context.Context,
	instance contracts.Instance,
	taskType contracts.ConnectorTaskType,
	input any,
	result any,
) error {
	return c.runTaskWithIdentity(ctx, instance, taskType, input, "", result)
}

func (c *ConnectorGatewayClient) runTaskWithIdentity(
	ctx context.Context,
	instance contracts.Instance,
	taskType contracts.ConnectorTaskType,
	input any,
	identity string,
	result any,
) error {
	return c.runTaskAt(ctx, instance, taskType, input, identity, time.Time{}, time.Time{}, result)
}

func (c *ConnectorGatewayClient) runTaskAt(
	ctx context.Context,
	instance contracts.Instance,
	taskType contracts.ConnectorTaskType,
	input any,
	identity string,
	availableAt time.Time,
	expiresAt time.Time,
	result any,
) error {
	return c.runTaskAtWithKey(ctx, instance, taskType, input, identity, availableAt, expiresAt, "", result)
}

func (c *ConnectorGatewayClient) runTaskAtWithKey(
	ctx context.Context,
	instance contracts.Instance,
	taskType contracts.ConnectorTaskType,
	input any,
	identity string,
	availableAt time.Time,
	expiresAt time.Time,
	idempotencyKeyOverride string,
	result any,
) error {
	if err := c.validateInstance(instance); err != nil {
		return err
	}
	if taskType.RiskLevel() == contracts.RiskLevelL3 {
		return fmt.Errorf("connector gateway: unsupported task type %q", taskType)
	}

	input = withSchedulingFence(ctx, taskType, input)
	rawInput, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("connector gateway: encode %s input: %w", taskType, err)
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	idempotencyKey := connectorTaskIdempotencyKey(c.kind, instance, taskType, rawInput, identity)
	if strings.TrimSpace(idempotencyKeyOverride) != "" {
		idempotencyKey = idempotencyKeyOverride
	}
	maxAttempts := 3
	if taskType == contracts.ConnectorTaskGatewayAccountDelete {
		maxAttempts = deferredAccountDeleteRetries
	}
	requested := contracts.ConnectorTask{
		UserID:         instance.UserID,
		InstanceID:     instance.ID,
		ConnectorID:    instance.ConnectorID,
		Type:           taskType,
		SchemaVersion:  connectorTaskSchemaVersion,
		RiskLevel:      taskType.RiskLevel(),
		Status:         contracts.ConnectorTaskPending,
		Input:          rawInput,
		IdempotencyKey: idempotencyKey,
		MaxAttempts:    maxAttempts,
		AvailableAt:    availableAt,
		ExpiresAt:      expiresAt,
	}
	if fence, fenced := contracts.ConnectorTaskSchedulingFence(requested); fenced {
		if planID, planScoped := contracts.GatewaySchedulingFencePlanID(fence); planScoped {
			if _, hasExecutionIdentity := contracts.ConnectorExecutionIdentityFromContext(ctx); hasExecutionIdentity {
				return mutationNotDispatched(fmt.Errorf("connector gateway: route-plan fence cannot carry a non-route-plan execution identity"))
			}
			requested.PlanID = planID
			requested.SchedulingGeneration = fence.Version
		} else if instanceID, _, hybridScoped := contracts.GatewaySchedulingFenceHybridIdentity(fence); hybridScoped {
			identity, ok := contracts.ConnectorExecutionIdentityFromContext(ctx)
			if !ok || !contracts.ValidHybridRoutingExecutionIdentity(identity) || instanceID != instance.ID {
				return mutationNotDispatched(fmt.Errorf("connector gateway: hybrid scheduling fence requires a matching execution identity"))
			}
			requested.ExecutionScope = identity.Scope
			requested.ExecutionID = identity.ID
			requested.ExecutionGeneration = identity.Generation
		} else {
			return mutationNotDispatched(fmt.Errorf("connector gateway: automatic scheduling fence has an unsupported scope"))
		}
	}
	if requested.AvailableAt.IsZero() {
		requested.AvailableAt = now()
	}
	if requested.ExpiresAt.IsZero() {
		requested.ExpiresAt = now().Add(connectorTaskExecutionWindow)
	}
	// A recovery caller may lose the response after the Connector already
	// completed the logical probe. Reuse that terminal typed result before
	// creating work so the same circuit attempt cannot issue another request.
	if taskType == contracts.ConnectorTaskGatewayQualityProbe && identity != "" || taskType == contracts.ConnectorTaskGatewayBindingInstall {
		if existing, ok := c.findExistingTask(ctx, requested); ok && existing.Status == contracts.ConnectorTaskSucceeded {
			return c.waitForResult(ctx, instance, existing, taskType, result)
		}
	}
	task, err := c.store.CreateConnectorTask(ctx, requested)
	if err != nil {
		if existing, ok := c.findExistingTask(ctx, requested); ok {
			task = existing
		} else {
			return fmt.Errorf("connector gateway: create %s task: %w", taskType, err)
		}
	}
	if strings.TrimSpace(task.ID) == "" {
		return fmt.Errorf("connector gateway: create %s task returned an empty id", taskType)
	}
	// Deferred lifecycle tasks are acknowledged once durably queued. Waiting
	// here would tie a request context to the 30-minute safety delay and turn a
	// successful enqueue into a false timeout.
	if task.AvailableAt.After(c.nowTime()) {
		return nil
	}
	return c.waitForResult(ctx, instance, task, taskType, result)
}

func withSchedulingFence(ctx context.Context, taskType contracts.ConnectorTaskType, input any) any {
	switch taskType {
	case contracts.ConnectorTaskGatewaySchedulableSet:
		if value, ok := input.(contracts.ConnectorGatewaySchedulableSetInput); ok {
			fence, fenced := contracts.NextGatewaySchedulingFence(ctx)
			if !fenced {
				return input
			}
			value.Fence = &fence
			return value
		}
	case contracts.ConnectorTaskGatewaySwitch:
		if value, ok := input.(contracts.ConnectorGatewaySwitchInput); ok {
			fence, fenced := contracts.NextGatewaySchedulingFence(ctx)
			if !fenced {
				return input
			}
			value.Fence = &fence
			return value
		}
	case contracts.ConnectorTaskGatewayAccountCreate:
		if value, ok := input.(contracts.ConnectorGatewayAccountCreateInput); ok {
			fence, fenced := contracts.NextGatewaySchedulingFence(ctx)
			if fenced {
				value.Fence = &fence
			}
			return value
		}
	case contracts.ConnectorTaskGatewayAccountUpdate:
		if value, ok := input.(contracts.ConnectorGatewayAccountUpdateInput); ok {
			fence, fenced := contracts.NextGatewaySchedulingFence(ctx)
			if fenced {
				value.Fence = &fence
			}
			return value
		}
	}
	return input
}

func (c *ConnectorGatewayClient) validateInstance(instance contracts.Instance) error {
	if c.store == nil {
		return fmt.Errorf("connector gateway: task store is not configured")
	}
	if strings.TrimSpace(instance.ID) == "" {
		return fmt.Errorf("connector gateway: instance id is required")
	}
	if strings.TrimSpace(instance.ConnectorID) == "" {
		return fmt.Errorf("connector gateway: instance %s has no connector_id", instance.ID)
	}
	if instance.Kind != "" && instance.Kind != c.kind {
		return fmt.Errorf("connector gateway: instance %s kind %q does not match adapter kind %q", instance.ID, instance.Kind, c.kind)
	}
	return nil
}

func (c *ConnectorGatewayClient) findExistingTask(ctx context.Context, requested contracts.ConnectorTask) (contracts.ConnectorTask, bool) {
	lister, ok := c.store.(connectorTaskLister)
	if !ok {
		return contracts.ConnectorTask{}, false
	}
	tasks, err := lister.ListConnectorTasks(ctx, contracts.ConnectorTaskFilter{
		UserID:      requested.UserID,
		InstanceID:  requested.InstanceID,
		ConnectorID: requested.ConnectorID,
		Limit:       100,
	})
	if err != nil {
		return contracts.ConnectorTask{}, false
	}
	for _, task := range tasks {
		if task.IdempotencyKey != requested.IdempotencyKey || task.Type != requested.Type {
			continue
		}
		if (requested.Type == contracts.ConnectorTaskGatewayQualityProbe || requested.Type == contracts.ConnectorTaskGatewayBindingInstall) && task.Status == contracts.ConnectorTaskSucceeded {
			return task, true
		}
		// Protocol v3 executing tasks own a durable remote-mutation permit and
		// intentionally outlive the queue expiry/lease deadlines that governed
		// their pre-execution states. Reuse that exact in-flight intent until it
		// is explicitly completed or manually resolved.
		if task.Status == contracts.ConnectorTaskExecuting {
			return task, true
		}
		if (task.Status == contracts.ConnectorTaskPending || task.Status == contracts.ConnectorTaskLeased) &&
			(task.ExpiresAt.IsZero() || task.ExpiresAt.After(c.nowTime())) {
			return task, true
		}
	}
	return contracts.ConnectorTask{}, false
}

func (c *ConnectorGatewayClient) nowTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *ConnectorGatewayClient) waitForResult(
	ctx context.Context,
	instance contracts.Instance,
	task contracts.ConnectorTask,
	taskType contracts.ConnectorTaskType,
	result any,
) error {
	timeout := c.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	poll := c.poll
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		current, err := c.store.GetConnectorTask(waitCtx, task.ID)
		if err != nil {
			return fmt.Errorf("connector gateway: get task %s: %w", task.ID, err)
		}
		if current.Type != taskType || current.InstanceID != instance.ID || current.ConnectorID != instance.ConnectorID {
			return fmt.Errorf("connector gateway: task %s identity does not match request", task.ID)
		}
		switch current.Status {
		case contracts.ConnectorTaskSucceeded:
			if err := decodeStrictTaskResult(current.Result, result); err != nil {
				return fmt.Errorf("connector gateway: decode %s result: %w", taskType, err)
			}
			return nil
		case contracts.ConnectorTaskFailed, contracts.ConnectorTaskExpired:
			return connectorTaskFailure(current)
		case contracts.ConnectorTaskPending, contracts.ConnectorTaskLeased, contracts.ConnectorTaskExecuting:
		default:
			return fmt.Errorf("connector gateway: task %s has invalid status %q", current.ID, current.Status)
		}

		select {
		case <-waitCtx.Done():
			return fmt.Errorf("connector gateway: task %s wait for connector %s: %w", task.ID, instance.ConnectorID, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func connectorTaskIdempotencyKey(
	kind contracts.InstanceKind,
	instance contracts.Instance,
	taskType contracts.ConnectorTaskType,
	input json.RawMessage,
	identity string,
) string {
	hash := sha256.New()
	for _, part := range []string{
		"connector-gateway-task-v1",
		string(kind),
		strings.TrimSpace(instance.ID),
		strings.TrimSpace(instance.ConnectorID),
		string(taskType),
		string(input),
		strings.TrimSpace(identity),
	} {
		_, _ = io.WriteString(hash, part)
		_, _ = hash.Write([]byte{0})
	}
	return "gateway:v1:" + hex.EncodeToString(hash.Sum(nil))
}

func decodeStrictTaskResult(raw json.RawMessage, target any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || trimmed[0] != '{' {
		return fmt.Errorf("result must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("result must contain one JSON value")
		}
		return fmt.Errorf("decode trailing result data: %w", err)
	}
	return nil
}

func connectorTaskFailure(task contracts.ConnectorTask) error {
	code := strings.TrimSpace(task.Error.Code)
	if code != "" {
		return fmt.Errorf("connector gateway: task %s %s (%s)", task.ID, task.Status, code)
	}
	return fmt.Errorf("connector gateway: task %s %s", task.ID, task.Status)
}
