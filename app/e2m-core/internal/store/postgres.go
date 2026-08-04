package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"e2m.local/contracts"
)

// migrationsFS embeds the golang-migrate SQL files so the binary applies the
// schema on startup without shipping .sql files separately.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore is the production Store implementation backed by PostgreSQL via
// pgx. It applies embedded migrations on construction.
type PostgresStore struct {
	pool *pgxpool.Pool
}

var _ Store = (*PostgresStore)(nil)

// NewPostgresStore connects to dsn, runs migrations, and returns a ready store.
func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	if dsn == "" {
		return nil, errors.New("store: E2M_CORE_DATABASE_URL is required for postgres backend")
	}
	if err := runMigrations(dsn); err != nil {
		return nil, fmt.Errorf("store: migrations: %w", err)
	}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: parse postgres config: %w", err)
	}
	previousAfterConnect := poolConfig.AfterConnect
	poolConfig.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		if previousAfterConnect != nil {
			if err := previousAfterConnect(ctx, connection); err != nil {
				return err
			}
		}
		_, err := connection.Exec(ctx, `SELECT set_config('e2m.operational_counter_writer','incremental-v1',false)`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

// Close releases the connection pool.
func (s *PostgresStore) Close() { s.pool.Close() }

func runMigrations(dsn string) error {
	migrationURL, err := postgresMigrationURL(dsn)
	if err != nil {
		return err
	}
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	baseDriver, err := database.Open(migrationURL)
	if err != nil {
		return err
	}
	driver := newForwardOnly0069Driver(baseDriver, dsn)
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		_ = baseDriver.Close()
		return err
	}
	defer m.Close()
	if err := recoverForwardOnly0069DownAttempt(m, driver); err != nil {
		return err
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// postgresMigrationURL validates the single DSN dialect shared by the Core
// pool, the recovery inspector and golang-migrate. migrate-only x-* options
// are deliberately rejected: they can otherwise make the migration driver
// use a different schema or metadata table than the recovery transaction.
func postgresMigrationURL(dsn string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("store: parse postgres migration URL: %w", err)
	}
	if parsed.Opaque != "" ||
		(!strings.EqualFold(parsed.Scheme, "postgres") && !strings.EqualFold(parsed.Scheme, "postgresql")) {
		return "", errors.New("store: postgres migrations require a postgres:// or postgresql:// URL")
	}
	if parsed.Fragment != "" {
		return "", errors.New("store: postgres migration URL must not contain a fragment")
	}
	if parsed.EscapedPath() == "" || parsed.EscapedPath() == "/" {
		return "", errors.New("store: postgres migration URL must name a database")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", fmt.Errorf("store: parse postgres migration URL query: %w", err)
	}
	for key := range query {
		if strings.HasPrefix(strings.ToLower(key), "x-") {
			return "", fmt.Errorf("store: postgres migration URL option %q is not supported", key)
		}
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return "", fmt.Errorf("store: parse postgres migration URL: %w", err)
	}
	for _, fallback := range config.Config.Fallbacks {
		if fallback.Host != config.Config.Host || fallback.Port != config.Config.Port {
			return "", errors.New("store: postgres migrations require a single host and port")
		}
	}
	parsed.Scheme = "pgx5"
	return parsed.String(), nil
}

type forwardOnly0069Recovery func(context.Context, string) error

// forwardOnly0069Driver moves the exceptional metadata repair into
// golang-migrate's advisory-lock critical section. It is armed only after the
// caller observes dirty version 68 and intercepts exactly Force(69)'s
// SetVersion(69,false); all ordinary migration writes are delegated unchanged.
type forwardOnly0069Driver struct {
	database.Driver
	dsn     string
	recover forwardOnly0069Recovery

	mu            sync.Mutex
	recoveryArmed bool
}

func newForwardOnly0069Driver(base database.Driver, dsn string) *forwardOnly0069Driver {
	return &forwardOnly0069Driver{
		Driver:  base,
		dsn:     dsn,
		recover: recoverForwardOnly0069Metadata,
	}
}

func (d *forwardOnly0069Driver) armRecovery() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.recoveryArmed {
		return errors.New("store: forward-only 0069 recovery is already armed")
	}
	d.recoveryArmed = true
	return nil
}

func (d *forwardOnly0069Driver) SetVersion(version int, dirty bool) error {
	d.mu.Lock()
	armed := d.recoveryArmed
	if armed {
		d.recoveryArmed = false
	}
	recoverMetadata := d.recover
	dsn := d.dsn
	d.mu.Unlock()

	if !armed {
		return d.Driver.SetVersion(version, dirty)
	}
	if version != 69 || dirty {
		return fmt.Errorf("store: unexpected migration metadata write while 0069 recovery is armed: version=%d dirty=%t", version, dirty)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return recoverMetadata(ctx, dsn)
}

// recoverForwardOnly0069DownAttempt repairs the one dirty state produced when
// an operator asks golang-migrate to run 0069.down.sql. That migration fails
// deliberately because its monotonic safety and duplicate counters cannot be
// reconstructed. golang-migrate marks version 68 dirty before executing the
// rejected down migration, even though the transactional 0069 schema remains
// intact. A current Core may restore the metadata to clean version 69 only
// after independently proving that every 0069 table, function and trigger is
// still present. No other dirty migration state is forced or hidden.
func recoverForwardOnly0069DownAttempt(m *migrate.Migrate, driver *forwardOnly0069Driver) error {
	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return nil
	}
	if err != nil || !dirty || version != 68 {
		return err
	}
	if err := driver.armRecovery(); err != nil {
		return err
	}
	if err := m.Force(69); err != nil {
		return fmt.Errorf("store: recover rejected 0069 downgrade metadata: %w", err)
	}
	return nil
}

const operationalCounterTriggerPrefix = "require_operational_counter_writer_"

type operationalCounterTriggerSpec struct {
	name   string
	table  string
	tgtype int
}

type operationalCounterTriggerBinding struct {
	operationalCounterTriggerSpec
	functionOID        int64
	relationKind       string
	enabled            string
	argumentCount      int
	argumentBytes      int
	whenExpression     *string
	oldTransitionName  *string
	newTransitionName  *string
	updatedColumns     []int16
	constraintOID      int64
	constraintTableOID int64
	constraintIndexOID int64
	parentTriggerOID   int64
	deferrable         bool
	initiallyDeferred  bool
}

var requiredOperationalCounterTriggers = []operationalCounterTriggerSpec{
	{name: operationalCounterTriggerPrefix + "reconcile", table: "reconcile_runs", tgtype: 31},
	{name: operationalCounterTriggerPrefix + "collection_runs", table: "upstream_collection_runs", tgtype: 31},
	{name: operationalCounterTriggerPrefix + "ingest_batches", table: "upstream_ingest_batches", tgtype: 31},
	{name: operationalCounterTriggerPrefix + "wallets", table: "upstream_wallet_observations", tgtype: 31},
	{name: operationalCounterTriggerPrefix + "offers", table: "upstream_offer_observations", tgtype: 31},
	{name: operationalCounterTriggerPrefix + "changes", table: "upstream_change_events", tgtype: 31},
	{name: operationalCounterTriggerPrefix + "shadow", table: "upstream_shadow_results", tgtype: 31},
	{name: operationalCounterTriggerPrefix + "dry_run", table: "upstream_dry_run_results", tgtype: 31},
	{name: operationalCounterTriggerPrefix + "reconcile_truncate", table: "reconcile_runs", tgtype: 34},
	{name: operationalCounterTriggerPrefix + "collection_runs_truncate", table: "upstream_collection_runs", tgtype: 34},
	{name: operationalCounterTriggerPrefix + "ingest_batches_truncate", table: "upstream_ingest_batches", tgtype: 34},
	{name: operationalCounterTriggerPrefix + "wallets_truncate", table: "upstream_wallet_observations", tgtype: 34},
	{name: operationalCounterTriggerPrefix + "offers_truncate", table: "upstream_offer_observations", tgtype: 34},
	{name: operationalCounterTriggerPrefix + "changes_truncate", table: "upstream_change_events", tgtype: 34},
	{name: operationalCounterTriggerPrefix + "shadow_truncate", table: "upstream_shadow_results", tgtype: 34},
	{name: operationalCounterTriggerPrefix + "dry_run_truncate", table: "upstream_dry_run_results", tgtype: 34},
}

var requiredOperationalCounterTables = []string{
	"operational_event_counters",
	"operational_metric_counters",
	"operational_collection_duration_counters",
	"reconcile_runs",
	"upstream_collection_runs",
	"upstream_ingest_batches",
	"upstream_wallet_observations",
	"upstream_offer_observations",
	"upstream_change_events",
	"upstream_shadow_results",
	"upstream_dry_run_results",
}

