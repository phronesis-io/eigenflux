package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
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

func (w *accountWatch) repairDesktopMessage(r *upgradeRepair, b dispatch.Binding, language string) (desktopnotify.Message, bool) {
	if r == nil || r.Status != "user_action" || r.Acknowledged == repairNoticeHash(*r) {
		return desktopnotify.Message{}, false
	}
	if b.Host != "codex" || b.MainSessionID == "" || b.MainSessionID != r.DispatchThread ||
		b.Revision != r.Binding {
		return desktopnotify.Message{}, false
	}
	var actions, labels []string
	zh := strings.HasPrefix(strings.ToLower(language), "zh")
	for _, action := range []struct{ key, en, zh string }{
		{"hook_review", "review the EigenFlux hooks", "审核 EigenFlux Hook"},
		{"policy_approval", "review the execution permission", "确认执行权限"},
		{"host_restart", "restart Codex", "重启 Codex"},
	} {
		if !slices.Contains(r.Blockers, action.key) {
			continue
		}
		actions = append(actions, action.key)
		label := action.en
		if zh {
			label = action.zh
		}
		labels = append(labels, label)
	}
	if len(actions) == 0 || slices.Contains(r.Blockers, "scheduler_paused") || b.ValidateMainSession() != nil {
		return desktopnotify.Message{}, false
	}
	title := "EigenFlux needs your help to finish the update"
	body := "Open Codex to " + strings.Join(labels, ", then ") + "."
	if zh {
		title = "EigenFlux 更新需要你确认"
		body = "打开 Codex，继续" + strings.Join(labels, "、") + "。"
	}
	key := sha256.Sum256([]byte(b.MainSessionID + "\x00" + strings.Join(actions, ",")))
	return desktopnotify.Message{
		ID:    w.scope + ":upgrade-repair:" + r.ID + ":action-" + hex.EncodeToString(key[:]),
		Title: title,
		Body:  body,
		URL:   "codex://threads/" + b.MainSessionID,
	}, true
}

func (w *accountWatch) upgradeRepairBinding() (dispatch.Binding, bool) {
	b, err := dispatch.ReadBinding(w.home, w.server.Name)
	expected := dispatch.Binding{Home: w.home, Server: w.server.Name, Endpoint: w.server.Endpoint, AgentID: w.identity.AgentID, PrincipalID: w.identity.PrincipalID, Scope: w.scope}
	return b, err == nil && sameBindingIdentity(b, expected) && b.Host == "codex"
}

func (w *accountWatch) enqueueUpgradeRepairDesktop(r *upgradeRepair) error {
	if w.desktop != nil && r != nil && r.Status == "user_action" {
		if b, ok := w.upgradeRepairBinding(); ok {
			if message, ok := w.repairDesktopMessage(r, b, desktopnotify.SystemLanguage()); ok {
				return w.desktop.Enqueue(message)
			}
		}
	}
	return nil
}

func (w *accountWatch) repairDesktopCurrent(id string) (bool, error) {
	prefix := w.scope + ":upgrade-repair:"
	rest, ok := strings.CutPrefix(id, prefix)
	if !ok {
		return true, nil
	}
	_, key, _ := strings.Cut(rest, ":")
	if !strings.HasPrefix(key, "action-") || !codexUpgradeRepairEnabled() {
		return false, nil
	}
	path, scope, err := upgradeRepairPath()
	if err != nil {
		return false, err
	}
	r, err := readUpgradeRepair(path, scope)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	b, ok := w.upgradeRepairBinding()
	if !ok {
		return false, nil
	}
	message, ok := w.repairDesktopMessage(&r, b, "en")
	return ok && message.ID == id, nil
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
		return w.enqueueUpgradeRepairDesktop(r)
	}
	if r.NextAttempt > time.Now().UnixMilli() {
		return nil
	}
	b, ok := w.upgradeRepairBinding()
	if !ok || !dispatch.MainNoticeBindingEnabled(b) {
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := syncUpgradeWatchRules(cfg, b, false); err != nil {
		_, receiptErr := recordUpgradeSkillsFailure(r.ID, err)
		return receiptErr
	}
	application, err := readUpgradeRepairRules(b.SkillsDir, automaticMaintenanceEnabled(cfg, autoSkillSyncKey))
	if err != nil {
		_, receiptErr := recordUpgradeSkillsFailure(r.ID, err)
		return receiptErr
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
	runErr := dispatch.ErrMainNoticeBusy
	if w.mainNoticeRunMu.TryLock() {
		_, runErr = runner.RunMaintenance(boundCtx, string(application), string(data))
		w.mainNoticeRunMu.Unlock()
	}
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
	if ctx.Err() != nil {
		return nil
	}
	return w.enqueueUpgradeRepairDesktop(latest)
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
			if errors.Is(runErr, dispatch.ErrMainNoticeBusy) {
				r.UnavailableAttempts = 0
			} else {
				r.UnavailableAttempts++
			}
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
