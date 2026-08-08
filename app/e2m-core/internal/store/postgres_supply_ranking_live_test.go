package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

// The preference ORDER BY variants are real SQL — float smoothing over a
// LEFT-JOINed aggregate that the memory mirror only approximates in Go — so
// only a live PostgreSQL run proves the production ranking. The fixture
// stacks the deck like the memory tests: the default order always favours the
// first channel, so a preference test passes only when re-ranking happened.
type postgresRankingFixture struct {
	store    *PostgresStore
	client   contracts.User
	group    contracts.UpstreamPool
	channels [3]contracts.UpstreamChannel
	makeKey  func(t *testing.T, preference contracts.SupplyRoutingPreference) string
}

func seedPostgresRankingFixture(t *testing.T) (context.Context, postgresRankingFixture) {
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

	suffix := newID("ranking")
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
		_, _ = st.pool.Exec(cleanup, `DELETE FROM supply_usage_records WHERE user_id=$1`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM wallet_reservations WHERE user_id=$1`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM virtual_keys WHERE user_id=$1`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM wallet_entries WHERE journal_id IN (SELECT id FROM wallet_journals WHERE user_id=$1)`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM wallet_journals WHERE user_id=$1`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM wallet_accounts WHERE user_id=$1`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM supply_channel_stats WHERE channel_id IN (SELECT id FROM upstream_channels WHERE pool_id=$1)`, group.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM supply_channel_endpoints WHERE channel_id IN (SELECT id FROM upstream_channels WHERE pool_id=$1)`, group.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM upstream_channels WHERE pool_id=$1`, group.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM users WHERE id=$1`, client.ID)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM upstream_pools WHERE id=$1`, group.ID)
	})

	// channels[0]: best priority, most expensive. channels[2]: worst
	// priority, cheapest.
	prices := [3]int64{10_000_000, 3_000_000, 1_000_000}
	var channels [3]contracts.UpstreamChannel
	for index := range channels {
		channel, channelErr := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
			PoolID: group.ID, DisplayName: fmt.Sprintf("%s-%d", suffix, index), Models: []string{"gpt-test"},
			AccountOwnership: contracts.GatewayAccountPlatformManaged, InventoryState: contracts.UpstreamInventoryReady,
			Priority: index + 1,
		})
		if channelErr != nil {
			t.Fatal(channelErr)
		}
		if _, endpointErr := st.UpsertSupplyChannelEndpoint(ctx, contracts.SupplyChannelEndpoint{
			ChannelID: channel.ID, BaseURL: "https://upstream.example/v1", SecretRef: fmt.Sprintf("credential_ref:%s-%d", suffix, index),
			Currency: "CNY", InputPriceMicrosPerMillion: prices[index], OutputPriceMicrosPerMillion: prices[index],
			MaxRequestMicros: 10_000, MaxConcurrency: 10, CapacityPercent: 100, Enabled: true,
		}); endpointErr != nil {
			t.Fatal(endpointErr)
		}
		channels[index] = channel
	}
	if _, _, err := st.AdjustWalletBalance(ctx, client.ID, "CNY", 1_000_000, newID("fund"), "ranking test funding"); err != nil {
		t.Fatal(err)
	}
	sequence := 0
	makeKey := func(t *testing.T, preference contracts.SupplyRoutingPreference) string {
		t.Helper()
		sequence++
		plaintext := fmt.Sprintf("e2m_v1_%s_%d", suffix, sequence)
		if _, keyErr := st.CreateVirtualKey(ctx, contracts.VirtualKey{
			UserID: client.ID, GroupID: group.ID, Name: fmt.Sprintf("%s-%d", suffix, sequence), ResourceClass: contracts.ResourceClassStable,
			Prefix: "e2m_v1_", TokenHash: contracts.HashVirtualKey(plaintext), SecretRef: fmt.Sprintf("credential_ref:virtual/%s-%d", suffix, sequence),
			RoutingPreference: preference,
		}); keyErr != nil {
			t.Fatal(keyErr)
		}
		return plaintext
	}
	return ctx, postgresRankingFixture{store: st, client: client, group: group, channels: channels, makeKey: makeKey}
}