func recoverForwardOnly0069Metadata(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: %w", err)
	}
	defer conn.Close(ctx)

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: set lock timeout: %w", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '30s'`); err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: set statement timeout: %w", err)
	}

	var schema string
	if err := tx.QueryRow(ctx, `SELECT CURRENT_SCHEMA()`).Scan(&schema); err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: current schema: %w", err)
	}
	if schema == "" {
		return errors.New("inspect forward-only migration recovery: CURRENT_SCHEMA is empty")
	}
	migrationsTable := pgx.Identifier{schema, "schema_migrations"}.Sanitize()
	if _, err := tx.Exec(ctx, `LOCK TABLE `+migrationsTable+` IN ACCESS EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: lock metadata: %w", err)
	}

	version, dirty, err := readSingleMigrationVersion(ctx, tx, migrationsTable)
	if err != nil {
		return err
	}
	alreadyRecovered, err := validateForwardOnly0069RecoveryState(version, dirty)
	if err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: %w", err)
	}
	if alreadyRecovered {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("inspect forward-only migration recovery: commit concurrent recovery: %w", err)
		}
		return nil
	}
	lockedTables := make([]string, 0, len(requiredOperationalCounterTables))
	for _, table := range requiredOperationalCounterTables {
		lockedTables = append(lockedTables, pgx.Identifier{schema, table}.Sanitize())
	}
	// ACCESS EXCLUSIVE conflicts with DDL and all table users. Holding it until
	// commit freezes every table/trigger contract while metadata is repaired.
	if _, err := tx.Exec(ctx, `LOCK TABLE `+strings.Join(lockedTables, ", ")+` IN ACCESS EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: lock 0069 tables: %w", err)
	}
	if err := validateOperationalCounterTableContracts(ctx, tx, schema); err != nil {
		return err
	}
	if err := validateOperationalCounterCanonicalContracts(ctx, tx, schema); err != nil {
		return err
	}

	functionOID, err := operationalCounterWriterFunctionOID(ctx, tx, schema)
	if err != nil {
		return err
	}
	// PostgreSQL has no LOCK FUNCTION command. A no-op ALTER obtains and holds
	// the function's object-level AccessExclusiveLock until this transaction
	// commits. The contract is validated both before and after acquiring it so
	// a concurrent CREATE OR REPLACE cannot pass through the validation window.
	functionName := pgx.Identifier{schema, "require_operational_counter_writer"}.Sanitize()
	if _, err := tx.Exec(ctx, `ALTER FUNCTION `+functionName+`() PARALLEL UNSAFE`); err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: lock writer function: %w", err)
	}
	lockedFunctionOID, err := operationalCounterWriterFunctionOID(ctx, tx, schema)
	if err != nil {
		return err
	}
	if lockedFunctionOID != functionOID {
		return fmt.Errorf("inspect forward-only migration recovery: writer function changed from oid=%d to oid=%d", functionOID, lockedFunctionOID)
	}
	bindings, err := operationalCounterTriggerBindings(ctx, tx, schema)
	if err != nil {
		return err
	}
	if err := validateOperationalCounterTriggerBindings(bindings, functionOID); err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: %w", err)
	}

	tag, err := tx.Exec(ctx, `UPDATE `+migrationsTable+`
		SET version=69, dirty=false WHERE version=68 AND dirty`)
	if err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: update metadata: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("inspect forward-only migration recovery: updated %d metadata rows, want 1", tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: commit: %w", err)
	}
	return nil
}

func validateForwardOnly0069RecoveryState(version int, dirty bool) (bool, error) {
	if version == 69 && !dirty {
		return true, nil
	}
	if version != 68 || !dirty {
		return false, fmt.Errorf("metadata changed to version=%d dirty=%t", version, dirty)
	}
	return false, nil
}

type operationalCounterColumnSpec struct {
	table      string
	column     string
	dataType   string
	notNull    bool
	primaryKey bool
	defaultSQL string
}

var requiredOperationalCounterColumns = []operationalCounterColumnSpec{
	{table: "operational_event_counters", column: "kind", dataType: "text", notNull: true, primaryKey: true},
	{table: "operational_event_counters", column: "total", dataType: "bigint", notNull: true, defaultSQL: "0"},
	{table: "operational_event_counters", column: "updated_at", dataType: "timestamp with time zone", notNull: true, defaultSQL: "statement_timestamp()"},
	{table: "operational_metric_counters", column: "metric", dataType: "text", notNull: true, primaryKey: true},
	{table: "operational_metric_counters", column: "label", dataType: "text", notNull: true, primaryKey: true},
	{table: "operational_metric_counters", column: "total", dataType: "bigint", notNull: true, defaultSQL: "0"},
	{table: "operational_metric_counters", column: "updated_at", dataType: "timestamp with time zone", notNull: true, defaultSQL: "statement_timestamp()"},
	{table: "operational_collection_duration_counters", column: "result", dataType: "text", notNull: true, primaryKey: true},
	{table: "operational_collection_duration_counters", column: "count", dataType: "bigint", notNull: true, defaultSQL: "0"},
	{table: "operational_collection_duration_counters", column: "sum_seconds", dataType: "double precision", notNull: true, defaultSQL: "0"},
	{table: "operational_collection_duration_counters", column: "le_0_1", dataType: "bigint", notNull: true, defaultSQL: "0"},
	{table: "operational_collection_duration_counters", column: "le_0_5", dataType: "bigint", notNull: true, defaultSQL: "0"},
	{table: "operational_collection_duration_counters", column: "le_1", dataType: "bigint", notNull: true, defaultSQL: "0"},
	{table: "operational_collection_duration_counters", column: "le_2", dataType: "bigint", notNull: true, defaultSQL: "0"},
	{table: "operational_collection_duration_counters", column: "le_5", dataType: "bigint", notNull: true, defaultSQL: "0"},
	{table: "operational_collection_duration_counters", column: "le_10", dataType: "bigint", notNull: true, defaultSQL: "0"},
	{table: "operational_collection_duration_counters", column: "le_30", dataType: "bigint", notNull: true, defaultSQL: "0"},
	{table: "operational_collection_duration_counters", column: "le_60", dataType: "bigint", notNull: true, defaultSQL: "0"},
	{table: "operational_collection_duration_counters", column: "le_300", dataType: "bigint", notNull: true, defaultSQL: "0"},
	{table: "operational_collection_duration_counters", column: "updated_at", dataType: "timestamp with time zone", notNull: true, defaultSQL: "statement_timestamp()"},
}

var requiredOperationalCounterCheckFragments = map[string][]string{
	"operational_event_counters": {
		"credential_leak_detected", "cross_owner_rejected", "false_removal_invariant", "total >= 0",
	},
	"operational_metric_counters": {
		"btrim(label)", "char_length(label) <= 64", "total >= 0",
		"collection_runs", "collection_facts", "collection_coverage", "ingest_facts",
		"change_events", "reconcile_runs", "experiments",
		"succeeded", "partial", "failed", "complete", "unavailable", "accepted", "duplicate",
		"balance_low", "balance_recovered", "group_added", "group_changed", "group_removed",
		"model_added", "price_increased", "price_decreased", "model_removed", "source_stale", "source_recovered",
		"dry_run", "apply", "rollback", "shadow",
	},
	"operational_collection_duration_counters": {
		"succeeded", "partial", "failed", "count >= 0", "sum_seconds >= (0)::double precision",
		"le_0_1 >= 0", "le_0_5 >= 0", "le_1 >= 0", "le_2 >= 0", "le_5 >= 0",
		"le_10 >= 0", "le_30 >= 0", "le_60 >= 0", "le_300 >= 0",
	},
}

var requiredOperationalCounterConstraintCounts = map[string]int{
	"operational_event_counters":               2,
	"operational_metric_counters":              4,
	"operational_collection_duration_counters": 12,
}

var requiredOperationalCounterPrimaryKeys = map[string][]string{
	"operational_event_counters":               {"kind"},
	"operational_metric_counters":              {"metric", "label"},
	"operational_collection_duration_counters": {"result"},
}

func validateOperationalCounterTableContracts(ctx context.Context, tx pgx.Tx, schema string) error {
	rows, err := tx.Query(ctx, `SELECT relation.relname, attribute.attname,
		pg_catalog.format_type(attribute.atttypid,attribute.atttypmod), attribute.attnotnull,
		COALESCE(pg_catalog.pg_get_expr(default_value.adbin,default_value.adrelid),''),
		EXISTS (
			SELECT 1 FROM pg_catalog.pg_index index
			WHERE index.indrelid=relation.oid AND index.indisprimary
			  AND attribute.attnum=ANY(index.indkey)
		)
		FROM pg_catalog.pg_class relation
		JOIN pg_catalog.pg_namespace namespace ON namespace.oid=relation.relnamespace
		JOIN pg_catalog.pg_attribute attribute ON attribute.attrelid=relation.oid
		LEFT JOIN pg_catalog.pg_attrdef default_value
		  ON default_value.adrelid=attribute.attrelid AND default_value.adnum=attribute.attnum
		WHERE namespace.nspname=$1
		  AND relation.relname=ANY($2::text[])
		  AND relation.relkind='r'
		  AND attribute.attnum>0 AND NOT attribute.attisdropped`, schema,
		[]string{"operational_event_counters", "operational_metric_counters", "operational_collection_duration_counters"})
	if err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: list counter columns: %w", err)
	}
	defer rows.Close()
	actual := make(map[string]operationalCounterActualColumn, len(requiredOperationalCounterColumns))
	for rows.Next() {
		var table, column string
		var got operationalCounterActualColumn
		if err := rows.Scan(&table, &column, &got.dataType, &got.notNull, &got.defaultSQL, &got.primaryKey); err != nil {
			return fmt.Errorf("inspect forward-only migration recovery: scan counter column: %w", err)
		}
		actual[table+"."+column] = got
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: list counter columns: %w", err)
	}
	if err := validateOperationalCounterColumns(actual); err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: %w", err)
	}

	for table, fragments := range requiredOperationalCounterCheckFragments {
		var definitions string
		var checkCount int
		var allValidated bool
		var anyNoInherit bool
		err := tx.QueryRow(ctx, `SELECT
			COALESCE(string_agg(pg_catalog.pg_get_constraintdef(constraint_def.oid),E'\n'), ''),
			COUNT(*)::integer, COALESCE(bool_and(constraint_def.convalidated),false),
			COALESCE(bool_or(constraint_def.connoinherit),false)
			FROM pg_catalog.pg_constraint constraint_def
			JOIN pg_catalog.pg_class relation ON relation.oid=constraint_def.conrelid
			JOIN pg_catalog.pg_namespace namespace ON namespace.oid=relation.relnamespace
			WHERE namespace.nspname=$1 AND relation.relname=$2 AND constraint_def.contype='c'`, schema, table).
			Scan(&definitions, &checkCount, &allValidated, &anyNoInherit)
		if err != nil {
			return fmt.Errorf("inspect forward-only migration recovery: read %s checks: %w", table, err)
		}
		if checkCount != requiredOperationalCounterConstraintCounts[table] || !allValidated || anyNoInherit {
			return fmt.Errorf("inspect forward-only migration recovery: %s has checks=%d validated=%t no_inherit=%t",
				table, checkCount, allValidated, anyNoInherit)
		}
		if err := validateOperationalCounterCheckDefinition(table, definitions, fragments); err != nil {
			return fmt.Errorf("inspect forward-only migration recovery: %w", err)
		}

		var primaryKeyColumns []string
		var constraintCount int
		var primaryValidated bool
		var primaryDeferrable bool
		var primaryInitiallyDeferred bool
		err = tx.QueryRow(ctx, `SELECT
			COALESCE((
				SELECT array_agg(attribute.attname ORDER BY key_column.ordinality)
				FROM pg_catalog.pg_constraint primary_key
				CROSS JOIN LATERAL unnest(primary_key.conkey) WITH ORDINALITY key_column(attnum,ordinality)
				JOIN pg_catalog.pg_attribute attribute
				  ON attribute.attrelid=primary_key.conrelid AND attribute.attnum=key_column.attnum
				WHERE primary_key.conrelid=relation.oid AND primary_key.contype='p'
			),ARRAY[]::text[]),
			COUNT(*)::integer,
			COALESCE(bool_and(constraint_def.convalidated) FILTER (WHERE constraint_def.contype='p'),false),
			COALESCE(bool_or(constraint_def.condeferrable) FILTER (WHERE constraint_def.contype='p'),false),
			COALESCE(bool_or(constraint_def.condeferred) FILTER (WHERE constraint_def.contype='p'),false)
			FROM pg_catalog.pg_constraint constraint_def
			JOIN pg_catalog.pg_class relation ON relation.oid=constraint_def.conrelid
			JOIN pg_catalog.pg_namespace namespace ON namespace.oid=relation.relnamespace
			WHERE namespace.nspname=$1 AND relation.relname=$2
			GROUP BY relation.oid`, schema, table).
			Scan(&primaryKeyColumns, &constraintCount, &primaryValidated, &primaryDeferrable, &primaryInitiallyDeferred)
		if err != nil {
			return fmt.Errorf("inspect forward-only migration recovery: read %s constraints: %w", table, err)
		}
		if !slices.Equal(primaryKeyColumns, requiredOperationalCounterPrimaryKeys[table]) ||
			constraintCount != requiredOperationalCounterConstraintCounts[table]+1 ||
			!primaryValidated || primaryDeferrable || primaryInitiallyDeferred {
			return fmt.Errorf("inspect forward-only migration recovery: %s has primary_key=%v constraints=%d validated=%t deferrable=%t/%t",
				table, primaryKeyColumns, constraintCount, primaryValidated, primaryDeferrable, primaryInitiallyDeferred)
		}
	}
	return nil
}

