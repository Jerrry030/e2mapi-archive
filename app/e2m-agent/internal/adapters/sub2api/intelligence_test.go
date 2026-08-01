package sub2api

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestIntelligenceFixtureParsersPreserveExactNumbers(t *testing.T) {
	wallet, err := ParseIntelligenceProfile(readIntelligenceFixture(t, "profile.json"))
	if err != nil || wallet.Balance != "18.25" || wallet.UnitKind != contracts.UpstreamWalletCredit {
		t.Fatalf("profile = %+v, %v", wallet, err)
	}

	groups, err := ParseIntelligenceGroups(readIntelligenceFixture(t, "groups.json"))
	if err != nil || len(groups) != 2 || groups[0].ID != 3 || groups[0].DefaultRate != "0.8" {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	rates, err := ParseIntelligenceRates(readIntelligenceFixture(t, "rates.json"))
	if err != nil || rates[3] != "0.75" {
		t.Fatalf("rates = %+v, %v", rates, err)
	}

	channels, err := ParseIntelligenceChannels(readIntelligenceFixture(t, "channels.json"))
	if err != nil || len(channels) != 1 || len(channels[0].Platforms) != 1 || len(channels[0].Platforms[0].Models) != 1 {
		t.Fatalf("channels = %+v, %v", channels, err)
	}
	model := channels[0].Platforms[0].Models[0]
	if model.ReferencePricing == nil || model.ReferencePricing.InputPrice == nil || *model.ReferencePricing.InputPrice != "0.000003" ||
		model.ReferencePricing.CacheReadPrice == nil || *model.ReferencePricing.CacheReadPrice != "0.0000003" ||
		model.SitePricing == nil || model.SitePricing.InputPrice == nil || *model.SitePricing.InputPrice != "0.0000025" ||
		model.ReferencePricing.PerTokens != 1 || len(model.SitePricing.Intervals) != 1 {
		t.Fatalf("pricing = %+v", model)
	}
}

func TestParseIntelligenceChannelsFallsBackForSub2APIV01164PricingDTO(t *testing.T) {
	channels, err := ParseIntelligenceChannels(readIntelligenceFixture(t, "channels_v0_1_164.json"))
	if err != nil || len(channels) != 1 || len(channels[0].Platforms) != 1 || len(channels[0].Platforms[0].Models) != 1 {
		t.Fatalf("v0.1.164 channels = %+v, %v", channels, err)
	}
	model := channels[0].Platforms[0].Models[0]
	if model.Name != "claude-sonnet-4" || model.Platform != "anthropic" ||
		model.ReferencePricing == nil || model.SitePricing == nil {
		t.Fatalf("v0.1.164 model pricing = %+v", model)
	}
	if model.ReferencePricing == model.SitePricing {
		t.Fatal("v0.1.164 fallback aliased reference and published pricing")
	}
	for label, pricing := range map[string]*IntelligencePricing{
		"reference": model.ReferencePricing,
		"published": model.SitePricing,
	} {
		if pricing.BillingMode != "token" || pricing.PerTokens != 1 ||
			pricing.InputPrice == nil || *pricing.InputPrice != "0.0000028" ||
			pricing.OutputPrice == nil || *pricing.OutputPrice != "0.000014" ||
			pricing.CacheReadPrice == nil || *pricing.CacheReadPrice != "0.00000028" ||
			pricing.CacheWritePrice != nil || pricing.PerRequestPrice != nil || len(pricing.Intervals) != 0 {
			t.Fatalf("%s pricing = %+v", label, pricing)
		}
	}
}

func TestParseIntelligenceChannelsPrefersDeclaredSitePricing(t *testing.T) {
	channels, err := ParseIntelligenceChannels(readIntelligenceFixture(t, "channels.json"))
	if err != nil {
		t.Fatalf("parse channels with declared site pricing: %v", err)
	}
	model := channels[0].Platforms[0].Models[0]
	if model.ReferencePricing == nil || model.SitePricing == nil ||
		model.ReferencePricing.InputPrice == nil || *model.ReferencePricing.InputPrice != "0.000003" ||
		model.SitePricing.InputPrice == nil || *model.SitePricing.InputPrice != "0.0000025" {
		t.Fatalf("declared site pricing did not remain authoritative: %+v", model)
	}

	explicitNull := []byte(`{"code":0,"data":[{"name":"Primary","description":"","platforms":[{"platform":"anthropic","groups":[],"supported_models":[{"name":"claude-sonnet-4","platform":"anthropic","pricing":{"billing_mode":"token","input_price":0.000003,"output_price":null,"cache_write_price":null,"cache_read_price":null,"image_output_price":null,"per_request_price":null,"intervals":[]},"site_pricing":null}]}]}]}`)
	channels, err = ParseIntelligenceChannels(explicitNull)
	if err != nil {
		t.Fatalf("parse explicit null site pricing: %v", err)
	}
	model = channels[0].Platforms[0].Models[0]
	if model.ReferencePricing == nil || model.SitePricing != nil {
		t.Fatalf("explicit site_pricing null was silently reinterpreted: %+v", model)
	}
}

func TestIntelligenceParsersRejectWrongTypesAndDoNotGuessAliases(t *testing.T) {
	for name, parse := range map[string]func([]byte) error{
		"profile string balance": func(raw []byte) error { _, err := ParseIntelligenceProfile(raw); return err },
		"profile alias":          func(raw []byte) error { _, err := ParseIntelligenceProfile(raw); return err },
		"groups string rate":     func(raw []byte) error { _, err := ParseIntelligenceGroups(raw); return err },
		"rates list":             func(raw []byte) error { _, err := ParseIntelligenceRates(raw); return err },
		"channels price string":  func(raw []byte) error { _, err := ParseIntelligenceChannels(raw); return err },
	} {
		var raw []byte
		switch name {
		case "profile string balance":
			raw = []byte(`{"code":0,"data":{"balance":"18.25"}}`)
		case "profile alias":
			raw = []byte(`{"code":0,"data":{"credit_balance":18.25}}`)
		case "groups string rate":
			raw = []byte(`{"code":0,"data":[{"id":3,"name":"Claude","platform":"anthropic","rate_multiplier":"0.8"}]}`)
		case "rates list":
			raw = []byte(`{"code":0,"data":[{"group_id":3,"rate_multiplier":0.8}]}`)
		case "channels price string":
			raw = readIntelligenceFixture(t, "channels_wrong_type.json")
		}
		t.Run(name, func(t *testing.T) {
			assertIntelligenceError(t, parse(raw), IntelligenceSchemaUnsupported, false)
		})
	}
}

func TestIntelligenceParsersRejectNonFiniteInvalidAndOversizedDecimal(t *testing.T) {
	inputs := []string{
		`{"code":0,"data":{"balance":-1}}`,
		`{"code":0,"data":{"balance":1e1001}}`,
		`{"code":0,"data":{"balance":0.0000000000000000001}}`,
		`{"code":0,"data":{"balance":999999999999999999999}}`,
		`{"code":0,"data":{"balance":NaN}}`,
		`{"code":0,"data":{"balance":18.25}} {}`,
	}
	for _, input := range inputs {
		_, err := ParseIntelligenceProfile([]byte(input))
		assertIntelligenceError(t, err, IntelligenceSchemaUnsupported, false)
	}

	if got, err := canonicalizeJSONNumber("3e-7"); err != nil || got != "0.0000003" {
		t.Fatalf("canonical exponent = %q, %v", got, err)
	}
	if got, err := canonicalizeJSONNumber("18.2500"); err != nil || got != "18.25" {
		t.Fatalf("canonical trailing zeros = %q, %v", got, err)
	}
}

func TestIntelligenceParsersApplyCollectionBounds(t *testing.T) {
	groups := strings.Repeat(`{"id":1,"name":"g","platform":"p","rate_multiplier":1},`, maxIntelligenceGroups+1)
	groups = strings.TrimSuffix(groups, ",")
	_, err := ParseIntelligenceGroups([]byte(`{"code":0,"data":[` + groups + `]}`))
	assertIntelligenceError(t, err, IntelligenceSchemaUnsupported, false)
}

func TestIntelligenceClientCollectsStandardUserPathsAndMergesRates(t *testing.T) {
	fixtures := map[string][]byte{
		IntelligenceProfilePath:  readIntelligenceFixture(t, "profile.json"),
		IntelligenceGroupsPath:   readIntelligenceFixture(t, "groups.json"),
		IntelligenceRatesPath:    readIntelligenceFixture(t, "rates.json"),
		IntelligenceChannelsPath: readIntelligenceFixture(t, "channels.json"),
	}
	var mu sync.Mutex
	seen := make([]string, 0, len(fixtures))
	client, err := NewIntelligenceClient(IntelligenceClientConfig{
		BaseURL: "https://supplier.example/api/v1/",
		HTTPClient: &http.Client{Transport: intelligenceRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			mu.Lock()
			seen = append(seen, req.URL.Path)
			mu.Unlock()
			if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer user-token" || req.Header.Get("Cookie") != "" {
				t.Fatalf("unsafe request: %s headers=%v", req.Method, req.Header)
			}
			body, ok := fixtures[strings.TrimPrefix(req.URL.Path, "/api/v1")]
			if !ok {
				t.Fatalf("unexpected path: %s", req.URL.Path)
			}
			return intelligenceHTTPResponse(req, http.StatusOK, body), nil
		})},
		Authorize: func(req *http.Request) error {
			req.Header.Set("Authorization", "Bearer user-token")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	snapshot := client.Collect(context.Background())
	if snapshot.Coverage != contracts.UpstreamCoverageComplete || snapshot.Wallet == nil || len(snapshot.Groups) != 2 || len(snapshot.Channels) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Groups[0].EffectiveRate == nil || *snapshot.Groups[0].EffectiveRate != "0.75" || snapshot.Groups[0].EffectiveSource != "user_override" ||
		snapshot.Groups[1].EffectiveRate == nil || *snapshot.Groups[1].EffectiveRate != "1" || snapshot.Groups[1].EffectiveSource != "group_default" {
		t.Fatalf("merged groups = %+v", snapshot.Groups)
	}
	if snapshot.RechargeYield.Value != nil || snapshot.RechargeYield.Accuracy != contracts.UpstreamEvidenceUnknown || snapshot.RechargeYield.ReasonCode != "recharge_yield_not_exposed" {
		t.Fatalf("recharge yield must remain unknown: %+v", snapshot.RechargeYield)
	}
	want := []string{
		"/api/v1" + IntelligenceProfilePath,
		"/api/v1" + IntelligenceGroupsPath,
		"/api/v1" + IntelligenceRatesPath,
		"/api/v1" + IntelligenceChannelsPath,
	}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("paths = %v, want %v", seen, want)
	}
}

