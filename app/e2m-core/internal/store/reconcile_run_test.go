package store

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
)

// The memory store must append reconcile runs, filter by plan, order them
// newest-first, and honour the limit — the query shape the console and Phase 4
// automatic evaluator depend on.
func TestMemoryReconcileRunAppendAndList(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))

	// Two runs on plan-a, one on plan-b, appended in order.
	seed := []contracts.ReconcileRun{
		{PlanID: "plan-a", Kind: contracts.ReconcileRunDryRun, Trigger: contracts.ReconcileTriggerManual, Status: contracts.ReconcileRunSucceeded},
		{PlanID: "plan-b", Kind: contracts.ReconcileRunApply, Trigger: contracts.ReconcileTriggerAuto, Status: contracts.ReconcileRunSucceeded},
		{PlanID: "plan-a", Kind: contracts.ReconcileRunApply, Trigger: contracts.ReconcileTriggerManual, Status: contracts.ReconcileRunPartial,
			Actions: []contracts.ReconcileAction{{Type: contracts.ReconcileEnable, ChannelID: "ch-a"}}, Error: "boom"},
	}
	for i := range seed {
		if _, err := st.AppendReconcileRun(ctx, seed[i]); err != nil {
			t.Fatalf("append run %d: %v", i, err)
		}
	}

	// plan-a: two runs, newest first (the apply appended last comes first).
	runsA, err := st.ListReconcileRuns(ctx, "plan-a", 0)
	if err != nil {
		t.Fatalf("list plan-a: %v", err)
	}
	if len(runsA) != 2 {
		t.Fatalf("expected 2 runs for plan-a, got %d", len(runsA))
	}
	if runsA[0].Kind != contracts.ReconcileRunApply || runsA[1].Kind != contracts.ReconcileRunDryRun {
		t.Fatalf("runs should be newest-first, got %q then %q", runsA[0].Kind, runsA[1].Kind)
	}
	if runsA[0].Status != contracts.ReconcileRunPartial || runsA[0].Error != "boom" {
		t.Fatalf("partial run not preserved: %+v", runsA[0])
	}
	if len(runsA[0].Actions) != 1 || runsA[0].Actions[0].ChannelID != "ch-a" {
		t.Fatalf("actions not preserved: %+v", runsA[0].Actions)
	}
	if runsA[0].ID == "" || runsA[0].StartedAt.IsZero() || runsA[0].FinishedAt.IsZero() {
		t.Fatalf("append should assign id + timestamps, got %+v", runsA[0])
	}

	// limit caps the result set.
	limited, err := st.ListReconcileRuns(ctx, "plan-a", 1)
	if err != nil {
		t.Fatalf("list limited: %v", err)
	}
	if len(limited) != 1 || limited[0].Kind != contracts.ReconcileRunApply {
		t.Fatalf("limit=1 should return only the newest run, got %+v", limited)
	}

	// empty planID returns all runs across plans.
	all, err := st.ListReconcileRuns(ctx, "", 0)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 runs across plans, got %d", len(all))
	}
}
