package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// FileVault stores secrets in a single JSON file, each value encrypted with
// AES-256-GCM under a master key supplied via environment (E2M_VAULT_KEY).
// It is the production-posture default until an external secret manager
// (Infisical / OpenBao) is wired in: the file on disk never contains plaintext,
// and losing the master key means losing the secrets (by design).
//
// The master key is hex (64 chars) or any string >= 16 bytes, which is
// stretched with SHA-256 into a 32-byte AES key.
type FileVault struct {
	mu   sync.RWMutex
	path string
	aead cipher.AEAD
	// refs maps credential_ref -> base64(nonce || ciphertext)
	refs map[string]string
}

var _ Vault = (*FileVault)(nil)

// NewFileVault opens (or creates) the vault file at path using masterKey.
func NewFileVault(path, masterKey string) (*FileVault, error) {
	if len(masterKey) < 16 {
		return nil, errors.New("vault: E2M_VAULT_KEY must be at least 16 characters (prefer 64 hex chars)")
	}
	key := deriveKey(masterKey)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	v := &FileVault{path: path, aead: aead, refs: map[string]string{}}
	if err := v.load(); err != nil {
		return nil, err
	}
	return v, nil
}

// deriveKey turns the configured master key into a 32-byte AES key: 64 hex
// chars are decoded directly; anything else is hashed with SHA-256.
func deriveKey(masterKey string) []byte {
	if len(masterKey) == 64 {
		if raw, err := hex.DecodeString(masterKey); err == nil {
			return raw
		}
	}
	sum := sha256.Sum256([]byte(masterKey))
	return sum[:]
}

func (v *FileVault) Store(ctx context.Context, ref string, plaintext string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if ref == "" {
		return "", errors.New("vault: ref is required")
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	// Bind the ciphertext to its ref so entries cannot be swapped on disk.
	sealed := v.aead.Seal(nonce, nonce, []byte(plaintext), []byte(ref))

	v.mu.Lock()
	defer v.mu.Unlock()
	old, existed := v.refs[ref]
	v.refs[ref] = base64.StdEncoding.EncodeToString(sealed)
	if err := v.persistLocked(); err != nil {
		if existed {
			v.refs[ref] = old
		} else {
			delete(v.refs, ref)
		}
		return "", err
	}
	return ref, nil
}

func (v *FileVault) Resolve(ctx context.Context, ref string) (Secret, error) {
	if err := ctx.Err(); err != nil {
		return Secret{}, err
	}
	// Reload under the write lock so credentials persisted by another E2M Core
	// process become visible without a restart.
	v.mu.Lock()
	if err := v.loadLocked(); err != nil {
		v.mu.Unlock()
		return Secret{}, err
	}
	encoded, ok := v.refs[ref]
	v.mu.Unlock()
	if !ok {
		return Secret{}, ErrNotFound
	}
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return Secret{}, fmt.Errorf("vault: corrupt entry for %s: %w", ref, err)
	}
	ns := v.aead.NonceSize()
	if len(sealed) < ns {
		return Secret{}, fmt.Errorf("vault: corrupt entry for %s: too short", ref)
	}
	plain, err := v.aead.Open(nil, sealed[:ns], sealed[ns:], []byte(ref))
	if err != nil {
		return Secret{}, fmt.Errorf("vault: decrypt %s failed (wrong E2M_VAULT_KEY?): %w", ref, err)
	}
	return Secret{Ref: ref, Value: string(plain)}, nil
}

func (v *FileVault) Delete(ctx context.Context, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	old, ok := v.refs[ref]
	if !ok {
		return ErrNotFound
	}
	delete(v.refs, ref)
	if err := v.persistLocked(); err != nil {
		v.refs[ref] = old
		return err
	}
	return nil
}

func (v *FileVault) ListRefs(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	refs := make([]string, 0, len(v.refs))
	for ref := range v.refs {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs, nil
}

// load reads the vault file; a missing file is an empty vault.
func (v *FileVault) load() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.loadLocked()
}

func (v *FileVault) loadLocked() error {
	raw, err := os.ReadFile(v.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("vault: read %s: %w", v.path, err)
	}
	if len(raw) == 0 {
		return nil
	}
	var refs map[string]string
	if err := json.Unmarshal(raw, &refs); err != nil {
		return fmt.Errorf("vault: parse %s: %w", v.path, err)
	}
	v.refs = refs
	return nil
}

// persistLocked writes the encrypted map atomically (tmp file + rename).
// Caller holds v.mu.
func (v *FileVault) persistLocked() error {
	raw, err := json.MarshalIndent(v.refs, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(v.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("vault: mkdir %s: %w", dir, err)
		}
	}
	tmp := v.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("vault: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, v.path); err != nil {
		// Windows rename-over-existing can fail; fall back to remove+rename.
		if rmErr := os.Remove(v.path); rmErr == nil {
			if err2 := os.Rename(tmp, v.path); err2 == nil {
				return nil
			}
		}
		_ = os.Remove(tmp)
		return fmt.Errorf("vault: rename %s: %w", v.path, err)
	}
	return nil
}
