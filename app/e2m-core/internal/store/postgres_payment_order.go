package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"e2m.local/contracts"
	"github.com/jackc/pgx/v5"
)

const paymentOrderColumns = `id, user_id, user_email, user_name,
	amount::text, pay_amount::text, fee_rate::text, currency, payment_type,
	out_trade_no, payment_trade_no, provider_order_id, order_type, COALESCE(provider_instance_id, ''),
	COALESCE(provider_key, ''), provider_name, status, refund_amount::text,
	refund_reason, refund_requested_at, refund_requested_by, refund_request_reason,
	expires_at, paid_at, completed_at, failed_at, failed_reason, created_at, updated_at`

func scanPaymentOrder(row rowScanner) (contracts.PaymentOrder, error) {
	var order contracts.PaymentOrder
	var orderType, providerKey, status string
	err := row.Scan(
		&order.ID, &order.UserID, &order.UserEmail, &order.UserName,
		&order.Amount, &order.PayAmount, &order.FeeRate, &order.Currency, &order.PaymentType,
		&order.OutTradeNo, &order.PaymentTradeNo, &order.ProviderOrderID, &orderType, &order.ProviderInstanceID,
		&providerKey, &order.ProviderName, &status, &order.RefundAmount,
		&order.RefundReason, &order.RefundRequestedAt, &order.RefundRequestedBy, &order.RefundRequestReason,
		&order.ExpiresAt, &order.PaidAt, &order.CompletedAt, &order.FailedAt,
		&order.FailedReason, &order.CreatedAt, &order.UpdatedAt,
	)
	if err != nil {
		return contracts.PaymentOrder{}, err
	}
	order.OrderType = contracts.PaymentOrderType(orderType)
	order.ProviderKey = contracts.PaymentProviderKey(providerKey)
	order.Status = contracts.PaymentOrderStatus(status)
	return order, nil
}

func (s *PostgresStore) CreatePaymentOrder(ctx context.Context, input contracts.PaymentOrder) (contracts.PaymentOrder, error) {
	order, err := normalizePaymentOrderForCreate(input)
	if err != nil {
		return contracts.PaymentOrder{}, err
	}
	if order.ID == "" {
		order.ID = newID("payord")
	}
	created, err := scanPaymentOrder(s.pool.QueryRow(ctx, `INSERT INTO payment_orders
		(id,user_id,user_email,user_name,amount,pay_amount,fee_rate,currency,payment_type,
		 out_trade_no,payment_trade_no,provider_order_id,order_type,provider_instance_id,provider_key,provider_name,
		 status,refund_amount,refund_reason,refund_requested_at,refund_requested_by,
		 refund_request_reason,expires_at,paid_at,completed_at,failed_at,failed_reason,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5::numeric,$6::numeric,$7::numeric,$8,$9,$10,$11,$12,
		 $13,NULLIF($14,''),NULLIF($15,''),$16,$17,$18::numeric,$19,$20,$21,$22,$23,$24,$25,$26,$27,
		 COALESCE(NULLIF($28,'0001-01-01 00:00:00+00'::timestamptz),now()),
		 COALESCE(NULLIF($29,'0001-01-01 00:00:00+00'::timestamptz),now()))
		RETURNING `+paymentOrderColumns,
		order.ID, order.UserID, order.UserEmail, order.UserName, order.Amount, order.PayAmount,
		order.FeeRate, order.Currency, order.PaymentType, order.OutTradeNo, order.PaymentTradeNo, order.ProviderOrderID,
		string(order.OrderType), order.ProviderInstanceID, string(order.ProviderKey), order.ProviderName,
		string(order.Status), order.RefundAmount, order.RefundReason, order.RefundRequestedAt,
		order.RefundRequestedBy, order.RefundRequestReason, order.ExpiresAt, order.PaidAt,
		order.CompletedAt, order.FailedAt, order.FailedReason, order.CreatedAt, order.UpdatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.PaymentOrder{}, ErrDuplicate
		}
		return contracts.PaymentOrder{}, err
	}
	return created, nil
}

