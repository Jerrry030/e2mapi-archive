package httpapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// ownerOnboardingWorkflowFacts keeps readiness evidence scoped to one
// instance/pool workflow. Instance-level totals are useful for display, but
// must never allow a healthy pool to mask an incomplete sibling workflow.
type ownerOnboardingWorkflowFacts struct {
	DeliveredKeys                int
	VerifiedKeys                 int
	PublishedBindings            int
	ActiveBindings               int
	CallableBindings             int
	AwaitingVerificationBindings int
	VerificationFailedBindings   int
	Ready                        bool
}

func (s *Server) ownerWorkflowFacts(
	ctx context.Context,
	instance contracts.Instance,
	workflow contracts.OnboardingWorkflow,
) (ownerOnboardingWorkflowFacts, error) {
	facts := ownerOnboardingWorkflowFacts{DeliveredKeys: len(workflow.KeyVersionSummary)}
	currentConnectorID := strings.TrimSpace(instance.ConnectorID)
	workflowConnectorID := strings.TrimSpace(workflow.ConnectorID)

	for channelID, keyVersion := range workflow.KeyVersionSummary {
		proof, err := s.store.GetUpstreamKeyProofReceipt(ctx, channelID, instance.ID)
		switch {
		case err == nil && currentConnectorID != "" && workflowConnectorID == currentConnectorID &&
			proof.ConnectorID == currentConnectorID && proof.KeyVersion == keyVersion &&
			proof.Status == contracts.DeliveryKeyProofVerified:
			facts.VerifiedKeys++
		case err == nil, errors.Is(err, store.ErrNotFound):
			// A stale/missing/mismatched proof is truthfully unverified.
		default:
			return ownerOnboardingWorkflowFacts{}, err
		}
	}

	if strings.TrimSpace(workflow.PlanID) != "" {
		plan, err := s.store.GetRoutePlan(ctx, workflow.PlanID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			// Missing plans are incomplete evidence, not an owner-visible store error.
		case err != nil:
			return ownerOnboardingWorkflowFacts{}, err
		case plan.UserID == instance.UserID && plan.InstanceID == instance.ID &&
			plan.Status == contracts.RoutePlanPublished:
			bindings, err := s.store.ListPublishedBindings(ctx, plan.ID)
			if err != nil {
				return ownerOnboardingWorkflowFacts{}, err
			}
			for _, binding := range bindings {
				if binding.InstanceID != "" && binding.InstanceID != instance.ID || binding.State == contracts.BindingRevoked {
					continue
				}
				if _, delivered := workflow.KeyVersionSummary[binding.ChannelID]; !delivered {
					continue
				}
				facts.PublishedBindings++
				if binding.State == contracts.BindingActive {
					facts.ActiveBindings++
				}
				if binding.IsCallable() {
					facts.CallableBindings++
				}
				switch binding.VerificationStatus {
				case contracts.BindingVerificationPublishedPending, contracts.BindingVerificationAwaitingFirstRequest, "":
					facts.AwaitingVerificationBindings++
				case contracts.BindingVerificationFailed:
					facts.VerificationFailedBindings++
				}
			}
		}
	}

	facts.Ready = workflow.Status == contracts.OnboardingReady &&
		workflow.Stage == contracts.OnboardingActive &&
		facts.DeliveredKeys > 0 && facts.VerifiedKeys == facts.DeliveredKeys &&
		facts.PublishedBindings > 0 && facts.CallableBindings > 0
	return facts, nil
}

func selectOwnerRetryMetadata(workflows []contracts.OnboardingWorkflow) (string, *time.Time) {
	var selected *contracts.OnboardingWorkflow
	for i := range workflows {
		workflow := &workflows[i]
		if workflow.Status != contracts.OnboardingRetryable ||
			(selected != nil && !workflow.UpdatedAt.After(selected.UpdatedAt)) {
			continue
		}
		selected = workflow
	}
	if selected == nil {
		return "", nil
	}
	blocker := ownerSafeOnboardingBlocker(selected.LastErrorCode)
	if blocker == "" {
		blocker = "automatic_delivery_retry"
	}
	if selected.NextAttemptAt == nil {
		return blocker, nil
	}
	next := *selected.NextAttemptAt
	return blocker, &next
}

func strictOwnerServiceState(
	item ownerOnboardingInstanceStatus,
	workflows []contracts.OnboardingWorkflow,
	facts []ownerOnboardingWorkflowFacts,
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

	liveWorkflows := 0
	allLiveReady := true
	readyEvidenceComplete := true
	awaitingCallability := false
	callabilityFailed := false
	for i, workflow := range workflows {
		if workflow.Status == contracts.OnboardingDormantStatus {
			continue
		}
		liveWorkflows++
		if workflow.Status == contracts.OnboardingRetryable {
			blocker, _ := selectOwnerRetryMetadata(workflows)
			return ownerServiceRetrying, blocker
		}
		if workflow.Status != contracts.OnboardingReady || workflow.Stage != contracts.OnboardingActive {
			allLiveReady = false
			continue
		}
		if i >= len(facts) || !facts[i].Ready {
			readyEvidenceComplete = false
			if i < len(facts) && facts[i].DeliveredKeys > 0 && facts[i].VerifiedKeys == facts[i].DeliveredKeys &&
				facts[i].CallableBindings == 0 {
				if facts[i].VerificationFailedBindings > 0 {
					callabilityFailed = true
				} else if facts[i].PublishedBindings > 0 {
					awaitingCallability = true
				}
			}
		}
	}
	if liveWorkflows == 0 {
		return ownerServicePaused, "platform_service_paused"
	}
	if !allLiveReady {
		return ownerServiceProvisioning, ""
	}
	if !readyEvidenceComplete {
		if callabilityFailed {
			return ownerServiceVerificationFailed, "service_verification_failed"
		}
		if awaitingCallability {
			return ownerServiceAwaitingVerification, "service_verification_pending"
		}
		return ownerServiceDegraded, "service_verification_pending"
	}
	return ownerServiceActive, ""
}
