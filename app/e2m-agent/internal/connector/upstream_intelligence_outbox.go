package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"e2m.local/contracts"
)

const (
	legacyUpstreamIntelligenceOutboxVersion = 1
	upstreamIntelligenceOutboxVersion       = 2
	upstreamIntelligenceOutboxFilename      = "upstream-intelligence-outbox.json"
	maxUpstreamIntelligenceOutboxBatches    = 4096
	maxUpstreamIntelligenceOutboxFileBytes  = 64 << 20
)

// UpstreamIntelligenceOutbox persists only sanitized ingest DTOs. Gateway
// URLs, credentials, headers and raw responses are not representable here.
// A complete run is enqueued with one atomic file replacement before any
// upload is attempted.
type UpstreamIntelligenceOutbox struct {
	path     string
	mu       *sync.Mutex
	replayMu *sync.Mutex
	now      func() time.Time
}

type upstreamIntelligenceOutboxEntry struct {
	EnqueuedAt *time.Time                                       `json:"enqueued_at,omitempty"`
	Request    contracts.UpstreamIntelligenceIngestBatchRequest `json:"request"`
}

type upstreamIntelligenceOutboxPayloadV2 struct {
	Version int                               `json:"version"`
	Pending []upstreamIntelligenceOutboxEntry `json:"pending"`
}

type upstreamIntelligenceOutboxFileV2 struct {
	Version  int                               `json:"version"`
	Pending  []upstreamIntelligenceOutboxEntry `json:"pending"`
	Checksum string                            `json:"checksum"`
}

type upstreamIntelligenceOutboxPayloadV1 struct {
	Version int                                                `json:"version"`
	Pending []contracts.UpstreamIntelligenceIngestBatchRequest `json:"pending"`
}

type upstreamIntelligenceOutboxFileV1 struct {
	Version  int                                                `json:"version"`
	Pending  []contracts.UpstreamIntelligenceIngestBatchRequest `json:"pending"`
	Checksum string                                             `json:"checksum"`
}

type upstreamIntelligenceOutboxState struct {
	entries     []upstreamIntelligenceOutboxEntry
	oldestKnown bool
}

// UpstreamIntelligenceOutboxMetrics is an exact snapshot of the durable
// intelligence queue. OldestEnqueuedAt is nil only when a non-empty legacy v1
// queue has no persisted enqueue timestamp. Callers must not infer it from
// observation timestamps or filesystem metadata.
type UpstreamIntelligenceOutboxMetrics struct {
	Depth            int
	OldestEnqueuedAt *time.Time
}

// UpstreamIntelligenceUploadFunc is the authenticated transport boundary.
// Callers normally use Connector.UploadUpstreamIntelligenceBatch; tests may
// inject a deterministic function into Replay.
type UpstreamIntelligenceUploadFunc func(context.Context, contracts.UpstreamIntelligenceIngestBatchRequest) (contracts.UpstreamIntelligenceIngestBatchResponse, error)

type upstreamIntelligenceOutboxLocks struct {
	mu       sync.Mutex
	replayMu sync.Mutex
}

var upstreamIntelligenceOutboxLockMap sync.Map

func newUpstreamIntelligenceOutboxLocks(path string) *upstreamIntelligenceOutboxLocks {
	value, _ := upstreamIntelligenceOutboxLockMap.LoadOrStore(path, &upstreamIntelligenceOutboxLocks{})
	return value.(*upstreamIntelligenceOutboxLocks)
}