func TestIntelligenceClientKeepsPartialEndpointEvidence(t *testing.T) {
	client, err := NewIntelligenceClient(IntelligenceClientConfig{
		BaseURL: "https://supplier.example/api/v1",
		HTTPClient: &http.Client{Transport: intelligenceRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch strings.TrimPrefix(req.URL.Path, "/api/v1") {
			case IntelligenceProfilePath:
				return intelligenceHTTPResponse(req, http.StatusOK, []byte(`{"code":0,"data":{"balance":7}}`)), nil
			case IntelligenceGroupsPath:
				return intelligenceHTTPResponse(req, http.StatusOK, []byte(`{"code":0,"data":[{"id":3,"name":"Claude","platform":"anthropic","rate_multiplier":0.8}]}`)), nil
			case IntelligenceRatesPath:
				return intelligenceHTTPResponse(req, http.StatusTooManyRequests, nil), nil
			case IntelligenceChannelsPath:
				return intelligenceHTTPResponse(req, http.StatusOK, []byte(`{"code":0,"data":"wrong"}`)), nil
			default:
				return nil, errors.New("unexpected request")
			}
		})},
		Authorize: func(*http.Request) error { return nil },
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	snapshot := client.Collect(context.Background())
	if snapshot.Coverage != contracts.UpstreamCoveragePartial || snapshot.Wallet == nil || len(snapshot.Groups) != 1 || snapshot.Groups[0].EffectiveRate != nil {
		t.Fatalf("partial snapshot = %+v", snapshot)
	}
	if snapshot.Endpoints[2].ErrorCode != IntelligenceRateLimited || !snapshot.Endpoints[2].Retryable ||
		snapshot.Endpoints[3].ErrorCode != IntelligenceSchemaUnsupported || snapshot.Endpoints[3].Retryable {
		t.Fatalf("endpoint evidence = %+v", snapshot.Endpoints)
	}
}

