package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestUI17StandaloneBatchOperationalMetricsLiveProof is an explicit release
// proof for the public standalone batch writer. It is intentionally opt-in
// because it requires the disposable PostgreSQL fixture used by UI-17.
func TestUI17StandaloneBatchOperationalMetricsLiveProof(t *testing.T) {
	if os.Getenv("E2M_UI17_OPERATIONAL_METRICS_LIVE") != "1" {
		t.Skip("E2M_UI17_OPERATIONAL_METRICS_LIVE is not set")
	}
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	before, err := st.GetOperationalMetrics(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	batch := UpstreamIntelligenceIngestBatch{
		RunID:        "ui17-run-succeeded",
		UserID:       900069,
		SourceID:     "ui17-source-0069",
		BatchNo:      1,
		BatchCount:   2,
		PayloadHash:  "4444444444444444444444444444444444444444444444444444444444444444",
		ManifestHash: "5555555555555555555555555555555555555555555555555555555555555555",
		WalletCount:  1,
	}
	if _, duplicate, err := st.UpsertUpstreamIntelligenceIngestBatch(ctx, batch); err != nil || duplicate {
		t.Fatalf("standalone accepted write duplicate=%v err=%v", duplicate, err)
	}
	if _, duplicate, err := st.UpsertUpstreamIntelligenceIngestBatch(ctx, batch); err != nil || !duplicate {
		t.Fatalf("standalone replay duplicate=%v err=%v", duplicate, err)
	}
	after, err := st.GetOperationalMetrics(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got := after.IngestFactsByOutcome["accepted"] - before.IngestFactsByOutcome["accepted"]; got != 1 {
		t.Fatalf("standalone accepted delta=%d, want 1", got)
	}
	if got := after.IngestFactsByOutcome["duplicate"] - before.IngestFactsByOutcome["duplicate"]; got != 1 {
		t.Fatalf("standalone duplicate delta=%d, want 1", got)
	}
	t.Log("standalone batch recorded accepted=1 and duplicate=1 atomically")
}
