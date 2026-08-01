package store

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"e2m.local/contracts"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresNotificationRouteTargetConstraint(t *testing.T) {
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

	key := newID("notification-target")
	owner, err := st.CreateUser(ctx, contracts.User{
		Email: key + "@example.com", PasswordHash: "test-only",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create route owner: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner.ID)
	})

	create := func(name string, channel contracts.NotificationChannel, targetRef string, enabled bool) error {
		t.Helper()
		_, createErr := st.CreateNotificationRoute(ctx, contracts.NotificationRoute{
			ID: key + "-" + name, UserID: owner.ID, Name: name,
			Channel: channel, TargetRef: targetRef, MinRiskLevel: contracts.RiskLevelL1,
			Enabled: enabled,
		})
		return createErr
	}

	valid := []struct {
		name      string
		channel   contracts.NotificationChannel
		targetRef string
		enabled   bool
	}{
		{"feishu", contracts.NotificationChannelFeishu, "system:feishu", true},
		{"qq", contracts.NotificationChannelQQ, "system:qq", false},
		{"personal-feishu", contracts.NotificationChannelFeishu, "credential_ref:user/" + strconv.FormatInt(owner.ID, 10) + "/notification/personal-feishu", true},
		{"personal-qq", contracts.NotificationChannelQQ, "credential_ref:user/" + strconv.FormatInt(owner.ID, 10) + "/notification/personal-qq", true},
		{"webhook", contracts.NotificationChannelWebhook, "credential_ref:user/" + strconv.FormatInt(owner.ID, 10) + "/notification/ops.main-1", true},
		{"quarantined-webhook", contracts.NotificationChannelWebhook, "", false},
		{"quarantined-unknown", contracts.NotificationChannel("legacy"), "", false},
	}
	for _, tc := range valid {
		if err := create(tc.name, tc.channel, tc.targetRef, tc.enabled); err != nil {
			t.Errorf("create valid %s route: %v", tc.name, err)
		}
	}

	ownerPrefix := "credential_ref:user/" + strconv.FormatInt(owner.ID, 10) + "/notification/"
	invalid := []struct {
		name      string
		channel   contracts.NotificationChannel
		targetRef string
		enabled   bool
	}{
		{"wrong-owner", contracts.NotificationChannelWebhook, "credential_ref:user/0/notification/ops", true},
		{"wrong-owner-disabled", contracts.NotificationChannelWebhook, "credential_ref:user/0/notification/ops", false},
		{"unsafe-name", contracts.NotificationChannelWebhook, ownerPrefix + "ops/child", true},
		{"untrimmed", contracts.NotificationChannelWebhook, " " + ownerPrefix + "ops ", true},
		{"wrong-system-ref", contracts.NotificationChannelFeishu, "system:qq", false},
		{"wrong-personal-channel", contracts.NotificationChannelFeishu, ownerPrefix + "personal-qq", true},
		{"active-unknown", contracts.NotificationChannel("legacy"), "", true},
	}
	for _, tc := range invalid {
		err := create(tc.name, tc.channel, tc.targetRef, tc.enabled)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" ||
			pgErr.ConstraintName != "notification_route_target_ref_check" {
			t.Errorf("create invalid %s route: error=%v, want notification target check violation", tc.name, err)
		}
	}
}
