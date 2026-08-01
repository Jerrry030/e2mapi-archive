package connector

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"e2m.local/contracts"
)

const (
	writeReceiptFileVersion     = 1
	defaultWriteReceiptLimit    = 1024
	maxWriteReceiptKeyBytes     = 512
	maxWriteReceiptResultBytes  = 4 << 10
	maxWriteReceiptLedgerBytes  = 8 << 20
	writeReceiptSignatureLength = sha256.Size * 2
)

var errWriteReceiptConflict = errors.New("idempotency key conflicts with an existing write receipt")

type writeReceiptLedgerFile struct {
	Version int                 `json:"version"`
	Entries []writeReceiptEntry `json:"entries"`
}

type writeReceiptEntry struct {
	Key       string          `json:"key"`
	Signature string          `json:"signature"`
	Result    json.RawMessage `json:"result"`
}

// writeReceiptLedger records successful gateway mutations before Connector
// acknowledges them to Core. The file lock stays held across the gateway side
// effect, preventing two Connector processes sharing a data directory from
// executing the same logical operation concurrently.
type writeReceiptLedger struct {
	mu    sync.Mutex
	path  string
	limit int
}

type writeReceiptLease struct {
	ledger    *writeReceiptLedger
	file      *os.File
	state     writeReceiptLedgerFile
	key       string
	signature string
	once      sync.Once
}

func newWriteReceiptLedger(configStore *LocalConfigStore, limit int) *writeReceiptLedger {
	if limit <= 0 {
		limit = defaultWriteReceiptLimit
	}
	if configStore == nil || strings.TrimSpace(configStore.path) == "" {
		return &writeReceiptLedger{limit: limit}
	}
	return &writeReceiptLedger{
		path:  filepath.Join(filepath.Dir(configStore.path), "write-receipts.json"),
		limit: limit,
	}
}

// begin returns a cached successful result, or a lease that must remain held
// until the caller has executed and recorded the gateway mutation.
func (l *writeReceiptLedger) begin(key, signature string) (cached taskResult, found bool, lease *writeReceiptLease, err error) {
	if l == nil || strings.TrimSpace(l.path) == "" {
		return taskResult{}, false, nil, errors.New("write receipt persistence is not configured")
	}
	key = strings.TrimSpace(key)
	if !validWriteReceiptKey(key) {
		return taskResult{}, false, nil, errors.New("write receipt idempotency key is invalid")
	}
	if !validWriteReceiptSignature(signature) {
		return taskResult{}, false, nil, errors.New("write receipt signature is invalid")
	}

	l.mu.Lock()
	lockFile, openErr := openWriteReceiptLock(l.path + ".lock")
	if openErr != nil {
		l.mu.Unlock()
		return taskResult{}, false, nil, openErr
	}
	held := &writeReceiptLease{ledger: l, file: lockFile, key: key, signature: signature}
	keepLease := false
	defer func() {
		if !keepLease {
			if releaseErr := held.release(); err == nil && releaseErr != nil {
				err = releaseErr
			}
		}
	}()

	state, loadErr := l.loadLocked()
	if loadErr != nil {
		return taskResult{}, false, nil, loadErr
	}
	for _, entry := range state.Entries {
		if entry.Key != key {
			continue
		}
		if entry.Signature != signature {
			return taskResult{}, false, nil, errWriteReceiptConflict
		}
		// Keep the receipt lock across Core's BeginExecution handshake too. A
		// cached write still represents a remote side effect and must not be
		// acknowledged under a stale route-plan generation.
		held.state = state
		keepLease = true
		return taskResult{success: true, result: append(json.RawMessage(nil), entry.Result...)}, true, held, nil
	}
	held.state = state
	keepLease = true
	return taskResult{}, false, held, nil
}

