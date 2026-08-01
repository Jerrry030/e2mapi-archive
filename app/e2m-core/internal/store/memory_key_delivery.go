package store

import (
	"context"
	"sort"
	"strings"

	"e2m.local/contracts"
)

func (s *MemoryStore) UpsertUpstreamKeyDelivery(ctx context.Context, input contracts.UpstreamKeyDelivery) (contracts.UpstreamKeyDelivery, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamKeyDelivery{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	channel, found := s.upstreamChannelLocked(input.ChannelID)
	if !found {
		return contracts.UpstreamKeyDelivery{}, ErrNotFound
	}
	if channel.AccountOwnership.Normalize() != contracts.GatewayAccountPlatformManaged {
		return contracts.UpstreamKeyDelivery{}, ErrConflict
	}
	now := s.now().UTC()
	if existing, ok := s.keyDeliveries[input.ChannelID]; ok {
		input.ID = existing.ID
		input.CreatedAt = existing.CreatedAt
		input.KeyVersion = existing.KeyVersion + 1
		input.ProofStatus = contracts.DeliveryKeyProofUnverified
		input.ProofConnectorID = ""
		input.ProofCheckedAt = nil
		input.UpdatedAt = now
		s.keyDeliveries[input.ChannelID] = input
		return input, nil
	}
	if input.ID == "" {
		input.ID = s.nextID("keydel")
	}
	input.KeyVersion = 1
	input.ProofStatus = contracts.DeliveryKeyProofUnverified
	input.ProofConnectorID = ""
	input.ProofCheckedAt = nil
	input.CreatedAt, input.UpdatedAt = now, now
	s.keyDeliveries[input.ChannelID] = input
	return input, nil
}

func (s *MemoryStore) UpdateUpstreamKeyDeliveryProof(ctx context.Context, channelID string, expectedKeyVersion int64, connectorID string, status contracts.DeliveryKeyProofStatus) (contracts.UpstreamKeyDelivery, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamKeyDelivery{}, err
	}
	connectorID = strings.TrimSpace(connectorID)
	if expectedKeyVersion <= 0 || connectorID == "" || !status.Valid() {
		return contracts.UpstreamKeyDelivery{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delivery, ok := s.keyDeliveries[channelID]
	if !ok {
		return contracts.UpstreamKeyDelivery{}, ErrNotFound
	}
	if delivery.KeyVersion != expectedKeyVersion {
		return contracts.UpstreamKeyDelivery{}, ErrConflict
	}
	now := s.now().UTC()
	delivery.ProofStatus = status
	delivery.ProofConnectorID = connectorID
	delivery.ProofCheckedAt = &now
	s.keyDeliveries[channelID] = delivery
	return delivery, nil
}

func (s *MemoryStore) GetUpstreamKeyProofReceipt(ctx context.Context, channelID, instanceID string) (contracts.UpstreamKeyProofReceipt, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamKeyProofReceipt{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	receipt, ok := s.keyProofReceipts[strings.TrimSpace(channelID)+"\x00"+strings.TrimSpace(instanceID)]
	if !ok {
		return contracts.UpstreamKeyProofReceipt{}, ErrNotFound
	}
	return receipt, nil
}

func (s *MemoryStore) UpsertUpstreamKeyProofReceipt(ctx context.Context, input contracts.UpstreamKeyProofReceipt) (contracts.UpstreamKeyProofReceipt, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamKeyProofReceipt{}, err
	}
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	input.InstanceID = strings.TrimSpace(input.InstanceID)
	input.ConnectorID = strings.TrimSpace(input.ConnectorID)
	if input.ChannelID == "" || input.InstanceID == "" || input.ConnectorID == "" || input.KeyVersion <= 0 || !input.Status.Valid() {
		return contracts.UpstreamKeyProofReceipt{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delivery, ok := s.keyDeliveries[input.ChannelID]
	if !ok {
		return contracts.UpstreamKeyProofReceipt{}, ErrNotFound
	}
	if delivery.KeyVersion != input.KeyVersion {
		return contracts.UpstreamKeyProofReceipt{}, ErrConflict
	}
	instanceFound := false
	for _, instance := range s.instances {
		if instance.ID == input.InstanceID {
			instanceFound = true
			break
		}
	}
	if !instanceFound {
		return contracts.UpstreamKeyProofReceipt{}, ErrNotFound
	}
	input.CheckedAt = s.now().UTC()
	s.keyProofReceipts[input.ChannelID+"\x00"+input.InstanceID] = input
	return input, nil
}

func (s *MemoryStore) GetUpstreamKeyDeployment(ctx context.Context, channelID, instanceID string) (contracts.UpstreamKeyDeployment, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamKeyDeployment{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	deployment, ok := s.keyDeployments[channelID+"\x00"+instanceID]
	if !ok {
		return contracts.UpstreamKeyDeployment{}, ErrNotFound
	}
	return deployment, nil
}

func (s *MemoryStore) UpsertUpstreamKeyDeployment(ctx context.Context, input contracts.UpstreamKeyDeployment) (contracts.UpstreamKeyDeployment, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamKeyDeployment{}, err
	}
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	input.InstanceID = strings.TrimSpace(input.InstanceID)
	input.ConnectorID = strings.TrimSpace(input.ConnectorID)
	if input.ChannelID == "" || input.InstanceID == "" || input.ConnectorID == "" || input.KeyVersion <= 0 || !input.Status.Valid() {
		return contracts.UpstreamKeyDeployment{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delivery, ok := s.keyDeliveries[input.ChannelID]
	if !ok {
		return contracts.UpstreamKeyDeployment{}, ErrNotFound
	}
	if delivery.KeyVersion != input.KeyVersion {
		return contracts.UpstreamKeyDeployment{}, ErrConflict
	}
	instanceFound := false
	for _, instance := range s.instances {
		if instance.ID == input.InstanceID {
			instanceFound = true
			break
		}
	}
	if !instanceFound {
		return contracts.UpstreamKeyDeployment{}, ErrNotFound
	}
	now := s.now().UTC()
	input.UpdatedAt = now
	if input.Status == contracts.DeliveryKeyDeploymentDeployed {
		input.DeployedAt = &now
	} else {
		input.DeployedAt = nil
	}
	s.keyDeployments[input.ChannelID+"\x00"+input.InstanceID] = input
	return input, nil
}

func (s *MemoryStore) GetUpstreamKeyDelivery(ctx context.Context, channelID string) (contracts.UpstreamKeyDelivery, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamKeyDelivery{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	delivery, ok := s.keyDeliveries[channelID]
	if !ok {
		return contracts.UpstreamKeyDelivery{}, ErrNotFound
	}
	return delivery, nil
}

func (s *MemoryStore) ListUpstreamKeyDeliveries(ctx context.Context) ([]contracts.UpstreamKeyDelivery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.UpstreamKeyDelivery, 0, len(s.keyDeliveries))
	for _, delivery := range s.keyDeliveries {
		out = append(out, delivery)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *MemoryStore) ListAssignedUpstreamKeys(ctx context.Context, userID int64) ([]contracts.AssignedUpstreamKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.AssignedUpstreamKey, 0)
	for channelID, allocation := range s.channelAllocations {
		if allocation.UserID != userID {
			continue
		}
		channel, found := s.upstreamChannelLocked(channelID)
		if !found || channel.AccountOwnership.Normalize() != contracts.GatewayAccountPlatformManaged {
			continue
		}
		delivery, ok := s.keyDeliveries[channelID]
		if !ok {
			continue
		}
		out = append(out, contracts.AssignedUpstreamKey{
			ID: delivery.ID, DisplayName: channel.DisplayName, Provider: channel.Provider,
			MaskedValue: delivery.MaskedValue, AllocatedAt: allocation.CreatedAt,
			KeyVersion: delivery.KeyVersion, ProofStatus: delivery.ProofStatus,
			ProofConnectorID: delivery.ProofConnectorID, ProofCheckedAt: delivery.ProofCheckedAt,
			ChannelID: channelID, SecretRef: delivery.SecretRef,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AllocatedAt.Equal(out[j].AllocatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].AllocatedAt.Before(out[j].AllocatedAt)
	})
	return out, nil
}

// Caller already holds s.mu for both read and write use sites.
func (s *MemoryStore) upstreamChannelLocked(id string) (contracts.UpstreamChannel, bool) {
	for _, channel := range s.upstreamChannels {
		if channel.ID == id {
			return channel, true
		}
	}
	return contracts.UpstreamChannel{}, false
}