func NewUpstreamIntelligenceOutbox(dataDir string) *UpstreamIntelligenceOutbox {
	path := filepath.Join(dataDir, upstreamIntelligenceOutboxFilename)
	locks := newUpstreamIntelligenceOutboxLocks(path)
	return &UpstreamIntelligenceOutbox{
		path: path, mu: &locks.mu, replayMu: &locks.replayMu,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// EnqueueRun atomically appends every batch of one complete run. An exact
// retry is idempotent; reusing run_id + batch_no for different content is a
// conflict. No prefix is persisted when validation or capacity checks fail.
func (o *UpstreamIntelligenceOutbox) EnqueueRun(requests []contracts.UpstreamIntelligenceIngestBatchRequest) (bool, error) {
	if o == nil || o.mu == nil || strings.TrimSpace(o.path) == "" {
		return false, errors.New("upstream intelligence outbox is not configured")
	}
	ordered, err := validateUpstreamIntelligenceOutboxRun(requests)
	if err != nil {
		return false, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	state, err := o.loadLocked()
	if errors.Is(err, os.ErrNotExist) {
		state = upstreamIntelligenceOutboxState{entries: []upstreamIntelligenceOutboxEntry{}, oldestKnown: true}
	} else if err != nil {
		return false, err
	}
	pending := state.entries

	existing := make(map[string]upstreamIntelligenceOutboxEntry, len(pending))
	for _, entry := range pending {
		existing[upstreamIntelligenceOutboxKey(entry.Request)] = entry
	}
	changed := false
	enqueuedAt := o.now().UTC()
	if enqueuedAt.IsZero() {
		return false, errors.New("upstream intelligence outbox clock returned zero time")
	}
	for _, request := range ordered {
		key := upstreamIntelligenceOutboxKey(request)
		if current, ok := existing[key]; ok {
			if current.Request.PayloadHash != request.PayloadHash || current.Request.Manifest.ManifestHash != request.Manifest.ManifestHash {
				return false, fmt.Errorf("upstream intelligence outbox identity conflict for %s", key)
			}
			continue
		}
		entryEnqueuedAt := enqueuedAt
		entry := upstreamIntelligenceOutboxEntry{EnqueuedAt: &entryEnqueuedAt, Request: request}
		pending = append(pending, entry)
		existing[key] = entry
		changed = true
	}
	if !changed {
		return false, nil
	}
	if len(pending) > maxUpstreamIntelligenceOutboxBatches {
		return false, fmt.Errorf("upstream intelligence outbox capacity exceeded: %d pending batches", len(pending))
	}
	sortUpstreamIntelligenceOutboxEntries(pending)
	if err := o.saveLocked(pending); err != nil {
		return false, err
	}
	return true, nil
}

func (o *UpstreamIntelligenceOutbox) List() ([]contracts.UpstreamIntelligenceIngestBatchRequest, error) {
	if o == nil || o.mu == nil || strings.TrimSpace(o.path) == "" {
		return nil, errors.New("upstream intelligence outbox is not configured")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	state, err := o.loadLocked()
	if errors.Is(err, os.ErrNotExist) {
		return []contracts.UpstreamIntelligenceIngestBatchRequest{}, nil
	}
	if err != nil {
		return nil, err
	}
	return upstreamIntelligenceOutboxRequests(state.entries), nil
}

// Metrics reads the same checksummed state used by replay. A missing file is
// an exactly empty queue. A corrupt or unsupported file is an error, never an
// apparent zero. Legacy v1 queues expose exact depth but omit oldest age until
// a new enqueue rewrites them with v2 timestamps.
func (o *UpstreamIntelligenceOutbox) Metrics() (UpstreamIntelligenceOutboxMetrics, error) {
	if o == nil || o.mu == nil || strings.TrimSpace(o.path) == "" {
		return UpstreamIntelligenceOutboxMetrics{}, errors.New("upstream intelligence outbox is not configured")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	state, err := o.loadLocked()
	if errors.Is(err, os.ErrNotExist) {
		return UpstreamIntelligenceOutboxMetrics{Depth: 0}, nil
	}
	if err != nil {
		return UpstreamIntelligenceOutboxMetrics{}, err
	}
	metrics := UpstreamIntelligenceOutboxMetrics{Depth: len(state.entries)}
	if len(state.entries) == 0 {
		return metrics, nil
	}
	if !state.oldestKnown || state.entries[0].EnqueuedAt == nil {
		return metrics, nil
	}
	oldest := state.entries[0].EnqueuedAt.UTC()
	for _, entry := range state.entries[1:] {
		if entry.EnqueuedAt == nil {
			return metrics, nil
		}
		if entry.EnqueuedAt.Before(oldest) {
			oldest = entry.EnqueuedAt.UTC()
		}
	}
	if !oldest.IsZero() {
		metrics.OldestEnqueuedAt = &oldest
	}
	return metrics, nil
}

// Acknowledge removes exactly one Core-confirmed payload. The payload hash is
// part of the acknowledgement fence so a stale uploader cannot remove a
// different request that reused the same logical identity.
func (o *UpstreamIntelligenceOutbox) Acknowledge(runID string, batchNo int, payloadHash string) error {
	if o == nil || o.mu == nil || strings.TrimSpace(o.path) == "" {
		return errors.New("upstream intelligence outbox is not configured")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	state, err := o.loadLocked()
	if err != nil {
		return err
	}
	pending := state.entries
	index := -1
	for i, entry := range pending {
		request := entry.Request
		if request.Run.ID == runID && request.BatchNo == batchNo {
			if request.PayloadHash != payloadHash {
				return errors.New("upstream intelligence outbox acknowledgement hash conflict")
			}
			index = i
			break
		}
	}
	if index < 0 {
		return nil
	}
	pending = append(pending[:index:index], pending[index+1:]...)
	if len(pending) == 0 {
		return o.removeLocked()
	}
	return o.saveLocked(pending)
}

// Replay uploads pending requests in deterministic run/batch order. It stops
// at the first unconfirmed response and leaves that batch and every later batch
// durable for a subsequent retry.
func (o *UpstreamIntelligenceOutbox) Replay(
	ctx context.Context,
	upload UpstreamIntelligenceUploadFunc,
) (int, error) {
	if upload == nil {
		return 0, errors.New("upstream intelligence upload callback is required")
	}
	if o == nil || o.replayMu == nil {
		return 0, errors.New("upstream intelligence outbox is not configured")
	}
	o.replayMu.Lock()
	defer o.replayMu.Unlock()
	pending, err := o.List()
	if err != nil {
		return 0, err
	}
	acknowledged := 0
	for _, request := range pending {
		if err := ctx.Err(); err != nil {
			return acknowledged, err
		}
		response, err := upload(ctx, request)
		if err != nil {
			return acknowledged, err
		}
		factCount := len(request.Wallets) + len(request.Offers)
		if response.Rejected != 0 || response.Accepted < 0 || response.Duplicate < 0 ||
			response.Accepted+response.Duplicate != factCount {
			return acknowledged, errors.New("Core did not confirm the complete upstream intelligence batch")
		}
		if request.BatchNo == request.Manifest.BatchCount-1 && !response.Finalized {
			return acknowledged, errors.New("Core did not finalize the complete upstream intelligence run")
		}
		if err := o.Acknowledge(request.Run.ID, request.BatchNo, request.PayloadHash); err != nil {
			return acknowledged, err
		}
		acknowledged++
	}
	return acknowledged, nil
}

func (c *Connector) UploadUpstreamIntelligenceBatch(ctx context.Context, request contracts.UpstreamIntelligenceIngestBatchRequest) (contracts.UpstreamIntelligenceIngestBatchResponse, error) {
	token := c.connectorToken()
	if token == "" {
		return contracts.UpstreamIntelligenceIngestBatchResponse{}, errors.New("connector token is required for upstream intelligence upload")
	}
	var response contracts.UpstreamIntelligenceIngestBatchResponse
	if err := c.postJSON(ctx, "/api/v1/connectors/upstream-intelligence/snapshots", token, request, &response); err != nil {
		return contracts.UpstreamIntelligenceIngestBatchResponse{}, fmt.Errorf("connector: upload upstream intelligence batch: %w", err)
	}
	if !contracts.IsConnectorUpstreamIntelligenceSourceID(response.SourceID) {
		return contracts.UpstreamIntelligenceIngestBatchResponse{}, errors.New("connector: upstream intelligence response did not include a valid source binding")
	}
	factCount := len(request.Wallets) + len(request.Offers)
	if response.Rejected != 0 || response.Accepted < 0 || response.Duplicate < 0 ||
		response.Accepted+response.Duplicate != factCount ||
		request.BatchNo == request.Manifest.BatchCount-1 && !response.Finalized {
		return contracts.UpstreamIntelligenceIngestBatchResponse{}, errors.New("connector: upstream intelligence response did not confirm the complete batch")
	}
	if c.upstreamIntelligenceSourceBindings == nil {
		return contracts.UpstreamIntelligenceIngestBatchResponse{}, errors.New("connector: upstream intelligence source binding store is not configured")
	}
	if err := c.upstreamIntelligenceSourceBindings.Bind(response.SourceID, request.Source.LocalRef); err != nil {
		return contracts.UpstreamIntelligenceIngestBatchResponse{}, errors.New("connector: persist upstream intelligence source binding")
	}
	return response, nil
}

func (c *Connector) replayUpstreamIntelligenceOutbox(ctx context.Context) error {
	if c == nil || c.upstreamIntelligenceOutbox == nil {
		return nil
	}
	_, err := c.upstreamIntelligenceOutbox.Replay(ctx, c.UploadUpstreamIntelligenceBatch)
	return err
}

func validateUpstreamIntelligenceOutboxRun(requests []contracts.UpstreamIntelligenceIngestBatchRequest) ([]contracts.UpstreamIntelligenceIngestBatchRequest, error) {
	if len(requests) == 0 || len(requests) > maxUpstreamIntelligenceOutboxBatches {
		return nil, errors.New("upstream intelligence outbox run must contain a bounded non-empty batch set")
	}
	ordered := append([]contracts.UpstreamIntelligenceIngestBatchRequest(nil), requests...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].BatchNo < ordered[j].BatchNo })
	first := ordered[0]
	if first.Manifest.BatchCount != len(ordered) || first.Run.BatchCount != len(ordered) {
		return nil, errors.New("upstream intelligence outbox run is incomplete")
	}
	leaves := make([]contracts.UpstreamIntelligenceManifestBatch, len(ordered))
	for index, request := range ordered {
		if request.BatchNo != index || request.Run.ID != first.Run.ID || !sameUpstreamIntelligenceOutboxRun(request.Run, first.Run) || request.Source != first.Source ||
			request.Manifest != first.Manifest {
			return nil, errors.New("upstream intelligence outbox batches do not describe one contiguous run")
		}
		payloadHash, err := contracts.CalculateUpstreamIntelligencePayloadHash(request)
		if err != nil || payloadHash != request.PayloadHash {
			return nil, fmt.Errorf("upstream intelligence outbox batch %d payload hash is invalid", index)
		}
		leaves[index] = contracts.UpstreamIntelligenceManifestBatch{BatchNo: index, PayloadHash: request.PayloadHash}
	}
	manifestHash, err := contracts.CalculateUpstreamIntelligenceManifestHash(leaves)
	if err != nil || manifestHash != first.Manifest.ManifestHash {
		return nil, errors.New("upstream intelligence outbox manifest hash is invalid")
	}
	return ordered, nil
}

func sameUpstreamIntelligenceOutboxRun(left, right contracts.UpstreamIntelligenceIngestRun) bool {
	leftCompleted, rightCompleted := left.CompletedAt, right.CompletedAt
	left.CompletedAt, right.CompletedAt = nil, nil
	if left != right || leftCompleted == nil != (rightCompleted == nil) {
		return false
	}
	return leftCompleted == nil || leftCompleted.Equal(*rightCompleted)
}

func upstreamIntelligenceOutboxKey(request contracts.UpstreamIntelligenceIngestBatchRequest) string {
	return request.Run.ID + "\x00" + fmt.Sprintf("%08d", request.BatchNo)
}

func sortUpstreamIntelligenceOutbox(pending []contracts.UpstreamIntelligenceIngestBatchRequest) {
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].Run.ObservedAt.Equal(pending[j].Run.ObservedAt) {
			if pending[i].Run.ID == pending[j].Run.ID {
				return pending[i].BatchNo < pending[j].BatchNo
			}
			return pending[i].Run.ID < pending[j].Run.ID
		}
		return pending[i].Run.ObservedAt.Before(pending[j].Run.ObservedAt)
	})
}

func sortUpstreamIntelligenceOutboxEntries(pending []upstreamIntelligenceOutboxEntry) {
	sort.Slice(pending, func(i, j int) bool {
		left, right := pending[i].Request, pending[j].Request
		if left.Run.ObservedAt.Equal(right.Run.ObservedAt) {
			if left.Run.ID == right.Run.ID {
				return left.BatchNo < right.BatchNo
			}
			return left.Run.ID < right.Run.ID
		}
		return left.Run.ObservedAt.Before(right.Run.ObservedAt)
	})
}

func upstreamIntelligenceOutboxRequests(entries []upstreamIntelligenceOutboxEntry) []contracts.UpstreamIntelligenceIngestBatchRequest {
	requests := make([]contracts.UpstreamIntelligenceIngestBatchRequest, len(entries))
	for index, entry := range entries {
		requests[index] = entry.Request
	}
	return requests
}

func (o *UpstreamIntelligenceOutbox) loadLocked() (upstreamIntelligenceOutboxState, error) {
	info, err := os.Lstat(o.path)
	if err != nil {
		return upstreamIntelligenceOutboxState{}, err
	}
	if info.Size() > maxUpstreamIntelligenceOutboxFileBytes {
		return upstreamIntelligenceOutboxState{}, errors.New("upstream intelligence outbox exceeds its size limit")
	}
	raw, err := readRegularFileNoSymlink(o.path)
	if err != nil {
		return upstreamIntelligenceOutboxState{}, err
	}
	var envelope struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return upstreamIntelligenceOutboxState{}, fmt.Errorf("decode upstream intelligence outbox: %w", err)
	}
	switch envelope.Version {
	case legacyUpstreamIntelligenceOutboxVersion:
		return loadLegacyUpstreamIntelligenceOutbox(raw)
	case upstreamIntelligenceOutboxVersion:
		return loadCurrentUpstreamIntelligenceOutbox(raw)
	default:
		return upstreamIntelligenceOutboxState{}, errors.New("upstream intelligence outbox version is invalid")
	}
}

