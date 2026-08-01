package store

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryUpstreamIntelligenceCurrentReadHidesOnlyConfirmedRemovals(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	base := time.Date(2026, 7, 24, 4, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return base.Add(10 * time.Minute) }
	seedMemoryIntelligenceOwner(t, st, 401, "inst", "conn")
	source, err := st.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source", UserID: 401, ConnectorID: "conn", InstanceID: "inst", LocalRef: "local",
		Mode: contracts.UpstreamSourceOwned, Provider: "sub2api", DisplayName: "owned", Status: contracts.UpstreamSourceActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	present := memoryChangeOffer("present", 401, source.ID, base, "g1", "m1", contracts.UpstreamPriceInput)
	finalizeMemorySnapshot(t, st, 401, source.ID, "present", base, contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete,
		[]contracts.UpstreamOfferObservation{present})
	assertCurrentOfferVisible(t, st, 401, "g1", "m1", true)

	// The first complete absence records suspicion but the last successful fact
	// remains visible. A partial run (even one carrying the same identity) and a
	// failed run are finalized facts, but neither is allowed to advance or reset
	// complete-snapshot deletion state.
	finalizeMemorySnapshot(t, st, 401, source.ID, "missing-one", base.Add(time.Minute), contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, nil)
	assertCurrentOfferVisible(t, st, 401, "g1", "m1", true)
	partial := memoryChangeOffer("partial", 401, source.ID, base.Add(2*time.Minute), "g1", "m1", contracts.UpstreamPriceInput)
	finalizeMemorySnapshot(t, st, 401, source.ID, "partial", base.Add(2*time.Minute), contracts.UpstreamCollectionPartial, contracts.UpstreamCoveragePartial,
		[]contracts.UpstreamOfferObservation{partial})
	finalizeMemorySnapshot(t, st, 401, source.ID, "failed", base.Add(3*time.Minute), contracts.UpstreamCollectionFailed, contracts.UpstreamCoverageUnavailable, nil)
	assertCurrentOfferVisible(t, st, 401, "g1", "m1", true)

	absences, err := st.ListUpstreamSnapshotAbsences(ctx, 401, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, absence := range absences {
		if absence.ComparisonKey == upstreamGroupAbsenceKey("g1") &&
			(absence.ConsecutiveCompleteRuns != 1 || absence.LastAbsentRunID != "missing-one") {
			t.Fatalf("partial/failed run changed group absence: %+v", absence)
		}
	}
	changes, err := st.ListUpstreamChangeEvents(ctx, contracts.UpstreamChangeEventFilter{UserID: 401, SourceID: source.ID})
	if err != nil || len(changes) != 0 {
		t.Fatalf("unconfirmed removal changes=%+v err=%v", changes, err)
	}

	finalizeMemorySnapshot(t, st, 401, source.ID, "missing-two", base.Add(4*time.Minute), contracts.UpstreamCollectionSucceeded, contracts.UpstreamCoverageComplete, nil)
	assertCurrentOfferVisible(t, st, 401, "g1", "m1", false)
	changes, err = st.ListUpstreamChangeEvents(ctx, contracts.UpstreamChangeEventFilter{UserID: 401, SourceID: source.ID})
	if err != nil || len(changes) != 1 || changes[0].Type != contracts.UpstreamChangeGroupRemoved {
		t.Fatalf("confirmed removal changes=%+v err=%v", changes, err)
	}
}

func assertCurrentOfferVisible(t *testing.T, st *MemoryStore, userID int64, groupKey, modelKey string, want bool) {
	t.Helper()
	snapshot, err := st.ReadUpstreamIntelligenceCurrent(context.Background(), userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, offer := range snapshot.Offers {
		if offer.GroupKey == groupKey && offer.ModelKey == modelKey {
			found = true
			break
		}
	}
	if found != want {
		t.Fatalf("current offer %s/%s visible=%v, want %v; offers=%+v", groupKey, modelKey, found, want, snapshot.Offers)
	}
}

func TestMemoryUpstreamIntelligenceCurrentReadIsFinalizedLatestAndOwnerScoped(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	base := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return base.Add(3 * time.Hour) }
	seedMemoryIntelligenceOwner(t, st, 501, "inst-a", "conn-a")
	seedMemoryIntelligenceOwner(t, st, 502, "inst-b", "conn-b")

	sourceA, err := st.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source-a", UserID: 501, ConnectorID: "conn-a", InstanceID: "inst-a", LocalRef: "local-a",
		Mode: contracts.UpstreamSourceExternal, Provider: "sub2api", DisplayName: "A", Status: contracts.UpstreamSourceActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceB, err := st.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source-b", UserID: 502, ConnectorID: "conn-b", InstanceID: "inst-b", LocalRef: "local-b",
		Mode: contracts.UpstreamSourceExternal, Provider: "sub2api", DisplayName: "B", Status: contracts.UpstreamSourceActive,
	})
	if err != nil {
		t.Fatal(err)
	}

	finalizeReadFixture(t, st, 501, sourceA.ID, "conn-a", "run-old", base, "10")
	finalizeReadFixture(t, st, 501, sourceA.ID, "conn-a", "run-new", base.Add(time.Hour), "20")
	// PostgreSQL and Memory use owner-scoped run/fact identities. Reusing the
	// current owner's newest run ID for another owner must not replace or leak
	// either owner's facts into this read.
	finalizeReadFixture(t, st, 502, sourceB.ID, "conn-b", "run-new", base.Add(2*time.Hour), "99")

	// An accepted but not finalized fact is not visible in the read model.
	pending := newMemoryIntelligenceRun("run-pending", 501, sourceA.ID, "conn-a", base.Add(2*time.Hour))
	pending.ManifestHash = manifestHash(t, hash64("read-run-pending"))
	if _, err := st.CreateUpstreamCollectionRun(ctx, pending); err != nil {
		t.Fatal(err)
	}
	pendingOffer := memoryOffer(pending.ID, 501, sourceA.ID, pending.ObservedAt)
	pendingOffer.ID, pendingOffer.PublishedUnitPrice = "offer-pending", readDecimal("777")
	if _, err := st.AppendUpstreamOfferObservation(ctx, pendingOffer); err != nil {
		t.Fatal(err)
	}

	snapshot, err := st.ReadUpstreamIntelligenceCurrent(ctx, 501, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.UserID != 501 || snapshot.FactVersion.FactVersion != 2 || !snapshot.GeneratedAt.Equal(base.Add(3*time.Hour)) {
		t.Fatalf("metadata=%+v", snapshot)
	}
	if len(snapshot.Sources) != 1 || snapshot.Sources[0].ID != sourceA.ID || len(snapshot.LatestRuns) != 1 || snapshot.LatestRuns[0].ID != "run-new" {
		t.Fatalf("source/run snapshot=%+v", snapshot)
	}
	if len(snapshot.Offers) != 1 || snapshot.Offers[0].RunID != "run-new" || snapshot.Offers[0].PublishedUnitPrice == nil || *snapshot.Offers[0].PublishedUnitPrice != "20" {
		t.Fatalf("current offers=%+v", snapshot.Offers)
	}
	if len(snapshot.Wallets) != 1 || snapshot.Wallets[0].RunID != "run-new" {
		t.Fatalf("current wallets=%+v", snapshot.Wallets)
	}
}

