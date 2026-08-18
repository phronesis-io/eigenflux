package consolev2

import (
	"testing"
	"time"
)

func TestCalculateCardCompletionUsesEditableRegistry(t *testing.T) {
	publicCard := `{
		"agent_name":"Atlas",
		"agent_description":"Research assistant",
		"human_description":"Works on agent infrastructure",
		"working_languages":["zh-CN","en"],
		"seeking":[],
		"offering":["research"]
	}`
	privateCard := `{
		"geo":"Singapore",
		"timezone":"Asia/Singapore",
		"current_focus":["trusted collaboration"],
		"demands":[],
		"agent_status":[],
		"human_status":[],
		"interests_negative":[]
	}`
	completed, total, percent, err := calculateCardCompletion(publicCard, privateCard)
	if err != nil {
		t.Fatal(err)
	}
	if completed != 8 || total != 13 || percent != 61 {
		t.Fatalf("unexpected completion completed=%d total=%d percent=%d", completed, total, percent)
	}
}

func TestCalculateCardCompletionRejectsInvalidProjection(t *testing.T) {
	if _, _, _, err := calculateCardCompletion(`{"agent_name":`, `{}`); err == nil {
		t.Fatal("invalid Card projection should fail closed")
	}
}

func TestTodayStartUsesAgentCardTimezone(t *testing.T) {
	now := time.Date(2026, 8, 18, 17, 30, 0, 0, time.UTC)
	want := time.Date(2026, 8, 18, 16, 0, 0, 0, time.UTC).UnixMilli() // midnight in Asia/Singapore
	if got := todayStartFromPrivateCard(`{"timezone":"Asia/Singapore"}`, now); got != want {
		t.Fatalf("today start=%d want=%d", got, want)
	}
}

func TestTodayStartFallsBackToUTC(t *testing.T) {
	now := time.Date(2026, 8, 18, 17, 30, 0, 0, time.UTC)
	want := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC).UnixMilli()
	if got := todayStartFromPrivateCard(`{"timezone":"not/a-zone"}`, now); got != want {
		t.Fatalf("today start=%d want=%d", got, want)
	}
}
