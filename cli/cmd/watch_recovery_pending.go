package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	"github.com/spf13/cobra"
)

type pendingWatchRecovery struct {
	Home     string `json:"home"`
	Server   string `json:"server"`
	Endpoint string `json:"endpoint"`
	Pending  bool   `json:"pending"`
}

func pendingWatchRecoveryScope(cfg *config.Config) (pendingWatchRecovery, string, error) {
	if cfg == nil {
		var err error
		cfg, err = config.Load()
		if err != nil {
			return pendingWatchRecovery{}, "", err
		}
	}
	server, err := cfg.GetActive(serverFlag)
	if err != nil {
		return pendingWatchRecovery{}, "", err
	}
	home, _ := config.HomeDirInfo()
	record := pendingWatchRecovery{Home: filepath.Clean(home), Server: server.Name, Endpoint: strings.TrimRight(server.Endpoint, "/"), Pending: true}
	key := sha256.Sum256([]byte(record.Server))
	return record, filepath.Join(home, "watch", fmt.Sprintf("%x.upgrade-pending.json", key[:16])), nil
}

// markWatchRecoveryPending records installation intent, never authentication data.
func markWatchRecoveryPending(cfg *config.Config) error {
	record, path, err := pendingWatchRecoveryScope(cfg)
	if err != nil {
		return err
	}
	return dispatch.WriteJSON(path, record)
}

func completePendingWatchRecovery(cmd *cobra.Command, cfg *config.Config) *updateWatchRecovery {
	return resumePendingWatchRecovery(cfg, func() updateWatchRecovery { return recoverWatchAfterUpdate(cmd, cfg, true) })
}

func resumePendingWatchRecovery(cfg *config.Config, recover func() updateWatchRecovery) *updateWatchRecovery {
	expected, path, err := pendingWatchRecoveryScope(cfg)
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var record pendingWatchRecovery
	if json.Unmarshal(raw, &record) != nil || !record.Pending || record.Home != expected.Home || record.Server != expected.Server || record.Endpoint != expected.Endpoint {
		return nil
	}
	result := recover()
	if result.Status == "ready" || result.Reason == "owner_disabled" || result.Reason == "owner_setup_disabled" {
		record.Pending = false
		if err := dispatch.WriteJSON(path, record); err != nil {
			// Authentication remains successful; an unconsumed marker permits safe retry.
			result.Reason = "recovery_receipt_not_saved"
		}
	}
	return &result
}
