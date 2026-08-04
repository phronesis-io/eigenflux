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
	// A missing Redis snapshot is recovered incrementally so a Redis restart or
	// first rollout cannot turn one hourly tick into a database rebuild storm.
	agentCardRecoveryBatch = 1000
)

type agentInfluenceRow struct {
	AgentID         int64
	Score           int64
	BroadcastCount  int64
	ConsumedCount   int64
	ScoredEvents    int64
	ContentRevision int64
}

// StartAgentCardUpdater ranks influence hourly and rebuilds only agents whose
// influence snapshot changed. A full reconciliation runs on startup and every
// 24 hours to repair events lost from the capped rebuild stream.
func StartAgentCardUpdater(ctx context.Context, cfg *config.Config, rdb *redis.Client) {
	ticker := time.NewTicker(agentCardUpdateInterval)
	defer ticker.Stop()

	run := func() {
		updateAgentCardsWithLock(ctx, rdb, false)
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
	token, acquired, err := acquireLock(ctx, rdb, lockKeyAgentCardUpdater, agentCardLockTTL)
	if err != nil {
		logger.Default().Warn("failed to acquire lock for agent card update", "err", err)
		return false
	}
	if !acquired {
		logger.Default().Debug("agent card update skipped (another instance is running)")
		return false
	}
	defer releaseLock(rdb, lockKeyAgentCardUpdater, token)
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	stopRenewal, lockLost := startLockRenewal(runCtx, rdb, lockKeyAgentCardUpdater, token, agentCardLockTTL)
	defer stopRenewal()
	go func() {
		select {
		case <-lockLost:
			cancelRun()
		case <-runCtx.Done():
		}
	}()

	lastFull, err := agentcard.GetLastFullReconcileAt(runCtx, rdb)
	if err != nil {
		logger.Default().Warn("agent card updater: full reconcile state read failed", "err", err)
		return false
	}
	now := time.Now()
	fullReconcile = fullReconcile || lastFull.IsZero() || lastFull.After(now) || now.Sub(lastFull) >= agentCardFullReconcileInterval

	start := time.Now()
	rankingStarted := time.Now()

	// Influence score (v1 formula: score_1 + 2*score_2) per author, ranked
	// into "you beat N% of agents" percentiles. Agents with no scored items
	// count as score 0 so the percentile is over the whole network.
	var rows []agentInfluenceRow
	err = db.DB.WithContext(runCtx).Raw(`
		SELECT a.agent_id,
		       COALESCE(SUM(s.score_1_count + 2 * s.score_2_count), 0) AS score,
		       COUNT(s.item_id) AS broadcast_count,
		       COALESCE(SUM(s.consumed_count), 0) AS consumed_count,
		       COALESCE(SUM(s.score_1_count + s.score_2_count), 0) AS scored_events,
		       COALESCE(MAX(GREATEST(s.updated_at, COALESCE(p.updated_at, 0))), 0) AS content_revision
		FROM agents a
		LEFT JOIN item_stats s ON s.author_agent_id = a.agent_id
		LEFT JOIN processed_items p ON p.item_id = s.item_id
		GROUP BY a.agent_id
		ORDER BY score ASC`).Scan(&rows).Error
	if err != nil {
		logger.Default().Error("agent card updater: influence ranking query failed", "err", err)
		return false
	}
	rankingTook := time.Since(rankingStarted)
	stateStarted := time.Now()
	previous, err := agentcard.GetInfluenceSnapshots(runCtx, rdb)
	if err != nil {
		logger.Default().Error("agent card updater: snapshot read failed", "err", err)
		return false
	}
	percentileIDs, err := agentcard.GetInfluencePercentileIDs(runCtx, rdb)
	if err != nil {
		logger.Default().Error("agent card updater: percentile state read failed", "err", err)
		return false
	}
	total := len(rows)
	if total == 0 {
		if err := agentcard.ClearInfluenceState(runCtx, rdb); err != nil {
			return false
		}
		if fullReconcile {
			_ = agentcard.SetLastFullReconcileAt(runCtx, rdb, time.Now())
		}
		return true
	}
	snapshots := buildInfluenceSnapshots(rows)
	// A fresh install has no timestamp; an established deployment can also lose
	// only the snapshot hash while retaining the timestamp. Both recover in
	// bounded batches once the missing set is large enough to create a spike.
	recoveryMode := shouldRecoverInfluenceSnapshots(total, len(previous), lastFull)
	if recoveryMode {
		fullReconcile = false
	}
	percentiles := make(map[int64]int, total)
	dirty := make(map[int64]struct{}, total)
	for agentID, snapshot := range snapshots {
		percentiles[agentID] = snapshot.Percentile
		if old, ok := previous[agentID]; !ok || old != snapshot {
			dirty[agentID] = struct{}{}
		}
	}
	if err := agentcard.SetInfluencePercentiles(runCtx, rdb, percentiles); err != nil {
		logger.Default().Error("agent card updater: percentile write failed", "err", err)
		return false
	}
	stateTook := time.Since(stateStarted)

	rebuildStarted := time.Now()
	successfulSnapshots := make(map[int64]agentcard.InfluenceSnapshot, len(dirty))
	failedIDs := make([]int64, 0)
	rebuilt, skipped, failed, deferred := 0, 0, 0, 0
	for _, row := range rows {
		if runCtx.Err() != nil {
			return false
		}
		_, isDirty := dirty[row.AgentID]
		if !fullReconcile && !isDirty {
			skipped++
			continue
		}
		if recoveryMode && rebuilt+failed >= agentCardRecoveryBatch {
			deferred++
			continue
		}
		if err := agentcard.Rebuild(runCtx, db.DB.WithContext(runCtx), rdb, row.AgentID); err != nil {
			failed++
			failedIDs = append(failedIDs, row.AgentID)
			logger.Default().Warn("agent card updater: rebuild failed", "agentID", row.AgentID, "err", err)
			continue
		}
		rebuilt++
		successfulSnapshots[row.AgentID] = snapshots[row.AgentID]
	}
	if err := agentcard.SetInfluenceSnapshots(runCtx, rdb, successfulSnapshots); err != nil {
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
	for agentID := range percentileIDs {
		if _, exists := snapshots[agentID]; !exists {
			if _, already := previous[agentID]; !already {
				staleIDs = append(staleIDs, agentID)
			}
		}
	}
	if err := agentcard.DeleteInfluenceState(runCtx, rdb, staleIDs, true); err != nil {
		logger.Default().Warn("agent card updater: stale snapshot cleanup failed", "err", err)
	}
	if err := agentcard.DeleteInfluenceState(runCtx, rdb, failedIDs, false); err != nil {
		logger.Default().Warn("agent card updater: failed snapshot reset failed", "err", err)
	}
	if fullReconcile {
		if err := agentcard.SetLastFullReconcileAt(runCtx, rdb, time.Now()); err != nil {
			logger.Default().Warn("agent card updater: full reconcile state write failed", "err", err)
			return false
		}
	} else if recoveryMode {
		complete := true
		for agentID := range snapshots {
			if old, oldOK := previous[agentID]; oldOK && old == snapshots[agentID] {
				continue
			}
			if _, newOK := successfulSnapshots[agentID]; !newOK {
				complete = false
				break
			}
		}
		if complete {
			if err := agentcard.SetLastFullReconcileAt(runCtx, rdb, time.Now()); err != nil {
				return false
			}
		}
	}
	logger.Default().Info("agent cards updated",
		"agents", total, "dirty", len(dirty), "rebuilt", rebuilt,
		"skipped", skipped, "deferred", deferred, "failed", failed,
		"full_reconcile", fullReconcile, "recovery_mode", recoveryMode,
		"ranking_took", rankingTook.String(), "state_took", stateTook.String(),
		"rebuild_took", time.Since(rebuildStarted).String(), "took", time.Since(start).String())
	return true
}

func shouldRecoverInfluenceSnapshots(total, snapshotCount int, lastFull time.Time) bool {
	missingSnapshots := total - snapshotCount
	return missingSnapshots >= agentCardRecoveryBatch || (lastFull.IsZero() && missingSnapshots > 0)
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
				Score:           rows[k].Score,
				BroadcastCount:  rows[k].BroadcastCount,
				ConsumedCount:   rows[k].ConsumedCount,
				ScoredEvents:    rows[k].ScoredEvents,
				ContentRevision: rows[k].ContentRevision,
				Percentile:      p,
			}
		}
		i = j
	}
	return snapshots
}
