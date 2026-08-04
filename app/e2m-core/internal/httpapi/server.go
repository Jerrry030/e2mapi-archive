package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/auth"
	"e2m.local/core/internal/keyproof"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/orchestrator"
	"e2m.local/core/internal/pricing"
	"e2m.local/core/internal/recommendationrollout"
	"e2m.local/core/internal/settings"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/vault"
	"e2m.local/core/internal/webui"
)

// HealthSource exposes the health checker's latest snapshots to the API without
// coupling the HTTP layer to the checker's internals.
type HealthSource interface {
	Snapshots(instanceID string) []contracts.InstanceHealthSnapshot
}

// BillingSource computes a user's statement for a YYYY-MM period.
type BillingSource interface {
	Statement(ctx context.Context, userID int64, period string) (contracts.BillingStatement, error)
}

// ApprovalEngine gates L2/L3 actions on a human decision.
type ApprovalEngine interface {
	Submit(ctx context.Context, req contracts.ApprovalRequest) (contracts.ApprovalRequest, error)
	Approve(ctx context.Context, id, decidedBy string) (contracts.ApprovalRequest, error)
	Reject(ctx context.Context, id, decidedBy, note string) (contracts.ApprovalRequest, error)
}

// Server exposes the E2M Core API over HTTP. It depends on the store.Store
// interface, not a concrete backend, so memory and PostgreSQL are interchangeable.
type Server struct {
	store                              store.Store
	orch                               *orchestrator.Orchestrator
	health                             HealthSource
	billing                            BillingSource
	approval                           ApprovalEngine
	auth                               *auth.Service
	events                             *EventBus
	publish                            ReconcileEngine
	notifier                           ReconcileNotifier
	autoswitch                         AutoSwitchController
	recommendationRollouts             RecommendationRolloutController
	quality                            QualityObservationRecorder
	costObservations                   store.UpstreamCostObservationStore
	pricing                            *pricing.Service
	settings                           *settings.Service
	secrets                            vault.Vault
	notificationRouter                 *notify.Router
	notificationTests                  notificationTestLimiter
	notificationTargetsMu              sync.Mutex
	intelligenceCursorMu               sync.RWMutex
	intelligenceCursorActiveKID        string
	intelligenceCursorKeys             map[string][32]byte
	paymentMu                          sync.Mutex
	redeemLimiterMu                    sync.Mutex
	redeemFailures                     map[int64][]time.Time
	keyProof                           DeliveryKeyVerifier
	hybridBindings                     HybridBindingProvisioner
	publicBaseURL                      string
	corsDev                            bool
	revealLimiter                      assignedKeyRevealLimiter
	allowAssignedKeyReveal             bool
	allowInsecureSupplyUpstreams       bool
	businessFeatures                   BusinessFeatureFlags
	upstreamIntelligenceIngestCapacity store.UpstreamIntelligenceIngestCapacityLimit
}

type assignedKeyRevealAttempt struct {
	Failures    int
	WindowStart time.Time
	BlockedTill time.Time
}

type assignedKeyRevealLimiter struct {
	mu       sync.Mutex
	attempts map[string]assignedKeyRevealAttempt
}

// AutoSwitchController is the health-driven auto-switch surface the HTTP layer
// drives for on-demand admin actions (evaluate a plan now, advance an observing
// decision now). It is satisfied by *autoswitch.Orchestrator and kept as an
// interface so the server stays decoupled and testable. Optional: when nil, the
// admin action endpoints report the controller is disabled.
type AutoSwitchController interface {
	Evaluate(ctx context.Context, planID string) (*contracts.AutoSwitchDecision, error)
	Observe(ctx context.Context, decisionID string) (*contracts.AutoSwitchDecision, error)
}

// RecommendationRolloutController is the narrow, owner-scoped staged rollout
// surface exposed to HTTP. Gateway account identities and rollback weights are
// deliberately absent from this boundary's request methods: the controller
// reconstructs them from trusted Core and Connector state.
type RecommendationRolloutController interface {
	List(context.Context, contracts.RecommendationRolloutFilter) ([]contracts.RecommendationRollout, error)
	Get(context.Context, string) (contracts.RecommendationRollout, []contracts.RecommendationRolloutOperation, error)
	Start(context.Context, int64, string) (recommendationrollout.MutationResult, error)
	Advance(context.Context, int64, string) (recommendationrollout.MutationResult, error)
	Rollback(context.Context, int64, string) (recommendationrollout.MutationResult, error)
}

// SourceQualityCohortReader exposes the controller's exact read-only rollout
// decision to the admin summary. Keeping it optional preserves deployments
// where the background auto-switch controller is not wired.
type SourceQualityCohortReader interface {
	SourceQualityCohort(ctx context.Context, sourceID string, percentage int) (map[string]bool, bool)
}

// QualityObservationRecorder is the append-only intake used by authenticated
// connectors. Aggregation remains asynchronous so request telemetry stays
// inexpensive and cannot block the downstream request path on a pool sweep.
type QualityObservationRecorder interface {
	RecordObservation(ctx context.Context, obs contracts.ChannelObservation) (contracts.ChannelObservation, error)
}

type DeliveryKeyVerifier interface {
	Verify(context.Context, string, string) (keyproof.Verification, error)
}

// HybridBindingProvisioner installs an E2M virtual key into the owner's
// Connector and materializes the matching aggregate NewAPI account. The HTTP
// layer owns authorization; the provisioner owns the secret-safe side effect.
type HybridBindingProvisioner interface {
	Apply(context.Context, int64, string, contracts.ResourceClass) (contracts.HybridGatewayBinding, error)
}

type HealthController interface {
	HealthSource
	CheckNow(ctx context.Context, instanceID string) (contracts.InstanceHealthSnapshot, error)
}

// ReconcileNotifier dispatches an operational alert over a user's configured
// notification routes. It is satisfied by *notify.Router (via a thin adapter)
// and kept as an interface so the HTTP layer stays decoupled and testable.
type ReconcileNotifier interface {
	Dispatch(ctx context.Context, userID int64, ev notify.Event)
}

type createInstanceRequest struct {
	UserID int64                  `json:"user_id"`
	Name   string                 `json:"name"`
	Kind   contracts.InstanceKind `json:"kind"`
}

type instanceResponse struct {
	contracts.Instance
	ConnectorInstall *contracts.CreateConnectorEnrollmentResponse `json:"connector_install,omitempty"`
}

var allowedInstanceRequestFields = map[string]struct{}{
	"user_id": {},
	"name":    {},
	"kind":    {},
}

