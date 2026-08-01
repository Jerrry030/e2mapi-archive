package adapters

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

type fakeTaskStore struct {
	mu      sync.Mutex
	created []contracts.ConnectorTask
	result  json.RawMessage
	failure contracts.ConnectorTaskError
	create  error
	listed  []contracts.ConnectorTask
}

type taskStatusSequenceStore struct {
	mu       sync.Mutex
	created  contracts.ConnectorTask
	sequence []contracts.ConnectorTask
	getCalls int
}

type executingDuplicateTaskStore struct {
	mu          sync.Mutex
	requested   contracts.ConnectorTask
	createCalls int
	getCalls    int
	result      json.RawMessage
}

func (s *executingDuplicateTaskStore) CreateConnectorTask(_ context.Context, input contracts.ConnectorTask) (contracts.ConnectorTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requested = input
	s.createCalls++
	return contracts.ConnectorTask{}, store.ErrDuplicate
}

func (s *executingDuplicateTaskStore) ListConnectorTasks(_ context.Context, _ contracts.ConnectorTaskFilter) ([]contracts.ConnectorTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := s.requested
	task.ID = "task-executing-duplicate"
	task.Status = contracts.ConnectorTaskExecuting
	task.ExpiresAt = time.Unix(99, 0).UTC()
	return []contracts.ConnectorTask{task}, nil
}

func (s *executingDuplicateTaskStore) GetConnectorTask(_ context.Context, id string) (contracts.ConnectorTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := s.requested
	task.ID = id
	s.getCalls++
	if s.getCalls == 1 {
		task.Status = contracts.ConnectorTaskExecuting
		task.ExpiresAt = time.Unix(99, 0).UTC()
		return task, nil
	}
	task.Status = contracts.ConnectorTaskSucceeded
	task.Result = append(json.RawMessage(nil), s.result...)
	return task, nil
}

func (s *taskStatusSequenceStore) CreateConnectorTask(_ context.Context, input contracts.ConnectorTask) (contracts.ConnectorTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	input.ID = "task-1"
	s.created = input
	return input, nil
}

func (s *taskStatusSequenceStore) GetConnectorTask(_ context.Context, id string) (contracts.ConnectorTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sequence) == 0 {
		return contracts.ConnectorTask{}, errors.New("task sequence is empty")
	}
	index := s.getCalls
	if index >= len(s.sequence) {
		index = len(s.sequence) - 1
	}
	s.getCalls++
	step := s.sequence[index]
	current := s.created
	current.ID = id
	current.Status = step.Status
	current.Result = append(json.RawMessage(nil), step.Result...)
	current.Error = step.Error
	return current, nil
}

func (s *fakeTaskStore) ListConnectorTasks(_ context.Context, _ contracts.ConnectorTaskFilter) ([]contracts.ConnectorTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]contracts.ConnectorTask(nil), s.listed...), nil
}

func (s *fakeTaskStore) CreateConnectorTask(_ context.Context, input contracts.ConnectorTask) (contracts.ConnectorTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.create != nil {
		return contracts.ConnectorTask{}, s.create
	}
	input.ID = "task-1"
	s.created = append(s.created, input)
	return input, nil
}

func (s *fakeTaskStore) GetConnectorTask(_ context.Context, id string) (contracts.ConnectorTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.created) == 0 {
		for _, task := range s.listed {
			if task.ID == id {
				return task, nil
			}
		}
		return contracts.ConnectorTask{}, errors.New("task not created")
	}
	task := s.created[len(s.created)-1]
	task.ID = id
	if s.failure.Code != "" {
		task.Status = contracts.ConnectorTaskFailed
		task.Error = s.failure
	} else {
		task.Status = contracts.ConnectorTaskSucceeded
		task.Result = append(json.RawMessage(nil), s.result...)
	}
	return task, nil
}

func TestConnectorGatewayClientReusesSucceededLogicalQualityProbe(t *testing.T) {
	observedAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	input := contracts.ConnectorGatewayQualityProbeInput{
		AccountID: "remote-7", ChannelID: "channel-a", Model: "gpt-test",
		Capability: contracts.QualityProbeTextStream, EndpointPath: contracts.QualityProbeEndpointResponses,
	}
	client := testClient(&fakeTaskStore{}, contracts.InstanceKindSub2API)
	instance := testInstance(contracts.InstanceKindSub2API)
	raw, _ := json.Marshal(input)
	key := connectorTaskIdempotencyKey(instance.Kind, instance, contracts.ConnectorTaskGatewayQualityProbe, raw, "circuit-1:attempt-1")
	st := &fakeTaskStore{listed: []contracts.ConnectorTask{{
		ID: "completed-probe", UserID: instance.UserID, InstanceID: instance.ID, ConnectorID: instance.ConnectorID,
		Type: contracts.ConnectorTaskGatewayQualityProbe, Status: contracts.ConnectorTaskSucceeded,
		IdempotencyKey: key, Result: mustRawJSON(t, contracts.ConnectorGatewayQualityProbeResult{
			Success: true, Status: 200, FirstTokenMS: 80, TotalMS: 140, ObservedAt: observedAt,
			Capability: contracts.QualityProbeTextStream, EndpointPath: contracts.QualityProbeEndpointResponses,
		}),
	}}}
	client = testClient(st, instance.Kind)
	result, err := client.ProbeQuality(context.Background(), instance, input, "circuit-1:attempt-1")
	if err != nil || !result.Success || !result.ObservedAt.Equal(observedAt) {
		t.Fatalf("reuse completed probe: result=%+v err=%v", result, err)
	}
	if len(st.created) != 0 {
		t.Fatalf("completed logical probe created new work: %+v", st.created)
	}
}

