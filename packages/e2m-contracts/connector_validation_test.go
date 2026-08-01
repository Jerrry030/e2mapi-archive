package contracts

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestUpstreamIntelligenceCollectContractIsClosedL0AndSanitized(t *testing.T) {
	if !IsConnectorCapability(ConnectorTaskUpstreamIntelligenceCollect) ||
		ConnectorTaskUpstreamIntelligenceCollect.RiskLevel() != RiskLevelL0 {
		t.Fatal("upstream intelligence collection must be a closed-list L0 capability")
	}
	validInput := ConnectorUpstreamIntelligenceCollectInput{
		SchemaVersion: 1, SourceID: "uisrc-0123456789abcdef", Reason: ConnectorUpstreamIntelligenceCollectManualRefresh,
	}
	if !ValidateConnectorUpstreamIntelligenceCollectInput(validInput) {
		t.Fatal("valid collection input was rejected")
	}
	for _, input := range []ConnectorUpstreamIntelligenceCollectInput{
		{SchemaVersion: 2, SourceID: validInput.SourceID, Reason: validInput.Reason},
		{SchemaVersion: 1, SourceID: "uis_0123456789abcdef0123456789abcdef", Reason: validInput.Reason},
		{SchemaVersion: 1, SourceID: "https://supplier.example", Reason: validInput.Reason},
		{SchemaVersion: 1, SourceID: validInput.SourceID, Reason: "scheduled"},
	} {
		if ValidateConnectorUpstreamIntelligenceCollectInput(input) {
			t.Fatalf("invalid collection input accepted: %+v", input)
		}
	}
	result := ConnectorUpstreamIntelligenceCollectResult{
		RunID: "uirun_0123456789abcdef", Status: UpstreamCollectionSucceeded,
		Coverage: UpstreamCoverageComplete, FactCount: 42, ObservedAt: time.Now().UTC(),
	}
	if !ValidateConnectorUpstreamIntelligenceCollectResult(result) {
		t.Fatal("valid sanitized collection result was rejected")
	}
	result.RunID = "https://supplier.example/run"
	if ValidateConnectorUpstreamIntelligenceCollectResult(result) {
		t.Fatal("endpoint-shaped run id was accepted")
	}
	for _, code := range []string{"source_not_found", "source_paused", "auth_failed", "rate_limited", "schema_unsupported", "response_too_large", "upstream_unavailable", "local_store_failed"} {
		if !IsConnectorReportedErrorCode(code) || !IsConnectorTaskErrorCode(code) {
			t.Fatalf("collection error %q is not allowlisted", code)
		}
	}
}

func TestBindingEncryptionRuntimeStateAndEnvelopeAreStrict(t *testing.T) {
	public := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, ConnectorBindingEncryptionPublicKeySize))
	state := SanitizeConnectorRuntimeState(ConnectorRuntimeState{
		BindingEncryptionPublicKey: public,
		Capabilities:               []ConnectorTaskType{ConnectorTaskGatewayBindingInstall},
	})
	if state.BindingEncryptionPublicKey != public || len(state.Capabilities) != 1 || state.Capabilities[0] != ConnectorTaskGatewayBindingInstall {
		t.Fatalf("sanitized binding capability = %+v", state)
	}
	if SanitizeConnectorRuntimeState(ConnectorRuntimeState{BindingEncryptionPublicKey: "invalid"}).BindingEncryptionPublicKey != "" {
		t.Fatal("invalid binding public key survived sanitization")
	}
	envelope, _ := json.Marshal(map[string]string{
		"algorithm":            ConnectorBindingEncryptionAlgorithm,
		"ephemeral_public_key": public,
		"nonce":                base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, ConnectorBindingEncryptionNonceSize)),
		"sealed":               base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 16)),
	})
	if !IsConnectorBindingCiphertext(string(envelope)) {
		t.Fatal("valid binding ciphertext envelope was rejected")
	}
	var fields map[string]string
	_ = json.Unmarshal(envelope, &fields)
	fields["extra"] = "not-allowed"
	withExtra, _ := json.Marshal(fields)
	if IsConnectorBindingCiphertext(string(withExtra)) {
		t.Fatal("binding ciphertext accepted an unknown field")
	}
}