func NewServer(st store.Store, orch *orchestrator.Orchestrator, health HealthSource, billing BillingSource, approvals ApprovalEngine, authSvc *auth.Service, events *EventBus, publisher ReconcileEngine) *Server {
	return &Server{
		store: st, orch: orch, health: health, billing: billing, approval: approvals, auth: authSvc, events: events, publish: publisher,
		upstreamIntelligenceIngestCapacity: store.NormalizeUpstreamIntelligenceIngestCapacityLimit(store.UpstreamIntelligenceIngestCapacityLimit{}),
	}
}

// SetUpstreamIntelligenceIngestCapacity configures the durable owner-scoped
// fixed-window admission gate. Invalid limits are normalized to conservative
// defaults so a malformed environment value cannot silently disable it.
func (s *Server) SetUpstreamIntelligenceIngestCapacity(limit store.UpstreamIntelligenceIngestCapacityLimit) {
	s.upstreamIntelligenceIngestCapacity = store.NormalizeUpstreamIntelligenceIngestCapacityLimit(limit)
}

// SetVault exposes the same credential boundary used by adapters to the
// authenticated console, so business-role users can write allowed refs without
// plaintext ever touching the database.
func (s *Server) SetVault(v vault.Vault) { s.secrets = v }

func (s *Server) SetDeliveryKeyVerifier(verifier DeliveryKeyVerifier) { s.keyProof = verifier }

func (s *Server) SetHybridBindingProvisioner(provisioner HybridBindingProvisioner) {
	s.hybridBindings = provisioner
}

// SetNotifier wires a notification dispatcher so reconcile applies announce
// managed switches over Feishu/QQ/webhook. Optional: nil disables reconcile
// notifications.
func (s *Server) SetNotifier(n ReconcileNotifier) { s.notifier = n }

// SetNotificationRouter wires durable test enqueue and system channel status.
func (s *Server) SetNotificationRouter(router *notify.Router) { s.notificationRouter = router }

// SetAutoSwitchController wires the health-driven auto-switch orchestrator so the
// console can trigger an on-demand evaluate/observe. Optional: nil disables those
// admin actions (the background runner still operates independently).
func (s *Server) SetAutoSwitchController(c AutoSwitchController) { s.autoswitch = c }

// SetRecommendationRolloutController wires the UI-16 staged rollout control
// plane. The background worker is wired separately and remains able to restore
// an exact baseline even when forward auto-apply is disabled.
func (s *Server) SetRecommendationRolloutController(c RecommendationRolloutController) {
	s.recommendationRollouts = c
}

// SetQualityObservationRecorder wires the health-metrics service used by the
// connector telemetry endpoint.
func (s *Server) SetQualityObservationRecorder(r QualityObservationRecorder) { s.quality = r }

// SetUpstreamCostObservationStore enables atomic quality-fact plus sanitized
// cost-attribution outbox intake. If it is unavailable, presence-aware cost
// usage fails closed instead of being silently dropped.
func (s *Server) SetUpstreamCostObservationStore(st store.UpstreamCostObservationStore) {
	s.costObservations = st
}

// SetPublicBaseURL sets the externally reachable Core origin used in generated
// connector install commands. Empty means infer the origin from the request.
func (s *Server) SetPublicBaseURL(url string) {
	s.publicBaseURL = strings.TrimRight(strings.TrimSpace(url), "/")
}

// EnableDevCORS allows cross-origin API calls from the Vite dev server. It must
// stay off in production, where the console is served same-origin.
func (s *Server) EnableDevCORS() { s.corsDev = true }

// EnableInsecureSupplyUpstreams permits explicitly marked HTTP upstreams for
// local development. Production deployments must leave this disabled.
func (s *Server) EnableInsecureSupplyUpstreams() { s.allowInsecureSupplyUpstreams = true }

