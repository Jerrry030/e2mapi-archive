package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/store"
)

const (
	loadBalanceRoundRobin  = "round-robin"
	loadBalanceLeastAmount = "least-amount"
)

var paymentProviderSpecs = map[contracts.PaymentProviderKey]struct {
	configKeys     map[string]bool
	secretKeys     map[string]bool
	supportedTypes map[string]bool
	paymentModes   map[string]bool
}{
	contracts.PaymentProviderEasyPay: {
		configKeys: stringSet("pid", "apiBase", "cidAlipay", "cidWxpay"),
		secretKeys: stringSet("pkey"), supportedTypes: stringSet("alipay", "wxpay"),
		paymentModes: stringSet("", "qrcode", "popup"),
	},
	contracts.PaymentProviderAlipay: {
		configKeys: stringSet("appId"), secretKeys: stringSet("privateKey", "publicKey"),
		supportedTypes: stringSet("alipay"), paymentModes: stringSet("", "redirect"),
	},
	contracts.PaymentProviderWxPay: {
		configKeys:     stringSet("appId", "mchId", "certSerial", "publicKeyId"),
		secretKeys:     stringSet("privateKey", "apiV3Key", "publicKey"),
		supportedTypes: stringSet("wxpay"), paymentModes: stringSet(""),
	},
	contracts.PaymentProviderStripe: {
		configKeys:     stringSet("publishableKey", "currency"),
		secretKeys:     stringSet("secretKey", "webhookSecret"),
		supportedTypes: stringSet("card", "alipay", "wxpay", "link"), paymentModes: stringSet(""),
	},
	contracts.PaymentProviderAirwallex: {
		configKeys:     stringSet("clientId", "apiBase", "countryCode", "currency", "accountId"),
		secretKeys:     stringSet("apiKey", "webhookSecret"),
		supportedTypes: stringSet("airwallex"), paymentModes: stringSet(""),
	},
}

func paymentSecretsConfigured(provider contracts.PaymentProvider) map[string]string {
	out := make(map[string]string, len(provider.SecretRefs))
	for key, ref := range provider.SecretRefs {
		if strings.TrimSpace(ref) != "" {
			out[key] = "configured"
		}
	}
	return out
}

func stringSet(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func defaultPaymentConfig() contracts.PaymentConfig {
	return contracts.PaymentConfig{
		MinAmount: 1, OrderTimeoutMinutes: 30, MaxPendingOrders: 3,
		EnabledPaymentTypes: []string{}, LoadBalanceStrategy: loadBalanceRoundRobin,
	}
}

func decodePaymentJSON(w http.ResponseWriter, r *http.Request, target any) error {
	// Provider certificates and keys can be multiline, but collection settings
	// should never need an unbounded body. MaxBytesReader returns a deterministic
	// error without retaining the complete request in memory.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	return decodeStrictJSON(r, target)
}
func writePaymentDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "payment request body exceeds 1 MiB")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
}
func (s *Server) handleGetPaymentConfig(w http.ResponseWriter, r *http.Request) {
	s.paymentMu.Lock()
	defer s.paymentMu.Unlock()
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	config, err := s.store.GetPaymentConfig(r.Context())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		config = defaultPaymentConfig()
	}
	if config.EnabledPaymentTypes == nil {
		config.EnabledPaymentTypes = []string{}
	}
	writeJSON(w, http.StatusOK, config)
}

