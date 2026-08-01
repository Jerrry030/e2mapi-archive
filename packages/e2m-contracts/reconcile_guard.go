package contracts

import (
	"context"
	"sync/atomic"
)

// ReconcileSideEffectGuard is called immediately before each reconcile item
// that can mutate gateway or binding state. Automatic schedulers use it to
// renew a durable lease and fence workers whose ownership has expired.
type ReconcileSideEffectGuard func(context.Context) error

type reconcileSideEffectGuardContextKey struct{}
type gatewaySchedulingFenceContextKey struct{}
type connectorExecutionIdentityContextKey struct{}

type gatewaySchedulingFenceState struct {
	base GatewaySchedulingFence
	seq  atomic.Int64
}

// GatewaySchedulingFence is carried into durable Connector mutation tasks.
// Scope identifies one ordering domain and Version increases whenever a new
// scheduling operation takes ownership of that domain.
type GatewaySchedulingFence struct {
	Scope    string `json:"scope"`
	Version  int64  `json:"version"`
	Sequence int64  `json:"sequence,omitempty"`
}

// ConnectorExecutionIdentity binds every per-account scheduling fence emitted
// by one durable, non-RoutePlan execution to the same Core-owned operation.
// RoutePlan tasks continue to use ConnectorTask.PlanID and
// SchedulingGeneration; the two identity forms are deliberately disjoint.
type ConnectorExecutionIdentity struct {
	Scope      string `json:"scope"`
	ID         string `json:"id"`
	Generation int64  `json:"generation"`
}

// WithReconcileSideEffectGuard attaches a per-item ownership check to a
// reconcile execution. Callers that do not need distributed fencing omit it.
func WithReconcileSideEffectGuard(ctx context.Context, guard ReconcileSideEffectGuard) context.Context {
	return context.WithValue(ctx, reconcileSideEffectGuardContextKey{}, guard)
}

// RunReconcileSideEffectGuard checks the attached ownership fence, if any.
func RunReconcileSideEffectGuard(ctx context.Context) error {
	guard, _ := ctx.Value(reconcileSideEffectGuardContextKey{}).(ReconcileSideEffectGuard)
	if guard == nil {
		return nil
	}
	return guard(ctx)
}

// WithGatewaySchedulingFence attaches durable ordering metadata to gateway
// mutation tasks created while reconciling an automatic scheduling decision.
func WithGatewaySchedulingFence(ctx context.Context, fence GatewaySchedulingFence) context.Context {
	return context.WithValue(ctx, gatewaySchedulingFenceContextKey{}, &gatewaySchedulingFenceState{base: fence})
}

// WithConnectorExecutionIdentity attaches the durable Core execution that
// authorizes non-RoutePlan scheduling fences created from this context.
func WithConnectorExecutionIdentity(ctx context.Context, identity ConnectorExecutionIdentity) context.Context {
	return context.WithValue(ctx, connectorExecutionIdentityContextKey{}, identity)
}

// ConnectorExecutionIdentityFromContext returns the attached execution
// identity. Domain-specific validation remains the caller's responsibility.
func ConnectorExecutionIdentityFromContext(ctx context.Context) (ConnectorExecutionIdentity, bool) {
	identity, ok := ctx.Value(connectorExecutionIdentityContextKey{}).(ConnectorExecutionIdentity)
	if !ok || identity.Scope == "" || identity.ID == "" || identity.Generation <= 0 {
		return ConnectorExecutionIdentity{}, false
	}
	return identity, true
}

// GatewaySchedulingFenceFromContext returns the attached task fence, if any.
func GatewaySchedulingFenceFromContext(ctx context.Context) (GatewaySchedulingFence, bool) {
	state, ok := ctx.Value(gatewaySchedulingFenceContextKey{}).(*gatewaySchedulingFenceState)
	if !ok || state == nil || state.base.Scope == "" || state.base.Version <= 0 {
		return GatewaySchedulingFence{}, false
	}
	return state.base, true
}

// NextGatewaySchedulingFence allocates the next mutation order inside one
// scheduling generation. Connector persists the tuple (version, sequence), so
// a late earlier command cannot overwrite a later compensation command.
func NextGatewaySchedulingFence(ctx context.Context) (GatewaySchedulingFence, bool) {
	state, ok := ctx.Value(gatewaySchedulingFenceContextKey{}).(*gatewaySchedulingFenceState)
	if !ok || state == nil || state.base.Scope == "" || state.base.Version <= 0 {
		return GatewaySchedulingFence{}, false
	}
	fence := state.base
	fence.Sequence = state.seq.Add(1)
	return fence, true
}
