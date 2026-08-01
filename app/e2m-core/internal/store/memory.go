package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/operationalmetrics"
)

// MemoryStore is an in-memory Store implementation for tests and local runs.
// It is safe for concurrent use.
type MemoryStore struct {
	mu                              sync.RWMutex
	now                             func() time.Time
	seq                             map[string]int
	instances                       []contracts.Instance
	monitorPolicies                 []contracts.InstanceMonitorPolicy
	hybridAllocations               map[string]contracts.HybridAllocation
	hybridGatewayBindings           map[string]contracts.HybridGatewayBinding
	hybridRoutingExecutions         []contracts.HybridRoutingExecution
	wallets                         map[string]contracts.Wallet
	walletJournals                  []contracts.WalletJournal
	walletReservations              map[string]contracts.WalletReservation
	virtualKeys                     map[string]contracts.VirtualKey
	supplyEndpoints                 map[string]contracts.SupplyChannelEndpoint
	supplyUsage                     map[string]contracts.SupplyUsageRecord
	paymentCallbackEvents           map[string]contracts.PaymentCallbackEvent
	supplyOffers                    []contracts.SupplyOffer
	supplyLedger                    []contracts.SupplyLedgerEntry
	connectors                      []contracts.Connector
	enrollments                     []contracts.ConnectorEnrollment
	connectorTasks                  []contracts.ConnectorTask
	audits                          []contracts.OperationAudit
	notificationRoutes              []contracts.NotificationRoute
	notificationDeliveries          []contracts.NotificationDelivery
	approvals                       []contracts.ApprovalRequest
	upstreamPools                   []contracts.UpstreamPool
	poolRolloutTargets              []contracts.PoolRolloutTarget
	poolRolloutOps                  []contracts.PoolRolloutOperation
	upstreamChannels                []contracts.UpstreamChannel
	routePlans                      []contracts.RoutePlan
	publishedBindings               []contracts.PublishedBinding
	channelAllocations              map[string]upstreamChannelAllocation
	keyDeliveries                   map[string]contracts.UpstreamKeyDelivery // channel ID -> delivery
	keyProofReceipts                map[string]contracts.UpstreamKeyProofReceipt
	keyDeployments                  map[string]contracts.UpstreamKeyDeployment
	onboardingFlows                 []contracts.OnboardingWorkflow
	reconcileRuns                   []contracts.ReconcileRun
	autoSwitchDecs                  []contracts.AutoSwitchDecision
	qualityCircuits                 []contracts.QualityCircuitRuntime
	routeStrategies                 []contracts.RouteStrategy
	channelObs                      []contracts.ChannelObservation
	channelSnapshots                []contracts.ChannelHealthSnapshot
	upstreamIntelSources            []contracts.UpstreamIntelligenceSource
	upstreamIntelRuns               []contracts.UpstreamCollectionRun
	upstreamIntelWallets            []contracts.UpstreamWalletObservation
	upstreamIntelOffers             []contracts.UpstreamOfferObservation
	upstreamIntelBatches            []UpstreamIntelligenceIngestBatch
	upstreamIntelAbsences           []UpstreamSnapshotAbsence
	upstreamIntelLinks              []contracts.UpstreamIntelligenceLink
	upstreamIntelChanges            []contracts.UpstreamChangeEvent
	upstreamIntelVersions           map[int64]contracts.UpstreamIntelligenceFactVersion
	upstreamIntelFactMutations      map[int64][]UpstreamIntelligenceFactMutation
	upstreamIntelLineageWatermarks  map[int64]int64
	upstreamIntelFinalized          map[string]memoryUpstreamFinalization
	upstreamIntelIngestCapacity     map[upstreamIntelligenceIngestCapacityWindowKey]*upstreamIntelligenceIngestCapacityWindow
	operationalEventCounters        map[operationalEventKind]int64
	operationalMetricCounters       map[operationalMetricKey]int64
	operationalCollectionDurations  map[string]operationalmetrics.DurationSummary
	upstreamCostFacts               []contracts.UpstreamCostFact
	upstreamCostJobs                []UpstreamCostAttributionJob
	upstreamRecommendations         []contracts.UpstreamRecommendation
	upstreamShadowResults           []contracts.UpstreamShadowResult
	upstreamDryRunResults           []contracts.UpstreamDryRunResult
	recommendationRollouts          []contracts.RecommendationRollout
	recommendationRolloutOperations []contracts.RecommendationRolloutOperation
	recommendationExecutionPolicies []contracts.RecommendationExecutionPolicy
	authSettings                    *contracts.AuthSystemSettings
	paymentConfig                   *contracts.PaymentConfig
	paymentProviders                []contracts.PaymentProvider
	paymentOrders                   []contracts.PaymentOrder
	users                           []contracts.User
	sessions                        map[string]contracts.Session // tokenHash -> session
	nextUserID                      int64
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore builds an empty user-scoped store. The first real account
// receives numeric ID 1.
func NewMemoryStore(startedAt time.Time) *MemoryStore {
	s := &MemoryStore{
		now:                            func() time.Time { return time.Now().UTC() },
		seq:                            map[string]int{},
		nextUserID:                     1,
		sessions:                       map[string]contracts.Session{},
		channelAllocations:             make(map[string]upstreamChannelAllocation),
		keyDeliveries:                  make(map[string]contracts.UpstreamKeyDelivery),
		keyProofReceipts:               make(map[string]contracts.UpstreamKeyProofReceipt),
		keyDeployments:                 make(map[string]contracts.UpstreamKeyDeployment),
		hybridAllocations:              make(map[string]contracts.HybridAllocation),
		hybridGatewayBindings:          make(map[string]contracts.HybridGatewayBinding),
		wallets:                        make(map[string]contracts.Wallet),
		walletReservations:             make(map[string]contracts.WalletReservation),
		virtualKeys:                    make(map[string]contracts.VirtualKey),
		supplyEndpoints:                make(map[string]contracts.SupplyChannelEndpoint),
		supplyUsage:                    make(map[string]contracts.SupplyUsageRecord),
		paymentCallbackEvents:          make(map[string]contracts.PaymentCallbackEvent),
		upstreamIntelVersions:          make(map[int64]contracts.UpstreamIntelligenceFactVersion),
		upstreamIntelFactMutations:     make(map[int64][]UpstreamIntelligenceFactMutation),
		upstreamIntelLineageWatermarks: make(map[int64]int64),
		upstreamIntelFinalized:         make(map[string]memoryUpstreamFinalization),
		upstreamIntelIngestCapacity:    make(map[upstreamIntelligenceIngestCapacityWindowKey]*upstreamIntelligenceIngestCapacityWindow),
		operationalEventCounters:       make(map[operationalEventKind]int64),
		operationalMetricCounters:      make(map[operationalMetricKey]int64),
		operationalCollectionDurations: make(map[string]operationalmetrics.DurationSummary),
	}
	return s
}

func (s *MemoryStore) nextID(prefix string) string {
	s.seq[prefix]++
	return fmt.Sprintf("%s-%04d", prefix, s.seq[prefix])
}

func (s *MemoryStore) CreateInstance(ctx context.Context, input contracts.Instance) (contracts.Instance, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Instance{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.ConnectorID != "" {
		for _, existing := range s.instances {
			if existing.ConnectorID == input.ConnectorID {
				return contracts.Instance{}, ErrDuplicate
			}
		}
	}

	now := s.now()
	instance := input
	instance.ID = s.nextID("inst")
	if instance.Status == "" {
		instance.Status = contracts.InstanceStatusUnknown
	}
	instance.CreatedAt = now
	instance.UpdatedAt = now
	s.instances = append(s.instances, instance)
	return instance, nil
}

func (s *MemoryStore) GetInstance(ctx context.Context, id string) (contracts.Instance, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Instance{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, in := range s.instances {
		if in.ID == id {
			return in, nil
		}
	}
	return contracts.Instance{}, ErrNotFound
}

func (s *MemoryStore) UpdateInstance(ctx context.Context, input contracts.Instance) (contracts.Instance, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Instance{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.ConnectorID != "" {
		for _, existing := range s.instances {
			if existing.ID != input.ID && existing.ConnectorID == input.ConnectorID {
				return contracts.Instance{}, ErrDuplicate
			}
		}
	}
	for i := range s.instances {
		if s.instances[i].ID == input.ID {
			input.UserID = s.instances[i].UserID
			input.CreatedAt = s.instances[i].CreatedAt
			input.UpdatedAt = s.now()
			s.instances[i] = input
			return s.instances[i], nil
		}
	}
	return contracts.Instance{}, ErrNotFound
}

func (s *MemoryStore) UpdateInstanceConnector(ctx context.Context, id, connectorID string) (contracts.Instance, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Instance{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var connector *contracts.Connector
	if connectorID != "" {
		for i := range s.connectors {
			if s.connectors[i].ID == connectorID {
				connector = &s.connectors[i]
				break
			}
		}
		if connector == nil {
			return contracts.Instance{}, ErrNotFound
		}
		for _, existing := range s.instances {
			if existing.ID != id && existing.ConnectorID == connectorID {
				return contracts.Instance{}, ErrDuplicate
			}
		}
	}
	for i := range s.instances {
		if s.instances[i].ID == id {
			if connector != nil {
				if connector.Status == contracts.ConnectorStatusRevoked ||
					connector.UserID != s.instances[i].UserID || connector.InstanceID != id {
					return contracts.Instance{}, ErrConflict
				}
			}
			s.instances[i].ConnectorID = connectorID
			s.instances[i].UpdatedAt = s.now()
			return s.instances[i], nil
		}
	}
	return contracts.Instance{}, ErrNotFound
}

func (s *MemoryStore) ListInstances(ctx context.Context, userID int64) ([]contracts.Instance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.Instance, 0, len(s.instances))
	for _, in := range s.instances {
		if userID == 0 || in.UserID == userID {
			out = append(out, in)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateSupplyOffer(ctx context.Context, input contracts.SupplyOffer) (contracts.SupplyOffer, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SupplyOffer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	offer := input
	if offer.ID == "" {
		offer.ID = s.nextID("offer")
	}
	if offer.Status == "" {
		offer.Status = contracts.SupplyOfferStatusPending
	}
	offer.CreatedAt = now
	offer.UpdatedAt = now
	offer = copySupplyOffer(offer)
	s.supplyOffers = append(s.supplyOffers, offer)
	return copySupplyOffer(offer), nil
}

func (s *MemoryStore) ListSupplyOffers(ctx context.Context, supplierUserID int64) ([]contracts.SupplyOffer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.SupplyOffer, 0, len(s.supplyOffers))
	for _, o := range s.supplyOffers {
		if supplierUserID == 0 || o.SupplierUserID == supplierUserID {
			out = append(out, copySupplyOffer(o))
		}
	}
	return out, nil
}

func (s *MemoryStore) GetSupplyOffer(ctx context.Context, id string) (contracts.SupplyOffer, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SupplyOffer{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, o := range s.supplyOffers {
		if o.ID == id {
			return copySupplyOffer(o), nil
		}
	}
	return contracts.SupplyOffer{}, ErrNotFound
}

func (s *MemoryStore) UpdateSupplyOffer(ctx context.Context, input contracts.SupplyOffer) (contracts.SupplyOffer, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SupplyOffer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.supplyOffers {
		existing := s.supplyOffers[i]
		if existing.ID != input.ID {
			continue
		}
		if existing.Status == contracts.SupplyOfferStatusRevoked {
			return contracts.SupplyOffer{}, ErrConflict
		}
		updated := copySupplyOffer(input)
		updated.ID = existing.ID
		updated.SupplierUserID = existing.SupplierUserID
		updated.Status = existing.Status
		updated.CreatedAt = existing.CreatedAt
		updated.UpdatedAt = s.now()
		s.supplyOffers[i] = updated
		return copySupplyOffer(updated), nil
	}
	return contracts.SupplyOffer{}, ErrNotFound
}

func (s *MemoryStore) UpdateSupplyOfferStatus(ctx context.Context, id string, status contracts.SupplyOfferStatus) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.supplyOffers {
		if s.supplyOffers[i].ID == id {
			s.supplyOffers[i].Status = status
			s.supplyOffers[i].UpdatedAt = s.now()
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) RevokeSupplyOffer(ctx context.Context, id string) (contracts.SupplyOffer, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SupplyOffer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.supplyOffers {
		if s.supplyOffers[i].ID != id {
			continue
		}
		if s.supplyOffers[i].Status == contracts.SupplyOfferStatusRevoked {
			return copySupplyOffer(s.supplyOffers[i]), nil
		}
		for _, entry := range s.supplyLedger {
			if entry.OfferID == id && entry.Status == contracts.SupplyLedgerAllocated {
				return contracts.SupplyOffer{}, ErrConflict
			}
		}
		s.supplyOffers[i].Status = contracts.SupplyOfferStatusRevoked
		s.supplyOffers[i].UpdatedAt = s.now()
		return copySupplyOffer(s.supplyOffers[i]), nil
	}
	return contracts.SupplyOffer{}, ErrNotFound
}

func (s *MemoryStore) AllocateSupplyOffer(ctx context.Context, input contracts.SupplyLedgerEntry) (contracts.SupplyLedgerEntry, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SupplyLedgerEntry{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	offerIndex := -1
	for i := range s.supplyOffers {
		if s.supplyOffers[i].ID == input.OfferID {
			offerIndex = i
			break
		}
	}
	if offerIndex < 0 {
		return contracts.SupplyLedgerEntry{}, ErrNotFound
	}
	offer := s.supplyOffers[offerIndex]
	if offer.SupplierUserID != input.SupplierUserID || offer.Status == contracts.SupplyOfferStatusRevoked {
		return contracts.SupplyLedgerEntry{}, ErrConflict
	}
	for _, existing := range s.supplyLedger {
		if existing.OfferID == input.OfferID && existing.InstanceID == input.InstanceID && existing.Status == contracts.SupplyLedgerAllocated {
			return contracts.SupplyLedgerEntry{}, ErrDuplicate
		}
	}

	now := s.now()
	entry := input
	if entry.ID == "" {
		entry.ID = s.nextID("ledger")
	}
	entry.Status = contracts.SupplyLedgerAllocated
	entry.CreatedAt = now
	entry.UpdatedAt = now
	s.supplyLedger = append(s.supplyLedger, entry)
	if offer.Status == contracts.SupplyOfferStatusPending {
		s.supplyOffers[offerIndex].Status = contracts.SupplyOfferStatusActive
		s.supplyOffers[offerIndex].UpdatedAt = now
	}
	return entry, nil
}

func (s *MemoryStore) AppendSupplyLedger(ctx context.Context, input contracts.SupplyLedgerEntry) (contracts.SupplyLedgerEntry, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SupplyLedgerEntry{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	offerFound := false
	for _, offer := range s.supplyOffers {
		if offer.ID != input.OfferID {
			continue
		}
		offerFound = true
		if offer.SupplierUserID != input.SupplierUserID || offer.Status == contracts.SupplyOfferStatusRevoked {
			return contracts.SupplyLedgerEntry{}, ErrConflict
		}
		break
	}
	if !offerFound {
		return contracts.SupplyLedgerEntry{}, ErrNotFound
	}

	now := s.now()
	entry := input
	if entry.ID == "" {
		entry.ID = s.nextID("ledger")
	}
	if entry.Status == "" {
		entry.Status = contracts.SupplyLedgerAllocated
	}
	entry.CreatedAt = now
	entry.UpdatedAt = now
	s.supplyLedger = append(s.supplyLedger, entry)
	return entry, nil
}

func (s *MemoryStore) UpdateSupplyLedgerStatus(ctx context.Context, id string, status contracts.SupplyLedgerEntryStatus, note string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.supplyLedger {
		if s.supplyLedger[i].ID == id {
			s.supplyLedger[i].Status = status
			if note != "" {
				s.supplyLedger[i].Note = note
			}
			s.supplyLedger[i].UpdatedAt = s.now()
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) ListSupplyLedger(ctx context.Context, offerID string) ([]contracts.SupplyLedgerEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.SupplyLedgerEntry, 0, len(s.supplyLedger))
	for _, e := range s.supplyLedger {
		if offerID == "" || e.OfferID == offerID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateConnectorEnrollment(ctx context.Context, input contracts.ConnectorEnrollment) (contracts.ConnectorEnrollment, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ConnectorEnrollment{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	enrollment := input
	if enrollment.InstanceID == "" {
		return contracts.ConnectorEnrollment{}, ErrConflict
	}
	instanceIndex := -1
	for i := range s.instances {
		if s.instances[i].ID == enrollment.InstanceID {
			instanceIndex = i
			break
		}
	}
	if instanceIndex < 0 {
		return contracts.ConnectorEnrollment{}, ErrNotFound
	}
	if s.instances[instanceIndex].UserID != enrollment.UserID {
		return contracts.ConnectorEnrollment{}, ErrConflict
	}
	if enrollment.ID == "" {
		enrollment.ID = s.nextID("cenroll")
	}
	if enrollment.ConnectorID == "" {
		enrollment.ConnectorID = s.nextID("conn")
	}
	for _, connector := range s.connectors {
		if connector.ID == enrollment.ConnectorID &&
			(connector.UserID != enrollment.UserID || connector.InstanceID != enrollment.InstanceID) {
			return contracts.ConnectorEnrollment{}, ErrConflict
		}
		if connector.InstanceID == enrollment.InstanceID && connector.ID != enrollment.ConnectorID {
			return contracts.ConnectorEnrollment{}, ErrConflict
		}
	}
	if enrollment.ExpiresAt.IsZero() {
		enrollment.ExpiresAt = now.Add(24 * time.Hour)
	}
	if !enrollment.ExpiresAt.After(now) {
		return contracts.ConnectorEnrollment{}, ErrConflict
	}
	remainingEnrollments := make([]contracts.ConnectorEnrollment, 0, len(s.enrollments))
	for _, existing := range s.enrollments {
		if existing.UsedAt == nil &&
			(existing.InstanceID == enrollment.InstanceID && existing.UserID == enrollment.UserID ||
				!existing.ExpiresAt.After(now) && existing.ConnectorID == enrollment.ConnectorID) {
			continue
		}
		remainingEnrollments = append(remainingEnrollments, existing)
	}
	for _, existing := range remainingEnrollments {
		if existing.UsedAt == nil &&
			(existing.ConnectorID == enrollment.ConnectorID || existing.InstanceID == enrollment.InstanceID) {
			return contracts.ConnectorEnrollment{}, ErrDuplicate
		}
	}
	enrollment.CreatedAt = now
	s.enrollments = append(remainingEnrollments, enrollment)
	return copyConnectorEnrollment(enrollment), nil
}

func (s *MemoryStore) UseConnectorEnrollment(ctx context.Context, tokenHash string, input contracts.Connector) (contracts.Connector, contracts.ConnectorEnrollment, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Connector{}, contracts.ConnectorEnrollment{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for i := range s.enrollments {
		enrollment := &s.enrollments[i]
		if enrollment.TokenHash != tokenHash {
			continue
		}
		if enrollment.UsedAt != nil || !enrollment.ExpiresAt.After(now) {
			return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrConflict
		}
		if !s.enabledClientUserLocked(enrollment.UserID) {
			return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrConflict
		}
		connector := input
		if connector.ID != "" && connector.ID != enrollment.ConnectorID {
			return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrConflict
		}
		if connector.InstanceID != "" && connector.InstanceID != enrollment.InstanceID {
			return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrConflict
		}
		if !contracts.IsConnectorVersion(connector.Version) || connector.ProtocolVersion != contracts.ConnectorProtocolVersion {
			return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrConflict
		}
		connector.ID = enrollment.ConnectorID
		connector.UserID = enrollment.UserID
		connector.InstanceID = enrollment.InstanceID
		if connector.Name == "" {
			connector.Name = enrollment.Name
		}
		connector.Gateway = contracts.SanitizeConnectorRuntimeState(connector.Gateway)
		connector.Status = contracts.ConnectorStatusOnline
		connector.LastSeenAt = &now
		connector.RevokedAt = nil
		instanceIndex := -1
		for idx := range s.instances {
			if s.instances[idx].ID == enrollment.InstanceID {
				instanceIndex = idx
				break
			}
		}
		if instanceIndex < 0 || s.instances[instanceIndex].UserID != enrollment.UserID {
			return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrConflict
		}
		if existingID := s.instances[instanceIndex].ConnectorID; existingID != "" && existingID != connector.ID {
			return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrConflict
		}
		found := false
		for idx := range s.connectors {
			if s.connectors[idx].ID == connector.ID {
				if s.connectors[idx].UserID != enrollment.UserID || s.connectors[idx].InstanceID != enrollment.InstanceID {
					return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrConflict
				}
				connector.CreatedAt = s.connectors[idx].CreatedAt
				connector.UpdatedAt = now
				s.connectors[idx] = connector
				found = true
				break
			}
		}
		if !found {
			connector.CreatedAt = now
			connector.UpdatedAt = now
			s.connectors = append(s.connectors, connector)
		}
		usedAt := now
		enrollment.UsedAt = &usedAt
		s.instances[instanceIndex].ConnectorID = connector.ID
		s.instances[instanceIndex].UpdatedAt = now
		return copyConnector(connector), copyConnectorEnrollment(*enrollment), nil
	}
	return contracts.Connector{}, contracts.ConnectorEnrollment{}, ErrNotFound
}

func (s *MemoryStore) ListConnectors(ctx context.Context, filter contracts.ConnectorFilter) ([]contracts.Connector, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.Connector, 0, len(s.connectors))
	for _, connector := range s.connectors {
		if filter.UserID != 0 && connector.UserID != filter.UserID {
			continue
		}
		if filter.InstanceID != "" && connector.InstanceID != filter.InstanceID {
			continue
		}
		if filter.Status != "" && connector.Status != filter.Status {
			continue
		}
		out = append(out, copyConnector(connector))
	}
	return out, nil
}

func (s *MemoryStore) GetConnector(ctx context.Context, id string) (contracts.Connector, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Connector{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, connector := range s.connectors {
		if connector.ID == id {
			return copyConnector(connector), nil
		}
	}
	return contracts.Connector{}, ErrNotFound
}

func (s *MemoryStore) GetConnectorByTokenHash(ctx context.Context, tokenHash string) (contracts.Connector, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Connector{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, connector := range s.connectors {
		user, found := s.connectorUserLocked(connector.UserID)
		if connector.TokenHash == tokenHash && connector.Status != contracts.ConnectorStatusRevoked && found &&
			(activeClientUser(user) || deactivatingUser(user)) {
			return copyConnector(connector), nil
		}
	}
	return contracts.Connector{}, ErrNotFound
}

func (s *MemoryStore) RecordConnectorSeen(ctx context.Context, id, version string, runtime contracts.ConnectorRuntimeState) (contracts.Connector, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Connector{}, err
	}
	if !contracts.IsConnectorVersion(version) || runtime.ProtocolVersion != contracts.ConnectorProtocolVersion {
		return contracts.Connector{}, ErrConflict
	}
	runtime = contracts.SanitizeConnectorRuntimeState(runtime)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for i := range s.connectors {
		if s.connectors[i].ID == id {
			user, found := s.connectorUserLocked(s.connectors[i].UserID)
			if s.connectors[i].Status == contracts.ConnectorStatusRevoked || !found ||
				(!activeClientUser(user) && !deactivatingUser(user)) {
				return contracts.Connector{}, ErrConflict
			}
			stateChanged := s.connectors[i].Status != contracts.ConnectorStatusOnline ||
				s.connectors[i].Version != version ||
				s.connectors[i].ProtocolVersion != contracts.ConnectorProtocolVersion ||
				!reflect.DeepEqual(s.connectors[i].Gateway, runtime)
			seenRecently := s.connectors[i].LastSeenAt != nil && now.Sub(*s.connectors[i].LastSeenAt) < 15*time.Second
			if !stateChanged && seenRecently {
				return copyConnector(s.connectors[i]), nil
			}
			s.connectors[i].Status = contracts.ConnectorStatusOnline
			s.connectors[i].Version = version
			s.connectors[i].ProtocolVersion = contracts.ConnectorProtocolVersion
			s.connectors[i].Gateway = runtime
			s.connectors[i].LastSeenAt = &now
			s.connectors[i].UpdatedAt = now
			return copyConnector(s.connectors[i]), nil
		}
	}
	return contracts.Connector{}, ErrNotFound
}

func (s *MemoryStore) enabledClientUserLocked(userID int64) bool {
	user, found := s.connectorUserLocked(userID)
	return found && activeClientUser(user)
}

func (s *MemoryStore) connectorUserLocked(userID int64) (contracts.User, bool) {
	for _, user := range s.users {
		if user.ID == userID {
			return user, true
		}
	}
	return contracts.User{}, false
}

func (s *MemoryStore) UpdateConnectorToken(ctx context.Context, id, tokenHash string) (contracts.Connector, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Connector{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.connectors {
		if s.connectors[i].ID == id {
			if s.connectors[i].Status == contracts.ConnectorStatusRevoked {
				return contracts.Connector{}, ErrConflict
			}
			user, found := s.connectorUserLocked(s.connectors[i].UserID)
			if !found || !activeClientUser(user) {
				return contracts.Connector{}, ErrConflict
			}
			s.connectors[i].TokenHash = tokenHash
			s.connectors[i].Status = contracts.ConnectorStatusOffline
			s.connectors[i].UpdatedAt = s.now()
			return copyConnector(s.connectors[i]), nil
		}
	}
	return contracts.Connector{}, ErrNotFound
}

func (s *MemoryStore) RevokeConnector(ctx context.Context, id string) (contracts.Connector, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Connector{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for i := range s.connectors {
		if s.connectors[i].ID == id {
			user, found := s.connectorUserLocked(s.connectors[i].UserID)
			if found && deactivatingUser(user) {
				return contracts.Connector{}, ErrConflict
			}
			s.connectors[i].Status = contracts.ConnectorStatusRevoked
			s.connectors[i].TokenHash = ""
			s.connectors[i].RevokedAt = &now
			s.connectors[i].UpdatedAt = now
			for instanceIndex := range s.instances {
				if s.instances[instanceIndex].ID == s.connectors[i].InstanceID &&
					s.instances[instanceIndex].ConnectorID == id {
					s.instances[instanceIndex].ConnectorID = ""
					s.instances[instanceIndex].UpdatedAt = now
					break
				}
			}
			return copyConnector(s.connectors[i]), nil
		}
	}
	return contracts.Connector{}, ErrNotFound
}

func (s *MemoryStore) CreateConnectorTask(ctx context.Context, input contracts.ConnectorTask) (contracts.ConnectorTask, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ConnectorTask{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	task := input
	if task.ConnectorID == "" || task.InstanceID == "" || task.UserID == 0 ||
		task.Type.RiskLevel() == contracts.RiskLevelL3 || !validStoredConnectorTaskError(task.Error) {
		return contracts.ConnectorTask{}, ErrConflict
	}
	if err := normalizeConnectorTaskPlanFence(&task); err != nil {
		return contracts.ConnectorTask{}, err
	}
	var connector *contracts.Connector
	for i := range s.connectors {
		if s.connectors[i].ID == task.ConnectorID {
			connector = &s.connectors[i]
			break
		}
	}
	if connector == nil {
		return contracts.ConnectorTask{}, ErrNotFound
	}
	if connector.Status == contracts.ConnectorStatusRevoked || connector.UserID != task.UserID || connector.InstanceID != task.InstanceID {
		return contracts.ConnectorTask{}, ErrConflict
	}
	if task.PlanID != "" {
		planIndex := s.routePlanIndex(task.PlanID)
		if planIndex < 0 {
			return contracts.ConnectorTask{}, ErrNotFound
		}
		if !connectorTaskMatchesCurrentPlan(task, s.routePlans[planIndex]) {
			return contracts.ConnectorTask{}, ErrConflict
		}
	} else if task.ExecutionScope != "" && !memoryConnectorTaskMatchesCurrentHybridExecutionLocked(s, task) {
		return contracts.ConnectorTask{}, ErrConflict
	}
	user, found := s.connectorUserLocked(task.UserID)
	if !found || (!activeClientUser(user) && (!deactivatingUser(user) || !connectorTaskAllowedDuringUserDeactivation(task))) {
		return contracts.ConnectorTask{}, ErrConflict
	}
	if task.IdempotencyKey != "" {
		for i := range s.connectorTasks {
			existing := &s.connectorTasks[i]
			if existing.ConnectorID != task.ConnectorID || existing.IdempotencyKey != task.IdempotencyKey ||
				(existing.Status != contracts.ConnectorTaskPending && existing.Status != contracts.ConnectorTaskLeased &&
					existing.Status != contracts.ConnectorTaskExecuting) {
				continue
			}
			if expireConnectorTask(existing, now) {
				continue
			}
			// Keep the in-memory store aligned with PostgreSQL's protocol-v3
			// partial unique index: pending, leased, and executing all reserve the
			// same connector/idempotency identity. In particular, executing never
			// expires automatically and must not admit a second remote intent.
			return contracts.ConnectorTask{}, ErrDuplicate
		}
	}
	if task.ID == "" {
		task.ID = s.nextID("ctask")
	}
	if task.SchemaVersion <= 0 {
		task.SchemaVersion = 1
	}
	task.RiskLevel = task.Type.RiskLevel()
	if task.Status == "" {
		task.Status = contracts.ConnectorTaskPending
	}
	if task.MaxAttempts <= 0 {
		task.MaxAttempts = 3
	}
	if task.AvailableAt.IsZero() {
		task.AvailableAt = now
	}
	if task.ExpiresAt.IsZero() {
		task.ExpiresAt = now.Add(10 * time.Minute)
	}
	task.CreatedAt = now
	task.UpdatedAt = now
	s.connectorTasks = append(s.connectorTasks, task)
	return copyConnectorTask(task), nil
}

func (s *MemoryStore) GetConnectorTask(ctx context.Context, id string) (contracts.ConnectorTask, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ConnectorTask{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.connectorTasks {
		if s.connectorTasks[i].ID == id {
			expireConnectorTask(&s.connectorTasks[i], s.now())
			return copyConnectorTask(s.connectorTasks[i]), nil
		}
	}
	return contracts.ConnectorTask{}, ErrNotFound
}

func (s *MemoryStore) ListConnectorTasks(ctx context.Context, filter contracts.ConnectorTaskFilter) ([]contracts.ConnectorTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	out := make([]contracts.ConnectorTask, 0)
	types := make(map[contracts.ConnectorTaskType]struct{}, len(filter.Types))
	for _, taskType := range filter.Types {
		types[taskType] = struct{}{}
	}
	for i := len(s.connectorTasks) - 1; i >= 0 && len(out) < limit; i-- {
		task := s.connectorTasks[i]
		if filter.UserID != 0 && task.UserID != filter.UserID {
			continue
		}
		if filter.InstanceID != "" && task.InstanceID != filter.InstanceID {
			continue
		}
		if filter.ConnectorID != "" && task.ConnectorID != filter.ConnectorID {
			continue
		}
		if filter.Status != "" && task.Status != filter.Status {
			continue
		}
		if len(types) > 0 {
			if _, ok := types[task.Type]; !ok {
				continue
			}
		}
		out = append(out, copyConnectorTask(task))
	}
	return out, nil
}

func (s *MemoryStore) LeaseConnectorTasks(ctx context.Context, req contracts.ConnectorTaskLeaseRequest) ([]contracts.ConnectorTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.ConnectorID == "" {
		return nil, ErrNotFound
	}
	connectorEnabled := false
	connectorDeactivating := false
	for _, connector := range s.connectors {
		if connector.ID == req.ConnectorID && connector.Status != contracts.ConnectorStatusRevoked &&
			connector.ProtocolVersion == contracts.ConnectorProtocolVersion {
			user, found := s.connectorUserLocked(connector.UserID)
			if found {
				connectorEnabled = activeClientUser(user) || deactivatingUser(user)
				connectorDeactivating = deactivatingUser(user)
			}
			break
		}
	}
	if !connectorEnabled {
		return nil, ErrConflict
	}
	maxTasks := req.MaxTasks
	if maxTasks <= 0 || maxTasks > 10 {
		maxTasks = 1
	}
	leaseSeconds := req.LeaseSeconds
	if leaseSeconds <= 0 {
		leaseSeconds = 30
	}
	if leaseSeconds > 300 {
		leaseSeconds = 300
	}
	now := s.now()
	requestedLeaseUntil := now.Add(time.Duration(leaseSeconds) * time.Second)
	type leaseCandidate struct {
		index int
		nonce string
	}
	candidates := make([]leaseCandidate, 0, maxTasks)
	for i := range s.connectorTasks {
		task := &s.connectorTasks[i]
		if task.ConnectorID != req.ConnectorID {
			continue
		}
		if task.AvailableAt.After(now) {
			continue
		}
		if task.PlanID != "" {
			planIndex := s.routePlanIndex(task.PlanID)
			if planIndex < 0 || !connectorTaskMatchesCurrentPlan(*task, s.routePlans[planIndex]) {
				supersedeConnectorTask(task, now)
				continue
			}
		} else if task.ExecutionScope != "" && !memoryConnectorTaskMatchesCurrentHybridExecutionLocked(s, *task) {
			supersedeConnectorTask(task, now)
			continue
		}
		if connectorDeactivating && !connectorTaskAllowedDuringUserDeactivation(*task) {
			continue
		}
		if expireConnectorTask(task, now) {
			continue
		}
		if task.Status == contracts.ConnectorTaskLeased && task.LeaseUntil != nil && task.LeaseUntil.After(now) {
			continue
		}
		if task.Status != contracts.ConnectorTaskPending && task.Status != contracts.ConnectorTaskLeased {
			continue
		}
		if task.Attempts >= task.MaxAttempts {
			task.Status = contracts.ConnectorTaskFailed
			task.Error = contracts.ConnectorTaskError{Code: "max_attempts_exceeded"}
			task.LeaseOwner = ""
			task.LeaseNonce = ""
			task.LeaseUntil = nil
			task.UpdatedAt = now
			continue
		}
		if len(candidates) >= maxTasks {
			continue
		}
		leaseNonce, err := newConnectorTaskLeaseNonce()
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, leaseCandidate{index: i, nonce: leaseNonce})
	}
	out := make([]contracts.ConnectorTask, 0, len(candidates))
	for _, candidate := range candidates {
		task := &s.connectorTasks[candidate.index]
		leaseUntil := requestedLeaseUntil
		if task.ExpiresAt.Before(leaseUntil) {
			leaseUntil = task.ExpiresAt
		}
		task.Status = contracts.ConnectorTaskLeased
		task.LeaseOwner = req.ConnectorID
		task.LeaseNonce = candidate.nonce
		task.LeaseUntil = &leaseUntil
		task.Attempts++
		task.UpdatedAt = now
		out = append(out, copyConnectorTask(*task))
	}
	return out, nil
}

func (s *MemoryStore) BeginConnectorTaskExecution(ctx context.Context, id string, req contracts.ConnectorTaskExecutionRequest) (contracts.ConnectorTask, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ConnectorTask{}, err
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(req.ConnectorID) == "" || strings.TrimSpace(req.LeaseNonce) == "" {
		return contracts.ConnectorTask{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for index := range s.connectorTasks {
		task := &s.connectorTasks[index]
		if task.ID != id {
			continue
		}
		if task.ConnectorID != req.ConnectorID || task.LeaseOwner != req.ConnectorID || task.LeaseNonce != req.LeaseNonce ||
			!connectorTaskRequiresExecutionPermit(*task) {
			return contracts.ConnectorTask{}, ErrConflict
		}
		// A retry after Core committed the permit but its response was lost is
		// safe and idempotent for the exact same execution identity.
		if task.Status == contracts.ConnectorTaskExecuting {
			return copyConnectorTask(*task), nil
		}
		if task.Status != contracts.ConnectorTaskLeased || task.LeaseUntil == nil || !task.LeaseUntil.After(now) ||
			task.ExpiresAt.IsZero() || !task.ExpiresAt.After(now) {
			return contracts.ConnectorTask{}, ErrConflict
		}
		if task.PlanID != "" {
			planIndex := s.routePlanIndex(task.PlanID)
			if planIndex < 0 || !connectorTaskMatchesCurrentPlan(*task, s.routePlans[planIndex]) {
				supersedeConnectorTask(task, now)
				return contracts.ConnectorTask{}, ErrConflict
			}
		} else if !memoryConnectorTaskMatchesCurrentHybridExecutionLocked(s, *task) {
			supersedeConnectorTask(task, now)
			return contracts.ConnectorTask{}, ErrConflict
		}
		connectorFound := false
		for connectorIndex := range s.connectors {
			connector := s.connectors[connectorIndex]
			if connector.ID != req.ConnectorID {
				continue
			}
			connectorFound = true
			if connector.Status == contracts.ConnectorStatusRevoked || connector.ProtocolVersion != contracts.ConnectorProtocolVersion ||
				connector.UserID != task.UserID || connector.InstanceID != task.InstanceID {
				return contracts.ConnectorTask{}, ErrConflict
			}
			break
		}
		if !connectorFound {
			return contracts.ConnectorTask{}, ErrNotFound
		}
		user, found := s.connectorUserLocked(task.UserID)
		if !found || (!activeClientUser(user) && (!deactivatingUser(user) || !connectorTaskAllowedDuringUserDeactivation(*task))) {
			return contracts.ConnectorTask{}, ErrConflict
		}
		task.Status = contracts.ConnectorTaskExecuting
		task.UpdatedAt = now
		// Lease identity and its original deadline are retained as immutable
		// execution-attempt audit data. executing never expires automatically.
		return copyConnectorTask(*task), nil
	}
	return contracts.ConnectorTask{}, ErrNotFound
}

func (s *MemoryStore) CompleteConnectorTask(ctx context.Context, id string, req contracts.ConnectorTaskCompleteRequest) (contracts.ConnectorTask, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ConnectorTask{}, err
	}
	if !validConnectorCompletionError(req) {
		return contracts.ConnectorTask{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for i := range s.connectorTasks {
		task := &s.connectorTasks[i]
		if task.ID != id {
			continue
		}
		if task.ConnectorID != req.ConnectorID {
			return contracts.ConnectorTask{}, ErrConflict
		}
		requiresPermit := connectorTaskRequiresExecutionPermit(*task)
		if task.PlanID != "" {
			planIndex := s.routePlanIndex(task.PlanID)
			if planIndex < 0 || !connectorTaskMatchesCurrentPlan(*task, s.routePlans[planIndex]) {
				supersedeConnectorTask(task, now)
				return contracts.ConnectorTask{}, ErrConflict
			}
		} else if task.ExecutionScope != "" && !memoryConnectorTaskMatchesCurrentHybridExecutionLocked(s, *task) {
			supersedeConnectorTask(task, now)
			return contracts.ConnectorTask{}, ErrConflict
		}
		user, found := s.connectorUserLocked(task.UserID)
		if !found || (!activeClientUser(user) && (!deactivatingUser(user) || !connectorTaskAllowedDuringUserDeactivation(*task))) {
			return contracts.ConnectorTask{}, ErrConflict
		}
		if !requiresPermit && expireConnectorTask(task, now) {
			return contracts.ConnectorTask{}, ErrConflict
		}
		validState := requiresPermit && task.Status == contracts.ConnectorTaskExecuting ||
			!requiresPermit && task.Status == contracts.ConnectorTaskLeased
		validDeadline := requiresPermit || task.LeaseUntil != nil && task.LeaseUntil.After(now)
		if req.LeaseNonce == "" || !validState || !validDeadline ||
			task.LeaseOwner != req.ConnectorID || task.LeaseNonce != req.LeaseNonce {
			return contracts.ConnectorTask{}, ErrConflict
		}
		// Protocol v3 has no outcome-known flag. Retrying an executing remote
		// mutation would release the plan freeze and could duplicate an operation
		// whose result is uncertain, so only terminal success/failure may resolve
		// an execution permit. The Agent keeps uncertain outcomes executing for
		// explicit operator reconciliation.
		if requiresPermit && !req.Success && req.Error.Retryable {
			return contracts.ConnectorTask{}, ErrConflict
		}
		if requiresPermit && req.Success && !validResolvedConnectorTaskResult(*task, req.Result) {
			return contracts.ConnectorTask{}, ErrConflict
		}
		task.Result = append(task.Result[:0], req.Result...)
		task.Error = req.Error
		task.LeaseOwner = ""
		task.LeaseNonce = ""
		task.LeaseUntil = nil
		task.UpdatedAt = now
		if req.Success {
			task.Status = contracts.ConnectorTaskSucceeded
		} else if !req.Error.Retryable || task.Attempts >= task.MaxAttempts {
			task.Result = nil
			task.Status = contracts.ConnectorTaskFailed
		} else {
			task.Result = nil
			task.Status = contracts.ConnectorTaskPending
			task.AvailableAt = now.Add(connectorTaskRetryDelay(task.Attempts))
		}
		return copyConnectorTask(*task), nil
	}
	return contracts.ConnectorTask{}, ErrNotFound
}

func (s *MemoryStore) ResolveConnectorTaskExecution(
	ctx context.Context,
	id string,
	req contracts.ConnectorTaskExecutionResolveRequest,
	audit contracts.OperationAudit,
) (contracts.ConnectorTask, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ConnectorTask{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for index := range s.connectorTasks {
		task := &s.connectorTasks[index]
		if task.ID != id {
			continue
		}
		if !validConnectorTaskExecutionResolution(*task, req) || !validConnectorTaskResolutionAudit(*task, req, audit) {
			return contracts.ConnectorTask{}, ErrConflict
		}
		if req.Resolution == contracts.ConnectorTaskExecutionRevokedUnverifiable {
			revoked := false
			for connectorIndex := range s.connectors {
				connector := s.connectors[connectorIndex]
				if connector.ID == task.ConnectorID {
					revoked = connector.Status == contracts.ConnectorStatusRevoked
					break
				}
			}
			if !revoked {
				return contracts.ConnectorTask{}, ErrConflict
			}
		}
		switch req.Resolution {
		case contracts.ConnectorTaskExecutionConfirmedApplied:
			task.Status = contracts.ConnectorTaskSucceeded
			task.Result = append(json.RawMessage(nil), req.Result...)
			task.Error = contracts.ConnectorTaskError{}
		case contracts.ConnectorTaskExecutionConfirmedNotApplied:
			task.Status = contracts.ConnectorTaskFailed
			task.Result = nil
			task.Error = contracts.ConnectorTaskError{Code: "execution_abandoned"}
		case contracts.ConnectorTaskExecutionRevokedUnverifiable:
			task.Status = contracts.ConnectorTaskFailed
			task.Result = nil
			task.Error = contracts.ConnectorTaskError{Code: "execution_outcome_unknown"}
		default:
			return contracts.ConnectorTask{}, ErrConflict
		}
		task.LeaseOwner = ""
		task.LeaseNonce = ""
		task.LeaseUntil = nil
		task.UpdatedAt = now
		resolved := copyConnectorTask(*task)
		audit.ID = s.nextID("audit")
		audit.CreatedAt = now
		audit.Details = copyStringMap(audit.Details)
		s.audits = append(s.audits, audit)
		return resolved, nil
	}
	return contracts.ConnectorTask{}, ErrNotFound
}

func (s *MemoryStore) AppendAudit(ctx context.Context, input contracts.OperationAudit) (contracts.OperationAudit, error) {
	if err := ctx.Err(); err != nil {
		return contracts.OperationAudit{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	audit := input
	if !audit.EventLevel.Valid() {
		audit.EventLevel = contracts.DefaultEventLevel(audit.RiskLevel, audit.Result)
	}
	if audit.ID == "" {
		audit.ID = s.nextID("audit")
	}
	if audit.CreatedAt.IsZero() {
		audit.CreatedAt = s.now()
	}
	s.audits = append(s.audits, audit)
	return audit, nil
}

func (s *MemoryStore) ListAudits(ctx context.Context, userID int64) ([]contracts.OperationAudit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.OperationAudit, 0, len(s.audits))
	for _, a := range s.audits {
		if userID == 0 || a.UserID == userID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateNotificationRoute(ctx context.Context, input contracts.NotificationRoute) (contracts.NotificationRoute, error) {
	if err := ctx.Err(); err != nil {
		return contracts.NotificationRoute{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	route := input
	if !route.MinEventLevel.Valid() {
		route.MinEventLevel = contracts.EventLevel(route.MinRiskLevel)
	}
	if route.ID == "" {
		route.ID = s.nextID("route")
	}
	route.CreatedAt = now
	route.UpdatedAt = now
	s.notificationRoutes = append(s.notificationRoutes, route)
	return route, nil
}

func (s *MemoryStore) GetNotificationRoute(ctx context.Context, id string) (contracts.NotificationRoute, error) {
	if err := ctx.Err(); err != nil {
		return contracts.NotificationRoute{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, route := range s.notificationRoutes {
		if route.ID == id {
			return route, nil
		}
	}
	return contracts.NotificationRoute{}, ErrNotFound
}

func (s *MemoryStore) UpdateNotificationRoute(ctx context.Context, input contracts.NotificationRoute) (contracts.NotificationRoute, error) {
	if err := ctx.Err(); err != nil {
		return contracts.NotificationRoute{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, r := range s.notificationRoutes {
		if r.ID == input.ID {
			updated := input
			if !updated.MinEventLevel.Valid() {
				updated.MinEventLevel = contracts.EventLevel(updated.MinRiskLevel)
			}
			updated.UserID = r.UserID
			updated.CreatedAt = r.CreatedAt
			updated.UpdatedAt = s.now()
			s.notificationRoutes[i] = updated
			return updated, nil
		}
	}
	return contracts.NotificationRoute{}, ErrNotFound
}

func (s *MemoryStore) DeleteNotificationRoute(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, r := range s.notificationRoutes {
		if r.ID == id {
			s.notificationRoutes = append(s.notificationRoutes[:i], s.notificationRoutes[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) ListNotificationRoutes(ctx context.Context, userID int64) ([]contracts.NotificationRoute, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.NotificationRoute, 0, len(s.notificationRoutes))
	for _, r := range s.notificationRoutes {
		if userID == 0 || r.UserID == userID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateApproval(ctx context.Context, input contracts.ApprovalRequest) (contracts.ApprovalRequest, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ApprovalRequest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	ap := input
	if ap.ID == "" {
		ap.ID = s.nextID("approval")
	}
	if ap.Status == "" {
		ap.Status = contracts.ApprovalPending
	}
	ap.CreatedAt = now
	ap.UpdatedAt = now
	s.approvals = append(s.approvals, ap)
	return ap, nil
}

func (s *MemoryStore) GetApproval(ctx context.Context, id string) (contracts.ApprovalRequest, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ApprovalRequest{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.approvals {
		if a.ID == id {
			return a, nil
		}
	}
	return contracts.ApprovalRequest{}, ErrNotFound
}

func (s *MemoryStore) UpdateApproval(ctx context.Context, input contracts.ApprovalRequest) (contracts.ApprovalRequest, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ApprovalRequest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, a := range s.approvals {
		if a.ID == input.ID {
			updated := input
			updated.CreatedAt = a.CreatedAt
			updated.UpdatedAt = s.now()
			s.approvals[i] = updated
			return updated, nil
		}
	}
	return contracts.ApprovalRequest{}, ErrNotFound
}

func (s *MemoryStore) TransitionApproval(ctx context.Context, input contracts.ApprovalRequest, expected contracts.ApprovalStatus) (contracts.ApprovalRequest, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ApprovalRequest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, current := range s.approvals {
		if current.ID != input.ID {
			continue
		}
		if current.Status != expected {
			return contracts.ApprovalRequest{}, ErrConflict
		}
		updated := input
		updated.CreatedAt = current.CreatedAt
		updated.UpdatedAt = s.now()
		s.approvals[i] = updated
		return updated, nil
	}
	return contracts.ApprovalRequest{}, ErrNotFound
}

func (s *MemoryStore) ListApprovals(ctx context.Context, userID int64, status contracts.ApprovalStatus) ([]contracts.ApprovalRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.ApprovalRequest, 0, len(s.approvals))
	for _, a := range s.approvals {
		if (userID == 0 || a.UserID == userID) && (status == "" || a.Status == status) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateUser(ctx context.Context, input contracts.User) (contracts.User, error) {
	if err := ctx.Err(); err != nil {
		return contracts.User{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.Email == input.Email {
			return contracts.User{}, ErrDuplicate
		}
	}
	now := s.now()
	user := input
	user.DeactivationStatus = normalizeUserDeactivationStatus(user.DeactivationStatus)
	if user.ID == 0 {
		user.ID = s.nextUserID
		s.nextUserID++
	} else if user.ID >= s.nextUserID {
		s.nextUserID = user.ID + 1
	}
	user.CreatedAt = now
	user.UpdatedAt = now
	s.users = append(s.users, user)
	return user, nil
}

func (s *MemoryStore) GetUserByEmail(ctx context.Context, email string) (contracts.User, error) {
	if err := ctx.Err(); err != nil {
		return contracts.User{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.Email == email {
			return u, nil
		}
	}
	return contracts.User{}, ErrNotFound
}

func (s *MemoryStore) GetUser(ctx context.Context, id int64) (contracts.User, error) {
	if err := ctx.Err(); err != nil {
		return contracts.User{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.ID == id {
			return u, nil
		}
	}
	return contracts.User{}, ErrNotFound
}

func (s *MemoryStore) ListUsers(ctx context.Context) ([]contracts.User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]contracts.User(nil), s.users...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *MemoryStore) CountUsers(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users), nil
}

func (s *MemoryStore) UpdateUser(ctx context.Context, input contracts.User) (contracts.User, error) {
	if err := ctx.Err(); err != nil {
		return contracts.User{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i := range s.users {
		if s.users[i].ID == input.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return contracts.User{}, ErrNotFound
	}
	for i := range s.users {
		if i != idx && s.users[i].Email == input.Email {
			return contracts.User{}, ErrDuplicate
		}
	}

	current := s.users[idx]
	current.DeactivationStatus = normalizeUserDeactivationStatus(current.DeactivationStatus)
	if input.UpdatedAt.IsZero() || !current.UpdatedAt.Equal(input.UpdatedAt) {
		return contracts.User{}, ErrConflict
	}
	if current.DeactivationStatus == contracts.UserDeactivationDraining {
		return contracts.User{}, ErrUserDeactivationInProgress
	}
	if current.Enabled && userHasRole(current.Roles, contracts.UserRoleAdmin) &&
		(!input.Enabled || !userHasRole(input.Roles, contracts.UserRoleAdmin)) {
		enabledAdmins := 0
		for _, user := range s.users {
			if user.Enabled && userHasRole(user.Roles, contracts.UserRoleAdmin) {
				enabledAdmins++
			}
		}
		if enabledAdmins <= 1 {
			return contracts.User{}, ErrLastEnabledAdmin
		}
	}

	rolesChanged := !sameUserRoles(current.Roles, input.Roles)
	enabledChanged := current.Enabled != input.Enabled
	currentActiveClient := activeClientUser(current)
	targetActiveClient := input.Enabled && userHasRole(input.Roles, contracts.UserRoleClient)
	deactivationRequested := currentActiveClient && !targetActiveClient
	deactivationRetry := current.DeactivationStatus == contracts.UserDeactivationFailed && !targetActiveClient
	if deactivationRequested || deactivationRetry {
		planIDs := make(map[string]struct{})
		for _, plan := range s.routePlans {
			if plan.UserID == current.ID {
				planIDs[plan.ID] = struct{}{}
			}
		}
		if s.anyRoutePlanHasExecutingConnectorTaskLocked(planIDs) {
			return contracts.User{}, ErrConflict
		}
	}
	updated := current
	updated.Email = input.Email
	updated.DisplayName = input.DisplayName
	updated.Roles = append([]contracts.UserRole(nil), input.Roles...)
	updated.Enabled = input.Enabled
	now := s.now()
	updated.UpdatedAt = now
	if !updated.UpdatedAt.After(current.UpdatedAt) {
		updated.UpdatedAt = current.UpdatedAt.Add(time.Nanosecond)
	}
	switch {
	case targetActiveClient:
		if current.DeactivationStatus == contracts.UserDeactivationFailed {
			for _, binding := range s.publishedBindings {
				for _, plan := range s.routePlans {
					if plan.UserID == current.ID && plan.ID == binding.PlanID && binding.State != contracts.BindingRevoked {
						return contracts.User{}, ErrUserDeactivationInProgress
					}
				}
			}
		}
		updated.DeactivationStatus = contracts.UserDeactivationNone
		updated.DeactivationErrorCode = ""
		updated.DeactivationRequestedAt = nil
		updated.DeactivationCompletedAt = nil
	case deactivationRequested || deactivationRetry:
		requestedAt := updated.UpdatedAt
		updated.DeactivationStatus = contracts.UserDeactivationDraining
		updated.DeactivationErrorCode = ""
		updated.DeactivationRequestedAt = &requestedAt
		updated.DeactivationCompletedAt = nil
	}
	s.users[idx] = updated
	if rolesChanged || enabledChanged {
		s.deleteUserSessionsLocked(updated.ID)
	}
	if deactivationRequested || deactivationRetry {
		for i := range s.routePlans {
			if s.routePlans[i].UserID != updated.ID {
				continue
			}
			s.advanceRoutePlanGenerationLocked(&s.routePlans[i], updated.UpdatedAt, "")
		}
	}
	return updated, nil
}

// ReconcileUserDeactivations is the in-memory atomic finalizer. Connector
// identity stays live while any route-plan binding lacks a revoked receipt.
func (s *MemoryStore) ReconcileUserDeactivations(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for userIndex := range s.users {
		user := &s.users[userIndex]
		user.DeactivationStatus = normalizeUserDeactivationStatus(user.DeactivationStatus)
		if !user.DeactivationStatus.InProgress() {
			continue
		}
		planIDs := make(map[string]struct{})
		for _, plan := range s.routePlans {
			if plan.UserID == user.ID {
				planIDs[plan.ID] = struct{}{}
			}
		}
		unrevoked := false
		for _, binding := range s.publishedBindings {
			if _, relevant := planIDs[binding.PlanID]; relevant && binding.State != contracts.BindingRevoked {
				unrevoked = true
				break
			}
		}
		now := s.now()
		if !unrevoked {
			operationsComplete := true
			for _, operation := range s.poolRolloutOps {
				if operation.UserID != user.ID || operation.Action != contracts.PoolRolloutOperationDrain {
					continue
				}
				if operation.Status == contracts.PoolRolloutOperationPending ||
					operation.Status == contracts.PoolRolloutOperationRunning ||
					operation.Status == contracts.PoolRolloutOperationFailed {
					operationsComplete = false
					break
				}
			}
			if !operationsComplete {
				continue
			}
			s.revokeUserConnectorsLocked(user.ID, now)
			s.deleteUserSessionsLocked(user.ID)
			for planIndex := range s.routePlans {
				if s.routePlans[planIndex].UserID != user.ID {
					continue
				}
				s.routePlans[planIndex].Status = contracts.RoutePlanSuspended
				s.routePlans[planIndex].UpdatedAt = now
			}
			if user.DeactivationRequestedAt == nil {
				requestedAt := now
				user.DeactivationRequestedAt = &requestedAt
			}
			completedAt := now
			user.DeactivationStatus = contracts.UserDeactivationCompleted
			user.DeactivationErrorCode = ""
			user.DeactivationCompletedAt = &completedAt
			user.UpdatedAt = monotonicUserUpdatedAt(user.UpdatedAt, now)
			continue
		}

		drainFailed := false
		for _, operation := range s.poolRolloutOps {
			if operation.UserID == user.ID && operation.Action == contracts.PoolRolloutOperationDrain &&
				operation.Status == contracts.PoolRolloutOperationFailed {
				drainFailed = true
				break
			}
		}
		nextStatus := contracts.UserDeactivationDraining
		nextCode := ""
		if drainFailed {
			nextStatus = contracts.UserDeactivationFailed
			nextCode = userDeactivationDrainFailedCode
		}
		if user.DeactivationStatus != nextStatus || user.DeactivationErrorCode != nextCode {
			user.DeactivationStatus = nextStatus
			user.DeactivationErrorCode = nextCode
			user.DeactivationCompletedAt = nil
			user.UpdatedAt = monotonicUserUpdatedAt(user.UpdatedAt, now)
		}
	}
	return nil
}

func monotonicUserUpdatedAt(current, next time.Time) time.Time {
	if !next.After(current) {
		return current.Add(time.Nanosecond)
	}
	return next
}

func (s *MemoryStore) revokeUserConnectorsLocked(userID int64, now time.Time) {
	for i := range s.connectors {
		if s.connectors[i].UserID != userID {
			continue
		}
		s.connectors[i].Status = contracts.ConnectorStatusRevoked
		s.connectors[i].TokenHash = ""
		s.connectors[i].RevokedAt = &now
		s.connectors[i].UpdatedAt = now
	}
	for i := range s.instances {
		if s.instances[i].UserID == userID && s.instances[i].ConnectorID != "" {
			s.instances[i].ConnectorID = ""
			s.instances[i].UpdatedAt = now
		}
	}
	for i := range s.enrollments {
		if s.enrollments[i].UserID == userID && s.enrollments[i].UsedAt == nil {
			usedAt := now
			s.enrollments[i].UsedAt = &usedAt
		}
	}
	for i := range s.connectorTasks {
		task := &s.connectorTasks[i]
		if task.UserID != userID || task.Status != contracts.ConnectorTaskPending && task.Status != contracts.ConnectorTaskLeased {
			continue
		}
		task.Status = contracts.ConnectorTaskExpired
		task.Error = contracts.ConnectorTaskError{Code: "expired"}
		task.LeaseOwner = ""
		task.LeaseNonce = ""
		task.LeaseUntil = nil
		task.UpdatedAt = now
	}
}

func (s *MemoryStore) UpdateUserPasswordHash(ctx context.Context, userID int64, passwordHash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.users {
		if s.users[i].ID != userID {
			continue
		}
		s.users[i].PasswordHash = passwordHash
		s.users[i].UpdatedAt = s.now()
		s.deleteUserSessionsLocked(userID)
		return nil
	}
	return ErrNotFound
}

func (s *MemoryStore) CreateSession(ctx context.Context, input contracts.Session, expectedUser contracts.User) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	userCurrent := false
	for _, user := range s.users {
		if user.ID == input.UserID && user.ID == expectedUser.ID && user.Enabled &&
			user.PasswordHash == expectedUser.PasswordHash && sameUserRoles(user.Roles, expectedUser.Roles) {
			userCurrent = true
			break
		}
	}
	if !userCurrent {
		return ErrConflict
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = s.now()
	}
	s.sessions[input.TokenHash] = input
	return nil
}

func (s *MemoryStore) deleteUserSessionsLocked(userID int64) {
	for tokenHash, session := range s.sessions {
		if session.UserID == userID {
			delete(s.sessions, tokenHash)
		}
	}
}

func userHasRole(roles []contracts.UserRole, want contracts.UserRole) bool {
	for _, role := range roles {
		if role == want {
			return true
		}
	}
	return false
}

func sameUserRoles(a, b []contracts.UserRole) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[contracts.UserRole]int, len(a))
	for _, role := range a {
		counts[role]++
	}
	for _, role := range b {
		counts[role]--
		if counts[role] < 0 {
			return false
		}
	}
	return true
}

func (s *MemoryStore) GetSession(ctx context.Context, tokenHash string) (contracts.Session, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Session{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[tokenHash]
	if !ok {
		return contracts.Session{}, ErrNotFound
	}
	return sess, nil
}

func (s *MemoryStore) DeleteSession(ctx context.Context, tokenHash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tokenHash)
	return nil
}

func (s *MemoryStore) GetAuthSystemSettings(ctx context.Context) (contracts.AuthSystemSettings, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AuthSystemSettings{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.authSettings == nil {
		return contracts.AuthSystemSettings{}, ErrNotFound
	}
	return copyAuthSystemSettings(*s.authSettings), nil
}

func (s *MemoryStore) UpsertAuthSystemSettings(ctx context.Context, input contracts.AuthSystemSettings) (contracts.AuthSystemSettings, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AuthSystemSettings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := copyAuthSystemSettings(input)
	settings.UpdatedAt = s.now()
	settings.TurnstileSecretConfigured = settings.TurnstileSecretKey != ""
	s.authSettings = &settings
	return copyAuthSystemSettings(settings), nil
}

func copyAuthSystemSettings(input contracts.AuthSystemSettings) contracts.AuthSystemSettings {
	out := input
	out.RegistrationEmailSuffixWhitelist = append([]string{}, input.RegistrationEmailSuffixWhitelist...)
	return out
}

func (s *MemoryStore) GetPaymentConfig(ctx context.Context) (contracts.PaymentConfig, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PaymentConfig{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.paymentConfig == nil {
		return contracts.PaymentConfig{}, ErrNotFound
	}
	return copyPaymentConfig(*s.paymentConfig), nil
}

func (s *MemoryStore) UpsertPaymentConfig(ctx context.Context, input contracts.PaymentConfig) (contracts.PaymentConfig, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PaymentConfig{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	config := copyPaymentConfig(input)
	config.UpdatedAt = s.now()
	s.paymentConfig = &config
	return copyPaymentConfig(config), nil
}

func (s *MemoryStore) CreatePaymentProvider(ctx context.Context, input contracts.PaymentProvider) (contracts.PaymentProvider, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PaymentProvider{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	provider := copyPaymentProvider(input)
	provider.ID = s.nextID("payprov")
	provider.CreatedAt = s.now()
	provider.UpdatedAt = provider.CreatedAt
	s.paymentProviders = append(s.paymentProviders, provider)
	return copyPaymentProvider(provider), nil
}

func (s *MemoryStore) GetPaymentProvider(ctx context.Context, id string) (contracts.PaymentProvider, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PaymentProvider{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, provider := range s.paymentProviders {
		if provider.ID == id {
			return copyPaymentProvider(provider), nil
		}
	}
	return contracts.PaymentProvider{}, ErrNotFound
}

func (s *MemoryStore) ListPaymentProviders(ctx context.Context) ([]contracts.PaymentProvider, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.PaymentProvider, len(s.paymentProviders))
	for i, provider := range s.paymentProviders {
		out[i] = copyPaymentProvider(provider)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SortOrder == out[j].SortOrder {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].SortOrder < out[j].SortOrder
	})
	return out, nil
}

func (s *MemoryStore) UpdatePaymentProvider(ctx context.Context, input contracts.PaymentProvider) (contracts.PaymentProvider, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PaymentProvider{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, provider := range s.paymentProviders {
		if provider.ID != input.ID {
			continue
		}
		updated := copyPaymentProvider(input)
		updated.CreatedAt = provider.CreatedAt
		updated.UpdatedAt = s.now()
		s.paymentProviders[i] = updated
		return copyPaymentProvider(updated), nil
	}
	return contracts.PaymentProvider{}, ErrNotFound
}

func (s *MemoryStore) DeletePaymentProvider(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, provider := range s.paymentProviders {
		if provider.ID == id {
			s.paymentProviders = append(s.paymentProviders[:i], s.paymentProviders[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func copyPaymentConfig(input contracts.PaymentConfig) contracts.PaymentConfig {
	out := input
	out.EnabledPaymentTypes = append([]string{}, input.EnabledPaymentTypes...)
	return out
}

func copyPaymentProvider(input contracts.PaymentProvider) contracts.PaymentProvider {
	out := input
	out.Config = copyStringMap(input.Config)
	out.SecretConfigured = copyBoolMap(input.SecretConfigured)
	out.SecretRefs = copyStringMap(input.SecretRefs)
	out.SupportedTypes = append([]string{}, input.SupportedTypes...)
	if input.Limits != nil {
		out.Limits = make(map[string]contracts.PaymentMethodLimit, len(input.Limits))
		for key, limit := range input.Limits {
			out.Limits[key] = limit
		}
	}
	return out
}

func copyStringMap(input map[string]string) map[string]string {
	if input == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func copyBoolMap(input map[string]bool) map[string]bool {
	if input == nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func copySupplyOffer(input contracts.SupplyOffer) contracts.SupplyOffer {
	out := input
	if input.Labels != nil {
		out.Labels = make(map[string]string, len(input.Labels))
		for key, value := range input.Labels {
			out.Labels[key] = value
		}
	}
	return out
}

func copyConnector(input contracts.Connector) contracts.Connector {
	out := input
	out.Gateway = contracts.SanitizeConnectorRuntimeState(input.Gateway)
	// Sanitization normally stamps the current wire version. A preserved v2
	// identity must remain visibly v2 until a real v3 RecordConnectorSeen
	// handshake atomically upgrades both protocol fields.
	out.Gateway.ProtocolVersion = input.ProtocolVersion
	if input.RevokedAt != nil {
		t := *input.RevokedAt
		out.RevokedAt = &t
	}
	return out
}

func copyConnectorEnrollment(input contracts.ConnectorEnrollment) contracts.ConnectorEnrollment {
	out := input
	if input.UsedAt != nil {
		t := *input.UsedAt
		out.UsedAt = &t
	}
	return out
}

func copyConnectorTask(input contracts.ConnectorTask) contracts.ConnectorTask {
	out := input
	if input.Input != nil {
		out.Input = append([]byte(nil), input.Input...)
	}
	if input.Result != nil {
		out.Result = append([]byte(nil), input.Result...)
	}
	if input.LeaseUntil != nil {
		t := *input.LeaseUntil
		out.LeaseUntil = &t
	}
	return out
}

func seedInstances(now time.Time) []contracts.Instance {
	return []contracts.Instance{
		{
			ID:        "inst-demo-sub2api",
			UserID:    1,
			Name:      "Demo sub2api",
			Kind:      contracts.InstanceKindSub2API,
			Status:    contracts.InstanceStatusUnknown,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
}

func seedUsers(now time.Time) []contracts.User {
	return []contracts.User{
		{
			ID:          1,
			Email:       "owner-demo@local.dev",
			DisplayName: "Demo Owner",
			Roles:       []contracts.UserRole{contracts.UserRoleOwner},
			Enabled:     true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}
}

func seedAudits(now time.Time) []contracts.OperationAudit {
	return []contracts.OperationAudit{
		{
			ID:         "audit-seed-0001",
			UserID:     1,
			ActorType:  "system",
			ActorID:    "e2m-core",
			Action:     "core.boot",
			RiskLevel:  contracts.RiskLevelL0,
			TargetType: "service",
			TargetID:   "e2m-core",
			Result:     "accepted",
			CreatedAt:  now,
		},
	}
}

func seedNotificationRoutes(now time.Time) []contracts.NotificationRoute {
	return []contracts.NotificationRoute{
		{
			ID:              "route-demo-feishu",
			UserID:          1,
			Name:            "Demo Feishu Alerts",
			Channel:         contracts.NotificationChannelFeishu,
			TargetRef:       "system:feishu",
			MinRiskLevel:    contracts.RiskLevelL1,
			Enabled:         true,
			EscalationAfter: "30m",
			CreatedAt:       now,
			UpdatedAt:       now,
		},
	}
}
