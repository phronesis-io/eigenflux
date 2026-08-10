package cmd

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
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
// streams. Plugin-owned refresh loops are skipped before state is touched,
// because their adapters discard stderr and run their own refresh timer. A
// short prompt cooldown still matters for bare agents:
// without it, a five-minute feed cadence can enqueue the same task repeatedly
// while the previous turn is still working. The one-hour window bounds that
// duplication.
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
	kvProfileRefreshAt         = "_profile_refresh_at"
	kvProfileRefreshCheckedAt  = "_profile_refresh_checked_at"
	kvProfileRefreshPromptedAt = "_profile_refresh_prompted_at"

	profileRefreshStaleAfter = 24 * time.Hour
	profilePromptCooldown    = time.Hour
	profilePromptClaimLease  = time.Minute
)

var profilePromptWriter io.Writer = os.Stderr

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
	return stampProfileRefreshKeyFor(srv, agentID, key)
}

func stampProfileRefreshKeyFor(srv, agentID, key string) error {
	now := time.Now().Unix()
	if key != kvProfileRefreshAt && key != kvProfileRefreshCheckedAt {
		return fmt.Errorf("unknown profile refresh state key %q", key)
	}
	_, err := profilestate.Update(config.HomeDir(), srv, agentID, func(state *profilestate.State) bool {
		if key == kvProfileRefreshAt {
			state.LastRefreshUnix = now
		} else {
			state.LastCheckedUnix = now
		}
		state.LastPromptedUnix = 0
		return true
	})
	return err
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
	return lastPrompted <= 0 || now-lastPrompted >= int64(profilePromptCooldown/time.Second)
}

// maybePromptProfileRefresh emits the block on stderr after a command's normal
// output. Best-effort throughout: config errors must never break the command.
func maybePromptProfileRefresh() {
	srv, agentID := activeProfileStateScope()
	if srv == "" || agentID == "" {
		return
	}
	maybePromptProfileRefreshFor(srv, agentID)
}

func maybePromptProfileRefreshFor(srv, agentID string) {
	// Plugin hosts own their refresh loop and discard CLI stderr. Keep this
	// gate in the scoped implementation so feed poll cannot bypass it.
	if pluginOwnsProfileRefresh(clientMeta.Host, clientMeta.Channel) {
		return
	}
	now := time.Now().Unix()
	emit := false
	claimStamp := now - int64((profilePromptCooldown-profilePromptClaimLease)/time.Second)
	_, err := profilestate.Update(config.HomeDir(), srv, agentID, func(state *profilestate.State) bool {
		lastTouch := maxInt64(
			validProfileStamp(state.LastRefreshUnix, now),
			validProfileStamp(state.LastCheckedUnix, now),
		)
		if lastTouch <= 0 {
			// First run on this host (or a stamp we had to discard). Seed the
			// clock instead of prompting: the profile was written at onboarding,
			// and prompting here would fire for every agent the day CLI upgrades.
			state.LastCheckedUnix = now
			return true
		}
		lastPrompted := validProfileStamp(state.LastPromptedUnix, now)
		if !shouldPromptProfileRefresh(lastTouch, lastPrompted, now) {
			return false
		}
		// Claim briefly before writing so concurrent polls cannot duplicate the
		// task. A failed write is retried after the short lease, not one hour.
		state.LastPromptedUnix = claimStamp
		emit = true
		return true
	})
	if err != nil || !emit {
		return
	}
	if err := output.PrintMessageTo(profilePromptWriter, "\n%s", output.ProfileRefreshPromptLine); err != nil {
		return
	}
	_, _ = profilestate.Update(config.HomeDir(), srv, agentID, func(state *profilestate.State) bool {
		if state.LastPromptedUnix != claimStamp {
			return false
		}
		state.LastPromptedUnix = now
		return true
	})
}

func pluginOwnsProfileRefresh(host, channel string) bool {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" || channel == "cli" || channel == "skill" {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(strings.SplitN(host, "/", 2)[0]))
	switch name {
	case "openclaw", "claude-code", "codex":
		return true
	default:
		return false
	}
}

func activeProfileStateScope() (string, string) {
	srv := activeServerName()
	return profileStateScopeForServer(srv)
}

func profileStateScopeForServer(srv string) (string, string) {
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
