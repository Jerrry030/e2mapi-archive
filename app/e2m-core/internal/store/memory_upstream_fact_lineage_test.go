package store

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryFactMutationLineageClassifiesEveryManagedVersionBump(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 5, 0, 0, 0, time.UTC)
	st := NewMemoryStore(base)
	now := base
	st.now = func() time.Time { return now }
	seedMemoryIntelligenceOwner(t, st, 81, "instance-lineage", "connector-lineage")
	st.channelAllocations["channel-lineage"] = upstreamChannelAllocation{UserID: 81, SourceID: "source-lineage"}
	if _, err := st.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source-lineage", UserID: 81, ConnectorID: "connector-lineage", InstanceID: "instance-lineage",
		LocalRef: "lineage", Mode: contracts.UpstreamSourceExternal, Provider: "sub2api", DisplayName: "Lineage source",
	}); err != nil {
		t.Fatalf("source: %v", err)
	}

	quality := contracts.ChannelHealthSnapshot{
		ID: "quality-lineage", ChannelID: "channel-lineage", InstanceID: "instance-lineage", Model: "model-lineage",
		Window: contracts.Window5m, BucketStart: base, CreatedAt: base, QualitySampleCount: 10,
		QualitySuccessRate: .99, QualityScore: 95, HealthState: contracts.HealthHealthy,
	}
	if _, err := st.UpsertChannelHealthSnapshot(ctx, quality); err != nil {
		t.Fatalf("quality: %v", err)
	}
	now = now.Add(time.Minute)
	link, err := st.UpsertUpstreamIntelligenceLink(ctx, contracts.UpstreamIntelligenceLink{
		ID: "link-lineage", UserID: 81, IntelligenceSourceID: "source-lineage", Scope: contracts.UpstreamLinkChannel,
		ChannelID: "channel-lineage", PriceDimension: contracts.UpstreamPriceInput,
		Status: contracts.UpstreamLinkActive, VerifiedAt: &now,
	})
	if err != nil {
		t.Fatalf("link: %v", err)
	}

	now = now.Add(time.Minute)
	run := contracts.UpstreamCollectionRun{
		ID: "run-lineage", UserID: 81, SourceID: "source-lineage", ConnectorID: "connector-lineage",
		Trigger: contracts.UpstreamCollectionScheduled, Status: contracts.UpstreamCollectionFailed,
		Coverage: contracts.UpstreamCoverageUnavailable, StartedAt: now.Add(-time.Minute), ObservedAt: now,
		CompletedAt: &now, ErrorCode: contracts.UpstreamCollectionErrorUpstreamUnavailable, Retryable: true,
		BatchCount: 1, FactCount: 0, PageCount: 1,
	}
	payloadHash := hash64("lineage-empty-payload")
	run.ManifestHash = manifestHash(t, payloadHash)
	if _, err := st.CreateUpstreamCollectionRun(ctx, run); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, _, err := st.UpsertUpstreamIntelligenceIngestBatch(ctx, UpstreamIntelligenceIngestBatch{
		RunID: run.ID, UserID: 81, SourceID: run.SourceID, BatchNo: 0, BatchCount: 1,
		PayloadHash: payloadHash, ManifestHash: run.ManifestHash,
	}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	if _, _, err := st.FinalizeUpstreamCollectionRun(ctx, 81, run.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	now = now.Add(time.Minute)
	st.mu.Lock()
	st.bumpUpstreamIntelligenceFactVersionLocked(81, now, UpstreamIntelligenceFactMutationRetention, "")
	st.mu.Unlock()

	got := st.upstreamIntelFactMutations[81]
	wantKinds := []UpstreamIntelligenceFactMutationKind{
		UpstreamIntelligenceFactMutationQuality,
		UpstreamIntelligenceFactMutationLink,
		UpstreamIntelligenceFactMutationCollection,
		UpstreamIntelligenceFactMutationRetention,
	}
	wantEvidence := []string{quality.ID, link.ID, run.ID, ""}
	if st.upstreamIntelLineageWatermarks[81] != 0 || len(got) != len(wantKinds) {
		t.Fatalf("watermark=%d mutations=%+v", st.upstreamIntelLineageWatermarks[81], got)
	}
	for index := range wantKinds {
		if got[index].UserID != 81 || got[index].FactVersion != int64(index+1) || got[index].Kind != wantKinds[index] ||
			got[index].EvidenceID != wantEvidence[index] || got[index].CreatedAt.IsZero() {
			t.Fatalf("mutation[%d]=%+v", index, got[index])
		}
	}
}

func TestMemoryQualityMutationSameBucketAppendsRevisionLineage(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 6, 0, 0, 0, time.UTC)
	st := NewMemoryStore(base)
	st.now = func() time.Time { return base.Add(2 * time.Minute) }
	st.channelAllocations["channel"] = upstreamChannelAllocation{UserID: 91, SourceID: "source"}
	quality := contracts.ChannelHealthSnapshot{
		ID: "same-quality", ChannelID: "channel", InstanceID: "instance", Model: "model", Window: contracts.Window5m,
		BucketStart: base, CreatedAt: base, QualitySampleCount: 10, QualitySuccessRate: .99,
		QualityScore: 95, HealthState: contracts.HealthHealthy,
	}
	if _, err := st.UpsertChannelHealthSnapshot(ctx, quality); err != nil {
		t.Fatal(err)
	}
	firstID := quality.ID
	quality.ID = ""
	quality.QualityScore = 70
	quality.CreatedAt = base.Add(time.Minute)
	second, err := st.UpsertChannelHealthSnapshot(ctx, quality)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == firstID || len(st.upstreamIntelFactMutations[91]) != 2 ||
		st.upstreamIntelFactMutations[91][0].EvidenceID != firstID ||
		st.upstreamIntelFactMutations[91][1].EvidenceID != second.ID || st.upstreamIntelFactMutations[91][1].FactVersion != 2 {
		t.Fatalf("revision lineage=%+v second=%+v", st.upstreamIntelFactMutations[91], second)
	}
	proof := st.memoryQualityOnlyFactAdvanceProof(91, 1, 2)
	if !proof.Complete || !ValidQualityOnlyFactAdvanceProof(proof, 91, 1, 2) {
		t.Fatalf("quality proof=%+v", proof)
	}
}

