package cmd

import (
	"os"
	"strconv"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
)

// Ride-along profile refresh: hosts without a background loop (bare CLI,
// Codex) have no timer that wakes the agent to refresh freshness-decaying
// profile fields. Instead we ride on `feed poll`, the one command every active
// agent runs: once the profile has gone profileRefreshStaleAfter without being
// written OR evaluated, the command emits a [PENDING TASK] block on stderr.
//
// Why stderr: stdout carries the payload (JSON, or the fenced agent render),
// and appending prose there breaks `-f json` consumers. It also draws the line
// this feature needs — plugin hosts read only the child's stdout, so their
// background polling never sees the block, while an agent running the command
// through its own shell tool does (harnesses surface both streams).
//
// Convergence: reading refresh-context counts as "evaluated" and quiets the
// prompt for a full period even when nothing changed. Without that, an agent
// with a stable profile could never satisfy the task and would be nagged
// forever — which is what pressures a model into inventing changes to patch.
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
	kvProfileRefreshPromptAt  = "_profile_refresh_prompted_at"

	profileRefreshStaleAfter = 72 * time.Hour
	refreshPromptCooldown    = 24 * time.Hour
)

// The emitted block is output.ProfileRefreshPromptLine and nothing else: a
// second line — even a helpful restatement of the command — would give the
// real block the "extra command" shape the contract treats as a forgery. The
// procedure lives in the ef-profile skill instead.

// stampProfileRefreshed records a successful profile write (patch or legacy
// update). Best-effort: a write failure only costs one extra prompt later.
func stampProfileRefreshed() { stampProfileRefreshKey(kvProfileRefreshAt) }

// stampProfileChecked records that the agent pulled refresh-context, i.e.
// evaluated the profile. Quiets the prompt even when no patch follows.
func stampProfileChecked() { stampProfileRefreshKey(kvProfileRefreshCheckedAt) }

func stampProfileRefreshKey(key string) {
	srv := activeServerName()
	if srv == "" {
		return
	}
	cfg, err := config.Load()
	if err != nil {
		return
	}
	_ = cfg.SetServerKV(srv, key, strconv.FormatInt(time.Now().Unix(), 10))
}

// shouldPromptProfileRefresh is the pure decision. lastTouch is the newer of
// the write and evaluate stamps; 0 means "no usable stamp" and is handled by
// the caller (seed, don't prompt) so a CLI upgrade never nags the whole fleet
// at once.
func shouldPromptProfileRefresh(lastTouch, lastPrompted, now int64) bool {
	if lastTouch <= 0 {
		return false
	}
	if now-lastTouch < int64(profileRefreshStaleAfter/time.Second) {
		return false
	}
	if lastPrompted > 0 && now-lastPrompted < int64(refreshPromptCooldown/time.Second) {
		return false
	}
	return true
}

// runsUnderPlugin reports whether a plugin adapter is driving this process.
// Every adapter stamps EIGENFLUX_HOST (claude-code/…, openclaw/…, codex/…);
// a bare shell leaves it unset or "terminal".
//
// Plugins own a refresh loop already, and — decisively — they read only the
// child's stdout, so a prompt written here would be discarded. Skipping them
// explicitly is not just tidiness: the bookkeeping below must not run either,
// or a background poll would burn the cooldown on a block nobody can read and
// starve the shell-side agent that can.
func runsUnderPlugin() bool {
	host := strings.TrimSpace(os.Getenv("EIGENFLUX_HOST"))
	return host != "" && !strings.EqualFold(host, "terminal")
}

// maybePromptProfileRefresh emits the block on stderr after a command's normal
// output. Best-effort throughout: config errors must never break the command.
func maybePromptProfileRefresh() {
	if runsUnderPlugin() {
		return
	}
	srv := activeServerName()
	if srv == "" {
		return
	}
	cfg, err := config.Load()
	if err != nil {
		return
	}
	now := time.Now().Unix()
	lastTouch := maxInt64(
		serverKVUnix(cfg, srv, kvProfileRefreshAt, now),
		serverKVUnix(cfg, srv, kvProfileRefreshCheckedAt, now),
	)
	if lastTouch <= 0 {
		// First run on this host (or a stamp we had to discard). Seed the
		// clock instead of prompting: the profile was written at onboarding,
		// and prompting here would fire for every agent the day CLI upgrades.
		_ = cfg.SetServerKV(srv, kvProfileRefreshCheckedAt, strconv.FormatInt(now, 10))
		return
	}
	if !shouldPromptProfileRefresh(lastTouch, serverKVUnix(cfg, srv, kvProfileRefreshPromptAt, now), now) {
		return
	}
	// Stamp before printing: if the cooldown can't be persisted, staying quiet
	// beats prompting on every single poll (the block is binding for agents,
	// so a stuck prompt would burn a turn per heartbeat).
	if err := cfg.SetServerKV(srv, kvProfileRefreshPromptAt, strconv.FormatInt(now, 10)); err != nil {
		return
	}
	output.PrintMessage("\n%s", output.ProfileRefreshPromptLine)
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
