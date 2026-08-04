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
	lockKeyAgentCardUpdater        = "lock:cron:agentcard_updater"
	agentCardUpdateInterval        = time.Hour
	agentCardFullReconcileInterval = 24 * time.Hour
	// lockTTL must expire before the next tick; a full pass over ~5k agents
	// takes well under this.
	agentCardLockTTL = 50 * time.Minute
)

type agentInfluenceRow struct {
	AgentID        int64
	Score          int64
	BroadcastCount int64
	ConsumedCount  int64
	ScoredEvents   int64
}

// StartAgentCardUpdater ranks influence hourly and rebuilds only agents whose
// influence snapshot changed. A full reconciliation runs on startup and every
// 24 hours to repair events lost from the capped rebuild stream.
func StartAgentCardUpdater(ctx context.Context, cfg *config.Config, rdb *redis.Client) {
	ticker := time.NewTicker(agentCardUpdateInterval)
	defer ticker.Stop()

	lastFullReconcile := time.Time{}
	run := func() {
		fullReconcile := lastFullReconcile.IsZero() || time.Since(lastFullReconcile) >= agentCardFullReconcileInterval
		if updateAgentCardsWithLock(ctx, rdb, fullReconcile) && fullReconcile {
			lastFullReconcile = time.Now()
		}
	}
	run()

	logger.Default().Info("agent card updater started", "interval", agentCardUpdateInterval.String())

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("agent card updater stopped")
			return
		case <-ticker.C:
			run()
		}
	}
}

func updateAgentCardsWithLock(ctx context.Context, rdb *redis.Client, fullReconcile bool) bool {
	acquired, err := acquireLock(ctx, rdb, lockKeyAgentCardUpdater, agentCardLockTTL)
	if err != nil {
		logger.Default().Warn("failed to acquire lock for agent card update", "err", err)
		return false
	}
	if !acquired {
		logger.Default().Debug("agent card update skipped (another instance is running)")
		return false
	}
	defer releaseLock(ctx, rdb, lockKeyAgentCardUpdater)

	start := time.Now()

	// Influence score (v1 formula: score_1 + 2*score_2) per author, ranked
	// into "you beat N% of agents" percentiles. Agents with no scored items
	// count as score 0 so the percentile is over the whole network.
	var rows []agentInfluenceRow
	err = db.DB.Raw(`
		SELECT a.agent_id,
		       COALESCE(SUM(s.score_1_count + 2 * s.score_2_count), 0) AS score,
		       COUNT(s.item_id) AS broadcast_count,
		       COALESCE(SUM(s.consumed_count), 0) AS consumed_count,
		       COALESCE(SUM(s.score_neg1_count + s.score_1_count + s.score_2_count), 0) AS scored_events
		FROM agents a
		LEFT JOIN item_stats s ON s.author_agent_id = a.agent_id
		GROUP BY a.agent_id
		ORDER BY score ASC`).Scan(&rows).Error
	if err != nil {
		logger.Default().Error("agent card updater: influence ranking query failed", "err", err)
		return false
	}
	total := len(rows)
	if total == 0 {
		return true
	}
	snapshots := buildInfluenceSnapshots(rows)
	previous, err := agentcard.GetInfluenceSnapshots(ctx, rdb)
	if err != nil {
		logger.Default().Error("agent card updater: snapshot read failed", "err", err)
		return false
	}
	percentiles := make(map[int64]int, total)
	dirty := make(map[int64]struct{}, total)
	for agentID, snapshot := range snapshots {
		percentiles[agentID] = snapshot.Percentile
		if old, ok := previous[agentID]; !ok || old != snapshot {
			dirty[agentID] = struct{}{}
		}
	}
	if err := agentcard.SetInfluencePercentiles(ctx, rdb, percentiles); err != nil {
		logger.Default().Error("agent card updater: percentile write failed", "err", err)
		return false
	}

	successfulSnapshots := make(map[int64]agentcard.InfluenceSnapshot, len(dirty))
	failedIDs := make([]int64, 0)
	rebuilt, skipped, failed := 0, 0, 0
	for _, row := range rows {
		if ctx.Err() != nil {
			return false
		}
		_, isDirty := dirty[row.AgentID]
		if !fullReconcile && !isDirty {
			skipped++
			continue
		}
		if err := agentcard.Rebuild(ctx, db.DB, rdb, row.AgentID); err != nil {
			failed++
			failedIDs = append(failedIDs, row.AgentID)
			logger.Default().Warn("agent card updater: rebuild failed", "agentID", row.AgentID, "err", err)
			continue
		}
		rebuilt++
		successfulSnapshots[row.AgentID] = snapshots[row.AgentID]
	}
	if err := agentcard.SetInfluenceSnapshots(ctx, rdb, successfulSnapshots); err != nil {
		logger.Default().Error("agent card updater: snapshot write failed", "err", err)
		return false
	}
	// Deleted agents and failed full-reconcile rows must not retain a snapshot
	// that would suppress cleanup/retry on the next hourly pass.
	staleIDs := make([]int64, 0)
	for agentID := range previous {
		if _, exists := snapshots[agentID]; !exists {
			staleIDs = append(staleIDs, agentID)
		}
	}
	if err := agentcard.DeleteInfluenceState(ctx, rdb, staleIDs, true); err != nil {
		logger.Default().Warn("agent card updater: stale snapshot cleanup failed", "err", err)
	}
	if err := agentcard.DeleteInfluenceState(ctx, rdb, failedIDs, false); err != nil {
		logger.Default().Warn("agent card updater: failed snapshot reset failed", "err", err)
	}
	logger.Default().Info("agent cards updated",
		"agents", total, "dirty", len(dirty), "rebuilt", rebuilt,
		"skipped", skipped, "failed", failed, "full_reconcile", fullReconcile,
		"took", time.Since(start).String())
	return failed == 0
}

func buildInfluenceSnapshots(rows []agentInfluenceRow) map[int64]agentcard.InfluenceSnapshot {
	total := len(rows)
	snapshots := make(map[int64]agentcard.InfluenceSnapshot, total)
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
			snapshots[rows[k].AgentID] = agentcard.InfluenceSnapshot{
				Score:          rows[k].Score,
				BroadcastCount: rows[k].BroadcastCount,
				ConsumedCount:  rows[k].ConsumedCount,
				ScoredEvents:   rows[k].ScoredEvents,
				Percentile:     p,
			}
		}
		i = j
	}
	return snapshots
}
