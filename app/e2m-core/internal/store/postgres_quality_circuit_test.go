package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPostgresQualityCircuitRuntimeRoundTripAndCAS(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	planID := "test-" + newID("quality-circuit")
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM quality_circuit_runtimes WHERE plan_id=$1`, planID)
	})
	openedAt := time.Date(2026, 7, 13, 8, 0, 0, 123456000, time.UTC)
	probeAfter := openedAt.Add(5 * time.Minute)
	created, err := st.UpsertQualityCircuitRuntime(ctx, contracts.QualityCircuitRuntime{
		PlanID: planID, ChannelID: "channel-a", State: contracts.QualityCircuitOpen,
		OpenedAt: &openedAt, ProbeAfter: &probeAfter, LastTransitionAt: &openedAt,
		OpenCount: 1, LastScore: 55,
		LastReason: contracts.QualityCircuitReason{Code: "penalty_threshold", Text: "quality ejected"},
	}, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Version != 1 || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("create defaults: %+v", created)
	}
	if _, err := st.UpsertQualityCircuitRuntime(ctx, created, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate create error=%v, want ErrConflict", err)
	}

	got, err := st.GetQualityCircuitRuntime(ctx, planID, "channel-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LastReason != created.LastReason || got.ProbeAfter == nil || !got.ProbeAfter.Equal(probeAfter) {
		t.Fatalf("round trip lost fields: %+v", got)
	}

	probeAt := probeAfter.Add(time.Second)
	got.State = contracts.QualityCircuitHalfOpen
	got.LastProbeAt = &probeAt
	got.ConsecutiveProbeSuccesses = 1
	got.LastScore = 90
	got.RestorePending = true
	updated, err := st.UpsertQualityCircuitRuntime(ctx, got, got.Version)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Version != 2 || updated.LastProbeAt == nil || !updated.LastProbeAt.Equal(probeAt) || !updated.RestorePending {
		t.Fatalf("update did not advance state: %+v", updated)
	}
	if _, err := st.UpsertQualityCircuitRuntime(ctx, got, got.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error=%v, want ErrConflict", err)
	}

	due, err := st.ListQualityCircuitRuntimes(ctx, contracts.QualityCircuitRuntimeFilter{
		PlanID: planID, States: []contracts.QualityCircuitState{contracts.QualityCircuitHalfOpen},
		ProbeDueBefore: probeAfter,
	})
	if err != nil || len(due) != 1 || due[0].ChannelID != "channel-a" {
		t.Fatalf("due list=%+v err=%v", due, err)
	}
}

func TestPostgresQualityCircuitRuntimeCASAllowsOneWinner(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	planID := "test-" + newID("quality-circuit-race")
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM quality_circuit_runtimes WHERE plan_id=$1`, planID)
	})
	current, err := st.UpsertQualityCircuitRuntime(ctx, contracts.QualityCircuitRuntime{
		PlanID: planID, ChannelID: "channel-a", State: contracts.QualityCircuitOpen,
		OpenCount: 1, LastScore: 50,
	}, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	const callers = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	var winners atomic.Int32
	errCh := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			input := current
			input.State = contracts.QualityCircuitHalfOpen
			input.ConsecutiveProbeSuccesses = 1
			_, updateErr := st.UpsertQualityCircuitRuntime(ctx, input, current.Version)
			if updateErr == nil {
				winners.Add(1)
			} else if !errors.Is(updateErr, ErrConflict) {
				errCh <- updateErr
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for updateErr := range errCh {
		t.Errorf("CAS: %v", updateErr)
	}
	if winners.Load() != 1 {
		t.Fatalf("CAS winners=%d, want 1", winners.Load())
	}
}
