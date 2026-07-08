package cmd

import (
	"net/http"
	"strconv"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/skills"
)

const (
	// autoSkillSyncKey disables the background refresh when set to "false".
	// Absent/any-other value => enabled (this is on by default).
	autoSkillSyncKey = "auto_skill_sync"
	// skillSyncIntervalKey overrides the throttle TTL, in whole hours.
	skillSyncIntervalKey = "skill_sync_interval_hours"

	defaultSkillSyncTTL = 24 * time.Hour
	// autoSkillSyncTimeout bounds the one-per-TTL blocking sync so a slow CDN
	// can never stall the heartbeat for long. `--if-stale` means the common
	// case is a single small manifest GET; only a real revision change downloads
	// the (tiny) skills tarball.
	autoSkillSyncTimeout = 10 * time.Second
)

// maybeSyncSkills is a best-effort, throttled background skill refresh meant to
// hang off the feed-poll heartbeat. It NEVER returns an error and blocks for at
// most autoSkillSyncTimeout, and only once per TTL (default 24h) — the rest of
// the heartbeats just read a small local state file and return. It writes to
// the host's autodetected skill-load dir (terminal => ~/.agents/skills);
// plugin hosts that load their own bundle are refreshed by the plugin's own
// periodic sync, not this hook.
func maybeSyncSkills(cfg *config.Config) {
	if cfg == nil || cfg.GetKV(autoSkillSyncKey) == "false" {
		return
	}

	ttl := defaultSkillSyncTTL
	if v := cfg.GetKV(skillSyncIntervalKey); v != "" {
		if h, err := strconv.Atoi(v); err == nil && h > 0 {
			ttl = time.Duration(h) * time.Hour
		}
	}

	home := config.HomeDir()
	state := skills.LoadAutoSyncState(home)
	if !skills.DueForAutoSync(state, ttl, time.Now()) {
		return
	}

	// Stamp the attempt BEFORE calling out, so an offline/slow stretch can't
	// re-punch the TTL gate on every subsequent heartbeat.
	state.LastAttemptUnix = time.Now().Unix()

	res, err := skills.Sync(skills.SyncOptions{
		IfStale:    true,
		Quiet:      true,
		CLIVersion: version,
		CDNBase:    cdnBase(),
		HTTPClient: &http.Client{Timeout: autoSkillSyncTimeout},
	})
	state.LastResult = classifyAutoSync(res, err)
	if res != nil {
		if m, mErr := skills.ReadLocalManifest(res.SkillsDir); mErr == nil && m != nil {
			state.LastRevision = m.Revision
		}
	}
	_ = skills.SaveAutoSyncState(home, state)
}

// classifyAutoSync maps a Sync outcome to a short state label for `doctor`.
func classifyAutoSync(res *skills.SyncResult, err error) string {
	switch {
	case err != nil || res == nil:
		return "error"
	case res.NoNetwork:
		return "offline"
	case res.Source == "local":
		// revision matched (or lock held) => nothing changed
		return "nochange"
	default:
		return "ok"
	}
}
