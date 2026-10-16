package cmd

import (
	"context"
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

const updateObservationFailedEnv = "EIGENFLUX_UPDATE_OBSERVATION_FAILED"

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
	initialDropped                                               uint64
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
	r := &maintenanceRecorder{component: component, trigger: trigger, attempt: attempt, started: time.Now(), diagnostics: diagnostics}
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
	state, err := maintenance.Snapshot(r.scope)
	if err != nil {
		r.failure("maintenance_state_failed")
	} else {
		r.initialDropped = state.Dropped
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
	if event.Component == "cli" && (event.Result == "installed" || event.Result == "executed") {
		if err := maintenance.RememberCLIAdoption(r.scope, r.principal, event); err != nil {
			r.failure("adoption_record_failed")
		}
	}
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
	if err := flushMaintenanceForIdentity(r.scope, r.principal, r.endpoint); err != nil {
		fmt.Fprintf(r.diagnostics, "[eigenflux:maintenance] upload_pending attempt=%s\n", r.attempt)
	}
}

func resumedCLIRecorder(server string, diagnostics io.Writer) *maintenanceRecorder {
	r := newMaintenanceRecorder(server, "cli", "adoption", os.Getenv(updateAttemptEnv), diagnostics)
	if os.Getenv(updateObservationFailedEnv) == "1" {
		r.recordFailed = true
	}
	if !r.authenticated {
		return r
	}
	previous, found, err := maintenance.CLIAdoptionFor(r.scope, r.principal, version)
	if err != nil || !found || previous.AttemptID != r.attempt {
		r.failure("execution_receipt_missing")
		return r
	}
	r.trigger = previous.Trigger
	r.started = time.UnixMilli(previous.EventAt).Add(-time.Duration(previous.DurationMS) * time.Millisecond)
	r.lastAt = previous.EventAt
	r.Finish("execute", "executed", previous.FromVersion, version, version, "")
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
	state, err := maintenance.Snapshot(r.scope)
	if err != nil {
		status.Status, status.ErrorCode = "record_failed", "maintenance_state_failed"
		return status
	}
	if state.Dropped > r.initialDropped {
		status.Status, status.ErrorCode = "record_failed", "maintenance_events_dropped"
		return status
	}
	status.Status = "reported"
	for _, event := range state.Events {
		if event.AttemptID == r.attempt {
			status.Status = "queued"
			break
		}
	}
	return status
}

func flushMaintenanceForIdentity(scope maintenance.Scope, principal, endpoint string) error {
	return maintenance.Flush(scope, time.Now(), func(events []maintenance.Event) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		credentials, err := maintenanceIdentity(scope, principal, endpoint)
		if err != nil {
			return err
		}
		if credentials.ExpiresAt <= time.Now().Add(30*time.Second).UnixMilli() {
			credentials, err = refreshPinnedV2Credentials(ctx, scope.Server, endpoint, false, scope.AgentID, principal)
			if err != nil {
				return err
			}
		}
		c := client.New(strings.TrimRight(endpoint, "/")+"/api/v2", credentials.AccessToken, version, clientMetaForServerName(scope.Server))
		c.HTTPClient = &http.Client{Timeout: 5 * time.Second, Transport: pinnedRefreshTransport{parent: ctx, base: http.DefaultTransport}}
		c.OnUnauthorized = func() (string, error) {
			if _, err := maintenanceIdentity(scope, principal, endpoint); err != nil {
				return "", err
			}
			refreshed, err := refreshPinnedV2Credentials(ctx, scope.Server, endpoint, true, scope.AgentID, principal)
			if err != nil {
				return "", err
			}
			return refreshed.AccessToken, nil
		}
		if _, err := maintenanceIdentity(scope, principal, endpoint); err != nil {
			return err
		}
		_, err = c.Post("/maintenance/events:batch", map[string]interface{}{"events": events})
		return err
	})
}

func cliAdoptionEvent(scope maintenance.Scope, principal, running string) (maintenance.Event, bool, error) {
	credentials, err := auth.LoadV2Credentials(scope.Server)
	if err != nil || credentials.AgentID != scope.AgentID || credentials.PrincipalID != principal {
		return maintenance.Event{}, false, fmt.Errorf("maintenance identity changed")
	}
	previous, found, err := maintenance.CLIAdoptionFor(scope, principal, running)
	if err != nil || !found {
		return maintenance.Event{}, false, err
	}
	event := maintenance.NewEvent(previous.AttemptID, "cli", "adoption", "adoption", "runtime_ready")
	event.FromVersion, event.ToVersion, event.RunningVersion = previous.FromVersion, running, running
	return event, true, nil
}
