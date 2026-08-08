package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/auth"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/vault"
)

const defaultPlatformMaxRequestMicros int64 = 1_000_000

// The per-request hold ceiling scales with what the upstream actually
// charges: a cap that fits a budget model is several requests' worth of an
// expensive one, and a fixed cap forced expensive-model settlements into
// debt as a matter of course. The reference size is a deliberately generous
// single request (200k prompt + 100k completion tokens) priced at the
// endpoint's own sell price, floored at the old fixed default so cheap
// models keep their current behaviour and capped so one request can never
// lock an unbounded slice of a wallet.
const (
	derivedHoldInputTokens  int64 = 200_000
	derivedHoldOutputTokens int64 = 100_000
	maxDerivedHoldMicros    int64 = 100_000_000
)

func derivedMaxRequestMicros(endpoint contracts.SupplyChannelEndpoint) int64 {
	cost := endpoint.InputPriceMicrosPerMillion*derivedHoldInputTokens/1_000_000 +
		endpoint.OutputPriceMicrosPerMillion*derivedHoldOutputTokens/1_000_000
	if cost < defaultPlatformMaxRequestMicros {
		return defaultPlatformMaxRequestMicros
	}
	if cost > maxDerivedHoldMicros {
		return maxDerivedHoldMicros
	}
	return cost
}

const platformUpstreamTestTimeout = 10 * time.Second

// registerPlatformDistributionRoutes exposes E2M's native distribution
// management surface. These routes manage the same store and vault used by the
// in-process OpenAI-compatible gateway; no external Sub2API service is involved.
func (s *Server) registerPlatformDistributionRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/platform/groups", s.handleListPlatformGroups)
	api.HandleFunc("POST /api/v1/platform/groups", s.handleCreatePlatformGroup)
	api.HandleFunc("GET /api/v1/platform/groups/{id}", s.handleGetPlatformGroup)
	api.HandleFunc("PUT /api/v1/platform/groups/{id}", s.handleUpdatePlatformGroup)
	api.HandleFunc("DELETE /api/v1/platform/groups/{id}", s.handleDeletePlatformGroup)

	api.HandleFunc("GET /api/v1/platform/upstreams", s.handleListPlatformUpstreams)
	api.HandleFunc("POST /api/v1/platform/upstreams", s.handleCreatePlatformUpstream)
	api.HandleFunc("GET /api/v1/platform/upstreams/{id}", s.handleGetPlatformUpstream)
	api.HandleFunc("PUT /api/v1/platform/upstreams/{id}", s.handleUpdatePlatformUpstream)
	api.HandleFunc("DELETE /api/v1/platform/upstreams/{id}", s.handleDeletePlatformUpstream)
	api.HandleFunc("POST /api/v1/platform/upstreams/{id}/test", s.handleTestPlatformUpstream)
	api.HandleFunc("GET /api/v1/platform/upstreams/{id}/stats", s.handleGetPlatformUpstreamStats)

	// /keys is the concise public name. /api-keys is retained as an explicit
	// alias for clients whose resource naming distinguishes login and API keys.
	for _, prefix := range []string{"/api/v1/platform/keys", "/api/v1/platform/api-keys"} {
		api.HandleFunc("GET "+prefix, s.handleListPlatformKeys)
		api.HandleFunc("POST "+prefix, s.handleCreatePlatformKey)
		api.HandleFunc("GET "+prefix+"/{id}", s.handleGetPlatformKey)
		api.HandleFunc("GET "+prefix+"/{id}/value", s.handleGetPlatformKeyValue)
		api.HandleFunc("PUT "+prefix+"/{id}", s.handleUpdatePlatformKey)
		api.HandleFunc("DELETE "+prefix+"/{id}", s.handleDeletePlatformKey)
	}

	api.HandleFunc("GET /api/v1/platform/wallet", s.handleGetPlatformWallet)
	api.HandleFunc("GET /api/v1/platform/wallet/journals", s.handleListPlatformWalletJournals)
	api.HandleFunc("POST /api/v1/platform/wallet-adjustments", s.handleAdjustPlatformWallet)
	api.HandleFunc("GET /api/v1/platform/usage", s.handleListPlatformUsage)
	api.HandleFunc("GET /api/v1/platform/pricing/preview", s.handleGetPlatformPricingPreview)
	api.HandleFunc("GET /api/v1/platform/model-market", s.handleGetPlatformModelMarket)
}

type platformGroupRequest struct {
	Name          string                       `json:"name"`
	Description   *string                      `json:"description,omitempty"`
	Provider      *string                      `json:"provider,omitempty"`
	Models        *[]string                    `json:"models,omitempty"`
	Region        *string                      `json:"region,omitempty"`
	Status        contracts.UpstreamPoolStatus `json:"status,omitempty"`
	ResourceClass contracts.ResourceClass      `json:"resource_class,omitempty"`
	Labels        *map[string]string           `json:"labels,omitempty"`
	// RateMultiplier scales base-table prices for every upstream in the group
	// (decimal, e.g. "1.25"). Stored as basis points on the pool labels.
	RateMultiplier *string `json:"rate_multiplier,omitempty"`
}

// platformGroupCatalogItem is the downstream-facing product catalog. Keep
// provider identity, region, delivery mode, safety stock, and operational
// labels out of this view: clients only need enough information to choose a
// product group and scope a virtual key.
type platformGroupCatalogItem struct {
	ID            string                       `json:"id"`
	Name          string                       `json:"name"`
	Description   string                       `json:"description,omitempty"`
	Models        []string                     `json:"models"`
	Status        contracts.UpstreamPoolStatus `json:"status"`
	ResourceClass contracts.ResourceClass      `json:"resource_class"`
}

func platformGroupCatalogView(group contracts.UpstreamPool) platformGroupCatalogItem {
	return platformGroupCatalogItem{
		ID: group.ID, Name: group.Name, Description: group.Description,
		Models: append([]string(nil), group.Models...), Status: group.Status,
		ResourceClass: group.ResourceClass,
	}
}

