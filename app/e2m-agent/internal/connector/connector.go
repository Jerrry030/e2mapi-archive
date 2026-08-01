package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"e2m.local/agent/internal/adapters/cpa"
	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/agent/internal/adapters/newapi"
	"e2m.local/agent/internal/adapters/sub2api"
	"e2m.local/contracts"
)

const (
	taskSchemaVersion       = 1
	defaultIdlePollInterval = 5 * time.Second
	busyPollInterval        = 1 * time.Second
	minCorePollInterval     = 1 * time.Second
	maxCorePollInterval     = 30 * time.Second
	maxBackoffJitter        = 1 * time.Second
)

var failureBackoffIntervals = [...]time.Duration{
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
}

var connectorCapabilities = []contracts.ConnectorTaskType{
	contracts.ConnectorTaskGatewayHealth,
	contracts.ConnectorTaskGatewayAccountsList,
	contracts.ConnectorTaskGatewaySchedulableSet,
	contracts.ConnectorTaskGatewaySwitch,
	contracts.ConnectorTaskGatewaySchedulingBarrier,
}

func isOwnedPoolTask(taskType contracts.ConnectorTaskType) bool {
	for _, supported := range connectorCapabilities {
		if taskType == supported {
			return true
		}
	}
	return false
}

// Config controls the outbound connector loop. Gateway access is deliberately
// absent: it is loaded from ConfigStore at execution time and never supplied by
// Core.
type Config struct {
	CoreURL     string
	Token       string
	EnrollToken string
	TokenFile   string
	ConnectorID string
	InstanceID  string
	ConfigStore *LocalConfigStore
	// Retained only for legacy helper source compatibility. The minimal
	// production entrypoint never initializes binding delivery.
	BindingEncryption *BindingEncryptionIdentity
	Version           string
}

type Connector struct {
	cfg             Config
	coreHTTP        *http.Client
	gatewayHTTP     *http.Client
	gatewayForTask  func() (gateways.Gateway, error)
	writeReceipts   *writeReceiptLedger
	schedulingFence *schedulingFenceLedger
	// Retired helpers remain compiled during the transition, but these fields
	// stay nil and their capabilities are neither advertised nor dispatched.
	bindingInstallLedger               *bindingInstallLedger
	upstreamIntelligenceOutbox         *UpstreamIntelligenceOutbox
	upstreamIntelligenceSourceBindings *UpstreamIntelligenceSourceBindingStore
	tokenMu                            sync.RWMutex
	wait                               func(context.Context, time.Duration) bool
	jitter                             func(time.Duration) time.Duration
	coreClockOffset                    time.Duration
	nextPassiveObservationAt           time.Time
}

func New(cfg Config) *Connector {
	cfg.CoreURL = strings.TrimRight(strings.TrimSpace(cfg.CoreURL), "/")
	cfg.ConnectorID = strings.TrimSpace(cfg.ConnectorID)
	cfg.InstanceID = strings.TrimSpace(cfg.InstanceID)
	connector := &Connector{
		cfg:             cfg,
		coreHTTP:        &http.Client{Timeout: 15 * time.Second},
		gatewayHTTP:     &http.Client{},
		writeReceipts:   newWriteReceiptLedger(cfg.ConfigStore, defaultWriteReceiptLimit),
		schedulingFence: newSchedulingFenceLedger(cfg.ConfigStore),
		wait:            waitForPoll,
		jitter:          randomBackoffJitter,
	}
	return connector
}

func dataDirForConfigStore(store *LocalConfigStore) string {
	if store == nil {
		return ""
	}
	return store.DataDir()
}

func (c *Connector) Validate() error {
	if c.cfg.CoreURL == "" {
		return errors.New("connector: core url is required")
	}
	if strings.TrimSpace(c.cfg.Token) == "" && strings.TrimSpace(c.cfg.EnrollToken) == "" && strings.TrimSpace(c.cfg.TokenFile) == "" {
		return errors.New("connector: connector token or enrollment token is required")
	}
	if c.cfg.ConnectorID == "" {
		return errors.New("connector: connector id is required")
	}
	if c.cfg.InstanceID == "" {
		return errors.New("connector: instance id is required")
	}
	if !contracts.IsConnectorVersion(c.cfg.Version) {
		return errors.New("connector: version must be a strict semantic version")
	}
	return nil
}

