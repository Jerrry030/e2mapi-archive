package sub2api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"e2m.local/contracts"
)

// The intelligence collector deliberately uses only the standard, user-scoped
// Sub2API read endpoints verified against the upstream source. It is separate
// from Gateway because Gateway is an administrator lifecycle adapter.
const (
	IntelligenceProfilePath  = "/user/profile"
	IntelligenceGroupsPath   = "/groups/available"
	IntelligenceRatesPath    = "/groups/rates"
	IntelligenceChannelsPath = "/channels/available"

	defaultIntelligenceEndpointTimeout = 10 * time.Second
	defaultIntelligenceResponseBytes   = int64(2 << 20)
	maxIntelligenceResponseBytes       = int64(8 << 20)

	maxIntelligenceGroups            = 2_048
	maxIntelligenceRates             = 2_048
	maxIntelligenceChannels          = 512
	maxIntelligencePlatforms         = 16
	maxIntelligenceGroupsPerPlatform = 2_048
	maxIntelligenceModelsPerPlatform = 4_096
	maxIntelligencePricingIntervals  = 64
	maxIntelligenceStringBytes       = 512
)

type IntelligenceErrorCode string

const (
	IntelligenceAuthFailed          IntelligenceErrorCode = "auth_failed"
	IntelligenceRateLimited         IntelligenceErrorCode = "rate_limited"
	IntelligenceSchemaUnsupported   IntelligenceErrorCode = "schema_unsupported"
	IntelligenceResponseTooLarge    IntelligenceErrorCode = "response_too_large"
	IntelligenceUpstreamUnavailable IntelligenceErrorCode = "upstream_unavailable"
)

// IntelligenceError is safe to persist or return from the Connector. It never
// carries the upstream URL, credential, response body, or an upstream message.
type IntelligenceError struct {
	Code      IntelligenceErrorCode
	Retryable bool
}

func (e *IntelligenceError) Error() string {
	if e == nil {
		return ""
	}
	return string(e.Code)
}

func intelligenceError(code IntelligenceErrorCode, retryable bool) error {
	return &IntelligenceError{Code: code, Retryable: retryable}
}

func schemaUnsupported() error {
	return intelligenceError(IntelligenceSchemaUnsupported, false)
}

type IntelligenceEndpoint string

const (
	IntelligenceEndpointProfile  IntelligenceEndpoint = "profile"
	IntelligenceEndpointGroups   IntelligenceEndpoint = "groups"
	IntelligenceEndpointRates    IntelligenceEndpoint = "rates"
	IntelligenceEndpointChannels IntelligenceEndpoint = "channels"
)

type IntelligenceEndpointState struct {
	Endpoint  IntelligenceEndpoint  `json:"endpoint"`
	Available bool                  `json:"available"`
	ErrorCode IntelligenceErrorCode `json:"error_code,omitempty"`
	Retryable bool                  `json:"retryable"`
}

// IntelligenceAuthorizeFunc makes authentication an explicit caller-owned
// concern. UI-03 currently stores an administrator x-api-key, but acceptance of
// that credential by these user endpoints is not established; this adapter does
// not silently reuse or reinterpret it.
type IntelligenceAuthorizeFunc func(*http.Request) error

type IntelligenceClientConfig struct {
	// BaseURL is the normalized Sub2API API root (normally ending in
	// /api/v1), not merely the station origin.
	BaseURL          string
	HTTPClient       *http.Client
	Authorize        IntelligenceAuthorizeFunc
	EndpointTimeout  time.Duration
	MaxResponseBytes int64
}

type IntelligenceClient struct {
	apiBaseURL       string
	client           *http.Client
	authorize        IntelligenceAuthorizeFunc
	endpointTimeout  time.Duration
	maxResponseBytes int64
}

type IntelligenceRechargeYield struct {
	Value      *contracts.CanonicalDecimal        `json:"value,omitempty"`
	Accuracy   contracts.UpstreamEvidenceAccuracy `json:"accuracy"`
	ReasonCode string                             `json:"reason_code"`
}

type IntelligenceWallet struct {
	Balance  contracts.CanonicalDecimal       `json:"balance"`
	UnitKind contracts.UpstreamWalletUnitKind `json:"unit_kind"`
}

type IntelligenceGroup struct {
	ID              int64                       `json:"id"`
	Name            string                      `json:"name"`
	Platform        string                      `json:"platform"`
	DefaultRate     contracts.CanonicalDecimal  `json:"default_rate"`
	EffectiveRate   *contracts.CanonicalDecimal `json:"effective_rate,omitempty"`
	EffectiveSource string                      `json:"effective_source,omitempty"`
}

