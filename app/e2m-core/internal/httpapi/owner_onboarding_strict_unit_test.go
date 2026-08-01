package httpapi

import (
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestStrictWorkflowReadyRequiresCurrentConnectorProof(t *testing.T) {
	proof := contracts.UpstreamKeyProofReceipt{
		ConnectorID: "old-connector", KeyVersion: 2, Status: contracts.DeliveryKeyProofVerified,
	}
	if ownerProofMatches(proof, "current-connector", "current-connector", 2) {
		t.Fatal("proof from an old connector was accepted")
	}
	proof.ConnectorID = "current-connector"
	if !ownerProofMatches(proof, "current-connector", "current-connector", 2) {
		t.Fatal("current connector proof was rejected")
	}
}

func TestStrictOwnerServiceStateRequiresEveryLiveWorkflowEvidence(t *testing.T) {
	item := ownerOnboardingInstanceStatus{ConnectorState: ownerConnectorReady}
	workflows := []contracts.OnboardingWorkflow{
		{Stage: contracts.OnboardingActive, Status: contracts.OnboardingReady},
		{Stage: contracts.OnboardingActive, Status: contracts.OnboardingReady},
	}
	state, blocker := strictOwnerServiceState(item, workflows, []ownerOnboardingWorkflowFacts{{Ready: true}, {Ready: false}})
	if state != ownerServiceDegraded || blocker != "service_verification_pending" {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}
}

func TestStrictOwnerServiceStateSeparatesCallabilityPendingAndFailure(t *testing.T) {
	item := ownerOnboardingInstanceStatus{ConnectorState: ownerConnectorReady}
	workflows := []contracts.OnboardingWorkflow{{Stage: contracts.OnboardingActive, Status: contracts.OnboardingReady}}
	pending := ownerOnboardingWorkflowFacts{
		DeliveredKeys: 1, VerifiedKeys: 1, PublishedBindings: 1, ActiveBindings: 1,
		AwaitingVerificationBindings: 1,
	}
	state, blocker := strictOwnerServiceState(item, workflows, []ownerOnboardingWorkflowFacts{pending})
	if state != ownerServiceAwaitingVerification || blocker != "service_verification_pending" {
		t.Fatalf("pending state=%s blocker=%s", state, blocker)
	}
	pending.AwaitingVerificationBindings = 0
	pending.VerificationFailedBindings = 1
	state, blocker = strictOwnerServiceState(item, workflows, []ownerOnboardingWorkflowFacts{pending})
	if state != ownerServiceVerificationFailed || blocker != "service_verification_failed" {
		t.Fatalf("failed state=%s blocker=%s", state, blocker)
	}
	pending.VerificationFailedBindings = 0
	pending.CallableBindings = 1
	pending.Ready = true
	state, blocker = strictOwnerServiceState(item, workflows, []ownerOnboardingWorkflowFacts{pending})
	if state != ownerServiceActive || blocker != "" {
		t.Fatalf("callable state=%s blocker=%s", state, blocker)
	}
}

func TestSelectOwnerRetryMetadataDoesNotMixWorkflowState(t *testing.T) {
	oldRetry := time.Date(2026, 7, 21, 1, 0, 0, 0, time.UTC)
	newRetry := oldRetry.Add(time.Minute)
	periodic := oldRetry.Add(-time.Hour)
	blocker, next := selectOwnerRetryMetadata([]contracts.OnboardingWorkflow{
		{Status: contracts.OnboardingReady, LastErrorCode: "pool_inactive", NextAttemptAt: &periodic, UpdatedAt: newRetry.Add(time.Minute)},
		{Status: contracts.OnboardingRetryable, LastErrorCode: "key_assignment_failed", NextAttemptAt: &oldRetry, UpdatedAt: oldRetry},
		{Status: contracts.OnboardingRetryable, LastErrorCode: "connector_unavailable", NextAttemptAt: &newRetry, UpdatedAt: newRetry},
	})
	if blocker != "connector_offline" || next == nil || !next.Equal(newRetry) {
		t.Fatalf("blocker=%s next=%v", blocker, next)
	}
}
