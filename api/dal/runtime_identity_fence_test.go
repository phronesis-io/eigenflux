package dal

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestUpdateDerivedRuntimeIfNotSuperseded(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&AgentSettings{}); err != nil {
		t.Fatal(err)
	}
	initial := AgentSettings{AgentID: 101, Mode: "skill", RuntimeName: "hermes", UpdatedAt: 100}
	if err := database.Create(&initial).Error; err != nil {
		t.Fatal(err)
	}

	updated, err := UpdateDerivedRuntimeIfNotSuperseded(
		database, initial.AgentID, "skill", "", "workbuddy", "5.3.8", "gpt-5", "0.0.31", 200,
	)
	if err != nil || !updated {
		t.Fatalf("unsuperseded update = %v, %v; want true, nil", updated, err)
	}

	if err := database.Model(&AgentSettings{}).Where("agent_id = ?", initial.AgentID).Updates(map[string]interface{}{
		"runtime_name": "codex", "runtime_version": "1.2.3", "runtime_reported_at": int64(500),
	}).Error; err != nil {
		t.Fatal(err)
	}
	updated, err = UpdateDerivedRuntimeIfNotSuperseded(
		database, initial.AgentID, "skill", "", "hermes", "0.21.0", "gpt-5", "0.0.31", 400,
	)
	if err != nil || updated {
		t.Fatalf("superseded update = %v, %v; want false, nil", updated, err)
	}
	var current AgentSettings
	if err := database.First(&current, "agent_id = ?", initial.AgentID).Error; err != nil {
		t.Fatal(err)
	}
	if current.RuntimeName != "codex" || current.RuntimeVersion != "1.2.3" {
		t.Fatalf("old feed overwrote explicit identity: %+v", current)
	}
}
