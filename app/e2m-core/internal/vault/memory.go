package vault

import (
	"context"
	"sort"
	"sync"
)

// MemoryVault is a local-development Vault implementation. It holds plaintext in
// process memory only — never on disk, never in the E2M database. Production
// deployments swap this for an Infisical / OpenBao backed implementation behind
// the same interface.
type MemoryVault struct {
	mu      sync.RWMutex
	secrets map[string]string
}

var _ Vault = (*MemoryVault)(nil)

func NewMemoryVault() *MemoryVault {
	return &MemoryVault{secrets: map[string]string{}}
}

func (v *MemoryVault) Store(ctx context.Context, ref string, plaintext string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.secrets[ref] = plaintext
	return ref, nil
}

func (v *MemoryVault) Resolve(ctx context.Context, ref string) (Secret, error) {
	if err := ctx.Err(); err != nil {
		return Secret{}, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	plaintext, ok := v.secrets[ref]
	if !ok {
		return Secret{}, ErrNotFound
	}
	return Secret{Ref: ref, Value: plaintext}, nil
}

func (v *MemoryVault) Delete(ctx context.Context, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.secrets, ref)
	return nil
}

func (v *MemoryVault) ListRefs(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	refs := make([]string, 0, len(v.secrets))
	for ref := range v.secrets {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs, nil
}
