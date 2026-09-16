package cmd

import (
	"testing"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/heartbeatmigration"
)

func TestDoctorNeverPromotesCachedSchedulerReceiptToLiveHealth(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	if report := schedulerDiagnostic("codex"); report["status"] != "unknown" || report["source"] != "host_native_tools_required" {
		t.Fatalf("missing inventory is not proof of removal: %v", report)
	}
	record := migrationRecord{Home: config.HomeDir(), Server: server, Verified: time.Now(), Plan: heartbeatmigration.Plan{TaskID: "owned-heartbeat"}}
	if err := saveMaintenance(migrationReceiptPath("codex"), record); err != nil {
		t.Fatal(err)
	}
	report := schedulerDiagnostic("codex")
	if report["status"] != "unknown" || report["source"] != "cached_host_readback" || report["task_id"] != "owned-heartbeat" {
		t.Fatalf("cached evidence was treated as live: %v", report)
	}
}
