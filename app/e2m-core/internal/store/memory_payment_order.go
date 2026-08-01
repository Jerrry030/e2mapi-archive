package store

import (
	"context"
	"sort"
	"strings"

	"e2m.local/contracts"
)

const (
	defaultPaymentOrderPageSize = 20
	maxPaymentOrderPageSize     = 100
)

func normalizePaymentOrderPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultPaymentOrderPageSize
	}
	if pageSize > maxPaymentOrderPageSize {
		pageSize = maxPaymentOrderPageSize
	}
	return page, pageSize
}

func (s *MemoryStore) CreatePaymentOrder(ctx context.Context, input contracts.PaymentOrder) (contracts.PaymentOrder, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PaymentOrder{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	userExists := false
	for _, user := range s.users {
		if user.ID == input.UserID {
			userExists = true
			break
		}
	}
	if !userExists {
		return contracts.PaymentOrder{}, ErrInvalid
	}
	order, err := normalizePaymentOrderForCreate(input)
	if err != nil {
		return contracts.PaymentOrder{}, err
	}
	if order.ID == "" {
		order.ID = s.nextID("payord")
	}
	for _, existing := range s.paymentOrders {
		if existing.ID == order.ID || order.OutTradeNo != "" && existing.OutTradeNo == order.OutTradeNo {
			return contracts.PaymentOrder{}, ErrDuplicate
		}
	}
	now := s.now()
	if order.CreatedAt.IsZero() {
		order.CreatedAt = now
	}
	if order.UpdatedAt.IsZero() {
		order.UpdatedAt = order.CreatedAt
	}

	s.paymentOrders = append(s.paymentOrders, order)
	return copyPaymentOrder(order), nil
}

func (s *MemoryStore) GetPaymentOrder(ctx context.Context, id string) (contracts.PaymentOrder, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PaymentOrder{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, order := range s.paymentOrders {
		if order.ID == id {
			return copyPaymentOrder(order), nil
		}
	}
	return contracts.PaymentOrder{}, ErrNotFound
}

func (s *MemoryStore) BindPaymentOrderCheckout(ctx context.Context, id, providerOrderID string) (contracts.PaymentOrder, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PaymentOrder{}, err
	}
	providerOrderID = strings.TrimSpace(providerOrderID)
	if providerOrderID == "" || len(providerOrderID) > 128 {
		return contracts.PaymentOrder{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, order := range s.paymentOrders {
		if order.ProviderOrderID == providerOrderID && order.ID != id {
			return contracts.PaymentOrder{}, ErrDuplicate
		}
	}
	for index := range s.paymentOrders {
		order := &s.paymentOrders[index]
		if order.ID != id {
			continue
		}
		if order.Status != contracts.PaymentOrderPending || order.ProviderOrderID != "" && order.ProviderOrderID != providerOrderID {
			return contracts.PaymentOrder{}, ErrConflict
		}
		order.ProviderOrderID = providerOrderID
		order.UpdatedAt = s.now()
		return copyPaymentOrder(*order), nil
	}
	return contracts.PaymentOrder{}, ErrNotFound
}

func (s *MemoryStore) GetPaymentOrderByOutTradeNo(ctx context.Context, outTradeNo string) (contracts.PaymentOrder, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PaymentOrder{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, order := range s.paymentOrders {
		if order.OutTradeNo == strings.TrimSpace(outTradeNo) {
			return copyPaymentOrder(order), nil
		}
	}
	return contracts.PaymentOrder{}, ErrNotFound
}

func (s *MemoryStore) ConfirmRechargePayment(ctx context.Context, notification contracts.PaymentNotification, bodyHash string) (contracts.PaymentOrder, contracts.Wallet, bool, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PaymentOrder{}, contracts.Wallet{}, false, err
	}
	notification.ProviderInstanceID = strings.TrimSpace(notification.ProviderInstanceID)
	notification.EventID = strings.TrimSpace(notification.EventID)
	notification.OutTradeNo = strings.TrimSpace(notification.OutTradeNo)
	notification.PaymentTradeNo = strings.TrimSpace(notification.PaymentTradeNo)
	notification.ProviderOrderID = strings.TrimSpace(notification.ProviderOrderID)
	notification.Currency = strings.ToUpper(strings.TrimSpace(notification.Currency))
	bodyHash = strings.TrimSpace(bodyHash)
	if notification.ProviderInstanceID == "" || notification.ProviderKey == "" || notification.EventID == "" ||
		notification.OutTradeNo == "" || notification.PaymentTradeNo == "" || notification.ProviderOrderID == "" || notification.PaidAmountMicros <= 0 ||
		!validCurrency(notification.Currency) || notification.PaidAt.IsZero() || bodyHash == "" {
		return contracts.PaymentOrder{}, contracts.Wallet{}, false, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	eventKey := notification.ProviderInstanceID + ":" + notification.EventID
	if event, exists := s.paymentCallbackEvents[eventKey]; exists {
		if event.BodyHash != bodyHash {
			return contracts.PaymentOrder{}, contracts.Wallet{}, false, ErrConflict
		}
		for _, order := range s.paymentOrders {
			if order.ID == event.OrderID {
				return copyPaymentOrder(order), s.wallets[walletMapKey(order.UserID, order.Currency)], false, nil
			}
		}
		return contracts.PaymentOrder{}, contracts.Wallet{}, false, ErrConflict
	}
	providerOK := false
	for _, provider := range s.paymentProviders {
		if provider.ID == notification.ProviderInstanceID && provider.ProviderKey == notification.ProviderKey {
			providerOK = true
			break
		}
	}
	if !providerOK {
		return contracts.PaymentOrder{}, contracts.Wallet{}, false, ErrInvalid
	}
	for index := range s.paymentOrders {
		order := &s.paymentOrders[index]
		if order.OutTradeNo != notification.OutTradeNo {
			continue
		}
		expectedMicros, err := paymentDecimalToMicros(order.PayAmount)
		if err != nil || expectedMicros != notification.PaidAmountMicros || order.Currency != notification.Currency ||
			order.ProviderInstanceID != notification.ProviderInstanceID || order.ProviderKey != notification.ProviderKey ||
			order.OrderType != contracts.PaymentOrderBalance || order.ProviderOrderID != "" && order.ProviderOrderID != notification.ProviderOrderID {
			return contracts.PaymentOrder{}, contracts.Wallet{}, false, ErrInvalid
		}
		if order.Status == contracts.PaymentOrderCompleted {
			now := s.now()
			s.paymentCallbackEvents[eventKey] = contracts.PaymentCallbackEvent{ID: s.nextID("payevt"), ProviderInstanceID: notification.ProviderInstanceID, ProviderKey: notification.ProviderKey, EventID: notification.EventID, OrderID: order.ID, BodyHash: bodyHash, Accepted: true, CreatedAt: now}
			return copyPaymentOrder(*order), s.wallets[walletMapKey(order.UserID, order.Currency)], false, nil
		}
		if order.Status != contracts.PaymentOrderPending {
			return contracts.PaymentOrder{}, contracts.Wallet{}, false, ErrConflict
		}
		now := s.now()
		paidAt := notification.PaidAt.UTC()
		order.Status = contracts.PaymentOrderCompleted
		order.PaymentTradeNo = notification.PaymentTradeNo
		order.ProviderOrderID = notification.ProviderOrderID
		order.PaidAt, order.CompletedAt = &paidAt, &now
		order.UpdatedAt = now
		walletKey := walletMapKey(order.UserID, order.Currency)
		wallet := s.wallets[walletKey]
		wallet.UserID, wallet.Currency = order.UserID, order.Currency
		wallet.AvailableMicros += notification.PaidAmountMicros
		wallet.Version++
		wallet.UpdatedAt = now
		s.wallets[walletKey] = wallet
		s.appendWalletJournalLocked(order.UserID, contracts.WalletJournalRecharge, order.Currency, notification.PaidAmountMicros, "payment:"+order.ID, "payment_order", order.ID, contracts.WalletAccountPlatformCash, contracts.WalletAccountUserAvailable, now)
		s.paymentCallbackEvents[eventKey] = contracts.PaymentCallbackEvent{ID: s.nextID("payevt"), ProviderInstanceID: notification.ProviderInstanceID, ProviderKey: notification.ProviderKey, EventID: notification.EventID, OrderID: order.ID, BodyHash: bodyHash, Accepted: true, CreatedAt: now}
		return copyPaymentOrder(*order), wallet, true, nil
	}
	return contracts.PaymentOrder{}, contracts.Wallet{}, false, ErrNotFound
}

func (s *MemoryStore) RecordRejectedPaymentCallback(ctx context.Context, event contracts.PaymentCallbackEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	event.ProviderInstanceID = strings.TrimSpace(event.ProviderInstanceID)
	event.EventID = strings.TrimSpace(event.EventID)
	event.BodyHash = strings.TrimSpace(event.BodyHash)
	event.ErrorCode = strings.TrimSpace(event.ErrorCode)
	if event.ProviderInstanceID == "" || event.ProviderKey == "" || event.EventID == "" || event.BodyHash == "" || event.Accepted || event.ErrorCode == "" {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	providerOK := false
	for _, provider := range s.paymentProviders {
		if provider.ID == event.ProviderInstanceID && provider.ProviderKey == event.ProviderKey {
			providerOK = true
			break
		}
	}
	if !providerOK {
		return ErrInvalid
	}
	eventKey := event.ProviderInstanceID + ":" + event.EventID
	if existing, ok := s.paymentCallbackEvents[eventKey]; ok {
		if existing.BodyHash != event.BodyHash {
			return ErrConflict
		}
		return nil
	}
	if event.ID == "" {
		event.ID = s.nextID("payevt")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = s.now()
	}
	s.paymentCallbackEvents[eventKey] = event
	return nil
}

func (s *MemoryStore) ListPaymentOrders(ctx context.Context, filter contracts.PaymentOrderFilter) (contracts.PaymentOrderPage, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PaymentOrderPage{}, err
	}
	page, pageSize := normalizePaymentOrderPage(filter.Page, filter.PageSize)
	keyword := strings.ToLower(strings.TrimSpace(filter.Keyword))
	s.mu.RLock()
	matches := make([]contracts.PaymentOrder, 0, len(s.paymentOrders))
	for _, order := range s.paymentOrders {
		if filter.Status != "" && order.Status != filter.Status ||
			filter.PaymentType != "" && order.PaymentType != filter.PaymentType ||
			filter.OrderType != "" && order.OrderType != filter.OrderType ||
			filter.UserID != 0 && order.UserID != filter.UserID ||
			filter.ProviderInstanceID != "" && order.ProviderInstanceID != filter.ProviderInstanceID ||
			filter.StartCreatedAt != nil && order.CreatedAt.Before(*filter.StartCreatedAt) ||
			filter.EndCreatedAt != nil && !order.CreatedAt.Before(*filter.EndCreatedAt) {
			continue
		}
		if keyword != "" {
			haystack := strings.ToLower(strings.Join([]string{order.ID, order.OutTradeNo, order.PaymentTradeNo, order.UserEmail, order.UserName}, "\x00"))
			if !strings.Contains(haystack, keyword) {
				continue
			}
		}
		matches = append(matches, copyPaymentOrder(order))
	}
	s.mu.RUnlock()
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].CreatedAt.Equal(matches[j].CreatedAt) {
			return matches[i].ID > matches[j].ID
		}
		return matches[i].CreatedAt.After(matches[j].CreatedAt)
	})
	total := int64(len(matches))
	start := (page - 1) * pageSize
	if start >= len(matches) {
		matches = []contracts.PaymentOrder{}
	} else {
		end := start + pageSize
		if end > len(matches) {
			end = len(matches)
		}
		matches = matches[start:end]
	}
	return contracts.PaymentOrderPage{Items: matches, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *MemoryStore) ListAuditsByTarget(ctx context.Context, targetType, targetID string) ([]contracts.OperationAudit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []contracts.OperationAudit{}
	for _, audit := range s.audits {
		if audit.TargetType == targetType && audit.TargetID == targetID {
			out = append(out, audit)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *MemoryStore) CancelPendingPaymentOrder(ctx context.Context, id string, audit contracts.OperationAudit) (contracts.PaymentOrder, error) {
	if err := ctx.Err(); err != nil {
		return contracts.PaymentOrder{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.paymentOrders {
		order := &s.paymentOrders[i]
		if order.ID != id {
			continue
		}
		if order.Status != contracts.PaymentOrderPending || strings.TrimSpace(order.PaymentTradeNo) != "" {
			return contracts.PaymentOrder{}, ErrConflict
		}
		now := s.now()
		order.Status = contracts.PaymentOrderCancelled
		order.UpdatedAt = now
		if !audit.EventLevel.Valid() {
			audit.EventLevel = contracts.DefaultEventLevel(audit.RiskLevel, audit.Result)
		}
		if audit.ID == "" {
			audit.ID = s.nextID("audit")
		}
		if audit.CreatedAt.IsZero() {
			audit.CreatedAt = now
		}
		audit.UserID = order.UserID
		audit.TargetType = "payment_order"
		audit.TargetID = order.ID
		s.audits = append(s.audits, audit)
		return copyPaymentOrder(*order), nil
	}
	return contracts.PaymentOrder{}, ErrNotFound
}

func copyPaymentOrder(input contracts.PaymentOrder) contracts.PaymentOrder {
	out := input
	out.RefundRequestedAt = copyTimePointer(input.RefundRequestedAt)
	out.PaidAt = copyTimePointer(input.PaidAt)
	out.CompletedAt = copyTimePointer(input.CompletedAt)
	out.FailedAt = copyTimePointer(input.FailedAt)
	return out
}