func (c *Connector) Run(ctx context.Context) {
	lastErrorCode := ""
	consecutiveFailures := 0
	for {
		result, err := c.pollOnce(ctx)
		var delay time.Duration
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			consecutiveFailures++
			delay = c.failureBackoff(consecutiveFailures)
			c.recordCoreRetry(consecutiveFailures, delay)
			errorCode := classifyCorePollError(err)
			if errorCode != lastErrorCode {
				slog.Error("connector poll failed", "connector_id", c.cfg.ConnectorID, "error_code", errorCode)
				lastErrorCode = errorCode
			}
		} else {
			consecutiveFailures = 0
			lastErrorCode = ""
			delay = c.nextPollDelay(result)
			c.recordCoreSync()
		}
		if !c.wait(ctx, delay) {
			return
		}
	}
}

func classifyCorePollError(err error) string {
	if err == nil {
		return ""
	}
	var netErr interface{ Timeout() bool }
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "core_timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case strings.Contains(strings.ToLower(err.Error()), "http"):
		return "core_http_error"
	default:
		return "core_unreachable"
	}
}

func (c *Connector) PollOnce(ctx context.Context) error {
	_, err := c.pollOnce(ctx)
	return err
}

type pollResult struct {
	taskCount     int
	nextPollAfter string
}

