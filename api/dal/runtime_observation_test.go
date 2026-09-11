package dal

import (
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"testing"
)

func observationDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&AgentSettings{}); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestRuntimeObservationFactsAndFence(t *testing.T) {
	database := observationDB(t)
	const id int64 = 101
	observe := func(obs RuntimeObservation) RuntimeObservationResult {
		t.Helper()
		result, err := ObserveRuntime(database, id, obs)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	read := func() AgentSettings {
		t.Helper()
		var row AgentSettings
		if err := database.First(&row, "agent_id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	observe(RuntimeObservation{Host: "openclaw/0.0.39", ObservedAt: 100, Active: true})
	if row := read(); row.Mode != "" || row.RuntimeName != "openclaw" || row.LastActivityAt != 100 {
		t.Fatalf("host must not imply mode: %+v", row)
	}
	observe(RuntimeObservation{Host: "openclaw", Mode: "plugin", ObservedAt: 200})
	if row := read(); row.RuntimeVersion != "0.0.39" || row.Mode != "plugin" {
		t.Fatalf("passive bare host must preserve version: %+v", row)
	}
	observe(RuntimeObservation{Host: "openclaw", Mode: "plugin", ObservedAt: 300, Explicit: true})
	if row := read(); row.RuntimeVersion != "" || row.ClientHost != "openclaw" {
		t.Fatalf("explicit bare host must clear fake version: %+v", row)
	}
	observe(RuntimeObservation{Mode: "skill", ObservedAt: 400, Explicit: true})
	result := observe(RuntimeObservation{Host: "openclaw/old", Mode: "plugin", ObservedAt: 350, Active: true})
	if row := read(); result.Outcome != "stale" || row.Mode != "skill" || row.ClientHost != "" || row.RuntimeReportedAt != 400 {
		t.Fatalf("mode-only fence lost: result=%+v row=%+v", result, row)
	}
	observe(RuntimeObservation{Host: "workbuddy", ObservedAt: 500})
	if row := read(); row.RuntimeName != "workbuddy" || row.RuntimeVersion != "" || row.Mode != "skill" {
		t.Fatalf("independent product merge failed: %+v", row)
	}
	observe(RuntimeObservation{Host: "terminal", ObservedAt: 600})
	if row := read(); row.RuntimeName != "workbuddy" || row.RuntimeReportedAt != 500 {
		t.Fatalf("unknown host changed identity: %+v", row)
	}
}

func TestRuntimeActivityCoalescesWithoutChangingSettingsTimestamp(t *testing.T) {
	database := observationDB(t)
	initial := AgentSettings{AgentID: 102, RuntimeName: "codex", RuntimeVersion: "1.2", UpdatedAt: 123, LastActivityAt: 100000}
	if err := database.Create(&initial).Error; err != nil {
		t.Fatal(err)
	}
	for _, ts := range []int64{99999, 100001, 130000, 160000, 159000} {
		if _, err := ObserveRuntime(database, 102, RuntimeObservation{Active: true, ObservedAt: ts}); err != nil {
			t.Fatal(err)
		}
	}
	var row AgentSettings
	if err := database.First(&row, "agent_id = ?", 102).Error; err != nil {
		t.Fatal(err)
	}
	if row.LastActivityAt != 160000 || row.UpdatedAt != 123 || row.RuntimeName != "codex" || row.RuntimeVersion != "1.2" {
		t.Fatalf("activity regressed or changed settings: %+v", row)
	}
}

func TestHandoffReportsModeBeforeOnboarding(t *testing.T) {
	database := observationDB(t)
	if err := UpdateHandoffClientIdentity(database, 103, "workbuddy", "", "machine", "0.0.44", "skill"); err != nil {
		t.Fatal(err)
	}
	var row AgentSettings
	if err := database.First(&row, "agent_id = ?", 103).Error; err != nil {
		t.Fatal(err)
	}
	if row.Mode != "skill" || row.RuntimeName != "workbuddy" || row.RuntimeReportedAt <= 0 || row.LastActivityAt != 0 {
		t.Fatalf("handoff report failed: %+v", row)
	}
}

func TestCompatibilityReportCannotBypassRuntimeFence(t *testing.T) {
	database := observationDB(t)
	if _, err := ObserveRuntime(database, 104, RuntimeObservation{Host: "workbuddy", CLIVersion: "0.0.44", ObservedAt: 500, Explicit: true}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateHeartbeatCompatibility(database, 104, "0.0.42", "eigenflux_heartbeat.v1", "current"); err != nil {
		t.Fatal(err)
	}
	if _, err := ObserveRuntime(database, 104, RuntimeObservation{CLIVersion: "0.0.42", ObservedAt: 400}); err != nil {
		t.Fatal(err)
	}
	var row AgentSettings
	if err := database.First(&row, "agent_id = ?", 104).Error; err != nil {
		t.Fatal(err)
	}
	if row.CLIVersion != "0.0.44" || row.RuntimeReportedAt != 500 {
		t.Fatalf("compatibility report bypassed fence: %+v", row)
	}
}
