package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"e2m.local/contracts"
)

// QQNotifier posts to the system OneBot 11 HTTP endpoint (e.g. NapCat) via
// send_group_msg. NTQQ is an unofficial protocol, so delivery is best-effort.
type QQNotifier struct {
	baseURL     string
	accessToken string
	groupID     int64
	http        *http.Client
}

var _ Notifier = (*QQNotifier)(nil)

func NewQQ(baseURL, accessToken string, groupID int64) *QQNotifier {
	return &QQNotifier{
		baseURL:     baseURL,
		accessToken: accessToken,
		groupID:     groupID,
		http:        &http.Client{Timeout: 8 * time.Second},
	}
}

func NewPersonalQQ(oneBotURL, accessToken string, groupID int64) *QQNotifier {
	notifier := NewQQ(strings.TrimRight(strings.TrimSpace(oneBotURL), "/"), accessToken, groupID)
	notifier.http = SafeNotificationHTTPClient(8 * time.Second)
	return notifier
}

func (q *QQNotifier) Channel() contracts.NotificationChannel {
	return contracts.NotificationChannelQQ
}

func (q *QQNotifier) Send(ctx context.Context, ev Event) error {
	payload := map[string]any{
		"group_id":    q.groupID,
		"message":     ev.Message(),
		"auto_escape": true,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	targetURL := strings.TrimRight(q.baseURL, "/") + "/send_group_msg"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if q.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+q.accessToken)
	}
	resp, err := q.http.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("qq: request failed: %w", urlErr.Err)
		}
		return errors.New("qq: request failed")
	}
	defer resp.Body.Close()
	// OneBot success requires both an HTTP success and a valid body with
	// status=ok/retcode=0; a proxy-generated empty 2xx is not delivery proof.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("qq: onebot HTTP %d", resp.StatusCode)
	}
	body, err := readProviderResponse(resp.Body)
	if err != nil {
		return Retryable("invalid_provider_response", "QQ 返回了无法识别的结果")
	}
	var result struct {
		Status  string `json:"status"`
		RetCode *int   `json:"retcode"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return Retryable("invalid_provider_response", "QQ 返回了无法识别的结果")
	}
	if result.Status != "ok" || result.RetCode == nil || *result.RetCode != 0 {
		return Permanent("delivery_rejected", "QQ 拒绝了消息")
	}
	return nil
}