func (c *Connector) pollOnce(ctx context.Context) (pollResult, error) {
	if err := c.ensureToken(ctx); err != nil {
		return pollResult{}, err
	}
	tasks, nextPollAfter, err := c.lease(ctx)
	if err != nil {
		return pollResult{}, err
	}
	result := pollResult{taskCount: len(tasks), nextPollAfter: nextPollAfter}
	for _, task := range tasks {
		taskResult := c.executeLeased(ctx, task)
		if taskResult.localErr != nil {
			return result, fmt.Errorf("connector: execute task %s: %w", task.ID, taskResult.localErr)
		}
		if err := c.complete(ctx, task, taskResult); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (c *Connector) nextPollDelay(result pollResult) time.Duration {
	if result.taskCount > 0 {
		return busyPollInterval
	}
	delay, err := time.ParseDuration(strings.TrimSpace(result.nextPollAfter))
	if err != nil || delay <= 0 {
		return defaultIdlePollInterval
	}
	if delay < minCorePollInterval {
		return minCorePollInterval
	}
	if delay > maxCorePollInterval {
		return maxCorePollInterval
	}
	return delay
}

func (c *Connector) failureBackoff(consecutiveFailures int) time.Duration {
	index := consecutiveFailures - 1
	if index < 0 {
		index = 0
	}
	if index >= len(failureBackoffIntervals) {
		index = len(failureBackoffIntervals) - 1
	}
	delay := failureBackoffIntervals[index]
	jitter := c.jitter(maxBackoffJitter)
	if jitter < 0 {
		jitter = 0
	}
	if jitter > maxBackoffJitter {
		jitter = maxBackoffJitter
	}
	return delay + jitter
}

func waitForPoll(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func randomBackoffJitter(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(limit) + 1))
}

func (c *Connector) ensureToken(ctx context.Context) error {
	if path := strings.TrimSpace(c.cfg.TokenFile); path != "" {
		raw, err := readRegularFileNoSymlink(path)
		if err == nil && strings.TrimSpace(string(raw)) != "" {
			c.setConnectorToken(strings.TrimSpace(string(raw)))
			return nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("connector: read connector token: %w", err)
		}
	}
	if c.connectorToken() != "" {
		return nil
	}
	if strings.TrimSpace(c.cfg.EnrollToken) == "" {
		return errors.New("connector: connector token is required")
	}
	reqBody := contracts.ConnectorEnrollRequest{
		EnrollmentToken: strings.TrimSpace(c.cfg.EnrollToken),
		ConnectorID:     c.cfg.ConnectorID,
		InstanceID:      c.cfg.InstanceID,
		Version:         strings.TrimSpace(c.cfg.Version),
		ProtocolVersion: contracts.ConnectorProtocolVersion,
	}
	var out contracts.ConnectorEnrollResponse
	if err := c.postJSON(ctx, "/api/v1/connectors/enroll", "", reqBody, &out); err != nil {
		return fmt.Errorf("connector: enroll: %w", err)
	}
	if out.ProtocolVersion != contracts.ConnectorProtocolVersion {
		return fmt.Errorf("connector: core protocol version %d is not supported", out.ProtocolVersion)
	}
	if !out.ServerTime.IsZero() {
		c.coreClockOffset = time.Until(out.ServerTime)
	}
	if strings.TrimSpace(out.ConnectorToken) == "" {
		return errors.New("connector: enrollment response did not include connector token")
	}
	c.setConnectorToken(strings.TrimSpace(out.ConnectorToken))
	if path := strings.TrimSpace(c.cfg.TokenFile); path != "" {
		if err := atomicWritePrivateFile(path, []byte(c.connectorToken()+"\n")); err != nil {
			return fmt.Errorf("connector: persist connector token: %w", err)
		}
	}
	return nil
}

type taskResult struct {
	success  bool
	result   json.RawMessage
	taskErr  contracts.ConnectorTaskError
	localErr error
}

func (c *Connector) lease(ctx context.Context) ([]contracts.ConnectorTask, string, error) {
	reqBody := contracts.ConnectorTaskLeaseRequest{
		ConnectorID:     c.cfg.ConnectorID,
		MaxTasks:        1,
		LeaseSeconds:    30,
		Version:         strings.TrimSpace(c.cfg.Version),
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		RuntimeState:    c.runtimeState(),
	}
	var out contracts.ConnectorTaskLeaseResponse
	if err := c.postJSON(ctx, "/api/v1/connectors/tasks/lease", c.connectorToken(), reqBody, &out); err != nil {
		return nil, "", fmt.Errorf("connector: lease: %w", err)
	}
	if out.ProtocolVersion != contracts.ConnectorProtocolVersion {
		return nil, "", fmt.Errorf("connector: core protocol version %d is not supported", out.ProtocolVersion)
	}
	if !out.ServerTime.IsZero() {
		c.coreClockOffset = time.Until(out.ServerTime)
	}
	for _, task := range out.Tasks {
		if strings.TrimSpace(task.LeaseNonce) == "" {
			return nil, "", fmt.Errorf("connector: leased task %s did not include a lease nonce", task.ID)
		}
	}
	return out.Tasks, out.NextPollAfter, nil
}

func (c *Connector) complete(ctx context.Context, task contracts.ConnectorTask, result taskResult) error {
	reqBody := contracts.ConnectorTaskCompleteRequest{
		ConnectorID: c.cfg.ConnectorID,
		LeaseNonce:  task.LeaseNonce,
		Success:     result.success,
		Result:      result.result,
		Error:       result.taskErr,
	}
	path := "/api/v1/connectors/tasks/" + task.ID + "/complete"
	if err := c.postJSON(ctx, path, c.connectorToken(), reqBody, nil); err != nil {
		return fmt.Errorf("connector: complete: %w", err)
	}
	return nil
}

func (c *Connector) beginExecution(ctx context.Context, task contracts.ConnectorTask) error {
	reqBody := contracts.ConnectorTaskExecutionRequest{
		ConnectorID: c.cfg.ConnectorID,
		LeaseNonce:  task.LeaseNonce,
	}
	var permitted contracts.ConnectorTask
	path := "/api/v1/connectors/tasks/" + task.ID + "/execute"
	if err := c.postJSON(ctx, path, c.connectorToken(), reqBody, &permitted); err != nil {
		return fmt.Errorf("connector: begin execution: %w", err)
	}
	if permitted.ID != task.ID || permitted.UserID != task.UserID || permitted.ConnectorID != task.ConnectorID || permitted.InstanceID != task.InstanceID ||
		permitted.PlanID != task.PlanID || permitted.SchedulingGeneration != task.SchedulingGeneration ||
		permitted.ExecutionScope != task.ExecutionScope || permitted.ExecutionID != task.ExecutionID ||
		permitted.ExecutionGeneration != task.ExecutionGeneration ||
		permitted.Type != task.Type || permitted.SchemaVersion != task.SchemaVersion || !bytes.Equal(permitted.Input, task.Input) ||
		permitted.IdempotencyKey != task.IdempotencyKey || permitted.LeaseNonce != task.LeaseNonce ||
		permitted.Status != contracts.ConnectorTaskExecuting {
		return errors.New("connector: begin execution response did not match the leased task")
	}
	return nil
}

func (c *Connector) connectorToken() string {
	if c == nil {
		return ""
	}
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return strings.TrimSpace(c.cfg.Token)
}

func (c *Connector) setConnectorToken(token string) {
	if c == nil {
		return
	}
	c.tokenMu.Lock()
	c.cfg.Token = strings.TrimSpace(token)
	c.tokenMu.Unlock()
}

func (c *Connector) postJSON(ctx context.Context, path, token string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.CoreURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.coreHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var responseError struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(raw, &responseError)
		return &coreHTTPError{StatusCode: resp.StatusCode, Code: strings.TrimSpace(responseError.Code)}
	}
	if output == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, output); err != nil {
		return fmt.Errorf("decode core response: %w", err)
	}
	return nil
}

