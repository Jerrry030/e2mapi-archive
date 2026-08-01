package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"e2m.local/contracts"
)

// FeishuNotifier posts to the system Feishu custom-bot webhook. It does not
// depend on an unofficial protocol and supports an optional signing secret.
type FeishuNotifier struct {
	webhookURL string
	secret     string
	http       *http.Client
	now        func() time.Time
}

var _ Notifier = (*FeishuNotifier)(nil)

func NewFeishu(webhookURL, secret string) *FeishuNotifier {
	return &FeishuNotifier{
		webhookURL: webhookURL,
		secret:     secret,
		http:       &http.Client{Timeout: 10 * time.Second},
		now:        func() time.Time { return time.Now() },
	}
}

func NewPersonalFeishu(webhookURL, secret string) *FeishuNotifier {
	notifier := NewFeishu(webhookURL, secret)
	notifier.http = SafeNotificationHTTPClient(10 * time.Second)
	return notifier
}

func (f *FeishuNotifier) Channel() contracts.NotificationChannel {
	return contracts.NotificationChannelFeishu
}

func (f *FeishuNotifier) Send(ctx context.Context, ev Event) error {
	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": ev.Message()},
	}
	if f.secret != "" {
		ts := strconv.FormatInt(f.now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = feishuSign(ts, f.secret)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.webhookURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.http.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("feishu: request failed: %w", urlErr.Err)
		}
		return errors.New("feishu: request failed")
	}
	defer resp.Body.Close()
	body, readErr := readProviderResponse(resp.Body)
	if readErr != nil {
		return Retryable("invalid_provider_response", "Feishu returned an unreadable response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("feishu: webhook HTTP %d", resp.StatusCode)
	}
	// Feishu returns {code:0,...} on success and {code:<n>,msg:...} on failure.
	var env struct {
		Code *int   `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return Retryable("invalid_provider_response", "飞书返回了无法识别的结果")
	}
	if env.Code == nil {
		return Retryable("invalid_provider_response", "Feishu returned an invalid response")
	}
	if *env.Code != 0 {
		return fmt.Errorf("feishu: webhook error %d", *env.Code)
	}
	return nil
}

// feishuSign computes the Feishu custom-bot signature: HmacSHA256 with key
// "<timestamp>\n<secret>" over an empty message, base64-encoded.
func feishuSign(timestamp, secret string) string {
	key := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(key))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
