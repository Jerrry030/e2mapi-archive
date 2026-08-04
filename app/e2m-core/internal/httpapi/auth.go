package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/auth"
	"e2m.local/core/internal/store"
)

// ctxKey namespaces context values set by the auth middleware.
type ctxKey int

const userKey ctxKey = 0

// currentUser returns the authenticated user placed by withAuth.
func currentUser(r *http.Request) contracts.User {
	u, _ := r.Context().Value(userKey).(contracts.User)
	return u
}

// bearerToken extracts credentials only from the Authorization header. Query
// tokens are accepted solely by the SSE handler because EventSource cannot set
// request headers.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

func eventStreamToken(r *http.Request) string {
	if token := bearerToken(r); token != "" {
		return token
	}
	return strings.TrimSpace(r.URL.Query().Get("access_token"))
}

// withAuth authenticates every request via the auth service and stores the
// user in the context. Unauthenticated requests get 401.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if r.URL.Path == "/api/v1/events/stream" {
			token = eventStreamToken(r)
		}
		user, err := s.auth.Authenticate(r.Context(), token)
		if err != nil {
			if errors.Is(err, auth.ErrUnauthorized) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "login required")
				return
			}
			writeError(w, http.StatusInternalServerError, "auth_error", err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), userKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// scopeUser applies RBAC user scoping to a request's user_id filter,
// writing the error response itself when the request is out of scope.
func (s *Server) scopeUser(w http.ResponseWriter, r *http.Request, requested string) (int64, bool) {
	requestedID, ok := parseOptionalUserID(w, requested)
	if !ok {
		return 0, false
	}
	scoped, err := auth.ScopeUser(currentUser(r), requestedID)
	if err != nil {
		s.recordCrossOwnerRejection(r, requestedID)
		writeError(w, http.StatusForbidden, "forbidden", "user out of scope")
		return 0, false
	}
	return scoped, true
}

func (s *Server) scopeOwnerUser(w http.ResponseWriter, r *http.Request, requested string) (int64, bool) {
	requestedID, ok := parseOptionalUserID(w, requested)
	if !ok {
		return 0, false
	}
	scoped, err := auth.ScopeOwnerUser(currentUser(r), requestedID)
	if err != nil {
		s.recordCrossOwnerRejection(r, requestedID)
		writeError(w, http.StatusForbidden, "forbidden", "owner role required for this user")
		return 0, false
	}
	return scoped, true
}

func (s *Server) scopeSupplierUser(w http.ResponseWriter, r *http.Request, requested string) (int64, bool) {
	requestedID, ok := parseOptionalUserID(w, requested)
	if !ok {
		return 0, false
	}
	scoped, err := auth.ScopeSupplierUser(currentUser(r), requestedID)
	if err != nil {
		s.recordCrossOwnerRejection(r, requestedID)
		writeError(w, http.StatusForbidden, "forbidden", "supplier role required for this user")
		return 0, false
	}
	return scoped, true
}

func (s *Server) recordCrossOwnerRejection(r *http.Request, requestedID int64) {
	actor := currentUser(r)
	if requestedID <= 0 || actor.ID <= 0 || actor.ID == requestedID || auth.IsPlatformAdmin(actor) {
		return
	}
	recorder, ok := store.AsOperationalEventRecorder(s.store)
	if ok {
		_ = recorder.RecordCrossOwnerRejected(r.Context())
	}
}

func parseOptionalUserID(w http.ResponseWriter, raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id must be a positive integer")
		return 0, false
	}
	return id, true
}

// requireUserRead 403s unless the user may read neutral user-scoped data.
func (s *Server) requireUserRead(w http.ResponseWriter, r *http.Request, userID int64) bool {
	if !auth.CanReadUser(currentUser(r), userID) {
		s.recordCrossOwnerRejection(r, userID)
		writeError(w, http.StatusForbidden, "forbidden", "user out of scope")
		return false
	}
	return true
}

