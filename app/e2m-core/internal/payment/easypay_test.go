package payment

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
)

const easyPayTestKey = "merchant-key-123"

func easyPayNotifyQuery(t *testing.T, overrides map[string]string) string {
	t.Helper()
	params := url.Values{
		"pid":          {"1001"},
		"trade_no":     {"epay-trade-1"},
		"out_trade_no": {"recharge_easypay_1"},
		"type":         {"alipay"},
		"name":         {"E2M balance recharge"},
		"money":        {"10.00"},
		"trade_status": {"TRADE_SUCCESS"},
	}
	for key, value := range overrides {
		params.Set(key, value)
	}
	params.Set("sign", easyPaySign(params, easyPayTestKey))
	params.Set("sign_type", "MD5")
	return params.Encode()
}

func TestEasyPayCreateCheckoutBuildsSignedSubmitURL(t *testing.T) {
	result, err := (EasyPay{}).CreateCheckout(context.Background(), CheckoutRequest{
		Order: contracts.PaymentOrder{
			OutTradeNo: "recharge_easypay_1", PayAmount: "10.00", PaymentType: "alipay", Currency: "CNY",
		},
		ReturnURL:  "https://e2m.example/payment/success",
		SecretKey:  easyPayTestKey,
		APIBaseURL: "https://epay.example",
		MerchantID: "1001",
		NotifyURL:  "https://e2m.example/api/v1/payment/webhooks/easypay/payprov-1",
		ChannelID:  "77",
	})
	if err != nil {
		t.Fatalf("create checkout: %v", err)
	}
	if result.ProviderOrderID != "recharge_easypay_1" {
		t.Fatalf("provider order id must be the out trade number, got %q", result.ProviderOrderID)
	}
	parsed, err := url.Parse(result.CheckoutURL)
	if err != nil || parsed.Host != "epay.example" || parsed.Path != "/submit.php" {
		t.Fatalf("unexpected checkout URL: %q err=%v", result.CheckoutURL, err)
	}
	query := parsed.Query()
	if query.Get("pid") != "1001" || query.Get("type") != "alipay" || query.Get("money") != "10.00" || query.Get("cid") != "77" {
		t.Fatalf("checkout parameters incomplete: %v", query)
	}
	if query.Get("sign") != easyPaySign(query, easyPayTestKey) || query.Get("sign_type") != "MD5" {
		t.Fatalf("checkout signature invalid: %v", query)
	}
	if strings.Contains(result.CheckoutURL, easyPayTestKey) {
		t.Fatalf("merchant key must never appear in the checkout URL")
	}
}

func TestEasyPayVerifyNotification(t *testing.T) {
	now := time.Now().UTC()
	verified, err := (EasyPay{}).VerifyNotification([]byte(easyPayNotifyQuery(t, nil)), "", easyPayTestKey, now)
	if err != nil {
		t.Fatalf("valid notify rejected: %v", err)
	}
	if verified.EventID != "easypay:epay-trade-1" || verified.OutTradeNo != "recharge_easypay_1" ||
		verified.PaymentTradeNo != "epay-trade-1" || verified.PaidAmountMicros != 10_000_000 || verified.Currency != "CNY" {
		t.Fatalf("unexpected verified notification: %+v", verified)
	}

	if _, err := (EasyPay{}).VerifyNotification([]byte(easyPayNotifyQuery(t, nil)), "", "wrong-key", now); err == nil {
		t.Fatalf("wrong merchant key must fail verification")
	}
	tampered := strings.Replace(easyPayNotifyQuery(t, nil), "money=10.00", "money=99.00", 1)
	if _, err := (EasyPay{}).VerifyNotification([]byte(tampered), "", easyPayTestKey, now); err == nil {
		t.Fatalf("tampered amount must fail verification")
	}
	if _, err := (EasyPay{}).VerifyNotification([]byte(easyPayNotifyQuery(t, map[string]string{"trade_status": "WAIT_BUYER_PAY"})), "", easyPayTestKey, now); err == nil {
		t.Fatalf("non-success trade status must fail verification")
	}
}

func TestEasyPayQueryCheckout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php" || r.URL.Query().Get("act") != "order" ||
			r.URL.Query().Get("pid") != "1001" || r.URL.Query().Get("key") != easyPayTestKey {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		switch r.URL.Query().Get("out_trade_no") {
		case "recharge_paid":
			_, _ = w.Write([]byte(`{"code":1,"status":1,"trade_no":"epay-trade-9","out_trade_no":"recharge_paid","money":"10.00"}`))
		default:
			_, _ = w.Write([]byte(`{"code":1,"status":0,"out_trade_no":"recharge_unpaid"}`))
		}
	}))
	defer server.Close()

	paid, err := (EasyPay{}).QueryCheckout(context.Background(), CheckoutQueryRequest{
		ProviderOrderID: "recharge_paid", SecretKey: easyPayTestKey, APIBaseURL: server.URL, MerchantID: "1001",
	})
	if err != nil || !paid.Paid || paid.PaymentTradeNo != "epay-trade-9" || paid.PaidAmountMicros != 10_000_000 {
		t.Fatalf("paid query failed: err=%v result=%+v", err, paid)
	}
	unpaid, err := (EasyPay{}).QueryCheckout(context.Background(), CheckoutQueryRequest{
		ProviderOrderID: "recharge_unpaid", SecretKey: easyPayTestKey, APIBaseURL: server.URL, MerchantID: "1001",
	})
	if err != nil || unpaid.Paid {
		t.Fatalf("unpaid query failed: err=%v result=%+v", err, unpaid)
	}
}

func TestEasyPayMoneyToMicros(t *testing.T) {
	cases := map[string]int64{"10.00": 10_000_000, "10.5": 10_500_000, "10": 10_000_000, "0.01": 10_000}
	for raw, want := range cases {
		got, err := easyPayMoneyToMicros(raw)
		if err != nil || got != want {
			t.Fatalf("money %q -> %d (err=%v), want %d", raw, got, err, want)
		}
	}
	for _, invalid := range []string{"", "0", "-1", "1.234", "abc", "1.2.3"} {
		if _, err := easyPayMoneyToMicros(invalid); err == nil {
			t.Fatalf("money %q must be rejected", invalid)
		}
	}
}
