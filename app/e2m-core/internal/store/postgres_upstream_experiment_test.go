package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/upstreamrecommendation"
)

func TestPostgresCompleteUpstreamExperimentsAtomicReplayAndConcurrentFence(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)
	owner, err := st.CreateUser(ctx, testUpstreamExperimentPostgresUser())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner.ID) })

	now := time.Now().UTC().Truncate(time.Microsecond)
	recommendation := atomicExperimentRecommendationFixture(t, now)
	recommendation.ID = "rec-" + newID("experiment")
	recommendation.UserID = owner.ID
	// Changing owner and id invalidates the stable fingerprint; rebuild through
	// the fixture candidate helper used by the memory tests.
	recommendation = postgresExperimentRecommendationFixture(t, owner.ID, recommendation.ID, now)
	storedRecommendation, err := st.CreateUpstreamRecommendation(ctx, recommendation)
	if err != nil {
		t.Fatal(err)
	}
	if !recommendationImmutableEqual(storedRecommendation, recommendation) {
		t.Fatalf("created recommendation changed immutable fields:\ninput=%#v\nstored=%#v", recommendation, storedRecommendation)
	}
	if storedRecommendation.CreatedAt.Location() != time.UTC || storedRecommendation.ExpiresAt.Location() != time.UTC {
		t.Fatalf("created recommendation timestamps not normalized to UTC: created=%s expires=%s", storedRecommendation.CreatedAt, storedRecommendation.ExpiresAt)
	}
	inputs := []contracts.UpstreamShadowResult{
		atomicExperimentShadowFixture(recommendation, "shadow-"+newID("race-a"), now.Add(time.Minute)),
		atomicExperimentShadowFixture(recommendation, "shadow-"+newID("race-b"), now.Add(time.Minute)),
	}
	start := make(chan struct{})
	errorsOut := make(chan error, 2)
	var wait sync.WaitGroup
	for _, input := range inputs {
		input := input
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, _, err := st.CompleteUpstreamShadow(ctx, recommendation, input)
			errorsOut <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsOut)
	succeeded, conflicted := 0, 0
	for err := range errorsOut {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrConflict) {
			conflicted++
		} else {
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	shadows, err := st.ListUpstreamShadowResults(ctx, owner.ID, recommendation.ID, 10)
	if err != nil || len(shadows) != 1 {
		t.Fatalf("shadow rows=%+v err=%v", shadows, err)
	}
	var committedInput contracts.UpstreamShadowResult
	for _, input := range inputs {
		if input.ID == shadows[0].ID {
			committedInput = input
			break
		}
	}
	if committedInput.ID == "" {
		t.Fatalf("committed shadow %q was not one of the concurrent inputs", shadows[0].ID)
	}
	replayedReady, replayedShadow, err := st.CompleteUpstreamShadow(ctx, recommendation, committedInput)
	if err != nil || replayedReady.Status != contracts.UpstreamRecommendationReadyForDryRun || replayedShadow.ID != committedInput.ID {
		t.Fatalf("replayed shadow recommendation=%+v result=%+v err=%v", replayedReady, replayedShadow, err)
	}
	ready, err := st.GetUpstreamRecommendation(ctx, owner.ID, recommendation.ID)
	if err != nil || ready.Status != contracts.UpstreamRecommendationReadyForDryRun {
		t.Fatalf("recommendation=%+v err=%v", ready, err)
	}
	dry := atomicExperimentDryRunFixture(ready, "dry-"+newID("atomic"), now.Add(2*time.Minute))
	passed, saved, err := st.CompleteUpstreamDryRun(ctx, ready, dry)
	if err != nil || passed.Status != contracts.UpstreamRecommendationDryRunPassed || saved.ID != dry.ID {
		t.Fatalf("passed=%+v saved=%+v err=%v", passed, saved, err)
	}
	replayed, replayedDry, err := st.CompleteUpstreamDryRun(ctx, ready, dry)
	if err != nil || replayed.Status != contracts.UpstreamRecommendationDryRunPassed || replayedDry.ID != dry.ID {
		t.Fatalf("replayed=%+v result=%+v err=%v", replayed, replayedDry, err)
	}
	dryRuns, err := st.ListUpstreamDryRunResults(ctx, owner.ID, recommendation.ID, 10)
	if err != nil || len(dryRuns) != 1 {
		t.Fatalf("dry-run rows=%+v err=%v", dryRuns, err)
	}
}

func postgresExperimentRecommendationFixture(t *testing.T, userID int64, id string, now time.Time) contracts.UpstreamRecommendation {
	t.Helper()
	value := atomicExperimentRecommendationFixture(t, now)
	candidate := contracts.UpstreamRecommendationCandidate{
		UserID: userID, IntelligenceFactVersion: value.IntelligenceFactVersion, CostLedgerFactVersion: value.CostLedgerFactVersion,
		LinkFactVersion: value.LinkFactVersion, PlanGeneration: value.PlanGeneration, FromSourceID: value.FromSourceID,
		FromChannelID: value.FromChannelID, FromGroupKey: value.FromGroupKey, ToSourceID: value.ToSourceID,
		ToChannelID: value.ToChannelID, ToGroupKey: value.ToGroupKey, ModelKey: value.ModelKey, PriceDimension: value.PriceDimension,
		SettlementCurrency: value.SettlementCurrency, PerTokens: value.PerTokens, AffectedPlanIDs: value.AffectedPlanIDs,
		AffectedDownstreams: value.AffectedDownstreams, EvidenceIDs: value.EvidenceIDs, Constraints: value.Constraints,
		FromCost: value.FromCost, ToCost: value.ToCost, FormulaVersion: value.FormulaVersion, StrategyVersion: value.StrategyVersion,
		CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	}
	rebuilt, err := upstreamrecommendation.Build(id, candidate)
	if err != nil {
		t.Fatal(err)
	}
	return rebuilt
}

func testUpstreamExperimentPostgresUser() contracts.User {
	return contracts.User{Email: newID("experiment-owner") + "@example.com", PasswordHash: "test", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true}
}
