package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresConnectorTaskRoutePlanFenceMigrationLive(t *testing.T) {
	fixture := newConnectorTaskFenceMigrationFixture(t, 73)
	seedLegacyConnectorTaskFenceRows(t, fixture.conn)
	legacyBefore := readLegacyConnectorTaskFenceRows(t, fixture.conn)

	upgradeErr := fixture.migrator.Migrate(74)
	if upgradeErr == nil || !strings.Contains(upgradeErr.Error(), "cannot upgrade while a protocol v2 fenced connector task is leased") {
		t.Fatalf("migrate with unresolved v2 leased mutations error=%v, want strict upgrade rejection", upgradeErr)
	}
	requireConnectorTaskFenceColumnsAbsent(t, fixture.conn)
	requireConnectorProtocolConstraint(t, fixture.conn, 2)
	requireLegacyConnectorTaskFenceRowsUnchanged(t, fixture.conn, legacyBefore)

	// golang-migrate marks the attempted up dirty outside the SQL transaction.
	// Restore only its metadata after proving the schema/data transaction rolled
	// back, then model the operator's explicit gateway reconciliation.
	reopenConnectorTaskFenceMigrator(t, fixture)
	if err := fixture.migrator.Force(73); err != nil {
		t.Fatalf("restore migration metadata after strict upgrade rejection: %v", err)
	}
	requireConnectorTaskFenceMigrationVersion(t, fixture.conn, 73, false)
	reconcileLegacyConnectorTaskFenceLeases(t, fixture.conn)
	if err := fixture.migrator.Migrate(74); err != nil {
		t.Fatalf("migrate valid and invalid legacy tasks from 73 to 74: %v", err)
	}
	requireConnectorTaskFenceMigrationVersion(t, fixture.conn, 74, false)
	requireConnectorProtocolVersion(t, fixture.conn, "connector-owner", 2)
	requireConnectorProtocolConstraint(t, fixture.conn, 2, 3)

	for _, taskID := range []string{
		"valid-pending", "valid-recommendation-slash", "valid-recommendation-dash",
	} {
		task := readConnectorTaskFenceRow(t, fixture.conn, taskID)
		if task.planID != "plan-owner" || task.generation != 7 || task.errorCode() != "" {
			t.Fatalf("valid legacy task %s was not backfilled: %+v", taskID, task)
		}
	}
	pending := readConnectorTaskFenceRow(t, fixture.conn, "valid-pending")
	if pending.status != "pending" || pending.leaseOwner != "" || pending.leaseNonce != "" || pending.leaseUntil != nil {
		t.Fatalf("valid pending task changed shape during backfill: %+v", pending)
	}

	invalidIDs := []string{
		"malformed-pending", "nested", "non-plan-scope", "stale-generation",
		"foreign-owner", "foreign-instance", "missing-plan",
	}
	for _, taskID := range invalidIDs {
		task := readConnectorTaskFenceRow(t, fixture.conn, taskID)
		if task.status != "failed" || task.errorCode() != "scheduling_fence_stale" || !task.resultIsNull() ||
			task.leaseOwner != "" || task.leaseNonce != "" || task.leaseUntil != nil || task.planID != "" || task.generation != 0 {
			t.Fatalf("invalid legacy task %s was not failed closed: %+v", taskID, task)
		}
	}

	terminal := map[string]string{
		"terminal-succeeded": "succeeded", "terminal-failed": "failed", "terminal-expired": "expired",
		"valid-leased": "succeeded", "malformed-leased": "failed", "nested-leased": "failed", "mandatory-leased": "failed",
	}
	for taskID, wantStatus := range terminal {
		task := readConnectorTaskFenceRow(t, fixture.conn, taskID)
		if task.status != wantStatus || task.planID != "" || task.generation != 0 {
			t.Fatalf("terminal legacy task %s was rewritten or backfilled: %+v", taskID, task)
		}
	}
	if got := readConnectorTaskFenceRow(t, fixture.conn, "terminal-succeeded"); !got.resultBool("preserved") {
		t.Fatalf("succeeded result changed during migration: %+v", got)
	}
	if got := readConnectorTaskFenceRow(t, fixture.conn, "terminal-failed"); got.errorCode() != "gateway_timeout" {
		t.Fatalf("failed error changed during migration: %+v", got)
	}
	if got := readConnectorTaskFenceRow(t, fixture.conn, "terminal-expired"); got.errorCode() != "expired" {
		t.Fatalf("expired error changed during migration: %+v", got)
	}
	if got := readConnectorTaskFenceRow(t, fixture.conn, "valid-leased"); !got.resultBool("operator_reconciled") {
		t.Fatalf("operator-reconciled success changed during migration: %+v", got)
	}
	for _, taskID := range []string{"malformed-leased", "nested-leased", "mandatory-leased"} {
		if got := readConnectorTaskFenceRow(t, fixture.conn, taskID); got.errorCode() != "operator_reconciled" {
			t.Fatalf("operator-reconciled failure %s changed during migration: %+v", taskID, got)
		}
	}

	insertCurrentConnectorTaskFenceRow(t, fixture.conn, connectorTaskFenceInsert{
		id: "post74-valid", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner",
		planID: "plan-owner", generation: 7, input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", 7),
	})
	requireConnectorTaskExecutingSchemaLive(t, fixture.conn)
	requireRoutePlanExecutingFenceLive(t, fixture.conn)

	post74Rejects := []struct {
		name      string
		row       connectorTaskFenceInsert
		SQLStates []string
	}{
		{name: "malformed", row: connectorTaskFenceInsert{
			id: "post74-malformed", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner",
			planID: "plan-owner", generation: 7, input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", "bad"),
		}, SQLStates: []string{"23514"}},
		{name: "nested", row: connectorTaskFenceInsert{
			id: "post74-nested", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner",
			planID: "plan-owner", generation: 7, input: nestedConnectorTaskFenceInput("auto-switch/plan/plan-owner", 7),
		}, SQLStates: []string{"23514"}},
		{name: "non plan scope", row: connectorTaskFenceInsert{
			id: "post74-non-plan", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner",
			planID: "plan-owner", generation: 7, input: topLevelConnectorTaskFenceInput("rollout/plan-owner", 7),
		}, SQLStates: []string{"23514"}},
		{name: "stale generation", row: connectorTaskFenceInsert{
			id: "post74-stale", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner",
			planID: "plan-owner", generation: 6, input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", 6),
		}, SQLStates: []string{"23514"}},
		{name: "foreign owner", row: connectorTaskFenceInsert{
			id: "post74-foreign-owner", userID: 92002, instanceID: "instance-foreign-owner", connectorID: "connector-foreign-owner",
			planID: "plan-owner", generation: 7, input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", 7),
		}, SQLStates: []string{"23514", "23503"}},
		{name: "foreign instance", row: connectorTaskFenceInsert{
			id: "post74-foreign-instance", userID: 91001, instanceID: "instance-owner-other", connectorID: "connector-owner-other",
			planID: "plan-owner", generation: 7, input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", 7),
		}, SQLStates: []string{"23514", "23503"}},
	}
	for _, test := range post74Rejects {
		t.Run("post-74 direct insert rejects "+test.name, func(t *testing.T) {
			err := execCurrentConnectorTaskFenceInsert(fixture.conn, test.row)
			requireConnectorTaskFenceSQLState(t, err, test.SQLStates...)
		})
	}

	driftUpdates := []struct {
		name      string
		query     string
		arguments []any
	}{
		{name: "type", query: `UPDATE connector_tasks SET type='gateway.health.get' WHERE id='post74-valid'`},
		{name: "input", query: `UPDATE connector_tasks SET input=jsonb_set(input,'{fence,version}','8'::jsonb) WHERE id='post74-valid'`},
		{name: "plan identity", query: `UPDATE connector_tasks SET plan_id='plan-owner-other' WHERE id='post74-valid'`},
		{name: "generation identity", query: `UPDATE connector_tasks SET scheduling_generation=8 WHERE id='post74-valid'`},
	}
	for _, drift := range driftUpdates {
		t.Run("post-74 direct update rejects "+drift.name+" drift", func(t *testing.T) {
			_, err := fixture.conn.Exec(context.Background(), drift.query, drift.arguments...)
			requireConnectorTaskFenceSQLState(t, err, "23514", "23503")
		})
	}
	unchanged := readConnectorTaskFenceRow(t, fixture.conn, "post74-valid")
	if unchanged.status != "pending" || unchanged.planID != "plan-owner" || unchanged.generation != 7 ||
		unchanged.taskType != "gateway.account.schedulable.set" || unchanged.errorCode() != "" {
		t.Fatalf("rejected direct drift mutated the valid task: %+v", unchanged)
	}

	// Down can safely invalidate work that has not crossed the remote execution
	// edge. It must fail pending/leased plan tasks closed before removing their
	// explicit identity; terminal business outcomes remain untouched.
	if err := fixture.migrator.Steps(-1); err != nil {
		t.Fatalf("safe 74 down with only pending/leased plan tasks: %v", err)
	}
	requireConnectorTaskFenceMigrationVersion(t, fixture.conn, 73, false)
	requireConnectorTaskFenceColumnsAbsent(t, fixture.conn)
	requireConnectorProtocolConstraint(t, fixture.conn, 2)
	for _, taskID := range []string{
		"valid-pending", "valid-recommendation-slash", "valid-recommendation-dash", "post74-valid",
	} {
		task := readConnectorTaskFenceDowngradedRow(t, fixture.conn, taskID)
		if task.status != "failed" || task.errorCode() != "scheduling_fence_stale" || !task.resultIsNull() ||
			task.leaseOwner != "" || task.leaseNonce != "" || task.leaseUntil != nil {
			t.Fatalf("safe down did not fail %s closed: %+v", taskID, task)
		}
	}
	for taskID, wantStatus := range terminal {
		task := readConnectorTaskFenceDowngradedRow(t, fixture.conn, taskID)
		if task.status != wantStatus {
			t.Fatalf("safe down changed terminal task %s: %+v", taskID, task)
		}
	}
	if got := readConnectorTaskFenceDowngradedRow(t, fixture.conn, "terminal-succeeded"); !got.resultBool("preserved") {
		t.Fatalf("safe down changed succeeded result: %+v", got)
	}
	if got := readConnectorTaskFenceDowngradedRow(t, fixture.conn, "terminal-failed"); got.errorCode() != "gateway_timeout" {
		t.Fatalf("safe down changed failed outcome: %+v", got)
	}
	if got := readConnectorTaskFenceDowngradedRow(t, fixture.conn, "terminal-expired"); got.errorCode() != "expired" {
		t.Fatalf("safe down changed expired outcome: %+v", got)
	}
}

