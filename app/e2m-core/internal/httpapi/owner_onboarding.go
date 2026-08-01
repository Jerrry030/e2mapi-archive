package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/auth"
	"e2m.local/core/internal/store"
)

// ownerOnboardingResponse is deliberately instance-scoped and owner-safe.
// Pool, plan, channel, remote-account, credential, desired-state fingerprint,
// lease, and internal error identifiers never cross this boundary.
type ownerOnboardingResponse struct {
	GeneratedAt time.Time                       `json:"generated_at"`
	Summary     ownerOnboardingSummary          `json:"summary"`
	Instances   []ownerOnboardingInstanceStatus `json:"instances"`
}

type ownerOnboardingSummary struct {
	TotalInstances               int `json:"total_instances"`
	ConnectorReady               int `json:"connector_ready"`
	ActiveInstances              int `json:"active_instances"`
	ActionRequired               int `json:"action_required"`
	DeliveredKeys                int `json:"delivered_keys"`
	VerifiedKeys                 int `json:"verified_keys"`
	PublishedBindings            int `json:"published_bindings"`
	ActiveBindings               int `json:"active_bindings"`
	CallableBindings             int `json:"callable_bindings"`
	AwaitingVerificationBindings int `json:"awaiting_verification_bindings"`
	VerificationFailedBindings   int `json:"verification_failed_bindings"`
}

type ownerConnectorReadiness string

const (
	ownerConnectorMissing       ownerConnectorReadiness = "missing"
	ownerConnectorOffline       ownerConnectorReadiness = "offline"
	ownerConnectorSetupRequired ownerConnectorReadiness = "setup_required"
	ownerConnectorUpdateNeeded  ownerConnectorReadiness = "update_required"
	ownerConnectorReady         ownerConnectorReadiness = "ready"
)

type ownerOnboardingServiceState string

const (
	ownerServiceAwaitingConnector    ownerOnboardingServiceState = "awaiting_connector"
	ownerServiceConnectorOffline     ownerOnboardingServiceState = "connector_offline"
	ownerServiceGatewaySetup         ownerOnboardingServiceState = "gateway_setup_required"
	ownerServiceConnectorUpdate      ownerOnboardingServiceState = "connector_update_required"
	ownerServiceWaitingPlatform      ownerOnboardingServiceState = "waiting_platform"
	ownerServiceProvisioning         ownerOnboardingServiceState = "provisioning"
	ownerServiceAwaitingVerification ownerOnboardingServiceState = "awaiting_verification"
	ownerServiceVerificationFailed   ownerOnboardingServiceState = "verification_failed"
	ownerServiceRetrying             ownerOnboardingServiceState = "retrying"
	ownerServicePaused               ownerOnboardingServiceState = "paused"
	ownerServiceDegraded             ownerOnboardingServiceState = "degraded"
	ownerServiceActive               ownerOnboardingServiceState = "active"
)

type ownerOnboardingInstanceStatus struct {
	InstanceID                   string                      `json:"instance_id"`
	InstanceName                 string                      `json:"instance_name"`
	InstanceKind                 contracts.InstanceKind      `json:"instance_kind"`
	ConnectorState               ownerConnectorReadiness     `json:"connector_state"`
	ConnectorLastSeenAt          *time.Time                  `json:"connector_last_seen_at,omitempty"`
	ServiceState                 ownerOnboardingServiceState `json:"service_state"`
	Stage                        contracts.OnboardingStage   `json:"stage,omitempty"`
	Status                       contracts.OnboardingStatus  `json:"status,omitempty"`
	WorkflowCount                int                         `json:"workflow_count"`
	ReadyWorkflows               int                         `json:"ready_workflows"`
	DeliveredKeys                int                         `json:"delivered_keys"`
	VerifiedKeys                 int                         `json:"verified_keys"`
	PublishedBindings            int                         `json:"published_bindings"`
	ActiveBindings               int                         `json:"active_bindings"`
	CallableBindings             int                         `json:"callable_bindings"`
	AwaitingVerificationBindings int                         `json:"awaiting_verification_bindings"`
	VerificationFailedBindings   int                         `json:"verification_failed_bindings"`
	BlockerCode                  string                      `json:"blocker_code,omitempty"`
	NextAttemptAt                *time.Time                  `json:"next_attempt_at,omitempty"`
	UpdatedAt                    time.Time                   `json:"updated_at"`
}

