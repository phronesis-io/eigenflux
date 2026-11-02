package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
)

const profileRefreshRunHeader = "X-EF-Profile-Refresh-Run"

type profileFunnelEvent struct {
	Stage         string `json:"stage"`
	RunID         string `json:"run_id,omitempty"`
	PollID        string `json:"poll_id,omitempty"`
	Cause         string `json:"cause"`
	FailureCode   string `json:"failure_code,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
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
	now := time.Now().UnixMilli()
	hasV2, err := auth.HasV2Credentials(server)
	if err != nil {
		return
	}
	if hasV2 {
		credentials, err := auth.LoadV2Credentials(server)
		if err != nil {
			return
		}
		if credentials.ExpiresAt <= now {
			return
		}
		token = credentials.AccessToken
		apiVersion = "/api/v2"
	} else {
		credentials, err := auth.LoadCredentials(server)
		if err != nil {
			return
		}
		if credentials.ExpiresAt != 0 && credentials.ExpiresAt <= now {
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

// Reasons are fixed diagnostics, never error text, response bodies, paths or URLs.
// Keep the coarse failure_code stable for existing dashboards and old clients.
func profileFailureReason(err error) string {
	if err == nil {
		return ""
	}
	if profileFailureCode(err) == "version_conflict" {
		return "version_conflict"
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		if apiErr.ErrorCode == "FEED_CONTEXT_READ_FAILED" {
			return "api_feed_context_read_failed"
		}
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			return "api_unauthorized"
		case http.StatusForbidden:
			return "api_forbidden"
		case http.StatusNotFound:
			return "api_not_found"
		case http.StatusTooManyRequests:
			return "api_rate_limited"
		default:
			if apiErr.StatusCode >= 500 {
				return "api_server"
			}
			return "api_other"
		}
	}
	var diagnostic *feedDiagnosticError
	if errors.As(err, &diagnostic) {
		return diagnostic.reason
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout() {
		return "transport_timeout"
	}
	if errors.As(err, &netErr) {
		return "transport_network"
	}
	var syntax *json.SyntaxError
	var mismatch *json.UnmarshalTypeError
	if errors.As(err, &syntax) || errors.As(err, &mismatch) {
		return "response_decode"
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return "local_storage"
	}
	return "client_other"
}

type feedDiagnosticError struct {
	reason string
	err    error
}

func (e *feedDiagnosticError) Error() string { return e.err.Error() }
func (e *feedDiagnosticError) Unwrap() error { return e.err }

func feedDiagnostic(reason string, err error) error {
	return &feedDiagnosticError{reason: reason, err: err}
}

func reportProfileFailure(server, agentID string, err error) {
	state := profilestate.Load(config.HomeDir(), server, agentID)
	if currentProfileRunID(server, agentID) == "" {
		return
	}
	// This is an attempt failure, not proof that the Agent abandoned the run.
	reportProfileFunnel(server, profileFunnelEvent{Stage: "refresh_attempt_failed", RunID: state.RefreshRunID, PollID: state.RefreshPollID, Cause: state.RefreshCause, FailureCode: profileFailureCode(err), FailureReason: profileFailureReason(err)})
}

func reportCurrentProfileFailure(err error) {
	server, agentID := activeProfileStateScope()
	if server != "" && agentID != "" {
		reportProfileFailure(server, agentID, err)
	}
}
