package cmd

import (
	"io"

	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/selfupdate"
)

// Record process execution before watch readiness: notification-only watches and
// host-owned restarts need the same receipt without claiming a runtime lease.
func (w *accountWatch) reportCLIExecution(diagnostics io.Writer) {
	scope := maintenance.Scope{Home: w.home, Server: w.server.Name, AgentID: w.identity.AgentID}
	state, err := maintenance.Snapshot(scope)
	if err != nil || state.PendingCLIAdoption == nil {
		return
	}
	pending := state.PendingCLIAdoption
	e := pending.Event
	if pending.PrincipalID != w.identity.PrincipalID || pending.Endpoint != w.server.Endpoint ||
		e.Component != "cli" || e.Result != "installed" || (e.Trigger != "auto" && e.Trigger != "manual") ||
		!selfupdate.ValidVersion(e.FromVersion) || !selfupdate.ValidVersion(e.ToVersion) || e.ToVersion != version || maintenance.Validate(e) != nil {
		return
	}
	r := newMaintenanceRecorder(w.server.Name, "cli", e.Trigger, e.AttemptID, diagnostics)
	if !r.authenticated || r.scope.AgentID != scope.AgentID || r.principal != w.identity.PrincipalID || r.endpoint != w.server.Endpoint {
		return
	}
	// Preserve ordering even when the clock moved backwards during restart.
	r.lastAt = e.EventAt
	r.Finish("execute", "executed", e.FromVersion, version, version, "")
	r.Flush()
}
