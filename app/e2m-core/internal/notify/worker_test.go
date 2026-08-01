package notify

import (
	"context"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

func TestWorkerCompletesSuccessfulDelivery(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now().UTC())
	sender := &workerTestNotifier{channel: contracts.NotificationChannelFeishu}
	router := NewRouter(sender, nil)
	delivery := createWorkerTestDelivery(t, st, 3)
	worker := NewWorker(st, router, time.Hour)
	worker.workerID = "worker-success"

	worker.drain(ctx)

	got, err := st.GetNotificationDelivery(ctx, delivery.ID)
	if err != nil {
		t.Fatalf("get delivery: %v", err)
	}
	if got.Status != contracts.NotificationDeliverySucceeded || got.Attempts != 1 || got.SentAt == nil {
		t.Fatalf("unexpected success state: %+v", got)
	}
	if sender.Calls() != 1 {
		t.Fatalf("send calls=%d, want 1", sender.Calls())
	}
}

func TestWorkerSchedulesRetryForTemporaryFailure(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now().UTC())
	sender := &workerTestNotifier{
		channel: contracts.NotificationChannelFeishu,
		errors:  []error{Retryable("provider_busy", "渠道暂时不可用")},
	}
	router := NewRouter(sender, nil)
	delivery := createWorkerTestDelivery(t, st, 3)
	worker := NewWorker(st, router, time.Hour)
	worker.workerID = "worker-retry"

	before := time.Now().UTC()
	worker.drain(ctx)

	got, err := st.GetNotificationDelivery(ctx, delivery.ID)
	if err != nil {
		t.Fatalf("get delivery: %v", err)
	}
	if got.Status != contracts.NotificationDeliveryRetrying || got.Attempts != 1 {
		t.Fatalf("unexpected retry state: %+v", got)
	}
	if got.LastErrorCode != "provider_busy" || got.LastErrorMessage != "渠道暂时不可用" {
		t.Fatalf("temporary failure was not recorded safely: %+v", got)
	}
	if got.NextAttemptAt.Before(before.Add(9 * time.Second)) {
		t.Fatalf("retry was not delayed: next=%s before=%s", got.NextAttemptAt, before)
	}
	if sender.Calls() != 1 {
		t.Fatalf("send calls=%d, want 1", sender.Calls())
	}
}

func TestWorkerFailsPermanentErrorWithoutAutomaticRetry(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now().UTC())
	sender := &workerTestNotifier{
		channel: contracts.NotificationChannelFeishu,
		errors:  []error{Permanent("channel_unconfigured", "渠道未配置")},
	}
	router := NewRouter(sender, nil)
	delivery := createWorkerTestDelivery(t, st, 5)
	worker := NewWorker(st, router, time.Hour)
	worker.workerID = "worker-permanent"

	worker.drain(ctx)

	got, err := st.GetNotificationDelivery(ctx, delivery.ID)
	if err != nil {
		t.Fatalf("get delivery: %v", err)
	}
	if got.Status != contracts.NotificationDeliveryFailed || got.Attempts != 1 {
		t.Fatalf("permanent failure was retried: %+v", got)
	}
	if got.LastErrorCode != "channel_unconfigured" || got.LastErrorMessage != "渠道未配置" {
		t.Fatalf("permanent failure was not recorded safely: %+v", got)
	}
	if sender.Calls() != 1 {
		t.Fatalf("send calls=%d, want 1", sender.Calls())
	}
}

func TestRouterDispatchEnqueuesWithoutSendingSynchronously(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now().UTC())
	sender := &workerTestNotifier{channel: contracts.NotificationChannelFeishu}
	router := NewRouter(sender, nil)
	router.SetDeliveryStore(st)
	route := contracts.NotificationRoute{
		ID: "route-ops", UserID: 42, Name: "运营通知", Enabled: true,
		Channel: contracts.NotificationChannelFeishu, TargetRef: "system:feishu", MinEventLevel: contracts.EventLevelWarning,
	}

	router.Dispatch(ctx, Event{
		UserID: 42, EventLevel: contracts.EventLevelWarning, RiskLevel: contracts.RiskLevelL1,
		Result: "failed", Title: "库存不足", Text: "剩余 1 个", Fields: map[string]string{"available": "1"},
	}, route)

	if sender.Calls() != 0 {
		t.Fatalf("dispatch performed synchronous network I/O: calls=%d", sender.Calls())
	}
	deliveries, err := st.ListNotificationDeliveries(ctx, contracts.NotificationDeliveryFilter{UserID: 42})
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("queued deliveries=%+v err=%v", deliveries, err)
	}
	got := deliveries[0]
	if got.RouteID != route.ID || got.Kind != contracts.NotificationDeliveryKindEvent || got.Status != contracts.NotificationDeliveryPending {
		t.Fatalf("unexpected queued snapshot: %+v", got)
	}
	if got.Fields["available"] != "1" || got.Title != "库存不足" {
		t.Fatalf("event snapshot was incomplete: %+v", got)
	}
}

func createWorkerTestDelivery(t *testing.T, st *store.MemoryStore, maxAttempts int) contracts.NotificationDelivery {
	t.Helper()
	delivery, err := st.CreateNotificationDelivery(context.Background(), contracts.NotificationDelivery{
		UserID: 42, RouteID: "route-worker", RouteName: "工作通知",
		Channel: contracts.NotificationChannelFeishu, TargetRef: "system:feishu", Kind: contracts.NotificationDeliveryKindEvent,
		Status: contracts.NotificationDeliveryPending, Title: "测试", Text: "消息", MaxAttempts: maxAttempts,
	})
	if err != nil {
		t.Fatalf("create delivery: %v", err)
	}
	return delivery
}

type workerTestNotifier struct {
	mu      sync.Mutex
	channel contracts.NotificationChannel
	errors  []error
	calls   int
}

func (n *workerTestNotifier) Channel() contracts.NotificationChannel { return n.channel }

func (n *workerTestNotifier) Send(context.Context, Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls++
	if len(n.errors) == 0 {
		return nil
	}
	err := n.errors[0]
	n.errors = n.errors[1:]
	return err
}

func (n *workerTestNotifier) Calls() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.calls
}
