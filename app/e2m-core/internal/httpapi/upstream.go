package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/publish"
	"e2m.local/core/internal/store"
)

// ReconcileEngine is the publish/reconcile surface the HTTP layer needs. It is
// satisfied by *publish.Engine and kept as an interface so the server stays
// decoupled and testable.
type ReconcileEngine interface {
	Plan(ctx context.Context, planID string) (contracts.ReconcilePlan, error)
	PlanScheduling(ctx context.Context, planID string, desired map[string]bool) (contracts.ReconcilePlan, error)
	Apply(ctx context.Context, planID string) (contracts.ReconcilePlan, error)
	// Rollback suspends the plan and reconciles it (drains scheduling) in the
	// engine's execution layer, so the run history captures rollbacks too.
	Rollback(ctx context.Context, planID string) (contracts.ReconcilePlan, error)
}

// --- upstream pools (platform-curated catalog; platform admin only) ---

func (s *Server) handleListUpstreamPools(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	pools, err := s.store.ListUpstreamPools(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if pools == nil {
		pools = []contracts.UpstreamPool{}
	}
	writeJSON(w, http.StatusOK, pools)
}

func (s *Server) handleCreateUpstreamPool(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	var input contracts.UpstreamPool
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(input.Name) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "name is required")
		return
	}
	if msg := validPoolStatus(input.Status); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	// New catalog pools start closed. Activation is a separate operator action
	// after stock admission and rollout targeting are configured.
	input.Status = contracts.UpstreamPoolMaintenance
	pool, err := s.store.CreateUpstreamPool(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.auditUpstream(r, 0, "upstream_pool.create", "upstream_pool", pool.ID)
	writeJSON(w, http.StatusCreated, pool)
}

func (s *Server) handleUpdateUpstreamPool(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	existing, err := s.store.GetUpstreamPool(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "upstream pool not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	var input contracts.UpstreamPool
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	input.ID = r.PathValue("id")
	// Safety stock is managed by the dedicated lifecycle endpoint. Older
	// catalog forms do not submit it, so an ordinary metadata edit must retain
	// the persisted threshold rather than silently resetting it to zero.
	input.SafetyStockThreshold = existing.SafetyStockThreshold
	if strings.TrimSpace(input.Name) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "name is required")
		return
	}
	if msg := validPoolStatus(input.Status); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	pool, err := s.store.UpdateUpstreamPool(r.Context(), input)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "upstream pool not found")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "pool_lifecycle_conflict", "pool lifecycle is controlled by an active retirement job")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.auditUpstream(r, 0, "upstream_pool.update", "upstream_pool", pool.ID)
	writeJSON(w, http.StatusOK, pool)
}

// --- upstream channels (contain Connector-local binding IDs; platform admin only) ---

func (s *Server) handleListUpstreamChannels(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	channels, err := s.store.ListUpstreamChannels(r.Context(), r.URL.Query().Get("pool_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if channels == nil {
		channels = []contracts.UpstreamChannel{}
	}
	writeJSON(w, http.StatusOK, channels)
}

func (s *Server) handleCreateUpstreamChannel(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	var input contracts.UpstreamChannel
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(input.PoolID) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "pool_id is required")
		return
	}
	input.SourceID = strings.TrimSpace(input.SourceID)
	if input.SourceID == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "source_id is required for a new upstream key")
		return
	}
	if input.SourceID != "" && !contracts.IsUpstreamSourceIdentity(input.SourceID) {
		writeError(w, http.StatusBadRequest, "validation_failed", "source_id must be a short opaque identifier")
		return
	}
	input.AccountOwnership = input.AccountOwnership.Normalize()
	if !input.AccountOwnership.Valid() {
		writeError(w, http.StatusBadRequest, "validation_failed", "account_ownership must be platform_managed or owner_provided")
		return
	}
	if msg := validChannelStatus(input.Status); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	// A newly entered key is inventory, not immediately schedulable supply.
	input.Status = contracts.UpstreamChannelMaintenance
	input.InventoryState = contracts.UpstreamInventoryDraft
	if msg := validateChannelProbeScope(input); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	// The pool must exist so a channel is never orphaned.
	_, err := s.store.GetUpstreamPool(r.Context(), input.PoolID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "validation_failed", "pool_id does not exist")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	channel, err := s.store.CreateUpstreamChannel(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.auditUpstream(r, 0, "upstream_channel.create", "upstream_channel", channel.ID)
	writeJSON(w, http.StatusCreated, channel)
}

