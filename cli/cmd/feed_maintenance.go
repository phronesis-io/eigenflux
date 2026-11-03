package cmd

import (
	"fmt"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/selfupdate"
	"github.com/spf13/cobra"
)

// Legacy schedulers may invoke only feed poll. Resolve a signed Skills minimum
// before the business pull, so re-execution cannot pull or acknowledge Feed twice.
func prepareFeedMaintenance(cmd *cobra.Command, cfg *config.Config) (bool, error) {
	res := maybeSyncSkills(cfg)
	if res == nil || res.RequiredCLIVersion == "" {
		return false, nil
	}
	defer func() { _ = flushMaintenanceEvents(activeServerName()) }()
	result, restarted, err := updateHeartbeatCLI(cmd, cfg, res.RequiredCLIVersion)
	if restarted || err != nil {
		return restarted, err
	}
	if !selfupdate.ValidVersion(result.Version) || selfupdate.Compare(result.Version, res.RequiredCLIVersion) < 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "[eigenflux:maintenance] component=skills phase=check error_code=skills_cli_incompatible required_cli_version=%s cli_update_status=%s cli_update_reason=%s\n", res.RequiredCLIVersion, result.Status, result.Reason)
	}
	// An unavailable update must not discard the already-installed compatible
	// rules or prevent the existing Feed transport from doing its business work.
	return false, nil
}