func (s *Server) handleListPlatformGroups(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformCustomer(w, r) {
		return
	}
	pools, err := s.store.ListUpstreamPools(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	groups := make([]contracts.UpstreamPool, 0, len(pools))
	for _, pool := range pools {
		if pool.DeliveryMode.Normalize() == contracts.UpstreamDeliverySupplyGateway {
			groups = append(groups, pool)
		}
	}
	if !auth.IsPlatformAdmin(currentUser(r)) {
		catalog := make([]platformGroupCatalogItem, 0, len(groups))
		for _, group := range groups {
			catalog = append(catalog, platformGroupCatalogView(group))
		}
		writeJSON(w, http.StatusOK, catalog)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *Server) handleGetPlatformGroup(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformCustomer(w, r) {
		return
	}
	group, err := s.platformGroup(r, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	if auth.IsPlatformAdmin(currentUser(r)) {
		writeJSON(w, http.StatusOK, group)
		return
	}
	writeJSON(w, http.StatusOK, platformGroupCatalogView(group))
}

func (s *Server) handleCreatePlatformGroup(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	var input platformGroupRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePlatformDecodeError(w, err)
		return
	}
	group := contracts.UpstreamPool{
		Name: strings.TrimSpace(input.Name), ResourceClass: contracts.NormalizePlatformResourceClass(input.ResourceClass),
		DeliveryMode: contracts.UpstreamDeliverySupplyGateway, Status: input.Status,
	}
	if msg := applyPlatformGroupRequest(&group, input); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	if group.Status == "" {
		group.Status = contracts.UpstreamPoolActive
	}
	if msg := validatePlatformGroup(group, true); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key != "" {
		group.ID = deterministicPlatformID("pgrp", strconv.FormatInt(currentUser(r).ID, 10)+":"+key)
		if existing, err := s.platformGroup(r, group.ID); err == nil {
			writeJSON(w, http.StatusOK, existing)
			return
		} else if !errors.Is(err, store.ErrNotFound) {
			writePlatformStoreError(w, err)
			return
		}
	}
	saved, err := s.store.CreateUpstreamPool(r.Context(), group)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	s.auditUpstream(r, 0, "platform_group.create", "upstream_pool", saved.ID)
	writeJSON(w, http.StatusCreated, saved)
}

func (s *Server) handleUpdatePlatformGroup(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	group, err := s.platformGroup(r, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	var input platformGroupRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePlatformDecodeError(w, err)
		return
	}
	if strings.TrimSpace(input.Name) != "" {
		group.Name = strings.TrimSpace(input.Name)
	}
	if input.ResourceClass != "" {
		if input.ResourceClass != group.ResourceClass {
			writeError(w, http.StatusConflict, "immutable_field", "resource_class is immutable")
			return
		}
	}
	if input.Status != "" {
		group.Status = input.Status
	}
	if msg := applyPlatformGroupRequest(&group, input); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	group.DeliveryMode = contracts.UpstreamDeliverySupplyGateway
	if msg := validatePlatformGroup(group, false); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	saved, err := s.store.UpdateUpstreamPool(r.Context(), group)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	s.auditUpstream(r, 0, "platform_group.update", "upstream_pool", saved.ID)
	writeJSON(w, http.StatusOK, saved)
}

// handleDeletePlatformGroup deliberately starts the existing retirement
// workflow instead of removing a group row. A group can already have issued
// virtual keys, reservations, usage, and published routes; a hard delete would
// violate those records' foreign keys and could strand active traffic.
func (s *Server) handleDeletePlatformGroup(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	group, err := s.platformGroup(r, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	lifecycle, ok := s.lifecycleStore(w)
	if !ok {
		return
	}
	job, err := lifecycle.CreatePoolRetirementJob(r.Context(), group.ID, currentUser(r).ID)
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	// No route plan means there is nothing external to drain. Complete the
	// retirement synchronously so a newly-created, unused group does not remain
	// stuck in maintenance waiting for a worker. Groups with published plans keep
	// the durable job pending; the ordinary retirement runner owns their drain.
	if job.TotalPlans == 0 {
		job, err = lifecycle.FinalizePoolRetirementJob(r.Context(), job.ID)
		if err != nil {
			writeLifecycleError(w, err)
			return
		}
	}
	s.auditUpstream(r, 0, "platform_group.retirement_create", "upstream_pool", group.ID)
	writeJSON(w, http.StatusAccepted, job)
}

func applyPlatformGroupRequest(group *contracts.UpstreamPool, input platformGroupRequest) string {
	if input.Description != nil {
		group.Description = strings.TrimSpace(*input.Description)
	}
	if input.Provider != nil {
		group.Provider = strings.TrimSpace(*input.Provider)
	}
	if input.Models != nil {
		group.Models = normalizedModels(*input.Models)
	}
	if input.Region != nil {
		group.Region = strings.TrimSpace(*input.Region)
	}
	if input.Labels != nil {
		group.Labels = *input.Labels
	}
	if input.RateMultiplier != nil {
		bps, ok := parseRateMultiplier(*input.RateMultiplier)
		if !ok {
			return "rate_multiplier must be a decimal between 0.0001 and 100 with at most four decimal places"
		}
		if group.Labels == nil {
			group.Labels = map[string]string{}
		}
		group.Labels[platformRateMultiplierLabel] = strconv.FormatInt(bps, 10)
	}
	return ""
}

func validatePlatformGroup(group contracts.UpstreamPool, creating bool) string {
	if strings.TrimSpace(group.Name) == "" {
		return "name is required"
	}
	if !group.ResourceClass.IsPlatformSupply() {
		return "resource_class must be economy or stable"
	}
	if !group.DeliveryMode.Valid() || group.DeliveryMode.Normalize() != contracts.UpstreamDeliverySupplyGateway {
		return "delivery_mode must be supply_gateway"
	}
	if msg := validPoolStatus(group.Status); msg != "" {
		return msg
	}
	if creating && group.Status == contracts.UpstreamPoolRetired {
		return "a new group cannot start retired"
	}
	if !validPlatformModels(group.Models) {
		return "models contain an invalid model identifier"
	}
	return ""
}

func (s *Server) platformGroup(r *http.Request, id string) (contracts.UpstreamPool, error) {
	group, err := s.store.GetUpstreamPool(r.Context(), id)
	if err != nil {
		return contracts.UpstreamPool{}, err
	}
	if group.DeliveryMode.Normalize() != contracts.UpstreamDeliverySupplyGateway {
		return contracts.UpstreamPool{}, store.ErrNotFound
	}
	return group, nil
}

type platformPriceRequest struct {
	InputMicrosPerMillion          int64  `json:"input_micros_per_million"`
	OutputMicrosPerMillion         int64  `json:"output_micros_per_million"`
	InputSupplierMicrosPerMillion  *int64 `json:"input_supplier_micros_per_million,omitempty"`
	OutputSupplierMicrosPerMillion *int64 `json:"output_supplier_micros_per_million,omitempty"`
}

type platformCapacityRequest struct {
	MaxConcurrency   *int   `json:"max_concurrency,omitempty"`
	CapacityPercent  *int   `json:"capacity_percent,omitempty"`
	MaxRequestMicros *int64 `json:"max_request_micros,omitempty"`
}

type platformUpstreamRequest struct {
	GroupID       string                          `json:"group_id"`
	Name          string                          `json:"name"`
	BaseURL       string                          `json:"base_url"`
	APIKey        string                          `json:"api_key,omitempty"`
	Models        []string                        `json:"models,omitempty"`
	Prices        map[string]platformPriceRequest `json:"prices,omitempty"`
	Currency      string                          `json:"currency,omitempty"`
	Capacity      platformCapacityRequest         `json:"capacity,omitempty"`
	Priority      *int                            `json:"priority,omitempty"`
	Weight        *int                            `json:"weight,omitempty"`
	Status        contracts.UpstreamChannelStatus `json:"status,omitempty"`
	Labels        map[string]string               `json:"labels,omitempty"`
	AllowInsecure bool                            `json:"allow_insecure,omitempty"`
}

type platformPriceResponse struct {
	InputMicrosPerMillion          int64 `json:"input_micros_per_million"`
	OutputMicrosPerMillion         int64 `json:"output_micros_per_million"`
	InputSupplierMicrosPerMillion  int64 `json:"input_supplier_micros_per_million"`
	OutputSupplierMicrosPerMillion int64 `json:"output_supplier_micros_per_million"`
}

type platformCapacityResponse struct {
	MaxConcurrency   int   `json:"max_concurrency"`
	CapacityPercent  int   `json:"capacity_percent"`
	MaxRequestMicros int64 `json:"max_request_micros"`
}

type platformUpstreamResponse struct {
	ID               string                           `json:"id"`
	GroupID          string                           `json:"group_id"`
	Name             string                           `json:"name"`
	Provider         string                           `json:"provider,omitempty"`
	BaseURL          string                           `json:"base_url,omitempty"`
	APIKeyConfigured bool                             `json:"api_key_configured"`
	APIKeyMasked     string                           `json:"api_key_masked,omitempty"`
	Models           []string                         `json:"models"`
	Prices           map[string]platformPriceResponse `json:"prices"`
	Currency         string                           `json:"currency,omitempty"`
	Capacity         platformCapacityResponse         `json:"capacity"`
	Priority         int                              `json:"priority"`
	Weight           int                              `json:"weight"`
	Status           contracts.UpstreamChannelStatus  `json:"status"`
	Enabled          bool                             `json:"enabled"`
	Labels           map[string]string                `json:"labels,omitempty"`
	CreatedAt        time.Time                        `json:"created_at"`
	UpdatedAt        time.Time                        `json:"updated_at"`
}

func (s *Server) handleListPlatformUpstreams(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	groupID := strings.TrimSpace(r.URL.Query().Get("group_id"))
	channels, err := s.store.ListUpstreamChannels(r.Context(), groupID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	items := make([]platformUpstreamResponse, 0, len(channels))
	for _, channel := range channels {
		group, groupErr := s.platformGroup(r, channel.PoolID)
		if groupErr != nil {
			continue
		}
		endpoint, endpointErr := s.store.GetSupplyChannelEndpoint(r.Context(), channel.ID)
		if endpointErr != nil && !errors.Is(endpointErr, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "store_error", endpointErr.Error())
			return
		}
		items = append(items, platformUpstreamView(group, channel, endpoint))
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleGetPlatformUpstream(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	view, err := s.getPlatformUpstream(r, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleCreatePlatformUpstream(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	var input platformUpstreamRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePlatformDecodeError(w, err)
		return
	}
	group, err := s.platformGroup(r, strings.TrimSpace(input.GroupID))
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	if strings.TrimSpace(input.APIKey) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "api_key is required")
		return
	}
	channelID := ""
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key != "" {
		channelID = deterministicPlatformID("pup", strconv.FormatInt(currentUser(r).ID, 10)+":"+key)
		if existing, existingErr := s.getPlatformUpstream(r, channelID); existingErr == nil {
			writeJSON(w, http.StatusOK, existing)
			return
		} else if !errors.Is(existingErr, store.ErrNotFound) {
			writePlatformStoreError(w, existingErr)
			return
		}
	}
	channel, endpoint, msg := s.platformUpstreamRecords(input, group, nil, nil)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	channel.ID = channelID
	channel.SourceID = deterministicPlatformID("source", group.ID+":"+strings.TrimSpace(input.Name))
	channel.AccountOwnership = contracts.GatewayAccountPlatformManaged
	channel.InventoryState = contracts.UpstreamInventoryReady
	created, err := s.store.CreateUpstreamChannel(r.Context(), channel)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	endpoint.ChannelID = created.ID
	endpoint.SecretRef = "credential_ref:system/platform/upstream/" + created.ID
	endpoint.MaskedValue = maskAssignedKey(input.APIKey)
	if _, err := s.secrets.Store(r.Context(), endpoint.SecretRef, input.APIKey); err != nil {
		// The channel row has already been created. Quarantine it so a failed
		// Vault write can never become schedulable supply; retaining the row
		// preserves an auditable operator-visible record of the failed attempt.
		created.Status = contracts.UpstreamChannelMaintenance
		created.InventoryState = contracts.UpstreamInventoryQuarantined
		_, _ = s.store.UpdateUpstreamChannel(r.Context(), created)
		writeError(w, http.StatusInternalServerError, "vault_error", "upstream key could not be stored")
		return
	}
	savedEndpoint, err := s.store.UpsertSupplyChannelEndpoint(r.Context(), endpoint)
	if err != nil {
		created.Status = contracts.UpstreamChannelMaintenance
		created.InventoryState = contracts.UpstreamInventoryQuarantined
		_, _ = s.store.UpdateUpstreamChannel(r.Context(), created)
		_ = s.secrets.Delete(r.Context(), endpoint.SecretRef)
		writePlatformStoreError(w, err)
		return
	}
	s.auditUpstream(r, 0, "platform_upstream.create", "upstream_channel", created.ID)
	writeJSON(w, http.StatusCreated, platformUpstreamView(group, created, savedEndpoint))
}

func (s *Server) handleUpdatePlatformUpstream(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	current, err := s.store.GetUpstreamChannel(r.Context(), id)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	group, err := s.platformGroup(r, current.PoolID)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	currentEndpoint, err := s.store.GetSupplyChannelEndpoint(r.Context(), id)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	var input platformUpstreamRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePlatformDecodeError(w, err)
		return
	}
	// An upstream may be moved between platform groups. Historical accounting
	// is unaffected: usage records snapshot the group at reservation time, and
	// settlement resolves by reservation, not by current membership.
	if target := strings.TrimSpace(input.GroupID); target != "" && target != current.PoolID {
		moved, groupErr := s.platformGroup(r, target)
		if groupErr != nil {
			writePlatformStoreError(w, groupErr)
			return
		}
		if moved.Status == contracts.UpstreamPoolRetired {
			writeError(w, http.StatusConflict, "group_retired", "a retired group cannot receive upstreams")
			return
		}
		group = moved
	}
	channel, endpoint, msg := s.platformUpstreamRecords(input, group, &current, &currentEndpoint)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	if strings.TrimSpace(input.APIKey) != "" {
		endpoint.MaskedValue = maskAssignedKey(input.APIKey)
		if _, err := s.secrets.Store(r.Context(), endpoint.SecretRef, input.APIKey); err != nil {
			writeError(w, http.StatusInternalServerError, "vault_error", "upstream key could not be stored")
			return
		}
	}
	savedChannel, err := s.store.UpdateUpstreamChannel(r.Context(), channel)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	savedEndpoint, err := s.store.UpsertSupplyChannelEndpoint(r.Context(), endpoint)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	s.auditUpstream(r, 0, "platform_upstream.update", "upstream_channel", savedChannel.ID)
	writeJSON(w, http.StatusOK, platformUpstreamView(group, savedChannel, savedEndpoint))
}

// handleDeletePlatformUpstream is a safe, reversible deletion: it drains the
// channel from new routing work while retaining the credential reference and
// immutable accounting history. Permanent channel removal is intentionally not
// exposed because issued keys, reservations, and usage may still reference it.
func (s *Server) handleDeletePlatformUpstream(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	channel, err := s.store.GetUpstreamChannel(r.Context(), id)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	group, err := s.platformGroup(r, channel.PoolID)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	endpoint, err := s.store.GetSupplyChannelEndpoint(r.Context(), channel.ID)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	channel.Status = contracts.UpstreamChannelMaintenance
	channel.InventoryState = contracts.UpstreamInventoryQuarantined
	endpoint.Enabled = false
	savedChannel, err := s.store.UpdateUpstreamChannel(r.Context(), channel)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	savedEndpoint, err := s.store.UpsertSupplyChannelEndpoint(r.Context(), endpoint)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	s.auditUpstream(r, 0, "platform_upstream.disable", "upstream_channel", savedChannel.ID)
	writeJSON(w, http.StatusOK, platformUpstreamView(group, savedChannel, savedEndpoint))
}

type platformUpstreamTestResponse struct {
	OK         bool     `json:"ok"`
	StatusCode int      `json:"status_code,omitempty"`
	LatencyMS  int64    `json:"latency_ms"`
	ModelCount int      `json:"model_count,omitempty"`
	Models     []string `json:"models,omitempty"`
	ErrorCode  string   `json:"error_code,omitempty"`
}

// handleTestPlatformUpstream validates the stored credential against the
// standard OpenAI model endpoint. The Vault value is used only to construct the
// outbound Authorization header; it is never included in a response or error.
func (s *Server) handleTestPlatformUpstream(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	view, err := s.getPlatformUpstream(r, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	endpoint, err := s.store.GetSupplyChannelEndpoint(r.Context(), view.ID)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	secret, err := s.secrets.Resolve(r.Context(), endpoint.SecretRef)
	if err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			writeError(w, http.StatusServiceUnavailable, "credential_unavailable", "upstream credential is unavailable")
			return
		}
		writeError(w, http.StatusInternalServerError, "vault_error", "upstream credential could not be resolved")
		return
	}
	testURL, err := platformUpstreamModelsURL(endpoint.BaseURL, endpoint.AllowInsecure)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "upstream URL is invalid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), platformUpstreamTestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "upstream test request could not be created")
		return
	}
	req.Header.Set("Authorization", "Bearer "+secret.Value)
	req.Header.Set("Accept", "application/json")
	started := time.Now()
	client := &http.Client{Timeout: platformUpstreamTestTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		s.auditUpstreamResult(r, 0, "platform_upstream.test", "upstream_channel", view.ID, "rejected", "transport_error")
		writeJSON(w, http.StatusOK, platformUpstreamTestResponse{OK: false, LatencyMS: latency, ErrorCode: "transport_error"})
		return
	}
	defer resp.Body.Close()
	result := platformUpstreamTestResponse{OK: resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices, StatusCode: resp.StatusCode, LatencyMS: latency}
	if result.OK {
		var payload struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil || len(body) >= 1<<20 || json.Unmarshal(body, &payload) != nil {
			result.OK = false
			result.ErrorCode = "invalid_models_response"
		} else {
			models := make([]string, 0, len(payload.Data))
			for _, model := range payload.Data {
				models = append(models, model.ID)
			}
			models = normalizedModels(models)
			if len(models) > 100 {
				models = models[:100]
			}
			if !validPlatformModels(models) {
				result.OK = false
				result.ErrorCode = "invalid_models_response"
				models = nil
			}
			result.Models = models
			result.ModelCount = len(models)
		}
	} else {
		// Do not copy the upstream body: providers commonly include diagnostic
		// data that must not become a control-plane response.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
		result.ErrorCode = "upstream_models_rejected"
	}
	if result.OK {
		s.auditUpstream(r, 0, "platform_upstream.test", "upstream_channel", view.ID)
	} else {
		s.auditUpstreamResult(r, 0, "platform_upstream.test", "upstream_channel", view.ID, "rejected", "models_request_failed")
	}
	writeJSON(w, http.StatusOK, result)
}

func platformUpstreamModelsURL(base string, allowInsecure bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Scheme != "https" && !(allowInsecure && parsed.Scheme == "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid upstream base URL")
	}
	path := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(path, "/v1") {
		path += "/models"
	} else {
		path += "/v1/models"
	}
	parsed.Path = path
	return parsed.String(), nil
}