func (s *Server) requireOwnerRead(w http.ResponseWriter, r *http.Request, userID int64) bool {
	if !auth.CanReadOwnerUser(currentUser(r), userID) {
		s.recordCrossOwnerRejection(r, userID)
		writeError(w, http.StatusForbidden, "forbidden", "owner role required for this user")
		return false
	}
	return true
}

func (s *Server) requireOwnerWrite(w http.ResponseWriter, r *http.Request, userID int64) bool {
	if !auth.CanWriteOwnerUser(currentUser(r), userID) {
		s.recordCrossOwnerRejection(r, userID)
		writeError(w, http.StatusForbidden, "forbidden", "owner role required for this user")
		return false
	}
	return true
}

func (s *Server) requireSupplierRead(w http.ResponseWriter, r *http.Request, userID int64) bool {
	if !auth.CanReadSupplierUser(currentUser(r), userID) {
		s.recordCrossOwnerRejection(r, userID)
		writeError(w, http.StatusForbidden, "forbidden", "supplier role required for this user")
		return false
	}
	return true
}

func (s *Server) requireSupplierWrite(w http.ResponseWriter, r *http.Request, userID int64) bool {
	if !auth.CanWriteSupplierUser(currentUser(r), userID) {
		s.recordCrossOwnerRejection(r, userID)
		writeError(w, http.StatusForbidden, "forbidden", "supplier role required for this user")
		return false
	}
	return true
}

func (s *Server) requireAnyBusinessUserWrite(w http.ResponseWriter, r *http.Request, userID int64) bool {
	user := currentUser(r)
	if !auth.CanWriteOwnerUser(user, userID) && !auth.CanWriteSupplierUser(user, userID) {
		s.recordCrossOwnerRejection(r, userID)
		writeError(w, http.StatusForbidden, "forbidden", "write not permitted for this role/user")
		return false
	}
	return true
}

// requirePlatformAdmin 403s unless the user is a platform admin.
func requirePlatformAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !auth.IsPlatformAdmin(currentUser(r)) {
		writeError(w, http.StatusForbidden, "forbidden", "platform admin required")
		return false
	}
	return true
}

func (s *Server) enabledUserWithRole(w http.ResponseWriter, r *http.Request, userID int64, role contracts.UserRole, label string) (contracts.User, bool) {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", label+" user not found")
		} else {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		}
		return contracts.User{}, false
	}
	if !user.Enabled {
		writeError(w, http.StatusBadRequest, "validation_failed", label+" user is disabled")
		return contracts.User{}, false
	}
	if !auth.HasRole(user, role) {
		writeError(w, http.StatusBadRequest, "validation_failed", label+" user must have the "+string(role)+" role")
		return contracts.User{}, false
	}
	return user, true
}

// instanceForWrite loads an owner-side instance and checks write permission on
// its owner user. Returns ok=false with the response already written on any failure.
func (s *Server) instanceForWrite(w http.ResponseWriter, r *http.Request, instanceID string) (contracts.Instance, bool) {
	inst, err := s.store.GetInstance(r.Context(), instanceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "instance not found")
		} else {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		}
		return contracts.Instance{}, false
	}
	if !s.requireOwnerWrite(w, r, inst.UserID) {
		return contracts.Instance{}, false
	}
	return inst, true
}

// instanceForRead loads an owner-side instance and checks read permission.
func (s *Server) instanceForRead(w http.ResponseWriter, r *http.Request, instanceID string) (contracts.Instance, bool) {
	inst, err := s.store.GetInstance(r.Context(), instanceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "instance not found")
		} else {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		}
		return contracts.Instance{}, false
	}
	if !s.requireOwnerRead(w, r, inst.UserID) {
		return contracts.Instance{}, false
	}
	return inst, true
}

// --- auth endpoints (mounted outside withAuth) ---

func (s *Server) handlePublicAuthConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.auth.PublicConfig())
}

