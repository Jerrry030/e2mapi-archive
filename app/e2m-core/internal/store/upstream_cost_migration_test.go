package store

import (
	"os"
	"strings"
	"testing"
)

func TestUpstreamCostMigrationIsOwnerScopedExactAndAppendOnly(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0061_upstream_cost_ledger.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(raw))
	for _, required := range []string{
		"numeric(38,18)", "unique (user_id, idempotency_key)", "upstream_cost_fact_versions",
		"fact_version  bigint not null", "attribution in ('exact','derived','estimated','unknown','unattributed')",
		"price_status in ('valid','expired','unavailable')", "price_dimension in ('input','output','cached_input','request')",
		"amount is null and unit_cost is null", "jsonb_array_length(missing_fields) > 0",
		"idx_upstream_cost_facts_owner_occurred", "idx_upstream_cost_facts_owner_source_occurred",
		"idx_upstream_cost_facts_owner_channel_model_occurred",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration lacks %q", required)
		}
	}
	for _, forbidden := range []string{"credential", "cookie", "authorization", "raw_response", "base_url", "endpoint_url"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("migration crosses Connector trust boundary with %q", forbidden)
		}
	}
}

func TestUpstreamCostStoreUsesAtomicBatchAndNoFreshnessInterval(t *testing.T) {
	storeRaw, err := os.ReadFile("postgres_upstream_cost.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(storeRaw)
	for _, required := range []string{"BeginTx", "FOR UPDATE", "fact_version=fact_version+1", "AppendUpstreamCostFacts"} {
		if !strings.Contains(code, required) {
			t.Errorf("postgres batch implementation lacks %q", required)
		}
	}
	ledgerRaw, err := os.ReadFile("../upstreamcost/ledger.go")
	if err != nil {
		t.Fatal(err)
	}
	ledger := string(ledgerRaw)
	functionAt := strings.Index(ledger, "func effectiveEndForCandidate")
	if functionAt < 0 {
		t.Fatal("ledger lacks explicit effective interval calculation")
	}
	if strings.Contains(ledger[functionAt:], "FreshUntil") {
		t.Fatal("historical price interval must not be truncated by FreshUntil")
	}
}
