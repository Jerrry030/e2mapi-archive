package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
)

func seedMemoryIntelligenceOwner(t *testing.T, st *MemoryStore, userID int64, instanceID, connectorID string) {
	t.Helper()
	now := time.Now().UTC()
	st.users = append(st.users, contracts.User{ID: userID})
	st.instances = append(st.instances, contracts.Instance{ID: instanceID, UserID: userID})
	st.connectors = append(st.connectors, contracts.Connector{ID: connectorID, UserID: userID, InstanceID: instanceID, CreatedAt: now, UpdatedAt: now})
}

func TestMemoryUpstreamIntelligenceOwnerIsolationAndBatchIdempotency(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	seedMemoryIntelligenceOwner(t, st, 11, "inst-a", "conn-a")
	seedMemoryIntelligenceOwner(t, st, 22, "inst-b", "conn-b")

	source, err := st.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source-a", UserID: 11, ConnectorID: "conn-a", InstanceID: "inst-a", LocalRef: "local-a",
		Mode: contracts.UpstreamSourceExternal, Provider: "sub2api", DisplayName: "A", Status: contracts.UpstreamSourceActive,
	})
	if err != nil {
		t.Fatalf("upsert source: %v", err)
	}
	if _, err := st.GetUpstreamIntelligenceSource(ctx, 22, source.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner get error=%v, want ErrNotFound", err)
	}
	retry := contracts.UpstreamIntelligenceSource{
		UserID: 11, ConnectorID: "conn-a", InstanceID: "inst-a", LocalRef: "local-a",
		Mode: contracts.UpstreamSourceExternal, Provider: "sub2api", DisplayName: "A updated", Status: contracts.UpstreamSourcePaused,
	}
	reused, err := st.UpsertUpstreamIntelligenceSource(ctx, retry)
	if err != nil || reused.ID != source.ID || reused.DisplayName != "A updated" {
		t.Fatalf("natural-key retry did not reuse identity: source=%+v err=%v", reused, err)
	}
	if got, err := st.ListUpstreamIntelligenceSources(ctx, contracts.UpstreamIntelligenceSourceFilter{UserID: 22}); err != nil || len(got) != 0 {
		t.Fatalf("cross-owner list=%+v err=%v", got, err)
	}
	if _, err := st.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "forged", UserID: 22, ConnectorID: "conn-a", InstanceID: "inst-a", LocalRef: "forged",
		Mode: contracts.UpstreamSourceExternal, Provider: "sub2api", DisplayName: "forged",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner connector injection error=%v, want ErrNotFound", err)
	}

	run := newMemoryIntelligenceRun("run-a", 11, source.ID, "conn-a", time.Now().UTC())
	if _, err := st.CreateUpstreamCollectionRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	batch := UpstreamIntelligenceIngestBatch{
		RunID: run.ID, UserID: 11, SourceID: source.ID, BatchNo: 0, BatchCount: 1,
		PayloadHash: hash64("a"), ManifestHash: manifestHash(t, hash64("a")), OfferCount: 1,
	}
	run.ManifestHash = batch.ManifestHash
	if _, duplicate, err := st.UpsertUpstreamIntelligenceIngestBatch(ctx, batch); err != nil || duplicate {
		t.Fatalf("first batch duplicate=%v err=%v", duplicate, err)
	}
	if _, duplicate, err := st.UpsertUpstreamIntelligenceIngestBatch(ctx, batch); err != nil || !duplicate {
		t.Fatalf("replayed batch duplicate=%v err=%v", duplicate, err)
	}
	metrics, err := st.GetOperationalMetrics(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("get operational metrics: %v", err)
	}
	if got := metrics.IngestFactsByOutcome["accepted"]; got != 1 {
		t.Fatalf("standalone accepted facts=%d, want 1", got)
	}
	if got := metrics.IngestFactsByOutcome["duplicate"]; got != 1 {
		t.Fatalf("standalone duplicate facts=%d, want 1", got)
	}
	conflict := batch
	conflict.PayloadHash = hash64("b")
	if _, _, err := st.UpsertUpstreamIntelligenceIngestBatch(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed batch error=%v, want ErrConflict", err)
	}
	metricsAfterConflict, err := st.GetOperationalMetrics(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("get metrics after conflict: %v", err)
	}
	if metricsAfterConflict.IngestFactsByOutcome["accepted"] != 1 || metricsAfterConflict.IngestFactsByOutcome["duplicate"] != 1 {
		t.Fatalf("conflicting replay changed outcome counters: %+v", metricsAfterConflict.IngestFactsByOutcome)
	}
}

