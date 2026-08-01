package connector

import (
	"bytes"
	"encoding/json"
	"strings"
)

func redactSensitiveJSON(body string) string {
	raw := bytes.TrimSpace([]byte(body))
	if len(raw) == 0 || !json.Valid(raw) {
		return body
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return body
	}
	out, err := json.Marshal(redactValue(value))
	if err != nil {
		return body
	}
	return string(out)
}

func redactValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			if sensitiveField(key) {
				out[key] = "[redacted]"
			} else {
				out[key] = redactValue(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = redactValue(child)
		}
		return out
	default:
		return value
	}
}

func sensitiveField(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, "-", "_")
	switch k {
	case "password", "passwd", "pwd", "token", "secret", "credential",
		"credentials", "authorization", "auth_token", "admin_key", "api_key",
		"secret_key", "turnstile_token", "turnstile_secret", "key", "content":
		return true
	}
	return strings.HasSuffix(k, "_token") ||
		strings.HasSuffix(k, "_secret") ||
		strings.HasSuffix(k, "_credential") ||
		strings.HasSuffix(k, "_credentials") ||
		strings.HasSuffix(k, "_key")
}
