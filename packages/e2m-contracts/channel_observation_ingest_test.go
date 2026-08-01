package contracts

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConnectorCostUsagePreservesMissingAndObservedZero(t *testing.T) {
	var observation ConnectorChannelObservation
	if err := json.Unmarshal([]byte(`{
		"observation_id":"usage-1","model":"gpt-test","success":true,
		"input_tokens":999,
		"cost_usage":{"input_tokens":0,"cached_input_tokens":7,"request_count":1,"group_key":"paid"}
	}`), &observation); err != nil {
		t.Fatal(err)
	}
	if observation.InputTokens != 999 || observation.CostUsage == nil {
		t.Fatalf("legacy or presence-safe usage lost: %+v", observation)
	}
	if observation.CostUsage.InputTokens == nil || *observation.CostUsage.InputTokens != 0 {
		t.Fatalf("observed zero lost: %+v", observation.CostUsage)
	}
	if observation.CostUsage.OutputTokens != nil {
		t.Fatalf("missing output became present: %+v", observation.CostUsage)
	}
	if observation.CostUsage.CachedInputTokens == nil || *observation.CostUsage.CachedInputTokens != 7 ||
		observation.CostUsage.RequestCount == nil || *observation.CostUsage.RequestCount != 1 ||
		observation.CostUsage.GroupKey == nil || *observation.CostUsage.GroupKey != "paid" {
		t.Fatalf("presence-safe usage mismatch: %+v", observation.CostUsage)
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	if !strings.Contains(encoded, `"cost_usage":{"input_tokens":0`) || strings.Contains(encoded, `"cost_usage":{"output_tokens"`) {
		t.Fatalf("presence semantics did not round-trip: %s", encoded)
	}
}

func TestConnectorCostUsageIsBackwardCompatibleAndSeparateFromLegacyTokens(t *testing.T) {
	var observation ConnectorChannelObservation
	if err := json.Unmarshal([]byte(`{"observation_id":"legacy","model":"gpt","success":true,"input_tokens":12,"output_tokens":3}`), &observation); err != nil {
		t.Fatal(err)
	}
	if observation.InputTokens != 12 || observation.OutputTokens != 3 || observation.CostUsage != nil {
		t.Fatalf("legacy payload was reinterpreted as financial usage: %+v", observation)
	}
}
