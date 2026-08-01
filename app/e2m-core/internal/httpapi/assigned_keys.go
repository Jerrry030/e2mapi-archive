package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/auth"
	"e2m.local/core/internal/keyproof"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/vault"
)

const (
	assignedKeyRevealTTL           = 60 * time.Second
	assignedKeyRevealAttemptWindow = 15 * time.Minute
	assignedKeyRevealBlockDuration = 15 * time.Minute
	assignedKeyRevealMaxFailures   = 5
)

type upsertDeliveryKeyRequest struct {
	Value string `json:"value"`
}

func (s *Server) handleListUpstreamKeyDeliveries(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	deliveries, err := s.store.ListUpstreamKeyDeliveries(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if deliveries == nil {
		deliveries = []contracts.UpstreamKeyDelivery{}
	}
	writeJSON(w, http.StatusOK, deliveries)
}

// handleUpsertUpstreamDeliveryKey writes a separate Core-vault secret used
// only for owner delivery. credential_binding_id remains Connector-local and
// is never resolved here.
func (s *Server) handleUpsertUpstreamDeliveryKey(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	channelID := strings.TrimSpace(r.PathValue("id"))
	channel, err := s.store.GetUpstreamChannel(r.Context(), channelID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "upstream channel not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if channel.AccountOwnership.Normalize() != contracts.GatewayAccountPlatformManaged {
		writeError(w, http.StatusConflict, "owner_provided_key", "owner-provided accounts cannot have a platform delivery key")
		return
	}
	var input upsertDeliveryKeyRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	value := strings.TrimSpace(input.Value)
	if value == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "value is required")
		return
	}
	_, getErr := s.store.GetUpstreamKeyDelivery(r.Context(), channelID)
	if getErr == nil {
		// This legacy endpoint remains valid only for the first key write. Once a
		// version exists, replacing it must use the two-version rotation workflow
		// so the previous Vault value survives until every target acknowledges it.
		writeError(w, http.StatusConflict, "key_rotation_required", "an existing delivery key must be changed through key rotation")
		return
	}
	if !errors.Is(getErr, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "store_error", getErr.Error())
		return
	}
	ref, err := newDeliverySecretRef(channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "secret_ref_error", "could not create delivery secret reference")
		return
	}
	if _, err := s.secrets.Store(r.Context(), ref, value); err != nil {
		writeError(w, http.StatusInternalServerError, "vault_error", err.Error())
		return
	}
	delivery, err := s.store.UpsertUpstreamKeyDelivery(r.Context(), contracts.UpstreamKeyDelivery{
		ChannelID: channelID, SecretRef: ref, MaskedValue: maskAssignedKey(value),
	})
	if err != nil {
		_ = s.secrets.Delete(r.Context(), ref)
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "owner_provided_key", "owner-provided accounts cannot have a platform delivery key")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.auditUpstream(r, 0, "upstream_key_delivery.upsert", "upstream_key_delivery", delivery.ID)
	writeJSON(w, http.StatusOK, delivery)
}

