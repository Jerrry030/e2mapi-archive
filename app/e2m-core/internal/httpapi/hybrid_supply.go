package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

func (s *Server) handleGetHybridAllocation(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	instance, ok := s.instanceForRead(w, r, strings.TrimSpace(r.PathValue("instanceId")))
	if !ok {
		return
	}
	allocation, err := s.store.GetHybridAllocation(r.Context(), instance.ID)
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, allocation)
}

func (s *Server) handleUpsertHybridAllocation(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	instance, ok := s.instanceForWrite(w, r, strings.TrimSpace(r.PathValue("instanceId")))
	if !ok {
		return
	}
	var input contracts.HybridAllocationRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePaymentDecodeError(w, err)
		return
	}
	allocation, err := s.store.UpsertHybridAllocation(r.Context(), contracts.HybridAllocation{
		UserID: instance.UserID, InstanceID: instance.ID, Basis: input.Basis, DefaultRule: input.DefaultRule,
		ModelOverrides: input.ModelOverrides, DailyBudgetMicros: input.DailyBudgetMicros, MaxUnitPriceMicros: input.MaxUnitPriceMicros,
	}, input.ExpectedVersion)
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, allocation)
}

func (s *Server) handleListHybridGatewayBindings(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	instance, ok := s.instanceForRead(w, r, strings.TrimSpace(r.PathValue("instanceId")))
	if !ok {
		return
	}
	bindings, err := s.store.ListHybridGatewayBindings(r.Context(), instance.UserID, instance.ID)
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	if bindings == nil {
		bindings = []contracts.HybridGatewayBinding{}
	}
	writeJSON(w, http.StatusOK, bindings)
}

func (s *Server) handleUpsertHybridGatewayBinding(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	instance, ok := s.instanceForWrite(w, r, strings.TrimSpace(r.PathValue("instanceId")))
	if !ok {
		return
	}
	if instance.Kind != contracts.InstanceKindNewAPI || strings.TrimSpace(instance.ConnectorID) == "" {
		writeError(w, http.StatusConflict, "hybrid_binding_unavailable", "hybrid binding requires a Connector-managed NewAPI instance")
		return
	}
	class := contracts.ResourceClass(strings.TrimSpace(r.PathValue("resourceClass")))
	if !class.IsPlatformSupply() {
		writeError(w, http.StatusBadRequest, "validation_failed", "resource_class must be economy or stable")
		return
	}
	var input contracts.HybridGatewayBindingRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePaymentDecodeError(w, err)
		return
	}
	key, err := s.store.GetVirtualKey(r.Context(), instance.UserID, strings.TrimSpace(input.VirtualKeyID))
	if err != nil || key.InstanceID != instance.ID || key.ResourceClass != class || !key.Enabled ||
		key.ExpiresAt != nil && !time.Now().UTC().Before(*key.ExpiresAt) {
		writeError(w, http.StatusConflict, "hybrid_virtual_key_unavailable", "an enabled matching virtual key is required")
		return
	}
	bindingID := hybridCredentialBindingID(instance.ID, class)
	binding := contracts.HybridGatewayBinding{
		UserID: instance.UserID, InstanceID: instance.ID, ResourceClass: class, ConnectorID: instance.ConnectorID,
		CredentialBindingID: bindingID, VirtualKeyID: key.ID, VirtualKeyVersion: key.KeyVersion,
		Status: contracts.HybridGatewayBindingPending,
	}
	if current, getErr := s.store.GetHybridGatewayBinding(r.Context(), instance.UserID, instance.ID, class); getErr == nil {
		binding.ID, binding.RemoteAccountID = current.ID, current.RemoteAccountID
	}
	saved, err := s.store.UpsertHybridGatewayBinding(r.Context(), binding, input.ExpectedVersion)
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	if s.hybridBindings == nil {
		writeJSON(w, http.StatusAccepted, saved)
		return
	}
	ready, err := s.hybridBindings.Apply(r.Context(), instance.UserID, instance.ID, class)
	if err != nil {
		writeError(w, http.StatusBadGateway, "hybrid_binding_provision_failed", "Connector binding or aggregate account provisioning failed")
		return
	}
	writeJSON(w, http.StatusOK, ready)
}

