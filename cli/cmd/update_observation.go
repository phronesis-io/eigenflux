package cmd

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/selfupdate"
)

const updateFromVersionEnv = "EIGENFLUX_UPDATE_FROM_VERSION"

type maintenanceObservationStatus struct {
	AttemptID string `json:"attempt_id"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
}

type maintenanceRecorder struct {
	scope                                                        maintenance.Scope
	principal, endpoint, host, mode, component, trigger, attempt string
	started                                                      time.Time
	lastAt                                                       int64
	pending                                                      map[string]bool
	authenticated, recorded, recordFailed                        bool
	diagnostics                                                  io.Writer
}

func newMaintenanceRecorder(server, component, trigger, attempt string, diagnostics io.Writer) *maintenanceRecorder {
	if attempt == "" {
		attempt = maintenance.NewID()
	}
	if diagnostics == nil {
		diagnostics = os.Stderr
	}
	r := &maintenanceRecorder{component: component, trigger: trigger, attempt: attempt, started: time.Now(), diagnostics: diagnostics, pending: map[string]bool{}}
	credentials, err := auth.LoadV2Credentials(server)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			r.failure("credentials_unreadable")
		}
		return r
	}
	r.scope = maintenance.Scope{Home: config.HomeDir(), Server: server, AgentID: credentials.AgentID}
	r.principal = credentials.PrincipalID
	cfg, err := config.Load()
	if err == nil {
		var target *config.Server
		target, err = cfg.GetActive(server)
		if err == nil {
			r.endpoint = target.Endpoint
		}
	}
	r.authenticated = err == nil && r.endpoint != ""
	if !r.authenticated {
		r.failure("configuration_unreadable")
		return r
	}
	meta := clientMetaForServerName(server)
	r.host, r.mode = runtimeProduct(meta.Host), meta.Mode
	return r
}

func maintenanceIdentity(scope maintenance.Scope, principal, endpoint string) (*auth.V2Credentials, error) {
	currentHome, _ := filepath.EvalSymlinks(config.HomeDir())
	pinnedHome, _ := filepath.EvalSymlinks(scope.Home)
	if currentHome == "" || pinnedHome == "" || currentHome != pinnedHome {
		return nil, fmt.Errorf("maintenance Home changed")
	}
	credentials, err := auth.LoadV2Credentials(scope.Server)
	if err != nil || credentials.AgentID != scope.AgentID || credentials.PrincipalID != principal {
		return nil, fmt.Errorf("maintenance identity changed")
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	server, err := cfg.GetActive(scope.Server)
	if err != nil || server.Endpoint != endpoint {
		return nil, fmt.Errorf("maintenance endpoint changed")
	}
	return credentials, nil
}

func (r *maintenanceRecorder) failure(code string) {
	r.recordFailed = true
	fmt.Fprintf(r.diagnostics, "[eigenflux:maintenance] %s attempt=%s\n", code, r.attempt)
}

func (r *maintenanceRecorder) Record(event maintenance.Event) {
	if !r.authenticated {
		return
	}
	if _, err := maintenanceIdentity(r.scope, r.principal, r.endpoint); err != nil {
		r.failure("identity_changed")
		return
	}
	event.Host, event.Mode = r.host, r.mode
	if event.EventAt <= r.lastAt {
		event.EventAt = r.lastAt + 1
	}
	r.lastAt = event.EventAt
	if err := maintenance.Record(r.scope, event); err != nil {
		r.failure("record_failed")
		return
	}
	r.recorded = true
	r.pending[event.EventID] = true
}

func (r *maintenanceRecorder) Phase(phase, from, target string) {
	r.Finish(phase, "started", from, target, "", "")
}

func (r *maintenanceRecorder) Finish(phase, result, from, target, running, code string) {
	event := maintenance.NewEvent(r.attempt, r.component, r.trigger, phase, result)
	event.FromVersion, event.ToVersion, event.RunningVersion = from, target, running
	event.DurationMS, event.ErrorCode = time.Since(r.started).Milliseconds(), code
	r.Record(event)
}

func (r *maintenanceRecorder) Progress(from string) func(selfupdate.Progress) {
	return func(progress selfupdate.Progress) { r.Phase(progress.Phase, from, progress.TargetVersion) }
}

func (r *maintenanceRecorder) Complete(result selfupdate.Result, from string) {
	if !result.Attempted && result.Status != "failed" && result.Status != "unconfigured" && result.Status != "updated" {
		return
	}
	phase, outcome, code := result.Phase, "no_update", ""
	if phase == "" {
		phase = "check"
	}
	target := result.TargetVersion
	switch result.Status {
	case "updated":
		outcome = "installed"
		if target == "" {
			target = result.Version
		}
	case "failed":
		outcome, code = "failed", "update_failed"
	case "unconfigured":
		outcome, code = "blocked", "trust_key_missing"
	}
	if result.RolledBack {
		outcome, code = "rolled_back", "probe_failed"
	}
	if !result.RolledBack && (strings.Contains(result.Error, "permission") || strings.Contains(result.Error, "access is denied") || strings.Contains(result.Error, "existing binary retained")) {
		outcome, code = "blocked", "installation_blocked"
	}
	r.Finish(phase, outcome, from, target, "", code)
}

func (r *maintenanceRecorder) Flush() {
	if !r.authenticated || !r.recorded {
		return
	}
	sent, err := flushMaintenanceBatch(r.scope, r.principal, r.endpoint)
	if err != nil {
		fmt.Fprintf(r.diagnostics, "[eigenflux:maintenance] upload_pending attempt=%s\n", r.attempt)
		return
	}
	for _, event := range sent {
		delete(r.pending, event.EventID)
	}
}

func resumedCLIRecorder(server, trigger string, diagnostics io.Writer) *maintenanceRecorder {
	attempt := os.Getenv(updateAttemptEnv)
	r := newMaintenanceRecorder(server, "cli", trigger, attempt, diagnostics)
	if attempt == "" {
		return r
	}
	from := os.Getenv(updateFromVersionEnv)
	if !selfupdate.ValidVersion(from) {
		r.failure("invalid_update_context")
		return r
	}
	r.Finish("execute", "executed", from, version, version, "")
	return r
}

func (r *maintenanceRecorder) Status() maintenanceObservationStatus {
	status := maintenanceObservationStatus{AttemptID: r.attempt, Status: "local_only"}
	if r.recordFailed {
		status.Status, status.ErrorCode = "record_failed", "maintenance_record_failed"
		return status
	}
	if !r.authenticated {
		return status
	}
	status.Status = "not_attempted"
	if !r.recorded {
		return status
	}
	status.Status = "reported"
	if len(r.pending) > 0 {
		status.Status = "queued"
	}
	return status
}

// Upload uses the existing token; normal CLI business owns credential refresh.
// An expired or rejected token leaves the bounded queue for a later invocation.
func flushMaintenanceForIdentity(scope maintenance.Scope, principal, endpoint string) error {
	_, err := flushMaintenanceBatch(scope, principal, endpoint)
	return err
}

func flushMaintenanceBatch(scope maintenance.Scope, principal, endpoint string) ([]maintenance.Event, error) {
	var sent []maintenance.Event
	err := maintenance.Flush(scope, time.Now(), func(events []maintenance.Event) error {
		credentials, err := maintenanceIdentity(scope, principal, endpoint)
		if err != nil {
			return err
		}
		if credentials.ExpiresAt <= time.Now().UnixMilli() {
			return fmt.Errorf("maintenance upload awaits valid credentials")
		}
		c := client.New(strings.TrimRight(endpoint, "/")+"/api/v2", credentials.AccessToken, version, clientMetaForServerName(scope.Server))
		c.HTTPClient = &http.Client{Timeout: 5 * time.Second}
		_, err = c.Post("/maintenance/events:batch", map[string]interface{}{"events": events})
		if err == nil {
			sent = events
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return sent, nil
}
