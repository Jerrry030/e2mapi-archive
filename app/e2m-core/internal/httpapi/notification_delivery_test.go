package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/store"
)

type acceptingNotifier struct {
	channel contracts.NotificationChannel
}

func (n acceptingNotifier) Channel() contracts.NotificationChannel   { return n.channel }
func (n acceptingNotifier) Send(context.Context, notify.Event) error { return nil }

func notificationTestServer(t *testing.T) (*Server, store.Store, string, contracts.User) {
	t.Helper()
	srv, st, authSvc := newTestServer(t)
	owner := createLoginUser(t, authSvc, testUserEmail(t, "notification-delivery-owner"), contracts.UserRoleClient)
	token, _, err := authSvc.Login(context.Background(), owner.Email, "password123")
	if err != nil {
		t.Fatalf("login owner: %v", err)
	}
	router := notify.NewRouter(acceptingNotifier{channel: contracts.NotificationChannelFeishu}, nil)
	router.SetDeliveryStore(st)
	srv.SetNotificationRouter(router)
	return srv, st, token, owner
}

func TestNotificationRouteTestQueuesDisabledRouteAndRateLimits(t *testing.T) {
	srv, st, token, owner := notificationTestServer(t)
	route, err := st.CreateNotificationRoute(context.Background(), contracts.NotificationRoute{
		UserID: owner.ID, Name: "disabled test route", Channel: contracts.NotificationChannelFeishu,
		TargetRef: "system:feishu", MinRiskLevel: contracts.RiskLevelL2,
		MinEventLevel: contracts.EventLevelWarning, Enabled: false,
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}

	path := "/api/v1/notification-routes/" + route.ID + "/test"
	first := do(t, srv.Routes(), http.MethodPost, path, token, nil)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first test: got %d %s", first.Code, first.Body.String())
	}
	var queued notificationDeliveryResponse
	decodeResponse(t, first, &queued)
	if queued.Kind != contracts.NotificationDeliveryKindTest || queued.Status != contracts.NotificationDeliveryPending {
		t.Fatalf("queued test = %+v", queued)
	}
	if strings.Contains(first.Body.String(), "target_ref") || strings.Contains(first.Body.String(), "system:feishu") ||
		strings.Contains(first.Body.String(), "Fields") {
		t.Fatalf("test response leaked internal delivery fields: %s", first.Body.String())
	}

	second := do(t, srv.Routes(), http.MethodPost, path, token, nil)
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("second test: got %d headers=%v body=%s", second.Code, second.Header(), second.Body.String())
	}
	deliveries, err := st.ListNotificationDeliveries(context.Background(), contracts.NotificationDeliveryFilter{UserID: owner.ID})
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("deliveries after rate limit: count=%d err=%v", len(deliveries), err)
	}
}

func TestNotificationDeliveryListIsOwnerScopedAndRedacted(t *testing.T) {
	srv, st, token, owner := notificationTestServer(t)
	other := createStoreUser(t, st, "notification-delivery-other", contracts.UserRoleClient)
	ownerRoute, _ := st.CreateNotificationRoute(context.Background(), contracts.NotificationRoute{
		UserID: owner.ID, Name: "owner", Channel: contracts.NotificationChannelFeishu,
		TargetRef: "system:feishu", MinRiskLevel: contracts.RiskLevelL0, Enabled: true,
	})
	otherRoute, _ := st.CreateNotificationRoute(context.Background(), contracts.NotificationRoute{
		UserID: other.ID, Name: "other", Channel: contracts.NotificationChannelFeishu,
		TargetRef: "system:feishu", MinRiskLevel: contracts.RiskLevelL0, Enabled: true,
	})
	for _, input := range []contracts.NotificationDelivery{
		{UserID: owner.ID, RouteID: ownerRoute.ID, RouteName: ownerRoute.Name, TargetRef: "system:feishu", Template: "secret-ish template", Channel: contracts.NotificationChannelFeishu, Kind: contracts.NotificationDeliveryKindEvent, EventLevel: contracts.EventLevelWarning, RiskLevel: contracts.RiskLevelL1, Title: "owner event", Text: "owner body", Fields: map[string]string{"credential": "do-not-return"}},
		{UserID: other.ID, RouteID: otherRoute.ID, RouteName: otherRoute.Name, TargetRef: "system:feishu", Channel: contracts.NotificationChannelFeishu, Kind: contracts.NotificationDeliveryKindEvent, EventLevel: contracts.EventLevelWarning, RiskLevel: contracts.RiskLevelL1, Title: "other event", Text: "other body"},
	} {
		if _, err := st.CreateNotificationDelivery(context.Background(), input); err != nil {
			t.Fatalf("create delivery: %v", err)
		}
	}

	listed := do(t, srv.Routes(), http.MethodGet, "/api/v1/notification-deliveries", token, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list own deliveries: %d %s", listed.Code, listed.Body.String())
	}
	var rows []notificationDeliveryResponse
	decodeResponse(t, listed, &rows)
	if len(rows) != 1 || rows[0].UserID != owner.ID || rows[0].Title != "owner event" {
		t.Fatalf("owner-scoped rows = %+v", rows)
	}
	body := listed.Body.String()
	for _, forbidden := range []string{"target_ref", "secret-ish template", "do-not-return", "owner body"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("delivery response contains %q: %s", forbidden, body)
		}
	}

	cross := do(t, srv.Routes(), http.MethodGet, "/api/v1/notification-deliveries?user_id="+strconv.FormatInt(other.ID, 10), token, nil)
	if cross.Code != http.StatusForbidden {
		t.Fatalf("cross-owner list: got %d %s", cross.Code, cross.Body.String())
	}
}