func loadCurrentUpstreamIntelligenceOutbox(raw []byte) (upstreamIntelligenceOutboxState, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var stored upstreamIntelligenceOutboxFileV2
	if err := decoder.Decode(&stored); err != nil {
		return upstreamIntelligenceOutboxState{}, fmt.Errorf("decode upstream intelligence outbox: %w", err)
	}
	if err := ensureUpstreamIntelligenceJSONEOF(decoder); err != nil {
		return upstreamIntelligenceOutboxState{}, err
	}
	if stored.Version != upstreamIntelligenceOutboxVersion || len(stored.Pending) > maxUpstreamIntelligenceOutboxBatches {
		return upstreamIntelligenceOutboxState{}, errors.New("upstream intelligence outbox version or capacity is invalid")
	}
	checksum, err := upstreamIntelligenceOutboxChecksumV2(stored.Pending)
	if err != nil || !contracts.IsUpstreamIntelligenceSHA256(stored.Checksum) || checksum != stored.Checksum {
		return upstreamIntelligenceOutboxState{}, errors.New("upstream intelligence outbox checksum mismatch")
	}
	if err := validateUpstreamIntelligenceOutboxEntries(stored.Pending); err != nil {
		return upstreamIntelligenceOutboxState{}, err
	}
	sortUpstreamIntelligenceOutboxEntries(stored.Pending)
	oldestKnown := true
	for _, entry := range stored.Pending {
		if entry.EnqueuedAt == nil {
			oldestKnown = false
			break
		}
	}
	return upstreamIntelligenceOutboxState{entries: stored.Pending, oldestKnown: oldestKnown}, nil
}

