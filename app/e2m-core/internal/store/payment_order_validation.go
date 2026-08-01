package store

import (
	"fmt"
	"strings"
	"time"

	"e2m.local/contracts"
)

func normalizePaymentOrderForCreate(input contracts.PaymentOrder) (contracts.PaymentOrder, error) {
	order := copyPaymentOrder(input)
	if order.UserID <= 0 {
		return contracts.PaymentOrder{}, fmt.Errorf("%w: user_id must be positive", ErrInvalid)
	}
	var err error
	if order.Amount, err = normalizePaymentDecimal(order.Amount, 18, 2); err != nil {
		return contracts.PaymentOrder{}, fmt.Errorf("%w: amount %v", ErrInvalid, err)
	}
	if order.PayAmount, err = normalizePaymentDecimal(order.PayAmount, 18, 2); err != nil {
		return contracts.PaymentOrder{}, fmt.Errorf("%w: pay_amount %v", ErrInvalid, err)
	}
	if order.FeeRate, err = normalizePaymentDecimal(order.FeeRate, 6, 4); err != nil {
		return contracts.PaymentOrder{}, fmt.Errorf("%w: fee_rate %v", ErrInvalid, err)
	}
	if order.RefundAmount == "" {
		order.RefundAmount = "0"
	}
	if order.RefundAmount, err = normalizePaymentDecimal(order.RefundAmount, 18, 2); err != nil {
		return contracts.PaymentOrder{}, fmt.Errorf("%w: refund_amount %v", ErrInvalid, err)
	}
	order.Currency = strings.ToUpper(strings.TrimSpace(order.Currency))
	if len(order.Currency) != 3 || !asciiUpper(order.Currency) {
		return contracts.PaymentOrder{}, fmt.Errorf("%w: currency must be a three-letter uppercase code", ErrInvalid)
	}
	order.PaymentType = strings.ToLower(strings.TrimSpace(order.PaymentType))
	if !validStoredPaymentType(order.PaymentType) {
		return contracts.PaymentOrder{}, fmt.Errorf("%w: unsupported payment_type", ErrInvalid)
	}
	order.OutTradeNo = strings.TrimSpace(order.OutTradeNo)
	if order.OutTradeNo == "" || len(order.OutTradeNo) > 64 || !safePaymentOrderNumber(order.OutTradeNo) {
		return contracts.PaymentOrder{}, fmt.Errorf("%w: out_trade_no must contain 1-64 letters, digits, underscores, or hyphens", ErrInvalid)
	}
	order.PaymentTradeNo = strings.TrimSpace(order.PaymentTradeNo)
	if len(order.PaymentTradeNo) > 128 {
		return contracts.PaymentOrder{}, fmt.Errorf("%w: payment_trade_no is too long", ErrInvalid)
	}
	if order.OrderType == "" {
		order.OrderType = contracts.PaymentOrderBalance
	}
	if order.OrderType != contracts.PaymentOrderBalance && order.OrderType != contracts.PaymentOrderSubscription {
		return contracts.PaymentOrder{}, fmt.Errorf("%w: unsupported order_type", ErrInvalid)
	}
	if order.Status == "" {
		order.Status = contracts.PaymentOrderPending
	}
	if !validStoredPaymentOrderStatus(order.Status) {
		return contracts.PaymentOrder{}, fmt.Errorf("%w: unsupported status", ErrInvalid)
	}
	if order.ExpiresAt.IsZero() {
		return contracts.PaymentOrder{}, fmt.Errorf("%w: expires_at is required", ErrInvalid)
	}
	order.ExpiresAt = order.ExpiresAt.UTC()
	order.PaidAt = utcTimePointer(order.PaidAt)
	order.CompletedAt = utcTimePointer(order.CompletedAt)
	order.FailedAt = utcTimePointer(order.FailedAt)
	order.RefundRequestedAt = utcTimePointer(order.RefundRequestedAt)
	order.UserEmail = strings.TrimSpace(order.UserEmail)
	order.UserName = strings.TrimSpace(order.UserName)
	order.ProviderInstanceID = strings.TrimSpace(order.ProviderInstanceID)
	order.ProviderOrderID = strings.TrimSpace(order.ProviderOrderID)
	if len(order.ProviderOrderID) > 128 {
		return contracts.PaymentOrder{}, fmt.Errorf("%w: provider_order_id is too long", ErrInvalid)
	}
	order.ProviderName = strings.TrimSpace(order.ProviderName)
	return order, nil
}

func paymentDecimalToMicros(raw string) (int64, error) {
	normalized, err := normalizePaymentDecimal(raw, 12, 2)
	if err != nil {
		return 0, err
	}
	parts := strings.Split(normalized, ".")
	var whole, fraction int64
	for _, character := range parts[0] {
		whole = whole*10 + int64(character-'0')
	}
	for _, character := range parts[1] {
		fraction = fraction*10 + int64(character-'0')
	}
	return whole*1_000_000 + fraction*10_000, nil
}

func normalizePaymentDecimal(raw string, maxIntegerDigits, scale int) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "-") || strings.HasPrefix(raw, "+") {
		return "", fmt.Errorf("must be a non-negative decimal")
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || parts[0] == "" || len(parts[0]) > maxIntegerDigits {
		return "", fmt.Errorf("is outside the supported precision")
	}
	for _, char := range parts[0] {
		if char < '0' || char > '9' {
			return "", fmt.Errorf("must be a non-negative decimal")
		}
	}
	integer := strings.TrimLeft(parts[0], "0")
	if integer == "" {
		integer = "0"
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" || len(fraction) > scale {
			return "", fmt.Errorf("supports at most %d decimal places", scale)
		}
		for _, char := range fraction {
			if char < '0' || char > '9' {
				return "", fmt.Errorf("must be a non-negative decimal")
			}
		}
	}
	fraction += strings.Repeat("0", scale-len(fraction))
	if scale == 0 {
		return integer, nil
	}
	return integer + "." + fraction, nil
}

func validStoredPaymentOrderStatus(status contracts.PaymentOrderStatus) bool {
	switch status {
	case contracts.PaymentOrderPending, contracts.PaymentOrderPaid, contracts.PaymentOrderRecharging,
		contracts.PaymentOrderCompleted, contracts.PaymentOrderExpired, contracts.PaymentOrderCancelled,
		contracts.PaymentOrderFailed, contracts.PaymentOrderRefundRequested, contracts.PaymentOrderRefunding,
		contracts.PaymentOrderRefundPending, contracts.PaymentOrderPartiallyRefunded,
		contracts.PaymentOrderRefunded, contracts.PaymentOrderRefundFailed:
		return true
	default:
		return false
	}
}

func validStoredPaymentType(value string) bool {
	switch value {
	case "alipay", "wxpay", "alipay_direct", "wxpay_direct", "card", "link", "stripe", "airwallex", "easypay":
		return true
	default:
		return false
	}
}

func safePaymentOrderNumber(value string) bool {
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func asciiUpper(value string) bool {
	for _, char := range value {
		if char < 'A' || char > 'Z' {
			return false
		}
	}
	return true
}

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}