func TestMemoryUpstreamIntelligenceRunAndObservationIDsAreOwnerScoped(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	seedMemoryIntelligenceOwner(t, st, 101, "inst-a", "conn-a")
	seedMemoryIntelligenceOwner(t, st, 202, "inst-b", "conn-b")
	now := time.Now().UTC().Truncate(time.Microsecond)

	for _, owner := range []struct {
		userID      int64
		instanceID  string
		connectorID string
		sourceID    string
	}{
		{userID: 101, instanceID: "inst-a", connectorID: "conn-a", sourceID: "source-a"},
		{userID: 202, instanceID: "inst-b", connectorID: "conn-b", sourceID: "source-b"},
	} {
		payloadHash := hash64("shared-payload")
		manifest := manifestHash(t, payloadHash)
		input := UpstreamIntelligenceIngest{
			Source: contracts.UpstreamIntelligenceSource{
				ID: owner.sourceID, UserID: owner.userID, ConnectorID: owner.connectorID, InstanceID: owner.instanceID,
				LocalRef: "shared-local-ref", Mode: contracts.UpstreamSourceExternal, Provider: "sub2api", DisplayName: "Shared identity",
			},
			Run: newMemoryIntelligenceRun("shared-run", owner.userID, owner.sourceID, owner.connectorID, now),
			Batch: UpstreamIntelligenceIngestBatch{
				RunID: "shared-run", UserID: owner.userID, SourceID: owner.sourceID, BatchNo: 0, BatchCount: 1,
				PayloadHash: payloadHash, ManifestHash: manifest, WalletCount: 1, OfferCount: 1,
			},
			Wallets: []contracts.UpstreamWalletObservation{memoryWallet("shared-run", owner.userID, owner.sourceID, now)},
			Offers:  []contracts.UpstreamOfferObservation{memoryOffer("shared-run", owner.userID, owner.sourceID, now)},
		}
		input.Run.ManifestHash = manifest
		input.Run.FactCount = 2
		if _, _, _, duplicate, err := st.IngestUpstreamIntelligenceBatch(ctx, input); err != nil || duplicate {
			t.Fatalf("owner %d ingest duplicate=%v err=%v", owner.userID, duplicate, err)
		}
		finalized, version, err := st.FinalizeUpstreamCollectionRun(ctx, owner.userID, "shared-run")
		if err != nil || finalized.UserID != owner.userID || version.UserID != owner.userID || version.FactVersion != 1 {
			t.Fatalf("owner %d finalize run=%+v version=%+v err=%v", owner.userID, finalized, version, err)
		}
	}

	if len(st.upstreamIntelRuns) != 2 || len(st.upstreamIntelBatches) != 2 || len(st.upstreamIntelWallets) != 2 || len(st.upstreamIntelOffers) != 2 || len(st.upstreamIntelFinalized) != 2 {
		t.Fatalf("owner-scoped identities collapsed: runs=%d batches=%d wallets=%d offers=%d finalized=%d", len(st.upstreamIntelRuns), len(st.upstreamIntelBatches), len(st.upstreamIntelWallets), len(st.upstreamIntelOffers), len(st.upstreamIntelFinalized))
	}
	for _, userID := range []int64{101, 202} {
		run, err := st.GetUpstreamCollectionRun(ctx, userID, "shared-run")
		if err != nil || run.UserID != userID {
			t.Fatalf("owner %d get shared run=%+v err=%v", userID, run, err)
		}
		offers, err := st.ListUpstreamOfferObservations(ctx, contracts.UpstreamOfferObservationFilter{UserID: userID})
		if err != nil || len(offers) != 1 || offers[0].ID != "offer-shared-run" || offers[0].UserID != userID {
			t.Fatalf("owner %d offers=%+v err=%v", userID, offers, err)
		}
		wallets, err := st.ListUpstreamWalletObservations(ctx, contracts.UpstreamWalletObservationFilter{UserID: userID})
		if err != nil || len(wallets) != 1 || wallets[0].ID != "wallet-shared-run" || wallets[0].UserID != userID {
			t.Fatalf("owner %d wallets=%+v err=%v", userID, wallets, err)
		}
	}

	conflict := newMemoryIntelligenceRun("shared-run", 101, "source-a", "conn-a", now.Add(time.Minute))
	if _, err := st.CreateUpstreamCollectionRun(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-owner changed run error=%v, want ErrConflict", err)
	}
}

