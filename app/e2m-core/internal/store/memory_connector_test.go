package store

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryRevokedConnectorCannotBeRebound(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	createMemoryConnectorTestUser(t, st)
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: 1,
		Name:   "Revoked connector binding",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	enrollment, err := st.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{
		UserID:      instance.UserID,
		InstanceID:  instance.ID,
		ConnectorID: "connector-revoked-binding",
		TokenHash:   "revoked-binding-enrollment",
	})
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	connector, _, err := st.UseConnectorEnrollment(ctx, enrollment.TokenHash, contracts.Connector{
		ID:              enrollment.ConnectorID,
		InstanceID:      instance.ID,
		Version:         "0.1.0-test",
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		TokenHash:       "revoked-binding-token",
	})
	if err != nil {
		t.Fatalf("use enrollment: %v", err)
	}
	if _, err := st.RevokeConnector(ctx, connector.ID); err != nil {
		t.Fatalf("revoke connector: %v", err)
	}
	if _, err := st.UpdateInstanceConnector(ctx, instance.ID, connector.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("rebind revoked connector error=%v, want ErrConflict", err)
	}
	persisted, err := st.GetInstance(ctx, instance.ID)
	if err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if persisted.ConnectorID != "" {
		t.Fatalf("revoked connector was rebound: %+v", persisted)
	}
}

func TestMemoryConnectorProtocolV2HandshakeUpgrade(t *testing.T) {
	st, _, _, connector := newMemoryConnectorTaskFixture(t, "connector-memory-v2-upgrade")
	ctx := context.Background()
	st.mu.Lock()
	for index := range st.connectors {
		if st.connectors[index].ID == connector.ID {
			st.connectors[index].ProtocolVersion = 2
			st.connectors[index].Gateway.ProtocolVersion = 2
		}
	}
	st.mu.Unlock()

	stored, err := st.GetConnectorByTokenHash(ctx, connector.TokenHash)
	if err != nil || stored.ProtocolVersion != 2 || stored.Gateway.ProtocolVersion != 2 {
		t.Fatalf("preserved v2 identity=%+v err=%v", stored, err)
	}
	if leased, err := st.LeaseConnectorTasks(ctx, contracts.ConnectorTaskLeaseRequest{
		ConnectorID: connector.ID, ProtocolVersion: contracts.ConnectorProtocolVersion,
	}); !errors.Is(err, ErrConflict) || len(leased) != 0 {
		t.Fatalf("stored v2 identity leased work=%+v err=%v", leased, err)
	}
	upgraded, err := st.RecordConnectorSeen(ctx, connector.ID, "0.3.0", contracts.ConnectorRuntimeState{
		ProtocolVersion: contracts.ConnectorProtocolVersion,
	})
	if err != nil || upgraded.ProtocolVersion != contracts.ConnectorProtocolVersion ||
		upgraded.Gateway.ProtocolVersion != contracts.ConnectorProtocolVersion {
		t.Fatalf("v3 handshake upgrade=%+v err=%v", upgraded, err)
	}
}

