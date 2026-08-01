package connector

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"e2m.local/contracts"
)

func validBindingProofInput(input contracts.ConnectorGatewayBindingProofInput) bool {
	if input.ChannelID != strings.TrimSpace(input.ChannelID) ||
		input.BindingID != strings.TrimSpace(input.BindingID) ||
		!contracts.IsConnectorQualityProbeField(input.ChannelID) ||
		!contracts.IsConnectorQualityProbeField(input.BindingID) {
		return false
	}
	return validLowerHex(input.Challenge, contracts.ConnectorGatewayBindingProofChallengeHexLength)
}

func validLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'f' {
				return false
			}
		}
	}
	return true
}

func (c *Connector) executeBindingProof(ctx context.Context, task contracts.ConnectorTask) taskResult {
	var input contracts.ConnectorGatewayBindingProofInput
	if err := json.Unmarshal(task.Input, &input); err != nil {
		return failedTask("invalid_task_input", "binding proof input is invalid", false)
	}
	if c.cfg.ConfigStore == nil {
		return failedTask("gateway_config_unavailable", "binding store is unavailable", true)
	}
	binding, err := c.cfg.ConfigStore.BindingResolver().ResolveBinding(ctx, input.BindingID)
	if errors.Is(err, os.ErrNotExist) {
		return failedTask("binding_not_found", "binding does not exist", false)
	}
	if err != nil {
		return failedTask("gateway_config_unavailable", "binding store is unavailable", true)
	}
	cfg, err := c.cfg.ConfigStore.Load()
	if err != nil {
		return failedTask("gateway_config_unavailable", "gateway kind is unavailable", true)
	}
	cfg.Normalize()
	apiKey, err := bindingAPIKey(cfg.GatewayKind, binding)
	if err != nil {
		return failedTask("invalid_gateway_request", "binding does not contain one unambiguous API key", false)
	}

	mac := hmac.New(sha256.New, []byte(apiKey))
	_, _ = mac.Write(contracts.ConnectorGatewayBindingProofMessage(input))
	result := contracts.ConnectorGatewayBindingProofResult{Proof: hex.EncodeToString(mac.Sum(nil))}
	return gatewayResult(result, nil)
}

// bindingAPIKey mirrors the selected lifecycle adapter's credential shape.
// Raw bindings are the API key itself. For JSON, NewAPI writes key while
// Sub2API and CPA write api_key; surrounding JSON is metadata, not the secret.
func bindingAPIKey(gatewayKind, binding string) (string, error) {
	trimmed := strings.TrimSpace(binding)
	if trimmed == "" {
		return "", errors.New("empty binding")
	}
	var field string
	switch strings.ToLower(strings.TrimSpace(gatewayKind)) {
	case "newapi", "new-api":
		field = "key"
	case "sub2api", "cpa":
		field = "api_key"
	default:
		return "", errors.New("unsupported gateway kind")
	}
	if !strings.HasPrefix(trimmed, "{") {
		return binding, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(binding), &fields); err != nil || fields == nil {
		return "", errors.New("invalid credential JSON")
	}
	raw, ok := fields[field]
	if !ok {
		return "", errors.New("API key field is missing")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", errors.New("API key field must be a non-empty string")
	}
	return value, nil
}