func (s *Server) handleUpdateUpstreamChannel(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	existing, err := s.store.GetUpstreamChannel(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "upstream channel not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	var input contracts.UpstreamChannel
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	input.ID = r.PathValue("id")
	// Inventory admission has its own guarded transition endpoint and is not an
	// editable catalog field. Preserve it across legacy ordinary updates.
	input.InventoryState = existing.InventoryState
	if input.AccountOwnership == "" {
		input.AccountOwnership = existing.AccountOwnership.Normalize()
	} else {
		input.AccountOwnership = input.AccountOwnership.Normalize()
	}
	if input.AccountOwnership != existing.AccountOwnership.Normalize() {
		writeError(w, http.StatusConflict, "account_ownership_locked", "account_ownership cannot change after channel creation")
		return
	}
	input.SourceID = strings.TrimSpace(input.SourceID)
	if input.SourceID == "" {
		// Older clients did not know source_id. Preserve the catalog identity
		// instead of silently turning a grouped key into an independent source.
		input.SourceID = existing.SourceID
	}
	if !contracts.IsUpstreamSourceIdentity(input.SourceID) {
		writeError(w, http.StatusBadRequest, "validation_failed", "source_id must be a short opaque identifier")
		return
	}
	if strings.TrimSpace(input.PoolID) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "pool_id is required")
		return
	}
	if msg := validChannelStatus(input.Status); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	if msg := validateChannelProbeScope(input); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	_, err = s.store.GetUpstreamPool(r.Context(), input.PoolID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "validation_failed", "pool_id does not exist")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	channel, err := s.store.UpdateUpstreamChannel(r.Context(), input)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "upstream channel not found")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "source_identity_locked", "source_id cannot change after this key has been allocated")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.auditUpstream(r, 0, "upstream_channel.update", "upstream_channel", channel.ID)
	writeJSON(w, http.StatusOK, channel)
}

func validateChannelProbeScope(input contracts.UpstreamChannel) string {
	if input.ProbeCapability == "" && strings.TrimSpace(input.ProbeEndpointPath) == "" {
		return ""
	}
	if !contracts.IsQualityProbeCapability(input.ProbeCapability) || !contracts.IsQualityProbeEndpointPath(input.ProbeEndpointPath) {
		return "probe_capability and probe_endpoint_path must identify a supported probe scope"
	}
	return ""
}

// --- route plans (platform-managed detail; platform admin only) ---

func (s *Server) handleListRoutePlans(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	userID, ok := s.scopeOwnerUser(w, r, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	plans, err := s.store.ListRoutePlans(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if plans == nil {
		plans = []contracts.RoutePlan{}
	}
	writeJSON(w, http.StatusOK, plans)
}

func (s *Server) handleCreateRoutePlan(w http.ResponseWriter, r *http.Request) {
	// Publishing a managed plan onto an owner's instance is a platform action.
	if !requirePlatformAdmin(w, r) {
		return
	}
	var input contracts.RoutePlan
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(input.InstanceID) == "" || strings.TrimSpace(input.PoolID) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "instance_id and pool_id are required")
		return
	}
	if msg := validPlanStatus(input.Status); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	if input.Status == contracts.RoutePlanPublished {
		writeError(w, http.StatusConflict, "apply_required", "a route plan can be published only by a successful reconcile apply")
		return
	}
	if input.MaxChannels < 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "max_channels must be >= 0")
		return
	}
	if msg := validRollout(input); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	// Resolve the target instance so the plan is user-consistent and the pool
	// exists before we persist a desired state.
	inst, err := s.store.GetInstance(r.Context(), input.InstanceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "validation_failed", "instance_id does not exist")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if _, ok := s.enabledUserWithRole(w, r, inst.UserID, contracts.UserRoleClient, "owner"); !ok {
		return
	}
	pool, err := s.store.GetUpstreamPool(r.Context(), input.PoolID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "validation_failed", "pool_id does not exist")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if pool.Status != contracts.UpstreamPoolActive {
		writeError(w, http.StatusConflict, "pool_inactive", "a route plan can be created only for an active pool")
		return
	}
	// The plan's owner is always the instance's owner user, regardless of
	// any client-supplied value.
	input.UserID = inst.UserID
	plan, err := s.store.CreateRoutePlan(r.Context(), input)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			writeError(w, http.StatusConflict, "route_plan_exists", "this instance already has a route plan for the pool")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "pool_inactive", "the pool entered maintenance; route plan creation was rejected")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.auditUpstream(r, plan.UserID, "route_plan.create", "route_plan", plan.ID)
	writeJSON(w, http.StatusCreated, plan)
}

