package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileVaultRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.enc")
	v, err := NewFileVault(path, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	if _, err := v.Store(ctx, "credential_ref:demo", "s3cret-admin-key"); err != nil {
		t.Fatalf("store: %v", err)
	}
	got, err := v.Resolve(ctx, "credential_ref:demo")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Value != "s3cret-admin-key" {
		t.Fatalf("roundtrip mismatch: %q", got.Value)
	}

	// The file on disk must not contain the plaintext.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if strings.Contains(string(raw), "s3cret-admin-key") {
		t.Fatal("plaintext leaked to disk")
	}
}

func TestFileVaultPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.enc")
	key := "correct horse battery staple!!"

	v1, err := NewFileVault(path, key)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := v1.Store(ctx, "ref:a", "value-a"); err != nil {
		t.Fatalf("store: %v", err)
	}

	v2, err := NewFileVault(path, key)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := v2.Resolve(ctx, "ref:a")
	if err != nil || got.Value != "value-a" {
		t.Fatalf("resolve after reopen: %v %q", err, got.Value)
	}
}

func TestFileVaultResolveReloadsWritesFromAnotherProcess(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.enc")
	key := "correct horse battery staple!!"
	reader, err := NewFileVault(path, key)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewFileVault(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Store(ctx, "ref:late", "late-value"); err != nil {
		t.Fatal(err)
	}
	secret, err := reader.Resolve(ctx, "ref:late")
	if err != nil || secret.Value != "late-value" {
		t.Fatalf("resolve after external write: %+v err=%v", secret, err)
	}
}

func TestFileVaultWrongKeyFails(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.enc")

	v1, _ := NewFileVault(path, "the-first-master-key-000")
	if _, err := v1.Store(ctx, "ref:a", "value-a"); err != nil {
		t.Fatalf("store: %v", err)
	}

	v2, err := NewFileVault(path, "a-different-master-key-1")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := v2.Resolve(ctx, "ref:a"); err == nil {
		t.Fatal("expected decrypt failure with wrong key")
	}
}

func TestFileVaultDelete(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.enc")
	v, _ := NewFileVault(path, "0123456789abcdef0123456789abcdef")

	if _, err := v.Store(ctx, "ref:gone", "x"); err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := v.Delete(ctx, "ref:gone"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := v.Resolve(ctx, "ref:gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := v.Delete(ctx, "ref:gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete should be ErrNotFound, got %v", err)
	}
}

func TestFileVaultShortKeyRejected(t *testing.T) {
	if _, err := NewFileVault(filepath.Join(t.TempDir(), "v.enc"), "short"); err == nil {
		t.Fatal("expected short master key to be rejected")
	}
}

func TestFileVaultStoreFailureRestoresExistingInMemoryValue(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.enc")
	v, err := NewFileVault(path, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Store(ctx, "ref:a", "original"); err != nil {
		t.Fatal(err)
	}
	v.path = filepath.Join(path, "child")
	if _, err := v.Store(ctx, "ref:a", "replacement"); err == nil {
		t.Fatal("expected persistence failure")
	}
	secret, err := v.Resolve(ctx, "ref:a")
	if err != nil || secret.Value != "original" {
		t.Fatalf("existing value was not restored: %+v err=%v", secret, err)
	}
}

func TestFileVaultResolveReloadFailureKeepsHeldSecretsOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.enc")
	v, err := NewFileVault(path, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Store(ctx, "ref:held", "held-value"); err != nil {
		t.Fatal(err)
	}
	v.path = filepath.Join(path, "child")

	// A held credential keeps resolving from the consistent in-memory snapshot.
	secret, err := v.Resolve(ctx, "ref:held")
	if err != nil || secret.Value != "held-value" {
		t.Fatalf("held secret must survive a reload failure: %+v err=%v", secret, err)
	}
	// An unknown ref must surface the reload error, not a misleading ErrNotFound.
	if _, err := v.Resolve(ctx, "ref:unknown"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown ref during reload failure must return the read error, got %v", err)
	}
}

func TestFileVaultDeleteFailureRestoresInMemoryValue(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.enc")
	v, err := NewFileVault(path, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Store(ctx, "ref:a", "original"); err != nil {
		t.Fatal(err)
	}
	v.path = filepath.Join(path, "child")
	if err := v.Delete(ctx, "ref:a"); err == nil {
		t.Fatal("expected persistence failure")
	}
	secret, err := v.Resolve(ctx, "ref:a")
	if err != nil || secret.Value != "original" {
		t.Fatalf("deleted value was not restored: %+v err=%v", secret, err)
	}
}
