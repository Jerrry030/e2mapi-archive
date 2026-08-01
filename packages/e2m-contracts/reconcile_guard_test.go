package contracts

import (
	"context"
	"testing"
)

func TestGatewaySchedulingFenceSequenceIsSharedAcrossDerivedContexts(t *testing.T) {
	ctx := WithGatewaySchedulingFence(context.Background(), GatewaySchedulingFence{
		Scope: "auto-switch/decision/d-1", Version: 7,
	})
	derived := WithActor(ctx, Actor{Type: "workflow", ID: "auto-switch"})
	first, ok := NextGatewaySchedulingFence(ctx)
	if !ok || first.Sequence != 1 {
		t.Fatalf("first fence=%+v ok=%v", first, ok)
	}
	second, ok := NextGatewaySchedulingFence(derived)
	if !ok || second.Sequence != 2 || second.Scope != first.Scope || second.Version != first.Version {
		t.Fatalf("second fence=%+v ok=%v", second, ok)
	}
}

func TestConnectorExecutionIdentitySurvivesDerivedContexts(t *testing.T) {
	want := ConnectorExecutionIdentity{Scope: HybridRoutingExecutionScope, ID: "hyexec-1", Generation: 3}
	ctx := WithConnectorExecutionIdentity(context.Background(), want)
	derived := WithActor(ctx, Actor{Type: "workflow", ID: "hybrid-routing"})
	got, ok := ConnectorExecutionIdentityFromContext(derived)
	if !ok || got != want || !ValidHybridRoutingExecutionIdentity(got) {
		t.Fatalf("identity=%+v ok=%v", got, ok)
	}
}