func (s *Server) handleUpdateRoutePlan(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	existing, err := s.store.GetRoutePlan(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "route plan not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	var input contracts.RoutePlan
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if msg := validPlanStatus(input.Status); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	if input.Status == contracts.RoutePlanPublished && existing.Status != contracts.RoutePlanPublished {
		writeError(w, http.StatusConflict, "apply_required", "a route plan can be published only by a successful reconcile apply")
		return
	}
	if input.MaxChannels < 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "max_channels must be >= 0")
		return
	}
	if msg := validRollout(input); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	// Identity fields (id/user/instance/pool) are immutable via update; only
	// tier/status/max_channels/rollout/labels change. Re-binding requires a new
	// plan.
	input.ID = existing.ID
	input.UserID = existing.UserID
	input.InstanceID = existing.InstanceID
	input.PoolID = existing.PoolID
	input.SchedulingGeneration = existing.SchedulingGeneration
	plan, err := s.store.UpdateRoutePlan(r.Context(), input)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "route plan not found")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "scheduling_conflict", "route plan scheduling changed; reload and retry")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.auditUpstream(r, plan.UserID, "route_plan.update", "route_plan", plan.ID)
	writeJSON(w, http.StatusOK, plan)
}

// handleReconcileRoutePlan runs the publish engine for one plan. dry-run (query
// ?dry_run=true, the default) returns the diff without mutating; apply executes
// the safe subset. This detailed managed-delivery surface is platform-admin
// only; owners receive anonymous service outcomes from /owner/pool-health.
func (s *Server) handleReconcileRoutePlan(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.publish == nil {
		writeError(w, http.StatusServiceUnavailable, "publish_unavailable", "publish engine not configured")
		return
	}
	planID := r.PathValue("id")
	plan, err := s.store.GetRoutePlan(r.Context(), planID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "route plan not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	dryRun := r.URL.Query().Get("dry_run") != "false"
	ctx := contracts.WithReconcileTrigger(actorCtx(r), contracts.ReconcileTriggerManual)
	var result contracts.ReconcilePlan
	var runErr error
	if dryRun {
		result, runErr = s.publish.Plan(ctx, planID)
	} else {
		result, runErr = s.publish.Apply(ctx, planID)
	}
	if runErr != nil && !dryRun {
		if errors.Is(runErr, publish.ErrUnsupportedLifecycle) {
			// Lifecycle capability is checked across the complete diff before the
			// engine executes anything, so this is a rejected apply, not a partial
			// gateway failure.
			s.auditUpstreamResult(r, plan.UserID, "route_plan.reconcile_apply", "route_plan", planID, "rejected", runErr.Error())
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"code":    "unsupported_lifecycle",
				"message": runErr.Error(),
				"plan":    result,
			})
			return
		}
		// Apply may fail before any write succeeds or after a partial mutation.
		// Keep those outcomes distinct for operators and API clients.
		s.auditUpstreamResult(r, plan.UserID, "route_plan.reconcile_apply", "route_plan", planID, "failed", runErr.Error())
		s.notifyReconcile(plan, summarizeReconcile(result), runErr)
		code := "reconcile_failed"
		if publish.IsPartialExecution(runErr) {
			code = "reconcile_partial"
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"code":    code,
			"message": runErr.Error(),
			"plan":    result,
		})
		return
	}
	if runErr != nil {
		// dry-run failures are load/validation errors, not partial applies.
		writeError(w, http.StatusBadGateway, "reconcile_error", runErr.Error())
		return
	}
	if dryRun {
		s.auditUpstreamResult(r, plan.UserID, "route_plan.reconcile_dryrun", "route_plan", planID, "accepted", "")
	} else {
		s.auditUpstreamResult(r, plan.UserID, "route_plan.reconcile_apply", "route_plan", planID, "accepted", "")
		s.emitReconcile(plan, result)
	}
	writeJSON(w, http.StatusOK, result)
}