func TestPostgresRunReplayComparisonIgnoresEquivalentTimeLocations(t *testing.T) {
	utc := time.Date(2026, 7, 27, 1, 2, 3, 456789000, time.UTC)
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	existing := newMemoryIntelligenceRun("same-run", 17, "source", "connector", utc)
	existing.ReceivedAt, existing.CreatedAt, existing.UpdatedAt = utc, utc, utc
	retry := existing
	retry.StartedAt = retry.StartedAt.In(shanghai)
	retry.ObservedAt = retry.ObservedAt.In(shanghai)
	// ReceivedAt, CreatedAt and UpdatedAt are storage-owned fields. Replay
	// comparison replaces them from the durable row before checking semantic
	// equality, so only request-owned timestamps belong in this timezone proof.
	completedAt := retry.CompletedAt.In(shanghai)
	retry.CompletedAt = &completedAt
	if !reflectUpstreamRunEqual(existing, retry) {
		t.Fatal("same PostgreSQL timestamptz instants in different locations must replay idempotently")
	}
	retry.ObservedAt = retry.ObservedAt.Add(time.Microsecond)
	if reflectUpstreamRunEqual(existing, retry) {
		t.Fatal("different instants must remain a replay conflict")
	}
}

func TestPostgresObservationReplayComparisonIgnoresEquivalentTimeLocations(t *testing.T) {
	utc := time.Date(2026, 7, 27, 1, 2, 3, 456789000, time.UTC)
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}

	wallet := memoryWallet("same-run", 17, "source", utc)
	wallet.ReceivedAt = utc.Add(time.Second)
	walletRetry := wallet
	walletRetry.ObservedAt = walletRetry.ObservedAt.In(shanghai)
	walletRetry.ReceivedAt = walletRetry.ReceivedAt.In(shanghai)
	walletRetry.FreshUntil = walletRetry.FreshUntil.In(shanghai)
	if !upstreamWalletEqual(wallet, walletRetry) {
		t.Fatal("same wallet timestamptz instants in different locations must replay idempotently")
	}
	walletRetry.FreshUntil = walletRetry.FreshUntil.Add(time.Microsecond)
	if upstreamWalletEqual(wallet, walletRetry) {
		t.Fatal("different wallet instants must remain a replay conflict")
	}

	offer := memoryOffer("same-run", 17, "source", utc)
	offer.ReceivedAt = utc.Add(time.Second)
	validUntil := utc.Add(2 * time.Hour)
	offer.ValidUntil = &validUntil
	offerRetry := offer
	offerRetry.ObservedAt = offerRetry.ObservedAt.In(shanghai)
	offerRetry.EffectiveAt = offerRetry.EffectiveAt.In(shanghai)
	offerRetry.ReceivedAt = offerRetry.ReceivedAt.In(shanghai)
	offerRetry.FreshUntil = offerRetry.FreshUntil.In(shanghai)
	retryValidUntil := offerRetry.ValidUntil.In(shanghai)
	offerRetry.ValidUntil = &retryValidUntil
	if !upstreamOfferEqual(offer, offerRetry) {
		t.Fatal("same offer timestamptz instants in different locations must replay idempotently")
	}
	offerRetry.ValidUntil = nil
	if upstreamOfferEqual(offer, offerRetry) {
		t.Fatal("nil and non-nil offer validity must remain a replay conflict")
	}
}

func TestPostgresFinalizeSharedRunParametersHaveExplicitTextType(t *testing.T) {
	source, err := os.ReadFile("postgres_upstream_intelligence.go")
	if err != nil {
		t.Fatal(err)
	}
	// PostgreSQL can assign different typmods to the wallet and offer run_id
	// columns. A shared extended-query parameter must therefore be explicitly
	// typed or preparation fails with SQLSTATE 42P08 before finalization runs.
	if got := strings.Count(string(source), "run_id=$2::text"); got < 4 {
		t.Fatalf("shared wallet/offer finalization parameters with explicit text type=%d, want at least 4", got)
	}
}

func TestPostgresCollectionDurationParameterHasExplicitFloatType(t *testing.T) {
	source, err := os.ReadFile("operational_events.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "$2::double precision") || !strings.Contains(text, "0.1::double precision") {
		t.Fatal("duration writer must explicitly type its shared parameter and fractional bucket literal")
	}
}

