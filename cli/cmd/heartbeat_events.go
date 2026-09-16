package cmd

import (
	"fmt"
	"net/http"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/output"
	"cli.eigenflux.ai/internal/skills"
	"github.com/spf13/cobra"
)

func maintenanceScope(server string) (maintenance.Scope, error) {
	credentials, err := auth.LoadV2Credentials(server)
	if err != nil {
		return maintenance.Scope{}, err
	}
	return maintenance.Scope{Home: config.HomeDir(), Server: server, AgentID: credentials.AgentID}, nil
}
func recordMaintenanceEvent(server string, event maintenance.Event) error {
	scope, err := maintenanceScope(server)
	if err != nil {
		return err
	}
	return recordMaintenanceEventAtScope(scope, event)
}
func recordMaintenanceEventAtScope(scope maintenance.Scope, event maintenance.Event) error {
	current, err := auth.LoadV2Credentials(scope.Server)
	if err != nil {
		return err
	}
	if current.AgentID != scope.AgentID {
		return fmt.Errorf("maintenance identity changed before observation")
	}
	meta := clientMetaForServerName(scope.Server)
	event.Host = runtimeProduct(meta.Host)
	event.Mode = meta.Mode
	return maintenance.Record(scope, event)
}
func maintenanceDue(server string, now time.Time) bool {
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	meta := clientMetaForServerName(server)
	if meta.Mode != "skill" && meta.Mode != "plugin" {
		return false
	}
	if !serverMaintenanceEnabled(cfg, server, "auto_cli_update") && !serverMaintenanceEnabled(cfg, server, "auto_plugin_update") && !serverMaintenanceEnabled(cfg, server, autoSkillSyncKey) {
		return false
	}
	scope, err := maintenanceScope(server)
	if err != nil {
		return false
	}
	due, err := maintenance.Due(scope, now, 24*time.Hour)
	return err == nil && due
}
func markMaintenanceAttempt(server string) {
	if scope, err := maintenanceScope(server); err == nil {
		_ = maintenance.MarkAttempt(scope, time.Now())
	}
}
func flushMaintenanceEvents(server string) error {
	scope, err := maintenanceScope(server)
	if err != nil {
		return err
	}
	return maintenance.Flush(scope, time.Now(), func(events []maintenance.Event) error {
		c, _, err := newV2ClientForServer(server, true)
		if err != nil {
			return err
		}
		// Credential refresh follows the existing bounded V2 flow. Never send an old
		// account's queue after refresh/account switching resolved another identity.
		current, err := auth.LoadV2Credentials(server)
		if err != nil {
			return err
		}
		if current.AgentID != scope.AgentID {
			return fmt.Errorf("maintenance identity changed")
		}
		c.HTTPClient = &http.Client{Timeout: 5 * time.Second}
		originalRefresh := c.OnUnauthorized
		c.OnUnauthorized = func() (string, error) {
			token, err := originalRefresh()
			if err != nil {
				return "", err
			}
			after, err := auth.LoadV2Credentials(server)
			if err != nil || after.AgentID != scope.AgentID {
				return "", fmt.Errorf("maintenance identity changed during refresh")
			}
			return token, nil
		}
		_, err = c.Post("/maintenance/events:batch", map[string]interface{}{"events": events})
		return err
	})
}

func maintenanceEvent(component, phase, result string) maintenance.Event {
	return maintenance.NewEvent(maintenance.NewID(), component, "auto", phase, result)
}

func serverMaintenanceEnabled(cfg *config.Config, server, key string) bool {
	if value, found, err := cfg.GetServerKV(server, key); err == nil && found {
		return value != "false"
	}
	return cfg.GetKV(key) != "false"
}
func init() {
	report := &cobra.Command{Use: "maintenance-report", Short: "Record a fresh host maintenance observation", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var event maintenance.Event
		if err := readMaintenanceInput(cmd, &event); err != nil {
			return err
		}
		scope, err := maintenanceScope(activeServerName())
		if err != nil {
			return err
		}
		previous, known, err := maintenance.ReadAttempt(scope, event.AttemptID)
		if err != nil {
			return err
		}
		if !known || previous.ToVersion != event.ToVersion {
			return fmt.Errorf("maintenance receipt does not match this account attempt")
		}
		local, err := localHeartbeatSkills(clientMetaForServerName(scope.Server).Host)
		if err != nil {
			return err
		}
		manifest, err := skills.ReadLocalManifest(local.SkillsDir)
		if err != nil {
			return err
		}
		if manifest == nil || manifest.Revision != event.ToVersion {
			return fmt.Errorf("rules changed; read a fresh plan before reporting adoption")
		}
		if event.Component != "skills" || event.Phase != "read" || event.Result != "rules_read" {
			return fmt.Errorf("use the component-specific receipt command for this observation")
		}
		event.EventAt = time.Now().UnixMilli()
		if err := recordMaintenanceEventAtScope(scope, event); err != nil {
			return err
		}
		flushErr := flushMaintenanceEvents(activeServerName())
		status := "reported"
		remaining, stateErr := maintenance.Snapshot(scope)
		if stateErr != nil || flushErr != nil {
			status = "queued"
		}
		for _, pending := range remaining.Events {
			if pending.EventID == event.EventID {
				status = "queued"
			}
		}
		output.PrintData(map[string]string{"status": status, "event_id": event.EventID}, resolveFormat())
		return nil
	}}
	report.Flags().Bool("stdin", false, "Read a maintenance observation from stdin")
	status := &cobra.Command{Use: "maintenance-status", Short: "Inspect account maintenance queue and dropped event count", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		scope, err := maintenanceScope(activeServerName())
		if err != nil {
			return err
		}
		state, err := maintenance.Snapshot(scope)
		if err != nil {
			return err
		}
		output.PrintData(state, resolveFormat())
		return nil
	}}
	heartbeatCmd.AddCommand(report, status)
}