// insertRankingBucket writes one current reliability bucket directly: ranking
// reads the table, and the write path already has its own coverage.
func insertRankingBucket(t *testing.T, ctx context.Context, st *PostgresStore, channelID string, requests, failures, ttftSumMS, ttftSamples int64) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := st.pool.Exec(ctx, `INSERT INTO supply_channel_stats(channel_id,bucket_start,requests,failures,ttft_sum_ms,ttft_samples,duration_sum_ms,duration_samples,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$5,$6,$7)`,
		channelID, contracts.SupplyStatsBucketStart(now), requests, failures, ttftSumMS, ttftSamples, now); err != nil {
		t.Fatal(err)
	}
}

func reservePostgresChannel(t *testing.T, ctx context.Context, st *PostgresStore, plaintext, requestID string, excluded []string) string {
	t.Helper()
	reserved, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), requestID, "gpt-test", "CNY", excluded)
	if err != nil {
		t.Fatal(err)
	}
	return reserved.Candidate.Channel.ID
}

func TestPostgresRankingPriceFirstAndDefaultOrder(t *testing.T) {
	ctx, fx := seedPostgresRankingFixture(t)
	st := fx.store
	if got := reservePostgresChannel(t, ctx, st, fx.makeKey(t, ""), newID("req"), nil); got != fx.channels[0].ID {
		t.Fatalf("default order picked %s, want %s", got, fx.channels[0].ID)
	}
	priceKey := fx.makeKey(t, contracts.SupplyRoutingPriceFirst)
	if got := reservePostgresChannel(t, ctx, st, priceKey, newID("req"), nil); got != fx.channels[2].ID {
		t.Fatalf("price_first picked %s, want cheapest %s", got, fx.channels[2].ID)
	}
	if got := reservePostgresChannel(t, ctx, st, priceKey, newID("req"), []string{fx.channels[2].ID}); got != fx.channels[1].ID {
		t.Fatalf("price_first failover picked %s, want next cheapest %s", got, fx.channels[1].ID)
	}
}

func TestPostgresRankingStatsBackedPreferences(t *testing.T) {
	ctx, fx := seedPostgresRankingFixture(t)
	st := fx.store
	// channels[2]: proven good and fast; channels[0]: proven problem and
	// slow; channels[1]: unknown, must land between them for both metrics.
	insertRankingBucket(t, ctx, st, fx.channels[2].ID, 20, 0, 3_000, 10) // 0 failures, 300ms avg
	insertRankingBucket(t, ctx, st, fx.channels[0].ID, 20, 10, 30_000, 10) // half failing, 3000ms avg

	successKey := fx.makeKey(t, contracts.SupplyRoutingSuccessFirst)
	if got := reservePostgresChannel(t, ctx, st, successKey, newID("req"), nil); got != fx.channels[2].ID {
		t.Fatalf("success_first picked %s, want proven-good %s", got, fx.channels[2].ID)
	}
	if got := reservePostgresChannel(t, ctx, st, successKey, newID("req"), []string{fx.channels[2].ID}); got != fx.channels[1].ID {
		t.Fatalf("success_first fallback picked %s, want unknown-over-problem %s", got, fx.channels[1].ID)
	}

	speedKey := fx.makeKey(t, contracts.SupplyRoutingSpeedFirst)
	if got := reservePostgresChannel(t, ctx, st, speedKey, newID("req"), nil); got != fx.channels[2].ID {
		t.Fatalf("speed_first picked %s, want measured-fast %s", got, fx.channels[2].ID)
	}
	if got := reservePostgresChannel(t, ctx, st, speedKey, newID("req"), []string{fx.channels[2].ID}); got != fx.channels[1].ID {
		t.Fatalf("speed_first fallback picked %s, want unknown-over-slow %s", got, fx.channels[1].ID)
	}
}
