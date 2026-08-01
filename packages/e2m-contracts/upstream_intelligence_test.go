package contracts

import (
	"encoding/json"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCanonicalDecimalGolden(t *testing.T) {
	valid := []string{
		"0", "1", "-1", "0.1", "-0.1",
		"99999999999999999999.123456789012345678",
		"0.000000000000000001",
	}
	for _, input := range valid {
		got, err := ParseCanonicalDecimal(input)
		if err != nil || string(got) != input {
			t.Fatalf("ParseCanonicalDecimal(%q) = %q, %v", input, got, err)
		}
	}
	invalid := []string{
		"", "+1", "-0", "-0.0", "00", "01", "1.0", ".1", "1.", "1e2", "NaN", "Infinity", " 1",
		strings.Repeat("9", 21), strings.Repeat("9", 39), "1." + strings.Repeat("1", 19),
	}
	for _, input := range invalid {
		if _, err := ParseCanonicalDecimal(input); err == nil {
			t.Fatalf("ParseCanonicalDecimal(%q) unexpectedly succeeded", input)
		}
	}
}

func TestCanonicalizeUpstreamDecimalText(t *testing.T) {
	tests := map[string]string{
		"0001.230000000000000000": "1.23",
		"-0001.230000":            "-1.23",
		"0.000000000000000000":    "0",
		"-000.000":                "0",
		"000000000000000000000000000000000000001.000": "1",
	}
	for input, want := range tests {
		got, err := CanonicalizeUpstreamDecimalText(input)
		if err != nil || string(got) != want {
			t.Fatalf("CanonicalizeUpstreamDecimalText(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{
		"", "+1", " 1", "1 ", "1e2", "NaN", "Infinity",
		strings.Repeat("9", 21), "0." + strings.Repeat("1", 19),
	} {
		if _, err := CanonicalizeUpstreamDecimalText(input); err == nil {
			t.Fatalf("CanonicalizeUpstreamDecimalText(%q) unexpectedly succeeded", input)
		}
	}
}

func TestCanonicalDecimalJSONRequiresString(t *testing.T) {
	var got CanonicalDecimal
	if err := json.Unmarshal([]byte(`"1.25"`), &got); err != nil || got != "1.25" {
		t.Fatalf("string decimal = %q, %v", got, err)
	}
	for _, input := range []string{`1.25`, `"1.250"`, `"-0"`, `"1e2"`, `"1" "2"`} {
		if err := json.Unmarshal([]byte(input), &got); err == nil {
			t.Fatalf("json %s unexpectedly succeeded", input)
		}
	}
}

func TestQuantizeCanonicalDecimalUsesHalfEven(t *testing.T) {
	tests := map[string]string{
		"1.225": "1.22", "1.235": "1.24", "-1.225": "-1.22", "-1.235": "-1.24",
		"0.005": "0", "0.015": "0.02",
	}
	for input, want := range tests {
		r, _ := new(big.Rat).SetString(input)
		got, err := QuantizeCanonicalDecimal(r, 2)
		if err != nil || string(got) != want {
			t.Fatalf("quantize %s = %q, %v; want %s", input, got, err, want)
		}
	}
	carry, _ := new(big.Rat).SetString("99999999999999999999.9999999999999999995")
	if _, err := QuantizeCanonicalDecimal(carry, UpstreamDecimalMaxScale); err == nil {
		t.Fatal("rounding carry beyond NUMERIC(38,18) unexpectedly succeeded")
	}
}

func TestEffectiveCostFormula(t *testing.T) {
	multiplier, err := CalculateEffectiveMultiplier("0.8", "2")
	if err != nil || multiplier != "0.4" {
		t.Fatalf("multiplier = %q, %v", multiplier, err)
	}
	cost, err := CalculateEffectiveUnitCost("15", multiplier)
	if err != nil || cost != "6" {
		t.Fatalf("cost = %q, %v", cost, err)
	}
	if _, err := CalculateEffectiveMultiplier("1", "0"); err == nil {
		t.Fatal("zero recharge yield accepted")
	}
}

func TestUpstreamCollectionErrorCodeAllowlist(t *testing.T) {
	for _, value := range []string{
		UpstreamCollectionErrorAuthFailed,
		UpstreamCollectionErrorRateLimited,
		UpstreamCollectionErrorSchemaUnsupported,
		UpstreamCollectionErrorResponseTooLarge,
		UpstreamCollectionErrorUpstreamUnavailable,
	} {
		if !IsUpstreamCollectionErrorCode(value) {
			t.Fatalf("stable error code %q was rejected", value)
		}
	}
	for _, value := range []string{"", "upstream unavailable", "Bearer secret", "https://supplier.example"} {
		if IsUpstreamCollectionErrorCode(value) {
			t.Fatalf("untrusted error code %q was accepted", value)
		}
	}
}

func TestIngestWireShapeCannotCarryCoreScopeOrDerivedCost(t *testing.T) {
	forbidden := []string{
		"user_id", "connector_id", "instance_id", "source_id", "received_at", "created_at", "updated_at",
		"finalized_fact_version", "effective_multiplier", "effective_unit_cost", "formula_version",
		"url", "credential", "cookie", "header", "raw_response", "authorization",
	}
	types := []reflect.Type{
		reflect.TypeOf(UpstreamIntelligenceIngestBatchRequest{}),
		reflect.TypeOf(UpstreamIntelligenceIngestSourceRegistration{}),
		reflect.TypeOf(UpstreamIntelligenceIngestRun{}),
		reflect.TypeOf(UpstreamIntelligenceIngestWalletObservation{}),
		reflect.TypeOf(UpstreamIntelligenceIngestOfferObservation{}),
	}
	for _, typ := range types {
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			wire := strings.ToLower(field.Name + " " + strings.Split(field.Tag.Get("json"), ",")[0])
			for _, needle := range forbidden {
				if strings.Contains(wire, needle) {
					t.Fatalf("%s.%s exposes forbidden wire concept %q", typ.Name(), field.Name, needle)
				}
			}
		}
	}
}

func TestUpstreamIntelligencePayloadHashGoldenAndTamperDetection(t *testing.T) {
	request := upstreamIntelligenceHashFixture()
	got, err := CalculateUpstreamIntelligencePayloadHash(request)
	if err != nil {
		t.Fatalf("payload hash: %v", err)
	}
	const want = "1261ba09577aae473cd9b6c46ee383f87a6c327e66bfcc197b962df1b130bef1"
	if got != want {
		t.Fatalf("payload hash = %q; want %q", got, want)
	}
	if !IsUpstreamIntelligenceSHA256(got) || IsUpstreamIntelligenceSHA256(strings.ToUpper(got)) {
		t.Fatalf("lowercase SHA-256 validation is inconsistent for %q", got)
	}

	reordered := request
	reordered.Offers = append([]UpstreamIntelligenceIngestOfferObservation(nil), request.Offers...)
	reordered.Offers[0], reordered.Offers[1] = reordered.Offers[1], reordered.Offers[0]
	reorderedHash, err := CalculateUpstreamIntelligencePayloadHash(reordered)
	if err != nil || reorderedHash != got {
		t.Fatalf("fact reordering changed canonical hash: %q, %v", reorderedHash, err)
	}

	tampered := request
	tampered.Offers = append([]UpstreamIntelligenceIngestOfferObservation(nil), request.Offers...)
	price := CanonicalDecimal("3")
	tampered.Offers[0].PublishedUnitPrice = &price
	tamperedHash, err := CalculateUpstreamIntelligencePayloadHash(tampered)
	if err != nil || tamperedHash == got {
		t.Fatalf("tampered fact was not detected: %q, %v", tamperedHash, err)
	}

	wrongRun := request
	wrongRun.Wallets = append([]UpstreamIntelligenceIngestWalletObservation(nil), request.Wallets...)
	wrongRun.Wallets[0].RunID = "another-run"
	if _, err := CalculateUpstreamIntelligencePayloadHash(wrongRun); err == nil {
		t.Fatal("cross-run observation unexpectedly hashed")
	}
}

func TestUpstreamIntelligenceManifestHashGoldenAndOrdering(t *testing.T) {
	leaves := []UpstreamIntelligenceManifestBatch{
		{BatchNo: 1, PayloadHash: strings.Repeat("b", 64)},
		{BatchNo: 0, PayloadHash: strings.Repeat("a", 64)},
	}
	got, err := CalculateUpstreamIntelligenceManifestHash(leaves)
	if err != nil {
		t.Fatalf("manifest hash: %v", err)
	}
	const want = "8bdeb99952e5286401fd0040607d21bbdc50849e0a311f0ac485850643ae5257"
	if got != want {
		t.Fatalf("manifest hash = %q; want %q", got, want)
	}
	reversed, err := CalculateUpstreamIntelligenceManifestHash([]UpstreamIntelligenceManifestBatch{leaves[1], leaves[0]})
	if err != nil || reversed != got {
		t.Fatalf("manifest input order changed hash: %q, %v", reversed, err)
	}
	for name, invalid := range map[string][]UpstreamIntelligenceManifestBatch{
		"empty":     nil,
		"gap":       {{BatchNo: 1, PayloadHash: strings.Repeat("a", 64)}},
		"duplicate": {{BatchNo: 0, PayloadHash: strings.Repeat("a", 64)}, {BatchNo: 0, PayloadHash: strings.Repeat("b", 64)}},
		"uppercase": {{BatchNo: 0, PayloadHash: strings.Repeat("A", 64)}},
	} {
		if _, err := CalculateUpstreamIntelligenceManifestHash(invalid); err == nil {
			t.Fatalf("invalid %s manifest unexpectedly succeeded", name)
		}
	}
}

func TestNormalizeUpstreamIntelligenceListLimit(t *testing.T) {
	for input, want := range map[int]int{
		-1: DefaultUpstreamIntelligenceListLimit, 0: DefaultUpstreamIntelligenceListLimit,
		1: 1, MaxUpstreamIntelligenceListLimit: MaxUpstreamIntelligenceListLimit,
		MaxUpstreamIntelligenceListLimit + 1: MaxUpstreamIntelligenceListLimit,
	} {
		if got := NormalizeUpstreamIntelligenceListLimit(input); got != want {
			t.Fatalf("NormalizeUpstreamIntelligenceListLimit(%d) = %d; want %d", input, got, want)
		}
	}
}

func upstreamIntelligenceHashFixture() UpstreamIntelligenceIngestBatchRequest {
	observed := time.Date(2026, 7, 24, 8, 9, 10, 123456789, time.FixedZone("fixture", 8*60*60))
	completed := observed.Add(time.Minute)
	balance, confidence := CanonicalDecimal("100.25"), CanonicalDecimal("0.9")
	group, yield, priceOne, priceTwo := CanonicalDecimal("0.8"), CanonicalDecimal("2"), CanonicalDecimal("15"), CanonicalDecimal("30")
	return UpstreamIntelligenceIngestBatchRequest{
		SchemaVersion: UpstreamIntelligenceSchemaVersion,
		Source: UpstreamIntelligenceIngestSourceRegistration{
			LocalRef: "src_local_01", Mode: UpstreamSourceExternal, Provider: "sub2api", DisplayName: "Fixture A",
			Currency: "USD", PollIntervalSeconds: 300, Status: UpstreamSourceActive,
			Capabilities: UpstreamIntelligenceCapabilities{Balance: true, Groups: true, Rates: true, Prices: true},
		},
		Run: UpstreamIntelligenceIngestRun{
			ID: "run_01", Trigger: UpstreamCollectionScheduled, Status: UpstreamCollectionSucceeded,
			Coverage: UpstreamCoverageComplete, StartedAt: observed.Add(-time.Minute), ObservedAt: observed,
			CompletedAt: &completed, SnapshotHash: strings.Repeat("1", 64), BatchCount: 2, FactCount: 3, PageCount: 1,
		},
		Manifest: UpstreamIngestBatchManifest{BatchCount: 2}, BatchNo: 0,
		Wallets: []UpstreamIntelligenceIngestWalletObservation{{
			ID: "wallet_01", RunID: "run_01", BalanceAmount: &balance, UnitKind: UpstreamWalletFiat, Currency: "USD",
			Accuracy: UpstreamEvidenceExact, Coverage: UpstreamCoverageComplete, ObservedAt: observed,
			FreshUntil: observed.Add(10 * time.Minute), MissingFields: []string{},
		}},
		Offers: []UpstreamIntelligenceIngestOfferObservation{
			{ID: "offer_02", RunID: "run_01", GroupKey: "default", ModelKey: "model-b", PriceDimension: UpstreamPriceOutput,
				SettlementCurrency: "USD", GroupMultiplier: &group, RechargeYield: &yield, PublishedUnitPrice: &priceTwo,
				PerTokens: 1_000_000, Accuracy: UpstreamEvidenceEstimated, Coverage: UpstreamCoverageComplete,
				Confidence: &confidence, ObservedAt: observed, EffectiveAt: observed, FreshUntil: observed.Add(10 * time.Minute),
				MissingFields: []string{"fee", "fx"}, AdapterSchemaVersion: 1},
			{ID: "offer_01", RunID: "run_01", GroupKey: "default", ModelKey: "model-a", PriceDimension: UpstreamPriceInput,
				SettlementCurrency: "USD", GroupMultiplier: &group, RechargeYield: &yield, PublishedUnitPrice: &priceOne,
				PerTokens: 1_000_000, Accuracy: UpstreamEvidenceExact, Coverage: UpstreamCoverageComplete,
				ObservedAt: observed, EffectiveAt: observed, FreshUntil: observed.Add(10 * time.Minute), AdapterSchemaVersion: 1},
		},
	}
}
