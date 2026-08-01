package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryCostObservationAndJobAreAtomicIdempotentAndLeased(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	user, err := st.CreateUser(ctx, contracts.User{
		Email: "cost-job@example.com", PasswordHash: "test", Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, _ := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "cost-job", Kind: contracts.InstanceKindSub2API})
	pool, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "cost-job"})
	channel, _ := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: pool.ID, DisplayName: "cost-job"})
	plan, _ := st.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID, Status: contracts.RoutePlanPublished})
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: instance.ID, ChannelID: channel.ID, State: contracts.BindingActive,
	}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	zero, requests := int64(0), int64(1)
	obs := contracts.ChannelObservation{ID: "core-cost-1", ChannelID: channel.ID, InstanceID: instance.ID, PoolID: pool.ID, Model: "gpt", Success: true, ObservedAt: at}
	job := UpstreamCostAttributionJob{
		UsageObservationID: obs.ID, UserID: user.ID, ChannelID: channel.ID, InstanceID: instance.ID,
		ModelKey: obs.Model, GroupKey: "paid", InputTokens: &zero, RequestCount: &requests,
		OccurredAt: at, CalculationVersion: contracts.UpstreamCostCalculationVersionV1,
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := st.AppendChannelObservationWithCostJob(ctx, obs, &job); err != nil {
			t.Fatalf("append attempt %d: %v", attempt+1, err)
		}
	}
	if len(st.channelObs) != 1 || len(st.upstreamCostJobs) != 1 {
		t.Fatalf("replay duplicated rows: observations=%d jobs=%d", len(st.channelObs), len(st.upstreamCostJobs))
	}
	conflict := job
	changed := int64(2)
	conflict.RequestCount = &changed
	if _, err := st.AppendChannelObservationWithCostJob(ctx, obs, &conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed cost replay error=%v, want conflict", err)
	}
	claim, claimed, err := st.ClaimUpstreamCostAttributionJob(ctx, "worker-a", time.Minute)
	if err != nil || !claimed || claim.LeaseVersion != 1 || claim.Attempts != 1 {
		t.Fatalf("first claim=%+v claimed=%v err=%v", claim, claimed, err)
	}
	if _, _, err := st.CompleteUpstreamCostAttributionJob(ctx, UpstreamCostAttributionJob{
		UsageObservationID: claim.UsageObservationID, LeaseOwner: "worker-b", LeaseVersion: claim.LeaseVersion,
	}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed stale completion error=%v", err)
	}
	if _, err := st.RetryUpstreamCostAttributionJob(ctx, claim, "evidence_read_failed", 0); err != nil {
		t.Fatal(err)
	}
	retry, claimed, err := st.ClaimUpstreamCostAttributionJob(ctx, "worker-b", time.Minute)
	if err != nil || !claimed || retry.LeaseVersion != 2 || retry.Attempts != 2 {
		t.Fatalf("retry claim=%+v claimed=%v err=%v", retry, claimed, err)
	}
}

func TestPostgresCostAttributionSourceRequiresFinalizedHistoricalOffers(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0062_upstream_cost_attribution_jobs.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"REFERENCES channel_observations(id)",
		"status IN ('pending','processing','retrying','succeeded')",
		"lease_version",
		"idx_upstream_cost_attribution_jobs_claim",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("0062 missing %q", required)
		}
	}
	source, err := os.ReadFile("postgres_upstream_cost_attribution_job.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"r.finalized_fact_version > 0",
		"link_scope='channel'",
		"verified_at IS NOT NULL",
		"FOR UPDATE SKIP LOCKED",
		"appendUpstreamCostFactsTx",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("postgres attribution lacks %q", required)
		}
	}
}