// coreHTTPError keeps the status and stable machine code available to callers
// that need protocol-specific handling while retaining the existing concise
// error text for logs and task polling.
type coreHTTPError struct {
	StatusCode int
	Code       string
}

func (e *coreHTTPError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("core rejected request with HTTP %d (%s)", e.StatusCode, e.Code)
	}
	return fmt.Sprintf("core rejected request with HTTP %d", e.StatusCode)
}

// execute is the local execution primitive used by focused adapter tests. The
// polling loop always calls executeLeased, which additionally acquires Core's
// durable route-plan execution permit.
func (c *Connector) execute(ctx context.Context, task contracts.ConnectorTask) taskResult {
	return c.executeTask(ctx, task, false)
}

func (c *Connector) executeLeased(ctx context.Context, task contracts.ConnectorTask) taskResult {
	return c.executeTask(ctx, task, true)
}

func (c *Connector) executeTask(ctx context.Context, task contracts.ConnectorTask, enforceExecutionPermit bool) (result taskResult) {
	fence, fencedMutation := schedulingFenceForTask(task)
	requiresPermit := enforceExecutionPermit && fencedMutation
	permitAcquired := false
	defer func() {
		// A fenced task cannot use the leased completion protocol. Any validation
		// or local-ledger rejection before BeginExecution therefore remains local
		// and performs neither a gateway call nor a completion request.
		if requiresPermit && !permitAcquired && result.localErr == nil {
			code := strings.TrimSpace(result.taskErr.Code)
			if code == "" {
				code = "execution_preflight_failed"
			}
			result = taskResult{localErr: fmt.Errorf("execution permit was not acquired (%s)", code)}
		}
	}()

	if result := c.validateTask(task); !result.success {
		return result
	}
	if enforceExecutionPermit && !fencedMutation && connectorTaskHasExecutionMetadata(task) {
		return taskResult{localErr: errors.New("execution metadata was supplied without a scheduling fence")}
	}
	if requiresPermit {
		if planID, planScoped := contracts.GatewaySchedulingFencePlanID(fence); planScoped {
			if task.PlanID == "" || task.SchedulingGeneration <= 0 || planID != task.PlanID ||
				fence.Version != task.SchedulingGeneration || connectorTaskHasGenericExecutionIdentity(task) {
				return taskResult{localErr: errors.New("route-plan execution identity does not match the scheduling fence")}
			}
		} else if instanceID, _, hybridScoped := contracts.GatewaySchedulingFenceHybridIdentity(fence); hybridScoped {
			identity := contracts.ConnectorExecutionIdentity{
				Scope: task.ExecutionScope, ID: task.ExecutionID, Generation: task.ExecutionGeneration,
			}
			if !contracts.ValidHybridRoutingExecutionIdentity(identity) || instanceID != task.InstanceID ||
				task.PlanID != "" || task.SchedulingGeneration != 0 {
				return taskResult{localErr: errors.New("hybrid execution identity does not match the scheduling fence")}
			}
		} else {
			return taskResult{localErr: errors.New("execution identity has an unsupported scheduling fence")}
		}
	}
	if fence, ok := schedulingFenceForTask(task); ok {
		accepted, lease, err := c.schedulingFence.begin(fence, taskSignatureBytes(task))
		if err != nil {
			if errors.Is(err, errSchedulingFenceConflict) {
				return failedTask("scheduling_fence_conflict", "scheduling generation was reused for another intent", false)
			}
			return taskResult{localErr: fmt.Errorf("scheduling fence could not be checked: %w", err)}
		}
		if !accepted {
			return failedTask("scheduling_fence_stale", "task was superseded by a newer scheduling generation", false)
		}
		defer func() { _ = lease.release() }()
	}
	// Fence before consulting the durable receipt ledger. A write that once
	// succeeded can be replayed after a newer scheduling tuple takes ownership;
	// reporting that stale success would let Core record obsolete binding facts.
	var receiptLease *writeReceiptLease
	var cachedReceipt taskResult
	cachedReceiptFound := false
	if isWriteTask(task.Type) {
		cached, ok, lease, err := c.writeReceipts.begin(task.IdempotencyKey, writeTaskPayloadSignature(task))
		if err != nil {
			if errors.Is(err, errWriteReceiptConflict) {
				return failedTask("idempotency_conflict", "idempotency key was already used for another task", false)
			}
			return taskResult{localErr: fmt.Errorf("write receipt could not be checked: %w", err)}
		}
		receiptLease = lease
		if receiptLease == nil {
			return taskResult{localErr: errors.New("write receipt lock was not retained")}
		}
		defer func() { _ = receiptLease.release() }()
		cachedReceipt = cached
		cachedReceiptFound = ok
	}
	if requiresPermit {
		if err := c.beginExecution(ctx, task); err != nil {
			return taskResult{localErr: err}
		}
		permitAcquired = true
	}
	if cachedReceiptFound {
		return cachedReceipt
	}
	if task.Type == contracts.ConnectorTaskGatewaySchedulingBarrier {
		result := gatewayResult(contracts.ConnectorGatewayMutationResult{}, nil)
		if err := receiptLease.commit(result); err != nil {
			return taskResult{localErr: fmt.Errorf("write receipt could not be persisted: %w", err)}
		}
		return result
	}
	gateway, gatewayGeneration, err := c.gatewayWithGeneration()
	if err != nil {
		return taskResult{taskErr: gateways.TaskError(err)}
	}
	startedAt := time.Now()
	result = c.executeGatewayTask(ctx, gateway, task)
	c.recordGatewayRequest(gatewayGeneration, result, time.Since(startedAt))
	if requiresPermit && !result.success && gatewayMutationOutcomeUncertain(task.Type, result.taskErr.Code) {
		return taskResult{localErr: fmt.Errorf("gateway mutation outcome is uncertain (%s)", result.taskErr.Code)}
	}
	if result.success && receiptLease != nil {
		if err := receiptLease.commit(result); err != nil {
			return taskResult{localErr: fmt.Errorf("write receipt could not be persisted: %w", err)}
		}
	}
	return result
}

