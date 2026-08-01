package newapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

type Gateway struct {
	http     *gateways.Transport
	bindings gateways.BindingResolver
}

const (
	listPageSize = 100
	// One extra request leaves room to detect a non-advancing final page while
	// keeping a hard bound even when a gateway reports a misleading total.
	maxListPages = (contracts.MaxConnectorAccounts+listPageSize-1)/listPageSize + 1
)

func NewGateway(cfg gateways.Config) *Gateway {
	return &Gateway{http: gateways.NewTransport(cfg, gateways.AuthNewAPI), bindings: cfg.BindingResolver}
}

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
}

type channel struct {
	ID        json.Number `json:"id"`
	Name      string      `json:"name"`
	Type      int         `json:"type"`
	Status    int         `json:"status"`
	Priority  int64       `json:"priority"`
	Weight    *int        `json:"weight"`
	Group     string      `json:"group"`
	Models    string      `json:"models"`
	Balance   *float64    `json:"balance"`
	UsedQuota *float64    `json:"used_quota"`
	Tag       string      `json:"tag"`
}

type channelPage struct {
	Items json.RawMessage `json:"items"`
	Total *int            `json:"total"`
}

func (g *Gateway) Health(ctx context.Context) (contracts.ConnectorGatewayHealthResult, error) {
	if _, err := g.ListAccounts(ctx); err != nil {
		return contracts.ConnectorGatewayHealthResult{}, err
	}
	return gateways.HealthOK(), nil
}

func (g *Gateway) ListAccounts(ctx context.Context) ([]contracts.GatewayAccount, error) {
	channels, err := g.listChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]contracts.GatewayAccount, 0, len(channels))
	for _, item := range channels {
		if item.Weight != nil && (*item.Weight < 0 || *item.Weight > 100) {
			return nil, gateways.InvalidResponse()
		}
		externalRef, err := mapExternalRef(item.Tag)
		if err != nil {
			return nil, err
		}
		out = append(out, contracts.GatewayAccount{
			ID: item.ID.String(), Type: fmt.Sprintf("type_%d", item.Type),
			Status: channelStatus(item.Status), Schedulable: item.Status == 1,
			Priority: int(item.Priority), CurrentWeight: cloneInt(item.Weight), GroupIDs: splitGroups(item.Group),
			Models:      splitContractIdentifiers(item.Models),
			DisplayName: strings.TrimSpace(item.Name), Balance: item.Balance, UsedQuota: item.UsedQuota,
			ExternalRef: externalRef,
		})
	}
	return out, nil
}

func (g *Gateway) listChannels(ctx context.Context) ([]channel, error) {
	channels := make([]channel, 0, listPageSize)
	seen := make(map[string]struct{}, listPageSize)
	for page := 1; page <= maxListPages; page++ {
		path := fmt.Sprintf("/api/channel/?p=%d&page_size=%d", page, listPageSize)
		status, raw, err := g.http.Do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		env, err := decodeEnvelope(status, raw)
		if err != nil {
			return nil, err
		}
		items, total, wrapped, err := decodeChannelPage(env.Data)
		if err != nil {
			return nil, err
		}
		if !wrapped && page > 1 {
			return nil, invalidPaginationResponse()
		}
		if total != nil {
			if *total < 0 || *total < len(items) {
				return nil, invalidPaginationResponse()
			}
			if *total > contracts.MaxConnectorAccounts {
				return nil, accountLimitExceeded()
			}
		}

		before := len(channels)
		for _, item := range items {
			id, err := normalizeChannelID(item.ID)
			if err != nil {
				return nil, err
			}
			if _, exists := seen[id]; exists {
				continue
			}
			if len(channels) >= contracts.MaxConnectorAccounts {
				return nil, accountLimitExceeded()
			}
			seen[id] = struct{}{}
			item.ID = json.Number(id)
			channels = append(channels, item)
		}

		if !wrapped {
			return channels, nil
		}
		if total != nil {
			if len(channels) >= *total {
				return channels, nil
			}
			if len(items) == 0 || len(channels) == before {
				return nil, invalidPaginationResponse()
			}
			continue
		}
		// Older NewAPI-compatible implementations omitted total. A short page is
		// the only trustworthy completion signal in that response shape.
		if len(items) < listPageSize {
			return channels, nil
		}
		if len(channels) == before {
			return nil, invalidPaginationResponse()
		}
	}
	return nil, invalidPaginationResponse()
}