func TestMemoryUpstreamIntelligenceFinalizationUsesRunFenceNotTimestamp(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	seedMemoryIntelligenceOwner(t, st, 33, "inst", "conn")
	source, err := st.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source", UserID: 33, ConnectorID: "conn", InstanceID: "inst", LocalRef: "local",
		Mode: contracts.UpstreamSourceOwned, Provider: "sub2api", DisplayName: "owned", Status: contracts.UpstreamSourceActive,
	})
	if err != nil {
		t.Fatalf("upsert source: %v", err)
	}
	completedAt := time.Now().UTC().Truncate(time.Microsecond)
	for i, runID := range []string{"run-one", "run-two"} {
		run := newMemoryIntelligenceRun(runID, 33, source.ID, "conn", completedAt)
		payloadHash := hash64(runID)
		run.ManifestHash = manifestHash(t, payloadHash)
		if _, err := st.CreateUpstreamCollectionRun(ctx, run); err != nil {
			t.Fatalf("create %s: %v", runID, err)
		}
		batch := UpstreamIntelligenceIngestBatch{RunID: runID, UserID: 33, SourceID: source.ID, BatchNo: 0, BatchCount: 1, PayloadHash: payloadHash, ManifestHash: run.ManifestHash, OfferCount: 1}
		if _, _, err := st.UpsertUpstreamIntelligenceIngestBatch(ctx, batch); err != nil {
			t.Fatalf("batch %s: %v", runID, err)
		}
		if _, err := st.AppendUpstreamOfferObservation(ctx, memoryOffer(runID, 33, source.ID, completedAt)); err != nil {
			t.Fatalf("offer %s: %v", runID, err)
		}
		finalized, version, err := st.FinalizeUpstreamCollectionRun(ctx, 33, runID)
		if err != nil || version.FactVersion != int64(i+1) || finalized.FinalizedFactVersion != int64(i+1) {
			t.Fatalf("finalize %s run=%+v version=%+v err=%v", runID, finalized, version, err)
		}
		replayed, replayVersion, err := st.FinalizeUpstreamCollectionRun(ctx, 33, runID)
		if err != nil || replayed.FinalizedFactVersion != version.FactVersion || replayVersion.FactVersion != version.FactVersion {
			t.Fatalf("replay %s run=%+v version=%+v err=%v", runID, replayed, replayVersion, err)
		}
	}
	version, err := st.GetUpstreamIntelligenceFactVersion(ctx, 33)
	if err != nil || version.FactVersion != 2 {
		t.Fatalf("owner fact version=%+v err=%v, want 2", version, err)
	}
	oldRun, oldVersion, err := st.FinalizeUpstreamCollectionRun(ctx, 33, "run-one")
	if err != nil || oldRun.FinalizedFactVersion != 1 || oldVersion.FactVersion != 1 {
		t.Fatalf("old run replay drifted: run=%+v version=%+v err=%v", oldRun, oldVersion, err)
	}
}

func TestMemoryUpstreamIntelligenceLateRunDoesNotRewindSourceCurrent(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	seedMemoryIntelligenceOwner(t, st, 34, "inst", "conn")
	source, err := st.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source", UserID: 34, ConnectorID: "conn", InstanceID: "inst", LocalRef: "local",
		Mode: contracts.UpstreamSourceOwned, Provider: "sub2api", DisplayName: "owned", Status: contracts.UpstreamSourceActive,
	})
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond)
	finalize := func(runID string, observedAt time.Time, status contracts.UpstreamCollectionStatus, coverage contracts.UpstreamEvidenceCoverage, errorCode string) int64 {
		run := newMemoryIntelligenceRun(runID, 34, source.ID, "conn", observedAt)
		run.Status, run.Coverage, run.ErrorCode = status, coverage, errorCode
		run.Retryable = status == contracts.UpstreamCollectionFailed
		if status == contracts.UpstreamCollectionFailed {
			run.FactCount = 0
		}
		payloadHash := hash64(runID)
		run.ManifestHash = manifestHash(t, payloadHash)
		if _, err := st.CreateUpstreamCollectionRun(ctx, run); err != nil {
			t.Fatalf("create %s: %v", runID, err)
		}
		batch := UpstreamIntelligenceIngestBatch{RunID: runID, UserID: 34, SourceID: source.ID, BatchNo: 0, BatchCount: 1, PayloadHash: payloadHash, ManifestHash: run.ManifestHash}
		if status == contracts.UpstreamCollectionSucceeded {
			batch.OfferCount = 1
		}
		if _, _, err := st.UpsertUpstreamIntelligenceIngestBatch(ctx, batch); err != nil {
			t.Fatalf("batch %s: %v", runID, err)
		}
		if status == contracts.UpstreamCollectionSucceeded {
			offer := memoryOffer(runID, 34, source.ID, observedAt)
			offer.Coverage = coverage
			if _, err := st.AppendUpstreamOfferObservation(ctx, offer); err != nil {
				t.Fatalf("offer %s: %v", runID, err)
			}
		}
		_, version, err := st.FinalizeUpstreamCollectionRun(ctx, 34, runID)
		if err != nil {
			t.Fatalf("finalize %s: %v", runID, err)
		}
		return version.FactVersion
	}

	if version := finalize("run-new", base.Add(10*time.Minute), contracts.UpstreamCollectionFailed, contracts.UpstreamCoverageUnavailable, contracts.UpstreamCollectionErrorUpstreamUnavailable); version != 1 {
		t.Fatalf("new run version=%d", version)
	}
	if version := finalize("run-old", base, contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, ""); version != 2 {
		t.Fatalf("late run version=%d", version)
	}
	got, err := st.GetUpstreamIntelligenceSource(ctx, 34, source.ID)
	if err != nil || got.LastRunAt == nil || !got.LastRunAt.Equal(base.Add(10*time.Minute)) || got.LastCoverage != contracts.UpstreamCoverageUnavailable ||
		got.LastErrorCode != contracts.UpstreamCollectionErrorUpstreamUnavailable || got.LastSuccessAt == nil || !got.LastSuccessAt.Equal(base) {
		t.Fatalf("late run rewound source current: %+v err=%v", got, err)
	}
}