func (s *PostgresStore) GetPaymentOrder(ctx context.Context, id string) (contracts.PaymentOrder, error) {
	order, err := scanPaymentOrder(s.pool.QueryRow(ctx,
		`SELECT `+paymentOrderColumns+` FROM payment_orders WHERE id=$1`, id))
	if err != nil {
		return contracts.PaymentOrder{}, mapNotFound(err)
	}
	return order, nil
}

func (s *PostgresStore) BindPaymentOrderCheckout(ctx context.Context, id, providerOrderID string) (contracts.PaymentOrder, error) {
	providerOrderID = strings.TrimSpace(providerOrderID)
	if providerOrderID == "" || len(providerOrderID) > 128 {
		return contracts.PaymentOrder{}, ErrInvalid
	}
	order, err := scanPaymentOrder(s.pool.QueryRow(ctx, `UPDATE payment_orders SET provider_order_id=$2,updated_at=statement_timestamp()
		WHERE id=$1 AND status=$3 AND (provider_order_id='' OR provider_order_id=$2) RETURNING `+paymentOrderColumns,
		id, providerOrderID, string(contracts.PaymentOrderPending)))
	if isUniqueViolation(err) {
		return contracts.PaymentOrder{}, ErrDuplicate
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.PaymentOrder{}, ErrConflict
	}
	return order, err
}

func (s *PostgresStore) GetPaymentOrderByOutTradeNo(ctx context.Context, outTradeNo string) (contracts.PaymentOrder, error) {
	order, err := scanPaymentOrder(s.pool.QueryRow(ctx,
		`SELECT `+paymentOrderColumns+` FROM payment_orders WHERE out_trade_no=$1`, strings.TrimSpace(outTradeNo)))
	if err != nil {
		return contracts.PaymentOrder{}, mapNotFound(err)
	}
	return order, nil
}

