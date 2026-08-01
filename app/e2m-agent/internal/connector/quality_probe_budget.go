package connector

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"e2m.local/agent/internal/adapters/gateways"
)

type qualityProbeBudgetRecord struct {
	StartedAt time.Time   `json:"started_at"`
	LastAt    time.Time   `json:"last_at,omitempty"`
	Requests  []time.Time `json:"requests"`
}

type qualityProbeBudget struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

func newQualityProbeBudget(store *LocalConfigStore) *qualityProbeBudget {
	path := ""
	if store != nil && strings.TrimSpace(store.path) != "" {
		path = filepath.Join(filepath.Dir(store.path), "quality-probe-budget.json")
	}
	return &qualityProbeBudget{path: path, now: func() time.Time { return time.Now().UTC() }}
}

func (b *qualityProbeBudget) Acquire(settings LocalQualityProbeSettings) error {
	if !settings.Enabled {
		return &gateways.Error{Code: "quality_probe_disabled", Message: "quality probes require explicit local opt-in"}
	}
	if b == nil || strings.TrimSpace(b.path) == "" {
		return &gateways.Error{Code: "quality_probe_disabled", Message: "quality probe budget persistence is unavailable"}
	}
	now := time.Now().UTC()
	if b.now != nil {
		now = b.now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	record, err := b.load()
	if err != nil {
		return &gateways.Error{Code: "quality_probe_rate_limited", Message: "quality probe budget could not be verified", Retryable: true}
	}
	cutoff := now.Add(-time.Hour)
	kept := record.Requests[:0]
	for _, at := range record.Requests {
		if at.After(cutoff) && !at.After(now) {
			kept = append(kept, at.UTC())
		}
	}
	record.Requests = kept
	if !record.LastAt.IsZero() && now.Sub(record.LastAt) < time.Duration(settings.MinIntervalSeconds)*time.Second {
		return &gateways.Error{Code: "quality_probe_rate_limited", Message: "quality probe minimum interval has not elapsed", Retryable: true}
	}
	if len(record.Requests) >= settings.MaxRequestsPerHour {
		return &gateways.Error{Code: "quality_probe_rate_limited", Message: "quality probe hourly budget is exhausted", Retryable: true}
	}
	if record.StartedAt.IsZero() {
		record.StartedAt = now
	}
	record.LastAt = now
	record.Requests = append(record.Requests, now)
	raw, err := json.Marshal(record)
	if err != nil {
		return &gateways.Error{Code: "quality_probe_rate_limited", Message: "quality probe budget could not be encoded", Retryable: true}
	}
	if err := atomicWritePrivateFile(b.path, append(raw, '\n')); err != nil {
		return &gateways.Error{Code: "quality_probe_rate_limited", Message: "quality probe budget could not be persisted", Retryable: true}
	}
	return nil
}

func (b *qualityProbeBudget) load() (qualityProbeBudgetRecord, error) {
	raw, err := readRegularFileNoSymlink(b.path)
	if errors.Is(err, os.ErrNotExist) {
		return qualityProbeBudgetRecord{}, nil
	}
	if err != nil {
		return qualityProbeBudgetRecord{}, err
	}
	var record qualityProbeBudgetRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return qualityProbeBudgetRecord{}, err
	}
	return record, nil
}
