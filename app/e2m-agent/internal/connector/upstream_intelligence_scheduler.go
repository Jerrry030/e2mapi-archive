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
	UpstreamIntelligenceSchedulerFilename = "upstream-intelligence-scheduler.json"

	upstreamIntelligenceSchedulerVersion       = 1
	maxUpstreamIntelligenceSchedulerFileBytes  = 1 << 20
	maxUpstreamIntelligenceSchedulerStates     = maxUpstreamIntelligenceSourceRecords
	upstreamIntelligenceRetryBase              = 30 * time.Second
	upstreamIntelligenceRetryLimit             = 30 * time.Minute
	upstreamIntelligenceNonRetryMinimum        = time.Hour
	upstreamIntelligenceNonRetryMaximum        = 24 * time.Hour
	upstreamIntelligenceMaximumJitter          = 30 * time.Second
	UpstreamIntelligenceScheduleOutboxCapacity = "outbox_capacity"
	UpstreamIntelligenceScheduleCollectFailed  = "collect_failed"
	UpstreamIntelligenceScheduleEnqueueFailed  = "enqueue_failed"
)

type UpstreamIntelligenceSchedulerSourceStore interface {
	List() ([]UpstreamIntelligenceSource, error)
}

type UpstreamIntelligenceSchedulerCollector interface {
	Collect(context.Context, UpstreamIntelligenceSource, contracts.UpstreamCollectionTrigger) ([]contracts.UpstreamIntelligenceIngestBatchRequest, UpstreamIntelligenceCollectionSummary, error)
}

type UpstreamIntelligenceSchedulerOutbox interface {
	List() ([]contracts.UpstreamIntelligenceIngestBatchRequest, error)
	EnqueueRun([]contracts.UpstreamIntelligenceIngestBatchRequest) (bool, error)
}

type UpstreamIntelligenceSchedulerClock func() time.Time
type UpstreamIntelligenceSchedulerJitter func(time.Duration) time.Duration

type UpstreamIntelligenceSchedulerConfig struct {
	DataDir     string
	SourceStore UpstreamIntelligenceSchedulerSourceStore
	Collector   UpstreamIntelligenceSchedulerCollector
	Outbox      UpstreamIntelligenceSchedulerOutbox
	Clock       UpstreamIntelligenceSchedulerClock
	Jitter      UpstreamIntelligenceSchedulerJitter
}