type IntelligencePricingInterval struct {
	MinTokens       int64                       `json:"min_tokens"`
	MaxTokens       *int64                      `json:"max_tokens,omitempty"`
	TierLabel       string                      `json:"tier_label,omitempty"`
	InputPrice      *contracts.CanonicalDecimal `json:"input_price,omitempty"`
	OutputPrice     *contracts.CanonicalDecimal `json:"output_price,omitempty"`
	CacheWritePrice *contracts.CanonicalDecimal `json:"cache_write_price,omitempty"`
	CacheReadPrice  *contracts.CanonicalDecimal `json:"cache_read_price,omitempty"`
	PerRequestPrice *contracts.CanonicalDecimal `json:"per_request_price,omitempty"`
}

// Token prices are the exact per-token numbers exposed by Sub2API. Per-request
// prices are per request. No implicit conversion to per-million-token prices is
// performed in the adapter.
type IntelligencePricing struct {
	BillingMode      string                        `json:"billing_mode"`
	PerTokens        int64                         `json:"per_tokens"`
	InputPrice       *contracts.CanonicalDecimal   `json:"input_price,omitempty"`
	OutputPrice      *contracts.CanonicalDecimal   `json:"output_price,omitempty"`
	CacheWritePrice  *contracts.CanonicalDecimal   `json:"cache_write_price,omitempty"`
	CacheReadPrice   *contracts.CanonicalDecimal   `json:"cache_read_price,omitempty"`
	ImageOutputPrice *contracts.CanonicalDecimal   `json:"image_output_price,omitempty"`
	PerRequestPrice  *contracts.CanonicalDecimal   `json:"per_request_price,omitempty"`
	Intervals        []IntelligencePricingInterval `json:"intervals"`
}

type IntelligenceModel struct {
	Name             string               `json:"name"`
	Platform         string               `json:"platform"`
	ReferencePricing *IntelligencePricing `json:"reference_pricing,omitempty"`
	SitePricing      *IntelligencePricing `json:"site_pricing,omitempty"`
}

type IntelligenceChannelGroup struct {
	ID          int64                      `json:"id"`
	Name        string                     `json:"name"`
	DefaultRate contracts.CanonicalDecimal `json:"default_rate"`
}

type IntelligenceChannelPlatform struct {
	Platform string                     `json:"platform"`
	Groups   []IntelligenceChannelGroup `json:"groups"`
	Models   []IntelligenceModel        `json:"models"`
}

type IntelligenceChannel struct {
	Name        string                        `json:"name"`
	Description string                        `json:"description"`
	Platforms   []IntelligenceChannelPlatform `json:"platforms"`
}

type IntelligenceSnapshot struct {
	Coverage      contracts.UpstreamEvidenceCoverage `json:"coverage"`
	Wallet        *IntelligenceWallet                `json:"wallet,omitempty"`
	Groups        []IntelligenceGroup                `json:"groups,omitempty"`
	Channels      []IntelligenceChannel              `json:"channels,omitempty"`
	RechargeYield IntelligenceRechargeYield          `json:"recharge_yield"`
	Endpoints     []IntelligenceEndpointState        `json:"endpoints"`
}

func NewIntelligenceClient(cfg IntelligenceClientConfig) (*IntelligenceClient, error) {
	baseURL, err := validateIntelligenceBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.Authorize == nil {
		return nil, errors.New("Sub2API intelligence authorizer is required")
	}
	timeout := cfg.EndpointTimeout
	if timeout == 0 {
		timeout = defaultIntelligenceEndpointTimeout
	}
	if timeout <= 0 || timeout > time.Minute {
		return nil, errors.New("Sub2API intelligence endpoint timeout is out of bounds")
	}
	maxBytes := cfg.MaxResponseBytes
	if maxBytes == 0 {
		maxBytes = defaultIntelligenceResponseBytes
	}
	if maxBytes <= 0 || maxBytes > maxIntelligenceResponseBytes {
		return nil, errors.New("Sub2API intelligence response limit is out of bounds")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	} else {
		clone := *client
		client = &clone
	}
	// Cookies and redirects can move a local credential beyond the configured
	// origin. Both are disabled even when the caller supplies a custom client.
	client.Jar = nil
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &IntelligenceClient{
		apiBaseURL: baseURL, client: client, authorize: cfg.Authorize,
		endpointTimeout: timeout, maxResponseBytes: maxBytes,
	}, nil
}

func validateIntelligenceBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("Sub2API intelligence base URL must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Sub2API intelligence base URL must not contain user info, query, or fragment")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func unknownRechargeYield() IntelligenceRechargeYield {
	return IntelligenceRechargeYield{
		Accuracy:   contracts.UpstreamEvidenceUnknown,
		ReasonCode: "recharge_yield_not_exposed",
	}
}

