package notify

import (
	"context"
	"fmt"
	"log"
	"time"

	"e2m.local/contracts"
)

type DeliveryWorkerStore interface {
	ClaimNotificationDelivery(context.Context, string, time.Duration) (contracts.NotificationDelivery, bool, error)
	CompleteNotificationDelivery(context.Context, string, string, int64, bool, string, string, time.Time) (contracts.NotificationDelivery, error)
}

type Worker struct {
	store    DeliveryWorkerStore
	router   *Router
	workerID string
	interval time.Duration
	lease    time.Duration
	timeout  time.Duration
}

func NewWorker(st DeliveryWorkerStore, router *Router, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = time.Second
	}
	return &Worker{store: st, router: router, workerID: fmt.Sprintf("notify-%d", time.Now().UnixNano()), interval: interval, lease: 30 * time.Second, timeout: 12 * time.Second}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		w.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) drain(ctx context.Context) {
	for {
		delivery, claimed, err := w.store.ClaimNotificationDelivery(ctx, w.workerID, w.lease)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("notify worker: claim failed: %v", err)
			}
			return
		}
		if !claimed {
			return
		}
		w.process(ctx, delivery)
	}
}

func (w *Worker) process(ctx context.Context, delivery contracts.NotificationDelivery) {
	sendCtx, cancel := context.WithTimeout(ctx, w.timeout)
	err := w.router.SendDelivery(sendCtx, delivery)
	cancel()
	if err == nil {
		if _, completeErr := w.store.CompleteNotificationDelivery(ctx, delivery.ID, w.workerID, delivery.LeaseVersion, true, "", "", time.Time{}); completeErr != nil && ctx.Err() == nil {
			log.Printf("notify worker: record success failed: %v", completeErr)
		}
		return
	}
	code, message, retryable := SafeDeliveryError(err)
	next := time.Time{}
	if retryable && delivery.Attempts < delivery.MaxAttempts {
		next = time.Now().UTC().Add(retryDelay(delivery.Attempts))
	}
	if _, completeErr := w.store.CompleteNotificationDelivery(ctx, delivery.ID, w.workerID, delivery.LeaseVersion, false, code, message, next); completeErr != nil && ctx.Err() == nil {
		log.Printf("notify worker: record failure failed: %v", completeErr)
	}
}

func retryDelay(attempt int) time.Duration {
	switch attempt {
	case 0, 1:
		return 10 * time.Second
	case 2:
		return 30 * time.Second
	case 3:
		return 2 * time.Minute
	default:
		return 10 * time.Minute
	}
}
