package upstreamrecommendation

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestBuildBindsVersionsIdentityEvidenceAndCalculatesConservativeSavings(t *testing.T) {
	input := candidate()
	got, err := Build("rec-1", input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != contracts.UpstreamRecommendationOpen || len(got.Fingerprint) != 64 ||
		got.UserID != 42 || got.IntelligenceFactVersion != 7 || got.CostLedgerFactVersion != 9 || got.LinkFactVersion != 4 || got.PlanGeneration != 11 {
		t.Fatalf("identity/version binding wrong: %+v", got)
	}
	if got.Savings.AmountLower != "1" || got.Savings.AmountExpected != "3" || got.Savings.AmountUpper != "5" ||
		got.Savings.PercentLower != "0.083333333333333333" || got.Savings.PercentExpected != "0.3" || got.Savings.PercentUpper != "0.625" {
		t.Fatalf("conservative savings wrong: %+v", got.Savings)
	}
}

func TestBuildFingerprintStableAcrossOrderAndPresentationText(t *testing.T) {
	first := candidate()
	second := candidate()
	second.AffectedPlanIDs[0], second.AffectedPlanIDs[1] = second.AffectedPlanIDs[1], second.AffectedPlanIDs[0]
	second.EvidenceIDs[0], second.EvidenceIDs[1] = second.EvidenceIDs[1], second.EvidenceIDs[0]
	second.Constraints[0], second.Constraints[2] = second.Constraints[2], second.Constraints[0]
	second.Constraints[0].Explanation = "different localized prose"
	left, err := Build("rec-left", first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Build("rec-right", second)
	if err != nil {
		t.Fatal(err)
	}
	if left.Fingerprint != right.Fingerprint {
		t.Fatalf("ordering/prose changed fingerprint: %s != %s", left.Fingerprint, right.Fingerprint)
	}
}

func TestBuildFingerprintChangesForEveryExecutionRelevantBinding(t *testing.T) {
	base := candidate()
	want, err := Fingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*contracts.UpstreamRecommendationCandidate)
	}{
		{"owner", func(v *contracts.UpstreamRecommendationCandidate) { v.UserID++ }},
		{"intelligence version", func(v *contracts.UpstreamRecommendationCandidate) { v.IntelligenceFactVersion++ }},
		{"cost version", func(v *contracts.UpstreamRecommendationCandidate) { v.CostLedgerFactVersion++ }},
		{"link version", func(v *contracts.UpstreamRecommendationCandidate) { v.LinkFactVersion++ }},
		{"generation", func(v *contracts.UpstreamRecommendationCandidate) { v.PlanGeneration++ }},
		{"source", func(v *contracts.UpstreamRecommendationCandidate) { v.ToSourceID = "source-3" }},
		{"channel", func(v *contracts.UpstreamRecommendationCandidate) { v.ToChannelID = "channel-3" }},
		{"model", func(v *contracts.UpstreamRecommendationCandidate) { v.ModelKey = "model-b" }},
		{"group", func(v *contracts.UpstreamRecommendationCandidate) { v.ToGroupKey = "group-b" }},
		{"dimension", func(v *contracts.UpstreamRecommendationCandidate) { v.PriceDimension = contracts.UpstreamPriceOutput }},
		{"evidence", func(v *contracts.UpstreamRecommendationCandidate) { v.EvidenceIDs[0] = "price-new" }},
		{"strategy", func(v *contracts.UpstreamRecommendationCandidate) { v.StrategyVersion = "strategy-v2" }},
		{"formula", func(v *contracts.UpstreamRecommendationCandidate) { v.FormulaVersion = "formula-v2" }},
		{"cost", func(v *contracts.UpstreamRecommendationCandidate) {
			v.ToCost.Lower, v.ToCost.Expected, v.ToCost.Upper = "6", "6", "6"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := candidate()
			test.mutate(&changed)
			got, fingerprintErr := Fingerprint(changed)
			if fingerprintErr != nil {
				t.Fatal(fingerprintErr)
			}
			if got == want {
				t.Fatalf("%s did not change fingerprint", test.name)
			}
		})
	}
}