func (s *Server) handleUpdatePaymentConfig(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	var input contracts.UpdatePaymentConfigRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePaymentDecodeError(w, err)
		return
	}
	// Serialize collection mutations so cross-provider routing checks share one
	// in-process consistency boundary. Decode before taking the lock so a slow
	// request body cannot block unrelated payment reads and writes.
	s.paymentMu.Lock()
	defer s.paymentMu.Unlock()
	config := contracts.PaymentConfig{
		Enabled: input.Enabled, MinAmount: input.MinAmount, MaxAmount: input.MaxAmount,
		DailyLimit: input.DailyLimit, OrderTimeoutMinutes: input.OrderTimeoutMinutes,
		MaxPendingOrders: input.MaxPendingOrders, EnabledPaymentTypes: input.EnabledPaymentTypes,
		LoadBalanceStrategy: strings.TrimSpace(input.LoadBalanceStrategy),
		ProductNamePrefix:   strings.TrimSpace(input.ProductNamePrefix), ProductNameSuffix: strings.TrimSpace(input.ProductNameSuffix),
		HelpImageURL: strings.TrimSpace(input.HelpImageURL), HelpText: strings.TrimSpace(input.HelpText),
		VisibleMethodAlipaySource:  strings.TrimSpace(input.VisibleMethodAlipaySource),
		VisibleMethodWxPaySource:   strings.TrimSpace(input.VisibleMethodWxPaySource),
		VisibleMethodAlipayEnabled: input.VisibleMethodAlipayEnabled,
		VisibleMethodWxPayEnabled:  input.VisibleMethodWxPayEnabled,
	}
	normalizePaymentConfig(&config)
	if config.Enabled || config.VisibleMethodAlipayEnabled || config.VisibleMethodWxPayEnabled {
		providers, err := s.store.ListPaymentProviders(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		if config.Enabled && !hasEnabledPaymentProvider(providers) {
			writeError(w, http.StatusBadRequest, "validation_failed", "enable at least one configured payment provider first")
			return
		}
		if config.Enabled {
			if message := validateEnabledPaymentTypes(config, providers); message != "" {
				writeError(w, http.StatusBadRequest, "validation_failed", message)
				return
			}
		}
		if message := validateVisiblePaymentSources(config, providers); message != "" {
			writeError(w, http.StatusBadRequest, "validation_failed", message)
			return
		}
	}
	if message := validatePaymentConfig(config); message != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", message)
		return
	}
	saved, err := s.store.UpsertPaymentConfig(r.Context(), config)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.appendPaymentAudit(r, "payment.config.update", "payment_config", "collection")
	writeJSON(w, http.StatusOK, saved)
}

func hasEnabledPaymentProvider(providers []contracts.PaymentProvider) bool {
	for _, provider := range providers {
		if provider.Enabled {
			return true
		}
	}
	return false
}

func normalizePaymentConfig(config *contracts.PaymentConfig) {
	seen := map[string]bool{}
	types := make([]string, 0, len(config.EnabledPaymentTypes))
	for _, value := range config.EnabledPaymentTypes {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && !seen[value] {
			seen[value] = true
			types = append(types, value)
		}
	}
	config.EnabledPaymentTypes = types
	if config.LoadBalanceStrategy == "" {
		config.LoadBalanceStrategy = loadBalanceRoundRobin
	}
}

func validatePaymentConfig(config contracts.PaymentConfig) string {
	if config.MinAmount < 0 || config.MaxAmount < 0 || config.DailyLimit < 0 {
		return "amount limits must be non-negative"
	}
	if config.MaxAmount > 0 && config.MinAmount > config.MaxAmount {
		return "min_amount cannot exceed max_amount"
	}
	if config.OrderTimeoutMinutes < 1 {
		return "order_timeout_minutes must be at least 1"
	}
	if config.MaxPendingOrders < 1 {
		return "max_pending_orders must be at least 1"
	}
	if config.LoadBalanceStrategy != loadBalanceRoundRobin && config.LoadBalanceStrategy != loadBalanceLeastAmount {
		return "load_balance_strategy must be round-robin or least-amount"
	}
	enabledTypes := map[string]bool{}
	for _, value := range config.EnabledPaymentTypes {
		if !stringSet("alipay", "wxpay", "stripe", "airwallex")[value] {
			return "enabled_payment_types contains an unsupported method"
		}
		enabledTypes[value] = true
	}
	if enabledTypes["alipay"] != config.VisibleMethodAlipayEnabled {
		return "enabled_payment_types and visible Alipay setting must agree"
	}
	if enabledTypes["wxpay"] != config.VisibleMethodWxPayEnabled {
		return "enabled_payment_types and visible WeChat Pay setting must agree"
	}
	if !stringSet("", "official_alipay", "easypay_alipay")[config.VisibleMethodAlipaySource] {
		return "invalid Alipay visible method source"
	}
	if !stringSet("", "official_wxpay", "easypay_wxpay")[config.VisibleMethodWxPaySource] {
		return "invalid WeChat Pay visible method source"
	}
	if config.VisibleMethodAlipayEnabled && config.VisibleMethodAlipaySource == "" {
		return "enabled Alipay method requires a source"
	}
	if config.VisibleMethodWxPayEnabled && config.VisibleMethodWxPaySource == "" {
		return "enabled WeChat Pay method requires a source"
	}
	if config.HelpImageURL != "" {
		if err := notify.ValidateWebhookURL(config.HelpImageURL); err != nil {
			return "help_image_url " + err.Error()
		}
	}
	return ""
}

