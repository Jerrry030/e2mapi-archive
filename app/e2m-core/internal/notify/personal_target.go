package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strconv"
	"strings"

	"e2m.local/contracts"
)

const personalTargetSchemaVersion = 1

const (
	maxNotificationTargetURLLength    = 2048
	maxNotificationTargetSecretLength = 4096
	maxNotificationTargetJSONLength   = 16 << 10
)

type personalTargetEnvelope struct {
	SchemaVersion int                           `json:"schema_version"`
	Type          string                        `json:"type"`
	Channel       contracts.NotificationChannel `json:"channel"`
	WebhookURL    string                        `json:"webhook_url,omitempty"`
	SigningSecret string                        `json:"signing_secret,omitempty"`
	OneBotURL     string                        `json:"onebot_url,omitempty"`
	AccessToken   string                        `json:"access_token,omitempty"`
	GroupID       string                        `json:"group_id,omitempty"`
}

// PersonalTargetCredential is plaintext resolved from the Vault at the final
// send boundary. It must never be persisted, returned by HTTP, or logged.
type PersonalTargetCredential struct {
	Channel       contracts.NotificationChannel
	WebhookURL    string
	SigningSecret string
	OneBotURL     string
	AccessToken   string
	GroupID       int64
}

func EncodePersonalTargetCredential(input PersonalTargetCredential) (string, error) {
	if err := ValidatePersonalTargetCredential(input); err != nil {
		return "", err
	}
	envelope := personalTargetEnvelope{
		SchemaVersion: personalTargetSchemaVersion,
		Type:          "notification_target",
		Channel:       input.Channel,
		WebhookURL:    strings.TrimSpace(input.WebhookURL),
		SigningSecret: input.SigningSecret,
		OneBotURL:     strings.TrimRight(strings.TrimSpace(input.OneBotURL), "/"),
		AccessToken:   input.AccessToken,
	}
	if input.Channel == contracts.NotificationChannelQQ {
		envelope.GroupID = strconv.FormatInt(input.GroupID, 10)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func DecodePersonalTargetCredential(raw string) (PersonalTargetCredential, error) {
	if len(raw) == 0 || len(raw) > maxNotificationTargetJSONLength {
		return PersonalTargetCredential{}, errors.New("invalid notification target credential")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var envelope personalTargetEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return PersonalTargetCredential{}, errors.New("invalid notification target credential")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return PersonalTargetCredential{}, errors.New("invalid notification target credential")
	}
	if envelope.SchemaVersion != personalTargetSchemaVersion || envelope.Type != "notification_target" {
		return PersonalTargetCredential{}, errors.New("unsupported notification target credential")
	}
	credential := PersonalTargetCredential{
		Channel: envelope.Channel, WebhookURL: strings.TrimSpace(envelope.WebhookURL),
		SigningSecret: envelope.SigningSecret, OneBotURL: strings.TrimRight(strings.TrimSpace(envelope.OneBotURL), "/"),
		AccessToken: envelope.AccessToken,
	}
	if envelope.Channel == contracts.NotificationChannelQQ {
		groupID, err := strconv.ParseInt(strings.TrimSpace(envelope.GroupID), 10, 64)
		if err != nil || groupID <= 0 {
			return PersonalTargetCredential{}, errors.New("invalid notification target credential")
		}
		credential.GroupID = groupID
	}
	if err := ValidatePersonalTargetCredential(credential); err != nil {
		return PersonalTargetCredential{}, errors.New("invalid notification target credential")
	}
	return credential, nil
}

func ValidatePersonalTargetCredential(input PersonalTargetCredential) error {
	if len(input.WebhookURL) > maxNotificationTargetURLLength || len(input.OneBotURL) > maxNotificationTargetURLLength ||
		len(input.SigningSecret) > maxNotificationTargetSecretLength || len(input.AccessToken) > maxNotificationTargetSecretLength {
		return errors.New("notification target field is too long")
	}
	switch input.Channel {
	case contracts.NotificationChannelFeishu:
		if strings.TrimSpace(input.WebhookURL) == "" {
			return errors.New("webhook_url is required")
		}
		if err := ValidateWebhookURL(input.WebhookURL); err != nil {
			return fmt.Errorf("invalid webhook_url: %w", err)
		}
		if err := validateFeishuWebhookURL(input.WebhookURL); err != nil {
			return err
		}
		if strings.TrimSpace(input.OneBotURL) != "" || strings.TrimSpace(input.AccessToken) != "" || input.GroupID != 0 {
			return errors.New("QQ fields are not allowed for Feishu")
		}
	case contracts.NotificationChannelQQ:
		if strings.TrimSpace(input.OneBotURL) == "" {
			return errors.New("onebot_url is required")
		}
		if err := ValidateOneBotURL(input.OneBotURL); err != nil {
			return err
		}
		if strings.TrimSpace(input.AccessToken) == "" {
			return errors.New("access_token is required")
		}
		if input.GroupID <= 0 {
			return errors.New("group_id must be a positive integer")
		}
		if strings.TrimSpace(input.WebhookURL) != "" || strings.TrimSpace(input.SigningSecret) != "" {
			return errors.New("Feishu fields are not allowed for QQ")
		}
	default:
		return errors.New("channel must be feishu or qq")
	}
	return nil
}

func ValidateOneBotURL(raw string) error {
	if err := ValidateWebhookURL(raw); err != nil {
		return fmt.Errorf("invalid onebot_url: %w", err)
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return errors.New("invalid onebot_url")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil || parsed.Port() != "" {
		return errors.New("onebot_url must not contain query, fragment, or credentials")
	}
	if parsed.EscapedPath() != parsed.Path || (parsed.Path != "" && parsed.Path != "/") ||
		(parsed.Path != "" && path.Clean(parsed.Path) != "/") {
		return errors.New("onebot_url must be an HTTPS origin without a path")
	}
	return nil
}

func validateFeishuWebhookURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return errors.New("invalid webhook_url")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host != "open.feishu.cn" && host != "open.larksuite.com" {
		return errors.New("webhook_url must be an official Feishu or Lark bot webhook")
	}
	if parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.EscapedPath() != parsed.Path {
		return errors.New("webhook_url contains unsupported URL components")
	}
	const prefix = "/open-apis/bot/v2/hook/"
	token := strings.TrimPrefix(parsed.Path, prefix)
	if token == parsed.Path || token == "" || strings.Contains(token, "/") || len(token) > 256 {
		return errors.New("webhook_url must be an official Feishu or Lark bot webhook")
	}
	for _, char := range token {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return errors.New("webhook_url must be an official Feishu or Lark bot webhook")
	}
	return nil
}

func PersonalNotificationTargetRef(userID int64, channel contracts.NotificationChannel) (string, error) {
	if userID <= 0 || (channel != contracts.NotificationChannelFeishu && channel != contracts.NotificationChannelQQ) {
		return "", errors.New("invalid personal notification target scope")
	}
	return fmt.Sprintf("credential_ref:user/%d/notification/personal-%s", userID, channel), nil
}

func IsPersonalNotificationTargetRef(ref string, userID int64, channel contracts.NotificationChannel) bool {
	want, err := PersonalNotificationTargetRef(userID, channel)
	return err == nil && strings.TrimSpace(ref) == want
}

func IsSystemNotificationTargetRef(ref string, channel contracts.NotificationChannel) bool {
	return strings.TrimSpace(ref) == "system:"+string(channel) &&
		(channel == contracts.NotificationChannelFeishu || channel == contracts.NotificationChannelQQ)
}
