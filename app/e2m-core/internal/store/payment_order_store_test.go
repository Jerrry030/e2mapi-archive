package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryPaymentOrdersFilterPaginateAndCancelAtomically(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }
	owner, err := st.CreateUser(ctx, contracts.User{Email: "orders-owner@example.com", PasswordHash: "test", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	create := func(id, tradeNo, paymentTradeNo, paymentType string, created time.Time) contracts.PaymentOrder {
		t.Helper()
		order, err := st.CreatePaymentOrder(ctx, contracts.PaymentOrder{
			ID: id, UserID: owner.ID, UserEmail: owner.Email, UserName: "Owner",
			Amount: "10", PayAmount: "10.50", FeeRate: "0.05", Currency: "cny",
			PaymentType: paymentType, OutTradeNo: tradeNo, PaymentTradeNo: paymentTradeNo,
			OrderType: contracts.PaymentOrderBalance, ProviderInstanceID: "payprov-main",
			ProviderKey: contracts.PaymentProviderEasyPay, ProviderName: "Main provider",
			Status: contracts.PaymentOrderPending, ExpiresAt: created.Add(time.Hour), CreatedAt: created,
		})
		if err != nil {
			t.Fatalf("create order: %v", err)
		}
		return order
	}
	older := create("payord-old", "local_old", "", "alipay", now.Add(-2*time.Hour))
	local := create("payord-local", "local_cancel", "", "wxpay", now.Add(-time.Hour))
	upstream := create("payord-upstream", "submitted", "gateway-123", "wxpay", now)

	start := now.Add(-90 * time.Minute)
	page, err := st.ListPaymentOrders(ctx, contracts.PaymentOrderFilter{
		Page: 1, PageSize: 1, UserID: owner.ID, PaymentType: "wxpay",
		ProviderInstanceID: "payprov-main", StartCreatedAt: &start, Keyword: "local_cancel",
	})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != local.ID {
		t.Fatalf("filtered page=%+v err=%v", page, err)
	}
	empty, err := st.ListPaymentOrders(ctx, contracts.PaymentOrderFilter{Page: 2, PageSize: 2})
	if err != nil || empty.Total != 3 || len(empty.Items) != 1 || empty.Items[0].ID != older.ID {
		t.Fatalf("second page=%+v err=%v", empty, err)
	}

	cancelled, err := st.CancelPendingPaymentOrder(ctx, local.ID, contracts.OperationAudit{
		ActorType: "user", ActorID: "admin@example.com", Action: "payment.order.cancel",
		RiskLevel: contracts.RiskLevelL2, Result: "accepted",
	})
	if err != nil || cancelled.Status != contracts.PaymentOrderCancelled {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
	audits, err := st.ListAuditsByTarget(ctx, "payment_order", local.ID)
	if err != nil || len(audits) != 1 || audits[0].UserID != owner.ID || audits[0].ActorID != "admin@example.com" {
		t.Fatalf("audits=%+v err=%v", audits, err)
	}
	if _, err := st.CancelPendingPaymentOrder(ctx, local.ID, contracts.OperationAudit{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second cancel error=%v, want ErrConflict", err)
	}
	if _, err := st.CancelPendingPaymentOrder(ctx, upstream.ID, contracts.OperationAudit{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("upstream cancel error=%v, want ErrConflict", err)
	}
	if got, _ := st.ListAuditsByTarget(ctx, "payment_order", upstream.ID); len(got) != 0 {
		t.Fatalf("conflicting cancellation wrote audit: %+v", got)
	}
}

func TestCreatePaymentOrderValidatesMoneyCurrencyAndIdentity(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	owner, err := st.CreateUser(ctx, contracts.User{Email: "validation@example.com", PasswordHash: "test", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	valid := contracts.PaymentOrder{
		UserID: owner.ID, Amount: "001.2", PayAmount: "1.20", FeeRate: "0.1",
		Currency: "cny", PaymentType: "alipay", OutTradeNo: "order_123",
		OrderType: contracts.PaymentOrderBalance, ExpiresAt: time.Now().Add(time.Hour),
	}
	created, err := st.CreatePaymentOrder(ctx, valid)
	if err != nil || created.Amount != "1.20" || created.FeeRate != "0.1000" || created.Currency != "CNY" {
		t.Fatalf("normalized order=%+v err=%v", created, err)
	}
	for name, mutate := range map[string]func(*contracts.PaymentOrder){
		"negative amount": func(o *contracts.PaymentOrder) { o.Amount = "-1" },
		"fraction scale":  func(o *contracts.PaymentOrder) { o.PayAmount = "1.001" },
		"bad currency":    func(o *contracts.PaymentOrder) { o.Currency = "CN" },
		"bad trade no":    func(o *contracts.PaymentOrder) { o.OutTradeNo = "bad/trade" },
		"bad order type":  func(o *contracts.PaymentOrder) { o.OrderType = "invoice" },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			input.OutTradeNo += name
			mutate(&input)
			if _, err := st.CreatePaymentOrder(ctx, input); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error=%v, want ErrInvalid", err)
			}
		})
	}
}
func TestMemoryPaymentOrderConcurrentCancelHasOneWinner(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	owner, err := st.CreateUser(ctx, contracts.User{
		Email: "cancel-race@example.com", PasswordHash: "test",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := st.CreatePaymentOrder(ctx, contracts.PaymentOrder{
		UserID: owner.ID, Amount: "20", PayAmount: "20", FeeRate: "0",
		Currency: "CNY", PaymentType: "alipay", OutTradeNo: "cancel_race",
		OrderType: contracts.PaymentOrderBalance, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := st.CancelPendingPaymentOrder(ctx, order.ID, contracts.OperationAudit{
				ActorType: "user", ActorID: "admin@example.com", Action: "payment.order.cancel",
				RiskLevel: contracts.RiskLevelL2, Result: "accepted",
			})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var succeeded, conflicted int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrConflict):
			conflicted++
		default:
			t.Fatalf("unexpected cancel error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("cancel race succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	audits, err := st.ListAuditsByTarget(ctx, "payment_order", order.ID)
	if err != nil || len(audits) != 1 || audits[0].Result != "accepted" {
		t.Fatalf("audits=%+v err=%v", audits, err)
	}
}