func (s *Server) platformUpstreamRecords(input platformUpstreamRequest, group contracts.UpstreamPool, current *contracts.UpstreamChannel, currentEndpoint *contracts.SupplyChannelEndpoint) (contracts.UpstreamChannel, contracts.SupplyChannelEndpoint, string) {
	channel := contracts.UpstreamChannel{PoolID: group.ID, Provider: group.Provider, Status: contracts.UpstreamChannelActive, Weight: 1, Models: append([]string(nil), group.Models...)}
	endpoint := contracts.SupplyChannelEndpoint{ChannelID: "pending", Currency: "CNY", CapacityPercent: 100, MaxRequestMicros: defaultPlatformMaxRequestMicros, Enabled: true}
	if current != nil {
		channel = *current
		// The caller resolves the target group, so this also applies a move.
		channel.PoolID = group.ID
	}
	if currentEndpoint != nil {
		endpoint = *currentEndpoint
	}
	if name := strings.TrimSpace(input.Name); name != "" {
		channel.DisplayName = name
	}
	if channel.DisplayName == "" {
		return channel, endpoint, "name is required"
	}
	if input.Models != nil {
		channel.Models = normalizedModels(input.Models)
	}
	if len(channel.Models) == 0 {
		channel.Models = append([]string(nil), group.Models...)
	}
	if !validPlatformModels(channel.Models) {
		return channel, endpoint, "models contain an invalid model identifier"
	}
	if input.Priority != nil {
		channel.Priority = *input.Priority
	}
	if input.Weight != nil {
		channel.Weight = *input.Weight
	}
	if channel.Priority < 0 || channel.Weight < 0 {
		return channel, endpoint, "priority and weight must be non-negative"
	}
	if input.Labels != nil {
		channel.Labels = input.Labels
	}
	if input.Status != "" {
		channel.Status = input.Status
	}
	if msg := validChannelStatus(channel.Status); msg != "" {
		return channel, endpoint, msg
	}
	if channel.Status == contracts.UpstreamChannelRetired {
		return channel, endpoint, "retire upstream inventory through the lifecycle API"
	}
	channel.AccountOwnership = contracts.GatewayAccountPlatformManaged
	channel.InventoryState = contracts.UpstreamInventoryReady
	if channel.Status != contracts.UpstreamChannelActive {
		channel.InventoryState = contracts.UpstreamInventoryQuarantined
	}
	if baseURL := strings.TrimRight(strings.TrimSpace(input.BaseURL), "/"); baseURL != "" {
		endpoint.BaseURL = baseURL
	}
	if endpoint.BaseURL == "" {
		return channel, endpoint, "base_url is required"
	}
	endpoint.AllowInsecure = strings.HasPrefix(strings.ToLower(endpoint.BaseURL), "http://") && s.allowInsecureSupplyUpstreams
	if input.AllowInsecure && !s.allowInsecureSupplyUpstreams {
		return channel, endpoint, "insecure HTTP upstreams are disabled"
	}
	if currency := strings.ToUpper(strings.TrimSpace(input.Currency)); currency != "" {
		endpoint.Currency = currency
	}
	if len(input.Prices) > 0 {
		price, priceErr := onePlatformPrice(input.Prices, channel.Models)
		if priceErr != "" {
			return channel, endpoint, priceErr
		}
		endpoint.InputPriceMicrosPerMillion = price.InputMicrosPerMillion
		endpoint.OutputPriceMicrosPerMillion = price.OutputMicrosPerMillion
		endpoint.InputSupplierMicrosPerMillion = price.InputMicrosPerMillion
		endpoint.OutputSupplierMicrosPerMillion = price.OutputMicrosPerMillion
		if price.InputSupplierMicrosPerMillion != nil {
			endpoint.InputSupplierMicrosPerMillion = *price.InputSupplierMicrosPerMillion
		}
		if price.OutputSupplierMicrosPerMillion != nil {
			endpoint.OutputSupplierMicrosPerMillion = *price.OutputSupplierMicrosPerMillion
		}
	} else if currentEndpoint == nil {
		// Explicit prices always win; on create without prices the sell price
		// is materialized from the base table at the group's rate multiplier.
		// V1 keeps one price per upstream, so every model must resolve to the
		// same converted price — otherwise the operator has to price by hand
		// (or split the upstream by model family). Without base-table pricing
		// an omitted price must fail rather than create a free upstream.
		if !s.pricing.Enabled() {
			return channel, endpoint, "prices are required: base price table pricing is not configured (set the USD→CNY rate under system settings → commerce)"
		}
		if msg := fillPricesFromBaseTable(&endpoint, channel.Models, s.pricing, poolRateMultiplierBps(group)); msg != "" {
			return channel, endpoint, msg
		}
	}
	if input.Capacity.MaxConcurrency != nil {
		endpoint.MaxConcurrency = *input.Capacity.MaxConcurrency
	}
	if input.Capacity.CapacityPercent != nil {
		endpoint.CapacityPercent = *input.Capacity.CapacityPercent
	}
	if input.Capacity.MaxRequestMicros != nil {
		endpoint.MaxRequestMicros = *input.Capacity.MaxRequestMicros
	} else if currentEndpoint == nil {
		// No explicit cap on create: derive it from this upstream's own sell
		// price instead of the one-size fixed default.
		endpoint.MaxRequestMicros = derivedMaxRequestMicros(endpoint)
	}
	endpoint.Enabled = channel.Status == contracts.UpstreamChannelActive
	if currentEndpoint == nil {
		endpoint.SecretRef = "pending"
	}
	if !endpoint.Valid() {
		return channel, endpoint, "upstream URL, currency, prices, or capacity are invalid"
	}
	return channel, endpoint, ""
}

