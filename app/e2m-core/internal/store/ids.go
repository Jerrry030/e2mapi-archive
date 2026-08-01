package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"e2m.local/contracts"
)

// nowUTC is the store's single clock source for created/updated timestamps.
func nowUTC() time.Time { return time.Now().UTC() }

// rowScanner is satisfied by both pgx.Row (QueryRow) and pgx.Rows (Query), so a
// single scan helper can serve get-one and list paths.
type rowScanner interface {
	Scan(dest ...any) error
}

// mapNotFound converts pgx's no-rows sentinel into the store's ErrNotFound and
// leaves every other error untouched.
func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// newID returns a prefixed random identifier for the PostgreSQL store, which
// (unlike the in-memory store) has no monotonic sequence counter.
func newID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// rand.Read on a healthy system does not fail; fall back to a fixed
		// suffix rather than panicking in a request path.
		return prefix + "-0000000000000000"
	}
	return prefix + "-" + hex.EncodeToString(b)
}

func unmarshalLabels(raw []byte) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var labels map[string]string
	if err := json.Unmarshal(raw, &labels); err != nil {
		return nil
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}

func userRolesToStrings(roles []contracts.UserRole) []string {
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		out = append(out, string(role))
	}
	return out
}

func userRolesFromStrings(roles []string) []contracts.UserRole {
	out := make([]contracts.UserRole, 0, len(roles))
	for _, role := range roles {
		switch role {
		case "platform_admin":
			out = append(out, contracts.UserRoleAdmin)
		case "owner":
			out = append(out, contracts.UserRoleClient)
		case string(contracts.UserRoleAdmin), string(contracts.UserRoleClient), string(contracts.UserRoleSupplier):
			out = append(out, contracts.UserRole(role))
		}
	}
	return out
}

// pgxNoRows exposes pgx.ErrNoRows to postgres.go without importing pgx there.
func pgxNoRows() error { return pgx.ErrNoRows }

// isUniqueViolation reports whether err is a PostgreSQL unique_violation (23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
