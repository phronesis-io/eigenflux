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
	// maxSkillSyncHours clamps a user- or backend-supplied interval to [1h, 1y].
	// The clamp is applied to the raw hour count BEFORE multiplying by
	// time.Hour, so an absurd value (e.g. pushed by a compromised backend) can
	// never overflow time.Duration into a negative TTL — which would flip the
	// throttle gate permanently open and hammer the CDN every heartbeat.
	minSkillSyncHours = 1
	maxSkillSyncHours = 365 * 24
	// autoSkillSyncTimeout bounds a SINGLE CDN request. A due heartbeat does a
	// few small GETs (manifest, then a tiny tarball only on a real change), so
	// worst-case blocking is a small multiple of this — not a hard per-sync cap.
	autoSkillSyncTimeout = 5 * time.Second
)

// maybeSyncSkills is a best-effort, throttled background skill refresh meant to
// hang off the feed-poll heartbeat. It NEVER returns an error and does real work
// at most once per TTL (default 24h) — the rest of the heartbeats just read a
// small local state file and return. When it does run, network is bounded per
// request (autoSkillSyncTimeout); the disk swap is not, so on the ~once/day
// update it can block a few seconds. It writes to the host's autodetected
// skill-load dir (terminal => ~/.agents/skills); plugin hosts that load their
// own bundle are refreshed by the plugin's own periodic sync, not this hook.
func maybeSyncSkills(cfg *config.Config) {
	if cfg == nil || cfg.GetKV(autoSkillSyncKey) == "false" {
		return
	}

	ttl := defaultSkillSyncTTL
	if v := cfg.GetKV(skillSyncIntervalKey); v != "" {
		// Clamp the hour count before multiplying (overflow-safe); out-of-range
		// or non-numeric falls back to the default TTL.
		if h, err := strconv.Atoi(v); err == nil && h >= minSkillSyncHours && h <= maxSkillSyncHours {
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
