package contracts

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestUpstreamIntelligenceLinkReadModelNeverSerializesOpaqueSourceIdentity(t *testing.T) {
	value := UpstreamIntelligenceLinkReadModel{
		ID: "link-1", IntelligenceSourceID: "source-1", Scope: UpstreamLinkSourceIdentity,
		ChannelID: "channel-1", PriceDimension: UpstreamPriceInput, Status: UpstreamLinkActive,
	}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "upstream_source_identity") || strings.Contains(string(payload), "opaque-local") {
		t.Fatalf("link read DTO exposed opaque source identity: %s", payload)
	}
}

func TestUpstreamIntelligenceReadResponsesExposeTopLevelConsistencyMetadata(t *testing.T) {
	generatedAt := time.Date(2026, 7, 24, 1, 2, 3, 0, time.UTC)
	metadata := UpstreamIntelligenceReadMetadata{FactVersion: 17, GeneratedAt: generatedAt}
	responses := []any{
		UpstreamIntelligenceOverviewResponse{UpstreamIntelligenceReadMetadata: metadata},
		UpstreamIntelligenceSourcesResponse{UpstreamIntelligenceReadMetadata: metadata},
		UpstreamIntelligenceSourceDetailResponse{UpstreamIntelligenceReadMetadata: metadata},
		UpstreamIntelligenceRatesResponse{UpstreamIntelligenceReadMetadata: metadata},
		UpstreamIntelligenceChangesResponse{UpstreamIntelligenceReadMetadata: metadata},
		UpstreamIntelligenceEvidenceResponse{UpstreamIntelligenceReadMetadata: metadata},
		UpstreamIntelligenceFrontierResponse{UpstreamIntelligenceReadMetadata: metadata},
	}
	for _, response := range responses {
		payload, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("marshal %T: %v", response, err)
		}
		var object map[string]any
		if err := json.Unmarshal(payload, &object); err != nil {
			t.Fatalf("decode %T: %v", response, err)
		}
		if object["fact_version"] != float64(17) || object["generated_at"] != generatedAt.Format(time.RFC3339) {
			t.Fatalf("%T metadata was not top-level: %s", response, payload)
		}
		if _, nested := object["snapshot"]; nested {
			t.Fatalf("%T unexpectedly nested consistency metadata: %s", response, payload)
		}
	}
}

func TestUpstreamIntelligenceReadJSONKeepsUnknownValuesNull(t *testing.T) {
	rate := UpstreamIntelligenceRateReadModel{
		ObservationID: "offer-1",
		Source: UpstreamIntelligenceReadSourceSummary{
			ID: "source-1", Mode: UpstreamSourceExternal, Provider: "sub2api", DisplayName: "Source A",
			Status: UpstreamSourceActive, Freshness: nil, LastRunAt: nil, LastSuccessAt: nil, NextPollAt: nil,
		},
		GroupKey: "default", ModelKey: "model-a", PriceDimension: UpstreamPriceInput,
		Evidence: UpstreamIntelligenceReadEvidence{
			Accuracy: UpstreamEvidenceUnknown, Coverage: UpstreamCoveragePartial, Freshness: UpstreamFreshnessStale,
			Confidence: nil, ObservedAt: time.Unix(1, 0).UTC(), ReceivedAt: time.Unix(2, 0).UTC(),
			FreshUntil: time.Unix(3, 0).UTC(), MissingFields: []string{"currency", "published_unit_price"},
		},
		UpstreamIntelligenceComparability: UpstreamIntelligenceComparability{
			Comparable: false, ComparabilityReason: UpstreamIntelligenceNotComparableMissingCurrency,
		},
	}
	payload, err := json.Marshal(rate)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"group_multiplier", "recharge_yield", "published_unit_price", "effective_multiplier", "effective_unit_cost",
	} {
		if value, exists := object[key]; !exists || value != nil {
			t.Fatalf("unknown %s must be explicit JSON null, got %#v in %s", key, value, payload)
		}
	}
	evidence, ok := object["evidence"].(map[string]any)
	if !ok || evidence["confidence"] != nil {
		t.Fatalf("unknown confidence must be explicit JSON null: %s", payload)
	}
	source, ok := object["source"].(map[string]any)
	if !ok {
		t.Fatalf("missing source: %s", payload)
	}
	for _, key := range []string{"freshness", "last_run_at", "last_success_at", "next_poll_at"} {
		if value, exists := source[key]; !exists || value != nil {
			t.Fatalf("unknown source %s must be explicit JSON null, got %#v in %s", key, value, payload)
		}
	}
}

