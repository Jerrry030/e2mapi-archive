package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryUserDeactivationRestrictsConnectorToDrainTasks(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Time{})
	user, err := st.CreateUser(ctx, contracts.User{
		Email: "drain@example.com", PasswordHash: "hash",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "gateway", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatal(err)
	}
	connector, _, err := st.UseConnectorEnrollment(ctx, seedMemoryEnrollment(t, st, user.ID, instance.ID, "drain-connector"), contracts.Connector{
		ID: "drain-connector", UserID: user.ID, InstanceID: instance.ID,
		TokenHash: "drain-token", Status: contracts.ConnectorStatusOffline,
		Version: "0.2.0", ProtocolVersion: contracts.ConnectorProtocolVersion,
	})
	if err != nil {
		t.Fatal(err)
	}

	falseValue := false
	trueValue := true
	marshal := func(value any) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	tasks := []contracts.ConnectorTask{
		{UserID: user.ID, InstanceID: instance.ID, ConnectorID: connector.ID, Type: contracts.ConnectorTaskGatewayAccountUpdate, Input: marshal(contracts.ConnectorGatewayAccountUpdateInput{})},
		{UserID: user.ID, InstanceID: instance.ID, ConnectorID: connector.ID, Type: contracts.ConnectorTaskGatewaySchedulableSet, Input: marshal(struct {
			AccountID   string `json:"account_id"`
			Schedulable *bool  `json:"schedulable"`
		}{"remote-true", &trueValue})},
		{UserID: user.ID, InstanceID: instance.ID, ConnectorID: connector.ID, Type: contracts.ConnectorTaskGatewaySchedulableSet, Input: marshal(struct {
			AccountID   string `json:"account_id"`
			Schedulable *bool  `json:"schedulable"`
		}{"remote-false", &falseValue})},
	}
	for i := range tasks {
		tasks[i].IdempotencyKey = "deactivation-task-" + string(rune('a'+i))
		created, err := st.CreateConnectorTask(ctx, tasks[i])
		if err != nil {
			t.Fatalf("create pre-disable task %d: %v", i, err)
		}
		tasks[i] = created
	}

	user.Enabled = false
	updated, err := st.UpdateUser(ctx, user)
	if err != nil || updated.DeactivationStatus != contracts.UserDeactivationDraining {
		t.Fatalf("disable user=%+v err=%v", updated, err)
	}
	if _, err := st.GetConnectorByTokenHash(ctx, "drain-token"); err != nil {
		t.Fatalf("drain connector token rejected: %v", err)
	}
	if _, err := st.UpdateConnectorToken(ctx, connector.ID, "rotated-during-drain"); !errors.Is(err, ErrConflict) {
		t.Fatalf("connector token rotation during drain error=%v, want conflict", err)
	}
	if _, err := st.RevokeConnector(ctx, connector.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("manual connector revoke during drain error=%v, want conflict", err)
	}
	if _, err := st.CreateConnectorTask(ctx, contracts.ConnectorTask{
		UserID: user.ID, InstanceID: instance.ID, ConnectorID: connector.ID,
		Type: contracts.ConnectorTaskGatewayAccountUpdate, Input: marshal(contracts.ConnectorGatewayAccountUpdateInput{}),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("new non-drain task error=%v, want conflict", err)
	}

	leased, err := st.LeaseConnectorTasks(ctx, contracts.ConnectorTaskLeaseRequest{ConnectorID: connector.ID, MaxTasks: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 1 || leased[0].ID != tasks[2].ID {
		t.Fatalf("leased tasks=%+v, want only schedulable=false", leased)
	}
}

func TestMemoryUserDeactivationFinalizesOnlyAfterBindingsRevoked(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Time{})
	user, err := st.CreateUser(ctx, contracts.User{
		Email: "finalize@example.com", PasswordHash: "hash",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, _ := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "gateway", Kind: contracts.InstanceKindSub2API})
	connector, _, err := st.UseConnectorEnrollment(ctx, seedMemoryEnrollment(t, st, user.ID, instance.ID, "finalize-connector"), contracts.Connector{
		ID: "finalize-connector", UserID: user.ID, InstanceID: instance.ID,
		TokenHash: "finalize-token", Status: contracts.ConnectorStatusOffline,
		Version: "0.2.0", ProtocolVersion: contracts.ConnectorProtocolVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "pool", Status: contracts.UpstreamPoolActive})
	channel, _ := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{PoolID: pool.ID, DisplayName: "channel", CredentialBindingID: "binding", Status: contracts.UpstreamChannelActive})
	plan, _ := st.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID, Status: contracts.RoutePlanPublished})
	binding, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: instance.ID, ChannelID: channel.ID,
		RemoteID: "remote", State: contracts.BindingActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, contracts.Session{TokenHash: "user-session", UserID: user.ID, ExpiresAt: time.Now().Add(time.Hour)}, user); err != nil {
		t.Fatal(err)
	}

	user.Enabled = false
	updatedUser, err := st.UpdateUser(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if updatedUser.DeactivationStatus != contracts.UserDeactivationDraining {
		t.Fatalf("updated user=%+v", updatedUser)
	}
	fencedPlan, err := st.GetRoutePlan(ctx, plan.ID)
	if err != nil || fencedPlan.SchedulingGeneration <= plan.SchedulingGeneration {
		t.Fatalf("plan was not generation-fenced: before=%d after=%d err=%v", plan.SchedulingGeneration, fencedPlan.SchedulingGeneration, err)
	}
	if err := st.ReconcileUserDeactivations(ctx); err != nil {
		t.Fatal(err)
	}
	current, _ := st.GetUser(ctx, user.ID)
	if current.DeactivationStatus != contracts.UserDeactivationDraining {
		t.Fatalf("status=%s before revoke", current.DeactivationStatus)
	}
	if _, err := st.GetConnectorByTokenHash(ctx, "finalize-token"); err != nil {
		t.Fatalf("connector revoked before binding receipt: %v", err)
	}

	binding.State = contracts.BindingRevoked
	currentPlan, err := st.GetRoutePlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding.SchedulingGeneration = currentPlan.SchedulingGeneration
	if _, err := st.UpsertPublishedBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	if err := st.ReconcileUserDeactivations(ctx); err != nil {
		t.Fatal(err)
	}
	current, _ = st.GetUser(ctx, user.ID)
	if current.DeactivationStatus != contracts.UserDeactivationCompleted || current.DeactivationCompletedAt == nil {
		t.Fatalf("final user=%+v", current)
	}
	persistedConnector, _ := st.GetConnector(ctx, connector.ID)
	if persistedConnector.Status != contracts.ConnectorStatusRevoked || persistedConnector.TokenHash != "" {
		t.Fatalf("connector=%+v", persistedConnector)
	}
	if _, err := st.GetSession(ctx, "user-session"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session error=%v", err)
	}
}