func TestMemoryIngestUpstreamIntelligenceBatchIsAtomicAndReplaySafe(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	seedMemoryIntelligenceOwner(t, st, 44, "inst", "conn")
	now := time.Now().UTC().Truncate(time.Microsecond)
	payloadHash := hash64("atomic")
	manifest := manifestHash(t, payloadHash)
	input := UpstreamIntelligenceIngest{
		Source: contracts.UpstreamIntelligenceSource{
			UserID: 44, ConnectorID: "conn", InstanceID: "inst", LocalRef: "local",
			Mode: contracts.UpstreamSourceExternal, Provider: "sub2api", DisplayName: "Atomic",
		},
		Run: newMemoryIntelligenceRun("run-atomic", 44, "", "conn", now),
		Batch: UpstreamIntelligenceIngestBatch{
			RunID: "run-atomic", UserID: 44, BatchNo: 0, BatchCount: 1,
			PayloadHash: payloadHash, ManifestHash: manifest, OfferCount: 1,
		},
		Offers: []contracts.UpstreamOfferObservation{memoryOffer("run-atomic", 44, "", now)},
	}
	input.Run.ManifestHash = manifest
	input.Offers[0].ReceivedAt = time.Unix(1, 0) // server-owned and ignored

	invalid := input
	invalid.Offers = append([]contracts.UpstreamOfferObservation(nil), input.Offers...)
	invalid.Offers[0].PerTokens = 0
	if _, _, _, _, err := st.IngestUpstreamIntelligenceBatch(ctx, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid atomic ingest error=%v, want ErrInvalid", err)
	}
	if len(st.upstreamIntelSources)+len(st.upstreamIntelRuns)+len(st.upstreamIntelOffers)+len(st.upstreamIntelBatches) != 0 {
		t.Fatalf("failed ingest left state: sources=%d runs=%d offers=%d batches=%d", len(st.upstreamIntelSources), len(st.upstreamIntelRuns), len(st.upstreamIntelOffers), len(st.upstreamIntelBatches))
	}

	source, run, batch, duplicate, err := st.IngestUpstreamIntelligenceBatch(ctx, input)
	if err != nil || duplicate || source.ID == "" || run.SourceID != source.ID || batch.SourceID != source.ID {
		t.Fatalf("first ingest source=%+v run=%+v batch=%+v duplicate=%v err=%v", source, run, batch, duplicate, err)
	}
	if len(st.upstreamIntelOffers) != 1 || st.upstreamIntelOffers[0].ReceivedAt.Equal(input.Offers[0].ReceivedAt) {
		t.Fatalf("offer received_at was not assigned by store: %+v", st.upstreamIntelOffers)
	}
	if _, _, _, duplicate, err := st.IngestUpstreamIntelligenceBatch(ctx, input); err != nil || !duplicate || len(st.upstreamIntelOffers) != 1 || len(st.upstreamIntelBatches) != 1 {
		t.Fatalf("replay duplicate=%v offers=%d batches=%d err=%v", duplicate, len(st.upstreamIntelOffers), len(st.upstreamIntelBatches), err)
	}
	conflict := input
	conflict.Offers = append([]contracts.UpstreamOfferObservation(nil), input.Offers...)
	conflict.Offers[0].ModelKey = "changed"
	if _, _, _, _, err := st.IngestUpstreamIntelligenceBatch(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay error=%v, want ErrConflict", err)
	}
	if len(st.upstreamIntelOffers) != 1 || len(st.upstreamIntelBatches) != 1 {
		t.Fatalf("conflicting replay mutated facts/receipt")
	}
}

