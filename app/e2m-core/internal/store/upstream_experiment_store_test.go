package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/upstreamexperiment"
	"e2m.local/core/internal/upstreamrecommendation"
)

func TestMemoryUpstreamExperimentStoreIsOwnerScopedImmutableAndDeepCopies(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	shadow := experimentShadowFixture()
	stored, err := st.AppendUpstreamShadowResult(ctx, shadow)
	if err != nil {
		t.Fatal(err)
	}
	shadow.Ranking[0].EvidenceIDs[0] = "mutated"
	read, err := st.GetUpstreamShadowResult(ctx, stored.UserID, stored.ID)
	if err != nil || read.Ranking[0].EvidenceIDs[0] != "price-1" {
		t.Fatalf("shadow copy/read: %+v %v", read, err)
	}
	if _, err := st.GetUpstreamShadowResult(ctx, stored.UserID+1, stored.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner read: %v", err)
	}
	tampered := stored
	tampered.Winner.ChannelID = "other"
	if _, err := st.AppendUpstreamShadowResult(ctx, tampered); !errors.Is(err, ErrConflict) {
		t.Fatalf("tampered replay: %v", err)
	}

	dry := experimentDryRunFixture()
	dryStored, err := st.AppendUpstreamDryRunResult(ctx, dry)
	if err != nil {
		t.Fatal(err)
	}
	dry.DesiredScheduling["channel-2"] = false
	dryRead, err := st.GetUpstreamDryRunResult(ctx, dryStored.UserID, dryStored.ID)
	if err != nil || !dryRead.DesiredScheduling["channel-2"] {
		t.Fatalf("dry-run copy/read: %+v %v", dryRead, err)
	}
	if _, err := st.GetUpstreamDryRunResult(ctx, dryStored.UserID+1, dryStored.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner dry-run read: %v", err)
	}
	tamperedDry := dryStored
	tamperedDry.ActionSetHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if _, err := st.AppendUpstreamDryRunResult(ctx, tamperedDry); !errors.Is(err, ErrConflict) {
		t.Fatalf("tampered dry-run replay: %v", err)
	}
}

func TestMemoryCompleteUpstreamExperimentsAreAtomicIdempotentAndFenced(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	recommendation := atomicExperimentRecommendationFixture(t, now)
	if _, err := st.CreateUpstreamRecommendation(ctx, recommendation); err != nil {
		t.Fatal(err)
	}
	shadow := atomicExperimentShadowFixture(recommendation, "shadow-atomic", now.Add(time.Minute))
	ready, storedShadow, err := st.CompleteUpstreamShadow(ctx, recommendation, shadow)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != contracts.UpstreamRecommendationReadyForDryRun || storedShadow.ID != shadow.ID {
		t.Fatalf("shadow completion recommendation=%+v result=%+v", ready, storedShadow)
	}
	replayed, replayedShadow, err := st.CompleteUpstreamShadow(ctx, recommendation, shadow)
	if err != nil || replayed.Status != contracts.UpstreamRecommendationReadyForDryRun || replayedShadow.ID != shadow.ID {
		t.Fatalf("idempotent shadow replay recommendation=%+v result=%+v err=%v", replayed, replayedShadow, err)
	}
	shadows, err := st.ListUpstreamShadowResults(ctx, recommendation.UserID, recommendation.ID, 10)
	if err != nil || len(shadows) != 1 {
		t.Fatalf("shadow evidence count=%d err=%v", len(shadows), err)
	}

	dry := atomicExperimentDryRunFixture(ready, "dry-atomic", now.Add(2*time.Minute))
	passed, storedDry, err := st.CompleteUpstreamDryRun(ctx, ready, dry)
	if err != nil {
		t.Fatal(err)
	}
	if passed.Status != contracts.UpstreamRecommendationDryRunPassed || passed.DryRunID != dry.ID || storedDry.ID != dry.ID {
		t.Fatalf("dry-run completion recommendation=%+v result=%+v", passed, storedDry)
	}
	replayedPassed, replayedDry, err := st.CompleteUpstreamDryRun(ctx, ready, dry)
	if err != nil || replayedPassed.Status != contracts.UpstreamRecommendationDryRunPassed || replayedDry.ID != dry.ID {
		t.Fatalf("idempotent dry-run replay recommendation=%+v result=%+v err=%v", replayedPassed, replayedDry, err)
	}
	dryRuns, err := st.ListUpstreamDryRunResults(ctx, recommendation.UserID, recommendation.ID, 10)
	if err != nil || len(dryRuns) != 1 {
		t.Fatalf("dry-run evidence count=%d err=%v", len(dryRuns), err)
	}
}

