package onboarding

import (
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestRunnerEmitsProgressForEachDesiredGenerationButNotPeriodicVerification(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	fixture.runner.activeCheck = 20 * time.Millisecond
	fixture.runner.RunOnce(fixture.ctx)
	assertOnboardingActionCount(t, fixture, auditActionOnboardingProgress, 1)

	// A due ready-state check only verifies the existing generation. It must not
	// look like another onboarding run started.
	time.Sleep(40 * time.Millisecond)
	fixture.runner.RunOnce(fixture.ctx)
	assertOnboardingActionCount(t, fixture, auditActionOnboardingProgress, 1)
	assertOnboardingActionCount(t, fixture, auditActionOnboardingVerified, 1)

	// A desired-state change resets Attempts, so the next claimed generation
	// gets one new progress event and then a reconfigured completion event.
	if _, err := fixture.store.UpsertUpstreamKeyDelivery(fixture.ctx, contracts.UpstreamKeyDelivery{
		ChannelID: fixture.channel.ID, SecretRef: "credential_ref:test/channel-a-v2", MaskedValue: "********v2",
	}); err != nil {
		t.Fatal(err)
	}
	fixture.runner.RunOnce(fixture.ctx)
	assertOnboardingActionCount(t, fixture, auditActionOnboardingProgress, 2)
	assertOnboardingActionCount(t, fixture, auditActionOnboardingReconfigured, 1)
}

func assertOnboardingActionCount(t *testing.T, fixture *onboardingFixture, action string, want int) {
	t.Helper()
	audits, err := fixture.store.ListAudits(fixture.ctx, fixture.instance.UserID)
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for _, audit := range audits {
		if audit.ActorID == "e2m-onboarding" && audit.Action == action {
			got++
		}
	}
	if got != want {
		t.Fatalf("onboarding action %q count=%d want=%d", action, got, want)
	}
}