func loadLegacyUpstreamIntelligenceOutbox(raw []byte) (upstreamIntelligenceOutboxState, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var stored upstreamIntelligenceOutboxFileV1
	if err := decoder.Decode(&stored); err != nil {
		return upstreamIntelligenceOutboxState{}, fmt.Errorf("decode upstream intelligence outbox: %w", err)
	}
	if err := ensureUpstreamIntelligenceJSONEOF(decoder); err != nil {
		return upstreamIntelligenceOutboxState{}, err
	}
	if stored.Version != legacyUpstreamIntelligenceOutboxVersion || len(stored.Pending) > maxUpstreamIntelligenceOutboxBatches {
		return upstreamIntelligenceOutboxState{}, errors.New("upstream intelligence outbox version or capacity is invalid")
	}
	checksum, err := upstreamIntelligenceOutboxChecksumV1(stored.Pending)
	if err != nil || !contracts.IsUpstreamIntelligenceSHA256(stored.Checksum) || checksum != stored.Checksum {
		return upstreamIntelligenceOutboxState{}, errors.New("upstream intelligence outbox checksum mismatch")
	}
	entries := make([]upstreamIntelligenceOutboxEntry, len(stored.Pending))
	for index, request := range stored.Pending {
		entries[index].Request = request
	}
	if err := validateUpstreamIntelligenceOutboxEntries(entries); err != nil {
		return upstreamIntelligenceOutboxState{}, err
	}
	sortUpstreamIntelligenceOutboxEntries(entries)
	return upstreamIntelligenceOutboxState{entries: entries, oldestKnown: len(entries) == 0}, nil
}

