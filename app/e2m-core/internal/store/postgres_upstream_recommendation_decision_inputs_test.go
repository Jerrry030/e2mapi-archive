package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/upstreamrecommendation"
)

func TestPostgresRecommendationRolloutDecisionInputsReadsOwnerExactHistoricalQuality(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	suffix := newID("decision-inputs")
	user, err := st.CreateUser(ctx, contracts.User{Email: suffix + "@example.test", PasswordHash: "test", Enabled: true, Roles: []contracts.UserRole{contracts.UserRoleClient}})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := st.CreateUser(ctx, contracts.User{Email: "foreign-" + suffix + "@example.test", PasswordHash: "test", Enabled: true, Roles: []contracts.UserRole{contracts.UserRoleClient}})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: suffix, Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatal(err)
	}
	poolID, planID := "pool-"+suffix, "plan-"+suffix
	fromChannelID, toChannelID := "from-"+suffix, "to-"+suffix
	if _, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{ID: poolID, Name: suffix, Status: contracts.UpstreamPoolActive}); err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{ID: planID, UserID: user.ID, InstanceID: instance.ID, PoolID: poolID, Status: contracts.RoutePlanPublished})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = st.ClaimRoutePlanScheduling(ctx, plan.ID, contracts.RoutePlanPublished)
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range []contracts.UpstreamChannel{
		{ID: fromChannelID, PoolID: poolID, SourceID: "source-from-" + suffix, DisplayName: "from", CredentialBindingID: "credential-from-" + suffix, Status: contracts.UpstreamChannelActive, AccountOwnership: contracts.GatewayAccountPlatformManaged},
		{ID: toChannelID, PoolID: poolID, SourceID: "source-to-" + suffix, DisplayName: "to", CredentialBindingID: "credential-to-" + suffix, Status: contracts.UpstreamChannelActive, AccountOwnership: contracts.GatewayAccountPlatformManaged},
	} {
		if _, err := st.CreateUpstreamChannel(ctx, channel); err != nil {
			t.Fatal(err)
		}
	}
	for _, binding := range []contracts.PublishedBinding{
		{PlanID: plan.ID, InstanceID: instance.ID, ChannelID: fromChannelID, RemoteID: "account-from", State: contracts.BindingActive, SchedulingGeneration: plan.SchedulingGeneration},
		{PlanID: plan.ID, InstanceID: instance.ID, ChannelID: toChannelID, RemoteID: "account-to", State: contracts.BindingActive, SchedulingGeneration: plan.SchedulingGeneration},
	} {
		if _, err := st.UpsertPublishedBinding(ctx, binding); err != nil {
			t.Fatal(err)
		}
	}
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	historical := []contracts.ChannelHealthSnapshot{
		postgresDecisionQuality("quality-from-"+suffix, fromChannelID, instance.ID, createdAt.Add(-time.Minute), contracts.HealthHealthy),
		postgresDecisionQuality("quality-to-"+suffix, toChannelID, instance.ID, createdAt.Add(-time.Minute), contracts.HealthHealthy),
	}
	for _, quality := range historical {
		if _, err := st.UpsertChannelHealthSnapshot(ctx, quality); err != nil {
			t.Fatal(err)
		}
	}
	recommendation, err := upstreamrecommendation.Build("recommendation-"+suffix, contracts.UpstreamRecommendationCandidate{
		UserID: user.ID, IntelligenceFactVersion: 1, CostLedgerFactVersion: 1, LinkFactVersion: 1, PlanGeneration: plan.SchedulingGeneration,
		FromSourceID: "source-from-" + suffix, FromChannelID: fromChannelID, FromGroupKey: "paid",
		ToSourceID: "source-to-" + suffix, ToChannelID: toChannelID, ToGroupKey: "paid", ModelKey: "gpt-test",
		PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", PerTokens: 1_000_000,
		AffectedPlanIDs: []string{plan.ID}, AffectedDownstreams: []string{instance.ID},
		EvidenceIDs: []string{historical[0].ID, historical[1].ID},
		Constraints: []contracts.UpstreamRecommendationConstraint{
			{Kind: contracts.UpstreamRecommendationConstraintQuality, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{historical[0].ID, historical[1].ID}},
			{Kind: contracts.UpstreamRecommendationConstraintCapacity, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{historical[0].ID}},
			{Kind: contracts.UpstreamRecommendationConstraintBalance, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{historical[1].ID}},
		},
		FromCost:       contracts.UpstreamRecommendationCostRange{Lower: "10", Expected: "10", Upper: "10"},
		ToCost:         contracts.UpstreamRecommendationCostRange{Lower: "5", Expected: "5", Upper: "5"},
		FormulaVersion: contracts.UpstreamRecommendationFormulaVersionV1, StrategyVersion: contracts.UpstreamRecommendationStrategyVersionV1,
		CreatedAt: createdAt, ExpiresAt: createdAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUpstreamRecommendation(ctx, recommendation); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_recommendations WHERE user_id=$1 AND id=$2`, user.ID, recommendation.ID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM channel_health_snapshots WHERE channel_id=ANY($1)`, []string{fromChannelID, toChannelID})
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM published_bindings WHERE plan_id=$1`, plan.ID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_channel_allocations WHERE channel_id=ANY($1)`, []string{fromChannelID, toChannelID})
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM route_plans WHERE id=$1`, plan.ID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_channels WHERE id=ANY($1)`, []string{fromChannelID, toChannelID})
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM upstream_pools WHERE id=$1`, poolID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM instances WHERE id=$1`, instance.ID)
		_, _ = st.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=ANY($1)`, []int64{user.ID, foreign.ID})
	})

	got, err := st.ReadRecommendationRolloutDecisionInputs(ctx, user.ID, recommendation.ID)
	if err != nil || got.Recommendation.ID != recommendation.ID || len(got.ExactQualityEvidence) != 2 {
		t.Fatalf("decision inputs=%+v err=%v", got, err)
	}
	if !ValidQualityOnlyFactAdvanceProof(got.QualityOnlyFactAdvance, user.ID,
		recommendation.IntelligenceFactVersion, got.Current.Intelligence.FactVersion.FactVersion) {
		t.Fatalf("quality-only advance proof=%+v", got.QualityOnlyFactAdvance)
	}
	if _, err := st.ReadRecommendationRolloutDecisionInputs(ctx, foreign.ID, recommendation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner recommendation error=%v", err)
	}

	// A later recomputation in the same bucket is a new revision. The exact ids
	// captured by this recommendation must continue to resolve to the original
	// evidence payload rather than the new current value.
	newer := historical[0]
	newer.ID = ""
	newer.CreatedAt = historical[0].CreatedAt.Add(time.Microsecond)
	newer.HealthState = contracts.HealthUnhealthy
	newer.QualityScore = 10
	newerRevision, err := st.UpsertChannelHealthSnapshot(ctx, newer)
	if err != nil || newerRevision.ID == historical[0].ID {
		t.Fatalf("append newer quality revision=%+v err=%v", newerRevision, err)
	}
	afterRevision, err := st.ReadRecommendationRolloutDecisionInputs(ctx, user.ID, recommendation.ID)
	if err != nil {
		t.Fatal(err)
	}
	var original contracts.ChannelHealthSnapshot
	for _, evidence := range afterRevision.ExactQualityEvidence {
		if evidence.ID == historical[0].ID {
			original = evidence
		}
	}
	if original.ID == "" || original.HealthState != contracts.HealthHealthy || original.QualityScore != 95 {
		t.Fatalf("historical recommendation evidence changed: %+v", afterRevision.ExactQualityEvidence)
	}

	// Store may report a complete interval, but Controller independently rejects
	// a typed non-quality mutation. Restoring the row lets the same fixture then
	// prove that a physical lineage gap is surfaced as Complete=false.
	currentVersion := got.Current.Intelligence.FactVersion.FactVersion
	if _, err := st.pool.Exec(ctx, `UPDATE upstream_intelligence_fact_mutations
		SET mutation_kind='link' WHERE user_id=$1 AND fact_version=$2`, user.ID, currentVersion); err != nil {
		t.Fatal(err)
	}
	nonQuality, err := st.ReadRecommendationRolloutDecisionInputs(ctx, user.ID, recommendation.ID)
	if err != nil || !nonQuality.QualityOnlyFactAdvance.Complete ||
		ValidQualityOnlyFactAdvanceProof(nonQuality.QualityOnlyFactAdvance, user.ID, recommendation.IntelligenceFactVersion, currentVersion) {
		t.Fatalf("non-quality proof was not rejected: proof=%+v err=%v", nonQuality.QualityOnlyFactAdvance, err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE upstream_intelligence_fact_mutations
		SET mutation_kind='quality' WHERE user_id=$1 AND fact_version=$2`, user.ID, currentVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `DELETE FROM upstream_intelligence_fact_mutations
		WHERE user_id=$1 AND fact_version=$2`, user.ID, currentVersion); err != nil {
		t.Fatal(err)
	}
	gap, err := st.ReadRecommendationRolloutDecisionInputs(ctx, user.ID, recommendation.ID)
	if err != nil || gap.QualityOnlyFactAdvance.Complete ||
		ValidQualityOnlyFactAdvanceProof(gap.QualityOnlyFactAdvance, user.ID, recommendation.IntelligenceFactVersion, currentVersion) {
		t.Fatalf("lineage gap did not fail closed: proof=%+v err=%v", gap.QualityOnlyFactAdvance, err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO upstream_intelligence_fact_mutations
		(user_id,fact_version,mutation_kind,evidence_id,created_at)
		SELECT user_id,fact_version,'quality',$3,updated_at
		FROM upstream_intelligence_fact_versions WHERE user_id=$1 AND fact_version=$2`, user.ID, currentVersion, historical[1].ID); err != nil {
		t.Fatal(err)
	}

	if _, err := st.pool.Exec(ctx, `DELETE FROM upstream_intelligence_fact_lineage_watermarks WHERE user_id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReadRecommendationRolloutDecisionInputs(ctx, user.ID, recommendation.ID); err == nil {
		t.Fatal("missing owner lineage watermark was accepted")
	}
}

func postgresDecisionQuality(id, channelID, instanceID string, at time.Time, state contracts.HealthState) contracts.ChannelHealthSnapshot {
	return contracts.ChannelHealthSnapshot{
		ID: id, ChannelID: channelID, InstanceID: instanceID, Model: "gpt-test", Window: contracts.Window5m,
		BucketStart: at.Truncate(time.Minute), CreatedAt: at, HealthState: state,
		SampleCount: 6, SuccessRate: 1, QualitySampleCount: 6, QualitySuccessRate: 1,
		QualityScore: 95, TTFTP95: 100, DurationP95: 500,
	}
}