func TestIntelligenceClientRejectsCrossEndpointCatalogInconsistency(t *testing.T) {
	tests := map[string]map[string]string{
		"unmatched rate": {
			IntelligenceGroupsPath:   `{"code":0,"data":[{"id":3,"name":"Claude","platform":"anthropic","rate_multiplier":0.8}]}`,
			IntelligenceRatesPath:    `{"code":0,"data":{"4":0.7}}`,
			IntelligenceChannelsPath: `{"code":0,"data":[]}`,
		},
		"duplicate channel": {
			IntelligenceGroupsPath: `{"code":0,"data":[]}`,
			IntelligenceRatesPath:  `{"code":0,"data":{}}`,
			IntelligenceChannelsPath: `{"code":0,"data":[` +
				`{"name":"Primary","description":"a","platforms":[]},` +
				`{"name":"primary","description":"b","platforms":[]}]}`,
		},
		"unknown channel group": {
			IntelligenceGroupsPath:   `{"code":0,"data":[{"id":3,"name":"Claude","platform":"anthropic","rate_multiplier":0.8}]}`,
			IntelligenceRatesPath:    `{"code":0,"data":{}}`,
			IntelligenceChannelsPath: `{"code":0,"data":[{"name":"Primary","description":"a","platforms":[{"platform":"anthropic","groups":[{"id":4,"name":"Other","rate_multiplier":1}],"supported_models":[]}]}]}`,
		},
		"overlapping price tiers": {
			IntelligenceGroupsPath: `{"code":0,"data":[]}`,
			IntelligenceRatesPath:  `{"code":0,"data":{}}`,
			IntelligenceChannelsPath: `{"code":0,"data":[{"name":"Primary","description":"a","platforms":[{"platform":"anthropic","groups":[],"supported_models":[` +
				`{"name":"model","platform":"anthropic","pricing":{"billing_mode":"token","input_price":1,"output_price":null,"cache_write_price":null,"cache_read_price":null,"image_output_price":null,"per_request_price":null,"intervals":[` +
				`{"min_tokens":0,"max_tokens":100,"tier_label":"a","input_price":1,"output_price":null,"cache_write_price":null,"cache_read_price":null,"per_request_price":null},` +
				`{"min_tokens":50,"max_tokens":200,"tier_label":"b","input_price":1,"output_price":null,"cache_write_price":null,"cache_read_price":null,"per_request_price":null}]},"site_pricing":null}]}]}]}`,
		},
	}
	for name, fixtures := range tests {
		t.Run(name, func(t *testing.T) {
			client, err := NewIntelligenceClient(IntelligenceClientConfig{
				BaseURL: "https://supplier.example/api/v1",
				HTTPClient: &http.Client{Transport: intelligenceRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					path := strings.TrimPrefix(req.URL.Path, "/api/v1")
					if path == IntelligenceProfilePath {
						return intelligenceHTTPResponse(req, http.StatusOK, []byte(`{"code":0,"data":{"balance":1}}`)), nil
					}
					return intelligenceHTTPResponse(req, http.StatusOK, []byte(fixtures[path])), nil
				})},
				Authorize: func(*http.Request) error { return nil },
			})
			if err != nil {
				t.Fatalf("new client: %v", err)
			}
			snapshot := client.Collect(context.Background())
			if snapshot.Coverage != contracts.UpstreamCoveragePartial || !snapshot.Endpoints[0].Available {
				t.Fatalf("inconsistent snapshot coverage = %+v", snapshot)
			}
			for _, index := range []int{1, 2, 3} {
				if snapshot.Endpoints[index].Available || snapshot.Endpoints[index].ErrorCode != IntelligenceSchemaUnsupported {
					t.Fatalf("catalog endpoint %d was trusted: %+v", index, snapshot.Endpoints)
				}
			}
		})
	}
}