func (s *PostgresStore) ConfirmRechargePayment(ctx context.Context, notification contracts.PaymentNotification, bodyHash string) (contracts.PaymentOrder, contracts.Wallet, bool, error) {
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.PaymentOrder{}, contracts.Wallet{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var existingOrderID, existingBodyHash string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(order_id,''),body_hash FROM payment_callback_events WHERE provider_instance_id=$1 AND event_id=$2`, notification.ProviderInstanceID, notification.EventID).Scan(&existingOrderID, &existingBodyHash); err == nil {
		if existingBodyHash != bodyHash {
			return contracts.PaymentOrder{}, contracts.Wallet{}, false, ErrConflict
		}
		if existingOrderID == "" {
			return contracts.PaymentOrder{}, contracts.Wallet{}, false, ErrConflict
		}
		order, err := scanPaymentOrder(tx.QueryRow(ctx, `SELECT `+paymentOrderColumns+` FROM payment_orders WHERE id=$1`, existingOrderID))
		if err != nil {
			return contracts.PaymentOrder{}, contracts.Wallet{}, false, err
		}
		wallet, err := scanHybridWallet(tx.QueryRow(ctx, `SELECT user_id,currency,available_micros,reserved_micros,version,updated_at FROM wallet_accounts WHERE user_id=$1 AND currency=$2`, order.UserID, order.Currency))
		if err != nil {
			return contracts.PaymentOrder{}, contracts.Wallet{}, false, err
		}
		return order, wallet, false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.PaymentOrder{}, contracts.Wallet{}, false, err
	}
	var providerKey string
	if err := tx.QueryRow(ctx, `SELECT provider_key FROM payment_provider_instances WHERE id=$1 FOR SHARE`, notification.ProviderInstanceID).Scan(&providerKey); err != nil {
		return contracts.PaymentOrder{}, contracts.Wallet{}, false, mapNotFound(err)
	}
	if contracts.PaymentProviderKey(providerKey) != notification.ProviderKey {
		return contracts.PaymentOrder{}, contracts.Wallet{}, false, ErrInvalid
	}
	order, err := scanPaymentOrder(tx.QueryRow(ctx, `SELECT `+paymentOrderColumns+` FROM payment_orders WHERE out_trade_no=$1 FOR UPDATE`, notification.OutTradeNo))
	if err != nil {
		return contracts.PaymentOrder{}, contracts.Wallet{}, false, mapNotFound(err)
	}
	expectedMicros, err := paymentDecimalToMicros(order.PayAmount)
	if err != nil || expectedMicros != notification.PaidAmountMicros || order.Currency != notification.Currency ||
		order.ProviderInstanceID != notification.ProviderInstanceID || order.ProviderKey != notification.ProviderKey ||
		order.OrderType != contracts.PaymentOrderBalance || order.ProviderOrderID != "" && order.ProviderOrderID != notification.ProviderOrderID {
		return contracts.PaymentOrder{}, contracts.Wallet{}, false, ErrInvalid
	}
	now := nowUTC()
	if _, err := tx.Exec(ctx, `INSERT INTO payment_callback_events
		(id,provider_instance_id,provider_key,event_id,order_id,body_hash,accepted,error_code,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,TRUE,'',$7)`, newID("payevt"), notification.ProviderInstanceID,
		string(notification.ProviderKey), notification.EventID, order.ID, bodyHash, now); err != nil {
		if isUniqueViolation(err) {
			return contracts.PaymentOrder{}, contracts.Wallet{}, false, ErrConflict
		}
		return contracts.PaymentOrder{}, contracts.Wallet{}, false, err
	}
	credited := false
	if order.Status == contracts.PaymentOrderPending {
		paidAt := notification.PaidAt.UTC()
		order, err = scanPaymentOrder(tx.QueryRow(ctx, `UPDATE payment_orders SET status=$2,payment_trade_no=$3,provider_order_id=$4,paid_at=$5,completed_at=$6,updated_at=$6 WHERE id=$1 AND status=$7 RETURNING `+paymentOrderColumns,
			order.ID, string(contracts.PaymentOrderCompleted), notification.PaymentTradeNo, notification.ProviderOrderID, paidAt, now, string(contracts.PaymentOrderPending)))
		if err != nil {
			return contracts.PaymentOrder{}, contracts.Wallet{}, false, err
		}
		credited = true
	} else if order.Status != contracts.PaymentOrderCompleted {
		return contracts.PaymentOrder{}, contracts.Wallet{}, false, ErrConflict
	}
	if credited {
		if _, err := tx.Exec(ctx, `INSERT INTO wallet_accounts(user_id,currency,available_micros,reserved_micros,version,updated_at)
			VALUES($1,$2,$3,0,1,$4) ON CONFLICT(user_id,currency) DO UPDATE SET
			available_micros=wallet_accounts.available_micros+EXCLUDED.available_micros,
			version=wallet_accounts.version+1,updated_at=EXCLUDED.updated_at`, order.UserID, order.Currency, notification.PaidAmountMicros, now); err != nil {
			return contracts.PaymentOrder{}, contracts.Wallet{}, false, err
		}
		journalID := newID("wjnl")
		if _, err := tx.Exec(ctx, `INSERT INTO wallet_journals(id,user_id,kind,currency,amount_micros,idempotency_key,reference_type,reference_id,created_at)
			VALUES($1,$2,'recharge',$3,$4,$5,'payment_order',$6,$7)`, journalID, order.UserID, order.Currency, notification.PaidAmountMicros, "payment:"+order.ID, order.ID, now); err != nil {
			return contracts.PaymentOrder{}, contracts.Wallet{}, false, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO wallet_entries(id,journal_id,account,direction,amount_micros,currency,created_at) VALUES
			($1,$3,'platform_cash','debit',$4,$5,$6),($2,$3,'user_available','credit',$4,$5,$6)`, newID("went"), newID("went"), journalID, notification.PaidAmountMicros, order.Currency, now); err != nil {
			return contracts.PaymentOrder{}, contracts.Wallet{}, false, err
		}
	}
	wallet, err := scanHybridWallet(tx.QueryRow(ctx, `SELECT user_id,currency,available_micros,reserved_micros,version,updated_at FROM wallet_accounts WHERE user_id=$1 AND currency=$2`, order.UserID, order.Currency))
	if err != nil {
		return contracts.PaymentOrder{}, contracts.Wallet{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.PaymentOrder{}, contracts.Wallet{}, false, err
	}
	return order, wallet, credited, nil
}