func TestMemoryFailedDeactivationCanBeCancelledAfterBindingsAreRevoked(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Time{})
	user, err := st.CreateUser(ctx, contracts.User{
		Email: "cancel-failed@example.com", PasswordHash: "hash",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	user.Enabled = false
	draining, err := st.UpdateUser(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.users[0].DeactivationStatus = contracts.UserDeactivationFailed
	st.users[0].DeactivationErrorCode = userDeactivationDrainFailedCode
	st.mu.Unlock()
	draining.DeactivationStatus = contracts.UserDeactivationFailed
	draining.DeactivationErrorCode = userDeactivationDrainFailedCode
	draining.Enabled = true
	updated, err := st.UpdateUser(ctx, draining)
	if err != nil {
		t.Fatalf("cancel failed deactivation: %v", err)
	}
	if updated.DeactivationStatus != contracts.UserDeactivationNone || !updated.Enabled {
		t.Fatalf("updated=%+v", updated)
	}
}

func seedMemoryEnrollment(t *testing.T, st *MemoryStore, userID int64, instanceID, connectorID string) string {
	t.Helper()
	token := connectorID + "-enrollment"
	_, err := st.CreateConnectorEnrollment(context.Background(), contracts.ConnectorEnrollment{
		UserID: userID, InstanceID: instanceID, ConnectorID: connectorID,
		TokenHash: token, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}