func (l *writeReceiptLease) commit(result taskResult) error {
	if l == nil || l.ledger == nil || l.file == nil {
		return errors.New("write receipt lease is not active")
	}
	if !result.success || result.localErr != nil || strings.TrimSpace(result.taskErr.Code) != "" {
		return errors.New("only a successful gateway mutation can be recorded")
	}
	if !validWriteReceiptResult(result.result) {
		return errors.New("write receipt result is invalid")
	}
	if len(l.state.Entries) >= l.ledger.limit {
		retained := l.ledger.limit - 1
		if retained == 0 {
			l.state.Entries = l.state.Entries[:0]
		} else {
			start := len(l.state.Entries) - retained
			copy(l.state.Entries, l.state.Entries[start:])
			l.state.Entries = l.state.Entries[:retained]
		}
	}
	l.state.Entries = append(l.state.Entries, writeReceiptEntry{
		Key:       l.key,
		Signature: l.signature,
		Result:    append(json.RawMessage(nil), result.result...),
	})
	raw, err := json.Marshal(l.state)
	if err != nil {
		return fmt.Errorf("encode write receipt ledger: %w", err)
	}
	if len(raw) > maxWriteReceiptLedgerBytes {
		return errors.New("write receipt ledger exceeds maximum size")
	}
	if err := atomicWritePrivateFile(l.ledger.path, append(raw, '\n')); err != nil {
		return fmt.Errorf("persist write receipt ledger: %w", err)
	}
	return nil
}

func (l *writeReceiptLease) release() error {
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

func (l *writeReceiptLedger) loadLocked() (writeReceiptLedgerFile, error) {
	state := writeReceiptLedgerFile{Version: writeReceiptFileVersion, Entries: make([]writeReceiptEntry, 0)}
	raw, err := readRegularFileNoSymlink(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read write receipt ledger: %w", err)
	}
	if len(raw) > maxWriteReceiptLedgerBytes {
		return state, errors.New("write receipt ledger exceeds maximum size")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, fmt.Errorf("decode write receipt ledger: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return state, errors.New("decode write receipt ledger: trailing data")
	}
	if state.Version != writeReceiptFileVersion || state.Entries == nil {
		return state, errors.New("write receipt ledger has unsupported format")
	}
	if len(state.Entries) > l.limit {
		return state, errors.New("write receipt ledger exceeds entry limit")
	}
	seen := make(map[string]struct{}, len(state.Entries))
	for _, entry := range state.Entries {
		if entry.Key != strings.TrimSpace(entry.Key) || !validWriteReceiptKey(entry.Key) ||
			!validWriteReceiptSignature(entry.Signature) || !validWriteReceiptResult(entry.Result) {
			return state, errors.New("write receipt ledger contains an invalid entry")
		}
		if _, exists := seen[entry.Key]; exists {
			return state, errors.New("write receipt ledger contains duplicate keys")
		}
		seen[entry.Key] = struct{}{}
	}
	return state, nil
}

func openWriteReceiptLock(path string) (*os.File, error) {
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
	locked := false
	closeOnReturn := true
	defer func() {
		if closeOnReturn {
			if locked {
				_ = unlockSchedulingFenceFile(f)
			}
			_ = f.Close()
		}
	}()
	if err := f.Chmod(0600); err != nil {
		return nil, err
	}
	if err := lockSchedulingFenceFile(f); err != nil {
		return nil, fmt.Errorf("lock write receipt ledger: %w", err)
	}
	locked = true
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(opened, after) {
		return nil, fmt.Errorf("write receipt lock path %q changed while opening", path)
	}
	closeOnReturn = false
	return f, nil
}

func validWriteReceiptKey(key string) bool {
	if key == "" || len(key) > maxWriteReceiptKeyBytes {
		return false
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validWriteReceiptSignature(signature string) bool {
	if len(signature) != writeReceiptSignatureLength {
		return false
	}
	decoded, err := hex.DecodeString(signature)
	return err == nil && len(decoded) == sha256.Size
}

func validWriteReceiptResult(result json.RawMessage) bool {
	trimmed := bytes.TrimSpace(result)
	return len(trimmed) >= 2 && len(trimmed) <= maxWriteReceiptResultBytes &&
		trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' && json.Valid(trimmed)
}

func writeTaskPayloadSignature(task contracts.ConnectorTask) string {
	value := struct {
		ConnectorID   string                      `json:"connector_id"`
		InstanceID    string                      `json:"instance_id"`
		Type          contracts.ConnectorTaskType `json:"type"`
		SchemaVersion int                         `json:"schema_version"`
		Input         json.RawMessage             `json:"input"`
	}{
		ConnectorID:   strings.TrimSpace(task.ConnectorID),
		InstanceID:    strings.TrimSpace(task.InstanceID),
		Type:          task.Type,
		SchemaVersion: task.SchemaVersion,
		Input:         task.Input,
	}
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