func onePlatformPrice(prices map[string]platformPriceRequest, models []string) (platformPriceRequest, string) {
	keys := make([]string, 0, len(prices))
	for model := range prices {
		keys = append(keys, model)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return platformPriceRequest{}, "prices are empty"
	}
	selected := prices[keys[0]]
	for _, model := range models {
		if price, ok := prices[model]; ok {
			selected = price
			break
		}
	}
	for _, price := range prices {
		if !samePlatformPrice(selected, price) {
			return platformPriceRequest{}, "V1 requires one price shared by every model on an upstream"
		}
	}
	return selected, ""
}

func samePlatformPrice(left, right platformPriceRequest) bool {
	return left.InputMicrosPerMillion == right.InputMicrosPerMillion && left.OutputMicrosPerMillion == right.OutputMicrosPerMillion &&
		optionalInt64Equal(left.InputSupplierMicrosPerMillion, right.InputSupplierMicrosPerMillion) && optionalInt64Equal(left.OutputSupplierMicrosPerMillion, right.OutputSupplierMicrosPerMillion)
}

func optionalInt64Equal(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Server) getPlatformUpstream(r *http.Request, id string) (platformUpstreamResponse, error) {
	channel, err := s.store.GetUpstreamChannel(r.Context(), id)
	if err != nil {
		return platformUpstreamResponse{}, err
	}
	group, err := s.platformGroup(r, channel.PoolID)
	if err != nil {
		return platformUpstreamResponse{}, err
	}
	endpoint, err := s.store.GetSupplyChannelEndpoint(r.Context(), id)
	if err != nil {
		return platformUpstreamResponse{}, err
	}
	return platformUpstreamView(group, channel, endpoint), nil
}

