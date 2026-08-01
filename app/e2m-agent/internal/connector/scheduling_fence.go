package connector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"e2m.local/contracts"
)

const schedulingFenceFileVersion = 1

var errSchedulingFenceConflict = errors.New("scheduling fence tuple conflicts with another intent")

type schedulingFenceLedgerFile struct {
	Version int                                 `json:"version"`
	Scopes  map[string]schedulingFenceWatermark `json:"scopes"`
}

type schedulingFenceWatermark struct {
	Version    int64  `json:"version"`
	Sequence   int64  `json:"sequence"`
	IntentHash string `json:"intent_hash"`
}

// schedulingFenceLedger persists the highest scheduler generation observed
// for each ordering scope. Advancing the watermark happens before the gateway
// side effect, so a task from an older owner cannot execute after takeover.
type schedulingFenceLedger struct {
	mu   sync.Mutex
	path string
}

type schedulingFenceLease struct {
	ledger *schedulingFenceLedger
	file   *os.File
	once   sync.Once
}

func newSchedulingFenceLedger(configStore *LocalConfigStore) *schedulingFenceLedger {
	if configStore == nil || strings.TrimSpace(configStore.path) == "" {
		return &schedulingFenceLedger{}
	}
	return &schedulingFenceLedger{path: filepath.Join(filepath.Dir(configStore.path), "scheduling-fences.json")}
}

func (l *schedulingFenceLedger) begin(fence contracts.GatewaySchedulingFence, intent []byte) (bool, *schedulingFenceLease, error) {
	if l == nil || strings.TrimSpace(l.path) == "" {
		return false, nil, errors.New("scheduling fence persistence is not configured")
	}
	scope := strings.TrimSpace(fence.Scope)
	if scope == "" || fence.Version <= 0 || fence.Sequence <= 0 {
		return false, nil, errors.New("scheduling fence is invalid")
	}
	sum := sha256.Sum256(intent)
	intentHash := hex.EncodeToString(sum[:])

	l.mu.Lock()
	lockFile, err := openSchedulingFenceLock(l.path + ".lock")
	if err != nil {
		l.mu.Unlock()
		return false, nil, err
	}
	lease := &schedulingFenceLease{ledger: l, file: lockFile}
	releaseOnReturn := true
	defer func() {
		if releaseOnReturn {
			_ = lease.release()
		}
	}()

	state, err := l.loadLocked()
	if err != nil {
		return false, nil, err
	}
	highest := state.Scopes[scope]
	if fence.Version < highest.Version || fence.Version == highest.Version && fence.Sequence < highest.Sequence {
		return false, nil, nil
	}
	if fence.Version == highest.Version && fence.Sequence == highest.Sequence {
		if highest.IntentHash != intentHash {
			return false, nil, errSchedulingFenceConflict
		}
		releaseOnReturn = false
		return true, lease, nil
	}
	state.Scopes[scope] = schedulingFenceWatermark{
		Version: fence.Version, Sequence: fence.Sequence, IntentHash: intentHash,
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return false, nil, fmt.Errorf("encode scheduling fence ledger: %w", err)
	}
	if err := atomicWritePrivateFile(l.path, append(raw, '\n')); err != nil {
		return false, nil, fmt.Errorf("persist scheduling fence ledger: %w", err)
	}
	releaseOnReturn = false
	return true, lease, nil
}

// release is deferred until after the gateway setter returns. Keeping the
// file lock across the side effect serializes two Connector processes sharing
// one data directory, so a lower tuple cannot finish after a higher tuple.
func (l *schedulingFenceLease) release() error {
	if l == nil {
		return nil
	}
	var releaseErr error
	l.once.Do(func() {
		releaseErr = unlockSchedulingFenceFile(l.file)
		if closeErr := l.file.Close(); releaseErr == nil {
			releaseErr = closeErr
		}
		l.ledger.mu.Unlock()
	})
	return releaseErr
}

func openSchedulingFenceLock(path string) (*os.File, error) {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := rejectSymlinkTarget(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	closeOnReturn := true
	defer func() {
		if closeOnReturn {
			_ = f.Close()
		}
	}()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(opened, after) {
		return nil, fmt.Errorf("scheduling fence lock path %q changed while opening", path)
	}
	if err := f.Chmod(0600); err != nil {
		return nil, err
	}
	if err := lockSchedulingFenceFile(f); err != nil {
		return nil, fmt.Errorf("lock scheduling fence ledger: %w", err)
	}
	closeOnReturn = false
	return f, nil
}

func (l *schedulingFenceLedger) loadLocked() (schedulingFenceLedgerFile, error) {
	state := schedulingFenceLedgerFile{Version: schedulingFenceFileVersion, Scopes: make(map[string]schedulingFenceWatermark)}
	raw, err := readRegularFileNoSymlink(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read scheduling fence ledger: %w", err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("decode scheduling fence ledger: %w", err)
	}
	if state.Version != schedulingFenceFileVersion || state.Scopes == nil {
		return state, errors.New("scheduling fence ledger has unsupported format")
	}
	for scope, watermark := range state.Scopes {
		if strings.TrimSpace(scope) == "" || watermark.Version <= 0 || watermark.Sequence <= 0 || len(watermark.IntentHash) != sha256.Size*2 {
			return state, errors.New("scheduling fence ledger contains invalid watermark")
		}
	}
	return state, nil
}
