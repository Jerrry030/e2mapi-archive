package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPostgresUpstreamCostBatchVersionReplayAndConflict(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	owner, err := st.CreateUser(ctx, testUpstreamCostPostgresUser())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner.ID) })
	facts := upstreamCostTestBatch(t, owner.ID, "pg-usage-"+newID("cost"), time.Now().UTC())
	created, version, err := st.AppendUpstreamCostFacts(ctx, facts)
	if err != nil || len(created) != 4 || version.FactVersion != 1 {
		t.Fatalf("append: facts=%+v version=%+v err=%v", created, version, err)
	}
	replayed, replayVersion, err := st.AppendUpstreamCostFacts(ctx, facts)
	if err != nil || len(replayed) != 4 || replayVersion.FactVersion != version.FactVersion {
		t.Fatalf("replay: facts=%+v version=%+v err=%v", replayed, replayVersion, err)
	}
	conflict := upstreamCostTestBatch(t, owner.ID, facts[0].UsageObservationID, facts[0].OccurredAt)
	*conflict[0].Amount = "9"
	if _, _, err := st.AppendUpstreamCostFacts(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error=%v", err)
	}
}

func testUpstreamCostPostgresUser() contracts.User {
	suffix := newID("cost-owner")
	return contracts.User{Email: suffix + "@example.com", PasswordHash: "test", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true}
}
