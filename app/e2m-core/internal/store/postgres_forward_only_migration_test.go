package store

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/source"
)

func TestPostgresMigrationURL(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "postgres",
			dsn:  "postgres://user:pass@example.test:5432/e2m",
			want: "pgx5://user:pass@example.test:5432/e2m",
		},
		{
			name: "postgresql normal query retained",
			dsn:  "postgresql://user:p%40ss@example.test/e2m?application_name=e2m+core&sslmode=require",
			want: "pgx5://user:p%40ss@example.test/e2m?application_name=e2m+core&sslmode=require",
		},
		{
			name: "search path options retained",
			dsn:  "postgres://example.test/e2m?options=-csearch_path%3Dtenant_a%2Cpublic",
			want: "pgx5://example.test/e2m?options=-csearch_path%3Dtenant_a%2Cpublic",
		},
		{
			name: "same host TLS fallback retained",
			dsn:  "postgres://example.test/e2m?sslmode=prefer",
			want: "pgx5://example.test/e2m?sslmode=prefer",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := postgresMigrationURL(tt.dsn)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("postgresMigrationURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPostgresMigrationURLRejectsAmbiguousDialects(t *testing.T) {
	for _, dsn := range []string{
		"host=localhost dbname=e2m",
		"pgx5://localhost/e2m",
		"mysql://localhost/e2m",
		"postgres://localhost",
		"postgres://localhost/",
		"postgres://localhost/e2m#fragment",
		"postgres://localhost/e2m?x-migrations-table=other",
		"postgres://localhost/e2m?X-STATEMENT-TIMEOUT=1",
		"postgres://localhost/e2m?x-custom=value",
		"postgres://host-a,host-b/e2m?sslmode=disable",
		"postgres://host-a:5432,host-a:5433/e2m?sslmode=disable",
	} {
		t.Run(dsn, func(t *testing.T) {
			if got, err := postgresMigrationURL(dsn); err == nil {
				t.Fatalf("postgresMigrationURL(%q) = %q, want error", dsn, got)
			}
		})
	}
}

func TestForwardOnly0069RecoveryRunsInsideMigrationLock(t *testing.T) {
	base := &fakeMigrationDatabase{version: 68, dirty: true}
	wrapper := newForwardOnly0069Driver(base, "postgres://example.test/e2m")
	wrapper.recover = func(context.Context, string) error {
		base.events = append(base.events, "recover")
		if !base.locked {
			return errors.New("recovery ran without migrate lock")
		}
		return nil
	}
	m := newFakeMigrate(t, wrapper)

	if err := recoverForwardOnly0069DownAttempt(m, wrapper); err != nil {
		t.Fatal(err)
	}
	if want := []string{"version", "lock", "recover", "unlock"}; !reflect.DeepEqual(base.events, want) {
		t.Fatalf("events = %#v, want %#v", base.events, want)
	}
	if base.setVersionCalls != 0 {
		t.Fatalf("underlying SetVersion called %d times during recovery", base.setVersionCalls)
	}
}

func TestForwardOnly0069RecoveryFailureDoesNotDelegateMetadataWrite(t *testing.T) {
	base := &fakeMigrationDatabase{version: 68, dirty: true}
	wrapper := newForwardOnly0069Driver(base, "postgres://example.test/e2m")
	wantErr := errors.New("schema contract incomplete")
	wrapper.recover = func(context.Context, string) error { return wantErr }
	m := newFakeMigrate(t, wrapper)

	err := recoverForwardOnly0069DownAttempt(m, wrapper)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if base.setVersionCalls != 0 || base.version != 68 || !base.dirty {
		t.Fatalf("validator failure changed metadata: calls=%d version=%d dirty=%t",
			base.setVersionCalls, base.version, base.dirty)
	}
}

func TestForwardOnly0069RecoveryRechecksStateInValidator(t *testing.T) {
	for _, tt := range []struct {
		name    string
		version int
		dirty   bool
		wantErr bool
	}{
		{name: "concurrent clean 69 is idempotent", version: 69, dirty: false},
		{name: "different clean version rejected", version: 70, dirty: false, wantErr: true},
		{name: "different dirty version rejected", version: 67, dirty: true, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base := &fakeMigrationDatabase{version: 68, dirty: true}
			wrapper := newForwardOnly0069Driver(base, "postgres://example.test/e2m")
			wrapper.recover = func(context.Context, string) error {
				// Models the authoritative metadata reread after the advisory
				// lock was acquired and the state changed while waiting for it.
				if tt.version == 69 && !tt.dirty {
					return nil
				}
				return errors.New("metadata changed")
			}
			m := newFakeMigrate(t, wrapper)
			err := recoverForwardOnly0069DownAttempt(m, wrapper)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr=%t", err, tt.wantErr)
			}
			if base.setVersionCalls != 0 {
				t.Fatalf("state drift delegated %d SetVersion calls", base.setVersionCalls)
			}
		})
	}
}

