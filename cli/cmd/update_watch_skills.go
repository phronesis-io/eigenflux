package cmd

import (
	"net/http"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/skills"
)

// Recovery receipts carry bounded reasons, never network errors or local paths.
type upgradeSkillsError struct {
	Reason string
	Err    error
}

func (e *upgradeSkillsError) Error() string { return e.Reason }
func (e *upgradeSkillsError) Unwrap() error { return e.Err }

func syncUpgradeWatchRules(cfg *config.Config, b dispatch.Binding, manual bool) error {
	return verifyUpgradeWatchRules(manual || automaticMaintenanceEnabled(cfg, autoSkillSyncKey), func() (*skills.SyncResult, error) {
		return skills.Sync(skills.SyncOptions{Into: b.SkillsDir, Host: b.Host, CLIVersion: version, CDNBase: cdnBase(), HTTPClient: &http.Client{Timeout: 5 * time.Second}})
	}, func() error {
		_, err := localHeartbeatSkillsAt(b.SkillsDir, b.Host)
		return err
	})
}

func verifyUpgradeWatchRules(syncEnabled bool, sync func() (*skills.SyncResult, error), local func() error) error {
	if syncEnabled {
		result, err := sync()
		if err != nil {
			return &upgradeSkillsError{Reason: "skills_sync_failed", Err: err}
		}
		if reason := upgradeSkillsSyncReason(result); reason != "" {
			return &upgradeSkillsError{Reason: reason}
		}
	}
	// Local signature, compatibility and file hashes are required even after a
	// fresh manifest check, and also when the owner disabled automatic syncing.
	if err := local(); err != nil {
		return &upgradeSkillsError{Reason: "skills_local_validation_failed", Err: err}
	}
	return nil
}

func upgradeSkillsSyncReason(result *skills.SyncResult) string {
	if result == nil {
		return "skills_latest_unverified"
	}
	if result.RequiredCLIVersion != "" {
		return "skills_cli_upgrade_required"
	}
	if len(result.Preserved) > 0 {
		return "skills_local_modified"
	}
	if result.NoNetwork {
		return "skills_latest_unavailable"
	}
	if result.Stale {
		return "skills_provisional"
	}
	for _, diagnostic := range result.Diagnostics {
		switch diagnostic.Reason {
		case "signed manifest rollback rejected":
			return "skills_rollback_rejected"
		case "signed manifest sequence reused for different content":
			return "skills_sequence_conflict"
		}
		if strings.HasPrefix(diagnostic.Reason, "skills need CLI") {
			return "skills_cli_upgrade_required"
		}
	}
	if !result.VerifiedManifest || len(result.Diagnostics) > 0 {
		return "skills_latest_unverified"
	}
	return ""
}
