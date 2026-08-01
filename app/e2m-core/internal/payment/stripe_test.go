package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestStripeCreateCheckout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/checkout/sessions" || r.Header.Get("Authorization") != "Bearer sk_test" || r.Header.Get("Idempotency-Key") != "recharge_1" {
			t.Fatalf("request=%s %s auth=%q idem=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"))
		}
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		if values.Get("client_reference_id") != "recharge_1" || values.Get("line_items[0][price_data][unit_amount]") != "1234" || values.Get("line_items[0][price_data][currency]") != "cny" {
			t.Fatalf("form=%v", values)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test","url":"https://checkout.stripe.test/cs_test"}`))
	}))
	defer upstream.Close()
	result, err := (Stripe{Client: upstream.Client()}).CreateCheckout(context.Background(), CheckoutRequest{
		Order:     contracts.PaymentOrder{PayAmount: "12.34", Currency: "CNY", OutTradeNo: "recharge_1"},
		ReturnURL: "https://merchant.test/success", CancelURL: "https://merchant.test/cancel", SecretKey: "sk_test", APIBaseURL: upstream.URL,
	})
	if err != nil || result.ProviderOrderID != "cs_test" || result.CheckoutURL == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestStripeExpireCheckout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/checkout/sessions/cs_test/expire" || r.Header.Get("Authorization") != "Bearer sk_test" || r.Header.Get("Idempotency-Key") != "expire-cs_test" {
			t.Fatalf("request=%s %s auth=%q idem=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"))
		}
		_, _ = w.Write([]byte(`{"id":"cs_test","status":"expired"}`))
	}))
	defer upstream.Close()
	err := (Stripe{Client: upstream.Client()}).ExpireCheckout(context.Background(), CheckoutExpiryRequest{
		ProviderOrderID: "cs_test", SecretKey: "sk_test", APIBaseURL: upstream.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := (Stripe{}).ExpireCheckout(context.Background(), CheckoutExpiryRequest{ProviderOrderID: "../unsafe", SecretKey: "sk_test"}); err == nil {
		t.Fatal("unsafe checkout session id accepted")
	}
}

func TestStripeVerifyNotification(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	payload := []byte(`{"id":"evt_1","type":"checkout.session.completed","created":1800000000,"data":{"object":{"id":"cs_1","payment_status":"paid","payment_intent":"pi_1","amount_total":1234,"currency":"cny","client_reference_id":"recharge_1","metadata":{"out_trade_no":"recharge_1"}}}}`)
	secret := "whsec_test"
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strconv.FormatInt(now.Unix(), 10) + "."))
	_, _ = mac.Write(payload)
	signature := fmt.Sprintf("t=%d,v1=%s", now.Unix(), hex.EncodeToString(mac.Sum(nil)))
	verified, err := (Stripe{}).VerifyNotification(payload, signature, secret, now)
	if err != nil || verified.EventID != "evt_1" || verified.ProviderOrderID != "cs_1" || verified.PaidAmountMicros != 12_340_000 || verified.Currency != "CNY" {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
	if _, err := (Stripe{}).VerifyNotification(payload, signature, "wrong", now); err == nil {
		t.Fatal("invalid signature accepted")
	}
	if _, err := (Stripe{}).VerifyNotification(payload, signature, secret, now.Add(10*time.Minute)); err == nil {
		t.Fatal("stale signature accepted")
	}
}