func (s *Server) handleGetAuthSystemSettings(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	settings, err := s.store.GetAuthSystemSettings(r.Context())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		settings = auth.SystemSettingsFromRegistrationConfig(s.auth.RegistrationConfig())
	}
	writeJSON(w, http.StatusOK, sanitizeAuthSystemSettings(settings))
}

func (s *Server) handleUpdateAuthSystemSettings(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	var input contracts.UpdateAuthSystemSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	current, err := s.store.GetAuthSystemSettings(r.Context())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		current = auth.SystemSettingsFromRegistrationConfig(s.auth.RegistrationConfig())
	}

	secret := current.TurnstileSecretKey
	if input.ClearTurnstileSecret {
		secret = ""
	} else if input.TurnstileSecretKey != nil && strings.TrimSpace(*input.TurnstileSecretKey) != "" {
		secret = strings.TrimSpace(*input.TurnstileSecretKey)
	}
	settings := contracts.AuthSystemSettings{
		RegistrationEnabled:              input.RegistrationEnabled,
		RegistrationEmailSuffixWhitelist: auth.NormalizeEmailSuffixes(input.RegistrationEmailSuffixWhitelist),
		InvitationRequired:               input.InvitationRequired,
		TurnstileEnabled:                 input.TurnstileEnabled,
		TurnstileSiteKey:                 strings.TrimSpace(input.TurnstileSiteKey),
		TurnstileSecretKey:               secret,
		TurnstileSecretConfigured:        secret != "",
	}
	saved, err := s.store.UpsertAuthSystemSettings(r.Context(), settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.auth.ConfigureRegistration(auth.RegistrationConfigFromSystemSettings(saved, s.auth.RegistrationConfig()))

	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     actor.ID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "auth.settings.update",
		RiskLevel:  contracts.RiskLevelL1,
		TargetType: "auth_settings",
		TargetID:   "auth.registration",
		Result:     "accepted",
	})

	writeJSON(w, http.StatusOK, sanitizeAuthSystemSettings(saved))
}

func sanitizeAuthSystemSettings(settings contracts.AuthSystemSettings) contracts.AuthSystemSettings {
	settings.RegistrationEmailSuffixWhitelist = append([]string{}, settings.RegistrationEmailSuffixWhitelist...)
	settings.TurnstileSecretConfigured = strings.TrimSpace(settings.TurnstileSecretKey) != ""
	settings.TurnstileSecretKey = ""
	return settings
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	var input contracts.AuthRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	result, err := s.auth.RegisterOwner(r.Context(), input, clientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrRegistrationDisabled):
			writeError(w, http.StatusForbidden, "registration_disabled", "registration is disabled")
		case errors.Is(err, auth.ErrEmailSuffixNotAllowed):
			writeError(w, http.StatusBadRequest, "email_suffix_not_allowed", "email suffix is not allowed")
		case errors.Is(err, auth.ErrInvitationRequired):
			writeError(w, http.StatusBadRequest, "invitation_required", "an invitation code is required")
		case errors.Is(err, auth.ErrInvitationCodeInvalid):
			writeError(w, http.StatusBadRequest, "invitation_code_invalid", "the invitation code is invalid or already used")
		case errors.Is(err, auth.ErrTurnstileRequired):
			writeError(w, http.StatusBadRequest, "turnstile_required", "turnstile token is required")
		case errors.Is(err, auth.ErrTurnstileVerificationFailed):
			writeError(w, http.StatusBadRequest, "turnstile_verification_failed", "turnstile verification failed")
		case errors.Is(err, store.ErrDuplicate):
			writeError(w, http.StatusConflict, "duplicate_email", "璇ラ偖绠卞凡娉ㄥ唽")
		default:
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		}
		return
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     result.User.ID,
		ActorType:  "user",
		ActorID:    result.User.Email,
		Action:     "auth.register",
		RiskLevel:  contracts.RiskLevelL0,
		TargetType: "user",
		TargetID:   strconv.FormatInt(result.User.ID, 10),
		Result:     "accepted",
	})
	writeJSON(w, http.StatusCreated, contracts.AuthRegisterResponse{
		Token:     result.Token,
		User:      result.User,
		ExpiresAt: result.ExpiresAt,
	})
}

