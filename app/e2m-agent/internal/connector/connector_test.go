package connector

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
)

const (
	testConnectorID    = "connector-1"
	testInstanceID     = "instance-1"
	testConnectorToken = "connector-token"
)

func TestPollOnceExecutesTypedTasksFromLocalGatewayConfig(t *testing.T) {
	type gatewayCall struct {
		method      string
		path        string
		schedulable bool
	}
	var (
		gatewayMu    sync.Mutex
		gatewayCalls []gatewayCall
	)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "local-admin-key" {
			t.Errorf("gateway x-api-key = %q, want local credential", got)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		call := gatewayCall{method: r.Method, path: r.URL.Path}
		if r.Method != http.MethodGet {
			var body struct {
				Schedulable bool `json:"schedulable"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode gateway mutation: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			call.schedulable = body.Schedulable
		}
		gatewayMu.Lock()
		gatewayCalls = append(gatewayCalls, call)
		gatewayMu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/accounts":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":0,"data":[{"id":7,"platform":"openai","type":"oauth","status":"active","schedulable":true,"priority":10,"groups":["primary"],"proxy_id":"proxy-1","name":"Primary"}],"token":"gateway-secret"}`)
		case r.Method == http.MethodPost && (r.URL.Path == "/api/v1/admin/accounts/7/schedulable" || r.URL.Path == "/api/v1/admin/accounts/8/schedulable"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
		default:
			t.Errorf("unexpected gateway request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()

	tasks := []contracts.ConnectorTask{
		newTask(t, "health", contracts.ConnectorTaskGatewayHealth, nil),
		newTask(t, "accounts", contracts.ConnectorTaskGatewayAccountsList, nil),
		newTask(t, "schedulable", contracts.ConnectorTaskGatewaySchedulableSet, contracts.ConnectorGatewaySchedulableSetInput{
			AccountID: "7", Schedulable: false,
		}),
		newTask(t, "switch", contracts.ConnectorTaskGatewaySwitch, contracts.ConnectorGatewaySwitchInput{
			DisableAccountID: "7", EnableAccountID: "8",
		}),
	}
	tasks[2].IdempotencyKey = "schedulable-7-disabled"
	tasks[3].IdempotencyKey = "switch-7-to-8"
	core := newCoreStub(t, [][]contracts.ConnectorTask{tasks})
	conn := newSub2APIConnector(t, core.server.URL, gateway.URL)

	if err := conn.PollOnce(t.Context()); err != nil {
		t.Fatalf("poll once: %v", err)
	}

	leases := core.leaseRequests()
	if len(leases) != 1 {
		t.Fatalf("lease requests = %d, want 1", len(leases))
	}
	lease := leases[0]
	if lease.ConnectorID != testConnectorID || lease.ProtocolVersion != contracts.ConnectorProtocolVersion {
		t.Fatalf("unexpected lease identity/protocol: %+v", lease)
	}
	wantCapabilities := []contracts.ConnectorTaskType{
		contracts.ConnectorTaskGatewayHealth,
		contracts.ConnectorTaskGatewayAccountsList,
		contracts.ConnectorTaskGatewaySchedulableSet,
		contracts.ConnectorTaskGatewaySwitch,
		contracts.ConnectorTaskGatewaySchedulingBarrier,
	}
	if !lease.RuntimeState.GatewayConfigured || lease.RuntimeState.GatewayKind != "sub2api" ||
		lease.RuntimeState.ProtocolVersion != contracts.ConnectorProtocolVersion ||
		!reflect.DeepEqual(lease.RuntimeState.Capabilities, wantCapabilities) ||
		lease.RuntimeState.ObservationCapabilities != nil || lease.RuntimeState.QualityProbe != nil {
		t.Fatalf("unexpected connector runtime state: %+v", lease.RuntimeState)
	}
	leaseJSON, err := json.Marshal(lease)
	if err != nil {
		t.Fatalf("marshal lease: %v", err)
	}
	if strings.Contains(string(leaseJSON), gateway.URL) || strings.Contains(string(leaseJSON), "local-admin-key") {
		t.Fatalf("lease leaked local gateway configuration: %s", leaseJSON)
	}

	completed := core.completionMap()
	if len(completed) != len(tasks) {
		t.Fatalf("completions = %d, want %d", len(completed), len(tasks))
	}
	for _, task := range tasks {
		completion := completed[task.ID]
		if !completion.Success || completion.ConnectorID != testConnectorID ||
			completion.LeaseNonce != task.LeaseNonce || completion.Error.Code != "" {
			t.Fatalf("task %s completion = %+v", task.ID, completion)
		}
		if strings.Contains(string(completion.Result), "gateway-secret") {
			t.Fatalf("task %s leaked an untyped gateway field: %s", task.ID, completion.Result)
		}
	}
	var health contracts.ConnectorGatewayHealthResult
	if err := json.Unmarshal(completed["health"].Result, &health); err != nil || health.Status != "ok" || health.CheckedAt.IsZero() {
		t.Fatalf("unexpected typed health result: %+v (err=%v)", health, err)
	}
	var accounts contracts.ConnectorGatewayAccountsListResult
	if err := json.Unmarshal(completed["accounts"].Result, &accounts); err != nil {
		t.Fatalf("decode accounts result: %v", err)
	}
	if len(accounts.Accounts) != 1 || accounts.Accounts[0].ID != "7" || accounts.Accounts[0].DisplayName != "Primary" || !accounts.Accounts[0].Schedulable {
		t.Fatalf("unexpected typed accounts result: %+v", accounts)
	}

	gatewayMu.Lock()
	gotCalls := append([]gatewayCall(nil), gatewayCalls...)
	gatewayMu.Unlock()
	wantCalls := []gatewayCall{
		{method: http.MethodGet, path: "/api/v1/admin/accounts"},
		{method: http.MethodGet, path: "/api/v1/admin/accounts"},
		{method: http.MethodPost, path: "/api/v1/admin/accounts/7/schedulable", schedulable: false},
		{method: http.MethodPost, path: "/api/v1/admin/accounts/7/schedulable", schedulable: false},
		{method: http.MethodPost, path: "/api/v1/admin/accounts/8/schedulable", schedulable: true},
	}
	if !reflect.DeepEqual(gotCalls, wantCalls) {
		t.Fatalf("gateway calls = %+v, want %+v", gotCalls, wantCalls)
	}
}

func TestGatewayClientUsesLocalTimeoutWithoutChangingCoreClient(t *testing.T) {
	conn := newSub2APIConnector(t, "http://core.invalid", "http://gateway.invalid")
	cfg, err := conn.cfg.ConfigStore.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Runtime.GatewayRequestTimeoutSeconds = 7
	if err := conn.cfg.ConfigStore.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if got := conn.gatewayClient(cfg.Runtime.GatewayRequestTimeoutSeconds).Timeout; got != 7*time.Second {
		t.Fatalf("gateway timeout = %s, want 7s", got)
	}
	if conn.coreHTTP.Timeout != 15*time.Second {
		t.Fatalf("gateway setting changed Core timeout to %s", conn.coreHTTP.Timeout)
	}
}

func TestPollOnceRejectsLeasedTaskWithoutNonce(t *testing.T) {
	task := newTask(t, "missing-lease-nonce", contracts.ConnectorTaskGatewayAccountsList, nil)
	task.LeaseNonce = ""
	core := newCoreStub(t, [][]contracts.ConnectorTask{{task}})
	conn := newSub2APIConnector(t, core.server.URL, "http://127.0.0.1:1")

	err := conn.PollOnce(t.Context())
	if err == nil || !strings.Contains(err.Error(), "did not include a lease nonce") {
		t.Fatalf("PollOnce() error = %v, want missing lease nonce rejection", err)
	}
	if completed := core.completionMap(); len(completed) != 0 {
		t.Fatalf("task without a lease nonce was completed: %+v", completed)
	}
}

func TestPollOnceEnrollsAndUsesCurrentProtocol(t *testing.T) {
	var (
		mu        sync.Mutex
		enroll    contracts.ConnectorEnrollRequest
		lease     contracts.ConnectorTaskLeaseRequest
		leaseAuth string
	)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/connectors/enroll":
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("enrollment authorization = %q, want empty", got)
			}
			var got contracts.ConnectorEnrollRequest
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode enrollment: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			mu.Lock()
			enroll = got
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(contracts.ConnectorEnrollResponse{
				ProtocolVersion: contracts.ConnectorProtocolVersion,
				ConnectorToken:  testConnectorToken,
			})
		case "/api/v1/connectors/tasks/lease":
			var got contracts.ConnectorTaskLeaseRequest
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode lease: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			mu.Lock()
			lease = got
			leaseAuth = r.Header.Get("Authorization")
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(contracts.ConnectorTaskLeaseResponse{
				ProtocolVersion: contracts.ConnectorProtocolVersion,
			})
		default:
			t.Errorf("unexpected core request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer core.Close()

	tokenFile := filepath.Join(t.TempDir(), "connector.token")
	conn := New(Config{
		CoreURL:     core.URL,
		EnrollToken: "one-time-enrollment-token",
		TokenFile:   tokenFile,
		ConnectorID: testConnectorID,
		InstanceID:  testInstanceID,
		Version:     "0.1.0-test",
	})
	if err := conn.PollOnce(t.Context()); err != nil {
		t.Fatalf("poll once: %v", err)
	}

	mu.Lock()
	gotEnroll, gotLease, gotLeaseAuth := enroll, lease, leaseAuth
	mu.Unlock()
	if gotEnroll.EnrollmentToken != "one-time-enrollment-token" || gotEnroll.ConnectorID != testConnectorID ||
		gotEnroll.InstanceID != testInstanceID || gotEnroll.Version != "0.1.0-test" ||
		gotEnroll.ProtocolVersion != contracts.ConnectorProtocolVersion {
		t.Fatalf("unexpected enrollment request: %+v", gotEnroll)
	}
	if gotLease.ConnectorID != testConnectorID || gotLease.ProtocolVersion != contracts.ConnectorProtocolVersion {
		t.Fatalf("unexpected post-enrollment lease: %+v", gotLease)
	}
	if gotLeaseAuth != "Bearer "+testConnectorToken {
		t.Fatalf("post-enrollment authorization = %q", gotLeaseAuth)
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("read persisted connector token: %v", err)
	}
	if strings.TrimSpace(string(raw)) != testConnectorToken {
		t.Fatalf("persisted connector token = %q", raw)
	}
}

func TestPollOnceRejectsUnsupportedCoreProtocol(t *testing.T) {
	t.Run("enrollment response", func(t *testing.T) {
		core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/connectors/enroll" {
				t.Errorf("unexpected core request %s %s", r.Method, r.URL.Path)
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(contracts.ConnectorEnrollResponse{
				ProtocolVersion: contracts.ConnectorProtocolVersion + 1,
				ConnectorToken:  testConnectorToken,
			})
		}))
		defer core.Close()

		tokenFile := filepath.Join(t.TempDir(), "connector.token")
		conn := New(Config{
			CoreURL:     core.URL,
			EnrollToken: "one-time-enrollment-token",
			TokenFile:   tokenFile,
			ConnectorID: testConnectorID,
			InstanceID:  testInstanceID,
		})
		err := conn.PollOnce(t.Context())
		if err == nil || !strings.Contains(err.Error(), "core protocol version") {
			t.Fatalf("PollOnce() error = %v, want protocol rejection", err)
		}
		if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
			t.Fatalf("unsupported enrollment protocol persisted a token: %v", err)
		}
	})

	t.Run("lease response", func(t *testing.T) {
		core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/connectors/tasks/lease" {
				t.Errorf("unexpected core request %s %s", r.Method, r.URL.Path)
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(contracts.ConnectorTaskLeaseResponse{
				ProtocolVersion: contracts.ConnectorProtocolVersion + 1,
			})
		}))
		defer core.Close()

		tokenFile := filepath.Join(t.TempDir(), "connector.token")
		if err := os.WriteFile(tokenFile, []byte(testConnectorToken+"\n"), 0600); err != nil {
			t.Fatalf("write connector token: %v", err)
		}
		conn := New(Config{
			CoreURL:     core.URL,
			TokenFile:   tokenFile,
			ConnectorID: testConnectorID,
			InstanceID:  testInstanceID,
		})
		err := conn.PollOnce(t.Context())
		if err == nil || !strings.Contains(err.Error(), "core protocol version") {
			t.Fatalf("PollOnce() error = %v, want protocol rejection", err)
		}
	})
}

