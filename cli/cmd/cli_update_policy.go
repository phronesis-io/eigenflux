package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
)

func cliUpdatePolicyScope(cfg *config.Config, server string) string {
	if cfg == nil {
		return ""
	}
	target, err := cfg.GetActive(server)
	agent := settingsAgentID(server)
	if err != nil || agent == "" {
		return ""
	}
	return strings.TrimRight(target.Endpoint, "/") + "\x1f" + agent
}

func cachedCLIUpdatePolicy(cfg *config.Config, server string, state profilestate.RuntimeState) bool {
	scope := cliUpdatePolicyScope(cfg, server)
	if scope != "" && state.CLIUpdateScope == scope && state.CLIAutoUpdate != nil {
		return *state.CLIAutoUpdate
	}
	return true
}

// Fetch on every automatic maintenance entry, independently of the binary's
// daily check stamp. Offline requests retain only this account/endpoint's policy.
func refreshCLIUpdatePolicy(cfg *config.Config, server string) (bool, error) {
	state, loadErr := profilestate.LoadRuntime(config.HomeDir(), server)
	enabled := cachedCLIUpdatePolicy(cfg, server, state)
	scope := cliUpdatePolicyScope(cfg, server)
	if scope == "" {
		return enabled, fmt.Errorf("CLI update policy identity unavailable")
	}
	c, err := newSettingsClient(cfg, server)
	if err != nil {
		return enabled, err
	}
	c.HTTPClient.Timeout = 3 * time.Second
	resp, err := c.Get("/agents/me/settings", nil)
	if err != nil {
		return enabled, err
	}
	if resp.Code != 0 {
		return enabled, fmt.Errorf("CLI update policy request failed: code=%d", resp.Code)
	}
	var remote struct {
		Enabled *bool `json:"auto_cli_update"`
	}
	if err := json.Unmarshal(resp.Data, &remote); err != nil {
		return enabled, err
	}
	// Older servers omit the field and preserve the default enabled policy.
	enabled = remote.Enabled == nil || *remote.Enabled
	if cliUpdatePolicyScope(cfg, server) != scope {
		return true, fmt.Errorf("CLI update policy identity changed")
	}
	if loadErr != nil {
		return enabled, loadErr
	}
	_, err = profilestate.UpdateRuntime(config.HomeDir(), server, func(current *profilestate.RuntimeState) (bool, error) {
		if current.CLIUpdateScope == scope && current.CLIAutoUpdate != nil && *current.CLIAutoUpdate == enabled {
			return false, nil
		}
		current.CLIUpdateScope, current.CLIAutoUpdate = scope, &enabled
		return true, nil
	})
	return enabled, err
}