func TestMemoryQualityOnlyFactAdvanceProofFailsClosedForWatermarkGapsAndNonQuality(t *testing.T) {
	base := time.Date(2026, 7, 27, 7, 0, 0, 0, time.UTC)
	st := NewMemoryStore(base)
	st.upstreamIntelVersions[101] = contracts.UpstreamIntelligenceFactVersion{UserID: 101, FactVersion: 7, UpdatedAt: base}
	st.mu.Lock()
	st.bumpUpstreamIntelligenceFactVersionLocked(101, base.Add(time.Minute), UpstreamIntelligenceFactMutationQuality, "quality-8")
	st.bumpUpstreamIntelligenceFactVersionLocked(101, base.Add(2*time.Minute), UpstreamIntelligenceFactMutationLink, "link-9")
	st.mu.Unlock()

	if proof := st.memoryQualityOnlyFactAdvanceProof(101, 6, 9); proof.Complete {
		t.Fatalf("pre-watermark proof must be incomplete: %+v", proof)
	}
	proof := st.memoryQualityOnlyFactAdvanceProof(101, 7, 9)
	if !proof.Complete || ValidQualityOnlyFactAdvanceProof(proof, 101, 7, 9) {
		t.Fatalf("non-quality interval must fail controller validation: %+v", proof)
	}

	// Simulate an untracked writer. The Memory helper must expose the gap and
	// never synthesize an unknown or quality mutation to make it look complete.
	st.upstreamIntelVersions[101] = contracts.UpstreamIntelligenceFactVersion{UserID: 101, FactVersion: 10, UpdatedAt: base.Add(3 * time.Minute)}
	st.mu.Lock()
	st.bumpUpstreamIntelligenceFactVersionLocked(101, base.Add(4*time.Minute), UpstreamIntelligenceFactMutationQuality, "quality-11")
	st.mu.Unlock()
	if gap := st.memoryQualityOnlyFactAdvanceProof(101, 9, 11); gap.Complete || len(gap.Mutations) != 0 {
		t.Fatalf("untracked version gap was hidden: %+v", gap)
	}
}

