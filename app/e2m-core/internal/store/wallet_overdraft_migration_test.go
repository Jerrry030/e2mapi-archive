package store

import (
	"strings"
	"testing"
)

// Overdraft is enforced by database constraints as much as by Go code, and the
// memory store has no constraints at all — a settlement above the hold passes
// its unit tests while PostgreSQL rejects it and silently falls back to a
// conservative (undercharging) settle. These assertions keep the three
// constraints that encode the old "never charge above the hold" invariant from
// creeping back.
func TestOverdraftMigrationsDropTheNonNegativeAndCeilingConstraints(t *testing.T) {
	for _, tc := range []struct {
		file     string
		required []string
	}{
		{
			file: "migrations/0081_wallet_overdraft.up.sql",
			required: []string{
				"ALTER TABLE wallet_accounts",
				"DROP CONSTRAINT IF EXISTS wallet_accounts_available_micros_check",
			},
		},
		{
			file: "migrations/0082_settled_may_exceed_hold.up.sql",
			required: []string{
				"DROP CONSTRAINT IF EXISTS wallet_reservations_check",
				"DROP CONSTRAINT IF EXISTS supply_usage_records_check",
			},
		},
	} {
		raw, err := migrationsFS.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		source := string(raw)
		for _, required := range tc.required {
			if !strings.Contains(source, required) {
				t.Fatalf("%s missing %q", tc.file, required)
			}
		}
	}

	// Both downgrades must refuse rather than rewrite customer money.
	for _, file := range []string{
		"migrations/0081_wallet_overdraft.down.sql",
		"migrations/0082_settled_may_exceed_hold.down.sql",
	} {
		raw, err := migrationsFS.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if !strings.Contains(string(raw), "RAISE EXCEPTION") {
			t.Fatalf("%s must refuse to downgrade over outstanding debt", file)
		}
	}
}