func validateOperationalCounterCanonicalContracts(ctx context.Context, tx pgx.Tx, schema string) error {
	// PostgreSQL is the canonicalizer. Temporary 0069 definitions produce the
	// server-version-specific default and constraint expression trees; target
	// tables must match those trees exactly, not merely contain string tokens.
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE e2m_0069_expected_event (
		kind TEXT PRIMARY KEY CHECK (kind IN ('credential_leak_detected','cross_owner_rejected','false_removal_invariant')),
		total BIGINT NOT NULL DEFAULT 0 CHECK (total >= 0),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
	) ON COMMIT DROP;
	CREATE TEMP TABLE e2m_0069_expected_metric (
		metric TEXT NOT NULL CHECK (metric IN ('collection_runs','collection_facts','collection_coverage','ingest_facts','change_events','reconcile_runs','experiments')),
		label TEXT NOT NULL CHECK (BTRIM(label) <> '' AND char_length(label) <= 64),
		total BIGINT NOT NULL DEFAULT 0 CHECK (total >= 0),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
		PRIMARY KEY (metric,label),
		CHECK (
			(metric IN ('collection_runs','collection_facts') AND label IN ('succeeded','partial','failed')) OR
			(metric='collection_coverage' AND label IN ('complete','partial','unavailable')) OR
			(metric='ingest_facts' AND label IN ('accepted','duplicate')) OR
			(metric='change_events' AND label IN ('balance_low','balance_recovered','group_added','group_changed','group_removed','model_added','price_increased','price_decreased','model_removed','source_stale','source_recovered')) OR
			(metric='reconcile_runs' AND label IN ('dry_run','apply','rollback')) OR
			(metric='experiments' AND label IN ('shadow','dry_run'))
		)
	) ON COMMIT DROP;
	CREATE TEMP TABLE e2m_0069_expected_duration (
		result TEXT PRIMARY KEY CHECK (result IN ('succeeded','partial','failed')),
		count BIGINT NOT NULL DEFAULT 0 CHECK (count >= 0),
		sum_seconds DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (sum_seconds >= 0),
		le_0_1 BIGINT NOT NULL DEFAULT 0 CHECK (le_0_1 >= 0),
		le_0_5 BIGINT NOT NULL DEFAULT 0 CHECK (le_0_5 >= 0),
		le_1 BIGINT NOT NULL DEFAULT 0 CHECK (le_1 >= 0),
		le_2 BIGINT NOT NULL DEFAULT 0 CHECK (le_2 >= 0),
		le_5 BIGINT NOT NULL DEFAULT 0 CHECK (le_5 >= 0),
		le_10 BIGINT NOT NULL DEFAULT 0 CHECK (le_10 >= 0),
		le_30 BIGINT NOT NULL DEFAULT 0 CHECK (le_30 >= 0),
		le_60 BIGINT NOT NULL DEFAULT 0 CHECK (le_60 >= 0),
		le_300 BIGINT NOT NULL DEFAULT 0 CHECK (le_300 >= 0),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
	) ON COMMIT DROP`); err != nil {
		return fmt.Errorf("inspect forward-only migration recovery: build canonical counter contracts: %w", err)
	}
	for _, pair := range []struct{ target, expected string }{
		{target: "operational_event_counters", expected: "e2m_0069_expected_event"},
		{target: "operational_metric_counters", expected: "e2m_0069_expected_metric"},
		{target: "operational_collection_duration_counters", expected: "e2m_0069_expected_duration"},
	} {
		var complete bool
		err := tx.QueryRow(ctx, `WITH target AS (
			SELECT relation.oid FROM pg_catalog.pg_class relation
			JOIN pg_catalog.pg_namespace namespace ON namespace.oid=relation.relnamespace
			WHERE namespace.nspname=$1 AND relation.relname=$2 AND relation.relkind='r'
		), expected AS (
			SELECT relation.oid FROM pg_catalog.pg_class relation
			WHERE relation.relnamespace=pg_my_temp_schema() AND relation.relname=$3 AND relation.relkind='r'
		), target_columns AS (
			SELECT attribute.attname, pg_catalog.format_type(attribute.atttypid,attribute.atttypmod) data_type,
				attribute.attnotnull, COALESCE(pg_catalog.pg_get_expr(default_value.adbin,default_value.adrelid),'') default_sql
			FROM target JOIN pg_catalog.pg_attribute attribute ON attribute.attrelid=target.oid
			LEFT JOIN pg_catalog.pg_attrdef default_value ON default_value.adrelid=attribute.attrelid AND default_value.adnum=attribute.attnum
			WHERE attribute.attnum>0 AND NOT attribute.attisdropped
		), expected_columns AS (
			SELECT attribute.attname, pg_catalog.format_type(attribute.atttypid,attribute.atttypmod) data_type,
				attribute.attnotnull, COALESCE(pg_catalog.pg_get_expr(default_value.adbin,default_value.adrelid),'') default_sql
			FROM expected JOIN pg_catalog.pg_attribute attribute ON attribute.attrelid=expected.oid
			LEFT JOIN pg_catalog.pg_attrdef default_value ON default_value.adrelid=attribute.attrelid AND default_value.adnum=attribute.attnum
			WHERE attribute.attnum>0 AND NOT attribute.attisdropped
		), target_constraints AS (
			SELECT constraint_def.contype,
				CASE WHEN constraint_def.contype='c' THEN pg_catalog.pg_get_expr(constraint_def.conbin,constraint_def.conrelid)
				     ELSE array_to_string(ARRAY(SELECT attribute.attname FROM unnest(constraint_def.conkey) WITH ORDINALITY key(attnum,position)
				          JOIN pg_catalog.pg_attribute attribute ON attribute.attrelid=constraint_def.conrelid AND attribute.attnum=key.attnum ORDER BY key.position),',') END definition,
				constraint_def.convalidated, constraint_def.condeferrable, constraint_def.condeferred, constraint_def.connoinherit
			FROM target JOIN pg_catalog.pg_constraint constraint_def ON constraint_def.conrelid=target.oid
		), expected_constraints AS (
			SELECT constraint_def.contype,
				CASE WHEN constraint_def.contype='c' THEN pg_catalog.pg_get_expr(constraint_def.conbin,constraint_def.conrelid)
				     ELSE array_to_string(ARRAY(SELECT attribute.attname FROM unnest(constraint_def.conkey) WITH ORDINALITY key(attnum,position)
				          JOIN pg_catalog.pg_attribute attribute ON attribute.attrelid=constraint_def.conrelid AND attribute.attnum=key.attnum ORDER BY key.position),',') END definition,
				constraint_def.convalidated, constraint_def.condeferrable, constraint_def.condeferred, constraint_def.connoinherit
			FROM expected JOIN pg_catalog.pg_constraint constraint_def ON constraint_def.conrelid=expected.oid
		)
		SELECT NOT EXISTS ((TABLE target_columns EXCEPT ALL TABLE expected_columns) UNION ALL (TABLE expected_columns EXCEPT ALL TABLE target_columns))
		   AND NOT EXISTS ((TABLE target_constraints EXCEPT ALL TABLE expected_constraints) UNION ALL (TABLE expected_constraints EXCEPT ALL TABLE target_constraints))`,
			schema, pair.target, pair.expected).Scan(&complete)
		if err != nil {
			return fmt.Errorf("inspect forward-only migration recovery: compare canonical %s contract: %w", pair.target, err)
		}
		if !complete {
			return fmt.Errorf("inspect forward-only migration recovery: %s differs from canonical 0069 contract", pair.target)
		}
	}
	return nil
}

type operationalCounterActualColumn struct {
	dataType   string
	notNull    bool
	primaryKey bool
	defaultSQL string
}

func validateOperationalCounterColumns(actual map[string]operationalCounterActualColumn) error {
	if len(actual) != len(requiredOperationalCounterColumns) {
		return fmt.Errorf("found %d counter columns, want %d", len(actual), len(requiredOperationalCounterColumns))
	}
	for _, want := range requiredOperationalCounterColumns {
		key := want.table + "." + want.column
		got, ok := actual[key]
		if !ok {
			return fmt.Errorf("counter column %s is missing", key)
		}
		if got.dataType != want.dataType || got.notNull != want.notNull ||
			got.primaryKey != want.primaryKey || got.defaultSQL != want.defaultSQL {
			return fmt.Errorf("counter column %s.%s has type=%q not_null=%t primary_key=%t default=%q",
				want.table, want.column, got.dataType, got.notNull, got.primaryKey, got.defaultSQL)
		}
	}
	return nil
}

func validateOperationalCounterCheckDefinition(table, definition string, fragments []string) error {
	for _, fragment := range fragments {
		if !strings.Contains(definition, fragment) {
			return fmt.Errorf("%s checks lack %q", table, fragment)
		}
	}
	return nil
}

func readSingleMigrationVersion(ctx context.Context, tx pgx.Tx, migrationsTable string) (int, bool, error) {
	rows, err := tx.Query(ctx, `SELECT version, dirty FROM `+migrationsTable+` FOR UPDATE`)
	if err != nil {
		return 0, false, fmt.Errorf("inspect forward-only migration recovery: read metadata: %w", err)
	}
	defer rows.Close()
	var version int
	var dirty bool
	count := 0
	for rows.Next() {
		if err := rows.Scan(&version, &dirty); err != nil {
			return 0, false, fmt.Errorf("inspect forward-only migration recovery: scan metadata: %w", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, false, fmt.Errorf("inspect forward-only migration recovery: read metadata: %w", err)
	}
	if count != 1 {
		return 0, false, fmt.Errorf("inspect forward-only migration recovery: metadata has %d rows, want 1", count)
	}
	return version, dirty, nil
}

func operationalCounterWriterFunctionOID(ctx context.Context, tx pgx.Tx, schema string) (int64, error) {
	var oid int64
	var body string
	var language string
	var securityDefiner bool
	var leakProof bool
	var strict bool
	var volatility string
	var parallelSafety string
	var config []string
	err := tx.QueryRow(ctx, `SELECT procedure.oid::bigint, procedure.prosrc,
		language.lanname, procedure.prosecdef, procedure.proleakproof,
		procedure.proisstrict, procedure.provolatile::text, procedure.proparallel::text,
		COALESCE(procedure.proconfig,ARRAY[]::text[])
		FROM pg_catalog.pg_proc procedure
		JOIN pg_catalog.pg_namespace namespace ON namespace.oid=procedure.pronamespace
		JOIN pg_catalog.pg_language language ON language.oid=procedure.prolang
		WHERE namespace.nspname=$1
		  AND procedure.proname='require_operational_counter_writer'
		  AND procedure.pronargs=0
		  AND procedure.prokind='f'
		  AND procedure.prorettype='pg_catalog.trigger'::pg_catalog.regtype`, schema).
		Scan(&oid, &body, &language, &securityDefiner, &leakProof, &strict, &volatility, &parallelSafety, &config)
	if err != nil {
		return 0, fmt.Errorf("inspect forward-only migration recovery: writer function: %w", err)
	}
	if language != "plpgsql" || securityDefiner || leakProof || strict ||
		volatility != "v" || parallelSafety != "u" || len(config) != 0 {
		return 0, fmt.Errorf("inspect forward-only migration recovery: writer function has language=%q security_definer=%t leakproof=%t strict=%t volatility=%q parallel=%q config=%v",
			language, securityDefiner, leakProof, strict, volatility, parallelSafety, config)
	}
	if normalizeSQLTokens(body) != normalizeSQLTokens(requiredOperationalCounterWriterBody) {
		return 0, errors.New("inspect forward-only migration recovery: writer function body differs from 0069 contract")
	}
	return oid, nil
}

const requiredOperationalCounterWriterBody = `
BEGIN
    IF current_setting('e2m.operational_counter_writer', true)
       IS DISTINCT FROM 'incremental-v1' THEN
        RAISE EXCEPTION USING
            ERRCODE = '55000',
            MESSAGE = 'operational counter writer compatibility fence',
            HINT = 'stop pre-0069 Core processes and use a current Core database connection';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
	IF TG_LEVEL = 'STATEMENT' THEN
		RETURN NULL;
	END IF;
    RETURN NEW;
END;
`

func normalizeSQLTokens(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func operationalCounterTriggerBindings(ctx context.Context, tx pgx.Tx, schema string) ([]operationalCounterTriggerBinding, error) {
	rows, err := tx.Query(ctx, `SELECT trigger.tgname, relation.relname,
		trigger.tgtype::integer, relation.relkind::text, trigger.tgenabled::text, procedure.oid::bigint,
		trigger.tgnargs::integer, octet_length(trigger.tgargs),
		pg_catalog.pg_get_expr(trigger.tgqual,trigger.tgrelid),
		trigger.tgoldtable, trigger.tgnewtable, trigger.tgattr::smallint[],
		trigger.tgconstraint::bigint, trigger.tgconstrrelid::bigint,
		trigger.tgconstrindid::bigint, trigger.tgparentid::bigint,
		trigger.tgdeferrable, trigger.tginitdeferred
		FROM pg_catalog.pg_trigger trigger
		JOIN pg_catalog.pg_class relation ON relation.oid=trigger.tgrelid
		JOIN pg_catalog.pg_namespace namespace ON namespace.oid=relation.relnamespace
		JOIN pg_catalog.pg_proc procedure ON procedure.oid=trigger.tgfoid
		WHERE namespace.nspname=$1
		  AND NOT trigger.tgisinternal
		  AND left(trigger.tgname,length($2))=$2`, schema, operationalCounterTriggerPrefix)
	if err != nil {
		return nil, fmt.Errorf("inspect forward-only migration recovery: list writer triggers: %w", err)
	}
	defer rows.Close()
	bindings := make([]operationalCounterTriggerBinding, 0, len(requiredOperationalCounterTriggers))
	for rows.Next() {
		var binding operationalCounterTriggerBinding
		if err := rows.Scan(&binding.name, &binding.table, &binding.tgtype, &binding.relationKind,
			&binding.enabled, &binding.functionOID,
			&binding.argumentCount, &binding.argumentBytes, &binding.whenExpression,
			&binding.oldTransitionName, &binding.newTransitionName, &binding.updatedColumns,
			&binding.constraintOID, &binding.constraintTableOID, &binding.constraintIndexOID,
			&binding.parentTriggerOID, &binding.deferrable, &binding.initiallyDeferred); err != nil {
			return nil, fmt.Errorf("inspect forward-only migration recovery: scan writer trigger: %w", err)
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect forward-only migration recovery: list writer triggers: %w", err)
	}
	return bindings, nil
}

func validateOperationalCounterTriggerBindings(bindings []operationalCounterTriggerBinding, functionOID int64) error {
	if len(bindings) != len(requiredOperationalCounterTriggers) {
		return fmt.Errorf("found %d writer triggers, want %d", len(bindings), len(requiredOperationalCounterTriggers))
	}
	expected := make(map[string]operationalCounterTriggerSpec, len(requiredOperationalCounterTriggers))
	for _, spec := range requiredOperationalCounterTriggers {
		expected[spec.name] = spec
	}
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		spec, ok := expected[binding.name]
		if !ok {
			return fmt.Errorf("unexpected writer trigger %q", binding.name)
		}
		if _, duplicate := seen[binding.name]; duplicate {
			return fmt.Errorf("duplicate writer trigger %q", binding.name)
		}
		seen[binding.name] = struct{}{}
		// Only the ordinary enabled state is accepted. Replica/always modes can
		// change when the fence fires and are not the exact 0069 contract.
		if binding.table != spec.table || binding.relationKind != "r" || binding.tgtype != spec.tgtype ||
			binding.enabled != "O" || binding.functionOID != functionOID ||
			binding.argumentCount != 0 || binding.argumentBytes != 0 ||
			binding.whenExpression != nil || binding.oldTransitionName != nil || binding.newTransitionName != nil ||
			len(binding.updatedColumns) != 0 ||
			binding.constraintOID != 0 || binding.constraintTableOID != 0 || binding.constraintIndexOID != 0 ||
			binding.parentTriggerOID != 0 || binding.deferrable || binding.initiallyDeferred {
			return fmt.Errorf("writer trigger %q has table=%q kind=%q tgtype=%d enabled=%q function_oid=%d args=%d/%d when=%v old_table=%v new_table=%v columns=%v constraint=%d/%d/%d parent=%d deferrable=%t/%t",
				binding.name, binding.table, binding.relationKind, binding.tgtype, binding.enabled, binding.functionOID,
				binding.argumentCount, binding.argumentBytes, binding.whenExpression,
				binding.oldTransitionName, binding.newTransitionName, binding.updatedColumns,
				binding.constraintOID, binding.constraintTableOID, binding.constraintIndexOID,
				binding.parentTriggerOID, binding.deferrable, binding.initiallyDeferred)
		}
	}
	return nil
}

func marshalLabels(labels map[string]string) ([]byte, error) {
	if labels == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(labels)
}

const instanceCols = `id, user_id, name, kind, status,
	COALESCE(connector_id, ''), created_at, updated_at`

func scanInstance(row rowScanner) (contracts.Instance, error) {
	var in contracts.Instance
	var kind, status string
	if err := row.Scan(
		&in.ID, &in.UserID, &in.Name, &kind, &status,
		&in.ConnectorID, &in.CreatedAt, &in.UpdatedAt,
	); err != nil {
		return contracts.Instance{}, err
	}
	in.Kind = contracts.InstanceKind(kind)
	in.Status = contracts.InstanceStatus(status)
	return in, nil
}

func (s *PostgresStore) CreateInstance(ctx context.Context, input contracts.Instance) (contracts.Instance, error) {
	in := input
	in.ID = newID("inst")
	if in.Status == "" {
		in.Status = contracts.InstanceStatusUnknown
	}
	now := time.Now().UTC()
	in.CreatedAt, in.UpdatedAt = now, now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO instances (id, user_id, name, kind, status, connector_id, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8)`,
		in.ID, in.UserID, in.Name, string(in.Kind), string(in.Status),
		in.ConnectorID, in.CreatedAt, in.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.Instance{}, ErrDuplicate
		}
		return contracts.Instance{}, err
	}
	return in, nil
}