func (s *Server) Routes() http.Handler {
	// Core owns both the Connector control plane and E2M's native platform
	// distribution management APIs. OpenAI-compatible traffic is mounted by the
	// process entrypoint so both surfaces still share one E2M identity/store.
	public := http.NewServeMux()
	public.HandleFunc("GET /healthz", s.handleHealth)
	public.HandleFunc("GET /api/v1/auth/public-config", s.handlePublicAuthConfig)
	public.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	public.HandleFunc("POST /api/v1/auth/register", s.handleRegister)
	public.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	public.HandleFunc("POST /api/v1/connectors/enroll", s.handleEnrollConnector)
	public.HandleFunc("POST /api/v1/connectors/tasks/lease", s.handleLeaseConnectorTasks)
	public.HandleFunc("POST /api/v1/connectors/tasks/{id}/execute", s.handleBeginConnectorTaskExecution)
	public.HandleFunc("POST /api/v1/connectors/tasks/{id}/complete", s.handleCompleteConnectorTask)
	// Provider callbacks authenticate by signature, not session; the business
	// feature gate still fail-closes these paths unless payments are enabled.
	// EasyPay gateways deliver the notify as GET or POST depending on operator
	// configuration, so both methods are accepted.
	public.HandleFunc("POST /api/v1/payment/webhooks/stripe/{providerId}", s.handleStripeWebhook)
	public.HandleFunc("GET /api/v1/payment/webhooks/easypay/{providerId}", s.handleEasyPayWebhook)
	public.HandleFunc("POST /api/v1/payment/webhooks/easypay/{providerId}", s.handleEasyPayWebhook)

	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/auth/me", s.handleMe)
	api.HandleFunc("GET /api/v1/system/auth-settings", s.handleGetAuthSystemSettings)
	api.HandleFunc("PUT /api/v1/system/auth-settings", s.handleUpdateAuthSystemSettings)
	api.HandleFunc("GET /api/v1/admin/settings/commerce", s.handleGetCommerceSettings)
	api.HandleFunc("PUT /api/v1/admin/settings/commerce", s.handleUpdateCommerceSettings)
	api.HandleFunc("GET /api/v1/users", s.handleListUsers)
	api.HandleFunc("POST /api/v1/users", s.handleCreateUser)
	api.HandleFunc("GET /api/v1/users/{id}", s.handleGetUser)
	api.HandleFunc("PUT /api/v1/users/{id}", s.handleUpdateUser)
	api.HandleFunc("POST /api/v1/users/{id}/reset-password", s.handleResetUserPassword)
	api.HandleFunc("GET /api/v1/instances", s.handleListInstances)
	api.HandleFunc("POST /api/v1/instances", s.handleCreateInstance)
	api.HandleFunc("PUT /api/v1/instances/{id}", s.handleUpdateInstance)
	api.HandleFunc("POST /api/v1/instances/{id}/connector-install", s.handleCreateInstanceConnectorInstall)
	api.HandleFunc("GET /api/v1/health-snapshots", s.handleHealthSnapshots)
	api.HandleFunc("GET /api/v1/instances/{id}/monitor-policy", s.handleGetInstanceMonitorPolicy)
	api.HandleFunc("PUT /api/v1/instances/{id}/monitor-policy", s.handleUpdateInstanceMonitorPolicy)
	api.HandleFunc("POST /api/v1/instances/{id}/health-check", s.handleCheckInstanceHealthNow)
	api.HandleFunc("GET /api/v1/instances/{id}/accounts", s.handleListAccounts)
	api.HandleFunc("POST /api/v1/instances/{id}/accounts/switch", s.handleSwitchUpstream)
	api.HandleFunc("POST /api/v1/instances/{id}/accounts/{accountId}/schedulable", s.handleSetSchedulable)
	api.HandleFunc("GET /api/v1/connectors", s.handleListConnectors)
	api.HandleFunc("POST /api/v1/connectors/enrollments", s.handleCreateConnectorEnrollment)
	api.HandleFunc("POST /api/v1/connectors/{id}/rotate-token", s.handleRotateConnectorToken)
	api.HandleFunc("POST /api/v1/connectors/{id}/revoke", s.handleRevokeConnector)
	api.HandleFunc("PUT /api/v1/instances/{id}/connector", s.handleBindInstanceConnector)
	api.HandleFunc("GET /api/v1/connector-tasks", s.handleListConnectorTasks)
	api.HandleFunc("POST /api/v1/connector-tasks/{id}/resolve-execution", s.handleResolveConnectorTaskExecution)
	api.HandleFunc("GET /api/v1/adapter-capabilities", s.handleListCapabilities)
	api.HandleFunc("GET /api/v1/audits", s.handleListAudits)
	api.HandleFunc("GET /api/v1/events/stream", s.handleEventStream)
	api.HandleFunc("GET /api/v1/notification-routes", s.handleListNotificationRoutes)
	api.HandleFunc("POST /api/v1/notification-routes", s.handleCreateNotificationRoute)
	api.HandleFunc("PUT /api/v1/notification-routes/{id}", s.handleUpdateNotificationRoute)
	api.HandleFunc("DELETE /api/v1/notification-routes/{id}", s.handleDeleteNotificationRoute)
	api.HandleFunc("POST /api/v1/notification-routes/{id}/test", s.handleTestNotificationRoute)
	api.HandleFunc("GET /api/v1/notification-channels/status", s.handleNotificationChannelStatus)
	api.HandleFunc("GET /api/v1/notification-targets", s.handleListNotificationTargets)
	api.HandleFunc("PUT /api/v1/notification-targets/{channel}", s.handleUpsertNotificationTarget)
	api.HandleFunc("DELETE /api/v1/notification-targets/{channel}", s.handleDeleteNotificationTarget)
	api.HandleFunc("GET /api/v1/notification-deliveries", s.handleListNotificationDeliveries)
	api.HandleFunc("POST /api/v1/notification-deliveries/{id}/retry", s.handleRetryNotificationDelivery)
	api.HandleFunc("GET /api/v1/secrets", s.handleListSecrets)
	api.HandleFunc("POST /api/v1/secrets", s.handleUpsertSecret)
	api.HandleFunc("DELETE /api/v1/secrets", s.handleDeleteSecret)
	api.HandleFunc("GET /api/v1/admin/payment/config", s.handleGetPaymentConfig)
	api.HandleFunc("PUT /api/v1/admin/payment/config", s.handleUpdatePaymentConfig)
	api.HandleFunc("GET /api/v1/admin/payment/providers", s.handleListPaymentProviders)
	api.HandleFunc("POST /api/v1/admin/payment/providers", s.handleCreatePaymentProvider)
	api.HandleFunc("PUT /api/v1/admin/payment/providers/{id}", s.handleUpdatePaymentProvider)
	api.HandleFunc("DELETE /api/v1/admin/payment/providers/{id}", s.handleDeletePaymentProvider)
	api.HandleFunc("GET /api/v1/admin/payment/orders", s.handleListPaymentOrders)
	api.HandleFunc("GET /api/v1/admin/payment/orders/{id}", s.handleGetPaymentOrder)
	api.HandleFunc("POST /api/v1/admin/payment/orders/{id}/cancel", s.handleCancelPaymentOrder)
	api.HandleFunc("POST /api/v1/owner/hybrid-supply/recharge-orders", s.handleCreateRechargeOrder)
	api.HandleFunc("POST /api/v1/admin/redeem-codes", s.handleGenerateRedeemCodes)
	api.HandleFunc("GET /api/v1/admin/redeem-codes", s.handleListRedeemCodes)
	api.HandleFunc("POST /api/v1/admin/redeem-codes/create-and-redeem", s.handleCreateAndRedeem)
	api.HandleFunc("POST /api/v1/admin/redeem-codes/{id}/disable", s.handleDisableRedeemCode)
	api.HandleFunc("DELETE /api/v1/admin/redeem-codes/{id}", s.handleDeleteRedeemCode)
	api.HandleFunc("POST /api/v1/redeem", s.handleRedeemCode)
	s.registerPlatformDistributionRoutes(api)

	mux := http.NewServeMux()
	mux.Handle("/healthz", public)
	mux.Handle("/api/v1/auth/public-config", public)
	mux.Handle("/api/v1/auth/login", public)
	mux.Handle("/api/v1/auth/register", public)
	mux.Handle("/api/v1/auth/logout", public)
	mux.Handle("/api/v1/connectors/enroll", public)
	mux.Handle("/api/v1/connectors/tasks/lease", public)
	mux.Handle("POST /api/v1/connectors/tasks/{id}/execute", public)
	mux.Handle("POST /api/v1/connectors/tasks/{id}/complete", public)
	mux.Handle("/api/v1/payment/webhooks/", public)
	mux.Handle("/api/", s.withAuth(api))

	// The business gates run before authentication so disabled commercial
	// modules answer 404 feature_disabled on every path, including webhooks.
	apiHandler := withJSON(s.withBusinessFeatureGates(mux))
	if s.corsDev {
		apiHandler = withDevCORS(apiHandler)
	}

	spa, _ := webui.Handler()

	// API/health routes are JSON; everything else serves the console SPA.
	root := http.NewServeMux()
	root.Handle("/healthz", apiHandler)
	root.Handle("/api/", apiHandler)
	root.Handle("/", spa)
	return root
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"service":    "e2m-core",
		"serverTime": time.Now().UTC(),
	})
}

