package autoswitch

import (
	"reflect"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/strategy"
)

func TestEvaluateUsesPresetToChooseHealthyBackup(t *testing.T) {
	tests := []struct {
		strategy contracts.RouteStrategyType
		want     string
	}{
		{contracts.StrategyStabilityFirst, "stable"},
		{contracts.StrategyLatencyFirst, "fast"},
		{contracts.StrategyBalanced, "balanced"},
		{contracts.StrategyCostFirst, "budget"},
	}
	for _, test := range tests {
		t.Run(string(test.strategy), func(t *testing.T) {
			f := seedFixture(t, 1, []chanSeed{
				{id: "primary", sourceID: "source-primary", status: contracts.UpstreamChannelActive, remoteID: "acc-primary", live: true, onGateway: true, schedulable: true},
				{id: "stable", sourceID: "source-stable", costHint: 4, status: contracts.UpstreamChannelActive, remoteID: "acc-stable", onGateway: true},
				{id: "fast", sourceID: "source-fast", costHint: 3, status: contracts.UpstreamChannelActive, remoteID: "acc-fast", onGateway: true},
				{id: "balanced", sourceID: "source-balanced", costHint: 2, status: contracts.UpstreamChannelActive, remoteID: "acc-balanced", onGateway: true},
				{id: "budget", sourceID: "source-budget", costHint: 1, status: contracts.UpstreamChannelActive, remoteID: "acc-budget", onGateway: true},
			})
			seedSnapshot(t, f.st, "primary", .40, contracts.HealthUnhealthy)
			seedPreferenceSnapshot(t, f.st, f.plan.InstanceID, "stable", 1, 1600, 7000, 100, 60, 60, 100)
			seedPreferenceSnapshot(t, f.st, f.plan.InstanceID, "fast", 1, 800, 4000, 95, 100, 100, 10)
			seedPreferenceSnapshot(t, f.st, f.plan.InstanceID, "balanced", 1, 1000, 5000, 92, 92, 92, 75)
			seedPreferenceSnapshot(t, f.st, f.plan.InstanceID, "budget", 1, 2800, 12000, 55, 20, 20, 20)

			decision, err := New(f.st, f.eng, WithClock(f.clk.now), WithStrategy(contracts.RouteStrategy{
				Type: test.strategy, AutoApply: true,
			})).Evaluate(f.ctx, f.plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if decision == nil || decision.ToChannelID != test.want {
				t.Fatalf("strategy %s selected %+v, want %s", test.strategy, decision, test.want)
			}
		})
	}
}

func TestAdmitHealthyReplacementsRejectsUnsafeEvidence(t *testing.T) {
	now := time.Date(2026, 7, 19, 5, 0, 0, 0, time.UTC)
	eligible := []strategy.ScoredCandidate{
		{ChannelID: "healthy", Confidence: 1, Snapshot: contracts.ChannelHealthSnapshot{HealthState: contracts.HealthHealthy, CreatedAt: now}},
		{ChannelID: "thin", Confidence: .8, Snapshot: contracts.ChannelHealthSnapshot{HealthState: contracts.HealthHealthy, CreatedAt: now}},
		{ChannelID: "unknown", Confidence: 1, Snapshot: contracts.ChannelHealthSnapshot{HealthState: contracts.HealthUnknown, CreatedAt: now}},
		{ChannelID: "degraded", Confidence: 1, Snapshot: contracts.ChannelHealthSnapshot{HealthState: contracts.HealthDegraded, CreatedAt: now}},
		{ChannelID: "stale", Confidence: 1, Snapshot: contracts.ChannelHealthSnapshot{HealthState: contracts.HealthHealthy, CreatedAt: now.Add(-replacementSnapshotMaxAge - time.Second)}},
		{ChannelID: "undated", Confidence: 1, Snapshot: contracts.ChannelHealthSnapshot{HealthState: contracts.HealthHealthy}},
	}

	got := admitHealthyReplacements(eligible, now)
	ids := make([]string, len(got))
	for i := range got {
		ids[i] = got[i].ChannelID
	}
	if want := []string{"healthy"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("admitted replacements = %v, want %v", ids, want)
	}
}
