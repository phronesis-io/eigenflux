package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// RunMaintenance submits signed maintenance instructions supplied by the caller
// to the existing bound Codex owner. It grants no execution, policy-write or
// hook-trust authority: the native turn inherits the thread's existing settings.
// Data remains untrusted. A successful silent turn is supported; its completion
// receipt does not assert that any installation or scheduler repair succeeded.
// Callers may retry only MainNoticeCanRetry errors, never an uncertain write.
func (r MainNoticeRunner) RunMaintenance(ctx context.Context, application, data string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	t := r.Target
	if t.Host != "codex" || t.Binding.Host != "codex" || t.Binding.Mode != "native" ||
		!t.HiddenInputEnabled || !MainNoticeBindingEnabled(t.Binding) || !filepath.IsAbs(t.Binding.Env["CODEX_HOME"]) {
		return Result{}, ErrMainNoticeUnsupported
	}
	if t.SessionID == "" || t.SessionID != t.Binding.MainSessionID || len(t.SessionID) > 4096 ||
		strings.HasPrefix(t.SessionID, "-") || strings.ContainsRune(t.SessionID, 0) || !utf8.ValidString(t.SessionID) {
		return Result{}, ErrMainSessionMismatch
	}
	if strings.TrimSpace(application) == "" || !utf8.ValidString(application) || !utf8.ValidString(data) ||
		len(application) > maxRunnerOutput || len(data) > maxRunnerOutput || len(application)+len(data) > maxRunnerOutput ||
		!json.Valid([]byte(data)) {
		return Result{}, errors.New("invalid_maintenance_envelope")
	}
	timeout := t.Binding.TimeoutSeconds
	if timeout <= 0 {
		timeout = 300
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	return r.runCodexEnvelope(ctx, application, data, true)
}
