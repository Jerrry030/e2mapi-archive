package store

import (
	"os"
	"strings"
	"testing"
)

func TestOperationalMetricsMigrationFencesLegacyWriters(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0069_operational_event_counters.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"coordinated, forward-only cutover, not a rolling/mixed-version",
		"SET LOCAL lock_timeout = '30s'",
		"SET LOCAL statement_timeout = '5min'",
		"IN ACCESS EXCLUSIVE MODE",
		"CREATE FUNCTION require_operational_counter_writer()",
		"current_setting('e2m.operational_counter_writer', true)",
		"IS DISTINCT FROM 'incremental-v1'",
		"ERRCODE = '55000'",
		"IF TG_LEVEL = 'STATEMENT'",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("0069 migration lacks legacy-writer fence %q", required)
		}
	}
	for _, table := range []string{
		"reconcile_runs",
		"upstream_collection_runs",
		"upstream_ingest_batches",
		"upstream_wallet_observations",
		"upstream_offer_observations",
		"upstream_change_events",
		"upstream_shadow_results",
		"upstream_dry_run_results",
	} {
		if !strings.Contains(sql, table) {
			t.Errorf("0069 migration does not fence counter-participating table %q", table)
		}
		if !strings.Contains(sql, "BEFORE TRUNCATE ON "+table) {
			t.Errorf("0069 migration does not fence TRUNCATE on counter-participating table %q", table)
		}
	}
}

func TestOperationalMetricsMigrationRejectsDestructiveDowngrade(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0069_operational_event_counters.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	if !strings.Contains(sql, "migration 0069 is forward-only") || !strings.Contains(sql, "ERRCODE = '0A000'") {
		t.Fatal("0069 down migration must fail closed with an actionable forward-only error")
	}
	if strings.Contains(strings.ToUpper(sql), "DROP TABLE") {
		t.Fatal("0069 down migration must not erase monotonic operational counters")
	}
	for _, required := range []string{"pre-0069 application is not compatible", "restore the entire database", "pre-0069 backup"} {
		if !strings.Contains(sql, required) {
			t.Errorf("0069 down migration does not state coordinated restore contract %q", required)
		}
	}
}

func TestPostgresPoolMarksOperationalCounterWriterCompatibility(t *testing.T) {
	raw, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if !strings.Contains(source, "poolConfig.AfterConnect") ||
		!strings.Contains(source, "set_config('e2m.operational_counter_writer','incremental-v1',false)") {
		t.Fatal("every PostgreSQL connection must carry the 0069 incremental-writer marker")
	}
}

func TestUI17NewAPIRunnerMarksEveryPostgresSessionWithoutLeakingMarker(t *testing.T) {
	raw, err := os.ReadFile("../../../../scripts/test-ui17-disposable-newapi.ps1")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "function Invoke-Postgres {")
	end := strings.Index(source, "function ConvertTo-SqlLiteral {")
	if start < 0 || end <= start {
		t.Fatal("could not isolate UI-17 NewAPI Invoke-Postgres")
	}
	invokePostgres := source[start:end]
	marker := "SELECT set_config('e2m.operational_counter_writer','incremental-v1',false)"
	markerAt := strings.Index(invokePostgres, marker)
	discardAt := -1
	if markerAt >= 0 {
		if relative := strings.Index(invokePostgres[markerAt:], `"\g /dev/null"`); relative >= 0 {
			discardAt = markerAt + relative
		}
	}
	callerAt := strings.Index(invokePostgres, "    $Sql\n  ) -join")
	pipeAt := strings.Index(invokePostgres, "$raw = $sessionSql | & docker")
	if markerAt < 0 || discardAt <= markerAt || callerAt <= discardAt || pipeAt <= callerAt {
		t.Fatal("UI-17 NewAPI PostgreSQL sessions must set the non-local writer marker, discard its result, then run caller SQL")
	}
	if strings.Contains(invokePostgres, "$raw = $Sql | & docker") {
		t.Fatal("UI-17 NewAPI runner must not bypass the marked session SQL")
	}
}