func (s *PostgresStore) GetInstance(ctx context.Context, id string) (contracts.Instance, error) {
	in, err := scanInstance(s.pool.QueryRow(ctx, `SELECT `+instanceCols+` FROM instances WHERE id=$1`, id))
	if err != nil {
		return contracts.Instance{}, mapNotFound(err)
	}
	return in, nil
}

func (s *PostgresStore) UpdateInstance(ctx context.Context, input contracts.Instance) (contracts.Instance, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE instances
		 SET name=$2, kind=$3, status=$4, connector_id=NULLIF($5,''), updated_at=now()
		 WHERE id=$1
		 RETURNING `+instanceCols,
		input.ID, input.Name, string(input.Kind), string(input.Status), input.ConnectorID)
	in, err := scanInstance(row)
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.Instance{}, ErrDuplicate
		}
		return contracts.Instance{}, mapNotFound(err)
	}
	return in, nil
}

func (s *PostgresStore) UpdateInstanceConnector(ctx context.Context, id, connectorID string) (contracts.Instance, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.Instance{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var connectorUserID int64
	var connectorInstanceID string
	var connectorStatus contracts.ConnectorStatus
	if connectorID != "" {
		var status string
		err = tx.QueryRow(ctx,
			`SELECT user_id, instance_id, status FROM connectors WHERE connector_id=$1 FOR UPDATE`, connectorID,
		).Scan(&connectorUserID, &connectorInstanceID, &status)
		if err != nil {
			if errors.Is(err, pgxNoRows()) {
				return contracts.Instance{}, ErrNotFound
			}
			return contracts.Instance{}, err
		}
		connectorStatus = contracts.ConnectorStatus(status)
	}

	// Lock connectors before instances, matching RevokeConnector's lock order.
	// The status read and the binding write then belong to one serialization
	// point, so a connector cannot be rebound after it has been revoked.
	current, err := scanInstance(tx.QueryRow(ctx, `SELECT `+instanceCols+` FROM instances WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return contracts.Instance{}, mapNotFound(err)
	}
	if connectorID != "" && (connectorStatus == contracts.ConnectorStatusRevoked ||
		connectorUserID != current.UserID || connectorInstanceID != current.ID) {
		return contracts.Instance{}, ErrConflict
	}

	updated, err := scanInstance(tx.QueryRow(ctx,
		`UPDATE instances SET connector_id=NULLIF($2,''), updated_at=now()
		 WHERE id=$1 RETURNING `+instanceCols,
		id, connectorID))
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.Instance{}, ErrDuplicate
		}
		return contracts.Instance{}, mapNotFound(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.Instance{}, err
	}
	return updated, nil
}