func TestConnectorVersionIsStrictSemver(t *testing.T) {
	for _, value := range []string{"0.1.0-dev", "1.2.3", "1.2.3+build.4"} {
		if !IsConnectorVersion(value) {
			t.Fatalf("expected valid connector version %q", value)
		}
	}
	for _, value := range []string{"", "v1.2.3", "1.2", "01.2.3", "1.2.3-01", " 1.2.3"} {
		if IsConnectorVersion(value) {
			t.Fatalf("expected invalid connector version %q", value)
		}
	}
}

func TestNormalizeConnectorGatewayAccountsRejectsSensitiveFields(t *testing.T) {
	valid := []GatewayAccount{
		{ID: "12345", Platform: "openai", Type: "type_1", Status: "active", GroupIDs: []string{"default"}},
		{ID: "user@example.com.json", DisplayName: "CPA account"},
		{ID: "1", ExternalRef: "e2m:seed-probe-newapi", DisplayName: "NewAPI channel"},
	}
	if normalized, issue := NormalizeConnectorGatewayAccounts(valid); issue != "" || len(normalized) != 3 {
		t.Fatalf("valid real-world account ids were rejected: issue=%q accounts=%+v", issue, normalized)
	}

	badBalance := math.Inf(1)
	for name, account := range map[string]GatewayAccount{
		"id url":       {ID: "https://gateway.example/accounts/1"},
		"group header": {ID: "1", GroupIDs: []string{"Authorization: Bearer secret"}},
		"proxy secret": {ID: "1", ProxyID: "sk-secret"},
		"display auth": {ID: "1", DisplayName: "Bearer secret"},
		"display json": {ID: "1", DisplayName: `{"token":"secret"}`},
		"opaque hex":   {ID: "0123456789abcdef0123456789abcdef0123456789abcdef"},
		"ref url":      {ID: "1", ExternalRef: "e2m:https://gateway.example/account"},
		"ref secret":   {ID: "1", ExternalRef: "e2m:sk-secret"},
		"ref empty":    {ID: "1", ExternalRef: "e2m:"},
		"ref nested":   {ID: "1", ExternalRef: "e2m:tenant:account"},
		"number":       {ID: "1", Balance: &badBalance},
	} {
		t.Run(name, func(t *testing.T) {
			if _, issue := NormalizeConnectorGatewayAccounts([]GatewayAccount{account}); issue == "" {
				t.Fatalf("malicious account was accepted: %+v", account)
			}
		})
	}
}

func TestSanitizeConnectorRuntimeStateUsesClosedAllowlists(t *testing.T) {
	state := SanitizeConnectorRuntimeState(ConnectorRuntimeState{
		ProtocolVersion: ConnectorProtocolVersion + 1,
		GatewayKind:     " NEWAPI ",
		GatewayStatus:   " OK ",
		ErrorCode:       " GATEWAY_TIMEOUT ",
		Capabilities: []ConnectorTaskType{
			ConnectorTaskGatewayHealth,
			ConnectorTaskType("gateway.raw.get"),
			ConnectorTaskGatewayHealth,
		},
	})
	if state.ProtocolVersion != ConnectorProtocolVersion || state.GatewayKind != "newapi" ||
		state.GatewayStatus != "ok" || state.ErrorCode != "gateway_timeout" ||
		len(state.Capabilities) != 1 || state.Capabilities[0] != ConnectorTaskGatewayHealth {
		t.Fatalf("unexpected sanitized runtime state: %+v", state)
	}
}

func TestSanitizeConnectorRuntimeStateKeepsExplicitObservationCapabilities(t *testing.T) {
	state := SanitizeConnectorRuntimeState(ConnectorRuntimeState{
		ObservationCapabilities: &ConnectorObservationCapabilities{
			PassiveCollection:   true,
			SuccessEvents:       true,
			FailureEvents:       true,
			ErrorClassification: true,
			FirstTokenMS:        true,
			TotalMS:             true,
			TokenCounts:         true,
		},
	})
	if state.ObservationCapabilities == nil || !state.ObservationCapabilities.FirstTokenMS ||
		!state.ObservationCapabilities.ErrorClassification {
		t.Fatalf("observation capabilities were discarded: %+v", state)
	}

	state = SanitizeConnectorRuntimeState(ConnectorRuntimeState{
		ObservationCapabilities: &ConnectorObservationCapabilities{
			PassiveCollection: false,
			FirstTokenMS:      true,
			TotalMS:           true,
		},
	})
	if state.ObservationCapabilities == nil || state.ObservationCapabilities.FirstTokenMS ||
		state.ObservationCapabilities.TotalMS {
		t.Fatalf("unsupported passive metrics survived sanitization: %+v", state)
	}
}