func TestUI17NewAPIRunnerRestoresHealthAggregationBeforeScenarioCBaseline(t *testing.T) {
	raw, err := os.ReadFile("../../../../scripts/test-ui17-disposable-newapi.ps1")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	prepare := `  [void](Prepare-ScenarioBaseline $Context $channels "C")`
	restart := `  Restart-DisposableCoreWithIntervals $Context "1s" "30m" "1m" $scenarioCPreWorkflowRestartReason`
	workflow := `  $workflowC = Invoke-RecommendationWorkflow $Context $channels "C" $false`
	prepareAt := strings.Index(source, prepare)
	restartAt := strings.Index(source, restart)
	workflowAt := strings.Index(source, workflow)
	if prepareAt < 0 || restartAt <= prepareAt || workflowAt <= restartAt {
		t.Fatal("UI-17 NewAPI runner must restore fast health aggregation after the C baseline fence and before C baseline traffic")
	}

	helperStart := strings.Index(source, "function Restart-DisposableCoreWithIntervals {")
	helperEnd := strings.Index(source, "function Record-DisposableCoreRestartEvidence {")
	if helperStart < 0 || helperEnd <= helperStart {
		t.Fatal("could not isolate UI-17 NewAPI Core restart helper")
	}
	helper := source[helperStart:helperEnd]
	for _, required := range []string{
		`SetEnvironmentVariable("E2M_UI17_HEALTH_METRICS_INTERVAL", $HealthInterval, "Process")`,
		`SetEnvironmentVariable("E2M_UI17_ROLLOUT_RUNNER_INTERVAL", $RolloutInterval, "Process")`,
		`SetEnvironmentVariable("E2M_UI17_ROLLOUT_WORKER_INTERVAL", $WorkerInterval, "Process")`,
		`Invoke-Compose up --detach --force-recreate --no-deps e2m-core`,
		`Wait-Http "$($Context.CoreBase)/healthz" "restarted E2M Core"`,
	} {
		if !strings.Contains(helper, required) {
			t.Errorf("UI-17 NewAPI Core restart helper lacks %q", required)
		}
	}
	if !strings.Contains(source, `$(if ($ReleaseEligible) { 300 } else { 5 })`) ||
		!strings.Contains(source, `$_.sample_count -ge 5`) {
		t.Fatal("UI-17 NewAPI runner weakened its release observation or quality sample gate")
	}
}

func TestPostgresMigrationRunnerRepairsOnlyRejected0069Downgrade(t *testing.T) {
	raw, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"recoverForwardOnly0069DownAttempt(m, driver)",
		"!dirty || version != 68",
		"driver.armRecovery()",
		"SELECT CURRENT_SCHEMA()",
		"validateOperationalCounterTableContracts(ctx, tx, schema)",
		"validateOperationalCounterTriggerBindings(bindings, functionOID)",
		"current_setting('e2m.operational_counter_writer', true)",
		"ERRCODE = '55000'",
		"WHERE version=68 AND dirty",
		"m.Force(69)",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("migration runner lacks bounded 0069 dirty-down recovery %q", required)
		}
	}
	if strings.Contains(source, "public.operational_event_counters") {
		t.Fatal("migration recovery must not hard-code the public schema")
	}
}

func TestPostgresStandaloneBatchUpsertCountsOutcomeAtomically(t *testing.T) {
	raw, err := os.ReadFile("postgres_upstream_intelligence.go")
	if err != nil {
		t.Fatal(err)
	}
	code := postgresFunctionSource(t, string(raw),
		"func (s *PostgresStore) UpsertUpstreamIntelligenceIngestBatch",
		"func (s *PostgresStore) IngestUpstreamIntelligenceBatch")
	for _, required := range []string{
		"tx, err := s.pool.Begin(ctx)",
		"row := tx.QueryRow(ctx, `INSERT INTO upstream_ingest_batches",
		"recordOperationalMetricTx(ctx, tx, \"ingest_facts\", \"accepted\"",
		"FOR UPDATE",
		"recordOperationalMetricTx(ctx, tx, \"ingest_facts\", \"duplicate\"",
	} {
		if !strings.Contains(code, required) {
			t.Errorf("standalone PostgreSQL batch upsert lacks atomic outcome step %q", required)
		}
	}
	if strings.Contains(code, "s.pool.QueryRow") || strings.Contains(code, "s.pool.Exec") {
		t.Fatal("standalone PostgreSQL batch upsert must not leave its transaction for receipt or counter writes")
	}
	assertSourceOrder(t, code,
		"recordOperationalMetricTx(ctx, tx, \"ingest_facts\", \"accepted\"",
		"tx.Commit(ctx)",
		"accepted counter must commit atomically after the receipt write")
	duplicateAt := strings.Index(code, "recordOperationalMetricTx(ctx, tx, \"ingest_facts\", \"duplicate\"")
	if duplicateAt < 0 || !strings.Contains(code[duplicateAt:], "tx.Commit(ctx)") {
		t.Fatal("duplicate counter must commit in the receipt-validation transaction")
	}
}