func validateEnabledPaymentTypes(config contracts.PaymentConfig, providers []contracts.PaymentProvider) string {
	if len(config.EnabledPaymentTypes) == 0 {
		return "enabled payment requires at least one payment type"
	}
	for _, paymentType := range config.EnabledPaymentTypes {
		supported := false
		for _, provider := range providers {
			if !provider.Enabled {
				continue
			}
			switch paymentType {
			case "stripe":
				supported = provider.ProviderKey == contracts.PaymentProviderStripe
			case "airwallex":
				supported = provider.ProviderKey == contracts.PaymentProviderAirwallex
			case "alipay", "wxpay":
				for _, method := range provider.SupportedTypes {
					if method == paymentType {
						supported = true
						break
					}
				}
			}
			if supported {
				break
			}
		}
		if !supported {
			return "enabled payment type has no enabled provider: " + paymentType
		}
	}
	return ""
}
func validateVisiblePaymentSources(config contracts.PaymentConfig, providers []contracts.PaymentProvider) string {
	if config.VisibleMethodAlipayEnabled && !hasVisiblePaymentSource(providers, config.VisibleMethodAlipaySource) {
		return "selected Alipay source has no enabled provider"
	}
	if config.VisibleMethodWxPayEnabled && !hasVisiblePaymentSource(providers, config.VisibleMethodWxPaySource) {
		return "selected WeChat Pay source has no enabled provider"
	}
	return ""
}

func hasVisiblePaymentSource(providers []contracts.PaymentProvider, source string) bool {
	wantProvider, wantMethod := contracts.PaymentProviderKey(""), ""
	switch source {
	case "official_alipay":
		wantProvider, wantMethod = contracts.PaymentProviderAlipay, "alipay"
	case "easypay_alipay":
		wantProvider, wantMethod = contracts.PaymentProviderEasyPay, "alipay"
	case "official_wxpay":
		wantProvider, wantMethod = contracts.PaymentProviderWxPay, "wxpay"
	case "easypay_wxpay":
		wantProvider, wantMethod = contracts.PaymentProviderEasyPay, "wxpay"
	default:
		return false
	}
	for _, provider := range providers {
		if !provider.Enabled || provider.ProviderKey != wantProvider {
			continue
		}
		for _, method := range provider.SupportedTypes {
			if method == wantMethod {
				return true
			}
		}
	}
	return false
}

func (s *Server) handleListPaymentProviders(w http.ResponseWriter, r *http.Request) {
	s.paymentMu.Lock()
	defer s.paymentMu.Unlock()
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	providers, err := s.store.ListPaymentProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	out := make([]contracts.PaymentProvider, len(providers))
	for i, provider := range providers {
		out[i] = sanitizePaymentProvider(provider)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreatePaymentProvider(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	var input contracts.CreatePaymentProviderRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePaymentDecodeError(w, err)
		return
	}
	// Serialize provider mutations after decoding so slow request bodies cannot
	// block other payment configuration reads and writes.
	s.paymentMu.Lock()
	defer s.paymentMu.Unlock()
	provider := contracts.PaymentProvider{
		ProviderKey: input.ProviderKey, Name: strings.TrimSpace(input.Name), Config: input.Config,
		SupportedTypes: input.SupportedTypes, Enabled: input.Enabled, PaymentMode: strings.TrimSpace(input.PaymentMode),
		SortOrder: input.SortOrder, Limits: input.Limits, RefundEnabled: input.RefundEnabled,
		AllowUserRefund: input.AllowUserRefund && input.RefundEnabled, SecretRefs: map[string]string{},
	}
	normalizePaymentProvider(&provider)
	if message := validatePaymentProvider(provider, input.Secrets); message != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", message)
		return
	}
	storedRefs, err := s.storePaymentSecrets(r, provider.ProviderKey, input.Secrets)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "vault_error", err.Error())
		return
	}
	provider.SecretRefs = storedRefs
	saved, err := s.store.CreatePaymentProvider(r.Context(), provider)
	if err != nil {
		s.deletePaymentSecretRefs(r, storedRefs)
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.appendPaymentAudit(r, "payment.provider.create", "payment_provider", saved.ID)
	writeJSON(w, http.StatusCreated, sanitizePaymentProvider(saved))
}