func TestSanitizeConnectorRuntimeStateCostUsageCapabilitiesAreDeepCopiedAndRequirePassiveCollection(t *testing.T) {
	cost := &ConnectorCostUsageCapabilities{InputTokens: true, GroupKey: true}
	state := SanitizeConnectorRuntimeState(ConnectorRuntimeState{ObservationCapabilities: &ConnectorObservationCapabilities{
		PassiveCollection: true, CostUsage: cost,
	}})
	if state.ObservationCapabilities == nil || state.ObservationCapabilities.CostUsage == nil ||
		!state.ObservationCapabilities.CostUsage.InputTokens || !state.ObservationCapabilities.CostUsage.GroupKey {
		t.Fatalf("cost usage capabilities lost: %+v", state.ObservationCapabilities)
	}
	cost.InputTokens = false
	if !state.ObservationCapabilities.CostUsage.InputTokens {
		t.Fatal("sanitized state aliases caller-owned cost usage capabilities")
	}
	disabled := SanitizeConnectorRuntimeState(ConnectorRuntimeState{ObservationCapabilities: &ConnectorObservationCapabilities{
		CostUsage: &ConnectorCostUsageCapabilities{InputTokens: true},
	}})
	if disabled.ObservationCapabilities == nil || disabled.ObservationCapabilities.CostUsage != nil {
		t.Fatalf("cost usage survived without passive collection: %+v", disabled.ObservationCapabilities)
	}
}

func TestQualityProbeIsARecognizedL0Task(t *testing.T) {
	if !IsConnectorCapability(ConnectorTaskGatewayQualityProbe) {
		t.Fatal("quality probe task must be recognized by the protocol allowlist")
	}
	if got := ConnectorTaskGatewayQualityProbe.RiskLevel(); got != RiskLevelL0 {
		t.Fatalf("quality probe risk=%q, want L0", got)
	}
}

func TestBindingProofIsARecognizedL0TaskWithStableDomainMessage(t *testing.T) {
	if !IsConnectorCapability(ConnectorTaskGatewayBindingProof) {
		t.Fatal("binding proof task must be recognized by the protocol allowlist")
	}
	if got := ConnectorTaskGatewayBindingProof.RiskLevel(); got != RiskLevelL0 {
		t.Fatalf("binding proof risk=%q, want L0", got)
	}
	input := ConnectorGatewayBindingProofInput{
		ChannelID: "channel-a",
		BindingID: "binding-a",
		Challenge: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	want := "e2m-binding-proof-v1\x00channel-a\x00binding-a\x000123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got := string(ConnectorGatewayBindingProofMessage(input)); got != want {
		t.Fatalf("binding proof message = %q, want %q", got, want)
	}
}

func TestSchedulingFenceErrorsAreAllowlisted(t *testing.T) {
	for _, code := range []string{"scheduling_fence_stale", "scheduling_fence_conflict"} {
		if !IsConnectorReportedErrorCode(code) || !IsConnectorTaskErrorCode(code) {
			t.Fatalf("scheduling fence error %q is not accepted by Connector/Core", code)
		}
	}
}

func TestPlatformObservationErrorHasDistinctWireValue(t *testing.T) {
	if ErrorPlatform != ObservationErrorType("platform_error") || ErrorPlatform == ErrorUnknown || ErrorPlatform == ErrorClient {
		t.Fatalf("platform error must remain a distinct factual attribution: %q", ErrorPlatform)
	}
}

func TestSchedulingBarrierIsARecognizedL1Task(t *testing.T) {
	if !IsConnectorCapability(ConnectorTaskGatewaySchedulingBarrier) {
		t.Fatal("scheduling barrier task must be recognized by the protocol allowlist")
	}
	if got := ConnectorTaskGatewaySchedulingBarrier.RiskLevel(); got != RiskLevelL1 {
		t.Fatalf("scheduling barrier risk=%q, want L1", got)
	}
}

func TestQualityProbeFieldsRejectConnectionMaterial(t *testing.T) {
	for _, value := range []string{"", "https://gateway.local/probe", "sk-secret", "model\nname"} {
		if IsConnectorQualityProbeField(value) {
			t.Fatalf("sensitive quality probe field accepted: %q", value)
		}
	}
	if !IsConnectorQualityProbeField("gpt-4.1-mini") {
		t.Fatal("ordinary model identifier was rejected")
	}
}
