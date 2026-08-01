package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

var paymentOrderStatuses = map[contracts.PaymentOrderStatus]bool{
	contracts.PaymentOrderPending: true, contracts.PaymentOrderPaid: true,
	contracts.PaymentOrderRecharging: true, contracts.PaymentOrderCompleted: true,
	contracts.PaymentOrderExpired: true, contracts.PaymentOrderCancelled: true,
	contracts.PaymentOrderFailed: true, contracts.PaymentOrderRefundRequested: true,
	contracts.PaymentOrderRefunding: true, contracts.PaymentOrderRefundPending: true,
	contracts.PaymentOrderPartiallyRefunded: true, contracts.PaymentOrderRefunded: true,
	contracts.PaymentOrderRefundFailed: true,
}

var paymentOrderTypes = map[contracts.PaymentOrderType]bool{
	contracts.PaymentOrderBalance: true, contracts.PaymentOrderSubscription: true,
}

func (s *Server) handleListPaymentOrders(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	filter, ok := parsePaymentOrderFilter(w, r)
	if !ok {
		return
	}
	page, err := s.store.ListPaymentOrders(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if page.Items == nil {
		page.Items = []contracts.PaymentOrder{}
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleGetPaymentOrder(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "order id is required")
		return
	}
	order, err := s.store.GetPaymentOrder(r.Context(), id)
	if err != nil {
		writePaymentOrderStoreError(w, err)
		return
	}
	audits, err := s.store.ListAuditsByTarget(r.Context(), "payment_order", order.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if audits == nil {
		audits = []contracts.OperationAudit{}
	}
	writeJSON(w, http.StatusOK, contracts.PaymentOrderDetail{Order: order, AuditLogs: audits})
}

func (s *Server) handleCancelPaymentOrder(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "order id is required")
		return
	}
	actor := currentUser(r)
	order, err := s.store.CancelPendingPaymentOrder(r.Context(), id, contracts.OperationAudit{
		ActorType: "user", ActorID: actor.Email, Action: "payment.order.cancel",
		RiskLevel: contracts.RiskLevelL2, Result: "accepted",
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "payment_order_conflict", "only a local PENDING order without an upstream trade number can be cancelled")
			return
		}
		writePaymentOrderStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func parsePaymentOrderFilter(w http.ResponseWriter, r *http.Request) (contracts.PaymentOrderFilter, bool) {
	q := r.URL.Query()
	filter := contracts.PaymentOrderFilter{Page: 1, PageSize: 20}
	var ok bool
	if filter.Page, ok = parsePositiveQueryInt(w, q.Get("page"), "page", 1, 1000000); !ok {
		return contracts.PaymentOrderFilter{}, false
	}
	if filter.PageSize, ok = parsePositiveQueryInt(w, q.Get("page_size"), "page_size", 20, 100); !ok {
		return contracts.PaymentOrderFilter{}, false
	}
	if raw := strings.TrimSpace(q.Get("user_id")); raw != "" {
		userID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || userID <= 0 {
			writeError(w, http.StatusBadRequest, "validation_failed", "user_id must be a positive integer")
			return contracts.PaymentOrderFilter{}, false
		}
		filter.UserID = userID
	}
	filter.Status = contracts.PaymentOrderStatus(strings.ToUpper(strings.TrimSpace(q.Get("status"))))
	if filter.Status != "" && !paymentOrderStatuses[filter.Status] {
		writeError(w, http.StatusBadRequest, "validation_failed", "unsupported payment order status")
		return contracts.PaymentOrderFilter{}, false
	}
	filter.OrderType = contracts.PaymentOrderType(strings.ToLower(strings.TrimSpace(q.Get("order_type"))))
	if filter.OrderType != "" && !paymentOrderTypes[filter.OrderType] {
		writeError(w, http.StatusBadRequest, "validation_failed", "unsupported order_type")
		return contracts.PaymentOrderFilter{}, false
	}
	filter.PaymentType = strings.ToLower(strings.TrimSpace(q.Get("payment_type")))
	if filter.PaymentType != "" && !validPaymentOrderPaymentType(filter.PaymentType) {
		writeError(w, http.StatusBadRequest, "validation_failed", "unsupported payment_type")
		return contracts.PaymentOrderFilter{}, false
	}
	filter.ProviderInstanceID = strings.TrimSpace(q.Get("provider_instance_id"))
	if len(filter.ProviderInstanceID) > 128 {
		writeError(w, http.StatusBadRequest, "validation_failed", "provider_instance_id is too long")
		return contracts.PaymentOrderFilter{}, false
	}
	filter.Keyword = strings.TrimSpace(q.Get("keyword"))
	if len(filter.Keyword) > 200 {
		writeError(w, http.StatusBadRequest, "validation_failed", "keyword is too long")
		return contracts.PaymentOrderFilter{}, false
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	filter.StartCreatedAt, ok = parsePaymentOrderDate(w, q.Get("start_date"), "start_date", location, false)
	if !ok {
		return contracts.PaymentOrderFilter{}, false
	}
	filter.EndCreatedAt, ok = parsePaymentOrderDate(w, q.Get("end_date"), "end_date", location, true)
	if !ok {
		return contracts.PaymentOrderFilter{}, false
	}
	if filter.StartCreatedAt != nil && filter.EndCreatedAt != nil && !filter.StartCreatedAt.Before(*filter.EndCreatedAt) {
		writeError(w, http.StatusBadRequest, "validation_failed", "start_date must be before end_date")
		return contracts.PaymentOrderFilter{}, false
	}
	return filter, true
}

func parsePositiveQueryInt(w http.ResponseWriter, raw, name string, fallback, maximum int) (int, bool) {
	if strings.TrimSpace(raw) == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || maximum > 0 && value > maximum {
		message := name + " must be a positive integer"
		if maximum > 0 {
			message += " no greater than " + strconv.Itoa(maximum)
		}
		writeError(w, http.StatusBadRequest, "validation_failed", message)
		return 0, false
	}
	return value, true
}

func parsePaymentOrderDate(w http.ResponseWriter, raw, name string, location *time.Location, endOfDay bool) (*time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		parsed = parsed.UTC()
		return &parsed, true
	}
	parsed, err := time.ParseInLocation("2006-01-02", raw, location)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", name+" must be YYYY-MM-DD or RFC3339")
		return nil, false
	}
	if endOfDay {
		parsed = parsed.AddDate(0, 0, 1)
	}
	parsed = parsed.UTC()
	return &parsed, true
}

func validPaymentOrderPaymentType(value string) bool {
	switch value {
	case "alipay", "wxpay", "alipay_direct", "wxpay_direct", "card", "link", "stripe", "airwallex", "easypay":
		return true
	default:
		return false
	}
}

func writePaymentOrderStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "payment order not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "store_error", err.Error())
}
