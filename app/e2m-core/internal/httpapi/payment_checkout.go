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
		spent := int64(0)
		const dailyLimitPageSize = 100
		// A spend ceiling must sum every completed order for the day, and it
		// must fail closed if the count is too pathological to finish summing.
		for pageNumber := 1; ; pageNumber++ {
			if pageNumber > 200 {
				writeError(w, http.StatusConflict, "daily_payment_limit", "daily payment limit exceeded")
				return
			}
			today, listErr := s.store.ListPaymentOrders(r.Context(), contracts.PaymentOrderFilter{UserID: user.ID, Status: contracts.PaymentOrderCompleted, StartCreatedAt: &start, EndCreatedAt: &end, Page: pageNumber, PageSize: dailyLimitPageSize})
			if listErr != nil {
				writeError(w, http.StatusInternalServerError, "store_error", listErr.Error())
				return
			}
			for _, order := range today.Items {
				_, micros, valid := normalizeRechargeAmount(order.PayAmount)
				if valid {
					spent += micros
				}
			}
			if len(today.Items) < dailyLimitPageSize {
				break
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
	secret, err := s.secrets.Resolve(r.Context(), rechargeSecretRef(provider))
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
		MerchantID: strings.TrimSpace(provider.Config["pid"]),
		NotifyURL:  baseURL + "/api/v1/payment/webhooks/easypay/" + provider.ID,
		ChannelID:  easyPayChannelID(provider, input.PaymentType),
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
		if !provider.Enabled {
			continue
		}
		switch provider.ProviderKey {
		case contracts.PaymentProviderStripe:
			if strings.ToUpper(provider.Config["currency"]) != currency {
				continue
			}
			if paymentType != "stripe" && !containsPaymentString(provider.SupportedTypes, paymentType) {
				continue
			}
			return provider, paymentruntime.Stripe{}, nil
		case contracts.PaymentProviderEasyPay:
			// The aggregator protocol carries no currency; it settles CNY only.
			if currency != "CNY" || paymentType != "alipay" && paymentType != "wxpay" {
				continue
			}
			if len(provider.SupportedTypes) > 0 && !containsPaymentString(provider.SupportedTypes, paymentType) {
				continue
			}
			return provider, paymentruntime.EasyPay{}, nil
		}
	}
	return contracts.PaymentProvider{}, nil, errors.New("no enabled provider supports this payment method and currency")
}

// rechargeSecretRef picks the vault reference that authenticates checkout and
// order-query calls for the provider family.
func rechargeSecretRef(provider contracts.PaymentProvider) string {
	if provider.ProviderKey == contracts.PaymentProviderEasyPay {
		return provider.SecretRefs["pkey"]
	}
	return provider.SecretRefs["secretKey"]
}

func easyPayChannelID(provider contracts.PaymentProvider, paymentType string) string {
	switch paymentType {
	case "alipay":
		return strings.TrimSpace(provider.Config["cidAlipay"])
	case "wxpay":
		return strings.TrimSpace(provider.Config["cidWxpay"])
	default:
		return ""
	}
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
	if !s.confirmVerifiedPayment(w, r, provider, verified, hex.EncodeToString(bodyHash[:])) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"received": true})
}

// handleEasyPayWebhook settles an aggregator notify. The gateway signs the
// urlencoded parameters themselves (GET query or POST form body) with the
// merchant key, and it keeps retrying until the response body is exactly
// "success".
func (s *Server) handleEasyPayWebhook(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	providerID := strings.TrimSpace(r.PathValue("providerId"))
	provider, err := s.store.GetPaymentProvider(r.Context(), providerID)
	if err != nil || provider.ProviderKey != contracts.PaymentProviderEasyPay || !provider.Enabled {
		writeError(w, http.StatusNotFound, "not_found", "payment provider not found")
		return
	}
	merchantKey, err := s.secrets.Resolve(r.Context(), provider.SecretRefs["pkey"])
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "payment_provider_unavailable", "webhook credential is unavailable")
		return
	}
	payload := []byte(r.URL.RawQuery)
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(readErr, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "payment webhook body exceeds 1 MiB")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_webhook", "payment webhook body could not be read")
			return
		}
		payload = body
	}
	verified, err := (paymentruntime.EasyPay{}).VerifyNotification(payload, "", merchantKey.Value, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_signature", "payment webhook signature or payload is invalid")
		return
	}
	bodyHash := sha256.Sum256(payload)
	if !s.confirmVerifiedPayment(w, r, provider, verified, hex.EncodeToString(bodyHash[:])) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("success"))
}

// confirmVerifiedPayment settles a signature-verified notification and reports
// whether the caller should acknowledge it. Authenticated orphans are durably
// recorded before the acknowledgement so provider retries stop without turning
// a missing local order into a silent accounting gap.
func (s *Server) confirmVerifiedPayment(w http.ResponseWriter, r *http.Request, provider contracts.PaymentProvider, verified paymentruntime.VerifiedNotification, bodyHashHex string) bool {
	_, _, _, err := s.store.ConfirmRechargePayment(r.Context(), contracts.PaymentNotification{
		ProviderInstanceID: provider.ID, ProviderKey: provider.ProviderKey, EventID: verified.EventID,
		ProviderOrderID: verified.ProviderOrderID, OutTradeNo: verified.OutTradeNo, PaymentTradeNo: verified.PaymentTradeNo,
		PaidAmountMicros: verified.PaidAmountMicros, Currency: verified.Currency, PaidAt: verified.PaidAt,
	}, bodyHashHex)
	if err == nil {
		return true
	}
	if !errors.Is(err, store.ErrNotFound) {
		writeHybridStoreError(w, err)
		return false
	}
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
		return false
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		ActorType: "provider", ActorID: provider.ID, Action: "payment.webhook.reject",
		RiskLevel: contracts.RiskLevelL2, Result: "rejected", ErrorMessage: "unknown_order",
		TargetType: "payment_callback", TargetID: verified.EventID, RequestHash: bodyHashHex,
		Details: map[string]string{"provider_key": string(provider.ProviderKey), "out_trade_no": verified.OutTradeNo},
	})
	return true
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
