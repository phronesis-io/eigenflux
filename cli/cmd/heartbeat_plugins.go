package cmd

import (
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/output"
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"time"
)

type pluginMaintenance struct {
	AttemptID        string    `json:"attempt_id,omitempty"`
	Host             string    `json:"host"`
	PluginID         string    `json:"plugin_id"`
	Scope            string    `json:"scope,omitempty"`
	Context          string    `json:"context,omitempty"`
	Due              bool      `json:"due"`
	Status           string    `json:"status"`
	InstalledVersion string    `json:"installed_version,omitempty"`
	LatestVersion    string    `json:"latest_version,omitempty"`
	LoadedVersion    string    `json:"loaded_version,omitempty"`
	Error            string    `json:"error,omitempty"`
	CheckedAt        time.Time `json:"checked_at,omitempty"`
}

func heartbeatPluginID(host string) string {
	switch runtimeProduct(host) {
	case "codex":
		return "codex-eigenflux@eigenflux"
	case "claude-code":
		return "eigenflux@eigenflux-marketplace"
	case "openclaw":
		return "openclaw-eigenflux"
	default:
		return ""
	}
}

func pluginContext() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir
}

func pluginMaintenanceForHost(host, mode string, cfg *config.Config) pluginMaintenance {
	p := pluginMaintenance{Host: runtimeProduct(host), PluginID: heartbeatPluginID(host), Context: pluginContext(), Status: "due", Due: true}
	if p.PluginID == "" || !heartbeatMaintenanceEnabled(mode, cfg, "auto_plugin_update") {
		p.Status, p.Due = "not_applicable", false
		return p
	}
	b, err := os.ReadFile(maintenancePath("plugin-" + p.Host))
	var saved pluginMaintenance
	context := pluginContext()
	if err == nil && json.Unmarshal(b, &saved) == nil && saved.PluginID == p.PluginID && saved.Host == p.Host && context != "" && saved.Context == context && saved.Scope != "" {
		age := time.Since(saved.CheckedAt)
		if age >= 0 && age < 24*time.Hour {
			// Cache release discovery, never the current installation or process state.
			p.Due, p.Status = false, "check_required"
			p.Scope, p.LatestVersion, p.CheckedAt = saved.Scope, saved.LatestVersion, saved.CheckedAt
			p.AttemptID = saved.AttemptID
			return p
		}
	}
	if scope, err := maintenanceScope(activeServerName()); err == nil {
		previous, _ := maintenance.LastAttempt(scope, "plugin")
		if previous.AttemptID != "" && previous.Result == "restart_required" {
			p.AttemptID = previous.AttemptID
		} else {
			p.AttemptID = maintenance.NewID()
		}
		e := maintenance.NewEvent(p.AttemptID, "plugin", "auto", "check", "started")
		_ = recordMaintenanceEvent(activeServerName(), e)
	}
	return p
}

func validatePluginReceipt(p pluginMaintenance, host string) error {
	if p.Host != runtimeProduct(host) || p.PluginID == "" || p.PluginID != heartbeatPluginID(host) {
		return fmt.Errorf("plugin does not belong to the current host")
	}
	if p.Scope == "" {
		return fmt.Errorf("fresh host-verified plugin installation scope required")
	}
	switch p.Status {
	case "loaded", "restart_required":
		if p.InstalledVersion == "" || p.LatestVersion != p.InstalledVersion {
			return fmt.Errorf("fresh installed and compatible latest versions must match")
		}
		if p.Status == "loaded" && p.LoadedVersion != p.InstalledVersion {
			return fmt.Errorf("loaded version is unverified")
		}
	case "not_installed":
		if p.InstalledVersion != "" || p.LoadedVersion != "" {
			return fmt.Errorf("not_installed receipt contains installed/loaded version")
		}
	case "failed", "blocked":
		if p.Error == "" {
			return fmt.Errorf("concrete plugin maintenance error required")
		}
	default:
		return fmt.Errorf("invalid plugin maintenance status")
	}
	return nil
}

func init() {
	c := &cobra.Command{Use: "plugin-check", Short: "Record host-verified EigenFlux plugin update and load status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var p pluginMaintenance
		if err := readMaintenanceInput(cmd, &p); err != nil {
			return err
		}
		if err := validatePluginReceipt(p, clientMetaForServerName(activeServerName()).Host); err != nil {
			return err
		}
		// A host plugin can fetch the plan outside the Agent's working directory.
		// Carry the plan's discovery context back without changing filesystem scope.
		if p.Context == "" {
			p.Context = pluginContext()
		}
		if !filepath.IsAbs(p.Context) {
			return fmt.Errorf("absolute plugin plan context required")
		}
		if scope, err := maintenanceScope(activeServerName()); err == nil {
			previous, _ := maintenance.LastAttempt(scope, "plugin")
			if p.AttemptID != "" && p.AttemptID != previous.AttemptID {
				return fmt.Errorf("plugin receipt does not match this account attempt")
			}
			if p.AttemptID == "" {
				p.AttemptID = previous.AttemptID
			}
		}
		if p.AttemptID == "" {
			p.AttemptID = maintenance.NewID()
		}
		p.CheckedAt, p.Due = pluginDiscoveryTime(p, time.Now()), false
		if err := saveMaintenance(maintenancePath("plugin-"+p.Host), p); err != nil {
			return err
		}
		e := maintenance.NewEvent(p.AttemptID, "plugin", "auto", "load", p.Status)
		e.ToVersion = p.LatestVersion
		e.RunningVersion = p.LoadedVersion
		if p.Error != "" {
			e.ErrorCode = "host_maintenance_failed"
		}
		_ = recordMaintenanceEvent(activeServerName(), e)
		_ = flushMaintenanceEvents(activeServerName())
		output.PrintData(p, resolveFormat())
		return nil
	}}
	c.Flags().Bool("stdin", false, "read fresh host plugin observations from stdin")
	heartbeatCmd.AddCommand(c)
}

// Fresh load observations must not postpone the next release discovery when
// they only confirm the cached target in the same installation context.
func pluginDiscoveryTime(p pluginMaintenance, now time.Time) time.Time {
	b, err := os.ReadFile(maintenancePath("plugin-" + p.Host))
	var previous pluginMaintenance
	if err == nil && json.Unmarshal(b, &previous) == nil && previous.PluginID == p.PluginID && previous.Scope == p.Scope && previous.Context == p.Context && previous.LatestVersion == p.LatestVersion {
		age := now.Sub(previous.CheckedAt)
		if age >= 0 && age < 24*time.Hour {
			return previous.CheckedAt
		}
	}
	return now
}
