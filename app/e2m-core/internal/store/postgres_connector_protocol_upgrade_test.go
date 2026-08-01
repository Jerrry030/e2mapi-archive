package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

// Migration 0074 keeps protocol-v2 connector identities so a deployed v3
// binary can reuse its token for the first handshake. The old stored identity
// must remain readable for authentication, but it must not lease work until a
// genuine v3 heartbeat atomically upgrades the row.
func TestPostgresConnectorProtocolV2HandshakeUpgrade(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	user, err := st.CreateUser(ctx, contracts.User{
		Email: "connector-v2-upgrade-" + newID("test") + "@example.com",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID, Name: "Connector protocol v2 upgrade", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	connectorID := newID("conn-v2-upgrade")
	tokenHash := newID("connector-token")
	enrollment, err := st.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{
		UserID: user.ID, InstanceID: instance.ID, ConnectorID: connectorID, TokenHash: newID("enroll-token"),
	})
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	connector, _, err := st.UseConnectorEnrollment(ctx, enrollment.TokenHash, contracts.Connector{
		ID: connectorID, InstanceID: instance.ID, Version: "0.2.0",
		ProtocolVersion: contracts.ConnectorProtocolVersion, TokenHash: tokenHash,
	})
	if err != nil {
		t.Fatalf("use enrollment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM connector_tasks WHERE connector_id=$1`, connector.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM connectors WHERE connector_id=$1`, connector.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM connector_enrollments WHERE connector_id=$1`, connector.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM instances WHERE id=$1`, instance.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user.ID)
	})

	task, err := st.CreateConnectorTask(ctx, contracts.ConnectorTask{
		UserID: user.ID, InstanceID: instance.ID, ConnectorID: connector.ID,
		Type: contracts.ConnectorTaskGatewayAccountsList, SchemaVersion: 1,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := st.pool.Exec(ctx,
		`UPDATE connectors SET protocol_version=2,
		 gateway_state=jsonb_set(gateway_state,'{protocol_version}','2'::jsonb,true)
		 WHERE connector_id=$1`, connector.ID); err != nil {
		t.Fatalf("downgrade stored fixture to protocol v2: %v", err)
	}

	stored, err := st.GetConnectorByTokenHash(ctx, tokenHash)
	if err != nil {
		t.Fatalf("authenticate preserved v2 connector: %v", err)
	}
	if stored.ProtocolVersion != 2 || stored.Gateway.ProtocolVersion != 2 {
		t.Fatalf("stored identity was falsely projected as upgraded: %+v", stored)
	}
	if leased, err := st.LeaseConnectorTasks(ctx, contracts.ConnectorTaskLeaseRequest{ConnectorID: connector.ID}); !errors.Is(err, ErrConflict) || len(leased) != 0 {
		t.Fatalf("v2 stored identity leased v3 work: tasks=%+v err=%v", leased, err)
	}

	runtime := contracts.ConnectorRuntimeState{
		ProtocolVersion:   contracts.ConnectorProtocolVersion,
		GatewayConfigured: true,
		GatewayKind:       "sub2api",
		GatewayStatus:     "healthy",
	}
	upgraded, err := st.RecordConnectorSeen(ctx, connector.ID, "0.3.0", runtime)
	if err != nil {
		t.Fatalf("record real v3 handshake: %v", err)
	}
	if upgraded.ProtocolVersion != contracts.ConnectorProtocolVersion ||
		upgraded.Gateway.ProtocolVersion != contracts.ConnectorProtocolVersion {
		t.Fatalf("v3 handshake did not atomically upgrade stored identity: %+v", upgraded)
	}
	leased, err := st.LeaseConnectorTasks(ctx, contracts.ConnectorTaskLeaseRequest{ConnectorID: connector.ID})
	if err != nil || len(leased) != 1 || leased[0].ID != task.ID {
		t.Fatalf("lease after v3 handshake: tasks=%+v err=%v", leased, err)
	}
}
