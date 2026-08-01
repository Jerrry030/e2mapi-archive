package onboarding

import (
	"strconv"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestCompleteActiveClassifiesReadyOutcomesAndKeepsOperationRiskSeparate(t *testing.T) {
	tests := []struct {
		name                  string
		lastReadyGeneration   int64
		desiredGeneration     int64
		verifiedWithoutRepair bool
		wantAction            string
		wantResult            string
		wantLevel             contracts.EventLevel
	}{
		{
			name: "first onboarding", lastReadyGeneration: 0, desiredGeneration: 1,
			wantAction: auditActionOnboardingCompleted, wantResult: "accepted", wantLevel: contracts.EventLevelNotice,
		},
		{
			name: "periodic verification", lastReadyGeneration: 1, desiredGeneration: 1, verifiedWithoutRepair: true,
			wantAction: auditActionOnboardingVerified, wantResult: "verified", wantLevel: contracts.EventLevelInfo,
		},
		{
			name: "desired state reconfiguration", lastReadyGeneration: 1, desiredGeneration: 2,
			wantAction: auditActionOnboardingReconfigured, wantResult: "accepted", wantLevel: contracts.EventLevelNotice,
		},
		{
			name: "same generation repair", lastReadyGeneration: 2, desiredGeneration: 2,
			wantAction: auditActionOnboardingRepaired, wantResult: "accepted", wantLevel: contracts.EventLevelNotice,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
			workflow, err := fixture.store.UpsertOnboardingWorkflow(fixture.ctx, contracts.OnboardingWorkflow{
				UserID: fixture.instance.UserID, InstanceID: fixture.instance.ID, PoolID: fixture.pool.ID,
				ConnectorID: fixture.connector.ID, DesiredFingerprint: "classification",
			})
			if err != nil {
				t.Fatal(err)
			}
			workflow, claimed, err := fixture.store.ClaimOnboardingWorkflow(fixture.ctx, "classification-worker", time.Minute)
			if err != nil || !claimed {
				t.Fatalf("claim workflow=%+v claimed=%v err=%v", workflow, claimed, err)
			}
			workflow.DesiredGeneration = tt.desiredGeneration
			workflow.LastReadyGeneration = tt.lastReadyGeneration

			if err := fixture.runner.completeActive(fixture.ctx, workflow, tt.verifiedWithoutRepair); err != nil {
				t.Fatalf("completeActive: %v", err)
			}
			audit := findOnboardingAudit(t, fixture, tt.wantAction)
			if audit.Result != tt.wantResult || audit.EventLevel != tt.wantLevel ||
				audit.RiskLevel != contracts.RiskLevelL2 {
				t.Fatalf("classified audit=%+v want result=%q event=%q risk=L2",
					audit, tt.wantResult, tt.wantLevel)
			}
			if audit.Details["instance_name"] != fixture.instance.Name ||
				audit.Details["pool_name"] != fixture.pool.Name {
				t.Fatalf("classified audit missing business names: %+v", audit.Details)
			}
		})
	}
}

func TestDeferWorkflowEscalatesOnlyContinuousFailureEventLevel(t *testing.T) {
	tests := []struct {
		name       string
		attempts   int
		wantAction string
		wantLevel  contracts.EventLevel
	}{
		{name: "early retry", attempts: 1, wantAction: auditActionOnboardingRetryScheduled, wantLevel: contracts.EventLevelNotice},
		{name: "continuous failure", attempts: 3, wantAction: auditActionOnboardingFailed, wantLevel: contracts.EventLevelWarning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
			workflow, err := fixture.store.UpsertOnboardingWorkflow(fixture.ctx, contracts.OnboardingWorkflow{
				UserID: fixture.instance.UserID, InstanceID: fixture.instance.ID, PoolID: fixture.pool.ID,
				ConnectorID: fixture.connector.ID, DesiredFingerprint: "failure-classification",
			})
			if err != nil {
				t.Fatal(err)
			}
			for attempt := 1; attempt <= tt.attempts; attempt++ {
				var claimed bool
				workflow, claimed, err = fixture.store.ClaimOnboardingWorkflow(fixture.ctx, "failure-worker", time.Minute)
				if err != nil || !claimed {
					t.Fatalf("attempt %d claim workflow=%+v claimed=%v err=%v", attempt, workflow, claimed, err)
				}
				if attempt < tt.attempts {
					if err := fixture.store.ReleaseOnboardingWorkflowLease(
						fixture.ctx, workflow.ID, workflow.LeaseOwner, workflow.Version,
					); err != nil {
						t.Fatalf("attempt %d release: %v", attempt, err)
					}
				}
			}
			cause := stepError("binding_delivery_failed", nil)
			if err := fixture.runner.deferWorkflow(fixture.ctx, workflow, cause, false); err == nil {
				t.Fatal("deferWorkflow unexpectedly returned nil cause")
			}
			audit := findOnboardingAudit(t, fixture, tt.wantAction)
			if audit.Result != "retrying" || audit.EventLevel != tt.wantLevel ||
				audit.RiskLevel != contracts.RiskLevelL2 {
				t.Fatalf("failure audit=%+v want retrying event=%q risk=L2", audit, tt.wantLevel)
			}
			if audit.Details["attempts"] != strconv.Itoa(tt.attempts) || audit.Details["next_attempt_at"] == "" {
				t.Fatalf("failure audit details=%+v", audit.Details)
			}
		})
	}
}
func findOnboardingAudit(t *testing.T, fixture *onboardingFixture, action string) contracts.OperationAudit {
	t.Helper()
	audits, err := fixture.store.ListAudits(fixture.ctx, fixture.instance.UserID)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(audits) - 1; i >= 0; i-- {
		if audits[i].ActorID == "e2m-onboarding" && audits[i].Action == action {
			return audits[i]
		}
	}
	t.Fatalf("onboarding audit %q not found", action)
	return contracts.OperationAudit{}
}
