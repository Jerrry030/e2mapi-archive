package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

const (
	maxRedeemBatchSize      = 1000
	redeemFailureWindow     = time.Hour
	redeemFailureLimit      = 20
	redeemIdempotencyPrefix = "car:"
)

// newRedeemCodePlaintext mints a XXXXXXXX-XXXXXXXX-XXXXXXXX-XXXXXXXX code.
// Only its SHA-256 hash is persisted.
func newRedeemCodePlaintext() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	full := strings.ToUpper(hex.EncodeToString(raw))
	return full[0:8] + "-" + full[8:16] + "-" + full[16:24] + "-" + full[24:32], nil
}

func redeemCodeHash(plaintext string) string {
	return contracts.HashRedeemCode(plaintext)
}

func redeemCodePrefix(plaintext string) string {
	normalized := strings.ToUpper(strings.TrimSpace(plaintext))
	if len(normalized) > 8 {
		return normalized[:8]
	}
	return normalized
}

func (s *Server) handleGenerateRedeemCodes(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	var input contracts.GenerateRedeemCodesRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePaymentDecodeError(w, err)
		return
	}
	if !input.Type.Valid() || input.Count < 1 || input.Count > maxRedeemBatchSize {
		writeError(w, http.StatusBadRequest, "validation_failed", "type must be balance or invitation and count must be 1..1000")
		return
	}
	amountMicros := int64(0)
	currency := strings.ToUpper(strings.TrimSpace(input.Currency))
	if input.Type == contracts.RedeemCodeBalance {
		if currency == "" {
			currency = "CNY"
		}
		var ok bool
		if _, amountMicros, ok = normalizeRechargeAmount(input.Amount); !ok {
			writeError(w, http.StatusBadRequest, "validation_failed", "amount must be a positive decimal with at most two places")
			return
		}
	} else {
		currency = "CNY"
		if strings.TrimSpace(input.Amount) != "" {
			writeError(w, http.StatusBadRequest, "validation_failed", "invitation codes carry no amount")
			return
		}
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now().UTC()) {
		writeError(w, http.StatusBadRequest, "validation_failed", "expires_at must be in the future")
		return
	}
	actor := currentUser(r)
	batchID, err := newRechargeTradeNo()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generation_failed", "could not generate a batch id")
		return
	}
	batchID = "batch_" + strings.TrimPrefix(batchID, "recharge_")
	plaintexts := make([]string, 0, input.Count)
	codes := make([]contracts.RedeemCode, 0, input.Count)
	for i := 0; i < input.Count; i++ {
		plaintext, codeErr := newRedeemCodePlaintext()
		if codeErr != nil {
			writeError(w, http.StatusInternalServerError, "generation_failed", "could not generate codes")
			return
		}
		plaintexts = append(plaintexts, plaintext)
		codes = append(codes, contracts.RedeemCode{
			Type: input.Type, CodeHash: redeemCodeHash(plaintext), CodePrefix: redeemCodePrefix(plaintext),
			Currency: currency, AmountMicros: amountMicros, Status: contracts.RedeemCodeUnused,
			BatchID: batchID, Notes: strings.TrimSpace(input.Notes), ExpiresAt: input.ExpiresAt, CreatedBy: actor.ID,
		})
	}
	created, err := s.store.CreateRedeemCodes(r.Context(), codes)
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: actor.ID, ActorType: "user", ActorID: actor.Email, Action: "redeem.codes.generate",
		RiskLevel: contracts.RiskLevelL1, Result: "accepted", TargetType: "redeem_batch", TargetID: batchID,
		Details: map[string]string{"type": string(input.Type), "count": strconv.Itoa(len(created)), "currency": currency},
	})
	writeJSON(w, http.StatusCreated, contracts.GenerateRedeemCodesResponse{BatchID: batchID, Codes: plaintexts, Items: created})
}