func TestMemoryFinalizeRejectsDeclaredManifestWithoutMatchingLeaves(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	seedMemoryIntelligenceOwner(t, st, 55, "inst", "conn")
	source, err := st.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source", UserID: 55, ConnectorID: "conn", InstanceID: "inst", LocalRef: "local",
		Mode: contracts.UpstreamSourceOwned, Provider: "sub2api", DisplayName: "owned",
	})
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	run := newMemoryIntelligenceRun("run", 55, source.ID, "conn", now)
	run.ManifestHash = hash64("forged-declaration")
	if _, err := st.CreateUpstreamCollectionRun(ctx, run); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, _, err := st.UpsertUpstreamIntelligenceIngestBatch(ctx, UpstreamIntelligenceIngestBatch{
		RunID: run.ID, UserID: 55, SourceID: source.ID, BatchNo: 0, BatchCount: 1,
		PayloadHash: hash64("actual-leaf"), ManifestHash: run.ManifestHash, OfferCount: 1,
	}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	if _, err := st.AppendUpstreamOfferObservation(ctx, memoryOffer(run.ID, 55, source.ID, now)); err != nil {
		t.Fatalf("offer: %v", err)
	}
	if _, _, err := st.FinalizeUpstreamCollectionRun(ctx, 55, run.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("forged manifest finalize error=%v, want ErrConflict", err)
	}
}

func TestMemoryFinalizeCompleteRunRejectsPartialFacts(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	seedMemoryIntelligenceOwner(t, st, 66, "inst", "conn")
	source, err := st.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source", UserID: 66, ConnectorID: "conn", InstanceID: "inst", LocalRef: "local",
		Mode: contracts.UpstreamSourceOwned, Provider: "sub2api", DisplayName: "owned",
	})
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	payloadHash := hash64("partial-fact")
	run := newMemoryIntelligenceRun("run", 66, source.ID, "conn", now)
	run.ManifestHash = manifestHash(t, payloadHash)
	if _, err := st.CreateUpstreamCollectionRun(ctx, run); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, _, err := st.UpsertUpstreamIntelligenceIngestBatch(ctx, UpstreamIntelligenceIngestBatch{
		RunID: run.ID, UserID: 66, SourceID: source.ID, BatchNo: 0, BatchCount: 1,
		PayloadHash: payloadHash, ManifestHash: run.ManifestHash, OfferCount: 1,
	}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	offer := memoryOffer(run.ID, 66, source.ID, now)
	offer.Coverage = contracts.UpstreamCoveragePartial
	if _, err := st.AppendUpstreamOfferObservation(ctx, offer); err != nil {
		t.Fatalf("offer: %v", err)
	}
	if _, _, err := st.FinalizeUpstreamCollectionRun(ctx, 66, run.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("complete run with partial fact error=%v, want ErrConflict", err)
	}
}

func TestMemoryUpstreamIntelligenceListLimitsAreBounded(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	seedMemoryIntelligenceOwner(t, st, 77, "inst", "conn")
	for index := 0; index < contracts.MaxUpstreamIntelligenceListLimit+10; index++ {
		st.upstreamIntelSources = append(st.upstreamIntelSources, contracts.UpstreamIntelligenceSource{
			ID: "source-" + time.Unix(int64(index), 0).Format("150405"), UserID: 77, ConnectorID: "conn", InstanceID: "inst",
			LocalRef: "local", Mode: contracts.UpstreamSourceExternal, Provider: "sub2api", DisplayName: "source",
			Status: contracts.UpstreamSourceActive, UpdatedAt: time.Unix(int64(index), 0),
		})
	}
	for _, test := range []struct {
		limit int
		want  int
	}{{0, contracts.DefaultUpstreamIntelligenceListLimit}, {-1, contracts.DefaultUpstreamIntelligenceListLimit}, {999, contracts.MaxUpstreamIntelligenceListLimit}} {
		got, err := st.ListUpstreamIntelligenceSources(ctx, contracts.UpstreamIntelligenceSourceFilter{UserID: 77, Limit: test.limit})
		if err != nil || len(got) != test.want {
			t.Fatalf("limit=%d got=%d want=%d err=%v", test.limit, len(got), test.want, err)
		}
	}
}