func (s *PostgresStore) ListInstances(ctx context.Context, userID int64) ([]contracts.Instance, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+instanceCols+` FROM instances WHERE ($1=0 OR user_id=$1) ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.Instance
	for rows.Next() {
		in, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CreateSupplyOffer(ctx context.Context, input contracts.SupplyOffer) (contracts.SupplyOffer, error) {
	o := input
	if o.ID == "" {
		o.ID = newID("offer")
	}
	if o.Status == "" {
		o.Status = contracts.SupplyOfferStatusPending
	}
	now := time.Now().UTC()
	o.CreatedAt, o.UpdatedAt = now, now
	labels, err := marshalLabels(o.Labels)
	if err != nil {
		return contracts.SupplyOffer{}, err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO supply_offers (id, supplier_user_id, kind, provider, credential_ref, proxy_ref, status, quota, unit_price, labels, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		o.ID, o.SupplierUserID, string(o.Kind), o.Provider, o.CredentialRef, o.ProxyRef, string(o.Status), o.Quota, o.UnitPrice, labels, o.CreatedAt, o.UpdatedAt)
	if err != nil {
		return contracts.SupplyOffer{}, err
	}
	return o, nil
}

func (s *PostgresStore) ListSupplyOffers(ctx context.Context, supplierUserID int64) ([]contracts.SupplyOffer, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, supplier_user_id, kind, provider, credential_ref, proxy_ref, status, quota, unit_price, labels, created_at, updated_at
		 FROM supply_offers WHERE ($1=0 OR supplier_user_id=$1) ORDER BY created_at`, supplierUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.SupplyOffer
	for rows.Next() {
		var o contracts.SupplyOffer
		var kind, status string
		var labels []byte
		if err := rows.Scan(&o.ID, &o.SupplierUserID, &kind, &o.Provider, &o.CredentialRef, &o.ProxyRef, &status, &o.Quota, &o.UnitPrice, &labels, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		o.Kind = contracts.SupplyOfferKind(kind)
		o.Status = contracts.SupplyOfferStatus(status)
		o.Labels = unmarshalLabels(labels)
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetSupplyOffer(ctx context.Context, id string) (contracts.SupplyOffer, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, supplier_user_id, kind, provider, credential_ref, proxy_ref, status, quota, unit_price, labels, created_at, updated_at
		 FROM supply_offers WHERE id=$1`, id)
	var o contracts.SupplyOffer
	var kind, status string
	var labels []byte
	if err := row.Scan(&o.ID, &o.SupplierUserID, &kind, &o.Provider, &o.CredentialRef, &o.ProxyRef, &status, &o.Quota, &o.UnitPrice, &labels, &o.CreatedAt, &o.UpdatedAt); err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.SupplyOffer{}, ErrNotFound
		}
		return contracts.SupplyOffer{}, err
	}
	o.Kind = contracts.SupplyOfferKind(kind)
	o.Status = contracts.SupplyOfferStatus(status)
	o.Labels = unmarshalLabels(labels)
	return o, nil
}

func (s *PostgresStore) UpdateSupplyOffer(ctx context.Context, input contracts.SupplyOffer) (contracts.SupplyOffer, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.SupplyOffer{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM supply_offers WHERE id=$1 FOR UPDATE`, input.ID).Scan(&status); err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.SupplyOffer{}, ErrNotFound
		}
		return contracts.SupplyOffer{}, err
	}
	if contracts.SupplyOfferStatus(status) == contracts.SupplyOfferStatusRevoked {
		return contracts.SupplyOffer{}, ErrConflict
	}
	labels, err := marshalLabels(input.Labels)
	if err != nil {
		return contracts.SupplyOffer{}, err
	}
	row := tx.QueryRow(ctx,
		`UPDATE supply_offers
		 SET kind=$2, provider=$3, credential_ref=$4, proxy_ref=$5, quota=$6, unit_price=$7, labels=$8, updated_at=now()
		 WHERE id=$1
		 RETURNING id, supplier_user_id, kind, provider, credential_ref, proxy_ref, status, quota, unit_price, labels, created_at, updated_at`,
		input.ID, string(input.Kind), input.Provider, input.CredentialRef, input.ProxyRef, input.Quota, input.UnitPrice, labels)
	var updated contracts.SupplyOffer
	var kind string
	var encodedLabels []byte
	if err := row.Scan(&updated.ID, &updated.SupplierUserID, &kind, &updated.Provider, &updated.CredentialRef, &updated.ProxyRef, &status, &updated.Quota, &updated.UnitPrice, &encodedLabels, &updated.CreatedAt, &updated.UpdatedAt); err != nil {
		return contracts.SupplyOffer{}, err
	}
	updated.Kind = contracts.SupplyOfferKind(kind)
	updated.Status = contracts.SupplyOfferStatus(status)
	updated.Labels = unmarshalLabels(encodedLabels)
	if err := tx.Commit(ctx); err != nil {
		return contracts.SupplyOffer{}, err
	}
	return updated, nil
}

func (s *PostgresStore) UpdateSupplyOfferStatus(ctx context.Context, id string, status contracts.SupplyOfferStatus) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE supply_offers SET status=$2, updated_at=now() WHERE id=$1`, id, string(status))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) RevokeSupplyOffer(ctx context.Context, id string) (contracts.SupplyOffer, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.SupplyOffer{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx,
		`SELECT id, supplier_user_id, kind, provider, credential_ref, proxy_ref, status, quota, unit_price, labels, created_at, updated_at
		 FROM supply_offers WHERE id=$1 FOR UPDATE`, id)
	var offer contracts.SupplyOffer
	var kind, status string
	var encodedLabels []byte
	if err := row.Scan(&offer.ID, &offer.SupplierUserID, &kind, &offer.Provider, &offer.CredentialRef, &offer.ProxyRef, &status, &offer.Quota, &offer.UnitPrice, &encodedLabels, &offer.CreatedAt, &offer.UpdatedAt); err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.SupplyOffer{}, ErrNotFound
		}
		return contracts.SupplyOffer{}, err
	}
	offer.Kind = contracts.SupplyOfferKind(kind)
	offer.Status = contracts.SupplyOfferStatus(status)
	offer.Labels = unmarshalLabels(encodedLabels)
	if offer.Status == contracts.SupplyOfferStatusRevoked {
		if err := tx.Commit(ctx); err != nil {
			return contracts.SupplyOffer{}, err
		}
		return offer, nil
	}
	var allocated bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM supply_ledger WHERE offer_id=$1 AND status=$2)`,
		id, string(contracts.SupplyLedgerAllocated)).Scan(&allocated); err != nil {
		return contracts.SupplyOffer{}, err
	}
	if allocated {
		return contracts.SupplyOffer{}, ErrConflict
	}
	if err := tx.QueryRow(ctx,
		`UPDATE supply_offers SET status=$2, updated_at=now() WHERE id=$1 RETURNING updated_at`,
		id, string(contracts.SupplyOfferStatusRevoked)).Scan(&offer.UpdatedAt); err != nil {
		return contracts.SupplyOffer{}, err
	}
	offer.Status = contracts.SupplyOfferStatusRevoked
	if err := tx.Commit(ctx); err != nil {
		return contracts.SupplyOffer{}, err
	}
	return offer, nil
}

