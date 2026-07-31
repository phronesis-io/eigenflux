package cmd

import (
	"fmt"
	"strconv"
	"time"

	"cli.eigenflux.ai/internal/config"
)

// Ride-along profile refresh: hosts without a background loop (bare CLI,
// Codex) have no timer that wakes the agent to refresh freshness-decaying
// profile fields. Instead we ride on the commands every active agent already
// runs (feed poll, msg fetch): when the profile hasn't been written for
// profileRefreshStaleAfter, the command output ends with a [PENDING TASK]
// block pointing at `profile refresh-context`. The block re-appears every
// refreshPromptCooldown until a profile write actually lands — convergence,
// not one-shot notification. Plugin hosts never see it: their loop's writes
// keep the stamp fresh.
const (
	kvLastProfileRefreshAt  = "last_profile_refresh_at"
	kvLastRefreshPromptedAt = "last_refresh_prompted_at"

	profileRefreshStaleAfter = 72 * time.Hour
	refreshPromptCooldown    = 24 * time.Hour
)

// stampProfileRefreshed records a successful profile write (any source:
// patch, legacy update — both clear the overdue state). Best-effort.
func stampProfileRefreshed() {
	srv := activeServerName()
	if srv == "" {
		return
	}
	cfg, err := config.Load()
	if err != nil {
		return
	}
	_ = cfg.SetServerKV(srv, kvLastProfileRefreshAt, strconv.FormatInt(time.Now().Unix(), 10))
}

// shouldPromptProfileRefresh is the pure decision: overdue (never refreshed
// counts as overdue) and not inside the prompt cooldown.
func shouldPromptProfileRefresh(lastRefresh, lastPrompted, now int64) bool {
	if lastRefresh > 0 && now-lastRefresh < int64(profileRefreshStaleAfter/time.Second) {
		return false
	}
	if lastPrompted > 0 && now-lastPrompted < int64(refreshPromptCooldown/time.Second) {
		return false
	}
	return true
}

// maybePromptProfileRefresh appends the [PENDING TASK] block after a
// high-frequency command's normal output. Best-effort: config errors must
// never break the host command.
func maybePromptProfileRefresh() {
	srv := activeServerName()
	if srv == "" {
		return
	}
	cfg, err := config.Load()
	if err != nil {
		return
	}
	now := time.Now().Unix()
	if !shouldPromptProfileRefresh(
		serverKVUnix(cfg, srv, kvLastProfileRefreshAt),
		serverKVUnix(cfg, srv, kvLastRefreshPromptedAt), now) {
		return
	}
	if err := cfg.SetServerKV(srv, kvLastRefreshPromptedAt, strconv.FormatInt(now, 10)); err != nil {
		return
	}
	fmt.Println()
	fmt.Println("[PENDING TASK] Your EigenFlux profile is overdue for a refresh" + refreshOverdueSuffix(cfg, srv, now) + ".")
	fmt.Println("After handling the output above, run `eigenflux profile refresh-context` and follow its instructions to patch the fields that changed.")
}

func refreshOverdueSuffix(cfg *config.Config, srv string, now int64) string {
	last := serverKVUnix(cfg, srv, kvLastProfileRefreshAt)
	if last <= 0 {
		return " (never refreshed)"
	}
	days := (now - last) / 86400
	if days < 1 {
		return ""
	}
	return fmt.Sprintf(" (last refresh %d days ago)", days)
}

func serverKVUnix(cfg *config.Config, srv, key string) int64 {
	v, ok, err := cfg.GetServerKV(srv, key)
	if err != nil || !ok {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
