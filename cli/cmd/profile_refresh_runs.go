package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/profilestate"
)

// Periodic Profile Refresh health telemetry. The CLI reports a `dispatched`
// event when it hands a refresh task to the agent (refresh-task or the
// [PENDING TASK] line) and a `completed` event when the agent finishes with
// `profile refresh-complete` or a `profile patch --source cli_daily_refresh`.
// Both share a client-generated run_id kept in the profile-refresh sidecar.
//
// The pending run is recorded before delivery so a fast completion always
// finds it. If delivery fails, a run created by that dispatch is cleared again;
// once delivery succeeds the dispatched event is reported even when later
// local bookkeeping fails.
//
// Reporting is strictly best-effort: it only runs on commands that actually
// dispatch or complete a refresh, after the command's own output, with a one
// second timeout. It never refreshes credentials, never writes to stdout, and
// ignores every failure (including 404 from servers without the endpoint).
// Failures are visible on stderr only with --verbose. A lost dispatched event
// leaves its completion without a dispatched row; server metrics account for
// that.
const (
	profileRefreshTriggerPluginTask  = "plugin_task"
	profileRefreshTriggerPendingLine = "pending_line"
	profileRefreshTriggerManualForce = "manual_force"
	// untracked marks a completion with no linked dispatch in local state,
	// such as an agent-initiated refresh or a dispatch older than the link window.
	profileRefreshTriggerUntracked = "untracked"

	profileRefreshRunReportTimeout = time.Second
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

// newProfileRefreshRunID returns "" when the system random source fails. It is
// replaceable in tests.
var newProfileRefreshRunID = func() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// profileRefreshDispatch is the run a dispatch reports. Created is true only
// when this dispatch started the run, so an undelivered dispatch can clear it.
type profileRefreshDispatch struct {
	RunID   string
	Trigger string
	Created bool
}

func isAutomaticProfileRefreshTrigger(trigger string) bool {
	return trigger == profileRefreshTriggerPluginTask || trigger == profileRefreshTriggerPendingLine
}

// setPendingProfileRefreshRun records a dispatched run inside a profilestate
// mutation and returns the run to report. A run_id is generated only when a
// new run starts.
//
//   - An automatic dispatch (plugin_task, pending_line) for a run that is still
//     unfinished, has the same trigger, and is younger than
//     profileRefreshRunReuseWindow keeps the original run_id and dispatch
//     time, so hourly re-prompts of one ignored refresh count as one failed
//     run rather than one per reminder.
//   - A manual forced review never replaces an unfinished automatic run
//     younger than the reuse window: the forced review completes that run and
//     the completion keeps its automatic trigger. Otherwise manual force
//     starts its own manual_force run.
//
// When the random source fails and nothing can be reused, the state is left
// unchanged and the returned RunID is empty, so nothing is reported.
func setPendingProfileRefreshRun(state *profilestate.State, trigger string, now int64) profileRefreshDispatch {
	unfinished := state.PendingRunID != "" &&
		state.PendingRunDispatchedUnix > 0 && state.PendingRunDispatchedUnix <= now &&
		now-state.PendingRunDispatchedUnix < int64(profileRefreshRunReuseWindow/time.Second)
	if unfinished && isAutomaticProfileRefreshTrigger(state.PendingRunTrigger) &&
		(state.PendingRunTrigger == trigger || trigger == profileRefreshTriggerManualForce) {
		return profileRefreshDispatch{RunID: state.PendingRunID, Trigger: state.PendingRunTrigger}
	}
	runID := newProfileRefreshRunID()
	if runID == "" {
		return profileRefreshDispatch{}
	}
	state.PendingRunID = runID
	state.PendingRunTrigger = trigger
	state.PendingRunDispatchedUnix = now
	return profileRefreshDispatch{RunID: runID, Trigger: trigger, Created: true}
}

// clearUndeliveredProfileRefreshRun removes a run created by a dispatch whose
// task or line was never delivered, so a later completion is not linked to
// it. A reused run was delivered earlier and is kept, as is any newer run.
func clearUndeliveredProfileRefreshRun(home, server, agentID string, dispatch profileRefreshDispatch) {
	if !dispatch.Created || dispatch.RunID == "" {
		return
	}
	_, _ = profilestate.Update(home, server, agentID, func(state *profilestate.State) bool {
		if state.PendingRunID != dispatch.RunID {
			return false
		}
		state.PendingRunID, state.PendingRunTrigger, state.PendingRunDispatchedUnix = "", "", 0
		return true
	})
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

func reportProfileRefreshDispatched(serverName string, dispatch profileRefreshDispatch) {
	reportProfileRefreshRun(serverName, profileRefreshRunEvent{RunID: dispatch.RunID, Stage: "dispatched", Trigger: dispatch.Trigger})
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
	// Never rotate or extend credentials for telemetry; the triggering
	// command already refreshed them when needed.
	c, v2, err := newStoredCredentialClientForServer(serverName)
	if err != nil {
		return err
	}
	path := legacyProfileRefreshRunsPath
	if v2 {
		path = v2ProfileRefreshRunsPath
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
