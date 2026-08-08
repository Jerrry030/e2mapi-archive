package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"e2m.local/contracts"
)

// The fixture stacks the deck against every preference: the platform default
// order (priority ASC) always favours channels[0], while the cheapest, most
// reliable, or fastest channel sits at the worst priority. A preference test
// therefore passes only when re-ranking actually happened.
func seedRoutingPreferenceFixture(t *testing.T) (*MemoryStore, [3]contracts.UpstreamChannel, func(contracts.SupplyRoutingPreference) string) {
	t.Helper()
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	base := time.Date(2026, 8, 7, 12, 2, 0, 0, time.UTC)
	st.now = func() time.Time { return base }
	owner, err := st.CreateUser(ctx, contracts.User{Email: "rank@example.com", DisplayName: "Rank", PasswordHash: "hash", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "Ranked", Models: []string{"gpt-test"}, ResourceClass: contracts.ResourceClassEconomy, DeliveryMode: contracts.UpstreamDeliverySupplyGateway})
	if err != nil {
		t.Fatal(err)
	}
	// channels[0] is the default-order winner (priority 1) and also the most
	// expensive; channels[2] is the cheapest at the worst priority.
	prices := [3]int64{10_000_000, 3_000_000, 1_000_000}
	var channels [3]contracts.UpstreamChannel
	for index := range channels {
		channel, channelErr := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
			PoolID: pool.ID, DisplayName: fmt.Sprintf("rank-%d", index), Models: []string{"gpt-test"},
			AccountOwnership: contracts.GatewayAccountPlatformManaged, InventoryState: contracts.UpstreamInventoryReady,
			Priority: index + 1,
		})
		if channelErr != nil {
			t.Fatal(channelErr)
		}
		if _, endpointErr := st.UpsertSupplyChannelEndpoint(ctx, contracts.SupplyChannelEndpoint{
			ChannelID: channel.ID, BaseURL: "https://upstream.example/v1", SecretRef: fmt.Sprintf("credential_ref:rank-%d", index), MaskedValue: "sk-***",
			Currency: "CNY", InputPriceMicrosPerMillion: prices[index], OutputPriceMicrosPerMillion: prices[index],
			MaxRequestMicros: 10_000, MaxConcurrency: 10, CapacityPercent: 100, Enabled: true,
		}); endpointErr != nil {
			t.Fatal(endpointErr)
		}
		channels[index] = channel
	}
	st.mu.Lock()
	st.wallets[walletMapKey(owner.ID, "CNY")] = contracts.Wallet{UserID: owner.ID, Currency: "CNY", AvailableMicros: 10_000_000, Version: 1, UpdatedAt: base}
	st.mu.Unlock()
	sequence := 0
	makeKey := func(preference contracts.SupplyRoutingPreference) string {
		sequence++
		plaintext := fmt.Sprintf("e2m_v1_rank_%d", sequence)
		if _, keyErr := st.CreateVirtualKey(ctx, contracts.VirtualKey{
			UserID: owner.ID, GroupID: pool.ID, Name: fmt.Sprintf("rank-%d", sequence), ResourceClass: contracts.ResourceClassEconomy,
			Prefix: "e2m_v1_", TokenHash: contracts.HashVirtualKey(plaintext), SecretRef: fmt.Sprintf("credential_ref:virtual/rank-%d", sequence),
			RoutingPreference: preference,
		}); keyErr != nil {
			t.Fatal(keyErr)
		}
		return plaintext
	}
	return st, channels, makeKey
}

func recordRankingSamples(t *testing.T, st *MemoryStore, channelID string, successes, failures int, ttftMS int64) {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := 0; i < successes; i++ {
		st.recordSupplyChannelStatsLocked(channelID, contracts.SupplyTelemetry{Outcome: contracts.SupplyOutcomeSuccess, FirstTokenMS: ttftMS, DurationMS: max(ttftMS, 1)}, st.now())
	}
	for i := 0; i < failures; i++ {
		st.recordSupplyChannelStatsLocked(channelID, contracts.SupplyTelemetry{Outcome: contracts.SupplyOutcomeFailure, DurationMS: 100}, st.now())
	}
}

func reserveChannel(t *testing.T, st *MemoryStore, plaintext string, requestID string, excluded []string) string {
	t.Helper()
	reserved, err := st.ReserveSupplyRequest(context.Background(), contracts.HashVirtualKey(plaintext), requestID, "gpt-test", "CNY", excluded)
	if err != nil {
		t.Fatal(err)
	}
	return reserved.Candidate.Channel.ID
}

