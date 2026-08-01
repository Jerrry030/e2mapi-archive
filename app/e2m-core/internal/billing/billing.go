// Package billing computes per-user hosting statements (W4). Pricing model
// matches the side-car architecture: fixed monthly fee per managed instance +
// per-disposition fee; usage is reference-only and never billed (the data path
// is owner-controlled, so gateway-reported usage is not a trustworthy basis).
package billing

import (
	"context"
	"fmt"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// Pricing is expressed in integer cents to avoid float drift.
type Pricing struct {
	InstanceMonthlyCents int64  // per managed instance per month
	DispositionCents     int64  // per accepted account disposition
	Currency             string // e.g. "CNY"
}

func DefaultPricing() Pricing {
	return Pricing{InstanceMonthlyCents: 19900, DispositionCents: 100, Currency: "CNY"}
}

type Calculator struct {
	store   store.Store
	pricing Pricing
	now     func() time.Time
}

func New(st store.Store, p Pricing) *Calculator {
	if p.Currency == "" {
		p.Currency = "CNY"
	}
	return &Calculator{store: st, pricing: p, now: time.Now}
}

// ParsePeriod turns "2026-07" into its UTC month bounds.
func ParsePeriod(period string) (time.Time, time.Time, error) {
	t, err := time.Parse("2006-01", period)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("billing: period must be YYYY-MM: %w", err)
	}
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0), nil
}

// Statement computes the bill for one user and month.
func (c *Calculator) Statement(ctx context.Context, userID int64, period string) (contracts.BillingStatement, error) {
	start, end, err := ParsePeriod(period)
	if err != nil {
		return contracts.BillingStatement{}, err
	}

	userEmail := ""
	if u, err := c.store.GetUser(ctx, userID); err == nil {
		userEmail = u.Email
	}

	// Axis 1: managed instances that existed during the period.
	instances, err := c.store.ListInstances(ctx, userID)
	if err != nil {
		return contracts.BillingStatement{}, err
	}
	var instCount int64
	for _, in := range instances {
		if in.CreatedAt.Before(end) {
			instCount++
		}
	}

	// Axis 2: accepted account dispositions (manual + auto switches) in period.
	audits, err := c.store.ListAudits(ctx, userID)
	if err != nil {
		return contracts.BillingStatement{}, err
	}
	var dispCount int64
	for _, a := range audits {
		if a.TargetType == "account" && a.Result == "accepted" &&
			strings.HasPrefix(a.Action, "account.") &&
			!a.CreatedAt.Before(start) && a.CreatedAt.Before(end) {
			dispCount++
		}
	}

	lines := []contracts.BillingLine{
		{
			Item:      "实例托管费",
			Quantity:  instCount,
			UnitPrice: cents(c.pricing.InstanceMonthlyCents),
			Amount:    cents(instCount * c.pricing.InstanceMonthlyCents),
			Note:      "按托管实例数 × 月费",
		},
		{
			Item:      "处置费",
			Quantity:  dispCount,
			UnitPrice: cents(c.pricing.DispositionCents),
			Amount:    cents(dispCount * c.pricing.DispositionCents),
			Note:      "账号停用/启用等处置动作（手动+自动），按次",
		},
	}
	total := instCount*c.pricing.InstanceMonthlyCents + dispCount*c.pricing.DispositionCents

	return contracts.BillingStatement{
		UserID:           userID,
		UserEmail:        userEmail,
		Period:           period,
		PeriodStart:      start,
		PeriodEnd:        end,
		InstanceCount:    instCount,
		DispositionCount: dispCount,
		Lines:            lines,
		Total:            cents(total),
		Currency:         c.pricing.Currency,
		GeneratedAt:      c.now().UTC(),
	}, nil
}

// cents renders integer cents as a decimal string ("19900" -> "199.00").
func cents(v int64) string {
	neg := ""
	if v < 0 {
		neg, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%02d", neg, v/100, v%100)
}