func (s *PostgresStore) RecordRejectedPaymentCallback(ctx context.Context, event contracts.PaymentCallbackEvent) error {
	event.ProviderInstanceID = strings.TrimSpace(event.ProviderInstanceID)
	event.EventID = strings.TrimSpace(event.EventID)
	event.BodyHash = strings.TrimSpace(event.BodyHash)
	event.ErrorCode = strings.TrimSpace(event.ErrorCode)
	if event.ProviderInstanceID == "" || event.ProviderKey == "" || event.EventID == "" || event.BodyHash == "" || event.Accepted || event.ErrorCode == "" {
		return ErrInvalid
	}
	if event.ID == "" {
		event.ID = newID("payevt")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = nowUTC()
	}
	command, err := s.pool.Exec(ctx, `INSERT INTO payment_callback_events
		(id,provider_instance_id,provider_key,event_id,order_id,body_hash,accepted,error_code,created_at)
		VALUES($1,$2,$3,$4,NULL,$5,FALSE,$6,$7)
		ON CONFLICT(provider_instance_id,event_id) DO NOTHING`, event.ID, event.ProviderInstanceID,
		string(event.ProviderKey), event.EventID, event.BodyHash, event.ErrorCode, event.CreatedAt)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 1 {
		return nil
	}
	var bodyHash string
	if err := s.pool.QueryRow(ctx, `SELECT body_hash FROM payment_callback_events WHERE provider_instance_id=$1 AND event_id=$2`, event.ProviderInstanceID, event.EventID).Scan(&bodyHash); err != nil {
		return err
	}
	if bodyHash != event.BodyHash {
		return ErrConflict
	}
	return nil
}

