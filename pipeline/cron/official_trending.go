package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
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

const lockKeyOfficialTrending = "lock:cron:official_trending"

// StartOfficialTrending (#5) periodically DMs the official account's friends a
// curated set of network-wide trending topics. Topics reuse the existing
// network-signal aggregation; the message is generated once per cycle and
// shared across recipients to bound LLM cost.
func StartOfficialTrending(ctx context.Context, cfg *config.Config, rdb *redis.Client, oc *official.Sender) {
	interval := time.Duration(cfg.OfficialTrendingIntervalSec) * time.Second
	if interval <= 0 {
		interval = 14 * 24 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	runOfficialTrending(ctx, cfg, rdb, oc)
	logger.Default().Info("official trending cron started", "interval_sec", cfg.OfficialTrendingIntervalSec)

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("official trending cron stopped")
			return
		case <-ticker.C:
			runOfficialTrending(ctx, cfg, rdb, oc)
		}
	}
}

func runOfficialTrending(ctx context.Context, cfg *config.Config, rdb *redis.Client, oc *official.Sender) {
	acquired, err := acquireLock(ctx, rdb, lockKeyOfficialTrending, 20*time.Minute)
	if err != nil || !acquired {
		return
	}
	defer releaseLock(ctx, rdb, lockKeyOfficialTrending)

	officialID := oc.ResolveOfficialID()
	if officialID == 0 {
		logger.Default().Info("official trending skipped (official account not provisioned)")
		return
	}

	windowDays := cfg.OfficialTrendingWindowDays
	if windowDays <= 0 {
		windowDays = 7
	}
	sinceMs := time.Now().AddDate(0, 0, -windowDays).UnixMilli()
	agg, err := apidal.GetNetworkSignalAgg(ctx, db.DB, fmt.Sprintf("%dd", windowDays), sinceMs)
	if err != nil || agg == nil || len(agg.Counts) == 0 {
		logger.Default().Warn("official trending: no network signals", "err", err)
		return
	}
	topics := pickTrendingTopics(agg.Counts, cfg.OfficialTrendingPoolN, cfg.OfficialTrendingPickN)
	if len(topics) == 0 {
		return
	}

	// One generation per cycle, shared by all recipients (bounds LLM cost).
	task := fmt.Sprintf(
		"Scenario 4 (network-wide trending topics, periodic). The current trending topics across the network are: %s. Write the periodic trending DM: curated, at most %d, each with one line on why it's worth a look, and a light way to mute or reduce frequency.",
		strings.Join(topics, ", "), cfg.OfficialTrendingPickN,
	)
	content, err := oc.Generate(ctx, task)
	if err != nil || content == "" {
		logger.Default().Warn("official trending: generate failed", "err", err)
		return
	}

	cooldown := time.Duration(cfg.OfficialTrendingIntervalSec) * time.Second
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
			if !oc.CooldownAcquire(ctx, "trending", f.AgentID, cooldown) {
				continue
			}
			if !oc.Send(ctx, officialID, f.AgentID, content) {
				oc.CooldownRelease(ctx, "trending", f.AgentID)
				continue
			}
			sent++
		}
		if len(friends) < 200 {
			break
		}
	}
	logger.Default().Info("official trending completed", "topics", topics, "sent", sent)
}

// pickTrendingTopics sorts tags by frequency, keeps the top poolN, then randomly
// samples pickN from that pool.
func pickTrendingTopics(counts map[string]int64, poolN, pickN int) []string {
	if poolN <= 0 {
		poolN = 20
	}
	if pickN <= 0 {
		pickN = 3
	}
	type tc struct {
		tag string
		n   int64
	}
	arr := make([]tc, 0, len(counts))
	for t, n := range counts {
		if t = strings.TrimSpace(t); t != "" {
			arr = append(arr, tc{t, n})
		}
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].n > arr[j].n })
	if len(arr) > poolN {
		arr = arr[:poolN]
	}
	rand.Shuffle(len(arr), func(i, j int) { arr[i], arr[j] = arr[j], arr[i] })
	if len(arr) > pickN {
		arr = arr[:pickN]
	}
	out := make([]string, len(arr))
	for i, x := range arr {
		out[i] = x.tag
	}
	return out
}