func (s *Server) handleCreateHybridRoutingExecution(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	instance, ok := s.instanceForWrite(w, r, strings.TrimSpace(r.PathValue("instanceId")))
	if !ok {
		return
	}
	if instance.Kind != contracts.InstanceKindNewAPI || strings.TrimSpace(instance.ConnectorID) == "" {
		writeError(w, http.StatusConflict, "hybrid_execution_unavailable", "hybrid execution requires a Connector-managed NewAPI instance")
		return
	}
	var input contracts.HybridRoutingExecutionRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePaymentDecodeError(w, err)
		return
	}
	allocation, err := s.store.GetHybridAllocation(r.Context(), instance.ID)
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	if input.ExpectedAllocationVersion <= 0 || allocation.Version != input.ExpectedAllocationVersion || !contracts.ValidHybridRoutingModel(strings.TrimSpace(input.Model)) {
		writeError(w, http.StatusConflict, "hybrid_allocation_stale", "allocation version or model scope is invalid")
		return
	}
	execution, err := s.store.CreateHybridRoutingExecution(r.Context(), contracts.HybridRoutingExecution{
		UserID: instance.UserID, InstanceID: instance.ID, AllocationVersion: allocation.Version, Model: strings.TrimSpace(input.Model),
	})
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, execution)
}

func (s *Server) handleListHybridRoutingExecutions(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	instance, ok := s.instanceForRead(w, r, strings.TrimSpace(r.URL.Query().Get("instance_id")))
	if !ok {
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "validation_failed", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	executions, err := s.store.ListHybridRoutingExecutions(r.Context(), instance.UserID, instance.ID, limit)
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	if executions == nil {
		executions = []contracts.HybridRoutingExecution{}
	}
	writeJSON(w, http.StatusOK, executions)
}

func (s *Server) handleGetHybridRoutingExecution(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	user := currentUser(r)
	if !s.requireOwnerRead(w, r, user.ID) {
		return
	}
	execution, err := s.store.GetHybridRoutingExecution(r.Context(), user.ID, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	if _, ok := s.instanceForRead(w, r, execution.InstanceID); !ok {
		return
	}
	writeJSON(w, http.StatusOK, execution)
}

func hybridCredentialBindingID(instanceID string, class contracts.ResourceClass) string {
	value := strings.TrimSpace(instanceID) + "-e2m-" + string(class)
	if contracts.IsConnectorQualityProbeField(value) {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return "e2m-hybrid-" + hex.EncodeToString(sum[:12])
}

func (s *Server) handleGetHybridWallet(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	user := currentUser(r)
	if !s.requireOwnerRead(w, r, user.ID) {
		return
	}
	currency := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("currency")))
	if currency == "" {
		currency = "CNY"
	}
	wallet, err := s.store.GetWallet(r.Context(), user.ID, currency)
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wallet)
}

func (s *Server) handleListHybridWalletJournals(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	user := currentUser(r)
	if !s.requireOwnerRead(w, r, user.ID) {
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "validation_failed", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	journals, err := s.store.ListWalletJournals(r.Context(), user.ID, limit)
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	if journals == nil {
		journals = []contracts.WalletJournal{}
	}
	writeJSON(w, http.StatusOK, journals)
}

func (s *Server) handleListHybridVirtualKeys(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	user := currentUser(r)
	if !s.requireOwnerRead(w, r, user.ID) {
		return
	}
	keys, err := s.store.ListVirtualKeys(r.Context(), user.ID)
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	if keys == nil {
		keys = []contracts.VirtualKey{}
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) handleCreateHybridVirtualKey(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	var input contracts.CreateVirtualKeyRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePaymentDecodeError(w, err)
		return
	}
	instance, ok := s.instanceForWrite(w, r, strings.TrimSpace(input.InstanceID))
	if !ok {
		return
	}
	if !input.ResourceClass.IsPlatformSupply() {
		writeError(w, http.StatusBadRequest, "validation_failed", "resource_class must be economy or stable")
		return
	}
	plaintext, err := newVirtualKeyToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "key_generation_failed", "could not generate virtual key")
		return
	}
	secretRef := "credential_ref:system/hybrid-supply/virtual-key/" + hex.EncodeToString([]byte(plaintext[len(plaintext)-16:]))
	if _, err := s.secrets.Store(r.Context(), secretRef, plaintext); err != nil {
		writeError(w, http.StatusInternalServerError, "vault_error", "virtual key could not be stored")
		return
	}
	key, err := s.store.CreateVirtualKey(r.Context(), contracts.VirtualKey{
		UserID: instance.UserID, InstanceID: instance.ID, Name: input.Name, ResourceClass: input.ResourceClass,
		Prefix: plaintext[:12], TokenHash: contracts.HashVirtualKey(plaintext), SecretRef: secretRef,
		Models: input.Models, DailyLimitMicros: input.DailyLimitMicros, ExpiresAt: input.ExpiresAt,
	})
	if err != nil {
		_ = s.secrets.Delete(r.Context(), secretRef)
		writeHybridStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, contracts.CreateVirtualKeyResponse{Key: key, Plaintext: plaintext})
}