// Without a preference (and with the explicit smart_auto choice) the platform
// default order must stay byte-for-byte what it was before this feature.
func TestMemoryReserveWithoutPreferenceKeepsDefaultOrder(t *testing.T) {
	st, channels, makeKey := seedRoutingPreferenceFixture(t)
	if got := reserveChannel(t, st, makeKey(""), "default-1", nil); got != channels[0].ID {
		t.Fatalf("unset preference picked %s, want default winner %s", got, channels[0].ID)
	}
	if got := reserveChannel(t, st, makeKey(contracts.SupplyRoutingSmartAuto), "default-2", nil); got != channels[0].ID {
		t.Fatalf("smart_auto picked %s, want default winner %s", got, channels[0].ID)
	}
}

func TestMemoryReservePriceFirstPicksCheapestAndFailsOverToNextCheapest(t *testing.T) {
	st, channels, makeKey := seedRoutingPreferenceFixture(t)
	key := makeKey(contracts.SupplyRoutingPriceFirst)
	if got := reserveChannel(t, st, key, "price-1", nil); got != channels[2].ID {
		t.Fatalf("price_first picked %s, want cheapest %s", got, channels[2].ID)
	}
	// The failover exclusion walks the same preference order: next cheapest,
	// not next by platform priority.
	if got := reserveChannel(t, st, key, "price-2", []string{channels[2].ID}); got != channels[1].ID {
		t.Fatalf("price_first failover picked %s, want next cheapest %s", got, channels[1].ID)
	}
}

// A channel with no evidence must rank between a proven-good and a
// proven-problem channel — never above the good one, never below the bad one.
func TestMemoryReserveSuccessFirstRanksProvenUnknownProblem(t *testing.T) {
	st, channels, makeKey := seedRoutingPreferenceFixture(t)
	recordRankingSamples(t, st, channels[2].ID, 20, 0, 200) // proven good: smoothed 1/30
	recordRankingSamples(t, st, channels[0].ID, 10, 10, 200) // proven problem: smoothed 11/30
	// channels[1] stays unknown: smoothed prior 1/10.
	key := makeKey(contracts.SupplyRoutingSuccessFirst)
	if got := reserveChannel(t, st, key, "success-1", nil); got != channels[2].ID {
		t.Fatalf("success_first picked %s, want proven-good %s", got, channels[2].ID)
	}
	if got := reserveChannel(t, st, key, "success-2", []string{channels[2].ID}); got != channels[1].ID {
		t.Fatalf("success_first fallback picked %s, want unknown-over-problem %s", got, channels[1].ID)
	}
}

func TestMemoryReserveSpeedFirstPrefersMeasuredFastOverUnknownOverSlow(t *testing.T) {
	st, channels, makeKey := seedRoutingPreferenceFixture(t)
	recordRankingSamples(t, st, channels[2].ID, 10, 0, 300)   // smoothed (3000+7500)/15 = 700ms
	recordRankingSamples(t, st, channels[0].ID, 10, 0, 3_000) // smoothed (30000+7500)/15 = 2500ms
	// channels[1] stays unknown: prior 1500ms sits between them.
	key := makeKey(contracts.SupplyRoutingSpeedFirst)
	if got := reserveChannel(t, st, key, "speed-1", nil); got != channels[2].ID {
		t.Fatalf("speed_first picked %s, want measured-fast %s", got, channels[2].ID)
	}
	if got := reserveChannel(t, st, key, "speed-2", []string{channels[2].ID}); got != channels[1].ID {
		t.Fatalf("speed_first fallback picked %s, want unknown-over-slow %s", got, channels[1].ID)
	}
}

func TestCreateVirtualKeyRejectsUnknownRoutingPreference(t *testing.T) {
	st, _, _ := seedRoutingPreferenceFixture(t)
	_, err := st.CreateVirtualKey(context.Background(), contracts.VirtualKey{
		UserID: 1, GroupID: "missing", Name: "bad", ResourceClass: contracts.ResourceClassEconomy,
		Prefix: "e2m_v1_", TokenHash: contracts.HashVirtualKey("e2m_v1_bad"), SecretRef: "credential_ref:bad",
		RoutingPreference: contracts.SupplyRoutingPreference("weird"),
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown preference error=%v", err)
	}
}