func (c *IntelligenceClient) Collect(ctx context.Context) IntelligenceSnapshot {
	snapshot := IntelligenceSnapshot{
		Coverage:      contracts.UpstreamCoverageUnavailable,
		RechargeYield: unknownRechargeYield(),
		Endpoints: []IntelligenceEndpointState{
			{Endpoint: IntelligenceEndpointProfile},
			{Endpoint: IntelligenceEndpointGroups},
			{Endpoint: IntelligenceEndpointRates},
			{Endpoint: IntelligenceEndpointChannels},
		},
	}

	succeeded := 0
	if raw, err := c.get(ctx, IntelligenceProfilePath); err != nil {
		setIntelligenceEndpointError(&snapshot.Endpoints[0], err)
	} else if wallet, err := ParseIntelligenceProfile(raw); err != nil {
		setIntelligenceEndpointError(&snapshot.Endpoints[0], err)
	} else {
		snapshot.Wallet = &wallet
		snapshot.Endpoints[0].Available = true
		succeeded++
	}

	groupsAvailable := false
	if raw, err := c.get(ctx, IntelligenceGroupsPath); err != nil {
		setIntelligenceEndpointError(&snapshot.Endpoints[1], err)
	} else if groups, err := ParseIntelligenceGroups(raw); err != nil {
		setIntelligenceEndpointError(&snapshot.Endpoints[1], err)
	} else {
		snapshot.Groups = groups
		groupsAvailable = true
		snapshot.Endpoints[1].Available = true
		succeeded++
	}

	var rates map[int64]contracts.CanonicalDecimal
	ratesAvailable := false
	if raw, err := c.get(ctx, IntelligenceRatesPath); err != nil {
		setIntelligenceEndpointError(&snapshot.Endpoints[2], err)
	} else if parsedRates, err := ParseIntelligenceRates(raw); err != nil {
		setIntelligenceEndpointError(&snapshot.Endpoints[2], err)
	} else {
		rates = parsedRates
		ratesAvailable = true
		snapshot.Endpoints[2].Available = true
		succeeded++
	}

	if raw, err := c.get(ctx, IntelligenceChannelsPath); err != nil {
		setIntelligenceEndpointError(&snapshot.Endpoints[3], err)
	} else if channels, err := ParseIntelligenceChannels(raw); err != nil {
		setIntelligenceEndpointError(&snapshot.Endpoints[3], err)
	} else {
		snapshot.Channels = channels
		snapshot.Endpoints[3].Available = true
		succeeded++
	}

	if groupsAvailable && ratesAvailable {
		for index := range snapshot.Groups {
			group := &snapshot.Groups[index]
			if rate, ok := rates[group.ID]; ok {
				value := rate
				group.EffectiveRate = &value
				group.EffectiveSource = "user_override"
				continue
			}
			value := group.DefaultRate
			group.EffectiveRate = &value
			group.EffectiveSource = "group_default"
		}
	}

	if err := validateIntelligenceSnapshotConsistency(snapshot, rates, groupsAvailable, ratesAvailable); err != nil {
		// Cross-endpoint disagreement makes the catalog unsafe to treat as a
		// complete snapshot. Keep independently valid evidence, but mark the
		// affected catalog endpoints as schema-unsupported so callers cannot use
		// it for absence/deletion or comparable-cost decisions.
		for index := range snapshot.Endpoints {
			if snapshot.Endpoints[index].Endpoint == IntelligenceEndpointGroups ||
				snapshot.Endpoints[index].Endpoint == IntelligenceEndpointRates ||
				snapshot.Endpoints[index].Endpoint == IntelligenceEndpointChannels {
				snapshot.Endpoints[index].Available = false
				snapshot.Endpoints[index].ErrorCode = IntelligenceSchemaUnsupported
				snapshot.Endpoints[index].Retryable = false
			}
		}
		succeeded = 0
		for _, endpoint := range snapshot.Endpoints {
			if endpoint.Available {
				succeeded++
			}
		}
	}

	switch succeeded {
	case 0:
		snapshot.Coverage = contracts.UpstreamCoverageUnavailable
	case len(snapshot.Endpoints):
		snapshot.Coverage = contracts.UpstreamCoverageComplete
	default:
		snapshot.Coverage = contracts.UpstreamCoveragePartial
	}
	return snapshot
}

