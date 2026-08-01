package store

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryQualityCircuitRuntimeLifecycleAndIsolation(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	openedAt := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	probeAfter := openedAt.Add(5 * time.Minute)
	transitionAt := openedAt

	created, err := st.UpsertQualityCircuitRuntime(ctx, contracts.QualityCircuitRuntime{
		PlanID: "plan-a", ChannelID: "channel-a", State: contracts.QualityCircuitOpen,
		OpenedAt: &openedAt, ProbeAfter: &probeAfter, LastTransitionAt: &transitionAt,
		OpenCount: 1, LastScore: 57.5,
		LastReason: contracts.QualityCircuitReason{Code: "penalty_threshold", Text: "quality score reached ejection threshold"},
	}, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Version != 1 || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("create must initialize version and timestamps: %+v", created)
	}

	got, err := st.GetQualityCircuitRuntime(ctx, "plan-a", "channel-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != contracts.QualityCircuitOpen || got.LastScore != 57.5 || got.LastReason.Code != "penalty_threshold" ||
		got.OpenedAt == nil || !got.OpenedAt.Equal(openedAt) || got.ProbeAfter == nil || !got.ProbeAfter.Equal(probeAfter) {
		t.Fatalf("persisted runtime lost state: %+v", got)
	}
	// Returned and input time pointers cannot mutate persisted state without CAS.
	mutated := openedAt.Add(time.Hour)
	*got.OpenedAt = mutated
	openedAt = mutated
	got, err = st.GetQualityCircuitRuntime(ctx, "plan-a", "channel-a")
	if err != nil || got.OpenedAt == nil || got.OpenedAt.Equal(mutated) {
		t.Fatalf("memory store leaked a time pointer: runtime=%+v err=%v", got, err)
	}

	// The same channel in another downstream plan is an independent circuit.
	other, err := st.UpsertQualityCircuitRuntime(ctx, contracts.QualityCircuitRuntime{
		PlanID: "plan-b", ChannelID: "channel-a", State: contracts.QualityCircuitClosed, LastScore: 100,
	}, 0)
	if err != nil {
		t.Fatalf("create isolated plan: %v", err)
	}
	if other.Version != 1 {
		t.Fatalf("isolated runtime version=%d, want 1", other.Version)
	}
	planA, err := st.ListQualityCircuitRuntimes(ctx, contracts.QualityCircuitRuntimeFilter{PlanID: "plan-a"})
	if err != nil || len(planA) != 1 || planA[0].ChannelID != "channel-a" || planA[0].State != contracts.QualityCircuitOpen {
		t.Fatalf("plan-scoped list=%+v err=%v", planA, err)
	}

	probeAt := probeAfter.Add(time.Second)
	updatedInput := got
	updatedInput.State = contracts.QualityCircuitHalfOpen
	updatedInput.LastProbeAt = &probeAt
	updatedInput.ConsecutiveProbeSuccesses = 1
	updatedInput.LastScore = 91
	updatedInput.RestorePending = true
	updated, err := st.UpsertQualityCircuitRuntime(ctx, updatedInput, got.Version)
	if err != nil {
		t.Fatalf("CAS update: %v", err)
	}
	if updated.Version != 2 || updated.CreatedAt != created.CreatedAt || !updated.UpdatedAt.After(created.UpdatedAt) && updated.UpdatedAt != created.UpdatedAt {
		t.Fatalf("CAS must advance version and preserve creation: created=%+v updated=%+v", created, updated)
	}
	if updated.LastProbeAt == nil || !updated.LastProbeAt.Equal(probeAt) {
		t.Fatalf("last probe was not persisted: %+v", updated)
	}
	if !updated.RestorePending {
		t.Fatalf("restore intent was not persisted: %+v", updated)
	}

	if _, err := st.UpsertQualityCircuitRuntime(ctx, updatedInput, got.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS error=%v, want ErrConflict", err)
	}
	if _, err := st.UpsertQualityCircuitRuntime(ctx, updatedInput, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate create error=%v, want ErrConflict", err)
	}
	if _, err := st.GetQualityCircuitRuntime(ctx, "missing", "channel-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing get error=%v, want ErrNotFound", err)
	}
}

