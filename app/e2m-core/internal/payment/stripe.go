package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
)

const defaultStripeAPIBase = "https://api.stripe.com"

type Stripe struct {
	Client *http.Client
}

func (Stripe) Key() contracts.PaymentProviderKey { return contracts.PaymentProviderStripe }

func (s Stripe) CreateCheckout(ctx context.Context, input CheckoutRequest) (CheckoutResult, error) {
	amountMicros, err := decimalToMicros(input.Order.PayAmount)
	if err != nil || amountMicros <= 0 || amountMicros%10_000 != 0 {
		return CheckoutResult{}, fmt.Errorf("stripe: amount must have cent precision")
	}
	base := strings.TrimRight(strings.TrimSpace(input.APIBaseURL), "/")
	if base == "" {
		base = defaultStripeAPIBase
	}
	form := url.Values{
		"mode":                   {"payment"},
		"success_url":            {input.ReturnURL},
		"cancel_url":             {input.CancelURL},
		"client_reference_id":    {input.Order.OutTradeNo},
		"metadata[out_trade_no]": {input.Order.OutTradeNo},
		"payment_intent_data[metadata][out_trade_no]":   {input.Order.OutTradeNo},
		"line_items[0][quantity]":                       {"1"},
		"line_items[0][price_data][currency]":           {strings.ToLower(input.Order.Currency)},
		"line_items[0][price_data][unit_amount]":        {strconv.FormatInt(amountMicros/10_000, 10)},
		"line_items[0][price_data][product_data][name]": {"E2M balance recharge " + input.Order.OutTradeNo},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return CheckoutResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+input.SecretKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", input.Order.OutTradeNo)
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return CheckoutResult{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return CheckoutResult{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return CheckoutResult{}, fmt.Errorf("stripe: checkout returned HTTP %d", response.StatusCode)
	}
	var result struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return CheckoutResult{}, err
	}
	if strings.TrimSpace(result.ID) == "" || strings.TrimSpace(result.URL) == "" {
		return CheckoutResult{}, errors.New("stripe: incomplete checkout response")
	}
	return CheckoutResult{ProviderOrderID: result.ID, CheckoutURL: result.URL}, nil
}

func (s Stripe) ExpireCheckout(ctx context.Context, input CheckoutExpiryRequest) error {
	providerOrderID := strings.TrimSpace(input.ProviderOrderID)
	if providerOrderID == "" || len(providerOrderID) > 128 || strings.ContainsAny(providerOrderID, "/?#\\") {
		return errors.New("stripe: invalid checkout session id")
	}
	base := strings.TrimRight(strings.TrimSpace(input.APIBaseURL), "/")
	if base == "" {
		base = defaultStripeAPIBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/checkout/sessions/"+url.PathEscape(providerOrderID)+"/expire", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+input.SecretKey)
	req.Header.Set("Idempotency-Key", "expire-"+providerOrderID)
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("stripe: expire checkout returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (Stripe) VerifyNotification(payload []byte, signature, webhookSecret string, now time.Time) (VerifiedNotification, error) {
	timestamp, signatures, err := parseStripeSignature(signature)
	if err != nil || timestamp < now.Add(-5*time.Minute).Unix() || timestamp > now.Add(5*time.Minute).Unix() {
		return VerifiedNotification{}, ErrInvalidNotification
	}
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	_, _ = mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(payload)
	expected := mac.Sum(nil)
	valid := false
	for _, signatureHex := range signatures {
		decoded, decodeErr := hex.DecodeString(signatureHex)
		if decodeErr == nil && hmac.Equal(decoded, expected) {
			valid = true
		}
	}
	if !valid {
		return VerifiedNotification{}, ErrInvalidNotification
	}
	var event struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object struct {
				ID              string            `json:"id"`
				PaymentStatus   string            `json:"payment_status"`
				PaymentIntent   string            `json:"payment_intent"`
				AmountTotal     int64             `json:"amount_total"`
				Currency        string            `json:"currency"`
				ClientReference string            `json:"client_reference_id"`
				Metadata        map[string]string `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
		Created int64 `json:"created"`
	}
	if err := json.Unmarshal(payload, &event); err != nil || event.Type != "checkout.session.completed" || event.Data.Object.PaymentStatus != "paid" {
		return VerifiedNotification{}, ErrInvalidNotification
	}
	outTradeNo := strings.TrimSpace(event.Data.Object.ClientReference)
	if metadataTradeNo := strings.TrimSpace(event.Data.Object.Metadata["out_trade_no"]); outTradeNo == "" {
		outTradeNo = metadataTradeNo
	} else if metadataTradeNo != "" && metadataTradeNo != outTradeNo {
		return VerifiedNotification{}, ErrInvalidNotification
	}
	paidAt := time.Unix(event.Created, 0).UTC()
	if event.ID == "" || event.Data.Object.ID == "" || event.Data.Object.PaymentIntent == "" || outTradeNo == "" || event.Data.Object.AmountTotal <= 0 || len(event.Data.Object.Currency) != 3 || event.Created <= 0 {
		return VerifiedNotification{}, ErrInvalidNotification
	}
	return VerifiedNotification{EventID: event.ID, ProviderOrderID: event.Data.Object.ID, OutTradeNo: outTradeNo,
		PaymentTradeNo: event.Data.Object.PaymentIntent, PaidAmountMicros: event.Data.Object.AmountTotal * 10_000,
		Currency: strings.ToUpper(event.Data.Object.Currency), PaidAt: paidAt}, nil
}

func parseStripeSignature(header string) (int64, []string, error) {
	var timestamp int64
	var signatures []string
	for _, item := range strings.Split(header, ",") {
		parts := strings.SplitN(strings.TrimSpace(item), "=", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "t":
			timestamp, _ = strconv.ParseInt(parts[1], 10, 64)
		case "v1":
			signatures = append(signatures, parts[1])
		}
	}
	if timestamp <= 0 || len(signatures) == 0 {
		return 0, nil, ErrInvalidNotification
	}
	return timestamp, signatures, nil
}

func decimalToMicros(raw string) (int64, error) {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	if len(parts) != 2 || len(parts[1]) != 2 {
		return 0, ErrInvalidNotification
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return 0, ErrInvalidNotification
	}
	fraction, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || fraction < 0 || fraction > 99 {
		return 0, ErrInvalidNotification
	}
	return whole*1_000_000 + fraction*10_000, nil
}