func (s *PostgresStore) ListPaymentOrders(ctx context.Context, filter contracts.PaymentOrderFilter) (contracts.PaymentOrderPage, error) {
	page, pageSize := normalizePaymentOrderPage(filter.Page, filter.PageSize)
	keyword := strings.ToLower(strings.TrimSpace(filter.Keyword))
	const where = ` WHERE ($1='' OR status=$1)
		AND ($2='' OR payment_type=$2) AND ($3='' OR order_type=$3)
		AND ($4=0 OR user_id=$4) AND ($5='' OR provider_instance_id=$5)
		AND ($6='' OR strpos(lower(id), $6)>0 OR strpos(lower(out_trade_no), $6)>0 OR
			strpos(lower(payment_trade_no), $6)>0 OR strpos(lower(user_email), $6)>0 OR strpos(lower(user_name), $6)>0)
		AND ($7::timestamptz IS NULL OR created_at >= $7)
		AND ($8::timestamptz IS NULL OR created_at < $8)`
	args := []any{string(filter.Status), filter.PaymentType, string(filter.OrderType), filter.UserID,
		filter.ProviderInstanceID, keyword, filter.StartCreatedAt, filter.EndCreatedAt}
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM payment_orders`+where, args...).Scan(&total); err != nil {
		return contracts.PaymentOrderPage{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+paymentOrderColumns+` FROM payment_orders`+where+
		` ORDER BY created_at DESC, id DESC LIMIT $9 OFFSET $10`, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return contracts.PaymentOrderPage{}, err
	}
	defer rows.Close()
	items := []contracts.PaymentOrder{}
	for rows.Next() {
		order, err := scanPaymentOrder(rows)
		if err != nil {
			return contracts.PaymentOrderPage{}, err
		}
		items = append(items, order)
	}
	if err := rows.Err(); err != nil {
		return contracts.PaymentOrderPage{}, err
	}
	return contracts.PaymentOrderPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *PostgresStore) ListAuditsByTarget(ctx context.Context, targetType, targetID string) ([]contracts.OperationAudit, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,user_id,instance_id,actor_type,actor_id,action,
		risk_level,event_level,target_type,target_id,request_payload_hash,result,error_message,approval_id,
		workflow_run_id,details,created_at FROM operation_audits
		WHERE target_type=$1 AND target_id=$2 ORDER BY created_at`, targetType, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contracts.OperationAudit{}
	for rows.Next() {
		var audit contracts.OperationAudit
		var risk, eventLevel string
		var details []byte
		if err := rows.Scan(&audit.ID, &audit.UserID, &audit.InstanceID, &audit.ActorType,
			&audit.ActorID, &audit.Action, &risk, &eventLevel, &audit.TargetType, &audit.TargetID,
			&audit.RequestHash, &audit.Result, &audit.ErrorMessage, &audit.ApprovalID,
			&audit.WorkflowRunID, &details, &audit.CreatedAt); err != nil {
			return nil, err
		}
		audit.RiskLevel = contracts.RiskLevel(risk)
		audit.EventLevel = contracts.EventLevel(eventLevel)
		if len(details) > 0 {
			_ = json.Unmarshal(details, &audit.Details)
		}
		out = append(out, audit)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CancelPendingPaymentOrder(ctx context.Context, id string, audit contracts.OperationAudit) (contracts.PaymentOrder, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.PaymentOrder{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	order, err := scanPaymentOrder(tx.QueryRow(ctx, `UPDATE payment_orders
		SET status=$2, updated_at=statement_timestamp()
		WHERE id=$1 AND status=$3 AND payment_trade_no=''
		RETURNING `+paymentOrderColumns, id, string(contracts.PaymentOrderCancelled), string(contracts.PaymentOrderPending)))
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return contracts.PaymentOrder{}, err
		}
		var exists bool
		if lookupErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_orders WHERE id=$1)`, id).Scan(&exists); lookupErr != nil {
			return contracts.PaymentOrder{}, lookupErr
		}
		if !exists {
			return contracts.PaymentOrder{}, ErrNotFound
		}
		return contracts.PaymentOrder{}, ErrConflict
	}
	if audit.ID == "" {
		audit.ID = newID("audit")
	}
	audit.UserID = order.UserID
	audit.TargetType = "payment_order"
	audit.TargetID = order.ID
	if audit.CreatedAt.IsZero() {
		audit.CreatedAt = nowUTC()
	}
	if !audit.EventLevel.Valid() {
		audit.EventLevel = contracts.DefaultEventLevel(audit.RiskLevel, audit.Result)
	}
	details, err := json.Marshal(audit.Details)
	if err != nil {
		return contracts.PaymentOrder{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operation_audits
		(id,user_id,instance_id,actor_type,actor_id,action,risk_level,event_level,target_type,target_id,
		 request_payload_hash,result,error_message,approval_id,workflow_run_id,details,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::jsonb,$17)`,
		audit.ID, audit.UserID, audit.InstanceID, audit.ActorType, audit.ActorID, audit.Action,
		string(audit.RiskLevel), string(audit.EventLevel), audit.TargetType, audit.TargetID, audit.RequestHash, audit.Result,
		audit.ErrorMessage, audit.ApprovalID, audit.WorkflowRunID, string(details), audit.CreatedAt); err != nil {
		return contracts.PaymentOrder{}, fmt.Errorf("append payment order audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.PaymentOrder{}, err
	}
	return order, nil
}
