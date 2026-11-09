package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/selfupdate"
	"cli.eigenflux.ai/internal/skills"
)

func codexUpgradeRepairEnabled() bool {
	m := clientMetaForServerName(activeServerName())
	return os.Getenv("EIGENFLUX_SKIP_AGENT_SETUP") != "1" && runtimeProduct(m.Host) == "codex" && m.Mode == "skill"
}

// Runs independently of Feed/Agent heartbeats, including notification-only
// watches. Failures remain local diagnostics and never stop business workers.
func (w *accountWatch) upgradeRepairLoop(ctx context.Context) error {
	if !codexUpgradeRepairEnabled() {
		return nil
	}
	for ctx.Err() == nil {
		if !w.handoff.begin() {
			return nil
		}
		err := w.tryUpgradeRepair(ctx)
		w.handoff.end()
		if err != nil && w.emit != nil {
			_ = w.emit("upgrade_repair_status", map[string]string{"status": "pending", "code": "repair_retry_pending"})
		}
		if !watchPause(ctx, time.Minute) {
			break
		}
	}
	return ctx.Err()
}

func (w *accountWatch) repairFallback(r *upgradeRepair) error {
	if r == nil || w.desktop == nil || r.Notification == "unchanged" {
		return nil
	}
	// Queue IDs survive process restarts; there is one fallback per request, not
	// one toast for every retry. The original owner chat is never guessed.
	title, body := upgradeRepairDesktopCopy(desktopnotify.SystemLanguage())
	noticeKey := r.NoticeID
	if noticeKey == "" {
		noticeKey = "owner-unavailable"
	}
	return w.desktop.Enqueue(desktopnotify.Message{
		ID:    w.scope + ":upgrade-repair:" + r.ID + ":" + noticeKey,
		Title: title,
		Body:  body,
		URL:   strings.TrimRight(w.server.Endpoint, "/") + "/dashboard",
	})
}

func upgradeRepairDesktopCopy(language string) (string, string) {
	if strings.HasPrefix(strings.ToLower(language), "zh") {
		return "EigenFlux 升级后的连接需要完成验收", "请回到原来的 EigenFlux 会话继续修复。Agent 会自动完成检查和修复；确需你的操作时，会一次列清 Hook 审核、权限确认及重启步骤，无需手动运行命令。"
	}
	return "Finish EigenFlux upgrade acceptance", "Return to your original EigenFlux chat. Your Agent will perform the checks and repairs, then combine any required hook review, approval and restart into one message. You do not need to run commands."
}

func (w *accountWatch) tryUpgradeRepair(ctx context.Context) error {
	if !codexUpgradeRepairEnabled() {
		return nil
	}
	if err := w.verifyHandoffIdentity(); err != nil {
		return err
	}
	// A manually started watch may outlive a change of the CLI's active server.
	// Pin request creation to the watch identity, not the new foreground default.
	scopeHash := sha256.Sum256([]byte(w.home + "\x00" + w.server.Name + "\x00" + w.server.Endpoint + "\x00" + w.scope))
	r, err := mutateUpgradeRepairScoped(hex.EncodeToString(scopeHash[:]), true, nil)
	if err != nil {
		return err
	}
	if r.Status == "verified" || r.Status == "awaiting_run" {
		return nil
	}
	if r.Status != "pending" {
		return w.repairFallback(r)
	}
	if r.NextAttempt > time.Now().UnixMilli() {
		return nil
	}
	b, err := dispatch.ReadBinding(w.home, w.server.Name)
	expected := dispatch.Binding{Home: w.home, Server: w.server.Name, Endpoint: w.server.Endpoint, AgentID: w.identity.AgentID, PrincipalID: w.identity.PrincipalID, Scope: w.scope}
	if err != nil || !sameBindingIdentity(b, expected) || b.Host != "codex" || !dispatch.MainNoticeBindingEnabled(b) {
		return w.repairFallback(r)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := syncUpgradeWatchRules(cfg, b, false); err != nil {
		failed, receiptErr := recordUpgradeSkillsFailure(r.ID, err)
		if receiptErr != nil {
			return receiptErr
		}
		if failed.Status == "pending" {
			return nil
		}
		return w.repairFallback(failed)
	}
	application, err := readUpgradeRepairRules(b.SkillsDir, automaticMaintenanceEnabled(cfg, autoSkillSyncKey))
	if err != nil {
		failed, receiptErr := recordUpgradeSkillsFailure(r.ID, err)
		if receiptErr != nil {
			return receiptErr
		}
		if failed.Status == "pending" {
			return nil
		}
		return w.repairFallback(failed)
	}
	cli, err := stableInstalledExecutable()
	if err != nil {
		return err
	}
	prefix, err := nativeHeartbeatCLIPrefix(w.home, w.server.Name, "", "skill")
	if err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"repair_id": r.ID, "cli": cli, "cli_prefix": prefix, "home": w.home, "server": w.server.Name, "agent_id": w.identity.AgentID, "dispatch_thread": b.MainSessionID, "codex_home": b.Env["CODEX_HOME"], "background": true})
	if err != nil {
		return err
	}
	if err := w.verifyHandoffIdentity(); err != nil {
		return err
	}
	currentBinding, err := dispatch.ReadBinding(w.home, w.server.Name)
	if err != nil || !reflect.DeepEqual(currentBinding, b) || !codexUpgradeRepairEnabled() {
		return errWatchConfiguration
	}
	r, err = mutateUpgradeRepair(false, func(current *upgradeRepair) error {
		if current.ID != r.ID || current.Status != "pending" || current.NextAttempt > time.Now().UnixMilli() {
			return errors.New("repair claim changed")
		}
		current.Status, current.Binding, current.DispatchThread = "submitted", b.Revision, b.MainSessionID
		current.Blockers = nil
		current.Attempts++
		return nil
	})
	if err != nil {
		return err
	}
	runner := dispatch.MainNoticeRunner{Target: dispatch.MainNoticeTarget{Host: b.Host, SessionID: b.MainSessionID, HiddenInputEnabled: dispatch.MainNoticeBindingEnabled(b), Binding: b}}
	boundCtx, cancel := w.upgradeRepairContext(ctx, b)
	defer cancel()
	_, runErr := runner.RunMaintenance(boundCtx, string(application), string(data))
	if err := w.verifyHandoffIdentity(); err != nil {
		return err
	}
	currentBinding, err = dispatch.ReadBinding(w.home, w.server.Name)
	if err != nil || !reflect.DeepEqual(currentBinding, b) {
		return errWatchConfiguration
	}
	latest, err := finishUpgradeRepairDispatch(r.ID, runErr)
	if err != nil {
		return err
	}
	if runErr != nil && !dispatch.MainNoticeCanRetry(runErr) {
		return w.repairFallback(latest)
	}
	return nil
}