func (s *Server) handleUpdatePaymentProvider(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	var input contracts.UpdatePaymentProviderRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePaymentDecodeError(w, err)
		return
	}
	// Keep the read/validate/Vault/write sequence in the same consistency
	// boundary as config updates and other provider mutations.
	s.paymentMu.Lock()
	defer s.paymentMu.Unlock()
	current, err := s.store.GetPaymentProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		writePaymentStoreError(w, err)
		return
	}
	next := copyPaymentProviderForUpdate(current)
	if input.Name != nil {
		next.Name = strings.TrimSpace(*input.Name)
	}
	if input.Config != nil {
		next.Config = *input.Config
	}
	if input.SupportedTypes != nil {
		next.SupportedTypes = *input.SupportedTypes
	}
	if input.Enabled != nil {
		next.Enabled = *input.Enabled
	}
	if input.PaymentMode != nil {
		next.PaymentMode = strings.TrimSpace(*input.PaymentMode)
	}
	if input.SortOrder != nil {
		next.SortOrder = *input.SortOrder
	}
	if input.Limits != nil {
		next.Limits = *input.Limits
	}
	if input.RefundEnabled != nil {
		next.RefundEnabled = *input.RefundEnabled
	}
	if input.AllowUserRefund != nil {
		next.AllowUserRefund = *input.AllowUserRefund
	}
	if !next.RefundEnabled {
		next.AllowUserRefund = false
	}
	normalizePaymentProvider(&next)
	providers, listErr := s.store.ListPaymentProviders(r.Context())
	if listErr != nil {
		writeError(w, http.StatusInternalServerError, "store_error", listErr.Error())
		return
	}
	config, configErr := s.paymentConfigOrDefault(r)
	if configErr != nil {
		writeError(w, http.StatusInternalServerError, "store_error", configErr.Error())
		return
	}
	if message := validateProviderAgainstVisibleRouting(next, providers, current.ID, config); message != "" {
		writeError(w, http.StatusConflict, "payment_source_conflict", message)
		return
	}
	clearSet := map[string]bool{}
	for _, key := range input.ClearSecrets {
		if !paymentProviderSpecs[next.ProviderKey].secretKeys[key] {
			writeError(w, http.StatusBadRequest, "validation_failed", "unsupported clear_secrets field: "+key)
			return
		}
		clearSet[key] = true
		delete(next.SecretRefs, key)
	}
	configured := paymentSecretsConfigured(next)
	for key, value := range input.Secrets {
		if strings.TrimSpace(value) != "" && !clearSet[key] {
			configured[key] = value
		}
	}
	if message := validatePaymentProvider(next, configured); message != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", message)
		return
	}
	secretsToStore := map[string]string{}
	for key, value := range input.Secrets {
		if !clearSet[key] {
			secretsToStore[key] = value
		}
	}
	newRefs, err := s.storePaymentSecrets(r, next.ProviderKey, secretsToStore)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "vault_error", err.Error())
		return
	}
	oldRefs := current.SecretRefs
	for key, ref := range newRefs {
		next.SecretRefs[key] = ref
	}
	saved, err := s.store.UpdatePaymentProvider(r.Context(), next)
	if err != nil {
		s.deletePaymentSecretRefs(r, newRefs)
		writePaymentStoreError(w, err)
		return
	}
	for key, ref := range oldRefs {
		if clearSet[key] || newRefs[key] != "" {
			_ = s.secrets.Delete(r.Context(), ref)
		}
	}
	s.appendPaymentAudit(r, "payment.provider.update", "payment_provider", saved.ID)
	writeJSON(w, http.StatusOK, sanitizePaymentProvider(saved))
}

func (s *Server) handleDeletePaymentProvider(w http.ResponseWriter, r *http.Request) {
	// Serialize collection mutations so cross-provider routing checks and Vault
	// reference replacement share one in-process consistency boundary.
	s.paymentMu.Lock()
	defer s.paymentMu.Unlock()
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	provider, err := s.store.GetPaymentProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		writePaymentStoreError(w, err)
		return
	}
	providers, listErr := s.store.ListPaymentProviders(r.Context())
	if listErr != nil {
		writeError(w, http.StatusInternalServerError, "store_error", listErr.Error())
		return
	}
	remaining := make([]contracts.PaymentProvider, 0, len(providers)-1)
	for _, candidate := range providers {
		if candidate.ID != provider.ID {
			remaining = append(remaining, candidate)
		}
	}
	config, configErr := s.paymentConfigOrDefault(r)
	if configErr != nil {
		writeError(w, http.StatusInternalServerError, "store_error", configErr.Error())
		return
	}
	if config.Enabled && !hasEnabledPaymentProvider(remaining) {
		writeError(w, http.StatusConflict, "payment_source_conflict", "payment is enabled; keep at least one provider enabled")
		return
	}
	if config.Enabled {
		if message := validateEnabledPaymentTypes(config, remaining); message != "" {
			writeError(w, http.StatusConflict, "payment_source_conflict", message)
			return
		}
	}
	if message := validateVisiblePaymentSources(config, remaining); message != "" {
		writeError(w, http.StatusConflict, "payment_source_conflict", message)
		return
	}
	if err := s.store.DeletePaymentProvider(r.Context(), provider.ID); err != nil {
		writePaymentStoreError(w, err)
		return
	}
	s.deletePaymentSecretRefs(r, provider.SecretRefs)
	s.appendPaymentAudit(r, "payment.provider.delete", "payment_provider", provider.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) paymentConfigOrDefault(r *http.Request) (contracts.PaymentConfig, error) {
	config, err := s.store.GetPaymentConfig(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		return defaultPaymentConfig(), nil
	}
	return config, err
}

