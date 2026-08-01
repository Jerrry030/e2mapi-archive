package onboarding

import (
	"context"
	"errors"
	"strings"
	"testing"

	"e2m.local/contracts"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/store"
)

type captureOnboardingDispatcher struct {
	userIDs []int64
	events  []notify.Event
}

func (d *captureOnboardingDispatcher) Dispatch(_ context.Context, userID int64, event notify.Event) {
	d.userIDs = append(d.userIDs, userID)
	d.events = append(d.events, event)
}

func TestOnboardingAuditDispatchesOnlyUserFacingBusinessOutcomes(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	capture := &captureOnboardingDispatcher{}
	fixture.runner.notifier = capture
	workflow := contracts.OnboardingWorkflow{
		ID: "workflow-a", UserID: fixture.instance.UserID, InstanceID: fixture.instance.ID,
		PoolID: fixture.pool.ID, Attempts: 3, DesiredGeneration: 2,
	}
	next := fixture.runner.now().UTC().Add(defaultActiveCheck)
	workflow.NextAttemptAt = &next

	for _, action := range []string{
		auditActionOnboardingProgress,
		auditActionOnboardingCompleted,
		auditActionOnboardingVerified,
		auditActionOnboardingReconfigured,
		auditActionOnboardingRepaired,
		auditActionOnboardingRetryScheduled,
		auditActionOnboardingFailed,
		auditActionOnboardingPaused,
	} {
		result, code, level := "accepted", "", contracts.EventLevelNotice
		switch action {
		case auditActionOnboardingProgress:
			result, level = "running", contracts.EventLevelInfo
		case auditActionOnboardingVerified:
			result, level = "verified", contracts.EventLevelInfo
		case auditActionOnboardingRetryScheduled, auditActionOnboardingFailed:
			result, code = "retrying", "connector_unavailable"
			if action == auditActionOnboardingFailed {
				level = contracts.EventLevelWarning
			}
		case auditActionOnboardingPaused:
			result, code = "paused", "pool_inactive"
		}
		fixture.runner.audit(fixture.ctx, workflow, action, result, code, level)
	}

	wantActions := []string{
		auditActionOnboardingCompleted,
		auditActionOnboardingReconfigured,
		auditActionOnboardingRepaired,
		auditActionOnboardingRetryScheduled,
		auditActionOnboardingFailed,
		auditActionOnboardingPaused,
	}
	if len(capture.events) != len(wantActions) {
		t.Fatalf("notifications=%d want=%d events=%+v", len(capture.events), len(wantActions), capture.events)
	}
	for i, event := range capture.events {
		if capture.userIDs[i] != workflow.UserID || event.UserID != workflow.UserID ||
			event.InstanceID != workflow.InstanceID || event.RiskLevel != contracts.RiskLevelL2 ||
			!event.EventLevel.Valid() || strings.TrimSpace(event.Title) == "" || strings.TrimSpace(event.Text) == "" {
			t.Fatalf("notification %d=%+v user=%d", i, event, capture.userIDs[i])
		}
		if event.Fields["instanceName"] != fixture.instance.Name || event.Fields["poolName"] != fixture.pool.Name {
			t.Fatalf("notification %q missing business labels: %+v", wantActions[i], event.Fields)
		}
	}
	if capture.events[4].EventLevel != contracts.EventLevelWarning || capture.events[4].Result != "retrying" ||
		capture.events[4].Fields["reasonCode"] != "connector_unavailable" {
		t.Fatalf("failed notification=%+v", capture.events[4])
	}
}

type failOnboardingAuditStore struct{ store.Store }

func (s failOnboardingAuditStore) AppendAudit(context.Context, contracts.OperationAudit) (contracts.OperationAudit, error) {
	return contracts.OperationAudit{}, errors.New("audit unavailable")
}

func TestOnboardingDoesNotNotifyWhenAuditCannotBePersisted(t *testing.T) {
	fixture := newOnboardingFixture(t, readyOnboardingRuntime("sub2api"), true)
	capture := &captureOnboardingDispatcher{}
	fixture.runner.store = failOnboardingAuditStore{Store: fixture.store}
	fixture.runner.notifier = capture
	fixture.runner.audit(fixture.ctx, contracts.OnboardingWorkflow{
		ID: "workflow-a", UserID: fixture.instance.UserID, InstanceID: fixture.instance.ID,
		PoolID: fixture.pool.ID, Attempts: 1, DesiredGeneration: 1,
	}, auditActionOnboardingCompleted, "accepted", "", contracts.EventLevelNotice)
	if len(capture.events) != 0 {
		t.Fatalf("notification escaped failed audit persistence: %+v", capture.events)
	}
}
