package sub2api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

const (
	passiveCursorVersion = "v1"
	passivePageSize      = 250
	maxPassivePages      = 256
)

type passiveCursor struct {
	UsageID int64
	ErrorID int64
}

type usagePage struct {
	Items []usageRecord `json:"items"`
}

type usageRecord struct {
	ID            int64     `json:"id"`
	AccountID     int64     `json:"account_id"`
	RequestID     string    `json:"request_id"`
	Model         string    `json:"model"`
	UpstreamModel *string   `json:"upstream_model"`
	InputTokens   int64     `json:"input_tokens"`
	OutputTokens  int64     `json:"output_tokens"`
	FirstTokenMS  *int64    `json:"first_token_ms"`
	DurationMS    *int64    `json:"duration_ms"`
	CreatedAt     time.Time `json:"created_at"`
}

type errorPage struct {
	Items []errorRecord `json:"items"`
}

type errorRecord struct {
	ID              int64     `json:"id"`
	CreatedAt       time.Time `json:"created_at"`
	Phase           string    `json:"phase"`
	Type            string    `json:"type"`
	Owner           string    `json:"error_owner"`
	Source          string    `json:"error_source"`
	StatusCode      int       `json:"status_code"`
	Model           string    `json:"model"`
	RequestedModel  string    `json:"requested_model"`
	UpstreamModel   string    `json:"upstream_model"`
	RequestID       string    `json:"request_id"`
	ClientRequestID string    `json:"client_request_id"`
	Message         string    `json:"message"`
	AccountID       *int64    `json:"account_id"`
}

type errorDetail struct {
	errorRecord
	ResponseLatencyMS  *int64 `json:"response_latency_ms"`
	TimeToFirstTokenMS *int64 `json:"time_to_first_token_ms"`
}

func (g *Gateway) ObservationCapabilities() contracts.ConnectorObservationCapabilities {
	return contracts.ConnectorObservationCapabilities{
		PassiveCollection:   true,
		SuccessEvents:       true,
		FailureEvents:       true,
		ErrorClassification: true,
		FirstTokenMS:        true,
		TotalMS:             true,
		TokenCounts:         true,
	}
}

// ReadPassiveObservations reads append-only request facts from Sub2API's
// administrator usage and ops logs. The two source ids are independently
// cursor-tracked because successful and failed requests live in different
// tables. Each source gets part of the batch, preventing a busy success stream
// from starving error evidence (or vice versa).
func (g *Gateway) ReadPassiveObservations(ctx context.Context, rawCursor string, limit int) (gateways.PassiveObservationPage, error) {
	if limit <= 0 {
		return gateways.PassiveObservationPage{}, &gateways.Error{Code: "invalid_gateway_request", Message: "observation limit must be positive"}
	}
	if limit > 500 {
		limit = 500
	}
	cursor, err := decodePassiveCursor(rawCursor)
	if err != nil {
		return gateways.PassiveObservationPage{}, &gateways.Error{Code: "gateway_response_invalid", Message: "passive observation cursor is invalid"}
	}

	errorLimit := limit / 2
	if errorLimit == 0 {
		errorLimit = 1
	}
	usageLimit := limit - errorLimit
	if usageLimit == 0 {
		usageLimit = 1
	}

	usage, nextUsageID, err := g.readUsageObservations(ctx, cursor.UsageID, usageLimit)
	if err != nil {
		return gateways.PassiveObservationPage{}, err
	}
	failures, nextErrorID, err := g.readErrorObservations(ctx, cursor.ErrorID, errorLimit)
	if err != nil {
		return gateways.PassiveObservationPage{}, err
	}

	observations := append(usage, failures...)
	sort.SliceStable(observations, func(i, j int) bool {
		if observations[i].ObservedAt.Equal(observations[j].ObservedAt) {
			return observations[i].ObservationID < observations[j].ObservationID
		}
		return observations[i].ObservedAt.Before(observations[j].ObservedAt)
	})
	return gateways.PassiveObservationPage{
		Observations: observations,
		NextCursor:   encodePassiveCursor(passiveCursor{UsageID: nextUsageID, ErrorID: nextErrorID}),
	}, nil
}

