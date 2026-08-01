package contracts

import "context"

// Actor identifies who initiated an operation; audit writers read it from the
// request context so audit rows carry the real operator instead of a
// hard-coded "console".
type Actor struct {
	Type string // "user" | "bot" | "workflow" | "system"
	ID   string // user email or system component id
}

type actorCtxKey struct{}

// WithActor attaches the acting identity to ctx.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorCtxKey{}, a)
}

// ActorFromContext returns the acting identity, if one was attached.
func ActorFromContext(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorCtxKey{}).(Actor)
	return a, ok
}