func TestMemoryConnectorTaskLeaseNonceFencesAttempts(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }
	createMemoryConnectorTestUser(t, st)

	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: 1,
		Name:   "Lease nonce gateway",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	enrollment, err := st.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{
		UserID:      1,
		InstanceID:  instance.ID,
		ConnectorID: "connector-lease-nonce",
		TokenHash:   "enrollment-token-hash",
	})
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	connector, _, err := st.UseConnectorEnrollment(ctx, enrollment.TokenHash, contracts.Connector{
		ID:              enrollment.ConnectorID,
		InstanceID:      instance.ID,
		Version:         "0.1.0-test",
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		TokenHash:       "connector-token-hash",
	})
	if err != nil {
		t.Fatalf("use enrollment: %v", err)
	}
	task, err := st.CreateConnectorTask(ctx, contracts.ConnectorTask{
		UserID:         1,
		InstanceID:     instance.ID,
		ConnectorID:    connector.ID,
		Type:           contracts.ConnectorTaskGatewayAccountsList,
		Input:          []byte(`{}`),
		IdempotencyKey: "accounts-list",
		MaxAttempts:    4,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := st.CreateConnectorTask(ctx, contracts.ConnectorTask{
		UserID:         1,
		InstanceID:     instance.ID,
		ConnectorID:    connector.ID,
		Type:           contracts.ConnectorTaskGatewayAccountsList,
		Input:          []byte(`{}`),
		IdempotencyKey: task.IdempotencyKey,
	}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("active duplicate task error = %v, want ErrDuplicate", err)
	}

	lease := func() contracts.ConnectorTask {
		t.Helper()
		tasks, err := st.LeaseConnectorTasks(ctx, contracts.ConnectorTaskLeaseRequest{
			ConnectorID:  connector.ID,
			LeaseSeconds: 10,
		})
		if err != nil {
			t.Fatalf("lease task: %v", err)
		}
		if len(tasks) != 1 {
			t.Fatalf("leased tasks = %d, want 1", len(tasks))
		}
		raw, err := base64.RawURLEncoding.DecodeString(tasks[0].LeaseNonce)
		if err != nil || len(raw) != connectorTaskLeaseNonceBytes {
			t.Fatalf("lease nonce is not a %d-byte opaque token: %q (%v)", connectorTaskLeaseNonceBytes, tasks[0].LeaseNonce, err)
		}
		return tasks[0]
	}
	complete := func(nonce string, success bool) (contracts.ConnectorTask, error) {
		req := contracts.ConnectorTaskCompleteRequest{
			ConnectorID: connector.ID,
			LeaseNonce:  nonce,
			Success:     success,
		}
		if success {
			req.Result = []byte(`{"accounts":[]}`)
		} else {
			req.Error = contracts.ConnectorTaskError{
				Code:      "gateway_timeout",
				Retryable: true,
			}
		}
		return st.CompleteConnectorTask(ctx, task.ID, req)
	}

	first := lease()
	if _, err := complete("", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing nonce completion error = %v, want ErrConflict", err)
	}
	now = now.Add(11 * time.Second)
	if _, err := complete(first.LeaseNonce, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired lease completion error = %v, want ErrConflict", err)
	}

	second := lease()
	if second.LeaseNonce == first.LeaseNonce {
		t.Fatal("re-lease reused the previous lease nonce")
	}
	if _, err := complete(first.LeaseNonce, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale attempt completion error = %v, want ErrConflict", err)
	}

	retried, err := complete(second.LeaseNonce, false)
	if err != nil {
		t.Fatalf("complete retryable attempt: %v", err)
	}
	if retried.Status != contracts.ConnectorTaskPending || retried.LeaseNonce != "" || retried.LeaseUntil != nil {
		t.Fatalf("retry did not clear lease state: %+v", retried)
	}
	if want := now.Add(connectorTaskRetryDelay(retried.Attempts)); !retried.AvailableAt.Equal(want) {
		t.Fatalf("retry available_at = %v, want %v", retried.AvailableAt, want)
	}

	now = retried.AvailableAt
	third := lease()
	if third.LeaseNonce == first.LeaseNonce || third.LeaseNonce == second.LeaseNonce {
		t.Fatal("new attempt reused an earlier lease nonce")
	}
	completed, err := complete(third.LeaseNonce, true)
	if err != nil {
		t.Fatalf("complete current attempt: %v", err)
	}
	if completed.Status != contracts.ConnectorTaskSucceeded || completed.LeaseNonce != "" || completed.LeaseUntil != nil {
		t.Fatalf("completion did not clear lease state: %+v", completed)
	}
	if _, err := complete(third.LeaseNonce, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate completion error = %v, want ErrConflict", err)
	}
	retriedAfterTerminal, err := st.CreateConnectorTask(ctx, contracts.ConnectorTask{
		UserID:         1,
		InstanceID:     instance.ID,
		ConnectorID:    connector.ID,
		Type:           contracts.ConnectorTaskGatewayAccountsList,
		Input:          []byte(`{}`),
		IdempotencyKey: task.IdempotencyKey,
	})
	if err != nil {
		t.Fatalf("create same operation after terminal result: %v", err)
	}
	if retriedAfterTerminal.ID == task.ID {
		t.Fatal("terminal task was reused instead of creating a fresh request")
	}
}

func TestMemoryConnectorTaskRejectsUnsupportedProtocolType(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	createMemoryConnectorTestUser(t, st)
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: 1,
		Name:   "Strict protocol gateway",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	enrollment, err := st.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{
		UserID:      1,
		InstanceID:  instance.ID,
		ConnectorID: "connector-strict-protocol",
		TokenHash:   "strict-enrollment-token-hash",
	})
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	connector, _, err := st.UseConnectorEnrollment(ctx, enrollment.TokenHash, contracts.Connector{
		ID:              enrollment.ConnectorID,
		InstanceID:      instance.ID,
		Version:         "0.1.0-test",
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		TokenHash:       "strict-connector-token-hash",
	})
	if err != nil {
		t.Fatalf("use enrollment: %v", err)
	}
	_, err = st.CreateConnectorTask(ctx, contracts.ConnectorTask{
		UserID:      1,
		InstanceID:  instance.ID,
		ConnectorID: connector.ID,
		Type:        contracts.ConnectorTaskType("gateway.account.provision"),
		Input:       []byte(`{}`),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("unsupported protocol task error = %v, want ErrConflict", err)
	}
}

func TestMemoryConnectorTaskDoesNotLeaseBeforeAvailableAt(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }
	createMemoryConnectorTestUser(t, st)
	instance, _ := st.CreateInstance(ctx, contracts.Instance{UserID: 1, Name: "Deferred delete", Kind: contracts.InstanceKindSub2API})
	enrollment, _ := st.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{UserID: 1, InstanceID: instance.ID, ConnectorID: "connector-deferred", TokenHash: "enroll"})
	connector, _, err := st.UseConnectorEnrollment(ctx, enrollment.TokenHash, contracts.Connector{
		ID: enrollment.ConnectorID, InstanceID: instance.ID, Version: "0.1.0-test", ProtocolVersion: contracts.ConnectorProtocolVersion, TokenHash: "connector-token",
	})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	available := now.Add(30 * time.Minute)
	_, err = st.CreateConnectorTask(ctx, contracts.ConnectorTask{
		UserID: 1, InstanceID: instance.ID, ConnectorID: connector.ID,
		Type: contracts.ConnectorTaskGatewayAccountDelete, Input: []byte(`{"account_id":"a","ownership":"platform_managed"}`),
		AvailableAt: available, ExpiresAt: available.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create deferred task: %v", err)
	}
	leased, err := st.LeaseConnectorTasks(ctx, contracts.ConnectorTaskLeaseRequest{ConnectorID: connector.ID})
	if err != nil || len(leased) != 0 {
		t.Fatalf("early lease = %+v err=%v", leased, err)
	}
	now = available
	leased, err = st.LeaseConnectorTasks(ctx, contracts.ConnectorTaskLeaseRequest{ConnectorID: connector.ID})
	if err != nil || len(leased) != 1 {
		t.Fatalf("ready lease = %+v err=%v", leased, err)
	}
}

