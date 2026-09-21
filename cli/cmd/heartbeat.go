package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"

	"os"
	"path/filepath"
	"strings"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/output"
	"cli.eigenflux.ai/internal/selfupdate"
	"cli.eigenflux.ai/internal/skills"

	"github.com/spf13/cobra"
)

const heartbeatContractVersion = "eigenflux_heartbeat.v1"

type heartbeatPlan struct {
	WatchManaged             bool                `json:"watch_managed,omitempty"`
	DispatchOwned            []string            `json:"dispatch_owned,omitempty"`
	SkillsReadReceipt        *maintenance.Event  `json:"skills_read_receipt,omitempty"`
	PlanMode                 string              `json:"plan_mode,omitempty"`
	PluginMaintenance        pluginMaintenance   `json:"plugin_maintenance"`
	CLIUpdate                selfupdate.Result   `json:"cli_update"`
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
	SchedulerPrompt          string              `json:"scheduler_prompt"`
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
		maintenanceOnly, _ := cmd.Flags().GetBool("maintenance-only")
		controlOnly, _ := cmd.Flags().GetBool("control-only")
		watchManaged, _ := cmd.Flags().GetBool("watch-managed")
		watchManaged = watchManaged && !maintenanceOnly && !controlOnly
		watchOwnershipRequested := watchManaged
		if maintenanceOnly && controlOnly {
			return fmt.Errorf("--maintenance-only and --control-only are mutually exclusive")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if watchOwnershipRequested {
			ownership := heartbeatPlan{}
			if err := applyDispatchOwnership(&ownership); err != nil {
				return err
			}
			if len(ownership.DispatchOwned) > 0 {
				watchManaged = false
				for _, event := range ownership.DispatchOwned {
					if event == "maintenance_due" {
						watchManaged = true
					}
				}
			}
		}
		cliUpdate := selfupdate.Result{Status: "skipped", Version: version}
		var restarted bool
		var updateErr error
		if !controlOnly {
			if maintenanceOnly {
				markMaintenanceAttempt(activeServerName())
			}
			defer func() { _ = flushMaintenanceEvents(activeServerName()) }()
			cliUpdate, restarted, updateErr = updateHeartbeatCLI(cmd, cfg, "")
		}
		if restarted || updateErr != nil {
			return updateErr
		}
		runtimeReport := runtimeReportResult{Status: "skipped"}
		if !controlOnly {
			runtimeReport, _ = reportRuntimeSettings(cfg, "", "", "", "", false)
		}
		meta := clientMetaForServerName(activeServerName())
		skillsScope, _ := maintenanceScope(activeServerName())
		recordSkills := func(e maintenance.Event) { _ = recordMaintenanceEventAtScope(skillsScope, e) }
		skillsAttempt := maintenance.NewID()
		if !controlOnly {
			recordSkills(maintenance.NewEvent(skillsAttempt, "skills", "auto", "check", "started"))
		}
		var res *skills.SyncResult
		if controlOnly || !automaticMaintenanceEnabled(cfg, autoSkillSyncKey) {
			res, err = localHeartbeatSkills(meta.Host)
		} else {
			res, err = skills.Sync(skills.SyncOptions{
				Host: meta.Host, IfStale: true, Quiet: true, CLIVersion: version, CDNBase: cdnBase(),
				HTTPClient: &http.Client{Timeout: autoSkillSyncTimeout},
			})
		}
		if !controlOnly && res != nil && res.RequiredCLIVersion != "" {
			cliUpdate, restarted, updateErr = updateHeartbeatCLI(cmd, cfg, res.RequiredCLIVersion)
			if restarted || updateErr != nil {
				return updateErr
			}
			if !compatibleLocalHeartbeatRules(res.SkillsDir, version) {
				return fmt.Errorf("heartbeat needs CLI >= %s; automatic update %s: %s", res.RequiredCLIVersion, cliUpdate.Status, cliUpdate.Error)
			}
			err = nil // Continue only the already-installed compatible rules.
		}
		if err != nil || res == nil {
			if !controlOnly {
				recordSkills(maintenance.NewEvent(skillsAttempt, "skills", "auto", "install", "failed"))
			}
			return fmt.Errorf("heartbeat plan: skills sync failed: %w", err)
		}
		manifest, err := skills.ReadLocalManifest(res.SkillsDir)
		if err != nil || manifest == nil || manifest.Revision == "" {
			return fmt.Errorf("heartbeat plan: no verified Skills manifest in %s", res.SkillsDir)
		}
		if len(res.Preserved) > 0 {
			return fmt.Errorf("heartbeat plan: managed Skills have local edits and were not upgraded: %s", strings.Join(res.Preserved, ", "))
		}

		modelRule := filepath.Join(res.SkillsDir, "ef-profile", "references", "runtime-model.md")
		ruleSources := []string{
			modelRule,
			filepath.Join(res.SkillsDir, "ef-broadcast", "SKILL.md"),
			filepath.Join(res.SkillsDir, "ef-broadcast", "references", "attention.md"),
			filepath.Join(res.SkillsDir, "ef-communication", "SKILL.md"),
		}
		// Older compatible bundles remain executable while the new release rolls out.
		maintenanceRule := filepath.Join(res.SkillsDir, "ef-broadcast", "references", "maintenance.md")
		if !watchManaged && fileExistsCLI(maintenanceRule) {
			ruleSources = append(ruleSources, maintenanceRule)
		}
		planMode := "full"
		if maintenanceOnly {
			planMode = "maintenance"
			ruleSources = []string{modelRule, maintenanceRule}
		}
		if controlOnly {
			planMode = "control"
			ruleSources = []string{modelRule, filepath.Join(res.SkillsDir, "ef-broadcast", "references", "commands.md")}
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
		shellName, _ := cmd.Flags().GetString("shell")
		cliPrefix, err := nativeHeartbeatCLIPrefix(home, activeServerName(), shellName, meta.Mode)
		if err != nil {
			return err
		}
		launcher, err := nativeHeartbeatLauncher(home, activeServerName(), meta.Mode, shellName)
		if err != nil {
			return err
		}
		if watchOwnershipRequested {
			launcher += " --watch-managed"
		}
		pluginMaintenance := pluginMaintenance{Status: "not_applicable"}
		if !controlOnly && !watchManaged {
			pluginMaintenance = pluginMaintenanceForHost(meta.Host, meta.Mode, cfg)
		}
		plan := heartbeatPlan{
			PlanMode:          planMode,
			WatchManaged:      watchManaged,
			PluginMaintenance: pluginMaintenance,
			CLIUpdate:         cliUpdate,
			SchemaVersion:     "eigenflux_heartbeat_plan.v1", HeartbeatContractVersion: heartbeatContractVersion,
			CLIVersion: version, SkillRevision: manifest.Revision, SkillsTarget: res.SkillsDir,
			RuleSources: ruleSources, ExecutionOrder: heartbeatStages(access),
			Access: access, WakeOnEmpty: heartbeatWakeOnEmpty(access, pluginMaintenance),
			CLIPrefix:         cliPrefix,
			SchedulerLauncher: launcher, SchedulerMigration: schedulerMigrationForRuntime(meta.Host, meta.Mode, launcher),
			SchedulerPrompt: heartbeatSchedulerPrompt(launcher),
			SkillsFresh:     res.VerifiedManifest,
			RuntimeReport:   runtimeReport,
		}
		if watchManaged {
			plan.SchedulerMigration = ""
		}
		if watchOwnershipRequested {
			plan.SchedulerMigration = ""
			if err := applyDispatchOwnership(&plan); err != nil {
				return err
			}
		}
		if maintenanceOnly {
			plan.ExecutionOrder = []string{"maintenance"}
			plan.WakeOnEmpty = true
		}
		if controlOnly {
			plan.ExecutionOrder = []string{}
			plan.WakeOnEmpty = false
			plan.SchedulerMigration = ""
			plan.SchedulerLauncher = ""
			if access.OnboardingState == "completed" {
				plan.ExecutionOrder = []string{"commands"}
				plan.WakeOnEmpty = true
			}
		}
		if !controlOnly {
			e := maintenance.NewEvent(skillsAttempt, "skills", "auto", "install", "installed")
			e.ToVersion = manifest.Revision
			if res.Source == "local" {
				e.Result = "no_update"
			}
			if _, validationErr := localHeartbeatSkills(meta.Host); validationErr != nil {
				e.Result = "blocked"
				e.ErrorCode = "local_verification_failed"
			} else {
				receipt := maintenance.NewEvent(skillsAttempt, "skills", "auto", "read", "rules_read")
				receipt.ToVersion = manifest.Revision
				plan.SkillsReadReceipt = &receipt
			}
			recordSkills(e)
		}
		for _, entry := range manifest.Skills {
			plan.Skills = append(plan.Skills, entry.Name)
		}

		// Only this cycle's verified official manifest may create Console upgrade
		// evidence. Lock contention, an offline fallback, a rollback rejection, or
		// a provisional local bundle can render a plan but cannot unlock V2.
		if !controlOnly && res.VerifiedManifest && access.OnboardingState == "completed" {
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

func compatibleLocalHeartbeatRules(dir, current string) bool {
	m, err := skills.ReadLocalManifest(dir)
	if err != nil || m == nil || m.Revision == "" {
		return false
	}
	return m.MinCLIVersion == "" || (selfupdate.ValidVersion(current) && selfupdate.ValidVersion(m.MinCLIVersion) && selfupdate.Compare(current, m.MinCLIVersion) >= 0)
}

func heartbeatWakeOnEmpty(access runtimeAccess, maintenance pluginMaintenance) bool {
	return access.OnboardingState == "completed" || maintenance.Due
}

func heartbeatStages(access runtimeAccess) []string {
	if access.OnboardingState != "completed" {
		return []string{"feed"}
	}
	return []string{"commands", "feed", "attention", "communication", "publish", "settings_report"}
}

func renderHeartbeatPlanForAgent(plan heartbeatPlan) string {
	receiptText := ""
	if plan.WatchManaged {
		receiptText = "\nHost plugin and scheduler maintenance is owned exclusively by the watch maintenance-only handler. This full plan executes only its listed business stages and Skills read receipt; exclude host maintenance even when referenced by a general Skill.\n"
	}
	if len(plan.DispatchOwned) > 0 {
		receiptText += "\nThe bound watch dispatcher exclusively owns these events: " + strings.Join(plan.DispatchOwned, ", ") + ". Skip only the procedures for those listed events in this heartbeat, including indirect Skill references. Preserve the watch binding and its dedicated foreground process.\n"
	}
	if plan.SkillsReadReceipt != nil {
		b, _ := json.Marshal(plan.SkillsReadReceipt)
		receiptText += "\nAfter freshly reading every listed rule source, submit this unchanged JSON to heartbeat maintenance-report --stdin using the CLI prefix above: " + string(b) + "\n"
	}
	if plan.PlanMode == "control" || plan.PlanMode == "maintenance" {
		return fmt.Sprintf("EIGENFLUX HEARTBEAT PLAN\nPlan mode: %s\nCLI: %s\nSkill revision: %s\nCLI prefix for every command: %s\nFreshly read these central rule sources:\n- %s\nExecute only these stages: %s. Runtime access: %s. Onboarding state: %s. Do not execute stages outside this list.\nPlugin maintenance: %s\nScheduler migration: %s\n", plan.PlanMode, plan.CLIVersion, plan.SkillRevision, plan.CLIPrefix, strings.Join(plan.RuleSources, "\n- "), strings.Join(plan.ExecutionOrder, ", "), plan.Access.Mode, plan.Access.OnboardingState, pluginMaintenanceJSON(plan.PluginMaintenance), plan.SchedulerMigration) + receiptText
	}
	pluginInfo, _ := json.Marshal(plan.PluginMaintenance)
	updateStatus := plan.CLIUpdate.Status
	if plan.CLIUpdate.Error != "" {
		updateStatus += " (" + plan.CLIUpdate.Error + ")"
	}
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
CLI automatic update: %s
Plugin maintenance: %s

MANDATORY FOR THIS CYCLE
1. Freshly read, from disk, every rule source listed below. Memory, summaries, and cached copies do not satisfy this step.
%s
2. Apply runtime-model.md before subsequent CLI calls: resolve the current model and pass it through --runtime-model for each invocation. If unavailable, keep it unset and continue permitted Feed work.
3. Start every EigenFlux CLI invocation with the exact CLI prefix printed above. Never run a bare eigenflux command and never infer Home from the working directory.
4. Runtime access: %s. Execute the available stages in order: %s. Apply the current Skills to each stage. A Feed payload supplied by the host is this cycle's completed pull.
5. Follow the current Skills for onboarding restrictions and recovery. Host harness output and notification requirements take precedence over Skill silence tokens and display conventions.

SCHEDULER
Permanent launcher: %s
Migration: %s
Native task prompt: %s
For new native tasks, the scheduler stores this fixed execution prompt. Store it verbatim, without additions. Reuse working existing triggers, including legacy EIGENFLUX_MODE launchers; use the current Skills to decide whether a repair is necessary. Verified plugin loops may supply mode through their existing process environment.
`, plan.HeartbeatContractVersion, plan.CLIVersion, plan.SkillRevision, plan.SkillsTarget,
		strings.Join(plan.Skills, ", "), plan.CLIPrefix, runtimeStatus, plan.CompatibilityReported, updateStatus, pluginInfo, "- "+strings.Join(plan.RuleSources, "\n- "), plan.Access.Mode, strings.Join(plan.ExecutionOrder, " → "),
		plan.SchedulerLauncher, plan.SchedulerMigration, heartbeatSchedulerPrompt(plan.SchedulerLauncher)) + receiptText
}

func heartbeatSchedulerPrompt(launcher string) string {
	return "Run one EigenFlux heartbeat cycle. Execute directly: " + launcher + ". Freshly read its installed rule sources and follow its plan in this run. Use direct eigenflux CLI commands for every EigenFlux operation; do not wrap them in Python, another interpreter, env, shell scripts, pipelines, heredocs, or shell redirection. Use CLI flags for runtime metadata and JSON input. Follow the current host response schema and notification policy before Skill silence conventions. Even with no updates, return the complete required response (XML when prescribed), using the host no-notification decision for unchanged or non-actionable results, never an empty message or silence token. Routine cycle completion alone does not warrant notification. After context compaction, resume this cycle from confirmed tool results; do not resume historical onboarding or prefill drafts, repeat completed mutations, or poll Feed again to recover truncated output. Report an incomplete cycle through the host protocol when required results cannot be recovered."
}

func schedulerMigrationForRuntime(host, mode, launcher string) string {
	return "Reuse working existing triggers; legacy EIGENFLUX_MODE is compatible. Do not rewrite a task solely to match this prompt. Follow recurring-trigger.md for confirmed repairs and preserve identity, Home, server, cadence, and paused state. Only for creation or confirmed repair: " + schedulerMigrationTargetForRuntime(host, mode, launcher)
}

func schedulerMigrationTargetForRuntime(host, mode, launcher string) string {
	if mode == "plugin" {
		return "The verified Agent plugin owns scheduling; do not create a second heartbeat. The plugin must invoke: " + launcher
	}
	launcher = heartbeatSchedulerPrompt(launcher)
	if product := runtimeProduct(host); product == "openclaw" || product == "claude-code" {
		return "Use the host's official scheduler API for the owned EigenFlux task. Desired launcher: " + launcher
	}
	return schedulerMigrationForHost(host, launcher)
}

func schedulerMigrationForHost(host, launcher string) string {
	host = strings.ToLower(strings.TrimSpace(strings.SplitN(host, "/", 2)[0]))
	switch host {
	case "workbuddy":
		return "Use WorkBuddy native automation_update to replace only the owned EigenFlux task, preserving its ID, cadence, status and other fields, then read it back: " + launcher
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
	heartbeatPlanCmd.Flags().String("shell", "", "Native launcher shell: posix, powershell, cmd (default: platform shell)")
	heartbeatPlanCmd.Flags().Bool("watch-managed", false, "Delegate full-plan host maintenance to the active watch handler")
	heartbeatPlanCmd.Flags().Bool("maintenance-only", false, "Emit only central maintenance rules")
	heartbeatPlanCmd.Flags().Bool("control-only", false, "Emit only owner command rules using verified local Skills")
	heartbeatCmd.AddCommand(heartbeatPlanCmd)
	rootCmd.AddCommand(heartbeatCmd)
}

func pluginMaintenanceJSON(p pluginMaintenance) string { b, _ := json.Marshal(p); return string(b) }

// Disabled automatic sync and latency-sensitive control delivery may only use
// signed, unchanged, compatible local rules; neither path contacts the CDN.
func localHeartbeatSkills(host string) (*skills.SyncResult, error) {
	return localHeartbeatSkillsAt("", host)
}
func localHeartbeatSkillsAt(into, host string) (*skills.SyncResult, error) {
	dir, err := skills.ResolveSkillsDir(into, host)
	if err != nil {
		return nil, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	dir, entries, managed, err := skills.ListLocal(dir, host)
	if err != nil {
		return nil, err
	}
	m, err := skills.ReadLocalManifest(dir)
	if err != nil {
		return nil, err
	}
	if !managed || m == nil || !compatibleLocalHeartbeatRules(dir, version) {
		return nil, fmt.Errorf("no compatible managed local Skills")
	}
	if err = skills.ValidateSignedRelease(m); err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.SHAMatch {
			return nil, fmt.Errorf("managed skill %s has local modifications", entry.Name)
		}
	}
	return &skills.SyncResult{SkillsDir: dir, Source: "local", CLIVersion: m.CLIVersion}, nil
}
