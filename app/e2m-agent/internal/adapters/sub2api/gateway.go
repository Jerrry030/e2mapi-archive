package sub2api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

const typedAdminBase = "/api/v1/admin"

type Gateway struct {
	http       *gateways.Transport
	bindings   gateways.BindingResolver
	probeGuard func() error
}

func NewGateway(cfg gateways.Config) *Gateway {
	return &Gateway{http: gateways.NewTransport(cfg, gateways.AuthXAPIKey), bindings: cfg.BindingResolver, probeGuard: cfg.QualityProbeGuard}
}

type typedEnvelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

type typedAccount struct {
	ID          json.Number    `json:"id"`
	Platform    string         `json:"platform"`
	Type        string         `json:"type"`
	Status      string         `json:"status"`
	Schedulable bool           `json:"schedulable"`
	Priority    int            `json:"priority"`
	Groups      []string       `json:"groups"`
	ProxyID     string         `json:"proxy_id"`
	Name        string         `json:"name"`
	ExternalRef string         `json:"external_ref"`
	Extra       map[string]any `json:"extra"`
}

func (g *Gateway) getAccount(ctx context.Context, accountID string) (typedAccount, error) {
	status, raw, err := g.http.Do(ctx, http.MethodGet, typedAdminBase+"/accounts/"+url.PathEscape(accountID), nil)
	if err != nil {
		return typedAccount{}, err
	}
	env, err := decodeTypedEnvelope(status, raw)
	if err != nil {
		return typedAccount{}, err
	}
	var account typedAccount
	if err := json.Unmarshal(env.Data, &account); err != nil || strings.TrimSpace(account.ID.String()) == "" {
		return typedAccount{}, gateways.InvalidResponse()
	}
	return account, nil
}

func (g *Gateway) Health(ctx context.Context) (contracts.ConnectorGatewayHealthResult, error) {
	if _, err := g.ListAccounts(ctx); err != nil {
		return contracts.ConnectorGatewayHealthResult{}, err
	}
	return gateways.HealthOK(), nil
}

func (g *Gateway) ListAccounts(ctx context.Context) ([]contracts.GatewayAccount, error) {
	status, raw, err := g.http.Do(ctx, http.MethodGet, typedAdminBase+"/accounts", nil)
	if err != nil {
		return nil, err
	}
	env, err := decodeTypedEnvelope(status, raw)
	if err != nil {
		return nil, err
	}
	var accounts []typedAccount
	if err := json.Unmarshal(env.Data, &accounts); err != nil {
		var wrapped struct {
			Items []typedAccount `json:"items"`
		}
		if err := json.Unmarshal(env.Data, &wrapped); err != nil || wrapped.Items == nil {
			return nil, gateways.InvalidResponse()
		}
		accounts = wrapped.Items
	}
	out := make([]contracts.GatewayAccount, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, contracts.GatewayAccount{
			ID: account.ID.String(), Platform: account.Platform, Type: account.Type,
			Status: account.Status, Schedulable: account.Schedulable,
			Priority: account.Priority, GroupIDs: account.Groups, ProxyID: account.ProxyID,
			DisplayName: account.Name,
			ExternalRef: account.ExternalRef,
		})
	}
	return out, nil
}

func (g *Gateway) ProvisionAccount(ctx context.Context, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	if spec.Ownership.Normalize() != contracts.GatewayAccountPlatformManaged {
		return contracts.GatewayProvisionResult{}, &gateways.Error{Code: "ownership_violation", Message: "owner-provided accounts are update-only"}
	}
	accounts, err := g.ListAccounts(ctx)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	for _, account := range accounts {
		if account.ExternalRef == spec.ChannelID {
			spec.RemoteID = account.ID
			result, err := g.UpdateAccount(ctx, spec)
			result.Created = false
			return result, err
		}
	}
	body, err := g.accountBody(ctx, spec)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	status, raw, err := g.http.Do(ctx, http.MethodPost, typedAdminBase+"/accounts", body)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	env, err := decodeTypedEnvelope(status, raw)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	var created typedAccount
	if err := json.Unmarshal(env.Data, &created); err != nil || created.ID.String() == "" {
		return contracts.GatewayProvisionResult{}, gateways.InvalidResponse()
	}
	return contracts.GatewayProvisionResult{RemoteID: created.ID.String(), Created: true}, nil
}