func (s *PostgresStore) AllocateSupplyOffer(ctx context.Context, input contracts.SupplyLedgerEntry) (contracts.SupplyLedgerEntry, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.SupplyLedgerEntry{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var supplierUserID int64
	var offerStatus string
	if err := tx.QueryRow(ctx,
		`SELECT supplier_user_id, status FROM supply_offers WHERE id=$1 FOR UPDATE`, input.OfferID,
	).Scan(&supplierUserID, &offerStatus); err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.SupplyLedgerEntry{}, ErrNotFound
		}
		return contracts.SupplyLedgerEntry{}, err
	}
	if supplierUserID != input.SupplierUserID || contracts.SupplyOfferStatus(offerStatus) == contracts.SupplyOfferStatusRevoked {
		return contracts.SupplyLedgerEntry{}, ErrConflict
	}
	var duplicate bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM supply_ledger
			WHERE offer_id=$1 AND instance_id=$2 AND status=$3
		)`, input.OfferID, input.InstanceID, string(contracts.SupplyLedgerAllocated),
	).Scan(&duplicate); err != nil {
		return contracts.SupplyLedgerEntry{}, err
	}
	if duplicate {
		return contracts.SupplyLedgerEntry{}, ErrDuplicate
	}

	entry := input
	if entry.ID == "" {
		entry.ID = newID("ledger")
	}
	entry.Status = contracts.SupplyLedgerAllocated
	now := time.Now().UTC()
	entry.CreatedAt, entry.UpdatedAt = now, now
	if _, err := tx.Exec(ctx,
		`INSERT INTO supply_ledger (id, offer_id, supplier_user_id, user_id, instance_id, status, note, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		entry.ID, entry.OfferID, entry.SupplierUserID, entry.UserID, entry.InstanceID, string(entry.Status), entry.Note, entry.CreatedAt, entry.UpdatedAt); err != nil {
		return contracts.SupplyLedgerEntry{}, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE supply_offers SET status=$2, updated_at=now() WHERE id=$1 AND status=$3`,
		input.OfferID, string(contracts.SupplyOfferStatusActive), string(contracts.SupplyOfferStatusPending)); err != nil {
		return contracts.SupplyLedgerEntry{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.SupplyLedgerEntry{}, err
	}
	return entry, nil
}

func (s *PostgresStore) AppendSupplyLedger(ctx context.Context, input contracts.SupplyLedgerEntry) (contracts.SupplyLedgerEntry, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.SupplyLedgerEntry{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var supplierUserID int64
	var offerStatus string
	if err := tx.QueryRow(ctx,
		`SELECT supplier_user_id, status FROM supply_offers WHERE id=$1 FOR UPDATE`, input.OfferID,
	).Scan(&supplierUserID, &offerStatus); err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.SupplyLedgerEntry{}, ErrNotFound
		}
		return contracts.SupplyLedgerEntry{}, err
	}
	if supplierUserID != input.SupplierUserID || contracts.SupplyOfferStatus(offerStatus) == contracts.SupplyOfferStatusRevoked {
		return contracts.SupplyLedgerEntry{}, ErrConflict
	}
	e := input
	if e.ID == "" {
		e.ID = newID("ledger")
	}
	if e.Status == "" {
		e.Status = contracts.SupplyLedgerAllocated
	}
	now := time.Now().UTC()
	e.CreatedAt, e.UpdatedAt = now, now
	_, err = tx.Exec(ctx,
		`INSERT INTO supply_ledger (id, offer_id, supplier_user_id, user_id, instance_id, status, note, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		e.ID, e.OfferID, e.SupplierUserID, e.UserID, e.InstanceID, string(e.Status), e.Note, e.CreatedAt, e.UpdatedAt)
	if err != nil {
		return contracts.SupplyLedgerEntry{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.SupplyLedgerEntry{}, err
	}
	return e, nil
}

func (s *PostgresStore) UpdateSupplyLedgerStatus(ctx context.Context, id string, status contracts.SupplyLedgerEntryStatus, note string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE supply_ledger SET status=$2, note=CASE WHEN $3='' THEN note ELSE $3 END, updated_at=now() WHERE id=$1`,
		id, string(status), note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListSupplyLedger(ctx context.Context, offerID string) ([]contracts.SupplyLedgerEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, offer_id, supplier_user_id, user_id, instance_id, status, note, created_at, updated_at
		 FROM supply_ledger WHERE ($1='' OR offer_id=$1) ORDER BY created_at DESC`, offerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.SupplyLedgerEntry
	for rows.Next() {
		var e contracts.SupplyLedgerEntry
		var status string
		if err := rows.Scan(&e.ID, &e.OfferID, &e.SupplierUserID, &e.UserID, &e.InstanceID, &status, &e.Note, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.Status = contracts.SupplyLedgerEntryStatus(status)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CreateApproval(ctx context.Context, input contracts.ApprovalRequest) (contracts.ApprovalRequest, error) {
	ap := input
	if ap.ID == "" {
		ap.ID = newID("approval")
	}
	if ap.Status == "" {
		ap.Status = contracts.ApprovalPending
	}
	now := time.Now().UTC()
	ap.CreatedAt, ap.UpdatedAt = now, now
	ids, err := json.Marshal(ap.AccountIDs)
	if err != nil {
		return contracts.ApprovalRequest{}, err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO approval_requests (id, user_id, instance_id, action, risk_level, account_ids, schedulable, reason, status, requested_by, decided_by, decided_at, result_note, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		ap.ID, ap.UserID, ap.InstanceID, ap.Action, string(ap.RiskLevel), ids, ap.Schedulable, ap.Reason, string(ap.Status), ap.RequestedBy, ap.DecidedBy, ap.DecidedAt, ap.ResultNote, ap.CreatedAt, ap.UpdatedAt)
	if err != nil {
		return contracts.ApprovalRequest{}, err
	}
	return ap, nil
}

func scanApproval(row interface {
	Scan(dest ...any) error
}) (contracts.ApprovalRequest, error) {
	var a contracts.ApprovalRequest
	var risk, status string
	var ids []byte
	if err := row.Scan(&a.ID, &a.UserID, &a.InstanceID, &a.Action, &risk, &ids, &a.Schedulable, &a.Reason, &status, &a.RequestedBy, &a.DecidedBy, &a.DecidedAt, &a.ResultNote, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return contracts.ApprovalRequest{}, err
	}
	a.RiskLevel = contracts.RiskLevel(risk)
	a.Status = contracts.ApprovalStatus(status)
	_ = json.Unmarshal(ids, &a.AccountIDs)
	return a, nil
}

const approvalCols = `id, user_id, instance_id, action, risk_level, account_ids, schedulable, reason, status, requested_by, decided_by, decided_at, result_note, created_at, updated_at`

func (s *PostgresStore) GetApproval(ctx context.Context, id string) (contracts.ApprovalRequest, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+approvalCols+` FROM approval_requests WHERE id=$1`, id)
	a, err := scanApproval(row)
	if err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.ApprovalRequest{}, ErrNotFound
		}
		return contracts.ApprovalRequest{}, err
	}
	return a, nil
}

func (s *PostgresStore) UpdateApproval(ctx context.Context, input contracts.ApprovalRequest) (contracts.ApprovalRequest, error) {
	ap := input
	ap.UpdatedAt = time.Now().UTC()
	ids, err := json.Marshal(ap.AccountIDs)
	if err != nil {
		return contracts.ApprovalRequest{}, err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE approval_requests SET action=$2, risk_level=$3, account_ids=$4, schedulable=$5, reason=$6, status=$7, requested_by=$8, decided_by=$9, decided_at=$10, result_note=$11, updated_at=$12
		 WHERE id=$1`,
		ap.ID, ap.Action, string(ap.RiskLevel), ids, ap.Schedulable, ap.Reason, string(ap.Status), ap.RequestedBy, ap.DecidedBy, ap.DecidedAt, ap.ResultNote, ap.UpdatedAt)
	if err != nil {
		return contracts.ApprovalRequest{}, err
	}
	if tag.RowsAffected() == 0 {
		return contracts.ApprovalRequest{}, ErrNotFound
	}
	return ap, nil
}

func (s *PostgresStore) TransitionApproval(ctx context.Context, input contracts.ApprovalRequest, expected contracts.ApprovalStatus) (contracts.ApprovalRequest, error) {
	ap := input
	ap.UpdatedAt = time.Now().UTC()
	ids, err := json.Marshal(ap.AccountIDs)
	if err != nil {
		return contracts.ApprovalRequest{}, err
	}
	row := s.pool.QueryRow(ctx,
		`UPDATE approval_requests
		 SET action=$2, risk_level=$3, account_ids=$4, schedulable=$5, reason=$6,
		     status=$7, requested_by=$8, decided_by=$9, decided_at=$10,
		     result_note=$11, updated_at=$12
		 WHERE id=$1 AND status=$13
		 RETURNING `+approvalCols,
		ap.ID, ap.Action, string(ap.RiskLevel), ids, ap.Schedulable, ap.Reason,
		string(ap.Status), ap.RequestedBy, ap.DecidedBy, ap.DecidedAt,
		ap.ResultNote, ap.UpdatedAt, string(expected))
	updated, err := scanApproval(row)
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, pgxNoRows()) {
		return contracts.ApprovalRequest{}, err
	}
	var exists bool
	if lookupErr := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM approval_requests WHERE id=$1)`, ap.ID,
	).Scan(&exists); lookupErr != nil {
		return contracts.ApprovalRequest{}, lookupErr
	}
	if !exists {
		return contracts.ApprovalRequest{}, ErrNotFound
	}
	return contracts.ApprovalRequest{}, ErrConflict
}

func (s *PostgresStore) ListApprovals(ctx context.Context, userID int64, status contracts.ApprovalStatus) ([]contracts.ApprovalRequest, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+approvalCols+` FROM approval_requests
		 WHERE ($1=0 OR user_id=$1) AND ($2='' OR status=$2) ORDER BY created_at DESC`,
		userID, string(status))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.ApprovalRequest
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PostgresStore) AppendAudit(ctx context.Context, input contracts.OperationAudit) (contracts.OperationAudit, error) {
	a := input
	if a.ID == "" {
		a.ID = newID("audit")
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if !a.EventLevel.Valid() {
		a.EventLevel = contracts.DefaultEventLevel(a.RiskLevel, a.Result)
	}
	details, err := json.Marshal(a.Details)
	if err != nil {
		return contracts.OperationAudit{}, err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO operation_audits (id, user_id, instance_id, actor_type, actor_id, action, risk_level, event_level, target_type, target_id, request_payload_hash, result, error_message, approval_id, workflow_run_id, details, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::jsonb,$17)`,
		a.ID, a.UserID, a.InstanceID, a.ActorType, a.ActorID, a.Action, string(a.RiskLevel), string(a.EventLevel), a.TargetType, a.TargetID, a.RequestHash, a.Result, a.ErrorMessage, a.ApprovalID, a.WorkflowRunID, string(details), a.CreatedAt)
	if err != nil {
		return contracts.OperationAudit{}, err
	}
	return a, nil
}

func (s *PostgresStore) ListAudits(ctx context.Context, userID int64) ([]contracts.OperationAudit, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, instance_id, actor_type, actor_id, action, risk_level, event_level, target_type, target_id, request_payload_hash, result, error_message, approval_id, workflow_run_id, details, created_at
		 FROM operation_audits WHERE ($1=0 OR user_id=$1) ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.OperationAudit
	for rows.Next() {
		var a contracts.OperationAudit
		var risk, eventLevel string
		var details []byte
		if err := rows.Scan(&a.ID, &a.UserID, &a.InstanceID, &a.ActorType, &a.ActorID, &a.Action, &risk, &eventLevel, &a.TargetType, &a.TargetID, &a.RequestHash, &a.Result, &a.ErrorMessage, &a.ApprovalID, &a.WorkflowRunID, &details, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.RiskLevel = contracts.RiskLevel(risk)
		a.EventLevel = contracts.EventLevel(eventLevel)
		if len(details) > 0 {
			_ = json.Unmarshal(details, &a.Details)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CreateNotificationRoute(ctx context.Context, input contracts.NotificationRoute) (contracts.NotificationRoute, error) {
	r := input
	if r.ID == "" {
		r.ID = newID("route")
	}
	now := time.Now().UTC()
	r.CreatedAt, r.UpdatedAt = now, now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO notification_routes (id, user_id, name, channel, target_ref, min_risk_level, min_event_level, enabled, template, quiet_window, escalation_after, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		r.ID, r.UserID, r.Name, string(r.Channel), r.TargetRef, string(r.MinRiskLevel), string(r.EffectiveMinEventLevel()), r.Enabled, r.Template, r.QuietWindow, r.EscalationAfter, r.CreatedAt, r.UpdatedAt)
	if err != nil {
		return contracts.NotificationRoute{}, err
	}
	return r, nil
}

func (s *PostgresStore) GetNotificationRoute(ctx context.Context, id string) (contracts.NotificationRoute, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, user_id, name, channel, target_ref, min_risk_level, min_event_level, enabled, template, quiet_window, escalation_after, created_at, updated_at
		 FROM notification_routes WHERE id=$1`, id)
	var route contracts.NotificationRoute
	var channel, risk, eventLevel string
	if err := row.Scan(&route.ID, &route.UserID, &route.Name, &channel, &route.TargetRef,
		&risk, &eventLevel, &route.Enabled, &route.Template, &route.QuietWindow, &route.EscalationAfter,
		&route.CreatedAt, &route.UpdatedAt); err != nil {
		return contracts.NotificationRoute{}, mapNotFound(err)
	}
	route.Channel = contracts.NotificationChannel(channel)
	route.MinRiskLevel = contracts.RiskLevel(risk)
	route.MinEventLevel = contracts.EventLevel(eventLevel)
	return route, nil
}

func (s *PostgresStore) UpdateNotificationRoute(ctx context.Context, input contracts.NotificationRoute) (contracts.NotificationRoute, error) {
	r := input
	row := s.pool.QueryRow(ctx,
		`UPDATE notification_routes SET name=$2, channel=$3, target_ref=$4, min_risk_level=$5, min_event_level=$6, enabled=$7, template=$8, quiet_window=$9, escalation_after=$10, updated_at=$11
		 WHERE id=$1
		 RETURNING id, user_id, name, channel, target_ref, min_risk_level, min_event_level, enabled, template, quiet_window, escalation_after, created_at, updated_at`,
		r.ID, r.Name, string(r.Channel), r.TargetRef, string(r.MinRiskLevel), string(r.EffectiveMinEventLevel()), r.Enabled,
		r.Template, r.QuietWindow, r.EscalationAfter, time.Now().UTC())
	var updated contracts.NotificationRoute
	var channel, risk, eventLevel string
	if err := row.Scan(&updated.ID, &updated.UserID, &updated.Name, &channel, &updated.TargetRef,
		&risk, &eventLevel, &updated.Enabled, &updated.Template, &updated.QuietWindow, &updated.EscalationAfter,
		&updated.CreatedAt, &updated.UpdatedAt); err != nil {
		return contracts.NotificationRoute{}, mapNotFound(err)
	}
	updated.Channel = contracts.NotificationChannel(channel)
	updated.MinRiskLevel = contracts.RiskLevel(risk)
	updated.MinEventLevel = contracts.EventLevel(eventLevel)
	return updated, nil
}

func (s *PostgresStore) DeleteNotificationRoute(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM notification_routes WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const userCols = `id, email, display_name, password_hash, roles, enabled,
	platform_concurrency, platform_rpm,
	deactivation_status, deactivation_error_code, deactivation_requested_at, deactivation_completed_at,
	created_at, updated_at`

func scanUser(row interface{ Scan(dest ...any) error }) (contracts.User, error) {
	var u contracts.User
	var roles []string
	var deactivationStatus string
	if err := row.Scan(
		&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &roles, &u.Enabled,
		&u.PlatformConcurrency, &u.PlatformRPM,
		&deactivationStatus, &u.DeactivationErrorCode, &u.DeactivationRequestedAt, &u.DeactivationCompletedAt,
		&u.CreatedAt, &u.UpdatedAt,
	); err != nil {
		return contracts.User{}, err
	}
	u.Roles = userRolesFromStrings(roles)
	u.DeactivationStatus = normalizeUserDeactivationStatus(contracts.UserDeactivationStatus(deactivationStatus))
	return u, nil
}

func (s *PostgresStore) CreateUser(ctx context.Context, input contracts.User) (contracts.User, error) {
	u := input
	u.DeactivationStatus = normalizeUserDeactivationStatus(u.DeactivationStatus)
	now := time.Now().UTC()
	u.CreatedAt, u.UpdatedAt = now, now
	var err error
	if u.ID == 0 {
		err = s.pool.QueryRow(ctx,
			`INSERT INTO users (email, display_name, password_hash, roles, enabled, platform_concurrency, platform_rpm, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			 RETURNING id`,
			u.Email, u.DisplayName, u.PasswordHash, userRolesToStrings(u.Roles), u.Enabled, u.PlatformConcurrency, u.PlatformRPM, u.CreatedAt, u.UpdatedAt).Scan(&u.ID)
	} else {
		_, err = s.pool.Exec(ctx,
			`INSERT INTO users (id, email, display_name, password_hash, roles, enabled, platform_concurrency, platform_rpm, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			u.ID, u.Email, u.DisplayName, u.PasswordHash, userRolesToStrings(u.Roles), u.Enabled, u.PlatformConcurrency, u.PlatformRPM, u.CreatedAt, u.UpdatedAt)
		if err == nil {
			_, err = s.pool.Exec(ctx,
				`SELECT setval(pg_get_serial_sequence('users','id'), GREATEST((SELECT COALESCE(MAX(id), 1) FROM users), 1), true)`)
		}
	}
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.User{}, ErrDuplicate
		}
		return contracts.User{}, err
	}
	return u, nil
}

func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (contracts.User, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email=$1`, email)
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.User{}, ErrNotFound
		}
		return contracts.User{}, err
	}
	return u, nil
}

func (s *PostgresStore) GetUser(ctx context.Context, id int64) (contracts.User, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id)
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.User{}, ErrNotFound
		}
		return contracts.User{}, err
	}
	return u, nil
}

