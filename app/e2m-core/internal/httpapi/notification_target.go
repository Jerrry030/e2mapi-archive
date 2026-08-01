package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/auth"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/vault"
)

func parsePersonalNotificationChannel(raw string) (contracts.NotificationChannel, bool) {
	channel := contracts.NotificationChannel(strings.ToLower(strings.TrimSpace(raw)))
	return channel, channel == contracts.NotificationChannelFeishu || channel == contracts.NotificationChannelQQ
}

func (s *Server) handleListNotificationTargets(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	userID, ok := s.scopeOwnerUser(w, r, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	if userID == 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id is required")
		return
	}
	out := make([]contracts.NotificationTarget, 0, 2)
	for _, channel := range []contracts.NotificationChannel{contracts.NotificationChannelFeishu, contracts.NotificationChannelQQ} {
		target, err := s.readNotificationTarget(r, userID, channel)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "vault_error", "notification target could not be read")
			return
		}
		out = append(out, target)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpsertNotificationTarget(w http.ResponseWriter, r *http.Request) {
	s.notificationTargetsMu.Lock()
	defer s.notificationTargetsMu.Unlock()
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	channel, ok := parsePersonalNotificationChannel(r.PathValue("channel"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "notification channel not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var input contracts.UpsertNotificationTargetRequest
	if err := decodeStrictJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !auth.IsPlatformAdmin(currentUser(r)) && input.UserID == 0 {
		input.UserID = currentUser(r).ID
	}
	requested := ""
	if input.UserID != 0 {
		requested = strconv.FormatInt(input.UserID, 10)
	}
	userID, ok := s.scopeOwnerUser(w, r, requested)
	if !ok {
		return
	}
	if userID <= 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id is required")
		return
	}
	if !s.requireOwnerWrite(w, r, userID) {
		return
	}
	if _, ok := s.enabledUserWithRole(w, r, userID, contracts.UserRoleOwner, "notification owner"); !ok {
		return
	}
	if invalidTargetFields(input, channel) {
		writeError(w, http.StatusBadRequest, "validation_failed", "request contains fields for another notification channel")
		return
	}
	if input.ClearSigningSecret && nonEmptyString(input.SigningSecret) {
		writeError(w, http.StatusBadRequest, "validation_failed", "a secret cannot be replaced and cleared in the same request")
		return
	}
	if channel == contracts.NotificationChannelQQ && input.ClearAccessToken {
		writeError(w, http.StatusBadRequest, "validation_failed", "a public OneBot target requires an access_token")
		return
	}
	credential, exists, err := s.resolvePersonalCredential(r, userID, channel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "vault_error", "notification target could not be read")
		return
	}
	if !exists {
		credential = notify.PersonalTargetCredential{Channel: channel}
	}
	applyTargetUpdate(&credential, input, channel)
	encoded, err := notify.EncodePersonalTargetCredential(credential)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}
	ref, _ := notify.PersonalNotificationTargetRef(userID, channel)
	if _, err := s.secrets.Store(r.Context(), ref, encoded); err != nil {
		writeError(w, http.StatusInternalServerError, "vault_error", "notification target could not be stored")
		return
	}
	s.auditNotificationTarget(r, userID, channel, "notification_target.upsert")
	writeJSON(w, http.StatusOK, safeNotificationTarget(userID, credential))
}