func TestForwardOnly0069RecoveryStateValidation(t *testing.T) {
	for _, tt := range []struct {
		version          int
		dirty            bool
		wantAlreadyClean bool
		wantErr          bool
	}{
		{version: 68, dirty: true},
		{version: 69, dirty: false, wantAlreadyClean: true},
		{version: 68, dirty: false, wantErr: true},
		{version: 69, dirty: true, wantErr: true},
		{version: 70, dirty: false, wantErr: true},
	} {
		alreadyClean, err := validateForwardOnly0069RecoveryState(tt.version, tt.dirty)
		if (err != nil) != tt.wantErr || alreadyClean != tt.wantAlreadyClean {
			t.Errorf("state (%d,%t) = (%t,%v), want (%t,error=%t)",
				tt.version, tt.dirty, alreadyClean, err, tt.wantAlreadyClean, tt.wantErr)
		}
	}
}

func TestForwardOnly0069DriverDelegatesOrdinarySetVersion(t *testing.T) {
	base := &fakeMigrationDatabase{}
	wrapper := newForwardOnly0069Driver(base, "postgres://example.test/e2m")
	if err := wrapper.SetVersion(70, true); err != nil {
		t.Fatal(err)
	}
	if base.setVersionCalls != 1 || base.version != 70 || !base.dirty {
		t.Fatalf("ordinary SetVersion not delegated: calls=%d version=%d dirty=%t",
			base.setVersionCalls, base.version, base.dirty)
	}
}

func TestOperationalCounterTriggerContractIsExact(t *testing.T) {
	if len(requiredOperationalCounterTriggers) != 16 {
		t.Fatalf("required trigger count = %d, want 16", len(requiredOperationalCounterTriggers))
	}
	bindings := make([]operationalCounterTriggerBinding, 0, len(requiredOperationalCounterTriggers))
	for _, spec := range requiredOperationalCounterTriggers {
		bindings = append(bindings, operationalCounterTriggerBinding{
			operationalCounterTriggerSpec: spec,
			functionOID:                   42,
			relationKind:                  "r",
			enabled:                       "O",
		})
	}
	if err := validateOperationalCounterTriggerBindings(bindings, 42); err != nil {
		t.Fatal(err)
	}

	for _, enabled := range []string{"A", "R", "D"} {
		mutated := append([]operationalCounterTriggerBinding(nil), bindings...)
		mutated[0].enabled = enabled
		if err := validateOperationalCounterTriggerBindings(mutated, 42); err == nil {
			t.Fatalf("trigger enabled state %q accepted; recovery requires exact ordinary state O", enabled)
		}
	}
	mutated := append([]operationalCounterTriggerBinding(nil), bindings...)
	mutated[0].table = "wrong_schema_table"
	if err := validateOperationalCounterTriggerBindings(mutated, 42); err == nil {
		t.Fatal("wrong trigger table accepted")
	}
	mutated = append([]operationalCounterTriggerBinding(nil), bindings...)
	mutated[0].functionOID = 43
	if err := validateOperationalCounterTriggerBindings(mutated, 42); err == nil {
		t.Fatal("wrong trigger function accepted")
	}
	mutations := []struct {
		name   string
		mutate func(*operationalCounterTriggerBinding)
	}{
		{name: "arguments", mutate: func(binding *operationalCounterTriggerBinding) { binding.argumentCount = 1 }},
		{name: "wrong relation kind", mutate: func(binding *operationalCounterTriggerBinding) { binding.relationKind = "p" }},
		{name: "argument bytes", mutate: func(binding *operationalCounterTriggerBinding) { binding.argumentBytes = 1 }},
		{name: "WHEN predicate", mutate: func(binding *operationalCounterTriggerBinding) {
			predicate := "false"
			binding.whenExpression = &predicate
		}},
		{name: "old transition table", mutate: func(binding *operationalCounterTriggerBinding) {
			name := "old_rows"
			binding.oldTransitionName = &name
		}},
		{name: "new transition table", mutate: func(binding *operationalCounterTriggerBinding) {
			name := "new_rows"
			binding.newTransitionName = &name
		}},
		{name: "updated columns", mutate: func(binding *operationalCounterTriggerBinding) { binding.updatedColumns = []int16{1} }},
		{name: "constraint trigger", mutate: func(binding *operationalCounterTriggerBinding) { binding.constraintOID = 1 }},
		{name: "parent trigger", mutate: func(binding *operationalCounterTriggerBinding) { binding.parentTriggerOID = 1 }},
		{name: "deferrable", mutate: func(binding *operationalCounterTriggerBinding) { binding.deferrable = true }},
	}
	for _, mutation := range mutations {
		mutated = append([]operationalCounterTriggerBinding(nil), bindings...)
		mutation.mutate(&mutated[0])
		if err := validateOperationalCounterTriggerBindings(mutated, 42); err == nil {
			t.Errorf("trigger %s mutation accepted", mutation.name)
		}
	}
}