func testClient(st ConnectorTaskStore, kind contracts.InstanceKind) *ConnectorGatewayClient {
	c := NewConnectorGatewayClient(st, kind)
	c.timeout = time.Second
	c.poll = time.Millisecond
	c.now = func() time.Time { return time.Unix(100, 0).UTC() }
	return c
}

func testInstance(kind contracts.InstanceKind) contracts.Instance {
	return contracts.Instance{
		ID:          "inst-1",
		UserID:      101,
		Kind:        kind,
		ConnectorID: "conn-1",
	}
}

func TestConnectorGatewayClientListAccountsQueuesTypedTask(t *testing.T) {
	st := &fakeTaskStore{result: json.RawMessage(`{"accounts":[{"id":"a-1","schedulable":true}]}`)}
	client := testClient(st, contracts.InstanceKindSub2API)

	accounts, err := client.ListAccounts(context.Background(), testInstance(contracts.InstanceKindSub2API))
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(st.created) != 1 || st.created[0].MaxAttempts != 3 ||
		!st.created[0].ExpiresAt.Equal(client.now().Add(connectorTaskExecutionWindow)) {
		t.Fatalf("task retry window does not cover declared attempts: %+v", st.created)
	}
	if len(accounts) != 1 || accounts[0].ID != "a-1" {
		t.Fatalf("unexpected accounts: %+v", accounts)
	}
	if len(st.created) != 1 {
		t.Fatalf("expected one task, got %d", len(st.created))
	}
	task := st.created[0]
	if task.ConnectorID != "conn-1" || task.Type != contracts.ConnectorTaskGatewayAccountsList {
		t.Fatalf("unexpected task identity: %+v", task)
	}
	if task.RiskLevel != contracts.RiskLevelL0 || task.SchemaVersion != 1 {
		t.Fatalf("unexpected task metadata: %+v", task)
	}
	if task.IdempotencyKey == "" || strings.Contains(string(task.Input), "http") {
		t.Fatalf("task is not typed/idempotent: %+v", task)
	}
}

func TestConnectorGatewayClientQueuesTypedQualityProbe(t *testing.T) {
	observedAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	st := &fakeTaskStore{result: mustRawJSON(t, contracts.ConnectorGatewayQualityProbeResult{
		Success: true, Status: 200, FirstTokenMS: 80, TotalMS: 140, ObservedAt: observedAt,
		Capability: contracts.QualityProbeTextStream, EndpointPath: contracts.QualityProbeEndpointResponses,
	})}
	client := testClient(st, contracts.InstanceKindSub2API)
	input := contracts.ConnectorGatewayQualityProbeInput{
		AccountID: "remote-7", ChannelID: "channel-a", Model: "gpt-test",
		Capability: contracts.QualityProbeTextStream, EndpointPath: contracts.QualityProbeEndpointResponses,
	}

	result, err := client.ProbeQuality(context.Background(), testInstance(contracts.InstanceKindSub2API), input, "circuit-1:attempt-1")
	if err != nil {
		t.Fatalf("probe quality: %v", err)
	}
	if !result.Success || result.Status != 200 || !result.ObservedAt.Equal(observedAt) {
		t.Fatalf("unexpected quality result: %+v", result)
	}
	if len(st.created) != 1 || st.created[0].Type != contracts.ConnectorTaskGatewayQualityProbe || st.created[0].RiskLevel != contracts.RiskLevelL0 {
		t.Fatalf("unexpected quality task: %+v", st.created)
	}
	var got contracts.ConnectorGatewayQualityProbeInput
	if err := json.Unmarshal(st.created[0].Input, &got); err != nil || got != input {
		t.Fatalf("quality task input=%+v err=%v", got, err)
	}
	if strings.Contains(string(st.created[0].Input), "http") || strings.Contains(string(st.created[0].Input), "sk-") {
		t.Fatalf("quality task leaked connection material: %s", st.created[0].Input)
	}
}