func TestMemoryConnectorTaskLeaseUntilIsBoundedByExpiration(t *testing.T) {
	tests := []struct {
		name              string
		expiresAfter      time.Duration
		leaseSeconds      int
		wantLeaseDuration time.Duration
	}{
		{
			name:              "task expires before requested lease",
			expiresAfter:      5 * time.Second,
			leaseSeconds:      30,
			wantLeaseDuration: 5 * time.Second,
		},
		{
			name:              "requested lease ends before task",
			expiresAfter:      2 * time.Minute,
			leaseSeconds:      30,
			wantLeaseDuration: 30 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, clock, instance, connector := newMemoryConnectorTaskFixture(t, "connector-lease-bound")
			task, err := st.CreateConnectorTask(context.Background(), contracts.ConnectorTask{
				UserID:      1,
				InstanceID:  instance.ID,
				ConnectorID: connector.ID,
				Type:        contracts.ConnectorTaskGatewayAccountsList,
				ExpiresAt:   clock.now.Add(tt.expiresAfter),
			})
			if err != nil {
				t.Fatalf("create task: %v", err)
			}

			leased, err := st.LeaseConnectorTasks(context.Background(), contracts.ConnectorTaskLeaseRequest{
				ConnectorID:  connector.ID,
				LeaseSeconds: tt.leaseSeconds,
			})
			if err != nil {
				t.Fatalf("lease task: %v", err)
			}
			if len(leased) != 1 || leased[0].ID != task.ID || leased[0].LeaseUntil == nil {
				t.Fatalf("unexpected leased tasks: %+v", leased)
			}
			want := clock.now.Add(tt.wantLeaseDuration)
			if !leased[0].LeaseUntil.Equal(want) {
				t.Fatalf("lease_until = %v, want %v", leased[0].LeaseUntil, want)
			}
			if leased[0].LeaseUntil.After(task.ExpiresAt) {
				t.Fatalf("lease_until %v exceeds task expiration %v", leased[0].LeaseUntil, task.ExpiresAt)
			}
		})
	}
}