func TestPostgresConnectorTaskRoutePlanFenceDownRejectsExecutingLive(t *testing.T) {
	fixture := newConnectorTaskFenceMigrationFixture(t, 73)
	seedLegacyConnectorTaskFenceRows(t, fixture.conn)
	reconcileLegacyConnectorTaskFenceLeases(t, fixture.conn)
	if err := fixture.migrator.Migrate(74); err != nil {
		t.Fatalf("migrate to 74: %v", err)
	}
	insertCurrentConnectorTaskFenceRow(t, fixture.conn, connectorTaskFenceInsert{
		id: "down-executing", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner",
		planID: "plan-owner", generation: 7, input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", 7),
	})
	if _, err := fixture.conn.Exec(context.Background(), `UPDATE connector_tasks SET status='executing' WHERE id='down-executing'`); err != nil {
		t.Fatalf("move current plan task across execution edge: %v", err)
	}
	executingBefore := readConnectorTaskFenceRow(t, fixture.conn, "down-executing")
	pendingBefore := readConnectorTaskFenceRow(t, fixture.conn, "valid-pending")
	if executingBefore.status != "executing" || executingBefore.planID != "plan-owner" || executingBefore.generation != 7 {
		t.Fatalf("executing down precondition missing: %+v", executingBefore)
	}

	if err := fixture.migrator.Steps(-1); err == nil {
		t.Fatal("0074 down accepted a plan-scoped executing connector task")
	}
	executingAfter := readConnectorTaskFenceRow(t, fixture.conn, "down-executing")
	pendingAfter := readConnectorTaskFenceRow(t, fixture.conn, "valid-pending")
	if !sameConnectorTaskFenceRow(executingBefore, executingAfter) {
		t.Fatalf("rejected down mutated executing task: before=%+v after=%+v", executingBefore, executingAfter)
	}
	if !sameConnectorTaskFenceRow(pendingBefore, pendingAfter) {
		t.Fatalf("rejected down partially failed pending task: before=%+v after=%+v", pendingBefore, pendingAfter)
	}
	requireConnectorTaskFenceColumnsPresent(t, fixture.conn)
	var triggerExists bool
	if err := fixture.conn.QueryRow(context.Background(), `SELECT EXISTS(
		SELECT 1 FROM pg_catalog.pg_trigger WHERE tgname='trg_enforce_connector_task_route_plan_fence' AND NOT tgisinternal
	)`).Scan(&triggerExists); err != nil {
		t.Fatal(err)
	}
	if !triggerExists {
		t.Fatal("rejected down removed the route-plan fence trigger")
	}
}

