package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

// The overdraft semantics shipped in 0081/0082 are enforced by the database as
// much as by Go code: the memory store carries no constraints, so only a real
// PostgreSQL run proves that a settlement above the hold can actually be
// written (settled_micros > reserved_micros), that the deferred journal
// trigger accepts the split debit, and that debt behaves as designed. These
// tests exist because the constraint-backed failure mode is silent: the data
// plane falls back to a conservative settle and undercharges.

type postgresSupplyFixture struct {
	store     *PostgresStore
	client    contracts.User
	group     contracts.UpstreamPool
	tokenHash string
}

func seedPostgresSupplyFixture(t *testing.T) (context.Context, postgresSupplyFixture) {
	t.Helper()
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	suffix := newID("settlement")
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
		_, _ = st.pool.Exec(cleanup, `DELETE FROM supply_channel_endpoints WHERE channel_id IN (SELECT id FROM upstream_channels WHERE pool_id=$1)`, group.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM upstream_channels WHERE pool_id=$1`, group.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM users WHERE id=$1`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM upstream_pools WHERE id=$1`, group.ID)
	})
	channel, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: group.ID, DisplayName: suffix, Models: []string{"gpt-test"}, AccountOwnership: contracts.GatewayAccountPlatformManaged, InventoryState: contracts.UpstreamInventoryReady})
	if err != nil {
		t.Fatal(err)
	}
	// 1_000_000 micros per million tokens = 1 micro per prompt token, so token
	// counts in these tests read directly as charged micros.
	_, err = st.UpsertSupplyChannelEndpoint(ctx, contracts.SupplyChannelEndpoint{
		ChannelID: channel.ID, BaseURL: "https://upstream.example/v1", SecretRef: "credential_ref:" + suffix, Currency: "CNY",
		InputPriceMicrosPerMillion: 1_000_000, OutputPriceMicrosPerMillion: 2_000_000,
		InputSupplierMicrosPerMillion: 500_000, OutputSupplierMicrosPerMillion: 1_000_000,
		MaxRequestMicros: 100_000, MaxConcurrency: 10, CapacityPercent: 100, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	plaintext := "e2m_v1_" + suffix
	_, err = st.CreateVirtualKey(ctx, contracts.VirtualKey{UserID: client.ID, GroupID: group.ID, Name: suffix, ResourceClass: contracts.ResourceClassStable, Prefix: "e2m_v1_", TokenHash: contracts.HashVirtualKey(plaintext), SecretRef: "credential_ref:virtual/" + suffix, Models: []string{"gpt-test"}})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, postgresSupplyFixture{store: st, client: client, group: group, tokenHash: contracts.HashVirtualKey(plaintext)}
}

func assertJournalsBalanced(t *testing.T, ctx context.Context, st *PostgresStore, userID int64) {
	t.Helper()
	journals, err := st.ListWalletJournals(ctx, userID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, journal := range journals {
		if !journal.Balanced() {
			t.Fatalf("unbalanced journal: %+v", journal)
		}
	}
}

func TestPostgresSettlementAboveHoldWritesAndCarriesDebt(t *testing.T) {
	ctx, fx := seedPostgresSupplyFixture(t)
	st := fx.store
	if _, _, err := st.AdjustWalletBalance(ctx, fx.client.ID, "CNY", 120_000, newID("fund"), "settlement test funding"); err != nil {
		t.Fatal(err)
	}

	reserved, err := st.ReserveSupplyRequest(ctx, fx.tokenHash, newID("req"), "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.Reservation.ReservedMicros != 100_000 {
		t.Fatalf("expected the configured hold, got %d", reserved.Reservation.ReservedMicros)
	}

	// 500_000 prompt tokens cost 500_000 micros: 5x the hold and well past the
	// 120_000 balance. Before 0082 the two settled<=reserved constraints made
	// this exact write fail and the caller fell back to undercharging.
	settled, err := st.SettleSupplyRequest(ctx, reserved.Reservation.ID, 500_000, 0)
	if err != nil {
		t.Fatalf("settling above the hold must succeed on a real database: %v", err)
	}
	if settled.ChargedMicros != 500_000 {
		t.Fatalf("the true cost must be charged, got %d", settled.ChargedMicros)
	}
	if settled.Wallet.AvailableMicros != -380_000 || settled.Wallet.ReservedMicros != 0 {
		t.Fatalf("the shortfall must be carried as debt: %+v", settled.Wallet)
	}

	// The overdraft rows must actually be persisted, not just returned.
	var reservationReserved, reservationSettled, usageSettled, walletAvailable int64
	if err := st.pool.QueryRow(ctx, `SELECT reserved_micros,settled_micros FROM wallet_reservations WHERE id=$1`, reserved.Reservation.ID).Scan(&reservationReserved, &reservationSettled); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT settled_micros FROM supply_usage_records WHERE reservation_id=$1`, reserved.Reservation.ID).Scan(&usageSettled); err != nil {
		t.Fatal(err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT available_micros FROM wallet_accounts WHERE user_id=$1 AND currency='CNY'`, fx.client.ID).Scan(&walletAvailable); err != nil {
		t.Fatal(err)
	}
	if reservationSettled != 500_000 || reservationSettled <= reservationReserved || usageSettled != 500_000 || walletAvailable != -380_000 {
		t.Fatalf("persisted overdraft mismatch: reservation settled=%d reserved=%d usage settled=%d available=%d",
			reservationSettled, reservationReserved, usageSettled, walletAvailable)
	}
	assertJournalsBalanced(t, ctx, st, fx.client.ID)

	// A wallet in debt cannot start new requests.
	if _, err := st.ReserveSupplyRequest(ctx, fx.tokenHash, newID("req"), "gpt-test", "CNY", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("a wallet in debt must be refused, got %v", err)
	}

	// A partial credit is always accepted and reduces the debt without
	// clearing it; refusing it would block repayment entirely.
	partial, _, err := st.AdjustWalletBalance(ctx, fx.client.ID, "CNY", 80_000, newID("repay"), "partial repayment")
	if err != nil {
		t.Fatalf("a credit onto a wallet in debt must be accepted: %v", err)
	}
	if partial.AvailableMicros != -300_000 {
		t.Fatalf("the credit must offset the debt, got %d", partial.AvailableMicros)
	}
	if _, err := st.ReserveSupplyRequest(ctx, fx.tokenHash, newID("req"), "gpt-test", "CNY", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("a still-negative wallet must stay refused, got %v", err)
	}

	// Clearing the debt restores service.
	cleared, _, err := st.AdjustWalletBalance(ctx, fx.client.ID, "CNY", 400_000, newID("clear"), "top up past the debt")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.AvailableMicros != 100_000 {
		t.Fatalf("the remainder must land in available funds, got %d", cleared.AvailableMicros)
	}
	restored, err := st.ReserveSupplyRequest(ctx, fx.tokenHash, newID("req"), "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatalf("a cleared wallet must be usable again: %v", err)
	}
	if _, err := st.ReleaseSupplyRequest(ctx, restored.Reservation.ID, "test_cleanup"); err != nil {
		t.Fatal(err)
	}
	assertJournalsBalanced(t, ctx, st, fx.client.ID)
}

func TestPostgresAdminDebitCannotCreateDebt(t *testing.T) {
	ctx, fx := seedPostgresSupplyFixture(t)
	st := fx.store
	if _, _, err := st.AdjustWalletBalance(ctx, fx.client.ID, "CNY", 50_000, newID("fund"), "debit test funding"); err != nil {
		t.Fatal(err)
	}
	// Only settlement may push a wallet negative; an admin debit past the
	// balance must be refused instead of manufacturing debt.
	if _, _, err := st.AdjustWalletBalance(ctx, fx.client.ID, "CNY", -60_000, newID("debit"), "over-debit"); !errors.Is(err, ErrConflict) {
		t.Fatalf("an admin debit past the balance must be refused, got %v", err)
	}
	wallet, err := st.GetWallet(ctx, fx.client.ID, "CNY")
	if err != nil || wallet.AvailableMicros != 50_000 {
		t.Fatalf("balance must be untouched after the refused debit: %+v err=%v", wallet, err)
	}
	// A debit within the balance still works.
	debited, _, err := st.AdjustWalletBalance(ctx, fx.client.ID, "CNY", -50_000, newID("debit"), "full debit")
	if err != nil || debited.AvailableMicros != 0 {
		t.Fatalf("debit within balance: %+v err=%v", debited, err)
	}
	assertJournalsBalanced(t, ctx, st, fx.client.ID)
}

func TestPostgresReleaseRestoresTheHoldAndBalancesTheLedger(t *testing.T) {
	ctx, fx := seedPostgresSupplyFixture(t)
	st := fx.store
	if _, _, err := st.AdjustWalletBalance(ctx, fx.client.ID, "CNY", 120_000, newID("fund"), "release test funding"); err != nil {
		t.Fatal(err)
	}
	reserved, err := st.ReserveSupplyRequest(ctx, fx.tokenHash, newID("req"), "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatal(err)
	}
	released, err := st.ReleaseSupplyRequest(ctx, reserved.Reservation.ID, "upstream_failed")
	if err != nil {
		t.Fatal(err)
	}
	if released.ReleasedMicros != 100_000 || released.Wallet.AvailableMicros != 120_000 || released.Wallet.ReservedMicros != 0 {
		t.Fatalf("release must restore the full hold: %+v", released)
	}
	var status string
	var settledMicros int64
	if err := st.pool.QueryRow(ctx, `SELECT status,settled_micros FROM wallet_reservations WHERE id=$1`, reserved.Reservation.ID).Scan(&status, &settledMicros); err != nil {
		t.Fatal(err)
	}
	if status != string(contracts.WalletReservationReleased) || settledMicros != 0 {
		t.Fatalf("persisted release mismatch: status=%s settled=%d", status, settledMicros)
	}
	assertJournalsBalanced(t, ctx, st, fx.client.ID)
}
