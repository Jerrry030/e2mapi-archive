package store

import (
	"context"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestUpstreamIntelligenceFactLineageMigrationIsAtomicTypedAndReversible(t *testing.T) {
	up, err := migrationsFS.ReadFile("migrations/0070_upstream_intelligence_fact_lineage.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(up))
	for _, required := range []string{
		"create table if not exists upstream_intelligence_fact_mutations",
		"primary key (user_id, fact_version)",
		"'quality','collection','link','retention','unknown'",
		"create table if not exists upstream_intelligence_fact_lineage_watermarks",
		"select user_id, fact_version, statement_timestamp()",
		"from upstream_intelligence_fact_versions",
		"create or replace function record_upstream_intelligence_fact_mutation",
		"returns table (\n    out_user_id bigint,\n    out_fact_version bigint,\n    out_updated_at timestamptz",
		"insert into upstream_intelligence_fact_versions",
		"for update",
		"insert into upstream_intelligence_fact_lineage_watermarks",
		"values (target_user_id, previous_fact_version, statement_timestamp())",
		"update upstream_intelligence_fact_versions",
		"set fact_version = fact_version + 1",
		"insert into upstream_intelligence_fact_mutations",
		"perform record_upstream_intelligence_fact_mutation(",
		"target_user_id, 'quality', new.id",
		"from upstream_channel_allocations as allocation",
		"where allocation.channel_id = new.channel_id",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("0070 up migration lacks %q", required)
		}
	}
	functionAt := strings.Index(sql, "create or replace function record_upstream_intelligence_fact_mutation")
	if functionAt < 0 {
		t.Fatal("0070 up migration lacks lineage function")
	}
	functionSQL := sql[functionAt:]
	assertSourceOrder(t, functionSQL,
		"insert into upstream_intelligence_fact_versions",
		"for update",
		"version row must be ensured before its current value is locked")
	assertSourceOrder(t, functionSQL,
		"for update",
		"values (target_user_id, previous_fact_version, statement_timestamp())",
		"watermark must use the locked pre-mutation version")
	assertSourceOrder(t, functionSQL,
		"insert into upstream_intelligence_fact_lineage_watermarks",
		"update upstream_intelligence_fact_versions",
		"watermark must be established before the version advances")
	assertSourceOrder(t, functionSQL,
		"update upstream_intelligence_fact_versions",
		"insert into upstream_intelligence_fact_mutations",
		"version and mutation lineage must be written in that order by one function")

	down, err := migrationsFS.ReadFile("migrations/0070_upstream_intelligence_fact_lineage.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := strings.ToLower(string(down))
	for _, required := range []string{
		"create or replace function bump_upstream_intelligence_version_for_quality_snapshot",
		"insert into upstream_intelligence_fact_versions",
		"drop function if exists record_upstream_intelligence_fact_mutation(bigint, text, text)",
		"drop table if exists upstream_intelligence_fact_mutations",
		"drop table if exists upstream_intelligence_fact_lineage_watermarks",
	} {
		if !strings.Contains(downSQL, required) {
			t.Errorf("0070 down migration lacks %q", required)
		}
	}
	assertSourceOrder(t, downSQL,
		"create or replace function bump_upstream_intelligence_version_for_quality_snapshot",
		"drop function if exists record_upstream_intelligence_fact_mutation",
		"down migration must restore the quality trigger function before dropping lineage")
}

func TestPostgresEveryUpstreamIntelligenceVersionBumpRecordsTypedLineage(t *testing.T) {
	implementation, err := os.ReadFile("postgres_upstream_intelligence.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(implementation)
	for _, required := range []string{
		`recordUpstreamIntelligenceFactMutationTx(ctx, tx, input.UserID, UpstreamIntelligenceFactMutationLink, link.ID)`,
		`recordUpstreamIntelligenceFactMutationTx(ctx, tx, userID, UpstreamIntelligenceFactMutationCollection, run.ID)`,
		`SELECT out_user_id,out_fact_version,out_updated_at
		FROM record_upstream_intelligence_fact_mutation($1,$2,$3)`,
	} {
		if !strings.Contains(code, required) {
			t.Errorf("PostgreSQL intelligence writer lacks %q", required)
		}
	}
	retention, err := os.ReadFile("upstream_intelligence_retention.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(retention), `recordUpstreamIntelligenceFactMutationTx(ctx, tx, userID, UpstreamIntelligenceFactMutationRetention, "")`) {
		t.Fatal("PostgreSQL retention does not record typed lineage")
	}
	for name, source := range map[string]string{
		"postgres writer":  code,
		"retention writer": string(retention),
	} {
		if strings.Contains(source, "ON CONFLICT (user_id) DO UPDATE SET fact_version=upstream_intelligence_fact_versions.fact_version+1") ||
			strings.Contains(source, "ON CONFLICT (user_id) DO UPDATE\n\tSET fact_version") {
			t.Errorf("%s retains a direct fact-version bump outside lineage function", name)
		}
	}
}

func TestPostgresUpstreamIntelligenceFactMutationFunctionLive(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("E2M_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL store: %v", err)
	}
	t.Cleanup(st.Close)

	owner, err := st.CreateUser(ctx, contracts.User{
		Email: newID("lineage") + "@example.test", PasswordHash: "test",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, owner.ID)
	})

	var firstUser, firstVersion int64
	var firstAt time.Time
	if err := st.pool.QueryRow(ctx, `SELECT out_user_id,out_fact_version,out_updated_at
		FROM record_upstream_intelligence_fact_mutation($1,'link',$2)`, owner.ID, "link-live-1").
		Scan(&firstUser, &firstVersion, &firstAt); err != nil {
		t.Fatal(err)
	}
	if firstUser != owner.ID || firstVersion != 1 || firstAt.IsZero() {
		t.Fatalf("first mutation=(%d,%d,%s)", firstUser, firstVersion, firstAt)
	}
	assertPostgresIntelligenceMutation(t, ctx, st, owner.ID, firstVersion, "link", "link-live-1")
	var watermark int64
	if err := st.pool.QueryRow(ctx, `SELECT fact_version FROM upstream_intelligence_fact_lineage_watermarks WHERE user_id=$1`, owner.ID).Scan(&watermark); err != nil || watermark != 0 {
		t.Fatalf("new-owner watermark=%d err=%v", watermark, err)
	}

	tx, err := st.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var rolledBackVersion int64
	if err := tx.QueryRow(ctx, `SELECT out_fact_version FROM record_upstream_intelligence_fact_mutation($1,'collection',$2)`, owner.ID, "run-rolled-back").Scan(&rolledBackVersion); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var durableVersion, rolledBackRows int64
	if err := st.pool.QueryRow(ctx, `SELECT
		(SELECT fact_version FROM upstream_intelligence_fact_versions WHERE user_id=$1),
		(SELECT count(*) FROM upstream_intelligence_fact_mutations WHERE user_id=$1 AND evidence_id='run-rolled-back')`, owner.ID).
		Scan(&durableVersion, &rolledBackRows); err != nil {
		t.Fatal(err)
	}
	if rolledBackVersion != firstVersion+1 || durableVersion != firstVersion || rolledBackRows != 0 {
		t.Fatalf("rollback version=%d durable=%d rows=%d", rolledBackVersion, durableVersion, rolledBackRows)
	}

	var retentionVersion int64
	if err := st.pool.QueryRow(ctx, `SELECT out_fact_version FROM record_upstream_intelligence_fact_mutation($1,'retention',NULL)`, owner.ID).Scan(&retentionVersion); err != nil {
		t.Fatal(err)
	}
	if retentionVersion != firstVersion+1 {
		t.Fatalf("retention version=%d want=%d", retentionVersion, firstVersion+1)
	}
	assertPostgresIntelligenceMutation(t, ctx, st, owner.ID, retentionVersion, "retention", "")

	if _, err := st.pool.Exec(ctx, `SELECT * FROM record_upstream_intelligence_fact_mutation($1,'link',NULL)`, owner.ID); err == nil {
		t.Fatal("required evidence mutation accepted NULL evidence")
	}
	if _, err := st.pool.Exec(ctx, `SELECT * FROM record_upstream_intelligence_fact_mutation($1,'invalid','evidence')`, owner.ID); err == nil {
		t.Fatal("unknown mutation kind accepted")
	}
	var afterRejected int64
	if err := st.pool.QueryRow(ctx, `SELECT fact_version FROM upstream_intelligence_fact_versions WHERE user_id=$1`, owner.ID).Scan(&afterRejected); err != nil || afterRejected != retentionVersion {
		t.Fatalf("rejected calls changed version=%d err=%v", afterRejected, err)
	}
}

func TestPostgresFirstManagedQualityMutationPreservesExistingVersionAsWatermarkLive(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("E2M_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL store: %v", err)
	}
	t.Cleanup(st.Close)

	suffix := newID("lineage-cutover")
	owner, err := st.CreateUser(ctx, contracts.User{
		Email: suffix + "@example.test", PasswordHash: "test",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	channelID, sourceID := "channel-"+suffix, "source-"+suffix
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM channel_health_snapshots WHERE channel_id=$1`, channelID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_channel_allocations WHERE channel_id=$1`, channelID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, owner.ID)
	})

	// Simulate an owner whose pre-lineage writers already published version 2,
	// while no cutover watermark exists yet. The first managed write must retain
	// 2 as the oldest provable baseline, never fabricate a zero baseline.
	if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_intelligence_fact_versions
		(user_id,fact_version,updated_at) VALUES ($1,2,statement_timestamp())`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `DELETE FROM upstream_intelligence_fact_lineage_watermarks WHERE user_id=$1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_channel_allocations
		(channel_id,source_id,user_id,first_plan_id,created_at)
		VALUES ($1,$2,$3,$4,statement_timestamp())`, channelID, sourceID, owner.ID, "plan-"+suffix); err != nil {
		t.Fatal(err)
	}
	quality := contracts.ChannelHealthSnapshot{
		ID: "quality-" + suffix, ChannelID: channelID, InstanceID: "instance-" + suffix,
		Model: "gpt-lineage", Window: contracts.Window5m, BucketStart: time.Now().UTC().Truncate(time.Minute),
		SampleCount: 1, SuccessRate: 1, QualitySampleCount: 1, QualitySuccessRate: 1,
		HealthState: contracts.HealthHealthy,
	}
	quality, err = st.UpsertChannelHealthSnapshot(ctx, quality)
	if err != nil {
		t.Fatal(err)
	}

	var watermark, current int64
	if err := st.pool.QueryRow(ctx, `SELECT
		(SELECT fact_version FROM upstream_intelligence_fact_lineage_watermarks WHERE user_id=$1),
		(SELECT fact_version FROM upstream_intelligence_fact_versions WHERE user_id=$1)`, owner.ID).
		Scan(&watermark, &current); err != nil {
		t.Fatal(err)
	}
	if watermark != 2 || current != 3 {
		t.Fatalf("cutover watermark=%d current=%d, want 2 and 3", watermark, current)
	}
	assertPostgresIntelligenceMutation(t, ctx, st, owner.ID, 3, "quality", quality.ID)

	unknownHistory, err := queryQualityOnlyFactAdvanceProof(ctx, st.pool, owner.ID, 1, 3)
	if err != nil || unknownHistory.Complete || unknownHistory.LineageWatermark != 2 {
		t.Fatalf("pre-watermark proof=%+v err=%v", unknownHistory, err)
	}
	managedHistory, err := queryQualityOnlyFactAdvanceProof(ctx, st.pool, owner.ID, 2, 3)
	if err != nil || !ValidQualityOnlyFactAdvanceProof(managedHistory, owner.ID, 2, 3) {
		t.Fatalf("managed quality proof=%+v err=%v", managedHistory, err)
	}
}

func TestPostgresConcurrentFirstFactMutationsSerializeVersionsAndWatermarkLive(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("E2M_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL store: %v", err)
	}
	t.Cleanup(st.Close)

	owner, err := st.CreateUser(ctx, contracts.User{
		Email: newID("lineage-concurrent") + "@example.test", PasswordHash: "test",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, owner.ID)
	})

	versions := make([]int64, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range versions {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			errs[index] = st.pool.QueryRow(ctx, `SELECT out_fact_version
				FROM record_upstream_intelligence_fact_mutation($1,'link',$2)`,
				owner.ID, "link-concurrent-"+string(rune('a'+index))).Scan(&versions[index])
		}(i)
	}
	close(start)
	workers.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	if versions[0] != 1 || versions[1] != 2 {
		t.Fatalf("concurrent allocated versions=%v, want [1 2]", versions)
	}
	var watermark, current, mutationCount int64
	if err := st.pool.QueryRow(ctx, `SELECT
		(SELECT fact_version FROM upstream_intelligence_fact_lineage_watermarks WHERE user_id=$1),
		(SELECT fact_version FROM upstream_intelligence_fact_versions WHERE user_id=$1),
		(SELECT count(*) FROM upstream_intelligence_fact_mutations WHERE user_id=$1)`, owner.ID).
		Scan(&watermark, &current, &mutationCount); err != nil {
		t.Fatal(err)
	}
	if watermark != 0 || current != 2 || mutationCount != 2 {
		t.Fatalf("concurrent watermark=%d current=%d mutations=%d", watermark, current, mutationCount)
	}
}

func TestPostgresLinkCollectionAndRetentionWritersRecordTypedLineageLive(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("E2M_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL store: %v", err)
	}
	t.Cleanup(st.Close)

	suffix := newID("lineage-writers")
	owner, err := st.CreateUser(ctx, contracts.User{
		Email: suffix + "@example.test", PasswordHash: "test",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: owner.ID, Name: suffix, Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatal(err)
	}
	connectorID := "connector-" + suffix
	if _, err := st.pool.Exec(ctx, `INSERT INTO connectors
		(connector_id,user_id,instance_id,name,status,token_hash,version,protocol_version,gateway_state)
		VALUES ($1,$2,$3,$4,'online',$5,'1.0.0',$6,'{}'::jsonb)`, connectorID, owner.ID,
		instance.ID, suffix, "sha256:"+hash64(suffix), contracts.ConnectorProtocolVersion); err != nil {
		t.Fatal(err)
	}
	source, err := st.UpsertUpstreamIntelligenceSource(ctx, contracts.UpstreamIntelligenceSource{
		ID: "source-" + suffix, UserID: owner.ID, ConnectorID: connectorID, InstanceID: instance.ID,
		LocalRef: "local", Mode: contracts.UpstreamSourceExternal, Provider: "sub2api",
		DisplayName: suffix, Status: contracts.UpstreamSourceActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	channelID := "channel-" + suffix
	if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_channel_allocations
		(channel_id,source_id,user_id,first_plan_id,created_at)
		VALUES ($1,$2,$3,$4,statement_timestamp())`, channelID, "catalog-"+suffix, owner.ID, "plan-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_channel_allocations WHERE channel_id=$1`, channelID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, owner.ID)
	})

	verifiedAt := time.Now().UTC().Truncate(time.Microsecond)
	link, err := st.UpsertUpstreamIntelligenceLink(ctx, contracts.UpstreamIntelligenceLink{
		ID: "link-" + suffix, UserID: owner.ID, IntelligenceSourceID: source.ID,
		Scope: contracts.UpstreamLinkChannel, ChannelID: channelID, PriceDimension: contracts.UpstreamPriceInput,
		Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPostgresIntelligenceMutation(t, ctx, st, owner.ID, 1, "link", link.ID)

	completedAt := time.Now().UTC().Truncate(time.Microsecond)
	payloadHash := hash64("payload-" + suffix)
	run, err := st.CreateUpstreamCollectionRun(ctx, contracts.UpstreamCollectionRun{
		ID: "run-" + suffix, UserID: owner.ID, SourceID: source.ID, ConnectorID: connectorID,
		Trigger: contracts.UpstreamCollectionScheduled, Status: contracts.UpstreamCollectionSucceeded,
		Coverage: contracts.UpstreamCoverageComplete, StartedAt: completedAt.Add(-time.Minute), ObservedAt: completedAt,
		CompletedAt: &completedAt, SnapshotHash: hash64("snapshot-" + suffix), ManifestHash: manifestHash(t, payloadHash),
		BatchCount: 1, FactCount: 1, PageCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertUpstreamIntelligenceIngestBatch(ctx, UpstreamIntelligenceIngestBatch{
		RunID: run.ID, UserID: owner.ID, SourceID: source.ID, BatchNo: 0, BatchCount: 1,
		PayloadHash: payloadHash, ManifestHash: run.ManifestHash, OfferCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendUpstreamOfferObservation(ctx, memoryOffer(run.ID, owner.ID, source.ID, completedAt)); err != nil {
		t.Fatal(err)
	}
	finalized, version, err := st.FinalizeUpstreamCollectionRun(ctx, owner.ID, run.ID)
	if err != nil || finalized.FinalizedFactVersion != 2 || version.FactVersion != 2 {
		t.Fatalf("finalize run=%+v version=%+v err=%v", finalized, version, err)
	}
	assertPostgresIntelligenceMutation(t, ctx, st, owner.ID, 2, "collection", run.ID)

	newerCompletedAt := completedAt.Add(time.Second)
	newerPayloadHash := hash64("payload-newer-" + suffix)
	newerRun, err := st.CreateUpstreamCollectionRun(ctx, contracts.UpstreamCollectionRun{
		ID: "run-newer-" + suffix, UserID: owner.ID, SourceID: source.ID, ConnectorID: connectorID,
		Trigger: contracts.UpstreamCollectionScheduled, Status: contracts.UpstreamCollectionSucceeded,
		Coverage: contracts.UpstreamCoverageComplete, StartedAt: newerCompletedAt.Add(-time.Minute), ObservedAt: newerCompletedAt,
		CompletedAt: &newerCompletedAt, SnapshotHash: hash64("snapshot-newer-" + suffix), ManifestHash: manifestHash(t, newerPayloadHash),
		BatchCount: 1, FactCount: 1, PageCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertUpstreamIntelligenceIngestBatch(ctx, UpstreamIntelligenceIngestBatch{
		RunID: newerRun.ID, UserID: owner.ID, SourceID: source.ID, BatchNo: 0, BatchCount: 1,
		PayloadHash: newerPayloadHash, ManifestHash: newerRun.ManifestHash, OfferCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendUpstreamOfferObservation(ctx, memoryOffer(newerRun.ID, owner.ID, source.ID, newerCompletedAt)); err != nil {
		t.Fatal(err)
	}
	if _, version, err := st.FinalizeUpstreamCollectionRun(ctx, owner.ID, newerRun.ID); err != nil || version.FactVersion != 3 {
		t.Fatalf("finalize newer version=%+v err=%v", version, err)
	}
	assertPostgresIntelligenceMutation(t, ctx, st, owner.ID, 3, "collection", newerRun.ID)

	// Retention is exercised through its production writer. Make this finalized
	// run old and superseded, then prune it; the deletion and typed version bump
	// must commit together.
	if _, err := st.pool.Exec(ctx, `UPDATE upstream_collection_runs SET
		started_at=statement_timestamp()-interval '200 days'-interval '1 minute',
		observed_at=statement_timestamp()-interval '200 days',
		received_at=statement_timestamp()-interval '200 days',
		completed_at=statement_timestamp()-interval '200 days',
		created_at=statement_timestamp()-interval '200 days',
		updated_at=statement_timestamp()-interval '200 days'
		WHERE user_id=$1 AND id=$2`, owner.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	pruned, err := st.PruneUpstreamIntelligenceHistory(ctx, owner.ID, time.Now().UTC().Add(-90*24*time.Hour), 100)
	if err != nil || pruned.RunsDeleted != 1 || pruned.ResultFactVersion != 4 {
		t.Fatalf("retention result=%+v err=%v", pruned, err)
	}
	assertPostgresIntelligenceMutation(t, ctx, st, owner.ID, 4, "retention", "")
}

func assertPostgresIntelligenceMutation(t *testing.T, ctx context.Context, st *PostgresStore, userID, factVersion int64, kind, evidenceID string) {
	t.Helper()
	var gotKind string
	var gotEvidence *string
	if err := st.pool.QueryRow(ctx, `SELECT mutation_kind,evidence_id FROM upstream_intelligence_fact_mutations
		WHERE user_id=$1 AND fact_version=$2`, userID, factVersion).Scan(&gotKind, &gotEvidence); err != nil {
		t.Fatal(err)
	}
	if gotKind != kind || evidenceID == "" && gotEvidence != nil || evidenceID != "" && (gotEvidence == nil || *gotEvidence != evidenceID) {
		t.Fatalf("mutation kind=%q evidence=%v, want kind=%q evidence=%q", gotKind, gotEvidence, kind, evidenceID)
	}
}
