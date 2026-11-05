package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

func TestHeartbeatReadyRecoveryStaysQuietAfterAcknowledgment(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "https://quiet.example.invalid", true)
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	checkReady := func() {
		t.Helper()
		r := updateWatchRecovery{Status: "ready", RuntimeReady: true}
		annotateWatchRecoveryNotice(cmd, cfg, &r)
		text := renderHeartbeatPlanForAgent(heartbeatPlan{OuterLoop: &r})
		if r.Notification != "" || strings.Contains(text, "Report unresolved recovery separately") || strings.Contains(text, "Notify the owner") {
			t.Fatalf("healthy or acknowledged recovery still asks for a notice: %+v\n%s", r, text)
		}
	}
	checkReady()
	failure := updateWatchRecovery{Status: "pending", Reason: "external_loop_unverified", Action: "verify scheduler"}
	annotateWatchRecoveryNotice(cmd, cfg, &failure)
	if err := acknowledgeWatchRecoveryNotice(failure.NotificationID); err != nil {
		t.Fatal(err)
	}
	recovered := updateWatchRecovery{Status: "ready", RuntimeReady: true}
	annotateWatchRecoveryNotice(cmd, cfg, &recovered)
	if err := acknowledgeWatchRecoveryNotice(recovered.NotificationID); err != nil {
		t.Fatal(err)
	}
	checkReady()
}

func TestHeartbeatRecoveryNoticeDecisionPreservesDiagnosticResult(t *testing.T) {
	for _, tc := range []struct {
		notification string
		instruction  string
	}{
		{"required", "Notify the owner of this new or changed recovery action"},
		{"unchanged", "Keep this unchanged recovery state internal"},
		{"recovered", "Notify the owner once that the recovery state has cleared"},
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

func TestHeartbeatRecoveryNoticeAckRequiresPendingIDAndCLIPrefix(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, tc := range []struct {
		notification, id, prefix string
		wantAck                  bool
	}{
		{"required", id, "'/stable/eigenflux' --homedir '/agent' --server 'main'", true},
		{"recovered", id, "'/stable/eigenflux' --homedir '/agent' --server 'main'", true},
		{"unchanged", id, "test-prefix", false},
		{"required", "", "test-prefix", false},
		{"required", "invalid;command", "test-prefix", false},
		{"required", id, "", false},
	} {
		t.Run(tc.notification+"/"+tc.id+"/"+tc.prefix, func(t *testing.T) {
			plan := heartbeatPlan{CLIPrefix: tc.prefix, OuterLoop: &updateWatchRecovery{Status: "pending", Notification: tc.notification, NotificationID: tc.id}}
			text := renderHeartbeatPlanForAgent(plan)
			command := tc.prefix + " heartbeat recovery-notice-ack --notice " + tc.id
			if strings.Contains(text, " heartbeat recovery-notice-ack --notice ") != tc.wantAck || tc.wantAck && !strings.Contains(text, command) {
				t.Fatalf("ack command does not match pending receipt: %s", text)
			}
			if tc.wantAck {
				for _, instruction := range []string{"only after the host confirms its submission", "exact final response containing this recovery notice", "emit that response unchanged", "Plan prefetch, Feed failure", "acknowledgment failure must not repeat business work"} {
					if !strings.Contains(text, instruction) {
						t.Fatalf("missing acknowledgment boundary %q", instruction)
					}
				}
			}
		})
	}
}
