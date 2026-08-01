package store

import (
	"strings"
	"testing"
)

func TestChannelHealthSnapshotRevisionMigrationProtectsImmutableEvidence(t *testing.T) {
	up, err := migrationsFS.ReadFile("migrations/0071_channel_health_snapshot_revisions.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	upSQL := strings.ToLower(string(up))
	for _, required := range []string{
		"drop constraint if exists channel_health_snapshots_scope_bucket_key",
		"bucket_start desc, created_at desc, id desc",
		"create or replace function reject_channel_health_snapshot_update",
		"before update on channel_health_snapshots",
		"drop trigger if exists trg_channel_health_snapshot_upstream_fact_version_update",
	} {
		if !strings.Contains(upSQL, required) {
			t.Errorf("0071 up migration lacks %q", required)
		}
	}

	down, err := migrationsFS.ReadFile("migrations/0071_channel_health_snapshot_revisions.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := strings.ToLower(string(down))
	for _, required := range []string{
		"drop function if exists reject_channel_health_snapshot_update",
		"(older.created_at, older.id) < (newer.created_at, newer.id)",
		"add constraint channel_health_snapshots_scope_bucket_key",
		"create trigger trg_channel_health_snapshot_upstream_fact_version_update",
	} {
		if !strings.Contains(downSQL, required) {
			t.Errorf("0071 down migration lacks %q", required)
		}
	}
}
