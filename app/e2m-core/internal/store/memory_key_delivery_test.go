package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryUpstreamKeyDeliveryRequiresPlatformOwnershipAndPermanentAllocation(t *testing.T) {
	s := NewMemoryStore(time.Now().UTC())
	ctx := context.Background()
	pool, _ := s.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "delivery pool"})
	platform, _ := s.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, SourceID: "platform-source", DisplayName: "platform key",
		AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	ownerProvided, _ := s.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, SourceID: "owner-source", DisplayName: "owner key",
		AccountOwnership: contracts.GatewayAccountOwnerProvided,
	})

	if _, err := s.UpsertUpstreamKeyDelivery(ctx, contracts.UpstreamKeyDelivery{
		ChannelID: ownerProvided.ID, SecretRef: "ref:owner", MaskedValue: "********wner",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("owner-provided delivery error=%v, want conflict", err)
	}
	delivery, err := s.UpsertUpstreamKeyDelivery(ctx, contracts.UpstreamKeyDelivery{
		ChannelID: platform.ID, SecretRef: "ref:platform", MaskedValue: "********form",
	})
	if err != nil {
		t.Fatalf("upsert platform delivery: %v", err)
	}
	keys, err := s.ListAssignedUpstreamKeys(ctx, 42)
	if err != nil || len(keys) != 0 {
		t.Fatalf("unallocated key was exposed: keys=%v err=%v", keys, err)
	}
	plan, _ := s.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: 42, InstanceID: "instance-a", PoolID: pool.ID})
	if _, err := s.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, ChannelID: platform.ID,
		AccountOwnership: contracts.GatewayAccountPlatformManaged,
	}); err != nil {
		t.Fatalf("claim platform key: %v", err)
	}
	keys, err = s.ListAssignedUpstreamKeys(ctx, 42)
	if err != nil || len(keys) != 1 {
		t.Fatalf("list assigned keys: keys=%v err=%v", keys, err)
	}
	if keys[0].ID != delivery.ID || keys[0].SecretRef != "ref:platform" || keys[0].MaskedValue != "********form" {
		t.Fatalf("unexpected assigned key: %+v", keys[0])
	}
	other, _ := s.ListAssignedUpstreamKeys(ctx, 43)
	if len(other) != 0 {
		t.Fatalf("other owner saw key: %+v", other)
	}
}

func TestMemoryDeliveryKeyVersionProofCASAndDeploymentAcknowledgement(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore(time.Now().UTC())
	instance, err := s.CreateInstance(ctx, contracts.Instance{UserID: 42, Name: "gateway", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := s.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "proof pool"})
	channel, _ := s.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, DisplayName: "proof key", AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	first, err := s.UpsertUpstreamKeyDelivery(ctx, contracts.UpstreamKeyDelivery{
		ChannelID: channel.ID, SecretRef: "ref:first", MaskedValue: "********irst",
	})
	if err != nil || first.KeyVersion != 1 || first.ProofStatus != contracts.DeliveryKeyProofUnverified {
		t.Fatalf("first delivery = %+v, %v", first, err)
	}
	keyUpdatedAt := first.UpdatedAt
	proved, err := s.UpdateUpstreamKeyDeliveryProof(ctx, channel.ID, first.KeyVersion, "connector-a", contracts.DeliveryKeyProofVerified)
	if err != nil || proved.ProofCheckedAt == nil || !proved.UpdatedAt.Equal(keyUpdatedAt) {
		t.Fatalf("proof update changed key metadata: %+v, %v", proved, err)
	}
	receipt, err := s.UpsertUpstreamKeyProofReceipt(ctx, contracts.UpstreamKeyProofReceipt{
		ChannelID: channel.ID, InstanceID: instance.ID, KeyVersion: first.KeyVersion,
		ConnectorID: "connector-a", Status: contracts.DeliveryKeyProofVerified,
	})
	if err != nil || receipt.CheckedAt.IsZero() {
		t.Fatalf("proof receipt = %+v, %v", receipt, err)
	}
	loadedReceipt, err := s.GetUpstreamKeyProofReceipt(ctx, channel.ID, instance.ID)
	if err != nil || loadedReceipt != receipt {
		t.Fatalf("loaded proof receipt = %+v, %v", loadedReceipt, err)
	}
	if _, err := s.UpdateUpstreamKeyDeliveryProof(ctx, channel.ID, first.KeyVersion+1, "connector-a", contracts.DeliveryKeyProofVerified); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale proof CAS error = %v", err)
	}
	deployed, err := s.UpsertUpstreamKeyDeployment(ctx, contracts.UpstreamKeyDeployment{
		ChannelID: channel.ID, InstanceID: instance.ID, KeyVersion: first.KeyVersion,
		ConnectorID: "connector-a", Status: contracts.DeliveryKeyDeploymentDeployed,
	})
	if err != nil || deployed.DeployedAt == nil {
		t.Fatalf("deployment = %+v, %v", deployed, err)
	}
	rotated, err := s.UpsertUpstreamKeyDelivery(ctx, contracts.UpstreamKeyDelivery{
		ChannelID: channel.ID, SecretRef: "ref:second", MaskedValue: "********cond",
	})
	if err != nil || rotated.KeyVersion != 2 || rotated.ProofStatus != contracts.DeliveryKeyProofUnverified || rotated.ProofCheckedAt != nil {
		t.Fatalf("rotated delivery = %+v, %v", rotated, err)
	}
	if _, err := s.UpsertUpstreamKeyDeployment(ctx, contracts.UpstreamKeyDeployment{
		ChannelID: channel.ID, InstanceID: instance.ID, KeyVersion: first.KeyVersion,
		ConnectorID: "connector-a", Status: contracts.DeliveryKeyDeploymentDeployed,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("old version deployment error = %v", err)
	}
}

func TestMemoryProofReceiptsAreIsolatedAcrossSameOwnerInstances(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore(time.Now().UTC())
	first, _ := s.CreateInstance(ctx, contracts.Instance{UserID: 42, Name: "A", Kind: contracts.InstanceKindNewAPI})
	second, _ := s.CreateInstance(ctx, contracts.Instance{UserID: 42, Name: "B", Kind: contracts.InstanceKindNewAPI})
	pool, _ := s.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "shared"})
	channel, _ := s.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, DisplayName: "key", AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	delivery, _ := s.UpsertUpstreamKeyDelivery(ctx, contracts.UpstreamKeyDelivery{ChannelID: channel.ID, SecretRef: "ref", MaskedValue: "********"})
	for _, input := range []contracts.UpstreamKeyProofReceipt{
		{ChannelID: channel.ID, InstanceID: first.ID, KeyVersion: delivery.KeyVersion, ConnectorID: "connector-a", Status: contracts.DeliveryKeyProofVerified},
		{ChannelID: channel.ID, InstanceID: second.ID, KeyVersion: delivery.KeyVersion, ConnectorID: "connector-b", Status: contracts.DeliveryKeyProofMismatch},
	} {
		if _, err := s.UpsertUpstreamKeyProofReceipt(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	receiptA, _ := s.GetUpstreamKeyProofReceipt(ctx, channel.ID, first.ID)
	receiptB, _ := s.GetUpstreamKeyProofReceipt(ctx, channel.ID, second.ID)
	if receiptA.Status != contracts.DeliveryKeyProofVerified || receiptA.ConnectorID != "connector-a" ||
		receiptB.Status != contracts.DeliveryKeyProofMismatch || receiptB.ConnectorID != "connector-b" {
		t.Fatalf("receipts A=%+v B=%+v", receiptA, receiptB)
	}
}