func TestPollOnceReloadsConnectorTokenFile(t *testing.T) {
	var (
		mu    sync.Mutex
		auths []string
	)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/connectors/tasks/lease" {
			t.Errorf("unexpected core request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(contracts.ConnectorTaskLeaseResponse{
			ProtocolVersion: contracts.ConnectorProtocolVersion,
		})
	}))
	defer core.Close()

	tokenFile := filepath.Join(t.TempDir(), "connector.token")
	if err := os.WriteFile(tokenFile, []byte("old-token\n"), 0600); err != nil {
		t.Fatalf("write old token: %v", err)
	}
	conn := New(Config{
		CoreURL:     core.URL,
		TokenFile:   tokenFile,
		ConnectorID: testConnectorID,
		InstanceID:  testInstanceID,
	})
	if err := conn.PollOnce(t.Context()); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if err := os.WriteFile(tokenFile, []byte("new-token\n"), 0600); err != nil {
		t.Fatalf("write new token: %v", err)
	}
	if err := conn.PollOnce(t.Context()); err != nil {
		t.Fatalf("second poll: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), auths...)
	mu.Unlock()
	want := []string{"Bearer old-token", "Bearer new-token"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lease authorization = %q, want %q", got, want)
	}
}

func TestPollOnceRejectsTaskIdentityAndSchemaBeforeGatewayAccess(t *testing.T) {
	var (
		gatewayMu    sync.Mutex
		gatewayCalls int
	)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayMu.Lock()
		gatewayCalls++
		gatewayMu.Unlock()
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer gateway.Close()

	wrongConnector := newTask(t, "wrong-connector", contracts.ConnectorTaskGatewayAccountsList, nil)
	wrongConnector.ConnectorID = "connector-other"
	wrongInstance := newTask(t, "wrong-instance", contracts.ConnectorTaskGatewayAccountsList, nil)
	wrongInstance.InstanceID = "instance-other"
	wrongVersion := newTask(t, "wrong-version", contracts.ConnectorTaskGatewayAccountsList, nil)
	wrongVersion.SchemaVersion = taskSchemaVersion + 1
	wrongInput := newTask(t, "wrong-input", contracts.ConnectorTaskGatewayAccountsList, nil)
	wrongInput.Input = json.RawMessage(`{"unexpected":true}`)

	core := newCoreStub(t, [][]contracts.ConnectorTask{{wrongConnector, wrongInstance, wrongVersion, wrongInput}})
	conn := newSub2APIConnector(t, core.server.URL, gateway.URL)
	if err := conn.PollOnce(t.Context()); err != nil {
		t.Fatalf("poll once: %v", err)
	}

	wantCodes := map[string]string{
		"wrong-connector": "connector_mismatch",
		"wrong-instance":  "instance_mismatch",
		"wrong-version":   "schema_version_unsupported",
		"wrong-input":     "invalid_task_input",
	}
	completed := core.completionMap()
	for taskID, wantCode := range wantCodes {
		completion, ok := completed[taskID]
		if !ok {
			t.Fatalf("task %s was not completed", taskID)
		}
		if completion.Success || completion.Error.Code != wantCode || completion.Error.Retryable {
			t.Fatalf("task %s completion = %+v, want non-retryable %s", taskID, completion, wantCode)
		}
	}
	gatewayMu.Lock()
	gotGatewayCalls := gatewayCalls
	gatewayMu.Unlock()
	if gotGatewayCalls != 0 {
		t.Fatalf("rejected tasks reached gateway %d times", gotGatewayCalls)
	}
}

func TestWriteTaskIdempotencyReplaysSuccessAndRejectsConflict(t *testing.T) {
	var (
		gatewayMu    sync.Mutex
		gatewayCalls int
	)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayMu.Lock()
		gatewayCalls++
		gatewayMu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/admin/accounts/7/schedulable" {
			t.Errorf("unexpected gateway request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
	}))
	defer gateway.Close()

	first := newTask(t, "write-first", contracts.ConnectorTaskGatewaySchedulableSet, contracts.ConnectorGatewaySchedulableSetInput{
		AccountID: "7", Schedulable: false,
	})
	first.IdempotencyKey = "set-7-disabled"
	replay := first
	replay.ID = "write-replay"
	replay.LeaseNonce = "lease-write-replay"
	conflict := newTask(t, "write-conflict", contracts.ConnectorTaskGatewaySchedulableSet, contracts.ConnectorGatewaySchedulableSetInput{
		AccountID: "8", Schedulable: false,
	})
	conflict.IdempotencyKey = first.IdempotencyKey

	core := newCoreStub(t, [][]contracts.ConnectorTask{{first}, {replay}, {conflict}})
	conn := newSub2APIConnector(t, core.server.URL, gateway.URL)
	for poll := 1; poll <= 3; poll++ {
		if err := conn.PollOnce(t.Context()); err != nil {
			t.Fatalf("poll %d: %v", poll, err)
		}
	}

	completed := core.completionMap()
	if !completed[first.ID].Success || !completed[replay.ID].Success {
		t.Fatalf("idempotent writes were not successful: %+v", completed)
	}
	if got := completed[conflict.ID]; got.Success || got.Error.Code != "idempotency_conflict" || got.Error.Retryable {
		t.Fatalf("conflicting idempotency key completion = %+v", got)
	}
	gatewayMu.Lock()
	gotGatewayCalls := gatewayCalls
	gatewayMu.Unlock()
	if gotGatewayCalls != 1 {
		t.Fatalf("gateway write calls = %d, want 1", gotGatewayCalls)
	}
}