func (s *Server) handleUpdateHybridVirtualKey(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	user := currentUser(r)
	if !s.requireOwnerWrite(w, r, user.ID) {
		return
	}
	current, err := s.store.GetVirtualKey(r.Context(), user.ID, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	var input contracts.UpdateVirtualKeyRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePaymentDecodeError(w, err)
		return
	}
	if input.Name != nil {
		current.Name = *input.Name
	}
	if input.Models != nil {
		current.Models = *input.Models
	}
	if input.DailyLimitMicros != nil {
		current.DailyLimitMicros = *input.DailyLimitMicros
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	if input.ExpiresAt != nil {
		current.ExpiresAt = *input.ExpiresAt
	}
	saved, err := s.store.UpdateVirtualKey(r.Context(), current)
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteHybridVirtualKey(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	user := currentUser(r)
	if !s.requireOwnerWrite(w, r, user.ID) {
		return
	}
	key, err := s.store.GetVirtualKey(r.Context(), user.ID, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	if err := s.store.DeleteVirtualKey(r.Context(), user.ID, key.ID); err != nil {
		writeHybridStoreError(w, err)
		return
	}
	if s.secrets != nil {
		_ = s.secrets.Delete(r.Context(), key.SecretRef)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleUpsertSupplyChannelEndpoint(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	var input struct {
		BaseURL                        string `json:"base_url"`
		Secret                         string `json:"secret"`
		MaskedValue                    string `json:"masked_value"`
		Currency                       string `json:"currency"`
		InputPriceMicrosPerMillion     int64  `json:"input_price_micros_per_million"`
		OutputPriceMicrosPerMillion    int64  `json:"output_price_micros_per_million"`
		InputSupplierMicrosPerMillion  int64  `json:"input_supplier_micros_per_million"`
		OutputSupplierMicrosPerMillion int64  `json:"output_supplier_micros_per_million"`
		MaxRequestMicros               int64  `json:"max_request_micros"`
		MaxConcurrency                 int    `json:"max_concurrency"`
		CapacityPercent                int    `json:"capacity_percent"`
		Enabled                        bool   `json:"enabled"`
	}
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePaymentDecodeError(w, err)
		return
	}
	channelID := strings.TrimSpace(r.PathValue("id"))
	secretRef := ""
	current, currentErr := s.store.GetSupplyChannelEndpoint(r.Context(), channelID)
	if currentErr == nil {
		secretRef = current.SecretRef
	}
	if strings.TrimSpace(input.Secret) != "" {
		secretRef = "credential_ref:system/hybrid-supply/channel/" + channelID
		if _, err := s.secrets.Store(r.Context(), secretRef, input.Secret); err != nil {
			writeError(w, http.StatusInternalServerError, "vault_error", "upstream key could not be stored")
			return
		}
	}
	endpoint, err := s.store.UpsertSupplyChannelEndpoint(r.Context(), contracts.SupplyChannelEndpoint{
		ChannelID: channelID, BaseURL: input.BaseURL, SecretRef: secretRef, MaskedValue: input.MaskedValue,
		Currency: strings.ToUpper(strings.TrimSpace(input.Currency)), InputPriceMicrosPerMillion: input.InputPriceMicrosPerMillion,
		OutputPriceMicrosPerMillion: input.OutputPriceMicrosPerMillion, InputSupplierMicrosPerMillion: input.InputSupplierMicrosPerMillion,
		OutputSupplierMicrosPerMillion: input.OutputSupplierMicrosPerMillion, MaxRequestMicros: input.MaxRequestMicros,
		MaxConcurrency: input.MaxConcurrency, CapacityPercent: input.CapacityPercent, Enabled: input.Enabled,
	})
	if err != nil {
		writeHybridStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, endpoint)
}

func newVirtualKeyToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "e2m_v1_" + hex.EncodeToString(raw), nil
}

func writeHybridStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "hybrid supply resource not found")
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrDuplicate):
		writeError(w, http.StatusConflict, "hybrid_supply_conflict", err.Error())
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
	}
}