func decodeChannelPage(data json.RawMessage) ([]channel, *int, bool, error) {
	if strings.TrimSpace(string(data)) == "null" {
		return nil, nil, false, gateways.InvalidResponse()
	}
	var unwrapped []channel
	if err := json.Unmarshal(data, &unwrapped); err == nil {
		if len(unwrapped) > contracts.MaxConnectorAccounts {
			return nil, nil, false, accountLimitExceeded()
		}
		return unwrapped, nil, false, nil
	}

	var page channelPage
	if err := json.Unmarshal(data, &page); err != nil || len(page.Items) == 0 {
		return nil, nil, false, gateways.InvalidResponse()
	}
	var items []channel
	if string(page.Items) != "null" {
		if err := json.Unmarshal(page.Items, &items); err != nil {
			return nil, nil, false, gateways.InvalidResponse()
		}
	}
	if len(items) > contracts.MaxConnectorAccounts {
		return nil, nil, false, accountLimitExceeded()
	}
	return items, page.Total, true, nil
}

func normalizeChannelID(value json.Number) (string, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(value.String()), 10, 64)
	if err != nil || id <= 0 {
		return "", gateways.InvalidResponse()
	}
	return strconv.FormatInt(id, 10), nil
}

func invalidPaginationResponse() error {
	return &gateways.Error{Code: "gateway_response_invalid", Message: "gateway pagination did not produce a complete account list", Retryable: true}
}

func accountLimitExceeded() error {
	return &gateways.Error{Code: "gateway_response_too_large", Message: "gateway account list exceeded the safe limit"}
}

func mapExternalRef(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "e2m:") {
		return "", nil
	}
	if !isContractIdentifier(strings.TrimPrefix(value, "e2m:"), contracts.MaxConnectorIdentifierBytes-len("e2m:")) {
		return "", gateways.InvalidResponse()
	}
	return value, nil
}

func (g *Gateway) ProvisionAccount(ctx context.Context, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	if spec.Ownership.Normalize() != contracts.GatewayAccountPlatformManaged {
		return contracts.GatewayProvisionResult{}, &gateways.Error{Code: "ownership_violation", Message: "owner-provided accounts are update-only"}
	}
	accounts, err := g.ListAccounts(ctx)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	externalRef := "e2m:" + spec.ChannelID
	account, found, err := findManagedAccount(accounts, externalRef)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	if found {
		spec.RemoteID = account.ID
		result, err := g.UpdateAccount(ctx, spec)
		result.Created = false
		return result, err
	}
	channelBody, err := g.channelBody(ctx, spec, 0)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	status, raw, err := g.http.Do(ctx, http.MethodPost, "/api/channel/", map[string]any{"mode": "single", "channel": channelBody})
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	env, err := decodeEnvelope(status, raw)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	var created channel
	if len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, &created); err == nil && created.ID.String() != "" {
			id, err := normalizeChannelID(created.ID)
			if err != nil {
				return contracts.GatewayProvisionResult{}, err
			}
			return contracts.GatewayProvisionResult{RemoteID: id, Created: true}, nil
		}
	}

	// Current QuantumNous/new-api versions acknowledge a successful create
	// without returning the inserted channel id. Resolve the id through the
	// E2M-owned tag instead of guessing from ordering or names.
	accounts, err = g.ListAccounts(ctx)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	account, found, err = findManagedAccount(accounts, externalRef)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	if !found {
		return contracts.GatewayProvisionResult{}, gateways.InvalidResponse()
	}
	return contracts.GatewayProvisionResult{RemoteID: account.ID, Created: true}, nil
}

func (g *Gateway) UpdateAccount(ctx context.Context, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(spec.RemoteID), 10, 64)
	if err != nil || id <= 0 {
		return contracts.GatewayProvisionResult{}, &gateways.Error{Code: "invalid_account_id", Message: "remote_id must be a positive numeric channel id"}
	}
	if spec.Ownership.Normalize() == contracts.GatewayAccountOwnerProvided {
		return g.updateOwnerChannel(ctx, id, spec)
	}
	body, err := g.channelBody(ctx, spec, id)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	// NewAPI exposes scheduling status through a dedicated operation endpoint
	// and rejects status on the general channel update endpoint.
	delete(body, "status")
	status, raw, err := g.http.Do(ctx, http.MethodPut, "/api/channel/", body)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	if _, err := decodeEnvelope(status, raw); err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	if err := g.SetSchedulable(ctx, strconv.FormatInt(id, 10), spec.Schedulable); err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	return contracts.GatewayProvisionResult{RemoteID: strconv.FormatInt(id, 10)}, nil
}

