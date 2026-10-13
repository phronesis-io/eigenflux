package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
)

const profileRefreshRunHeader = "X-EF-Profile-Refresh-Run"

type profileFunnelEvent struct {
	Stage       string `json:"stage"`
	RunID       string `json:"run_id,omitempty"`
	PollID      string `json:"poll_id,omitempty"`
	Cause       string `json:"cause"`
	FailureCode string `json:"failure_code,omitempty"`
}

func newProfileRunID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(id[:])
}

// Telemetry never changes command output or result. One short timeout bounds
// each observation; older gateways simply reject the unknown endpoint.
func reportProfileFunnel(server string, event profileFunnelEvent) {
	if event.RunID == "" && event.PollID == "" {
		return
	}
	cfg, err := config.Load()
	if err != nil {
		return
	}
	srv, err := cfg.GetActive(server)
	if err != nil {
		return
	}
	token, apiVersion := "", "/api/v1"
	hasV2, err := auth.HasV2Credentials(server)
	if err != nil {
		return
	}
	if hasV2 {
		credentials, err := auth.LoadV2Credentials(server)
		if err != nil {
			return
		}
		token = credentials.AccessToken
		apiVersion = "/api/v2"
	} else {
		credentials, err := auth.LoadCredentials(server)
		if err != nil {
			return
		}
		token = credentials.AccessToken
	}
	if token == "" {
		return
	}
	c := client.New(strings.TrimRight(srv.Endpoint, "/")+apiVersion, token, version, clientMetaForServer(srv))
	c.HTTPClient = &http.Client{Timeout: time.Second}
	_, _ = c.Post("/agents/me/card/refresh-events", event)
}

func reportProfileDispatch(server string, state profilestate.State) {
	reportProfileFunnel(server, profileFunnelEvent{Stage: "refresh_dispatched", RunID: state.RefreshRunID, PollID: state.RefreshPollID, Cause: state.RefreshCause})
}

func currentProfileRunID(server, agentID string) string {
	state := profilestate.Load(config.HomeDir(), server, agentID)
	if activeProfileRefreshTrigger(state, time.Now().Unix()) == profileRefreshTriggerUnknown {
		return ""
	}
	return state.RefreshRunID
}

func profileFailureCode(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict || strings.Contains(err.Error(), "profile changed since version") {
		return "version_conflict"
	}
	if errors.As(err, &apiErr) {
		return "api_error"
	}
	return "client_error"
}

func reportProfileFailure(server, agentID string, err error) {
	state := profilestate.Load(config.HomeDir(), server, agentID)
	if currentProfileRunID(server, agentID) == "" {
		return
	}
	// This is an attempt failure, not proof that the Agent abandoned the run.
	reportProfileFunnel(server, profileFunnelEvent{Stage: "refresh_attempt_failed", RunID: state.RefreshRunID, PollID: state.RefreshPollID, Cause: state.RefreshCause, FailureCode: profileFailureCode(err)})
}

func reportCurrentProfileFailure(err error) {
	server, agentID := activeProfileStateScope()
	if server != "" && agentID != "" {
		reportProfileFailure(server, agentID, err)
	}
}