func TestConnectorGatewayClientQueuesTypedBindingProof(t *testing.T) {
	proof := strings.Repeat("a", contracts.ConnectorGatewayBindingProofHexLength)
	st := &fakeTaskStore{result: mustRawJSON(t, contracts.ConnectorGatewayBindingProofResult{Proof: proof})}
	client := testClient(st, contracts.InstanceKindNewAPI)
	input := contracts.ConnectorGatewayBindingProofInput{
		ChannelID: "channel-a", BindingID: "binding-a",
		Challenge: strings.Repeat("b", contracts.ConnectorGatewayBindingProofChallengeHexLength),
	}
	result, err := client.ProveBinding(context.Background(), testInstance(contracts.InstanceKindNewAPI), input)
	if err != nil || result.Proof != proof {
		t.Fatalf("binding proof result=%+v err=%v", result, err)
	}
	if len(st.created) != 1 || st.created[0].Type != contracts.ConnectorTaskGatewayBindingProof || st.created[0].RiskLevel != contracts.RiskLevelL0 {
		t.Fatalf("unexpected binding proof task: %+v", st.created)
	}
	if strings.Contains(string(st.created[0].Input), "sk-") {
		t.Fatalf("binding proof task leaked key material: %s", st.created[0].Input)
	}
}

func TestConnectorGatewayClientQueuesEncryptedBindingWithStableLogicalIdempotency(t *testing.T) {
	input := contracts.ConnectorGatewayBindingInstallInput{
		BindingID: "binding-a", ChannelID: "channel-a", KeyVersion: 3,
		Ciphertext: testBindingCiphertext(t, 'a'),
	}
	result := mustRawJSON(t, contracts.ConnectorGatewayBindingInstallResult{
		BindingID: input.BindingID, ChannelID: input.ChannelID, KeyVersion: input.KeyVersion,
	})
	first := &fakeTaskStore{result: result}
	second := &fakeTaskStore{result: result}
	instance := testInstance(contracts.InstanceKindNewAPI)
	if _, err := testClient(first, instance.Kind).InstallBinding(context.Background(), instance, input); err != nil {
		t.Fatal(err)
	}
	input.Ciphertext = testBindingCiphertext(t, 'b')
	if _, err := testClient(second, instance.Kind).InstallBinding(context.Background(), instance, input); err != nil {
		t.Fatal(err)
	}
	if first.created[0].IdempotencyKey != second.created[0].IdempotencyKey {
		t.Fatal("random ciphertext changed logical binding-install idempotency")
	}
	if first.created[0].Type != contracts.ConnectorTaskGatewayBindingInstall || first.created[0].RiskLevel != contracts.RiskLevelL2 {
		t.Fatalf("binding install task = %+v", first.created[0])
	}
	if strings.Contains(string(first.created[0].Input), "sk-") {
		t.Fatalf("binding install task leaked plaintext: %s", first.created[0].Input)
	}
}