func TestMemoryConnectorTaskCompletionRejectsExpiredTaskWithLiveLease(t *testing.T) {
	st, clock, instance, connector := newMemoryConnectorTaskFixture(t, "connector-expired-completion")
	task, err := st.CreateConnectorTask(context.Background(), contracts.ConnectorTask{
		UserID:      1,
		InstanceID:  instance.ID,
		ConnectorID: connector.ID,
		Type:        contracts.ConnectorTaskGatewayAccountsList,
		ExpiresAt:   clock.now.Add(5 * time.Second),
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	leased, err := st.LeaseConnectorTasks(context.Background(), contracts.ConnectorTaskLeaseRequest{
		ConnectorID:  connector.ID,
		LeaseSeconds: 30,
	})
	if err != nil || len(leased) != 1 {
		t.Fatalf("lease task: tasks=%+v err=%v", leased, err)
	}

	// Simulate a lease written by an older Core version before leases were
	// capped at ExpiresAt.
	legacyLeaseUntil := clock.now.Add(time.Minute)
	for i := range st.connectorTasks {
		if st.connectorTasks[i].ID == task.ID {
			st.connectorTasks[i].LeaseUntil = &legacyLeaseUntil
		}
	}
	clock.now = clock.now.Add(6 * time.Second)
	if !legacyLeaseUntil.After(clock.now) {
		t.Fatal("test setup did not leave the legacy lease valid")
	}
	_, err = st.CompleteConnectorTask(context.Background(), task.ID, contracts.ConnectorTaskCompleteRequest{
		ConnectorID: connector.ID,
		LeaseNonce:  leased[0].LeaseNonce,
		Success:     true,
		Result:      []byte(`{"accounts":[]}`),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expired task completion error = %v, want ErrConflict", err)
	}
	persisted, err := st.GetConnectorTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get rejected task: %v", err)
	}
	if persisted.Status != contracts.ConnectorTaskExpired || persisted.Error.Code != "expired" ||
		persisted.Error.Retryable || persisted.LeaseOwner != "" || persisted.LeaseNonce != "" ||
		persisted.LeaseUntil != nil || len(persisted.Result) != 0 {
		t.Fatalf("rejected completion did not materialize stable expiry: %+v", persisted)
	}
}

func TestMemoryConnectorTaskFailureRetryPolicy(t *testing.T) {
	tests := []struct {
		name        string
		retryable   bool
		maxAttempts int
		wantStatus  contracts.ConnectorTaskStatus
	}{
		{
			name:        "non-retryable failure is terminal immediately",
			retryable:   false,
			maxAttempts: 3,
			wantStatus:  contracts.ConnectorTaskFailed,
		},
		{
			name:        "retryable failure with attempts remaining is pending",
			retryable:   true,
			maxAttempts: 2,
			wantStatus:  contracts.ConnectorTaskPending,
		},
		{
			name:        "retryable failure at max attempts is terminal",
			retryable:   true,
			maxAttempts: 1,
			wantStatus:  contracts.ConnectorTaskFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, clock, instance, connector := newMemoryConnectorTaskFixture(t, "connector-retry-policy")
			task, err := st.CreateConnectorTask(context.Background(), contracts.ConnectorTask{
				UserID:      1,
				InstanceID:  instance.ID,
				ConnectorID: connector.ID,
				Type:        contracts.ConnectorTaskGatewayAccountsList,
				MaxAttempts: tt.maxAttempts,
			})
			if err != nil {
				t.Fatalf("create task: %v", err)
			}
			leased, err := st.LeaseConnectorTasks(context.Background(), contracts.ConnectorTaskLeaseRequest{
				ConnectorID: connector.ID,
			})
			if err != nil || len(leased) != 1 {
				t.Fatalf("lease task: tasks=%+v err=%v", leased, err)
			}
			completed, err := st.CompleteConnectorTask(context.Background(), task.ID, contracts.ConnectorTaskCompleteRequest{
				ConnectorID: connector.ID,
				LeaseNonce:  leased[0].LeaseNonce,
				Error: contracts.ConnectorTaskError{
					Code:      "gateway_request_failed",
					Retryable: tt.retryable,
				},
			})
			if err != nil {
				t.Fatalf("complete failed task: %v", err)
			}
			if completed.Status != tt.wantStatus || completed.LeaseNonce != "" || completed.LeaseUntil != nil {
				t.Fatalf("completed task = %+v, want status %s with cleared lease", completed, tt.wantStatus)
			}
			if completed.LeaseOwner != "" {
				t.Fatalf("completed task retained lease owner: %+v", completed)
			}

			next, err := st.LeaseConnectorTasks(context.Background(), contracts.ConnectorTaskLeaseRequest{
				ConnectorID: connector.ID,
			})
			if err != nil {
				t.Fatalf("lease after failure: %v", err)
			}
			if len(next) != 0 {
				t.Fatalf("failed task was re-leased before its retry delay: %+v", next)
			}
			if tt.wantStatus == contracts.ConnectorTaskPending {
				wantAvailableAt := clock.now.Add(connectorTaskRetryDelay(completed.Attempts))
				if !completed.AvailableAt.Equal(wantAvailableAt) {
					t.Fatalf("retry available_at = %v, want %v", completed.AvailableAt, wantAvailableAt)
				}
				clock.now = completed.AvailableAt
				next, err = st.LeaseConnectorTasks(context.Background(), contracts.ConnectorTaskLeaseRequest{
					ConnectorID: connector.ID,
				})
				if err != nil || len(next) != 1 || next[0].ID != task.ID {
					t.Fatalf("retryable task was not re-leased after delay: tasks=%+v err=%v", next, err)
				}
			} else if len(next) != 0 {
				t.Fatalf("terminal task was re-leased: %+v", next)
			}
		})
	}
}

func TestMemoryConnectorTaskGetMaterializesStableExpiry(t *testing.T) {
	st, clock, instance, connector := newMemoryConnectorTaskFixture(t, "connector-get-expiry")
	task, err := st.CreateConnectorTask(context.Background(), contracts.ConnectorTask{
		UserID:      1,
		InstanceID:  instance.ID,
		ConnectorID: connector.ID,
		Type:        contracts.ConnectorTaskGatewayAccountsList,
		ExpiresAt:   clock.now.Add(5 * time.Second),
		Error:       contracts.ConnectorTaskError{Code: "gateway_timeout", Retryable: true},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	clock.now = task.ExpiresAt
	persisted, err := st.GetConnectorTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get expired task: %v", err)
	}
	if persisted.Status != contracts.ConnectorTaskExpired ||
		persisted.Error != (contracts.ConnectorTaskError{Code: "expired"}) ||
		persisted.LeaseOwner != "" || persisted.LeaseNonce != "" || persisted.LeaseUntil != nil {
		t.Fatalf("expired task = %+v, want terminal expired error", persisted)
	}
}

func TestConnectorTaskRetryDelayIsBounded(t *testing.T) {
	want := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second}
	for index, expected := range want {
		if got := connectorTaskRetryDelay(index + 1); got != expected {
			t.Fatalf("attempt %d retry delay = %s, want %s", index+1, got, expected)
		}
	}
}

func TestMemoryConnectorEnrollmentReclaimsExpiredUnusedSlots(t *testing.T) {
	t.Run("instance slot", func(t *testing.T) {
		clock := &memoryConnectorTestClock{now: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)}
		st := NewMemoryStore(clock.now)
		st.now = func() time.Time { return clock.now }
		createMemoryConnectorTestUser(t, st)
		instance := createMemoryConnectorTestInstance(t, st, "instance-slot")
		first, err := st.CreateConnectorEnrollment(context.Background(), contracts.ConnectorEnrollment{
			ID:          "enrollment-old-instance-slot",
			UserID:      1,
			InstanceID:  instance.ID,
			ConnectorID: "connector-old-instance-slot",
			TokenHash:   "token-old-instance-slot",
			ExpiresAt:   clock.now.Add(time.Minute),
		})
		if err != nil {
			t.Fatalf("create first enrollment: %v", err)
		}
		input := contracts.ConnectorEnrollment{
			ID:          "enrollment-new-instance-slot",
			UserID:      1,
			InstanceID:  instance.ID,
			ConnectorID: "connector-new-instance-slot",
			TokenHash:   "token-new-instance-slot",
		}
		reissued, err := st.CreateConnectorEnrollment(context.Background(), input)
		if err != nil {
			t.Fatalf("reissue unexpired instance enrollment: %v", err)
		}
		if reissued.ConnectorID != input.ConnectorID {
			t.Fatalf("reissued enrollment = %+v", reissued)
		}
		if _, _, err := st.UseConnectorEnrollment(context.Background(), first.TokenHash, contracts.Connector{}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("replaced enrollment lookup error = %v, want ErrNotFound", err)
		}

		clock.now = first.ExpiresAt
		input.ID = "enrollment-newer-instance-slot"
		input.TokenHash = "token-newer-instance-slot"
		created, err := st.CreateConnectorEnrollment(context.Background(), input)
		if err != nil {
			t.Fatalf("replace expired instance enrollment: %v", err)
		}
		if created.ConnectorID != input.ConnectorID {
			t.Fatalf("created enrollment = %+v", created)
		}
		if _, err := st.CreateConnectorEnrollment(context.Background(), contracts.ConnectorEnrollment{
			ID:          "enrollment-already-expired",
			UserID:      1,
			InstanceID:  instance.ID,
			ConnectorID: "connector-already-expired",
			TokenHash:   "token-already-expired",
			ExpiresAt:   clock.now,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("already-expired enrollment error = %v, want ErrConflict", err)
		}
	})

	t.Run("connector slot", func(t *testing.T) {
		clock := &memoryConnectorTestClock{now: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)}
		st := NewMemoryStore(clock.now)
		st.now = func() time.Time { return clock.now }
		createMemoryConnectorTestUser(t, st)
		firstInstance := createMemoryConnectorTestInstance(t, st, "connector-slot-a")
		secondInstance := createMemoryConnectorTestInstance(t, st, "connector-slot-b")
		first, err := st.CreateConnectorEnrollment(context.Background(), contracts.ConnectorEnrollment{
			ID:          "enrollment-old-connector-slot",
			UserID:      1,
			InstanceID:  firstInstance.ID,
			ConnectorID: "connector-shared-slot",
			TokenHash:   "token-old-connector-slot",
			ExpiresAt:   clock.now.Add(time.Minute),
		})
		if err != nil {
			t.Fatalf("create first enrollment: %v", err)
		}
		input := contracts.ConnectorEnrollment{
			ID:          "enrollment-new-connector-slot",
			UserID:      1,
			InstanceID:  secondInstance.ID,
			ConnectorID: first.ConnectorID,
			TokenHash:   "token-new-connector-slot",
		}
		if _, err := st.CreateConnectorEnrollment(context.Background(), input); !errors.Is(err, ErrDuplicate) {
			t.Fatalf("unexpired connector slot error = %v, want ErrDuplicate", err)
		}

		clock.now = first.ExpiresAt
		created, err := st.CreateConnectorEnrollment(context.Background(), input)
		if err != nil {
			t.Fatalf("replace expired connector enrollment: %v", err)
		}
		if created.InstanceID != secondInstance.ID {
			t.Fatalf("created enrollment = %+v", created)
		}
		if _, _, err := st.UseConnectorEnrollment(context.Background(), first.TokenHash, contracts.Connector{}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cleaned enrollment lookup error = %v, want ErrNotFound", err)
		}
	})
}