// UpstreamIntelligenceScheduleState is Connector-private operational state.
// It deliberately contains only an opaque local source reference and bounded
// scheduling metadata, never a gateway URL, credential, header, or response.
type UpstreamIntelligenceScheduleState struct {
	LocalRef            string     `json:"local_ref"`
	NextAttemptAt       time.Time  `json:"next_attempt_at"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastAttemptAt       *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastErrorCode       string     `json:"last_error_code,omitempty"`
}

type UpstreamIntelligenceScheduleResult struct {
	LocalRef      string                             `json:"local_ref"`
	Attempted     bool                               `json:"attempted"`
	Queued        bool                               `json:"queued"`
	RunID         string                             `json:"run_id,omitempty"`
	Status        contracts.UpstreamCollectionStatus `json:"status,omitempty"`
	FactCount     int                                `json:"fact_count"`
	BatchCount    int                                `json:"batch_count"`
	ErrorCode     string                             `json:"error_code,omitempty"`
	NextAttemptAt time.Time                          `json:"next_attempt_at"`
}

type UpstreamIntelligenceScheduleReport struct {
	Results []UpstreamIntelligenceScheduleResult `json:"results"`
}

type upstreamIntelligenceSchedulerPayload struct {
	Version int                                 `json:"version"`
	States  []UpstreamIntelligenceScheduleState `json:"states"`
}

type upstreamIntelligenceSchedulerFile struct {
	Version  int                                 `json:"version"`
	States   []UpstreamIntelligenceScheduleState `json:"states"`
	Checksum string                              `json:"checksum"`
}

type UpstreamIntelligenceScheduler struct {
	path        string
	sourceStore UpstreamIntelligenceSchedulerSourceStore
	collector   UpstreamIntelligenceSchedulerCollector
	outbox      UpstreamIntelligenceSchedulerOutbox
	clock       UpstreamIntelligenceSchedulerClock
	jitter      UpstreamIntelligenceSchedulerJitter
	runMu       *sync.Mutex
}

var upstreamIntelligenceSchedulerLocks sync.Map

func upstreamIntelligenceSchedulerLock(path string) *sync.Mutex {
	value, _ := upstreamIntelligenceSchedulerLocks.LoadOrStore(path, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func NewUpstreamIntelligenceScheduler(config UpstreamIntelligenceSchedulerConfig) *UpstreamIntelligenceScheduler {
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	jitter := config.Jitter
	if jitter == nil {
		jitter = randomBackoffJitter
	}
	path := filepath.Join(config.DataDir, UpstreamIntelligenceSchedulerFilename)
	return &UpstreamIntelligenceScheduler{
		path: path, sourceStore: config.SourceStore, collector: config.Collector,
		outbox: config.Outbox, clock: clock, jitter: jitter,
		runMu: upstreamIntelligenceSchedulerLock(path),
	}
}

// RunOnce polls every active source that is due. Calls sharing the same data
// directory serialize, so overlapping scheduled ticks (or a manual caller
// using this same entry point) observe the persisted next-attempt fence and do
// not collect a source twice. Collection is always enqueued before RunOnce
// reports success; Core upload is intentionally outside this scheduler.
func (scheduler *UpstreamIntelligenceScheduler) RunOnce(ctx context.Context) (UpstreamIntelligenceScheduleReport, error) {
	var report UpstreamIntelligenceScheduleReport
	if scheduler == nil || scheduler.sourceStore == nil || scheduler.collector == nil || scheduler.outbox == nil ||
		scheduler.clock == nil || scheduler.jitter == nil || scheduler.runMu == nil || strings.TrimSpace(scheduler.path) == "" {
		return report, errors.New("upstream intelligence scheduler is not configured")
	}
	scheduler.runMu.Lock()
	defer scheduler.runMu.Unlock()

	sources, err := scheduler.sourceStore.List()
	if err != nil {
		return report, errors.New("list upstream intelligence sources")
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].LocalRef < sources[j].LocalRef })
	states, err := scheduler.load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return report, err
	}
	stateByRef := make(map[string]UpstreamIntelligenceScheduleState, len(states))
	for _, state := range states {
		stateByRef[state.LocalRef] = state
	}

	active := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if source.Status != UpstreamIntelligenceSourceActive || source.TombstonedAt != nil {
			continue
		}
		active[source.LocalRef] = struct{}{}
		if err := ctx.Err(); err != nil {
			return report, err
		}
		now := canonicalUpstreamIntelligenceTime(scheduler.clock())
		state := stateByRef[source.LocalRef]
		state.LocalRef = source.LocalRef
		if !state.NextAttemptAt.IsZero() && now.Before(state.NextAttemptAt) {
			report.Results = append(report.Results, scheduleResultFromState(state, false))
			continue
		}

		result := scheduler.collectOne(ctx, source, now, &state)
		stateByRef[source.LocalRef] = state
		report.Results = append(report.Results, result)
		if err := scheduler.save(activeScheduleStates(stateByRef, active)); err != nil {
			return report, err
		}
	}
	// Also prune state belonging to paused/tombstoned/removed sources.
	if err := scheduler.save(activeScheduleStates(stateByRef, active)); err != nil {
		return report, err
	}
	return report, nil
}

func (scheduler *UpstreamIntelligenceScheduler) collectOne(ctx context.Context, source UpstreamIntelligenceSource, now time.Time, state *UpstreamIntelligenceScheduleState) UpstreamIntelligenceScheduleResult {
	state.LastAttemptAt = upstreamIntelligenceScheduleTimePointer(now)
	result := UpstreamIntelligenceScheduleResult{LocalRef: source.LocalRef, Attempted: true}
	pending, err := scheduler.outbox.List()
	if err != nil {
		scheduler.fail(state, source, now, UpstreamIntelligenceScheduleEnqueueFailed, true)
		return finishScheduleResult(result, *state, UpstreamIntelligenceScheduleEnqueueFailed)
	}
	if len(pending) >= maxUpstreamIntelligenceOutboxBatches {
		scheduler.fail(state, source, now, UpstreamIntelligenceScheduleOutboxCapacity, true)
		return finishScheduleResult(result, *state, UpstreamIntelligenceScheduleOutboxCapacity)
	}

	batches, summary, collectErr := scheduler.collector.Collect(ctx, source, contracts.UpstreamCollectionScheduled)
	result.RunID, result.Status = summary.RunID, summary.Status
	result.FactCount, result.BatchCount = summary.FactCount, summary.BatchCount
	if collectErr != nil {
		code := safeUpstreamIntelligenceScheduleError(summary.ErrorCode, UpstreamIntelligenceScheduleCollectFailed)
		scheduler.fail(state, source, now, code, summary.Retryable)
		return finishScheduleResult(result, *state, code)
	}
	if len(pending)+len(batches) > maxUpstreamIntelligenceOutboxBatches {
		scheduler.fail(state, source, now, UpstreamIntelligenceScheduleOutboxCapacity, true)
		return finishScheduleResult(result, *state, UpstreamIntelligenceScheduleOutboxCapacity)
	}
	if _, err := scheduler.outbox.EnqueueRun(batches); err != nil {
		scheduler.fail(state, source, now, UpstreamIntelligenceScheduleEnqueueFailed, true)
		return finishScheduleResult(result, *state, UpstreamIntelligenceScheduleEnqueueFailed)
	}
	result.Queued = true
	if summary.Status == contracts.UpstreamCollectionFailed {
		code := safeUpstreamIntelligenceScheduleError(summary.ErrorCode, contracts.UpstreamCollectionErrorUpstreamUnavailable)
		scheduler.fail(state, source, now, code, summary.Retryable)
		return finishScheduleResult(result, *state, code)
	}

	state.ConsecutiveFailures = 0
	state.LastErrorCode = ""
	if summary.Status == contracts.UpstreamCollectionSucceeded {
		state.LastSuccessAt = upstreamIntelligenceScheduleTimePointer(now)
	}
	interval := time.Duration(source.PollIntervalSeconds) * time.Second
	state.NextAttemptAt = now.Add(interval + scheduler.boundedJitter(scheduleJitterLimit(interval)))
	return finishScheduleResult(result, *state, "")
}

func (scheduler *UpstreamIntelligenceScheduler) fail(state *UpstreamIntelligenceScheduleState, source UpstreamIntelligenceSource, now time.Time, code string, retryable bool) {
	state.ConsecutiveFailures++
	state.LastErrorCode = safeUpstreamIntelligenceScheduleError(code, UpstreamIntelligenceScheduleCollectFailed)
	delay := nonRetryDelay(source)
	if retryable || code == contracts.UpstreamCollectionErrorRateLimited || code == contracts.UpstreamCollectionErrorUpstreamUnavailable {
		delay = retryDelay(state.ConsecutiveFailures)
	}
	state.NextAttemptAt = now.Add(delay + scheduler.boundedJitter(scheduleJitterLimit(delay)))
}

func retryDelay(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	delay := upstreamIntelligenceRetryBase
	for attempt := 1; attempt < failures && delay < upstreamIntelligenceRetryLimit; attempt++ {
		if delay > upstreamIntelligenceRetryLimit/2 {
			return upstreamIntelligenceRetryLimit
		}
		delay *= 2
	}
	if delay > upstreamIntelligenceRetryLimit {
		return upstreamIntelligenceRetryLimit
	}
	return delay
}

func nonRetryDelay(source UpstreamIntelligenceSource) time.Duration {
	delay := 4 * time.Duration(source.PollIntervalSeconds) * time.Second
	if delay < upstreamIntelligenceNonRetryMinimum {
		return upstreamIntelligenceNonRetryMinimum
	}
	if delay > upstreamIntelligenceNonRetryMaximum {
		return upstreamIntelligenceNonRetryMaximum
	}
	return delay
}

func scheduleJitterLimit(delay time.Duration) time.Duration {
	limit := delay / 10
	if limit > upstreamIntelligenceMaximumJitter {
		return upstreamIntelligenceMaximumJitter
	}
	if limit < 0 {
		return 0
	}
	return limit
}

func (scheduler *UpstreamIntelligenceScheduler) boundedJitter(limit time.Duration) time.Duration {
	value := scheduler.jitter(limit)
	if value < 0 {
		return 0
	}
	if value > limit {
		return limit
	}
	return value
}

func scheduleResultFromState(state UpstreamIntelligenceScheduleState, attempted bool) UpstreamIntelligenceScheduleResult {
	return UpstreamIntelligenceScheduleResult{
		LocalRef: state.LocalRef, Attempted: attempted, ErrorCode: state.LastErrorCode,
		NextAttemptAt: state.NextAttemptAt,
	}
}

func finishScheduleResult(result UpstreamIntelligenceScheduleResult, state UpstreamIntelligenceScheduleState, code string) UpstreamIntelligenceScheduleResult {
	result.ErrorCode = code
	result.NextAttemptAt = state.NextAttemptAt
	return result
}

func safeUpstreamIntelligenceScheduleError(value, fallback string) string {
	switch value {
	case contracts.UpstreamCollectionErrorAuthFailed,
		contracts.UpstreamCollectionErrorRateLimited,
		contracts.UpstreamCollectionErrorSchemaUnsupported,
		contracts.UpstreamCollectionErrorResponseTooLarge,
		contracts.UpstreamCollectionErrorUpstreamUnavailable,
		UpstreamIntelligenceScheduleOutboxCapacity,
		UpstreamIntelligenceScheduleCollectFailed,
		UpstreamIntelligenceScheduleEnqueueFailed:
		return value
	default:
		return fallback
	}
}

func upstreamIntelligenceScheduleTimePointer(value time.Time) *time.Time {
	copyValue := value
	return &copyValue
}

func activeScheduleStates(byRef map[string]UpstreamIntelligenceScheduleState, active map[string]struct{}) []UpstreamIntelligenceScheduleState {
	states := make([]UpstreamIntelligenceScheduleState, 0, len(active))
	for localRef := range active {
		state, ok := byRef[localRef]
		if !ok || state.NextAttemptAt.IsZero() {
			continue
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].LocalRef < states[j].LocalRef })
	return states
}

func (scheduler *UpstreamIntelligenceScheduler) load() ([]UpstreamIntelligenceScheduleState, error) {
	raw, err := readRegularFileNoSymlink(scheduler.path)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxUpstreamIntelligenceSchedulerFileBytes {
		return nil, errors.New("upstream intelligence scheduler file exceeds its size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var stored upstreamIntelligenceSchedulerFile
	if err := decoder.Decode(&stored); err != nil {
		return nil, errors.New("decode upstream intelligence scheduler state")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("upstream intelligence scheduler state must contain one JSON value")
	}
	if stored.Version != upstreamIntelligenceSchedulerVersion || len(stored.States) > maxUpstreamIntelligenceSchedulerStates {
		return nil, errors.New("upstream intelligence scheduler version or capacity is invalid")
	}
	want, err := upstreamIntelligenceSchedulerChecksum(stored.States)
	if err != nil || want != stored.Checksum {
		return nil, errors.New("upstream intelligence scheduler checksum mismatch")
	}
	seen := make(map[string]struct{}, len(stored.States))
	for index := range stored.States {
		state := &stored.States[index]
		state.LocalRef = strings.TrimSpace(state.LocalRef)
		state.NextAttemptAt = canonicalUpstreamIntelligenceTime(state.NextAttemptAt)
		if !validUpstreamIntelligenceLocalRef(state.LocalRef) || state.NextAttemptAt.IsZero() ||
			state.ConsecutiveFailures < 0 || state.ConsecutiveFailures > 1_000_000 ||
			safeUpstreamIntelligenceScheduleError(state.LastErrorCode, "") != state.LastErrorCode {
			return nil, errors.New("upstream intelligence scheduler state is invalid")
		}
		if _, duplicate := seen[state.LocalRef]; duplicate {
			return nil, errors.New("upstream intelligence scheduler state contains a duplicate source")
		}
		seen[state.LocalRef] = struct{}{}
	}
	return stored.States, nil
}

func (scheduler *UpstreamIntelligenceScheduler) save(states []UpstreamIntelligenceScheduleState) error {
	if len(states) > maxUpstreamIntelligenceSchedulerStates {
		return errors.New("upstream intelligence scheduler state capacity exceeded")
	}
	checksum, err := upstreamIntelligenceSchedulerChecksum(states)
	if err != nil {
		return err
	}
	stored := upstreamIntelligenceSchedulerFile{Version: upstreamIntelligenceSchedulerVersion, States: states, Checksum: checksum}
	raw, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	if len(raw)+1 > maxUpstreamIntelligenceSchedulerFileBytes {
		return errors.New("upstream intelligence scheduler file exceeds its size limit")
	}
	return atomicWritePrivateFile(scheduler.path, append(raw, '\n'))
}

func upstreamIntelligenceSchedulerChecksum(states []UpstreamIntelligenceScheduleState) (string, error) {
	if states == nil {
		states = []UpstreamIntelligenceScheduleState{}
	}
	raw, err := json.Marshal(upstreamIntelligenceSchedulerPayload{Version: upstreamIntelligenceSchedulerVersion, States: states})
	if err != nil {
		return "", fmt.Errorf("encode upstream intelligence scheduler checksum: %w", err)
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
