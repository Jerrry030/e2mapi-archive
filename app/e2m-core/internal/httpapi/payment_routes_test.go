package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/vault"
)

func TestPaymentRoutesFailClosedWithoutFeatureFlags(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	srv.SetBusinessFeatureFlags(BusinessFeatureFlags{})
	handler := srv.Routes()
	ctx := context.Background()

	admin := createLoginUser(t, authSvc, "pay-gate-admin@example.com", contracts.UserRolePlatformAdmin)
	adminToken, _, err := authSvc.Login(ctx, admin.Email, "password123")
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}

	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/payment/config"},
		{http.MethodGet, "/api/v1/admin/payment/providers"},
		{http.MethodGet, "/api/v1/admin/payment/orders"},
		{http.MethodPost, "/api/v1/owner/hybrid-supply/recharge-orders"},
		{http.MethodPost, "/api/v1/payment/webhooks/stripe/payprov-1"},
	}
	for _, tc := range cases {
		w := do(t, handler, tc.method, tc.path, adminToken, nil)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "feature_disabled") {
			t.Fatalf("%s %s must fail closed when payments are disabled, got %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

func TestPaymentRoutesNeedOnlyThePaymentsFlag(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	srv.SetBusinessFeatureFlags(BusinessFeatureFlags{Payments: true})
	handler := srv.Routes()
	ctx := context.Background()

	admin := createLoginUser(t, authSvc, "pay-flag-admin@example.com", contracts.UserRolePlatformAdmin)
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")

	if w := do(t, handler, http.MethodGet, "/api/v1/admin/payment/config", adminToken, nil); w.Code != http.StatusOK {
		t.Fatalf("payments flag alone must open admin payment config, got %d %s", w.Code, w.Body.String())
	}
	// These reach their handlers (provider lookup / vault checks fail later);
	// the gate itself must not answer feature_disabled anymore.
	if w := do(t, handler, http.MethodPost, "/api/v1/payment/webhooks/stripe/payprov-missing", adminToken, nil); strings.Contains(w.Body.String(), "feature_disabled") {
		t.Fatalf("webhook path must pass the gate with payments enabled, got %d %s", w.Code, w.Body.String())
	}
	if w := do(t, handler, http.MethodPost, "/api/v1/owner/hybrid-supply/recharge-orders", adminToken, map[string]any{"amount": "10.00", "payment_type": "stripe"}); strings.Contains(w.Body.String(), "feature_disabled") {
		t.Fatalf("recharge path must pass the gate with payments enabled, got %d %s", w.Code, w.Body.String())
	}
}

func TestPaymentAdminRoutesRequirePlatformAdmin(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	handler := srv.Routes()
	ctx := context.Background()

	admin := createLoginUser(t, authSvc, "pay-admin@example.com", contracts.UserRolePlatformAdmin)
	owner := createLoginUser(t, authSvc, "pay-nonadmin@example.com", contracts.UserRoleOwner)
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")
	ownerToken, _, _ := authSvc.Login(ctx, owner.Email, "password123")

	if w := do(t, handler, http.MethodGet, "/api/v1/admin/payment/config", ownerToken, nil); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin payment config read must 403, got %d %s", w.Code, w.Body.String())
	}
	if w := do(t, handler, http.MethodGet, "/api/v1/admin/payment/config", adminToken, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "order_timeout_minutes") {
		t.Fatalf("admin payment config read failed: %d %s", w.Code, w.Body.String())
	}
	if w := do(t, handler, http.MethodGet, "/api/v1/admin/payment/orders", ownerToken, nil); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin payment order list must 403, got %d %s", w.Code, w.Body.String())
	}
}