func connectorTaskHasGenericExecutionIdentity(task contracts.ConnectorTask) bool {
	return task.ExecutionScope != "" || task.ExecutionID != "" || task.ExecutionGeneration != 0
}

func connectorTaskHasExecutionMetadata(task contracts.ConnectorTask) bool {
	return task.PlanID != "" || task.SchedulingGeneration != 0 || connectorTaskHasGenericExecutionIdentity(task)
}

// gatewayMutationOutcomeUncertain is deliberately conservative. Once a
// request may have crossed the local gateway boundary, transport loss,
// malformed responses, server errors, or a partially applied multi-step switch
// cannot release the plan's executing permit automatically.
func gatewayMutationOutcomeUncertain(taskType contracts.ConnectorTaskType, code string) bool {
	if taskType == contracts.ConnectorTaskGatewaySwitch {
		return true
	}
	switch strings.TrimSpace(code) {
	case "task_type_unsupported", "invalid_gateway_request", "gateway_auth_failed",
		"gateway_resource_not_found", "gateway_rejected", "gateway_redirect_rejected",
		"invalid_account_id", "binding_not_found", "binding_version_stale", "ownership_violation":
		return false
	default:
		return true
	}
}

func schedulingFenceForTask(task contracts.ConnectorTask) (contracts.GatewaySchedulingFence, bool) {
	return contracts.ConnectorTaskSchedulingFence(task)
}

func (c *Connector) diagnostics() *LocalDiagnostics {
	if c.cfg.ConfigStore == nil {
		return nil
	}
	return c.cfg.ConfigStore.Diagnostics()
}