func (s *Server) handleOwnerOnboarding(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	user := currentUser(r)
	// This is a self-service projection, not an administrator impersonation API.
	// A dual-role account may read its own client state; user_id is intentionally
	// ignored because no cross-user selector is part of this contract.
	if !auth.IsOwner(user) || user.ID == 0 {
		writeError(w, http.StatusForbidden, "forbidden", "client role required")
		return
	}

	ctx := r.Context()
	instances, err := s.store.ListInstances(ctx, user.ID)
	if err != nil {
		writeOwnerOnboardingStoreError(w)
		return
	}
	workflows, err := s.store.ListOnboardingWorkflows(ctx, contracts.OnboardingWorkflowFilter{UserID: user.ID})
	if err != nil {
		writeOwnerOnboardingStoreError(w)
		return
	}
	workflowByInstance := make(map[string][]contracts.OnboardingWorkflow, len(instances))
	for _, workflow := range workflows {
		workflowByInstance[workflow.InstanceID] = append(workflowByInstance[workflow.InstanceID], workflow)
	}

	now := time.Now().UTC()
	response := ownerOnboardingResponse{
		GeneratedAt: now,
		Instances:   make([]ownerOnboardingInstanceStatus, 0, len(instances)),
	}
	for _, instance := range instances {
		item, err := s.ownerOnboardingInstanceStrict(ctx, instance, workflowByInstance[instance.ID], now)
		if err != nil {
			writeOwnerOnboardingStoreError(w)
			return
		}
		response.Instances = append(response.Instances, item)
		response.Summary.TotalInstances++
		if item.ConnectorState == ownerConnectorReady {
			response.Summary.ConnectorReady++
		}
		if item.ServiceState == ownerServiceActive {
			response.Summary.ActiveInstances++
		}
		if ownerOnboardingActionRequired(item.ServiceState) {
			response.Summary.ActionRequired++
		}
		response.Summary.DeliveredKeys += item.DeliveredKeys
		response.Summary.VerifiedKeys += item.VerifiedKeys
		response.Summary.PublishedBindings += item.PublishedBindings
		response.Summary.ActiveBindings += item.ActiveBindings
		response.Summary.CallableBindings += item.CallableBindings
		response.Summary.AwaitingVerificationBindings += item.AwaitingVerificationBindings
		response.Summary.VerificationFailedBindings += item.VerificationFailedBindings
	}
	sort.Slice(response.Instances, func(i, j int) bool {
		if response.Instances[i].CreatedOrder() != response.Instances[j].CreatedOrder() {
			return response.Instances[i].CreatedOrder() < response.Instances[j].CreatedOrder()
		}
		return response.Instances[i].InstanceID < response.Instances[j].InstanceID
	})
	writeJSON(w, http.StatusOK, response)
}

// CreatedOrder keeps the API stable without exposing an additional timestamp:
// active instances sort last so the next actionable setup remains prominent.
func (item ownerOnboardingInstanceStatus) CreatedOrder() int {
	if ownerOnboardingActionRequired(item.ServiceState) {
		return 0
	}
	if item.ServiceState != ownerServiceActive {
		return 1
	}
	return 2
}