// CLI publication precedes its companion Skills publication. An intact signed
// pre-repair Skill is a release gap, not corruption. Authenticate the whole
// installed bundle before making that distinction; never execute an unsigned
// fallback or reinterpret missing files in a release claiming this contract.
func readUpgradeRepairRules(dir string, autoSync bool) ([]byte, error) {
	data, err := skills.ReadSignedFile(dir, "ef-onboarding", "references/upgrade-repair.md", 64<<10)
	if err == nil {
		return data, nil
	}
	if errors.Is(err, skills.ErrSyncInProgress) {
		return nil, &upgradeSkillsError{Reason: "skills_sync_in_progress", Err: err}
	}
	failure := &upgradeSkillsError{Reason: "skills_local_validation_failed", Err: err}
	if !os.IsNotExist(err) {
		return nil, failure
	}
	if _, verifyErr := skills.ReadSignedFile(dir, "ef-onboarding", "SKILL.md", 64<<10); verifyErr != nil {
		if errors.Is(verifyErr, skills.ErrSyncInProgress) {
			return nil, &upgradeSkillsError{Reason: "skills_sync_in_progress", Err: verifyErr}
		}
		return nil, failure
	}
	m, manifestErr := skills.ReadLocalManifest(dir)
	if manifestErr != nil || skills.ValidateSignedRelease(m) != nil {
		return nil, failure
	}
	for _, entry := range m.Skills {
		if entry.Name == "ef-onboarding" && selfupdate.ValidVersion(entry.DisplayVersion) && selfupdate.Compare(entry.DisplayVersion, "0.3.40") < 0 {
			if !autoSync {
				return nil, &upgradeSkillsError{Reason: "skills_sync_disabled", Err: err}
			}
			return nil, &upgradeSkillsError{Reason: "skills_release_pending", Err: err}
		}
	}
	// A completed signed swap may have supplied the new file after the first
	// missing-path result. Re-read before treating a new contract as broken.
	data, freshErr := skills.ReadSignedFile(dir, "ef-onboarding", "references/upgrade-repair.md", 64<<10)
	if freshErr == nil {
		return data, nil
	}
	if errors.Is(freshErr, skills.ErrSyncInProgress) {
		return nil, &upgradeSkillsError{Reason: "skills_sync_in_progress", Err: freshErr}
	}
	return nil, &upgradeSkillsError{Reason: "skills_local_validation_failed", Err: freshErr}
}

func (w *accountWatch) upgradeRepairContext(parent context.Context, b dispatch.Binding) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, err := dispatch.ReadBinding(w.home, w.server.Name)
				if err != nil || !reflect.DeepEqual(current, b) || w.verifyHandoffIdentity() != nil || !codexUpgradeRepairEnabled() {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-done }
}

func recordUpgradeSkillsFailure(id string, cause error) (*upgradeRepair, error) {
	status := "pending"
	var detail *upgradeSkillsError
	if errors.As(cause, &detail) {
		for _, reason := range []string{detail.Reason, detail.FailureReason} {
			switch reason {
			case "skills_local_modified", "skills_local_validation_failed", "skills_target_unresolved", "skills_cli_upgrade_required", "skills_rollback_rejected", "skills_sequence_conflict":
				status = "blocked"
			}
		}
		if detail.Reason == "skills_sync_disabled" {
			status = "deferred"
		}
	}
	return mutateUpgradeRepair(false, func(r *upgradeRepair) error {
		if r.ID != id || r.Status != "pending" {
			return errors.New("repair request changed")
		}
		setRepairOutcome(r, status, []string{"skills_unavailable"})
		if status == "pending" {
			r.NextAttempt = time.Now().Add(5 * time.Minute).UnixMilli()
		}
		return nil
	})
}

func finishUpgradeRepairDispatch(id string, runErr error) (*upgradeRepair, error) {
	return mutateUpgradeRepair(false, func(r *upgradeRepair) error {
		if r.ID != id {
			return errors.New("repair identity changed")
		}
		// The Agent can report combined actions while its turn is running, or a
		// real scheduler can finish acceptance. Never overwrite either receipt.
		if r.Status != "submitted" {
			return nil
		}
		if runErr != nil && dispatch.MainNoticeCanRetry(runErr) {
			r.Status = "pending"
			delay := time.Minute
			if r.Attempts >= 5 {
				delay = 5 * time.Minute
			}
			r.NextAttempt = time.Now().Add(delay).UnixMilli()
			return nil
		}
		// Submission/completion without a report is uncertain, not permission to
		// repeat the maintenance turn. Foreground continuation recovers receipts.
		setRepairOutcome(r, "blocked", []string{"host_unavailable"})
		return nil
	})
}