func TestWriteTaskNewLogicalIntentRunsAgainAfterInterveningChange(t *testing.T) {
	var (
		gatewayMu   sync.Mutex
		schedulable []bool
	)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/admin/accounts/7/schedulable" {
			t.Errorf("unexpected gateway request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body struct {
			Schedulable bool `json:"schedulable"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode gateway request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		gatewayMu.Lock()
		schedulable = append(schedulable, body.Schedulable)
		gatewayMu.Unlock()
		_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
	}))
	defer gateway.Close()

	newSetTask := func(id string, enabled bool) contracts.ConnectorTask {
		task := newTask(t, id, contracts.ConnectorTaskGatewaySchedulableSet, contracts.ConnectorGatewaySchedulableSetInput{
			AccountID: "7", Schedulable: enabled,
		})
		if enabled {
			task.IdempotencyKey = "set-7-enabled"
		} else {
			task.IdempotencyKey = "set-7-disabled"
		}
		return task
	}
	firstDisable := newSetTask("disable-first", false)
	enable := newSetTask("enable", true)
	secondDisable := newSetTask("disable-second", false)
	secondDisable.IdempotencyKey = "set-7-disabled-again"
	core := newCoreStub(t, [][]contracts.ConnectorTask{{firstDisable}, {enable}, {secondDisable}})
	conn := newSub2APIConnector(t, core.server.URL, gateway.URL)
	for poll := 1; poll <= 3; poll++ {
		if err := conn.PollOnce(t.Context()); err != nil {
			t.Fatalf("poll %d: %v", poll, err)
		}
	}

	gatewayMu.Lock()
	got := append([]bool(nil), schedulable...)
	gatewayMu.Unlock()
	want := []bool{false, true, false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gateway schedulable writes = %v, want %v", got, want)
	}
}

func TestUnknownTaskTypeIsRejectedBeforeGateway(t *testing.T) {
	var (
		gatewayMu    sync.Mutex
		gatewayCalls int
	)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayMu.Lock()
		gatewayCalls++
		gatewayMu.Unlock()
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer gateway.Close()

	task := newTask(t, "unsupported", contracts.ConnectorTaskType("gateway.account.provision"), map[string]any{
		"spec": map[string]any{"channel_id": "channel-1"},
	})
	core := newCoreStub(t, [][]contracts.ConnectorTask{{task}})
	conn := newSub2APIConnector(t, core.server.URL, gateway.URL)
	if err := conn.PollOnce(t.Context()); err != nil {
		t.Fatalf("poll once: %v", err)
	}

	completed := core.completionMap()
	completion, ok := completed[task.ID]
	if !ok {
		t.Fatalf("task %s was not completed", task.ID)
	}
	if completion.Success || completion.Error.Code != "task_type_unsupported" || completion.Error.Retryable {
		t.Fatalf("task %s completion = %+v", task.ID, completion)
	}
	gatewayMu.Lock()
	gotGatewayCalls := gatewayCalls
	gatewayMu.Unlock()
	if gotGatewayCalls != 0 {
		t.Fatalf("unsupported binding tasks reached gateway %d times", gotGatewayCalls)
	}
}

func TestValidateRequiresConnectorCredentialAndIdentity(t *testing.T) {
	valid := Config{
		CoreURL:     "http://core.local",
		TokenFile:   "connector.token",
		ConnectorID: testConnectorID,
		InstanceID:  testInstanceID,
		Version:     "0.1.0-test",
	}
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "credential", mutate: func(cfg *Config) { cfg.TokenFile = "" }, want: "token"},
		{name: "connector id", mutate: func(cfg *Config) { cfg.ConnectorID = "" }, want: "connector id"},
		{name: "instance id", mutate: func(cfg *Config) { cfg.InstanceID = "" }, want: "instance id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			err := New(cfg).Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tt.want)
			}
		})
	}
	if err := New(valid).Validate(); err != nil {
		t.Fatalf("valid connector config: %v", err)
	}
}

type recordedCompletion struct {
	taskID  string
	request contracts.ConnectorTaskCompleteRequest
}

type coreStub struct {
	server      *httptest.Server
	mu          sync.Mutex
	batches     [][]contracts.ConnectorTask
	leases      []contracts.ConnectorTaskLeaseRequest
	completions []recordedCompletion
}

func newCoreStub(t *testing.T, batches [][]contracts.ConnectorTask) *coreStub {
	t.Helper()
	stub := &coreStub{batches: batches}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testConnectorToken {
			t.Errorf("core authorization = %q", got)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/connectors/tasks/lease":
			var request contracts.ConnectorTaskLeaseRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode lease request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			stub.mu.Lock()
			batchIndex := len(stub.leases)
			stub.leases = append(stub.leases, request)
			var tasks []contracts.ConnectorTask
			if batchIndex < len(stub.batches) {
				tasks = append([]contracts.ConnectorTask(nil), stub.batches[batchIndex]...)
			}
			stub.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(contracts.ConnectorTaskLeaseResponse{
				ProtocolVersion: contracts.ConnectorProtocolVersion,
				Tasks:           tasks,
			})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/connectors/tasks/") && strings.HasSuffix(r.URL.Path, "/complete"):
			taskID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/connectors/tasks/"), "/complete")
			if taskID == "" || strings.Contains(taskID, "/") {
				t.Errorf("invalid completion path %q", r.URL.Path)
				http.Error(w, "bad task id", http.StatusBadRequest)
				return
			}
			var request contracts.ConnectorTaskCompleteRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode completion request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			stub.mu.Lock()
			stub.completions = append(stub.completions, recordedCompletion{taskID: taskID, request: request})
			stub.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected core request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *coreStub) leaseRequests() []contracts.ConnectorTaskLeaseRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]contracts.ConnectorTaskLeaseRequest(nil), s.leases...)
}

func (s *coreStub) completionMap() map[string]contracts.ConnectorTaskCompleteRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]contracts.ConnectorTaskCompleteRequest, len(s.completions))
	for _, completion := range s.completions {
		out[completion.taskID] = completion.request
	}
	return out
}

func (s *coreStub) completionsFor(taskID string) []contracts.ConnectorTaskCompleteRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]contracts.ConnectorTaskCompleteRequest, 0)
	for _, completion := range s.completions {
		if completion.taskID == taskID {
			out = append(out, completion.request)
		}
	}
	return out
}

func newSub2APIConnector(t *testing.T, coreURL, gatewayURL string) *Connector {
	t.Helper()
	store := NewLocalConfigStore(t.TempDir())
	if err := store.Save(GatewayLocalConfig{
		GatewayKind: "sub2api",
		GatewayURL:  gatewayURL,
		Auth:        GatewayAuthXAPIKey,
		Credentials: GatewayLocalCredentials{XAPIKey: "local-admin-key"},
	}); err != nil {
		t.Fatalf("save local gateway config: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "connector.token")
	if err := os.WriteFile(tokenFile, []byte(testConnectorToken+"\n"), 0600); err != nil {
		t.Fatalf("write connector token: %v", err)
	}
	return New(Config{
		CoreURL:     coreURL,
		TokenFile:   tokenFile,
		ConnectorID: testConnectorID,
		InstanceID:  testInstanceID,
		ConfigStore: store,
		Version:     "0.1.0-test",
	})
}

func newTask(t *testing.T, id string, taskType contracts.ConnectorTaskType, input any) contracts.ConnectorTask {
	t.Helper()
	var raw json.RawMessage
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatalf("marshal task input: %v", err)
		}
		raw = encoded
	}
	task := contracts.ConnectorTask{
		ID:            id,
		InstanceID:    testInstanceID,
		ConnectorID:   testConnectorID,
		LeaseNonce:    "lease-" + id,
		Type:          taskType,
		SchemaVersion: taskSchemaVersion,
		RiskLevel:     taskType.RiskLevel(),
		Input:         raw,
	}
	if isWriteTask(taskType) {
		task.IdempotencyKey = "test-write:" + id
	}
	return task
}