func TestCreateRechargeOrderReturnsCheckoutURL(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	v := vault.NewMemoryVault()
	srv.SetVault(v)
	handler := srv.Routes()
	ctx := context.Background()

	owner := createLoginUser(t, authSvc, "pay-recharge-owner@example.com", contracts.UserRoleOwner)
	ownerToken, _, _ := authSvc.Login(ctx, owner.Email, "password123")

	fakeStripe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/checkout/sessions" || r.Header.Get("Authorization") != "Bearer sk-test-secret" {
			http.Error(w, "unexpected checkout request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_local_1","url":"https://checkout.stripe.example/session/cs_local_1"}`))
	}))
	defer fakeStripe.Close()

	secretRef, err := v.Store(ctx, "credential_ref:test-stripe-secret", "sk-test-secret")
	if err != nil {
		t.Fatalf("store stripe secret: %v", err)
	}
	provider, err := st.CreatePaymentProvider(ctx, contracts.PaymentProvider{
		Name: "stripe local", ProviderKey: contracts.PaymentProviderStripe, Enabled: true,
		Config:     map[string]string{"currency": "CNY", "apiBase": fakeStripe.URL},
		SecretRefs: map[string]string{"secretKey": secretRef},
	})
	if err != nil {
		t.Fatalf("create payment provider: %v", err)
	}
	if _, err := st.UpsertPaymentConfig(ctx, contracts.PaymentConfig{
		Enabled: true, MinAmount: 1, MaxAmount: 10000, OrderTimeoutMinutes: 30,
		MaxPendingOrders: 3, EnabledPaymentTypes: []string{"stripe"},
	}); err != nil {
		t.Fatalf("upsert payment config: %v", err)
	}

	w := do(t, handler, http.MethodPost, "/api/v1/owner/hybrid-supply/recharge-orders", ownerToken, map[string]any{
		"amount": "10.00", "currency": "CNY", "payment_type": "stripe",
	})
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), "https://checkout.stripe.example/session/cs_local_1") {
		t.Fatalf("create recharge order failed: %d %s", w.Code, w.Body.String())
	}
	page, err := st.ListPaymentOrders(ctx, contracts.PaymentOrderFilter{UserID: owner.ID, Page: 1, PageSize: 10})
	if err != nil || page.Total != 1 {
		t.Fatalf("expected exactly one recharge order, err=%v total=%d", err, page.Total)
	}
	order := page.Items[0]
	if order.Status != contracts.PaymentOrderPending || order.ProviderOrderID != "cs_local_1" || order.ProviderInstanceID != provider.ID {
		t.Fatalf("unexpected recharge order state: %+v", order)
	}
}

func TestStripeWebhookConfirmsRechargeExactlyOnce(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	v := vault.NewMemoryVault()
	srv.SetVault(v)
	handler := srv.Routes()
	ctx := context.Background()

	owner := createLoginUser(t, authSvc, "pay-webhook-owner@example.com", contracts.UserRoleOwner)
	const webhookSecret = "whsec-test-secret"
	webhookRef, err := v.Store(ctx, "credential_ref:test-stripe-webhook", webhookSecret)
	if err != nil {
		t.Fatalf("store webhook secret: %v", err)
	}
	provider, err := st.CreatePaymentProvider(ctx, contracts.PaymentProvider{
		Name: "stripe webhook", ProviderKey: contracts.PaymentProviderStripe, Enabled: true,
		Config:     map[string]string{"currency": "CNY"},
		SecretRefs: map[string]string{"webhookSecret": webhookRef},
	})
	if err != nil {
		t.Fatalf("create payment provider: %v", err)
	}
	order, err := st.CreatePaymentOrder(ctx, contracts.PaymentOrder{
		UserID: owner.ID, UserEmail: owner.Email, Amount: "10.00", PayAmount: "10.00", FeeRate: "0",
		Currency: "CNY", PaymentType: "stripe", OutTradeNo: "recharge_webhook_test_1",
		OrderType: contracts.PaymentOrderBalance, ProviderInstanceID: provider.ID,
		ProviderKey: provider.ProviderKey, Status: contracts.PaymentOrderPending,
		ExpiresAt: time.Now().UTC().Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create pending order: %v", err)
	}

	now := time.Now().UTC()
	body := fmt.Sprintf(`{"id":"evt_webhook_1","type":"checkout.session.completed","created":%d,"data":{"object":{"id":"cs_webhook_1","payment_status":"paid","payment_intent":"pi_webhook_1","amount_total":1000,"currency":"cny","client_reference_id":%q}}}`,
		now.Unix(), order.OutTradeNo)
	path := "/api/v1/payment/webhooks/stripe/" + provider.ID

	if w := doStripeWebhook(t, handler, path, body, "t=1,v1=deadbeef"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid signature must 400, got %d %s", w.Code, w.Body.String())
	}

	signature := stripeTestSignature(webhookSecret, body, now)
	if w := doStripeWebhook(t, handler, path, body, signature); w.Code != http.StatusOK {
		t.Fatalf("webhook confirm failed: %d %s", w.Code, w.Body.String())
	}
	wallet, err := st.GetWallet(ctx, owner.ID, "CNY")
	if err != nil || wallet.AvailableMicros != 10_000_000 {
		t.Fatalf("wallet must be credited exactly 10.00, err=%v wallet=%+v", err, wallet)
	}
	confirmed, err := st.GetPaymentOrder(ctx, order.ID)
	if err != nil || confirmed.Status != contracts.PaymentOrderCompleted {
		t.Fatalf("order must be completed, err=%v order=%+v", err, confirmed)
	}

	// A duplicate delivery of the same event must acknowledge without a second credit.
	if w := doStripeWebhook(t, handler, path, body, signature); w.Code != http.StatusOK {
		t.Fatalf("duplicate webhook must still acknowledge, got %d %s", w.Code, w.Body.String())
	}
	wallet, err = st.GetWallet(ctx, owner.ID, "CNY")
	if err != nil || wallet.AvailableMicros != 10_000_000 {
		t.Fatalf("duplicate webhook must not credit twice, err=%v wallet=%+v", err, wallet)
	}
}

func TestStripeWebhookRecordsAuthenticatedOrphan(t *testing.T) {
	srv, st, _ := newTestServer(t)
	v := vault.NewMemoryVault()
	srv.SetVault(v)
	handler := srv.Routes()
	ctx := context.Background()

	const webhookSecret = "whsec-orphan-secret"
	webhookRef, err := v.Store(ctx, "credential_ref:test-stripe-orphan", webhookSecret)
	if err != nil {
		t.Fatalf("store webhook secret: %v", err)
	}
	provider, err := st.CreatePaymentProvider(ctx, contracts.PaymentProvider{
		Name: "stripe orphan", ProviderKey: contracts.PaymentProviderStripe, Enabled: true,
		Config:     map[string]string{"currency": "CNY"},
		SecretRefs: map[string]string{"webhookSecret": webhookRef},
	})
	if err != nil {
		t.Fatalf("create payment provider: %v", err)
	}

	now := time.Now().UTC()
	body := fmt.Sprintf(`{"id":"evt_orphan_1","type":"checkout.session.completed","created":%d,"data":{"object":{"id":"cs_orphan_1","payment_status":"paid","payment_intent":"pi_orphan_1","amount_total":500,"currency":"cny","client_reference_id":"recharge_unknown_order"}}}`, now.Unix())
	w := doStripeWebhook(t, handler, "/api/v1/payment/webhooks/stripe/"+provider.ID, body, stripeTestSignature(webhookSecret, body, now))
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated orphan must be acknowledged, got %d %s", w.Code, w.Body.String())
	}
	audits, err := st.ListAuditsByTarget(ctx, "payment_callback", "evt_orphan_1")
	if err != nil || len(audits) == 0 {
		t.Fatalf("orphan webhook must leave an audit trail, err=%v audits=%d", err, len(audits))
	}
}

func TestEasyPayWebhookConfirmsRechargeExactlyOnce(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	v := vault.NewMemoryVault()
	srv.SetVault(v)
	handler := srv.Routes()
	ctx := context.Background()

	owner := createLoginUser(t, authSvc, "easypay-owner@example.com", contracts.UserRoleOwner)
	const merchantKey = "merchant-key-e2m"
	keyRef, err := v.Store(ctx, "credential_ref:test-easypay-key", merchantKey)
	if err != nil {
		t.Fatalf("store merchant key: %v", err)
	}
	provider, err := st.CreatePaymentProvider(ctx, contracts.PaymentProvider{
		Name: "easypay main", ProviderKey: contracts.PaymentProviderEasyPay, Enabled: true,
		Config:     map[string]string{"pid": "1001", "apiBase": "https://epay.example"},
		SecretRefs: map[string]string{"pkey": keyRef},
	})
	if err != nil {
		t.Fatalf("create payment provider: %v", err)
	}
	order, err := st.CreatePaymentOrder(ctx, contracts.PaymentOrder{
		UserID: owner.ID, UserEmail: owner.Email, Amount: "10.00", PayAmount: "10.00", FeeRate: "0",
		Currency: "CNY", PaymentType: "alipay", OutTradeNo: "recharge_easypay_httpapi_1",
		OrderType: contracts.PaymentOrderBalance, ProviderInstanceID: provider.ID,
		ProviderKey: provider.ProviderKey, Status: contracts.PaymentOrderPending,
		ExpiresAt: time.Now().UTC().Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create pending order: %v", err)
	}
	if _, err := st.BindPaymentOrderCheckout(ctx, order.ID, order.OutTradeNo); err != nil {
		t.Fatalf("bind checkout: %v", err)
	}

	query := easyPaySignedNotify(map[string]string{
		"pid": "1001", "trade_no": "epay-trade-77", "out_trade_no": order.OutTradeNo,
		"type": "alipay", "name": "recharge", "money": "10.00", "trade_status": "TRADE_SUCCESS",
	}, merchantKey)
	path := "/api/v1/payment/webhooks/easypay/" + provider.ID + "?" + query

	tampered := strings.Replace(path, "money=10.00", "money=99.00", 1)
	if w := do(t, handler, http.MethodGet, tampered, "", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("tampered notify must 400, got %d %s", w.Code, w.Body.String())
	}

	first := do(t, handler, http.MethodGet, path, "", nil)
	if first.Code != http.StatusOK || first.Body.String() != "success" {
		t.Fatalf("notify must be acknowledged with plain success, got %d %q", first.Code, first.Body.String())
	}
	wallet, err := st.GetWallet(ctx, owner.ID, "CNY")
	if err != nil || wallet.AvailableMicros != 10_000_000 {
		t.Fatalf("wallet must be credited exactly 10.00, err=%v wallet=%+v", err, wallet)
	}

	replay := do(t, handler, http.MethodGet, path, "", nil)
	if replay.Code != http.StatusOK || replay.Body.String() != "success" {
		t.Fatalf("replayed notify must still acknowledge, got %d %q", replay.Code, replay.Body.String())
	}
	wallet, err = st.GetWallet(ctx, owner.ID, "CNY")
	if err != nil || wallet.AvailableMicros != 10_000_000 {
		t.Fatalf("replayed notify must not credit twice, err=%v wallet=%+v", err, wallet)
	}
}

// easyPaySignedNotify recomputes the aggregator MD5 scheme independently of
// the production implementation: ASCII-sorted k=v pairs joined by & with the
// merchant key appended.
func easyPaySignedNotify(params map[string]string, key string) string {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	values := url.Values{}
	for _, name := range names {
		pairs = append(pairs, name+"="+params[name])
		values.Set(name, params[name])
	}
	sum := md5.Sum([]byte(strings.Join(pairs, "&") + key))
	values.Set("sign", hex.EncodeToString(sum[:]))
	values.Set("sign_type", "MD5")
	return values.Encode()
}

func doStripeWebhook(t *testing.T, handler http.Handler, path, body, signature string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Stripe-Signature", signature)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func stripeTestSignature(secret, body string, now time.Time) string {
	timestamp := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write([]byte(body))
	return "t=" + timestamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