func TestUpstreamIntelligenceReadDecimalIsJSONString(t *testing.T) {
	cost := CanonicalDecimal("0.000000000000000001")
	response := UpstreamIntelligenceRatesResponse{
		Items: []UpstreamIntelligenceRateReadModel{{EffectiveUnitCost: &cost}},
	}
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"effective_unit_cost":"0.000000000000000001"`) {
		t.Fatalf("canonical decimal lost string representation: %s", payload)
	}
}

func TestUpstreamIntelligenceComparabilityInvariant(t *testing.T) {
	valid := []UpstreamIntelligenceComparability{
		{Comparable: true},
		{Comparable: false, ComparabilityReason: UpstreamIntelligenceNotComparableMissingCurrency},
		{Comparable: false, ComparabilityReason: UpstreamIntelligenceNotComparableUnattributedEvidence},
	}
	for _, value := range valid {
		if !value.Valid() {
			t.Fatalf("valid comparability rejected: %#v", value)
		}
	}
	invalid := []UpstreamIntelligenceComparability{
		{Comparable: true, ComparabilityReason: UpstreamIntelligenceNotComparableMissingCurrency},
		{Comparable: false},
		{Comparable: false, ComparabilityReason: "arbitrary"},
	}
	for _, value := range invalid {
		if value.Valid() {
			t.Fatalf("invalid comparability accepted: %#v", value)
		}
	}
	comparableJSON, _ := json.Marshal(UpstreamIntelligenceComparability{Comparable: true})
	if strings.Contains(string(comparableJSON), "comparability_reason") {
		t.Fatalf("comparable rows must omit an empty blocker: %s", comparableJSON)
	}
}

func TestUpstreamIntelligenceQualityBlockersAreAllowlisted(t *testing.T) {
	for _, reason := range []UpstreamIntelligenceComparabilityReason{
		UpstreamIntelligenceNotComparableUnlinkedQuality,
		UpstreamIntelligenceNotComparableQualityUnavailable,
		UpstreamIntelligenceNotComparableQualityInsufficient,
		UpstreamIntelligenceNotComparableQualityStale,
	} {
		if !IsUpstreamIntelligenceComparabilityReason(reason) {
			t.Fatalf("quality blocker %q is not allowlisted", reason)
		}
	}
}

func TestUpstreamIntelligenceFrontierQualityEvidenceKeepsUnknownMetricsNull(t *testing.T) {
	score := CanonicalDecimal("90")
	response := UpstreamIntelligenceFrontierResponse{Items: []UpstreamIntelligenceFrontierPoint{{
		QualityScore: &score,
		QualityEvidence: &UpstreamIntelligenceFrontierQualityEvidence{
			SnapshotID: "quality-1", Window: Window5m, QualitySampleCount: 10, MinimumSampleCount: 5,
			HealthState: HealthHealthy, Freshness: UpstreamFreshnessCurrent,
		},
	}}}
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var object struct {
		Items []struct {
			QualityEvidence map[string]any `json:"quality_evidence"`
		} `json:"items"`
	}
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"success_rate", "ttft_p95_ms", "duration_p95_ms"} {
		if value, exists := object.Items[0].QualityEvidence[key]; !exists || value != nil {
			t.Fatalf("unknown quality metric %s must be explicit JSON null, got %#v in %s", key, value, payload)
		}
	}
}

func TestUpstreamIntelligenceReadEvidenceInvariants(t *testing.T) {
	now := time.Now().UTC()
	confidence := CanonicalDecimal("0.8")
	base := UpstreamIntelligenceReadEvidence{
		Coverage: UpstreamCoverageComplete, Freshness: UpstreamFreshnessCurrent,
		ObservedAt: now, ReceivedAt: now, FreshUntil: now.Add(time.Minute), MissingFields: []string{},
	}
	for _, value := range []UpstreamIntelligenceReadEvidence{
		func() UpstreamIntelligenceReadEvidence {
			value := base
			value.Accuracy = UpstreamEvidenceExact
			return value
		}(),
		func() UpstreamIntelligenceReadEvidence {
			value := base
			value.Accuracy = UpstreamEvidenceDerived
			value.Confidence = &confidence
			return value
		}(),
		func() UpstreamIntelligenceReadEvidence {
			value := base
			value.Accuracy = UpstreamEvidenceUnknown
			value.MissingFields = []string{"currency"}
			return value
		}(),
		func() UpstreamIntelligenceReadEvidence {
			value := base
			value.Accuracy = UpstreamEvidenceUnattributed
			value.ReasonCode = "source_unlinked"
			return value
		}(),
	} {
		if !value.Valid() {
			t.Fatalf("valid evidence rejected: %#v", value)
		}
	}
	invalid := base
	invalid.Accuracy, invalid.Confidence = UpstreamEvidenceExact, &confidence
	if invalid.Valid() {
		t.Fatal("exact evidence accepted a confidence value")
	}
	invalid = base
	invalid.Accuracy = UpstreamEvidenceUnknown
	if invalid.Valid() {
		t.Fatal("unexplained unknown evidence was accepted")
	}
	withoutConfidence := base
	withoutConfidence.Accuracy = UpstreamEvidenceEstimated
	if !withoutConfidence.Valid() {
		t.Fatal("estimated evidence with unknown confidence was rejected")
	}
}

func TestUpstreamIntelligenceReadFiltersNormalizeBoundedLimits(t *testing.T) {
	tests := map[string]struct {
		got  int
		want int
	}{
		"sources/default":       {UpstreamIntelligenceSourcesFilter{}.Normalize().Limit, DefaultUpstreamIntelligenceListLimit},
		"rates/capped":          {UpstreamIntelligenceRatesFilter{Limit: MaxUpstreamIntelligenceListLimit + 1}.Normalize().Limit, MaxUpstreamIntelligenceListLimit},
		"changes/kept":          {UpstreamIntelligenceChangesFilter{Limit: 7}.Normalize().Limit, 7},
		"frontier/negative":     {UpstreamIntelligenceFrontierFilter{Limit: -1}.Normalize().Limit, DefaultUpstreamIntelligenceListLimit},
		"source-detail/rates":   {UpstreamIntelligenceSourceDetailFilter{}.Normalize().RatesLimit, DefaultUpstreamIntelligenceListLimit},
		"source-detail/changes": {UpstreamIntelligenceSourceDetailFilter{ChangesLimit: MaxUpstreamIntelligenceListLimit + 99}.Normalize().ChangesLimit, MaxUpstreamIntelligenceListLimit},
	}
	for name, test := range tests {
		if test.got != test.want {
			t.Errorf("%s limit = %d; want %d", name, test.got, test.want)
		}
	}
}

func TestUpstreamIntelligenceReadWireTypesCannotExposeSecretsOrRawResponses(t *testing.T) {
	forbidden := []string{
		"url", "credential", "authorization", "access_token", "refresh_token", "bearer", "secret", "cookie", "header", "raw_response", "rawresponse",
		"local_ref", "localref", "connector_id", "connectorid", "instance_id", "instanceid", "snapshot_hash",
		"snapshothash", "manifest_hash", "manifesthash", "user_id", "userid",
	}
	visited := map[reflect.Type]bool{}
	var inspect func(reflect.Type)
	inspect = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}
		if typ.PkgPath() != reflect.TypeOf(UpstreamIntelligenceReadMetadata{}).PkgPath() || typ.Kind() != reflect.Struct || visited[typ] {
			return
		}
		visited[typ] = true
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			wire := strings.ToLower(field.Name + " " + strings.Split(field.Tag.Get("json"), ",")[0])
			for _, needle := range forbidden {
				if strings.Contains(wire, needle) {
					t.Fatalf("%s.%s exposes forbidden read concept %q", typ.Name(), field.Name, needle)
				}
			}
			inspect(field.Type)
		}
	}
	for _, response := range []any{
		UpstreamIntelligenceOverviewResponse{}, UpstreamIntelligenceSourcesResponse{},
		UpstreamIntelligenceSourceDetailResponse{}, UpstreamIntelligenceRatesResponse{},
		UpstreamIntelligenceChangesResponse{}, UpstreamIntelligenceEvidenceResponse{},
		UpstreamIntelligenceFrontierResponse{},
	} {
		inspect(reflect.TypeOf(response))
	}
}