func TestOperationalCounterColumnAndCheckContractsRejectDrift(t *testing.T) {
	actual := make(map[string]operationalCounterActualColumn, len(requiredOperationalCounterColumns))
	for _, column := range requiredOperationalCounterColumns {
		actual[column.table+"."+column.column] = operationalCounterActualColumn{
			dataType: column.dataType, notNull: column.notNull,
			primaryKey: column.primaryKey, defaultSQL: column.defaultSQL,
		}
	}
	if err := validateOperationalCounterColumns(actual); err != nil {
		t.Fatal(err)
	}
	delete(actual, "operational_metric_counters.label")
	if err := validateOperationalCounterColumns(actual); err == nil {
		t.Fatal("missing required counter column accepted")
	}
	if err := validateOperationalCounterCheckDefinition("counter", "CHECK (total >= 0)", []string{"total >= 0"}); err != nil {
		t.Fatal(err)
	}
	if err := validateOperationalCounterCheckDefinition("counter", "CHECK (total >= 0)", []string{"closed_label"}); err == nil {
		t.Fatal("missing required counter check fragment accepted")
	}
}

func TestForwardOnly0069ValidatorUsesCurrentSchema(t *testing.T) {
	raw, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	// Guard against reintroducing the original public-schema shortcut. The
	// validator resolves CURRENT_SCHEMA and qualifies every object itself.
	if strings.Contains(source, "public.operational_") ||
		!strings.Contains(source, "SELECT CURRENT_SCHEMA()") ||
		!strings.Contains(source, `pgx.Identifier{schema, "schema_migrations"}.Sanitize()`) {
		t.Fatal("forward-only recovery validator must use CURRENT_SCHEMA and no hard-coded public objects")
	}
}

func newFakeMigrate(t *testing.T, driver database.Driver) *migrate.Migrate {
	t.Helper()
	m, err := migrate.NewWithInstance("fake", emptyMigrationSource{}, "fake", driver)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

type fakeMigrationDatabase struct {
	version         int
	dirty           bool
	locked          bool
	events          []string
	setVersionCalls int
}

func (d *fakeMigrationDatabase) Open(string) (database.Driver, error) { return d, nil }
func (d *fakeMigrationDatabase) Close() error                         { return nil }
func (d *fakeMigrationDatabase) Lock() error {
	d.events = append(d.events, "lock")
	if d.locked {
		return database.ErrLocked
	}
	d.locked = true
	return nil
}
func (d *fakeMigrationDatabase) Unlock() error {
	d.events = append(d.events, "unlock")
	if !d.locked {
		return database.ErrNotLocked
	}
	d.locked = false
	return nil
}
func (d *fakeMigrationDatabase) Run(io.Reader) error { return nil }
func (d *fakeMigrationDatabase) SetVersion(version int, dirty bool) error {
	d.events = append(d.events, "set-version")
	d.setVersionCalls++
	d.version, d.dirty = version, dirty
	return nil
}
func (d *fakeMigrationDatabase) Version() (int, bool, error) {
	d.events = append(d.events, "version")
	return d.version, d.dirty, nil
}
func (d *fakeMigrationDatabase) Drop() error { return nil }

type emptyMigrationSource struct{}

var _ source.Driver = emptyMigrationSource{}

func (emptyMigrationSource) Open(string) (source.Driver, error) { return emptyMigrationSource{}, nil }
func (emptyMigrationSource) Close() error                       { return nil }
func (emptyMigrationSource) First() (uint, error)               { return 0, errors.New("unused") }
func (emptyMigrationSource) Prev(uint) (uint, error)            { return 0, errors.New("unused") }
func (emptyMigrationSource) Next(uint) (uint, error)            { return 0, errors.New("unused") }
func (emptyMigrationSource) ReadUp(uint) (io.ReadCloser, string, error) {
	return nil, "", errors.New("unused")
}
func (emptyMigrationSource) ReadDown(uint) (io.ReadCloser, string, error) {
	return nil, "", errors.New("unused")
}