func TestNotificationChannelStatusIsOwnerScoped(t *testing.T) {
	srv, st, token, owner := notificationTestServer(t)
	other := createStoreUser(t, st, "notification-status-other", contracts.UserRoleClient)
	ownerRoute, _ := st.CreateNotificationRoute(context.Background(), contracts.NotificationRoute{
		UserID: owner.ID, Name: "owner", Channel: contracts.NotificationChannelFeishu,
		TargetRef: "system:feishu", MinRiskLevel: contracts.RiskLevelL0, Enabled: true,
	})
	otherRoute, _ := st.CreateNotificationRoute(context.Background(), contracts.NotificationRoute{
		UserID: other.ID, Name: "other", Channel: contracts.NotificationChannelFeishu,
		TargetRef: "system:feishu", MinRiskLevel: contracts.RiskLevelL0, Enabled: true,
	})
	ownerDelivery, _ := st.CreateNotificationDelivery(context.Background(), contracts.NotificationDelivery{
		UserID: owner.ID, RouteID: ownerRoute.ID, RouteName: ownerRoute.Name, TargetRef: ownerRoute.TargetRef,
		Channel: ownerRoute.Channel, Kind: contracts.NotificationDeliveryKindEvent,
		EventLevel: contracts.EventLevelWarning, RiskLevel: contracts.RiskLevelL1, Title: "owner pending",
		NextAttemptAt: time.Now().UTC().Add(time.Hour),
	})
	otherDelivery, _ := st.CreateNotificationDelivery(context.Background(), contracts.NotificationDelivery{
		UserID: other.ID, RouteID: otherRoute.ID, RouteName: otherRoute.Name, TargetRef: otherRoute.TargetRef,
		Channel: otherRoute.Channel, Kind: contracts.NotificationDeliveryKindEvent,
		EventLevel: contracts.EventLevelWarning, RiskLevel: contracts.RiskLevelL1, Title: "other failure", MaxAttempts: 1,
	})
	claimed, ok, err := st.ClaimNotificationDelivery(context.Background(), "status-worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim a status fixture: delivery=%+v claimed=%v err=%v", claimed, ok, err)
	}
	if claimed.ID != otherDelivery.ID {
		t.Fatalf("claimed delivery %q, want other %q (owner=%q)", claimed.ID, otherDelivery.ID, ownerDelivery.ID)
	}
	if _, err := st.CompleteNotificationDelivery(context.Background(), claimed.ID, "status-worker", claimed.LeaseVersion, false, "other_private_failure", "other failure", time.Time{}); err != nil {
		t.Fatal(err)
	}

	w := do(t, srv.Routes(), http.MethodGet, "/api/v1/notification-channels/status", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("channel status: %d %s", w.Code, w.Body.String())
	}
	var statuses []contracts.NotificationChannelStatus
	decodeResponse(t, w, &statuses)
	if len(statuses) != 2 || statuses[0].State != contracts.NotificationChannelUnknown || statuses[0].LastErrorCode != "" {
		t.Fatalf("owner status leaked another user's failure: %+v", statuses)
	}
}

func TestNotificationDeliveryRetryKeepsOriginalFailureAndIsSingleUse(t *testing.T) {
	srv, st, token, owner := notificationTestServer(t)
	route, _ := st.CreateNotificationRoute(context.Background(), contracts.NotificationRoute{
		UserID: owner.ID, Name: "failed", Channel: contracts.NotificationChannelFeishu,
		TargetRef: "system:feishu", MinRiskLevel: contracts.RiskLevelL0, Enabled: true,
	})
	failed, err := st.CreateNotificationDelivery(context.Background(), contracts.NotificationDelivery{
		UserID: owner.ID, RouteID: route.ID, RouteName: route.Name, TargetRef: route.TargetRef,
		Channel: route.Channel, Kind: contracts.NotificationDeliveryKindEvent, Status: contracts.NotificationDeliveryFailed,
		EventLevel: contracts.EventLevelWarning, RiskLevel: contracts.RiskLevelL1,
		Title: "failed event", Text: "failed body", Attempts: 5, MaxAttempts: 5,
		LastErrorCode: "provider_unavailable", LastErrorMessage: "接收渠道暂时不可用",
	})
	if err != nil {
		t.Fatalf("create failed delivery: %v", err)
	}
	path := "/api/v1/notification-deliveries/" + failed.ID + "/retry"
	first := do(t, srv.Routes(), http.MethodPost, path, token, nil)
	if first.Code != http.StatusAccepted {
		t.Fatalf("retry failed delivery: %d %s", first.Code, first.Body.String())
	}
	var retried notificationDeliveryResponse
	if err := json.Unmarshal(first.Body.Bytes(), &retried); err != nil {
		t.Fatalf("decode retry: %v", err)
	}
	if retried.ID == failed.ID || retried.Status != contracts.NotificationDeliveryPending || retried.Attempts != 0 {
		t.Fatalf("retry clone = %+v", retried)
	}
	original, err := st.GetNotificationDelivery(context.Background(), failed.ID)
	if err != nil || original.Status != contracts.NotificationDeliveryFailed || original.Attempts != 5 {
		t.Fatalf("original failure changed: %+v err=%v", original, err)
	}
	second := do(t, srv.Routes(), http.MethodPost, path, token, nil)
	if second.Code != http.StatusConflict {
		t.Fatalf("duplicate retry: got %d %s", second.Code, second.Body.String())
	}
}