func TestIntelligenceClientClassifiesStatusSizeTimeoutAndCookie(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		wantCode  IntelligenceErrorCode
		retryable bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, wantCode: IntelligenceAuthFailed},
		{name: "forbidden", status: http.StatusForbidden, wantCode: IntelligenceAuthFailed},
		{name: "rate limited", status: http.StatusTooManyRequests, wantCode: IntelligenceRateLimited, retryable: true},
		{name: "redirect", status: http.StatusFound, wantCode: IntelligenceSchemaUnsupported},
		{name: "server", status: http.StatusBadGateway, wantCode: IntelligenceUpstreamUnavailable, retryable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newIntelligenceTestClient(t, 64, time.Second, func(req *http.Request) (*http.Response, error) {
				return intelligenceHTTPResponse(req, tc.status, nil), nil
			}, func(*http.Request) error { return nil })
			_, err := client.get(context.Background(), IntelligenceProfilePath)
			assertIntelligenceError(t, err, tc.wantCode, tc.retryable)
		})
	}

	t.Run("response too large", func(t *testing.T) {
		client := newIntelligenceTestClient(t, 8, time.Second, func(req *http.Request) (*http.Response, error) {
			return intelligenceHTTPResponse(req, http.StatusOK, []byte(`123456789`)), nil
		}, func(*http.Request) error { return nil })
		_, err := client.get(context.Background(), IntelligenceProfilePath)
		assertIntelligenceError(t, err, IntelligenceResponseTooLarge, false)
	})

	t.Run("compressed response is bounded after transparent decompression", func(t *testing.T) {
		const maxBytes = int64(1 << 10)
		decompressed := bytes.Repeat([]byte{'x'}, 64<<10)
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(decompressed); err != nil {
			t.Fatalf("compress fixture: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close compressed fixture: %v", err)
		}
		if int64(compressed.Len()) >= maxBytes || int64(len(decompressed)) <= maxBytes {
			t.Fatalf("invalid compression-bomb fixture: compressed=%d decompressed=%d limit=%d", compressed.Len(), len(decompressed), maxBytes)
		}

		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path != IntelligenceProfilePath {
				http.NotFound(response, request)
				return
			}
			response.Header().Set("Content-Encoding", "gzip")
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write(compressed.Bytes())
		}))
		defer server.Close()

		client, err := NewIntelligenceClient(IntelligenceClientConfig{
			BaseURL: server.URL, HTTPClient: server.Client(),
			Authorize: func(*http.Request) error { return nil }, MaxResponseBytes: maxBytes,
		})
		if err != nil {
			t.Fatalf("new compressed-response client: %v", err)
		}
		_, err = client.get(context.Background(), IntelligenceProfilePath)
		assertIntelligenceError(t, err, IntelligenceResponseTooLarge, false)
	})

	t.Run("endpoint timeout", func(t *testing.T) {
		client := newIntelligenceTestClient(t, 64, 5*time.Millisecond, func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}, func(*http.Request) error { return nil })
		_, err := client.get(context.Background(), IntelligenceProfilePath)
		assertIntelligenceError(t, err, IntelligenceUpstreamUnavailable, true)
	})

	t.Run("cookie rejected before network", func(t *testing.T) {
		called := false
		client := newIntelligenceTestClient(t, 64, time.Second, func(req *http.Request) (*http.Response, error) {
			called = true
			return intelligenceHTTPResponse(req, http.StatusOK, nil), nil
		}, func(req *http.Request) error { req.Header.Set("Cookie", "session=secret"); return nil })
		_, err := client.get(context.Background(), IntelligenceProfilePath)
		assertIntelligenceError(t, err, IntelligenceAuthFailed, false)
		if called {
			t.Fatal("cookie-bearing request reached transport")
		}
	})
}