func (g *Gateway) readUsageObservations(ctx context.Context, afterID int64, limit int) ([]contracts.ConnectorChannelObservation, int64, error) {
	records, err := g.unseenUsageRecords(ctx, afterID)
	if err != nil {
		return nil, afterID, err
	}
	out := make([]contracts.ConnectorChannelObservation, 0, min(limit, len(records)))
	nextID := afterID
	for _, item := range records {
		if item.ID <= afterID {
			continue
		}
		// Advance across rows that cannot be attributed without inventing a
		// remote account, model, or timestamp. They can never become valid later.
		nextID = item.ID
		model := strings.TrimSpace(item.Model)
		if item.UpstreamModel != nil && strings.TrimSpace(*item.UpstreamModel) != "" {
			model = strings.TrimSpace(*item.UpstreamModel)
		}
		if item.AccountID <= 0 || model == "" || item.CreatedAt.IsZero() {
			continue
		}
		observation := contracts.ConnectorChannelObservation{
			ObservationID: passiveObservationID("usage", item.ID),
			RemoteID:      strconv.FormatInt(item.AccountID, 10),
			Model:         model,
			Success:       true,
			InputTokens:   max(item.InputTokens, 0),
			OutputTokens:  max(item.OutputTokens, 0),
			Source:        contracts.ObservationPassive,
			ObservedAt:    item.CreatedAt.UTC(),
		}
		if item.FirstTokenMS != nil && *item.FirstTokenMS >= 0 {
			observation.FirstTokenMS = float64(*item.FirstTokenMS)
		}
		if item.DurationMS != nil && *item.DurationMS >= 0 {
			observation.TotalMS = float64(*item.DurationMS)
		}
		if observation.FirstTokenMS > 0 && observation.TotalMS > 0 && observation.FirstTokenMS > observation.TotalMS {
			// Preserve the valid duration while treating an inconsistent TTFT as
			// unavailable. A fabricated clamp would no longer be a gateway fact.
			observation.FirstTokenMS = 0
		}
		out = append(out, observation)
		if len(out) == limit {
			break
		}
	}
	return out, nextID, nil
}

