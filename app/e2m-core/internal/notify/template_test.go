package notify

import (
	"testing"

	"e2m.local/contracts"
)

func TestRenderTemplateDefault(t *testing.T) {
	got := RenderTemplate("", Event{Title: "T", Text: "B"})
	if got != "T\nB" {
		t.Fatalf("default render: %q", got)
	}
}

func TestRenderTemplatePlaceholders(t *testing.T) {
	ev := Event{
		Title:      "余额预警",
		Text:       "详情",
		UserID:     101,
		InstanceID: "inst-9",
		RiskLevel:  contracts.RiskLevelL1,
		Fields:     map[string]string{"instanceName": "生产站", "balance": "3.50"},
	}
	got := RenderTemplate("[{riskLevel}] {instanceName} 余额 {balance}（{title}）", ev)
	want := "[L1] 生产站 余额 3.50（余额预警）"
	if got != want {
		t.Fatalf("render: got %q want %q", got, want)
	}
}

func TestRenderTemplateResultPlaceholder(t *testing.T) {
	got := RenderTemplate("{eventLevel}/{result}", Event{EventLevel: contracts.EventLevelNotice, Result: "accepted"})
	if got != "L1/accepted" {
		t.Fatalf("result placeholder: got %q", got)
	}
}

func TestRenderTemplateUserIDPlaceholder(t *testing.T) {
	got := RenderTemplate("{userId}", Event{UserID: 101})
	if got != "101" {
		t.Fatalf("userId placeholder: got %q", got)
	}
}

func TestRenderTemplateUnknownPlaceholderKept(t *testing.T) {
	got := RenderTemplate("{title} {nope}", Event{Title: "X"})
	if got != "X {nope}" {
		t.Fatalf("unknown placeholder must stay visible, got %q", got)
	}
}

func TestEventMessage(t *testing.T) {
	if (Event{Title: "A", Text: "B"}).Message() != "A\nB" {
		t.Fatal("title+text")
	}
	if (Event{Text: "B"}).Message() != "B" {
		t.Fatal("text only")
	}
	if (Event{Title: "A"}).Message() != "A" {
		t.Fatal("title only")
	}
}
