package store

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
)

const ui17PostgresSampleCount = 20

const (
	ui17PostgresChangeEventCount  = ui17ScaleSourceCount * ui17ScaleHistoryDays
	ui17PostgresRollupSampleCount = 20
)

// TestPostgresUI17Scale100Sources5000Facts is the PostgreSQL release fixture
// for UI-17. It intentionally skips when no disposable PostgreSQL DSN is
// supplied; a skipped database boundary is not release evidence.
func TestPostgresUI17Scale100Sources5000Facts(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("E2M_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL store: %v", err)
	}
	t.Cleanup(st.Close)

	writeStarted := time.Now()
	ownerID, referenceTime, fixturePrefix := seedUI17PostgresScaleFixture(t, ctx, st)
	writeElapsed := time.Since(writeStarted)
	changeWriteStarted := time.Now()
	seedUI17PostgresChangeHistory(t, ctx, st, ownerID, fixturePrefix, referenceTime)
	changeWriteElapsed := time.Since(changeWriteStarted)
	t.Logf("ui17 PostgreSQL write load: sources=%d rate_facts=%d elapsed=%s; change_events=%d history_days=%d elapsed=%s",
		ui17ScaleSourceCount, ui17ScaleFactCount, writeElapsed, ui17PostgresChangeEventCount, ui17ScaleHistoryDays, changeWriteElapsed)
	maximumP95 := optionalUI17DurationBudget(t, "E2M_UI17_PG_P95_MAX_MS")

	// Warm caches and validate the complete projection before taking the fixed
	// sample set. This warm-up is deliberately excluded from P95.
	warm, err := st.ReadUpstreamIntelligenceCurrent(ctx, ownerID, &referenceTime)
	if err != nil {
		t.Fatalf("warm PostgreSQL current read: %v", err)
	}
	assertUI17PostgresScaleSnapshot(t, ownerID, warm)

	samples := make([]time.Duration, 0, ui17PostgresSampleCount)
	for sample := 0; sample < ui17PostgresSampleCount; sample++ {
		started := time.Now()
		snapshot, readErr := st.ReadUpstreamIntelligenceCurrent(ctx, ownerID, &referenceTime)
		elapsed := time.Since(started)
		if readErr != nil {
			t.Fatalf("PostgreSQL current read sample %d: %v", sample+1, readErr)
		}
		assertUI17PostgresScaleSnapshot(t, ownerID, snapshot)
		samples = append(samples, elapsed)
	}
	sort.Slice(samples, func(left, right int) bool { return samples[left] < samples[right] })
	// Nearest-rank P95 for 20 observations is the 19th ordered sample.
	p95 := samples[18]
	t.Logf("ui17 PostgreSQL current read: sources=%d rate_facts=%d samples=%d min=%s median=%s p95=%s max=%s budget=%s",
		ui17ScaleSourceCount, ui17ScaleFactCount, len(samples), samples[0], samples[len(samples)/2], p95,
		samples[len(samples)-1], formatUI17Budget(maximumP95))
	if maximumP95 > 0 && p95 > maximumP95 {
		t.Fatalf("PostgreSQL current-read P95 %s exceeds explicitly configured budget %s", p95, maximumP95)
	}

	plan := explainUI17PostgresLatestOffers(t, ctx, st, ownerID)
	t.Logf("ui17 PostgreSQL latest-offer EXPLAIN (ANALYZE, BUFFERS):\n%s", plan)

	rollupSamples := make([]time.Duration, 0, ui17PostgresRollupSampleCount)
	var rollupRows int
	for sample := 0; sample < ui17PostgresRollupSampleCount; sample++ {
		started := time.Now()
		rollupRows = queryUI17Postgres400DayChangeRollup(t, ctx, st, ownerID, referenceTime)
		rollupSamples = append(rollupSamples, time.Since(started))
	}
	if rollupRows != ui17ScaleHistoryDays {
		t.Fatalf("400-day change rollup returned %d rows, want %d", rollupRows, ui17ScaleHistoryDays)
	}
	sort.Slice(rollupSamples, func(left, right int) bool { return rollupSamples[left] < rollupSamples[right] })
	rollupP95 := rollupSamples[18]
	t.Logf("ui17 PostgreSQL 400-day change rollup: change_events=%d rows=%d samples=%d min=%s median=%s p95=%s max=%s",
		ui17PostgresChangeEventCount, rollupRows, len(rollupSamples), rollupSamples[0], rollupSamples[len(rollupSamples)/2],
		rollupP95, rollupSamples[len(rollupSamples)-1])
	rollupPlan := explainUI17Postgres400DayChangeRollup(t, ctx, st, ownerID, referenceTime)
	t.Logf("ui17 PostgreSQL 400-day change-rollup EXPLAIN (ANALYZE, BUFFERS):\n%s", rollupPlan)
}