func (s *PostgresStore) ListUsers(ctx context.Context) ([]contracts.User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *PostgresStore) UpdateUser(ctx context.Context, input contracts.User) (contracts.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialize admin demotions/disables so two concurrent requests cannot both
	// observe another enabled administrator and remove the final two at once.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1162173773)`); err != nil {
		return contracts.User{}, err
	}
	current, err := scanUser(tx.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1 FOR UPDATE`, input.ID))
	if err != nil {
		return contracts.User{}, mapNotFound(err)
	}
	if input.UpdatedAt.IsZero() || !current.UpdatedAt.Equal(input.UpdatedAt) {
		return contracts.User{}, ErrConflict
	}
	if current.DeactivationStatus == contracts.UserDeactivationDraining {
		return contracts.User{}, ErrUserDeactivationInProgress
	}
	if current.Enabled && userHasRole(current.Roles, contracts.UserRoleAdmin) &&
		(!input.Enabled || !userHasRole(input.Roles, contracts.UserRoleAdmin)) {
		var enabledAdmins int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM users WHERE enabled AND 'admin'=ANY(roles)`,
		).Scan(&enabledAdmins); err != nil {
			return contracts.User{}, err
		}
		if enabledAdmins <= 1 {
			return contracts.User{}, ErrLastEnabledAdmin
		}
	}
	rolesChanged := !sameUserRoles(current.Roles, input.Roles)
	enabledChanged := current.Enabled != input.Enabled
	currentActiveClient := activeClientUser(current)
	targetActiveClient := input.Enabled && userHasRole(input.Roles, contracts.UserRoleClient)
	deactivationRequested := currentActiveClient && !targetActiveClient
	deactivationRetry := current.DeactivationStatus == contracts.UserDeactivationFailed && !targetActiveClient
	if current.DeactivationStatus == contracts.UserDeactivationFailed && targetActiveClient {
		var unrevoked bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1 FROM route_plans plan
			   JOIN published_bindings binding ON binding.plan_id=plan.id
			   WHERE plan.user_id=$1 AND binding.state<>$2
			 )`, current.ID, string(contracts.BindingRevoked)).Scan(&unrevoked); err != nil {
			return contracts.User{}, err
		}
		if unrevoked {
			return contracts.User{}, ErrUserDeactivationInProgress
		}
	}

	deactivationStatus := current.DeactivationStatus
	deactivationErrorCode := current.DeactivationErrorCode
	deactivationRequestedAt := current.DeactivationRequestedAt
	deactivationCompletedAt := current.DeactivationCompletedAt
	var deactivationRequestedAtNow bool
	switch {
	case targetActiveClient:
		deactivationStatus = contracts.UserDeactivationNone
		deactivationErrorCode = ""
		deactivationRequestedAt = nil
		deactivationCompletedAt = nil
	case deactivationRequested || deactivationRetry:
		deactivationStatus = contracts.UserDeactivationDraining
		deactivationErrorCode = ""
		deactivationRequestedAt = nil
		deactivationRequestedAtNow = true
		deactivationCompletedAt = nil
	}

	updated, err := scanUser(tx.QueryRow(ctx,
		`UPDATE users
		 SET email=$2, display_name=$3, roles=$4, enabled=$5,
		     platform_concurrency=$11, platform_rpm=$12,
		     deactivation_status=$6, deactivation_error_code=$7,
		     deactivation_requested_at=CASE WHEN $10 THEN statement_timestamp() ELSE $8 END,
		     deactivation_completed_at=$9,
		     updated_at=statement_timestamp()
		 WHERE id=$1
		 RETURNING `+userCols,
		input.ID, input.Email, input.DisplayName, userRolesToStrings(input.Roles), input.Enabled,
		string(deactivationStatus), deactivationErrorCode, deactivationRequestedAt, deactivationCompletedAt,
		deactivationRequestedAtNow, input.PlatformConcurrency, input.PlatformRPM))
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.User{}, ErrDuplicate
		}
		return contracts.User{}, mapNotFound(err)
	}
	if enabledChanged || rolesChanged {
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, updated.ID); err != nil {
			return contracts.User{}, err
		}
	}
	if deactivationRequested || deactivationRetry {
		rows, err := tx.Query(ctx,
			`UPDATE route_plans
			 SET scheduling_generation=scheduling_generation+1, updated_at=statement_timestamp()
			 WHERE user_id=$1 RETURNING id,scheduling_generation`, updated.ID)
		if err != nil {
			return contracts.User{}, err
		}
		type advancedPlan struct {
			id         string
			generation int64
		}
		advanced := make([]advancedPlan, 0)
		for rows.Next() {
			var plan advancedPlan
			if err := rows.Scan(&plan.id, &plan.generation); err != nil {
				rows.Close()
				return contracts.User{}, err
			}
			advanced = append(advanced, plan)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return contracts.User{}, err
		}
		rows.Close()
		for _, plan := range advanced {
			if err := supersedeRoutePlanOwnersPostgres(ctx, tx, plan.id, "", plan.generation); err != nil {
				return contracts.User{}, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.User{}, err
	}
	return updated, nil
}

// ReconcileUserDeactivations locks each due user and validates the complete
// binding ledger in the same transaction as final Connector/session cleanup.
func (s *PostgresStore) ReconcileUserDeactivations(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`SELECT id FROM users
		 WHERE deactivation_status IN ('draining','failed')
		 ORDER BY id FOR UPDATE`)
	if err != nil {
		return err
	}
	userIDs := make([]int64, 0)
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			rows.Close()
			return err
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, userID := range userIDs {
		var unrevoked bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1 FROM route_plans plan
			   JOIN published_bindings binding ON binding.plan_id=plan.id
			   WHERE plan.user_id=$1 AND binding.state<>$2
			 )`, userID, string(contracts.BindingRevoked)).Scan(&unrevoked); err != nil {
			return err
		}
		if !unrevoked {
			var incompleteOperations bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (
				   SELECT 1 FROM pool_rollout_operations
				   WHERE user_id=$1 AND action='drain' AND status IN ('pending','running','failed')
				 )`, userID).Scan(&incompleteOperations); err != nil {
				return err
			}
			if incompleteOperations {
				continue
			}
			if _, err := tx.Exec(ctx,
				`UPDATE connectors
				 SET status=$2, token_hash='', revoked_at=statement_timestamp(), updated_at=statement_timestamp()
				 WHERE user_id=$1 AND status<>$2`, userID, string(contracts.ConnectorStatusRevoked)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE instances SET connector_id=NULL, updated_at=statement_timestamp()
				 WHERE user_id=$1 AND connector_id IS NOT NULL`, userID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE connector_enrollments SET used_at=statement_timestamp()
				 WHERE user_id=$1 AND used_at IS NULL`, userID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE connector_tasks
				 SET status=$2, result='null'::jsonb, error='{"code":"expired"}'::jsonb,
				     lease_owner='', lease_nonce='', lease_until=NULL, updated_at=statement_timestamp()
				 WHERE user_id=$1 AND status IN ($3,$4)`, userID,
				string(contracts.ConnectorTaskExpired), string(contracts.ConnectorTaskPending),
				string(contracts.ConnectorTaskLeased)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE route_plans SET status=$2, updated_at=statement_timestamp()
				 WHERE user_id=$1 AND status<>$2`, userID, string(contracts.RoutePlanSuspended)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE users
				 SET deactivation_status='completed', deactivation_error_code='',
				     deactivation_requested_at=COALESCE(deactivation_requested_at,statement_timestamp()),
				     deactivation_completed_at=statement_timestamp(), updated_at=statement_timestamp()
				 WHERE id=$1`, userID); err != nil {
				return err
			}
			continue
		}

		var drainFailed bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1 FROM pool_rollout_operations
			   WHERE user_id=$1 AND action='drain' AND status='failed'
			 )`, userID).Scan(&drainFailed); err != nil {
			return err
		}
		nextStatus, nextCode := string(contracts.UserDeactivationDraining), ""
		if drainFailed {
			nextStatus, nextCode = string(contracts.UserDeactivationFailed), userDeactivationDrainFailedCode
		}
		if _, err := tx.Exec(ctx,
			`UPDATE users
			 SET deactivation_status=$2, deactivation_error_code=$3,
			     deactivation_completed_at=NULL,
			     updated_at=CASE
			       WHEN deactivation_status<>$2 OR deactivation_error_code<>$3
			       THEN statement_timestamp() ELSE updated_at END
			 WHERE id=$1`, userID, nextStatus, nextCode); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) UpdateUserPasswordHash(ctx context.Context, userID int64, passwordHash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE users SET password_hash=$2, updated_at=now() WHERE id=$1`,
		userID, passwordHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) CreateSession(ctx context.Context, input contracts.Session, expectedUser contracts.User) error {
	if input.CreatedAt.IsZero() {
		input.CreatedAt = time.Now().UTC()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanUser(tx.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1 FOR UPDATE`, input.UserID))
	if err != nil {
		if errors.Is(mapNotFound(err), ErrNotFound) {
			return ErrConflict
		}
		return err
	}
	if current.ID != expectedUser.ID || !current.Enabled ||
		current.PasswordHash != expectedUser.PasswordHash || !sameUserRoles(current.Roles, expectedUser.Roles) {
		return ErrConflict
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, created_at) VALUES ($1,$2,$3,$4)`,
		input.TokenHash, input.UserID, input.ExpiresAt, input.CreatedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) GetSession(ctx context.Context, tokenHash string) (contracts.Session, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT token_hash, user_id, expires_at, created_at FROM sessions WHERE token_hash=$1`, tokenHash)
	var sess contracts.Session
	if err := row.Scan(&sess.TokenHash, &sess.UserID, &sess.ExpiresAt, &sess.CreatedAt); err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.Session{}, ErrNotFound
		}
		return contracts.Session{}, err
	}
	return sess, nil
}

func (s *PostgresStore) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, tokenHash)
	return err
}

func (s *PostgresStore) ListNotificationRoutes(ctx context.Context, userID int64) ([]contracts.NotificationRoute, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, name, channel, target_ref, min_risk_level, min_event_level, enabled, template, quiet_window, escalation_after, created_at, updated_at
		 FROM notification_routes WHERE ($1=0 OR user_id=$1) ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.NotificationRoute
	for rows.Next() {
		var r contracts.NotificationRoute
		var channel, risk, eventLevel string
		if err := rows.Scan(&r.ID, &r.UserID, &r.Name, &channel, &r.TargetRef, &risk, &eventLevel, &r.Enabled, &r.Template, &r.QuietWindow, &r.EscalationAfter, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Channel = contracts.NotificationChannel(channel)
		r.MinRiskLevel = contracts.RiskLevel(risk)
		r.MinEventLevel = contracts.EventLevel(eventLevel)
		out = append(out, r)
	}
	return out, rows.Err()
}
