package store

import (
	"time"

	"e2m.local/contracts"
)

// supersedeConnectorTasksForPlanLocked invalidates every not-yet-terminal
// route-plan mutation from an older generation. Callers hold MemoryStore.mu and
// invoke this in the same critical section that advances the plan generation.
func (s *MemoryStore) supersedeConnectorTasksForPlanLocked(planID string, generation int64, now time.Time) {
	if planID == "" || generation <= 0 {
		return
	}
	for index := range s.connectorTasks {
		task := &s.connectorTasks[index]
		if task.PlanID == planID && task.SchedulingGeneration < generation {
			supersedeConnectorTask(task, now)
		}
	}
}

func (s *MemoryStore) advanceRoutePlanGenerationLocked(plan *contracts.RoutePlan, now time.Time, keepDecisionID string) {
	plan.SchedulingGeneration++
	plan.UpdatedAt = now
	s.supersedeActiveAutoSwitchDecisions(plan.ID, keepDecisionID, plan.SchedulingGeneration, now)
	s.supersedeConnectorTasksForPlanLocked(plan.ID, plan.SchedulingGeneration, now)
}

// routePlanHasExecutingConnectorTaskLocked reports whether a durable remote
// side-effect permit currently freezes the plan. Callers must check this before
// changing any related in-memory state: MemoryStore has no transaction rollback
// that could make a late conflict safe.
func (s *MemoryStore) routePlanHasExecutingConnectorTaskLocked(planID string) bool {
	if planID == "" {
		return false
	}
	for index := range s.connectorTasks {
		task := &s.connectorTasks[index]
		if task.PlanID == planID && task.Status == contracts.ConnectorTaskExecuting {
			return true
		}
	}
	return false
}

func (s *MemoryStore) anyRoutePlanHasExecutingConnectorTaskLocked(planIDs map[string]struct{}) bool {
	for planID := range planIDs {
		if s.routePlanHasExecutingConnectorTaskLocked(planID) {
			return true
		}
	}
	return false
}
