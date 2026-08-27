package cmd

import (
	"strings"
	"testing"
)

func TestRenderHeartbeatPlanForAgentIsThinAndCurrent(t *testing.T) {
	plan := heartbeatPlan{
		HeartbeatContractVersion: heartbeatContractVersion,
		CLIVersion:               "0.0.34", SkillRevision: "rev-123", SkillsTarget: "/tmp/skills",
		Skills:             []string{"ef-broadcast", "ef-communication", "ef-future"},
		RuleSources:        []string{"/tmp/skills/ef-broadcast/SKILL.md", "/tmp/skills/ef-broadcast/references/attention.md"},
		SchedulerLauncher:  "eigenflux --homedir /tmp/home heartbeat plan --format agent",
		SchedulerMigration: "migrate owned task",
	}
	text := renderHeartbeatPlanForAgent(plan)
	for _, required := range []string{
		heartbeatContractVersion, "rev-123", "ef-future", "Freshly read, from disk",
		"Commands → Feed → Attention → Communication → Publish → Settings report",
		"scheduler stores only the launcher",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("heartbeat plan missing %q:\n%s", required, text)
		}
	}
}

func TestSchedulerMigrationUsesNativeHostOwnership(t *testing.T) {
	launcher := "eigenflux --homedir /stable heartbeat plan --format agent"
	tests := map[string][]string{
		"workbuddy/5.3.14": {"CronList/CronUpdate", "owned EigenFlux task"},
		"codex/1.0":        {"native automation", "owned EigenFlux task"},
		"hermes/0.20":      {"cron list/edit", "ownership marker", "matching Home"},
		"openclaw/1.0":     {"plugin owns scheduling", "do not create a second heartbeat"},
		"claude-code/1.0":  {"plugin owns scheduling", "do not create a second heartbeat"},
		"unknown/1.0":      {"official scheduler API", "proposed diff"},
	}
	for host, required := range tests {
		got := schedulerMigrationForHost(host, launcher)
		if !strings.Contains(got, launcher) {
			t.Fatalf("%s migration dropped launcher: %s", host, got)
		}
		for _, fragment := range required {
			if !strings.Contains(got, fragment) {
				t.Fatalf("%s migration missing %q: %s", host, fragment, got)
			}
		}
	}
}

func TestHeartbeatCommandsAreRegistered(t *testing.T) {
	if heartbeatPlanCmd.Parent() != heartbeatCmd || heartbeatCmd.Parent() != rootCmd {
		t.Fatal("heartbeat plan command is not registered under the root command")
	}
}
