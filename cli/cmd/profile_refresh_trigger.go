package cmd

import (
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
)

// Refresh trigger attribution. The CLI is the only component that knows
// whether a Periodic Profile Refresh was dispatched by a schedule (a plugin
// heartbeat calling `profile refresh-task`, or the feed-poll [PENDING TASK]
// reminder) or by an explicit user request (`profile refresh-task --force`).
// It records that decision in the account's profile-refresh sidecar at
// dispatch time; `profile patch --source cli_daily_refresh` and
// `profile refresh-complete` read it back and report it to the server.
//
// A refresh that was not dispatched by the CLI (the agent decided on its own
// to refresh) or whose record has expired reports "unknown" and keeps its
// source unchanged, exactly like older CLIs.
const (
	profileRefreshTriggerHeader = "X-EF-Profile-Refresh-Trigger"

	profileRefreshTriggerScheduled = "scheduled"
	profileRefreshTriggerManual    = "manual"
	profileRefreshTriggerUnknown   = "unknown"

	// profileRefreshSourceScheduled is the historical source of automated
	// refresh writes; scheduled runs keep it so existing history stays
	// comparable. A manually requested run is recorded as
	// profileRefreshSourceManual instead.
	profileRefreshSourceScheduled = "cli_daily_refresh"
	profileRefreshSourceManual    = "cli_manual_refresh"

	// profileRefreshTriggerTTL bounds how long a dispatch is attributed to
	// later refresh writes; host delivery can lag behind the dispatch.
	profileRefreshTriggerTTL = 6 * time.Hour
	// profileRefreshTriggerGrace keeps the attribution for follow-up writes
	// of the same run (a second patch, a retry after a stale completion) once
	// the run has recorded its first completion.
	profileRefreshTriggerGrace = 15 * time.Minute
)

func isProfileRefreshSource(source string) bool {
	return source == profileRefreshSourceScheduled || source == profileRefreshSourceManual
}

// recordProfileRefreshDispatch marks a delivered refresh task. The newest
// dispatch wins.
func recordProfileRefreshDispatch(state *profilestate.State, trigger string, now int64) {
	state.RefreshTrigger = trigger
	state.RefreshTriggerExpiresUnix = now + int64(profileRefreshTriggerTTL/time.Second)
}

// activeProfileRefreshTrigger returns the recorded trigger while it is valid.
// Missing, unknown, expired, or implausibly far-future records (clock skew,
// hand edits) are "unknown".
func activeProfileRefreshTrigger(state profilestate.State, now int64) string {
	if state.RefreshTrigger != profileRefreshTriggerScheduled && state.RefreshTrigger != profileRefreshTriggerManual {
		return profileRefreshTriggerUnknown
	}
	remaining := state.RefreshTriggerExpiresUnix - now
	if remaining <= 0 || remaining > int64(profileRefreshTriggerTTL/time.Second) {
		return profileRefreshTriggerUnknown
	}
	return state.RefreshTrigger
}

// settleProfileRefreshTrigger shortens a valid record to the follow-up grace
// once the run records a completion, so a later refresh the agent starts on
// its own is not attributed to this dispatch.
func settleProfileRefreshTrigger(state *profilestate.State, now int64) {
	if activeProfileRefreshTrigger(*state, now) == profileRefreshTriggerUnknown {
		state.RefreshTrigger = ""
		state.RefreshTriggerExpiresUnix = 0
		return
	}
	if limit := now + int64(profileRefreshTriggerGrace/time.Second); state.RefreshTriggerExpiresUnix > limit {
		state.RefreshTriggerExpiresUnix = limit
	}
}

// currentProfileRefreshTrigger reads the trigger for one account. Read
// failures degrade to "unknown"; attribution must never block a refresh.
func currentProfileRefreshTrigger(srv, agentID string) string {
	state := profilestate.Load(config.HomeDir(), srv, agentID)
	return activeProfileRefreshTrigger(state, time.Now().Unix())
}

// profileRefreshWriteSource returns the source recorded for a refresh patch.
// Only a scheduled-source patch inside a manual run is rewritten.
func profileRefreshWriteSource(source, trigger string) string {
	if source == profileRefreshSourceScheduled && trigger == profileRefreshTriggerManual {
		return profileRefreshSourceManual
	}
	return source
}
