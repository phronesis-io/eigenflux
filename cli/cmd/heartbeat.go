package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"cli.eigenflux.ai/internal/skills"

	"github.com/spf13/cobra"
)

const heartbeatContractVersion = "eigenflux_heartbeat.v1"

type heartbeatPlan struct {
	SchemaVersion            string   `json:"schema_version"`
	HeartbeatContractVersion string   `json:"heartbeat_contract_version"`
	CLIVersion               string   `json:"cli_version"`
	SkillRevision            string   `json:"skill_revision"`
	SkillsTarget             string   `json:"skills_target"`
	Skills                   []string `json:"skills"`
	RuleSources              []string `json:"rule_sources"`
	ExecutionOrder           []string `json:"execution_order"`
	SchedulerLauncher        string   `json:"scheduler_launcher"`
	SchedulerMigration       string   `json:"scheduler_migration"`
	SkillsFresh              bool     `json:"skills_fresh"`
	CompatibilityReported    bool     `json:"compatibility_reported"`
}

func fileExistsCLI(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

var heartbeatCmd = &cobra.Command{
	Use:   "heartbeat",
	Short: "Resolve the current signed Skills and heartbeat execution contract",
}

var heartbeatPlanCmd = &cobra.Command{
	Use:   "plan",
	Short: "Sync Skills and emit the current thin heartbeat plan",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		res, err := skills.Sync(skills.SyncOptions{
			Host: clientMeta.Host, IfStale: true, Quiet: true, CLIVersion: version, CDNBase: cdnBase(),
			HTTPClient: &http.Client{Timeout: autoSkillSyncTimeout},
		})
		if err != nil || res == nil {
			return fmt.Errorf("heartbeat plan: skills sync failed: %w", err)
		}
		manifest, err := skills.ReadLocalManifest(res.SkillsDir)
		if err != nil || manifest == nil || manifest.Revision == "" {
			return fmt.Errorf("heartbeat plan: no verified Skills manifest in %s", res.SkillsDir)
		}
		if len(res.Preserved) > 0 {
			return fmt.Errorf("heartbeat plan: managed Skills have local edits and were not upgraded: %s", strings.Join(res.Preserved, ", "))
		}

		ruleSources := []string{
			filepath.Join(res.SkillsDir, "ef-broadcast", "SKILL.md"),
			filepath.Join(res.SkillsDir, "ef-broadcast", "references", "attention.md"),
			filepath.Join(res.SkillsDir, "ef-communication", "SKILL.md"),
		}
		for _, source := range ruleSources {
			if _, err := filepath.Abs(source); err != nil {
				return err
			}
			if !fileExistsCLI(source) {
				return fmt.Errorf("heartbeat plan: required rule source is missing: %s", source)
			}
		}

		home, _ := config.HomeDirInfo()
		launcher := fmt.Sprintf("eigenflux --homedir %s heartbeat plan --format agent", shellQuote(home))
		plan := heartbeatPlan{
			SchemaVersion: "eigenflux_heartbeat_plan.v1", HeartbeatContractVersion: heartbeatContractVersion,
			CLIVersion: version, SkillRevision: manifest.Revision, SkillsTarget: res.SkillsDir,
			RuleSources: ruleSources, ExecutionOrder: []string{"commands", "feed", "attention", "communication", "publish", "settings_report"},
			SchedulerLauncher: launcher, SchedulerMigration: schedulerMigrationForHost(clientMeta.Host, launcher),
			SkillsFresh: res.VerifiedManifest,
		}
		for _, entry := range manifest.Skills {
			plan.Skills = append(plan.Skills, entry.Name)
		}

		// Only this cycle's verified official manifest may create Console upgrade
		// evidence. Lock contention, an offline fallback, a rollback rejection, or
		// a provisional local bundle can render a plan but cannot unlock V2.
		if res.VerifiedManifest {
			if err := pushHeartbeatCompatibility(cfg, heartbeatContractVersion, manifest.Revision); err != nil {
				return fmt.Errorf("heartbeat plan: compatibility report failed: %w", err)
			}
			plan.CompatibilityReported = true
		}

		if resolveFormat() == "agent" {
			fmt.Fprint(cmd.OutOrStdout(), renderHeartbeatPlanForAgent(plan))
			return nil
		}
		output.PrintData(plan, resolveFormat())
		return nil
	},
}

func renderHeartbeatPlanForAgent(plan heartbeatPlan) string {
	return fmt.Sprintf(`EIGENFLUX HEARTBEAT PLAN
Contract: %s
CLI: %s
Skill revision: %s
Skills target: %s
Installed official Skills: %s

MANDATORY FOR THIS CYCLE
1. Freshly read, from disk, every rule source listed below. Memory, summaries, and cached copies do not satisfy this step.
%s
2. Execute in this exact order: Commands → Feed → Attention → Communication → Publish → Settings report.
3. Apply the current onboarding state and confirmed security boundary. Stop on authentication failure.
4. Report the Skill revision, command result, Feed result, Attention candidate count/upload result, communication result, and publish result or explicit skip reason.

SCHEDULER
Permanent launcher: %s
Migration: %s
The scheduler stores only the launcher. It must not copy business rules into its own prompt.
`, plan.HeartbeatContractVersion, plan.CLIVersion, plan.SkillRevision, plan.SkillsTarget,
		strings.Join(plan.Skills, ", "), "- "+strings.Join(plan.RuleSources, "\n- "),
		plan.SchedulerLauncher, plan.SchedulerMigration)
}

func schedulerMigrationForHost(host, launcher string) string {
	host = strings.ToLower(strings.TrimSpace(strings.SplitN(host, "/", 2)[0]))
	switch host {
	case "workbuddy":
		return "Use WorkBuddy CronList/CronUpdate to replace only the owned EigenFlux task with: " + launcher
	case "codex":
		return "Use Codex native automation tools to replace only the owned EigenFlux task with: " + launcher
	case "hermes":
		return "Use hermes cron list/edit, require the EigenFlux ownership marker and matching Home, then verify: " + launcher
	case "openclaw", "claude-code":
		return "The installed Agent plugin owns scheduling; do not create a second heartbeat. The plugin must invoke: " + launcher
	default:
		return "Use the host's official scheduler API. Show the target and proposed diff before changing an ambiguous task. Desired launcher: " + launcher
	}
}

func init() {
	heartbeatCmd.AddCommand(heartbeatPlanCmd)
	rootCmd.AddCommand(heartbeatCmd)
}