func TestPostgresConnectorTaskRoutePlanFenceDownRejectsProtocolV3Live(t *testing.T) {
	fixture := newConnectorTaskFenceMigrationFixture(t, 73)
	seedLegacyConnectorTaskFenceRows(t, fixture.conn)
	reconcileLegacyConnectorTaskFenceLeases(t, fixture.conn)
	if err := fixture.migrator.Migrate(74); err != nil {
		t.Fatalf("migrate to 74: %v", err)
	}
	insertConnectorFenceProtocolV3(t, fixture.conn)
	requireConnectorProtocolVersion(t, fixture.conn, "connector-owner", 2)
	requireConnectorProtocolVersion(t, fixture.conn, "connector-v3", 3)
	pendingBefore := readConnectorTaskFenceRow(t, fixture.conn, "valid-pending")

	if err := fixture.migrator.Steps(-1); err == nil {
		t.Fatal("0074 down accepted a protocol v3 connector")
	}
	pendingAfter := readConnectorTaskFenceRow(t, fixture.conn, "valid-pending")
	if !sameConnectorTaskFenceRow(pendingBefore, pendingAfter) {
		t.Fatalf("rejected protocol down partially failed pending task: before=%+v after=%+v", pendingBefore, pendingAfter)
	}
	requireConnectorTaskFenceColumnsPresent(t, fixture.conn)
	requireConnectorProtocolConstraint(t, fixture.conn, 2, 3)
	requireConnectorProtocolVersion(t, fixture.conn, "connector-v3", 3)

	// golang-migrate records a failed down as (73, dirty) outside the migration
	// transaction. Restore only that control-plane marker before retrying the
	// now-safe down; the assertions above prove schema/data were rolled back.
	reopenConnectorTaskFenceMigrator(t, fixture)
	if err := fixture.migrator.Force(74); err != nil {
		t.Fatalf("restore migration metadata after rejected protocol down: %v", err)
	}
	if _, err := fixture.conn.Exec(context.Background(), `DELETE FROM connectors WHERE connector_id='connector-v3'`); err != nil {
		t.Fatal(err)
	}
	if err := fixture.migrator.Steps(-1); err != nil {
		t.Fatalf("down after removing protocol v3 connector: %v", err)
	}
	requireConnectorTaskFenceMigrationVersion(t, fixture.conn, 73, false)
	requireConnectorTaskFenceColumnsAbsent(t, fixture.conn)
	requireConnectorProtocolConstraint(t, fixture.conn, 2)
	requireConnectorProtocolVersion(t, fixture.conn, "connector-owner", 2)
}