func validateUpstreamIntelligenceOutboxEntries(entries []upstreamIntelligenceOutboxEntry) error {
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		request := entry.Request
		if entry.EnqueuedAt != nil && entry.EnqueuedAt.IsZero() {
			return errors.New("upstream intelligence outbox contains an invalid enqueue time")
		}
		key := upstreamIntelligenceOutboxKey(request)
		if _, duplicate := seen[key]; duplicate {
			return errors.New("upstream intelligence outbox contains a duplicate identity")
		}
		seen[key] = struct{}{}
		payloadHash, hashErr := contracts.CalculateUpstreamIntelligencePayloadHash(request)
		if hashErr != nil || payloadHash != request.PayloadHash {
			return errors.New("upstream intelligence outbox contains an invalid payload")
		}
	}
	return nil
}

func (o *UpstreamIntelligenceOutbox) saveLocked(pending []upstreamIntelligenceOutboxEntry) error {
	if pending == nil {
		pending = []upstreamIntelligenceOutboxEntry{}
	}
	checksum, err := upstreamIntelligenceOutboxChecksumV2(pending)
	if err != nil {
		return err
	}
	stored := upstreamIntelligenceOutboxFileV2{Version: upstreamIntelligenceOutboxVersion, Pending: pending, Checksum: checksum}
	raw, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	if len(raw)+1 > maxUpstreamIntelligenceOutboxFileBytes {
		return errors.New("upstream intelligence outbox file capacity exceeded")
	}
	return atomicWritePrivateFile(o.path, append(raw, '\n'))
}

func (o *UpstreamIntelligenceOutbox) removeLocked() error {
	info, err := os.Lstat(o.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("upstream intelligence outbox is not a regular file")
	}
	return os.Remove(o.path)
}

func upstreamIntelligenceOutboxChecksumV2(pending []upstreamIntelligenceOutboxEntry) (string, error) {
	if pending == nil {
		pending = []upstreamIntelligenceOutboxEntry{}
	}
	payload := upstreamIntelligenceOutboxPayloadV2{Version: upstreamIntelligenceOutboxVersion, Pending: pending}
	return upstreamIntelligenceOutboxChecksum(payload)
}

func upstreamIntelligenceOutboxChecksumV1(pending []contracts.UpstreamIntelligenceIngestBatchRequest) (string, error) {
	if pending == nil {
		pending = []contracts.UpstreamIntelligenceIngestBatchRequest{}
	}
	payload := upstreamIntelligenceOutboxPayloadV1{Version: legacyUpstreamIntelligenceOutboxVersion, Pending: pending}
	return upstreamIntelligenceOutboxChecksum(payload)
}

func upstreamIntelligenceOutboxChecksum(payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func ensureUpstreamIntelligenceJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("upstream intelligence outbox must contain one JSON value")
	}
	return nil
}
