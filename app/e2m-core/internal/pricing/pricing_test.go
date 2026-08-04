package pricing

import "testing"

func TestLoadEmbeddedTableAndResolve(t *testing.T) {
	table, err := Load("")
	if err != nil {
		t.Fatalf("load embedded table: %v", err)
	}
	if _, ok := table.Resolve("gpt-4o-mini"); !ok {
		t.Fatalf("embedded table must price gpt-4o-mini")
	}
	if _, ok := table.Resolve("openai/gpt-4o-mini"); !ok {
		t.Fatalf("provider prefix must be stripped")
	}
	if _, ok := table.Resolve("claude-sonnet-4-20250514"); !ok {
		t.Fatalf("date suffix must be stripped")
	}
	if _, ok := table.Resolve("gpt-4o-mini-latest"); !ok {
		t.Fatalf("latest suffix must be stripped")
	}
	if _, ok := table.Resolve("totally-unknown-model"); ok {
		t.Fatalf("unknown model must fail closed")
	}
}

func TestParseRejectsUnusableTables(t *testing.T) {
	if _, err := Parse([]byte(`{}`)); err == nil {
		t.Fatalf("empty table must be rejected")
	}
	if _, err := Parse([]byte(`{"free-model":{"input_cost_per_token":0,"output_cost_per_token":0}}`)); err == nil {
		t.Fatalf("zero-price-only table must be rejected")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Fatalf("invalid JSON must be rejected")
	}
}

func TestQuoteCNYConversion(t *testing.T) {
	table, err := Parse([]byte(`{"m1":{"input_cost_per_token":1.5e-7,"output_cost_per_token":6e-7}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	service := NewService(table, StaticRate(7.0))

	// 0.15 USD/M × 7.0 = 1.05 CNY/M = 1_050_000 micros at 1.0x.
	quote, ok := service.QuoteCNY("m1", 10_000)
	if !ok || quote.InputMicrosPerMillion != 1_050_000 || quote.OutputMicrosPerMillion != 4_200_000 {
		t.Fatalf("unexpected quote at 1.0x: ok=%v %+v", ok, quote)
	}
	// 1.5x multiplier.
	quote, ok = service.QuoteCNY("m1", 15_000)
	if !ok || quote.InputMicrosPerMillion != 1_575_000 || quote.OutputMicrosPerMillion != 6_300_000 {
		t.Fatalf("unexpected quote at 1.5x: ok=%v %+v", ok, quote)
	}
	if _, ok := service.QuoteCNY("m1", 0); ok {
		t.Fatalf("non-positive multiplier must fail")
	}
	if _, ok := service.QuoteCNY("missing", 10_000); ok {
		t.Fatalf("unknown model must fail")
	}
	if NewService(nil, StaticRate(7.0)) != nil || NewService(table, nil) != nil {
		t.Fatalf("service without a table or rate provider must be nil")
	}
	var disabled *Service
	if disabled.Enabled() {
		t.Fatalf("nil service must report disabled")
	}
	// A live provider returning zero disables pricing dynamically.
	dynamicRate := 7.0
	dynamic := NewService(table, func() float64 { return dynamicRate })
	if !dynamic.Enabled() {
		t.Fatalf("positive live rate must enable pricing")
	}
	dynamicRate = 0
	if dynamic.Enabled() {
		t.Fatalf("zero live rate must disable pricing")
	}
	if _, ok := dynamic.QuoteCNY("m1", 10_000); ok {
		t.Fatalf("quotes must fail closed while the rate is zero")
	}
}
