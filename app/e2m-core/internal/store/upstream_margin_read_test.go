package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryUpstreamMarginReadIsOwnerScopedBoundedAndUnpaginated(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	for index := 0; index < 51; index++ {
		facts := upstreamCostTestBatch(t, 11, fmt.Sprintf("margin-usage-%d", index), now.Add(-time.Hour))
		if _, _, err := st.AppendUpstreamCostFacts(ctx, facts); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.AppendUpstreamCostFacts(ctx, upstreamCostTestBatch(t, 22, "foreign", now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	got, err := st.ReadUpstreamMarginCostFacts(ctx, 11, now.Add(-24*time.Hour), now)
	if err != nil || len(got) != 204 {
		t.Fatalf("unpaginated owner read: count=%d err=%v", len(got), err)
	}
	for _, fact := range got {
		if fact.UserID != 11 {
			t.Fatalf("foreign owner leaked: %+v", fact)
		}
	}
}

func TestMemoryUpstreamMarginReadUsesHalfOpenWindow(t *testing.T) {
	ctx := context.Background()
	until := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	since := until.Add(-24 * time.Hour)
	st := NewMemoryStore(until)
	for _, item := range []struct {
		id string
		at time.Time
	}{{"at-since", since}, {"inside", since.Add(time.Second)}, {"at-until", until}, {"before", since.Add(-time.Nanosecond)}} {
		if _, _, err := st.AppendUpstreamCostFacts(ctx, upstreamCostTestBatch(t, 11, item.id, item.at)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ReadUpstreamMarginCostFacts(ctx, 11, since, until)
	if err != nil || len(got) != 8 {
		t.Fatalf("half-open read: count=%d err=%v", len(got), err)
	}
}

func TestMemoryUpstreamMarginReadRejectsInvalidWindowAndFailsClosedAtBound(t *testing.T) {
	now := time.Now().UTC()
	st := NewMemoryStore(now)
	if _, err := st.ReadUpstreamMarginCostFacts(context.Background(), 0, now.Add(-time.Hour), now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ownerless read error=%v", err)
	}
	st.mu.Lock()
	st.upstreamCostFacts = make([]contracts.UpstreamCostFact, maxUpstreamMarginCostFacts+1)
	for i := range st.upstreamCostFacts {
		st.upstreamCostFacts[i] = contracts.UpstreamCostFact{UserID: 11, OccurredAt: now.Add(-time.Minute)}
	}
	st.mu.Unlock()
	if _, err := st.ReadUpstreamMarginCostFacts(context.Background(), 11, now.Add(-time.Hour), now); !errors.Is(err, ErrConflict) {
		t.Fatalf("oversize read error=%v", err)
	}
}