func platformUpstreamView(group contracts.UpstreamPool, channel contracts.UpstreamChannel, endpoint contracts.SupplyChannelEndpoint) platformUpstreamResponse {
	price := platformPriceResponse{
		InputMicrosPerMillion: endpoint.InputPriceMicrosPerMillion, OutputMicrosPerMillion: endpoint.OutputPriceMicrosPerMillion,
		InputSupplierMicrosPerMillion: endpoint.InputSupplierMicrosPerMillion, OutputSupplierMicrosPerMillion: endpoint.OutputSupplierMicrosPerMillion,
	}
	prices := make(map[string]platformPriceResponse, len(channel.Models))
	for _, model := range channel.Models {
		prices[model] = price
	}
	return platformUpstreamResponse{
		ID: channel.ID, GroupID: channel.PoolID, Name: channel.DisplayName, Provider: channel.Provider,
		BaseURL: endpoint.BaseURL, APIKeyConfigured: strings.TrimSpace(endpoint.SecretRef) != "", APIKeyMasked: endpoint.MaskedValue,
		Models: append([]string(nil), channel.Models...), Prices: prices, Currency: endpoint.Currency,
		Capacity: platformCapacityResponse{MaxConcurrency: endpoint.MaxConcurrency, CapacityPercent: endpoint.CapacityPercent, MaxRequestMicros: endpoint.MaxRequestMicros},
		Priority: channel.Priority, Weight: channel.Weight, Status: channel.Status, Enabled: endpoint.Enabled,
		Labels:    channel.Labels,
		CreatedAt: channel.CreatedAt, UpdatedAt: channel.UpdatedAt,
	}
}

