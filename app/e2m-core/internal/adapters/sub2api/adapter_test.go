package sub2api

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
	if a.Kind() != contracts.InstanceKindSub2API {
		t.Fatalf("unexpected kind %q", a.Kind())
	}
	if len(a.Capabilities()) != 6 {
		t.Fatalf("unexpected capability count %d", len(a.Capabilities()))
	}
}
