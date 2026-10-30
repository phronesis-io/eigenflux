package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
	"cli.eigenflux.ai/internal/selfupdate"
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
		return enabled, policyFailure("identity", fmt.Errorf("CLI update policy identity unavailable"))
	}
	c, err := newSettingsClient(cfg, server)
	if err != nil {
		return enabled, policyFailure("credentials", err)
	}
	c.HTTPClient.Timeout = 3 * time.Second
	resp, err := c.Get("/agents/me/settings", nil)
	if err != nil {
		return enabled, policyFailure("request", err)
	}
	if resp.Code != 0 {
		return enabled, policyFailure("response", fmt.Errorf("CLI update policy request failed: code=%d", resp.Code))
	}
	var remote struct {
		Enabled *bool `json:"auto_cli_update"`
	}
	if err := json.Unmarshal(resp.Data, &remote); err != nil {
		return enabled, policyFailure("decode", err)
	}
	// Older servers omit the field and preserve the default enabled policy.
	enabled = remote.Enabled == nil || *remote.Enabled
	if cliUpdatePolicyScope(cfg, server) != scope {
		return true, policyFailure("identity_changed", fmt.Errorf("CLI update policy identity changed"))
	}
	if loadErr != nil {
		return enabled, policyFailure("cache_read", loadErr)
	}
	_, err = profilestate.UpdateRuntime(config.HomeDir(), server, func(current *profilestate.RuntimeState) (bool, error) {
		if current.CLIUpdateScope == scope && current.CLIAutoUpdate != nil && *current.CLIAutoUpdate == enabled {
			return false, nil
		}
		current.CLIUpdateScope, current.CLIAutoUpdate = scope, &enabled
		return true, nil
	})
	return enabled, policyFailure("cache_write", err)
}

// The original error stays local. Only a bounded category reaches maintenance.
type cliUpdatePolicyError struct {
	code string
	err  error
}

func (e *cliUpdatePolicyError) Error() string { return e.err.Error() }
func (e *cliUpdatePolicyError) Unwrap() error { return e.err }

func policyFailure(step string, err error) error {
	if err == nil {
		return nil
	}
	kind := selfupdate.FailureKind(err)
	var api *client.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode {
		case "ONBOARDING_REQUIRED":
			kind = "onboarding_required"
		case "AGENT_AUTH_REQUIRED", "AGENT_SCOPE_REQUIRED":
			kind = "authentication_required"
		default:
			if api.StatusCode >= 400 && api.StatusCode <= 599 {
				kind = fmt.Sprintf("http_%d", api.StatusCode)
			} else {
				kind = "response_failed"
			}
		}
	}
	return &cliUpdatePolicyError{code: "update_policy_" + step + "_" + kind, err: err}
}

func (r *maintenanceRecorder) PolicyFailure(err error, from string) {
	code := "update_policy_unavailable"
	var policy *cliUpdatePolicyError
	if errors.As(err, &policy) {
		code = policy.code
	}
	r.Finish("check", "waiting_online", from, "", "", code)
}