func validateProviderAgainstVisibleRouting(next contracts.PaymentProvider, providers []contracts.PaymentProvider, currentID string, config contracts.PaymentConfig) string {
	for i := range providers {
		if providers[i].ID == currentID {
			providers[i] = next
		}
	}
	if config.Enabled && !hasEnabledPaymentProvider(providers) {
		return "payment is enabled; keep at least one provider enabled"
	}
	if config.Enabled {
		if message := validateEnabledPaymentTypes(config, providers); message != "" {
			return message
		}
	}
	if message := validateVisiblePaymentSources(config, providers); message != "" {
		return message
	}
	return ""
}

func normalizePaymentProvider(provider *contracts.PaymentProvider) {
	if provider.Config == nil {
		provider.Config = map[string]string{}
	}
	for key, value := range provider.Config {
		provider.Config[key] = strings.TrimSpace(value)
	}
	if currency := provider.Config["currency"]; currency != "" {
		provider.Config["currency"] = strings.ToUpper(currency)
	}
	if provider.ProviderKey == contracts.PaymentProviderAirwallex {
		if countryCode := provider.Config["countryCode"]; countryCode != "" {
			provider.Config["countryCode"] = strings.ToUpper(countryCode)
		}
	}
}
func validatePaymentProvider(provider contracts.PaymentProvider, secrets map[string]string) string {
	spec, ok := paymentProviderSpecs[provider.ProviderKey]
	if !ok {
		return "unsupported provider_key"
	}
	if provider.Name == "" {
		return "name is required"
	}
	for key := range provider.Config {
		if !spec.configKeys[key] {
			return "unsupported config field: " + key
		}
	}
	for key := range secrets {
		if !spec.secretKeys[key] {
			return "unsupported secret field: " + key
		}
	}
	for _, method := range provider.SupportedTypes {
		if !spec.supportedTypes[method] {
			return "unsupported payment type: " + method
		}
	}
	if !spec.paymentModes[provider.PaymentMode] {
		return "unsupported payment_mode"
	}
	limitKeys := spec.supportedTypes
	if provider.ProviderKey == contracts.PaymentProviderStripe {
		// Stripe's payment methods share one provider-level limit, matching the
		// reference implementation's persisted "stripe" limits entry.
		limitKeys = stringSet("stripe")
	}
	for method, limit := range provider.Limits {
		if !limitKeys[method] {
			return "limits contains an unsupported payment type"
		}
		if limit.SingleMin < 0 || limit.SingleMax < 0 || limit.DailyLimit < 0 || limit.SingleMax > 0 && limit.SingleMin > limit.SingleMax {
			return "invalid provider limits"
		}
	}
	if apiBase := strings.TrimSpace(provider.Config["apiBase"]); apiBase != "" {
		if err := notify.ValidateWebhookURL(apiBase); err != nil {
			return "apiBase " + err.Error()
		}
		if provider.ProviderKey == contracts.PaymentProviderAirwallex && !validAirwallexAPIBase(apiBase) {
			return "Airwallex apiBase must be https://api.airwallex.com/api/v1 or https://api-demo.airwallex.com/api/v1"
		}
	}
	if currency := provider.Config["currency"]; currency != "" {
		if len(currency) != 3 || strings.ToUpper(currency) != currency || !asciiLetters(currency) {
			return "currency must be a three-letter uppercase code"
		}
	}
	if provider.ProviderKey == contracts.PaymentProviderAirwallex {
		if countryCode := provider.Config["countryCode"]; countryCode != "" &&
			(len(countryCode) != 2 || !asciiLetters(countryCode)) {
			return "countryCode must be a two-letter uppercase code"
		}
	}
	if provider.SortOrder < 0 {
		return "sort_order must be non-negative"
	}
	if provider.Enabled {
		if len(provider.SupportedTypes) == 0 {
			return "enabled provider requires at least one supported payment type"
		}
		for key := range spec.secretKeys {
			if strings.TrimSpace(secrets[key]) == "" {
				return "enabled provider requires secret: " + key
			}
		}
		for _, key := range requiredPaymentConfigKeys(provider.ProviderKey) {
			if strings.TrimSpace(provider.Config[key]) == "" {
				return "enabled provider requires config: " + key
			}
		}
	}
	return ""
}