func validateIntelligenceSnapshotConsistency(snapshot IntelligenceSnapshot, rates map[int64]contracts.CanonicalDecimal, groupsAvailable, ratesAvailable bool) error {
	groupIDs := make(map[int64]struct{}, len(snapshot.Groups))
	for _, group := range snapshot.Groups {
		groupIDs[group.ID] = struct{}{}
	}
	if ratesAvailable {
		// Every returned override must identify an advertised group. In
		// particular, a non-empty rates response paired with an empty group list
		// is inconsistent rather than a complete empty catalog.
		for groupID := range rates {
			if _, exists := groupIDs[groupID]; !exists {
				return schemaUnsupported()
			}
		}
		if groupsAvailable {
			for _, group := range snapshot.Groups {
				if group.EffectiveRate == nil {
					return schemaUnsupported()
				}
			}
		}
	}

	seenChannels := make(map[string]struct{}, len(snapshot.Channels))
	for _, channel := range snapshot.Channels {
		channelKey := strings.ToLower(channel.Name)
		if _, duplicate := seenChannels[channelKey]; duplicate {
			return schemaUnsupported()
		}
		seenChannels[channelKey] = struct{}{}
		seenPlatforms := make(map[string]struct{}, len(channel.Platforms))
		for _, platform := range channel.Platforms {
			platformKey := strings.ToLower(platform.Platform)
			if _, duplicate := seenPlatforms[platformKey]; duplicate {
				return schemaUnsupported()
			}
			seenPlatforms[platformKey] = struct{}{}
			for _, group := range platform.Groups {
				if groupsAvailable {
					if _, exists := groupIDs[group.ID]; !exists {
						return schemaUnsupported()
					}
				}
			}
			for _, model := range platform.Models {
				if !validIntelligencePricingIntervals(model.ReferencePricing) || !validIntelligencePricingIntervals(model.SitePricing) {
					return schemaUnsupported()
				}
			}
		}
	}
	return nil
}

func validIntelligencePricingIntervals(pricing *IntelligencePricing) bool {
	if pricing == nil {
		return true
	}
	var previousMax *int64
	for index, interval := range pricing.Intervals {
		if index > 0 {
			if previousMax == nil || interval.MinTokens < *previousMax {
				return false
			}
		}
		previousMax = interval.MaxTokens
	}
	return true
}

func setIntelligenceEndpointError(state *IntelligenceEndpointState, err error) {
	var classified *IntelligenceError
	if errors.As(err, &classified) {
		state.ErrorCode = classified.Code
		state.Retryable = classified.Retryable
		return
	}
	state.ErrorCode = IntelligenceUpstreamUnavailable
	state.Retryable = true
}

func (c *IntelligenceClient) get(ctx context.Context, path string) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.endpointTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.apiBaseURL+path, nil)
	if err != nil {
		return nil, intelligenceError(IntelligenceUpstreamUnavailable, true)
	}
	req.Header.Set("Accept", "application/json")
	if err := c.authorize(req); err != nil {
		return nil, intelligenceError(IntelligenceAuthFailed, false)
	}
	if req.Header.Get("Cookie") != "" {
		return nil, intelligenceError(IntelligenceAuthFailed, false)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, intelligenceError(IntelligenceUpstreamUnavailable, true)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, classifyIntelligenceStatus(resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, intelligenceError(IntelligenceUpstreamUnavailable, true)
	}
	if int64(len(raw)) > c.maxResponseBytes {
		return nil, intelligenceError(IntelligenceResponseTooLarge, false)
	}
	return raw, nil
}

func classifyIntelligenceStatus(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return intelligenceError(IntelligenceAuthFailed, false)
	case status == http.StatusTooManyRequests:
		return intelligenceError(IntelligenceRateLimited, true)
	case status == http.StatusRequestTimeout || status >= http.StatusInternalServerError:
		return intelligenceError(IntelligenceUpstreamUnavailable, true)
	default:
		return intelligenceError(IntelligenceSchemaUnsupported, false)
	}
}

func ParseIntelligenceProfile(raw []byte) (IntelligenceWallet, error) {
	data, err := decodeIntelligenceEnvelope(raw)
	if err != nil {
		return IntelligenceWallet{}, err
	}
	object, ok := data.(map[string]any)
	if !ok {
		return IntelligenceWallet{}, schemaUnsupported()
	}
	balance, err := requiredCanonicalDecimal(object, "balance", false)
	if err != nil {
		return IntelligenceWallet{}, err
	}
	return IntelligenceWallet{Balance: balance, UnitKind: contracts.UpstreamWalletCredit}, nil
}

func ParseIntelligenceGroups(raw []byte) ([]IntelligenceGroup, error) {
	data, err := decodeIntelligenceEnvelope(raw)
	if err != nil {
		return nil, err
	}
	items, ok := data.([]any)
	if !ok || len(items) > maxIntelligenceGroups {
		return nil, schemaUnsupported()
	}
	groups := make([]IntelligenceGroup, 0, len(items))
	seen := make(map[int64]struct{}, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, schemaUnsupported()
		}
		id, err := requiredPositiveInteger(object, "id")
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, schemaUnsupported()
		}
		seen[id] = struct{}{}
		name, err := requiredIntelligenceString(object, "name", maxIntelligenceStringBytes)
		if err != nil {
			return nil, err
		}
		platform, err := requiredIntelligenceString(object, "platform", maxIntelligenceStringBytes)
		if err != nil {
			return nil, err
		}
		rate, err := requiredCanonicalDecimal(object, "rate_multiplier", false)
		if err != nil {
			return nil, err
		}
		groups = append(groups, IntelligenceGroup{ID: id, Name: name, Platform: platform, DefaultRate: rate})
	}
	return groups, nil
}

