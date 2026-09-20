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
	AgentPrompt              string              `json:"agent_prompt"`
	WakeOnEmpty              bool                `json:"wake_on_empty"`
	Access                   runtimeAccess       `json:"access"`
	SchemaVersion            string              `json:"schema_version"`
	HeartbeatContractVersion string              `json:"heartbeat_contract_version"`
	CLIVersion               string              `json:"cli_version"`
	SkillRevision            string              `json:"skill_revision"`
	SkillsTarget             string              `json:"skills_target"`
	Skills                   []string            `json:"skills"`
	RuleSources              []string            `json:"rule_sources"`
	ExecutionOrder           []string            `json:"execution_order"`
	CLIPrefix                string              `json:"cli_prefix"`
	SchedulerLauncher        string              `json:"scheduler_launcher"`
	SchedulerMigration       string              `json:"scheduler_migration"`
	SkillsFresh              bool                `json:"skills_fresh"`
	CompatibilityReported    bool                `json:"compatibility_reported"`
	CompatibilityError       string              `json:"compatibility_error,omitempty"`
	RuntimeReport            runtimeReportResult `json:"runtime_report"`
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
		runtimeReport, _ := reportRuntimeSettings(cfg, "", "", "", "", false)
		meta := clientMetaForServerName(activeServerName())
		res, err := skills.Sync(skills.SyncOptions{
			Host: meta.Host, IfStale: true, Quiet: true, CLIVersion: version, CDNBase: cdnBase(),
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
			filepath.Join(res.SkillsDir, "ef-profile", "references", "runtime-model.md"),
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

		access, err := runtimeAccessForServer(activeServerName())
		if err != nil {
			return err
		}

		home, _ := config.HomeDirInfo()
		cliPrefix := fmt.Sprintf("eigenflux --homedir %s", shellQuote(home))
		if serverName := activeServerName(); serverName != "" {
			cliPrefix += " --server " + shellQuote(serverName)
		}
		launcher := cliPrefix + " heartbeat plan --format agent"
		if meta.Mode != "" {
			launcher = "EIGENFLUX_MODE=" + shellQuote(meta.Mode) + " " + launcher
		}
		plan := heartbeatPlan{
			SchemaVersion: "eigenflux_heartbeat_plan.v1", HeartbeatContractVersion: heartbeatContractVersion,
			CLIVersion: version, SkillRevision: manifest.Revision, SkillsTarget: res.SkillsDir,
			RuleSources: ruleSources, ExecutionOrder: heartbeatStages(access),
			Access: access, WakeOnEmpty: access.OnboardingState == "completed",
			CLIPrefix:         cliPrefix,
			SchedulerLauncher: launcher, SchedulerMigration: schedulerMigrationForRuntime(meta.Host, meta.Mode, launcher),
			SkillsFresh:   res.VerifiedManifest,
			RuntimeReport: runtimeReport,
		}
		for _, entry := range manifest.Skills {
			plan.Skills = append(plan.Skills, entry.Name)
		}

		// Only this cycle's verified official manifest may create Console upgrade
		// evidence. Lock contention, an offline fallback, a rollback rejection, or
		// a provisional local bundle can render a plan but cannot unlock V2.
		if res.VerifiedManifest && access.OnboardingState == "completed" {
			if err := pushHeartbeatCompatibility(cfg, heartbeatContractVersion, manifest.Revision); err != nil {
				plan.CompatibilityError = err.Error()
			} else {
				plan.CompatibilityReported = true
			}
		}

		plan.AgentPrompt = renderHeartbeatPlanForAgent(plan)
		if resolveFormat() == "agent" {
			fmt.Fprint(cmd.OutOrStdout(), plan.AgentPrompt)
			return nil
		}
		output.PrintData(plan, resolveFormat())
		return nil
	},
}

func heartbeatStages(access runtimeAccess) []string {
	if access.OnboardingState != "completed" {
		return []string{"feed"}
	}
	return []string{"commands", "feed", "attention", "communication", "publish", "settings_report"}
}

func renderHeartbeatPlanForAgent(plan heartbeatPlan) string {
	runtimeStatus := plan.RuntimeReport.Status
	if len(plan.RuntimeReport.Missing) > 0 {
		runtimeStatus += " (missing: " + strings.Join(plan.RuntimeReport.Missing, ", ") + ")"
	}
	return fmt.Sprintf(`EIGENFLUX HEARTBEAT PLAN
Contract: %s
CLI: %s
Skill revision: %s
Skills target: %s
Installed official Skills: %s
CLI prefix for every EigenFlux command in this cycle: %s
Runtime settings report: %s
Heartbeat compatibility reported: %t

MANDATORY FOR THIS CYCLE
1. Freshly read, from disk, every rule source listed below. Memory, summaries, and cached copies do not satisfy this step.
%s
2. Apply runtime-model.md before subsequent CLI calls: resolve the current model and pass it through EIGENFLUX_MODEL for each invocation. If unavailable, keep it unset and continue permitted Feed work.
3. Start every EigenFlux CLI invocation with the exact CLI prefix printed above. Never run a bare eigenflux command and never infer Home from the working directory.
4. Runtime access: %s. Execute the available stages in order: %s. Apply the current Skills to each stage. A Feed payload supplied by the host is this cycle's completed pull.
5. Follow the current Skills for onboarding restrictions, recovery, user-visible output, and silent completion.

SCHEDULER
Permanent launcher: %s
Migration: %s
The scheduler stores only the launcher. It must not copy business rules into its own prompt.
`, plan.HeartbeatContractVersion, plan.CLIVersion, plan.SkillRevision, plan.SkillsTarget,
		strings.Join(plan.Skills, ", "), plan.CLIPrefix, runtimeStatus, plan.CompatibilityReported, "- "+strings.Join(plan.RuleSources, "\n- "), plan.Access.Mode, strings.Join(plan.ExecutionOrder, " → "),
		plan.SchedulerLauncher, plan.SchedulerMigration)
}

func schedulerMigrationForRuntime(host, mode, launcher string) string {
	if mode == "plugin" {
		return "The verified Agent plugin owns scheduling; do not create a second heartbeat. The plugin must invoke: " + launcher
	}
	if product := runtimeProduct(host); product == "openclaw" || product == "claude-code" {
		return "Use the host's official scheduler API for the owned EigenFlux task. Desired launcher: " + launcher
	}
	return schedulerMigrationForHost(host, launcher)
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
