package contracts

import (
	"encoding/json"
	"testing"
)

func TestSummarizeConnectorTaskProjectsOnlyLifecycleTargets(t *testing.T) {
	tests := []struct {
		name        string
		taskType    ConnectorTaskType
		input       any
		wantChannel string
		wantAccount string
	}{
		{
			name:     "create",
			taskType: ConnectorTaskGatewayAccountCreate,
			input: ConnectorGatewayAccountCreateInput{Spec: GatewayAccountSpec{
				ChannelID: "channel-create", CredentialBindingID: "binding-private",
			}},
			wantChannel: "channel-create",
		},
		{
			name:     "update",
			taskType: ConnectorTaskGatewayAccountUpdate,
			input: ConnectorGatewayAccountUpdateInput{Spec: GatewayAccountSpec{
				ChannelID: "channel-update", RemoteID: "account-update", CredentialBindingID: "binding-private",
			}},
			wantChannel: "channel-update",
			wantAccount: "account-update",
		},
		{
			name:     "delete",
			taskType: ConnectorTaskGatewayAccountDelete,
			input: ConnectorGatewayAccountDeleteInput{AccountID: "account-delete", Fence: &GatewaySchedulingFence{
				Scope: "auto-switch/plan/plan-delete", Version: 8, Sequence: 3,
			}},
			wantAccount: "account-delete",
		},
		{
			name:     "unrelated task",
			taskType: ConnectorTaskGatewayBindingInstall,
			input: ConnectorGatewayBindingInstallInput{
				ChannelID: "channel-hidden", BindingID: "binding-private", Ciphertext: "ciphertext-private",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.input)
			if err != nil {
				t.Fatalf("marshal input: %v", err)
			}
			summary := SummarizeConnectorTask(ConnectorTask{Type: test.taskType, Input: raw})
			if summary.TargetChannelID != test.wantChannel || summary.TargetAccountID != test.wantAccount {
				t.Fatalf("targets = (%q, %q), want (%q, %q)", summary.TargetChannelID, summary.TargetAccountID, test.wantChannel, test.wantAccount)
			}
			if test.taskType == ConnectorTaskGatewayAccountDelete {
				if summary.SchedulingFence == nil || summary.SchedulingFence.Scope != "auto-switch/plan/plan-delete" ||
					summary.SchedulingFence.Version != 8 || summary.SchedulingFence.Sequence != 3 {
					t.Fatalf("delete scheduling fence = %+v", summary.SchedulingFence)
				}
			} else if summary.SchedulingFence != nil {
				t.Fatalf("unrelated scheduling fence exposed: %+v", summary.SchedulingFence)
			}
		})
	}
}

func TestSummarizeConnectorTaskRejectsInvalidFenceProjection(t *testing.T) {
	raw, err := json.Marshal(ConnectorGatewayAccountDeleteInput{
		AccountID: "account-delete",
		Fence:     &GatewaySchedulingFence{Scope: "auto-switch/plan/plan\nprivate", Version: 8, Sequence: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	summary := SummarizeConnectorTask(ConnectorTask{Type: ConnectorTaskGatewayAccountDelete, Input: raw})
	if summary.SchedulingFence != nil {
		t.Fatalf("invalid fence was projected: %+v", summary.SchedulingFence)
	}
}

func TestSummarizeConnectorTaskIgnoresMalformedLifecycleInput(t *testing.T) {
	summary := SummarizeConnectorTask(ConnectorTask{
		Type:  ConnectorTaskGatewayAccountUpdate,
		Input: json.RawMessage(`{"spec":`),
	})
	if summary.TargetChannelID != "" || summary.TargetAccountID != "" {
		t.Fatalf("malformed task exposed targets: %+v", summary)
	}
}

func TestSummarizeConnectorTaskOmitsInvalidTargetIDs(t *testing.T) {
	raw, err := json.Marshal(ConnectorGatewayAccountUpdateInput{Spec: GatewayAccountSpec{
		ChannelID: "channel-valid", RemoteID: "account\nprivate",
	}})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	summary := SummarizeConnectorTask(ConnectorTask{Type: ConnectorTaskGatewayAccountUpdate, Input: raw})
	if summary.TargetChannelID != "channel-valid" || summary.TargetAccountID != "" {
		t.Fatalf("invalid target projection = %+v", summary)
	}
}
