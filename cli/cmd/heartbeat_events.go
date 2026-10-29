package cmd

import (
	"fmt"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/output"
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
func flushMaintenanceEvents(server string) error {
	scope, err := maintenanceScope(server)
	if err != nil {
		return err
	}
	return flushMaintenanceEventsAtScope(scope)
}

func flushMaintenanceEventsAtScope(scope maintenance.Scope) error {
	credentials, err := auth.LoadV2Credentials(scope.Server)
	if err != nil || credentials.AgentID != scope.AgentID {
		return fmt.Errorf("maintenance identity changed")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	server, err := cfg.GetActive(scope.Server)
	if err != nil {
		return err
	}
	return flushMaintenanceForIdentity(scope, credentials.PrincipalID, server.Endpoint)
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
	heartbeatCmd.AddCommand(status)
}
