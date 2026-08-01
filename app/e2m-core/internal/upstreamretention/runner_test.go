package upstreamretention

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"e2m.local/core/internal/store"
)

type retentionStoreFake struct {
	owners       []int64
	listCalls    []int64
	pruneCalls   []int64
	cutoffs      []time.Time
	failPruneFor map[int64]error
}

func (f *retentionStoreFake) ListUpstreamIntelligenceRetentionOwners(_ context.Context, cutoff time.Time, after int64, limit int) ([]int64, error) {
	f.listCalls = append(f.listCalls, after)
	f.cutoffs = append(f.cutoffs, cutoff)
	out := make([]int64, 0, limit)
	for _, owner := range f.owners {
		if owner > after {
			out = append(out, owner)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (f *retentionStoreFake) PruneUpstreamIntelligenceHistory(_ context.Context, userID int64, cutoff time.Time, _ int) (store.UpstreamIntelligenceRetentionResult, error) {
	f.pruneCalls = append(f.pruneCalls, userID)
	f.cutoffs = append(f.cutoffs, cutoff)
	return store.UpstreamIntelligenceRetentionResult{UserID: userID}, f.failPruneFor[userID]
}

func TestRunOncePagesOwnersAndIsolatesOwnerFailures(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	failure := errors.New("temporary database failure")
	fake := &retentionStoreFake{owners: []int64{11, 22, 33}, failPruneFor: map[int64]error{22: failure}}
	runner := New(fake, time.Hour, WithClock(func() time.Time { return now }), WithBatchSizes(2, 7))

	err := runner.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "owner 22") || !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
	if !reflect.DeepEqual(fake.pruneCalls, []int64{11, 22, 33}) {
		t.Fatalf("one owner blocked peers: calls=%v", fake.pruneCalls)
	}
	if !reflect.DeepEqual(fake.listCalls, []int64{0, 22}) {
		t.Fatalf("cursor pages=%v", fake.listCalls)
	}
	wantCutoff := now.Add(-DefaultHistoryRetention)
	for _, cutoff := range fake.cutoffs {
		if !cutoff.Equal(wantCutoff) {
			t.Fatalf("cutoff drift=%s want=%s", cutoff, wantCutoff)
		}
	}
}

func TestWithHistoryRetentionRefusesShorterThanPolicy(t *testing.T) {
	fake := &retentionStoreFake{failPruneFor: map[int64]error{}}
	runner := New(fake, time.Hour, WithHistoryRetention(24*time.Hour))
	if runner.historyRetention != DefaultHistoryRetention {
		t.Fatalf("short retention accepted: %s", runner.historyRetention)
	}
	longer := 120 * 24 * time.Hour
	runner = New(fake, time.Hour, WithHistoryRetention(longer))
	if runner.historyRetention != longer {
		t.Fatalf("longer retention ignored: %s", runner.historyRetention)
	}
}
