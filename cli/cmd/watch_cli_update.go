package cmd

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"runtime"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/selfupdate"
	"cli.eigenflux.ai/internal/skills"
)

// Check independently of Agent turns and heartbeat plans, including notification-only watches.
func (w *accountWatch) cliUpdateLoop(ctx context.Context) error {
	key, _ := base64.StdEncoding.DecodeString(skills.VerifyPublicKeyBase64)
	if !selfupdate.ValidVersion(version) {
		return w.emitCLIUpdateResult(selfupdate.Result{Status: "skipped", Reason: "invalid_version"}, version, "")
	}
	if len(key) != ed25519.PublicKeySize {
		return w.emitCLIUpdateResult(selfupdate.Result{Status: "unconfigured", Reason: "trust_key_missing"}, version, "")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	options := selfupdate.Options{
		CompletePackage: runtime.GOOS == "darwin" || runtime.GOOS == "windows",
		Home:            w.home, Executable: executable, Version: version, CDN: cdnBase(), Key: key,
	}
	for ctx.Err() == nil {
		done, err := w.checkCLIUpdate(ctx, options)
		if err != nil || done {
			return err
		}
		if !watchPause(ctx, time.Minute) {
			break
		}
	}
	return ctx.Err()
}

func (w *accountWatch) checkCLIUpdate(ctx context.Context, options selfupdate.Options) (bool, error) {
	if !w.handoff.begin() {
		return true, nil
	}
	defer w.handoff.end()
	if err := w.verifyHandoffIdentity(); err != nil {
		return false, err
	}
	cfg, err := config.Load()
	if err != nil {
		return false, err
	}
	if !serverMaintenanceEnabled(cfg, w.server.Name, "auto_cli_update") {
		return false, w.emitCLIUpdateResult(selfupdate.Result{Status: "skipped", Reason: "automatic_update_disabled"}, options.Version, "")
	}
	recorder := newMaintenanceRecorder(w.server.Name, "cli", "auto", "", os.Stderr)
	options.Home = w.home
	options.OnProgress = recorder.Progress(options.Version)
	result := selfupdate.Check(ctx, options)
	recorder.Complete(result, options.Version)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err := w.verifyHandoffIdentity(); err != nil {
		return false, err
	}
	if err := w.emitCLIUpdateResult(result, options.Version, recorder.attempt); err != nil {
		return false, err
	}
	if result.Status == "updated" {
		w.handoff.request(result.Version)
		return true, nil
	}
	return false, nil
}

func (w *accountWatch) emitCLIUpdateResult(result selfupdate.Result, from, attempt string) error {
	decision := result.Status + "\x00" + result.Reason + "\x00" + result.NextCheckAt
	if !result.Attempted && result.Status != "updated" && decision == w.cliUpdateDecision {
		return nil
	}
	// Repeat neither the same deferred decision nor raw provider error text.
	if err := w.emit("cli_update_status", map[string]interface{}{
		"status": result.Status, "phase": result.Phase, "reason": result.Reason,
		"next_check_at": result.NextCheckAt, "from_version": from,
		"target_version": result.TargetVersion, "attempt_id": attempt,
	}); err != nil {
		return err
	}
	w.cliUpdateDecision = decision
	return nil
}
