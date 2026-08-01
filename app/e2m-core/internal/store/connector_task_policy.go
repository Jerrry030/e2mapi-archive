package store

import (
	"encoding/json"
	"strings"
	"time"

	"e2m.local/contracts"
)

const connectorTaskSupersededErrorCode = "scheduling_fence_stale"

// connectorTaskRequiresExecutionPermit identifies tasks whose remote mutation
// is authorized by a durable Core execution. Legacy/manual writes and
// read-only tasks retain the leased completion protocol.
func connectorTaskRequiresExecutionPermit(task contracts.ConnectorTask) bool {
	if task.PlanID == "" && task.ExecutionScope == "" {
		return false
	}
	fence, fenced := contracts.ConnectorTaskSchedulingFence(task)
	return fenced && contracts.IsGatewaySchedulingFence(fence) && connectorTaskExecutionIdentityMatchesFence(task, fence)
}

func normalizeConnectorTaskPlanFence(task *contracts.ConnectorTask) error {
	if task == nil {
		return ErrInvalid
	}
	fence, fenced := contracts.ConnectorTaskSchedulingFence(*task)
	planID := strings.TrimSpace(task.PlanID)
	executionIdentity := contracts.ConnectorExecutionIdentity{
		Scope:      strings.TrimSpace(task.ExecutionScope),
		ID:         strings.TrimSpace(task.ExecutionID),
		Generation: task.ExecutionGeneration,
	}
	hasAnyExecutionIdentity := executionIdentity.Scope != "" || executionIdentity.ID != "" || executionIdentity.Generation != 0
	hasCompleteExecutionIdentity := executionIdentity.Scope != "" && executionIdentity.ID != "" && executionIdentity.Generation > 0
	if hasAnyExecutionIdentity != hasCompleteExecutionIdentity {
		return ErrConflict
	}
	if !fenced {
		if planID != "" || task.SchedulingGeneration != 0 || hasAnyExecutionIdentity {
			return ErrConflict
		}
		task.PlanID = ""
		task.ExecutionScope = ""
		task.ExecutionID = ""
		task.ExecutionGeneration = 0
		return nil
	}
	if parsedPlanID, routePlanScoped := contracts.GatewaySchedulingFencePlanID(fence); routePlanScoped {
		if hasAnyExecutionIdentity || planID != "" && planID != parsedPlanID ||
			task.SchedulingGeneration != 0 && task.SchedulingGeneration != fence.Version {
			return ErrConflict
		}
		task.PlanID = parsedPlanID
		task.SchedulingGeneration = fence.Version
		return nil
	}
	instanceID, _, hybridScoped := contracts.GatewaySchedulingFenceHybridIdentity(fence)
	if !hybridScoped || planID != "" || task.SchedulingGeneration != 0 ||
		!hasCompleteExecutionIdentity || !contracts.ValidHybridRoutingExecutionIdentity(executionIdentity) ||
		instanceID != task.InstanceID || executionIdentity.Generation != fence.Version {
		return ErrConflict
	}
	task.PlanID = ""
	task.SchedulingGeneration = 0
	task.ExecutionScope = executionIdentity.Scope
	task.ExecutionID = executionIdentity.ID
	task.ExecutionGeneration = executionIdentity.Generation
	return nil
}

func connectorTaskExecutionIdentityMatchesFence(task contracts.ConnectorTask, fence contracts.GatewaySchedulingFence) bool {
	if planID, ok := contracts.GatewaySchedulingFencePlanID(fence); ok {
		return task.PlanID == planID && task.SchedulingGeneration == fence.Version &&
			task.ExecutionScope == "" && task.ExecutionID == "" && task.ExecutionGeneration == 0
	}
	instanceID, _, ok := contracts.GatewaySchedulingFenceHybridIdentity(fence)
	identity := contracts.ConnectorExecutionIdentity{
		Scope: task.ExecutionScope, ID: task.ExecutionID, Generation: task.ExecutionGeneration,
	}
	return ok && instanceID == task.InstanceID && contracts.ValidHybridRoutingExecutionIdentity(identity) &&
		identity.Generation == fence.Version && task.PlanID == "" && task.SchedulingGeneration == 0
}

