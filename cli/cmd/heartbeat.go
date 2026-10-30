package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"os"
	"path/filepath"
	"slices"
	"strings"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/output"
	"cli.eigenflux.ai/internal/selfupdate"
	"cli.eigenflux.ai/internal/skills"

	"github.com/spf13/cobra"
)

const heartbeatContractVersion = "eigenflux_heartbeat.v4"

type heartbeatPlan struct {
	FeedCompletionCheck      bool                       `json:"feed_completion_check"`
	MainSessionBinding       *mainSessionBindingPlan    `json:"main_session_binding,omitempty"`
	WatchManaged             bool                       `json:"watch_managed,omitempty"`
	DispatchOwned            []string                   `json:"dispatch_owned,omitempty"`
	SkillsReadReceipt        *maintenance.Event         `json:"skills_read_receipt,omitempty"`
	PlanMode                 string                     `json:"plan_mode,omitempty"`
	PluginMaintenance        pluginMaintenance          `json:"plugin_maintenance"`
	CLIUpdate                selfupdate.Result          `json:"cli_update"`
	OuterLoop                *updateWatchRecovery       `json:"outer_loop,omitempty"`
	Notifications            *upgradeNotificationStatus `json:"notifications,omitempty"`
	AgentPrompt              string                     `json:"agent_prompt"`
	WakeOnEmpty              bool                       `json:"wake_on_empty"`
	Access                   runtimeAccess              `json:"access"`
	SchemaVersion            string                     `json:"schema_version"`
	HeartbeatContractVersion string                     `json:"heartbeat_contract_version"`
	CLIVersion               string                     `json:"cli_version"`
	SkillRevision            string                     `json:"skill_revision"`
	SkillsTarget             string                     `json:"skills_target"`
	Skills                   []string                   `json:"skills"`
	RuleSources              []string                   `json:"rule_sources"`
	ExecutionOrder           []string                   `json:"execution_order"`
	CLIPrefix                string                     `json:"cli_prefix"`
	SchedulerLauncher        string                     `json:"scheduler_launcher"`
	SchedulerPrompt          string                     `json:"scheduler_prompt"`
	SchedulerMigration       string                     `json:"scheduler_migration"`
	SkillsFresh              bool                       `json:"skills_fresh"`
	CompatibilityReported    bool                       `json:"compatibility_reported"`
	CompatibilityError       string                     `json:"compatibility_error,omitempty"`
	RuntimeReport            runtimeReportResult        `json:"runtime_report"`
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
	RunE: func(cmd *cobra.Command, _ []string) (resultErr error) {
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
		flushMaintenance := !controlOnly
		if !controlOnly {
			if maintenanceOnly {
				markMaintenanceAttempt(activeServerName())
			}
			defer func() {
				if flushMaintenance && !restarted && !isV2CredentialError(resultErr) && !v2CredentialNeedsLogin(resultErr) && !isAgentAuthUnavailable(resultErr) {
					_ = flushMaintenanceEvents(activeServerName())
				}
			}()
			cliUpdate, restarted, updateErr = updateHeartbeatCLI(cmd, cfg, "")
		}
		if restarted || updateErr != nil {
			return updateErr
		}
		// Public recovery rules remain available before authenticated work.
		meta := clientMetaForServerName(activeServerName())
		skillsScope, _ := maintenanceScope(activeServerName())
		skillsAttempt := maintenance.NewID()
		recordSkills := func(event maintenance.Event) {
			if err := recordMaintenanceEventAtScope(skillsScope, event); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "[eigenflux:maintenance] component=skills attempt_id=%s phase=%s error_code=maintenance_record_failed\n", skillsAttempt, event.Phase)
			}
		}
		skillsFinished := controlOnly
		defer func() {
			if !skillsFinished {
				event := maintenance.NewEvent(skillsAttempt, "skills", "auto", "install", "failed")
				event.ErrorCode = heartbeatSkillsFailureCode(resultErr)
				switch event.ErrorCode {
				case "skills_cli_incompatible", "skills_signature_failed", "skills_manifest_unavailable", "skills_rule_missing", "skills_local_modified", "skills_auth_blocked":
					event.Phase = "check"
				}
				// Authentication failures already have a structured recovery notice.
				if event.ErrorCode != "skills_auth_blocked" {
					fmt.Fprintf(cmd.ErrOrStderr(), "[eigenflux:maintenance] component=skills attempt_id=%s phase=%s error_code=%s cli_version=%s\n", skillsAttempt, event.Phase, event.ErrorCode, version)
				}
				recordSkills(event)
			}
		}()
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
				skillsFinished = restarted
				return updateErr
			}
			if !compatibleLocalHeartbeatRules(res.SkillsDir, version) {
				return &heartbeatSkillsError{code: "skills_cli_incompatible", err: fmt.Errorf("heartbeat needs CLI >= %s (have %s); automatic update %s reason=%s", res.RequiredCLIVersion, version, cliUpdate.Status, cliUpdate.Reason)}
			}
			err = nil // Continue only the already-installed compatible rules.
		}
		runtimeReport := runtimeReportResult{Status: "skipped"}
		if !controlOnly {
			var reportErr error
			runtimeReport, reportErr = reportRuntimeSettings(cfg, "", "", "", "", false)
			if isV2CredentialError(reportErr) || v2CredentialNeedsLogin(reportErr) || isAgentAuthUnavailable(reportErr) {
				return reportErr
			}
		}
		if err != nil || res == nil {
			return fmt.Errorf("heartbeat plan: skills sync failed: %w", err)
		}
		manifest, err := skills.ReadLocalManifest(res.SkillsDir)
		if err != nil || manifest == nil || manifest.Revision == "" {
			return &heartbeatSkillsError{code: "skills_manifest_unavailable", err: fmt.Errorf("heartbeat plan: no verified Skills manifest in %s", res.SkillsDir)}
		}
		if err := skills.ValidateSignedRelease(manifest); err != nil {
			return &heartbeatSkillsError{code: "skills_signature_failed", err: fmt.Errorf("local Skills validation failed (CLI %s, required CLI %q, manifest revision %q): %w", version, manifest.MinCLIVersion, manifest.Revision, err)}
		}
		if err := heartbeatSkillsCompatibility(manifest); err != nil {
			return err
		}
		if len(res.Preserved) > 0 {
			return &heartbeatSkillsError{code: "skills_local_modified", err: fmt.Errorf("heartbeat plan: managed Skills have local edits and were not upgraded: %s", strings.Join(res.Preserved, ", "))}
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
				return &heartbeatSkillsError{code: "skills_rule_missing", err: fmt.Errorf("heartbeat plan: required rule source is missing: %s", source)}
			}
		}

		var skillsReadReceipt *maintenance.Event
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
				skillsReadReceipt = &receipt
			}
			recordSkills(e)
			skillsFinished = true
		}
		access, err := runtimeAccessForServer(activeServerName())
		if err != nil {
			return err
		}
		flushMaintenance = !controlOnly

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
			pluginMaintenance = scheduleOpenClawUpdate(pluginMaintenance)
		}
		// Mark commands from this cycle, but not the persisted launcher.
		cliPrefix += " --origin " + publishOriginHeartbeat
		plan := heartbeatPlan{
			FeedCompletionCheck: access.OnboardingState == "completed" && !controlOnly && !maintenanceOnly,
			PlanMode:            planMode,
			SkillsReadReceipt:   skillsReadReceipt,
			WatchManaged:        watchManaged,
			PluginMaintenance:   pluginMaintenance,
			CLIUpdate:           cliUpdate,
			SchemaVersion:       "eigenflux_heartbeat_plan.v1", HeartbeatContractVersion: heartbeatContractVersion,
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
			plan.MainSessionBinding, err = inspectMainSessionBinding()
			if err != nil {
				return err
			}
			if plan.MainSessionBinding != nil && plan.MainSessionBinding.Status != "ready" {
				source := filepath.Join(res.SkillsDir, "ef-communication", "references", "main-session.md")
				if _, ruleErr := skills.ReadSignedFile(res.SkillsDir, "ef-communication", "references/main-session.md", 64<<10); ruleErr == nil {
					plan.RuleSources = append(plan.RuleSources, source)
					if plan.MainSessionBinding.Status == "bootstrap_required" && planMode == "full" &&
						access.OnboardingState == "completed" && meta.Host == "codex" && meta.Mode == "skill" {
						plan.ExecutionOrder = append([]string{"main_session_bootstrap"}, plan.ExecutionOrder...)
					}
				} else {
					plan.MainSessionBinding.Error = "main session rules missing or unsigned; sync compatible signed Skills before binding"
				}
				plan.WakeOnEmpty = true
			}
		}

		for _, entry := range manifest.Skills {
			plan.Skills = append(plan.Skills, entry.Name)
		}

		// Only this cycle's verified official manifest may create Console upgrade
		// evidence. Lock contention, an offline fallback, a rollback rejection, or
		// a provisional local bundle can render a plan but cannot unlock V2.
		if !controlOnly && res.VerifiedManifest && access.OnboardingState == "completed" {
			if err := pushHeartbeatCompatibility(cfg, heartbeatContractVersion, manifest.Revision); err != nil {
				if isV2CredentialError(err) || v2CredentialNeedsLogin(err) || isAgentAuthUnavailable(err) {
					flushMaintenance = false
					return err
				}
				plan.CompatibilityError = err.Error()
			} else {
				plan.CompatibilityReported = true
			}
		}

		if !controlOnly && access.OnboardingState == "completed" {
			notifications := upgradeNotifications(cmd, false)
			plan.Notifications = &notifications
			recovery := recoverWatchAfterUpdate(cmd, cfg, false)
			plan.OuterLoop = &recovery
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

// Error codes are fixed local classifications; raw errors never enter observations.
type heartbeatSkillsError struct {
	code string
	err  error
}

func (e *heartbeatSkillsError) Error() string { return e.err.Error() }
func (e *heartbeatSkillsError) Unwrap() error { return e.err }

func heartbeatSkillsCompatibility(manifest *skills.Manifest) error {
	minimum := manifest.MinCLIVersion
	if minimum == "" || (selfupdate.ValidVersion(version) && selfupdate.ValidVersion(minimum) && selfupdate.Compare(version, minimum) >= 0) {
		return nil
	}
	return &heartbeatSkillsError{code: "skills_cli_incompatible", err: fmt.Errorf("no compatible managed local Skills (CLI %s, required CLI %q, manifest revision %q)", version, minimum, manifest.Revision)}
}

func heartbeatSkillsFailureCode(err error) string {
	if isV2CredentialError(err) || v2CredentialNeedsLogin(err) || isAgentAuthUnavailable(err) {
		return "skills_auth_blocked"
	}
	var failure *heartbeatSkillsError
	if errors.As(err, &failure) {
		return failure.code
	}
	if errors.Is(err, os.ErrPermission) {
		return "skills_permission_denied"
	}
	if err != nil {
		message := err.Error()
		switch {
		case strings.Contains(message, "skills manifest signature"), strings.Contains(message, "skills manifest signing key"), strings.Contains(message, "release revision does not match"):
			return "skills_signature_failed"
		case strings.Contains(message, "signed manifest rollback rejected"), strings.Contains(message, "signed manifest sequence reused"):
			return "skills_rollback_rejected"
		}
	}
	return "skills_plan_failed"
}

func heartbeatWakeOnEmpty(access runtimeAccess, maintenance pluginMaintenance) bool {
	return access.OnboardingState == "completed" || maintenance.Due
}

// Disabled automatic sync uses only signed, unchanged, compatible local rules.
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
	if !managed || m == nil {
		return nil, &heartbeatSkillsError{code: "skills_manifest_unavailable", err: fmt.Errorf("no managed local Skills manifest (CLI %s)", version)}
	}
	context := fmt.Sprintf("CLI %s, required CLI %q, manifest revision %q", version, m.MinCLIVersion, m.Revision)
	if err = skills.ValidateSignedRelease(m); err != nil {
		return nil, &heartbeatSkillsError{code: "skills_signature_failed", err: fmt.Errorf("local Skills validation failed (%s): %w", context, err)}
	}
	if err := heartbeatSkillsCompatibility(m); err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.SHAMatch {
			return nil, &heartbeatSkillsError{code: "skills_local_modified", err: fmt.Errorf("managed skill %s has local modifications (%s)", entry.Name, context)}
		}
	}
	return &skills.SyncResult{SkillsDir: dir, Source: "local", CLIVersion: m.CLIVersion}, nil
}

