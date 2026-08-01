package newapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	newAPIPassiveCursorVersion = "v1"
	newAPILogPageSize          = 100
	newAPIMaxLogPages          = 256
)

// NewAPI log ids are not guaranteed to be stable across every supported log
// backend. The API does guarantee a request id and a second-resolution
// created_at. Retaining the ids seen at the high-water second prevents both
// loss and duplicate delivery when several requests finish in that second.
type newAPIPassiveCursor struct {
	Timestamp int64    `json:"t"`
	Seen      []string `json:"s,omitempty"`
}

type logPage struct {
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
	Total    int         `json:"total"`
	Items    []logRecord `json:"items"`
}

type logRecord struct {
	ID               int             `json:"id"`
	CreatedAt        int64           `json:"created_at"`
	Type             int             `json:"type"`
	Content          string          `json:"content"`
	ModelName        string          `json:"model_name"`
	PromptTokens     int64           `json:"prompt_tokens"`
	CompletionTokens int64           `json:"completion_tokens"`
	UseTime          int64           `json:"use_time"`
	IsStream         bool            `json:"is_stream"`
	ChannelID        int64           `json:"channel"`
	RequestID        string          `json:"request_id"`
	Other            json.RawMessage `json:"other"`
}

func (g *Gateway) ObservationCapabilities() contracts.ConnectorObservationCapabilities {
	return contracts.ConnectorObservationCapabilities{
		PassiveCollection: true, SuccessEvents: true, FailureEvents: true,
		ErrorClassification: true, FirstTokenMS: true, TotalMS: true, TokenCounts: true,
	}
}

// ReadPassiveObservations consumes NewAPI's administrator log API. It reads
// both consume (2) and error (5) rows because a successful request and a failed
// request are recorded as different log types upstream.
func (g *Gateway) ReadPassiveObservations(ctx context.Context, rawCursor string, limit int) (gateways.PassiveObservationPage, error) {
	if limit <= 0 {
		return gateways.PassiveObservationPage{}, &gateways.Error{Code: "invalid_gateway_request", Message: "observation limit must be positive"}
	}
	if limit > 500 {
		limit = 500
	}
	cursor, err := decodeNewAPIPassiveCursor(rawCursor)
	if err != nil {
		return gateways.PassiveObservationPage{}, &gateways.Error{Code: "gateway_response_invalid", Message: "passive observation cursor is invalid"}
	}
	rows, err := g.unseenLogs(ctx, cursor, limit)
	if err != nil {
		return gateways.PassiveObservationPage{}, err
	}
	seenAtWatermark := make(map[string]struct{}, len(cursor.Seen)+len(rows))
	for _, id := range cursor.Seen {
		seenAtWatermark[id] = struct{}{}
	}
	next := cursor
	out := make([]contracts.ConnectorChannelObservation, 0, len(rows))
	for _, row := range rows {
		stableID := newAPILogIdentity(row)
		if row.CreatedAt < cursor.Timestamp {
			continue
		}
		if row.CreatedAt == cursor.Timestamp {
			if _, duplicate := seenAtWatermark[stableID]; duplicate {
				continue
			}
		}
		if row.CreatedAt > next.Timestamp {
			next = newAPIPassiveCursor{Timestamp: row.CreatedAt}
			seenAtWatermark = make(map[string]struct{})
		}
		if row.CreatedAt == next.Timestamp {
			next.Seen = append(next.Seen, stableID)
			seenAtWatermark[stableID] = struct{}{}
		}
		observation, ok := newAPILogObservation(row, stableID)
		if ok {
			out = append(out, observation)
			if len(out) == limit {
				break
			}
		}
	}
	next.Seen = uniqueSorted(next.Seen)
	return gateways.PassiveObservationPage{Observations: out, NextCursor: encodeNewAPIPassiveCursor(next)}, nil
}

func (g *Gateway) unseenLogs(ctx context.Context, cursor newAPIPassiveCursor, limit int) ([]logRecord, error) {
	var rows []logRecord
	for page := 1; page <= newAPIMaxLogPages; page++ {
		query := url.Values{}
		query.Set("p", strconv.Itoa(page))
		query.Set("page_size", strconv.Itoa(newAPILogPageSize))
		if cursor.Timestamp > 0 {
			// Include the watermark second so late commits in that second remain visible.
			query.Set("start_timestamp", strconv.FormatInt(cursor.Timestamp, 10))
		}
		status, raw, err := g.http.Do(ctx, http.MethodGet, "/api/log/?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		env, err := decodeEnvelope(status, raw)
		if err != nil {
			return nil, err
		}
		var pageData logPage
		if err := json.Unmarshal(env.Data, &pageData); err != nil || pageData.Items == nil || pageData.Total < 0 {
			return nil, gateways.InvalidResponse()
		}
		rows = append(rows, pageData.Items...)
		if len(pageData.Items) < newAPILogPageSize || len(rows) >= pageData.Total || len(rows) >= limit+len(cursor.Seen)+newAPILogPageSize {
			break
		}
		if page == newAPIMaxLogPages {
			return nil, &gateways.Error{Code: "gateway_response_too_large", Message: "passive observation backlog exceeded scan limit", Retryable: true}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].CreatedAt == rows[j].CreatedAt {
			return newAPILogIdentity(rows[i]) < newAPILogIdentity(rows[j])
		}
		return rows[i].CreatedAt < rows[j].CreatedAt
	})
	return rows, nil
}