type connectorTaskFenceMigrationFixture struct {
	schema   string
	dsn      string
	conn     *pgx.Conn
	migrator *migrate.Migrate
}

func newConnectorTaskFenceMigrationFixture(t *testing.T, target uint) *connectorTaskFenceMigrationFixture {
	t.Helper()
	baseDSN := strings.TrimSpace(os.Getenv("E2M_TEST_POSTGRES_DSN"))
	if baseDSN == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	baseConn, err := pgx.Connect(ctx, baseDSN)
	if err != nil {
		t.Fatalf("connect disposable PostgreSQL: %v", err)
	}
	schema := fmt.Sprintf("ui17_ctask_fence_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := baseConn.Exec(ctx, `CREATE SCHEMA `+identifier); err != nil {
		baseConn.Close(context.Background())
		t.Fatalf("create isolated schema: %v", err)
	}
	schemaDSN := connectorTaskFenceSchemaDSN(t, baseDSN, schema)
	migrationURL, err := postgresMigrationURL(schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	source, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	driver, err := database.Open(migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	migrator, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		driver.Close()
		t.Fatal(err)
	}
	if err := migrator.Migrate(target); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		migrator.Close()
		t.Fatalf("migrate isolated schema to %d: %v", target, err)
	}
	conn, err := pgx.Connect(ctx, schemaDSN)
	if err != nil {
		migrator.Close()
		t.Fatal(err)
	}
	fixture := &connectorTaskFenceMigrationFixture{schema: schema, dsn: schemaDSN, conn: conn, migrator: migrator}
	t.Cleanup(func() {
		_ = conn.Close(context.Background())
		_, _ = migrator.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := baseConn.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS `+identifier+` CASCADE`); cleanupErr != nil {
			t.Errorf("drop isolated schema %s: %v", schema, cleanupErr)
		}
		_ = baseConn.Close(context.Background())
	})
	return fixture
}

func reopenConnectorTaskFenceMigrator(t *testing.T, fixture *connectorTaskFenceMigrationFixture) {
	t.Helper()
	_, _ = fixture.migrator.Close()
	migrationURL, err := postgresMigrationURL(fixture.dsn)
	if err != nil {
		t.Fatal(err)
	}
	source, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	driver, err := database.Open(migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	migrator, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		driver.Close()
		t.Fatal(err)
	}
	fixture.migrator = migrator
}

func connectorTaskFenceSchemaDSN(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	options := strings.TrimSpace(query.Get("options") + " -csearch_path=" + schema)
	query.Set("options", options)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func seedLegacyConnectorTaskFenceRows(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	for _, user := range []struct {
		id    int64
		email string
	}{{91001, "connector-fence-owner@example.test"}, {92002, "connector-fence-foreign@example.test"}} {
		if _, err := conn.Exec(ctx, `INSERT INTO users(id,email,password_hash,roles,enabled) VALUES($1,$2,'test',ARRAY['client'],true)`, user.id, user.email); err != nil {
			t.Fatal(err)
		}
	}
	instances := []struct {
		id     string
		userID int64
	}{{"instance-owner", 91001}, {"instance-owner-other", 91001}, {"instance-foreign-owner", 92002}}
	for _, instance := range instances {
		if _, err := conn.Exec(ctx, `INSERT INTO instances(id,user_id,name,kind,status) VALUES($1,$2,$1,'newapi','active')`, instance.id, instance.userID); err != nil {
			t.Fatal(err)
		}
	}
	connectors := []struct {
		id         string
		userID     int64
		instanceID string
	}{{"connector-owner", 91001, "instance-owner"}, {"connector-owner-other", 91001, "instance-owner-other"}, {"connector-foreign-owner", 92002, "instance-foreign-owner"}}
	for _, connector := range connectors {
		if _, err := conn.Exec(ctx, `INSERT INTO connectors(
			connector_id,user_id,instance_id,status,token_hash,version,protocol_version
		) VALUES($1,$2,$3,'online',$1||'-token','test',2)`, connector.id, connector.userID, connector.instanceID); err != nil {
			t.Fatal(err)
		}
	}
	for _, plan := range []struct {
		id, instanceID, poolID string
		userID                 int64
		generation             int64
	}{{"plan-owner", "instance-owner", "pool-owner", 91001, 7}, {"plan-owner-other", "instance-owner-other", "pool-owner-other", 91001, 11}, {"plan-foreign-owner", "instance-foreign-owner", "pool-foreign", 92002, 5}} {
		if _, err := conn.Exec(ctx, `INSERT INTO upstream_pools(id,name,status) VALUES($1,$1,'active')`, plan.poolID); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO route_plans(
			id,user_id,instance_id,pool_id,status,scheduling_generation
		) VALUES($1,$2,$3,$4,'published',$5)`, plan.id, plan.userID, plan.instanceID, plan.poolID, plan.generation); err != nil {
			t.Fatal(err)
		}
	}

	rows := []legacyConnectorTaskFenceInsert{
		{id: "valid-pending", status: "pending", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", 7)},
		{id: "valid-leased", status: "leased", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", 7), leaseOwner: "connector-owner", leaseNonce: "legacy-lease-nonce", leased: true},
		{id: "valid-recommendation-slash", status: "pending", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", input: topLevelConnectorTaskFenceInput("recommendation/rollout/plan-owner", 7)},
		{id: "valid-recommendation-dash", status: "pending", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", input: topLevelConnectorTaskFenceInput("recommendation-rollout/plan-owner", 7)},
		{id: "malformed-pending", status: "pending", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", "not-a-number")},
		{id: "malformed-leased", status: "leased", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", "not-a-number"), leaseOwner: "connector-owner", leaseNonce: "malformed-lease", leased: true},
		{id: "nested-leased", status: "leased", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", input: nestedConnectorTaskFenceInput("auto-switch/plan/plan-owner", 7), leaseOwner: "connector-owner", leaseNonce: "nested-lease", leased: true},
		{id: "mandatory-leased", status: "leased", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", taskType: "gateway.account.traffic_share.set", input: `{"account_id":"account-fence","weight":10}`, leaseOwner: "connector-owner", leaseNonce: "mandatory-lease", leased: true},
		{id: "nested", status: "pending", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", input: nestedConnectorTaskFenceInput("auto-switch/plan/plan-owner", 7)},
		{id: "non-plan-scope", status: "pending", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", input: topLevelConnectorTaskFenceInput("rollout/plan-owner", 7)},
		{id: "stale-generation", status: "pending", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", 6)},
		{id: "foreign-owner", status: "pending", userID: 92002, instanceID: "instance-foreign-owner", connectorID: "connector-foreign-owner", input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", 7)},
		{id: "foreign-instance", status: "pending", userID: 91001, instanceID: "instance-owner-other", connectorID: "connector-owner-other", input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", 7)},
		{id: "missing-plan", status: "pending", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-missing", 1)},
		{id: "terminal-succeeded", status: "succeeded", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", 7), result: `{"preserved":true}`},
		{id: "terminal-failed", status: "failed", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", 7), errorJSON: `{"code":"gateway_timeout"}`},
		{id: "terminal-expired", status: "expired", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner", input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-owner", 7), errorJSON: `{"code":"expired"}`},
	}
	for _, row := range rows {
		insertLegacyConnectorTaskFenceRow(t, conn, row)
	}
}

type legacyConnectorTaskFenceInsert struct {
	id, status, instanceID, connectorID string
	taskType                            string
	userID                              int64
	input, result, errorJSON            string
	leaseOwner, leaseNonce              string
	leased                              bool
}

func insertLegacyConnectorTaskFenceRow(t *testing.T, conn *pgx.Conn, row legacyConnectorTaskFenceInsert) {
	t.Helper()
	if row.taskType == "" {
		row.taskType = "gateway.account.schedulable.set"
	}
	if row.result == "" {
		row.result = "null"
	}
	if row.errorJSON == "" {
		row.errorJSON = "{}"
	}
	var leaseUntil *time.Time
	attempts := 0
	if row.leased {
		value := time.Now().UTC().Add(time.Hour)
		leaseUntil = &value
		attempts = 1
	}
	_, err := conn.Exec(context.Background(), `INSERT INTO connector_tasks(
		id,user_id,instance_id,connector_id,type,schema_version,risk_level,status,input,result,error,
		idempotency_key,lease_owner,lease_nonce,lease_until,attempts,max_attempts,available_at,expires_at
	) VALUES($1,$2,$3,$4,$5,1,'L1',$6,$7::jsonb,$8::jsonb,$9::jsonb,
		$1,$10,$11,$12,$13,3,now(),now()+interval '2 hours')`,
		row.id, row.userID, row.instanceID, row.connectorID, row.taskType, row.status, row.input, row.result, row.errorJSON,
		row.leaseOwner, row.leaseNonce, leaseUntil, attempts)
	if err != nil {
		t.Fatalf("insert legacy connector task %s: %v", row.id, err)
	}
}

type connectorTaskFenceInsert struct {
	id, instanceID, connectorID, planID string
	userID, generation                  int64
	input                               string
}

func insertCurrentConnectorTaskFenceRow(t *testing.T, conn *pgx.Conn, row connectorTaskFenceInsert) {
	t.Helper()
	if err := execCurrentConnectorTaskFenceInsert(conn, row); err != nil {
		t.Fatalf("insert current connector task %s: %v", row.id, err)
	}
}

func execCurrentConnectorTaskFenceInsert(conn *pgx.Conn, row connectorTaskFenceInsert) error {
	_, err := conn.Exec(context.Background(), `INSERT INTO connector_tasks(
		id,user_id,instance_id,connector_id,plan_id,scheduling_generation,type,schema_version,risk_level,status,
		input,result,error,idempotency_key,lease_owner,lease_nonce,attempts,max_attempts,available_at,expires_at
	) VALUES($1,$2,$3,$4,$5,$6,'gateway.account.schedulable.set',1,'L1','pending',
		$7::jsonb,'null'::jsonb,'{}'::jsonb,$1,'','',0,3,now(),now()+interval '2 hours')`,
		row.id, row.userID, row.instanceID, row.connectorID, row.planID, row.generation, row.input)
	return err
}

func insertConnectorFenceProtocolV3(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	_, err := conn.Exec(context.Background(), `INSERT INTO instances(id,user_id,name,kind,status)
		VALUES('instance-v3',91001,'instance-v3','newapi','active');
		INSERT INTO connectors(
		connector_id,user_id,instance_id,status,token_hash,version,protocol_version
	) VALUES('connector-v3',91001,'instance-v3','online','connector-v3-token','test',3)`)
	if err != nil {
		t.Fatalf("insert protocol v3 connector: %v", err)
	}
}

func topLevelConnectorTaskFenceInput(scope string, version any) string {
	raw, _ := json.Marshal(map[string]any{
		"account_id": "account-fence", "schedulable": false,
		"fence": map[string]any{"scope": scope, "version": version, "sequence": 1},
	})
	return string(raw)
}

func nestedConnectorTaskFenceInput(scope string, version any) string {
	raw, _ := json.Marshal(map[string]any{
		"account_id": "account-fence", "schedulable": false,
		"spec": map[string]any{"fence": map[string]any{"scope": scope, "version": version, "sequence": 1}},
	})
	return string(raw)
}

type connectorTaskFenceRow struct {
	id, taskType, status, planID string
	generation, attempts         int64
	input, result, errorJSON     []byte
	leaseOwner, leaseNonce       string
	leaseUntil                   *time.Time
}

func readLegacyConnectorTaskFenceRows(t *testing.T, conn *pgx.Conn) map[string]connectorTaskFenceRow {
	t.Helper()
	rows, err := conn.Query(context.Background(), `SELECT id,type,status,input,result,error,
		lease_owner,lease_nonce,lease_until,attempts FROM connector_tasks ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := make(map[string]connectorTaskFenceRow)
	for rows.Next() {
		var row connectorTaskFenceRow
		if err := rows.Scan(&row.id, &row.taskType, &row.status, &row.input, &row.result, &row.errorJSON,
			&row.leaseOwner, &row.leaseNonce, &row.leaseUntil, &row.attempts); err != nil {
			t.Fatal(err)
		}
		result[row.id] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func requireLegacyConnectorTaskFenceRowsUnchanged(t *testing.T, conn *pgx.Conn, before map[string]connectorTaskFenceRow) {
	t.Helper()
	after := readLegacyConnectorTaskFenceRows(t, conn)
	if len(after) != len(before) {
		t.Fatalf("rejected strict upgrade changed connector task count: before=%d after=%d", len(before), len(after))
	}
	for taskID, beforeRow := range before {
		afterRow, ok := after[taskID]
		if !ok || !sameConnectorTaskFenceRow(beforeRow, afterRow) {
			t.Fatalf("rejected strict upgrade changed task %s: before=%+v after=%+v present=%t", taskID, beforeRow, afterRow, ok)
		}
	}
}

func reconcileLegacyConnectorTaskFenceLeases(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), `UPDATE connector_tasks
		SET status='succeeded',result='{"operator_reconciled":true}'::jsonb,error='{}'::jsonb,
		    lease_owner='',lease_nonce='',lease_until=NULL
		WHERE id='valid-leased';
		UPDATE connector_tasks
		SET status='failed',result='null'::jsonb,error='{"code":"operator_reconciled"}'::jsonb,
		    lease_owner='',lease_nonce='',lease_until=NULL
		WHERE id IN ('malformed-leased','nested-leased','mandatory-leased')`); err != nil {
		t.Fatalf("operator-reconcile legacy leased mutations: %v", err)
	}
}

func readConnectorTaskFenceRow(t *testing.T, conn *pgx.Conn, taskID string) connectorTaskFenceRow {
	t.Helper()
	var row connectorTaskFenceRow
	err := conn.QueryRow(context.Background(), `SELECT id,type,status,COALESCE(plan_id,''),COALESCE(scheduling_generation,0),
		input,result,error,lease_owner,lease_nonce,lease_until,attempts
		FROM connector_tasks WHERE id=$1`, taskID).Scan(
		&row.id, &row.taskType, &row.status, &row.planID, &row.generation,
		&row.input, &row.result, &row.errorJSON, &row.leaseOwner, &row.leaseNonce, &row.leaseUntil, &row.attempts,
	)
	if err != nil {
		t.Fatalf("read connector task %s: %v", taskID, err)
	}
	return row
}

func readConnectorTaskFenceDowngradedRow(t *testing.T, conn *pgx.Conn, taskID string) connectorTaskFenceRow {
	t.Helper()
	var row connectorTaskFenceRow
	err := conn.QueryRow(context.Background(), `SELECT id,type,status,input,result,error,lease_owner,lease_nonce,lease_until,attempts
		FROM connector_tasks WHERE id=$1`, taskID).Scan(
		&row.id, &row.taskType, &row.status, &row.input, &row.result, &row.errorJSON,
		&row.leaseOwner, &row.leaseNonce, &row.leaseUntil, &row.attempts,
	)
	if err != nil {
		t.Fatalf("read downgraded connector task %s: %v", taskID, err)
	}
	return row
}

func sameConnectorTaskFenceRow(left, right connectorTaskFenceRow) bool {
	return left.id == right.id && left.taskType == right.taskType && left.status == right.status &&
		left.planID == right.planID && left.generation == right.generation && left.attempts == right.attempts &&
		bytes.Equal(left.input, right.input) && bytes.Equal(left.result, right.result) && bytes.Equal(left.errorJSON, right.errorJSON) &&
		left.leaseOwner == right.leaseOwner && left.leaseNonce == right.leaseNonce && equalConnectorTaskFenceTime(left.leaseUntil, right.leaseUntil)
}

func equalConnectorTaskFenceTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func (row connectorTaskFenceRow) errorCode() string {
	return connectorTaskFenceErrorCode(row.errorJSON)
}
func (row connectorTaskFenceRow) resultIsNull() bool {
	return bytes.Equal(bytes.TrimSpace(row.result), []byte("null"))
}
func (row connectorTaskFenceRow) resultBool(key string) bool {
	var value map[string]any
	return json.Unmarshal(row.result, &value) == nil && value[key] == true
}

func connectorTaskFenceErrorCode(raw []byte) string {
	var value struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &value)
	return value.Code
}

func requireConnectorTaskFenceSQLState(t *testing.T, err error, allowed ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("direct SQL mutation succeeded, want one of SQLSTATE %v", allowed)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("direct SQL mutation error=%v, want PostgreSQL error", err)
	}
	for _, code := range allowed {
		if pgErr.Code == code {
			return
		}
	}
	t.Fatalf("direct SQL mutation SQLSTATE=%s, want one of %v: %v", pgErr.Code, allowed, err)
}

func requireConnectorTaskFenceMigrationVersion(t *testing.T, conn *pgx.Conn, want uint, dirty bool) {
	t.Helper()
	var got uint
	var gotDirty bool
	if err := conn.QueryRow(context.Background(), `SELECT version,dirty FROM schema_migrations`).Scan(&got, &gotDirty); err != nil {
		t.Fatal(err)
	}
	if got != want || gotDirty != dirty {
		t.Fatalf("migration metadata=(%d,%t), want (%d,%t)", got, gotDirty, want, dirty)
	}
}

func requireConnectorTaskFenceColumnsPresent(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var count int
	if err := conn.QueryRow(context.Background(), `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='connector_tasks'
		  AND column_name IN ('plan_id','scheduling_generation')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("connector task fence columns present=%d, want 2", count)
	}
}

func requireConnectorTaskExecutingSchemaLive(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var statusDefinition string
	if err := conn.QueryRow(context.Background(), `SELECT pg_get_constraintdef(oid)
		FROM pg_catalog.pg_constraint
		WHERE conrelid='connector_tasks'::regclass AND contype='c'
		  AND pg_get_constraintdef(oid) ILIKE '%status%' LIMIT 1`).Scan(&statusDefinition); err != nil {
		t.Fatalf("read connector task status constraint: %v", err)
	}
	if !strings.Contains(statusDefinition, "executing") {
		t.Fatalf("connector task status constraint does not admit executing: %s", statusDefinition)
	}

	var activeIndexCount int
	if err := conn.QueryRow(context.Background(), `SELECT COUNT(*) FROM pg_catalog.pg_indexes
		WHERE schemaname=current_schema() AND tablename='connector_tasks'
		  AND indexdef ILIKE '%status%' AND indexdef ILIKE '%executing%'`).Scan(&activeIndexCount); err != nil {
		t.Fatalf("inspect connector task active indexes: %v", err)
	}
	if activeIndexCount == 0 {
		t.Fatal("connector task active indexes do not include executing")
	}
}

func requireRoutePlanExecutingFenceLive(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), `INSERT INTO upstream_pools(id,name,status) VALUES('pool-executing-guard','pool-executing-guard','active');
		INSERT INTO route_plans(id,user_id,instance_id,pool_id,status,scheduling_generation)
		VALUES('plan-executing-guard',91001,'instance-owner','pool-executing-guard','published',13)`); err != nil {
		t.Fatalf("seed executing route-plan guard: %v", err)
	}
	insertCurrentConnectorTaskFenceRow(t, conn, connectorTaskFenceInsert{
		id: "post74-executing-guard", userID: 91001, instanceID: "instance-owner", connectorID: "connector-owner",
		planID: "plan-executing-guard", generation: 13,
		input: topLevelConnectorTaskFenceInput("auto-switch/plan/plan-executing-guard", 13),
	})
	if _, err := conn.Exec(context.Background(), `UPDATE connector_tasks SET status='executing' WHERE id='post74-executing-guard'`); err != nil {
		t.Fatalf("move route-plan task to executing: %v", err)
	}
	for _, mutation := range []struct {
		name string
		sql  string
	}{
		{name: "generation bump", sql: `UPDATE route_plans SET scheduling_generation=scheduling_generation+1 WHERE id='plan-executing-guard'`},
		{name: "delete", sql: `DELETE FROM route_plans WHERE id='plan-executing-guard'`},
	} {
		t.Run("executing task rejects route plan "+mutation.name, func(t *testing.T) {
			_, err := conn.Exec(context.Background(), mutation.sql)
			requireConnectorTaskFenceSQLState(t, err, "23514")
		})
	}
	var generation int64
	if err := conn.QueryRow(context.Background(), `SELECT scheduling_generation FROM route_plans WHERE id='plan-executing-guard'`).Scan(&generation); err != nil {
		t.Fatalf("read route plan after rejected mutations: %v", err)
	}
	if generation != 13 {
		t.Fatalf("rejected route plan mutation advanced generation to %d", generation)
	}
	if _, err := conn.Exec(context.Background(), `UPDATE connector_tasks
		SET status='succeeded',result='{"applied":true}'::jsonb,error='{}'::jsonb,
		    lease_owner='',lease_nonce='',lease_until=NULL WHERE id='post74-executing-guard'`); err != nil {
		t.Fatalf("terminalize executing task: %v", err)
	}
	if _, err := conn.Exec(context.Background(), `UPDATE route_plans
		SET scheduling_generation=scheduling_generation+1 WHERE id='plan-executing-guard'`); err != nil {
		t.Fatalf("terminal task still blocked route plan generation bump: %v", err)
	}
	if _, err := conn.Exec(context.Background(), `DELETE FROM route_plans WHERE id='plan-executing-guard'`); err != nil {
		t.Fatalf("terminal task still blocked route plan delete: %v", err)
	}
	var remaining int
	if err := conn.QueryRow(context.Background(), `SELECT COUNT(*) FROM route_plans WHERE id='plan-executing-guard'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("terminal task route plan delete did not commit")
	}
}

func requireConnectorProtocolVersion(t *testing.T, conn *pgx.Conn, connectorID string, want int) {
	t.Helper()
	var got int
	if err := conn.QueryRow(context.Background(), `SELECT protocol_version FROM connectors WHERE connector_id=$1`, connectorID).Scan(&got); err != nil {
		t.Fatalf("read connector %s protocol version: %v", connectorID, err)
	}
	if got != want {
		t.Fatalf("connector %s protocol version=%d, want %d", connectorID, got, want)
	}
}

func requireConnectorProtocolConstraint(t *testing.T, conn *pgx.Conn, allowed ...int) {
	t.Helper()
	var definition string
	if err := conn.QueryRow(context.Background(), `SELECT pg_get_constraintdef(oid)
		FROM pg_catalog.pg_constraint
		WHERE conrelid='connectors'::regclass AND conname='connectors_protocol_version_check'`).Scan(&definition); err != nil {
		t.Fatalf("read connector protocol constraint: %v", err)
	}
	normalized := strings.Join(strings.Fields(strings.ToLower(definition)), " ")
	for _, version := range allowed {
		if !strings.Contains(normalized, fmt.Sprintf("%d", version)) {
			t.Fatalf("connector protocol constraint %q does not admit v%d", definition, version)
		}
	}
	if len(allowed) == 1 && strings.Contains(normalized, "3") {
		t.Fatalf("downgraded connector protocol constraint still admits v3: %s", definition)
	}
}

func requireConnectorTaskFenceColumnsAbsent(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var count int
	if err := conn.QueryRow(context.Background(), `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='connector_tasks'
		  AND column_name IN ('plan_id','scheduling_generation')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("connector task fence columns remained after safe down: %d", count)
	}
}