func (s *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.scopeOwnerUser(w, r, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	instances, err := s.store.ListInstances(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, instances)
}

func (s *Server) handleCreateInstance(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	var req createInstanceRequest
	if !decodeInstanceRequest(w, r, &req) {
		return
	}
	input := contracts.Instance{
		UserID: req.UserID,
		Name:   strings.TrimSpace(req.Name),
		Kind:   req.Kind,
	}
	if strings.TrimSpace(input.Name) == "" || !validInstanceKind(input.Kind) {
		writeError(w, http.StatusBadRequest, "validation_failed", "name and kind are required")
		return
	}
	if !auth.IsPlatformAdmin(currentUser(r)) {
		input.UserID = currentUser(r).ID
	}
	if input.UserID == 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id is required")
		return
	}
	if !s.requireOwnerWrite(w, r, input.UserID) {
		return
	}
	if _, ok := s.enabledUserWithRole(w, r, input.UserID, contracts.UserRoleClient, "owner"); !ok {
		return
	}

	instance, err := s.store.CreateInstance(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	actor := currentUser(r)
	if _, err := s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     instance.UserID,
		InstanceID: instance.ID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "instance.create",
		RiskLevel:  contracts.RiskLevelL0,
		TargetType: "instance",
		TargetID:   instance.ID,
		Result:     "accepted",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	var install *contracts.CreateConnectorEnrollmentResponse
	if created, err := s.createConnectorEnrollmentForInstance(r, instance, actor.Email); err == nil {
		install = &created
	} else if errors.Is(err, store.ErrDuplicate) || errors.Is(err, store.ErrConflict) {
		// A deterministic connector id may collide if the caller retries while an
		// unused install token still exists. The instance itself is valid; the user
		// can regenerate from the instance row.
	} else {
		writeError(w, http.StatusInternalServerError, "connector_install_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, instanceResponse{Instance: instance, ConnectorInstall: install})
}

func (s *Server) handleUpdateInstance(w http.ResponseWriter, r *http.Request) {
	existing, ok := s.instanceForWrite(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var req createInstanceRequest
	if !decodeInstanceRequest(w, r, &req) {
		return
	}
	updated := existing
	if name := strings.TrimSpace(req.Name); name != "" {
		updated.Name = name
	}
	if req.Kind != "" {
		updated.Kind = req.Kind
	}
	if strings.TrimSpace(updated.Name) == "" || !validInstanceKind(updated.Kind) {
		writeError(w, http.StatusBadRequest, "validation_failed", "name and kind are required")
		return
	}
	saved, err := s.store.UpdateInstance(r.Context(), updated)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "instance not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     saved.UserID,
		InstanceID: saved.ID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "instance.update",
		RiskLevel:  contracts.RiskLevelL1,
		TargetType: "instance",
		TargetID:   saved.ID,
		Result:     "accepted",
	})
	writeJSON(w, http.StatusOK, saved)
}

func decodeInstanceRequest(w http.ResponseWriter, r *http.Request, out *createInstanceRequest) bool {
	var raw map[string]json.RawMessage
	if err := decodeStrictJSON(r, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return false
	}
	for field := range raw {
		if _, ok := allowedInstanceRequestFields[field]; !ok {
			writeError(w, http.StatusBadRequest, "validation_failed", "unsupported or local-only instance fields are not accepted")
			return false
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return false
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return false
	}
	return true
}

func (s *Server) handleCreateInstanceConnectorInstall(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	inst, ok := s.instanceForWrite(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	actor := currentUser(r)
	result, err := s.createConnectorEnrollmentForInstance(r, inst, actor.Email)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) || errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "duplicate_connector", "connector install token already exists; wait for it to expire or use the connector page")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.scopeOwnerUser(w, r, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	status := contracts.ApprovalStatus(r.URL.Query().Get("status"))
	items, err := s.store.ListApprovals(r.Context(), userID, status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !auth.IsPlatformAdmin(currentUser(r)) {
		instanceIDs := map[string]struct{}{}
		for _, item := range items {
			if item.InstanceID != "" {
				instanceIDs[item.InstanceID] = struct{}{}
			}
		}
		managed, err := s.managedRemoteIDs(r.Context(), instanceIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", "managed account classification is temporarily unavailable")
			return
		}
		for i := range items {
			items[i] = filterManagedApproval(items[i], managed[items[i].InstanceID])
		}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleSubmitApproval(w http.ResponseWriter, r *http.Request) {
	var input contracts.ApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(input.InstanceID) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "instance_id is required")
		return
	}
	// Submitting an approval is a user-scoped write on the target instance.
	if _, ok := s.instanceForWrite(w, r, input.InstanceID); !ok {
		return
	}
	user := currentUser(r)
	input.RequestedBy = user.Email
	ap, err := s.approval.Submit(actorCtx(r), input)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "instance not found")
			return
		}
		if errors.Is(err, orchestrator.ErrManagedAccountSchedulingFence) {
			writeError(w, http.StatusConflict, "managed_account_requires_route_plan", "managed account scheduling must be changed through its route plan")
			return
		}
		writeError(w, http.StatusBadRequest, "approval_error", err.Error())
		return
	}
	ap, err = s.approvalForActor(r.Context(), ap, auth.IsPlatformAdmin(currentUser(r)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "managed account classification is temporarily unavailable")
		return
	}
	writeJSON(w, http.StatusCreated, ap)
}

func (s *Server) handleApproveApproval(w http.ResponseWriter, r *http.Request) {
	ap, err := s.store.GetApproval(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "approval not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !s.requireOwnerWrite(w, r, ap.UserID) {
		return
	}
	user := currentUser(r)
	var input struct {
		DecidedBy string `json:"decided_by"`
	}
	_ = json.NewDecoder(r.Body).Decode(&input)
	input.DecidedBy = user.Email
	decided, err := s.approval.Approve(actorCtx(r), ap.ID, input.DecidedBy)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "approval not found")
			return
		}
		writeError(w, http.StatusConflict, "approval_error", err.Error())
		return
	}
	decided, err = s.approvalForActor(r.Context(), decided, auth.IsPlatformAdmin(currentUser(r)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "managed account classification is temporarily unavailable")
		return
	}
	writeJSON(w, http.StatusOK, decided)
}

func (s *Server) handleRejectApproval(w http.ResponseWriter, r *http.Request) {
	ap, err := s.store.GetApproval(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "approval not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !s.requireOwnerWrite(w, r, ap.UserID) {
		return
	}
	user := currentUser(r)
	var input struct {
		DecidedBy string `json:"decided_by"`
		Note      string `json:"note"`
	}
	_ = json.NewDecoder(r.Body).Decode(&input)
	input.DecidedBy = user.Email
	decided, err := s.approval.Reject(actorCtx(r), ap.ID, input.DecidedBy, input.Note)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "approval not found")
			return
		}
		writeError(w, http.StatusConflict, "approval_error", err.Error())
		return
	}
	decided, err = s.approvalForActor(r.Context(), decided, auth.IsPlatformAdmin(currentUser(r)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "managed account classification is temporarily unavailable")
		return
	}
	writeJSON(w, http.StatusOK, decided)
}