func TestIntelligenceClientRequiresExplicitAuthAndSafeBaseURL(t *testing.T) {
	for _, baseURL := range []string{"", "supplier.example", "ftp://supplier.example", "https://user:pass@supplier.example", "https://supplier.example?token=secret"} {
		if _, err := NewIntelligenceClient(IntelligenceClientConfig{BaseURL: baseURL, Authorize: func(*http.Request) error { return nil }}); err == nil {
			t.Fatalf("unsafe base URL accepted: %q", baseURL)
		}
	}
	if _, err := NewIntelligenceClient(IntelligenceClientConfig{BaseURL: "https://supplier.example/api/v1"}); err == nil {
		t.Fatal("implicit authentication accepted")
	}
}

func newIntelligenceTestClient(
	t *testing.T,
	maxBytes int64,
	timeout time.Duration,
	roundTrip intelligenceRoundTripFunc,
	authorize IntelligenceAuthorizeFunc,
) *IntelligenceClient {
	t.Helper()
	client, err := NewIntelligenceClient(IntelligenceClientConfig{
		BaseURL: "https://supplier.example/api/v1", HTTPClient: &http.Client{Transport: roundTrip},
		Authorize: authorize, EndpointTimeout: timeout, MaxResponseBytes: maxBytes,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func assertIntelligenceError(t *testing.T, err error, code IntelligenceErrorCode, retryable bool) {
	t.Helper()
	var classified *IntelligenceError
	if !errors.As(err, &classified) || classified.Code != code || classified.Retryable != retryable {
		t.Fatalf("error = %#v, want %s retryable=%v", err, code, retryable)
	}
}

func readIntelligenceFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "intelligence", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

type intelligenceRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn intelligenceRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func intelligenceHTTPResponse(req *http.Request, status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Request:    req,
	}
}
