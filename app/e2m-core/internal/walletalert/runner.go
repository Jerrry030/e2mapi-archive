// Package walletalert warns downstream users whose platform wallet balance
// fell under the configured threshold. Alerts are edge-triggered: one alert
// when a wallet crosses below, and re-armed only after it recovers above the
// threshold, so a persistently low wallet does not spam the channels.
package walletalert

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/store"
)

const DefaultInterval = 5 * time.Minute

type Runner struct {
	store           store.Store
	router          *notify.Router
	currency        string
	thresholdMicros int64
	interval        time.Duration

	mu    sync.Mutex
	below map[int64]bool
}

func New(st store.Store, router *notify.Router, currency string, thresholdMicros int64, intervals ...time.Duration) *Runner {
	interval := DefaultInterval
	if len(intervals) > 0 && intervals[0] > 0 {
		interval = intervals[0]
	}
	return &Runner{
		store: st, router: router, currency: currency,
		thresholdMicros: thresholdMicros, interval: interval, below: map[int64]bool{},
	}
}

func (r *Runner) Run(ctx context.Context) {
	r.RunOnce(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.RunOnce(ctx)
		}
	}
}

func (r *Runner) RunOnce(ctx context.Context) {
	if r == nil || r.store == nil || r.thresholdMicros <= 0 {
		return
	}
	wallets, err := r.store.ListWalletsBelow(ctx, r.currency, r.thresholdMicros)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("wallet-alert: list wallets failed: %v", err)
		}
		return
	}
	current := make(map[int64]bool, len(wallets))
	for _, wallet := range wallets {
		current[wallet.UserID] = true
	}
	r.mu.Lock()
	fresh := make([]contracts.Wallet, 0, len(wallets))
	for _, wallet := range wallets {
		if !r.below[wallet.UserID] {
			fresh = append(fresh, wallet)
		}
	}
	r.below = current
	r.mu.Unlock()

	for _, wallet := range fresh {
		text := fmt.Sprintf("平台钱包余额 %.2f %s，低于阈值 %.2f，请及时充值以免请求被拒绝",
			float64(wallet.AvailableMicros)/1_000_000, wallet.Currency, float64(r.thresholdMicros)/1_000_000)
		_, _ = r.store.AppendAudit(ctx, contracts.OperationAudit{
			UserID: wallet.UserID, ActorType: "system", ActorID: "wallet-alert",
			Action: "platform.wallet.balance_low", RiskLevel: contracts.RiskLevelL0,
			EventLevel: contracts.EventLevelWarning, TargetType: "wallet",
			TargetID: strconv.FormatInt(wallet.UserID, 10) + ":" + wallet.Currency,
			Result:   "detected", ErrorMessage: text,
		})
		if r.router == nil {
			continue
		}
		routes, routesErr := r.store.ListNotificationRoutes(ctx, wallet.UserID)
		if routesErr != nil {
			continue
		}
		r.router.DispatchAll(ctx, notify.Event{
			UserID: wallet.UserID, EventLevel: contracts.EventLevelWarning,
			RiskLevel: contracts.RiskLevelL0, Result: "detected",
			Title: "💰 平台余额预警", Text: text,
		}, routes)
	}
}
