package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/auth"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/vault"
)

var safeSecretSegment = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	userID, ok := s.scopeUser(w, r, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	refs, err := s.secrets.ListRefs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "vault_error", err.Error())
		return
	}
	out := make([]contracts.SecretRef, 0)
	for _, ref := range refs {
		if isReservedPersonalNotificationRef(ref) {
			continue
		}
		refUserID := userID
		if refUserID == 0 {
			refUserID = userIDFromSecretRef(ref)
			if refUserID == 0 {
				continue
			}
		} else if !strings.HasPrefix(ref, secretUserPrefix(refUserID)) {
			continue
		}
		item := secretRefFromRef(ref, refUserID)
		if !canReadSecretKind(currentUser(r), refUserID, item.Kind) {
			continue
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpsertSecret(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	var input contracts.UpsertSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !auth.IsPlatformAdmin(currentUser(r)) && input.UserID == 0 {
		input.UserID = currentUser(r).ID
	}
	var requestedUserID string
	if input.UserID != 0 {
		requestedUserID = strconv.FormatInt(input.UserID, 10)
	}
	userID, ok := s.scopeUser(w, r, requestedUserID)
	if !ok {
		return
	}
	if userID == 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id is required")
		return
	}
	if !s.requireAnyBusinessUserWrite(w, r, userID) {
		return
	}
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "user not found")
		} else {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		}
		return
	}
	if !user.Enabled {
		writeError(w, http.StatusBadRequest, "validation_failed", "user is disabled")
		return
	}
	value := strings.TrimSpace(input.Value)
	if value == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "value is required")
		return
	}
	kind := normalizeSecretKind(input.Kind)
	if kind == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "kind must be notification, upstream, or proxy; gateway credentials stay in the connector")
		return
	}
	if kind == contracts.SecretKindNotification {
		if err := notify.ValidateWebhookURL(value); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", "notification secret must contain a safe HTTPS webhook URL")
			return
		}
	}
	if auth.IsPlatformAdmin(currentUser(r)) {
		requiredRole := contracts.UserRoleSupplier
		label := "supplier"
		if kind == contracts.SecretKindNotification {
			requiredRole = contracts.UserRoleClient
			label = "owner"
		}
		if !auth.HasRole(user, requiredRole) {
			writeError(w, http.StatusBadRequest, "validation_failed", label+" user must have the "+string(requiredRole)+" role")
			return
		}
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "name is required")
		return
	}
	if kind == contracts.SecretKindNotification && isReservedPersonalNotificationName(name) {
		writeError(w, http.StatusBadRequest, "reserved_secret_name", "personal notification targets must use the notification target API")
		return
	}
	if !canWriteSecretKind(currentUser(r), userID, kind) {
		writeError(w, http.StatusForbidden, "forbidden", "this role cannot manage this secret kind")
		return
	}
	ref := buildSecretRef(userID, kind, name)
	if _, err := s.secrets.Store(r.Context(), ref, input.Value); err != nil {
		writeError(w, http.StatusInternalServerError, "vault_error", err.Error())
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     userID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "secret.upsert",
		RiskLevel:  contracts.RiskLevelL2,
		TargetType: "secret",
		TargetID:   ref,
		Result:     "accepted",
	})
	writeJSON(w, http.StatusCreated, contracts.UpsertSecretResponse{
		Secret: secretRefFromRef(ref, userID),
	})
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "ref is required")
		return
	}
	userID, ok := s.scopeUser(w, r, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	if userID == 0 {
		userID = userIDFromSecretRef(ref)
		if userID == 0 {
			writeError(w, http.StatusBadRequest, "validation_failed", "user_id is required for this ref")
			return
		}
	}
	if !s.requireAnyBusinessUserWrite(w, r, userID) {
		return
	}
	if !secretRefAllowedForUser(ref, userID) {
		writeError(w, http.StatusForbidden, "forbidden", "secret ref is outside user scope")
		return
	}
	if isReservedPersonalNotificationRef(ref) {
		writeError(w, http.StatusBadRequest, "reserved_secret_ref", "personal notification targets must use the notification target API")
		return
	}
	if !canWriteSecretKind(currentUser(r), userID, secretRefFromRef(ref, userID).Kind) {
		writeError(w, http.StatusForbidden, "forbidden", "this role cannot manage this secret kind")
		return
	}
	if err := s.secrets.Delete(r.Context(), ref); err != nil && !errors.Is(err, vault.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "vault_error", err.Error())
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     userID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "secret.delete",
		RiskLevel:  contracts.RiskLevelL2,
		TargetType: "secret",
		TargetID:   ref,
		Result:     "accepted",
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func normalizeSecretKind(kind contracts.SecretKind) contracts.SecretKind {
	switch kind {
	case contracts.SecretKindNotification, contracts.SecretKindUpstream, contracts.SecretKindProxy:
		return kind
	default:
		return ""
	}
}

func canReadSecretKind(user contracts.User, userID int64, kind contracts.SecretKind) bool {
	if normalizeSecretKind(kind) == "" {
		return false
	}
	return canWriteSecretKind(user, userID, kind)
}

func canWriteSecretKind(user contracts.User, userID int64, kind contracts.SecretKind) bool {
	if auth.IsPlatformAdmin(user) {
		return true
	}
	if auth.CanWriteOwnerUser(user, userID) && normalizeSecretKind(kind) == contracts.SecretKindNotification {
		return true
	}
	if auth.CanWriteSupplierUser(user, userID) {
		switch normalizeSecretKind(kind) {
		case contracts.SecretKindUpstream, contracts.SecretKindProxy:
			return true
		}
	}
	return false
}

func secretUserPrefix(userID int64) string {
	return fmt.Sprintf("credential_ref:user/%d/", userID)
}

func buildSecretRef(userID int64, kind contracts.SecretKind, name string) string {
	return fmt.Sprintf("%s%s/%s", secretUserPrefix(userID), kind, safeSecretName(name))
}

func safeSecretName(name string) string {
	cleaned := safeSecretSegment.ReplaceAllString(strings.TrimSpace(name), "-")
	cleaned = strings.Trim(cleaned, "-._")
	if cleaned == "" {
		return fmt.Sprintf("secret-%d", time.Now().UTC().Unix())
	}
	return strings.ToLower(cleaned)
}

func isReservedPersonalNotificationName(name string) bool {
	cleaned := safeSecretName(name)
	return cleaned == "personal-feishu" || cleaned == "personal-qq"
}

func isReservedPersonalNotificationRef(ref string) bool {
	return strings.HasSuffix(strings.TrimSpace(ref), "/notification/personal-feishu") ||
		strings.HasSuffix(strings.TrimSpace(ref), "/notification/personal-qq")
}

func secretRefAllowedForUser(ref string, userID int64) bool {
	return strings.HasPrefix(ref, secretUserPrefix(userID))
}

func secretRefUsableByUser(ref string, userID int64) bool {
	return userIDFromSecretRef(strings.TrimSpace(ref)) == userID
}

func userIDFromSecretRef(ref string) int64 {
	const p = "credential_ref:user/"
	if !strings.HasPrefix(ref, p) {
		return 0
	}
	rest := strings.TrimPrefix(ref, p)
	raw, _, ok := strings.Cut(rest, "/")
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func secretRefFromRef(ref string, userID int64) contracts.SecretRef {
	var kind contracts.SecretKind
	name := ref
	prefix := secretUserPrefix(userID)
	if strings.HasPrefix(ref, prefix) {
		rest := strings.TrimPrefix(ref, prefix)
		if k, n, ok := strings.Cut(rest, "/"); ok {
			kind = normalizeSecretKind(contracts.SecretKind(k))
			name = n
		}
	}
	return contracts.SecretRef{
		Ref:    ref,
		UserID: userID,
		Kind:   kind,
		Name:   name,
		Exists: true,
	}
}
