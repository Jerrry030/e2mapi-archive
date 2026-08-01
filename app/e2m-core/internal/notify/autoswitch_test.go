package notify

import (
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }

// TestBuildAutoSwitchTextWithMetrics: the body carries instance/strategy/action and
// a before/after metrics block when metrics are present.
func TestBuildAutoSwitchTextWithMetrics(t *testing.T) {
	got := BuildAutoSwitchText(AutoSwitchNotice{
		Status:          "已执行",
		InstanceName:    "生产实例",
		StrategyName:    "稳定优先",
		FromChannel:     "主渠道A",
		ToChannel:       "备用B",
		TriggerReason:   "主渠道成功率骤降",
		FromSuccessRate: f64(0.914),
		ToSuccessRate:   f64(0.991),
		FromTTFTP95:     f64(4200),
		ToTTFTP95:       f64(1300),
		ObservationNote: "正在灰度观察",
	})
	for _, want := range []string{"生产实例", "稳定优先", "主渠道A", "备用B", "91.4%", "99.1%", "4200ms", "1300ms", "正在灰度观察"} {
		if !strings.Contains(got, want) {
			t.Fatalf("body missing %q in:\n%s", want, got)
		}
	}
}

// TestBuildAutoSwitchTextNoMetrics: with no metrics, the metrics block is
// omitted (no stray "-> -" success line) but the core fields remain.
func TestBuildAutoSwitchTextNoMetrics(t *testing.T) {
	got := BuildAutoSwitchText(AutoSwitchNotice{
		Status:       "待审批",
		InstanceName: "实例X",
		StrategyName: "成本优先",
		FromChannel:  "A",
		ToChannel:    "B",
	})
	if strings.Contains(got, "成功率") {
		t.Fatalf("should not render a metrics block without metrics:\n%s", got)
	}
	if !strings.Contains(got, "实例X") || !strings.Contains(got, "成本优先") {
		t.Fatalf("core fields missing:\n%s", got)
	}
}

func TestAutoSwitchTitle(t *testing.T) {
	if got := AutoSwitchTitle("已执行"); got != "【E2M 上游自动切换】已执行" {
		t.Fatalf("title = %q", got)
	}
}
