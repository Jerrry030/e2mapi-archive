package store

import (
	"context"
	"errors"
	"time"

	"e2m.local/contracts"
)

// ErrNotFound is returned when a lookup finds no matching record.
var ErrNotFound = errors.New("store: not found")

// ErrDuplicate is returned when a unique constraint (e.g. user email) is hit.
var ErrDuplicate = errors.New("store: duplicate")

// ErrConflict is returned when a state transition is no longer valid.
var ErrConflict = errors.New("store: conflict")

// ErrInvalid reports a persistence input that violates the domain contract.
var ErrInvalid = errors.New("store: invalid input")

// ErrNoSupply distinguishes an otherwise valid downstream key from a request
// for which every compatible platform upstream is unavailable or excluded.
var ErrNoSupply = errors.New("store: no platform supply available")

// ErrLastEnabledAdmin protects the control plane from losing its final usable
// administrator account.
var ErrLastEnabledAdmin = errors.New("store: cannot disable or demote the last enabled admin")

// ErrUserDeactivationInProgress prevents an administrator from changing the
// target identity while its Connector is the only safe route-drain channel.
var ErrUserDeactivationInProgress = errors.New("store: user deactivation is in progress")

// Store is the narrow persistence boundary for E2M Core. Every method takes a
// context and returns an error so that a real backend (PostgreSQL) can honour
// cancellation and surface failures — the in-memory implementation satisfies
// the same contract for tests and local runs.
//
// Audit writes are explicit (AppendAudit): the store never records audit events
// as a hidden side effect of another write. Callers decide what to audit.
type Store interface {
	// Instances (managed sub2api / new-api / CPA), scoped by owner user.
	CreateInstance(ctx context.Context, input contracts.Instance) (contracts.Instance, error)
	ListInstances(ctx context.Context, userID int64) ([]contracts.Instance, error)
	GetInstance(ctx context.Context, id string) (contracts.Instance, error)
	UpdateInstance(ctx context.Context, input contracts.Instance) (contracts.Instance, error)
	UpdateInstanceConnector(ctx context.Context, id, connectorID string) (contracts.Instance, error)
	GetInstanceMonitorPolicy(ctx context.Context, instanceID string) (contracts.InstanceMonitorPolicy, error)
	UpsertInstanceMonitorPolicy(ctx context.Context, input contracts.InstanceMonitorPolicy) (contracts.InstanceMonitorPolicy, error)

	// Hybrid Supply base allocation is versioned per owner instance. Upsert is
	// an optimistic CAS: expectedVersion=0 creates a missing row; positive
	// versions update only the exact stored generation.
	GetHybridAllocation(ctx context.Context, instanceID string) (contracts.HybridAllocation, error)
	UpsertHybridAllocation(ctx context.Context, input contracts.HybridAllocation, expectedVersion int64) (contracts.HybridAllocation, error)
	GetHybridGatewayBinding(ctx context.Context, userID int64, instanceID string, class contracts.ResourceClass) (contracts.HybridGatewayBinding, error)
	ListHybridGatewayBindings(ctx context.Context, userID int64, instanceID string) ([]contracts.HybridGatewayBinding, error)
	UpsertHybridGatewayBinding(ctx context.Context, input contracts.HybridGatewayBinding, expectedVersion int64) (contracts.HybridGatewayBinding, error)
	CreateHybridRoutingExecution(ctx context.Context, input contracts.HybridRoutingExecution) (contracts.HybridRoutingExecution, error)
	GetHybridRoutingExecution(ctx context.Context, userID int64, id string) (contracts.HybridRoutingExecution, error)
	ListHybridRoutingExecutions(ctx context.Context, userID int64, instanceID string, limit int) ([]contracts.HybridRoutingExecution, error)
	ClaimHybridRoutingExecution(ctx context.Context, workerID string, leaseDuration time.Duration) (contracts.HybridRoutingExecution, bool, error)
	RenewHybridRoutingExecution(ctx context.Context, id, workerID string, expectedVersion int64, leaseDuration time.Duration) (contracts.HybridRoutingExecution, error)
	PlanHybridRoutingExecution(ctx context.Context, input contracts.HybridRoutingExecutionPlan) (contracts.HybridRoutingExecution, error)
	CompleteHybridRoutingExecution(ctx context.Context, input contracts.HybridRoutingExecutionCompletion) (contracts.HybridRoutingExecution, error)

	// Wallet and virtual keys. All monetary mutations are expressed through
	// atomic, balanced journal operations; callers cannot set a balance.
	GetWallet(ctx context.Context, userID int64, currency string) (contracts.Wallet, error)
	ListWalletJournals(ctx context.Context, userID int64, limit int) ([]contracts.WalletJournal, error)
	AdjustWalletBalance(ctx context.Context, userID int64, currency string, deltaMicros int64, idempotencyKey, note string) (contracts.Wallet, contracts.WalletJournal, error)
	CreateVirtualKey(ctx context.Context, input contracts.VirtualKey) (contracts.VirtualKey, error)
	GetVirtualKey(ctx context.Context, userID int64, id string) (contracts.VirtualKey, error)
	GetVirtualKeyByHash(ctx context.Context, tokenHash string) (contracts.VirtualKey, error)
	ListVirtualKeys(ctx context.Context, userID int64) ([]contracts.VirtualKey, error)
	UpdateVirtualKey(ctx context.Context, input contracts.VirtualKey) (contracts.VirtualKey, error)
	DeleteVirtualKey(ctx context.Context, userID int64, id string) error

	// Supply runtime configuration and trusted request accounting.
	UpsertSupplyChannelEndpoint(ctx context.Context, input contracts.SupplyChannelEndpoint) (contracts.SupplyChannelEndpoint, error)
	GetSupplyChannelEndpoint(ctx context.Context, channelID string) (contracts.SupplyChannelEndpoint, error)
	ListSupplyCandidates(ctx context.Context, class contracts.ResourceClass, model string) ([]contracts.SupplyCandidate, error)
	GetSupplyDailyUsage(ctx context.Context, userID int64, instanceID, virtualKeyID, currency string) (contracts.SupplyDailyUsage, error)
	ListSupplyUsage(ctx context.Context, filter contracts.SupplyUsageFilter) ([]contracts.SupplyUsageRecord, error)
	ReserveSupplyRequest(ctx context.Context, tokenHash, requestID, model, currency string, excludedChannelIDs []string) (contracts.SupplyReservationResult, error)
	SettleSupplyRequest(ctx context.Context, reservationID string, promptTokens, completionTokens int64) (contracts.SupplySettlementResult, error)
	SettleSupplyRequestConservatively(ctx context.Context, reservationID, reasonCode string) (contracts.SupplySettlementResult, error)
	ReleaseSupplyRequest(ctx context.Context, reservationID, reasonCode string) (contracts.SupplySettlementResult, error)

	// Supply offers registered by supplier users.
	CreateSupplyOffer(ctx context.Context, input contracts.SupplyOffer) (contracts.SupplyOffer, error)
	ListSupplyOffers(ctx context.Context, supplierUserID int64) ([]contracts.SupplyOffer, error)
	GetSupplyOffer(ctx context.Context, id string) (contracts.SupplyOffer, error)
	// UpdateSupplyOffer replaces editable offer metadata while preserving its
	// identity, supplier ownership, lifecycle status, and creation time.
	UpdateSupplyOffer(ctx context.Context, input contracts.SupplyOffer) (contracts.SupplyOffer, error)
	UpdateSupplyOfferStatus(ctx context.Context, id string, status contracts.SupplyOfferStatus) error
	// RevokeSupplyOffer atomically refuses offers that still have allocated
	// ledger entries, then moves the offer to the terminal revoked state.
	RevokeSupplyOffer(ctx context.Context, id string) (contracts.SupplyOffer, error)

	// Supply ledger: allocations of offers to owner instances (the paper trail).
	// AllocateSupplyOffer atomically checks the offer lifecycle and duplicate
	// active allocation, appends the ledger entry, and promotes pending offers
	// to active. Duplicate active offer/instance pairs return ErrDuplicate.
	AllocateSupplyOffer(ctx context.Context, input contracts.SupplyLedgerEntry) (contracts.SupplyLedgerEntry, error)
	AppendSupplyLedger(ctx context.Context, input contracts.SupplyLedgerEntry) (contracts.SupplyLedgerEntry, error)
	UpdateSupplyLedgerStatus(ctx context.Context, id string, status contracts.SupplyLedgerEntryStatus, note string) error
	ListSupplyLedger(ctx context.Context, offerID string) ([]contracts.SupplyLedgerEntry, error)

	// Connector tasks. Core queues typed work and the
	// customer-side connector leases/completes it over an outbound HTTPS poll.
	CreateConnectorEnrollment(ctx context.Context, input contracts.ConnectorEnrollment) (contracts.ConnectorEnrollment, error)
	UseConnectorEnrollment(ctx context.Context, tokenHash string, input contracts.Connector) (contracts.Connector, contracts.ConnectorEnrollment, error)
	ListConnectors(ctx context.Context, filter contracts.ConnectorFilter) ([]contracts.Connector, error)
	GetConnector(ctx context.Context, id string) (contracts.Connector, error)
	GetConnectorByTokenHash(ctx context.Context, tokenHash string) (contracts.Connector, error)
	RecordConnectorSeen(ctx context.Context, id, version string, runtime contracts.ConnectorRuntimeState) (contracts.Connector, error)
	UpdateConnectorToken(ctx context.Context, id, tokenHash string) (contracts.Connector, error)
	RevokeConnector(ctx context.Context, id string) (contracts.Connector, error)
	CreateConnectorTask(ctx context.Context, input contracts.ConnectorTask) (contracts.ConnectorTask, error)
	GetConnectorTask(ctx context.Context, id string) (contracts.ConnectorTask, error)
	ListConnectorTasks(ctx context.Context, filter contracts.ConnectorTaskFilter) ([]contracts.ConnectorTask, error)
	LeaseConnectorTasks(ctx context.Context, req contracts.ConnectorTaskLeaseRequest) ([]contracts.ConnectorTask, error)
	BeginConnectorTaskExecution(ctx context.Context, id string, req contracts.ConnectorTaskExecutionRequest) (contracts.ConnectorTask, error)
	CompleteConnectorTask(ctx context.Context, id string, req contracts.ConnectorTaskCompleteRequest) (contracts.ConnectorTask, error)
	// ResolveConnectorTaskExecution atomically terminates a fail-closed
	// executing task and appends the mandatory administrator audit.
	ResolveConnectorTaskExecution(ctx context.Context, id string, req contracts.ConnectorTaskExecutionResolveRequest, audit contracts.OperationAudit) (contracts.ConnectorTask, error)

	// Audit trail. AppendAudit is the only way an audit row is written.
	AppendAudit(ctx context.Context, input contracts.OperationAudit) (contracts.OperationAudit, error)
	ListAudits(ctx context.Context, userID int64) ([]contracts.OperationAudit, error)

	// Notification routes (system Feishu/QQ channels or per-user webhook refs).
	CreateNotificationRoute(ctx context.Context, input contracts.NotificationRoute) (contracts.NotificationRoute, error)
	GetNotificationRoute(ctx context.Context, id string) (contracts.NotificationRoute, error)
	UpdateNotificationRoute(ctx context.Context, input contracts.NotificationRoute) (contracts.NotificationRoute, error)
	DeleteNotificationRoute(ctx context.Context, id string) error
	ListNotificationRoutes(ctx context.Context, userID int64) ([]contracts.NotificationRoute, error)
	CreateNotificationDelivery(ctx context.Context, input contracts.NotificationDelivery) (contracts.NotificationDelivery, error)
	GetNotificationDelivery(ctx context.Context, id string) (contracts.NotificationDelivery, error)
	ListNotificationDeliveries(ctx context.Context, filter contracts.NotificationDeliveryFilter) ([]contracts.NotificationDelivery, error)
	ClaimNotificationDelivery(ctx context.Context, workerID string, leaseDuration time.Duration) (contracts.NotificationDelivery, bool, error)
	CompleteNotificationDelivery(ctx context.Context, id, workerID string, expectedLeaseVersion int64, succeeded bool, errorCode, errorMessage string, nextAttemptAt time.Time) (contracts.NotificationDelivery, error)
	RetryNotificationDelivery(ctx context.Context, id string) (contracts.NotificationDelivery, error)

	// Upstream pools + channels (platform-curated managed upstream layer).
	CreateUpstreamPool(ctx context.Context, input contracts.UpstreamPool) (contracts.UpstreamPool, error)
	GetUpstreamPool(ctx context.Context, id string) (contracts.UpstreamPool, error)
	ListUpstreamPools(ctx context.Context) ([]contracts.UpstreamPool, error)
	UpdateUpstreamPool(ctx context.Context, input contracts.UpstreamPool) (contracts.UpstreamPool, error)

	CreateUpstreamChannel(ctx context.Context, input contracts.UpstreamChannel) (contracts.UpstreamChannel, error)
	GetUpstreamChannel(ctx context.Context, id string) (contracts.UpstreamChannel, error)
	ListUpstreamChannels(ctx context.Context, poolID string) ([]contracts.UpstreamChannel, error)
	UpdateUpstreamChannel(ctx context.Context, input contracts.UpstreamChannel) (contracts.UpstreamChannel, error)

	// Route plans: desired-state binding of a pool to an owner instance.
	CreateRoutePlan(ctx context.Context, input contracts.RoutePlan) (contracts.RoutePlan, error)
	GetRoutePlan(ctx context.Context, id string) (contracts.RoutePlan, error)
	ListRoutePlans(ctx context.Context, userID int64) ([]contracts.RoutePlan, error)
	// UpdateRoutePlan applies a desired-state change under the caller's exact
	// scheduling generation. Every material change advances that generation so
	// recommendations, workers, and queued gateway writes bound to the old plan
	// fail closed. Identity fields are immutable.
	UpdateRoutePlan(ctx context.Context, input contracts.RoutePlan) (contracts.RoutePlan, error)
	// CompleteRoutePlanPublish is the narrow completion edge for a successful
	// reconcile. It changes only draft -> published at the generation that
	// already owns the gateway and binding writes; it never creates a new
	// scheduling identity.
	CompleteRoutePlanPublish(ctx context.Context, id string, expectedSchedulingGeneration int64) (contracts.RoutePlan, error)
	// ClaimRoutePlanScheduling atomically verifies the plan's current lifecycle
	// state and advances its scheduling generation. The returned plan owns all
	// subsequent gateway/binding mutations until a newer claim supersedes it.
	ClaimRoutePlanScheduling(ctx context.Context, id string, allowedStatuses ...contracts.RoutePlanStatus) (contracts.RoutePlan, error)
	// TransitionRoutePlanScheduling atomically changes lifecycle state and
	// advances the same scheduling generation. Rollback uses it so a stale plan
	// snapshot cannot suspend a concurrently republished plan.
	TransitionRoutePlanScheduling(ctx context.Context, id string, expected, target contracts.RoutePlanStatus) (contracts.RoutePlan, error)
	// ClaimPlanChannels atomically selects at most one permanent Key per active
	// source, claims it for the plan owner, and creates pending bindings before
	// any secret delivery can begin. An inactive pool fails closed.
	ClaimPlanChannels(ctx context.Context, planID string) ([]contracts.UpstreamChannel, error)

	// Published bindings: the reconcile paper trail (plan -> gateway channel).
	UpsertPublishedBinding(ctx context.Context, input contracts.PublishedBinding) (contracts.PublishedBinding, error)
	ListPublishedBindings(ctx context.Context, planID string) ([]contracts.PublishedBinding, error)
	// RecordPublishedBindingVerification atomically applies callability evidence
	// without participating in the gateway scheduling-generation fence. A
	// successful request upgrades pending/failed evidence; a failed request never
	// downgrades a previously verified binding.
	RecordPublishedBindingVerification(ctx context.Context, planID, channelID string, status contracts.PublishedBindingVerificationStatus, source contracts.PublishedBindingVerificationSource, observedAt time.Time, errorCode string) (contracts.PublishedBinding, error)
	// Upstream key delivery is a separate Core-vault mapping used only for
	// controlled owner delivery. Connector credential binding IDs are never
	// resolved by these methods.
	UpsertUpstreamKeyDelivery(ctx context.Context, input contracts.UpstreamKeyDelivery) (contracts.UpstreamKeyDelivery, error)
	GetUpstreamKeyDelivery(ctx context.Context, channelID string) (contracts.UpstreamKeyDelivery, error)
	ListUpstreamKeyDeliveries(ctx context.Context) ([]contracts.UpstreamKeyDelivery, error)
	UpdateUpstreamKeyDeliveryProof(ctx context.Context, channelID string, expectedKeyVersion int64, connectorID string, status contracts.DeliveryKeyProofStatus) (contracts.UpstreamKeyDelivery, error)
	GetUpstreamKeyProofReceipt(ctx context.Context, channelID, instanceID string) (contracts.UpstreamKeyProofReceipt, error)
	UpsertUpstreamKeyProofReceipt(ctx context.Context, input contracts.UpstreamKeyProofReceipt) (contracts.UpstreamKeyProofReceipt, error)
	GetUpstreamKeyDeployment(ctx context.Context, channelID, instanceID string) (contracts.UpstreamKeyDeployment, error)
	UpsertUpstreamKeyDeployment(ctx context.Context, input contracts.UpstreamKeyDeployment) (contracts.UpstreamKeyDeployment, error)
	ListAssignedUpstreamKeys(ctx context.Context, userID int64) ([]contracts.AssignedUpstreamKey, error)

	// Automatic onboarding is a durable, leased state machine scoped to one
	// instance/shared-pool pair. Claims and transitions use the store clock;
	// stale worker versions or leases return ErrConflict.
	UpsertOnboardingWorkflow(ctx context.Context, input contracts.OnboardingWorkflow) (contracts.OnboardingWorkflow, error)
	GetOnboardingWorkflow(ctx context.Context, id string) (contracts.OnboardingWorkflow, error)
	ListOnboardingWorkflows(ctx context.Context, filter contracts.OnboardingWorkflowFilter) ([]contracts.OnboardingWorkflow, error)
	ClaimOnboardingWorkflow(ctx context.Context, workerID string, leaseDuration time.Duration) (workflow contracts.OnboardingWorkflow, claimed bool, err error)
	// RenewOnboardingWorkflowLease extends a live claim using the store clock.
	// It advances Version, so callers must carry the returned workflow and stale
	// workers cannot transition or renew after another generation takes over.
	RenewOnboardingWorkflowLease(ctx context.Context, id, workerID string, expectedVersion int64, leaseDuration time.Duration) (contracts.OnboardingWorkflow, error)
	// ReleaseOnboardingWorkflowLease makes the exact live claim immediately
	// reclaimable. Crashes still recover through the bounded lease TTL.
	ReleaseOnboardingWorkflowLease(ctx context.Context, id, workerID string, expectedVersion int64) error
	TransitionOnboardingWorkflow(ctx context.Context, input contracts.OnboardingWorkflow, expectedVersion int64) (contracts.OnboardingWorkflow, error)

	// Reconcile runs: the execution history of every publish/reconcile
	// (dry-run/apply/rollback), written by the engine's unified execution layer
	// so background/automatic switches are audited too.
	AppendReconcileRun(ctx context.Context, input contracts.ReconcileRun) (contracts.ReconcileRun, error)
	ListReconcileRuns(ctx context.Context, planID string, limit int) ([]contracts.ReconcileRun, error)

	// Auto-switch decisions: the persisted record of every health-driven
	// automatic switch intent and its dry-run/apply/observe/rollback outcome
	// (Phase 4). ClaimAutoSwitchDecision atomically creates the transient
	// applying row that owns all subsequent side effects, or returns the active
	// row that already owns the same (plan, fingerprint). Transition uses an
	// expected-status compare-and-swap so only the claim owner can advance it.
	CreateAutoSwitchDecision(ctx context.Context, input contracts.AutoSwitchDecision) (contracts.AutoSwitchDecision, error)
	ClaimAutoSwitchDecision(ctx context.Context, input contracts.AutoSwitchDecision) (decision contracts.AutoSwitchDecision, claimed bool, err error)
	// ClaimApprovedAutoSwitchDecision atomically moves one approved operator
	// decision into applying, advances the plan scheduling generation, and
	// installs a bounded lease. A losing caller must perform no side effects.
	ClaimApprovedAutoSwitchDecision(ctx context.Context, id string, leaseDuration time.Duration) (decision contracts.AutoSwitchDecision, claimed bool, err error)
	// ClaimAutoSwitchObservation atomically moves one observing decision back to
	// applying, starts a store-clock lease, and advances its fencing generation.
	ClaimAutoSwitchObservation(ctx context.Context, input contracts.AutoSwitchDecision, leaseDuration time.Duration) (contracts.AutoSwitchDecision, error)
	// ClaimExpiredAutoSwitchDecision atomically renews one expired applying
	// lease. lease-less legacy rows are eligible only when last updated no later
	// than legacyStaleBefore. Exactly one repair worker receives claimed=true.
	ClaimExpiredAutoSwitchDecision(ctx context.Context, id string, now, legacyStaleBefore, leaseUntil time.Time) (decision contracts.AutoSwitchDecision, claimed bool, err error)
	// RenewAutoSwitchDecisionLease verifies an unexpired applying owner and
	// extends its lease using the store clock. Stale generations return
	// ErrConflict and cannot perform another external side effect.
	RenewAutoSwitchDecisionLease(ctx context.Context, id string, leaseVersion int64, leaseDuration time.Duration) (contracts.AutoSwitchDecision, error)
	// ReleaseAutoSwitchDecisionLease makes a still-owned applying lease
	// immediately repairable without accepting a caller-clock timestamp.
	ReleaseAutoSwitchDecisionLease(ctx context.Context, id string, leaseVersion int64) error
	GetAutoSwitchDecision(ctx context.Context, id string) (contracts.AutoSwitchDecision, error)
	// UpdateAutoSwitchDecision is retained for interface compatibility but
	// rejects lifecycle mutation. Callers must use the fenced transition APIs.
	UpdateAutoSwitchDecision(ctx context.Context, input contracts.AutoSwitchDecision) (contracts.AutoSwitchDecision, error)
	TransitionAutoSwitchDecision(ctx context.Context, input contracts.AutoSwitchDecision, expected contracts.AutoSwitchStatus) (contracts.AutoSwitchDecision, error)
	ListAutoSwitchDecisions(ctx context.Context, filter contracts.AutoSwitchDecisionFilter) ([]contracts.AutoSwitchDecision, error)
	FindActiveAutoSwitchDecisionByFingerprint(ctx context.Context, planID, fingerprint string) (contracts.AutoSwitchDecision, error)
	// NextGatewaySchedulingGeneration allocates a durable, process-independent
	// ordering generation for automatic gateway scheduling mutations. Callers
	// pair it with a stable plan scope so delayed Connector tasks from an older
	// decision, repair, or recovery cannot overwrite newer intent.

	// Quality circuit runtimes: durable per-plan/per-channel ejection and
	// guarded-recovery state. UpsertQualityCircuitRuntime is an optimistic CAS:
	// expectedVersion=0 creates a missing scope, while a positive value updates
	// only the row at that version. Successful writes advance Version; stale or
	// duplicate creates return ErrConflict.
	GetQualityCircuitRuntime(ctx context.Context, planID, channelID string) (contracts.QualityCircuitRuntime, error)
	ListQualityCircuitRuntimes(ctx context.Context, filter contracts.QualityCircuitRuntimeFilter) ([]contracts.QualityCircuitRuntime, error)
	UpsertQualityCircuitRuntime(ctx context.Context, input contracts.QualityCircuitRuntime, expectedVersion int64) (contracts.QualityCircuitRuntime, error)

	// Route strategies: the persisted health-driven selection policy, scoped to a
	// plan, pool, or user (Phase 5). The auto-switch orchestrator resolves a
	// plan's effective strategy by precedence plan > pool > user > default, so
	// the platform can ship account-wide defaults while a single plan overrides.
	// Upsert is keyed by (scope, scope-owner id): re-saving a plan's strategy
	// replaces it rather than accumulating duplicates.
	UpsertRouteStrategy(ctx context.Context, input contracts.RouteStrategy) (contracts.RouteStrategy, error)
	GetRouteStrategy(ctx context.Context, id string) (contracts.RouteStrategy, error)
	ListRouteStrategies(ctx context.Context, filter contracts.RouteStrategyFilter) ([]contracts.RouteStrategy, error)
	DeleteRouteStrategy(ctx context.Context, id string) error

	// Channel health metrics: append-only per-request/probe observations and the
	// windowed snapshots aggregated from them. These feed health-driven
	// automatic switching (strategy scoring + auto-switch), which reads
	// snapshots, never the raw observations.
	AppendChannelObservation(ctx context.Context, input contracts.ChannelObservation) (contracts.ChannelObservation, error)
	ListChannelObservations(ctx context.Context, filter contracts.ChannelObservationFilter) ([]contracts.ChannelObservation, error)
	UpsertChannelHealthSnapshot(ctx context.Context, input contracts.ChannelHealthSnapshot) (contracts.ChannelHealthSnapshot, error)
	ListChannelHealthSnapshots(ctx context.Context, filter contracts.ChannelHealthSnapshotFilter) ([]contracts.ChannelHealthSnapshot, error)

	// Approval requests (L2/L3 actions gated on a human decision).
	CreateApproval(ctx context.Context, input contracts.ApprovalRequest) (contracts.ApprovalRequest, error)
	GetApproval(ctx context.Context, id string) (contracts.ApprovalRequest, error)
	UpdateApproval(ctx context.Context, input contracts.ApprovalRequest) (contracts.ApprovalRequest, error)
	// TransitionApproval updates an approval only while its persisted status
	// matches expected. A lost decision race returns ErrConflict.
	TransitionApproval(ctx context.Context, input contracts.ApprovalRequest, expected contracts.ApprovalStatus) (contracts.ApprovalRequest, error)
	ListApprovals(ctx context.Context, userID int64, status contracts.ApprovalStatus) ([]contracts.ApprovalRequest, error)

	// Console users and sessions (auth + RBAC).
	CreateUser(ctx context.Context, input contracts.User) (contracts.User, error)
	GetUserByEmail(ctx context.Context, email string) (contracts.User, error)
	GetUser(ctx context.Context, id int64) (contracts.User, error)
	ListUsers(ctx context.Context) ([]contracts.User, error)
	CountUsers(ctx context.Context) (int, error)
	// UpdateUser rejects stale UpdatedAt versions and removal of the final
	// enabled admin. Client deactivation enters a durable drain phase before
	// Connector identity and sessions are physically revoked.
	UpdateUser(ctx context.Context, input contracts.User) (contracts.User, error)
	// ReconcileUserDeactivations atomically finalizes users only after every
	// binding on their route plans is revoked. Failed drain operations remain
	// retryable and are projected as a safe error code on the user.
	ReconcileUserDeactivations(ctx context.Context) error
	// UpdateUserPasswordHash atomically replaces a password hash and revokes
	// every existing session for the target user.
	UpdateUserPasswordHash(ctx context.Context, userID int64, passwordHash string) error
	// CreateSession verifies the current user is still enabled and still has the
	// password hash and roles that were authenticated. This closes the race
	// between login and a concurrent password reset, disable, or role change.
	CreateSession(ctx context.Context, input contracts.Session, expectedUser contracts.User) error
	GetSession(ctx context.Context, tokenHash string) (contracts.Session, error)
	DeleteSession(ctx context.Context, tokenHash string) error

	// System settings (platform-admin managed).
	GetAuthSystemSettings(ctx context.Context) (contracts.AuthSystemSettings, error)
	UpsertAuthSystemSettings(ctx context.Context, input contracts.AuthSystemSettings) (contracts.AuthSystemSettings, error)
	GetPaymentConfig(ctx context.Context) (contracts.PaymentConfig, error)
	UpsertPaymentConfig(ctx context.Context, input contracts.PaymentConfig) (contracts.PaymentConfig, error)

	// Collection provider instances. SecretRefs contains opaque Vault references
	// only; provider credentials never live in the database.
	CreatePaymentProvider(ctx context.Context, input contracts.PaymentProvider) (contracts.PaymentProvider, error)
	GetPaymentProvider(ctx context.Context, id string) (contracts.PaymentProvider, error)
	ListPaymentProviders(ctx context.Context) ([]contracts.PaymentProvider, error)
	UpdatePaymentProvider(ctx context.Context, input contracts.PaymentProvider) (contracts.PaymentProvider, error)
	DeletePaymentProvider(ctx context.Context, id string) error

	// Payment orders are durable local records. Creation is a store primitive for
	// the future checkout service and for persistence tests; current admin HTTP
	// routes expose query/detail plus safe local cancellation only.
	CreatePaymentOrder(ctx context.Context, input contracts.PaymentOrder) (contracts.PaymentOrder, error)
	BindPaymentOrderCheckout(ctx context.Context, id, providerOrderID string) (contracts.PaymentOrder, error)
	GetPaymentOrder(ctx context.Context, id string) (contracts.PaymentOrder, error)
	ListPaymentOrders(ctx context.Context, filter contracts.PaymentOrderFilter) (contracts.PaymentOrderPage, error)
	ListAuditsByTarget(ctx context.Context, targetType, targetID string) ([]contracts.OperationAudit, error)
	// CancelPendingPaymentOrder atomically transitions a purely local PENDING
	// order with no upstream trade number and appends the supplied explicit audit.
	CancelPendingPaymentOrder(ctx context.Context, id string, audit contracts.OperationAudit) (contracts.PaymentOrder, error)
	GetPaymentOrderByOutTradeNo(ctx context.Context, outTradeNo string) (contracts.PaymentOrder, error)
	// ConfirmRechargePayment atomically records the verified provider event,
	// transitions PENDING -> COMPLETED and credits the wallet with a balanced
	// double-entry journal. Duplicate provider events or already completed orders
	// return the existing result without a second credit.
	ConfirmRechargePayment(ctx context.Context, notification contracts.PaymentNotification, bodyHash string) (contracts.PaymentOrder, contracts.Wallet, bool, error)
	// RecordRejectedPaymentCallback durably records an authenticated provider
	// event that could not be matched to a safe local payment transition. The
	// provider event identity remains idempotent and a body-hash mismatch is a
	// conflict, just like an accepted callback.
	RecordRejectedPaymentCallback(ctx context.Context, event contracts.PaymentCallbackEvent) error
}

// normalizeRouteStrategyRecord canonicalizes the persisted scope key. A plan
// strategy is keyed only by plan_id, a pool strategy only by pool_id, and a user
// strategy only by user_id; unrelated fields must never participate in upsert
// identity or survive in storage.
func normalizeRouteStrategyRecord(input contracts.RouteStrategy) contracts.RouteStrategy {
	input.Scope = input.Scope.Normalize()
	input.Type = input.Type.Normalize()
	switch input.Scope {
	case contracts.StrategyScopePlan:
		input.PoolID = ""
		input.UserID = 0
	case contracts.StrategyScopePool:
		input.PlanID = ""
		input.UserID = 0
	case contracts.StrategyScopeUser:
		input.PlanID = ""
		input.PoolID = ""
	}
	return input
}
