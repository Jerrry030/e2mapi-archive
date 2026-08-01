package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPostgresPlatformCustomerRoleFence(t *testing.T) {
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

	suffix := newID("platform-role-fence")
	client, err := st.CreateUser(ctx, contracts.User{Email: suffix + "@example.test", PasswordHash: "test", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	group, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: suffix, Models: []string{"gpt-test"}, ResourceClass: contracts.ResourceClassStable, DeliveryMode: contracts.UpstreamDeliverySupplyGateway})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		// Delete leaves in foreign-key order. These are all uniquely named test
		// fixtures; production records are never selected by this cleanup.
		_, _ = st.pool.Exec(cleanup, `DELETE FROM supply_usage_records WHERE user_id=$1`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM wallet_reservations WHERE user_id=$1`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM virtual_keys WHERE user_id=$1`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM wallet_entries WHERE journal_id IN (SELECT id FROM wallet_journals WHERE user_id=$1)`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM wallet_journals WHERE user_id=$1`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM wallet_accounts WHERE user_id=$1`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM upstream_channels WHERE pool_id=$1`, group.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM users WHERE id=$1`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM upstream_pools WHERE id=$1`, group.ID)
	})
	channel, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: group.ID, DisplayName: suffix, Models: []string{"gpt-test"}, AccountOwnership: contracts.GatewayAccountPlatformManaged, InventoryState: contracts.UpstreamInventoryReady})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertSupplyChannelEndpoint(ctx, contracts.SupplyChannelEndpoint{ChannelID: channel.ID, BaseURL: "https://upstream.example/v1", SecretRef: "credential_ref:" + suffix, Currency: "CNY", InputPriceMicrosPerMillion: 1, OutputPriceMicrosPerMillion: 1, MaxRequestMicros: 100, MaxConcurrency: 1, CapacityPercent: 100, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	plaintext := "e2m_v1_" + suffix
	key, err := st.CreateVirtualKey(ctx, contracts.VirtualKey{UserID: client.ID, GroupID: group.ID, Name: suffix, ResourceClass: contracts.ResourceClassStable, TokenHash: contracts.HashVirtualKey(plaintext), SecretRef: "credential_ref:virtual/" + suffix, Models: []string{"gpt-test"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.AdjustWalletBalance(ctx, client.ID, "CNY", 1_000, suffix, "role fence test"); err != nil {
		t.Fatal(err)
	}

	// Removing the client role must invalidate an already-issued key on the
	// request path, even while the login itself remains enabled.
	if _, err = st.pool.Exec(ctx, `UPDATE users SET roles=ARRAY['supplier']::text[] WHERE id=$1`, client.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ReserveSupplyRequest(ctx, key.TokenHash, suffix+"-request", "gpt-test", "CNY", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("existing platform key remained usable after client role removal: %v", err)
	}
	_, err = st.CreateVirtualKey(ctx, contracts.VirtualKey{UserID: client.ID, GroupID: group.ID, Name: suffix + "-new", ResourceClass: contracts.ResourceClassStable, TokenHash: contracts.HashVirtualKey(plaintext + "-new"), SecretRef: "credential_ref:virtual/" + suffix + "-new", Models: []string{"gpt-test"}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("supplier-only user created a platform key: %v", err)
	}

}
