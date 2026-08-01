package store

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPostgresUpstreamIntelligenceRetentionOwnerBoundedProtectedAndVersioned(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("E2M_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL store: %v", err)
	}
	t.Cleanup(st.Close)

	now := time.Now().UTC().Truncate(time.Microsecond)
	ownerA, sourceA := seedPostgresRetentionOwner(t, ctx, st, "a", now, true)
	ownerB, sourceB := seedPostgresRetentionOwner(t, ctx, st, "b", now, false)
	cutoff := now.Add(-90 * 24 * time.Hour)

	owners, err := st.ListUpstreamIntelligenceRetentionOwners(ctx, cutoff, 0, 100)
	if err != nil || !containsInt64(owners, ownerA) || !containsInt64(owners, ownerB) {
		t.Fatalf("retention owners=%v err=%v", owners, err)
	}
	beforeCurrent := currentPostgresRetentionOfferIDs(t, ctx, st, ownerA, sourceA, now)
	beforeOwnerB := postgresRetentionRunCount(t, ctx, st, ownerB, sourceB)

	first, err := st.PruneUpstreamIntelligenceHistory(ctx, ownerA, cutoff, 1)
	if err != nil || first.RunsDeleted != 1 || first.BatchesDeleted != 1 || first.WalletsDeleted != 1 ||
		first.OffersDeleted != 1 || first.FinalizedDeleted != 1 || first.ResultFactVersion != 8 {
		t.Fatalf("first prune=%+v err=%v", first, err)
	}
	assertPostgresRetentionRun(t, ctx, st, ownerA, "a-old-delete-1", false)
	assertPostgresRetentionRun(t, ctx, st, ownerA, "a-old-delete-2", true)
	if got := postgresRetentionRunCount(t, ctx, st, ownerB, sourceB); got != beforeOwnerB {
		t.Fatalf("owner B changed: before=%d after=%d", beforeOwnerB, got)
	}

	second, err := st.PruneUpstreamIntelligenceHistory(ctx, ownerA, cutoff, 100)
	if err != nil || second.RunsDeleted != 1 || second.ResultFactVersion != 9 {
		t.Fatalf("second prune=%+v err=%v", second, err)
	}
	assertPostgresRetentionRun(t, ctx, st, ownerA, "a-old-delete-2", false)
	for _, protected := range []string{"a-event", "a-absence", "a-legacy-current", "a-current"} {
		assertPostgresRetentionRun(t, ctx, st, ownerA, protected, true)
	}
	afterCurrent := currentPostgresRetentionOfferIDs(t, ctx, st, ownerA, sourceA, now)
	if strings.Join(beforeCurrent, ",") != strings.Join(afterCurrent, ",") {
		t.Fatalf("current offers changed: before=%v after=%v", beforeCurrent, afterCurrent)
	}
	var changes, absences int
	if err := st.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM upstream_change_events WHERE user_id=$1),
		(SELECT count(*) FROM upstream_snapshot_absences WHERE user_id=$1)`, ownerA).Scan(&changes, &absences); err != nil || changes != 1 || absences != 1 {
		t.Fatalf("protected references changes=%d absences=%d err=%v", changes, absences, err)
	}
	version, err := st.GetUpstreamIntelligenceFactVersion(ctx, ownerA)
	if err != nil || version.FactVersion != 9 {
		t.Fatalf("owner A version=%+v err=%v", version, err)
	}
	ownerBVersion, err := st.GetUpstreamIntelligenceFactVersion(ctx, ownerB)
	if err != nil || ownerBVersion.FactVersion != 3 {
		t.Fatalf("owner B version=%+v err=%v", ownerBVersion, err)
	}
	empty, err := st.PruneUpstreamIntelligenceHistory(ctx, ownerA, cutoff, 100)
	if err != nil || empty.RunsDeleted != 0 || empty.ResultFactVersion != 0 {
		t.Fatalf("empty prune=%+v err=%v", empty, err)
	}
	version, _ = st.GetUpstreamIntelligenceFactVersion(ctx, ownerA)
	if version.FactVersion != 9 {
		t.Fatalf("empty prune advanced version: %+v", version)
	}
}

func seedPostgresRetentionOwner(t *testing.T, ctx context.Context, st *PostgresStore, label string, now time.Time, withProtected bool) (int64, string) {
	t.Helper()
	owner, err := st.CreateUser(ctx, contracts.User{
		Email: newID("retention-"+label) + "@example.test", PasswordHash: "test",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_intelligence_sources WHERE user_id=$1`, owner.ID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, owner.ID)
	})
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: owner.ID, Name: "retention " + label, Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatal(err)
	}
	connectorID := "conn-retention-" + newID(label)
	if _, err := st.pool.Exec(ctx, `INSERT INTO connectors
		(connector_id,user_id,instance_id,name,status,token_hash,version,protocol_version,gateway_state)
		VALUES ($1,$2,$3,$4,'online',$5,'1.0.0',$6,'{}'::jsonb)`,
		connectorID, owner.ID, instance.ID, "retention "+label, "sha256:"+newID("retention"), contracts.ConnectorProtocolVersion); err != nil {
		t.Fatal(err)
	}
	sourceID := label + "-source"
	if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_intelligence_sources
		(id,user_id,connector_id,instance_id,local_ref,mode,provider,display_name,poll_interval_seconds,status,
		 capability_balance,capability_groups,capability_rates,capability_prices,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5::text,'external','sub2api',$6,300,'active',true,true,true,true,$7,$7)`,
		sourceID, owner.ID, connectorID, instance.ID, "local-"+label, "retention "+label, now); err != nil {
		t.Fatal(err)
	}

	runs := []struct {
		id    string
		days  int
		model string
	}{
		{label + "-old-delete-1", 170, "common"},
		{label + "-old-delete-2", 160, "common"},
	}
	if withProtected {
		runs = append(runs,
			struct {
				id    string
				days  int
				model string
			}{label + "-event", 150, "event"},
			struct {
				id    string
				days  int
				model string
			}{label + "-absence", 140, "common"},
			struct {
				id    string
				days  int
				model string
			}{label + "-legacy-current", 130, "legacy"},
		)
	}
	runs = append(runs, struct {
		id    string
		days  int
		model string
	}{label + "-current", 100, "common"})
	for index, run := range runs {
		observedAt := now.Add(-time.Duration(run.days) * 24 * time.Hour)
		version := int64(index + 1)
		if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_collection_runs
			(id,user_id,source_id,connector_id,trigger,status,coverage,started_at,observed_at,received_at,completed_at,
			 snapshot_hash,manifest_hash,batch_count,fact_count,page_count,finalized_fact_version,created_at,updated_at)
			VALUES ($1,$2,$3,$4,'scheduled','succeeded','complete',$5,$5,$5,$5,$6,$7,1,1,1,$8,$5,$5)`,
			run.id, owner.ID, sourceID, connectorID, observedAt, capacityTestHash('a'), capacityTestHash('b'), version); err != nil {
			t.Fatal(err)
		}
		if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_ingest_batches
			(run_id,user_id,source_id,batch_no,batch_count,payload_hash,manifest_hash,wallet_count,offer_count,received_at)
			VALUES ($1,$2,$3,0,1,$4,$5,1,1,$6)`, run.id, owner.ID, sourceID, capacityTestHash(byte('c'+index%4)), capacityTestHash('b'), observedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_wallet_observations
			(run_id,id,user_id,source_id,balance_amount,unit_kind,currency,observed_at,received_at,fresh_until,accuracy,coverage)
			VALUES ($1,$2,$3,$4,100,'fiat','USD',$5::timestamptz,$5::timestamptz,$5::timestamptz+interval '1 hour','exact','complete')`,
			run.id, "wallet-"+run.id, owner.ID, sourceID, observedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_offer_observations
			(run_id,id,user_id,source_id,group_key,model_key,price_dimension,settlement_currency,published_unit_price,per_tokens,
			 accuracy,coverage,observed_at,effective_at,received_at,fresh_until,adapter_schema_version)
			VALUES ($1,$2,$3,$4,'default',$5,'input','USD',1,1000000,'exact','complete',$6::timestamptz,$6::timestamptz,$6::timestamptz,$6::timestamptz+interval '1 hour',1)`,
			run.id, "offer-"+run.id, owner.ID, sourceID, run.model, observedAt); err != nil {
			t.Fatal(err)
		}
	}
	if withProtected {
		currentAt := now.Add(-100 * 24 * time.Hour)
		if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_offer_observations
			(run_id,id,user_id,source_id,group_key,model_key,price_dimension,settlement_currency,published_unit_price,per_tokens,
			 accuracy,coverage,observed_at,effective_at,received_at,fresh_until,adapter_schema_version)
			VALUES ($1,$2,$3,$4,'default','event','input','USD',2,1000000,'exact','complete',$5::timestamptz,$5::timestamptz,$5::timestamptz,$5::timestamptz+interval '1 hour',1)`,
			label+"-current", "offer-"+label+"-current-event", owner.ID, sourceID, currentAt); err != nil {
			t.Fatal(err)
		}
		if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_snapshot_absences
			(source_id,user_id,comparison_key,last_present_observation_id,last_present_run_id,updated_at)
			VALUES ($1,$2,'model:absence',$3,$4,$5)`, sourceID, owner.ID, "offer-"+label+"-absence", label+"-absence", now); err != nil {
			t.Fatal(err)
		}
		if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_change_events
			(id,user_id,source_id,event_type,event_fingerprint,before_observation_id,first_detected_at,confirmed_at,severity)
			VALUES ($1,$2,$3,'price_increased',$4,$5,$6,$6,'info')`,
			"change-"+newID(label), owner.ID, sourceID, "fingerprint-"+newID(label), "offer-"+label+"-event", now.Add(-120*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	version := int64(3)
	if withProtected {
		version = 7
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_intelligence_fact_versions (user_id,fact_version,updated_at) VALUES ($1,$2,$3)`, owner.ID, version, now); err != nil {
		t.Fatal(err)
	}
	return owner.ID, sourceID
}

func currentPostgresRetentionOfferIDs(t *testing.T, ctx context.Context, st *PostgresStore, ownerID int64, sourceID string, referenceTime time.Time) []string {
	t.Helper()
	snapshot, err := st.ReadUpstreamIntelligenceCurrent(ctx, ownerID, &referenceTime)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0)
	for _, offer := range snapshot.Offers {
		if offer.SourceID == sourceID {
			ids = append(ids, offer.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func postgresRetentionRunCount(t *testing.T, ctx context.Context, st *PostgresStore, ownerID int64, sourceID string) int {
	t.Helper()
	var count int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM upstream_collection_runs WHERE user_id=$1 AND source_id=$2`, ownerID, sourceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertPostgresRetentionRun(t *testing.T, ctx context.Context, st *PostgresStore, ownerID int64, runID string, want bool) {
	t.Helper()
	var exists bool
	if err := st.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM upstream_collection_runs WHERE user_id=$1 AND id=$2)`, ownerID, runID).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != want {
		t.Fatalf("owner %d run %s exists=%v want=%v", ownerID, runID, exists, want)
	}
}

func containsInt64(values []int64, want int64) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
