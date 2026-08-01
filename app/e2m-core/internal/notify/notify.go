// Package notify delivers operational alerts through route-selected Feishu,
// QQ OneBot, and generic webhook channels. It is deliberately thin: a Notifier
// interface plus channel implementations, routed by RiskLevel.
package notify

import (
	"context"

	"e2m.local/contracts"
)

// Event is one alert to deliver.
type Event struct {
	UserID     int64
	InstanceID string
	// EventLevel controls delivery urgency; RiskLevel describes operation sensitivity.
	EventLevel contracts.EventLevel
	RiskLevel  contracts.RiskLevel
	// Result enables outcome-aware fallback for legacy producers that have not
	// yet assigned EventLevel explicitly.
	Result string
	Title  string
	Text   string
	// Fields feed route template placeholders ({instanceName}, {accountName},
	// {balance}...) beyond the always-available {title}/{text}/{eventLevel}/{riskLevel}.
	Fields map[string]string
}

// Message renders the delivered text: "Title\nText", omitting either side when
// empty (a route template collapses everything into Text).
func (ev Event) Message() string {
	switch {
	case ev.Title == "":
		return ev.Text
	case ev.Text == "":
		return ev.Title
	default:
		return ev.Title + "\n" + ev.Text
	}
}

// Notifier sends an event to one channel. Implementations must not block long;
// callers treat errors as non-fatal (best-effort delivery).
type Notifier interface {
	Channel() contracts.NotificationChannel
	Send(ctx context.Context, ev Event) error
}

// riskRank orders RiskLevel so route MinRiskLevel gating is a simple comparison.
func riskRank(r contracts.RiskLevel) int {
	switch r {
	case contracts.RiskLevelL0:
		return 0
	case contracts.RiskLevelL1:
		return 1
	case contracts.RiskLevelL2:
		return 2
	case contracts.RiskLevelL3:
		return 3
	default:
		return 0
	}
}

// MeetsMin reports whether an operation risk is at or above the legacy route minimum.
func MeetsMin(ev contracts.RiskLevel, min contracts.RiskLevel) bool {
	return riskRank(ev) >= riskRank(min)
}

func eventRank(level contracts.EventLevel) int {
	switch level {
	case contracts.EventLevelInfo:
		return 0
	case contracts.EventLevelNotice:
		return 1
	case contracts.EventLevelWarning:
		return 2
	case contracts.EventLevelCritical:
		return 3
	default:
		return 0
	}
}

// MeetsMinEvent reports whether an outcome is severe enough for a route.
func MeetsMinEvent(event, minimum contracts.EventLevel) bool {
	return eventRank(event) >= eventRank(minimum)
}
