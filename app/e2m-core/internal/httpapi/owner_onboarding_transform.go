package httpapi

import (
	"context"
	"time"

	"e2m.local/contracts"
)

// ownerOnboardingInstanceStrict is the fail-closed implementation used by the
// owner API. It keeps every pool workflow's verification evidence independent.
func (s *Server) ownerOnboardingInstanceStrict(
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

	workflowFacts := make([]ownerOnboardingWorkflowFacts, 0, len(workflows))
	for _, workflow := range workflows {
		if workflow.UpdatedAt.After(item.UpdatedAt) {
			item.UpdatedAt = workflow.UpdatedAt
		}
		facts, err := s.ownerWorkflowFacts(ctx, instance, workflow)
		if err != nil {
			return ownerOnboardingInstanceStatus{}, err
		}
		workflowFacts = append(workflowFacts, facts)
		if facts.Ready {
			item.ReadyWorkflows++
		}
		item.DeliveredKeys += facts.DeliveredKeys
		item.VerifiedKeys += facts.VerifiedKeys
		item.PublishedBindings += facts.PublishedBindings
		item.ActiveBindings += facts.ActiveBindings
		item.CallableBindings += facts.CallableBindings
		item.AwaitingVerificationBindings += facts.AwaitingVerificationBindings
		item.VerificationFailedBindings += facts.VerificationFailedBindings
		item.Stage, item.Status = aggregateOwnerOnboardingState(item.Stage, item.Status, workflow)
	}
	item.BlockerCode, item.NextAttemptAt = selectOwnerRetryMetadata(workflows)
	item.ServiceState, item.BlockerCode = strictOwnerServiceState(item, workflows, workflowFacts)
	return item, nil
}