func TestMemoryConnectorEnrollmentReissuesUnusedInstanceSlot(t *testing.T) {
	clock := &memoryConnectorTestClock{now: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)}
	st := NewMemoryStore(clock.now)
	st.now = func() time.Time { return clock.now }
	createMemoryConnectorTestUser(t, st)
	instance := createMemoryConnectorTestInstance(t, st, "reissue-instance-slot")
	first, err := st.CreateConnectorEnrollment(context.Background(), contracts.ConnectorEnrollment{
		ID:          "enrollment-first-reissue",
		UserID:      1,
		InstanceID:  instance.ID,
		ConnectorID: "connector-first-reissue",
		TokenHash:   "token-first-reissue",
	})
	if err != nil {
		t.Fatalf("create first enrollment: %v", err)
	}
	second, err := st.CreateConnectorEnrollment(context.Background(), contracts.ConnectorEnrollment{
		ID:          "enrollment-second-reissue",
		UserID:      1,
		InstanceID:  instance.ID,
		ConnectorID: "connector-second-reissue",
		TokenHash:   "token-second-reissue",
	})
	if err != nil {
		t.Fatalf("reissue enrollment: %v", err)
	}
	if second.ConnectorID == first.ConnectorID {
		t.Fatalf("reissue kept stale unbound identity: %+v", second)
	}
	if _, _, err := st.UseConnectorEnrollment(context.Background(), first.TokenHash, contracts.Connector{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replaced enrollment lookup error = %v, want ErrNotFound", err)
	}
	if _, _, err := st.UseConnectorEnrollment(context.Background(), second.TokenHash, contracts.Connector{
		ID:              second.ConnectorID,
		InstanceID:      instance.ID,
		Version:         "0.1.0-test",
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		TokenHash:       "connector-runtime-token",
	}); err != nil {
		t.Fatalf("use replacement enrollment: %v", err)
	}
}