func TestMemoryQualityCircuitRuntimeProbeDueFilter(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	for _, seed := range []contracts.QualityCircuitRuntime{
		{PlanID: "plan-a", ChannelID: "due-first", State: contracts.QualityCircuitOpen, ProbeAfter: timePtr(now.Add(-time.Minute)), OpenCount: 1, LastScore: 40},
		{PlanID: "plan-a", ChannelID: "due-second", State: contracts.QualityCircuitOpen, ProbeAfter: timePtr(now), OpenCount: 1, LastScore: 50},
		{PlanID: "plan-a", ChannelID: "future", State: contracts.QualityCircuitOpen, ProbeAfter: timePtr(now.Add(time.Minute)), OpenCount: 1, LastScore: 60},
		{PlanID: "plan-a", ChannelID: "closed", State: contracts.QualityCircuitClosed, LastScore: 100},
	} {
		if _, err := st.UpsertQualityCircuitRuntime(ctx, seed, 0); err != nil {
			t.Fatalf("seed %s: %v", seed.ChannelID, err)
		}
	}
	due, err := st.ListQualityCircuitRuntimes(ctx, contracts.QualityCircuitRuntimeFilter{
		States: []contracts.QualityCircuitState{contracts.QualityCircuitOpen}, ProbeDueBefore: now, Limit: 1,
	})
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 1 || due[0].ChannelID != "due-first" {
		t.Fatalf("due list must filter, order, then limit: %+v", due)
	}
}

func TestMemoryQualityCircuitRuntimeCASAllowsOneWinner(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	current, err := st.UpsertQualityCircuitRuntime(ctx, contracts.QualityCircuitRuntime{
		PlanID: "plan-race", ChannelID: "channel-a", State: contracts.QualityCircuitOpen,
		OpenCount: 1, LastScore: 50,
	}, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	const callers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	var winners atomic.Int32
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			input := current
			input.State = contracts.QualityCircuitHalfOpen
			input.ConsecutiveProbeSuccesses = 1
			if _, updateErr := st.UpsertQualityCircuitRuntime(ctx, input, current.Version); updateErr == nil {
				winners.Add(1)
			} else if !errors.Is(updateErr, ErrConflict) {
				t.Errorf("CAS: %v", updateErr)
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("CAS winners=%d, want 1", winners.Load())
	}
	stored, _ := st.GetQualityCircuitRuntime(ctx, current.PlanID, current.ChannelID)
	if stored.Version != 2 || stored.State != contracts.QualityCircuitHalfOpen {
		t.Fatalf("winner did not persist exactly one transition: %+v", stored)
	}
}

func TestMemoryQualityCircuitRuntimeRejectsInvalidInput(t *testing.T) {
	st := NewMemoryStore(time.Now())
	ctx := context.Background()
	for name, input := range map[string]contracts.QualityCircuitRuntime{
		"missing plan":    {ChannelID: "channel-a", State: contracts.QualityCircuitClosed, LastScore: 100},
		"missing channel": {PlanID: "plan-a", State: contracts.QualityCircuitClosed, LastScore: 100},
		"bad state":       {PlanID: "plan-a", ChannelID: "channel-a", State: "broken", LastScore: 100},
		"bad score":       {PlanID: "plan-a", ChannelID: "channel-a", State: contracts.QualityCircuitClosed, LastScore: 101},
		"nan score":       {PlanID: "plan-a", ChannelID: "channel-a", State: contracts.QualityCircuitClosed, LastScore: math.NaN()},
		"bad count":       {PlanID: "plan-a", ChannelID: "channel-a", State: contracts.QualityCircuitClosed, OpenCount: -1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := st.UpsertQualityCircuitRuntime(ctx, input, 0); !errors.Is(err, ErrConflict) {
				t.Fatalf("error=%v, want ErrConflict", err)
			}
		})
	}
}

func timePtr(value time.Time) *time.Time { return &value }
