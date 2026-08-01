package recommendationrollout

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

func TestSourceBaselineStagesAreStrictlyObservable(t *testing.T) {
	if sourceBaselineSupportsEveryStage(5) {
		t.Fatal("source weight 5 repeats an integer stage and must fail closed")
	}
	if !sourceBaselineSupportsEveryStage(6) {
		t.Fatal("source weight 6 should support four distinct migrations")
	}
	want := []int{1, 2, 3, 6}
	for index, stage := range forwardRolloutStages {
		moved, err := sourceBaselineMoved(6, stage)
		if err != nil || moved != want[index] {
			t.Fatalf("stage %d moved=%d err=%v, want %d", stage, moved, err, want[index])
		}
	}
	for _, invalid := range []contracts.RecommendationRolloutStage{contracts.RecommendationRolloutStageNone, 11} {
		if _, err := sourceBaselineMoved(100, invalid); err == nil {
			t.Fatalf("invalid stage %d accepted", invalid)
		}
	}
}

func TestRecommendationTTLAllowsRolloutRequiresStrictBudget(t *testing.T) {
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	required := 4*time.Duration(DefaultObservationSeconds)*time.Second + DefaultRolloutExecutionMargin
	if recommendationTTLAllowsRollout(now, now.Add(required), DefaultObservationSeconds) {
		t.Fatal("TTL equal to observation plus execution budget must fail closed")
	}
	if !recommendationTTLAllowsRollout(now, now.Add(required+time.Nanosecond), DefaultObservationSeconds) {
		t.Fatal("TTL strictly beyond observation plus execution budget was rejected")
	}
	if recommendationTTLAllowsRollout(now, now.Add(time.Hour), 0) {
		t.Fatal("zero observation window accepted")
	}
}

func TestReadStartBaselineAcceptsExistingDestinationAndRejectsRepeatedStages(t *testing.T) {
	controller := &Controller{gateway: &workerGatewayFixture{weights: []contracts.RecommendationRolloutAccountWeight{
		{AccountID: "from", Weight: 55}, {AccountID: "idle", Weight: 30}, {AccountID: "to", Weight: 15},
	}}}
	recommendation := contracts.UpstreamRecommendation{FromChannelID: "channel-from", ToChannelID: "channel-to"}
	plan := contracts.RoutePlan{ID: "plan", InstanceID: "instance"}
	snapshot := store.UpstreamRecommendationInputs{Bindings: []contracts.PublishedBinding{
		{PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "channel-from", RemoteID: "from", State: contracts.BindingActive},
		{PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "channel-idle", RemoteID: "idle", State: contracts.BindingActive},
		{PlanID: plan.ID, InstanceID: plan.InstanceID, ChannelID: "channel-to", RemoteID: "to", State: contracts.BindingActive},
	}}
	baseline, from, to, err := controller.readStartBaseline(context.Background(), recommendation, snapshot, plan)
	if err != nil || from != "from" || to != "to" || len(baseline) != 3 {
		t.Fatalf("baseline=%+v from=%q to=%q err=%v", baseline, from, to, err)
	}

	controller.gateway = &workerGatewayFixture{weights: []contracts.RecommendationRolloutAccountWeight{
		{AccountID: "from", Weight: 5}, {AccountID: "idle", Weight: 80}, {AccountID: "to", Weight: 15},
	}}
	if _, _, _, err := controller.readStartBaseline(context.Background(), recommendation, snapshot, plan); !errors.Is(err, ErrControllerBlocked) {
		t.Fatalf("unrepresentable source baseline error=%v", err)
	}
}