func (g *Gateway) UpdateAccount(ctx context.Context, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	accountID := strings.TrimSpace(spec.RemoteID)
	if accountID == "" {
		return contracts.GatewayProvisionResult{}, &gateways.Error{Code: "invalid_account_id", Message: "remote account id is required"}
	}
	if spec.Ownership.Normalize() == contracts.GatewayAccountOwnerProvided {
		return g.updateOwnerAccount(ctx, accountID, spec)
	}
	body, err := g.accountBody(ctx, spec)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	status, raw, err := g.http.Do(ctx, http.MethodPut, typedAdminBase+"/accounts/"+url.PathEscape(accountID), body)
	if err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	if _, err := decodeTypedEnvelope(status, raw); err != nil {
		return contracts.GatewayProvisionResult{}, err
	}
	return contracts.GatewayProvisionResult{RemoteID: accountID}, nil
}

// Owner-provided accounts keep their credential document and every field not
// explicitly represented by a non-zero spec value. In particular, an empty
// binding id is not a request to clear or rotate the owner's credential.
func (g *Gateway) updateOwnerAccount(ctx context.Context, accountID string, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	body := map[string]any{}
	if value := strings.TrimSpace(spec.DisplayName); value != "" {
		body["name"] = value
	}
	if value := strings.TrimSpace(spec.Type); value != "" {
		body["type"] = value
	}
	if spec.Priority != 0 {
		body["priority"] = spec.Priority
	}
	if len(spec.Groups) > 0 {
		groups := make([]int64, 0, len(spec.Groups))
		for _, group := range spec.Groups {
			id, err := strconv.ParseInt(strings.TrimSpace(group), 10, 64)
			if err != nil || id <= 0 {
				return contracts.GatewayProvisionResult{}, &gateways.Error{Code: "invalid_gateway_request", Message: "owner account group ids must be positive integers"}
			}
			groups = append(groups, id)
		}
		body["group_ids"] = groups
	}
	if len(body) > 0 {
		status, raw, err := g.http.Do(ctx, http.MethodPut, typedAdminBase+"/accounts/"+url.PathEscape(accountID), body)
		if err != nil {
			return contracts.GatewayProvisionResult{}, err
		}
		if _, err := decodeTypedEnvelope(status, raw); err != nil {
			return contracts.GatewayProvisionResult{}, err
		}
	}
	return contracts.GatewayProvisionResult{RemoteID: accountID}, nil
}

func (g *Gateway) DeleteAccount(ctx context.Context, accountID string) error {
	status, raw, err := g.http.Do(ctx, http.MethodDelete, typedAdminBase+"/accounts/"+url.PathEscape(strings.TrimSpace(accountID)), nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	_, err = decodeTypedEnvelope(status, raw)
	return err
}

func (g *Gateway) accountBody(ctx context.Context, spec contracts.GatewayAccountSpec) (map[string]any, error) {
	credential, err := gateways.ResolveBinding(ctx, g.bindings, spec.CredentialBindingID)
	if err != nil {
		return nil, err
	}
	credentials := any(map[string]any{"api_key": credential})
	trimmed := bytes.TrimSpace([]byte(credential))
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var decoded map[string]any
		if json.Unmarshal(trimmed, &decoded) != nil {
			return nil, &gateways.Error{Code: "invalid_gateway_request", Message: "credential binding JSON is invalid"}
		}
		credentials = decoded
	}
	body := map[string]any{
		"name": spec.DisplayName, "platform": spec.Provider, "type": spec.Type,
		"credentials": credentials, "extra": map[string]any{},
		"schedulable": spec.Schedulable, "priority": spec.Priority,
		"groups": spec.Groups, "external_ref": spec.ChannelID,
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

func (g *Gateway) SetSchedulable(ctx context.Context, accountID string, schedulable bool) error {
	path := typedAdminBase + "/accounts/" + url.PathEscape(accountID) + "/schedulable"
	status, raw, err := g.http.Do(ctx, http.MethodPost, path, map[string]bool{"schedulable": schedulable})
	if err != nil {
		return err
	}
	_, err = decodeTypedEnvelope(status, raw)
	return err
}

func decodeTypedEnvelope(status int, raw []byte) (typedEnvelope, error) {
	if status < 200 || status >= 300 {
		return typedEnvelope{}, gateways.HTTPStatusError(status)
	}
	var env typedEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return typedEnvelope{}, gateways.InvalidResponse()
	}
	if env.Code != 0 {
		return typedEnvelope{}, &gateways.Error{Code: "gateway_rejected", Message: "gateway rejected the request"}
	}
	return env, nil
}