func (g *Gateway) unseenUsageRecords(ctx context.Context, afterID int64) ([]usageRecord, error) {
	return scanUnseenPages(ctx, afterID, func(page int) ([]usageRecord, error) {
		query := url.Values{}
		query.Set("page", strconv.Itoa(page))
		query.Set("page_size", strconv.Itoa(passivePageSize))
		// Unknown sort keys intentionally fall back to the immutable id in
		// Sub2API. An id-ordered page matches our durable watermark even when
		// concurrent requests commit out of created_at order.
		query.Set("sort_by", "id")
		query.Set("sort_order", "desc")
		// Core accepts observations up to 24 hours old. A two-day UTC query is
		// deliberately wider to tolerate timezone boundaries; Connector applies
		// the final Core-clock age check before upload.
		now := time.Now().UTC()
		query.Set("start_date", now.Add(-48*time.Hour).Format("2006-01-02"))
		query.Set("end_date", now.Format("2006-01-02"))
		query.Set("timezone", "UTC")
		status, raw, err := g.http.Do(ctx, http.MethodGet, typedAdminBase+"/usage?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		env, err := decodeTypedEnvelope(status, raw)
		if err != nil {
			return nil, err
		}
		var data usagePage
		if err := json.Unmarshal(env.Data, &data); err != nil || data.Items == nil {
			return nil, gateways.InvalidResponse()
		}
		return data.Items, nil
	}, func(item usageRecord) int64 { return item.ID })
}

func (g *Gateway) readErrorObservations(ctx context.Context, afterID int64, limit int) ([]contracts.ConnectorChannelObservation, int64, error) {
	records, err := g.unseenErrorRecords(ctx, afterID)
	if err != nil {
		return nil, afterID, err
	}
	out := make([]contracts.ConnectorChannelObservation, 0, min(limit, len(records)))
	nextID := afterID
	for _, item := range records {
		if item.ID <= afterID {
			continue
		}
		nextID = item.ID
		model := firstNonEmpty(item.UpstreamModel, item.Model, item.RequestedModel)
		if item.AccountID == nil || *item.AccountID <= 0 || model == "" || item.CreatedAt.IsZero() {
			continue
		}
		errorType := classifyPassiveError(item)
		var detail errorDetail
		if passiveErrorNeedsTiming(errorType) {
			var found bool
			detail, found, err = g.readErrorDetail(ctx, item.ID)
			if err != nil {
				return nil, afterID, err
			}
			// Detail retention is optional in Sub2API. The list row remains a
			// valid failure fact when the detail has already expired.
			if !found {
				detail = errorDetail{}
			}
		}
		observation := contracts.ConnectorChannelObservation{
			ObservationID: passiveObservationID("error", item.ID),
			RemoteID:      strconv.FormatInt(*item.AccountID, 10),
			Model:         model,
			Success:       false,
			StatusCode:    item.StatusCode,
			ErrorType:     errorType,
			Source:        contracts.ObservationPassive,
			ObservedAt:    item.CreatedAt.UTC(),
		}
		if detail.TimeToFirstTokenMS != nil && *detail.TimeToFirstTokenMS >= 0 {
			observation.FirstTokenMS = float64(*detail.TimeToFirstTokenMS)
		}
		if detail.ResponseLatencyMS != nil && *detail.ResponseLatencyMS >= 0 {
			observation.TotalMS = float64(*detail.ResponseLatencyMS)
		}
		if observation.FirstTokenMS > 0 && observation.TotalMS > 0 && observation.FirstTokenMS > observation.TotalMS {
			observation.FirstTokenMS = 0
		}
		out = append(out, observation)
		if len(out) == limit {
			break
		}
	}
	return out, nextID, nil
}

func (g *Gateway) unseenErrorRecords(ctx context.Context, afterID int64) ([]errorRecord, error) {
	return scanUnseenPages(ctx, afterID, func(page int) ([]errorRecord, error) {
		query := url.Values{}
		query.Set("page", strconv.Itoa(page))
		query.Set("page_size", strconv.Itoa(passivePageSize))
		query.Set("sort_by", "created_at")
		query.Set("sort_order", "desc")
		query.Set("view", "all")
		query.Set("time_range", "24h")
		status, raw, err := g.http.Do(ctx, http.MethodGet, typedAdminBase+"/ops/errors?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		env, err := decodeTypedEnvelope(status, raw)
		if err != nil {
			return nil, err
		}
		var data errorPage
		if err := json.Unmarshal(env.Data, &data); err != nil || data.Items == nil {
			return nil, gateways.InvalidResponse()
		}
		return data.Items, nil
	}, func(item errorRecord) int64 { return item.ID })
}

func (g *Gateway) readErrorDetail(ctx context.Context, id int64) (errorDetail, bool, error) {
	status, raw, err := g.http.Do(ctx, http.MethodGet, typedAdminBase+"/ops/errors/"+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return errorDetail{}, false, err
	}
	if status == http.StatusNotFound {
		return errorDetail{}, false, nil
	}
	env, err := decodeTypedEnvelope(status, raw)
	if err != nil {
		return errorDetail{}, false, err
	}
	var detail errorDetail
	if err := json.Unmarshal(env.Data, &detail); err != nil || detail.ID != id {
		return errorDetail{}, false, gateways.InvalidResponse()
	}
	return detail, true, nil
}

// scanUnseenPages walks newest-first gateway pages until it reaches the durable
// id watermark, then returns rows oldest-first. On first enablement only the
// newest page is backfilled; older history is intentionally outside the live
// quality window and cannot be accepted by Core after 24 hours anyway.
func scanUnseenPages[T any](ctx context.Context, afterID int64, fetch func(int) ([]T, error), id func(T) int64) ([]T, error) {
	var unseen []T
	for page := 1; page <= maxPassivePages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items, err := fetch(page)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		reached := false
		for _, item := range items {
			itemID := id(item)
			if itemID <= afterID {
				reached = true
				continue
			}
			unseen = append(unseen, item)
		}
		if afterID == 0 || reached || len(items) < passivePageSize {
			break
		}
		if page == maxPassivePages {
			return nil, &gateways.Error{Code: "gateway_response_too_large", Message: "passive observation backlog exceeded scan limit", Retryable: true}
		}
	}
	sort.Slice(unseen, func(i, j int) bool { return id(unseen[i]) < id(unseen[j]) })
	return unseen, nil
}

func classifyPassiveError(item errorRecord) contracts.ObservationErrorType {
	combined := strings.ToLower(strings.Join([]string{item.Type, item.Phase, item.Source, item.Message}, " "))
	owner := strings.ToLower(strings.TrimSpace(item.Owner))
	if item.StatusCode == 499 || containsAny(combined, "cancel", "canceled", "cancelled", "client disconnected", "broken pipe") {
		return contracts.ErrorCanceled
	}
	if owner == "platform" {
		return contracts.ErrorPlatform
	}
	if owner == "provider" && (containsAny(combined, "insufficient_balance", "insufficient balance", "credit balance", "quota exhausted") || item.StatusCode == http.StatusPaymentRequired) {
		return contracts.ErrorInsufficientBalance
	}
	if owner == "provider" && (item.StatusCode == http.StatusTooManyRequests || containsAny(combined, "rate_limit", "rate limit", "too many requests")) {
		return contracts.ErrorRateLimit
	}
	// Binding authentication failures are upstream-account facts even though
	// they share the generic "auth" phase with malformed downstream keys.
	if owner == "provider" && (item.StatusCode == http.StatusUnauthorized || item.StatusCode == http.StatusForbidden || containsAny(combined, "invalid_api_key", "authentication", "unauthorized")) {
		return contracts.ErrorAuth
	}
	if owner == "client" || containsAny(item.Source, "client_request") || item.Phase == "request" || item.Phase == "auth" {
		return contracts.ErrorClient
	}
	if containsAny(combined, "insufficient_balance", "insufficient balance", "credit balance", "quota exhausted") || item.StatusCode == http.StatusPaymentRequired {
		return contracts.ErrorInsufficientBalance
	}
	if item.StatusCode == http.StatusTooManyRequests || containsAny(combined, "rate_limit", "rate limit", "too many requests") {
		return contracts.ErrorRateLimit
	}
	if containsAny(combined, "timeout", "deadline exceeded", "timed out") || item.StatusCode == http.StatusGatewayTimeout {
		return contracts.ErrorTimeout
	}
	if owner == "provider" && item.StatusCode >= 400 && item.StatusCode < 500 {
		// An upstream-owned 4xx that is not auth/balance/rate-limit is still
		// provider-side evidence. Unknown remains quality-eligible in Core,
		// whereas client_error would incorrectly exclude it from deductions.
		return contracts.ErrorUnknown
	}
	if item.StatusCode >= 400 && item.StatusCode < 500 {
		return contracts.ErrorClient
	}
	if item.StatusCode >= 500 {
		return contracts.ErrorServer
	}
	if item.Phase == "network" || containsAny(combined, "network", "connection reset", "connection refused", "dial tcp", "dns") {
		return contracts.ErrorNetwork
	}
	return contracts.ErrorUnknown
}

func passiveErrorNeedsTiming(kind contracts.ObservationErrorType) bool {
	switch kind {
	case contracts.ErrorTimeout, contracts.ErrorRateLimit, contracts.ErrorServer,
		contracts.ErrorNetwork, contracts.ErrorUnknown:
		return true
	default:
		return false
	}
}

func decodePassiveCursor(raw string) (passiveCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return passiveCursor{}, nil
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] != passiveCursorVersion {
		return passiveCursor{}, errors.New("unsupported cursor")
	}
	usageID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || usageID < 0 {
		return passiveCursor{}, errors.New("invalid usage cursor")
	}
	errorID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || errorID < 0 {
		return passiveCursor{}, errors.New("invalid error cursor")
	}
	return passiveCursor{UsageID: usageID, ErrorID: errorID}, nil
}

func encodePassiveCursor(cursor passiveCursor) string {
	return fmt.Sprintf("%s.%d.%d", passiveCursorVersion, cursor.UsageID, cursor.ErrorID)
}

func passiveObservationID(kind string, id int64) string {
	return "sub2api." + kind + ".v1." + strconv.FormatInt(id, 10)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func containsAny(value string, needles ...string) bool {
	value = strings.ToLower(value)
	for _, needle := range needles {
		if strings.Contains(value, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
