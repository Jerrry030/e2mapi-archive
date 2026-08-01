package contracts

import "testing"

func TestDefaultEventLevelSeparatesOutcomeUrgencyFromOperationRisk(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		risk   RiskLevel
		result string
		want   EventLevel
	}{
		{name: "successful sensitive operation is a notice", risk: RiskLevelL3, result: "accepted", want: EventLevelNotice},
		{name: "failed read is a warning", risk: RiskLevelL0, result: "failed", want: EventLevelWarning},
		{name: "running sensitive operation is informational", risk: RiskLevelL3, result: "running", want: EventLevelInfo},
		{name: "retrying read needs attention", risk: RiskLevelL0, result: "retrying", want: EventLevelNotice},
		{name: "successful read is informational", risk: RiskLevelL0, result: "success", want: EventLevelInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := DefaultEventLevel(tt.risk, tt.result); got != tt.want {
				t.Fatalf("DefaultEventLevel(%q, %q) = %q, want %q", tt.risk, tt.result, got, tt.want)
			}
		})
	}
}

func TestEffectiveEventLevelSupportsLegacyAuditsAndExplicitOverrides(t *testing.T) {
	t.Parallel()
	legacy := OperationAudit{RiskLevel: RiskLevelL2, Result: "accepted"}
	if got := legacy.EffectiveEventLevel(); got != EventLevelNotice {
		t.Fatalf("legacy audit effective event level = %q, want %q", got, EventLevelNotice)
	}

	explicit := legacy
	explicit.EventLevel = EventLevelCritical
	if got := explicit.EffectiveEventLevel(); got != EventLevelCritical {
		t.Fatalf("explicit audit event level = %q, want %q", got, EventLevelCritical)
	}
	if explicit.RiskLevel != RiskLevelL2 {
		t.Fatalf("effective event lookup changed operation risk to %q", explicit.RiskLevel)
	}
}

func TestEffectiveMinEventLevelSupportsLegacyRoutesAndExplicitThresholds(t *testing.T) {
	t.Parallel()
	legacy := NotificationRoute{MinRiskLevel: RiskLevelL1}
	if got := legacy.EffectiveMinEventLevel(); got != EventLevelNotice {
		t.Fatalf("legacy route threshold = %q, want %q", got, EventLevelNotice)
	}

	explicit := legacy
	explicit.MinEventLevel = EventLevelWarning
	if got := explicit.EffectiveMinEventLevel(); got != EventLevelWarning {
		t.Fatalf("explicit route threshold = %q, want %q", got, EventLevelWarning)
	}
}
