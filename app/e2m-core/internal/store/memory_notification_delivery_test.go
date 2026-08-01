package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryNotificationDeliveryClaimLeaseAndSuccess(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.July, 23, 9, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }

	created, err := st.CreateNotificationDelivery(ctx, notificationDeliveryFixture())
	if err != nil {
		t.Fatalf("create delivery: %v", err)
	}
	if created.ID == "" || created.Status != contracts.NotificationDeliveryPending {
		t.Fatalf("unexpected created delivery: %+v", created)
	}
	if created.Attempts != 0 || created.MaxAttempts != 5 || !created.NextAttemptAt.Equal(now) {
		t.Fatalf("unexpected initial retry state: %+v", created)
	}

	claimed, ok, err := st.ClaimNotificationDelivery(ctx, "worker-a", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim delivery: delivery=%+v claimed=%v err=%v", claimed, ok, err)
	}
	if claimed.ID != created.ID || claimed.Status != contracts.NotificationDeliveryProcessing || claimed.Attempts != 1 || claimed.LeaseVersion != 1 {
		t.Fatalf("unexpected claimed delivery: %+v", claimed)
	}
	if claimed.LeaseOwner != "worker-a" || claimed.LeaseUntil == nil || !claimed.LeaseUntil.Equal(now.Add(30*time.Second)) {
		t.Fatalf("unexpected first lease: %+v", claimed)
	}
	if _, secondClaim, err := st.ClaimNotificationDelivery(ctx, "worker-b", time.Minute); err != nil || secondClaim {
		t.Fatalf("active lease was claimed again: claimed=%v err=%v", secondClaim, err)
	}

	// A crashed worker's processing row becomes claimable when its lease expires.
	now = now.Add(30 * time.Second)
	reclaimed, ok, err := st.ClaimNotificationDelivery(ctx, "worker-b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("reclaim expired lease: delivery=%+v claimed=%v err=%v", reclaimed, ok, err)
	}
	if reclaimed.ID != created.ID || reclaimed.Attempts != 2 || reclaimed.LeaseOwner != "worker-b" || reclaimed.LeaseVersion != 2 {
		t.Fatalf("unexpected reclaimed delivery: %+v", reclaimed)
	}
	if _, err := st.CompleteNotificationDelivery(ctx, created.ID, "worker-a", claimed.LeaseVersion, true, "", "", time.Time{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale worker completed reclaimed delivery: %v", err)
	}
	if _, err := st.CompleteNotificationDelivery(ctx, created.ID, "worker-b", claimed.LeaseVersion, true, "", "", time.Time{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale lease version completed reclaimed delivery: %v", err)
	}

	completed, err := st.CompleteNotificationDelivery(ctx, created.ID, "worker-b", reclaimed.LeaseVersion, true, "ignored", "ignored", time.Time{})
	if err != nil {
		t.Fatalf("complete delivery: %v", err)
	}
	if completed.Status != contracts.NotificationDeliverySucceeded || completed.SentAt == nil || !completed.SentAt.Equal(now) {
		t.Fatalf("unexpected successful delivery: %+v", completed)
	}
	if completed.LeaseOwner != "" || completed.LeaseUntil != nil || completed.LastErrorCode != "" || completed.LastErrorMessage != "" {
		t.Fatalf("success retained lease/error state: %+v", completed)
	}
	if _, ok, err := st.ClaimNotificationDelivery(ctx, "worker-c", time.Minute); err != nil || ok {
		t.Fatalf("terminal delivery was claimable: claimed=%v err=%v", ok, err)
	}
}

func TestMemoryNotificationDeliveryRetryFailureAndManualResend(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.July, 23, 10, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }

	input := notificationDeliveryFixture()
	input.MaxAttempts = 2
	created, err := st.CreateNotificationDelivery(ctx, input)
	if err != nil {
		t.Fatalf("create delivery: %v", err)
	}
	first, ok, err := st.ClaimNotificationDelivery(ctx, "worker-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim first attempt: delivery=%+v claimed=%v err=%v", first, ok, err)
	}
	nextAttempt := now.Add(2 * time.Minute)
	retrying, err := st.CompleteNotificationDelivery(ctx, created.ID, "worker-a", first.LeaseVersion, false, "provider_busy", "try later", nextAttempt)
	if err != nil {
		t.Fatalf("schedule retry: %v", err)
	}
	if retrying.Status != contracts.NotificationDeliveryRetrying || retrying.Attempts != 1 || !retrying.NextAttemptAt.Equal(nextAttempt) {
		t.Fatalf("unexpected retry state: %+v", retrying)
	}
	if retrying.LastErrorCode != "provider_busy" || retrying.LastErrorMessage != "try later" {
		t.Fatalf("retry error was not recorded: %+v", retrying)
	}
	if _, ok, err := st.ClaimNotificationDelivery(ctx, "worker-b", time.Minute); err != nil || ok {
		t.Fatalf("future retry was claimed early: claimed=%v err=%v", ok, err)
	}

	now = nextAttempt
	second, ok, err := st.ClaimNotificationDelivery(ctx, "worker-b", time.Minute)
	if err != nil || !ok || second.Attempts != 2 {
		t.Fatalf("claim second attempt: delivery=%+v claimed=%v err=%v", second, ok, err)
	}
	failed, err := st.CompleteNotificationDelivery(ctx, created.ID, "worker-b", second.LeaseVersion, false, "provider_busy", "still busy", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("complete final automatic attempt: %v", err)
	}
	if failed.Status != contracts.NotificationDeliveryFailed || failed.Attempts != 2 || failed.SentAt != nil {
		t.Fatalf("max attempts did not become terminal failure: %+v", failed)
	}

	resent, err := st.RetryNotificationDelivery(ctx, created.ID)
	if err != nil {
		t.Fatalf("manual resend: %v", err)
	}
	if resent.ID == created.ID || resent.RetriedFromID != created.ID || resent.Status != contracts.NotificationDeliveryPending {
		t.Fatalf("unexpected manual resend clone: %+v", resent)
	}
	if resent.Attempts != 0 || resent.LastErrorCode != "" || resent.LastErrorMessage != "" || resent.SentAt != nil {
		t.Fatalf("manual resend retained prior attempt state: %+v", resent)
	}
	original, err := st.GetNotificationDelivery(ctx, created.ID)
	if err != nil || original.Status != contracts.NotificationDeliveryFailed {
		t.Fatalf("manual resend changed original failure: delivery=%+v err=%v", original, err)
	}
	if _, err := st.RetryNotificationDelivery(ctx, created.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("same failure could be resent more than once: %v", err)
	}

	manualAttempt, ok, err := st.ClaimNotificationDelivery(ctx, "worker-c", time.Minute)
	if err != nil || !ok || manualAttempt.ID != resent.ID {
		t.Fatalf("claim manual resend: delivery=%+v claimed=%v err=%v", manualAttempt, ok, err)
	}
	permanentFailure, err := st.CompleteNotificationDelivery(ctx, resent.ID, "worker-c", manualAttempt.LeaseVersion, false, "invalid_target", "invalid", time.Time{})
	if err != nil {
		t.Fatalf("complete permanent failure: %v", err)
	}
	if permanentFailure.Status != contracts.NotificationDeliveryFailed || permanentFailure.Attempts != 1 {
		t.Fatalf("permanent error did not fail immediately: %+v", permanentFailure)
	}
}

func notificationDeliveryFixture() contracts.NotificationDelivery {
	return contracts.NotificationDelivery{
		UserID:    42,
		RouteID:   "route-ops",
		RouteName: "运营通知",
		Channel:   contracts.NotificationChannelFeishu,
		Kind:      contracts.NotificationDeliveryKindEvent,
		Title:     "库存不足",
		Text:      "可用库存低于安全线",
	}
}