func TestBuildFailsClosedForBlockedUnknownMissingOrDuplicateConstraints(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*contracts.UpstreamRecommendationCandidate)
		want   error
	}{
		{"blocked", func(v *contracts.UpstreamRecommendationCandidate) {
			v.Constraints[0].Status = contracts.UpstreamRecommendationConstraintBlocked
			v.Constraints[0].ReasonCode = "below_floor"
		}, ErrUnsafeCandidate},
		{"unknown", func(v *contracts.UpstreamRecommendationCandidate) {
			v.Constraints[1].Status = contracts.UpstreamRecommendationConstraintUnknown
			v.Constraints[1].ReasonCode = "missing"
		}, ErrUnsafeCandidate},
		{"missing", func(v *contracts.UpstreamRecommendationCandidate) { v.Constraints = v.Constraints[:2] }, ErrInvalidCandidate},
		{"duplicate", func(v *contracts.UpstreamRecommendationCandidate) { v.Constraints[2].Kind = v.Constraints[1].Kind }, ErrInvalidCandidate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := candidate()
			test.mutate(&input)
			_, err := Build("rec-1", input)
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
		})
	}
}

func TestBuildRejectsUnprovenSavingsAndMalformedComparableDimensions(t *testing.T) {
	tests := []func(*contracts.UpstreamRecommendationCandidate){
		func(v *contracts.UpstreamRecommendationCandidate) { v.ToCost.Upper = v.FromCost.Lower },
		func(v *contracts.UpstreamRecommendationCandidate) { v.SettlementCurrency = "usd" },
		func(v *contracts.UpstreamRecommendationCandidate) { v.PerTokens = 0 },
		func(v *contracts.UpstreamRecommendationCandidate) { v.PriceDimension = "" },
		func(v *contracts.UpstreamRecommendationCandidate) { v.FromCost.Lower = "8.00" },
		func(v *contracts.UpstreamRecommendationCandidate) { v.FromChannelID = v.ToChannelID },
	}
	for index, mutate := range tests {
		input := candidate()
		mutate(&input)
		if _, err := Build("rec-1", input); err == nil {
			t.Fatalf("unsafe candidate %d accepted", index)
		}
	}
}

func TestValidateCurrentDetectsFactLinkPlanMappingAndEvidenceStaleness(t *testing.T) {
	recommendation, err := Build("rec-1", candidate())
	if err != nil {
		t.Fatal(err)
	}
	current := currentFacts(recommendation, recommendation.CreatedAt.Add(time.Minute))
	if got := ValidateCurrent(recommendation, current); !got.Current || len(got.Reasons) != 0 {
		t.Fatalf("fresh facts stale: %+v", got)
	}
	current.IntelligenceFactVersion++
	current.CostLedgerFactVersion++
	current.LinkFactVersion++
	current.PlanGeneration++
	current.ToChannelID = "remapped"
	current.EvidenceIDs[0] = "changed"
	got := ValidateCurrent(recommendation, current)
	for _, reason := range []contracts.UpstreamRecommendationStaleReason{
		contracts.UpstreamRecommendationStaleIntelligenceVersion,
		contracts.UpstreamRecommendationStaleCostVersion,
		contracts.UpstreamRecommendationStaleLinkVersion,
		contracts.UpstreamRecommendationStalePlanGeneration,
		contracts.UpstreamRecommendationStaleMapping,
		contracts.UpstreamRecommendationStaleEvidence,
	} {
		assertStale(t, got, reason)
	}
}

func TestValidateCurrentExpiresAtBoundary(t *testing.T) {
	recommendation, err := Build("rec-1", candidate())
	if err != nil {
		t.Fatal(err)
	}
	current := currentFacts(recommendation, recommendation.ExpiresAt)
	got := ValidateCurrent(recommendation, current)
	if got.Current {
		t.Fatal("now == expires_at treated as current")
	}
	assertStale(t, got, contracts.UpstreamRecommendationStaleExpired)
}