type memoryConnectorTestClock struct {
	now time.Time
}

func newMemoryConnectorTaskFixture(t *testing.T, connectorID string) (*MemoryStore, *memoryConnectorTestClock, contracts.Instance, contracts.Connector) {
	t.Helper()
	clock := &memoryConnectorTestClock{now: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)}
	st := NewMemoryStore(clock.now)
	st.now = func() time.Time { return clock.now }
	createMemoryConnectorTestUser(t, st)
	instance := createMemoryConnectorTestInstance(t, st, connectorID)
	enrollment, err := st.CreateConnectorEnrollment(context.Background(), contracts.ConnectorEnrollment{
		UserID:      1,
		InstanceID:  instance.ID,
		ConnectorID: connectorID,
		TokenHash:   connectorID + "-enrollment-token",
	})
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	connector, _, err := st.UseConnectorEnrollment(context.Background(), enrollment.TokenHash, contracts.Connector{
		ID:              connectorID,
		InstanceID:      instance.ID,
		Version:         "0.1.0-test",
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		TokenHash:       connectorID + "-connector-token",
	})
	if err != nil {
		t.Fatalf("use enrollment: %v", err)
	}
	return st, clock, instance, connector
}

func createMemoryConnectorTestInstance(t *testing.T, st *MemoryStore, name string) contracts.Instance {
	t.Helper()
	instance, err := st.CreateInstance(context.Background(), contracts.Instance{
		UserID: 1,
		Name:   name,
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	return instance
}

func createMemoryConnectorTestUser(t *testing.T, st *MemoryStore) {
	t.Helper()
	if _, err := st.CreateUser(context.Background(), contracts.User{
		ID: 1, Email: "connector-test@example.com", PasswordHash: "test-only",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	}); err != nil && !errors.Is(err, ErrDuplicate) {
		t.Fatalf("create connector test user: %v", err)
	}
}
