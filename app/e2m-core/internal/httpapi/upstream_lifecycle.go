package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/retirement"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/vault"
)

// RegisterUpstreamLifecycleRoutes is kept separate from Routes so the shared
// router only needs one append-only hook during integration.
func (s *Server) RegisterUpstreamLifecycleRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/upstream-inventory", s.handleGetUpstreamInventory)
	api.HandleFunc("POST /api/v1/upstream-pools/{id}/inventory/import", s.handleImportUpstreamInventory)
	api.HandleFunc("PUT /api/v1/upstream-pools/{id}/safety-stock", s.handleSetUpstreamSafetyStock)
	api.HandleFunc("PUT /api/v1/upstream-channels/{id}/inventory-state", s.handleSetUpstreamInventoryState)
	api.HandleFunc("POST /api/v1/upstream-channels/{id}/migrate", s.handleMigrateUpstreamChannel)
	api.HandleFunc("GET /api/v1/upstream-channels/{id}/key-rotation", s.handleGetUpstreamKeyRotation)
	api.HandleFunc("POST /api/v1/upstream-channels/{id}/key-rotation", s.handleStartUpstreamKeyRotation)
	api.HandleFunc("POST /api/v1/upstream-channels/{id}/key-rotation/rollback", s.handleRollbackUpstreamKeyRotation)
	api.HandleFunc("POST /api/v1/upstream-channels/{id}/key-rotation/finalize", s.handleFinalizeUpstreamKeyRotation)
	api.HandleFunc("GET /api/v1/pool-retirement-jobs", s.handleListPoolRetirementJobs)
	api.HandleFunc("POST /api/v1/upstream-pools/{id}/retirement-jobs", s.handleCreatePoolRetirementJob)
	api.HandleFunc("POST /api/v1/pool-retirement-jobs/{id}/run", s.handleRunPoolRetirementJob)
}

func (s *Server) lifecycleStore(w http.ResponseWriter) (store.UpstreamLifecycleStore, bool) {
	lifecycle, ok := store.AsUpstreamLifecycleStore(s.store)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "lifecycle_store_unavailable", "upstream lifecycle persistence is not configured")
	}
	return lifecycle, ok
}

func (s *Server) handleGetUpstreamInventory(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	lifecycle, ok := s.lifecycleStore(w)
	if !ok {
		return
	}
	snapshot, err := lifecycle.GetUpstreamInventory(r.Context(), r.URL.Query().Get("pool_id"))
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	if snapshot.Items == nil {
		snapshot.Items = []contracts.UpstreamInventoryItem{}
	}
	if snapshot.Pools == nil {
		snapshot.Pools = []contracts.UpstreamPoolInventorySummary{}
	}
	if snapshot.Alerts == nil {
		snapshot.Alerts = []contracts.UpstreamInventoryAlert{}
	}
	writeJSON(w, http.StatusOK, snapshot)
}

type inventoryImportRequest struct {
	Entries []inventoryImportEntry `json:"entries"`
}
type inventoryImportEntry struct {
	SourceID            string   `json:"source_id"`
	DisplayName         string   `json:"display_name"`
	Provider            string   `json:"provider,omitempty"`
	Models              []string `json:"models,omitempty"`
	Groups              []string `json:"groups,omitempty"`
	Value               string   `json:"value"`
	CredentialBindingID string   `json:"credential_binding_id,omitempty"`
	ProxyBindingID      string   `json:"proxy_binding_id,omitempty"`
	Priority            int      `json:"priority,omitempty"`
	Weight              int      `json:"weight,omitempty"`
	CostHint            float64  `json:"cost_hint,omitempty"`
}

