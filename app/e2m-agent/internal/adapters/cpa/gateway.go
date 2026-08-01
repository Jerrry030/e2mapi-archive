package cpa

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

const managementBase = "/v0/management"

type Gateway struct {
	http                   *gateways.Transport
	bindings               gateways.BindingResolver
	usageStatisticsEnabled bool
	authIndexMu            sync.RWMutex
	authIndexToName        map[string]string
}

func NewGateway(cfg gateways.Config) *Gateway {
	return &Gateway{http: gateways.NewTransport(cfg, gateways.AuthBearer), bindings: cfg.BindingResolver, usageStatisticsEnabled: cfg.CPAUsageStatisticsEnabled}
}

func (g *Gateway) ProvisionAccount(ctx context.Context, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	if spec.Ownership.Normalize() != contracts.GatewayAccountPlatformManaged {
		return contracts.GatewayProvisionResult{}, &gateways.Error{Code: "ownership_violation", Message: "owner-provided accounts are update-only"}
	}
	name := cpaManagedName(spec.ChannelID)
	accounts, err := g.ListAccounts(ctx)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	for _, account := range accounts {
		if account.ID == name {
			spec.RemoteID = name
			result, err := g.UpdateAccount(ctx, spec)
			result.Created = false
			return result, err
		}
	}
	if err := g.writeAuthFile(ctx, name, spec); err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	return contracts.GatewayProvisionResult{RemoteID: name, Created: true}, nil
}

func (g *Gateway) UpdateAccount(ctx context.Context, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	name := strings.TrimSpace(spec.RemoteID)
	if name == "" {
		return contracts.GatewayProvisionResult{}, &gateways.Error{Code: "invalid_account_id", Message: "remote auth file name is required"}
	}
	if spec.Ownership.Normalize() == contracts.GatewayAccountOwnerProvided {
		return g.updateOwnerAuthFile(ctx, name, spec)
	}
	if err := g.writeAuthFile(ctx, name, spec); err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	return contracts.GatewayProvisionResult{RemoteID: name}, nil
}

// CPA exposes a field-level patch for existing auth files. Using it keeps the
// credential file opaque to Connector: owner updates never download or upload
// the file and an empty spec value means that field remains unmanaged.
func (g *Gateway) updateOwnerAuthFile(ctx context.Context, name string, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	body := map[string]any{"name": name}
	if spec.Priority != 0 {
		body["priority"] = spec.Priority
	}
	if len(body) > 1 {
		status, _, err := g.http.Do(ctx, http.MethodPatch, managementBase+"/auth-files/fields", body)
		if err != nil {
			return contracts.GatewayProvisionResult{}, err
		}
		if status < 200 || status >= 300 {
			return contracts.GatewayProvisionResult{}, gateways.HTTPStatusError(status)
		}
	}
	return contracts.GatewayProvisionResult{RemoteID: name}, nil
}

func (g *Gateway) DeleteAccount(ctx context.Context, accountID string) error {
	name := strings.TrimSpace(accountID)
	if name == "" {
		return &gateways.Error{Code: "invalid_account_id", Message: "account_id is required"}
	}
	status, _, err := g.http.Do(ctx, http.MethodDelete, managementBase+"/auth-files?name="+url.QueryEscape(name), nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	if status < 200 || status >= 300 {
		return gateways.HTTPStatusError(status)
	}
	return nil
}

func (g *Gateway) writeAuthFile(ctx context.Context, name string, spec contracts.GatewayAccountSpec) error {
	credential, err := gateways.ResolveBinding(ctx, g.bindings, spec.CredentialBindingID)
	if err != nil {
		return err
	}
	var body map[string]any
	if json.Unmarshal([]byte(credential), &body) != nil || body == nil {
		body = map[string]any{"api_key": credential}
	}
	if spec.ProxyBindingID != "" {
		proxy, err := gateways.ResolveBinding(ctx, g.bindings, spec.ProxyBindingID)
		if err != nil {
			return err
		}
		body["proxy_url"] = proxy
	}
	status, _, err := g.http.Do(ctx, http.MethodPost, managementBase+"/auth-files?name="+url.QueryEscape(name), body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return gateways.HTTPStatusError(status)
	}
	if !spec.Schedulable {
		return g.SetSchedulable(ctx, name, false)
	}
	return nil
}

func cpaManagedName(channelID string) string {
	id := strings.Trim(strings.TrimSpace(channelID), ".")
	id = strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(id)
	if id == "" {
		id = "managed"
	}
	return "e2m-" + id + ".json"
}

type authFile struct {
	ID        cpaWireScalar `json:"id,omitempty"`
	AuthIndex cpaWireScalar `json:"auth_index,omitempty"`
	Name      string        `json:"name"`
	Label     string        `json:"label"`
	Status    string        `json:"status"`
	Disabled  bool          `json:"disabled"`
	Provider  string        `json:"provider"`
	Type      string        `json:"type"`
}

// cpaWireScalar accepts the string and numeric identifiers emitted by
// different CLIProxyAPI versions while keeping their exact textual identity.
type cpaWireScalar string

func (value *cpaWireScalar) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) || len(raw) == 0 {
		*value = ""
		return nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		*value = cpaWireScalar(strings.TrimSpace(text))
		return nil
	}
	if _, err := strconv.ParseInt(string(raw), 10, 64); err != nil {
		return err
	}
	*value = cpaWireScalar(string(raw))
	return nil
}