func newAPILogObservation(row logRecord, stableID string) (contracts.ConnectorChannelObservation, bool) {
	model := strings.TrimSpace(row.ModelName)
	if row.ChannelID <= 0 || row.CreatedAt <= 0 || model == "" {
		return contracts.ConnectorChannelObservation{}, false
	}
	success := row.Type == 2
	if !success && row.Type != 5 {
		return contracts.ConnectorChannelObservation{}, false
	}
	observation := contracts.ConnectorChannelObservation{
		ObservationID: "newapi.log.v1." + stableID,
		RemoteID:      strconv.FormatInt(row.ChannelID, 10), Model: model,
		Success: success, InputTokens: max(row.PromptTokens, 0), OutputTokens: max(row.CompletionTokens, 0),
		Source: contracts.ObservationPassive, ObservedAt: time.Unix(row.CreatedAt, 0).UTC(),
	}
	if row.UseTime > 0 {
		observation.TotalMS = float64(row.UseTime) * 1000
	}
	var other map[string]any
	if len(row.Other) > 0 && json.Unmarshal(row.Other, &other) == nil {
		if frt, ok := finiteJSONNumber(other["frt"]); ok && frt >= 0 {
			observation.FirstTokenMS = frt
		}
		if status, ok := finiteJSONNumber(other["status_code"]); ok && status >= 0 && status <= 599 {
			observation.StatusCode = int(status)
		}
	}
	if success {
		return observation, true
	}
	observation.ErrorType = classifyNewAPIError(row.Content, observation.StatusCode)
	return observation, true
}

func classifyNewAPIError(content string, status int) contracts.ObservationErrorType {
	value := strings.ToLower(content)
	switch {
	case status == 499 || strings.Contains(value, "cancel") || strings.Contains(value, "client disconnected"):
		return contracts.ErrorCanceled
	case status == http.StatusUnauthorized || status == http.StatusForbidden || strings.Contains(value, "unauthorized") || strings.Contains(value, "invalid api key"):
		return contracts.ErrorAuth
	case status == http.StatusPaymentRequired || strings.Contains(value, "insufficient") || strings.Contains(value, "quota exhausted"):
		return contracts.ErrorInsufficientBalance
	case status == http.StatusTooManyRequests || strings.Contains(value, "rate limit"):
		return contracts.ErrorRateLimit
	case status == http.StatusGatewayTimeout || strings.Contains(value, "timeout") || strings.Contains(value, "deadline"):
		return contracts.ErrorTimeout
	case status >= 500:
		return contracts.ErrorServer
	case status >= 400 && status < 500:
		return contracts.ErrorClient
	case strings.Contains(value, "network") || strings.Contains(value, "connection") || strings.Contains(value, "dns"):
		return contracts.ErrorNetwork
	default:
		return contracts.ErrorUnknown
	}
}

func newAPILogIdentity(row logRecord) string {
	if id := strings.TrimSpace(row.RequestID); id != "" {
		sum := sha256.Sum256([]byte(id))
		return hex.EncodeToString(sum[:12])
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%d\x00%d\x00%s\x00%s", row.ID, row.CreatedAt, row.ChannelID, row.ModelName, row.Content)))
	return hex.EncodeToString(sum[:12])
}

func decodeNewAPIPassiveCursor(raw string) (newAPIPassiveCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return newAPIPassiveCursor{}, nil
	}
	parts := strings.SplitN(raw, ".", 2)
	if len(parts) != 2 || parts[0] != newAPIPassiveCursorVersion {
		return newAPIPassiveCursor{}, errors.New("unsupported cursor")
	}
	decoded, err := hex.DecodeString(parts[1])
	if err != nil {
		return newAPIPassiveCursor{}, err
	}
	var cursor newAPIPassiveCursor
	if json.Unmarshal(decoded, &cursor) != nil || cursor.Timestamp < 0 || len(cursor.Seen) > 1000 {
		return newAPIPassiveCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}

func encodeNewAPIPassiveCursor(cursor newAPIPassiveCursor) string {
	raw, _ := json.Marshal(cursor)
	return newAPIPassiveCursorVersion + "." + hex.EncodeToString(raw)
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if value == "" || len(out) > 0 && out[len(out)-1] == value {
			continue
		}
		out = append(out, value)
	}
	return out
}

func finiteJSONNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, v == v && v > -1e308 && v < 1e308
	case json.Number:
		parsed, err := v.Float64()
		return parsed, err == nil && parsed == parsed && parsed > -1e308 && parsed < 1e308
	default:
		return 0, false
	}
}
