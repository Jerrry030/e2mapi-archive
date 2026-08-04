// Package pricing resolves base model prices from a LiteLLM-format table
// (model_prices_and_context_window.json) and converts them to CNY micros per
// million tokens. The embedded table is a curated bootstrap snapshot only —
// production deployments should mount a current table via
// E2M_PRICE_TABLE_PATH and review it like any other pricing input. Base-table
// pricing stays disabled unless an explicit USD→CNY rate is configured, so a
// missing rate can never silently misprice settlement.
package pricing

import (
	_ "embed"
	"encoding/json"
	"errors"
	"math"
	"os"
	"regexp"
	"strings"
)

//go:embed base_prices.json
var embeddedTable []byte

// ModelPrice is the USD per-token price pair in LiteLLM units.
type ModelPrice struct {
	InputUSDPerToken  float64 `json:"input_cost_per_token"`
	OutputUSDPerToken float64 `json:"output_cost_per_token"`
}

type Table struct {
	prices map[string]ModelPrice
}

var dateSuffix = regexp.MustCompile(`-(20\d{6}|20\d{2}-\d{2}-\d{2})$`)

// Parse reads a LiteLLM-format JSON document. Unknown fields and non-model
// bookkeeping entries (like "sample_spec") are ignored; entries without a
// positive price pair are skipped rather than treated as free.
func Parse(raw []byte) (*Table, error) {
	var entries map[string]ModelPrice
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	prices := make(map[string]ModelPrice, len(entries))
	for model, price := range entries {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "" || model == "sample_spec" || price.InputUSDPerToken <= 0 || price.OutputUSDPerToken <= 0 {
			continue
		}
		prices[model] = price
	}
	if len(prices) == 0 {
		return nil, errors.New("pricing: table contains no usable model prices")
	}
	return &Table{prices: prices}, nil
}

// Load returns the embedded bootstrap table, or the operator-provided file
// when path is non-empty.
func Load(path string) (*Table, error) {
	raw := embeddedTable
	if strings.TrimSpace(path) != "" {
		fileRaw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		raw = fileRaw
	}
	return Parse(raw)
}

// Resolve finds a model price with conservative normalization: exact match,
// then a provider-prefix strip (openai/gpt-4o), then a trailing date or
// "latest" suffix strip. Anything fuzzier risks billing the wrong family.
func (t *Table) Resolve(model string) (ModelPrice, bool) {
	if t == nil {
		return ModelPrice{}, false
	}
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return ModelPrice{}, false
	}
	candidates := []string{name}
	if index := strings.LastIndex(name, "/"); index >= 0 && index+1 < len(name) {
		candidates = append(candidates, name[index+1:])
	}
	expanded := candidates[:len(candidates):len(candidates)]
	for _, candidate := range candidates {
		if stripped := dateSuffix.ReplaceAllString(candidate, ""); stripped != candidate {
			expanded = append(expanded, stripped)
		}
		if stripped := strings.TrimSuffix(candidate, "-latest"); stripped != candidate {
			expanded = append(expanded, stripped)
		}
	}
	for _, candidate := range expanded {
		if price, ok := t.prices[candidate]; ok {
			return price, true
		}
	}
	return ModelPrice{}, false
}

// Service combines a table with a live USD→CNY conversion-rate provider. The
// rate is read per quote so operators can change it through the settings
// module without a restart; a non-positive rate disables pricing fail-closed.
type Service struct {
	table *Table
	rate  func() float64
}

func NewService(table *Table, rate func() float64) *Service {
	if table == nil || rate == nil {
		return nil
	}
	return &Service{table: table, rate: rate}
}

// StaticRate adapts a fixed conversion rate (tests, one-shot tooling).
func StaticRate(rate float64) func() float64 { return func() float64 { return rate } }

func (s *Service) Enabled() bool { return s != nil && s.rate() > 0 }

func (s *Service) USDToCNYRate() float64 {
	if s == nil {
		return 0
	}
	return s.rate()
}

// Quote is a resolved sell price in CNY micros per million tokens, after the
// group rate multiplier. The USD components and rate are kept for snapshots.
type Quote struct {
	Model                  string  `json:"model"`
	InputMicrosPerMillion  int64   `json:"input_micros_per_million"`
	OutputMicrosPerMillion int64   `json:"output_micros_per_million"`
	InputUSDPerMillion     float64 `json:"input_usd_per_million"`
	OutputUSDPerMillion    float64 `json:"output_usd_per_million"`
	RateMultiplierBps      int64   `json:"rate_multiplier_bps"`
	USDToCNYRate           float64 `json:"usd_to_cny_rate"`
}

// QuoteCNY resolves one model at a group multiplier (basis points; 10000 =
// 1.0x). It fails closed on unknown models or a disabled rate instead of
// guessing. The rate is read once per quote so a concurrent settings change
// yields an internally consistent quote.
func (s *Service) QuoteCNY(model string, multiplierBps int64) (Quote, bool) {
	if s == nil || multiplierBps <= 0 {
		return Quote{}, false
	}
	rate := s.rate()
	if rate <= 0 {
		return Quote{}, false
	}
	price, ok := s.table.Resolve(model)
	if !ok {
		return Quote{}, false
	}
	return Quote{
		Model:                  model,
		InputMicrosPerMillion:  cnyMicrosPerMillion(price.InputUSDPerToken, rate, multiplierBps),
		OutputMicrosPerMillion: cnyMicrosPerMillion(price.OutputUSDPerToken, rate, multiplierBps),
		InputUSDPerMillion:     price.InputUSDPerToken * 1_000_000,
		OutputUSDPerMillion:    price.OutputUSDPerToken * 1_000_000,
		RateMultiplierBps:      multiplierBps,
		USDToCNYRate:           rate,
	}, true
}

// cnyMicrosPerMillion converts USD-per-token into CNY micros per million
// tokens: usd/token × 1e6 tokens × rate × 1e6 micros × multiplier.
func cnyMicrosPerMillion(usdPerToken, usdToCny float64, multiplierBps int64) int64 {
	value := usdPerToken * 1e12 * usdToCny * float64(multiplierBps) / 10_000
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return int64(math.Round(value))
}
