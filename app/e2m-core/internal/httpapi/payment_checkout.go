package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	paymentruntime "e2m.local/core/internal/payment"
	"e2m.local/core/internal/store"
)

func (s *Server) handleCreateRechargeOrder(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	user := currentUser(r)
	if !s.requireOwnerWrite(w, r, user.ID) {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	var input contracts.RechargeOrderRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePaymentDecodeError(w, err)
		return
	}
	amount, amountMicros, ok := normalizeRechargeAmount(input.Amount)
	if !ok {
		writeError(w, http.StatusBadRequest, "validation_failed", "amount must be a positive decimal with at most two places")
		return
	}
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	input.PaymentType = strings.ToLower(strings.TrimSpace(input.PaymentType))
	if input.Currency == "" {
		input.Currency = "CNY"
	}
	if len(input.Currency) != 3 || !asciiLetters(input.Currency) || input.PaymentType == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "currency and payment_type are required")
		return
	}
	returnURL, err := validateCheckoutReturnURL(input.ReturnURL, s.externalCoreURL(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}
	config, err := s.paymentConfigOrDefault(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !config.Enabled || !containsPaymentString(config.EnabledPaymentTypes, input.PaymentType) ||
		config.MinAmount > 0 && float64(amountMicros)/1_000_000 < config.MinAmount ||
		config.MaxAmount > 0 && float64(amountMicros)/1_000_000 > config.MaxAmount {
		writeError(w, http.StatusBadRequest, "payment_unavailable", "payment method or amount is not enabled")
		return
	}
	page, err := s.store.ListPaymentOrders(r.Context(), contracts.PaymentOrderFilter{UserID: user.ID, Status: contracts.PaymentOrderPending, Page: 1, PageSize: 100})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if config.MaxPendingOrders > 0 && page.Total >= int64(config.MaxPendingOrders) {
		writeError(w, http.StatusConflict, "pending_order_limit", "too many pending payment orders")
		return
	}
	if config.DailyLimit > 0 {
		start := time.Now().UTC().Truncate(24 * time.Hour)
		end := start.Add(24 * time.Hour)
		today, listErr := s.store.ListPaymentOrders(r.Context(), contracts.PaymentOrderFilter{UserID: user.ID, Status: contracts.PaymentOrderCompleted, StartCreatedAt: &start, EndCreatedAt: &end, Page: 1, PageSize: 100})
		if listErr != nil {
			writeError(w, http.StatusInternalServerError, "store_error", listErr.Error())
			return
		}
		spent := int64(0)
		for _, order := range today.Items {
			_, micros, valid := normalizeRechargeAmount(order.PayAmount)
			if valid {
				spent += micros
			}
		}
		if spent+amountMicros > int64(math.Round(config.DailyLimit*1_000_000)) {
			writeError(w, http.StatusConflict, "daily_payment_limit", "daily payment limit exceeded")
			return
		}
	}
	provider, adapter, err := s.chooseRechargeProvider(r, input.PaymentType, input.Currency)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "payment_provider_unavailable", err.Error())
		return
	}
	secretRef := provider.SecretRefs["secretKey"]
	secret, err := s.secrets.Resolve(r.Context(), secretRef)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "payment_provider_unavailable", "provider credential is unavailable")
		return
	}
	outTradeNo, err := newRechargeTradeNo()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "order_generation_failed", "could not generate payment order")
		return
	}
	baseURL := s.externalCoreURL(r)
	order, err := s.store.CreatePaymentOrder(r.Context(), contracts.PaymentOrder{
		UserID: user.ID, UserEmail: user.Email, UserName: user.DisplayName, Amount: amount, PayAmount: amount, FeeRate: "0",
		Currency: input.Currency, PaymentType: input.PaymentType, OutTradeNo: outTradeNo, OrderType: contracts.PaymentOrderBalance,
		ProviderInstanceID: provider.ID, ProviderKey: provider.ProviderKey, ProviderName: provider.Name,
		Status: contracts.PaymentOrderPending, ExpiresAt: time.Now().UTC().Add(time.Duration(config.OrderTimeoutMinutes) * time.Minute),
	})
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	checkout, err := adapter.CreateCheckout(r.Context(), paymentruntime.CheckoutRequest{
		Order: order, ReturnURL: returnURL, CancelURL: baseURL + "/payment/cancelled?order=" + url.QueryEscape(order.ID), SecretKey: secret.Value,
		APIBaseURL: strings.TrimSpace(provider.Config["apiBase"]),
	})
	if err != nil {
		_, _ = s.store.CancelPendingPaymentOrder(r.Context(), order.ID, contracts.OperationAudit{ActorType: "system", ActorID: "payment-checkout", Action: "payment.checkout.create", RiskLevel: contracts.RiskLevelL2, Result: "failed", ErrorMessage: "provider_checkout_failed"})
		writeError(w, http.StatusBadGateway, "payment_provider_error", "payment checkout could not be created")
		return
	}
	order, err = s.store.BindPaymentOrderCheckout(r.Context(), order.ID, checkout.ProviderOrderID)
	if err != nil {
		expireErr := adapter.ExpireCheckout(r.Context(), paymentruntime.CheckoutExpiryRequest{
			ProviderOrderID: checkout.ProviderOrderID,
			SecretKey:       secret.Value,
			APIBaseURL:      strings.TrimSpace(provider.Config["apiBase"]),
		})
		result, errorCode := "compensated", "checkout_binding_failed"
		if expireErr != nil {
			result, errorCode = "failed", "checkout_binding_and_expiry_failed"
		}
		_, _ = s.store.CancelPendingPaymentOrder(r.Context(), order.ID, contracts.OperationAudit{
			ActorType: "system", ActorID: "payment-checkout", Action: "payment.checkout.bind",
			RiskLevel: contracts.RiskLevelL2, Result: result, ErrorMessage: errorCode,
			Details: map[string]string{"provider_instance_id": provider.ID, "provider_order_id": checkout.ProviderOrderID},
		})
		writeHybridStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, contracts.RechargeOrderResponse{Order: order, CheckoutURL: checkout.CheckoutURL})
}

