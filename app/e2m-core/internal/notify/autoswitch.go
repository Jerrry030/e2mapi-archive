package notify

import (
	"fmt"
	"strings"
)

// This file builds the health-driven auto-switch notification bodies (Phase 5).
// The design goal (docs/development/health-driven-auto-switching.md) is that a
// notification explains *why* a switch happened, *what* changed, and *the
// current state* -- not the low-level gateway operations. The three shapes are
// the switch-executed alert, the completion review, and the rollback alert;
// proposed/skipped/failed reuse the same builder with a different headline.

// AutoSwitchNotice is the structured input to the auto-switch templates. It is a
// plain data holder so the notify package stays decoupled from the orchestrator
// and the contracts package; the caller fills whatever it knows and leaves the
// rest zero. Metric fields are pointers so "unknown" is distinguishable from a
// real zero measurement.
type AutoSwitchNotice struct {
	Status       string // human status headline suffix, e.g. "已执行"
	PlanID       string
	InstanceID   string
	InstanceName string // friendly instance name when known, else InstanceID
	StrategyName string // human strategy name, e.g. "稳定优先"
	FromChannel  string // failing channel (drained)
	ToChannel    string // backup channel (promoted)

	TriggerReason   string
	RiskReason      string
	ObservationNote string

	// Optional before/after quality metrics. From* describe the failing channel
	// at decision time; To* describe the backup. Nil means "not measured".
	FromSuccessRate *float64
	ToSuccessRate   *float64
	FromTTFTP95     *float64
	ToTTFTP95       *float64
	FromDurationP95 *float64
	ToDurationP95   *float64
}

// dashOr returns v or "-" when empty, so a template never renders a blank field.
func dashOr(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

// pct renders an optional 0..1 success rate as a percentage, or "-" when nil.
func pct(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", *v*100)
}

// ms renders an optional millisecond latency, or "-" when nil.
func ms(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.0fms", *v)
}

// AutoSwitchTitle is the headline for an auto-switch notification.
func AutoSwitchTitle(status string) string {
	return "【E2M 上游自动切换】" + status
}

// BuildAutoSwitchText renders the notification body from a notice. It always
// includes the site, strategy, from/to channels, and the trigger/observation
// reason; it appends a before/after metrics block whenever any metric is known.
func BuildAutoSwitchText(n AutoSwitchNotice) string {
	var b strings.Builder
	fmt.Fprintf(&b, "实例：%s\n", dashOr(firstNonEmptyStr(n.InstanceName, n.InstanceID, n.PlanID)))
	fmt.Fprintf(&b, "策略：%s\n", dashOr(n.StrategyName))
	fmt.Fprintf(&b, "动作：主渠道 %s -> 备用渠道 %s\n", dashOr(n.FromChannel), dashOr(n.ToChannel))
	if reason := firstNonEmptyStr(n.TriggerReason, n.RiskReason); reason != "" {
		fmt.Fprintf(&b, "原因：%s\n", reason)
	}
	if hasMetrics(n) {
		fmt.Fprintf(&b, "成功率：%s -> %s\n", pct(n.FromSuccessRate), pct(n.ToSuccessRate))
		fmt.Fprintf(&b, "p95首字：%s -> %s\n", ms(n.FromTTFTP95), ms(n.ToTTFTP95))
		fmt.Fprintf(&b, "p95总耗时：%s -> %s\n", ms(n.FromDurationP95), ms(n.ToDurationP95))
	}
	if strings.TrimSpace(n.ObservationNote) != "" {
		fmt.Fprintf(&b, "状态：%s\n", n.ObservationNote)
	}
	return strings.TrimRight(b.String(), "\n")
}

func hasMetrics(n AutoSwitchNotice) bool {
	return n.FromSuccessRate != nil || n.ToSuccessRate != nil ||
		n.FromTTFTP95 != nil || n.ToTTFTP95 != nil ||
		n.FromDurationP95 != nil || n.ToDurationP95 != nil
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