func (s *Server) ownerOnboardingInstance(
	ctx context.Context,
	instance contracts.Instance,
	workflows []contracts.OnboardingWorkflow,
	now time.Time,
) (ownerOnboardingInstanceStatus, error) {
	item := ownerOnboardingInstanceStatus{
		InstanceID: instance.ID, InstanceName: instance.Name, InstanceKind: instance.Kind,
		WorkflowCount: len(workflows), UpdatedAt: instance.UpdatedAt,
	}
	connectorState, lastSeen, err := s.ownerConnectorState(ctx, instance, now)
	if err != nil {
		return ownerOnboardingInstanceStatus{}, err
	}
	item.ConnectorState = connectorState
	item.ConnectorLastSeenAt = lastSeen

	for _, workflow := range workflows {
		if workflow.UpdatedAt.After(item.UpdatedAt) {
			item.UpdatedAt = workflow.UpdatedAt
		}
		if workflow.Status == contracts.OnboardingReady && workflow.Stage == contracts.OnboardingActive {
			item.ReadyWorkflows++
		}
		item.DeliveredKeys += len(workflow.KeyVersionSummary)
		if workflow.NextAttemptAt != nil && (item.NextAttemptAt == nil || workflow.NextAttemptAt.Before(*item.NextAttemptAt)) {
			next := *workflow.NextAttemptAt
			item.NextAttemptAt = &next
		}
		item.Stage, item.Status = aggregateOwnerOnboardingState(item.Stage, item.Status, workflow)
		if item.BlockerCode == "" {
			item.BlockerCode = ownerSafeOnboardingBlocker(workflow.LastErrorCode)
		}

		for channelID, keyVersion := range workflow.KeyVersionSummary {
			proof, proofErr := s.store.GetUpstreamKeyProofReceipt(ctx, channelID, instance.ID)
			switch {
			case proofErr == nil && proof.KeyVersion == keyVersion && proof.Status == contracts.DeliveryKeyProofVerified:
				item.VerifiedKeys++
			case proofErr == nil, errors.Is(proofErr, store.ErrNotFound):
				// Absence is a truthful unverified count, not a storage failure.
			default:
				return ownerOnboardingInstanceStatus{}, proofErr
			}
		}
		if strings.TrimSpace(workflow.PlanID) == "" {
			continue
		}
		plan, planErr := s.store.GetRoutePlan(ctx, workflow.PlanID)
		if planErr != nil {
			if errors.Is(planErr, store.ErrNotFound) {
				continue
			}
			return ownerOnboardingInstanceStatus{}, planErr
		}
		if plan.UserID != instance.UserID || plan.InstanceID != instance.ID {
			continue
		}
		bindings, bindingErr := s.store.ListPublishedBindings(ctx, plan.ID)
		if bindingErr != nil {
			return ownerOnboardingInstanceStatus{}, bindingErr
		}
		for _, binding := range bindings {
			if binding.InstanceID != "" && binding.InstanceID != instance.ID || binding.State == contracts.BindingRevoked {
				continue
			}
			item.PublishedBindings++
			if binding.State == contracts.BindingActive {
				item.ActiveBindings++
			}
			if binding.IsCallable() {
				item.CallableBindings++
			}
			switch binding.VerificationStatus {
			case contracts.BindingVerificationPublishedPending, contracts.BindingVerificationAwaitingFirstRequest, "":
				item.AwaitingVerificationBindings++
			case contracts.BindingVerificationFailed:
				item.VerificationFailedBindings++
			}
		}
	}
	item.ServiceState, item.BlockerCode = deriveOwnerOnboardingService(item, workflows)
	return item, nil
}

func (s *Server) ownerConnectorState(ctx context.Context, instance contracts.Instance, now time.Time) (ownerConnectorReadiness, *time.Time, error) {
	if strings.TrimSpace(instance.ConnectorID) == "" {
		return ownerConnectorMissing, nil, nil
	}
	connector, err := s.store.GetConnector(ctx, instance.ConnectorID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ownerConnectorMissing, nil, nil
		}
		return ownerConnectorOffline, nil, err
	}
	lastSeen := connector.LastSeenAt
	effective := effectiveConnectorStatus(connector, now)
	if effective.Status != contracts.ConnectorStatusOnline || connector.UserID != instance.UserID || connector.InstanceID != instance.ID {
		return ownerConnectorOffline, lastSeen, nil
	}
	if connector.ProtocolVersion != contracts.ConnectorProtocolVersion {
		return ownerConnectorUpdateNeeded, lastSeen, nil
	}
	runtime := connector.Gateway
	if !runtime.GatewayConfigured || runtime.GatewayStatus != "ok" || runtime.GatewayKind != string(instance.Kind) ||
		!contracts.IsConnectorBindingEncryptionPublicKey(runtime.BindingEncryptionPublicKey) {
		return ownerConnectorSetupRequired, lastSeen, nil
	}
	for _, capability := range ownerOnboardingRequiredCapabilities() {
		if !ownerConnectorHasCapability(runtime.Capabilities, capability) {
			return ownerConnectorUpdateNeeded, lastSeen, nil
		}
	}
	return ownerConnectorReady, lastSeen, nil
}

func ownerOnboardingRequiredCapabilities() []contracts.ConnectorTaskType {
	return []contracts.ConnectorTaskType{
		contracts.ConnectorTaskGatewayAccountsList,
		contracts.ConnectorTaskGatewayBindingInstall,
		contracts.ConnectorTaskGatewayBindingProof,
		contracts.ConnectorTaskGatewaySchedulingBarrier,
		contracts.ConnectorTaskGatewayAccountCreate,
		contracts.ConnectorTaskGatewayAccountUpdate,
	}
}

