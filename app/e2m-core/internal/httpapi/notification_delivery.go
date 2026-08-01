package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/store"
)

const notificationTestCooldown = 5 * time.Second

// notificationTestLimiter prevents a double click (or a scripted client) from
// filling an operator's group with test messages. The key is the persisted
// route ID, so the same destination cannot be tested concurrently by its owner
// and an administrator.
type notificationTestLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (l *notificationTestLimiter) reserve(key string, now time.Time) (time.Duration, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = make(map[string]time.Time)
	}
	if previous, ok := l.last[key]; ok {
		remaining := notificationTestCooldown - now.Sub(previous)
		if remaining > 0 {
			return remaining, time.Time{}
		}
	}
	l.last[key] = now
	return 0, now
}

func (l *notificationTestLimiter) release(key string, reservation time.Time) {
	if reservation.IsZero() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if current, ok := l.last[key]; ok && current.Equal(reservation) {
		delete(l.last, key)
	}
}

type notificationDeliveryResponse struct {
	ID               string                               `json:"id"`
	UserID           int64                                `json:"user_id"`
	RouteID          string                               `json:"route_id"`
	RouteName        string                               `json:"route_name"`
	Channel          contracts.NotificationChannel        `json:"channel"`
	Kind             contracts.NotificationDeliveryKind   `json:"kind"`
	Status           contracts.NotificationDeliveryStatus `json:"status"`
	EventLevel       contracts.EventLevel                 `json:"event_level"`
	Title            string                               `json:"title"`
	Attempts         int                                  `json:"attempts"`
	MaxAttempts      int                                  `json:"max_attempts"`
	NextAttemptAt    time.Time                            `json:"next_attempt_at,omitempty"`
	LastErrorCode    string                               `json:"last_error_code,omitempty"`
	LastErrorMessage string                               `json:"last_error_message,omitempty"`
	CreatedAt        time.Time                            `json:"created_at"`
	UpdatedAt        time.Time                            `json:"updated_at"`
	SentAt           *time.Time                           `json:"sent_at,omitempty"`
}

func safeNotificationDelivery(d contracts.NotificationDelivery) notificationDeliveryResponse {
	return notificationDeliveryResponse{
		ID: d.ID, UserID: d.UserID, RouteID: d.RouteID, RouteName: d.RouteName, Channel: d.Channel,
		Kind: d.Kind, Status: d.Status, EventLevel: d.EventLevel, Title: d.Title,
		Attempts: d.Attempts, MaxAttempts: d.MaxAttempts, NextAttemptAt: d.NextAttemptAt,
		LastErrorCode: d.LastErrorCode, LastErrorMessage: d.LastErrorMessage,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, SentAt: d.SentAt,
	}
}

