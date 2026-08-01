package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	DefaultUpstreamIntelligenceIngestWindow    = time.Minute
	DefaultUpstreamIntelligenceOwnerBatchQuota = 120
	DefaultUpstreamIntelligenceOwnerFactQuota  = 60_000
	MaxUpstreamIntelligenceOwnerBatchQuota     = 1_000_000
	MaxUpstreamIntelligenceOwnerFactQuota      = 500_000_000
)

var ErrUpstreamIntelligenceIngestQuotaExceeded = errors.New("store: upstream intelligence ingest quota exceeded")

type UpstreamIntelligenceIngestCapacityLimit struct {
	Window     time.Duration
	MaxBatches int
	MaxFacts   int
}

type UpstreamIntelligenceIngestCapacityRequest struct {
	UserID      int64
	RunID       string
	BatchNo     int
	PayloadHash string
	FactCount   int
	Limit       UpstreamIntelligenceIngestCapacityLimit
}

type UpstreamIntelligenceIngestCapacityResult struct {
	WindowStart time.Time
	WindowEnd   time.Time
	BatchesUsed int
	FactsUsed   int
	Admitted    bool
	Replay      bool
}

type UpstreamIntelligenceIngestCapacityStore interface {
	AdmitUpstreamIntelligenceIngest(context.Context, UpstreamIntelligenceIngestCapacityRequest) (UpstreamIntelligenceIngestCapacityResult, error)
}

func AsUpstreamIntelligenceIngestCapacityStore(st Store) (UpstreamIntelligenceIngestCapacityStore, bool) {
	capacity, ok := st.(UpstreamIntelligenceIngestCapacityStore)
	return capacity, ok
}

func NormalizeUpstreamIntelligenceIngestCapacityLimit(limit UpstreamIntelligenceIngestCapacityLimit) UpstreamIntelligenceIngestCapacityLimit {
	if limit.Window < time.Second || limit.Window > 24*time.Hour {
		limit.Window = DefaultUpstreamIntelligenceIngestWindow
	}
	if limit.MaxBatches <= 0 || limit.MaxBatches > MaxUpstreamIntelligenceOwnerBatchQuota {
		limit.MaxBatches = DefaultUpstreamIntelligenceOwnerBatchQuota
	}
	if limit.MaxFacts <= 0 || limit.MaxFacts > MaxUpstreamIntelligenceOwnerFactQuota {
		limit.MaxFacts = DefaultUpstreamIntelligenceOwnerFactQuota
	}
	return limit
}

func validUpstreamIntelligenceIngestCapacityRequest(input UpstreamIntelligenceIngestCapacityRequest) bool {
	return input.UserID > 0 && strings.TrimSpace(input.RunID) != "" && input.BatchNo >= 0 &&
		input.FactCount >= 0 && input.FactCount <= MaxUpstreamIntelligenceOwnerFactQuota &&
		isLowerHexSHA256(input.PayloadHash)
}

func isLowerHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

type upstreamIntelligenceIngestCapacityWindowKey struct {
	UserID      int64
	WindowStart time.Time
	Window      time.Duration
}

type upstreamIntelligenceIngestCapacityIdempotencyKey struct {
	RunID       string
	BatchNo     int
	PayloadHash string
}

type upstreamIntelligenceIngestCapacityWindow struct {
	Batches int
	Facts   int
	Keys    map[upstreamIntelligenceIngestCapacityIdempotencyKey]struct{}
}

func (s *MemoryStore) AdmitUpstreamIntelligenceIngest(ctx context.Context, input UpstreamIntelligenceIngestCapacityRequest) (UpstreamIntelligenceIngestCapacityResult, error) {
	if err := ctx.Err(); err != nil {
		return UpstreamIntelligenceIngestCapacityResult{}, err
	}
	input.Limit = NormalizeUpstreamIntelligenceIngestCapacityLimit(input.Limit)
	if !validUpstreamIntelligenceIngestCapacityRequest(input) {
		return UpstreamIntelligenceIngestCapacityResult{}, ErrInvalid
	}
	now := normalizeUpstreamTime(s.now())
	windowStart := now.Truncate(input.Limit.Window)
	result := UpstreamIntelligenceIngestCapacityResult{WindowStart: windowStart, WindowEnd: windowStart.Add(input.Limit.Window)}
	key := upstreamIntelligenceIngestCapacityWindowKey{UserID: input.UserID, WindowStart: windowStart, Window: input.Limit.Window}
	idempotency := upstreamIntelligenceIngestCapacityIdempotencyKey{RunID: input.RunID, BatchNo: input.BatchNo, PayloadHash: input.PayloadHash}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upstreamIntelIngestCapacity == nil {
		s.upstreamIntelIngestCapacity = make(map[upstreamIntelligenceIngestCapacityWindowKey]*upstreamIntelligenceIngestCapacityWindow)
	}
	// Capacity keys are useful only until their fixed window expires. Durable
	// ingest receipts provide cross-window replay protection, so expired
	// in-memory windows can be removed without changing idempotency semantics.
	for existingKey := range s.upstreamIntelIngestCapacity {
		if !existingKey.WindowStart.Add(existingKey.Window).After(now) {
			delete(s.upstreamIntelIngestCapacity, existingKey)
		}
	}
	// A durable receipt means this exact batch was already accepted in a prior
	// window. Replays remain free across window boundaries as well as within one
	// window; a changed payload hash still receives a new admission attempt and
	// is rejected by the ingest idempotency conflict after admission.
	for _, batch := range s.upstreamIntelBatches {
		if batch.UserID == input.UserID && batch.RunID == input.RunID && batch.BatchNo == input.BatchNo && batch.PayloadHash == input.PayloadHash {
			result.Admitted, result.Replay = true, true
			if window := s.upstreamIntelIngestCapacity[key]; window != nil {
				result.BatchesUsed, result.FactsUsed = window.Batches, window.Facts
			}
			return result, nil
		}
	}
	window := s.upstreamIntelIngestCapacity[key]
	if window == nil {
		window = &upstreamIntelligenceIngestCapacityWindow{Keys: make(map[upstreamIntelligenceIngestCapacityIdempotencyKey]struct{})}
		s.upstreamIntelIngestCapacity[key] = window
	}
	if _, replay := window.Keys[idempotency]; replay {
		result.BatchesUsed, result.FactsUsed, result.Admitted, result.Replay = window.Batches, window.Facts, true, true
		return result, nil
	}
	if window.Batches+1 > input.Limit.MaxBatches || window.Facts+input.FactCount > input.Limit.MaxFacts {
		result.BatchesUsed, result.FactsUsed = window.Batches, window.Facts
		return result, ErrUpstreamIntelligenceIngestQuotaExceeded
	}
	window.Batches++
	window.Facts += input.FactCount
	window.Keys[idempotency] = struct{}{}
	result.BatchesUsed, result.FactsUsed, result.Admitted = window.Batches, window.Facts, true
	return result, nil
}

var (
	_ UpstreamIntelligenceIngestCapacityStore = (*MemoryStore)(nil)
	_ UpstreamIntelligenceIngestCapacityStore = (*PostgresStore)(nil)
)