func (g *Gateway) Health(ctx context.Context) (contracts.ConnectorGatewayHealthResult, error) {
	if _, err := g.ListAccounts(ctx); err != nil {
		return contracts.ConnectorGatewayHealthResult{}, err
	}
	return gateways.HealthOK(), nil
}

func (g *Gateway) ListAccounts(ctx context.Context) ([]contracts.GatewayAccount, error) {
	status, raw, err := g.http.Do(ctx, http.MethodGet, managementBase+"/auth-files", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, gateways.HTTPStatusError(status)
	}
	var wrapped struct {
		Files []authFile `json:"files"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil || wrapped.Files == nil {
		return nil, gateways.InvalidResponse()
	}
	out := make([]contracts.GatewayAccount, 0, len(wrapped.Files))
	index := make(map[string]string, len(wrapped.Files)*3)
	for _, file := range wrapped.Files {
		name := strings.TrimSpace(file.Name)
		if name == "" {
			continue
		}
		index[name] = name
		if id := strings.TrimSpace(string(file.ID)); id != "" {
			index[id] = name
		}
		if authIndex := strings.TrimSpace(string(file.AuthIndex)); authIndex != "" {
			index[authIndex] = name
		}
		displayName := file.Label
		if displayName == "" {
			displayName = file.Name
		}
		out = append(out, contracts.GatewayAccount{
			ID: file.Name, Platform: file.Provider, Type: file.Type,
			Status:      authFileStatus(file.Status, file.Disabled),
			Schedulable: !file.Disabled, DisplayName: displayName,
		})
	}
	g.authIndexMu.Lock()
	g.authIndexToName = index
	g.authIndexMu.Unlock()
	return out, nil
}

func (g *Gateway) resolveAuthIndex(value string) (string, bool) {
	value = strings.TrimSpace(value)
	g.authIndexMu.RLock()
	name, ok := g.authIndexToName[value]
	g.authIndexMu.RUnlock()
	return name, ok
}

func (g *Gateway) ProbeQuality(context.Context, contracts.ConnectorGatewayQualityProbeInput) (contracts.ConnectorGatewayQualityProbeResult, error) {
	return contracts.ConnectorGatewayQualityProbeResult{}, gateways.QualityProbeUnsupported()
}

func (g *Gateway) SetSchedulable(ctx context.Context, accountID string, schedulable bool) error {
	status, _, err := g.http.Do(ctx, http.MethodPatch, managementBase+"/auth-files/status", map[string]any{
		"name": accountID, "disabled": !schedulable,
	})
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return gateways.HTTPStatusError(status)
	}
	return nil
}

func authFileStatus(status string, disabled bool) string {
	switch normalized := strings.ToLower(strings.TrimSpace(status)); normalized {
	case "", "ok", "active", "ready":
		if disabled {
			return "disabled"
		}
		return "active"
	case "error", "invalid", "failed":
		return "error"
	case "quota", "quota_exceeded", "rate_limited", "429":
		return "rate_limited"
	default:
		return normalized
	}
}