func testBindingCiphertext(t *testing.T, fill byte) string {
	t.Helper()
	value := func(size int) string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, size)) }
	raw, err := json.Marshal(map[string]string{
		"algorithm":            contracts.ConnectorBindingEncryptionAlgorithm,
		"ephemeral_public_key": value(contracts.ConnectorBindingEncryptionPublicKeySize),
		"nonce":                value(contracts.ConnectorBindingEncryptionNonceSize), "sealed": value(16),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestConnectorGatewayClientRejectsIncompleteQualityProbe(t *testing.T) {
	client := testClient(&fakeTaskStore{}, contracts.InstanceKindSub2API)
	_, err := client.ProbeQuality(context.Background(), testInstance(contracts.InstanceKindSub2API), contracts.ConnectorGatewayQualityProbeInput{
		AccountID: "a", ChannelID: "c",
	}, "attempt-1")
	if err == nil || !strings.Contains(err.Error(), "requires account_id") {
		t.Fatalf("expected typed input error, got %v", err)
	}
}

func TestConnectorGatewayClientRejectsSensitiveQualityProbeBeforePersistence(t *testing.T) {
	st := &fakeTaskStore{}
	client := testClient(st, contracts.InstanceKindSub2API)
	_, err := client.ProbeQuality(context.Background(), testInstance(contracts.InstanceKindSub2API), contracts.ConnectorGatewayQualityProbeInput{
		AccountID: "https://gateway.local/probe", ChannelID: "channel-a", Model: "gpt-test",
	}, "attempt-1")
	if err == nil {
		t.Fatal("Core accepted a URL in quality probe input")
	}
	if len(st.created) != 0 {
		t.Fatal("sensitive quality probe input was persisted")
	}
}

func mustRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestConnectorGatewayClientStableIdempotencyKey(t *testing.T) {
	first := &fakeTaskStore{result: json.RawMessage(`{}`)}
	second := &fakeTaskStore{result: json.RawMessage(`{}`)}
	inst := testInstance(contracts.InstanceKindNewAPI)

	if err := testClient(first, inst.Kind).SetSchedulable(context.Background(), inst, "42", false); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if err := testClient(second, inst.Kind).SetSchedulable(context.Background(), inst, "42", false); err != nil {
		t.Fatalf("second request: %v", err)
	}
	if first.created[0].IdempotencyKey != second.created[0].IdempotencyKey {
		t.Fatalf("idempotency keys differ: %q != %q", first.created[0].IdempotencyKey, second.created[0].IdempotencyKey)
	}
	if first.created[0].RiskLevel != contracts.RiskLevelL1 {
		t.Fatalf("unexpected risk %q", first.created[0].RiskLevel)
	}
}

func TestConnectorGatewayClientSequencesAutomaticSchedulingFence(t *testing.T) {
	st := &fakeTaskStore{result: json.RawMessage(`{}`)}
	client := testClient(st, contracts.InstanceKindSub2API)
	inst := testInstance(contracts.InstanceKindSub2API)
	ctx := contracts.WithGatewaySchedulingFence(context.Background(), contracts.GatewaySchedulingFence{
		Scope: "auto-switch/plan/plan-1", Version: 7,
	})

	if err := client.SetSchedulable(ctx, inst, "account-a", true); err != nil {
		t.Fatalf("first automatic scheduling task: %v", err)
	}
	if err := client.SetSchedulable(ctx, inst, "account-b", false); err != nil {
		t.Fatalf("second automatic scheduling task: %v", err)
	}
	if len(st.created) != 2 {
		t.Fatalf("created tasks = %d, want 2", len(st.created))
	}
	for i, task := range st.created {
		var input contracts.ConnectorGatewaySchedulableSetInput
		if err := json.Unmarshal(task.Input, &input); err != nil {
			t.Fatalf("decode task %d input: %v", i, err)
		}
		if input.Fence == nil || input.Fence.Scope != "auto-switch/plan/plan-1" ||
			input.Fence.Version != 7 || input.Fence.Sequence != int64(i+1) {
			t.Fatalf("task %d fence = %+v", i, input.Fence)
		}
	}
}

func TestConnectorGatewayClientQueuesExactFencedTrafficShareIncludingZero(t *testing.T) {
	for _, weight := range []int{0, 10, 100} {
		t.Run(string(rune('a'+weight%26)), func(t *testing.T) {
			fence := contracts.GatewaySchedulingFence{Scope: "recommendation/rollout/plan-1", Version: 9, Sequence: 1}
			st := &fakeTaskStore{result: mustRawJSON(t, contracts.ConnectorGatewayTrafficShareSetResult{
				AccountID: "42", Weight: weight, Fence: fence,
			})}
			client := testClient(st, contracts.InstanceKindNewAPI)
			ctx := contracts.WithGatewaySchedulingFence(context.Background(), contracts.GatewaySchedulingFence{
				Scope: fence.Scope, Version: fence.Version,
			})
			if err := client.SetTrafficShare(ctx, testInstance(contracts.InstanceKindNewAPI), "42", weight); err != nil {
				t.Fatal(err)
			}
			if len(st.created) != 1 || st.created[0].Type != contracts.ConnectorTaskGatewayTrafficShareSet ||
				st.created[0].RiskLevel != contracts.RiskLevelL1 {
				t.Fatalf("task = %+v", st.created)
			}
			var input contracts.ConnectorGatewayTrafficShareSetInput
			if err := json.Unmarshal(st.created[0].Input, &input); err != nil {
				t.Fatal(err)
			}
			if input.AccountID != "42" || input.Weight != weight || input.Fence != fence {
				t.Fatalf("input = %+v", input)
			}
		})
	}
}

func TestConnectorGatewayClientWaitsForExecutingTrafficShareReceipt(t *testing.T) {
	fence := contracts.GatewaySchedulingFence{Scope: "recommendation/rollout/plan-1", Version: 9, Sequence: 1}
	st := &taskStatusSequenceStore{sequence: []contracts.ConnectorTask{
		{Status: contracts.ConnectorTaskExecuting},
		{Status: contracts.ConnectorTaskExecuting},
		{Status: contracts.ConnectorTaskSucceeded, Result: mustRawJSON(t, contracts.ConnectorGatewayTrafficShareSetResult{
			AccountID: "42", Weight: 0, Fence: fence,
		})},
	}}
	ctx := contracts.WithGatewaySchedulingFence(context.Background(), contracts.GatewaySchedulingFence{
		Scope: fence.Scope, Version: fence.Version,
	})
	if err := testClient(st, contracts.InstanceKindNewAPI).SetTrafficShare(
		ctx, testInstance(contracts.InstanceKindNewAPI), "42", 0,
	); err != nil {
		t.Fatalf("executing traffic-share task: %v", err)
	}
	if st.getCalls != 3 {
		t.Fatalf("task reads = %d, want 3", st.getCalls)
	}
}

func TestConnectorGatewayClientExecutingTaskTimesOutWithoutProtocolError(t *testing.T) {
	st := &taskStatusSequenceStore{sequence: []contracts.ConnectorTask{{Status: contracts.ConnectorTaskExecuting}}}
	client := testClient(st, contracts.InstanceKindNewAPI)
	client.timeout = 30 * time.Millisecond
	client.poll = time.Millisecond
	ctx := contracts.WithGatewaySchedulingFence(context.Background(), contracts.GatewaySchedulingFence{
		Scope: "recommendation/rollout/plan-1", Version: 9,
	})
	err := client.SetTrafficShare(ctx, testInstance(contracts.InstanceKindNewAPI), "42", 25)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "invalid status") {
		t.Fatalf("executing timeout = %v", err)
	}
	if st.getCalls < 2 {
		t.Fatalf("executing task was not polled: reads=%d", st.getCalls)
	}
}

func TestConnectorGatewayClientExecutingTaskPreservesTerminalFailureCode(t *testing.T) {
	st := &taskStatusSequenceStore{sequence: []contracts.ConnectorTask{
		{Status: contracts.ConnectorTaskExecuting},
		{Status: contracts.ConnectorTaskFailed, Error: contracts.ConnectorTaskError{Code: "gateway_rejected"}},
	}}
	ctx := contracts.WithGatewaySchedulingFence(context.Background(), contracts.GatewaySchedulingFence{
		Scope: "recommendation/rollout/plan-1", Version: 9,
	})
	err := testClient(st, contracts.InstanceKindNewAPI).SetTrafficShare(
		ctx, testInstance(contracts.InstanceKindNewAPI), "42", 25,
	)
	if err == nil || !strings.Contains(err.Error(), "gateway_rejected") || strings.Contains(err.Error(), "invalid status") {
		t.Fatalf("executing terminal failure = %v", err)
	}
}

func TestConnectorGatewayClientReusesExpiredExecutingDuplicate(t *testing.T) {
	fence := contracts.GatewaySchedulingFence{Scope: "recommendation/rollout/plan-1", Version: 9, Sequence: 1}
	st := &executingDuplicateTaskStore{result: mustRawJSON(t, contracts.ConnectorGatewayTrafficShareSetResult{
		AccountID: "42", Weight: 0, Fence: fence,
	})}
	ctx := contracts.WithGatewaySchedulingFence(context.Background(), contracts.GatewaySchedulingFence{
		Scope: fence.Scope, Version: fence.Version,
	})
	if err := testClient(st, contracts.InstanceKindNewAPI).SetTrafficShare(
		ctx, testInstance(contracts.InstanceKindNewAPI), "42", 0,
	); err != nil {
		t.Fatalf("reuse expired executing duplicate: %v", err)
	}
	if st.createCalls != 1 || st.getCalls != 2 {
		t.Fatalf("duplicate recovery calls: create=%d get=%d", st.createCalls, st.getCalls)
	}
}

func TestConnectorGatewayClientRejectsUnsupportedOrUnfencedTrafficShare(t *testing.T) {
	for _, kind := range []contracts.InstanceKind{contracts.InstanceKindSub2API, contracts.InstanceKindCPA} {
		st := &fakeTaskStore{}
		err := testClient(st, kind).SetTrafficShare(context.Background(), testInstance(kind), "42", 10)
		if !errors.Is(err, ErrGatewayMutationNotDispatched) || len(st.created) != 0 {
			t.Fatalf("kind %s: err=%v tasks=%+v", kind, err, st.created)
		}
	}
	st := &fakeTaskStore{}
	err := testClient(st, contracts.InstanceKindNewAPI).SetTrafficShare(
		context.Background(), testInstance(contracts.InstanceKindNewAPI), "42", 10,
	)
	if !errors.Is(err, ErrGatewayMutationNotDispatched) || len(st.created) != 0 {
		t.Fatalf("unfenced: err=%v tasks=%+v", err, st.created)
	}
	for _, weight := range []int{-1, 101} {
		st = &fakeTaskStore{}
		ctx := contracts.WithGatewaySchedulingFence(context.Background(), contracts.GatewaySchedulingFence{
			Scope: "recommendation/rollout/plan-1", Version: 1,
		})
		err = testClient(st, contracts.InstanceKindNewAPI).SetTrafficShare(ctx, testInstance(contracts.InstanceKindNewAPI), "42", weight)
		if !errors.Is(err, ErrGatewayMutationNotDispatched) || len(st.created) != 0 {
			t.Fatalf("weight %d: err=%v tasks=%+v", weight, err, st.created)
		}
	}
}

func TestConnectorGatewayClientRejectsMismatchedTrafficShareReceipt(t *testing.T) {
	st := &fakeTaskStore{result: mustRawJSON(t, contracts.ConnectorGatewayTrafficShareSetResult{
		AccountID: "42", Weight: 25,
		Fence: contracts.GatewaySchedulingFence{Scope: "recommendation/rollout/plan-1", Version: 1, Sequence: 1},
	})}
	ctx := contracts.WithGatewaySchedulingFence(context.Background(), contracts.GatewaySchedulingFence{
		Scope: "recommendation/rollout/plan-1", Version: 1,
	})
	err := testClient(st, contracts.InstanceKindNewAPI).SetTrafficShare(ctx, testInstance(contracts.InstanceKindNewAPI), "42", 10)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestConnectorGatewayClientQueuesSchedulingBarrierWithNextFence(t *testing.T) {
	st := &fakeTaskStore{result: json.RawMessage(`{}`)}
	client := testClient(st, contracts.InstanceKindSub2API)
	inst := testInstance(contracts.InstanceKindSub2API)
	ctx := contracts.WithGatewaySchedulingFence(context.Background(), contracts.GatewaySchedulingFence{
		Scope: "auto-switch/plan/plan-1", Version: 7,
	})

	if err := client.SchedulingBarrier(ctx, inst); err != nil {
		t.Fatalf("scheduling barrier: %v", err)
	}
	if err := client.SetSchedulable(ctx, inst, "account-a", false); err != nil {
		t.Fatalf("scheduling mutation after barrier: %v", err)
	}
	if len(st.created) != 2 {
		t.Fatalf("created tasks = %d, want 2", len(st.created))
	}
	barrier := st.created[0]
	if barrier.Type != contracts.ConnectorTaskGatewaySchedulingBarrier || barrier.RiskLevel != contracts.RiskLevelL1 {
		t.Fatalf("unexpected barrier task: %+v", barrier)
	}
	var barrierInput contracts.ConnectorGatewaySchedulingBarrierInput
	if err := json.Unmarshal(barrier.Input, &barrierInput); err != nil {
		t.Fatal(err)
	}
	if barrierInput.Fence.Scope != "auto-switch/plan/plan-1" ||
		barrierInput.Fence.Version != 7 || barrierInput.Fence.Sequence != 1 {
		t.Fatalf("barrier fence = %+v", barrierInput.Fence)
	}
	var mutationInput contracts.ConnectorGatewaySchedulableSetInput
	if err := json.Unmarshal(st.created[1].Input, &mutationInput); err != nil {
		t.Fatal(err)
	}
	if mutationInput.Fence == nil || mutationInput.Fence.Sequence != 2 {
		t.Fatalf("mutation fence = %+v, want sequence 2", mutationInput.Fence)
	}
}

func TestConnectorGatewayClientRejectsSchedulingBarrierWithoutFenceContext(t *testing.T) {
	st := &fakeTaskStore{result: json.RawMessage(`{}`)}
	err := testClient(st, contracts.InstanceKindSub2API).SchedulingBarrier(
		context.Background(),
		testInstance(contracts.InstanceKindSub2API),
	)
	if err == nil || !strings.Contains(err.Error(), "requires a scheduling fence") {
		t.Fatalf("expected scheduling fence error, got %v", err)
	}
	if len(st.created) != 0 {
		t.Fatal("barrier without a fence must not create a task")
	}
}

func TestConnectorGatewayClientLeavesManualSchedulingUnfenced(t *testing.T) {
	st := &fakeTaskStore{result: json.RawMessage(`{}`)}
	client := testClient(st, contracts.InstanceKindSub2API)
	if err := client.SetSchedulable(context.Background(), testInstance(contracts.InstanceKindSub2API), "account-a", false); err != nil {
		t.Fatalf("manual scheduling task: %v", err)
	}
	var input contracts.ConnectorGatewaySchedulableSetInput
	if err := json.Unmarshal(st.created[0].Input, &input); err != nil {
		t.Fatal(err)
	}
	if input.Fence != nil {
		t.Fatalf("manual task unexpectedly fenced: %+v", input.Fence)
	}
}

func TestConnectorGatewayClientRequiresConnectorID(t *testing.T) {
	st := &fakeTaskStore{result: json.RawMessage(`{"accounts":[]}`)}
	inst := testInstance(contracts.InstanceKindCPA)
	inst.ConnectorID = ""
	_, err := testClient(st, inst.Kind).ListAccounts(context.Background(), inst)
	if err == nil || !strings.Contains(err.Error(), "connector_id") {
		t.Fatalf("expected connector_id error, got %v", err)
	}
	if len(st.created) != 0 {
		t.Fatal("task should not be created without connector_id")
	}
}

func TestConnectorGatewayClientQueuesProtocolV2LifecycleTasks(t *testing.T) {
	st := &fakeTaskStore{result: json.RawMessage(`{"remote_id":"remote-1","created":true}`)}
	client := testClient(st, contracts.InstanceKindSub2API)
	inst := testInstance(contracts.InstanceKindSub2API)
	spec := contracts.GatewayAccountSpec{
		Ownership:           contracts.GatewayAccountPlatformManaged,
		ChannelID:           "channel-1",
		RemoteID:            "remote-1",
		CredentialBindingID: "credential-binding-1",
		ProxyBindingID:      "proxy-binding-1",
	}

	if _, err := client.ProvisionAccount(context.Background(), inst, spec); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if _, err := client.UpdateAccount(context.Background(), inst, spec); err != nil {
		t.Fatalf("update: %v", err)
	}
	ctx := contracts.WithGatewaySchedulingFence(context.Background(), contracts.GatewaySchedulingFence{Scope: "auto-switch/plan/plan-1", Version: 7})
	if err := client.DeleteAccount(ctx, inst, "remote-1"); err != nil {
		t.Fatalf("delete enqueue: %v", err)
	}
	if len(st.created) != 3 || st.created[0].Type != contracts.ConnectorTaskGatewayAccountCreate ||
		st.created[1].Type != contracts.ConnectorTaskGatewayAccountUpdate || st.created[2].Type != contracts.ConnectorTaskGatewayAccountDelete {
		t.Fatalf("lifecycle tasks = %+v", st.created)
	}
	if !st.created[2].AvailableAt.Equal(client.now().Add(deferredAccountDeleteDelay)) ||
		!st.created[2].ExpiresAt.Equal(client.now().Add(deferredAccountDeleteExpiry)) ||
		st.created[2].MaxAttempts != deferredAccountDeleteRetries {
		t.Fatalf("deferred delete window = available %s expires %s", st.created[2].AvailableAt, st.created[2].ExpiresAt)
	}
}

func TestConnectorGatewayClientDeferredDeleteIdempotencyIsGenerationScoped(t *testing.T) {
	st := &fakeTaskStore{result: json.RawMessage(`{}`)}
	client := testClient(st, contracts.InstanceKindSub2API)
	inst := testInstance(contracts.InstanceKindSub2API)

	for _, generation := range []int64{7, 8} {
		ctx := contracts.WithGatewaySchedulingFence(context.Background(), contracts.GatewaySchedulingFence{
			Scope: "auto-switch/plan/plan-1", Version: generation,
		})
		if err := client.DeleteAccount(ctx, inst, "remote-1"); err != nil {
			t.Fatalf("enqueue generation %d delete: %v", generation, err)
		}
	}
	if len(st.created) != 2 {
		t.Fatalf("created deletes=%d, want one per generation", len(st.created))
	}
	if st.created[0].IdempotencyKey == st.created[1].IdempotencyKey {
		t.Fatalf("new generation reused stale delete idempotency key %q", st.created[0].IdempotencyKey)
	}
	for i, generation := range []int64{7, 8} {
		var input contracts.ConnectorGatewayAccountDeleteInput
		if err := json.Unmarshal(st.created[i].Input, &input); err != nil {
			t.Fatalf("decode delete %d: %v", i, err)
		}
		if input.Fence == nil || input.Fence.Version != generation || input.Fence.Sequence != 1 {
			t.Fatalf("delete %d fence=%+v, want generation %d sequence 1", i, input.Fence, generation)
		}
	}
}

func TestConnectorGatewayClientCurrentGenerationDeleteCoexistsWithOlderPendingDelete(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(100, 0).UTC()
	st := store.NewMemoryStore(now)
	user, err := st.CreateUser(ctx, contracts.User{
		Email: "delete-generation@example.com", PasswordHash: "test",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID, Name: "gateway", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	enrollment, err := st.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{
		UserID: user.ID, InstanceID: instance.ID, ConnectorID: "connector-delete-generation",
		TokenHash: "delete-generation-enrollment",
	})
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	connector, _, err := st.UseConnectorEnrollment(ctx, enrollment.TokenHash, contracts.Connector{
		ID: enrollment.ConnectorID, InstanceID: instance.ID, Version: "0.1.0-test",
		ProtocolVersion: contracts.ConnectorProtocolVersion, TokenHash: "delete-generation-token",
	})
	if err != nil {
		t.Fatalf("use enrollment: %v", err)
	}
	instance.ConnectorID = connector.ID
	client := testClient(st, instance.Kind)
	client.now = func() time.Time { return now }
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: "plan-1", UserID: user.ID, InstanceID: instance.ID,
		Status: contracts.RoutePlanPublished, SchedulingGeneration: 7,
	})
	if err != nil {
		t.Fatalf("create route plan: %v", err)
	}

	for _, generation := range []int64{7, 8} {
		if generation != plan.SchedulingGeneration {
			plan, err = st.ClaimRoutePlanScheduling(ctx, plan.ID, contracts.RoutePlanPublished)
			if err != nil {
				t.Fatalf("advance route plan generation: %v", err)
			}
		}
		deleteCtx := contracts.WithGatewaySchedulingFence(ctx, contracts.GatewaySchedulingFence{
			Scope: "auto-switch/plan/plan-1", Version: generation,
		})
		if err := client.DeleteAccount(deleteCtx, instance, "remote-1"); err != nil {
			t.Fatalf("enqueue generation %d delete: %v", generation, err)
		}
	}
	tasks, err := st.ListConnectorTasks(ctx, contracts.ConnectorTaskFilter{
		InstanceID: instance.ID, ConnectorID: connector.ID,
		Types: []contracts.ConnectorTaskType{contracts.ConnectorTaskGatewayAccountDelete},
	})
	if err != nil {
		t.Fatalf("list delete tasks: %v", err)
	}
	if len(tasks) != 2 || tasks[0].Status != contracts.ConnectorTaskPending ||
		tasks[1].Status != contracts.ConnectorTaskFailed || tasks[1].Error.Code != "scheduling_fence_stale" {
		t.Fatalf("delete tasks=%+v, want current pending and old generation fenced", tasks)
	}
	if tasks[0].IdempotencyKey == tasks[1].IdempotencyKey {
		t.Fatalf("current generation delete collided with older pending task: %+v", tasks)
	}
}

func TestConnectorGatewayClientRejectsOwnerProvidedCreateAndUnfencedDelete(t *testing.T) {
	st := &fakeTaskStore{}
	client := testClient(st, contracts.InstanceKindSub2API)
	inst := testInstance(contracts.InstanceKindSub2API)
	_, err := client.ProvisionAccount(context.Background(), inst, contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountOwnerProvided, ChannelID: "channel-1", CredentialBindingID: "binding-1",
	})
	if err == nil || !strings.Contains(err.Error(), "update-only") {
		t.Fatalf("owner create error = %v", err)
	}
	if err := client.DeleteAccount(context.Background(), inst, "remote-1"); err == nil || !strings.Contains(err.Error(), "requires a scheduling fence") {
		t.Fatalf("unfenced delete error = %v", err)
	}
	if len(st.created) != 0 {
		t.Fatalf("forbidden operations queued tasks: %+v", st.created)
	}
}