func (s *Server) handleImportUpstreamInventory(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	lifecycle, ok := s.lifecycleStore(w)
	if !ok {
		return
	}
	var request inventoryImportRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if len(request.Entries) == 0 || len(request.Entries) > 500 {
		writeError(w, http.StatusBadRequest, "validation_failed", "entries must contain 1 to 500 keys")
		return
	}
	poolID := strings.TrimSpace(r.PathValue("id"))
	entries := make([]contracts.UpstreamInventoryImportEntry, 0, len(request.Entries))
	refs := make([]string, 0, len(request.Entries))
	for _, input := range request.Entries {
		value := strings.TrimSpace(input.Value)
		if value == "" || strings.TrimSpace(input.SourceID) == "" || strings.TrimSpace(input.DisplayName) == "" {
			s.deleteVaultRefs(r, refs)
			writeError(w, http.StatusBadRequest, "validation_failed", "source_id, display_name, and value are required")
			return
		}
		if !contracts.IsUpstreamSourceIdentity(strings.TrimSpace(input.SourceID)) {
			s.deleteVaultRefs(r, refs)
			writeError(w, http.StatusBadRequest, "validation_failed", "source_id must be a short opaque identifier")
			return
		}
		ref, err := newInventorySecretRef(poolID)
		if err != nil {
			s.deleteVaultRefs(r, refs)
			writeError(w, http.StatusInternalServerError, "secret_ref_error", "could not create inventory secret reference")
			return
		}
		if _, err := s.secrets.Store(r.Context(), ref, value); err != nil {
			s.deleteVaultRefs(r, refs)
			writeError(w, http.StatusInternalServerError, "vault_error", err.Error())
			return
		}
		refs = append(refs, ref)
		entries = append(entries, contracts.UpstreamInventoryImportEntry{Channel: contracts.UpstreamChannel{
			PoolID: poolID, SourceID: strings.TrimSpace(input.SourceID), DisplayName: strings.TrimSpace(input.DisplayName), Provider: strings.TrimSpace(input.Provider), Models: input.Models, Groups: input.Groups,
			CredentialBindingID: strings.TrimSpace(input.CredentialBindingID), ProxyBindingID: strings.TrimSpace(input.ProxyBindingID), Priority: input.Priority, Weight: input.Weight, CostHint: input.CostHint,
			AccountOwnership: contracts.GatewayAccountPlatformManaged, Status: contracts.UpstreamChannelMaintenance, InventoryState: contracts.UpstreamInventoryDraft,
		}, SecretRef: ref, MaskedValue: maskAssignedKey(value)})
	}
	result, err := lifecycle.ImportUpstreamInventory(r.Context(), poolID, entries)
	if err != nil {
		s.deleteVaultRefs(r, refs)
		writeLifecycleError(w, err)
		return
	}
	s.auditUpstream(r, 0, "upstream_inventory.import", "upstream_pool", poolID)
	writeJSON(w, http.StatusCreated, result)
}

func newInventorySecretRef(poolID string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return fmt.Sprintf("credential_ref:platform/inventory/%s/%s", safeSecretName(poolID), hex.EncodeToString(raw)), nil
}
func (s *Server) deleteVaultRefs(r *http.Request, refs []string) {
	for _, ref := range refs {
		_ = s.secrets.Delete(r.Context(), ref)
	}
}

type safetyStockRequest struct {
	Threshold int `json:"threshold"`
}

func (s *Server) handleSetUpstreamSafetyStock(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	lifecycle, ok := s.lifecycleStore(w)
	if !ok {
		return
	}
	var input safetyStockRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := lifecycle.SetUpstreamPoolSafetyStock(r.Context(), r.PathValue("id"), input.Threshold); err != nil {
		writeLifecycleError(w, err)
		return
	}
	s.auditUpstream(r, 0, "upstream_inventory.safety_stock", "upstream_pool", r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"pool_id": r.PathValue("id"), "threshold": input.Threshold})
}

type inventoryStateRequest struct {
	State contracts.UpstreamInventoryState `json:"state"`
}

func (s *Server) handleSetUpstreamInventoryState(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	lifecycle, ok := s.lifecycleStore(w)
	if !ok {
		return
	}
	var input inventoryStateRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	out, err := lifecycle.SetUpstreamInventoryState(r.Context(), r.PathValue("id"), input.State)
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	s.auditUpstream(r, 0, "upstream_inventory.state", "upstream_channel", r.PathValue("id"))
	writeJSON(w, http.StatusOK, out)
}

type migrateChannelRequest struct {
	PoolID string `json:"pool_id"`
	Reason string `json:"reason"`
}

func (s *Server) handleMigrateUpstreamChannel(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	lifecycle, ok := s.lifecycleStore(w)
	if !ok {
		return
	}
	var input migrateChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	out, err := lifecycle.MigrateUpstreamChannel(r.Context(), r.PathValue("id"), input.PoolID, input.Reason, currentUser(r).ID)
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	s.auditUpstream(r, 0, "upstream_channel.migrate", "upstream_channel", r.PathValue("id"))
	writeJSON(w, http.StatusOK, out)
}

type rotateKeyRequest struct {
	Value string `json:"value"`
}