// NewAPI's general update endpoint is patch-like and preserves omitted fields,
// including the key. Owner updates therefore submit only explicitly managed,
// non-zero routing metadata and never resolve or transmit a credential binding.
func (g *Gateway) updateOwnerChannel(ctx context.Context, id int64, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	body := map[string]any{"id": id}
	if value := strings.TrimSpace(spec.DisplayName); value != "" {
		body["name"] = value
	}
	if value := strings.TrimSpace(spec.Type); value != "" {
		typeID, err := strconv.Atoi(strings.TrimPrefix(value, "type_"))
		if err != nil || typeID <= 0 {
			return contracts.GatewayProvisionResult{}, &gateways.Error{Code: "invalid_gateway_request", Message: "owner channel type must be type_<positive integer>"}
		}
		body["type"] = typeID
	}
	if len(spec.Models) > 0 {
		body["models"] = strings.Join(spec.Models, ",")
	}
	if len(spec.Groups) > 0 {
		body["group"] = strings.Join(spec.Groups, ",")
	}
	if spec.Priority != 0 {
		body["priority"] = spec.Priority
	}
	if spec.Weight != 0 {
		body["weight"] = spec.Weight
	}
	if len(body) > 1 {
		status, raw, err := g.http.Do(ctx, http.MethodPut, "/api/channel/", body)
		if err != nil {
			return contracts.GatewayProvisionResult{}, err
		}
		if _, err := decodeEnvelope(status, raw); err != nil {
			return contracts.GatewayProvisionResult{}, err
		}
	}
	return contracts.GatewayProvisionResult{RemoteID: strconv.FormatInt(id, 10)}, nil
}

func (g *Gateway) DeleteAccount(ctx context.Context, accountID string) error {
	id, err := strconv.ParseInt(strings.TrimSpace(accountID), 10, 64)
	if err != nil || id <= 0 {
		return &gateways.Error{Code: "invalid_account_id", Message: "account_id must be a positive numeric channel id"}
	}
	status, raw, err := g.http.Do(ctx, http.MethodDelete, "/api/channel/"+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	_, err = decodeEnvelope(status, raw)
	return err
}

func (g *Gateway) channelBody(ctx context.Context, spec contracts.GatewayAccountSpec, id int64) (map[string]any, error) {
	credential, err := gateways.ResolveBinding(ctx, g.bindings, spec.CredentialBindingID)
	if err != nil {
		return nil, err
	}
	values, err := newAPICredentialFields(credential)
	if err != nil {
		return nil, err
	}
	typeID := 1
	if rawType := strings.TrimPrefix(strings.TrimSpace(spec.Type), "type_"); rawType != "" {
		if parsed, parseErr := strconv.Atoi(rawType); parseErr == nil && parsed > 0 {
			typeID = parsed
		}
	}
	status := 2
	if spec.Schedulable {
		status = 1
	}
	body := map[string]any{
		"id": id, "name": spec.DisplayName, "type": typeID, "status": status,
		"group": strings.Join(spec.Groups, ","), "priority": spec.Priority,
		"weight": spec.Weight, "models": strings.Join(spec.Models, ","),
		"tag": "e2m:" + spec.ChannelID,
	}
	for key, value := range values {
		body[key] = value
	}
	if spec.ProxyBindingID != "" {
		proxy, err := gateways.ResolveBinding(ctx, g.bindings, spec.ProxyBindingID)
		if err != nil {
			return nil, err
		}
		body["proxy"] = proxy
	}
	return body, nil
}

type newAPICredential struct {
	Key     string `json:"key"`
	BaseURL string `json:"base_url"`
}

func newAPICredentialFields(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, &gateways.Error{Code: "invalid_gateway_request", Message: "credential binding key is empty"}
	}
	if !strings.HasPrefix(trimmed, "{") {
		return map[string]any{"key": raw}, nil
	}
	var credential newAPICredential
	decoder := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credential); err != nil {
		return nil, &gateways.Error{Code: "invalid_gateway_request", Message: "credential binding JSON is invalid"}
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, &gateways.Error{Code: "invalid_gateway_request", Message: "credential binding JSON has trailing data"}
	}
	credential.Key, credential.BaseURL = strings.TrimSpace(credential.Key), strings.TrimSpace(credential.BaseURL)
	parsed, err := url.Parse(credential.BaseURL)
	if credential.Key == "" || err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.String() != credential.BaseURL {
		return nil, &gateways.Error{Code: "invalid_gateway_request", Message: "credential binding key or base_url is invalid"}
	}
	return map[string]any{"key": credential.Key, "base_url": credential.BaseURL}, nil
}