func (s *Server) handleDeleteNotificationTarget(w http.ResponseWriter, r *http.Request) {
	s.notificationTargetsMu.Lock()
	defer s.notificationTargetsMu.Unlock()
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	channel, ok := parsePersonalNotificationChannel(r.PathValue("channel"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "notification channel not found")
		return
	}
	userID, ok := s.scopeOwnerUser(w, r, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	if userID <= 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id is required")
		return
	}
	if !s.requireOwnerWrite(w, r, userID) {
		return
	}
	ref, _ := notify.PersonalNotificationTargetRef(userID, channel)
	routes, err := s.store.ListNotificationRoutes(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	for _, route := range routes {
		if route.Enabled && route.TargetRef == ref {
			writeError(w, http.StatusConflict, "notification_target_in_use", "notification target is used by an enabled notification setting")
			return
		}
	}
	for _, status := range []contracts.NotificationDeliveryStatus{
		contracts.NotificationDeliveryPending, contracts.NotificationDeliveryProcessing, contracts.NotificationDeliveryRetrying,
	} {
		deliveries, err := s.store.ListNotificationDeliveries(r.Context(), contracts.NotificationDeliveryFilter{
			UserID: userID, Channel: channel, TargetRef: ref, Status: status, Limit: 1,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		if len(deliveries) > 0 {
			writeError(w, http.StatusConflict, "notification_target_in_use", "notification target has messages waiting to be sent")
			return
		}
	}
	if err := s.secrets.Delete(r.Context(), ref); err != nil && !errors.Is(err, vault.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "vault_error", "notification target could not be deleted")
		return
	}
	s.auditNotificationTarget(r, userID, channel, "notification_target.delete")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func invalidTargetFields(input contracts.UpsertNotificationTargetRequest, channel contracts.NotificationChannel) bool {
	if channel == contracts.NotificationChannelFeishu {
		return input.OneBotURL != nil || input.AccessToken != nil || input.ClearAccessToken || input.GroupID != nil
	}
	return input.WebhookURL != nil || input.SigningSecret != nil || input.ClearSigningSecret
}

func nonEmptyString(value *string) bool { return value != nil && strings.TrimSpace(*value) != "" }

func applyTargetUpdate(target *notify.PersonalTargetCredential, input contracts.UpsertNotificationTargetRequest, channel contracts.NotificationChannel) {
	if channel == contracts.NotificationChannelFeishu {
		if nonEmptyString(input.WebhookURL) {
			target.WebhookURL = strings.TrimSpace(*input.WebhookURL)
		}
		if input.ClearSigningSecret {
			target.SigningSecret = ""
		} else if nonEmptyString(input.SigningSecret) {
			target.SigningSecret = strings.TrimSpace(*input.SigningSecret)
		}
		return
	}
	if nonEmptyString(input.OneBotURL) {
		target.OneBotURL = strings.TrimSpace(*input.OneBotURL)
	}
	if nonEmptyString(input.AccessToken) {
		target.AccessToken = strings.TrimSpace(*input.AccessToken)
	}
	if nonEmptyString(input.GroupID) {
		raw := strings.TrimSpace(*input.GroupID)
		if isPositiveDecimal(raw) {
			id, _ := strconv.ParseInt(raw, 10, 64)
			target.GroupID = id
		} else {
			target.GroupID = 0
		}
	}
}

func isPositiveDecimal(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	_, err := strconv.ParseInt(value, 10, 64)
	return err == nil
}

func (s *Server) resolvePersonalCredential(r *http.Request, userID int64, channel contracts.NotificationChannel) (notify.PersonalTargetCredential, bool, error) {
	ref, _ := notify.PersonalNotificationTargetRef(userID, channel)
	secret, err := s.secrets.Resolve(r.Context(), ref)
	if errors.Is(err, vault.ErrNotFound) {
		return notify.PersonalTargetCredential{}, false, nil
	}
	if err != nil {
		return notify.PersonalTargetCredential{}, false, err
	}
	credential, err := notify.DecodePersonalTargetCredential(secret.Value)
	if err != nil || credential.Channel != channel {
		return notify.PersonalTargetCredential{}, false, errors.New("invalid notification target credential")
	}
	return credential, true, nil
}

func (s *Server) readNotificationTarget(r *http.Request, userID int64, channel contracts.NotificationChannel) (contracts.NotificationTarget, error) {
	ref, _ := notify.PersonalNotificationTargetRef(userID, channel)
	target := contracts.NotificationTarget{UserID: userID, Channel: channel, Scope: contracts.NotificationTargetScopePersonal, TargetRef: ref}
	credential, exists, err := s.resolvePersonalCredential(r, userID, channel)
	if err != nil {
		return contracts.NotificationTarget{}, err
	}
	if !exists {
		return target, nil
	}
	return safeNotificationTarget(userID, credential), nil
}

func safeNotificationTarget(userID int64, credential notify.PersonalTargetCredential) contracts.NotificationTarget {
	ref, _ := notify.PersonalNotificationTargetRef(userID, credential.Channel)
	rawURL := credential.WebhookURL
	if credential.Channel == contracts.NotificationChannelQQ {
		rawURL = credential.OneBotURL
	}
	parsed, _ := url.Parse(rawURL)
	target := contracts.NotificationTarget{
		UserID: userID, Channel: credential.Channel, Scope: contracts.NotificationTargetScopePersonal,
		TargetRef: ref, Configured: true, EndpointHost: parsed.Hostname(),
		SigningSecretConfigured: credential.Channel == contracts.NotificationChannelFeishu && credential.SigningSecret != "",
		AccessTokenConfigured:   credential.Channel == contracts.NotificationChannelQQ && credential.AccessToken != "",
	}
	if credential.Channel == contracts.NotificationChannelQQ {
		target.GroupIDMasked = maskGroupID(strconv.FormatInt(credential.GroupID, 10))
	}
	return target
}

func maskGroupID(value string) string {
	if len(value) <= 4 {
		return "********"
	}
	return "****" + value[len(value)-4:]
}

func (s *Server) auditNotificationTarget(r *http.Request, userID int64, channel contracts.NotificationChannel, action string) {
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: userID, ActorType: "user", ActorID: currentUser(r).Email,
		Action: action, RiskLevel: contracts.RiskLevelL2, TargetType: "notification_target",
		TargetID: fmt.Sprintf("personal:%s", channel), Result: "accepted",
	})
}