func TestMemoryUpstreamIntelligenceLinkRequiresOwnerAllocationAndBumpsVersion(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	seedMemoryIntelligenceOwner(t, st, 88, "inst-a", "conn-a")
	seedMemoryIntelligenceOwner(t, st, 99, "inst-b", "conn-b")
	source, err := st.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "intelligence-a", UserID: 88, ConnectorID: "conn-a", InstanceID: "inst-a", LocalRef: "local-a",
		Mode: contracts.UpstreamSourceExternal, Provider: "sub2api", DisplayName: "External A",
	})
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	verifiedAt := time.Now().UTC().Truncate(time.Microsecond)
	st.channelAllocations["channel-a"] = upstreamChannelAllocation{UserID: 88, SourceID: "allocated-a"}
	st.channelAllocations["channel-b"] = upstreamChannelAllocation{UserID: 99, SourceID: "allocated-b"}
	st.channelAllocations["channel-ambiguous-a"] = upstreamChannelAllocation{UserID: 88, SourceID: "ambiguous"}
	st.channelAllocations["channel-ambiguous-b"] = upstreamChannelAllocation{UserID: 88, SourceID: "ambiguous"}

	for name, input := range map[string]contracts.UpstreamIntelligenceLink{
		"unallocated source identity": {
			UserID: 88, IntelligenceSourceID: source.ID, Scope: contracts.UpstreamLinkSourceIdentity,
			UpstreamSourceIdentity: "guessed", PriceDimension: contracts.UpstreamPriceInput,
			Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt,
		},
		"other owner source identity": {
			UserID: 88, IntelligenceSourceID: source.ID, Scope: contracts.UpstreamLinkSourceIdentity,
			UpstreamSourceIdentity: "allocated-b", PriceDimension: contracts.UpstreamPriceInput,
			Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt,
		},
		"other owner channel": {
			UserID: 88, IntelligenceSourceID: source.ID, Scope: contracts.UpstreamLinkChannel,
			ChannelID: "channel-b", PriceDimension: contracts.UpstreamPriceInput,
			Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt,
		},
	} {
		if _, err := st.UpsertUpstreamIntelligenceLink(ctx, input); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s error=%v, want ErrNotFound", name, err)
		}
	}
	if _, err := st.UpsertUpstreamIntelligenceLink(ctx, contracts.UpstreamIntelligenceLink{
		UserID: 88, IntelligenceSourceID: source.ID, Scope: contracts.UpstreamLinkChannel,
		ChannelID: "channel-a", Status: contracts.UpstreamLinkActive,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unverified active link error=%v, want ErrInvalid", err)
	}
	if _, err := st.UpsertUpstreamIntelligenceLink(ctx, contracts.UpstreamIntelligenceLink{
		UserID: 88, IntelligenceSourceID: source.ID, Scope: contracts.UpstreamLinkChannel,
		ChannelID: "channel-a", Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("dimensionless active link error=%v, want ErrInvalid", err)
	}
	for _, unsafeIdentity := range []string{
		"https://supplier.example", "Bearer secret", "source\nidentity", "credential=super-secret",
		"Authorization : abc", "token = abc", `{"token":"abc"}`, `{"cookie":"abc"}`,
		"token abc", "credential-super-secret", "raw-response-abc", "api.supplier.example/v1",
		strings.Repeat("x", contracts.MaxUpstreamSourceIdentityBytes+1),
	} {
		if _, err := st.UpsertUpstreamIntelligenceLink(ctx, contracts.UpstreamIntelligenceLink{
			UserID: 88, IntelligenceSourceID: source.ID, Scope: contracts.UpstreamLinkSourceIdentity,
			UpstreamSourceIdentity: unsafeIdentity, PriceDimension: contracts.UpstreamPriceInput,
			Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt,
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsafe source identity %q error=%v, want ErrInvalid", unsafeIdentity, err)
		}
	}
	if !validUpstreamIntelligenceLink(contracts.UpstreamIntelligenceLink{
		UserID: 88, IntelligenceSourceID: source.ID, Scope: contracts.UpstreamLinkChannel,
		ChannelID: "channel-a", Status: contracts.UpstreamLinkInactive,
	}) {
		t.Fatal("historical dimensionless inactive link must remain representable")
	}
	if _, err := st.UpsertUpstreamIntelligenceLink(ctx, contracts.UpstreamIntelligenceLink{
		UserID: 88, IntelligenceSourceID: source.ID, Scope: contracts.UpstreamLinkSourceIdentity,
		UpstreamSourceIdentity: "ambiguous", PriceDimension: contracts.UpstreamPriceInput,
		Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("ambiguous active source identity error=%v, want ErrConflict", err)
	}

	created, err := st.UpsertUpstreamIntelligenceLink(ctx, contracts.UpstreamIntelligenceLink{
		UserID: 88, IntelligenceSourceID: source.ID, Scope: contracts.UpstreamLinkSourceIdentity,
		UpstreamSourceIdentity: "allocated-a", PriceDimension: contracts.UpstreamPriceInput,
		Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt,
	})
	if err != nil {
		t.Fatalf("create verified link: %v", err)
	}
	version, err := st.GetUpstreamIntelligenceFactVersion(ctx, 88)
	if err != nil || version.FactVersion != 1 {
		t.Fatalf("created link version=%+v err=%v", version, err)
	}
	if _, err := st.UpsertUpstreamIntelligenceLink(ctx, created); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	replayedVersion, _ := st.GetUpstreamIntelligenceFactVersion(ctx, 88)
	if replayedVersion.FactVersion != version.FactVersion {
		t.Fatalf("idempotent replay bumped version from %d to %d", version.FactVersion, replayedVersion.FactVersion)
	}
	created.Status = contracts.UpstreamLinkInactive
	created.VerifiedAt = nil
	updated, err := st.UpsertUpstreamIntelligenceLink(ctx, created)
	if err != nil || updated.Status != contracts.UpstreamLinkInactive {
		t.Fatalf("deactivate link=%+v err=%v", updated, err)
	}
	updatedVersion, _ := st.GetUpstreamIntelligenceFactVersion(ctx, 88)
	if updatedVersion.FactVersion != version.FactVersion+1 {
		t.Fatalf("changed link version=%d want=%d", updatedVersion.FactVersion, version.FactVersion+1)
	}
	otherVersion, _ := st.GetUpstreamIntelligenceFactVersion(ctx, 99)
	if otherVersion.FactVersion != 0 {
		t.Fatalf("owner 99 version changed: %+v", otherVersion)
	}
}

func newMemoryIntelligenceRun(id string, userID int64, sourceID, connectorID string, completedAt time.Time) contracts.UpstreamCollectionRun {
	return contracts.UpstreamCollectionRun{
		ID: id, UserID: userID, SourceID: sourceID, ConnectorID: connectorID,
		Trigger: contracts.UpstreamCollectionScheduled, Status: contracts.UpstreamCollectionSucceeded,
		Coverage: contracts.UpstreamCoverageComplete, StartedAt: completedAt.Add(-time.Minute), ObservedAt: completedAt,
		CompletedAt: &completedAt, ManifestHash: hash64("manifest-" + id), SnapshotHash: hash64("snapshot-" + id),
		BatchCount: 1, FactCount: 1, PageCount: 1,
	}
}

func memoryOffer(runID string, userID int64, sourceID string, observedAt time.Time) contracts.UpstreamOfferObservation {
	return contracts.UpstreamOfferObservation{
		ID: "offer-" + runID, RunID: runID, UserID: userID, SourceID: sourceID,
		GroupKey: "default", ModelKey: "model", PriceDimension: contracts.UpstreamPriceInput,
		PerTokens: 1_000_000, Accuracy: contracts.UpstreamEvidenceExact, Coverage: contracts.UpstreamCoverageComplete,
		ObservedAt: observedAt, EffectiveAt: observedAt, ReceivedAt: observedAt, FreshUntil: observedAt.Add(time.Hour),
		AdapterSchemaVersion: 1,
	}
}

func memoryWallet(runID string, userID int64, sourceID string, observedAt time.Time) contracts.UpstreamWalletObservation {
	return contracts.UpstreamWalletObservation{
		ID: "wallet-" + runID, RunID: runID, UserID: userID, SourceID: sourceID,
		UnitKind: contracts.UpstreamWalletFiat, Currency: "USD", Accuracy: contracts.UpstreamEvidenceExact,
		Coverage: contracts.UpstreamCoverageComplete, ObservedAt: observedAt, FreshUntil: observedAt.Add(time.Hour),
	}
}

func hash64(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func manifestHash(t *testing.T, payloadHashes ...string) string {
	t.Helper()
	batches := make([]contracts.UpstreamIntelligenceManifestBatch, len(payloadHashes))
	for index, payloadHash := range payloadHashes {
		batches[index] = contracts.UpstreamIntelligenceManifestBatch{BatchNo: index, PayloadHash: payloadHash}
	}
	hash, err := contracts.CalculateUpstreamIntelligenceManifestHash(batches)
	if err != nil {
		t.Fatalf("calculate manifest: %v", err)
	}
	return hash
}
