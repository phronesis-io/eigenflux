package main

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	profiledal "eigenflux_server/rpc/profile/dal"
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
	// Bound database round-trips per hourly run. At larger scale a due full
	// reconcile rotates across the population over successive hourly passes.
	agentCardMaxRebuildsPerRun = 5000
	agentCardRebuildTimeout    = 2 * time.Minute
	agentCardRebuildBudget     = 45 * time.Minute
	// Schema upgrades are prioritized, but remain inside the shared rebuild
	// budget so they cannot starve influence updates or full reconciliation.
	agentCardSchemaUpgradeBatch = 500
	agentCardSchemaRetryTTL     = 30 * 24 * time.Hour
	agentCardSchemaRetryZSet    = "agentcard:schema_upgrade:retry_at:v"
	agentCardSchemaRetryHash    = "agentcard:schema_upgrade:retry_count:v"
)

type agentInfluenceRow struct {
	AgentID         int64
	Score           int64
	BroadcastCount  int64
	ConsumedCount   int64
	ScoredEvents    int64
	ContentRevision int64
}

type outdatedAgentCardRow struct {
	AgentID       int64
	SchemaVersion int
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

	// Allocate once, before ranking reads. Every card written by this run is
	// ordered behind newer cron/consumer/read-on-miss attempts, even if this
	// process resumes after losing the Redis lease.
	runFence, err := profiledal.NextAgentCardRebuildFence(db.DB.WithContext(runCtx))
	if err != nil {
		logger.Default().Error("agent card updater: allocate run fence failed", "err", err)
		return false
	}

	lastFull, err := agentcard.GetLastFullReconcileAtFenced(runCtx, rdb, lockKeyAgentCardUpdater, token)
	if err != nil {
		logger.Default().Warn("agent card updater: full reconcile state read failed", "err", err)
		return false
	}
	now := time.Now()
	if lastFull.After(now) {
		logger.Default().Warn("agent card updater: future full-reconcile timestamp ignored", "lastFull", lastFull)
		lastFull = time.Time{}
	}
	fullEpoch, fullDone, err := agentcard.GetFullReconcileProgressFenced(runCtx, rdb, lockKeyAgentCardUpdater, token)
	if err != nil {
		logger.Default().Warn("agent card updater: full reconcile progress read failed", "err", err)
		return false
	}
	fullActive := !fullEpoch.IsZero()
	fullDue := fullReconcile || lastFull.IsZero() || now.Sub(lastFull) >= agentCardFullReconcileInterval
	if fullDue && !fullActive {
		if err := agentcard.EnsureFullReconcileProgressFenced(runCtx, rdb, now.UnixMilli(), lockKeyAgentCardUpdater, token); err != nil {
			return false
		}
		fullEpoch = now
		fullActive = true
		fullDone = map[int64]struct{}{}
	}
	fullReconcile = fullActive

	start := time.Now()
	rankingStarted := time.Now()
	backfillDeadline := time.Now().Add(10 * time.Minute)
	backfilled, rollupReady := 0, false
	for !rollupReady && time.Now().Before(backfillDeadline) {
		processed, complete, backfillErr := agentcard.AdvanceInfluenceRollupBackfill(runCtx, db.DB, 100)
		backfilled += processed
		if backfillErr != nil {
			logger.Default().Warn("agent card updater: influence rollup backfill failed", "processed", backfilled, "err", backfillErr)
			return false
		}
		rollupReady = complete
		if processed == 0 && !complete {
			break
		}
	}
	if !rollupReady {
		logger.Default().Info("agent card updater: influence rollup backfill advanced", "processed", backfilled)
		return false
	}

	// Rollups are maintained transactionally by item_stats/processed_items
	// triggers. Ranking is therefore O(agents), not O(historical items).
	var rows []agentInfluenceRow
	err = db.DB.WithContext(runCtx).Raw(`
		SELECT a.agent_id,
		       COALESCE(r.score, 0) AS score,
		       COALESCE(r.broadcast_count, 0) AS broadcast_count,
		       COALESCE(r.consumed_count, 0) AS consumed_count,
		       COALESCE(r.scored_events, 0) AS scored_events,
		       COALESCE(r.content_revision, 0) AS content_revision
		FROM agents a
		LEFT JOIN (
			SELECT agent_id,
			       (SUM(score_1_count) + 2 * SUM(score_2_count))::BIGINT AS score,
			       SUM(broadcast_count)::BIGINT AS broadcast_count,
			       SUM(consumed_count)::BIGINT AS consumed_count,
			       (SUM(score_1_count) + SUM(score_2_count))::BIGINT AS scored_events,
			       SUM(content_revision)::BIGINT AS content_revision
			FROM agent_influence_rollups GROUP BY agent_id
		) r ON r.agent_id = a.agent_id
		ORDER BY score ASC`).Scan(&rows).Error
	if err != nil {
		logger.Default().Error("agent card updater: influence ranking query failed", "err", err)
		return false
	}
	rankingTook := time.Since(rankingStarted)
	stateStarted := time.Now()
	previous, err := agentcard.GetInfluenceSnapshotsFenced(runCtx, rdb, lockKeyAgentCardUpdater, token)
	if err != nil {
		logger.Default().Error("agent card updater: snapshot read failed", "err", err)
		return false
	}
	percentileIDs, err := agentcard.GetInfluencePercentileIDsFenced(runCtx, rdb, lockKeyAgentCardUpdater, token)
	if err != nil {
		logger.Default().Error("agent card updater: percentile state read failed", "err", err)
		return false
	}
	total := len(rows)
	if total == 0 {
		if err := agentcard.ClearInfluenceStateFenced(runCtx, rdb, lockKeyAgentCardUpdater, token); err != nil {
			return false
		}
		if fullReconcile {
			if err := agentcard.CompleteFullReconcileFenced(runCtx, rdb, fullEpoch, lockKeyAgentCardUpdater, token); err != nil {
				return false
			}
		}
		return true
	}
	snapshots := buildInfluenceSnapshots(rows)
	// A fresh install has no timestamp; an established deployment can also lose
	// only the snapshot hash while retaining the timestamp. Both recover in
	// bounded batches once the missing set is large enough to create a spike.
	missingSnapshots := countMissingInfluenceSnapshots(snapshots, previous)
	recoveryMode := shouldRecoverInfluenceSnapshots(total, total-missingSnapshots, lastFull)
	recoveryMode = recoveryMode && !fullReconcile
	percentiles := make(map[int64]int)
	dirty := make(map[int64]struct{}, total)
	for agentID, snapshot := range snapshots {
		old, oldOK := previous[agentID]
		_, percentileOK := percentileIDs[agentID]
		if !oldOK || old != snapshot {
			dirty[agentID] = struct{}{}
		}
		if !oldOK || old.Percentile != snapshot.Percentile || !percentileOK {
			percentiles[agentID] = snapshot.Percentile
		}
	}
	if err := agentcard.SetInfluencePercentilesFenced(runCtx, rdb, percentiles, lockKeyAgentCardUpdater, token); err != nil {
		logger.Default().Error("agent card updater: percentile write failed", "err", err)
		return false
	}
	stateTook := time.Since(stateStarted)

	rebuildStarted := time.Now()
	successfulSnapshots := make(map[int64]agentcard.InfluenceSnapshot, len(dirty))
	fullCompleted := make([]int64, 0)
	failedIDs := make([]int64, 0)
	checkpoint := func() error {
		if err := agentcard.SetInfluenceSnapshotsFenced(runCtx, rdb, successfulSnapshots, lockKeyAgentCardUpdater, token); err != nil {
			return err
		}
		if err := agentcard.MarkFullReconcileDoneFenced(runCtx, rdb, fullCompleted, lockKeyAgentCardUpdater, token); err != nil {
			return err
		}
		successfulSnapshots = make(map[int64]agentcard.InfluenceSnapshot)
		fullCompleted = fullCompleted[:0]
		return nil
	}
	rebuilt, skipped, failed, deferred, attempted := 0, 0, 0, 0, 0
	schemaCandidates, err := listSchemaUpgradeCandidates(runCtx, rdb, now, agentCardSchemaUpgradeBatch)
	if err != nil {
		logger.Default().Warn("agent card updater: schema upgrade candidates failed", "err", err)
		return false
	}
	schemaPending := make(map[int64]struct{}, len(schemaCandidates))
	for _, agentID := range schemaCandidates {
		schemaPending[agentID] = struct{}{}
	}
	orderedRows := prioritizeInfluenceRows(rows, schemaCandidates, now, agentCardMaxRebuildsPerRun)
	rebuildDeadline := time.Now().Add(agentCardRebuildBudget)
	attemptLimit := agentCardMaxRebuildsPerRun
	if recoveryMode || missingSnapshots >= agentCardRecoveryBatch {
		attemptLimit = agentCardRecoveryBatch
	}
	for _, row := range orderedRows {
		if runCtx.Err() != nil {
			return false
		}
		_, isDirty := dirty[row.AgentID]
		_, needsSchema := schemaPending[row.AgentID]
		_, alreadyFull := fullDone[row.AgentID]
		needsFull := fullReconcile && !alreadyFull
		if !needsSchema && !needsFull && !isDirty {
			skipped++
			continue
		}
		if attempted >= attemptLimit || time.Now().After(rebuildDeadline) {
			deferred++
			continue
		}
		attempted++
		rebuildCtx, cancelRebuild := context.WithTimeout(runCtx, agentCardRebuildTimeout)
		err := agentcard.RebuildWithFence(rebuildCtx, db.DB.WithContext(rebuildCtx), rdb, row.AgentID, runFence)
		cancelRebuild()
		if err != nil {
			failed++
			failedIDs = append(failedIDs, row.AgentID)
			if needsSchema {
				if retryErr := deferSchemaUpgradeRetry(runCtx, rdb, row.AgentID, now); retryErr != nil {
					logger.Default().Warn("agent card updater: schema retry state write failed", "agentID", row.AgentID, "err", retryErr)
				}
			}
			logger.Default().Warn("agent card updater: rebuild failed", "agentID", row.AgentID, "err", err)
			continue
		}
		rebuilt++
		if needsSchema {
			if retryErr := clearSchemaUpgradeRetry(runCtx, rdb, row.AgentID); retryErr != nil {
				logger.Default().Warn("agent card updater: schema retry state cleanup failed", "agentID", row.AgentID, "err", retryErr)
			}
		}
		successfulSnapshots[row.AgentID] = snapshots[row.AgentID]
		if needsFull {
			fullCompleted = append(fullCompleted, row.AgentID)
			fullDone[row.AgentID] = struct{}{}
		}
		if rebuilt%100 == 0 {
			if err := checkpoint(); err != nil {
				logger.Default().Error("agent card updater: progress checkpoint failed", "err", err)
				return false
			}
		}
	}
	if err := checkpoint(); err != nil {
		logger.Default().Error("agent card updater: final progress checkpoint failed", "err", err)
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
	if err := agentcard.DeleteInfluenceStateFenced(runCtx, rdb, staleIDs, true, lockKeyAgentCardUpdater, token); err != nil {
		logger.Default().Warn("agent card updater: stale snapshot cleanup failed", "err", err)
		return false
	}
	if err := agentcard.DeleteInfluenceStateFenced(runCtx, rdb, failedIDs, false, lockKeyAgentCardUpdater, token); err != nil {
		logger.Default().Warn("agent card updater: failed snapshot reset failed", "err", err)
		return false
	}
	if fullReconcile {
		complete := true
		for _, row := range rows {
			if _, ok := fullDone[row.AgentID]; !ok {
				complete = false
				break
			}
		}
		if complete {
			// Schedule the next cycle from this cycle's start, not its end. If a
			// population needs several hourly passes, changes after an early pass
			// are therefore still repaired within the documented 24-hour bound.
			if err := agentcard.CompleteFullReconcileFenced(runCtx, rdb, fullEpoch, lockKeyAgentCardUpdater, token); err != nil {
				logger.Default().Warn("agent card updater: full reconcile state write failed", "err", err)
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

func listSchemaUpgradeCandidates(ctx context.Context, rdb *redis.Client, now time.Time, limit int) ([]int64, error) {
	if limit <= 0 {
		return nil, nil
	}
	retryZSet, _ := schemaUpgradeRetryKeys()
	deferred, err := rdb.ZRangeByScore(ctx, retryZSet, &redis.ZRangeBy{
		Min: strconv.FormatInt(now.Unix()+1, 10),
		Max: "+inf",
	}).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	deferredSet := make(map[int64]struct{}, len(deferred))
	for _, raw := range deferred {
		if agentID, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil {
			deferredSet[agentID] = struct{}{}
		}
	}

	const pageSize = 500
	candidates := make([]int64, 0, limit)
	lastSchema, lastAgentID := -1, int64(0)
	for len(candidates) < limit {
		var page []outdatedAgentCardRow
		err := db.DB.WithContext(ctx).Raw(`
			SELECT agent_id, schema_version
			FROM agent_cards
			WHERE schema_version < ?
			  AND (schema_version, agent_id) > (?, ?)
			ORDER BY schema_version ASC, agent_id ASC
			LIMIT ?`, agentcard.SchemaVersion, lastSchema, lastAgentID, pageSize).Scan(&page).Error
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for _, row := range page {
			if _, wait := deferredSet[row.AgentID]; !wait {
				candidates = append(candidates, row.AgentID)
				if len(candidates) == limit {
					break
				}
			}
		}
		last := page[len(page)-1]
		lastSchema, lastAgentID = last.SchemaVersion, last.AgentID
		if len(page) < pageSize {
			break
		}
	}
	return candidates, nil
}

func prioritizeInfluenceRows(rows []agentInfluenceRow, priorityIDs []int64, now time.Time, batch int) []agentInfluenceRow {
	byID := make(map[int64]agentInfluenceRow, len(rows))
	for _, row := range rows {
		byID[row.AgentID] = row
	}
	ordered := make([]agentInfluenceRow, 0, len(rows))
	seen := make(map[int64]struct{}, len(priorityIDs))
	for _, agentID := range priorityIDs {
		if _, ok := seen[agentID]; ok {
			continue
		}
		if row, ok := byID[agentID]; ok {
			ordered = append(ordered, row)
			seen[agentID] = struct{}{}
		}
	}
	for _, row := range rotateInfluenceRows(rows, now, batch) {
		if _, ok := seen[row.AgentID]; ok {
			continue
		}
		ordered = append(ordered, row)
	}
	return ordered
}

func deferSchemaUpgradeRetry(ctx context.Context, rdb *redis.Client, agentID int64, now time.Time) error {
	member := strconv.FormatInt(agentID, 10)
	retryZSet, retryHash := schemaUpgradeRetryKeys()
	count, err := rdb.HIncrBy(ctx, retryHash, member, 1).Result()
	if err != nil {
		return err
	}
	delay := schemaUpgradeRetryDelay(count)
	pipe := rdb.TxPipeline()
	pipe.ZAdd(ctx, retryZSet, redis.Z{Score: float64(now.Add(delay).Unix()), Member: member})
	pipe.Expire(ctx, retryZSet, agentCardSchemaRetryTTL)
	pipe.Expire(ctx, retryHash, agentCardSchemaRetryTTL)
	_, err = pipe.Exec(ctx)
	return err
}

func schemaUpgradeRetryDelay(count int64) time.Duration {
	if count >= 10 {
		return 24 * time.Hour
	}
	if count >= 3 {
		return 6 * time.Hour
	}
	return time.Hour
}

func clearSchemaUpgradeRetry(ctx context.Context, rdb *redis.Client, agentID int64) error {
	member := strconv.FormatInt(agentID, 10)
	retryZSet, retryHash := schemaUpgradeRetryKeys()
	pipe := rdb.TxPipeline()
	pipe.HDel(ctx, retryHash, member)
	pipe.ZRem(ctx, retryZSet, member)
	_, err := pipe.Exec(ctx)
	return err
}

func schemaUpgradeRetryKeys() (string, string) {
	version := strconv.Itoa(int(agentcard.SchemaVersion))
	return agentCardSchemaRetryZSet + version, agentCardSchemaRetryHash + version
}

func shouldRecoverInfluenceSnapshots(total, snapshotCount int, lastFull time.Time) bool {
	missingSnapshots := total - snapshotCount
	return missingSnapshots >= agentCardRecoveryBatch || (lastFull.IsZero() && missingSnapshots > 0)
}

func countMissingInfluenceSnapshots(current, previous map[int64]agentcard.InfluenceSnapshot) int {
	missing := 0
	for agentID := range current {
		if _, ok := previous[agentID]; !ok {
			missing++
		}
	}
	return missing
}

func rotateInfluenceRows(rows []agentInfluenceRow, now time.Time, batch int) []agentInfluenceRow {
	if len(rows) <= batch || batch <= 0 {
		return rows
	}
	offset := int(((now.Unix() / int64(time.Hour/time.Second)) * int64(batch)) % int64(len(rows)))
	rotated := make([]agentInfluenceRow, 0, len(rows))
	rotated = append(rotated, rows[offset:]...)
	rotated = append(rotated, rows[:offset]...)
	return rotated
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