func asciiLetters(value string) bool {
	for _, char := range value {
		if char < 'A' || char > 'Z' {
			return false
		}
	}
	return true
}

func validAirwallexAPIBase(raw string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	return trimmed == "https://api.airwallex.com/api/v1" || trimmed == "https://api-demo.airwallex.com/api/v1"
}

func requiredPaymentConfigKeys(key contracts.PaymentProviderKey) []string {
	switch key {
	case contracts.PaymentProviderEasyPay:
		return []string{"pid", "apiBase"}
	case contracts.PaymentProviderAlipay:
		return []string{"appId"}
	case contracts.PaymentProviderWxPay:
		return []string{"appId", "mchId", "certSerial", "publicKeyId"}
	case contracts.PaymentProviderStripe:
		return []string{"publishableKey", "currency"}
	case contracts.PaymentProviderAirwallex:
		return []string{"clientId", "apiBase", "countryCode", "currency"}
	default:
		return nil
	}
}

func sanitizePaymentProvider(provider contracts.PaymentProvider) contracts.PaymentProvider {
	out := copyPaymentProviderForUpdate(provider)
	out.SecretConfigured = map[string]bool{}
	for key, ref := range provider.SecretRefs {
		out.SecretConfigured[key] = strings.TrimSpace(ref) != ""
	}
	out.SecretRefs = nil
	if out.Config == nil {
		out.Config = map[string]string{}
	}
	if out.SupportedTypes == nil {
		out.SupportedTypes = []string{}
	}
	if out.Limits == nil {
		out.Limits = map[string]contracts.PaymentMethodLimit{}
	}
	return out
}

func copyPaymentProviderForUpdate(input contracts.PaymentProvider) contracts.PaymentProvider {
	out := input
	out.Config = map[string]string{}
	for key, value := range input.Config {
		out.Config[key] = value
	}
	out.SecretRefs = map[string]string{}
	for key, value := range input.SecretRefs {
		out.SecretRefs[key] = value
	}
	out.SupportedTypes = append([]string{}, input.SupportedTypes...)
	out.Limits = map[string]contracts.PaymentMethodLimit{}
	for key, value := range input.Limits {
		out.Limits[key] = value
	}
	return out
}

func (s *Server) storePaymentSecrets(r *http.Request, providerKey contracts.PaymentProviderKey, secrets map[string]string) (map[string]string, error) {
	refs := map[string]string{}
	keys := make([]string, 0, len(secrets))
	for key := range secrets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := strings.TrimSpace(secrets[key])
		if value == "" {
			continue
		}
		ref, err := newPaymentSecretRef(providerKey, key)
		if err != nil {
			s.deletePaymentSecretRefs(r, refs)
			return nil, err
		}
		if _, err := s.secrets.Store(r.Context(), ref, secrets[key]); err != nil {
			s.deletePaymentSecretRefs(r, refs)
			return nil, err
		}
		refs[key] = ref
	}
	return refs, nil
}

func newPaymentSecretRef(providerKey contracts.PaymentProviderKey, key string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return fmt.Sprintf("credential_ref:system/payment/%s/%s/%s", providerKey, key, hex.EncodeToString(raw)), nil
}

func (s *Server) deletePaymentSecretRefs(r *http.Request, refs map[string]string) {
	if s.secrets == nil {
		return
	}
	for _, ref := range refs {
		if ref != "" {
			_ = s.secrets.Delete(r.Context(), ref)
		}
	}
}

func (s *Server) appendPaymentAudit(r *http.Request, action, targetType, targetID string) {
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: actor.ID, ActorType: "user", ActorID: actor.Email, Action: action, RiskLevel: contracts.RiskLevelL2,
		TargetType: targetType, TargetID: targetID, Result: "accepted",
	})
}

func writePaymentStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "payment provider not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "store_error", err.Error())
}