func (c *Connector) recordCoreSync() {
	if diagnostics := c.diagnostics(); diagnostics != nil {
		diagnostics.Record(LocalDiagnosticCoreSync, LocalDiagnosticOK, time.Now(), 0, http.StatusOK, "")
	}
}

func (c *Connector) recordCoreRetry(failureCount int, delay time.Duration) {
	if diagnostics := c.diagnostics(); diagnostics != nil {
		now := time.Now()
		diagnostics.RecordRetry(LocalDiagnosticCoreSync, now, failureCount, now.Add(delay), "core_sync_failed")
	}
}

func (c *Connector) recordGatewayRequest(generation uint64, result taskResult, latency time.Duration) {
	if c.cfg.ConfigStore == nil {
		return
	}
	if result.success {
		c.cfg.ConfigStore.recordGatewayRequest(generation, LocalDiagnosticOK, time.Now(), latency, "")
		return
	}
	c.cfg.ConfigStore.recordGatewayRequest(generation, LocalDiagnosticError, time.Now(), latency, result.taskErr.Code)
}

func (c *Connector) validateTask(task contracts.ConnectorTask) taskResult {
	if task.SchemaVersion != taskSchemaVersion {
		return failedTask("schema_version_unsupported", "task schema version is not supported", false)
	}
	if strings.TrimSpace(task.ConnectorID) != c.cfg.ConnectorID {
		return failedTask("connector_mismatch", "task belongs to another connector", false)
	}
	if strings.TrimSpace(task.InstanceID) != c.cfg.InstanceID {
		return failedTask("instance_mismatch", "task belongs to another instance", false)
	}
	if !isOwnedPoolTask(task.Type) {
		return failedTask("task_type_unsupported", "task is outside the owner-pool Connector boundary", false)
	}
	if isWriteTask(task.Type) && (task.IdempotencyKey != strings.TrimSpace(task.IdempotencyKey) ||
		!validWriteReceiptKey(task.IdempotencyKey)) {
		return failedTask("invalid_task_input", "write task idempotency_key is required", false)
	}
	switch task.Type {
	case contracts.ConnectorTaskGatewayHealth:
		var input contracts.ConnectorGatewayHealthInput
		return validateInput(task.Input, &input, true)
	case contracts.ConnectorTaskGatewayAccountsList:
		var input contracts.ConnectorGatewayAccountsListInput
		return validateInput(task.Input, &input, true)
	case contracts.ConnectorTaskGatewaySchedulableSet:
		var input contracts.ConnectorGatewaySchedulableSetInput
		if result := validateInput(task.Input, &input, false); !result.success {
			return result
		}
		if strings.TrimSpace(input.AccountID) == "" {
			return failedTask("invalid_task_input", "account_id is required", false)
		}
		if input.Fence != nil && (strings.TrimSpace(input.Fence.Scope) == "" || input.Fence.Version <= 0 || input.Fence.Sequence <= 0) {
			return failedTask("invalid_task_input", "scheduling fence is invalid", false)
		}
	case contracts.ConnectorTaskGatewaySwitch:
		var input contracts.ConnectorGatewaySwitchInput
		if result := validateInput(task.Input, &input, false); !result.success {
			return result
		}
		disableID := strings.TrimSpace(input.DisableAccountID)
		enableID := strings.TrimSpace(input.EnableAccountID)
		if disableID == "" {
			return failedTask("invalid_task_input", "disable_account_id is required", false)
		}
		if enableID != "" && enableID == disableID {
			return failedTask("invalid_task_input", "switch account ids must be different", false)
		}
		if input.Fence != nil && (strings.TrimSpace(input.Fence.Scope) == "" || input.Fence.Version <= 0 || input.Fence.Sequence <= 0) {
			return failedTask("invalid_task_input", "scheduling fence is invalid", false)
		}
	case contracts.ConnectorTaskGatewaySchedulingBarrier:
		var input contracts.ConnectorGatewaySchedulingBarrierInput
		if result := validateInput(task.Input, &input, false); !result.success {
			return result
		}
		if strings.TrimSpace(input.Fence.Scope) == "" || input.Fence.Version <= 0 || input.Fence.Sequence <= 0 {
			return failedTask("invalid_task_input", "scheduling fence is invalid", false)
		}
	default:
		return failedTask("task_type_unsupported", "task type is not supported", false)
	}
	return taskResult{success: true}
}

