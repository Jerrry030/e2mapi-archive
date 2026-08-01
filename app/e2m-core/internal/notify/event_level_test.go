package notify

import (
	"context"
	"sync/atomic"
	"testing"

	"e2m.local/contracts"
)

func TestRouterFiltersOnEventLevelInsteadOfOperationRisk(t *testing.T) {
	var sent int32
	router := NewRouter(&fakeNotifier{
		ch: contracts.NotificationChannelFeishu, sent: &sent,
	}, nil)
	route := contracts.NotificationRoute{
		Enabled: true, Channel: contracts.NotificationChannelFeishu,
		TargetRef:    "system:feishu",
		MinRiskLevel: contracts.RiskLevelL0, MinEventLevel: contracts.EventLevelWarning,
	}

	// A high-risk operation that succeeded is only a notice and must not cross
	// a WARNING notification threshold.
	router.Dispatch(context.Background(), Event{
		RiskLevel: contracts.RiskLevelL3, EventLevel: contracts.EventLevelNotice,
	}, route)
	if got := atomic.LoadInt32(&sent); got != 0 {
		t.Fatalf("notice from a high-risk operation was delivered: sent=%d", got)
	}

	// A low-risk read that failed is a warning and must be delivered even though
	// the operation's authorization risk is only L0.
	router.Dispatch(context.Background(), Event{
		RiskLevel: contracts.RiskLevelL0, EventLevel: contracts.EventLevelWarning,
	}, route)
	if got := atomic.LoadInt32(&sent); got != 1 {
		t.Fatalf("warning from a low-risk operation was filtered: sent=%d", got)
	}
}

func TestRouterFallsBackForLegacyEventsAndRoutes(t *testing.T) {
	var sent int32
	router := NewRouter(&fakeNotifier{
		ch: contracts.NotificationChannelFeishu, sent: &sent,
	}, nil)
	// Neither the pre-event-level route nor the producer supplies the new field.
	// The router must continue interpreting the legacy risk scale.
	route := contracts.NotificationRoute{
		Enabled: true, Channel: contracts.NotificationChannelFeishu,
		TargetRef:    "system:feishu",
		MinRiskLevel: contracts.RiskLevelL2,
	}
	router.Dispatch(context.Background(), Event{RiskLevel: contracts.RiskLevelL1}, route)
	router.Dispatch(context.Background(), Event{RiskLevel: contracts.RiskLevelL2}, route)
	if got := atomic.LoadInt32(&sent); got != 1 {
		t.Fatalf("legacy event/route fallback sent=%d, want 1", got)
	}
}