func (s *Server) handleBillingStatement(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseOptionalUserID(w, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	period := r.URL.Query().Get("period")
	if userID == 0 || strings.TrimSpace(period) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id and period (YYYY-MM) are required")
		return
	}
	if !s.requireOwnerRead(w, r, userID) {
		return
	}
	if s.billing == nil {
		writeError(w, http.StatusServiceUnavailable, "billing_unavailable", "billing calculator not configured")
		return
	}
	st, err := s.billing.Statement(r.Context(), userID, period)
	if err != nil {
		writeError(w, http.StatusBadRequest, "billing_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleHealthSnapshots(w http.ResponseWriter, r *http.Request) {
	instanceID := r.URL.Query().Get("instance_id")
	snaps := []contracts.InstanceHealthSnapshot{}
	if s.health != nil {
		snaps = s.health.Snapshots(instanceID)
	}
	// Only owner-role users see owner-side instance health for their account.
	user := currentUser(r)
	if !auth.IsPlatformAdmin(user) {
		if !auth.IsOwner(user) {
			writeError(w, http.StatusForbidden, "forbidden", "owner role required for this user")
			return
		}
		scoped := snaps[:0]
		for _, sn := range snaps {
			if auth.CanReadOwnerUser(user, sn.UserID) {
				scoped = append(scoped, sn)
			}
		}
		snaps = scoped
		instanceIDs := map[string]struct{}{}
		for _, snapshot := range snaps {
			if snapshot.InstanceID != "" {
				instanceIDs[snapshot.InstanceID] = struct{}{}
			}
		}
		managed, err := s.managedRemoteIDs(r.Context(), instanceIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", "managed account classification is temporarily unavailable")
			return
		}
		for i := range snaps {
			snaps[i] = filterManagedHealthSnapshot(snaps[i], managed[snaps[i].InstanceID])
		}
	}
	writeJSON(w, http.StatusOK, snaps)
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.instanceForRead(w, r, id); !ok {
		return
	}
	accounts, err := s.orch.ListAccounts(r.Context(), id)
	if err != nil {
		s.writeOrchError(w, err)
		return
	}
	if !auth.IsPlatformAdmin(currentUser(r)) {
		managed, err := s.managedRemoteIDs(r.Context(), instanceIDSet(id))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", "managed account classification is temporarily unavailable")
			return
		}
		accounts = filterManagedGatewayAccounts(accounts, managed[id])
	}
	if accounts == nil {
		accounts = []contracts.GatewayAccount{}
	}
	writeJSON(w, http.StatusOK, accounts)
}

func (s *Server) handleSetSchedulable(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	accountID := strings.TrimSpace(r.PathValue("accountId"))
	inst, ok := s.instanceForWrite(w, r, id)
	if !ok {
		return
	}
	if !validGatewayAccountID(accountID) {
		writeError(w, http.StatusBadRequest, "validation_failed", "account_id is invalid")
		return
	}
	var input struct {
		Schedulable bool   `json:"schedulable"`
		Reason      string `json:"reason"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if len(input.Reason) > 256 {
		writeError(w, http.StatusBadRequest, "validation_failed", "reason is too long")
		return
	}
	if managed, err := s.managedGatewayAccount(r.Context(), inst, accountID); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	} else if managed {
		writeError(w, http.StatusConflict, "managed_account_requires_route_plan", "managed account scheduling must be changed through its route plan")
		return
	}
	if err := s.orch.SetSchedulable(actorCtx(r), id, accountID, input.Schedulable, input.Reason); err != nil {
		s.writeOrchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSwitchUpstream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inst, ok := s.instanceForWrite(w, r, id)
	if !ok {
		return
	}
	var input struct {
		DisableAccountID string `json:"disable_account_id"`
		EnableAccountID  string `json:"enable_account_id"`
		Reason           string `json:"reason"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	input.DisableAccountID = strings.TrimSpace(input.DisableAccountID)
	input.EnableAccountID = strings.TrimSpace(input.EnableAccountID)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.DisableAccountID == "" && input.EnableAccountID == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "disable_account_id or enable_account_id is required")
		return
	}
	if input.DisableAccountID != "" && !validGatewayAccountID(input.DisableAccountID) ||
		input.EnableAccountID != "" && !validGatewayAccountID(input.EnableAccountID) {
		writeError(w, http.StatusBadRequest, "validation_failed", "account id is invalid")
		return
	}
	if input.DisableAccountID != "" && input.DisableAccountID == input.EnableAccountID {
		writeError(w, http.StatusBadRequest, "validation_failed", "account ids must be different")
		return
	}
	if len(input.Reason) > 256 {
		writeError(w, http.StatusBadRequest, "validation_failed", "reason is too long")
		return
	}
	for _, accountID := range []string{input.DisableAccountID, input.EnableAccountID} {
		if accountID == "" {
			continue
		}
		managed, err := s.managedGatewayAccount(r.Context(), inst, accountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		if managed {
			writeError(w, http.StatusConflict, "managed_account_requires_route_plan", "managed account switching must be changed through its route plan")
			return
		}
	}
	err := s.orch.SwitchUpstream(actorCtx(r), contracts.AccountSwitch{
		InstanceID:       id,
		DisableAccountID: input.DisableAccountID,
		EnableAccountID:  input.EnableAccountID,
		Reason:           input.Reason,
	})
	if err != nil {
		s.writeOrchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// managedGatewayAccount reports whether a gateway-native account is currently
// owned by a RoutePlan binding. Direct gateway mutations have no scheduling
// generation, so allowing them for a managed binding would bypass publish and
// let an old manual request overwrite a newer automatic decision.
func (s *Server) managedGatewayAccount(ctx context.Context, inst contracts.Instance, accountID string) (bool, error) {
	plans, err := s.store.ListRoutePlans(ctx, inst.UserID)
	if err != nil {
		return false, err
	}
	for _, plan := range plans {
		if plan.InstanceID != inst.ID || plan.Status == contracts.RoutePlanDraft {
			continue
		}
		bindings, err := s.store.ListPublishedBindings(ctx, plan.ID)
		if err != nil {
			return false, err
		}
		for _, binding := range bindings {
			if binding.RemoteID == accountID && binding.State != contracts.BindingRevoked {
				return true, nil
			}
		}
	}
	return false, nil
}

func validInstanceKind(kind contracts.InstanceKind) bool {
	switch kind {
	case contracts.InstanceKindSub2API, contracts.InstanceKindNewAPI, contracts.InstanceKindCPA:
		return true
	default:
		return false
	}
}

func validGatewayAccountID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '_' || r == '-' || r == '@' || r == '+' {
			continue
		}
		return false
	}
	return true
}

// actorCtx attaches the logged-in user as the audit actor.
func actorCtx(r *http.Request) context.Context {
	user := currentUser(r)
	return contracts.WithActor(r.Context(), contracts.Actor{Type: "user", ID: user.Email})
}

// writeOrchError maps orchestration errors to HTTP: unknown instance -> 404,
// everything else (adapter/admin API failures) -> 502 upstream error.
func (s *Server) writeOrchError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "instance not found")
		return
	}
	if errors.Is(err, orchestrator.ErrManagedAccountSchedulingFence) {
		writeError(w, http.StatusConflict, "managed_account_requires_route_plan", "managed account scheduling must be changed through its route plan")
		return
	}
	if isConnectorRequiredError(err) {
		writeError(w, http.StatusBadRequest, "connector_not_configured", "该实例还没有可用连接器。请先安装并上线连接器，再在连接器运行环境中配置网关凭证。")
		return
	}
	writeError(w, http.StatusBadGateway, "gateway_error", err.Error())
}

