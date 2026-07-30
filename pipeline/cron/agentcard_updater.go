package main

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
)

const (
	lockKeyAgentCardUpdater = "lock:cron:agentcard_updater"
	agentCardUpdateInterval = time.Hour
	// lockTTL must expire before the next tick; a full pass over ~5k agents
	// takes well under this.
	agentCardLockTTL = 50 * time.Minute
)

// StartAgentCardUpdater periodically (1) ranks every agent's influence into
// percentiles and (2) rebuilds every agent_cards projection. This is both the
// influence/percentile refresher (those change on every feedback and are far
// too hot to rebuild per event) and the reconciler that repairs any rebuild
// event lost from the capped stream.
func StartAgentCardUpdater(ctx context.Context, cfg *config.Config, rdb *redis.Client) {
	ticker := time.NewTicker(agentCardUpdateInterval)
	defer ticker.Stop()

	updateAgentCardsWithLock(ctx, rdb)

	logger.Default().Info("agent card updater started", "interval", agentCardUpdateInterval.String())

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("agent card updater stopped")
			return
		case <-ticker.C:
			updateAgentCardsWithLock(ctx, rdb)
		}
	}
}

func updateAgentCardsWithLock(ctx context.Context, rdb *redis.Client) {
	acquired, err := acquireLock(ctx, rdb, lockKeyAgentCardUpdater, agentCardLockTTL)
	if err != nil {
		logger.Default().Warn("failed to acquire lock for agent card update", "err", err)
		return
	}
	if !acquired {
		logger.Default().Debug("agent card update skipped (another instance is running)")
		return
	}
	defer releaseLock(ctx, rdb, lockKeyAgentCardUpdater)

	start := time.Now()

	// Influence score (v1 formula: score_1 + 2*score_2) per author, ranked
	// into "you beat N% of agents" percentiles. Agents with no scored items
	// count as score 0 so the percentile is over the whole network.
	var rows []struct {
		AgentID int64
		Score   int64
	}
	err = db.DB.Raw(`
		SELECT a.agent_id,
		       COALESCE(SUM(s.score_1_count + 2 * s.score_2_count), 0) AS score
		FROM agents a
		LEFT JOIN item_stats s ON s.author_agent_id = a.agent_id
		GROUP BY a.agent_id
		ORDER BY score ASC`).Scan(&rows).Error
	if err != nil {
		logger.Default().Error("agent card updater: influence ranking query failed", "err", err)
		return
	}
	total := len(rows)
	if total == 0 {
		return
	}
	percentiles := make(map[int64]int, total)
	// Percentile = share of agents with a strictly lower score. Equal scores
	// share the same percentile (i is advanced past ties per group).
	i := 0
	for i < total {
		j := i
		for j < total && rows[j].Score == rows[i].Score {
			j++
		}
		p := i * 100 / total
		for k := i; k < j; k++ {
			percentiles[rows[k].AgentID] = p
		}
		i = j
	}
	if err := agentcard.SetInfluencePercentiles(ctx, rdb, percentiles); err != nil {
		logger.Default().Error("agent card updater: percentile write failed", "err", err)
		return
	}

	rebuilt, failed := 0, 0
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		if err := agentcard.Rebuild(ctx, db.DB, rdb, row.AgentID); err != nil {
			failed++
			logger.Default().Warn("agent card updater: rebuild failed", "agentID", row.AgentID, "err", err)
			continue
		}
		rebuilt++
	}
	logger.Default().Info("agent cards updated",
		"agents", total, "rebuilt", rebuilt, "failed", failed,
		"took", time.Since(start).String())
}