func (g *Gateway) ProbeQuality(context.Context, contracts.ConnectorGatewayQualityProbeInput) (contracts.ConnectorGatewayQualityProbeResult, error) {
	return contracts.ConnectorGatewayQualityProbeResult{}, gateways.QualityProbeUnsupported()
}

func (g *Gateway) SetSchedulable(ctx context.Context, accountID string, schedulable bool) error {
	id, err := strconv.ParseInt(accountID, 10, 64)
	if err != nil || id <= 0 {
		return &gateways.Error{Code: "invalid_account_id", Message: "account_id must be a positive numeric channel id"}
	}
	channelStatus := 2
	if schedulable {
		channelStatus = 1
	}
	status, raw, err := g.http.Do(ctx, http.MethodPost, "/api/channel/"+strconv.FormatInt(id, 10)+"/status", map[string]any{"status": channelStatus})
	if err != nil {
		return err
	}
	_, err = decodeEnvelope(status, raw)
	return err
}

// SetTrafficShare updates NewAPI's native integer channel weight. It never
// changes status: weight 0 is a real value and is not a synonym for disabled.
func (g *Gateway) SetTrafficShare(ctx context.Context, accountID string, weight int) error {
	id, err := strconv.ParseInt(strings.TrimSpace(accountID), 10, 64)
	if err != nil || id <= 0 {
		return &gateways.Error{Code: "invalid_account_id", Message: "account_id must be a positive numeric channel id"}
	}
	if weight < 0 || weight > 100 {
		return &gateways.Error{Code: "invalid_gateway_request", Message: "traffic share weight must be between 0 and 100"}
	}
	status, raw, err := g.http.Do(ctx, http.MethodPut, "/api/channel/", map[string]any{
		"id": id, "weight": weight,
	})
	if err != nil {
		return err
	}
	_, err = decodeEnvelope(status, raw)
	return err
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func findManagedAccount(accounts []contracts.GatewayAccount, externalRef string) (contracts.GatewayAccount, bool, error) {
	var match contracts.GatewayAccount
	found := false
	for _, account := range accounts {
		if account.ExternalRef != externalRef {
			continue
		}
		if found {
			return contracts.GatewayAccount{}, false, gateways.InvalidResponse()
		}
		match = account
		found = true
	}
	return match, found, nil
}

func decodeEnvelope(status int, raw []byte) (envelope, error) {
	if status < 200 || status >= 300 {
		return envelope{}, gateways.HTTPStatusError(status)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return envelope{}, gateways.InvalidResponse()
	}
	if !env.Success {
		return envelope{}, &gateways.Error{Code: "gateway_rejected", Message: "gateway rejected the request"}
	}
	return env, nil
}

func channelStatus(status int) string {
	switch status {
	case 1:
		return "active"
	case 2:
		return "disabled"
	case 3:
		return "error"
	default:
		return fmt.Sprintf("status_%d", status)
	}
}

func splitGroups(value string) []string {
	return splitContractIdentifiers(value)
}

func splitContractIdentifiers(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if !isContractIdentifier(part, contracts.MaxConnectorIdentifierBytes) {
			continue
		}
		if _, exists := seen[part]; exists {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return out
}

func isContractIdentifier(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || value == "." || value == ".." || contracts.LooksLikeConnectorSensitiveValue(value) {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		alphaNumeric := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
		if alphaNumeric || index > 0 && (char == '.' || char == '_' || char == '@' || char == '-') {
			continue
		}
		return false
	}
	return true
}