func ParseIntelligenceRates(raw []byte) (map[int64]contracts.CanonicalDecimal, error) {
	data, err := decodeIntelligenceEnvelope(raw)
	if err != nil {
		return nil, err
	}
	// The verified handler can encode a nil repository result as JSON null. In
	// that state no override rows exist, so the group default remains effective.
	if data == nil {
		return map[int64]contracts.CanonicalDecimal{}, nil
	}
	object, ok := data.(map[string]any)
	if !ok || len(object) > maxIntelligenceRates {
		return nil, schemaUnsupported()
	}
	rates := make(map[int64]contracts.CanonicalDecimal, len(object))
	for rawID, rawRate := range object {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != rawID {
			return nil, schemaUnsupported()
		}
		rate, err := canonicalDecimalValue(rawRate, false)
		if err != nil {
			return nil, err
		}
		rates[id] = rate
	}
	return rates, nil
}

func ParseIntelligenceChannels(raw []byte) ([]IntelligenceChannel, error) {
	data, err := decodeIntelligenceEnvelope(raw)
	if err != nil {
		return nil, err
	}
	items, ok := data.([]any)
	if !ok || len(items) > maxIntelligenceChannels {
		return nil, schemaUnsupported()
	}
	channels := make([]IntelligenceChannel, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, schemaUnsupported()
		}
		name, err := requiredIntelligenceString(object, "name", maxIntelligenceStringBytes)
		if err != nil {
			return nil, err
		}
		description, err := requiredStringAllowEmpty(object, "description", maxIntelligenceStringBytes)
		if err != nil {
			return nil, err
		}
		rawPlatforms, ok := object["platforms"].([]any)
		if !ok || len(rawPlatforms) > maxIntelligencePlatforms {
			return nil, schemaUnsupported()
		}
		platforms := make([]IntelligenceChannelPlatform, 0, len(rawPlatforms))
		seenPlatforms := make(map[string]struct{}, len(rawPlatforms))
		for _, rawPlatform := range rawPlatforms {
			platform, err := parseIntelligencePlatform(rawPlatform)
			if err != nil {
				return nil, err
			}
			if _, duplicate := seenPlatforms[platform.Platform]; duplicate {
				return nil, schemaUnsupported()
			}
			seenPlatforms[platform.Platform] = struct{}{}
			platforms = append(platforms, platform)
		}
		channels = append(channels, IntelligenceChannel{Name: name, Description: description, Platforms: platforms})
	}
	return channels, nil
}