func TestMemoryCompleteUpstreamShadowConcurrentCASCommitsOneResult(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	recommendation := atomicExperimentRecommendationFixture(t, now)
	if _, err := st.CreateUpstreamRecommendation(ctx, recommendation); err != nil {
		t.Fatal(err)
	}
	inputs := []contracts.UpstreamShadowResult{
		atomicExperimentShadowFixture(recommendation, "shadow-race-a", now.Add(time.Minute)),
		atomicExperimentShadowFixture(recommendation, "shadow-race-b", now.Add(time.Minute)),
	}
	start := make(chan struct{})
	errorsOut := make(chan error, len(inputs))
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
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrConflict):
			conflicted++
		default:
			t.Fatalf("unexpected completion error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	results, err := st.ListUpstreamShadowResults(ctx, recommendation.UserID, recommendation.ID, 10)
	current, getErr := st.GetUpstreamRecommendation(ctx, recommendation.UserID, recommendation.ID)
	if err != nil || getErr != nil || len(results) != 1 || current.Status != contracts.UpstreamRecommendationReadyForDryRun {
		t.Fatalf("results=%d recommendation=%+v listErr=%v getErr=%v", len(results), current, err, getErr)
	}
}

func TestMemoryCompleteUpstreamDryRunRejectsMismatchedEvidenceWithoutPartialWrite(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	recommendation := atomicExperimentRecommendationFixture(t, now)
	if _, err := st.CreateUpstreamRecommendation(ctx, recommendation); err != nil {
		t.Fatal(err)
	}
	ready, _, err := st.CompleteUpstreamShadow(ctx, recommendation, atomicExperimentShadowFixture(recommendation, "shadow-ready", now.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	dry := atomicExperimentDryRunFixture(ready, "dry-mismatch", now.Add(2*time.Minute))
	dry.RecommendationFingerprint = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if _, _, err := st.CompleteUpstreamDryRun(ctx, ready, dry); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched dry-run error=%v", err)
	}
	current, err := st.GetUpstreamRecommendation(ctx, recommendation.UserID, recommendation.ID)
	if err != nil || current.Status != contracts.UpstreamRecommendationReadyForDryRun || current.DryRunID != "" {
		t.Fatalf("recommendation changed after rejection: %+v err=%v", current, err)
	}
	results, err := st.ListUpstreamDryRunResults(ctx, recommendation.UserID, recommendation.ID, 10)
	if err != nil || len(results) != 0 {
		t.Fatalf("partial dry-run evidence=%+v err=%v", results, err)
	}
}

func TestMemoryCompleteUpstreamShadowRejectsTamperedCostWithoutPartialWrite(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	recommendation := atomicExperimentRecommendationFixture(t, now)
	if _, err := st.CreateUpstreamRecommendation(ctx, recommendation); err != nil {
		t.Fatal(err)
	}
	shadow := atomicExperimentShadowFixture(recommendation, "shadow-tampered", now.Add(time.Minute))
	shadow.Ranking[0].Cost = "0"
	shadow.Winner.Cost = "0"
	if _, _, err := st.CompleteUpstreamShadow(ctx, recommendation, shadow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered shadow error=%v", err)
	}
	current, err := st.GetUpstreamRecommendation(ctx, recommendation.UserID, recommendation.ID)
	results, listErr := st.ListUpstreamShadowResults(ctx, recommendation.UserID, recommendation.ID, 10)
	if err != nil || listErr != nil || current.Status != contracts.UpstreamRecommendationOpen || len(results) != 0 {
		t.Fatalf("recommendation=%+v results=%+v err=%v listErr=%v", current, results, err, listErr)
	}
}

func TestMemoryCompleteUpstreamDryRunConcurrentCASCommitsOneResult(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	recommendation := atomicExperimentRecommendationFixture(t, now)
	if _, err := st.CreateUpstreamRecommendation(ctx, recommendation); err != nil {
		t.Fatal(err)
	}
	ready, _, err := st.CompleteUpstreamShadow(ctx, recommendation, atomicExperimentShadowFixture(recommendation, "shadow-dry-race", now.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	inputs := []contracts.UpstreamDryRunResult{
		atomicExperimentDryRunFixture(ready, "dry-race-a", now.Add(2*time.Minute)),
		atomicExperimentDryRunFixture(ready, "dry-race-b", now.Add(2*time.Minute)),
	}
	start := make(chan struct{})
	errorsOut := make(chan error, len(inputs))
	var wait sync.WaitGroup
	for _, input := range inputs {
		input := input
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, _, err := st.CompleteUpstreamDryRun(ctx, ready, input)
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
			t.Fatalf("unexpected completion error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	results, err := st.ListUpstreamDryRunResults(ctx, recommendation.UserID, recommendation.ID, 10)
	current, getErr := st.GetUpstreamRecommendation(ctx, recommendation.UserID, recommendation.ID)
	if err != nil || getErr != nil || len(results) != 1 || current.Status != contracts.UpstreamRecommendationDryRunPassed || current.DryRunID != results[0].ID {
		t.Fatalf("results=%+v recommendation=%+v listErr=%v getErr=%v", results, current, err, getErr)
	}
}

func atomicExperimentRecommendationFixture(t *testing.T, now time.Time) contracts.UpstreamRecommendation {
	t.Helper()
	constraints := []contracts.UpstreamRecommendationConstraint{
		{Kind: contracts.UpstreamRecommendationConstraintQuality, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{"quality-from", "quality-to"}},
		{Kind: contracts.UpstreamRecommendationConstraintCapacity, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{"binding-from", "binding-to", "link-from", "link-to"}},
		{Kind: contracts.UpstreamRecommendationConstraintBalance, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{"wallet-from", "wallet-to"}},
	}
	value, err := upstreamrecommendation.Build("rec-atomic", contracts.UpstreamRecommendationCandidate{
		UserID: 42, IntelligenceFactVersion: 7, CostLedgerFactVersion: 8, LinkFactVersion: 7, PlanGeneration: 10,
		FromSourceID: "source-from", FromChannelID: "channel-from", FromGroupKey: "default",
		ToSourceID: "source-to", ToChannelID: "channel-to", ToGroupKey: "default", ModelKey: "model-a",
		PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", PerTokens: 1_000_000,
		AffectedPlanIDs: []string{"plan-1"}, AffectedDownstreams: []string{"instance-1"},
		EvidenceIDs:    []string{"offer-from", "cost-from", "quality-from", "wallet-from", "link-from", "binding-from", "offer-to", "cost-to", "quality-to", "wallet-to", "link-to", "binding-to"},
		Constraints:    constraints,
		FromCost:       contracts.UpstreamRecommendationCostRange{Lower: "10", Expected: "10", Upper: "10"},
		ToCost:         contracts.UpstreamRecommendationCostRange{Lower: "5", Expected: "5", Upper: "5"},
		FormulaVersion: contracts.UpstreamRecommendationFormulaVersionV1, StrategyVersion: contracts.UpstreamRecommendationStrategyVersionV1,
		CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatalf("build recommendation fixture: %v", err)
	}
	return value
}

func atomicExperimentShadowFixture(recommendation contracts.UpstreamRecommendation, id string, now time.Time) contracts.UpstreamShadowResult {
	constraints := append([]contracts.UpstreamRecommendationConstraint(nil), recommendation.Constraints...)
	from := contracts.UpstreamShadowCandidate{
		UserID: recommendation.UserID, SourceID: recommendation.FromSourceID, ChannelID: recommendation.FromChannelID,
		GroupKey: recommendation.FromGroupKey, ModelKey: recommendation.ModelKey, PriceDimension: recommendation.PriceDimension,
		SettlementCurrency: recommendation.SettlementCurrency, PerTokens: recommendation.PerTokens, Cost: "10", QualityScore: "98",
		Constraints: constraints, EvidenceIDs: append([]string(nil), recommendation.EvidenceIDs...),
	}
	to := from
	to.SourceID, to.ChannelID, to.GroupKey, to.Cost, to.QualityScore = recommendation.ToSourceID, recommendation.ToChannelID, recommendation.ToGroupKey, "5", "97"
	return contracts.UpstreamShadowResult{
		ID: id, UserID: recommendation.UserID, RecommendationID: recommendation.ID, RecommendationFingerprint: recommendation.Fingerprint,
		Winner: to, Ranking: []contracts.UpstreamShadowCandidate{to, from}, EvidenceIDs: append([]string(nil), recommendation.EvidenceIDs...), EvaluatedAt: now,
	}
}

func atomicExperimentDryRunFixture(recommendation contracts.UpstreamRecommendation, id string, now time.Time) contracts.UpstreamDryRunResult {
	plan := contracts.ReconcilePlan{InstanceID: recommendation.AffectedDownstreams[0], PlanID: recommendation.AffectedPlanIDs[0], DryRun: true,
		Actions: []contracts.ReconcileAction{{Type: contracts.ReconcileDisable, ChannelID: recommendation.FromChannelID}, {Type: contracts.ReconcileEnable, ChannelID: recommendation.ToChannelID}}, CreatedAt: now}
	value := contracts.UpstreamDryRunResult{
		ID: id, UserID: recommendation.UserID, RecommendationID: recommendation.ID, RecommendationFingerprint: recommendation.Fingerprint,
		IntelligenceFactVersion: recommendation.IntelligenceFactVersion, CostLedgerFactVersion: recommendation.CostLedgerFactVersion,
		LinkFactVersion: recommendation.LinkFactVersion, PlanGeneration: recommendation.PlanGeneration, PlanID: recommendation.AffectedPlanIDs[0],
		FromChannelID: recommendation.FromChannelID, ToChannelID: recommendation.ToChannelID,
		DesiredScheduling: map[string]bool{recommendation.FromChannelID: false, recommendation.ToChannelID: true},
		ReconcileKind:     contracts.ReconcileRunDryRun, Plan: plan, ActionHashVersion: contracts.UpstreamExperimentActionHashVersionV1, CreatedAt: now,
	}
	value.ActionSetHash, _ = upstreamexperiment.ActionSetHash(plan)
	return value
}

func TestMemoryUpstreamExperimentStoreNormalizesPostgresTimePrecision(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	shadow := experimentShadowFixture()
	shadow.EvaluatedAt = shadow.EvaluatedAt.Add(987 * time.Nanosecond)
	storedShadow, err := st.AppendUpstreamShadowResult(ctx, shadow)
	if err != nil {
		t.Fatal(err)
	}
	if storedShadow.EvaluatedAt.Nanosecond()%1000 != 0 {
		t.Fatalf("shadow time not normalized: %s", storedShadow.EvaluatedAt)
	}
	dry := experimentDryRunFixture()
	dry.CreatedAt = dry.CreatedAt.Add(987 * time.Nanosecond)
	dry.Plan.CreatedAt = dry.CreatedAt
	storedDry, err := st.AppendUpstreamDryRunResult(ctx, dry)
	if err != nil {
		t.Fatal(err)
	}
	if storedDry.CreatedAt.Nanosecond()%1000 != 0 || storedDry.Plan.CreatedAt.Nanosecond()%1000 != 0 || !storedDry.Plan.CreatedAt.Equal(storedDry.CreatedAt) {
		t.Fatalf("dry-run times not normalized together: result=%s plan=%s", storedDry.CreatedAt, storedDry.Plan.CreatedAt)
	}
}

func TestRecommendationImmutableEqualComparesTimestampInstants(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 123456000, time.UTC)
	left := atomicExperimentRecommendationFixture(t, now)
	right := cloneUpstreamRecommendation(left)
	right.CreatedAt = right.CreatedAt.In(time.FixedZone("UTC+8", 8*60*60))
	right.ExpiresAt = right.ExpiresAt.In(time.FixedZone("UTC-7", -7*60*60))
	if !recommendationImmutableEqual(left, right) {
		t.Fatal("same timestamp instants with different locations must compare equal")
	}
	right.ExpiresAt = right.ExpiresAt.Add(time.Microsecond)
	if recommendationImmutableEqual(left, right) {
		t.Fatal("different timestamp instants must fail the immutable fence")
	}
}

func experimentShadowFixture() contracts.UpstreamShadowResult {
	candidate := contracts.UpstreamShadowCandidate{
		UserID: 42, SourceID: "source-1", ChannelID: "channel-2", GroupKey: "default", ModelKey: "model-a",
		PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", PerTokens: 1000000,
		Cost: "5", QualityScore: "95", EvidenceIDs: []string{"price-1", "quality-1"},
	}
	return contracts.UpstreamShadowResult{
		ID: "shadow-1", UserID: 42, RecommendationID: "rec-1",
		RecommendationFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Winner:                    candidate, Ranking: []contracts.UpstreamShadowCandidate{candidate}, EvidenceIDs: []string{"price-1", "quality-1"},
		EvaluatedAt: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
	}
}

func experimentDryRunFixture() contracts.UpstreamDryRunResult {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	plan := contracts.ReconcilePlan{InstanceID: "instance-1", PlanID: "plan-1", DryRun: true,
		Actions: []contracts.ReconcileAction{{Type: contracts.ReconcileEnable, ChannelID: "channel-2", RemoteID: "remote-2"}}, CreatedAt: now}
	return contracts.UpstreamDryRunResult{
		ID: "dry-1", UserID: 42, RecommendationID: "rec-1",
		RecommendationFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IntelligenceFactVersion:   7, CostLedgerFactVersion: 8, LinkFactVersion: 9, PlanGeneration: 10,
		PlanID: "plan-1", FromChannelID: "channel-1", ToChannelID: "channel-2",
		DesiredScheduling: map[string]bool{"channel-1": false, "channel-2": true}, ReconcileKind: contracts.ReconcileRunDryRun,
		Plan: plan, ActionHashVersion: contracts.UpstreamExperimentActionHashVersionV1,
		ActionSetHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", CreatedAt: now,
	}
}
