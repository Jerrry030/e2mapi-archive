package store

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestConnectorTrafficShareMigrationDefinesExactCurrentClosedTaskTypeSet(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0066_connector_traffic_share.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(raw))
	want := append(connectorTaskTypesAt0041(),
		"gateway.account.traffic_share.set",
		"upstream.intelligence.collect",
	)
	slices.Sort(want)
	got := migrationQuotedValues(sql)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("closed connector task set = %q, want %q", got, want)
	}
	for _, unknown := range []string{"gateway.raw.request", "gateway.account.weight.set", "upstream.intelligence.raw.collect"} {
		if slices.Contains(got, unknown) {
			t.Fatalf("unknown task type %q is permitted by CHECK", unknown)
		}
	}
}

func TestConnectorTrafficShareDownMigrationDeletesRowsBeforeRestoring0041Set(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0066_connector_traffic_share.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(raw))
	deleteAt := strings.Index(sql, "delete from connector_tasks")
	restoreAt := strings.Index(sql, "add constraint connector_tasks_type_check")
	if deleteAt < 0 || restoreAt < 0 || deleteAt >= restoreAt {
		t.Fatalf("down migration must delete traffic-share rows before restoring the CHECK: %s", sql)
	}
	restoredCheck := sql[restoreAt:]
	for _, required := range connectorTaskTypesAt0041() {
		if !strings.Contains(restoredCheck, "'"+required+"'") {
			t.Errorf("restored CHECK lacks 0041 task type %q", required)
		}
	}
	if strings.Contains(restoredCheck, "'gateway.account.traffic_share.set'") {
		t.Fatal("down migration retained traffic-share in the restored 0041 CHECK")
	}
	if strings.Contains(restoredCheck, "'upstream.intelligence.collect'") {
		t.Fatal("down migration retained upstream collection in the restored 0041 CHECK")
	}
	deleteBlock := sql[deleteAt:restoreAt]
	for _, taskType := range []string{"gateway.account.traffic_share.set", "upstream.intelligence.collect"} {
		if !strings.Contains(deleteBlock, "'"+taskType+"'") {
			t.Fatalf("down migration does not remove %q rows before restoring 0041", taskType)
		}
	}
}

func migrationQuotedValues(sql string) []string {
	matches := regexp.MustCompile(`'([a-z0-9._-]+)'`).FindAllStringSubmatch(sql, -1)
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		values = append(values, match[1])
	}
	return values
}

func connectorTaskTypesAt0041() []string {
	return []string{
		"gateway.health.get",
		"gateway.accounts.list",
		"gateway.account.quality.probe",
		"gateway.binding.proof",
		"gateway.binding.install",
		"gateway.account.schedulable.set",
		"gateway.account.switch",
		"gateway.scheduling.barrier",
		"gateway.account.create",
		"gateway.account.update",
		"gateway.account.delete",
	}
}