func TestTransitionCoversFullLifecycle(t *testing.T) {
	recommendation, err := Build("rec-1", candidate())
	if err != nil {
		t.Fatal(err)
	}
	now := recommendation.CreatedAt.Add(time.Minute)
	steps := []struct {
		event  contracts.UpstreamRecommendationEvent
		status contracts.UpstreamRecommendationStatus
	}{
		{contracts.UpstreamRecommendationEvent{Type: contracts.UpstreamRecommendationEventStartShadow, UserID: 42, Now: now}, contracts.UpstreamRecommendationShadowing},
		{contracts.UpstreamRecommendationEvent{Type: contracts.UpstreamRecommendationEventShadowPassed, UserID: 42, Now: now}, contracts.UpstreamRecommendationReadyForDryRun},
		{contracts.UpstreamRecommendationEvent{Type: contracts.UpstreamRecommendationEventStartDryRun, UserID: 42, Now: now, DryRunID: "dry-1"}, contracts.UpstreamRecommendationDryRunning},
		{contracts.UpstreamRecommendationEvent{Type: contracts.UpstreamRecommendationEventDryRunPassed, UserID: 42, Now: now, DryRunID: "dry-1"}, contracts.UpstreamRecommendationDryRunPassed},
	}
	for _, step := range steps {
		recommendation, err = Transition(recommendation, step.event)
		if err != nil || recommendation.Status != step.status {
			t.Fatalf("transition to %s: result=%+v err=%v", step.status, recommendation, err)
		}
	}
}

func TestTransitionSupportsBlockedRetryDismissAndExpiry(t *testing.T) {
	recommendation, _ := Build("rec-1", candidate())
	now := recommendation.CreatedAt.Add(time.Minute)
	shadowing, _ := Transition(recommendation, contracts.UpstreamRecommendationEvent{Type: contracts.UpstreamRecommendationEventStartShadow, UserID: 42, Now: now})
	reopened, err := Transition(shadowing, contracts.UpstreamRecommendationEvent{Type: contracts.UpstreamRecommendationEventShadowBlocked, UserID: 42, Now: now})
	if err != nil || reopened.Status != contracts.UpstreamRecommendationOpen {
		t.Fatalf("shadow retry wrong: %+v %v", reopened, err)
	}
	dismissed, err := Transition(reopened, contracts.UpstreamRecommendationEvent{Type: contracts.UpstreamRecommendationEventDismiss, UserID: 42, Now: now})
	if err != nil || dismissed.Status != contracts.UpstreamRecommendationDismissed {
		t.Fatalf("dismiss wrong: %+v %v", dismissed, err)
	}

	expirable, _ := Build("rec-2", candidate())
	expired, err := Transition(expirable, contracts.UpstreamRecommendationEvent{Type: contracts.UpstreamRecommendationEventExpire, UserID: 42, Now: expirable.ExpiresAt})
	if err != nil || expired.Status != contracts.UpstreamRecommendationExpired {
		t.Fatalf("expiry boundary wrong: %+v %v", expired, err)
	}
}

func TestTransitionRejectsSkippingStagesWrongOwnerDryRunOrExpiredEvidence(t *testing.T) {
	recommendation, _ := Build("rec-1", candidate())
	original := recommendation
	tests := []contracts.UpstreamRecommendationEvent{
		{Type: contracts.UpstreamRecommendationEventStartDryRun, UserID: 42, Now: recommendation.CreatedAt.Add(time.Minute), DryRunID: "dry"},
		{Type: contracts.UpstreamRecommendationEventStartShadow, UserID: 7, Now: recommendation.CreatedAt.Add(time.Minute)},
		{Type: contracts.UpstreamRecommendationEventStartShadow, UserID: 42, Now: recommendation.ExpiresAt},
	}
	for _, event := range tests {
		if _, err := Transition(recommendation, event); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("invalid event accepted: %+v err=%v", event, err)
		}
	}
	if !reflect.DeepEqual(recommendation, original) {
		t.Fatal("failed transition mutated input")
	}
}

