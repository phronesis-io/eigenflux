package cmd

import (
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
