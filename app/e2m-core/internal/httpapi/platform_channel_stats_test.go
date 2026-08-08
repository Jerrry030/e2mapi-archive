package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"e2m.local/contracts"
)

// The stats endpoint must aggregate exactly what settlement recorded, stay
// admin-only, and report an empty window as "no samples" rather than a
// fabricated zero rate.
func TestPlatformUpstreamStatsEndpoint(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	handler := srv.Routes()
	ctx := context.Background()

	admin := createLoginUser(t, authSvc, "stats-admin@example.com", contracts.UserRolePlatformAdmin)
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")
	client := createLoginUser(t, authSvc, "stats-client@example.com", contracts.UserRoleClient)
	clientToken, _, _ := authSvc.Login(ctx, client.Email, "password123")

	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{
		Name: "Stats pool", Models: []string{"gpt-test"},
		ResourceClass: contracts.ResourceClassEconomy, DeliveryMode: contracts.UpstreamDeliverySupplyGateway,
		Status: contracts.UpstreamPoolActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, DisplayName: "Stats channel", Models: []string{"gpt-test"},
		AccountOwnership: contracts.GatewayAccountPlatformManaged, InventoryState: contracts.UpstreamInventoryReady,
		Status: contracts.UpstreamChannelActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSupplyChannelEndpoint(ctx, contracts.SupplyChannelEndpoint{
		ChannelID: channel.ID, BaseURL: "https://upstream.example/v1", SecretRef: "credential_ref:supply/stats", MaskedValue: "sk-***",
		Currency: "CNY", InputPriceMicrosPerMillion: 1_000_000, OutputPriceMicrosPerMillion: 2_000_000,
		MaxRequestMicros: 100_000, MaxConcurrency: 10, CapacityPercent: 100, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	// An existing channel with no samples reports counts of zero and omits
	// every rate/average instead of inventing one.
	empty := do(t, handler, http.MethodGet, "/api/v1/platform/upstreams/"+channel.ID+"/stats", adminToken, nil)
	if empty.Code != http.StatusOK {
		t.Fatalf("empty stats: %d %s", empty.Code, empty.Body.String())
	}
	var emptyPayload map[string]any
	if err := json.Unmarshal(empty.Body.Bytes(), &emptyPayload); err != nil {
		t.Fatal(err)
	}
	if emptyPayload["requests"] != float64(0) || emptyPayload["bucket_seconds"] != float64(300) {
		t.Fatalf("empty payload=%v", emptyPayload)
	}
	for _, forbidden := range []string{"success_rate", "avg_ttft_ms", "avg_duration_ms"} {
		if _, present := emptyPayload[forbidden]; present {
			t.Fatalf("empty window must omit %s: %v", forbidden, emptyPayload)
		}
	}

	// Drive two real settlements through the reservation path so the endpoint
	// aggregates data produced exactly the way production writes it.
	plaintext := "e2m_v1_stats_key"
	if _, err := st.CreateVirtualKey(ctx, contracts.VirtualKey{
		UserID: client.ID, GroupID: pool.ID, Name: "stats", ResourceClass: contracts.ResourceClassEconomy,
		Prefix: "e2m_v1_", TokenHash: contracts.HashVirtualKey(plaintext), SecretRef: "credential_ref:virtual/stats",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AdjustWalletBalance(ctx, client.ID, "CNY", 1_000_000, "stats-test-credit", "test credit"); err != nil {
		t.Fatal(err)
	}
	reserved, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "stats-ok", "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SettleSupplyRequest(ctx, reserved.Reservation.ID, 10, 10,
		contracts.SupplyTelemetry{FirstTokenMS: 100, DurationMS: 200, Outcome: contracts.SupplyOutcomeSuccess}); err != nil {
		t.Fatal(err)
	}
	reserved2, err := st.ReserveSupplyRequest(ctx, contracts.HashVirtualKey(plaintext), "stats-fail", "gpt-test", "CNY", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReleaseSupplyRequest(ctx, reserved2.Reservation.ID, "upstream_transport_error",
		contracts.SupplyTelemetry{DurationMS: 300, Outcome: contracts.SupplyOutcomeFailure}); err != nil {
		t.Fatal(err)
	}

	response := do(t, handler, http.MethodGet, "/api/v1/platform/upstreams/"+channel.ID+"/stats?window_minutes=120", adminToken, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("stats: %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		ChannelID     string                               `json:"channel_id"`
		WindowMinutes int                                  `json:"window_minutes"`
		Requests      int64                                `json:"requests"`
		Failures      int64                                `json:"failures"`
		SuccessRate   *float64                             `json:"success_rate"`
		AvgTTFTMS     *int64                               `json:"avg_ttft_ms"`
		AvgDurationMS *int64                               `json:"avg_duration_ms"`
		Buckets       []contracts.SupplyChannelStatsBucket `json:"buckets"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ChannelID != channel.ID || payload.WindowMinutes != 120 || payload.Requests != 2 || payload.Failures != 1 || len(payload.Buckets) != 1 {
		t.Fatalf("payload=%+v", payload)
	}
	if payload.SuccessRate == nil || *payload.SuccessRate != 0.5 ||
		payload.AvgTTFTMS == nil || *payload.AvgTTFTMS != 100 ||
		payload.AvgDurationMS == nil || *payload.AvgDurationMS != 250 {
		t.Fatalf("aggregates=%+v", payload)
	}

	// Guards: admin-only, real channel, sane window.
	if w := do(t, handler, http.MethodGet, "/api/v1/platform/upstreams/"+channel.ID+"/stats", clientToken, nil); w.Code != http.StatusForbidden {
		t.Fatalf("client access must 403, got %d", w.Code)
	}
	if w := do(t, handler, http.MethodGet, "/api/v1/platform/upstreams/missing/stats", adminToken, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown channel must 404, got %d %s", w.Code, w.Body.String())
	}
	if w := do(t, handler, http.MethodGet, "/api/v1/platform/upstreams/"+channel.ID+"/stats?window_minutes=0", adminToken, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid window must 400, got %d", w.Code)
	}
}