func isConnectorRequiredError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "has no connector") ||
		strings.Contains(msg, "install a connector and configure the gateway credential locally")
}

func (s *Server) handleListSupplyOffers(w http.ResponseWriter, r *http.Request) {
	supplierUserID, ok := parseOptionalUserID(w, r.URL.Query().Get("supplier_user_id"))
	if !ok {
		return
	}
	user := currentUser(r)
	if !auth.IsPlatformAdmin(user) {
		if !auth.IsSupplier(user) || user.ID == 0 {
			writeError(w, http.StatusForbidden, "forbidden", "supplier role required")
			return
		}
		if supplierUserID != 0 && supplierUserID != user.ID {
			writeError(w, http.StatusForbidden, "forbidden", "supplier out of scope")
			return
		}
		supplierUserID = user.ID
	}
	offers, err := s.store.ListSupplyOffers(r.Context(), supplierUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, offers)
}

func (s *Server) handleCreateSupplyOffer(w http.ResponseWriter, r *http.Request) {
	var req supplyOfferWriteRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	input := req.supplyOffer()
	actor := currentUser(r)
	if !auth.IsPlatformAdmin(actor) {
		if actor.ID == 0 {
			writeError(w, http.StatusForbidden, "forbidden", "supplier role required")
			return
		}
		if input.SupplierUserID == 0 {
			input.SupplierUserID = actor.ID
		}
		if !s.requireSupplierWrite(w, r, input.SupplierUserID) {
			return
		}
	} else if input.SupplierUserID != 0 {
		if _, ok := s.enabledUserWithRole(w, r, input.SupplierUserID, contracts.UserRoleSupplier, "supplier"); !ok {
			return
		}
	}
	if !s.validateSupplyOffer(w, r, &input) {
		return
	}
	input.Status = contracts.SupplyOfferStatusPending
	offer, err := s.store.CreateSupplyOffer(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     offer.SupplierUserID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "supply.create",
		RiskLevel:  contracts.RiskLevelL1,
		TargetType: "supply_offer",
		TargetID:   offer.ID,
		Result:     "accepted",
	})
	writeJSON(w, http.StatusCreated, offer)
}

type supplyOfferWriteRequest struct {
	SupplierUserID int64                     `json:"supplier_user_id,omitempty"`
	Kind           contracts.SupplyOfferKind `json:"kind"`
	Provider       string                    `json:"provider,omitempty"`
	CredentialRef  string                    `json:"credential_ref"`
	ProxyRef       string                    `json:"proxy_ref,omitempty"`
	Quota          int64                     `json:"quota,omitempty"`
	UnitPrice      string                    `json:"unit_price,omitempty"`
	Labels         map[string]string         `json:"labels,omitempty"`
}

func (req supplyOfferWriteRequest) supplyOffer() contracts.SupplyOffer {
	return contracts.SupplyOffer{
		SupplierUserID: req.SupplierUserID,
		Kind:           req.Kind,
		Provider:       strings.TrimSpace(req.Provider),
		CredentialRef:  strings.TrimSpace(req.CredentialRef),
		ProxyRef:       strings.TrimSpace(req.ProxyRef),
		Quota:          req.Quota,
		UnitPrice:      strings.TrimSpace(req.UnitPrice),
		Labels:         req.Labels,
	}
}

func (s *Server) validateSupplyOffer(w http.ResponseWriter, r *http.Request, input *contracts.SupplyOffer) bool {
	if input.SupplierUserID == 0 || input.Kind == "" || input.CredentialRef == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "supplier_user_id, kind, and credential_ref are required")
		return false
	}
	if input.Kind != contracts.SupplyOfferOAuthSubscription && input.Kind != contracts.SupplyOfferAPIKey {
		writeError(w, http.StatusBadRequest, "validation_failed", "kind must be oauth_subscription or api_key")
		return false
	}
	if input.Quota < 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "quota must be non-negative")
		return false
	}
	if input.Kind == contracts.SupplyOfferOAuthSubscription && input.ProxyRef == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "proxy_ref is required for oauth_subscription")
		return false
	}
	if !secretRefUsableByUser(input.CredentialRef, input.SupplierUserID) ||
		(input.ProxyRef != "" && !secretRefUsableByUser(input.ProxyRef, input.SupplierUserID)) {
		writeError(w, http.StatusForbidden, "forbidden", "supply refs are outside supplier scope")
		return false
	}
	if secretRefFromRef(input.CredentialRef, input.SupplierUserID).Kind != contracts.SecretKindUpstream {
		writeError(w, http.StatusBadRequest, "validation_failed", "credential_ref must reference an upstream secret")
		return false
	}
	if input.ProxyRef != "" && secretRefFromRef(input.ProxyRef, input.SupplierUserID).Kind != contracts.SecretKindProxy {
		writeError(w, http.StatusBadRequest, "validation_failed", "proxy_ref must reference a proxy secret")
		return false
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return false
	}
	refs, err := s.secrets.ListRefs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "vault_error", err.Error())
		return false
	}
	availableRefs := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		availableRefs[ref] = struct{}{}
	}
	if _, ok := availableRefs[input.CredentialRef]; !ok {
		writeError(w, http.StatusBadRequest, "validation_failed", "credential_ref does not exist")
		return false
	}
	if _, ok := availableRefs[input.ProxyRef]; input.ProxyRef != "" && !ok {
		writeError(w, http.StatusBadRequest, "validation_failed", "proxy_ref does not exist")
		return false
	}
	return true
}

