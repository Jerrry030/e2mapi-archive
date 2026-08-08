package store

import (
	"context"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

// modelFixture builds one client, one group, one channel and one key so a test
// can state only what it varies.
type modelFixture struct {
	poolModels    []string
	channelModels []string
	keyModels     []string
}

func seedModelCatalog(t *testing.T, st Store, ctx context.Context, name string, fixture modelFixture) string {
	t.Helper()
	client, err := st.CreateUser(ctx, contracts.User{
		Email: name + "@example.test", PasswordHash: "test",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{
		Name: name, Models: fixture.poolModels,
		ResourceClass: contracts.ResourceClassStable,
		DeliveryMode:  contracts.UpstreamDeliverySupplyGateway,
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, DisplayName: name, Models: fixture.channelModels,
		AccountOwnership: contracts.GatewayAccountPlatformManaged,
		InventoryState:   contracts.UpstreamInventoryReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSupplyChannelEndpoint(ctx, contracts.SupplyChannelEndpoint{
		ChannelID: channel.ID, BaseURL: "https://upstream.example/v1", SecretRef: "credential_ref:" + name,
		Currency: "CNY", InputPriceMicrosPerMillion: 1, OutputPriceMicrosPerMillion: 1,
		MaxRequestMicros: 100, CapacityPercent: 100, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	plaintext := "e2m_v1_" + name
	if _, err := st.CreateVirtualKey(ctx, contracts.VirtualKey{
		UserID: client.ID, GroupID: pool.ID, Name: name,
		ResourceClass: contracts.ResourceClassStable,
		TokenHash:     contracts.HashVirtualKey(plaintext),
		SecretRef:     "credential_ref:virtual/" + name,
		Models:        fixture.keyModels, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	// Fund the wallet so a reservation can get past the balance gate. The
	// catalog itself deliberately ignores the balance, which one test asserts.
	if _, _, err := st.AdjustWalletBalance(ctx, client.ID, "CNY", 10_000_000, "seed-"+name, "test fixture"); err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func catalogModels(catalog contracts.SupplyModelCatalog) []string {
	out := make([]string, 0, len(catalog.Models))
	for _, entry := range catalog.Models {
		out = append(out, entry.Model)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The reservation predicate applies the pool and channel model gates
// independently, so a pair serves their INTERSECTION. Advertising the union
// would list models that then fail to reserve.
func TestListSupplyModelsIntersectsPoolAndChannelModels(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	token := seedModelCatalog(t, st, ctx, "intersect", modelFixture{
		poolModels:    []string{"gpt-4o", "gpt-4o-mini", "claude-sonnet"},
		channelModels: []string{"gpt-4o", "claude-sonnet", "gemini-pro"},
	})

	catalog, err := st.ListSupplyModels(ctx, contracts.HashVirtualKey(token), "CNY")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"claude-sonnet", "gpt-4o"}; !equalStrings(catalogModels(catalog), want) {
		t.Fatalf("catalog=%v, want %v", catalogModels(catalog), want)
	}
	if catalog.Unenumerable {
		t.Fatal("a fully declared catalog must be exhaustive")
	}
	for _, entry := range catalog.Models {
		if entry.Channels != 1 {
			t.Fatalf("%s: channels=%d, want 1", entry.Model, entry.Channels)
		}
		if entry.CreatedAt.IsZero() {
			t.Fatalf("%s: missing provenance timestamp", entry.Model)
		}
	}
}

// Every model the catalog advertises must actually reserve, and every model it
// withholds must actually fail. This is the property the whole endpoint exists
// to guarantee.
func TestListSupplyModelsAgreesWithReservation(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	token := seedModelCatalog(t, st, ctx, "agrees", modelFixture{
		poolModels:    []string{"gpt-4o", "gpt-4o-mini"},
		channelModels: []string{"gpt-4o", "gemini-pro"},
	})
	hash := contracts.HashVirtualKey(token)
	catalog, err := st.ListSupplyModels(ctx, hash, "CNY")
	if err != nil {
		t.Fatal(err)
	}
	advertised := map[string]bool{}
	for _, entry := range catalog.Models {
		advertised[entry.Model] = true
	}
	for _, model := range []string{"gpt-4o", "gpt-4o-mini", "gemini-pro", "never-declared"} {
		result, err := st.ReserveSupplyRequest(ctx, hash, "probe-"+model, model, "CNY", nil)
		reservable := err == nil
		if reservable != advertised[model] {
			t.Fatalf("%s: advertised=%v reservable=%v (err=%v)", model, advertised[model], reservable, err)
		}
		if reservable {
			// Release so the probe does not hold capacity for the next model.
			if _, err := st.ReleaseSupplyRequest(ctx, result.Reservation.ID, "test_probe", contracts.SupplyTelemetry{}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// An empty array on a key means "no allowlist", not "no models".
func TestListSupplyModelsNarrowsToTheKeyAllowlist(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	token := seedModelCatalog(t, st, ctx, "narrow", modelFixture{
		poolModels:    []string{"gpt-4o", "gpt-4o-mini", "claude-sonnet"},
		channelModels: []string{"gpt-4o", "gpt-4o-mini", "claude-sonnet"},
		keyModels:     []string{"gpt-4o", "claude-sonnet"},
	})

	catalog, err := st.ListSupplyModels(ctx, contracts.HashVirtualKey(token), "CNY")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"claude-sonnet", "gpt-4o"}; !equalStrings(catalogModels(catalog), want) {
		t.Fatalf("catalog=%v, want %v", catalogModels(catalog), want)
	}
}

// A key may name a model no upstream declares. It must not be advertised.
func TestListSupplyModelsDropsKeyModelsNoUpstreamServes(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	token := seedModelCatalog(t, st, ctx, "unserved", modelFixture{
		poolModels:    []string{"gpt-4o"},
		channelModels: []string{"gpt-4o"},
		keyModels:     []string{"gpt-4o", "model-nobody-has"},
	})

	catalog, err := st.ListSupplyModels(ctx, contracts.HashVirtualKey(token), "CNY")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gpt-4o"}; !equalStrings(catalogModels(catalog), want) {
		t.Fatalf("catalog=%v, want %v", catalogModels(catalog), want)
	}
}

// A pool and channel that both declare nothing accept any model, so the
// catalog cannot claim to be complete.
func TestListSupplyModelsReportsAWildcardUpstreamAsUnenumerable(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	token := seedModelCatalog(t, st, ctx, "wildcard", modelFixture{})

	catalog, err := st.ListSupplyModels(ctx, contracts.HashVirtualKey(token), "CNY")
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.Unenumerable || len(catalog.Models) != 0 {
		t.Fatalf("catalog=%+v, want unenumerable with no nameable models", catalog)
	}
}

// With a wildcard upstream the key's own list is the complete answer, because
// the upstream serves every name the key allows.
func TestListSupplyModelsResolvesAWildcardUpstreamThroughTheKeyAllowlist(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	token := seedModelCatalog(t, st, ctx, "wildcard-key", modelFixture{
		keyModels: []string{"gpt-4o", "claude-sonnet"},
	})

	catalog, err := st.ListSupplyModels(ctx, contracts.HashVirtualKey(token), "CNY")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"claude-sonnet", "gpt-4o"}; !equalStrings(catalogModels(catalog), want) {
		t.Fatalf("catalog=%v, want %v", catalogModels(catalog), want)
	}
	if catalog.Unenumerable {
		t.Fatal("a key allowlist makes the catalog exhaustive even behind a wildcard upstream")
	}
	for _, entry := range catalog.Models {
		if entry.Channels != 1 {
			t.Fatalf("%s: channels=%d, want the wildcard channel counted", entry.Model, entry.Channels)
		}
	}
}

// Discovery must not depend on having money. A customer with an empty wallet
// still needs to see what they could buy before topping up.
func TestListSupplyModelsIgnoresTheWalletBalance(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	token := seedModelCatalog(t, st, ctx, "broke", modelFixture{
		poolModels: []string{"gpt-4o"}, channelModels: []string{"gpt-4o"},
	})
	hash := contracts.HashVirtualKey(token)
	if _, _, err := st.AdjustWalletBalance(ctx, 1, "CNY", -10_000_000, "drain-broke", "spend it all"); err != nil {
		t.Fatal(err)
	}
	// A request is now refused for lack of funds...
	if _, err := st.ReserveSupplyRequest(ctx, hash, "broke-probe", "gpt-4o", "CNY", nil); err == nil {
		t.Fatal("an empty wallet must not be able to reserve")
	}
	// ...but the catalog still answers.
	catalog, err := st.ListSupplyModels(ctx, hash, "CNY")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gpt-4o"}; !equalStrings(catalogModels(catalog), want) {
		t.Fatalf("catalog=%v, want %v", catalogModels(catalog), want)
	}
}

// ReserveSupplyRequest rejects a candidate whose endpoint is priced in another
// currency AFTER selecting it, returning 400 rather than failing over. Listing
// such a model would advertise something that answers 400 on every call.
func TestListSupplyModelsExcludesForeignCurrencyEndpoints(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	token := seedModelCatalog(t, st, ctx, "currency", modelFixture{
		poolModels: []string{"gpt-4o"}, channelModels: []string{"gpt-4o"},
	})
	hash := contracts.HashVirtualKey(token)

	channels, err := st.ListUpstreamChannels(ctx, "")
	if err != nil || len(channels) != 1 {
		t.Fatalf("channels=%v err=%v", channels, err)
	}
	endpoint, err := st.GetSupplyChannelEndpoint(ctx, channels[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Currency = "USD"
	if _, err := st.UpsertSupplyChannelEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}

	catalog, err := st.ListSupplyModels(ctx, hash, "CNY")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 0 {
		t.Fatalf("catalog=%v, want the foreign-currency channel excluded", catalogModels(catalog))
	}
	// Confirm the reservation really does reject it, so the exclusion is not
	// over-cautious.
	if _, err := st.ReserveSupplyRequest(ctx, hash, "currency-probe", "gpt-4o", "CNY", nil); err == nil {
		t.Fatal("a foreign-currency endpoint must not be reservable")
	}
}

// The upstream is the authority on a model id: an id folded to lower case both
// misses the channel's case-sensitive e2m.model_mapping lookup and can be
// rejected by the upstream outright.
func TestListSupplyModelsReportsTheDeclaredSpellingNotAFoldedOne(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	token := seedModelCatalog(t, st, ctx, "casing", modelFixture{
		poolModels:    []string{"Qwen/Qwen2.5-7B-Instruct"},
		channelModels: []string{"Qwen/Qwen2.5-7B-Instruct"},
	})

	catalog, err := st.ListSupplyModels(ctx, contracts.HashVirtualKey(token), "CNY")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Qwen/Qwen2.5-7B-Instruct"}; !equalStrings(catalogModels(catalog), want) {
		t.Fatalf("catalog=%v, want %v", catalogModels(catalog), want)
	}
}

func TestListSupplyModelsRejectsUnusableKeys(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	token := seedModelCatalog(t, st, ctx, "gates", modelFixture{
		poolModels: []string{"gpt-4o"}, channelModels: []string{"gpt-4o"},
	})
	hash := contracts.HashVirtualKey(token)

	if _, err := st.ListSupplyModels(ctx, "", "CNY"); err != ErrInvalid {
		t.Fatalf("empty hash: err=%v, want ErrInvalid", err)
	}
	if _, err := st.ListSupplyModels(ctx, hash, "not-a-currency"); err != ErrInvalid {
		t.Fatalf("bad currency: err=%v, want ErrInvalid", err)
	}
	if _, err := st.ListSupplyModels(ctx, contracts.HashVirtualKey("e2m_v1_nobody"), "CNY"); err != ErrNotFound {
		t.Fatalf("unknown hash: err=%v, want ErrNotFound", err)
	}

	keys, err := st.ListVirtualKeys(ctx, 1)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
	disabled := keys[0]
	disabled.Enabled = false
	if _, err := st.UpdateVirtualKey(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ListSupplyModels(ctx, hash, "CNY"); err != ErrNotFound {
		t.Fatalf("disabled key: err=%v, want ErrNotFound", err)
	}
}

// The memory backend is the one exercised by every unit test, so its catalog
// must match the real database's. Runs only with a DSN, like the other
// real-database tests.
func TestPostgresListSupplyModelsMatchesTheMemoryBackend(t *testing.T) {
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

	name := newID("supply-models")
	// The mixed-case id proves the SQL groups case-insensitively but reports a
	// declared spelling, which is a different mechanism from the memory
	// backend's and so needs its own real-database evidence.
	token := seedModelCatalog(t, st, ctx, name, modelFixture{
		poolModels:    []string{"gpt-4o", "gpt-4o-mini", "claude-sonnet", "Qwen/Qwen2.5-7B-Instruct"},
		channelModels: []string{"gpt-4o", "claude-sonnet", "gemini-pro", "Qwen/Qwen2.5-7B-Instruct"},
	})
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		// Uniquely named fixtures only; production rows are never selected.
		// Deletes run in foreign-key order, wallet rows included, because the
		// fixture funds the customer.
		_, _ = st.pool.Exec(cleanup, `DELETE FROM virtual_keys WHERE name=$1`, name)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM supply_channel_endpoints WHERE channel_id IN
			(SELECT id FROM upstream_channels WHERE display_name=$1)`, name)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM upstream_channels WHERE display_name=$1`, name)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM upstream_pools WHERE name=$1`, name)
		_, _ = st.pool.Exec(cleanup, `DELETE FROM wallet_entries WHERE journal_id IN
			(SELECT id FROM wallet_journals WHERE user_id IN (SELECT id FROM users WHERE email=$1))`, name+"@example.test")
		_, _ = st.pool.Exec(cleanup, `DELETE FROM wallet_journals WHERE user_id IN
			(SELECT id FROM users WHERE email=$1)`, name+"@example.test")
		_, _ = st.pool.Exec(cleanup, `DELETE FROM wallet_accounts WHERE user_id IN
			(SELECT id FROM users WHERE email=$1)`, name+"@example.test")
		_, _ = st.pool.Exec(cleanup, `DELETE FROM users WHERE email=$1`, name+"@example.test")
	})

	catalog, err := st.ListSupplyModels(ctx, contracts.HashVirtualKey(token), "CNY")
	if err != nil {
		t.Fatal(err)
	}
	// Ordered case-insensitively, spelling preserved verbatim.
	if want := []string{"claude-sonnet", "gpt-4o", "Qwen/Qwen2.5-7B-Instruct"}; !equalStrings(catalogModels(catalog), want) {
		t.Fatalf("catalog=%v, want %v", catalogModels(catalog), want)
	}
	if catalog.Unenumerable {
		t.Fatal("a fully declared catalog must be exhaustive")
	}
	for _, entry := range catalog.Models {
		if entry.Channels != 1 || entry.CreatedAt.IsZero() {
			t.Fatalf("%s: channels=%d created=%v", entry.Model, entry.Channels, entry.CreatedAt)
		}
	}
	// A catalog scoped to a currency no endpoint uses must come back empty
	// rather than advertising models that answer 400 on every call.
	foreign, err := st.ListSupplyModels(ctx, contracts.HashVirtualKey(token), "USD")
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign.Models) != 0 || foreign.Unenumerable {
		t.Fatalf("USD catalog=%+v, want empty", foreign)
	}
}
