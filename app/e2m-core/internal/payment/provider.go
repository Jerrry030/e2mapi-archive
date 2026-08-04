package payment

import (
	"context"
	"errors"
	"time"

	"e2m.local/contracts"
)

var ErrInvalidNotification = errors.New("payment: invalid notification")

type CheckoutRequest struct {
	Order      contracts.PaymentOrder
	ReturnURL  string
	CancelURL  string
	SecretKey  string
	APIBaseURL string
	// MerchantID, NotifyURL, and ChannelID serve providers whose checkout is a
	// signed redirect instead of an API session (EasyPay). Stripe ignores them.
	MerchantID string
	NotifyURL  string
	ChannelID  string
}

type CheckoutResult struct {
	ProviderOrderID string
	CheckoutURL     string
}

type CheckoutExpiryRequest struct {
	ProviderOrderID string
	SecretKey       string
	APIBaseURL      string
}

type CheckoutQueryRequest struct {
	ProviderOrderID string
	SecretKey       string
	APIBaseURL      string
	// MerchantID is required by providers whose query API authenticates with a
	// merchant id + key pair (EasyPay). Stripe ignores it.
	MerchantID string
}

// CheckoutQueryResult reports the provider-side state of one checkout so the
// expiry sweeper can recover missed notifications before expiring an order.
type CheckoutQueryResult struct {
	Paid             bool
	PaymentTradeNo   string
	PaidAmountMicros int64
	Currency         string
}

type VerifiedNotification struct {
	EventID          string
	ProviderOrderID  string
	OutTradeNo       string
	PaymentTradeNo   string
	PaidAmountMicros int64
	Currency         string
	PaidAt           time.Time
}

type Provider interface {
	Key() contracts.PaymentProviderKey
	CreateCheckout(context.Context, CheckoutRequest) (CheckoutResult, error)
	ExpireCheckout(context.Context, CheckoutExpiryRequest) error
	VerifyNotification(payload []byte, signature, webhookSecret string, now time.Time) (VerifiedNotification, error)
}
