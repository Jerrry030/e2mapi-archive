// Package vault is the credential boundary for E2M Core. Plaintext secrets for
// notifications, upstream accounts, and proxies live only behind a Vault; the
// rest of the system passes a credential_ref string and resolves it through
// this interface. Gateway admin credentials intentionally stay outside Core and
// are configured in the customer-side connector runtime.
package vault

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a credential_ref resolves to nothing.
var ErrNotFound = errors.New("vault: credential ref not found")

// Secret is resolved plaintext. Callers must not persist or log its Value.
type Secret struct {
	Ref   string
	Value string
}

// Vault stores and resolves secrets by reference. Store returns the ref to keep
// in the database; Resolve exchanges a ref for plaintext at the moment of use.
type Vault interface {
	Store(ctx context.Context, ref string, plaintext string) (string, error)
	Resolve(ctx context.Context, ref string) (Secret, error)
	Delete(ctx context.Context, ref string) error
	ListRefs(ctx context.Context) ([]string, error)
}