func validateInput(raw json.RawMessage, target any, allowEmpty bool) taskResult {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		if !allowEmpty {
			return failedTask("invalid_task_input", "task input is required", false)
		}
		raw = []byte("{}")
	}
	if bytes.Equal(raw, []byte("null")) {
		return failedTask("invalid_task_input", "task input must be an object", false)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return failedTask("invalid_task_input", "task input does not match its schema", false)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return failedTask("invalid_task_input", "task input must contain one JSON object", false)
	}
	return taskResult{success: true}
}

func (c *Connector) executeGatewayTask(ctx context.Context, gateway gateways.Gateway, task contracts.ConnectorTask) taskResult {
	switch task.Type {
	case contracts.ConnectorTaskGatewayHealth:
		result, err := gateway.Health(ctx)
		return gatewayResult(result, err)
	case contracts.ConnectorTaskGatewayAccountsList:
		accounts, err := gateway.ListAccounts(ctx)
		if accounts == nil {
			accounts = []contracts.GatewayAccount{}
		}
		return gatewayResult(contracts.ConnectorGatewayAccountsListResult{Accounts: accounts}, err)
	case contracts.ConnectorTaskGatewaySchedulableSet:
		var input contracts.ConnectorGatewaySchedulableSetInput
		_ = json.Unmarshal(task.Input, &input)
		err := gateway.SetSchedulable(ctx, strings.TrimSpace(input.AccountID), input.Schedulable)
		return gatewayResult(contracts.ConnectorGatewayMutationResult{}, err)
	case contracts.ConnectorTaskGatewaySwitch:
		var input contracts.ConnectorGatewaySwitchInput
		_ = json.Unmarshal(task.Input, &input)
		if err := gateway.SetSchedulable(ctx, strings.TrimSpace(input.DisableAccountID), false); err != nil {
			return gatewayResult(contracts.ConnectorGatewayMutationResult{}, err)
		}
		if accountID := strings.TrimSpace(input.EnableAccountID); accountID != "" {
			if err := gateway.SetSchedulable(ctx, accountID, true); err != nil {
				return gatewayResult(contracts.ConnectorGatewayMutationResult{}, err)
			}
		}
		return gatewayResult(contracts.ConnectorGatewayMutationResult{}, nil)
	default:
		return failedTask("task_type_unsupported", "task type is not supported", false)
	}
}

func gatewayResult(value any, err error) taskResult {
	if err != nil {
		return taskResult{taskErr: gateways.TaskError(err)}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return failedTask("result_encoding_failed", "task result could not be encoded", false)
	}
	return taskResult{success: true, result: raw}
}

func failedTask(code, _ string, retryable bool) taskResult {
	if !contracts.IsConnectorReportedErrorCode(code) {
		code = "connector_task_failed"
	}
	return taskResult{taskErr: contracts.ConnectorTaskError{Code: code, Retryable: retryable}}
}

func (c *Connector) gateway() (gateways.Gateway, error) {
	if c.gatewayForTask != nil {
		return c.gatewayForTask()
	}
	if c.cfg.ConfigStore == nil {
		return nil, &gateways.Error{Code: "gateway_not_configured", Message: "gateway is not configured"}
	}
	cfg, _, err := c.cfg.ConfigStore.loadWithGatewayGeneration()
	return c.gatewayFromConfig(cfg, err)
}

func (c *Connector) gatewayWithGeneration() (gateways.Gateway, uint64, error) {
	if c.gatewayForTask != nil {
		gateway, err := c.gatewayForTask()
		generation := uint64(0)
		if c.cfg.ConfigStore != nil {
			generation = c.cfg.ConfigStore.gatewayGenerationSnapshot()
		}
		return gateway, generation, err
	}
	if c.cfg.ConfigStore == nil {
		return nil, 0, &gateways.Error{Code: "gateway_not_configured", Message: "gateway is not configured"}
	}
	cfg, generation, err := c.cfg.ConfigStore.loadWithGatewayGeneration()
	gateway, err := c.gatewayFromConfig(cfg, err)
	return gateway, generation, err
}

