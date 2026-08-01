package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
)

const ui17ForwardOnlyLiveEnvironment = "E2M_UI17_FORWARD_ONLY_LIVE_DSN"

func TestUI17ForwardOnly0069LiveRecovery(t *testing.T) {
	dsn := ui17ForwardOnlyLiveDSN(t)
	for _, schema := range []string{"public", "ui17_non_public"} {
		t.Run(schema, func(t *testing.T) {
			ui17ResetForwardOnlySchema(t, dsn, schema, schema == "public")
			schemaDSN := ui17SchemaDSN(dsn, schema)
			ui17InduceRejected0069Down(t, schemaDSN)
			if err := runMigrations(schemaDSN); err != nil {
				t.Fatalf("recover dirty-down metadata: %v", err)
			}
			ui17RequireMigrationVersion(t, schemaDSN, schema, 74, false)
		})
	}
}

func TestUI17ForwardOnly0069LiveCorruptionRejectMatrix(t *testing.T) {
	dsn := ui17ForwardOnlyLiveDSN(t)
	mutations := []struct {
		name string
		sql  string
	}{
		{name: "counter default", sql: `ALTER TABLE operational_metric_counters ALTER COLUMN total SET DEFAULT 1`},
		{name: "function body", sql: `CREATE OR REPLACE FUNCTION require_operational_counter_writer() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`},
		{name: "trigger disabled", sql: `ALTER TABLE reconcile_runs DISABLE TRIGGER require_operational_counter_writer_reconcile`},
		{name: "trigger predicate", sql: `DROP TRIGGER require_operational_counter_writer_reconcile ON reconcile_runs;
			CREATE TRIGGER require_operational_counter_writer_reconcile BEFORE INSERT OR UPDATE OR DELETE ON reconcile_runs
			FOR EACH ROW WHEN (false) EXECUTE FUNCTION require_operational_counter_writer()`},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			const schema = "ui17_corrupt"
			ui17ResetForwardOnlySchema(t, dsn, schema, false)
			schemaDSN := ui17SchemaDSN(dsn, schema)
			ui17InduceRejected0069Down(t, schemaDSN)
			conn := ui17ForwardOnlyConnect(t, schemaDSN)
			if _, err := conn.Exec(context.Background(), mutation.sql); err != nil {
				conn.Close(context.Background())
				t.Fatal(err)
			}
			conn.Close(context.Background())

			if err := runMigrations(schemaDSN); err == nil {
				t.Fatal("corrupt 0069 schema was recovered instead of failing closed")
			}
			ui17RequireMigrationVersion(t, schemaDSN, schema, 68, true)
		})
	}
}

func TestUI17ForwardOnly0069LiveConcurrentStartup(t *testing.T) {
	dsn := ui17ForwardOnlyLiveDSN(t)
	const schema = "ui17_concurrent"
	ui17ResetForwardOnlySchema(t, dsn, schema, false)
	schemaDSN := ui17SchemaDSN(dsn, schema)
	ui17InduceRejected0069Down(t, schemaDSN)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			errs <- runMigrations(schemaDSN)
		}()
	}
	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent startup: %v", err)
		}
	}
	ui17RequireMigrationVersion(t, schemaDSN, schema, 74, false)
}