func parseIntelligencePlatform(raw any) (IntelligenceChannelPlatform, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return IntelligenceChannelPlatform{}, schemaUnsupported()
	}
	platform, err := requiredIntelligenceString(object, "platform", maxIntelligenceStringBytes)
	if err != nil {
		return IntelligenceChannelPlatform{}, err
	}
	rawGroups, ok := object["groups"].([]any)
	if !ok || len(rawGroups) > maxIntelligenceGroupsPerPlatform {
		return IntelligenceChannelPlatform{}, schemaUnsupported()
	}
	groups := make([]IntelligenceChannelGroup, 0, len(rawGroups))
	seenGroups := make(map[int64]struct{}, len(rawGroups))
	for _, rawGroup := range rawGroups {
		groupObject, ok := rawGroup.(map[string]any)
		if !ok {
			return IntelligenceChannelPlatform{}, schemaUnsupported()
		}
		id, err := requiredPositiveInteger(groupObject, "id")
		if err != nil {
			return IntelligenceChannelPlatform{}, err
		}
		if _, duplicate := seenGroups[id]; duplicate {
			return IntelligenceChannelPlatform{}, schemaUnsupported()
		}
		seenGroups[id] = struct{}{}
		name, err := requiredIntelligenceString(groupObject, "name", maxIntelligenceStringBytes)
		if err != nil {
			return IntelligenceChannelPlatform{}, err
		}
		rate, err := requiredCanonicalDecimal(groupObject, "rate_multiplier", false)
		if err != nil {
			return IntelligenceChannelPlatform{}, err
		}
		groups = append(groups, IntelligenceChannelGroup{ID: id, Name: name, DefaultRate: rate})
	}
	rawModels, ok := object["supported_models"].([]any)
	if !ok || len(rawModels) > maxIntelligenceModelsPerPlatform {
		return IntelligenceChannelPlatform{}, schemaUnsupported()
	}
	models := make([]IntelligenceModel, 0, len(rawModels))
	seenModels := make(map[string]struct{}, len(rawModels))
	for _, rawModel := range rawModels {
		modelObject, ok := rawModel.(map[string]any)
		if !ok {
			return IntelligenceChannelPlatform{}, schemaUnsupported()
		}
		name, err := requiredIntelligenceString(modelObject, "name", maxIntelligenceStringBytes)
		if err != nil {
			return IntelligenceChannelPlatform{}, err
		}
		modelPlatform, err := requiredIntelligenceString(modelObject, "platform", maxIntelligenceStringBytes)
		if err != nil || modelPlatform != platform {
			return IntelligenceChannelPlatform{}, schemaUnsupported()
		}
		key := strings.ToLower(name)
		if _, duplicate := seenModels[key]; duplicate {
			return IntelligenceChannelPlatform{}, schemaUnsupported()
		}
		seenModels[key] = struct{}{}
		reference, err := optionalIntelligencePricing(modelObject, "pricing")
		if err != nil {
			return IntelligenceChannelPlatform{}, err
		}
		_, sitePricingDeclared := modelObject["site_pricing"]
		site, err := optionalIntelligencePricing(modelObject, "site_pricing")
		if err != nil {
			return IntelligenceChannelPlatform{}, err
		}
		// Sub2API v0.1.164 exposes the channel's configured, user-visible
		// settlement price only as `pricing`; it does not serialize the newer
		// `site_pricing` field. Preserve an explicitly declared site_pricing
		// value (including null) as authoritative, and use the already strictly
		// parsed pricing object only when the field is absent. This keeps newer
		// DTOs unambiguous while allowing the pinned v0.1.164 contract to produce
		// published offers without weakening decimal or shape validation.
		if !sitePricingDeclared {
			site = cloneIntelligencePricing(reference)
		}
		models = append(models, IntelligenceModel{
			Name: name, Platform: modelPlatform, ReferencePricing: reference, SitePricing: site,
		})
	}
	return IntelligenceChannelPlatform{Platform: platform, Groups: groups, Models: models}, nil
}

func cloneIntelligencePricing(input *IntelligencePricing) *IntelligencePricing {
	if input == nil {
		return nil
	}
	cloned := *input
	cloned.InputPrice = cloneIntelligenceDecimal(input.InputPrice)
	cloned.OutputPrice = cloneIntelligenceDecimal(input.OutputPrice)
	cloned.CacheWritePrice = cloneIntelligenceDecimal(input.CacheWritePrice)
	cloned.CacheReadPrice = cloneIntelligenceDecimal(input.CacheReadPrice)
	cloned.ImageOutputPrice = cloneIntelligenceDecimal(input.ImageOutputPrice)
	cloned.PerRequestPrice = cloneIntelligenceDecimal(input.PerRequestPrice)
	cloned.Intervals = make([]IntelligencePricingInterval, len(input.Intervals))
	for index := range input.Intervals {
		cloned.Intervals[index] = input.Intervals[index]
		cloned.Intervals[index].MaxTokens = cloneIntelligenceInt64(input.Intervals[index].MaxTokens)
		cloned.Intervals[index].InputPrice = cloneIntelligenceDecimal(input.Intervals[index].InputPrice)
		cloned.Intervals[index].OutputPrice = cloneIntelligenceDecimal(input.Intervals[index].OutputPrice)
		cloned.Intervals[index].CacheWritePrice = cloneIntelligenceDecimal(input.Intervals[index].CacheWritePrice)
		cloned.Intervals[index].CacheReadPrice = cloneIntelligenceDecimal(input.Intervals[index].CacheReadPrice)
		cloned.Intervals[index].PerRequestPrice = cloneIntelligenceDecimal(input.Intervals[index].PerRequestPrice)
	}
	return &cloned
}

func cloneIntelligenceDecimal(input *contracts.CanonicalDecimal) *contracts.CanonicalDecimal {
	if input == nil {
		return nil
	}
	value := *input
	return &value
}

func cloneIntelligenceInt64(input *int64) *int64 {
	if input == nil {
		return nil
	}
	value := *input
	return &value
}

