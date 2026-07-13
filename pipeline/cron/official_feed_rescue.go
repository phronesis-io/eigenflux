package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apidal "eigenflux_server/api/dal"
	"eigenflux_server/pipeline/official"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	pmdal "eigenflux_server/rpc/pm/dal"
	profiledal "eigenflux_server/rpc/profile/dal"

	"github.com/redis/go-redis/v9"
)

const lockKeyOfficialFeedRescue = "lock:cron:official_feed_rescue"

// StartOfficialFeedRescue (#4) periodically finds official-account friends whose
// feed in their declared domains has been quiet, and DMs them a couple of
// concrete topic suggestions (drawn from network-wide trending) nudging them to
// broaden a domain or update their profile.
func StartOfficialFeedRescue(ctx context.Context, cfg *config.Config, rdb *redis.Client, oc *official.Sender) {
	interval := time.Duration(cfg.OfficialRescueIntervalSec) * time.Second
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	runOfficialFeedRescue(ctx, cfg, rdb, oc)
	logger.Default().Info("official feed-rescue cron started", "interval_sec", cfg.OfficialRescueIntervalSec)

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("official feed-rescue cron stopped")
			return
		case <-ticker.C:
			runOfficialFeedRescue(ctx, cfg, rdb, oc)
		}
	}
}

func runOfficialFeedRescue(ctx context.Context, cfg *config.Config, rdb *redis.Client, oc *official.Sender) {
	acquired, err := acquireLock(ctx, rdb, lockKeyOfficialFeedRescue, 20*time.Minute)
	if err != nil || !acquired {
		return
	}
	defer releaseLock(ctx, rdb, lockKeyOfficialFeedRescue)

	officialID := oc.ResolveOfficialID()
	if officialID == 0 {
		logger.Default().Info("official feed-rescue skipped (official account not provisioned)")
		return
	}

	windowDays := cfg.OfficialRescueWindowDays
	if windowDays <= 0 {
		windowDays = 3
	}
	threshold := cfg.OfficialRescueThreshold
	if threshold <= 0 {
		threshold = 30
	}
	cooldownDays := cfg.OfficialRescueCooldownDays
	if cooldownDays <= 0 {
		cooldownDays = 3
	}
	cooldown := time.Duration(cooldownDays) * 24 * time.Hour
	sinceMs := time.Now().AddDate(0, 0, -windowDays).UnixMilli()

	// Network-wide hot topics (7d) used as the recommendation pool.
	hotSinceMs := time.Now().AddDate(0, 0, -7).UnixMilli()
	var hotTopics []string
	if agg, aerr := apidal.GetNetworkSignalAgg(ctx, db.DB, "7d", hotSinceMs); aerr == nil && agg != nil {
		hotTopics = pickTrendingTopics(agg.Counts, cfg.OfficialTrendingPoolN, 5)
	}

	llmBudget := cfg.OfficialLLMMaxPerRun
	if llmBudget <= 0 {
		llmBudget = 100
	}
	sent := 0
	var cursor int64
	for {
		friends, ferr := pmdal.ListFriends(db.DB, officialID, cursor, 200)
		if ferr != nil || len(friends) == 0 {
			break
		}
		for _, f := range friends {
			cursor = f.RelationID
			agent, aerr := profiledal.GetAgentByID(db.DB, f.AgentID)
			if aerr != nil {
				continue
			}
			if !oc.PassesGate(officialID, f.AgentID, agent.Email) {
				continue
			}
			domains := agentRecentDomains(f.AgentID)
			if len(domains) == 0 {
				continue
			}
			cnt, cerr := deliveredCountInDomains(f.AgentID, sinceMs, domains)
			if cerr != nil {
				continue
			}
			// Only recommend when their in-domain feed is below the threshold.
			if cnt >= threshold {
				continue
			}
			if !oc.CooldownAcquire(ctx, "rescue", f.AgentID, cooldown) {
				continue
			}
			if llmBudget <= 0 {
				oc.CooldownRelease(ctx, "rescue", f.AgentID)
				logger.Default().Warn("official feed-rescue: LLM budget exhausted for this run")
				goto done
			}
			task := fmt.Sprintf(
				"Scenario 3 (topic-recommendation DM, feed quiet). The member's declared domains are: %s. Their feed in these areas has been quiet over the last %d days (only %d items). Network-wide hot topics right now: %s. Pick 1-2 concrete topics/directions relevant to their declared interests; suggest they broaden a domain or update their profile to catch more; leave a light opt-out. Match their language.",
				strings.Join(domains, ", "), windowDays, cnt, strings.Join(hotTopics, ", "),
			) + official.LangDirective(f.AgentID)
			content, gerr := oc.Generate(ctx, task)
			llmBudget--
			if gerr != nil || content == "" {
				oc.CooldownRelease(ctx, "rescue", f.AgentID)
				continue
			}
			if !oc.Send(ctx, officialID, f.AgentID, content) {
				oc.CooldownRelease(ctx, "rescue", f.AgentID)
				continue
			}
			sent++
		}
		if len(friends) < 200 {
			break
		}
	}
done:
	logger.Default().Info("official feed-rescue completed", "sent", sent)
}

// agentRecentDomains returns the agent's declared domains from its most recent
// replay_logs snapshot (agent_features.domains). Empty when none recorded.
func agentRecentDomains(agentID int64) []string {
	var raw string
	err := db.DB.Raw(
		`SELECT COALESCE(agent_features->'domains','[]')::text
		   FROM replay_logs WHERE agent_id = ? ORDER BY served_at DESC LIMIT 1`,
		agentID,
	).Scan(&raw).Error
	if err != nil || raw == "" {
		return nil
	}
	var ds []string
	if json.Unmarshal([]byte(raw), &ds) != nil {
		return nil
	}
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// deliveredCountInDomains counts distinct delivered items served to the agent in
// the window whose domains overlap the given domains.
func deliveredCountInDomains(agentID, sinceMs int64, domains []string) (int, error) {
	if len(domains) == 0 {
		return 0, nil
	}
	var n int
	// Bind domains as a single Postgres text[] literal + cast, rather than a Go
	// []string (which gorm would expand into multiple placeholders).
	err := db.DB.Raw(
		`SELECT count(DISTINCT item_id) FROM replay_logs
		  WHERE agent_id = ? AND served_at >= ? AND delivered IS DISTINCT FROM FALSE
		    AND jsonb_exists_any(item_features->'domains', ?::text[])`,
		agentID, sinceMs, pgTextArray(domains),
	).Scan(&n).Error
	return n, err
}

// pgTextArray renders a Postgres text[] array literal (e.g. {"ai","fintech"})
// from a Go slice, quoting/escaping each element.
func pgTextArray(items []string) string {
	parts := make([]string, len(items))
	for i, it := range items {
		s := strings.ReplaceAll(it, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		parts[i] = `"` + s + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}