type platformKeyCreateRequest struct {
	UserID           int64      `json:"user_id,omitempty"`
	GroupID          string     `json:"group_id"`
	Name             string     `json:"name"`
	Models           []string   `json:"models,omitempty"`
	DailyLimitMicros int64      `json:"daily_limit_micros,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	Status           string     `json:"status,omitempty"`
}

type platformKeyCreateResponse struct {
	Key          contracts.VirtualKey `json:"key"`
	PlaintextKey string               `json:"plaintext_key"`
}

type platformKeyValueResponse struct {
	Value string `json:"value"`
}

type platformKeyUpdateRequest struct {
	Name             *string     `json:"name,omitempty"`
	Models           *[]string   `json:"models,omitempty"`
	DailyLimitMicros *int64      `json:"daily_limit_micros,omitempty"`
	Enabled          *bool       `json:"enabled,omitempty"`
	Status           *string     `json:"status,omitempty"`
	ExpiresAt        **time.Time `json:"expires_at,omitempty"`
}

func (s *Server) handleListPlatformKeys(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	userID, ok := s.platformUserID(w, r, r.URL.Query().Get("user_id"), false)
	if !ok {
		return
	}
	keys, err := s.store.ListVirtualKeys(r.Context(), userID)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	items := keys[:0]
	for _, key := range keys {
		if key.GroupID != "" && key.InstanceID == "" {
			items = append(items, key)
		}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleGetPlatformKey(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	userID, ok := s.platformUserID(w, r, r.URL.Query().Get("user_id"), false)
	if !ok {
		return
	}
	key, err := s.store.GetVirtualKey(r.Context(), userID, strings.TrimSpace(r.PathValue("id")))
	if err != nil || key.GroupID == "" || key.InstanceID != "" {
		if err == nil {
			err = store.ErrNotFound
		}
		writePlatformStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, key)
}

// handleGetPlatformKeyValue mirrors Sub2API's operator experience: a key
// owner can retrieve an existing downstream credential and copy it again.
// E2M keeps the plaintext encrypted in Vault rather than in the relational
// key row, and this dedicated no-store route avoids returning it from ordinary
// list/detail responses where it could be cached or logged accidentally.
func (s *Server) handleGetPlatformKeyValue(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	userID, ok := s.platformUserID(w, r, r.URL.Query().Get("user_id"), false)
	if !ok || !s.enabledPlatformCustomer(w, r, userID) {
		return
	}
	key, err := s.store.GetVirtualKey(r.Context(), userID, strings.TrimSpace(r.PathValue("id")))
	if err != nil || key.GroupID == "" || key.InstanceID != "" {
		if err == nil {
			err = store.ErrNotFound
		}
		writePlatformStoreError(w, err)
		return
	}
	secret, err := s.secrets.Resolve(r.Context(), key.SecretRef)
	if err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			writeError(w, http.StatusServiceUnavailable, "platform_key_unavailable", "API key value is unavailable")
			return
		}
		writeError(w, http.StatusInternalServerError, "vault_error", "API key could not be resolved")
		return
	}
	// Fail closed if the sensitive read cannot be added to the durable audit.
	if err := s.auditPlatformCustomer(r, userID, "platform_key.view", "virtual_key", key.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "audit_error", "could not record API key access")
		return
	}
	writeJSON(w, http.StatusOK, platformKeyValueResponse{Value: secret.Value})
}

func (s *Server) handleCreatePlatformKey(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	var input platformKeyCreateRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePlatformDecodeError(w, err)
		return
	}
	rawUserID := ""
	if input.UserID > 0 {
		rawUserID = strconv.FormatInt(input.UserID, 10)
	}
	userID, ok := s.platformUserID(w, r, rawUserID, false)
	if !ok || !s.enabledPlatformCustomer(w, r, userID) {
		return
	}
	group, err := s.platformGroup(r, strings.TrimSpace(input.GroupID))
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	if group.Status == contracts.UpstreamPoolRetired {
		writeError(w, http.StatusConflict, "group_unavailable", "group is retired")
		return
	}
	if strings.TrimSpace(input.Name) == "" || input.DailyLimitMicros < 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "name is required and daily_limit_micros must be non-negative")
		return
	}
	models := normalizedModels(input.Models)
	if len(models) == 0 {
		models = append([]string(nil), group.Models...)
	}
	if !validPlatformModels(models) {
		writeError(w, http.StatusBadRequest, "validation_failed", "models contain an invalid model identifier")
		return
	}
	enabled, ok := parsePlatformEnabled(input.Status, true)
	if !ok {
		writeError(w, http.StatusBadRequest, "validation_failed", "status must be active or disabled")
		return
	}
	id := ""
	if idem := strings.TrimSpace(r.Header.Get("Idempotency-Key")); idem != "" {
		id = deterministicPlatformID("pkey", strconv.FormatInt(userID, 10)+":"+idem)
		if existing, getErr := s.store.GetVirtualKey(r.Context(), userID, id); getErr == nil {
			secret, secretErr := s.secrets.Resolve(r.Context(), existing.SecretRef)
			if secretErr != nil {
				writeError(w, http.StatusInternalServerError, "vault_error", "idempotent key response could not be restored")
				return
			}
			writeJSON(w, http.StatusOK, platformKeyCreateResponse{Key: existing, PlaintextKey: secret.Value})
			return
		} else if !errors.Is(getErr, store.ErrNotFound) {
			writePlatformStoreError(w, getErr)
			return
		}
	}
	plaintext, err := newVirtualKeyToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "key_generation_failed", "could not generate API key")
		return
	}
	if id == "" {
		id = deterministicPlatformID("pkey", plaintext)
	}
	secretRef := "credential_ref:system/platform/virtual-key/" + id
	if _, err := s.secrets.Store(r.Context(), secretRef, plaintext); err != nil {
		writeError(w, http.StatusInternalServerError, "vault_error", "API key could not be stored")
		return
	}
	key, err := s.store.CreateVirtualKey(r.Context(), contracts.VirtualKey{
		ID: id, UserID: userID, GroupID: group.ID, Name: strings.TrimSpace(input.Name), ResourceClass: group.ResourceClass,
		Prefix: plaintext[:12], TokenHash: contracts.HashVirtualKey(plaintext), SecretRef: secretRef,
		Models: models, DailyLimitMicros: input.DailyLimitMicros, ExpiresAt: input.ExpiresAt,
	})
	if err != nil {
		_ = s.secrets.Delete(r.Context(), secretRef)
		writePlatformStoreError(w, err)
		return
	}
	if !enabled {
		key.Enabled = false
		key, err = s.store.UpdateVirtualKey(r.Context(), key)
		if err != nil {
			writePlatformStoreError(w, err)
			return
		}
	}
	_ = s.auditPlatformCustomer(r, userID, "platform_key.create", "virtual_key", key.ID)
	writeJSON(w, http.StatusCreated, platformKeyCreateResponse{Key: key, PlaintextKey: plaintext})
}

func (s *Server) handleUpdatePlatformKey(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	userID, ok := s.platformUserID(w, r, r.URL.Query().Get("user_id"), false)
	if !ok {
		return
	}
	key, err := s.store.GetVirtualKey(r.Context(), userID, strings.TrimSpace(r.PathValue("id")))
	if err != nil || key.GroupID == "" || key.InstanceID != "" {
		if err == nil {
			err = store.ErrNotFound
		}
		writePlatformStoreError(w, err)
		return
	}
	var input platformKeyUpdateRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePlatformDecodeError(w, err)
		return
	}
	if input.Name != nil {
		key.Name = strings.TrimSpace(*input.Name)
	}
	if input.Models != nil {
		key.Models = normalizedModels(*input.Models)
	}
	if input.DailyLimitMicros != nil {
		key.DailyLimitMicros = *input.DailyLimitMicros
	}
	if input.Enabled != nil {
		key.Enabled = *input.Enabled
	}
	if input.Status != nil {
		var valid bool
		key.Enabled, valid = parsePlatformEnabled(*input.Status, key.Enabled)
		if !valid {
			writeError(w, http.StatusBadRequest, "validation_failed", "status must be active or disabled")
			return
		}
	}
	if input.ExpiresAt != nil {
		key.ExpiresAt = *input.ExpiresAt
	}
	if strings.TrimSpace(key.Name) == "" || key.DailyLimitMicros < 0 || !validPlatformModels(key.Models) {
		writeError(w, http.StatusBadRequest, "validation_failed", "name, models, or daily_limit_micros are invalid")
		return
	}
	saved, err := s.store.UpdateVirtualKey(r.Context(), key)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	_ = s.auditPlatformCustomer(r, userID, "platform_key.update", "virtual_key", saved.ID)
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeletePlatformKey(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	userID, ok := s.platformUserID(w, r, r.URL.Query().Get("user_id"), false)
	if !ok {
		return
	}
	key, err := s.store.GetVirtualKey(r.Context(), userID, strings.TrimSpace(r.PathValue("id")))
	if err != nil || key.GroupID == "" || key.InstanceID != "" {
		if err == nil {
			err = store.ErrNotFound
		}
		writePlatformStoreError(w, err)
		return
	}
	if err := s.store.DeleteVirtualKey(r.Context(), userID, key.ID); err != nil {
		writePlatformStoreError(w, err)
		return
	}
	if s.secrets != nil {
		_ = s.secrets.Delete(r.Context(), key.SecretRef)
	}
	_ = s.auditPlatformCustomer(r, userID, "platform_key.delete", "virtual_key", key.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleGetPlatformWallet(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	userID, ok := s.platformUserID(w, r, r.URL.Query().Get("user_id"), false)
	if !ok {
		return
	}
	currency := platformCurrency(r.URL.Query().Get("currency"))
	wallet, err := s.store.GetWallet(r.Context(), userID, currency)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wallet)
}

func (s *Server) handleListPlatformWalletJournals(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	userID, ok := s.platformUserID(w, r, r.URL.Query().Get("user_id"), false)
	if !ok {
		return
	}
	limit, ok := parsePlatformLimit(w, r.URL.Query().Get("limit"), 50, 100)
	if !ok {
		return
	}
	items, err := s.store.ListWalletJournals(r.Context(), userID, limit)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	if items == nil {
		items = []contracts.WalletJournal{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

type platformWalletAdjustmentRequest struct {
	UserID       int64  `json:"user_id"`
	AmountMicros int64  `json:"amount_micros"`
	Currency     string `json:"currency,omitempty"`
	Reason       string `json:"reason"`
}

func (s *Server) handleAdjustPlatformWallet(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	var input platformWalletAdjustmentRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePlatformDecodeError(w, err)
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	input.Reason = strings.TrimSpace(input.Reason)
	if input.UserID <= 0 || input.AmountMicros == 0 || input.AmountMicros > 1_000_000_000_000_000 || input.AmountMicros < -1_000_000_000_000_000 || input.Reason == "" || idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id, non-zero amount_micros, reason, and Idempotency-Key are required")
		return
	}
	if !s.enabledPlatformCustomer(w, r, input.UserID) {
		return
	}
	wallet, journal, err := s.store.AdjustWalletBalance(r.Context(), input.UserID, platformCurrency(input.Currency), input.AmountMicros, idempotencyKey, input.Reason)
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	_ = s.auditPlatformCustomer(r, input.UserID, "platform_wallet.adjust", "wallet_journal", journal.ID)
	writeJSON(w, http.StatusOK, map[string]any{"wallet": wallet, "journal": journal})
}

func (s *Server) handleListPlatformUsage(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	userID, ok := s.platformUserID(w, r, r.URL.Query().Get("user_id"), true)
	if !ok {
		return
	}
	limit, ok := parsePlatformLimit(w, r.URL.Query().Get("limit"), 50, 200)
	if !ok {
		return
	}
	status := contracts.SupplyUsageStatus(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status"))))
	if status != "" && status != contracts.SupplyUsageReserved && status != contracts.SupplyUsageSettled && status != contracts.SupplyUsageReleased {
		writeError(w, http.StatusBadRequest, "validation_failed", "status must be reserved, settled, or released")
		return
	}
	items, err := s.store.ListSupplyUsage(r.Context(), contracts.SupplyUsageFilter{
		UserID: userID, GroupID: strings.TrimSpace(r.URL.Query().Get("group_id")),
		VirtualKeyID: strings.TrimSpace(r.URL.Query().Get("key_id")), Status: status, Limit: limit,
	})
	if err != nil {
		writePlatformStoreError(w, err)
		return
	}
	if items == nil {
		items = []contracts.SupplyUsageRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) platformUserID(w http.ResponseWriter, r *http.Request, raw string, allowAdminAll bool) (int64, bool) {
	requested, ok := parseOptionalUserID(w, raw)
	if !ok {
		return 0, false
	}
	actor := currentUser(r)
	if auth.IsPlatformAdmin(actor) {
		if requested == 0 && !allowAdminAll {
			return actor.ID, true
		}
		return requested, true
	}
	if !auth.IsOwner(actor) {
		writeError(w, http.StatusForbidden, "forbidden", "client role required")
		return 0, false
	}
	if requested == 0 || requested == actor.ID {
		return actor.ID, true
	}
	s.recordCrossOwnerRejection(r, requested)
	writeError(w, http.StatusForbidden, "forbidden", "user out of scope")
	return 0, false
}

// Platform groups are the downstream product catalog: clients need their IDs,
// models and economy/stable class to create scoped virtual keys. Upstream
// credentials and channel management remain administrator-only.
func requirePlatformCustomer(w http.ResponseWriter, r *http.Request) bool {
	actor := currentUser(r)
	if auth.IsPlatformAdmin(actor) || auth.IsOwner(actor) {
		return true
	}
	writeError(w, http.StatusForbidden, "forbidden", "client role required")
	return false
}

func (s *Server) enabledPlatformCustomer(w http.ResponseWriter, r *http.Request, userID int64) bool {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		writePlatformStoreError(w, err)
		return false
	}
	if !user.Enabled || !auth.IsOwner(user) && !auth.IsPlatformAdmin(user) {
		writeError(w, http.StatusBadRequest, "validation_failed", "customer must be an enabled client or administrator")
		return false
	}
	return true
}

func (s *Server) auditPlatformCustomer(r *http.Request, userID int64, action, targetType, targetID string) error {
	actor := currentUser(r)
	_, err := s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: userID, ActorType: "user", ActorID: actor.Email, Action: action,
		RiskLevel: contracts.RiskLevelL1, TargetType: targetType, TargetID: targetID, Result: "accepted",
	})
	return err
}

func parsePlatformEnabled(status string, fallback bool) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "":
		return fallback, true
	case "active", "enabled":
		return true, true
	case "disabled", "inactive":
		return false, true
	default:
		return false, false
	}
}

func platformCurrency(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return "CNY"
	}
	return value
}

func parsePlatformLimit(w http.ResponseWriter, raw string, fallback, maximum int) (int, bool) {
	if strings.TrimSpace(raw) == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 || value > maximum {
		writeError(w, http.StatusBadRequest, "validation_failed", "limit must be between 1 and "+strconv.Itoa(maximum))
		return 0, false
	}
	return value, true
}

func normalizedModels(models []string) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		key := strings.ToLower(model)
		if model == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, model)
	}
	return out
}

func validPlatformModels(models []string) bool {
	if len(models) > 100 {
		return false
	}
	for _, model := range models {
		if strings.TrimSpace(model) == "" || !contracts.ValidHybridRoutingModel(model) {
			return false
		}
	}
	return true
}

func deterministicPlatformID(prefix, value string) string {
	sum := sha256.Sum256([]byte(value))
	return prefix + "-" + hex.EncodeToString(sum[:12])
}

func writePlatformDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "platform request body exceeds 1 MiB")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
}

func writePlatformStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "platform distribution resource not found")
	case errors.Is(err, store.ErrDuplicate), errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "platform_conflict", err.Error())
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
	}
}