func (s *Server) handleNotificationChannelStatus(w http.ResponseWriter, r *http.Request) {
	actor := currentUser(r)
	statuses := make([]contracts.NotificationChannelStatus, 0, 2)
	for _, channel := range []contracts.NotificationChannel{contracts.NotificationChannelFeishu, contracts.NotificationChannelQQ} {
		configured := s.notificationRouter != nil && s.notificationRouter.ChannelConfigured(channel)
		status := contracts.NotificationChannelStatus{Channel: channel, Scope: "system", Configured: configured, State: contracts.NotificationChannelUnconfigured}
		if configured {
			status.State = contracts.NotificationChannelUnknown
		}
		filter := contracts.NotificationDeliveryFilter{UserID: actor.ID, Channel: channel, Limit: 50}
		deliveries, err := s.store.ListNotificationDeliveries(r.Context(), filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		for _, delivery := range deliveries {
			if !isSystemChannelDelivery(delivery) {
				continue
			}
			switch delivery.Status {
			case contracts.NotificationDeliverySucceeded:
				if delivery.SentAt != nil && (status.LastSuccessAt == nil || delivery.SentAt.After(*status.LastSuccessAt)) {
					status.LastSuccessAt = delivery.SentAt
				}
			case contracts.NotificationDeliveryFailed:
				at := delivery.UpdatedAt
				if status.LastFailureAt == nil || at.After(*status.LastFailureAt) {
					status.LastFailureAt = &at
					status.LastErrorCode = delivery.LastErrorCode
				}
			}
		}
		if configured {
			if status.LastFailureAt != nil && (status.LastSuccessAt == nil || status.LastFailureAt.After(*status.LastSuccessAt)) {
				status.State = contracts.NotificationChannelFailing
			} else if status.LastSuccessAt != nil {
				status.State = contracts.NotificationChannelHealthy
			}
		}
		statuses = append(statuses, status)
	}
	writeJSON(w, http.StatusOK, statuses)
}

func isSystemChannelDelivery(delivery contracts.NotificationDelivery) bool {
	if delivery.Channel == contracts.NotificationChannelFeishu {
		return delivery.TargetRef == "system:feishu"
	}
	if delivery.Channel == contracts.NotificationChannelQQ {
		return delivery.TargetRef == "system:qq"
	}
	return false
}

func (s *Server) handleTestNotificationRoute(w http.ResponseWriter, r *http.Request) {
	s.notificationTargetsMu.Lock()
	defer s.notificationTargetsMu.Unlock()
	route, err := s.store.GetNotificationRoute(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "notification setting not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !s.requireUserNotificationWrite(w, r, route.UserID) {
		return
	}
	if s.notificationRouter == nil {
		writeError(w, http.StatusServiceUnavailable, "notification_unavailable", "notification service is not configured")
		return
	}
	if !s.notificationRouter.RouteConfigured(r.Context(), route) {
		writeError(w, http.StatusConflict, "channel_unconfigured", "the selected notification channel is not configured")
		return
	}
	now := time.Now().UTC()
	retryAfter, reservation := s.notificationTests.reserve(route.ID, now)
	if retryAfter > 0 {
		seconds := int((retryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, http.StatusTooManyRequests, "notification_test_rate_limited", "please wait before sending another test message")
		return
	}
	delivery, _, err := s.notificationRouter.EnqueueRoute(r.Context(), notify.Event{
		UserID: route.UserID, EventLevel: contracts.EventLevelInfo, RiskLevel: contracts.RiskLevelL0,
		Result: "test", Title: "E2M 通知测试", Text: "这是一条测试消息，用于确认通知设置能否正常发送。",
	}, route, contracts.NotificationDeliveryKindTest, true)
	if err != nil {
		s.notificationTests.release(route.ID, reservation)
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: route.UserID, ActorType: "user", ActorID: actor.Email,
		Action: "notification_route.test", RiskLevel: contracts.RiskLevelL0,
		TargetType: "notification_route", TargetID: route.ID, Result: "accepted",
	})
	writeJSON(w, http.StatusAccepted, safeNotificationDelivery(delivery))
}

func (s *Server) handleListNotificationDeliveries(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.scopeNotificationUser(w, r, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "validation_failed", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	status := contracts.NotificationDeliveryStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && !status.Valid() {
		writeError(w, http.StatusBadRequest, "validation_failed", "invalid notification delivery status")
		return
	}
	deliveries, err := s.store.ListNotificationDeliveries(r.Context(), contracts.NotificationDeliveryFilter{
		UserID: userID, RouteID: strings.TrimSpace(r.URL.Query().Get("route_id")), Status: status, Limit: limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	response := make([]notificationDeliveryResponse, 0, len(deliveries))
	for _, delivery := range deliveries {
		response = append(response, safeNotificationDelivery(delivery))
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleRetryNotificationDelivery(w http.ResponseWriter, r *http.Request) {
	s.notificationTargetsMu.Lock()
	defer s.notificationTargetsMu.Unlock()
	existing, err := s.store.GetNotificationDelivery(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "notification delivery not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !s.requireUserNotificationWrite(w, r, existing.UserID) {
		return
	}
	if notify.IsPersonalNotificationTargetRef(existing.TargetRef, existing.UserID, existing.Channel) {
		route := contracts.NotificationRoute{UserID: existing.UserID, Channel: existing.Channel, TargetRef: existing.TargetRef}
		if s.notificationRouter == nil || !s.notificationRouter.RouteConfigured(r.Context(), route) {
			writeError(w, http.StatusConflict, "channel_unconfigured", "the personal notification channel is not configured")
			return
		}
	}
	retried, err := s.store.RetryNotificationDelivery(r.Context(), existing.ID)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "invalid_state", "only one retry may be queued for a failed delivery")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: existing.UserID, ActorType: "user", ActorID: currentUser(r).Email,
		Action: "notification_delivery.retry", RiskLevel: contracts.RiskLevelL1,
		TargetType: "notification_delivery", TargetID: existing.ID, Result: "accepted",
	})
	writeJSON(w, http.StatusAccepted, safeNotificationDelivery(retried))
}

func (s *Server) scopeNotificationUser(w http.ResponseWriter, r *http.Request, requested string) (int64, bool) {
	return s.scopeUser(w, r, requested)
}

func (s *Server) requireUserNotificationWrite(w http.ResponseWriter, r *http.Request, userID int64) bool {
	return s.requireOwnerWrite(w, r, userID)
}
