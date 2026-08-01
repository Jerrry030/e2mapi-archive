package httpapi

import (
	"context"
	"fmt"
	"strings"

	"e2m.local/contracts"
)

// managedRemoteIDs resolves gateway-native account IDs owned by published
// bindings. Every binding state is included: disabled/revoked bindings can
// still exist in a gateway or in historical snapshots until cleanup finishes.
func (s *Server) managedRemoteIDs(ctx context.Context, instanceIDs map[string]struct{}) (map[string]map[string]struct{}, error) {
	result := make(map[string]map[string]struct{}, len(instanceIDs))
	for instanceID := range instanceIDs {
		result[instanceID] = map[string]struct{}{}
	}
	if len(instanceIDs) == 0 {
		return result, nil
	}

	bindings, err := s.store.ListPublishedBindings(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, binding := range bindings {
		remoteID := strings.TrimSpace(binding.RemoteID)
		if remoteID == "" {
			continue
		}
		instanceID := strings.TrimSpace(binding.InstanceID)
		if instanceID == "" {
			plan, err := s.store.GetRoutePlan(ctx, binding.PlanID)
			if err != nil {
				return nil, fmt.Errorf("resolve binding plan %q: %w", binding.PlanID, err)
			}
			instanceID = plan.InstanceID
		}
		if _, requested := instanceIDs[instanceID]; requested {
			result[instanceID][remoteID] = struct{}{}
		}
	}
	return result, nil
}

func filterManagedGatewayAccounts(accounts []contracts.GatewayAccount, managed map[string]struct{}) []contracts.GatewayAccount {
	filtered := make([]contracts.GatewayAccount, 0, len(accounts))
	for _, account := range accounts {
		if _, hidden := managed[strings.TrimSpace(account.ID)]; hidden {
			continue
		}
		filtered = append(filtered, account)
	}
	return filtered
}

func filterManagedHealthSnapshot(snapshot contracts.InstanceHealthSnapshot, managed map[string]struct{}) contracts.InstanceHealthSnapshot {
	accounts := make([]contracts.AccountHealth, 0, len(snapshot.Accounts))
	removed := false
	for _, account := range snapshot.Accounts {
		if _, hidden := managed[strings.TrimSpace(account.AccountID)]; hidden {
			removed = true
			continue
		}
		accounts = append(accounts, account)
	}
	snapshot.Accounts = accounts
	snapshot.TotalAccounts = len(accounts)
	snapshot.HealthyCount = 0
	snapshot.Schedulable = 0
	for _, account := range accounts {
		if account.Healthy {
			snapshot.HealthyCount++
		}
		if account.Schedulable {
			snapshot.Schedulable++
		}
	}
	if removed || len(managed) > 0 {
		// Historical notes were free-form and can contain a managed account's
		// gateway ID or display name, so they cannot be safely token-redacted.
		snapshot.AutoSwitchNote = ""
		// The instance-level gateway error has no account attribution and can
		// include a provider response, remote ID, or credential fragment.
		snapshot.LastError = ""
	}
	return snapshot
}

func filterManagedApproval(approval contracts.ApprovalRequest, managed map[string]struct{}) contracts.ApprovalRequest {
	accountIDs := make([]string, 0, len(approval.AccountIDs))
	removed := false
	for _, accountID := range approval.AccountIDs {
		if _, hidden := managed[strings.TrimSpace(accountID)]; hidden {
			removed = true
			continue
		}
		accountIDs = append(accountIDs, accountID)
	}
	approval.AccountIDs = accountIDs
	if removed {
		// Historical free-form text may repeat an account identifier or display
		// name that cannot be safely reconstructed from the current gateway row.
		approval.Reason = ""
		// Failed batch results historically embedded "accountID: gateway error".
		// Action and status retain the useful outcome without that evidence.
		approval.ResultNote = ""
	}
	return approval
}

func (s *Server) approvalForActor(ctx context.Context, approval contracts.ApprovalRequest, platformAdmin bool) (contracts.ApprovalRequest, error) {
	if platformAdmin {
		return approval, nil
	}
	managed, err := s.managedRemoteIDs(ctx, instanceIDSet(approval.InstanceID))
	if err != nil {
		return contracts.ApprovalRequest{}, err
	}
	return filterManagedApproval(approval, managed[approval.InstanceID]), nil
}

func instanceIDSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}
