package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPostgresUpstreamKeyDeliveryOwnerScope(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	suffix := newID("delivery")
	poolID, channelID, planID, instanceID := "pool-"+suffix, "channel-"+suffix, "plan-"+suffix, "instance-"+suffix
	user, err := st.CreateUser(ctx, contracts.User{
		Email: "delivery-" + suffix + "@example.test", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_key_deliveries WHERE channel_id=$1`, channelID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM published_bindings WHERE plan_id=$1`, planID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_channel_allocations WHERE channel_id=$1`, channelID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM route_plans WHERE id=$1`, planID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_channels WHERE id=$1`, channelID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_pools WHERE id=$1`, poolID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM instances WHERE id=$1`, instanceID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user.ID)
	})
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "delivery", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatal(err)
	}
	instanceID = instance.ID
	_, _ = st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: "delivery test"})
	_, _ = st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		ID: channelID, PoolID: poolID, SourceID: "source-" + suffix,
		DisplayName: "platform key", AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	_, _ = st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: planID, UserID: user.ID, InstanceID: instanceID, PoolID: poolID})
	_, _ = st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: planID, ChannelID: channelID, AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	delivery, err := st.UpsertUpstreamKeyDelivery(ctx, contracts.UpstreamKeyDelivery{
		ChannelID: channelID, SecretRef: "credential_ref:platform/delivery/" + suffix,
		MaskedValue: "********test",
	})
	if err != nil {
		t.Fatalf("upsert delivery: %v", err)
	}
	if delivery.KeyVersion != 1 || delivery.ProofStatus != contracts.DeliveryKeyProofUnverified {
		t.Fatalf("initial delivery state: %+v", delivery)
	}
	keys, err := st.ListAssignedUpstreamKeys(ctx, user.ID)
	if err != nil || len(keys) != 1 || keys[0].ID != delivery.ID {
		t.Fatalf("list assigned keys: keys=%+v err=%v", keys, err)
	}
	other, err := st.ListAssignedUpstreamKeys(ctx, user.ID+1000000)
	if err != nil || len(other) != 0 {
		t.Fatalf("other user saw delivery: keys=%+v err=%v", other, err)
	}
	keyUpdatedAt := delivery.UpdatedAt
	proved, err := st.UpdateUpstreamKeyDeliveryProof(ctx, channelID, delivery.KeyVersion, "connector-test", contracts.DeliveryKeyProofVerified)
	if err != nil || proved.ProofCheckedAt == nil || !proved.UpdatedAt.Equal(keyUpdatedAt) {
		t.Fatalf("proof round trip: delivery=%+v err=%v", proved, err)
	}
	receipt, err := st.UpsertUpstreamKeyProofReceipt(ctx, contracts.UpstreamKeyProofReceipt{
		ChannelID: channelID, InstanceID: instanceID, KeyVersion: delivery.KeyVersion,
		ConnectorID: "connector-test", Status: contracts.DeliveryKeyProofVerified,
	})
	if err != nil || receipt.CheckedAt.IsZero() {
		t.Fatalf("proof receipt=%+v err=%v", receipt, err)
	}
	if _, err := st.UpdateUpstreamKeyDeliveryProof(ctx, channelID, delivery.KeyVersion+1, "connector-test", contracts.DeliveryKeyProofVerified); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale proof CAS error=%v", err)
	}
	rotated, err := st.UpsertUpstreamKeyDelivery(ctx, contracts.UpstreamKeyDelivery{
		ChannelID: channelID, SecretRef: "credential_ref:platform/delivery/rotated/" + suffix,
		MaskedValue: "********ated",
	})
	if err != nil || rotated.KeyVersion != 2 || rotated.ProofStatus != contracts.DeliveryKeyProofUnverified || rotated.ProofCheckedAt != nil {
		t.Fatalf("rotated delivery=%+v err=%v", rotated, err)
	}
}

func TestPostgresProofReceiptsAreIsolatedAcrossSameOwnerInstances(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	suffix := newID("proof-receipt")
	poolID, channelID := "pool-"+suffix, "channel-"+suffix
	instanceA, instanceB := "instance-a-"+suffix, "instance-b-"+suffix
	user, err := st.CreateUser(ctx, contracts.User{
		Email: "proof-receipt-" + suffix + "@example.test", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_key_deliveries WHERE channel_id=$1`, channelID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_channels WHERE id=$1`, channelID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_pools WHERE id=$1`, poolID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM instances WHERE id=ANY($1)`, []string{instanceA, instanceB})
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user.ID)
	})
	createdA, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "A", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatal(err)
	}
	createdB, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "B", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatal(err)
	}
	instanceA, instanceB = createdA.ID, createdB.ID
	_, _ = st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: "shared"})
	_, _ = st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{ID: channelID, PoolID: poolID, DisplayName: "key", AccountOwnership: contracts.GatewayAccountPlatformManaged})
	delivery, err := st.UpsertUpstreamKeyDelivery(ctx, contracts.UpstreamKeyDelivery{ChannelID: channelID, SecretRef: "ref:" + suffix, MaskedValue: "********"})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []contracts.UpstreamKeyProofReceipt{
		{ChannelID: channelID, InstanceID: instanceA, KeyVersion: delivery.KeyVersion, ConnectorID: "connector-a", Status: contracts.DeliveryKeyProofVerified},
		{ChannelID: channelID, InstanceID: instanceB, KeyVersion: delivery.KeyVersion, ConnectorID: "connector-b", Status: contracts.DeliveryKeyProofMismatch},
	} {
		if _, err := st.UpsertUpstreamKeyProofReceipt(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	receiptA, _ := st.GetUpstreamKeyProofReceipt(ctx, channelID, instanceA)
	receiptB, _ := st.GetUpstreamKeyProofReceipt(ctx, channelID, instanceB)
	if receiptA.Status != contracts.DeliveryKeyProofVerified || receiptA.ConnectorID != "connector-a" ||
		receiptB.Status != contracts.DeliveryKeyProofMismatch || receiptB.ConnectorID != "connector-b" {
		t.Fatalf("receipts A=%+v B=%+v", receiptA, receiptB)
	}
}
