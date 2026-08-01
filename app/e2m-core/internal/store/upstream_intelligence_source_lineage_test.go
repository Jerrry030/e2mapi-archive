package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestUpstreamIntelligenceSourceLineageMigrationIsTypedAndReversible(t *testing.T) {
	up, err := migrationsFS.ReadFile("migrations/0073_upstream_intelligence_source_lineage.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	upSQL := strings.ToLower(string(up))
	for _, required := range []string{
		"drop constraint upstream_intelligence_fact_mutations_mutation_kind_check",
		"'quality','collection','link','source','retention','unknown'",
		"create or replace function record_upstream_intelligence_fact_mutation",
		"target_mutation_kind in ('quality','collection','link','source')",
		"for update",
		"values (target_user_id, previous_fact_version, statement_timestamp())",
	} {
		if !strings.Contains(upSQL, required) {
			t.Errorf("0073 up migration lacks %q", required)
		}
	}
	down, err := migrationsFS.ReadFile("migrations/0073_upstream_intelligence_source_lineage.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := strings.ToLower(string(down))
	for _, required := range []string{
		"set mutation_kind = 'unknown'",
		"where mutation_kind = 'source'",
		"'quality','collection','link','retention','unknown'",
	} {
		if !strings.Contains(downSQL, required) {
			t.Errorf("0073 down migration lacks %q", required)
		}
	}
	assertSourceOrder(t, downSQL,
		"set mutation_kind = 'unknown'",
		"drop constraint upstream_intelligence_fact_mutations_mutation_kind_check",
		"downgrade must preserve source rows as fail-closed unknown before narrowing the constraint")
}

func TestMemorySourceStatusMutationAdvancesTypedLineageButReplayAndMetadataDoNot(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	seedMemoryIntelligenceOwner(t, st, 171, "instance-source", "connector-source")
	input := contracts.UpstreamIntelligenceSource{
		ID: "source-status", UserID: 171, ConnectorID: "connector-source", InstanceID: "instance-source",
		LocalRef: "source-status", Mode: contracts.UpstreamSourceExternal, Provider: "sub2api",
		DisplayName: "source", Currency: "USD", PollIntervalSeconds: 300, Status: contracts.UpstreamSourceActive,
		Capabilities: contracts.UpstreamIntelligenceCapabilities{Balance: true, Groups: true, Rates: true, Prices: true},
	}
	created, err := st.UpsertUpstreamIntelligenceSource(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if version, _ := st.GetUpstreamIntelligenceFactVersion(ctx, 171); version.FactVersion != 0 {
		t.Fatalf("new source advanced version: %+v", version)
	}
	if _, err := st.UpsertUpstreamIntelligenceSource(ctx, created); err != nil {
		t.Fatal(err)
	}
	metadata := created
	metadata.DisplayName = "renamed source"
	if _, err := st.UpsertUpstreamIntelligenceSource(ctx, metadata); err != nil {
		t.Fatal(err)
	}
	if version, _ := st.GetUpstreamIntelligenceFactVersion(ctx, 171); version.FactVersion != 0 {
		t.Fatalf("non-generator metadata advanced version: %+v", version)
	}
	metadata.Status = contracts.UpstreamSourcePaused
	paused, err := st.UpsertUpstreamIntelligenceSource(ctx, metadata)
	if err != nil {
		t.Fatal(err)
	}
	version, err := st.GetUpstreamIntelligenceFactVersion(ctx, 171)
	if err != nil || version.FactVersion != 1 {
		t.Fatalf("paused version=%+v err=%v", version, err)
	}
	lineage := st.upstreamIntelFactMutations[171]
	if len(lineage) != 1 || lineage[0].Kind != UpstreamIntelligenceFactMutationSource || lineage[0].EvidenceID != created.ID {
		t.Fatalf("source lineage=%+v", lineage)
	}
	if _, err := st.UpsertUpstreamIntelligenceSource(ctx, paused); err != nil {
		t.Fatal(err)
	}
	if replayVersion, _ := st.GetUpstreamIntelligenceFactVersion(ctx, 171); replayVersion.FactVersion != 1 || len(st.upstreamIntelFactMutations[171]) != 1 {
		t.Fatalf("source replay changed lineage version=%+v lineage=%+v", replayVersion, st.upstreamIntelFactMutations[171])
	}
	proof := st.memoryQualityOnlyFactAdvanceProof(171, 0, 1)
	if !proof.Complete || len(proof.Mutations) != 1 || proof.Mutations[0].Kind != UpstreamIntelligenceFactMutationSource {
		t.Fatalf("source mutation did not fail quality-only proof closed: %+v", proof)
	}
}

func TestMemoryAtomicIngestSourceStatusMutationIsTypedAndReplaySafe(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	seedMemoryIntelligenceOwner(t, st, 172, "instance-ingest-source", "connector-ingest-source")
	payloadHash := hash64("source-ingest")
	manifest := manifestHash(t, payloadHash)
	input := UpstreamIntelligenceIngest{
		Source: contracts.UpstreamIntelligenceSource{
			ID: "source-ingest", UserID: 172, ConnectorID: "connector-ingest-source", InstanceID: "instance-ingest-source",
			LocalRef: "source-ingest", Mode: contracts.UpstreamSourceExternal, Provider: "sub2api",
			DisplayName: "source ingest", PollIntervalSeconds: 300, Status: contracts.UpstreamSourceActive,
		},
		Run: newMemoryIntelligenceRun("run-source-ingest", 172, "source-ingest", "connector-ingest-source", now),
		Batch: UpstreamIntelligenceIngestBatch{
			RunID: "run-source-ingest", UserID: 172, SourceID: "source-ingest", BatchNo: 0, BatchCount: 1,
			PayloadHash: payloadHash, ManifestHash: manifest, OfferCount: 1,
		},
		Offers: []contracts.UpstreamOfferObservation{memoryOffer("run-source-ingest", 172, "source-ingest", now)},
	}
	input.Run.ManifestHash = manifest
	if _, _, _, duplicate, err := st.IngestUpstreamIntelligenceBatch(ctx, input); err != nil || duplicate {
		t.Fatalf("first ingest duplicate=%v err=%v", duplicate, err)
	}
	input.Source.Status = contracts.UpstreamSourceDisconnected
	if _, _, _, duplicate, err := st.IngestUpstreamIntelligenceBatch(ctx, input); err != nil || !duplicate {
		t.Fatalf("changed-source replay duplicate=%v err=%v", duplicate, err)
	}
	if version, _ := st.GetUpstreamIntelligenceFactVersion(ctx, 172); version.FactVersion != 1 {
		t.Fatalf("atomic ingest source version=%+v", version)
	}
	lineage := st.upstreamIntelFactMutations[172]
	if len(lineage) != 1 || lineage[0].Kind != UpstreamIntelligenceFactMutationSource || lineage[0].EvidenceID != "source-ingest" {
		t.Fatalf("atomic ingest source lineage=%+v", lineage)
	}
	if _, _, _, duplicate, err := st.IngestUpstreamIntelligenceBatch(ctx, input); err != nil || !duplicate {
		t.Fatalf("exact replay duplicate=%v err=%v", duplicate, err)
	}
	if version, _ := st.GetUpstreamIntelligenceFactVersion(ctx, 172); version.FactVersion != 1 || len(st.upstreamIntelFactMutations[172]) != 1 {
		t.Fatalf("exact ingest replay changed lineage version=%+v lineage=%+v", version, st.upstreamIntelFactMutations[172])
	}
}

func TestPostgresSourceStatusMutationAdvancesTypedLineageAtomicallyLive(t *testing.T) {
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

	suffix := newID("source-lineage")
	owner, err := st.CreateUser(ctx, contracts.User{
		Email: suffix + "@example.test", PasswordHash: "test", Enabled: true,
		Roles: []contracts.UserRole{contracts.UserRoleClient},
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
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, owner.ID)
	})
	input := contracts.UpstreamIntelligenceSource{
		ID: "source-" + suffix, UserID: owner.ID, ConnectorID: connectorID, InstanceID: instance.ID,
		LocalRef: "source", Mode: contracts.UpstreamSourceExternal, Provider: "sub2api", DisplayName: suffix,
		Currency: "USD", PollIntervalSeconds: 300, Status: contracts.UpstreamSourceActive,
	}
	created, err := st.UpsertUpstreamIntelligenceSource(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	version, err := st.GetUpstreamIntelligenceFactVersion(ctx, owner.ID)
	if err != nil || version.FactVersion != 0 {
		t.Fatalf("new source version=%+v err=%v", version, err)
	}
	created.DisplayName += " renamed"
	created, err = st.UpsertUpstreamIntelligenceSource(ctx, created)
	if err != nil {
		t.Fatal(err)
	}
	version, _ = st.GetUpstreamIntelligenceFactVersion(ctx, owner.ID)
	if version.FactVersion != 0 {
		t.Fatalf("metadata-only source version=%+v", version)
	}
	created.Status = contracts.UpstreamSourcePaused
	paused, err := st.UpsertUpstreamIntelligenceSource(ctx, created)
	if err != nil {
		t.Fatal(err)
	}
	version, err = st.GetUpstreamIntelligenceFactVersion(ctx, owner.ID)
	if err != nil || version.FactVersion != 1 {
		t.Fatalf("paused source version=%+v err=%v", version, err)
	}
	assertPostgresIntelligenceMutation(t, ctx, st, owner.ID, 1, "source", created.ID)
	if _, err := st.UpsertUpstreamIntelligenceSource(ctx, paused); err != nil {
		t.Fatal(err)
	}
	var durableVersion, lineageRows int64
	if err := st.pool.QueryRow(ctx, `SELECT
		(SELECT fact_version FROM upstream_intelligence_fact_versions WHERE user_id=$1),
		(SELECT count(*) FROM upstream_intelligence_fact_mutations WHERE user_id=$1)`, owner.ID).
		Scan(&durableVersion, &lineageRows); err != nil {
		t.Fatal(err)
	}
	if durableVersion != 1 || lineageRows != 1 {
		t.Fatalf("source replay version=%d lineage=%d", durableVersion, lineageRows)
	}
	proof, err := queryQualityOnlyFactAdvanceProof(ctx, st.pool, owner.ID, 0, 1)
	if err != nil || !proof.Complete || proof.Mutations[0].Kind != UpstreamIntelligenceFactMutationSource {
		t.Fatalf("source proof=%+v err=%v", proof, err)
	}
	if _, err := st.pool.Exec(ctx, `ALTER TABLE upstream_intelligence_fact_mutations
		ADD CONSTRAINT source_lineage_atomicity_probe CHECK (mutation_kind<>'source') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `ALTER TABLE upstream_intelligence_fact_mutations
			DROP CONSTRAINT IF EXISTS source_lineage_atomicity_probe`)
	})
	paused.Status = contracts.UpstreamSourceDisconnected
	if _, err := st.UpsertUpstreamIntelligenceSource(ctx, paused); err == nil {
		t.Fatal("source update committed without required lineage")
	}
	stored, err := st.GetUpstreamIntelligenceSource(ctx, owner.ID, paused.ID)
	if err != nil || stored.Status != contracts.UpstreamSourcePaused {
		t.Fatalf("failed lineage did not roll back source: source=%+v err=%v", stored, err)
	}
	version, _ = st.GetUpstreamIntelligenceFactVersion(ctx, owner.ID)
	if version.FactVersion != 1 {
		t.Fatalf("failed lineage advanced version: %+v", version)
	}
}

func TestPostgresAtomicIngestSourceStatusMutationIsTypedAndReplaySafeLive(t *testing.T) {
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

	suffix := newID("source-ingest-lineage")
	owner, err := st.CreateUser(ctx, contracts.User{
		Email: suffix + "@example.test", PasswordHash: "test", Enabled: true,
		Roles: []contracts.UserRole{contracts.UserRoleClient},
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
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, owner.ID)
	})
	observedAt := time.Now().UTC().Truncate(time.Microsecond)
	payloadHash := hash64("source-ingest-" + suffix)
	manifest := manifestHash(t, payloadHash)
	input := UpstreamIntelligenceIngest{
		Source: contracts.UpstreamIntelligenceSource{
			ID: "source-" + suffix, UserID: owner.ID, ConnectorID: connectorID, InstanceID: instance.ID,
			LocalRef: "source", Mode: contracts.UpstreamSourceExternal, Provider: "sub2api", DisplayName: suffix,
			PollIntervalSeconds: 300, Status: contracts.UpstreamSourceActive,
		},
		Run: newMemoryIntelligenceRun("run-"+suffix, owner.ID, "source-"+suffix, connectorID, observedAt),
		Batch: UpstreamIntelligenceIngestBatch{
			RunID: "run-" + suffix, UserID: owner.ID, SourceID: "source-" + suffix,
			BatchNo: 0, BatchCount: 1, PayloadHash: payloadHash, ManifestHash: manifest, OfferCount: 1,
		},
		Offers: []contracts.UpstreamOfferObservation{memoryOffer("run-"+suffix, owner.ID, "source-"+suffix, observedAt)},
	}
	input.Run.ManifestHash = manifest
	if _, _, _, duplicate, err := st.IngestUpstreamIntelligenceBatch(ctx, input); err != nil || duplicate {
		t.Fatalf("first ingest duplicate=%v err=%v", duplicate, err)
	}
	input.Source.Status = contracts.UpstreamSourceDisconnected
	if _, _, _, duplicate, err := st.IngestUpstreamIntelligenceBatch(ctx, input); err != nil || !duplicate {
		t.Fatalf("changed-source replay duplicate=%v err=%v", duplicate, err)
	}
	version, err := st.GetUpstreamIntelligenceFactVersion(ctx, owner.ID)
	if err != nil || version.FactVersion != 1 {
		t.Fatalf("atomic ingest source version=%+v err=%v", version, err)
	}
	assertPostgresIntelligenceMutation(t, ctx, st, owner.ID, 1, "source", input.Source.ID)
	if _, _, _, duplicate, err := st.IngestUpstreamIntelligenceBatch(ctx, input); err != nil || !duplicate {
		t.Fatalf("exact replay duplicate=%v err=%v", duplicate, err)
	}
	var durableVersion, lineageRows int64
	if err := st.pool.QueryRow(ctx, `SELECT
		(SELECT fact_version FROM upstream_intelligence_fact_versions WHERE user_id=$1),
		(SELECT count(*) FROM upstream_intelligence_fact_mutations WHERE user_id=$1)`, owner.ID).
		Scan(&durableVersion, &lineageRows); err != nil {
		t.Fatal(err)
	}
	if durableVersion != 1 || lineageRows != 1 {
		t.Fatalf("exact ingest replay version=%d lineage=%d", durableVersion, lineageRows)
	}
}
