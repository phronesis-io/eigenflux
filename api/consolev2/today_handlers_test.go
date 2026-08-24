package consolev2

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTodayCountryCodeNormalizesKnownAndISOValues(t *testing.T) {
	tests := map[string]string{
		"CN": "CN", "China": "CN", "中国大陆": "CN",
		"SG": "SG", "Singapore": "SG", "DE": "DE",
		"": "", "ZZ": "", "not-a-country": "",
	}
	for input, want := range tests {
		if got := todayCountryCode(input); got != want {
			t.Fatalf("todayCountryCode(%q)=%q want=%q", input, got, want)
		}
	}
}

func TestTodayEncounterReturnsCountryCode(t *testing.T) {
	payload, err := json.Marshal(todayEncounter{PeerAgentID: 123, CountryCode: "SG"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"peer_agent_id":"123"`) || !strings.Contains(string(payload), `"country_code":"SG"`) {
		t.Fatalf("unexpected Today encounter payload: %s", payload)
	}
}

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
	if completed != 7 || total != 11 || percent != 64 {
		t.Fatalf("unexpected completion completed=%d total=%d percent=%d", completed, total, percent)
	}
}

func TestTodayStartAcceptsDisplayTimezone(t *testing.T) {
	now := time.Date(2026, 8, 18, 17, 30, 0, 0, time.UTC)
	want := time.Date(2026, 8, 18, 16, 0, 0, 0, time.UTC).UnixMilli()
	if got := todayStartFromPrivateCard(`{"timezone":"Asia/Singapore (UTC+8)"}`, now); got != want {
		t.Fatalf("today start=%d want=%d", got, want)
	}
}

func TestCalculateCardCompletionRejectsInvalidProjection(t *testing.T) {
	if _, _, _, err := calculateCardCompletion(`{"agent_name":`, `{}`); err == nil {
		t.Fatal("invalid Card projection should fail closed")
	}
}

func TestCalculateCurrentCardCompletionUsesFactData(t *testing.T) {
	profileData := `{
		"human_description":"Works on agent infrastructure",
		"working_languages":["zh-CN","en"],
		"seeking":[],
		"offering":["research"],
		"geo":"Singapore",
		"timezone":"Asia/Singapore",
		"agent_status":[],
		"human_status":[],
		"interests_negative":[]
	}`
	completed, total, percent, err := calculateCurrentCardCompletion("Atlas", "Research assistant", profileData)
	if err != nil {
		t.Fatal(err)
	}
	if completed != 7 || total != 11 || percent != 64 {
		t.Fatalf("unexpected current completion completed=%d total=%d percent=%d", completed, total, percent)
	}
}

func TestCalculateCurrentCardCompletionRejectsInvalidFactData(t *testing.T) {
	if _, _, _, err := calculateCurrentCardCompletion("Atlas", "Research assistant", `{"timezone":`); err == nil {
		t.Fatal("invalid fact data should fail closed")
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

func TestTodayObservationStateUsesDurableMilestones(t *testing.T) {
	tests := []struct {
		name                                      string
		hasData, scanDone, connected, runtimeSeen bool
		want                                      string
	}{
		{name: "waiting before first runtime", want: "waiting"},
		{name: "offline after a known runtime stops", runtimeSeen: true, want: "offline"},
		{name: "starting while first scan runs", connected: true, runtimeSeen: true, want: "starting"},
		{name: "confirmed empty only after scan completion", scanDone: true, connected: true, runtimeSeen: true, want: "complete_empty"},
		{name: "offline remains visible after a completed scan", scanDone: true, runtimeSeen: true, want: "offline"},
		{name: "module data wins over background state", hasData: true, scanDone: true, want: "data"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := todayObservationState(test.hasData, test.scanDone, test.connected, test.runtimeSeen); got != test.want {
				t.Fatalf("state=%q want=%q", got, test.want)
			}
		})
	}
}
