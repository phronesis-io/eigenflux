package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
)

// Periodic Profile Refresh health telemetry. The CLI reports a `dispatched`
// event when it hands a refresh task to the agent (refresh-task or the
// [PENDING TASK] line) and a `completed` event when the agent finishes with
// `profile refresh-complete` or a `profile patch --source cli_daily_refresh`.
// Both share a client-generated run_id kept in the profile-refresh sidecar.
//
// Reporting is strictly best-effort: it runs after the command's own output,
// uses a short timeout, never refreshes credentials, never writes to stdout,
// and ignores every failure (including 404 from servers without the endpoint).
// Failures are visible on stderr only with --verbose.
const (
	profileRefreshTriggerPluginTask  = "plugin_task"
	profileRefreshTriggerPendingLine = "pending_line"
	profileRefreshTriggerManualForce = "manual_force"
	// untracked marks a completion with no linked dispatch in local state,
	// such as an agent-initiated refresh or a dispatch older than the link window.
	profileRefreshTriggerUntracked = "untracked"

	profileRefreshRunReportTimeout = 3 * time.Second
	profileRefreshRunLinkWindow    = 72 * time.Hour
	// Matches the 24h window of the server-side failure metric.
	profileRefreshRunReuseWindow = 24 * time.Hour

	legacyProfileRefreshRunsPath = "/agents/me/card/refresh-runs"
	v2ProfileRefreshRunsPath     = "/agent-profile/refresh-runs"
)

type profileRefreshRunEvent struct {
	RunID        string   `json:"run_id"`
	Stage        string   `json:"stage"`
	Outcome      string   `json:"outcome,omitempty"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
	Trigger      string   `json:"trigger"`
}

// reportProfileRefreshRun is replaceable in tests.
var reportProfileRefreshRun = postProfileRefreshRun

func newProfileRefreshRunID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// setPendingProfileRefreshRun records a dispatched run inside a profilestate
// mutation and returns the run_id to report. A repeat reminder for a run that
// is still unfinished, has the same trigger, and is younger than
// profileRefreshRunReuseWindow keeps the original run_id and dispatch time, so
// hourly re-prompts of one ignored refresh count as one failed run rather than
// one per reminder. Manual forced reviews always start a new run.
func setPendingProfileRefreshRun(state *profilestate.State, runID, trigger string, now int64) string {
	if runID == "" {
		return ""
	}
	if trigger != profileRefreshTriggerManualForce &&
		state.PendingRunID != "" && state.PendingRunTrigger == trigger &&
		state.PendingRunDispatchedUnix > 0 && state.PendingRunDispatchedUnix <= now &&
		now-state.PendingRunDispatchedUnix < int64(profileRefreshRunReuseWindow/time.Second) {
		return state.PendingRunID
	}
	state.PendingRunID = runID
	state.PendingRunTrigger = trigger
	state.PendingRunDispatchedUnix = now
	return runID
}

// takePendingProfileRefreshRun clears the pending run inside a profilestate
// mutation and returns the run a completion belongs to.
func takePendingProfileRefreshRun(state *profilestate.State, now int64) (string, string) {
	runID, trigger, dispatchedAt := state.PendingRunID, state.PendingRunTrigger, state.PendingRunDispatchedUnix
	state.PendingRunID, state.PendingRunTrigger, state.PendingRunDispatchedUnix = "", "", 0
	linked := runID != "" && dispatchedAt > 0 && dispatchedAt <= now &&
		now-dispatchedAt <= int64(profileRefreshRunLinkWindow/time.Second)
	switch trigger {
	case profileRefreshTriggerPluginTask, profileRefreshTriggerPendingLine, profileRefreshTriggerManualForce:
	default:
		linked = false
	}
	if !linked {
		return newProfileRefreshRunID(), profileRefreshTriggerUntracked
	}
	return runID, trigger
}

func reportProfileRefreshDispatched(serverName, runID, trigger string) {
	reportProfileRefreshRun(serverName, profileRefreshRunEvent{RunID: runID, Stage: "dispatched", Trigger: trigger})
}

func reportProfileRefreshCompleted(serverName, runID, trigger string, changedPaths []string) {
	event := profileRefreshRunEvent{RunID: runID, Stage: "completed", Outcome: "unchanged", Trigger: trigger}
	if len(changedPaths) > 0 {
		event.Outcome = "changed"
		event.ChangedPaths = changedPaths
	}
	reportProfileRefreshRun(serverName, event)
}

func postProfileRefreshRun(serverName string, event profileRefreshRunEvent) {
	if event.RunID == "" {
		return
	}
	if err := sendProfileRefreshRun(serverName, event); err != nil && verboseFlag {
		fmt.Fprintf(os.Stderr, "debug: profile refresh run report skipped: %v\n", err)
	}
}

func sendProfileRefreshRun(serverName string, event profileRefreshRunEvent) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	srv, err := cfg.GetActive(serverName)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(srv.Endpoint, "/")
	var c *client.Client
	path := legacyProfileRefreshRunsPath
	hasV2, err := auth.HasV2Credentials(srv.Name)
	if err != nil {
		return err
	}
	if hasV2 {
		credentials, credErr := auth.LoadV2Credentials(srv.Name)
		if credErr != nil {
			return credErr
		}
		// Never rotate credentials for telemetry; the triggering command
		// already refreshed them when needed.
		if credentials.AccessToken == "" || credentials.ExpiresAt <= time.Now().UnixMilli() {
			return fmt.Errorf("agent v2 access token is expired")
		}
		c = client.New(endpoint+"/api/v2", credentials.AccessToken, version, clientMetaForServer(srv))
		path = v2ProfileRefreshRunsPath
	} else {
		credentials, credErr := auth.LoadCredentials(srv.Name)
		if credErr != nil {
			return credErr
		}
		if credentials.IsExpired() {
			return fmt.Errorf("access token is expired")
		}
		c = client.New(endpoint+"/api/v1", credentials.AccessToken, version, clientMetaForServer(srv))
	}
	c.HTTPClient.Timeout = profileRefreshRunReportTimeout
	resp, err := c.Post(path, event)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("%s", resp.Msg)
	}
	return nil
}

// patchChangedPaths extracts changed_paths from a profile fields response.
func patchChangedPaths(data json.RawMessage) []string {
	var payload struct {
		ChangedPaths []string `json:"changed_paths"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return nil
	}
	return payload.ChangedPaths
}