// handleListPublishedBindings returns the platform-private reconcile paper trail.
func (s *Server) handleListPublishedBindings(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	planID := r.URL.Query().Get("plan_id")
	if strings.TrimSpace(planID) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "plan_id is required")
		return
	}
	_, err := s.store.GetRoutePlan(r.Context(), planID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "route plan not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	bindings, err := s.store.ListPublishedBindings(r.Context(), planID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if bindings == nil {
		bindings = []contracts.PublishedBinding{}
	}
	writeJSON(w, http.StatusOK, bindings)
}

// handleRollbackRoutePlan is the one-click "pull this managed switch back"
// action: it suspends the plan and immediately reconciles, which drains every
// published channel out of scheduling on the gateway (revoke). It is a
// plan-owner/platform write that mutates managed delivery. Deprovisioning of
// the remote accounts is left to a later reconcile once the pool is retired, so
// a rollback is reversible by re-publishing.
func (s *Server) handleRollbackRoutePlan(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.publish == nil {
		writeError(w, http.StatusServiceUnavailable, "publish_unavailable", "publish engine not configured")
		return
	}
	planID := r.PathValue("id")
	plan, err := s.store.GetRoutePlan(r.Context(), planID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "route plan not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	// The engine's Rollback suspends the plan and reconciles it, recording a
	// rollback run in the unified execution layer. Reflect the suspended status
	// locally so audit/notify use the post-rollback user/instance context.
	plan.Status = contracts.RoutePlanSuspended
	ctx := contracts.WithReconcileTrigger(actorCtx(r), contracts.ReconcileTriggerManual)
	result, runErr := s.publish.Rollback(ctx, planID)
	if runErr != nil {
		if errors.Is(runErr, publish.ErrUnsupportedLifecycle) {
			s.auditUpstreamResult(r, plan.UserID, "route_plan.rollback", "route_plan", planID, "rejected", runErr.Error())
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"code":    "unsupported_lifecycle",
				"message": runErr.Error(),
				"plan":    result,
			})
			return
		}
		s.auditUpstreamResult(r, plan.UserID, "route_plan.rollback", "route_plan", planID, "failed", runErr.Error())
		s.notifyReconcile(plan, summarizeReconcile(result), runErr)
		code := "reconcile_failed"
		if publish.IsPartialExecution(runErr) {
			code = "reconcile_partial"
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"code":    code,
			"message": runErr.Error(),
			"plan":    result,
		})
		return
	}
	s.auditUpstreamResult(r, plan.UserID, "route_plan.rollback", "route_plan", planID, "accepted", "")
	s.emitReconcile(plan, result)
	writeJSON(w, http.StatusOK, result)
}

// handleListReconcileRuns returns the reconcile-run history for a plan (the
// execution log of dry-run/apply/rollback). This contains channel and gateway
// action details, so it is platform-admin only.
func (s *Server) handleListReconcileRuns(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	planID := r.URL.Query().Get("plan_id")
	if strings.TrimSpace(planID) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "plan_id is required")
		return
	}
	_, err := s.store.GetRoutePlan(r.Context(), planID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "route plan not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	limit := 100
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	runs, err := s.store.ListReconcileRuns(r.Context(), planID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if runs == nil {
		runs = []contracts.ReconcileRun{}
	}
	writeJSON(w, http.StatusOK, runs)
}

// --- helpers ---

func validPoolStatus(s contracts.UpstreamPoolStatus) string {
	switch s {
	case "", contracts.UpstreamPoolActive, contracts.UpstreamPoolMaintenance, contracts.UpstreamPoolRetired:
		return ""
	default:
		return "status must be active, maintenance, or retired"
	}
}

func validChannelStatus(s contracts.UpstreamChannelStatus) string {
	switch s {
	case "", contracts.UpstreamChannelActive, contracts.UpstreamChannelMaintenance, contracts.UpstreamChannelRetired:
		return ""
	default:
		return "status must be active, maintenance, or retired"
	}
}

func validPlanStatus(s contracts.RoutePlanStatus) string {
	switch s {
	case "", contracts.RoutePlanDraft, contracts.RoutePlanPublished, contracts.RoutePlanSuspended:
		return ""
	default:
		return "status must be draft, published, or suspended"
	}
}

// validRollout checks the gray-rollout policy on a plan.
func validRollout(p contracts.RoutePlan) string {
	switch p.Rollout {
	case "", contracts.RolloutImmediate, contracts.RolloutCanary, contracts.RolloutBatched:
	default:
		return "rollout must be immediate, canary, or batched"
	}
	if p.RolloutBatchSize < 0 {
		return "rollout_batch_size must be >= 0"
	}
	if p.RolloutCanaryCount < 0 {
		return "rollout_canary_count must be >= 0"
	}
	return ""
}

func (s *Server) auditUpstream(r *http.Request, userID int64, action, targetType, targetID string) {
	s.auditUpstreamResult(r, userID, action, targetType, targetID, "accepted", "")
}

func (s *Server) auditUpstreamResult(r *http.Request, userID int64, action, targetType, targetID, result, errMsg string) {
	actor := currentUser(r)
	if userID == 0 {
		userID = actor.ID
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:       userID,
		ActorType:    "user",
		ActorID:      actor.Email,
		Action:       action,
		RiskLevel:    contracts.RiskLevelL1,
		TargetType:   targetType,
		TargetID:     targetID,
		Result:       result,
		ErrorMessage: errMsg,
	})
}