func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("CF-Connecting-IP"); forwarded != "" {
		return strings.TrimSpace(forwarded)
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		return strings.TrimSpace(first)
	}
	return auth.RemoteIPFromAddr(r.RemoteAddr)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	token, user, err := s.auth.Login(r.Context(), input.Email, input.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "邮箱或密码错误")
			return
		}
		writeError(w, http.StatusInternalServerError, "auth_error", err.Error())
		return
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     user.ID,
		ActorType:  "user",
		ActorID:    user.Email,
		Action:     "auth.login",
		RiskLevel:  contracts.RiskLevelL0,
		TargetType: "user",
		TargetID:   strconv.FormatInt(user.ID, 10),
		Result:     "accepted",
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"user":       user,
		"expires_at": time.Now().UTC().Add(auth.SessionTTL),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := bearerToken(r); token != "" {
		_ = s.auth.Logout(r.Context(), token)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- user management (platform admin only) ---

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, currentUser(r))
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if users == nil {
		users = []contracts.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	var input struct {
		Email       string   `json:"email"`
		Password    string   `json:"password"`
		DisplayName string   `json:"display_name"`
		Roles       []string `json:"roles"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	roles := make([]contracts.UserRole, 0, len(input.Roles))
	for _, role := range input.Roles {
		roles = append(roles, contracts.UserRole(role))
	}
	user, err := s.auth.CreateUser(r.Context(), input.Email, input.Password, input.DisplayName, roles)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			writeError(w, http.StatusConflict, "duplicate_email", "璇ラ偖绠卞凡娉ㄥ唽")
			return
		}
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     user.ID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "user.create",
		RiskLevel:  contracts.RiskLevelL1,
		TargetType: "user",
		TargetID:   strconv.FormatInt(user.ID, 10),
		Result:     "accepted",
	})
	writeJSON(w, http.StatusCreated, user)
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	userID, ok := parseUserPathID(w, r)
	if !ok {
		return
	}
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	userID, ok := parseUserPathID(w, r)
	if !ok {
		return
	}
	var input contracts.UpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	actor := currentUser(r)
	user, err := s.auth.UpdateUser(r.Context(), actor.ID, userID, input)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "user not found")
		case errors.Is(err, store.ErrDuplicate):
			writeError(w, http.StatusConflict, "duplicate_email", "email is already registered")
		case errors.Is(err, store.ErrLastEnabledAdmin):
			writeError(w, http.StatusConflict, "last_enabled_admin", "the last enabled admin cannot be disabled or demoted")
		case errors.Is(err, auth.ErrSelfAdminLockout):
			writeError(w, http.StatusConflict, "self_admin_lockout", "administrators cannot disable or demote their own account")
		case errors.Is(err, store.ErrUserDeactivationInProgress):
			writeError(w, http.StatusConflict, "user_deactivation_in_progress", "user service deactivation is still draining or awaiting retry")
		case errors.Is(err, store.ErrConflict):
			writeError(w, http.StatusConflict, "stale_user", "user was updated by another administrator; refresh and retry")
		default:
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		}
		return
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     user.ID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "user.update",
		RiskLevel:  contracts.RiskLevelL2,
		TargetType: "user",
		TargetID:   strconv.FormatInt(user.ID, 10),
		Result:     "accepted",
	})
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	userID, ok := parseUserPathID(w, r)
	if !ok {
		return
	}
	var input contracts.ResetUserPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := s.auth.ResetUserPassword(r.Context(), userID, input.Password); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     userID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "user.password_reset",
		RiskLevel:  contracts.RiskLevelL2,
		TargetType: "user",
		TargetID:   strconv.FormatInt(userID, 10),
		Result:     "accepted",
	})
	w.WriteHeader(http.StatusNoContent)
}

func parseUserPathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	userID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user id must be a positive integer")
		return 0, false
	}
	return userID, true
}
