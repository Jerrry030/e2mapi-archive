package store

import (
	"os"
	"strings"
	"testing"
)

func TestUpstreamIntelligenceMigrationCarriesTrustAndEvidenceConstraints(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0058_upstream_intelligence.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := strings.ToLower(string(raw))
	for _, required := range []string{
		"numeric(38,18)",
		"capability_balance    boolean",
		"capability_groups     boolean",
		"capability_rates      boolean",
		"capability_prices     boolean",
		"unique (user_id, connector_id, local_ref)",
		"foreign key (connector_id, user_id, instance_id)",
		"primary key (user_id, id)",
		"primary key (user_id, run_id, batch_no)",
		"primary key (user_id, run_id, id)",
		"foreign key (user_id, run_id, source_id)",
		"finalized_fact_version bigint not null default 0",
		"unique (user_id, run_id, group_key, model_key, price_dimension)",
		"primary key (source_id, comparison_key)",
		"unique (source_id, event_fingerprint)",
		"where status = 'active' and link_scope = 'channel'",
		"accuracy in ('exact','derived','estimated','unknown','unattributed')",
		"coverage in ('complete','partial','unavailable')",
		"status in ('succeeded','partial','failed')",
		"completed_at >= observed_at",
		"error_code in ('auth_failed','rate_limited','schema_unsupported','response_too_large','upstream_unavailable')",
		"fact_count <> 0",
		"unique index if not exists uq_upstream_channel_allocations_owner_channel",
		"foreign key (user_id, channel_id)",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration lacks required invariant %q", required)
		}
	}
	// Core stores normalized facts and hashes, not transport secrets or an
	// upstream HTTP response body.
	for _, forbidden := range []string{"credential", "cookie", "authorization", "raw_response", "base_url", "endpoint_url", "capabilities jsonb"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("migration crosses Connector trust boundary with %q", forbidden)
		}
	}
	sourceStart := strings.Index(sql, "create table if not exists upstream_intelligence_sources")
	runStart := strings.Index(sql, "create table if not exists upstream_collection_runs")
	if sourceStart < 0 || runStart <= sourceStart || strings.Contains(sql[sourceStart:runStart], "fact_version") {
		t.Fatal("source table must not carry owner fact_version state")
	}
}

func TestPostgresSourceIdentityLinkRequiresUniqueAllocatedChannel(t *testing.T) {
	implementation, err := os.ReadFile("postgres_upstream_intelligence.go")
	if err != nil {
		t.Fatal(err)
	}
	code := postgresFunctionSource(t, string(implementation),
		"func (s *PostgresStore) UpsertUpstreamIntelligenceLink", "func recordUpstreamIntelligenceFactMutationTx")
	for _, required := range []string{
		"SELECT COUNT(*)",
		"FROM upstream_channel_allocations WHERE source_id=$1 AND user_id=$2",
		"input.Status == contracts.UpstreamLinkActive && targetCount != 1",
		"return contracts.UpstreamIntelligenceLink{}, ErrConflict",
	} {
		if !strings.Contains(code, required) {
			t.Errorf("source-identity resolution lacks %q", required)
		}
	}
	assertSourceOrder(t, code,
		"SELECT COUNT(*)",
		"INSERT INTO upstream_intelligence_links",
		"unique source-identity allocation must be proven before the link write")
}

func TestPostgresFinalizationPersistsRunFactVersionFence(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0058_upstream_intelligence.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := strings.ToLower(string(raw))
	if !strings.Contains(sql, "finalized_fact_version") {
		t.Fatal("migration lacks durable per-run finalization fence")
	}
	// This is a source-level regression guard for environments without the
	// optional PostgreSQL integration DSN: finalization must not infer replay
	// from timestamps, since independent runs can complete in the same instant.
	implementation, err := os.ReadFile("postgres_upstream_intelligence.go")
	if err != nil {
		t.Fatalf("read postgres implementation: %v", err)
	}
	code := strings.ToLower(string(implementation))
	if !strings.Contains(code, "run.finalizedfactversion > 0") ||
		!strings.Contains(code, "update upstream_collection_runs set finalized_fact_version") {
		t.Fatal("postgres finalization does not persist/use the run fence")
	}
	if strings.Contains(code, "lastrunat.equal(*run.completedat)") {
		t.Fatal("postgres finalization still uses timestamp equality as a replay fence")
	}
}