func TestConnectorGatewayClientQueuesOwnerProvidedUpdateOnly(t *testing.T) {
	st := &fakeTaskStore{result: json.RawMessage(`{"remote_id":"remote-owner","created":false}`)}
	client := testClient(st, contracts.InstanceKindSub2API)
	inst := testInstance(contracts.InstanceKindSub2API)
	result, err := client.UpdateAccount(context.Background(), inst, contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountOwnerProvided, ChannelID: "channel-owner",
		RemoteID: "remote-owner",
	})
	if err != nil || result.RemoteID != "remote-owner" || result.Created {
		t.Fatalf("owner update = %+v err=%v", result, err)
	}
	if len(st.created) != 1 || st.created[0].Type != contracts.ConnectorTaskGatewayAccountUpdate {
		t.Fatalf("owner update tasks = %+v", st.created)
	}
	var input contracts.ConnectorGatewayAccountUpdateInput
	if err := json.Unmarshal(st.created[0].Input, &input); err != nil ||
		input.Spec.Ownership != contracts.GatewayAccountOwnerProvided || input.Spec.RemoteID != "remote-owner" ||
		input.Spec.CredentialBindingID != "" || input.Spec.ProxyBindingID != "" {
		t.Fatalf("owner update input = %+v err=%v", input, err)
	}
}

