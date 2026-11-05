package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHeartbeatAgentExposesIncompleteUpgradeReadiness(t *testing.T) {
	plan := heartbeatPlan{
		OuterLoop:     &updateWatchRecovery{Status: "pending", Reason: "main_session_bootstrap_required", SkillsStatus: "local_verified_auto_sync_disabled"},
		Notifications: &upgradeNotificationStatus{Status: "permission_or_setup_required"},
	}
	text := renderHeartbeatPlanForAgent(plan)
	for _, want := range []string{"main_session_bootstrap_required", "local_verified_auto_sync_disabled", "permission_or_setup_required", "never claim the owner granted permission automatically"} {
		if !strings.Contains(text, want) {
			t.Fatalf("agent output omitted readiness %q", want)
		}
	}
}

func TestHeartbeatRecoveryNoticeDecisionPreservesDiagnosticResult(t *testing.T) {
	for _, tc := range []struct {
		notification string
		instruction  string
	}{
		{"required", "Notify the owner of this new or changed recovery action"},
		{"unchanged", "Keep this unchanged recovery state internal"},
		{"recovered", "Notify the owner once that the previously reported recovery state has cleared"},
	} {
		t.Run(tc.notification, func(t *testing.T) {
			recovery := updateWatchRecovery{Status: "pending", Reason: "external_loop_unverified", Action: "verify the existing scheduler", Notification: tc.notification}
			if tc.notification == "recovered" {
				recovery.Status, recovery.Reason, recovery.Action = "ready", "", ""
			}
			plan := heartbeatPlan{OuterLoop: &recovery}
			text := renderHeartbeatPlanForAgent(plan)
			diagnostic, err := json.Marshal(recovery)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{string(diagnostic), tc.instruction} {
				if !strings.Contains(text, want) {
					t.Fatalf("decision lost diagnosis or notification policy %q", want)
				}
			}
			if tc.notification == "unchanged" && strings.Contains(text, "Report unresolved recovery separately") {
				t.Fatal("unchanged state still asks for another owner notification")
			}
		})
	}
}
