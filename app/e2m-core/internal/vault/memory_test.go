package vault

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryVaultStoreResolveDelete(t *testing.T) {
	ctx := context.Background()
	v := NewMemoryVault()

	ref, err := v.Store(ctx, "credential_ref:offer-1", "sk-secret-plaintext")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if ref != "credential_ref:offer-1" {
		t.Fatalf("unexpected ref: %s", ref)
	}

	sec, err := v.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if sec.Value != "sk-secret-plaintext" {
		t.Fatalf("unexpected plaintext: %s", sec.Value)
	}

	if err := v.Delete(ctx, ref); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := v.Resolve(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}
