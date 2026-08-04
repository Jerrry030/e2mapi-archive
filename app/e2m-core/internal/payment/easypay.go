package payment

import (
	"context"
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
)

// EasyPay implements the widespread 易支付 aggregator protocol: MD5-signed
// form parameters, a hosted submit page for checkout, an unauthenticated
// notify callback verified by the same signature, and a merchant-key order
// query API. There is no provider-side session to create or expire.
type EasyPay struct {
	Client *http.Client
}

func (EasyPay) Key() contracts.PaymentProviderKey { return contracts.PaymentProviderEasyPay }

// CreateCheckout builds the signed submit.php redirect URL. No network call
// happens here; the customer's browser carries the parameters to the gateway.
func (EasyPay) CreateCheckout(ctx context.Context, input CheckoutRequest) (CheckoutResult, error) {
	base := strings.TrimRight(strings.TrimSpace(input.APIBaseURL), "/")
	pid := strings.TrimSpace(input.MerchantID)
	if base == "" || !strings.HasPrefix(base, "http") {
		return CheckoutResult{}, errors.New("easypay: apiBase is required")
	}
	if pid == "" || strings.TrimSpace(input.SecretKey) == "" || strings.TrimSpace(input.NotifyURL) == "" {
		return CheckoutResult{}, errors.New("easypay: merchant id, key, and notify URL are required")
	}
	if _, err := easyPayMoneyToMicros(input.Order.PayAmount); err != nil {
		return CheckoutResult{}, err
	}
	params := url.Values{
		"pid":          {pid},
		"type":         {strings.TrimSpace(input.Order.PaymentType)},
		"out_trade_no": {input.Order.OutTradeNo},
		"notify_url":   {input.NotifyURL},
		"return_url":   {input.ReturnURL},
		"name":         {"E2M balance recharge " + input.Order.OutTradeNo},
		"money":        {input.Order.PayAmount},
	}
	if cid := strings.TrimSpace(input.ChannelID); cid != "" {
		params.Set("cid", cid)
	}
	params.Set("sign", easyPaySign(params, input.SecretKey))
	params.Set("sign_type", "MD5")
	// The out_trade_no doubles as the provider order id: the protocol has no
	// session identifier before the customer pays.
	return CheckoutResult{
		ProviderOrderID: input.Order.OutTradeNo,
		CheckoutURL:     base + "/submit.php?" + params.Encode(),
	}, nil
}

// ExpireCheckout is a no-op: the protocol has no provider-side session, so
// closing the local order is sufficient to stop a late payment from settling
// (a late notify then fails order matching and is durably recorded).
func (EasyPay) ExpireCheckout(ctx context.Context, input CheckoutExpiryRequest) error {
	return nil
}

// VerifyNotification authenticates a notify callback. The payload is the raw
// urlencoded parameter string (query for GET, body for POST); the webhook
// secret is the merchant key.
func (EasyPay) VerifyNotification(payload []byte, signature, webhookSecret string, now time.Time) (VerifiedNotification, error) {
	if len(payload) > 1<<16 {
		return VerifiedNotification{}, ErrInvalidNotification
	}
	params, err := url.ParseQuery(string(payload))
	if err != nil {
		return VerifiedNotification{}, ErrInvalidNotification
	}
	provided := strings.ToLower(strings.TrimSpace(params.Get("sign")))
	if provided == "" || !strings.EqualFold(params.Get("sign_type"), "MD5") {
		return VerifiedNotification{}, ErrInvalidNotification
	}
	expected := easyPaySign(params, webhookSecret)
	if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		return VerifiedNotification{}, ErrInvalidNotification
	}
	if params.Get("trade_status") != "TRADE_SUCCESS" {
		return VerifiedNotification{}, ErrInvalidNotification
	}
	outTradeNo := strings.TrimSpace(params.Get("out_trade_no"))
	tradeNo := strings.TrimSpace(params.Get("trade_no"))
	micros, err := easyPayMoneyToMicros(params.Get("money"))
	if err != nil || outTradeNo == "" || tradeNo == "" {
		return VerifiedNotification{}, ErrInvalidNotification
	}
	return VerifiedNotification{
		// The gateway retries the identical notify, so the trade number is a
		// stable event identity for exactly-once fulfillment.
		EventID:          "easypay:" + tradeNo,
		ProviderOrderID:  outTradeNo,
		OutTradeNo:       outTradeNo,
		PaymentTradeNo:   tradeNo,
		PaidAmountMicros: micros,
		Currency:         "CNY",
		PaidAt:           now.UTC(),
	}, nil
}

