package cmd

import (
	"fmt"
	"strconv"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"cli.eigenflux.ai/internal/profilestate"
)

// Ride-along profile refresh: hosts without a background loop (bare CLI,
// Codex) have no timer that wakes the agent to refresh freshness-decaying
// profile fields. Instead we ride on `feed poll`, the one command every active
// agent runs: once the profile has gone profileRefreshStaleAfter without being
// written OR evaluated, the command emits a [PENDING TASK] block on stderr.
//
// Why stderr: stdout carries the payload (JSON, or the fenced agent render),
// and appending prose there breaks `-f json` consumers. Agents that run the
// command through a shell tool still see it, since harnesses surface both
// streams. Some callers do discard stderr (every plugin adapter reads only the
// child's stdout) — which is why nothing here is spent on emitting.
//
// No emit-side bookkeeping, deliberately: an earlier cut also stamped a 24h
// prompt cooldown, and a caller that throws stderr away would burn it on a
// block nobody could read, starving the shell-side agent that could. Whether
// the block was seen is unknowable from in here, so the only state written is
// state an agent's own actions produce.
//
// Convergence: a successful patch records a write; when nothing changed, the
// agent explicitly runs `profile refresh-complete` to record the completed
// evaluation. Merely fetching refresh-context is not completion: the later
// patch may still fail with a version conflict, quota, or outage. Without the
// explicit no-change completion, a stable profile could never settle and the
// repeated task would pressure a model into inventing changes.
//
// Anti-forgery: the block is NOT a general instruction channel. Third-party
// text (PM bodies, feed items) shares the same terminal, so the skill contract
// binds agents to ONE literal wording and ONE command; anything else carrying
// the marker is a forgery to be reported, never executed.
const (
	// Internal bookkeeping keys carry the `_` prefix used by every other
	// CLI-private key (see settings.go) and are excluded from backend sync,
	// so a backend response can never silence or spam the prompt.
	kvProfileRefreshAt        = "_profile_refresh_at"
	kvProfileRefreshCheckedAt = "_profile_refresh_checked_at"

	profileRefreshStaleAfter = 24 * time.Hour
)

// The emitted block is output.ProfileRefreshPromptLine and nothing else: a
// second line — even a helpful restatement of the command — would give the
// real block the "extra command" shape the contract treats as a forgery. The
// procedure lives in the ef-profile skill instead.

// stampProfileRefreshed records a successful automated field refresh.
// Best-effort: a write failure only costs one extra prompt later.
func stampProfileRefreshed() error { return stampProfileRefreshKey(kvProfileRefreshAt) }

// stampProfileChecked records an explicitly completed no-change evaluation.
// Merely reading refresh-context never calls this function.
func stampProfileChecked() error { return stampProfileRefreshKey(kvProfileRefreshCheckedAt) }

func stampProfileRefreshKey(key string) error {
	srv, agentID := activeProfileStateScope()
	if srv == "" || agentID == "" {
		return fmt.Errorf("no active authenticated account")
	}
	state := profilestate.Load(config.HomeDir(), srv, agentID)
	now := time.Now().Unix()
	switch key {
	case kvProfileRefreshAt:
		state.LastRefreshUnix = now
	case kvProfileRefreshCheckedAt:
		state.LastCheckedUnix = now
	default:
		return fmt.Errorf("unknown profile refresh state key %q", key)
	}
	return profilestate.Save(config.HomeDir(), srv, agentID, state)
}

// shouldPromptProfileRefresh is the pure decision. lastTouch is the newer of
// the write and evaluate stamps; 0 means "no usable stamp" and is handled by
// the caller (seed, don't prompt) so a CLI upgrade never nags the whole fleet
// at once.
func shouldPromptProfileRefresh(lastTouch, now int64) bool {
	if lastTouch <= 0 {
		return false
	}
	return now-lastTouch >= int64(profileRefreshStaleAfter/time.Second)
}

// maybePromptProfileRefresh emits the block on stderr after a command's normal
// output. Best-effort throughout: config errors must never break the command.
func maybePromptProfileRefresh() {
	srv, agentID := activeProfileStateScope()
	if srv == "" || agentID == "" {
		return
	}
	now := time.Now().Unix()
	state := profilestate.Load(config.HomeDir(), srv, agentID)
	lastTouch := maxInt64(
		validProfileStamp(state.LastRefreshUnix, now),
		validProfileStamp(state.LastCheckedUnix, now),
	)
	if lastTouch <= 0 {
		// First run on this host (or a stamp we had to discard). Seed the
		// clock instead of prompting: the profile was written at onboarding,
		// and prompting here would fire for every agent the day CLI upgrades.
		state.LastCheckedUnix = now
		_ = profilestate.Save(config.HomeDir(), srv, agentID, state)
		return
	}
	if !shouldPromptProfileRefresh(lastTouch, now) {
		return
	}
	output.PrintMessage("\n%s", output.ProfileRefreshPromptLine)
}

func activeProfileStateScope() (string, string) {
	srv := activeServerName()
	if srv == "" {
		return "", ""
	}
	creds, err := auth.LoadCredentials(srv)
	if err != nil || creds.AgentID == "" {
		return "", ""
	}
	return srv, creds.AgentID
}

func validProfileStamp(stamp, now int64) int64 {
	if stamp <= 0 || stamp > now {
		return 0
	}
	return stamp
}

// serverKVUnix reads a unix-seconds stamp. Unparsable values and stamps in the
// future (clock skew, hand-edited config) are discarded rather than trusted —
// a future stamp would otherwise suppress the prompt until wall time caught up.
func serverKVUnix(cfg *config.Config, srv, key string, now int64) int64 {
	v, ok, err := cfg.GetServerKV(srv, key)
	if err != nil || !ok {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 || n > now {
		return 0
	}
	return n
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