func (s *Server) handleUpdateSupplyOffer(w http.ResponseWriter, r *http.Request) {
	existing, ok := s.supplyOfferForWrite(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if existing.Status == contracts.SupplyOfferStatusRevoked {
		writeError(w, http.StatusConflict, "offer_revoked", "cannot edit a revoked offer")
		return
	}
	var req supplyOfferWriteRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.SupplierUserID != 0 && req.SupplierUserID != existing.SupplierUserID {
		writeError(w, http.StatusBadRequest, "immutable_field", "supplier_user_id is immutable")
		return
	}
	updated := req.supplyOffer()
	updated.ID = existing.ID
	updated.SupplierUserID = existing.SupplierUserID
	updated.Status = existing.Status
	updated.CreatedAt = existing.CreatedAt
	if !s.validateSupplyOffer(w, r, &updated) {
		return
	}
	saved, err := s.store.UpdateSupplyOffer(r.Context(), updated)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "offer_revoked", "cannot edit a revoked offer")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "supply offer not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     saved.SupplierUserID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "supply.update",
		RiskLevel:  contracts.RiskLevelL1,
		TargetType: "supply_offer",
		TargetID:   saved.ID,
		Result:     "accepted",
	})
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleRevokeSupplyOffer(w http.ResponseWriter, r *http.Request) {
	existing, ok := s.supplyOfferForWrite(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if existing.Status == contracts.SupplyOfferStatusRevoked {
		writeJSON(w, http.StatusOK, existing)
		return
	}
	revoked, err := s.store.RevokeSupplyOffer(r.Context(), existing.ID)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "offer_allocated", "revoke allocated ledger entries before revoking the offer")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "supply offer not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     revoked.SupplierUserID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "supply_offer.revoke",
		RiskLevel:  contracts.RiskLevelL1,
		TargetType: "supply_offer",
		TargetID:   revoked.ID,
		Result:     "accepted",
	})
	writeJSON(w, http.StatusOK, revoked)
}

func (s *Server) supplyOfferForWrite(w http.ResponseWriter, r *http.Request, id string) (contracts.SupplyOffer, bool) {
	offer, err := s.store.GetSupplyOffer(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "supply offer not found")
			return contracts.SupplyOffer{}, false
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return contracts.SupplyOffer{}, false
	}
	if !s.requireSupplierWrite(w, r, offer.SupplierUserID) {
		return contracts.SupplyOffer{}, false
	}
	return offer, true
}