func heartbeatStages(access runtimeAccess) []string {
	if access.OnboardingState != "completed" {
		return []string{"feed"}
	}
	return []string{"commands", "feed", "attention", "communication", "publish", "need_capture", "settings_report", "commission_confirmations"}
}

func renderHeartbeatPlanForAgent(plan heartbeatPlan) string {
	receiptText := ""
	if plan.PluginMaintenance.Executor == "cli" {
		receiptText += "\nOpenClaw plugin maintenance is executed by the independent CLI worker. Do not run plugin discovery, installation, restart, or plugin-check from this Agent turn. Cached maintenance results are historical observations.\n"
	}
	if plan.MainSessionBinding != nil {
		b, _ := json.Marshal(plan.MainSessionBinding)
		receiptText += "\nCodex main session binding: " + string(b) + ". Ordinary heartbeat and PM execution cannot select a replacement thread.\n"
		if slices.Contains(plan.ExecutionOrder, "main_session_bootstrap") {
			receiptText += "Execute main_session_bootstrap automatically before business stages using the listed ef-communication/references/main-session.md. Complete the existing preflight, guarded binding update and watch recovery without asking the owner to enable binding. Preserve existing targets; ask only when the verified heartbeat target is missing or ambiguous.\n"
		} else if plan.MainSessionBinding.Status == "unavailable" {
			receiptText += "The established main session is unavailable. Preserve its binding and request an owner-directed switch.\n"
		} else if plan.MainSessionBinding.Status == "bootstrap_required" {
			receiptText += "Bootstrap remains pending for the next full native Codex heartbeat or owner-requested upgrade. Watch-owned narrow handlers must not stop their own watch.\n"
		}
	}
	if plan.WatchManaged {
		receiptText += "\nHost plugin and scheduler maintenance is owned exclusively by the watch maintenance-only handler. Execute only the listed stages and Skills read receipt. A listed main_session_bootstrap is an independent host upgrade step; other host maintenance stays with the watch.\n"
	}
	if len(plan.DispatchOwned) > 0 {
		receiptText += "\nThe bound watch dispatcher exclusively owns these events: " + strings.Join(plan.DispatchOwned, ", ") + ". Skip only the procedures for those listed events in this heartbeat, including indirect Skill references. Preserve the watch binding and its dedicated foreground process except during the listed guarded main_session_bootstrap procedure.\n"
	}
	if slices.Contains(plan.ExecutionOrder, "commission_confirmations") {
		receiptText += "\nAfter this cycle's Feed and other business stages, run order pending-confirmations --format json using the exact CLI prefix. Include its pending-order reminder in the owner-facing result every cycle while orders remain awaiting buyer confirmation, even if Feed is empty or unchanged. Empty or failed checks must not be reported as successful completion; never confirm receipt automatically.\n"
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
	if plan.CLIUpdate.Reason != "" {
		updateStatus += " reason=" + plan.CLIUpdate.Reason
	}
	if plan.CLIUpdate.NextCheckAt != "" {
		updateStatus += " next_check_at=" + plan.CLIUpdate.NextCheckAt
	}
	if plan.CLIUpdate.Error != "" {
		updateStatus += " (" + plan.CLIUpdate.Error + ")"
	}
	completion := ""
	if plan.FeedCompletionCheck {
		completion = "\nBefore leaving the Feed stage, run " + plan.CLIPrefix + " feed status. Score every item it lists, then continue."
		completion += "\nAfter the Feed push, run " + plan.CLIPrefix + " feed status --after-push. Report surface only for listed items the owner was actually shown."
	}
	executorNotice := ""
	if plan.PluginMaintenance.Executor == "cli" {
		executorNotice = "\nOpenClaw plugin maintenance is executed by the independent CLI worker. Do not run plugin discovery, installation, restart, or plugin-check from this Agent turn. Cached maintenance results are historical observations.\n"
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
4. Runtime access: %s. Execute the available stages in order: %s. Apply the current Skills to each stage. A Feed payload supplied by the host is this cycle's completed pull. feed next reads this cycle's saved page and is not a pull. A Feed batch names its output contract file; read that file completely before processing the batch.
%s
5. Follow the current Skills for onboarding restrictions and recovery. Host harness output and notification requirements take precedence over Skill silence tokens and display conventions.

SCHEDULER
Permanent launcher: %s
Migration: %s
Native task prompt: %s
For new native tasks, the scheduler stores this fixed execution prompt. Store it verbatim, without additions. Reuse working existing triggers, including legacy EIGENFLUX_MODE launchers; use the current Skills to decide whether a repair is necessary. Verified plugin loops may supply mode through their existing process environment.
`, plan.HeartbeatContractVersion, plan.CLIVersion, plan.SkillRevision, plan.SkillsTarget,
		strings.Join(plan.Skills, ", "), plan.CLIPrefix, runtimeStatus, plan.CompatibilityReported, updateStatus, pluginInfo, "- "+strings.Join(plan.RuleSources, "\n- "), plan.Access.Mode, strings.Join(plan.ExecutionOrder, " → "), completion,
		plan.SchedulerLauncher, plan.SchedulerMigration, heartbeatSchedulerPrompt(plan.SchedulerLauncher)) + receiptText + executorNotice
}

func heartbeatSchedulerPrompt(launcher string) string {
	return "Run one EigenFlux heartbeat cycle. Execute directly: " + launcher + ". Freshly read its installed rule sources and follow its plan in this run. Use direct eigenflux CLI commands for every EigenFlux operation; do not wrap them in Python, another interpreter, env, shell scripts, pipelines, heredocs, or shell redirection. Use CLI flags for runtime metadata and JSON input. Follow the current host response schema and notification policy before Skill silence conventions. Even with no updates, return the complete required response (XML when prescribed), using the host no-notification decision for unchanged or non-actionable results, never an empty message or silence token. Routine cycle completion alone does not warrant notification. Authentication failure stops this check and requires the host failure notification; use NOTIFY for Codex, state that EigenFlux checks are blocked, and provide the CLI recovery instruction. Do not suppress unresolved authentication failures as unchanged results or repeat an automatic recovery already attempted by the CLI. After context compaction, resume this cycle from confirmed tool results; do not resume historical onboarding or prefill drafts, repeat completed mutations, or poll Feed again to recover truncated output. Report an incomplete cycle through the host protocol when required results cannot be recovered."
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
