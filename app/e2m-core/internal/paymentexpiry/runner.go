// Package paymentexpiry sweeps timed-out PENDING recharge orders. Before an
// order is expired the provider is queried one last time so a missed webhook
// becomes a recovered credit instead of a silent accounting gap.
package paymentexpiry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/payment"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/vault"
)

const DefaultInterval = time.Minute
const defaultBatchSize = 50

// Runner drives the sweep loop. Every decision is conservative: when the
// provider cannot be queried the order stays PENDING for the next cycle, and a
// racing webhook always wins because ExpirePaymentOrder only touches PENDING
// rows without an upstream trade number.
type Runner struct {
	store    store.Store
	secrets  vault.Vault
	stripe   payment.Stripe
	easypay  payment.EasyPay
	interval time.Duration
	batch    int
	now      func() time.Time
}

func New(st store.Store, secrets vault.Vault, intervals ...time.Duration) *Runner {
	interval := DefaultInterval
	if len(intervals) > 0 && intervals[0] > 0 {
		interval = intervals[0]
	}
	return &Runner{store: st, secrets: secrets, interval: interval, batch: defaultBatchSize, now: time.Now}
}

func (r *Runner) Run(ctx context.Context) {
	r.RunOnce(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.RunOnce(ctx)
		}
	}
}

func (r *Runner) RunOnce(ctx context.Context) {
	if r == nil || r.store == nil {
		return
	}
	orders, err := r.store.ListExpiredPendingPaymentOrders(ctx, r.now().UTC(), r.batch)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("payment-expiry: list timed-out orders failed: %v", err)
		}
		return
	}
	for _, order := range orders {
		if ctx.Err() != nil {
			return
		}
		r.sweep(ctx, order)
	}
}

func (r *Runner) sweep(ctx context.Context, order contracts.PaymentOrder) {
	queryable := strings.TrimSpace(order.ProviderOrderID) != "" &&
		(order.ProviderKey == contracts.PaymentProviderStripe || order.ProviderKey == contracts.PaymentProviderEasyPay)
	if queryable {
		provider, err := r.store.GetPaymentProvider(ctx, order.ProviderInstanceID)
		if err != nil {
			log.Printf("payment-expiry: order %s provider lookup failed, retrying next cycle: %v", order.ID, err)
			return
		}
		secret, err := r.secrets.Resolve(ctx, sweeperSecretRef(provider))
		if err != nil {
			log.Printf("payment-expiry: order %s provider credential unavailable, retrying next cycle", order.ID)
			return
		}
		query := payment.CheckoutQueryRequest{
			ProviderOrderID: order.ProviderOrderID, SecretKey: secret.Value,
			APIBaseURL: strings.TrimSpace(provider.Config["apiBase"]),
			MerchantID: strings.TrimSpace(provider.Config["pid"]),
		}
		var result payment.CheckoutQueryResult
		if order.ProviderKey == contracts.PaymentProviderStripe {
			result, err = r.stripe.QueryCheckout(ctx, query)
		} else {
			result, err = r.easypay.QueryCheckout(ctx, query)
		}
		if err != nil {
			log.Printf("payment-expiry: order %s upstream query failed, retrying next cycle: %v", order.ID, err)
			return
		}
		if result.Paid {
			r.recover(ctx, order, provider, result)
			return
		}
		// The customer could still complete an open Stripe session after local
		// expiry, so the provider-side session must be closed before the local
		// row. EasyPay has no session to close and this is a no-op there.
		if order.ProviderKey == contracts.PaymentProviderStripe {
			if err := r.stripe.ExpireCheckout(ctx, payment.CheckoutExpiryRequest{
				ProviderOrderID: order.ProviderOrderID, SecretKey: secret.Value, APIBaseURL: query.APIBaseURL,
			}); err != nil {
				log.Printf("payment-expiry: order %s upstream expiry failed, retrying next cycle: %v", order.ID, err)
				return
			}
		}
	}
	if _, err := r.store.ExpirePaymentOrder(ctx, order.ID, contracts.OperationAudit{
		ActorType: "system", ActorID: "payment-expiry", Action: "payment.order.expire",
		RiskLevel: contracts.RiskLevelL1, Result: "accepted",
	}); err != nil {
		log.Printf("payment-expiry: order %s local expiry skipped: %v", order.ID, err)
	}
}

// sweeperSecretRef mirrors the checkout-side credential choice per provider
// family: Stripe authenticates with the API secret, EasyPay with the merchant
// key.
func sweeperSecretRef(provider contracts.PaymentProvider) string {
	if provider.ProviderKey == contracts.PaymentProviderEasyPay {
		return provider.SecretRefs["pkey"]
	}
	return provider.SecretRefs["secretKey"]
}

// recover settles a paid-but-unnotified order through the same exactly-once
// confirmation path the webhook uses. Event ID and body hash are deterministic
// so repeated sweeps of the same order replay instead of double-crediting.
func (r *Runner) recover(ctx context.Context, order contracts.PaymentOrder, provider contracts.PaymentProvider, result payment.CheckoutQueryResult) {
	evidence := strings.Join([]string{
		"recover", order.OutTradeNo, order.ProviderOrderID, result.PaymentTradeNo,
		strconv.FormatInt(result.PaidAmountMicros, 10), result.Currency,
	}, ":")
	bodyHash := sha256.Sum256([]byte(evidence))
	_, _, credited, err := r.store.ConfirmRechargePayment(ctx, contracts.PaymentNotification{
		ProviderInstanceID: provider.ID, ProviderKey: provider.ProviderKey,
		EventID: "recover:" + order.ProviderOrderID, ProviderOrderID: order.ProviderOrderID,
		OutTradeNo: order.OutTradeNo, PaymentTradeNo: result.PaymentTradeNo,
		PaidAmountMicros: result.PaidAmountMicros, Currency: result.Currency, PaidAt: r.now().UTC(),
	}, hex.EncodeToString(bodyHash[:]))
	if err != nil {
		log.Printf("payment-expiry: order %s recovery failed, retrying next cycle: %v", order.ID, err)
		return
	}
	if credited {
		log.Printf("payment-expiry: order %s recovered a missed payment notification", order.ID)
		_, _ = r.store.AppendAudit(ctx, contracts.OperationAudit{
			ActorType: "system", ActorID: "payment-expiry", Action: "payment.order.recover",
			RiskLevel: contracts.RiskLevelL2, Result: "accepted",
			TargetType: "payment_order", TargetID: order.ID, UserID: order.UserID,
		})
	}
}