func TestPostgresUpstreamIntelligenceLinkProvesBothTargetFormsAndBumpsVersion(t *testing.T) {
	implementation, err := os.ReadFile("postgres_upstream_intelligence.go")
	if err != nil {
		t.Fatalf("read postgres implementation: %v", err)
	}
	code := postgresFunctionSource(t, string(implementation),
		"func (s *PostgresStore) UpsertUpstreamIntelligenceLink", "func recordUpstreamIntelligenceFactMutationTx")
	for _, required := range []string{
		"WHERE channel_id=$1 AND user_id=$2",
		"WHERE source_id=$1 AND user_id=$2",
		"FOR UPDATE",
		"recordUpstreamIntelligenceFactMutationTx(ctx, tx, input.UserID, UpstreamIntelligenceFactMutationLink, link.ID)",
	} {
		if !strings.Contains(code, required) {
			t.Errorf("PostgreSQL link upsert lacks %q", required)
		}
	}
	assertSourceOrder(t, code,
		"WHERE source_id=$1 AND user_id=$2",
		"INSERT INTO upstream_intelligence_links",
		"source-identity ownership must be proven before the link write")
	assertSourceOrder(t, code,
		"INSERT INTO upstream_intelligence_links",
		"recordUpstreamIntelligenceFactMutationTx(ctx, tx, input.UserID, UpstreamIntelligenceFactMutationLink, link.ID)",
		"link and fact-version writes must share one transaction")
	bumpAt := strings.Index(code, "recordUpstreamIntelligenceFactMutationTx(ctx, tx, input.UserID, UpstreamIntelligenceFactMutationLink, link.ID)")
	if bumpAt < 0 || strings.Index(code[bumpAt:], "tx.Commit(ctx)") < 0 {
		t.Fatal("fact version must advance before the changed-link transaction commits")
	}
}

func TestPostgresCompleteSnapshotChangesShareFinalizationTransaction(t *testing.T) {
	implementation, err := os.ReadFile("postgres_upstream_intelligence.go")
	if err != nil {
		t.Fatalf("read postgres implementation: %v", err)
	}
	code := string(implementation)
	finalize := postgresFunctionSource(t, code,
		"func (s *PostgresStore) FinalizeUpstreamCollectionRun",
		"func reconcileCompleteSnapshotAbsencesTx")
	assertSourceOrder(t, finalize,
		"tx, err := s.pool.Begin(ctx)",
		"reconcileCompleteSnapshotAbsencesTx(ctx, tx, run)",
		"absence reconciliation must run after opening finalization transaction")
	assertSourceOrder(t, finalize,
		"reconcileCompleteSnapshotAbsencesTx(ctx, tx, run)",
		"tx.Commit(ctx)",
		"absence reconciliation must commit atomically with fact version and source pointer")

	reconcile := postgresFunctionSource(t, code,
		"func reconcileCompleteSnapshotAbsencesTx",
		"")
	for _, required := range []string{
		"WHERE user_id=$1 AND source_id=$2 AND run_id=$3",
		"WHERE user_id=$1 AND source_id=$2 ORDER BY comparison_key FOR UPDATE",
		"INSERT INTO upstream_snapshot_absences",
		"INSERT INTO upstream_change_events",
		"ON CONFLICT (source_id,event_fingerprint) DO NOTHING",
	} {
		if !strings.Contains(reconcile, required) {
			t.Errorf("PostgreSQL transactional change detection lacks %q", required)
		}
	}
}

func TestPostgresFalseRemovalSignalSurvivesBusinessRollback(t *testing.T) {
	implementation, err := os.ReadFile("postgres_upstream_intelligence.go")
	if err != nil {
		t.Fatalf("read postgres implementation: %v", err)
	}
	code := string(implementation)
	finalize := postgresFunctionSource(t, code,
		"func (s *PostgresStore) FinalizeUpstreamCollectionRun",
		"func reconcileCompleteSnapshotAbsencesTx")
	assertSourceOrder(t, finalize,
		"tx.Rollback(ctx)",
		"s.RecordFalseRemovalInvariant(ctx)",
		"false-removal counter must be recorded only after the business transaction rolls back")
	reconcile := postgresFunctionSource(t, code,
		"func reconcileCompleteSnapshotAbsencesTx",
		"")
	if strings.Contains(reconcile, "recordOperationalEventTx") {
		t.Fatal("false-removal signal must not be written inside the transaction that rejects the business mutation")
	}
}

func TestPostgresUpstreamIngestConflictTargetsAreOwnerScoped(t *testing.T) {
	implementation, err := os.ReadFile("postgres_upstream_intelligence.go")
	if err != nil {
		t.Fatalf("read postgres implementation: %v", err)
	}
	code := strings.ToLower(string(implementation))
	for _, required := range []string{
		"on conflict (user_id,id) do nothing returning `+upstreamruncols",
		"on conflict (user_id,run_id,batch_no) do nothing returning `+upstreambatchcols",
		"on conflict (user_id,run_id,id) do nothing returning `+walletcols",
	} {
		if !strings.Contains(code, required) {
			t.Errorf("postgres upstream ingest lacks owner-scoped conflict target %q", required)
		}
	}
	if strings.Contains(code, "on conflict (run_id,batch_no)") || strings.Contains(code, "on conflict (run_id,id)") {
		t.Fatal("postgres upstream ingest still has a globally scoped run/fact conflict target")
	}
}

