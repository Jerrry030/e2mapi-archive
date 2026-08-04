package paymentexpiry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/vault"
)

type fixture struct {
	store    store.Store
	runner   *Runner
	provider contracts.PaymentProvider
	user     contracts.User
	paid     map[string]bool
	expired  map[string]int
	server   *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{paid: map[string]bool{}, expired: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-sweeper-secret" {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/checkout/sessions/")
		if r.Method == http.MethodPost && strings.HasSuffix(id, "/expire") {
			f.expired[strings.TrimSuffix(id, "/expire")]++
			_, _ = w.Write([]byte(`{}`))
			return
		}
		status := "unpaid"
		if f.paid[id] {
			status = "paid"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "payment_status": status, "payment_intent": "pi_" + id,
			"amount_total": 1000, "currency": "cny",
		})
	}))
	t.Cleanup(f.server.Close)

	st := store.NewMemoryStore(time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC))
	f.store = st
	ctx := context.Background()
	user, err := st.CreateUser(ctx, contracts.User{Email: "sweeper-owner@example.com", DisplayName: "sweeper", Roles: []contracts.UserRole{contracts.UserRoleOwner}, Enabled: true})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	f.user = user

	v := vault.NewMemoryVault()
	secretRef, err := v.Store(ctx, "credential_ref:sweeper-stripe-secret", "sk-sweeper-secret")
	if err != nil {
		t.Fatalf("store secret: %v", err)
	}
	provider, err := st.CreatePaymentProvider(ctx, contracts.PaymentProvider{
		Name: "stripe sweeper", ProviderKey: contracts.PaymentProviderStripe, Enabled: true,
		Config:     map[string]string{"currency": "CNY", "apiBase": f.server.URL},
		SecretRefs: map[string]string{"secretKey": secretRef},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	f.provider = provider
	f.runner = New(st, v, time.Hour)
	return f
}

func (f *fixture) createOrder(t *testing.T, outTradeNo, providerOrderID string, expiresAt time.Time) contracts.PaymentOrder {
	t.Helper()
	order, err := f.store.CreatePaymentOrder(context.Background(), contracts.PaymentOrder{
		UserID: f.user.ID, UserEmail: f.user.Email, Amount: "10.00", PayAmount: "10.00", FeeRate: "0",
		Currency: "CNY", PaymentType: "stripe", OutTradeNo: outTradeNo,
		OrderType: contracts.PaymentOrderBalance, ProviderInstanceID: f.provider.ID,
		ProviderKey: f.provider.ProviderKey, Status: contracts.PaymentOrderPending, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("create order %s: %v", outTradeNo, err)
	}
	if providerOrderID != "" {
		if order, err = f.store.BindPaymentOrderCheckout(context.Background(), order.ID, providerOrderID); err != nil {
			t.Fatalf("bind order %s: %v", outTradeNo, err)
		}
	}
	return order
}

func TestSweepExpiresUnpaidOrderAndClosesUpstreamSession(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	past := time.Now().UTC().Add(-time.Minute)
	order := f.createOrder(t, "recharge_sweep_unpaid", "cs_sweep_unpaid", past)
	fresh := f.createOrder(t, "recharge_sweep_fresh", "cs_sweep_fresh", time.Now().UTC().Add(time.Hour))

	f.runner.RunOnce(ctx)

	swept, err := f.store.GetPaymentOrder(ctx, order.ID)
	if err != nil || swept.Status != contracts.PaymentOrderExpired {
		t.Fatalf("timed-out order must expire, err=%v order=%+v", err, swept)
	}
	if f.expired["cs_sweep_unpaid"] != 1 {
		t.Fatalf("upstream session must be closed exactly once, got %d", f.expired["cs_sweep_unpaid"])
	}
	untouched, err := f.store.GetPaymentOrder(ctx, fresh.ID)
	if err != nil || untouched.Status != contracts.PaymentOrderPending {
		t.Fatalf("fresh order must stay pending, err=%v order=%+v", err, untouched)
	}
	audits, err := f.store.ListAuditsByTarget(ctx, "payment_order", order.ID)
	if err != nil || len(audits) == 0 || audits[len(audits)-1].Action != "payment.order.expire" {
		t.Fatalf("expiry must leave an audit trail, err=%v audits=%+v", err, audits)
	}
}

func TestSweepRecoversPaidOrderExactlyOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	order := f.createOrder(t, "recharge_sweep_paid", "cs_sweep_paid", time.Now().UTC().Add(-time.Minute))
	f.paid["cs_sweep_paid"] = true

	f.runner.RunOnce(ctx)
	f.runner.RunOnce(ctx)

	recovered, err := f.store.GetPaymentOrder(ctx, order.ID)
	if err != nil || recovered.Status != contracts.PaymentOrderCompleted || recovered.PaymentTradeNo != "pi_cs_sweep_paid" {
		t.Fatalf("paid order must be recovered, err=%v order=%+v", err, recovered)
	}
	wallet, err := f.store.GetWallet(ctx, f.user.ID, "CNY")
	if err != nil || wallet.AvailableMicros != 10_000_000 {
		t.Fatalf("recovery must credit exactly once, err=%v wallet=%+v", err, wallet)
	}
	if f.expired["cs_sweep_paid"] != 0 {
		t.Fatalf("a paid session must never be expired upstream, got %d", f.expired["cs_sweep_paid"])
	}
}

func TestSweepExpiresOrderWithoutUpstreamSessionLocally(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	order := f.createOrder(t, "recharge_sweep_local", "", time.Now().UTC().Add(-time.Minute))

	f.runner.RunOnce(ctx)

	swept, err := f.store.GetPaymentOrder(ctx, order.ID)
	if err != nil || swept.Status != contracts.PaymentOrderExpired {
		t.Fatalf("order without checkout session must expire locally, err=%v order=%+v", err, swept)
	}
	if len(f.expired) != 0 {
		t.Fatalf("no upstream call expected, got %+v", f.expired)
	}
}

func TestSweepHandlesEasyPayOrders(t *testing.T) {
	ctx := context.Background()
	paidOrders := map[string]bool{"recharge_epay_paid": true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php" || r.URL.Query().Get("key") != "merchant-key-sweep" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		no := r.URL.Query().Get("out_trade_no")
		if paidOrders[no] {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "status": 1, "trade_no": "epay-" + no, "out_trade_no": no, "money": "10.00"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "status": 0, "out_trade_no": no})
	}))
	defer server.Close()

	st := store.NewMemoryStore(time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC))
	user, err := st.CreateUser(ctx, contracts.User{Email: "epay-sweep@example.com", DisplayName: "epay", Roles: []contracts.UserRole{contracts.UserRoleOwner}, Enabled: true})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	v := vault.NewMemoryVault()
	keyRef, err := v.Store(ctx, "credential_ref:epay-sweep-key", "merchant-key-sweep")
	if err != nil {
		t.Fatalf("store key: %v", err)
	}
	provider, err := st.CreatePaymentProvider(ctx, contracts.PaymentProvider{
		Name: "easypay sweep", ProviderKey: contracts.PaymentProviderEasyPay, Enabled: true,
		Config:     map[string]string{"pid": "1001", "apiBase": server.URL},
		SecretRefs: map[string]string{"pkey": keyRef},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	makeOrder := func(outTradeNo string) contracts.PaymentOrder {
		order, orderErr := st.CreatePaymentOrder(ctx, contracts.PaymentOrder{
			UserID: user.ID, UserEmail: user.Email, Amount: "10.00", PayAmount: "10.00", FeeRate: "0",
			Currency: "CNY", PaymentType: "alipay", OutTradeNo: outTradeNo,
			OrderType: contracts.PaymentOrderBalance, ProviderInstanceID: provider.ID,
			ProviderKey: provider.ProviderKey, Status: contracts.PaymentOrderPending,
			ExpiresAt: time.Now().UTC().Add(-time.Minute),
		})
		if orderErr != nil {
			t.Fatalf("create order %s: %v", outTradeNo, orderErr)
		}
		if order, orderErr = st.BindPaymentOrderCheckout(ctx, order.ID, outTradeNo); orderErr != nil {
			t.Fatalf("bind order %s: %v", outTradeNo, orderErr)
		}
		return order
	}
	paid := makeOrder("recharge_epay_paid")
	unpaid := makeOrder("recharge_epay_unpaid")

	runner := New(st, v, time.Hour)
	runner.RunOnce(ctx)
	runner.RunOnce(ctx)

	recovered, err := st.GetPaymentOrder(ctx, paid.ID)
	if err != nil || recovered.Status != contracts.PaymentOrderCompleted || recovered.PaymentTradeNo != "epay-recharge_epay_paid" {
		t.Fatalf("paid easypay order must be recovered, err=%v order=%+v", err, recovered)
	}
	wallet, err := st.GetWallet(ctx, user.ID, "CNY")
	if err != nil || wallet.AvailableMicros != 10_000_000 {
		t.Fatalf("recovery must credit exactly once, err=%v wallet=%+v", err, wallet)
	}
	expired, err := st.GetPaymentOrder(ctx, unpaid.ID)
	if err != nil || expired.Status != contracts.PaymentOrderExpired {
		t.Fatalf("unpaid easypay order must expire locally, err=%v order=%+v", err, expired)
	}
}

func TestSweepKeepsOrderPendingWhenUpstreamQueryFails(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	order := f.createOrder(t, "recharge_sweep_stuck", "cs_sweep_stuck", time.Now().UTC().Add(-time.Minute))
	f.server.Close() // upstream unreachable: the sweeper must stay conservative

	f.runner.RunOnce(ctx)

	stuck, err := f.store.GetPaymentOrder(ctx, order.ID)
	if err != nil || stuck.Status != contracts.PaymentOrderPending {
		t.Fatalf("unqueryable order must stay pending for the next cycle, err=%v order=%+v", err, stuck)
	}
}