func connectorTaskMatchesCurrentPlan(task contracts.ConnectorTask, plan contracts.RoutePlan) bool {
	return task.PlanID == plan.ID && task.UserID == plan.UserID && task.InstanceID == plan.InstanceID &&
		task.SchedulingGeneration > 0 && task.SchedulingGeneration == plan.SchedulingGeneration
}

func connectorTaskMatchesCurrentHybridExecution(task contracts.ConnectorTask, execution contracts.HybridRoutingExecution) bool {
	return task.ExecutionScope == contracts.HybridRoutingExecutionScope && task.ExecutionID == execution.ID &&
		task.UserID == execution.UserID && task.InstanceID == execution.InstanceID && task.ExecutionGeneration > 0 &&
		task.ExecutionGeneration == execution.Generation && execution.Status == contracts.HybridRoutingExecutionApplying
}

func memoryHybridRoutingExecutionIndexLocked(s *MemoryStore, id string) int {
	for index := range s.hybridRoutingExecutions {
		if s.hybridRoutingExecutions[index].ID == id {
			return index
		}
	}
	return -1
}

func memoryConnectorTaskMatchesCurrentHybridExecutionLocked(s *MemoryStore, task contracts.ConnectorTask) bool {
	index := memoryHybridRoutingExecutionIndexLocked(s, task.ExecutionID)
	if index < 0 || !connectorTaskMatchesCurrentHybridExecution(task, s.hybridRoutingExecutions[index]) {
		return false
	}
	allocation, ok := s.hybridAllocations[task.InstanceID]
	return ok && allocation.UserID == task.UserID && allocation.RoutingGeneration == task.ExecutionGeneration
}

func supersedeConnectorTask(task *contracts.ConnectorTask, now time.Time) bool {
	if task == nil || task.PlanID == "" && task.ExecutionScope == "" ||
		task.Status != contracts.ConnectorTaskPending && task.Status != contracts.ConnectorTaskLeased {
		return false
	}
	task.Status = contracts.ConnectorTaskFailed
	task.Result = nil
	task.Error = contracts.ConnectorTaskError{Code: connectorTaskSupersededErrorCode}
	task.LeaseOwner = ""
	task.LeaseNonce = ""
	task.LeaseUntil = nil
	task.UpdatedAt = now
	return true
}

func connectorTaskFenceFromRaw(taskType contracts.ConnectorTaskType, raw json.RawMessage) (contracts.GatewaySchedulingFence, bool) {
	return contracts.ConnectorTaskSchedulingFence(contracts.ConnectorTask{Type: taskType, Input: raw})
}

var connectorTaskRetryBackoffs = [...]time.Duration{
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
}

// connectorTaskRetryDelay returns the delay after a completed failed attempt.
// Attempts is one-based because leasing increments it before execution.
func connectorTaskRetryDelay(attempts int) time.Duration {
	index := attempts - 1
	if index < 0 {
		index = 0
	}
	if index >= len(connectorTaskRetryBackoffs) {
		index = len(connectorTaskRetryBackoffs) - 1
	}
	return connectorTaskRetryBackoffs[index]
}

func expireConnectorTask(task *contracts.ConnectorTask, now time.Time) bool {
	if task == nil || task.ExpiresAt.IsZero() || task.ExpiresAt.After(now) ||
		task.Status != contracts.ConnectorTaskPending && task.Status != contracts.ConnectorTaskLeased {
		return false
	}
	task.Status = contracts.ConnectorTaskExpired
	task.Result = nil
	task.Error = contracts.ConnectorTaskError{Code: "expired"}
	task.LeaseOwner = ""
	task.LeaseNonce = ""
	task.LeaseUntil = nil
	task.UpdatedAt = now
	return true
}