func (c *Connector) gatewayFromConfig(cfg GatewayLocalConfig, loadErr error) (gateways.Gateway, error) {
	err := loadErr
	if errors.Is(err, os.ErrNotExist) {
		return nil, &gateways.Error{Code: "gateway_not_configured", Message: "gateway is not configured"}
	}
	if err != nil {
		return nil, &gateways.Error{Code: "gateway_config_unavailable", Message: "gateway configuration could not be loaded", Retryable: true}
	}
	cfg.Normalize()
	if err := ValidateGatewayLocalConfig(cfg); err != nil {
		return nil, &gateways.Error{Code: "gateway_not_configured", Message: "gateway is not configured"}
	}
	adapterCfg := gateways.Config{
		BaseURL:      cfg.GatewayURL,
		XAPIKey:      cfg.Credentials.XAPIKey,
		NewAPIUserID: cfg.Credentials.NewAPIUserID,
		NewAPIToken:  cfg.Credentials.NewAPIToken,
		BearerToken:  cfg.Credentials.BearerToken,
		HTTPClient:   c.gatewayClient(cfg.Runtime.GatewayRequestTimeoutSeconds),
	}
	switch cfg.GatewayKind {
	case "sub2api":
		if cfg.Auth != GatewayAuthXAPIKey {
			return nil, invalidGatewayAuth()
		}
		return sub2api.NewGateway(adapterCfg), nil
	case "newapi", "new-api":
		if cfg.Auth != GatewayAuthNewAPI {
			return nil, invalidGatewayAuth()
		}
		return newapi.NewGateway(adapterCfg), nil
	case "cpa":
		if cfg.Auth != GatewayAuthBearer {
			return nil, invalidGatewayAuth()
		}
		return cpa.NewGateway(adapterCfg), nil
	default:
		return nil, &gateways.Error{Code: "gateway_kind_unsupported", Message: "gateway kind is not supported"}
	}
}

func (c *Connector) gatewayClient(timeoutSeconds int) *http.Client {
	base := c.gatewayHTTP
	if base == nil {
		base = http.DefaultClient
	}
	client := *base
	client.Timeout = time.Duration(timeoutSeconds) * time.Second
	return &client
}

func invalidGatewayAuth() error {
	return &gateways.Error{Code: "gateway_auth_unsupported", Message: "gateway authentication does not match gateway kind"}
}

func (c *Connector) runtimeState() contracts.ConnectorRuntimeState {
	state := contracts.ConnectorRuntimeState{
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		Capabilities:    append([]contracts.ConnectorTaskType(nil), connectorCapabilities...),
	}
	if c.cfg.ConfigStore == nil {
		state.GatewayStatus = "missing"
		state.ErrorCode = "gateway_not_configured"
		return state
	}
	cfg, err := c.cfg.ConfigStore.Load()
	if err != nil {
		state.GatewayStatus = "missing"
		state.ErrorCode = "gateway_not_configured"
		return state
	}
	cfg.Normalize()
	state.GatewayKind = cfg.GatewayKind
	if err := ValidateGatewayLocalConfig(cfg); err != nil {
		state.GatewayStatus = "missing"
		state.ErrorCode = "gateway_not_configured"
		return state
	}
	state.GatewayConfigured = true
	state.GatewayStatus = "configured"
	if diagnostics := c.diagnostics(); diagnostics != nil {
		snapshot := diagnostics.Snapshot()
		// GatewayTest is a manual test of an unsaved candidate configuration.
		// Only requests made by the saved runtime may influence state reported to
		// Core; otherwise a candidate can falsely mark the live runtime healthy or
		// unhealthy.
		if snapshot.GatewayRequest.Status == LocalDiagnosticOK {
			state.GatewayStatus = "ok"
		} else if snapshot.GatewayRequest.Status == LocalDiagnosticError {
			state.GatewayStatus = "error"
			state.ErrorCode = snapshot.GatewayRequest.ErrorCode
			if !contracts.IsConnectorReportedErrorCode(state.ErrorCode) {
				state.ErrorCode = "gateway_request_failed"
			}
		}
	}
	return state
}

func isWriteTask(taskType contracts.ConnectorTaskType) bool {
	switch taskType {
	case contracts.ConnectorTaskGatewaySchedulableSet,
		contracts.ConnectorTaskGatewaySwitch,
		contracts.ConnectorTaskGatewaySchedulingBarrier:
		return true
	default:
		return false
	}
}