func TestPostgresUpstreamRunAdvisoryLockPrecedesRowLocks(t *testing.T) {
	implementation, err := os.ReadFile("postgres_upstream_intelligence.go")
	if err != nil {
		t.Fatalf("read postgres implementation: %v", err)
	}
	code := string(implementation)
	if !strings.Contains(code, "pg_advisory_xact_lock(hashtextextended($2::text, $1::bigint))") {
		t.Fatal("postgres ingest/finalization lacks a stable owner-scoped run advisory lock")
	}

	ingest := postgresFunctionSource(t, code,
		"func (s *PostgresStore) IngestUpstreamIntelligenceBatch",
		"func lockUpstreamIntelligenceRunTx")
	assertSourceOrder(t, ingest,
		"lockUpstreamIntelligenceRunTx(ctx, tx, input.Source.UserID, input.Run.ID)",
		"upsertUpstreamSourceTx(ctx, tx, input.Source)",
		"ingest must take the owner/run advisory lock before locking the source")

	finalize := postgresFunctionSource(t, code,
		"func (s *PostgresStore) FinalizeUpstreamCollectionRun",
		"")
	assertSourceOrder(t, finalize,
		"lockUpstreamIntelligenceRunTx(ctx, tx, userID, runID)",
		"FROM upstream_collection_runs WHERE user_id=$1 AND id=$2 FOR UPDATE",
		"finalization must take the owner/run advisory lock before locking the run")
}

func postgresFunctionSource(t *testing.T, code, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(code, startMarker)
	if start < 0 {
		t.Fatalf("postgres implementation lacks %q", startMarker)
	}
	end := len(code)
	if endMarker != "" {
		relativeEnd := strings.Index(code[start:], endMarker)
		if relativeEnd <= 0 {
			t.Fatalf("postgres implementation lacks boundary %q after %q", endMarker, startMarker)
		}
		end = start + relativeEnd
	}
	return code[start:end]
}

func assertSourceOrder(t *testing.T, code, first, second, message string) {
	t.Helper()
	firstAt, secondAt := strings.Index(code, first), strings.Index(code, second)
	if firstAt < 0 || secondAt < 0 || firstAt >= secondAt {
		t.Fatalf("%s: first=%d second=%d", message, firstAt, secondAt)
	}
}

func TestUpstreamIntelligenceDownMigrationDropsChildrenFirst(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0058_upstream_intelligence.down.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := strings.ToLower(string(raw))
	ordered := []string{
		"drop table if exists upstream_intelligence_links",
		"drop table if exists upstream_change_events",
		"drop table if exists upstream_snapshot_absences",
		"drop table if exists upstream_offer_observations",
		"drop table if exists upstream_wallet_observations",
		"drop table if exists upstream_ingest_batches",
		"drop table if exists upstream_collection_runs",
		"drop table if exists upstream_intelligence_sources",
		"drop table if exists upstream_intelligence_fact_versions",
	}
	last := -1
	for _, statement := range ordered {
		position := strings.Index(sql, statement)
		if position < 0 {
			t.Fatalf("down migration lacks %q", statement)
		}
		if position <= last {
			t.Fatalf("down migration dependency order is invalid at %q", statement)
		}
		last = position
	}
}

func TestUpstreamIntelligenceQualityJoinMigrationIsOwnerScopedAndNonGuessing(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0060_upstream_intelligence_quality_join.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := strings.ToLower(string(raw))
	for _, required := range []string{
		"where status = 'active'",
		"and price_dimension = ''",
		"set status = 'inactive'",
		"join channel_health_snapshots as snapshot",
		"check (status <> 'active' or price_dimension <> '')",
		"from upstream_channel_allocations as allocation",
		"where allocation.channel_id = new.channel_id",
		"insert into upstream_intelligence_fact_versions",
		"fact_version = upstream_intelligence_fact_versions.fact_version + 1",
		"after insert on channel_health_snapshots",
		"after update on channel_health_snapshots",
		"old is distinct from new",
		"idx_channel_health_snapshots_frontier_current",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("quality-join migration lacks %q", required)
		}
	}
	for _, forbidden := range []string{"credential", "cookie", "authorization", "raw_response", "base_url", "endpoint_url"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("quality-join migration crosses trust boundary with %q", forbidden)
		}
	}
	if strings.Contains(sql, "set price_dimension") {
		t.Fatal("migration must not guess a dimension for historical links")
	}
}