func (s *Server) handleListRedeemCodes(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	q := r.URL.Query()
	filter := contracts.RedeemCodeFilter{
		Type:    contracts.RedeemCodeType(strings.ToLower(strings.TrimSpace(q.Get("type")))),
		Status:  contracts.RedeemCodeStatus(strings.ToLower(strings.TrimSpace(q.Get("status")))),
		BatchID: strings.TrimSpace(q.Get("batch_id")),
	}
	if filter.Type != "" && !filter.Type.Valid() {
		writeError(w, http.StatusBadRequest, "validation_failed", "unsupported redeem code type")
		return
	}
	if filter.Status != "" && !filter.Status.Valid() {
		writeError(w, http.StatusBadRequest, "validation_failed", "unsupported redeem code status")
		return
	}
	var ok bool
	if filter.Page, ok = parsePositiveQueryInt(w, q.Get("page"), "page", 1, 1000000); !ok {
		return
	}
	if filter.PageSize, ok = parsePositiveQueryInt(w, q.Get("page_size"), "page_size", 20, 100); !ok {
		return
	}
	page, err := s.store.ListRedeemCodes(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if page.Items == nil {
		page.Items = []contracts.RedeemCode{}
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleDisableRedeemCode(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	code, err := s.store.DisableRedeemCode(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeRedeemStoreError(w, err, "only an unused code can be disabled")
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: actor.ID, ActorType: "user", ActorID: actor.Email, Action: "redeem.code.disable",
		RiskLevel: contracts.RiskLevelL1, Result: "accepted", TargetType: "redeem_code", TargetID: code.ID,
	})
	writeJSON(w, http.StatusOK, code)
}

func (s *Server) handleDeleteRedeemCode(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.store.DeleteUnusedRedeemCode(r.Context(), id); err != nil {
		writeRedeemStoreError(w, err, "a used code cannot be deleted")
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: actor.ID, ActorType: "user", ActorID: actor.Email, Action: "redeem.code.delete",
		RiskLevel: contracts.RiskLevelL1, Result: "accepted", TargetType: "redeem_code", TargetID: id,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleCreateAndRedeem atomically mints one balance code and redeems it to a
// user. It exists for external fulfillment systems: replays with the same
// Idempotency-Key return the original result, a different payload under the
// same key conflicts, and a crash between mint and redeem is recovered on the
// next replay.
func (s *Server) handleCreateAndRedeem(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 100 {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required (max 100 chars)")
		return
	}
	var input contracts.CreateAndRedeemRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePaymentDecodeError(w, err)
		return
	}
	currency := strings.ToUpper(strings.TrimSpace(input.Currency))
	if currency == "" {
		currency = "CNY"
	}
	_, amountMicros, ok := normalizeRechargeAmount(input.Amount)
	if !ok || input.UserID <= 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id and a positive two-decimal amount are required")
		return
	}
	// One in-process consistency boundary keeps two concurrent replays of the
	// same key from minting two codes. Multi-node exactness is provided by the
	// unique redeem journal key underneath.
	s.paymentMu.Lock()
	defer s.paymentMu.Unlock()
	batchID := redeemIdempotencyPrefix + idempotencyKey
	existing, err := s.store.ListRedeemCodes(r.Context(), contracts.RedeemCodeFilter{BatchID: batchID, Page: 1, PageSize: 2})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if len(existing.Items) > 0 {
		code := existing.Items[0]
		if code.AmountMicros != amountMicros || code.Currency != currency {
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used with a different payload")
			return
		}
		switch code.Status {
		case contracts.RedeemCodeUsed:
			if code.UsedBy != input.UserID {
				writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different user")
				return
			}
			wallet, walletErr := s.store.GetWallet(r.Context(), input.UserID, currency)
			if walletErr != nil && !errors.Is(walletErr, store.ErrNotFound) {
				writeError(w, http.StatusInternalServerError, "store_error", walletErr.Error())
				return
			}
			writeJSON(w, http.StatusOK, contracts.CreateAndRedeemResponse{Code: code, Wallet: wallet, Replay: true})
			return
		case contracts.RedeemCodeUnused:
			// Crash between mint and redeem: finish the redeem now.
			redeemed, wallet, redeemErr := s.store.RedeemBalanceCode(r.Context(), code.CodeHash, input.UserID, time.Now().UTC())
			if redeemErr != nil {
				writeRedeemStoreError(w, redeemErr, "code recovery failed")
				return
			}
			writeJSON(w, http.StatusOK, contracts.CreateAndRedeemResponse{Code: redeemed, Wallet: wallet, Replay: true})
			return
		default:
			writeError(w, http.StatusConflict, "idempotency_conflict", "the minted code is no longer redeemable")
			return
		}
	}
	actor := currentUser(r)
	plaintext, err := newRedeemCodePlaintext()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generation_failed", "could not generate a code")
		return
	}
	created, err := s.store.CreateRedeemCodes(r.Context(), []contracts.RedeemCode{{
		Type: contracts.RedeemCodeBalance, CodeHash: redeemCodeHash(plaintext), CodePrefix: redeemCodePrefix(plaintext),
		Currency: currency, AmountMicros: amountMicros, Status: contracts.RedeemCodeUnused,
		BatchID: batchID, Notes: strings.TrimSpace(input.Notes), CreatedBy: actor.ID,
	}})
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	code, wallet, err := s.store.RedeemBalanceCode(r.Context(), created[0].CodeHash, input.UserID, time.Now().UTC())
	if err != nil {
		writeRedeemStoreError(w, err, "redeem failed after mint; replay with the same Idempotency-Key to recover")
		return
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: actor.ID, ActorType: "user", ActorID: actor.Email, Action: "redeem.create_and_redeem",
		RiskLevel: contracts.RiskLevelL2, Result: "accepted", TargetType: "redeem_code", TargetID: code.ID,
		Details: map[string]string{"beneficiary_user_id": strconv.FormatInt(input.UserID, 10), "currency": currency},
	})
	writeJSON(w, http.StatusCreated, contracts.CreateAndRedeemResponse{Code: code, Wallet: wallet})
}

