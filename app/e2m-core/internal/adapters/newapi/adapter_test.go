package newapi

import (
	"context"
	"testing"

	"e2m.local/contracts"
)

type taskStore struct{}

func (taskStore) CreateConnectorTask(context.Context, contracts.ConnectorTask) (contracts.ConnectorTask, error) {
	return contracts.ConnectorTask{}, nil
}

func (taskStore) GetConnectorTask(context.Context, string) (contracts.ConnectorTask, error) {
	return contracts.ConnectorTask{}, nil
}

func TestNew(t *testing.T) {
	a := New(taskStore{})
	if a.Kind() != contracts.InstanceKindNewAPI {
		t.Fatalf("unexpected kind %q", a.Kind())
	}
	if len(a.Capabilities()) != 7 {
		t.Fatalf("unexpected capability count %d", len(a.Capabilities()))
	}
	foundTrafficShare := false
	for _, capability := range a.Capabilities() {
		if capability.Name == contracts.CapabilitySetAccountTrafficShare {
			foundTrafficShare = capability.Supported && capability.Mode == contracts.CapabilityModeWrite
		}
	}
	if !foundTrafficShare {
		t.Fatal("newapi adapter did not declare verified traffic-share support")
	}
}