func (s *Server) handleListAssignedUpstreamKeys(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	user := currentUser(r)
	if !auth.IsOwner(user) || user.ID == 0 {
		writeError(w, http.StatusForbidden, "forbidden", "client role required")
		return
	}
	keys, err := s.store.ListAssignedUpstreamKeys(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if keys == nil {
		keys = []contracts.AssignedUpstreamKey{}
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) handleRevealAssignedUpstreamKey(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	user := currentUser(r)
	deliveryID := strings.TrimSpace(r.PathValue("id"))
	if !auth.IsOwner(user) || user.ID == 0 {
		s.auditAssignedKeyReveal(r, user.ID, deliveryID, "rejected", "client role required")
		writeError(w, http.StatusForbidden, "forbidden", "client role required")
		return
	}
	if !s.allowAssignedKeyReveal {
		s.auditAssignedKeyReveal(r, user.ID, deliveryID, "rejected", "plaintext key reveal disabled")
		writeError(w, http.StatusForbidden, "key_reveal_disabled", "plaintext key export is disabled")
		return
	}
	limitKey := fmt.Sprintf("%d\x00%s", user.ID, clientIP(r))
	if retryAfter := s.revealAttemptRetryAfter(limitKey, time.Now().UTC()); retryAfter > 0 {
		s.auditAssignedKeyReveal(r, user.ID, deliveryID, "rejected", "re-authentication rate limited")
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(retryAfter.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "reauthentication_rate_limited", "too many failed attempts; try again later")
		return
	}
	var input contracts.RevealAssignedUpstreamKeyRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.auditAssignedKeyReveal(r, user.ID, deliveryID, "rejected", "invalid re-authentication request")
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := s.auth.VerifyPassword(r.Context(), user.ID, input.Password); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			s.recordRevealAttemptFailure(limitKey, time.Now().UTC())
			s.auditAssignedKeyReveal(r, user.ID, deliveryID, "rejected", "re-authentication failed")
			writeError(w, http.StatusUnauthorized, "reauthentication_failed", "current password is incorrect")
			return
		}
		s.auditAssignedKeyReveal(r, user.ID, deliveryID, "failed", "authentication service failed")
		writeError(w, http.StatusInternalServerError, "auth_error", err.Error())
		return
	}
	s.clearRevealAttemptFailures(limitKey)
	key, ok, err := s.assignedKeyForUser(r, user.ID, deliveryID)
	if err != nil {
		s.auditAssignedKeyReveal(r, user.ID, deliveryID, "failed", "assigned key lookup failed")
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !ok {
		s.auditAssignedKeyReveal(r, user.ID, deliveryID, "rejected", "assigned key not found")
		writeError(w, http.StatusNotFound, "not_found", "assigned key not found")
		return
	}
	if err := s.verifyAssignedKeyBindings(r, user.ID, key); err != nil {
		s.auditAssignedKeyReveal(r, user.ID, deliveryID, "rejected", "delivery key proof failed")
		if errors.Is(err, keyproof.ErrMismatch) {
			writeError(w, http.StatusConflict, "delivery_key_mismatch", "assigned key does not match the gateway binding")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "delivery_key_unverified", "assigned key has not been verified against the gateway binding")
		return
	}
	if s.secrets == nil {
		s.auditAssignedKeyReveal(r, user.ID, deliveryID, "failed", "credential vault unavailable")
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	secret, err := s.secrets.Resolve(r.Context(), key.SecretRef)
	if err != nil {
		s.auditAssignedKeyReveal(r, user.ID, deliveryID, "failed", "delivery secret unavailable")
		if errors.Is(err, vault.ErrNotFound) {
			writeError(w, http.StatusServiceUnavailable, "delivery_key_unavailable", "assigned key is not available for reveal")
			return
		}
		writeError(w, http.StatusInternalServerError, "vault_error", "assigned key could not be resolved")
		return
	}
	// The accepted audit is part of the authorization boundary: if it cannot be
	// persisted, fail closed and never return plaintext.
	if err := s.auditAssignedKeyReveal(r, user.ID, deliveryID, "accepted", ""); err != nil {
		writeError(w, http.StatusInternalServerError, "audit_error", "could not record key reveal")
		return
	}
	writeJSON(w, http.StatusOK, contracts.RevealAssignedUpstreamKeyResponse{
		Value: secret.Value, ExpiresAt: time.Now().UTC().Add(assignedKeyRevealTTL),
	})
}

func (s *Server) verifyAssignedKeyBindings(r *http.Request, userID int64, key contracts.AssignedUpstreamKey) error {
	if s.keyProof == nil || key.ChannelID == "" {
		return keyproof.ErrUnverified
	}
	plans, err := s.store.ListRoutePlans(r.Context(), userID)
	if err != nil {
		return fmt.Errorf("%w: list route plans", keyproof.ErrUnverified)
	}
	instances := make(map[string]struct{})
	for _, plan := range plans {
		bindings, err := s.store.ListPublishedBindings(r.Context(), plan.ID)
		if err != nil {
			return fmt.Errorf("%w: list bindings", keyproof.ErrUnverified)
		}
		for _, binding := range bindings {
			if binding.ChannelID != key.ChannelID || binding.State == contracts.BindingRevoked {
				continue
			}
			if binding.InstanceID != "" && binding.InstanceID != plan.InstanceID {
				return keyproof.ErrUnverified
			}
			instanceID := strings.TrimSpace(binding.InstanceID)
			if instanceID == "" {
				instanceID = strings.TrimSpace(plan.InstanceID)
			}
			if instanceID != "" {
				instances[instanceID] = struct{}{}
			}
		}
	}
	if len(instances) == 0 {
		return keyproof.ErrUnverified
	}
	for instanceID := range instances {
		verification, err := s.keyProof.Verify(r.Context(), key.ChannelID, instanceID)
		if err != nil {
			return err
		}
		if verification.Proof.ChannelID != key.ChannelID || verification.Proof.InstanceID != instanceID ||
			verification.Proof.KeyVersion != key.KeyVersion || verification.Proof.Status != contracts.DeliveryKeyProofVerified ||
			verification.Proof.ConnectorID == "" || verification.DeploymentRequired {
			return keyproof.ErrUnverified
		}
	}
	return nil
}

func (s *Server) revealAttemptRetryAfter(key string, now time.Time) time.Duration {
	s.revealLimiter.mu.Lock()
	defer s.revealLimiter.mu.Unlock()
	if s.revealLimiter.attempts == nil {
		s.revealLimiter.attempts = make(map[string]assignedKeyRevealAttempt)
	}
	attempt, exists := s.revealLimiter.attempts[key]
	if !exists {
		return 0
	}
	if now.Before(attempt.BlockedTill) {
		return attempt.BlockedTill.Sub(now)
	}
	if now.Sub(attempt.WindowStart) >= assignedKeyRevealAttemptWindow {
		delete(s.revealLimiter.attempts, key)
	}
	return 0
}

func (s *Server) recordRevealAttemptFailure(key string, now time.Time) {
	s.revealLimiter.mu.Lock()
	defer s.revealLimiter.mu.Unlock()
	if s.revealLimiter.attempts == nil {
		s.revealLimiter.attempts = make(map[string]assignedKeyRevealAttempt)
	}
	attempt := s.revealLimiter.attempts[key]
	if attempt.WindowStart.IsZero() || now.Sub(attempt.WindowStart) >= assignedKeyRevealAttemptWindow {
		attempt = assignedKeyRevealAttempt{WindowStart: now}
	}
	attempt.Failures++
	if attempt.Failures >= assignedKeyRevealMaxFailures {
		attempt.BlockedTill = now.Add(assignedKeyRevealBlockDuration)
	}
	s.revealLimiter.attempts[key] = attempt
}

func (s *Server) clearRevealAttemptFailures(key string) {
	s.revealLimiter.mu.Lock()
	defer s.revealLimiter.mu.Unlock()
	delete(s.revealLimiter.attempts, key)
}

func (s *Server) assignedKeyForUser(r *http.Request, userID int64, deliveryID string) (contracts.AssignedUpstreamKey, bool, error) {
	keys, err := s.store.ListAssignedUpstreamKeys(r.Context(), userID)
	if err != nil {
		return contracts.AssignedUpstreamKey{}, false, err
	}
	for _, key := range keys {
		if key.ID == deliveryID {
			return key, true, nil
		}
	}
	return contracts.AssignedUpstreamKey{}, false, nil
}

func (s *Server) auditAssignedKeyReveal(r *http.Request, userID int64, deliveryID, result, message string) error {
	actor := currentUser(r)
	_, err := s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: userID, ActorType: "user", ActorID: actor.Email,
		Action: "upstream_key.reveal", RiskLevel: contracts.RiskLevelL2,
		TargetType: "assigned_upstream_key", TargetID: deliveryID,
		Result: result, ErrorMessage: message,
	})
	return err
}

func newDeliverySecretRef(channelID string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return fmt.Sprintf("credential_ref:platform/delivery/%s/%s", safeSecretName(channelID), hex.EncodeToString(raw)), nil
}

func maskAssignedKey(value string) string {
	runes := []rune(value)
	if len(runes) <= 4 {
		return "********"
	}
	return "********" + string(runes[len(runes)-4:])
}

func setSensitiveResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}