func assertUI17PostgresScaleSnapshot(t *testing.T, ownerID int64, snapshot UpstreamIntelligenceCurrentSnapshot) {
	t.Helper()
	if snapshot.UserID != ownerID || snapshot.FactVersion.UserID != ownerID || snapshot.FactVersion.FactVersion != 1 {
		t.Fatalf("owner/version mismatch: user=%d version=%+v", snapshot.UserID, snapshot.FactVersion)
	}
	if len(snapshot.Sources) != ui17ScaleSourceCount || len(snapshot.LatestRuns) != ui17ScaleSourceCount || len(snapshot.Offers) != ui17ScaleFactCount {
		t.Fatalf("incomplete PostgreSQL snapshot: sources=%d runs=%d offers=%d", len(snapshot.Sources), len(snapshot.LatestRuns), len(snapshot.Offers))
	}
	seen := make(map[string]struct{}, len(snapshot.Offers))
	for _, offer := range snapshot.Offers {
		key := upstreamReadOfferKey(offer)
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate current PostgreSQL offer key %q", key)
		}
		seen[key] = struct{}{}
	}
}

func seedUI17PostgresScaleFixture(t *testing.T, ctx context.Context, st *PostgresStore) (int64, time.Time, string) {
	t.Helper()
	suffix := newID("ui17-pg-scale")
	owner, err := st.CreateUser(ctx, contracts.User{
		Email: suffix + "@example.test", PasswordHash: "test",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create scale owner: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Removing sources first avoids relying on cascade ordering across the
		// source-to-Connector NO ACTION boundary.
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_intelligence_sources WHERE user_id=$1`, owner.ID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, owner.ID)
	})
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: owner.ID, Name: "UI-17 PostgreSQL scale fixture", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create scale instance: %v", err)
	}
	connectorID := "conn-" + suffix
	if _, err := st.pool.Exec(ctx, `INSERT INTO connectors
		(connector_id,user_id,instance_id,name,status,token_hash,version,protocol_version,gateway_state)
		VALUES ($1,$2,$3,'UI-17 scale Connector','online',$4,'1.0.0',$5,'{}'::jsonb)`,
		connectorID, owner.ID, instance.ID, "sha256:"+suffix, contracts.ConnectorProtocolVersion); err != nil {
		t.Fatalf("create scale Connector: %v", err)
	}

	referenceTime := time.Now().UTC().Truncate(time.Microsecond)
	prefix := "ui17-" + suffix
	if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_intelligence_sources
		(id,user_id,connector_id,instance_id,local_ref,mode,provider,display_name,currency,
		 poll_interval_seconds,status,capability_balance,capability_groups,capability_rates,capability_prices,
		 last_run_at,last_success_at,created_at,updated_at)
		SELECT $1||'-source-'||lpad(source_no::text,3,'0'),$2,$3,$4,
		       'local-'||lpad(source_no::text,3,'0'),'external','sub2api',
		       'UI-17 source '||lpad(source_no::text,3,'0'),'USD',300,'active',TRUE,TRUE,TRUE,TRUE,
		       $5::timestamptz-$6::interval,$5::timestamptz-$6::interval,$5::timestamptz-$7::interval,$5::timestamptz
		FROM generate_series(0,$8-1) AS source_no`, prefix, owner.ID, connectorID, instance.ID,
		referenceTime, "1 minute", "1 hour", ui17ScaleSourceCount); err != nil {
		t.Fatalf("insert scale sources: %v", err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_collection_runs
		(id,user_id,source_id,connector_id,trigger,status,coverage,started_at,observed_at,received_at,completed_at,
		 batch_count,fact_count,page_count,finalized_fact_version,created_at,updated_at)
		SELECT $1||'-run-'||lpad(source_no::text,3,'0'),$2,
		       $1||'-source-'||lpad(source_no::text,3,'0'),$3,'scheduled','succeeded','complete',
		       $4::timestamptz-$5::interval,$4::timestamptz-$6::interval,$4::timestamptz-$7::interval,$4::timestamptz-$7::interval,
		       1,$8/$9,1,1,$4::timestamptz-$7::interval,$4::timestamptz-$7::interval
		FROM generate_series(0,$9-1) AS source_no`, prefix, owner.ID, connectorID, referenceTime,
		"2 minutes", "1 minute", "30 seconds", ui17ScaleFactCount, ui17ScaleSourceCount); err != nil {
		t.Fatalf("insert scale runs: %v", err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_offer_observations
		(run_id,id,user_id,source_id,group_key,model_key,price_dimension,settlement_currency,
		 group_multiplier,recharge_yield,published_unit_price,per_tokens,effective_multiplier,effective_unit_cost,
		 formula_version,accuracy,coverage,observed_at,effective_at,received_at,fresh_until,missing_fields,
		 adapter_schema_version,source_revision)
		SELECT $1||'-run-'||lpad(source_no::text,3,'0'),
		       $1||'-offer-'||lpad(source_no::text,3,'0')||'-'||lpad(model_no::text,3,'0'),$2,
		       $1||'-source-'||lpad(source_no::text,3,'0'),'default',
		       'model-ui17-'||lpad(model_no::text,3,'0'),'input','USD',
		       0.8,2,(1+(source_no%20))::numeric,1000000,0.4,
		       ((1+(source_no%20))::numeric*0.4),'effective-cost/v1','exact','complete',
		       $3::timestamptz-$4::interval,$3::timestamptz-$4::interval,$3::timestamptz-$5::interval,$3::timestamptz+$6::interval,'[]'::jsonb,1,'ui17'
		FROM generate_series(0,$7-1) AS source_no
		CROSS JOIN generate_series(0,($8/$7)-1) AS model_no`, prefix, owner.ID, referenceTime,
		"1 minute", "30 seconds", "9 minutes", ui17ScaleSourceCount, ui17ScaleFactCount); err != nil {
		t.Fatalf("insert scale offers: %v", err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_intelligence_fact_versions (user_id,fact_version,updated_at)
		VALUES ($1,1,$2)`, owner.ID, referenceTime); err != nil {
		t.Fatalf("insert scale fact version: %v", err)
	}
	return owner.ID, referenceTime, prefix
}

func explainUI17PostgresLatestOffers(t *testing.T, ctx context.Context, st *PostgresStore, ownerID int64) string {
	t.Helper()
	rows, err := st.pool.Query(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
		SELECT `+prefixedUpstreamReadColumns("ranked", offerCols)+` FROM (
			SELECT offer.*,row_number() OVER (
				PARTITION BY offer.source_id,offer.group_key,offer.model_key,offer.price_dimension
				ORDER BY offer.observed_at DESC,offer.run_id DESC,offer.id DESC
			) AS read_rank
			FROM upstream_offer_observations AS offer
			JOIN upstream_collection_runs AS run ON run.user_id=offer.user_id AND run.id=offer.run_id
			WHERE offer.user_id=$1 AND run.finalized_fact_version>0
		) AS ranked WHERE read_rank=1
		ORDER BY source_id,group_key,model_key,price_dimension LIMIT $2`, ownerID, maxUpstreamIntelligenceReadOffers+1)
	if err != nil {
		t.Fatalf("EXPLAIN latest-offer query: %v", err)
	}
	defer rows.Close()
	lines := make([]string, 0, 16)
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan EXPLAIN output: %v", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read EXPLAIN output: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("EXPLAIN latest-offer query returned no plan")
	}
	return fmt.Sprintf("owner-scoped fixture; plan lines=%d\n%s", len(lines), strings.Join(lines, "\n"))
}

func seedUI17PostgresChangeHistory(t *testing.T, ctx context.Context, st *PostgresStore, ownerID int64, prefix string, referenceTime time.Time) {
	t.Helper()
	if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_change_events
		(id,user_id,source_id,event_type,event_fingerprint,absolute_change,percentage_change,
		 first_detected_at,confirmed_at,severity,impact_scope,group_key,model_key,price_dimension)
		SELECT $1||'-change-'||lpad(day_no::text,3,'0')||'-'||lpad(source_no::text,3,'0'),$2,
		       $1||'-source-'||lpad(source_no::text,3,'0'),'price_increased',
		       'price-increased-'||lpad(day_no::text,3,'0')||'-'||lpad(source_no::text,3,'0'),1,0.01,
		       date_trunc('day',$3::timestamptz)-(day_no::text||' days')::interval+interval '12 hours',
		       date_trunc('day',$3::timestamptz)-(day_no::text||' days')::interval+interval '12 hours','info','{}'::jsonb,
		       'default','model-ui17-'||lpad((source_no%50)::text,3,'0'),'input'
		FROM generate_series(0,$4-1) AS day_no
		CROSS JOIN generate_series(0,$5-1) AS source_no`,
		prefix, ownerID,
		referenceTime, ui17ScaleHistoryDays, ui17ScaleSourceCount); err != nil {
		t.Fatalf("insert 400-day change history: %v", err)
	}
}

const ui17Postgres400DayRollupSQL = `SELECT date_trunc('day',confirmed_at) AS day,
	count(*) AS change_count,
	count(*) FILTER (WHERE event_type='price_increased') AS price_increase_count,
	count(DISTINCT source_id) AS source_count
	FROM upstream_change_events
	WHERE user_id=$1 AND confirmed_at >= date_trunc('day',$2::timestamptz)-interval '399 days'
	  AND confirmed_at < date_trunc('day',$2::timestamptz)+interval '1 day'
	GROUP BY day ORDER BY day`

func queryUI17Postgres400DayChangeRollup(t *testing.T, ctx context.Context, st *PostgresStore, ownerID int64, referenceTime time.Time) int {
	t.Helper()
	rows, err := st.pool.Query(ctx, ui17Postgres400DayRollupSQL, ownerID, referenceTime)
	if err != nil {
		t.Fatalf("query 400-day change rollup: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var day time.Time
		var changes, priceIncreases, sources int
		if err := rows.Scan(&day, &changes, &priceIncreases, &sources); err != nil {
			t.Fatalf("scan 400-day change rollup: %v", err)
		}
		if changes != ui17ScaleSourceCount || priceIncreases != ui17ScaleSourceCount || sources != ui17ScaleSourceCount {
			t.Fatalf("incomplete change rollup day=%s changes=%d increased=%d sources=%d", day, changes, priceIncreases, sources)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read 400-day change rollup: %v", err)
	}
	return count
}

func explainUI17Postgres400DayChangeRollup(t *testing.T, ctx context.Context, st *PostgresStore, ownerID int64, referenceTime time.Time) string {
	t.Helper()
	rows, err := st.pool.Query(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) `+ui17Postgres400DayRollupSQL, ownerID, referenceTime)
	if err != nil {
		t.Fatalf("EXPLAIN 400-day change rollup: %v", err)
	}
	defer rows.Close()
	lines := make([]string, 0, 16)
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}
