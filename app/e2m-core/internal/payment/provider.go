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