func (s *Server) chooseRechargeProvider(r *http.Request, paymentType, currency string) (contracts.PaymentProvider, paymentruntime.Provider, error) {
	providers, err := s.store.ListPaymentProviders(r.Context())
	if err != nil {
		return contracts.PaymentProvider{}, nil, err
	}
	for _, provider := range providers {
		if !provider.Enabled || provider.ProviderKey != contracts.PaymentProviderStripe || strings.ToUpper(provider.Config["currency"]) != currency {
			continue
		}
		if paymentType != "stripe" && !containsPaymentString(provider.SupportedTypes, paymentType) {
			continue
		}
		return provider, paymentruntime.Stripe{}, nil
	}
	return contracts.PaymentProvider{}, nil, errors.New("no enabled provider supports this payment method and currency")
}

func (s *Server) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	providerID := strings.TrimSpace(r.PathValue("providerId"))
	provider, err := s.store.GetPaymentProvider(r.Context(), providerID)
	if err != nil || provider.ProviderKey != contracts.PaymentProviderStripe || !provider.Enabled {
		writeError(w, http.StatusNotFound, "not_found", "payment provider not found")
		return
	}
	webhookSecret, err := s.secrets.Resolve(r.Context(), provider.SecretRefs["webhookSecret"])
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "payment_provider_unavailable", "webhook credential is unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "payment webhook body exceeds 1 MiB")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_webhook", "payment webhook body could not be read")
		return
	}
	verified, err := (paymentruntime.Stripe{}).VerifyNotification(body, r.Header.Get("Stripe-Signature"), webhookSecret.Value, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_signature", "payment webhook signature or payload is invalid")
		return
	}
	bodyHash := sha256.Sum256(body)
	bodyHashHex := hex.EncodeToString(bodyHash[:])
	_, _, _, err = s.store.ConfirmRechargePayment(r.Context(), contracts.PaymentNotification{
		ProviderInstanceID: provider.ID, ProviderKey: provider.ProviderKey, EventID: verified.EventID,
		ProviderOrderID: verified.ProviderOrderID, OutTradeNo: verified.OutTradeNo, PaymentTradeNo: verified.PaymentTradeNo,
		PaidAmountMicros: verified.PaidAmountMicros, Currency: verified.Currency, PaidAt: verified.PaidAt,
	}, bodyHashHex)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Persist the authenticated orphan before acknowledging it. This prevents
			// endless provider retries without turning a missing local order into a
			// silent accounting gap.
			recordErr := s.store.RecordRejectedPaymentCallback(r.Context(), contracts.PaymentCallbackEvent{
				ProviderInstanceID: provider.ID,
				ProviderKey:        provider.ProviderKey,
				EventID:            verified.EventID,
				BodyHash:           bodyHashHex,
				Accepted:           false,
				ErrorCode:          "unknown_order",
			})
			if recordErr != nil {
				writeHybridStoreError(w, recordErr)
				return
			}
			_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
				ActorType: "provider", ActorID: provider.ID, Action: "payment.webhook.reject",
				RiskLevel: contracts.RiskLevelL2, Result: "rejected", ErrorMessage: "unknown_order",
				TargetType: "payment_callback", TargetID: verified.EventID, RequestHash: bodyHashHex,
				Details: map[string]string{"provider_key": string(provider.ProviderKey), "out_trade_no": verified.OutTradeNo},
			})
			writeJSON(w, http.StatusOK, map[string]bool{"received": true})
			return
		}
		writeHybridStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"received": true})
}

func normalizeRechargeAmount(raw string) (string, int64, bool) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || parts[0] == "" || len(parts[0]) > 12 {
		return "", 0, false
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return "", 0, false
	}
	fractionRaw := ""
	if len(parts) == 2 {
		fractionRaw = parts[1]
		if fractionRaw == "" || len(fractionRaw) > 2 {
			return "", 0, false
		}
	}
	fractionRaw += strings.Repeat("0", 2-len(fractionRaw))
	fraction, err := strconv.ParseInt(fractionRaw, 10, 64)
	if err != nil {
		return "", 0, false
	}
	micros := whole*1_000_000 + fraction*10_000
	if micros <= 0 {
		return "", 0, false
	}
	return strconv.FormatInt(whole, 10) + "." + fractionRaw, micros, true
}

func validateCheckoutReturnURL(raw, fallbackBase string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallbackBase + "/payment/success", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("return_url must be an absolute HTTPS URL")
	}
	return parsed.String(), nil
}

func newRechargeTradeNo() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "recharge_" + hex.EncodeToString(raw), nil
}

func containsPaymentString(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}