func TestMemoryUpstreamIntelligenceCurrentReadUsesReferenceTimeForChangeCutoff(t *testing.T) {
	st := NewMemoryStore(time.Now())
	serverNow := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	referenceTime := serverNow.Add(-10 * time.Minute)
	st.now = func() time.Time { return serverNow }
	st.upstreamIntelChanges = []contracts.UpstreamChangeEvent{
		{ID: "before-reference-cutoff", UserID: 801, ConfirmedAt: referenceTime.Add(-7*24*time.Hour - time.Second)},
		{ID: "at-reference-cutoff", UserID: 801, ConfirmedAt: referenceTime.Add(-7 * 24 * time.Hour)},
		{ID: "after-reference-cutoff", UserID: 801, ConfirmedAt: referenceTime.Add(-7*24*time.Hour + time.Second)},
	}
	snapshot, err := st.ReadUpstreamIntelligenceCurrent(context.Background(), 801, &referenceTime)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.GeneratedAt.Equal(referenceTime) || len(snapshot.Changes) != 2 ||
		snapshot.Changes[0].ID != "after-reference-cutoff" || snapshot.Changes[1].ID != "at-reference-cutoff" {
		t.Fatalf("reference-time snapshot=%+v", snapshot)
	}
}

func TestMemoryUpstreamIntelligenceEvidenceRequiresFinalizedOwnerFact(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	base := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	seedMemoryIntelligenceOwner(t, st, 601, "inst", "conn")
	seedMemoryIntelligenceOwner(t, st, 602, "inst-foreign", "conn-foreign")
	source, err := st.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source", UserID: 601, ConnectorID: "conn", InstanceID: "inst", LocalRef: "local",
		Mode: contracts.UpstreamSourceOwned, Provider: "sub2api", DisplayName: "owned", Status: contracts.UpstreamSourceActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	finalizeReadFixture(t, st, 601, source.ID, "conn", "run-final", base, "10")

	evidence, err := st.ReadUpstreamIntelligenceEvidence(ctx, 601, "offer-run-final")
	if err != nil || evidence.Offer == nil || evidence.Run == nil || evidence.Source.ID != source.ID || evidence.FactVersion.FactVersion != 1 {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	if _, err := st.ReadUpstreamIntelligenceEvidence(ctx, 602, "offer-run-final"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner evidence err=%v", err)
	}

	pending := newMemoryIntelligenceRun("run-pending", 601, source.ID, "conn", base.Add(time.Hour))
	pending.ManifestHash = manifestHash(t, hash64("read-pending"))
	if _, err := st.CreateUpstreamCollectionRun(ctx, pending); err != nil {
		t.Fatal(err)
	}
	pendingOffer := memoryOffer(pending.ID, 601, source.ID, pending.ObservedAt)
	pendingOffer.ID = "offer-pending"
	if _, err := st.AppendUpstreamOfferObservation(ctx, pendingOffer); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReadUpstreamIntelligenceEvidence(ctx, 601, pendingOffer.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unfinalized evidence err=%v", err)
	}
}

func TestMemoryUpstreamIntelligenceReadsDeepCopyMutableFacts(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	base := time.Date(2026, 7, 24, 6, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return base.Add(time.Hour) }
	seedMemoryIntelligenceOwner(t, st, 701, "inst", "conn")

	lastRunAt, lastSuccessAt, nextPollAt := base, base, base.Add(5*time.Minute)
	completedAt, validUntil, firstAbsentAt, verifiedAt := base, base.Add(2*time.Hour), base.Add(10*time.Minute), base.Add(-time.Minute)
	balance, walletConfidence := contracts.CanonicalDecimal("101.25"), contracts.CanonicalDecimal("0.7")
	group, recharge, price := contracts.CanonicalDecimal("0.8"), contracts.CanonicalDecimal("2"), contracts.CanonicalDecimal("15")
	effectiveMultiplier, effectiveCost, offerConfidence := contracts.CanonicalDecimal("0.4"), contracts.CanonicalDecimal("6"), contracts.CanonicalDecimal("0.8")
	absolute, percentage := contracts.CanonicalDecimal("1.5"), contracts.CanonicalDecimal("10")

	source := contracts.UpstreamIntelligenceSource{
		ID: "source-copy", UserID: 701, ConnectorID: "conn", InstanceID: "inst", LocalRef: "local-copy",
		Mode: contracts.UpstreamSourceExternal, Provider: "sub2api", DisplayName: "copy", Status: contracts.UpstreamSourceActive,
		LastRunAt: &lastRunAt, LastSuccessAt: &lastSuccessAt, NextPollAt: &nextPollAt,
	}
	run := contracts.UpstreamCollectionRun{
		ID: "run-copy", UserID: 701, SourceID: source.ID, ConnectorID: "conn", Trigger: contracts.UpstreamCollectionScheduled,
		Status: contracts.UpstreamCollectionSucceeded, Coverage: contracts.UpstreamCoverageComplete,
		StartedAt: base.Add(-time.Minute), ObservedAt: base, ReceivedAt: base, CompletedAt: &completedAt,
		BatchCount: 1, FactCount: 2, PageCount: 1, FinalizedFactVersion: 1,
	}
	wallet := contracts.UpstreamWalletObservation{
		ID: "wallet-copy", RunID: run.ID, UserID: 701, SourceID: source.ID, BalanceAmount: &balance,
		UnitKind: contracts.UpstreamWalletFiat, Currency: "USD", Accuracy: contracts.UpstreamEvidenceEstimated,
		Coverage: contracts.UpstreamCoverageComplete, Confidence: &walletConfidence, ObservedAt: base,
		ReceivedAt: base, FreshUntil: base.Add(time.Hour), MissingFields: []string{"wallet-original"},
	}
	offer := contracts.UpstreamOfferObservation{
		ID: "offer-copy", RunID: run.ID, UserID: 701, SourceID: source.ID, GroupKey: "g", ModelKey: "m",
		PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", GroupMultiplier: &group,
		RechargeYield: &recharge, PublishedUnitPrice: &price, PerTokens: 1_000_000,
		EffectiveMultiplier: &effectiveMultiplier, EffectiveUnitCost: &effectiveCost, FormulaVersion: "effective-cost/v1",
		Accuracy: contracts.UpstreamEvidenceEstimated, Coverage: contracts.UpstreamCoverageComplete, Confidence: &offerConfidence,
		ObservedAt: base, EffectiveAt: base, ReceivedAt: base, FreshUntil: base.Add(time.Hour), ValidUntil: &validUntil,
		MissingFields: []string{"offer-original"}, AdapterSchemaVersion: 1,
	}
	absence := UpstreamSnapshotAbsence{
		UserID: 701, SourceID: source.ID, ComparisonKey: upstreamGroupAbsenceKey("not-this-offer"),
		ConsecutiveCompleteRuns: 1, FirstAbsentAt: &firstAbsentAt, UpdatedAt: base,
	}
	change := contracts.UpstreamChangeEvent{
		ID: "change-copy", UserID: 701, SourceID: source.ID, Type: contracts.UpstreamChangePriceIncreased,
		Fingerprint: "copy-fingerprint", AbsoluteChange: &absolute, PercentageChange: &percentage,
		FirstDetectedAt: base, ConfirmedAt: base, Severity: contracts.UpstreamChangeWarning,
		ImpactScope: map[string]string{"scope": "original"}, CreatedAt: base,
	}
	link := contracts.UpstreamIntelligenceLink{
		ID: "link-copy", UserID: 701, IntelligenceSourceID: source.ID, Scope: contracts.UpstreamLinkSourceIdentity,
		UpstreamSourceIdentity: "opaque-upstream", PriceDimension: contracts.UpstreamPriceInput,
		Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt,
	}
	st.channelAllocations["channel-copy"] = upstreamChannelAllocation{UserID: 701, SourceID: "opaque-upstream"}
	st.upstreamIntelSources = append(st.upstreamIntelSources, source)
	st.upstreamIntelRuns = append(st.upstreamIntelRuns, run)
	st.upstreamIntelWallets = append(st.upstreamIntelWallets, wallet)
	st.upstreamIntelOffers = append(st.upstreamIntelOffers, offer)
	st.upstreamIntelAbsences = append(st.upstreamIntelAbsences, absence)
	st.upstreamIntelChanges = append(st.upstreamIntelChanges, change)
	st.upstreamIntelLinks = append(st.upstreamIntelLinks, link)
	st.upstreamIntelVersions[701] = contracts.UpstreamIntelligenceFactVersion{UserID: 701, FactVersion: 1, UpdatedAt: base}

	first, err := st.ReadUpstreamIntelligenceCurrent(ctx, 701, nil)
	if err != nil {
		t.Fatal(err)
	}
	mutatedTime := base.Add(24 * time.Hour)
	mutatedDecimal := contracts.CanonicalDecimal("999")
	*first.Sources[0].LastRunAt, *first.Sources[0].LastSuccessAt, *first.Sources[0].NextPollAt = mutatedTime, mutatedTime, mutatedTime
	*first.LatestRuns[0].CompletedAt = mutatedTime
	*first.Wallets[0].BalanceAmount, *first.Wallets[0].Confidence = mutatedDecimal, mutatedDecimal
	first.Wallets[0].MissingFields[0] = "wallet-mutated"
	*first.Offers[0].GroupMultiplier, *first.Offers[0].RechargeYield = mutatedDecimal, mutatedDecimal
	*first.Offers[0].PublishedUnitPrice, *first.Offers[0].EffectiveMultiplier = mutatedDecimal, mutatedDecimal
	*first.Offers[0].EffectiveUnitCost, *first.Offers[0].Confidence = mutatedDecimal, mutatedDecimal
	*first.Offers[0].ValidUntil = mutatedTime
	first.Offers[0].MissingFields[0] = "offer-mutated"
	*first.Absences[0].FirstAbsentAt = mutatedTime
	*first.Changes[0].AbsoluteChange, *first.Changes[0].PercentageChange = mutatedDecimal, mutatedDecimal
	first.Changes[0].ImpactScope["scope"] = "mutated"
	*first.Links[0].VerifiedAt = mutatedTime

	second, err := st.ReadUpstreamIntelligenceCurrent(ctx, 701, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Sources[0].LastRunAt.Equal(base) || !second.LatestRuns[0].CompletedAt.Equal(base) ||
		*second.Wallets[0].BalanceAmount != "101.25" || *second.Wallets[0].Confidence != "0.7" ||
		second.Wallets[0].MissingFields[0] != "wallet-original" ||
		*second.Offers[0].GroupMultiplier != "0.8" || *second.Offers[0].RechargeYield != "2" ||
		*second.Offers[0].PublishedUnitPrice != "15" || *second.Offers[0].EffectiveMultiplier != "0.4" ||
		*second.Offers[0].EffectiveUnitCost != "6" || *second.Offers[0].Confidence != "0.8" ||
		!second.Offers[0].ValidUntil.Equal(validUntil) || second.Offers[0].MissingFields[0] != "offer-original" ||
		!second.Absences[0].FirstAbsentAt.Equal(firstAbsentAt) || *second.Changes[0].AbsoluteChange != "1.5" ||
		*second.Changes[0].PercentageChange != "10" || second.Changes[0].ImpactScope["scope"] != "original" ||
		!second.Links[0].VerifiedAt.Equal(verifiedAt) {
		t.Fatalf("mutating current read polluted store: %+v", second)
	}

	evidence, err := st.ReadUpstreamIntelligenceEvidence(ctx, 701, offer.ID)
	if err != nil || evidence.Offer == nil || evidence.Run == nil {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	*evidence.Source.LastRunAt = mutatedTime
	*evidence.Run.CompletedAt = mutatedTime
	*evidence.Offer.EffectiveUnitCost = mutatedDecimal
	evidence.Offer.MissingFields[0] = "evidence-mutated"
	again, err := st.ReadUpstreamIntelligenceEvidence(ctx, 701, offer.ID)
	if err != nil || again.Offer == nil || again.Run == nil || !again.Source.LastRunAt.Equal(base) ||
		!again.Run.CompletedAt.Equal(base) || *again.Offer.EffectiveUnitCost != "6" || again.Offer.MissingFields[0] != "offer-original" {
		t.Fatalf("mutating evidence read polluted store: %+v err=%v", again, err)
	}
}

func TestMemoryUpstreamIntelligenceCurrentResolvesUniqueLinksAndSelectsWorstTrustworthyQuality(t *testing.T) {
	ctx := context.Background()
	reference := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(reference)
	st.now = func() time.Time { return reference }
	seedMemoryIntelligenceOwner(t, st, 801, "instance-a", "connector-a")
	seedMemoryIntelligenceOwner(t, st, 802, "instance-foreign", "connector-foreign")
	st.instances = append(st.instances,
		contracts.Instance{ID: "instance-b", UserID: 801},
		contracts.Instance{ID: "instance-future", UserID: 801},
		contracts.Instance{ID: "instance-not-owner", UserID: 802})
	st.channelAllocations["channel-owner"] = upstreamChannelAllocation{UserID: 801, SourceID: "source-identity"}
	st.channelAllocations["channel-foreign"] = upstreamChannelAllocation{UserID: 802, SourceID: "foreign-identity"}
	verifiedAt := reference.Add(-time.Hour)
	st.upstreamIntelLinks = append(st.upstreamIntelLinks,
		contracts.UpstreamIntelligenceLink{
			ID: "link-owner", UserID: 801, IntelligenceSourceID: "intelligence-owner",
			Scope: contracts.UpstreamLinkSourceIdentity, UpstreamSourceIdentity: "source-identity",
			PriceDimension: contracts.UpstreamPriceInput, Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt,
		},
		contracts.UpstreamIntelligenceLink{
			ID: "link-ambiguous", UserID: 801, IntelligenceSourceID: "intelligence-owner",
			Scope: contracts.UpstreamLinkSourceIdentity, UpstreamSourceIdentity: "ambiguous",
			PriceDimension: contracts.UpstreamPriceInput, Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt,
		})
	st.channelAllocations["ambiguous-a"] = upstreamChannelAllocation{UserID: 801, SourceID: "ambiguous"}
	st.channelAllocations["ambiguous-b"] = upstreamChannelAllocation{UserID: 801, SourceID: "ambiguous"}

	quality := func(id, channel, instance, model string, score, success, ttft, duration float64, samples int, at time.Time, state contracts.HealthState) contracts.ChannelHealthSnapshot {
		return contracts.ChannelHealthSnapshot{
			ID: id, ChannelID: channel, InstanceID: instance, Model: model, Window: contracts.Window5m,
			BucketStart: at.Truncate(time.Minute), CreatedAt: at, QualityScore: score,
			QualitySuccessRate: success, TTFTP95: ttft, DurationP95: duration,
			QualitySampleCount: samples, HealthState: state,
		}
	}
	st.channelSnapshots = append(st.channelSnapshots,
		quality("better", "channel-owner", "instance-a", "model-a", 90, .99, 100, 500, 20, reference.Add(-time.Minute), contracts.HealthHealthy),
		quality("worst", "channel-owner", "instance-b", "model-a", 70, .95, 200, 900, 10, reference.Add(-time.Minute), contracts.HealthDegraded),
		// Newer history in the same instance scope replaces the old row before
		// conservative instance selection.
		quality("old-worse", "channel-owner", "instance-a", "model-b", 40, .8, 500, 1000, 5, reference.Add(-4*time.Minute), contracts.HealthDegraded),
		quality("current-model-b", "channel-owner", "instance-a", "model-b", 80, .98, 150, 600, 15, reference.Add(-time.Minute), contracts.HealthHealthy),
		quality("unknown", "channel-owner", "instance-a", "model-unknown", 0, 0, 0, 0, 0, reference.Add(-time.Minute), contracts.HealthUnknown),
		quality("stale", "channel-owner", "instance-a", "model-stale", 10, .5, 900, 2000, 50, reference.Add(-6*time.Minute), contracts.HealthUnhealthy),
		quality("insufficient", "channel-owner", "instance-a", "model-insufficient", 60, .9, 300, 700, 1, reference.Add(-time.Minute), contracts.HealthUnknown),
		quality("unknown-worst", "channel-owner", "instance-a", "model-fail-closed", 99, .99, 10, 20, 100, reference.Add(-time.Minute), contracts.HealthUnknown),
		quality("healthy-peer", "channel-owner", "instance-b", "model-fail-closed", 100, 1, 1, 2, 100, reference.Add(-time.Minute), contracts.HealthHealthy),
		quality("nan", "channel-owner", "instance-a", "model-non-finite", math.NaN(), .9, 100, 500, 10, reference.Add(-time.Minute), contracts.HealthHealthy),
		quality("inf", "channel-owner", "instance-b", "model-non-finite", 80, .9, math.Inf(1), 500, 10, reference.Add(-time.Minute), contracts.HealthHealthy),
		func() contracts.ChannelHealthSnapshot {
			value := quality("wrong-window", "channel-owner", "instance-a", "model-30m", 1, .1, 900, 2000, 50, reference.Add(-time.Minute), contracts.HealthUnhealthy)
			value.Window = contracts.Window30m
			return value
		}(),
		quality("future", "channel-owner", "instance-a", "model-future", 1, .1, 1000, 3000, 50, reference.Add(time.Minute), contracts.HealthUnhealthy),
		quality("wrong-instance-owner", "channel-owner", "instance-not-owner", "model-cross-owner", 1, .1, 1000, 3000, 50, reference.Add(-time.Minute), contracts.HealthUnhealthy),
		quality("foreign", "channel-foreign", "instance-foreign", "model-a", 1, .1, 1000, 3000, 50, reference.Add(-time.Minute), contracts.HealthUnhealthy))

	snapshot, err := st.ReadUpstreamIntelligenceCurrent(ctx, 801, &reference)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.LinkResolutions) != 2 {
		t.Fatalf("resolutions=%+v", snapshot.LinkResolutions)
	}
	if got := snapshot.LinkResolutions[1]; got.LinkID != "link-owner" || got.ResolvedChannelID != "channel-owner" ||
		got.ResolvedChannelOwnerID != 801 || !got.TargetVerified {
		t.Fatalf("unique resolution=%+v", got)
	}
	if got := snapshot.LinkResolutions[0]; got.LinkID != "link-ambiguous" || got.ResolvedChannelID != "" || got.TargetVerified {
		t.Fatalf("ambiguous resolution must remain unverified: %+v", got)
	}
	wantQuality := []string{"worst", "current-model-b", "unknown-worst", "insufficient", "stale", "unknown"}
	if len(snapshot.QualitySnapshots) != len(wantQuality) {
		t.Fatalf("conservative quality=%+v", snapshot.QualitySnapshots)
	}
	for index, want := range wantQuality {
		if snapshot.QualitySnapshots[index].ID != want {
			t.Fatalf("quality[%d]=%q, want %q; all=%+v", index, snapshot.QualitySnapshots[index].ID, want, snapshot.QualitySnapshots)
		}
	}
}

func TestWorstUpstreamQualitySnapshotTieBreakIsFullyConservativeAndStable(t *testing.T) {
	base := contracts.ChannelHealthSnapshot{ID: "z", QualityScore: 50, QualitySuccessRate: .9, TTFTP95: 100, DurationP95: 500, QualitySampleCount: 20}
	checks := []contracts.ChannelHealthSnapshot{
		{ID: "success", QualityScore: 50, QualitySuccessRate: .8, TTFTP95: 1, DurationP95: 1, QualitySampleCount: 100},
		{ID: "ttft", QualityScore: 50, QualitySuccessRate: .9, TTFTP95: 200, DurationP95: 1, QualitySampleCount: 100},
		{ID: "duration", QualityScore: 50, QualitySuccessRate: .9, TTFTP95: 100, DurationP95: 600, QualitySampleCount: 100},
		{ID: "samples", QualityScore: 50, QualitySuccessRate: .9, TTFTP95: 100, DurationP95: 500, QualitySampleCount: 10},
		{ID: "a", QualityScore: 50, QualitySuccessRate: .9, TTFTP95: 100, DurationP95: 500, QualitySampleCount: 20},
	}
	for _, candidate := range checks {
		if !worseUpstreamQualitySnapshot(candidate, base) {
			t.Errorf("candidate should conservatively precede base: %+v", candidate)
		}
	}
}

func TestMemoryUpstreamQualitySnapshotRejectsEveryNonFiniteFrontierMetric(t *testing.T) {
	base := contracts.ChannelHealthSnapshot{QualityScore: 50, QualitySuccessRate: .9, TTFTP95: 100, DurationP95: 500}
	checks := []struct {
		name   string
		mutate func(*contracts.ChannelHealthSnapshot)
	}{
		{"quality nan", func(value *contracts.ChannelHealthSnapshot) { value.QualityScore = math.NaN() }},
		{"quality infinity", func(value *contracts.ChannelHealthSnapshot) { value.QualityScore = math.Inf(1) }},
		{"success negative infinity", func(value *contracts.ChannelHealthSnapshot) { value.QualitySuccessRate = math.Inf(-1) }},
		{"ttft nan", func(value *contracts.ChannelHealthSnapshot) { value.TTFTP95 = math.NaN() }},
		{"duration infinity", func(value *contracts.ChannelHealthSnapshot) { value.DurationP95 = math.Inf(1) }},
	}
	for _, check := range checks {
		value := base
		check.mutate(&value)
		if projectableUpstreamQualitySnapshot(value) {
			t.Errorf("%s unexpectedly projectable", check.name)
		}
	}
}

func TestPrefixedUpstreamReadColumnsQualifiesCasts(t *testing.T) {
	got := prefixedUpstreamReadColumns("offer", "run_id,published_unit_price::text, observed_at")
	want := "offer.run_id,offer.published_unit_price::text,offer.observed_at"
	if got != want {
		t.Fatalf("qualified columns=%q, want %q", got, want)
	}
}

func TestPostgresUpstreamIntelligenceCurrentReadUsesOneOwnerScopedFinalizedSnapshot(t *testing.T) {
	implementation, err := os.ReadFile("upstream_intelligence_read.go")
	if err != nil {
		t.Fatalf("read postgres current implementation: %v", err)
	}
	code := string(implementation)
	current := postgresFunctionSource(t, code,
		"func (s *PostgresStore) ReadUpstreamIntelligenceCurrent",
		"func (s *PostgresStore) ReadUpstreamIntelligenceEvidence")
	for _, required := range []string{
		"pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}",
		"SELECT transaction_timestamp()",
		"readUpstreamFactVersionQuery(ctx, tx, userID",
		"queryUpstreamReadSources(ctx, tx, userID",
		"queryUpstreamReadLatestRuns(ctx, tx, userID",
		"queryUpstreamReadWallets(ctx, tx, userID",
		"queryUpstreamReadOffers(ctx, tx, userID",
		"queryUpstreamReadAbsences(ctx, tx, userID",
		"queryUpstreamReadChanges(ctx, tx, userID",
		"queryUpstreamReadLinks(ctx, tx, userID",
		"queryUpstreamReadLinkResolutions(ctx, tx, userID",
		"queryUpstreamReadQualitySnapshots(ctx, tx, userID, snapshot.GeneratedAt",
		"tx.Commit(ctx)",
	} {
		if !strings.Contains(current, required) {
			t.Errorf("PostgreSQL current read lacks consistency guard %q", required)
		}
	}
	assertSourceOrder(t, current, "SELECT transaction_timestamp()", "readUpstreamFactVersionQuery(ctx, tx, userID",
		"generated_at must come from the same transaction before the fact projection")
	assertSourceOrder(t, current, "readUpstreamFactVersionQuery(ctx, tx, userID", "queryUpstreamReadSources(ctx, tx, userID",
		"fact_version must be captured before reading the snapshot projection")
	assertSourceOrder(t, current, "queryUpstreamReadQualitySnapshots(ctx, tx, userID", "tx.Commit(ctx)",
		"all owner projections must complete before committing the consistent read")

	checks := []struct {
		start string
		end   string
		want  []string
	}{
		{
			start: "func queryUpstreamReadSources", end: "func queryUpstreamReadLatestRuns",
			want: []string{"WHERE user_id=$1", "ORDER BY id LIMIT $2"},
		},
		{
			start: "func queryUpstreamReadLatestRuns", end: "func queryUpstreamReadWallets",
			want: []string{"WHERE user_id=$1 AND finalized_fact_version>0", "PARTITION BY source_id"},
		},
		{
			start: "func queryUpstreamReadWallets", end: "func queryUpstreamReadOffers",
			want: []string{
				"JOIN upstream_collection_runs AS run ON run.user_id=wallet.user_id AND run.id=wallet.run_id",
				"WHERE wallet.user_id=$1 AND run.finalized_fact_version>0",
			},
		},
		{
			start: "func queryUpstreamReadOffers", end: "func queryUpstreamReadAbsences",
			want: []string{
				"JOIN upstream_collection_runs AS run ON run.user_id=offer.user_id AND run.id=offer.run_id",
				"WHERE offer.user_id=$1 AND run.finalized_fact_version>0",
				"PARTITION BY offer.source_id,offer.group_key,offer.model_key,offer.price_dimension",
			},
		},
		{
			start: "func queryUpstreamReadAbsences", end: "func queryUpstreamReadChanges",
			want: []string{"WHERE user_id=$1", "source_id"},
		},
		{
			start: "func queryUpstreamReadChanges", end: "func queryUpstreamReadLinks",
			want: []string{"WHERE user_id=$1", "confirmed_at >= $2"},
		},
		{
			start: "func queryUpstreamReadLinks", end: "func queryUpstreamReadLinkResolutions",
			want: []string{"WHERE user_id=$1", "LIMIT $2"},
		},
		{
			start: "func queryUpstreamReadLinkResolutions", end: "func queryUpstreamReadQualitySnapshots",
			want: []string{
				"FROM upstream_channel_allocations AS allocation",
				"allocation.user_id=link.user_id",
				"COUNT(*) AS match_count",
				"target.match_count=1",
				"WHERE link.user_id=$1",
			},
		},
		{
			start: "func queryUpstreamReadQualitySnapshots", end: "func queryUniqueUpstreamWalletEvidence",
			want: []string{
				"JOIN upstream_channel_allocations AS allocation",
				"allocation.channel_id=snapshot.channel_id AND allocation.user_id=$1",
				"JOIN instances AS instance",
				"instance.id=snapshot.instance_id AND instance.user_id=allocation.user_id",
				"snapshot.bucket_start <= $2 AND snapshot.created_at <= $2",
				"snapshot.\"window\"='5m'",
				"PARTITION BY channel_id,model",
				"ORDER BY (health_state='unknown') DESC",
				"quality_score NOT IN ('NaN'::double precision,'Infinity'::double precision,'-Infinity'::double precision)",
				"quality_success_rate NOT IN ('NaN'::double precision,'Infinity'::double precision,'-Infinity'::double precision)",
				"ttft_p95 NOT IN ('NaN'::double precision,'Infinity'::double precision,'-Infinity'::double precision)",
				"duration_p95 NOT IN ('NaN'::double precision,'Infinity'::double precision,'-Infinity'::double precision)",
			},
		},
	}
	for _, check := range checks {
		query := postgresFunctionSource(t, code, check.start, check.end)
		for _, required := range check.want {
			if !strings.Contains(query, required) {
				t.Errorf("%s lacks owner/finalization predicate %q", check.start, required)
			}
		}
	}
}

func TestPostgresUpstreamIntelligenceEvidenceReadIsOwnerScopedAndFinalizedOnly(t *testing.T) {
	implementation, err := os.ReadFile("upstream_intelligence_read.go")
	if err != nil {
		t.Fatalf("read postgres evidence implementation: %v", err)
	}
	code := string(implementation)
	evidence := postgresFunctionSource(t, code,
		"func (s *PostgresStore) ReadUpstreamIntelligenceEvidence",
		"func readUpstreamFactVersionQuery")
	for _, required := range []string{
		"pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}",
		"SELECT transaction_timestamp()",
		"readUpstreamFactVersionQuery(ctx, tx, userID",
		"queryUniqueUpstreamWalletEvidence(ctx, tx, userID, evidenceID)",
		"queryUniqueUpstreamOfferEvidence(ctx, tx, userID, evidenceID)",
		"queryUniqueUpstreamChangeEvidence(ctx, tx, userID, evidenceID)",
		"WHERE user_id=$1 AND id=$2",
		"tx.Commit(ctx)",
	} {
		if !strings.Contains(evidence, required) {
			t.Errorf("PostgreSQL evidence read lacks consistency/owner guard %q", required)
		}
	}

	for _, check := range []struct {
		start string
		end   string
		want  []string
	}{
		{
			start: "func queryUniqueUpstreamWalletEvidence", end: "func queryUniqueUpstreamOfferEvidence",
			want: []string{
				"wallet.user_id=$1 AND wallet.id=$2",
				"run.user_id=wallet.user_id AND run.id=wallet.run_id AND run.finalized_fact_version>0",
			},
		},
		{
			start: "func queryUniqueUpstreamOfferEvidence", end: "func queryUniqueUpstreamChangeEvidence",
			want: []string{
				"offer.user_id=$1 AND offer.id=$2",
				"run.user_id=offer.user_id AND run.id=offer.run_id AND run.finalized_fact_version>0",
			},
		},
		{
			start: "func queryUniqueUpstreamChangeEvidence", end: "func upstreamReadRunNewer",
			want: []string{"WHERE user_id=$1 AND id=$2"},
		},
	} {
		query := postgresFunctionSource(t, code, check.start, check.end)
		for _, required := range check.want {
			if !strings.Contains(query, required) {
				t.Errorf("%s lacks owner/finalization predicate %q", check.start, required)
			}
		}
	}
}

func finalizeReadFixture(t *testing.T, st *MemoryStore, userID int64, sourceID, connectorID, runID string, observedAt time.Time, price string) {
	t.Helper()
	run := newMemoryIntelligenceRun(runID, userID, sourceID, connectorID, observedAt)
	run.FactCount = 2
	payloadHash := hash64("read-" + runID)
	run.ManifestHash = manifestHash(t, payloadHash)
	offer := memoryOffer(runID, userID, sourceID, observedAt)
	offer.PublishedUnitPrice, offer.EffectiveUnitCost = readDecimal(price), readDecimal(price)
	offer.SettlementCurrency = "USD"
	wallet := memoryWallet(runID, userID, sourceID, observedAt)
	wallet.ID, wallet.BalanceAmount = "wallet-"+runID, readDecimal(price)
	if _, err := st.CreateUpstreamCollectionRun(context.Background(), run); err != nil {
		t.Fatalf("create %s: %v", runID, err)
	}
	if _, _, err := st.UpsertUpstreamIntelligenceIngestBatch(context.Background(), UpstreamIntelligenceIngestBatch{
		RunID: runID, UserID: userID, SourceID: sourceID, BatchNo: 0, BatchCount: 1,
		PayloadHash: payloadHash, ManifestHash: run.ManifestHash, WalletCount: 1, OfferCount: 1,
	}); err != nil {
		t.Fatalf("batch %s: %v", runID, err)
	}
	if _, err := st.AppendUpstreamWalletObservation(context.Background(), wallet); err != nil {
		t.Fatalf("wallet %s: %v", runID, err)
	}
	if _, err := st.AppendUpstreamOfferObservation(context.Background(), offer); err != nil {
		t.Fatalf("offer %s: %v", runID, err)
	}
	if _, _, err := st.FinalizeUpstreamCollectionRun(context.Background(), userID, runID); err != nil {
		t.Fatalf("finalize %s: %v", runID, err)
	}
}

func readDecimal(value string) *contracts.CanonicalDecimal {
	decimal := contracts.CanonicalDecimal(value)
	return &decimal
}