func (s *Server) handleRedeemCode(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	user := currentUser(r)
	if !s.allowRedeemAttempt(user.ID) {
		writeError(w, http.StatusTooManyRequests, "redeem_rate_limited", "too many failed redeem attempts; try again later")
		return
	}
	var input contracts.RedeemRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePaymentDecodeError(w, err)
		return
	}
	plaintext := strings.TrimSpace(input.Code)
	if plaintext == "" || len(plaintext) > 64 {
		writeError(w, http.StatusBadRequest, "validation_failed", "code is required")
		return
	}
	code, wallet, err := s.store.RedeemBalanceCode(r.Context(), redeemCodeHash(plaintext), user.ID, time.Now().UTC())
	if err != nil {
		s.recordRedeemFailure(user.ID)
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
			// One deliberately vague answer: a probing client cannot tell a
			// nonexistent code from a used or disabled one.
			writeError(w, http.StatusBadRequest, "redeem_rejected", "the code is invalid or no longer redeemable")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: user.ID, ActorType: "user", ActorID: user.Email, Action: "redeem.code.use",
		RiskLevel: contracts.RiskLevelL1, Result: "accepted", TargetType: "redeem_code", TargetID: code.ID,
	})
	writeJSON(w, http.StatusOK, contracts.RedeemResponse{
		Type: code.Type, AmountMicros: code.AmountMicros, Currency: code.Currency, Wallet: wallet,
	})
}

// allowRedeemAttempt implements a small in-process failure limiter: after 20
// failed attempts inside an hour further attempts are rejected until the
// window slides. Single-node scope is deliberate — brute force against a
// 128-bit code space is hopeless anyway; this only quiets log noise.
func (s *Server) allowRedeemAttempt(userID int64) bool {
	s.redeemLimiterMu.Lock()
	defer s.redeemLimiterMu.Unlock()
	if s.redeemFailures == nil {
		s.redeemFailures = map[int64][]time.Time{}
	}
	cutoff := time.Now().Add(-redeemFailureWindow)
	recent := s.redeemFailures[userID][:0]
	for _, at := range s.redeemFailures[userID] {
		if at.After(cutoff) {
			recent = append(recent, at)
		}
	}
	s.redeemFailures[userID] = recent
	return len(recent) < redeemFailureLimit
}

func (s *Server) recordRedeemFailure(userID int64) {
	s.redeemLimiterMu.Lock()
	defer s.redeemLimiterMu.Unlock()
	if s.redeemFailures == nil {
		s.redeemFailures = map[int64][]time.Time{}
	}
	s.redeemFailures[userID] = append(s.redeemFailures[userID], time.Now())
}

func writeRedeemStoreError(w http.ResponseWriter, err error, conflictMessage string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "redeem code not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "redeem_conflict", conflictMessage)
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
	}
}