func ownerConnectorHasCapability(values []contracts.ConnectorTaskType, want contracts.ConnectorTaskType) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func aggregateOwnerOnboardingState(
	stage contracts.OnboardingStage,
	status contracts.OnboardingStatus,
	workflow contracts.OnboardingWorkflow,
) (contracts.OnboardingStage, contracts.OnboardingStatus) {
	if stage == "" || ownerOnboardingStageRank(workflow.Stage) < ownerOnboardingStageRank(stage) {
		stage = workflow.Stage
	}
	if status == "" || ownerOnboardingStatusRank(workflow.Status) < ownerOnboardingStatusRank(status) {
		status = workflow.Status
	}
	return stage, status
}

func ownerOnboardingStageRank(stage contracts.OnboardingStage) int {
	switch stage {
	case contracts.OnboardingWaitingConnector:
		return 0
	case contracts.OnboardingCheckingGateway:
		return 1
	case contracts.OnboardingAssigningKeys:
		return 2
	case contracts.OnboardingDeliveringBindings:
		return 3
	case contracts.OnboardingPublishing:
		return 4
	case contracts.OnboardingVerifying:
		return 5
	case contracts.OnboardingFailedRetryable:
		return 6
	case contracts.OnboardingDormant:
		return 7
	case contracts.OnboardingActive:
		return 8
	default:
		return 9
	}
}

func ownerOnboardingStatusRank(status contracts.OnboardingStatus) int {
	switch status {
	case contracts.OnboardingRetryable:
		return 0
	case contracts.OnboardingRunning:
		return 1
	case contracts.OnboardingPending:
		return 2
	case contracts.OnboardingDormantStatus:
		return 3
	case contracts.OnboardingReady:
		return 4
	default:
		return 5
	}
}

func deriveOwnerOnboardingService(
	item ownerOnboardingInstanceStatus,
	workflows []contracts.OnboardingWorkflow,
) (ownerOnboardingServiceState, string) {
	switch item.ConnectorState {
	case ownerConnectorMissing:
		return ownerServiceAwaitingConnector, "connector_required"
	case ownerConnectorOffline:
		return ownerServiceConnectorOffline, "connector_offline"
	case ownerConnectorSetupRequired:
		return ownerServiceGatewaySetup, "gateway_setup_required"
	case ownerConnectorUpdateNeeded:
		return ownerServiceConnectorUpdate, "connector_update_required"
	}
	if len(workflows) == 0 {
		return ownerServiceWaitingPlatform, "platform_resources_pending"
	}
	allDormant := true
	anyRetryable := false
	for _, workflow := range workflows {
		allDormant = allDormant && workflow.Status == contracts.OnboardingDormantStatus
		anyRetryable = anyRetryable || workflow.Status == contracts.OnboardingRetryable
	}
	if allDormant {
		return ownerServicePaused, "platform_service_paused"
	}
	if anyRetryable {
		if item.BlockerCode == "" {
			item.BlockerCode = "automatic_delivery_retry"
		}
		return ownerServiceRetrying, item.BlockerCode
	}
	if item.ReadyWorkflows == len(workflows) {
		if item.CallableBindings == 0 && item.VerificationFailedBindings > 0 {
			return ownerServiceVerificationFailed, "service_verification_failed"
		}
		if item.CallableBindings == 0 && item.AwaitingVerificationBindings > 0 {
			return ownerServiceAwaitingVerification, "service_verification_pending"
		}
		if item.PublishedBindings == 0 || item.CallableBindings == 0 || item.VerifiedKeys < item.DeliveredKeys {
			return ownerServiceDegraded, "service_verification_pending"
		}
		return ownerServiceActive, ""
	}
	return ownerServiceProvisioning, item.BlockerCode
}

func ownerSafeOnboardingBlocker(code string) string {
	switch strings.TrimSpace(code) {
	case "":
		return ""
	case "connector_unavailable":
		return "connector_offline"
	case "connector_gateway_not_ready":
		return "gateway_setup_required"
	case "connector_capability_missing":
		return "connector_update_required"
	case "key_capacity_unavailable", "key_catalog_unavailable", "key_assignment_failed":
		return "platform_capacity_pending"
	case "pool_inactive", "route_plan_suspended":
		return "platform_service_paused"
	default:
		return "automatic_delivery_retry"
	}
}

func ownerOnboardingActionRequired(state ownerOnboardingServiceState) bool {
	switch state {
	case ownerServiceAwaitingConnector, ownerServiceConnectorOffline,
		ownerServiceGatewaySetup, ownerServiceConnectorUpdate:
		return true
	default:
		return false
	}
}

func writeOwnerOnboardingStoreError(w http.ResponseWriter) {
	writeError(w, http.StatusInternalServerError, "store_error", "onboarding status is temporarily unavailable")
}