func TestConnectorGatewayClientRejectsOwnerProvidedUpdateWithCredentialBinding(t *testing.T) {
	st := &fakeTaskStore{}
	client := testClient(st, contracts.InstanceKindSub2API)
	_, err := client.UpdateAccount(context.Background(), testInstance(contracts.InstanceKindSub2API), contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountOwnerProvided, ChannelID: "channel-owner",
		RemoteID: "remote-owner", CredentialBindingID: "must-not-be-sent",
	})
	if !errors.Is(err, ErrGatewayMutationNotDispatched) || len(st.created) != 0 {
		t.Fatalf("credential-bearing owner update err=%v tasks=%+v", err, st.created)
	}
}

func TestConnectorGatewayClientRejectsLooseResult(t *testing.T) {
	st := &fakeTaskStore{result: json.RawMessage(`{"accounts":[],"raw_response":"not allowed"}`)}
	_, err := testClient(st, contracts.InstanceKindNewAPI).ListAccounts(context.Background(), testInstance(contracts.InstanceKindNewAPI))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected strict DTO error, got %v", err)
	}
}

func TestConnectorGatewayClientReturnsStructuredTaskFailure(t *testing.T) {
	st := &fakeTaskStore{failure: contracts.ConnectorTaskError{Code: "gateway_auth_failed"}}
	_, err := testClient(st, contracts.InstanceKindSub2API).ListAccounts(context.Background(), testInstance(contracts.InstanceKindSub2API))
	if err == nil || !strings.Contains(err.Error(), "gateway_auth_failed") {
		t.Fatalf("expected connector failure, got %v", err)
	}
}

func TestConnectorGatewayClientDoesNotReuseTerminalDuplicate(t *testing.T) {
	st := &fakeTaskStore{
		create: errors.New("duplicate"),
		listed: []contracts.ConnectorTask{{
			ID:     "old-failed-task",
			Type:   contracts.ConnectorTaskGatewayAccountsList,
			Status: contracts.ConnectorTaskFailed,
			IdempotencyKey: connectorTaskIdempotencyKey(
				contracts.InstanceKindSub2API,
				testInstance(contracts.InstanceKindSub2API),
				contracts.ConnectorTaskGatewayAccountsList,
				json.RawMessage(`{}`),
				"",
			),
		}},
	}
	_, err := testClient(st, contracts.InstanceKindSub2API).ListAccounts(
		context.Background(),
		testInstance(contracts.InstanceKindSub2API),
	)
	if err == nil || !strings.Contains(err.Error(), "create gateway.accounts.list task") {
		t.Fatalf("terminal duplicate must not be reused, got %v", err)
	}
}
