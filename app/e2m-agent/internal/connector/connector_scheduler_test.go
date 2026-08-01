package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPollOnceReturnsLeaseSchedulingMetadata(t *testing.T) {
	task := schedulingTestTask("task-1")
	completionCount := 0
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/connectors/tasks/lease":
			_ = json.NewEncoder(w).Encode(contracts.ConnectorTaskLeaseResponse{
				ProtocolVersion: contracts.ConnectorProtocolVersion,
				Tasks:           []contracts.ConnectorTask{task},
				NextPollAfter:   "17s",
			})
		case "/api/v1/connectors/tasks/task-1/complete":
			completionCount++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer core.Close()

	conn := newSchedulingTestConnector(core.URL)
	result, err := conn.pollOnce(t.Context())
	if err != nil {
		t.Fatalf("pollOnce() error = %v", err)
	}
	if result.taskCount != 1 || result.nextPollAfter != "17s" {
		t.Fatalf("pollOnce() result = %+v, want one task and 17s", result)
	}
	if completionCount != 1 {
		t.Fatalf("completion count = %d, want 1", completionCount)
	}
	if err := conn.PollOnce(t.Context()); err != nil {
		t.Fatalf("public PollOnce() compatibility call failed: %v", err)
	}
}

func TestNextPollDelay(t *testing.T) {
	conn := newSchedulingTestConnector("http://core.invalid")
	tests := []struct {
		name   string
		result pollResult
		want   time.Duration
	}{
		{name: "tasks use busy interval", result: pollResult{taskCount: 1, nextPollAfter: "12s"}, want: time.Second},
		{name: "core idle interval", result: pollResult{nextPollAfter: "12s"}, want: 12 * time.Second},
		{name: "trim core interval", result: pollResult{nextPollAfter: " 7s "}, want: 7 * time.Second},
		{name: "clamp short core interval", result: pollResult{nextPollAfter: "100ms"}, want: time.Second},
		{name: "clamp long core interval", result: pollResult{nextPollAfter: "2m"}, want: 30 * time.Second},
		{name: "missing core interval", result: pollResult{}, want: 5 * time.Second},
		{name: "invalid core interval", result: pollResult{nextPollAfter: "later"}, want: 5 * time.Second},
		{name: "nonpositive core interval", result: pollResult{nextPollAfter: "-1s"}, want: 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := conn.nextPollDelay(tt.result); got != tt.want {
				t.Fatalf("nextPollDelay(%+v) = %s, want %s", tt.result, got, tt.want)
			}
		})
	}
}

func TestFailureBackoffSequenceAndJitter(t *testing.T) {
	conn := newSchedulingTestConnector("http://core.invalid")
	var jitterLimits []time.Duration
	conn.jitter = func(limit time.Duration) time.Duration {
		jitterLimits = append(jitterLimits, limit)
		return 250 * time.Millisecond
	}

	var got []time.Duration
	for failure := 1; failure <= 7; failure++ {
		got = append(got, conn.failureBackoff(failure))
	}
	want := []time.Duration{
		2250 * time.Millisecond,
		5250 * time.Millisecond,
		10250 * time.Millisecond,
		20250 * time.Millisecond,
		30250 * time.Millisecond,
		30250 * time.Millisecond,
		30250 * time.Millisecond,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("failure backoff = %v, want %v", got, want)
	}
	for index, limit := range jitterLimits {
		if limit != time.Second {
			t.Fatalf("jitter limit %d = %s, want 1s", index, limit)
		}
	}
}

func TestRunUsesDynamicWaitsAndResetsBackoff(t *testing.T) {
	task := schedulingTestTask("task-1")
	var (
		mu         sync.Mutex
		leaseCalls int
	)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/connectors/tasks/task-1/complete" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path != "/api/v1/connectors/tasks/lease" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		leaseCalls++
		call := leaseCalls
		mu.Unlock()
		switch call {
		case 1:
			_ = json.NewEncoder(w).Encode(contracts.ConnectorTaskLeaseResponse{
				ProtocolVersion: contracts.ConnectorProtocolVersion,
				NextPollAfter:   "9s",
			})
		case 2:
			_ = json.NewEncoder(w).Encode(contracts.ConnectorTaskLeaseResponse{
				ProtocolVersion: contracts.ConnectorProtocolVersion,
				Tasks:           []contracts.ConnectorTask{task},
				NextPollAfter:   "9s",
			})
		case 3, 4, 6:
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		case 5:
			_ = json.NewEncoder(w).Encode(contracts.ConnectorTaskLeaseResponse{
				ProtocolVersion: contracts.ConnectorProtocolVersion,
				NextPollAfter:   "4s",
			})
		default:
			t.Errorf("unexpected lease call %d", call)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer core.Close()

	conn := newSchedulingTestConnector(core.URL)
	conn.jitter = func(time.Duration) time.Duration { return 100 * time.Millisecond }
	var waits []time.Duration
	conn.wait = func(_ context.Context, delay time.Duration) bool {
		waits = append(waits, delay)
		return len(waits) < 6
	}

	conn.Run(t.Context())

	want := []time.Duration{
		9 * time.Second,
		time.Second,
		2100 * time.Millisecond,
		5100 * time.Millisecond,
		4 * time.Second,
		2100 * time.Millisecond,
	}
	if !reflect.DeepEqual(waits, want) {
		t.Fatalf("Run() waits = %v, want %v", waits, want)
	}
}

func TestNewUsesFixedCoreTimeoutAndIdleDefault(t *testing.T) {
	conn := New(Config{})
	if conn.coreHTTP.Timeout != 15*time.Second {
		t.Fatalf("Core HTTP timeout = %s, want 15s", conn.coreHTTP.Timeout)
	}
	if got := conn.nextPollDelay(pollResult{}); got != 5*time.Second {
		t.Fatalf("idle poll interval = %s, want 5s", got)
	}
}

func newSchedulingTestConnector(coreURL string) *Connector {
	return New(Config{
		CoreURL:     coreURL,
		Token:       testConnectorToken,
		ConnectorID: testConnectorID,
		InstanceID:  testInstanceID,
		Version:     "0.1.0-test",
	})
}

func schedulingTestTask(id string) contracts.ConnectorTask {
	return contracts.ConnectorTask{
		ID:            id,
		InstanceID:    testInstanceID,
		ConnectorID:   testConnectorID,
		LeaseNonce:    "lease-" + id,
		Type:          contracts.ConnectorTaskType("test.unsupported"),
		SchemaVersion: taskSchemaVersion,
	}
}