func (s *Server) handleGetUpstreamKeyRotation(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	lifecycle, ok := s.lifecycleStore(w)
	if !ok {
		return
	}
	out, err := lifecycle.GetUpstreamKeyRotation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleStartUpstreamKeyRotation(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	lifecycle, ok := s.lifecycleStore(w)
	if !ok {
		return
	}
	var input rotateKeyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	value := strings.TrimSpace(input.Value)
	if value == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "value is required")
		return
	}
	ref, err := newDeliverySecretRef(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "secret_ref_error", "could not create key reference")
		return
	}
	if _, err = s.secrets.Store(r.Context(), ref, value); err != nil {
		writeError(w, http.StatusInternalServerError, "vault_error", err.Error())
		return
	}
	out, err := lifecycle.StartUpstreamKeyRotation(r.Context(), r.PathValue("id"), ref, maskAssignedKey(value))
	if err != nil {
		_ = s.secrets.Delete(r.Context(), ref)
		writeLifecycleError(w, err)
		return
	}
	s.auditUpstream(r, 0, "upstream_key_rotation.start", "upstream_channel", r.PathValue("id"))
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleRollbackUpstreamKeyRotation(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	lifecycle, ok := s.lifecycleStore(w)
	if !ok {
		return
	}
	out, err := lifecycle.BeginUpstreamKeyRotationRollback(r.Context(), r.PathValue("id"))
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	s.auditUpstream(r, 0, "upstream_key_rotation.rollback", "upstream_channel", r.PathValue("id"))
	writeJSON(w, http.StatusOK, out.Rotation)
}
func (s *Server) handleFinalizeUpstreamKeyRotation(w http.ResponseWriter, r *http.Request) {
	setSensitiveResponseHeaders(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return
	}
	lifecycle, ok := s.lifecycleStore(w)
	if !ok {
		return
	}
	barrier, err := lifecycle.BeginUpstreamKeyRotationFinalize(r.Context(), r.PathValue("id"))
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	if err := s.secrets.Delete(r.Context(), barrier.PreviousSecretRef); err != nil && !errors.Is(err, vault.ErrNotFound) {
		_ = lifecycle.AbortUpstreamKeyRotationFinalize(r.Context(), r.PathValue("id"), barrier.Rotation.CurrentKeyVersion)
		writeError(w, http.StatusInternalServerError, "vault_error", "previous key could not be destroyed")
		return
	}
	out, err := lifecycle.CompleteUpstreamKeyRotationFinalize(r.Context(), r.PathValue("id"), barrier.Rotation.CurrentKeyVersion)
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	s.auditUpstream(r, 0, "upstream_key_rotation.finalize", "upstream_channel", r.PathValue("id"))
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListPoolRetirementJobs(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	lifecycle, ok := s.lifecycleStore(w)
	if !ok {
		return
	}
	jobs, err := lifecycle.ListPoolRetirementJobs(r.Context(), r.URL.Query().Get("pool_id"))
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	if jobs == nil {
		jobs = []contracts.PoolRetirementJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}
func (s *Server) handleCreatePoolRetirementJob(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	lifecycle, ok := s.lifecycleStore(w)
	if !ok {
		return
	}
	job, err := lifecycle.CreatePoolRetirementJob(r.Context(), r.PathValue("id"), currentUser(r).ID)
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	s.auditUpstream(r, 0, "upstream_pool.retirement_create", "upstream_pool", r.PathValue("id"))
	writeJSON(w, http.StatusCreated, job)
}
func (s *Server) handleRunPoolRetirementJob(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.publish == nil {
		writeError(w, http.StatusServiceUnavailable, "publish_unavailable", "publish engine not configured")
		return
	}
	lifecycle, ok := s.lifecycleStore(w)
	if !ok {
		return
	}
	jobID := r.PathValue("id")
	job, err := retirement.New(lifecycle, s.publish).RunJob(
		contracts.WithReconcileTrigger(actorCtx(r), contracts.ReconcileTriggerManual), jobID,
	)
	if err != nil && job.ID == "" {
		writeLifecycleError(w, err)
		return
	}
	s.auditUpstream(r, 0, "upstream_pool.retirement_run", "pool_retirement_job", jobID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": "retirement_partial", "message": err.Error(), "job": job})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func writeLifecycleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, store.ErrDuplicate):
		writeError(w, http.StatusConflict, "duplicate", err.Error())
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "lifecycle_conflict", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
	}
}