func TestMemoryQualityOnlyFactAdvanceProofIsOwnerScopedAndReturnsCopies(t *testing.T) {
	base := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	st := NewMemoryStore(base)
	st.upstreamIntelVersions[111] = contracts.UpstreamIntelligenceFactVersion{UserID: 111, FactVersion: 3, UpdatedAt: base}
	st.upstreamIntelVersions[222] = contracts.UpstreamIntelligenceFactVersion{UserID: 222, FactVersion: 3, UpdatedAt: base}
	for _, userID := range []int64{111, 222} {
		st.mu.Lock()
		st.bumpUpstreamIntelligenceFactVersionLocked(userID, base.Add(time.Minute), UpstreamIntelligenceFactMutationQuality, "quality-4")
		st.bumpUpstreamIntelligenceFactVersionLocked(userID, base.Add(2*time.Minute), UpstreamIntelligenceFactMutationQuality, "quality-5")
		st.mu.Unlock()
	}

	first := st.memoryQualityOnlyFactAdvanceProof(111, 3, 5)
	if !first.Complete || first.UserID != 111 || len(first.Mutations) != 2 {
		t.Fatalf("owner proof=%+v", first)
	}
	first.Mutations[0].EvidenceID = "mutated-by-caller"
	second := st.memoryQualityOnlyFactAdvanceProof(111, 3, 5)
	foreign := st.memoryQualityOnlyFactAdvanceProof(222, 3, 5)
	if second.Mutations[0].EvidenceID != "quality-4" || foreign.UserID != 222 || !foreign.Complete ||
		foreign.Mutations[0].UserID != 222 {
		t.Fatalf("proof leaked mutable or cross-owner state: owner=%+v foreign=%+v", second, foreign)
	}
}

func TestMemoryFactMutationLineageRejectsIdempotentReplayAndRecordsExplicitUnknown(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	st := NewMemoryStore(base)
	st.now = func() time.Time { return base }
	st.channelAllocations["channel"] = upstreamChannelAllocation{UserID: 121, SourceID: "source"}
	quality := contracts.ChannelHealthSnapshot{
		ID: "quality", ChannelID: "channel", InstanceID: "instance", Model: "model", Window: contracts.Window5m,
		BucketStart: base, CreatedAt: base, QualitySampleCount: 10, QualitySuccessRate: .99,
		QualityScore: 95, HealthState: contracts.HealthHealthy,
	}
	stored, err := st.UpsertChannelHealthSnapshot(ctx, quality)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertChannelHealthSnapshot(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if len(st.upstreamIntelFactMutations[121]) != 1 {
		t.Fatalf("idempotent replay appended lineage: %+v", st.upstreamIntelFactMutations[121])
	}
	st.mu.Lock()
	st.bumpUpstreamIntelligenceFactVersionLocked(121, base.Add(time.Minute), UpstreamIntelligenceFactMutationUnknown, "")
	st.mu.Unlock()
	proof := st.memoryQualityOnlyFactAdvanceProof(121, 1, 2)
	if !proof.Complete || ValidQualityOnlyFactAdvanceProof(proof, 121, 1, 2) ||
		proof.Mutations[0].Kind != UpstreamIntelligenceFactMutationUnknown {
		t.Fatalf("explicit unknown was not preserved fail-closed: %+v", proof)
	}
}
