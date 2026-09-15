package cmd

import (
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"os"
	"time"
)

type pluginMaintenance struct {
	Host             string    `json:"host"`
	PluginID         string    `json:"plugin_id"`
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

func pluginMaintenanceForHost(host string, cfg *config.Config) pluginMaintenance {
	p := pluginMaintenance{Host: runtimeProduct(host), PluginID: heartbeatPluginID(host), Status: "due", Due: true}
	if p.PluginID == "" || !automaticMaintenanceEnabled(cfg, "auto_plugin_update") {
		p.Status, p.Due = "not_applicable", false
		return p
	}
	b, err := os.ReadFile(maintenancePath("plugin-" + p.Host))
	var saved pluginMaintenance
	if err == nil && json.Unmarshal(b, &saved) == nil && saved.PluginID == p.PluginID && saved.Host == p.Host {
		age := time.Since(saved.CheckedAt)
		if age >= 0 && age < 24*time.Hour {
			saved.Due = false
			return saved
		}
	}
	return p
}

func validatePluginReceipt(p pluginMaintenance, host string) error {
	if p.Host != runtimeProduct(host) || p.PluginID == "" || p.PluginID != heartbeatPluginID(host) {
		return fmt.Errorf("plugin does not belong to the current host")
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
		p.CheckedAt, p.Due = time.Now(), false
		if err := saveMaintenance(maintenancePath("plugin-"+p.Host), p); err != nil {
			return err
		}
		output.PrintData(p, resolveFormat())
		return nil
	}}
	c.Flags().Bool("stdin", false, "read fresh host plugin observations from stdin")
	heartbeatCmd.AddCommand(c)
}