// reconcileSummary tallies actions by type for SSE payloads and notifications.
type reconcileSummary struct {
	Total       int `json:"total"`
	Created     int `json:"created"`
	Enabled     int `json:"enabled"`
	Disabled    int `json:"disabled"`
	Revoked     int `json:"revoked"`
	Updated     int `json:"updated"`
	Deprovision int `json:"deprovisioned"`
	Held        int `json:"held"`
	Failed      int `json:"failed"`
}

func summarizeReconcile(result contracts.ReconcilePlan) reconcileSummary {
	sum := reconcileSummary{Total: len(result.Actions)}
	for _, a := range result.Actions {
		switch a.Type {
		case contracts.ReconcileCreate:
			sum.Created++
		case contracts.ReconcileEnable:
			sum.Enabled++
		case contracts.ReconcileDisable:
			sum.Disabled++
		case contracts.ReconcileRevoke:
			sum.Revoked++
		case contracts.ReconcileUpdate:
			sum.Updated++
		case contracts.ReconcileDeprovision:
			sum.Deprovision++
		case contracts.ReconcileHold:
			sum.Held++
		}
		if strings.HasPrefix(a.Detail, "error:") {
			sum.Failed++
		}
	}
	return sum
}

// emitReconcile pushes an SSE event so the owner console reflects a managed
// switch/publish in realtime (the platform-managed switching UX), and dispatches
// an operational notification over the user's routes.
func (s *Server) emitReconcile(plan contracts.RoutePlan, result contracts.ReconcilePlan) {
	sum := summarizeReconcile(result)
	if s.events != nil {
		s.events.Publish(StreamEvent{
			Type:    "upstream.reconcile",
			UserID:  plan.UserID,
			Payload: map[string]any{"summary": sum},
		})
	}
	s.notifyReconcile(plan, sum, nil)
}

// notifyReconcile announces a managed reconcile over the user's notification
// routes (Feishu/QQ/webhook). applyErr, when non-nil, marks a partial failure.
func (s *Server) notifyReconcile(plan contracts.RoutePlan, sum reconcileSummary, applyErr error) {
	if s.notifier == nil {
		return
	}
	// Nothing happened: do not spam a "0 changes" alert.
	if sum.Total == 0 && applyErr == nil {
		return
	}
	title := "\u2705 E2M upstream reconcile"
	if applyErr != nil {
		title = "\u274c E2M upstream reconcile (partial failure)"
	}
	text := fmt.Sprintf(
		"created=%d enabled=%d disabled=%d revoked=%d held=%d failed=%d",
		sum.Created, sum.Enabled, sum.Disabled, sum.Revoked, sum.Held, sum.Failed)
	if applyErr != nil {
		text += "\n" + applyErr.Error()
	}
	risk := contracts.RiskLevelL1
	level := contracts.EventLevelNotice
	result := "accepted"
	if applyErr != nil {
		risk = contracts.RiskLevelL2
		level = contracts.EventLevelWarning
		result = "failed"
	}
	s.notifier.Dispatch(context.Background(), plan.UserID, notify.Event{
		UserID:     plan.UserID,
		EventLevel: level,
		RiskLevel:  risk,
		Result:     result,
		Title:      title,
		Text:       text,
		Fields: map[string]string{
			"created":  itoaSummary(sum.Created),
			"enabled":  itoaSummary(sum.Enabled),
			"disabled": itoaSummary(sum.Disabled),
			"revoked":  itoaSummary(sum.Revoked),
			"held":     itoaSummary(sum.Held),
			"failed":   itoaSummary(sum.Failed),
		},
	})
}

func itoaSummary(n int) string { return strconv.Itoa(n) }