func optionalIntelligencePricing(object map[string]any, key string) (*IntelligencePricing, error) {
	raw, exists := object[key]
	if !exists || raw == nil {
		return nil, nil
	}
	pricingObject, ok := raw.(map[string]any)
	if !ok {
		return nil, schemaUnsupported()
	}
	billingMode, err := requiredIntelligenceString(pricingObject, "billing_mode", 32)
	if err != nil {
		return nil, err
	}
	switch billingMode {
	case "token", "per_request", "image", "video":
	default:
		return nil, schemaUnsupported()
	}
	pricing := &IntelligencePricing{BillingMode: billingMode, PerTokens: 1}
	if pricing.InputPrice, err = optionalCanonicalDecimal(pricingObject, "input_price", false); err != nil {
		return nil, err
	}
	if pricing.OutputPrice, err = optionalCanonicalDecimal(pricingObject, "output_price", false); err != nil {
		return nil, err
	}
	if pricing.CacheWritePrice, err = optionalCanonicalDecimal(pricingObject, "cache_write_price", false); err != nil {
		return nil, err
	}
	if pricing.CacheReadPrice, err = optionalCanonicalDecimal(pricingObject, "cache_read_price", false); err != nil {
		return nil, err
	}
	if pricing.ImageOutputPrice, err = optionalCanonicalDecimal(pricingObject, "image_output_price", false); err != nil {
		return nil, err
	}
	if pricing.PerRequestPrice, err = optionalCanonicalDecimal(pricingObject, "per_request_price", false); err != nil {
		return nil, err
	}
	rawIntervals, ok := pricingObject["intervals"].([]any)
	if !ok || len(rawIntervals) > maxIntelligencePricingIntervals {
		return nil, schemaUnsupported()
	}
	pricing.Intervals = make([]IntelligencePricingInterval, 0, len(rawIntervals))
	for _, rawInterval := range rawIntervals {
		interval, err := parseIntelligencePricingInterval(rawInterval)
		if err != nil {
			return nil, err
		}
		pricing.Intervals = append(pricing.Intervals, interval)
	}
	return pricing, nil
}

func parseIntelligencePricingInterval(raw any) (IntelligencePricingInterval, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return IntelligencePricingInterval{}, schemaUnsupported()
	}
	minTokens, err := requiredNonNegativeInteger(object, "min_tokens")
	if err != nil {
		return IntelligencePricingInterval{}, err
	}
	maxTokens, err := optionalPositiveInteger(object, "max_tokens")
	if err != nil || (maxTokens != nil && *maxTokens <= minTokens) {
		return IntelligencePricingInterval{}, schemaUnsupported()
	}
	tier, err := optionalStringAllowEmpty(object, "tier_label", maxIntelligenceStringBytes)
	if err != nil {
		return IntelligencePricingInterval{}, err
	}
	interval := IntelligencePricingInterval{MinTokens: minTokens, MaxTokens: maxTokens, TierLabel: tier}
	if interval.InputPrice, err = optionalCanonicalDecimal(object, "input_price", false); err != nil {
		return IntelligencePricingInterval{}, err
	}
	if interval.OutputPrice, err = optionalCanonicalDecimal(object, "output_price", false); err != nil {
		return IntelligencePricingInterval{}, err
	}
	if interval.CacheWritePrice, err = optionalCanonicalDecimal(object, "cache_write_price", false); err != nil {
		return IntelligencePricingInterval{}, err
	}
	if interval.CacheReadPrice, err = optionalCanonicalDecimal(object, "cache_read_price", false); err != nil {
		return IntelligencePricingInterval{}, err
	}
	if interval.PerRequestPrice, err = optionalCanonicalDecimal(object, "per_request_price", false); err != nil {
		return IntelligencePricingInterval{}, err
	}
	return interval, nil
}

func decodeIntelligenceEnvelope(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, schemaUnsupported()
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, schemaUnsupported()
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, schemaUnsupported()
	}
	rawCode, exists := object["code"]
	if !exists {
		return nil, schemaUnsupported()
	}
	codeNumber, ok := rawCode.(json.Number)
	if !ok {
		return nil, schemaUnsupported()
	}
	code, err := strconv.Atoi(codeNumber.String())
	if err != nil {
		return nil, schemaUnsupported()
	}
	if code != 0 {
		return nil, classifyIntelligenceStatus(code)
	}
	data, exists := object["data"]
	if !exists {
		return nil, schemaUnsupported()
	}
	return data, nil
}

func requiredPositiveInteger(object map[string]any, key string) (int64, error) {
	value, err := requiredInteger(object, key)
	if err != nil || value <= 0 {
		return 0, schemaUnsupported()
	}
	return value, nil
}

func requiredNonNegativeInteger(object map[string]any, key string) (int64, error) {
	value, err := requiredInteger(object, key)
	if err != nil || value < 0 {
		return 0, schemaUnsupported()
	}
	return value, nil
}