// QueryCheckout asks api.php for the order state so the expiry sweeper can
// recover a paid order whose notify never arrived.
func (e EasyPay) QueryCheckout(ctx context.Context, input CheckoutQueryRequest) (CheckoutQueryResult, error) {
	base := strings.TrimRight(strings.TrimSpace(input.APIBaseURL), "/")
	pid := strings.TrimSpace(input.MerchantID)
	outTradeNo := strings.TrimSpace(input.ProviderOrderID)
	if base == "" || pid == "" || strings.TrimSpace(input.SecretKey) == "" || outTradeNo == "" {
		return CheckoutQueryResult{}, errors.New("easypay: merchant id, key, apiBase, and order number are required")
	}
	query := url.Values{
		"act":          {"order"},
		"pid":          {pid},
		"key":          {input.SecretKey},
		"out_trade_no": {outTradeNo},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api.php?"+query.Encode(), nil)
	if err != nil {
		return CheckoutQueryResult{}, err
	}
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return CheckoutQueryResult{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return CheckoutQueryResult{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return CheckoutQueryResult{}, fmt.Errorf("easypay: order query returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Code       json.Number `json:"code"`
		Status     json.Number `json:"status"`
		TradeNo    string      `json:"trade_no"`
		OutTradeNo string      `json:"out_trade_no"`
		Money      string      `json:"money"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return CheckoutQueryResult{}, err
	}
	if result.Code.String() != "1" {
		return CheckoutQueryResult{}, fmt.Errorf("easypay: order query rejected with code %s", result.Code.String())
	}
	if result.OutTradeNo != "" && result.OutTradeNo != outTradeNo {
		return CheckoutQueryResult{}, errors.New("easypay: order query returned a different order")
	}
	if result.Status.String() != "1" {
		return CheckoutQueryResult{}, nil
	}
	micros, err := easyPayMoneyToMicros(result.Money)
	if err != nil || strings.TrimSpace(result.TradeNo) == "" {
		return CheckoutQueryResult{}, errors.New("easypay: paid order response is incomplete")
	}
	return CheckoutQueryResult{
		Paid: true, PaymentTradeNo: strings.TrimSpace(result.TradeNo),
		PaidAmountMicros: micros, Currency: "CNY",
	}, nil
}

// easyPaySign implements the aggregator MD5 scheme: ASCII-sort the non-empty
// parameters excluding sign/sign_type, join as k=v with &, append the merchant
// key directly, and hex-encode the lowercase MD5.
func easyPaySign(params url.Values, key string) string {
	names := make([]string, 0, len(params))
	for name := range params {
		if name == "sign" || name == "sign_type" || strings.TrimSpace(params.Get(name)) == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, name+"="+params.Get(name))
	}
	sum := md5.Sum([]byte(strings.Join(pairs, "&") + key))
	return hex.EncodeToString(sum[:])
}

// easyPayMoneyToMicros parses the gateway's yuan amount, which may carry zero
// to two decimal places, into integer micros.
func easyPayMoneyToMicros(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || parts[0] == "" || len(parts[0]) > 12 {
		return 0, ErrInvalidNotification
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return 0, ErrInvalidNotification
	}
	fraction := int64(0)
	if len(parts) == 2 {
		fractionRaw := parts[1]
		if fractionRaw == "" || len(fractionRaw) > 2 {
			return 0, ErrInvalidNotification
		}
		fractionRaw += strings.Repeat("0", 2-len(fractionRaw))
		if fraction, err = strconv.ParseInt(fractionRaw, 10, 64); err != nil || fraction < 0 {
			return 0, ErrInvalidNotification
		}
	}
	micros := whole*1_000_000 + fraction*10_000
	if micros <= 0 {
		return 0, ErrInvalidNotification
	}
	return micros, nil
}
