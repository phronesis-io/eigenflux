package agentcardapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
)

func TestRefreshCompleteOutcome(t *testing.T) {
	cases := []struct {
		header  string
		current int64
		outcome string
		ok      bool
	}{
		{"", 7, "", false},
		{"abc", 7, "", false},
		{"7", 7, "unchanged", true},
		{" 7 ", 7, "unchanged", true},
		{"6", 7, "stale", true},
	}
	for _, tc := range cases {
		outcome, ok := refreshCompleteOutcome(tc.header, tc.current)
		if outcome != tc.outcome || ok != tc.ok {
			t.Errorf("refreshCompleteOutcome(%q, %d) = %q, %v; want %q, %v", tc.header, tc.current, outcome, ok, tc.outcome, tc.ok)
		}
	}
}

func TestLogProfileRefreshRunWritesBoundedFields(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	c := app.NewContext(0)
	c.Request.Header.Set("X-Client-Mode", "plugin")
	c.Request.Header.Set("X-CLI-Ver", "0.0.60")
	c.Request.Header.Set("X-Client-Plugin-Version", "bad version; drop")
	logProfileRefreshRun(context.Background(), c, 42, "unchanged", "scheduled")

	var line map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log line is not JSON: %v (%q)", err, buf.String())
	}
	want := map[string]interface{}{
		"msg": profileRefreshRunLogMsg, "agent_id": float64(42), "outcome": "unchanged", "trigger": "scheduled",
		"mode": "plugin", "cli_version": "0.0.60", "plugin_version": "",
	}
	for key, value := range want {
		if line[key] != value {
			t.Errorf("%s = %#v, want %#v", key, line[key], value)
		}
	}
}

func TestRefreshTriggerNeverRejectsAndStaysBounded(t *testing.T) {
	cases := []struct {
		source, header, want string
	}{
		{"cli_daily_refresh", "", "unknown"}, // old CLI
		{"cli_daily_refresh", "scheduled", "scheduled"},
		{"cli_daily_refresh", " Manual ", "manual"},
		{"cli_daily_refresh", "cron; drop table", "unknown"},
		{"cli_manual_refresh", "", "manual"},
		{"cli_manual_refresh", "scheduled", "manual"},
	}
	for _, tc := range cases {
		if got := refreshTriggerFor(tc.source, tc.header); got != tc.want {
			t.Errorf("refreshTriggerFor(%q, %q) = %q, want %q", tc.source, tc.header, got, tc.want)
		}
	}
	for source, want := range map[string]bool{
		"cli_daily_refresh": true, "cli_manual_refresh": true, "console_v2": false, "": false,
	} {
		if got := isProfileRefreshSource(source); got != want {
			t.Errorf("isProfileRefreshSource(%q) = %v, want %v", source, got, want)
		}
	}
}

func TestLogProfileRefreshRunBoundsTrigger(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	logProfileRefreshRun(context.Background(), app.NewContext(0), 7, "changed", "anything-else")
	var line map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log line is not JSON: %v (%q)", err, buf.String())
	}
	if line["trigger"] != "unknown" {
		t.Fatalf("trigger = %#v, want unknown", line["trigger"])
	}
}