// handleAllocateSupplyOffer records an allocation of an offer to an owner
// instance in the ledger and moves the offer to active. Configuration delivery
// to the gateway itself is a separate (W6 provisioning) step.
func (s *Server) handleAllocateSupplyOffer(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	offerID := r.PathValue("id")
	var input struct {
		InstanceID string `json:"instance_id"`
		Note       string `json:"note,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(input.InstanceID) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "instance_id is required")
		return
	}

	offer, err := s.store.GetSupplyOffer(r.Context(), offerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "supply offer not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if offer.Status == contracts.SupplyOfferStatusRevoked {
		writeError(w, http.StatusConflict, "offer_revoked", "cannot allocate a revoked offer")
		return
	}
	inst, err := s.store.GetInstance(r.Context(), input.InstanceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "instance not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if _, ok := s.enabledUserWithRole(w, r, inst.UserID, contracts.UserRoleClient, "owner"); !ok {
		return
	}

	entry, err := s.store.AllocateSupplyOffer(r.Context(), contracts.SupplyLedgerEntry{
		OfferID:        offer.ID,
		SupplierUserID: offer.SupplierUserID,
		UserID:         inst.UserID,
		InstanceID:     inst.ID,
		Status:         contracts.SupplyLedgerAllocated,
		Note:           input.Note,
	})
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			writeError(w, http.StatusConflict, "duplicate_allocation", "offer already has an active allocation for this instance")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "offer_revoked", "cannot allocate a revoked offer")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "supply offer not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     inst.UserID,
		InstanceID: inst.ID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "supply.allocate",
		RiskLevel:  contracts.RiskLevelL1,
		TargetType: "supply_offer",
		TargetID:   offer.ID,
		Result:     "accepted",
	})
	writeJSON(w, http.StatusCreated, entry)
}

// handleRevokeSupplyLedger marks one allocation revoked; if no allocations
// remain, the offer's own status is a supplier concern (left unchanged here).
func (s *Server) handleRevokeSupplyLedger(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	var input struct {
		Note string `json:"note,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&input)

	entries, err := s.store.ListSupplyLedger(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	var target *contracts.SupplyLedgerEntry
	for i := range entries {
		if entries[i].ID == id {
			target = &entries[i]
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "not_found", "ledger entry not found")
		return
	}
	if err := s.store.UpdateSupplyLedgerStatus(r.Context(), id, contracts.SupplyLedgerRevoked, input.Note); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     target.UserID,
		InstanceID: target.InstanceID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     "supply.revoke",
		RiskLevel:  contracts.RiskLevelL1,
		TargetType: "supply_ledger",
		TargetID:   id,
		Result:     "accepted",
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListSupplyLedger(w http.ResponseWriter, r *http.Request) {
	offerID := r.URL.Query().Get("offer_id")
	entries, err := s.store.ListSupplyLedger(r.Context(), offerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	user := currentUser(r)
	if !auth.IsPlatformAdmin(user) {
		scoped := entries[:0]
		for _, entry := range entries {
			if auth.IsSupplier(user) && user.ID != 0 && entry.SupplierUserID == user.ID {
				scoped = append(scoped, entry)
				continue
			}
			if auth.IsOwner(user) && user.ID != 0 && entry.UserID == user.ID {
				scoped = append(scoped, entry)
			}
		}
		entries = scoped
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleListCapabilities(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, contracts.ExecutableGatewayCapabilities())
}

func (s *Server) handleListAudits(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.scopeUser(w, r, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	audits, err := s.store.ListAudits(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !auth.IsPlatformAdmin(currentUser(r)) {
		for i := range audits {
			audits[i] = ownerSafeAudit(audits[i])
		}
	}
	writeJSON(w, http.StatusOK, audits)
}

// ownerSafeAudit preserves the existence and outcome of an operation while
// removing identifiers and diagnostic payloads that can disclose a managed
// channel, remote account, decision, approval, or gateway response. Detailed
// audit evidence remains available to platform administrators.
func ownerSafeAudit(audit contracts.OperationAudit) contracts.OperationAudit {
	audit.TargetID = ""
	audit.RequestHash = ""
	audit.ErrorMessage = ""
	audit.ApprovalID = ""
	audit.WorkflowRunID = ""
	// Keep only owner-safe display context. Internal pool/channel/account IDs
	// and workflow generations remain administrator-only evidence.
	if len(audit.Details) > 0 {
		safe := make(map[string]string)
		for _, key := range []string{
			"instance_name", "pool_name", "channel_name", "account_name",
			"from_account_name", "to_account_name", "account_count", "probe_status",
			"schedulable", "attempts", "delivered_keys", "next_attempt_at", "reason_code",
		} {
			if value := audit.Details[key]; value != "" {
				safe[key] = value
			}
		}
		audit.Details = safe
	}
	return audit
}

func (s *Server) handleListNotificationRoutes(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.scopeNotificationUser(w, r, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	routes, err := s.store.ListNotificationRoutes(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, routes)
}

func validRoute(input contracts.NotificationRoute) string {
	if input.UserID == 0 || strings.TrimSpace(input.Name) == "" {
		return "user_id and name are required"
	}
	switch input.Channel {
	case contracts.NotificationChannelQQ, contracts.NotificationChannelFeishu, contracts.NotificationChannelWebhook:
	default:
		return "channel must be qq, feishu, or webhook"
	}
	if input.Channel == contracts.NotificationChannelWebhook {
		if strings.TrimSpace(input.TargetRef) == "" {
			return "target_ref is required"
		}
		if err := notify.ValidateNotificationTargetRef(input.TargetRef, input.UserID); err != nil {
			return "webhook target_ref " + err.Error()
		}
	} else if !notify.IsSystemNotificationTargetRef(input.TargetRef, input.Channel) &&
		!notify.IsPersonalNotificationTargetRef(input.TargetRef, input.UserID, input.Channel) {
		return "target_ref must select the system channel or the owner's personal channel"
	}
	switch input.MinRiskLevel {
	case contracts.RiskLevelL0, contracts.RiskLevelL1, contracts.RiskLevelL2, contracts.RiskLevelL3:
	default:
		return "min_risk_level must be L0-L3"
	}
	if input.MinEventLevel != "" && !input.MinEventLevel.Valid() {
		return "min_event_level must be L0-L3"
	}
	return ""
}

func normalizeNotificationTarget(input *contracts.NotificationRoute) {
	if !input.MinEventLevel.Valid() {
		input.MinEventLevel = contracts.EventLevel(input.MinRiskLevel)
	}
	switch input.Channel {
	case contracts.NotificationChannelFeishu:
		input.TargetRef = strings.TrimSpace(input.TargetRef)
		if input.TargetRef == "" {
			input.TargetRef = "system:feishu"
		}
	case contracts.NotificationChannelQQ:
		input.TargetRef = strings.TrimSpace(input.TargetRef)
		if input.TargetRef == "" {
			input.TargetRef = "system:qq"
		}
	case contracts.NotificationChannelWebhook:
		input.TargetRef = strings.TrimSpace(input.TargetRef)
	}
}

func (s *Server) validateNotificationRouteTarget(w http.ResponseWriter, r *http.Request, input contracts.NotificationRoute) bool {
	if notify.IsSystemNotificationTargetRef(input.TargetRef, input.Channel) {
		return true
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault is not configured")
		return false
	}
	secret, err := s.secrets.Resolve(r.Context(), strings.TrimSpace(input.TargetRef))
	if err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "validation_failed", "notification credential does not exist")
			return false
		}
		writeError(w, http.StatusInternalServerError, "vault_error", "notification credential could not be resolved")
		return false
	}
	if input.Channel == contracts.NotificationChannelWebhook {
		if err := notify.ValidateWebhookURL(strings.TrimSpace(secret.Value)); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", "notification credential must contain a safe HTTPS webhook URL")
			return false
		}
		return true
	}
	credential, err := notify.DecodePersonalTargetCredential(secret.Value)
	if err != nil || credential.Channel != input.Channel || !notify.IsPersonalNotificationTargetRef(input.TargetRef, input.UserID, input.Channel) {
		writeError(w, http.StatusBadRequest, "validation_failed", "personal notification target is invalid")
		return false
	}
	return true
}

func (s *Server) auditRouteChange(r *http.Request, route contracts.NotificationRoute, action string) {
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID:     route.UserID,
		ActorType:  "user",
		ActorID:    actor.Email,
		Action:     action,
		RiskLevel:  contracts.RiskLevelL1,
		TargetType: "notification_route",
		TargetID:   route.ID,
		Result:     "accepted",
	})
}

func (s *Server) handleCreateNotificationRoute(w http.ResponseWriter, r *http.Request) {
	s.notificationTargetsMu.Lock()
	defer s.notificationTargetsMu.Unlock()
	var input contracts.NotificationRoute
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !auth.IsPlatformAdmin(currentUser(r)) {
		input.UserID = currentUser(r).ID
	}
	normalizeNotificationTarget(&input)
	if msg := validRoute(input); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	if !s.requireUserNotificationWrite(w, r, input.UserID) {
		return
	}
	targetUser, err := s.store.GetUser(r.Context(), input.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "notification owner not found")
		} else {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		}
		return
	}
	if !targetUser.Enabled || (!auth.HasRole(targetUser, contracts.UserRoleClient) && !auth.HasRole(targetUser, contracts.UserRoleAdmin)) {
		writeError(w, http.StatusBadRequest, "validation_failed", "notification owner must be an enabled client or platform admin")
		return
	}
	if !s.validateNotificationRouteTarget(w, r, input) {
		return
	}
	route, err := s.store.CreateNotificationRoute(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.auditRouteChange(r, route, "notification_route.create")
	writeJSON(w, http.StatusCreated, route)
}

func (s *Server) handleUpdateNotificationRoute(w http.ResponseWriter, r *http.Request) {
	s.notificationTargetsMu.Lock()
	defer s.notificationTargetsMu.Unlock()
	existing, err := s.store.GetNotificationRoute(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "notification route not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !s.requireUserNotificationWrite(w, r, existing.UserID) {
		return
	}
	var input contracts.NotificationRoute
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	input.ID = existing.ID
	if input.UserID != 0 && input.UserID != existing.UserID {
		writeError(w, http.StatusBadRequest, "immutable_field", "user_id is immutable")
		return
	}
	input.UserID = existing.UserID
	normalizeNotificationTarget(&input)
	if msg := validRoute(input); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	if !s.validateNotificationRouteTarget(w, r, input) {
		return
	}
	route, err := s.store.UpdateNotificationRoute(r.Context(), input)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "notification route not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.auditRouteChange(r, route, "notification_route.update")
	writeJSON(w, http.StatusOK, route)
}

func (s *Server) handleDeleteNotificationRoute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Look the route up first so the audit carries its user.
	routes, err := s.store.ListNotificationRoutes(r.Context(), 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	var target *contracts.NotificationRoute
	for i := range routes {
		if routes[i].ID == id {
			target = &routes[i]
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "not_found", "notification route not found")
		return
	}
	if !s.requireUserNotificationWrite(w, r, target.UserID) {
		return
	}
	if err := s.store.DeleteNotificationRoute(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.auditRouteChange(r, *target, "notification_route.delete")
	w.WriteHeader(http.StatusNoContent)
}

func withJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		next.ServeHTTP(w, r)
	})
}

// withDevCORS permits cross-origin calls from the Vite dev server. Development
// only - production serves the console same-origin and does not enable this.
func withDevCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func setNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{
		"code":    code,
		"message": message,
	})
}
