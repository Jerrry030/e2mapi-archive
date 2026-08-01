package httpapi

import (
	"testing"

	"e2m.local/contracts"
)

func TestConnectorCompletionUsesDedicatedActionForEveryTaskType(t *testing.T) {
	t.Parallel()
	types := []contracts.ConnectorTaskType{
		contracts.ConnectorTaskGatewayHealth,
		contracts.ConnectorTaskGatewayAccountsList,
		contracts.ConnectorTaskGatewayQualityProbe,
		contracts.ConnectorTaskGatewayBindingProof,
		contracts.ConnectorTaskGatewayBindingInstall,
		contracts.ConnectorTaskGatewaySchedulableSet,
		contracts.ConnectorTaskGatewaySwitch,
		contracts.ConnectorTaskGatewaySchedulingBarrier,
		contracts.ConnectorTaskGatewayAccountCreate,
		contracts.ConnectorTaskGatewayAccountUpdate,
		contracts.ConnectorTaskGatewayAccountDelete,
	}
	seen := make(map[string]contracts.ConnectorTaskType, len(types))
	for _, taskType := range types {
		taskType := taskType
		t.Run(string(taskType), func(t *testing.T) {
			t.Parallel()
			want := "connector_task.complete." + string(taskType)
			got := connectorTaskCompletionAuditAction(taskType)
			if got != want {
				t.Fatalf("completion action=%q want=%q", got, want)
			}
		})
		action := connectorTaskCompletionAuditAction(taskType)
		if prior, duplicate := seen[action]; duplicate {
			t.Fatalf("task types %q and %q share completion action %q", prior, taskType, action)
		}
		seen[action] = taskType
	}
	if len(seen) != 11 {
		t.Fatalf("dedicated connector completion actions=%d want=11", len(seen))
	}
}

func TestConnectorCompletionEventLevelIsIndependentFromTaskRisk(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		task   contracts.ConnectorTask
		result string
		want   contracts.EventLevel
	}{
		{
			name:   "successful read is info",
			task:   contracts.ConnectorTask{Type: contracts.ConnectorTaskGatewayAccountsList, RiskLevel: contracts.RiskLevelL0},
			result: "accepted", want: contracts.EventLevelInfo,
		},
		{
			name:   "successful sensitive key delivery is notice",
			task:   contracts.ConnectorTask{Type: contracts.ConnectorTaskGatewayBindingInstall, RiskLevel: contracts.RiskLevelL2},
			result: "accepted", want: contracts.EventLevelNotice,
		},
		{
			name:   "low-risk read retry is notice",
			task:   contracts.ConnectorTask{Type: contracts.ConnectorTaskGatewayAccountsList, RiskLevel: contracts.RiskLevelL0},
			result: "retrying", want: contracts.EventLevelNotice,
		},
		{
			name:   "low-risk read failure is warning",
			task:   contracts.ConnectorTask{Type: contracts.ConnectorTaskGatewayAccountsList, RiskLevel: contracts.RiskLevelL0},
			result: "failed", want: contracts.EventLevelWarning,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := connectorTaskEventLevel(tt.task, tt.result); got != tt.want {
				t.Fatalf("connector event level=%q want=%q (risk=%q)", got, tt.want, tt.task.RiskLevel)
			}
		})
	}
}

func TestUnknownConnectorTaskKeepsLegacyCompletionAction(t *testing.T) {
	t.Parallel()
	if got := connectorTaskCompletionAuditAction(contracts.ConnectorTaskType("future.task")); got != "connector_task.complete" {
		t.Fatalf("unknown task fallback action=%q", got)
	}
}
