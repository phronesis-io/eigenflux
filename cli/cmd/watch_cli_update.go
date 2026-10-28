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
	if len(key) != ed25519.PublicKeySize || !selfupdate.ValidVersion(version) {
		return nil
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
		return false, nil
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
	if result.Attempted || result.Status == "updated" || result.Status == "failed" || result.Status == "unconfigured" {
		// Raw provider errors stay out of the event stream and its Agent journal.
		if err := w.emit("cli_update_status", map[string]interface{}{
			"status": result.Status, "phase": result.Phase,
			"from_version": options.Version, "target_version": result.TargetVersion,
			"attempt_id": recorder.attempt,
		}); err != nil {
			return false, err
		}
	}
	if result.Status == "updated" {
		w.handoff.request(result.Version)
		return true, nil
	}
	return false, nil
}
