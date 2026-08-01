package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"e2m.local/contracts"
	"e2m.local/core/internal/vault"
)

func TestPersonalTargetCredentialIsStrictAndChannelBound(t *testing.T) {
	encoded, err := EncodePersonalTargetCredential(PersonalTargetCredential{
		Channel:       contracts.NotificationChannelFeishu,
		WebhookURL:    "https://open.feishu.cn/open-apis/bot/v2/hook/abc_DEF-123",
		SigningSecret: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := DecodePersonalTargetCredential(encoded)
	if err != nil || credential.Channel != contracts.NotificationChannelFeishu || credential.SigningSecret != "secret" {
		t.Fatalf("round trip=%+v err=%v", credential, err)
	}
	for _, raw := range []string{
		strings.TrimSuffix(encoded, "}") + `,"unknown":true}`,
		encoded + `{}`,
		`{"schema_version":1,"type":"notification_target","channel":"qq","onebot_url":"https://bot.example.com","access_token":"token","group_id":"0"}`,
	} {
		if _, err := DecodePersonalTargetCredential(raw); err == nil {
			t.Fatalf("invalid credential accepted: %s", raw)
		}
	}
}

func TestPersonalTargetURLPolicies(t *testing.T) {
	validQQ := PersonalTargetCredential{Channel: contracts.NotificationChannelQQ, OneBotURL: "https://bot.example.com", AccessToken: "token", GroupID: 12345}
	if err := ValidatePersonalTargetCredential(validQQ); err != nil {
		t.Fatalf("valid QQ origin rejected: %v", err)
	}
	for _, target := range []string{
		"https://bot.example.com/api", "https://bot.example.com/?token=x", "https://bot.example.com/%2e%2e/",
		"https://127.0.0.1", "https://bot.example.com:8443",
	} {
		input := validQQ
		input.OneBotURL = target
		if err := ValidatePersonalTargetCredential(input); err == nil {
			t.Fatalf("unsafe QQ target accepted: %q", target)
		}
	}
	for _, target := range []string{
		"https://evil.example.com/open-apis/bot/v2/hook/token",
		"https://open.feishu.cn.evil.example.com/open-apis/bot/v2/hook/token",
		"https://open.feishu.cn/open-apis/bot/v2/hook/token/path",
		"https://open.feishu.cn/open-apis/bot/v2/hook/token?x=1",
	} {
		if err := ValidatePersonalTargetCredential(PersonalTargetCredential{Channel: contracts.NotificationChannelFeishu, WebhookURL: target}); err == nil {
			t.Fatalf("unsafe Feishu target accepted: %q", target)
		}
	}
}

func TestRouterSendsPersonalQQWithAutoEscape(t *testing.T) {
	var autoEscape bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		autoEscape, _ = payload["auto_escape"].(bool)
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0}`))
	}))
	defer server.Close()

	// The policy itself intentionally rejects local endpoints; inject the test
	// sender only to assert the OneBot payload contract.
	qq := NewQQ(server.URL, "token", 12345)
	if err := qq.Send(context.Background(), Event{Text: "[CQ:at,qq=all]"}); err != nil {
		t.Fatal(err)
	}
	if !autoEscape {
		t.Fatal("personal QQ messages must set auto_escape=true")
	}
}

func TestRouterRejectsPersonalCredentialChannelMismatch(t *testing.T) {
	v := vault.NewMemoryVault()
	ref, _ := PersonalNotificationTargetRef(42, contracts.NotificationChannelFeishu)
	qq, _ := EncodePersonalTargetCredential(PersonalTargetCredential{
		Channel: contracts.NotificationChannelQQ, OneBotURL: "https://bot.example.com", AccessToken: "token", GroupID: 123,
	})
	_, _ = v.Store(context.Background(), ref, qq)
	router := NewRouter(nil, nil)
	router.SetSecretResolver(v)
	err := router.SendDelivery(context.Background(), contracts.NotificationDelivery{
		UserID: 42, Channel: contracts.NotificationChannelFeishu, TargetRef: ref,
	})
	code, _, _ := SafeDeliveryError(err)
	if code != "invalid_credential" {
		t.Fatalf("channel mismatch code=%q err=%v", code, err)
	}
}
