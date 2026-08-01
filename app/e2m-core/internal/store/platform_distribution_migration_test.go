package store

import (
	"strings"
	"testing"
)

func TestPlatformDistributionMigrationOwnsKeysAndUsageInsideE2M(t *testing.T) {
	up, err := migrationsFS.ReadFile("migrations/0078_platform_distribution.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(up)
	for _, required := range []string{
		"ALTER TABLE virtual_keys",
		"group_id TEXT REFERENCES upstream_pools(id)",
		"ALTER TABLE supply_usage_records",
		"input_price_micros_per_million",
		"ALTER TABLE supply_channel_endpoints",
		"allow_insecure",
		"'adjustment'",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("0078 migration missing %q", required)
		}
	}
	down, err := migrationsFS.ReadFile("migrations/0078_platform_distribution.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(down), "cannot downgrade 0078 while E2M platform distribution data exists") {
		t.Fatal("0078 downgrade must refuse implicit deletion of platform data")
	}
}