func TestTransitionRejectsTamperedFingerprintOrSavings(t *testing.T) {
	recommendation, err := Build("rec-1", candidate())
	if err != nil {
		t.Fatal(err)
	}
	event := contracts.UpstreamRecommendationEvent{
		Type:   contracts.UpstreamRecommendationEventStartShadow,
		UserID: recommendation.UserID,
		Now:    recommendation.CreatedAt.Add(time.Minute),
	}
	tamperedFingerprint := recommendation
	tamperedFingerprint.Fingerprint = strings.Repeat("0", 64)
	if _, err := Transition(tamperedFingerprint, event); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("tampered fingerprint accepted: %v", err)
	}
	tamperedSavings := recommendation
	tamperedSavings.Savings.AmountExpected = "999"
	if _, err := Transition(tamperedSavings, event); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("tampered savings accepted: %v", err)
	}
	current := currentFacts(tamperedSavings, event.Now)
	validity := ValidateCurrent(tamperedSavings, current)
	if validity.Current {
		t.Fatal("tampered recommendation reported as current")
	}
	assertStale(t, validity, contracts.UpstreamRecommendationStaleInvalidCurrentFacts)
}

func candidate() contracts.UpstreamRecommendationCandidate {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	constraints := make([]contracts.UpstreamRecommendationConstraint, 0)
	for _, kind := range contracts.UpstreamRecommendationRequiredConstraints() {
		constraints = append(constraints, contracts.UpstreamRecommendationConstraint{Kind: kind, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{string(kind) + "-evidence"}, Explanation: "display"})
	}
	return contracts.UpstreamRecommendationCandidate{
		UserID: 42, IntelligenceFactVersion: 7, CostLedgerFactVersion: 9, LinkFactVersion: 4, PlanGeneration: 11,
		FromSourceID: "source-1", FromChannelID: "channel-1", FromGroupKey: "group-a",
		ToSourceID: "source-2", ToChannelID: "channel-2", ToGroupKey: "group-a", ModelKey: "model-a",
		PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", PerTokens: 1_000_000,
		AffectedPlanIDs: []string{"plan-2", "plan-1"}, AffectedDownstreams: []string{"downstream-1"},
		EvidenceIDs: []string{"quality-1", "price-1"}, Constraints: constraints,
		FromCost:        contracts.UpstreamRecommendationCostRange{Lower: "8", Expected: "10", Upper: "12"},
		ToCost:          contracts.UpstreamRecommendationCostRange{Lower: "7", Expected: "7", Upper: "7"},
		FormulaVersion:  contracts.UpstreamRecommendationFormulaVersionV1,
		StrategyVersion: contracts.UpstreamRecommendationStrategyVersionV1,
		CreatedAt:       now, ExpiresAt: now.Add(time.Hour),
	}
}

func currentFacts(value contracts.UpstreamRecommendation, now time.Time) contracts.UpstreamRecommendationCurrentFacts {
	return contracts.UpstreamRecommendationCurrentFacts{
		UserID: value.UserID, IntelligenceFactVersion: value.IntelligenceFactVersion, CostLedgerFactVersion: value.CostLedgerFactVersion,
		LinkFactVersion: value.LinkFactVersion, PlanGeneration: value.PlanGeneration,
		FromSourceID: value.FromSourceID, FromChannelID: value.FromChannelID, FromGroupKey: value.FromGroupKey,
		ToSourceID: value.ToSourceID, ToChannelID: value.ToChannelID, ToGroupKey: value.ToGroupKey, ModelKey: value.ModelKey,
		PriceDimension: value.PriceDimension, SettlementCurrency: value.SettlementCurrency, PerTokens: value.PerTokens,
		AffectedPlanIDs: append([]string(nil), value.AffectedPlanIDs...), AffectedDownstreams: append([]string(nil), value.AffectedDownstreams...),
		EvidenceIDs: append([]string(nil), value.EvidenceIDs...), FormulaVersion: value.FormulaVersion, StrategyVersion: value.StrategyVersion, Now: now,
	}
}

func assertStale(t *testing.T, got contracts.UpstreamRecommendationValidity, want contracts.UpstreamRecommendationStaleReason) {
	t.Helper()
	for _, reason := range got.Reasons {
		if reason == want {
			return
		}
	}
	t.Fatalf("missing stale reason %q in %+v", want, got)
}
