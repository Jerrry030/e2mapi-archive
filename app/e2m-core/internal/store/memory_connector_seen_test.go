package store

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryRecordConnectorSeenThrottlesUnchangedHeartbeats(t *testing.T) {
	st, clock, _, connector := newMemoryConnectorTaskFixture(t, "connector-seen-throttle")
	ctx := context.Background()
	runtime := contracts.ConnectorRuntimeState{
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		GatewayKind:     string(contracts.InstanceKindSub2API), GatewayStatus: "ok", GatewayConfigured: true,
	}

	clock.now = clock.now.Add(time.Second)
	first, err := st.RecordConnectorSeen(ctx, connector.ID, "0.2.0", runtime)
	if err != nil {
		t.Fatalf("record changed state: %v", err)
	}
	firstSeen := *first.LastSeenAt
	clock.now = clock.now.Add(14 * time.Second)
	second, err := st.RecordConnectorSeen(ctx, connector.ID, "0.2.0", runtime)
	if err != nil {
		t.Fatalf("record unchanged state: %v", err)
	}
	if !second.LastSeenAt.Equal(firstSeen) {
		t.Fatalf("unchanged heartbeat was persisted early: got %v want %v", second.LastSeenAt, firstSeen)
	}

	clock.now = clock.now.Add(time.Second)
	third, err := st.RecordConnectorSeen(ctx, connector.ID, "0.2.0", runtime)
	if err != nil {
		t.Fatalf("record due heartbeat: %v", err)
	}
	if !third.LastSeenAt.Equal(clock.now) {
		t.Fatalf("due heartbeat was not persisted: got %v want %v", third.LastSeenAt, clock.now)
	}

	clock.now = clock.now.Add(time.Second)
	runtime.GatewayStatus = "error"
	changed, err := st.RecordConnectorSeen(ctx, connector.ID, "0.2.0", runtime)
	if err != nil {
		t.Fatalf("record runtime change: %v", err)
	}
	if !changed.LastSeenAt.Equal(clock.now) || changed.Gateway.GatewayStatus != "error" {
		t.Fatalf("runtime change was not persisted immediately: %+v", changed)
	}
}
