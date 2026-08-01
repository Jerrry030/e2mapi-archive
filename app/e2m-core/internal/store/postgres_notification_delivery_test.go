package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPostgresNotificationDeliveryLifecycleAndFencing(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	key := newID("notification-delivery-pg")
	owner, err := st.CreateUser(ctx, contracts.User{
		Email: key + "@example.com", PasswordHash: "test-only",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create delivery owner: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM notification_deliveries WHERE route_id=$1`, key)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner.ID)
	})

	created, err := st.CreateNotificationDelivery(ctx, contracts.NotificationDelivery{
		UserID: owner.ID, RouteID: key, RouteName: "ops", TargetRef: "system:feishu",
		Channel: contracts.NotificationChannelFeishu, Kind: contracts.NotificationDeliveryKindEvent,
		EventLevel: contracts.EventLevelWarning, RiskLevel: contracts.RiskLevelL1,
		Title: "test", Text: "body", Fields: map[string]string{"source": "postgres"}, MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("create delivery: %v", err)
	}
	if created.RetriedFromID != "" || created.Fields["source"] != "postgres" {
		t.Fatalf("nullable/json fields did not round-trip: %+v", created)
	}

	first, claimed, err := st.ClaimNotificationDelivery(ctx, "same-worker", 5*time.Minute)
	if err != nil || !claimed || first.ID != created.ID || first.LeaseVersion != 1 || first.Attempts != 1 {
		t.Fatalf("first claim: delivery=%+v claimed=%v err=%v", first, claimed, err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE notification_deliveries SET lease_until=statement_timestamp()-interval '1 second' WHERE id=$1`, created.ID); err != nil {
		t.Fatalf("expire first lease: %v", err)
	}
	second, claimed, err := st.ClaimNotificationDelivery(ctx, "same-worker", 5*time.Minute)
	if err != nil || !claimed || second.ID != created.ID || second.LeaseVersion != 2 || second.Attempts != 2 {
		t.Fatalf("second claim: delivery=%+v claimed=%v err=%v", second, claimed, err)
	}
	if _, err := st.CompleteNotificationDelivery(ctx, created.ID, "same-worker", first.LeaseVersion, true, "", "", time.Time{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale lease generation completed delivery: %v", err)
	}
	failed, err := st.CompleteNotificationDelivery(ctx, created.ID, "same-worker", second.LeaseVersion, false, "provider_busy", "busy", time.Now().Add(time.Hour))
	if err != nil || failed.Status != contracts.NotificationDeliveryFailed {
		t.Fatalf("complete final attempt: delivery=%+v err=%v", failed, err)
	}

	retried, err := st.RetryNotificationDelivery(ctx, created.ID)
	if err != nil || retried.RetriedFromID != created.ID || retried.LeaseVersion != 0 {
		t.Fatalf("manual retry: delivery=%+v err=%v", retried, err)
	}
	if _, err := st.RetryNotificationDelivery(ctx, created.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate manual retry error=%v, want ErrConflict", err)
	}
}

func TestPostgresNotificationDeliveryFinalExpiredLeaseBecomesFailed(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	key := newID("notification-delivery-expired-pg")
	owner, err := st.CreateUser(ctx, contracts.User{
		Email: key + "@example.com", PasswordHash: "test-only",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create delivery owner: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM notification_deliveries WHERE route_id=$1`, key)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner.ID)
	})
	created, err := st.CreateNotificationDelivery(ctx, contracts.NotificationDelivery{
		UserID: owner.ID, RouteID: key, RouteName: "ops", TargetRef: "system:feishu",
		Channel: contracts.NotificationChannelFeishu, Kind: contracts.NotificationDeliveryKindEvent,
		EventLevel: contracts.EventLevelWarning, RiskLevel: contracts.RiskLevelL1,
		Title: "test", Text: "body", MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("create delivery: %v", err)
	}
	if _, claimed, err := st.ClaimNotificationDelivery(ctx, "worker", time.Minute); err != nil || !claimed {
		t.Fatalf("claim final attempt: claimed=%v err=%v", claimed, err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE notification_deliveries SET lease_until=statement_timestamp()-interval '1 second' WHERE id=$1`, created.ID); err != nil {
		t.Fatalf("expire final lease: %v", err)
	}
	if _, claimed, err := st.ClaimNotificationDelivery(ctx, "other-worker", time.Minute); err != nil || claimed {
		t.Fatalf("exhausted delivery was reclaimed: claimed=%v err=%v", claimed, err)
	}
	got, err := st.GetNotificationDelivery(ctx, created.ID)
	if err != nil || got.Status != contracts.NotificationDeliveryFailed || got.LastErrorCode != "lease_expired" || got.LeaseUntil != nil {
		t.Fatalf("final expired lease was not terminalized: delivery=%+v err=%v", got, err)
	}
}