func TestUI17ForwardOnly0069LiveReverseLockFailsBounded(t *testing.T) {
	dsn := ui17ForwardOnlyLiveDSN(t)
	const schema = "ui17_reverse_lock"
	ui17ResetForwardOnlySchema(t, dsn, schema, false)
	schemaDSN := ui17SchemaDSNWithDeadlockTimeout(t, dsn, schema)
	ui17InduceRejected0069Down(t, schemaDSN)

	migrationURL, err := postgresMigrationURL(schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	parsedMigrationURL, err := url.Parse(migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	advisoryKey, err := database.GenerateAdvisoryLockId(parsedMigrationURL.Path, schema, "schema_migrations")
	if err != nil {
		t.Fatal(err)
	}

	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	baseDriver, err := database.Open(migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	driver := newForwardOnly0069Driver(baseDriver, schemaDSN)
	recoveryEntered := make(chan struct{})
	driver.recover = func(ctx context.Context, recoveryDSN string) error {
		close(recoveryEntered)
		return recoverForwardOnly0069Metadata(ctx, recoveryDSN)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		baseDriver.Close()
		t.Fatal(err)
	}
	defer m.Close()
	version, dirty, err := m.Version()
	if err != nil {
		t.Fatal(err)
	}
	if version != 68 || !dirty {
		t.Fatalf("migration metadata=(%d,%t), want (68,true)", version, dirty)
	}
	if err := driver.armRecovery(); err != nil {
		t.Fatal(err)
	}

	blocker := ui17ForwardOnlyConnect(t, schemaDSN)
	defer blocker.Close(context.Background())
	tx, err := blocker.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	migrationsTable := pgx.Identifier{schema, "schema_migrations"}.Sanitize()
	if _, err := tx.Exec(context.Background(), `LOCK TABLE `+migrationsTable+` IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}

	observer := ui17ForwardOnlyConnect(t, schemaDSN)
	defer observer.Close(context.Background())

	started := time.Now()
	forceResult := make(chan error, 1)
	go func() {
		forceResult <- m.Force(69)
	}()
	select {
	case <-recoveryEntered:
	case err := <-forceResult:
		t.Fatalf("Force(69) returned before recovery entered: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Force(69) did not enter recovery after acquiring the migrate advisory lock")
	}

	recoveryPID := ui17WaitForRelationLockWait(t, observer, schema+".schema_migrations", blocker.PgConn().PID())
	var advisoryAvailable bool
	if err := observer.QueryRow(context.Background(), `SELECT pg_try_advisory_lock($1::bigint)`, advisoryKey).Scan(&advisoryAvailable); err != nil {
		t.Fatal(err)
	}
	if advisoryAvailable {
		_, _ = observer.Exec(context.Background(), `SELECT pg_advisory_unlock($1::bigint)`, advisoryKey)
		t.Fatalf("migrate advisory lock %s was not held while recovery backend %d waited for metadata", advisoryKey, recoveryPID)
	}
	t.Logf("observed recovery backend %d waiting for AccessExclusiveLock while migrate advisory lock %s remained held", recoveryPID, advisoryKey)

	blockerResult := make(chan error, 1)
	blockerContext, cancelBlocker := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancelBlocker()
	go func() {
		_, blockerErr := tx.Exec(blockerContext, `SELECT pg_advisory_lock($1::bigint)`, advisoryKey)
		blockerResult <- blockerErr
	}()
	ui17WaitForBackendLockWait(t, observer, blocker.PgConn().PID(), "advisory")
	t.Logf("observed metadata holder backend %d waiting for the same migrate advisory lock", blocker.PgConn().PID())

	select {
	case err = <-forceResult:
	case <-time.After(35 * time.Second):
		t.Fatal("Force(69) did not fail within the bounded recovery timeout")
	}
	elapsed := time.Since(started)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "lock metadata") {
		t.Fatalf("reverse lock error=%v, want bounded lock failure", err)
	}
	if elapsed > 35*time.Second {
		t.Fatalf("reverse lock failed after %s, want <=35s", elapsed)
	}
	select {
	case err := <-blockerResult:
		if err != nil {
			t.Fatalf("metadata holder did not acquire advisory lock after recovery failed closed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("metadata holder remained blocked after Force(69) released the advisory lock")
	}

	var blockedVersion int
	var blockedDirty bool
	if err := tx.QueryRow(context.Background(), `SELECT version, dirty FROM `+migrationsTable).Scan(&blockedVersion, &blockedDirty); err != nil {
		t.Fatal(err)
	}
	if blockedVersion != 68 || !blockedDirty {
		t.Fatalf("metadata changed inside reverse lock=(%d,%t), want (68,true)", blockedVersion, blockedDirty)
	}
	t.Logf("bounded recovery failed closed after %s with metadata still (%d,%t)", elapsed, blockedVersion, blockedDirty)
	var unlocked bool
	if err := tx.QueryRow(context.Background(), `SELECT pg_advisory_unlock($1::bigint)`, advisoryKey).Scan(&unlocked); err != nil {
		t.Fatal(err)
	}
	if !unlocked {
		t.Fatalf("metadata holder did not own migrate advisory lock %s after recovery released it", advisoryKey)
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	ui17RequireMigrationVersion(t, schemaDSN, schema, 68, true)
	if err := runMigrations(schemaDSN); err != nil {
		t.Fatalf("recovery was not retryable after lock release: %v", err)
	}
	ui17RequireMigrationVersion(t, schemaDSN, schema, 74, false)
	t.Log("retry after releasing both sides of the reverse lock reached migration metadata (74,false)")
}

func ui17WaitForRelationLockWait(t *testing.T, observer *pgx.Conn, relation string, excludedPID uint32) uint32 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var pid uint32
		var waitEventType string
		var waitEvent string
		err := observer.QueryRow(context.Background(), `SELECT a.pid, COALESCE(a.wait_event_type, ''), COALESCE(a.wait_event, '')
			FROM pg_catalog.pg_locks AS l
			JOIN pg_catalog.pg_stat_activity AS a ON a.pid = l.pid
			WHERE l.locktype = 'relation'
			  AND l.relation = pg_catalog.to_regclass($1)::oid
			  AND l.mode = 'AccessExclusiveLock'
			  AND NOT l.granted
			  AND l.pid <> $2
			ORDER BY a.pid
			LIMIT 1`, relation, excludedPID).Scan(&pid, &waitEventType, &waitEvent)
		if err == nil {
			if waitEventType != "Lock" || waitEvent != "relation" {
				t.Fatalf("recovery backend %d wait=(%q,%q), want (Lock,relation)", pid, waitEventType, waitEvent)
			}
			return pid
		}
		if err != pgx.ErrNoRows {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no recovery backend waited for AccessExclusiveLock on %s", relation)
	return 0
}

func ui17WaitForBackendLockWait(t *testing.T, observer *pgx.Conn, pid uint32, waitEvent string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waitEventType string
		var gotWaitEvent string
		err := observer.QueryRow(context.Background(), `SELECT COALESCE(wait_event_type, ''), COALESCE(wait_event, '')
			FROM pg_catalog.pg_stat_activity WHERE pid = $1`, pid).Scan(&waitEventType, &gotWaitEvent)
		if err == nil && waitEventType == "Lock" && gotWaitEvent == waitEvent {
			return
		}
		if err != nil && err != pgx.ErrNoRows {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("backend %d did not block on %s lock", pid, waitEvent)
}

func ui17ForwardOnlyLiveDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(ui17ForwardOnlyLiveEnvironment))
	if dsn == "" {
		t.Skip("set " + ui17ForwardOnlyLiveEnvironment + " to an exclusively disposable PostgreSQL database")
	}
	if strings.TrimSpace(os.Getenv("E2M_UI17_FORWARD_ONLY_DISPOSABLE_ACK")) != "1" {
		t.Fatal("E2M_UI17_FORWARD_ONLY_DISPOSABLE_ACK=1 is required; the live test drops dedicated schemas")
	}
	return dsn
}

func ui17SchemaDSN(dsn, schema string) string {
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "options=-csearch_path%3D" + schema
}

func ui17SchemaDSNWithDeadlockTimeout(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	options := strings.TrimSpace(query.Get("options") + " -csearch_path=" + schema + " -cdeadlock_timeout=60s")
	query.Set("options", options)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func ui17ResetForwardOnlySchema(t *testing.T, dsn, schema string, resetDatabase bool) {
	t.Helper()
	if schema != "public" && !strings.HasPrefix(schema, "ui17_") {
		t.Fatalf("refusing destructive schema reset for %q", schema)
	}
	conn := ui17ForwardOnlyConnect(t, dsn)
	defer conn.Close(context.Background())
	if resetDatabase {
		rows, err := conn.Query(context.Background(), `SELECT nspname FROM pg_catalog.pg_namespace
			WHERE nspname NOT LIKE 'pg_%' AND nspname <> 'information_schema'`)
		if err != nil {
			t.Fatal(err)
		}
		var schemas []string
		for rows.Next() {
			var existing string
			if err := rows.Scan(&existing); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			schemas = append(schemas, existing)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		for _, existing := range schemas {
			if _, err := conn.Exec(context.Background(), `DROP SCHEMA `+pgx.Identifier{existing}.Sanitize()+` CASCADE`); err != nil {
				t.Fatalf("reset disposable database schema %s: %v", existing, err)
			}
		}
	}
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+identifier+` CASCADE; CREATE SCHEMA `+identifier); err != nil {
		t.Fatalf("reset disposable schema %s: %v", schema, err)
	}
	if err := runMigrations(ui17SchemaDSN(dsn, schema)); err != nil {
		t.Fatalf("migrate disposable schema %s: %v", schema, err)
	}
}

func ui17InduceRejected0069Down(t *testing.T, dsn string) {
	t.Helper()
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	migrationURL, err := postgresMigrationURL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := database.Open(migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		driver.Close()
		t.Fatal(err)
	}
	defer m.Close()
	// Keep this recovery proof pinned to the exceptional 0069 boundary even
	// as later migrations are added. A single down step from latest would only
	// exercise the newest ordinary down migration, not 0069's fail-closed path.
	if err := m.Migrate(69); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate to 0069 recovery boundary: %v", err)
	}
	err = m.Steps(-1)
	if err == nil || !strings.Contains(err.Error(), "forward-only") {
		t.Fatalf("0069 down error=%v, want deliberate forward-only rejection", err)
	}
}

func ui17RequireMigrationVersion(t *testing.T, dsn, schema string, version int, dirty bool) {
	t.Helper()
	conn := ui17ForwardOnlyConnect(t, dsn)
	defer conn.Close(context.Background())
	var gotVersion int
	var gotDirty bool
	query := fmt.Sprintf(`SELECT version,dirty FROM %s`, pgx.Identifier{schema, "schema_migrations"}.Sanitize())
	if err := conn.QueryRow(context.Background(), query).Scan(&gotVersion, &gotDirty); err != nil {
		t.Fatal(err)
	}
	if gotVersion != version || gotDirty != dirty {
		t.Fatalf("migration metadata=(%d,%t), want (%d,%t)", gotVersion, gotDirty, version, dirty)
	}
}

func ui17ForwardOnlyConnect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}