func requiredInteger(object map[string]any, key string) (int64, error) {
	raw, exists := object[key]
	if !exists {
		return 0, schemaUnsupported()
	}
	number, ok := raw.(json.Number)
	if !ok {
		return 0, schemaUnsupported()
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil {
		return 0, schemaUnsupported()
	}
	return value, nil
}

func optionalPositiveInteger(object map[string]any, key string) (*int64, error) {
	raw, exists := object[key]
	if !exists || raw == nil {
		return nil, nil
	}
	number, ok := raw.(json.Number)
	if !ok {
		return nil, schemaUnsupported()
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil || value <= 0 {
		return nil, schemaUnsupported()
	}
	return &value, nil
}

func requiredIntelligenceString(object map[string]any, key string, maxBytes int) (string, error) {
	value, err := requiredStringAllowEmpty(object, key, maxBytes)
	if err != nil || value == "" {
		return "", schemaUnsupported()
	}
	return value, nil
}

func requiredStringAllowEmpty(object map[string]any, key string, maxBytes int) (string, error) {
	raw, exists := object[key]
	if !exists {
		return "", schemaUnsupported()
	}
	value, ok := raw.(string)
	if !ok || !validIntelligenceString(value, maxBytes) {
		return "", schemaUnsupported()
	}
	return value, nil
}

func optionalStringAllowEmpty(object map[string]any, key string, maxBytes int) (string, error) {
	raw, exists := object[key]
	if !exists {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok || !validIntelligenceString(value, maxBytes) {
		return "", schemaUnsupported()
	}
	return value, nil
}

func validIntelligenceString(value string, maxBytes int) bool {
	if len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}

func requiredCanonicalDecimal(object map[string]any, key string, allowNegative bool) (contracts.CanonicalDecimal, error) {
	raw, exists := object[key]
	if !exists {
		return "", schemaUnsupported()
	}
	return canonicalDecimalValue(raw, allowNegative)
}

func optionalCanonicalDecimal(object map[string]any, key string, allowNegative bool) (*contracts.CanonicalDecimal, error) {
	raw, exists := object[key]
	if !exists || raw == nil {
		return nil, nil
	}
	value, err := canonicalDecimalValue(raw, allowNegative)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func canonicalDecimalValue(raw any, allowNegative bool) (contracts.CanonicalDecimal, error) {
	number, ok := raw.(json.Number)
	if !ok {
		return "", schemaUnsupported()
	}
	plain, err := canonicalizeJSONNumber(number.String())
	if err != nil {
		return "", schemaUnsupported()
	}
	value, err := contracts.ParseCanonicalDecimal(plain)
	if err != nil {
		return "", schemaUnsupported()
	}
	if !allowNegative && strings.HasPrefix(string(value), "-") {
		return "", schemaUnsupported()
	}
	return value, nil
}

// canonicalizeJSONNumber converts an exact JSON number token, including an
// exponent form, into the contracts plain-decimal representation without a
// float64 round trip or rounding.
func canonicalizeJSONNumber(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty JSON number")
	}
	negative := raw[0] == '-'
	unsigned := raw
	if negative {
		unsigned = raw[1:]
	}
	mantissa := unsigned
	exponent := int64(0)
	if index := strings.IndexAny(unsigned, "eE"); index >= 0 {
		mantissa = unsigned[:index]
		exponentText := unsigned[index+1:]
		parsed, err := strconv.ParseInt(exponentText, 10, 32)
		if err != nil || parsed < -1_000 || parsed > 1_000 {
			return "", errors.New("JSON exponent is out of bounds")
		}
		exponent = parsed
	}
	whole, fractional, hasPoint := strings.Cut(mantissa, ".")
	if whole == "" || (hasPoint && fractional == "") || !allDecimalDigits(whole) || (hasPoint && !allDecimalDigits(fractional)) {
		return "", errors.New("invalid JSON number")
	}
	digits := whole + fractional
	decimalAt := int64(len(whole)) + exponent
	if decimalAt < -1_000 || decimalAt > 1_000 {
		return "", errors.New("JSON number is out of bounds")
	}
	var plain string
	switch {
	case decimalAt <= 0:
		plain = "0." + strings.Repeat("0", int(-decimalAt)) + digits
	case decimalAt >= int64(len(digits)):
		plain = digits + strings.Repeat("0", int(decimalAt-int64(len(digits))))
	default:
		plain = digits[:decimalAt] + "." + digits[decimalAt:]
	}
	parts := strings.SplitN(plain, ".", 2)
	parts[0] = strings.TrimLeft(parts[0], "0")
	if parts[0] == "" {
		parts[0] = "0"
	}
	if len(parts) == 2 {
		parts[1] = strings.TrimRight(parts[1], "0")
		if parts[1] != "" {
			plain = parts[0] + "." + parts[1]
		} else {
			plain = parts[0]
		}
	} else {
		plain = parts[0]
	}
	if negative {
		if plain == "0" {
			return "", errors.New("negative zero is not canonical")
		}
		plain = "-" + plain
	}
	return plain, nil
}

func allDecimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
