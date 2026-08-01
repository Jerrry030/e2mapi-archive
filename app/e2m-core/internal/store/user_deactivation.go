package store

import (
	"encoding/json"
	"strings"

	"e2m.local/contracts"
)

const userDeactivationDrainFailedCode = "gateway_drain_failed"

func normalizeUserDeactivationStatus(status contracts.UserDeactivationStatus) contracts.UserDeactivationStatus {
	if status == "" {
		return contracts.UserDeactivationNone
	}
	return status
}

func activeClientUser(user contracts.User) bool {
	return user.Enabled && userHasRole(user.Roles, contracts.UserRoleClient)
}

func deactivatingUser(user contracts.User) bool {
	return normalizeUserDeactivationStatus(user.DeactivationStatus).InProgress()
}

// connectorTaskAllowedDuringUserDeactivation is intentionally narrower than
// the ordinary Connector capability list. In particular, schedulable.set must
// contain an explicit false value; a missing or malformed boolean never passes.
func connectorTaskAllowedDuringUserDeactivation(task contracts.ConnectorTask) bool {
	switch task.Type {
	case contracts.ConnectorTaskGatewayAccountsList, contracts.ConnectorTaskGatewaySchedulingBarrier:
		return true
	case contracts.ConnectorTaskGatewaySchedulableSet:
		var input struct {
			AccountID   string `json:"account_id"`
			Schedulable *bool  `json:"schedulable"`
		}
		return json.Unmarshal(task.Input, &input) == nil && strings.TrimSpace(input.AccountID) != "" &&
			input.Schedulable != nil && !*input.Schedulable
	default:
		return false
	}
}
