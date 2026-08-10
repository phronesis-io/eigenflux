package main

import (
	"context"
	"fmt"
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
	// Schema upgrades get a dedicated priority lane. The attempt and time caps
	// reserve the rest of the shared run for influence/full-reconcile work.
	agentCardSchemaUpgradeBatch        = 500
	agentCardSchemaUpgradeAttemptLimit = 200
	agentCardSchemaUpgradeBudget       = 15 * time.Minute
	agentCardSchemaDiscoveryTimeout    = 30 * time.Second
	agentCardSchemaScanPageSize        = 500
	agentCardSchemaScanMaxPages        = 4
	agentCardRetryScoreBatch           = 500
	agentCardSchemaRetryTTL            = 30 * 24 * time.Hour
	agentCardSchemaRetryZSet           = "agentcard:rebuild:retry_at:v"
	agentCardSchemaRetryHash           = "agentcard:rebuild:retry_count:v"
	agentCardSchemaCursorKey           = "agentcard:schema_upgrade:cursor:v"
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

type schemaUpgradeCursor struct {
	SchemaVersion int
	AgentID       int64
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
	rebuildDeadline := rebuildStarted.Add(agentCardRebuildBudget)
	discoveryCtx, cancelDiscovery := context.WithTimeout(runCtx, agentCardSchemaDiscoveryTimeout)
	schemaCandidates, err := listSchemaUpgradeCandidates(
		discoveryCtx, rdb, now, agentCardSchemaUpgradeBatch, lockKeyAgentCardUpdater, token,
	)
	cancelDiscovery()
	if err != nil {
		logger.Default().Warn("agent card updater: schema upgrade candidates failed", "err", err)
		return false
	}
	schemaPending := make(map[int64]struct{}, len(schemaCandidates))
	for _, agentID := range schemaCandidates {
		schemaPending[agentID] = struct{}{}
	}
	attemptLimit := agentCardMaxRebuildsPerRun
	if recoveryMode || missingSnapshots >= agentCardRecoveryBatch {
		attemptLimit = agentCardRecoveryBatch
	}
	generalScanLimit := attemptLimit * 2
	if generalScanLimit < attemptLimit {
		generalScanLimit = attemptLimit
	}
	generalCandidates, workRows := selectGeneralRebuildRows(
		rows, schemaPending, dirty, fullDone, fullReconcile, now, generalScanLimit,
	)
	workRows += len(schemaPending)
	skipped = total - workRows
	if skipped < 0 {
		skipped = 0
	}

	byID := make(map[int64]agentInfluenceRow, len(rows))
	for _, row := range rows {
		byID[row.AgentID] = row
	}
	retryIDs := make([]int64, 0, len(schemaCandidates)+len(generalCandidates))
	retryIDs = append(retryIDs, schemaCandidates...)
	for _, row := range generalCandidates {
		retryIDs = append(retryIDs, row.AgentID)
	}
	retryAt, err := getRebuildRetryAtFenced(runCtx, rdb, retryIDs, lockKeyAgentCardUpdater, token)
	if err != nil {
		logger.Default().Warn("agent card updater: retry state read failed", "err", err)
		return false
	}

	attemptRow := func(row agentInfluenceRow, deadline time.Time) error {
		current := time.Now()
		if until := retryAt[row.AgentID]; until > current.Unix() {
			deferred++
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || attempted >= attemptLimit {
			deferred++
			return nil
		}
		attemptTimeout := agentCardRebuildTimeout
		if remaining < attemptTimeout {
			attemptTimeout = remaining
		}
		attempted++
		rebuildCtx, cancelRebuild := context.WithTimeout(runCtx, attemptTimeout)
		rebuildErr := agentcard.RebuildWithFence(rebuildCtx, db.DB.WithContext(rebuildCtx), rdb, row.AgentID, runFence)
		cancelRebuild()
		if rebuildErr != nil {
			failed++
			failedIDs = append(failedIDs, row.AgentID)
			nextRetry, retryErr := deferSchemaUpgradeRetry(
				runCtx, rdb, row.AgentID, time.Now(), lockKeyAgentCardUpdater, token,
			)
			if retryErr != nil {
				return fmt.Errorf("persist rebuild retry for agent %d: %w", row.AgentID, retryErr)
			}
			retryAt[row.AgentID] = nextRetry.Unix()
			logger.Default().Warn("agent card updater: rebuild failed", "agentID", row.AgentID, "retryAt", nextRetry, "err", rebuildErr)
			return nil
		}
		if retryErr := clearSchemaUpgradeRetry(runCtx, rdb, row.AgentID, lockKeyAgentCardUpdater, token); retryErr != nil {
			return fmt.Errorf("clear rebuild retry for agent %d: %w", row.AgentID, retryErr)
		}
		delete(retryAt, row.AgentID)
		rebuilt++
		successfulSnapshots[row.AgentID] = snapshots[row.AgentID]
		if _, alreadyFull := fullDone[row.AgentID]; fullReconcile && !alreadyFull {
			fullCompleted = append(fullCompleted, row.AgentID)
			fullDone[row.AgentID] = struct{}{}
		}
		if rebuilt%100 == 0 {
			if err := checkpoint(); err != nil {
				return fmt.Errorf("checkpoint rebuild progress: %w", err)
			}
		}
		return nil
	}

	// The priority lane is intentionally capped independently. Even repeated
	// two-minute timeouts cannot consume the general lane's reserved 30 minutes.
	schemaDeadline := rebuildStarted.Add(agentCardSchemaUpgradeBudget)
	if schemaDeadline.After(rebuildDeadline) {
		schemaDeadline = rebuildDeadline
	}
	schemaAttempts := 0
	for i, agentID := range schemaCandidates {
		if runCtx.Err() != nil {
			return false
		}
		if schemaAttempts >= agentCardSchemaUpgradeAttemptLimit || attempted >= attemptLimit || time.Now().After(schemaDeadline) {
			deferred += len(schemaCandidates) - i
			break
		}
		row, ok := byID[agentID]
		if !ok {
			continue
		}
		before := attempted
		if err := attemptRow(row, schemaDeadline); err != nil {
			logger.Default().Warn("agent card updater: schema lane failed", "agentID", agentID, "err", err)
			return false
		}
		if attempted > before {
			schemaAttempts++
		}
	}

	for i, row := range generalCandidates {
		if runCtx.Err() != nil {
			return false
		}
		if attempted >= attemptLimit || time.Now().After(rebuildDeadline) {
			deferred += len(generalCandidates) - i
			break
		}
		if err := attemptRow(row, rebuildDeadline); err != nil {
			logger.Default().Warn("agent card updater: general lane failed", "agentID", row.AgentID, "err", err)
			return false
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
		"schema_candidates", len(schemaCandidates), "schema_attempted", schemaAttempts,
		"full_reconcile", fullReconcile, "recovery_mode", recoveryMode,
		"ranking_took", rankingTook.String(), "state_took", stateTook.String(),
		"rebuild_took", time.Since(rebuildStarted).String(), "took", time.Since(start).String())
	return true
}

func listSchemaUpgradeCandidates(ctx context.Context, rdb *redis.Client, now time.Time, limit int, lockKey, token string) ([]int64, error) {
	if limit <= 0 {
		return nil, nil
	}
	cursor, err := getSchemaUpgradeCursorFenced(ctx, rdb, lockKey, token)
	if err != nil {
		return nil, err
	}
	candidates := make([]int64, 0, limit)
	seen := make(map[int64]struct{}, limit)
	wrapped := cursor.SchemaVersion < 0 && cursor.AgentID == 0
	for pages := 0; pages < agentCardSchemaScanMaxPages && len(candidates) < limit; pages++ {
		var page []outdatedAgentCardRow
		err := db.DB.WithContext(ctx).Raw(`
			SELECT agent_id, schema_version
			FROM agent_cards
			WHERE schema_version < ?
			  AND (schema_version, agent_id) > (?, ?)
			ORDER BY schema_version ASC, agent_id ASC
			LIMIT ?`, agentcard.SchemaVersion, cursor.SchemaVersion, cursor.AgentID, agentCardSchemaScanPageSize).Scan(&page).Error
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			if !wrapped {
				cursor = schemaUpgradeCursor{SchemaVersion: -1}
				wrapped = true
				if err := setSchemaUpgradeCursorFenced(ctx, rdb, cursor, lockKey, token); err != nil {
					return nil, err
				}
				continue
			}
			cursor = schemaUpgradeCursor{SchemaVersion: -1}
			if err := setSchemaUpgradeCursorFenced(ctx, rdb, cursor, lockKey, token); err != nil {
				return nil, err
			}
			break
		}
		ids := make([]int64, len(page))
		for i, row := range page {
			ids[i] = row.AgentID
		}
		retryAt, err := getRebuildRetryAtFenced(ctx, rdb, ids, lockKey, token)
		if err != nil {
			return nil, err
		}
		for _, row := range page {
			cursor = schemaUpgradeCursor{SchemaVersion: row.SchemaVersion, AgentID: row.AgentID}
			if retryAt[row.AgentID] <= now.Unix() {
				if _, duplicate := seen[row.AgentID]; duplicate {
					continue
				}
				candidates = append(candidates, row.AgentID)
				seen[row.AgentID] = struct{}{}
				if len(candidates) == limit {
					break
				}
			}
		}
		if err := setSchemaUpgradeCursorFenced(ctx, rdb, cursor, lockKey, token); err != nil {
			return nil, err
		}
		if len(candidates) == limit {
			break
		}
		if len(page) < agentCardSchemaScanPageSize {
			cursor = schemaUpgradeCursor{SchemaVersion: -1}
			if err := setSchemaUpgradeCursorFenced(ctx, rdb, cursor, lockKey, token); err != nil {
				return nil, err
			}
			if wrapped {
				break
			}
			wrapped = true
		}
	}
	return candidates, nil
}

func selectGeneralRebuildRows(rows []agentInfluenceRow, schemaPending, dirty, fullDone map[int64]struct{}, fullReconcile bool, now time.Time, limit int) ([]agentInfluenceRow, int) {
	selected := make([]agentInfluenceRow, 0, limit)
	workRows := 0
	for _, row := range rotateInfluenceRows(rows, now, agentCardMaxRebuildsPerRun) {
		if _, schema := schemaPending[row.AgentID]; schema {
			continue
		}
		_, isDirty := dirty[row.AgentID]
		_, alreadyFull := fullDone[row.AgentID]
		if !isDirty && (!fullReconcile || alreadyFull) {
			continue
		}
		workRows++
		if len(selected) < limit {
			selected = append(selected, row)
		}
	}
	return selected, workRows
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

func deferSchemaUpgradeRetry(ctx context.Context, rdb *redis.Client, agentID int64, now time.Time, lockKey, token string) (time.Time, error) {
	member := strconv.FormatInt(agentID, 10)
	retryZSet, retryHash := schemaUpgradeRetryKeys()
	const script = `
if redis.call("GET",KEYS[1]) ~= ARGV[1] then return {0,0} end
local count = redis.call("HINCRBY",KEYS[3],ARGV[2],1)
local delay = tonumber(ARGV[4])
if count >= 10 then delay = tonumber(ARGV[6]) elseif count >= 3 then delay = tonumber(ARGV[5]) end
local retry_at = tonumber(ARGV[3]) + delay
redis.call("ZADD",KEYS[2],retry_at,ARGV[2])
redis.call("EXPIRE",KEYS[2],ARGV[7])
redis.call("EXPIRE",KEYS[3],ARGV[7])
return {count,retry_at}`
	result, err := rdb.Eval(ctx, script, []string{lockKey, retryZSet, retryHash},
		token, member, now.Unix(), int64(time.Hour/time.Second), int64(6*time.Hour/time.Second),
		int64(24*time.Hour/time.Second), int64(agentCardSchemaRetryTTL/time.Second)).Int64Slice()
	if err != nil {
		return time.Time{}, err
	}
	if len(result) != 2 || result[0] == 0 {
		return time.Time{}, agentcard.ErrReconcileLeaseLost
	}
	return time.Unix(result[1], 0), nil
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

func clearSchemaUpgradeRetry(ctx context.Context, rdb *redis.Client, agentID int64, lockKey, token string) error {
	member := strconv.FormatInt(agentID, 10)
	retryZSet, retryHash := schemaUpgradeRetryKeys()
	const script = `if redis.call("GET",KEYS[1]) ~= ARGV[1] then return 0 end redis.call("HDEL",KEYS[3],ARGV[2]); redis.call("ZREM",KEYS[2],ARGV[2]); return 1`
	result, err := rdb.Eval(ctx, script, []string{lockKey, retryZSet, retryHash}, token, member).Int64()
	if err != nil {
		return err
	}
	if result != 1 {
		return agentcard.ErrReconcileLeaseLost
	}
	return nil
}

func schemaUpgradeRetryKeys() (string, string) {
	version := strconv.Itoa(int(agentcard.SchemaVersion))
	return agentCardSchemaRetryZSet + version, agentCardSchemaRetryHash + version
}

func getRebuildRetryAtFenced(ctx context.Context, rdb *redis.Client, agentIDs []int64, lockKey, token string) (map[int64]int64, error) {
	retryAt := make(map[int64]int64, len(agentIDs))
	retryZSet, _ := schemaUpgradeRetryKeys()
	seen := make(map[int64]struct{}, len(agentIDs))
	unique := make([]int64, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		if _, duplicate := seen[agentID]; duplicate {
			continue
		}
		seen[agentID] = struct{}{}
		unique = append(unique, agentID)
	}
	const script = `
if redis.call("GET",KEYS[1]) ~= ARGV[1] then return {"LEASE_LOST"} end
local out = {}
for i=2,#ARGV do
  local score = redis.call("ZSCORE",KEYS[2],ARGV[i])
  out[#out+1] = score or "0"
end
return out`
	for start := 0; start < len(unique); start += agentCardRetryScoreBatch {
		end := start + agentCardRetryScoreBatch
		if end > len(unique) {
			end = len(unique)
		}
		args := make([]interface{}, 1, end-start+1)
		args[0] = token
		for _, agentID := range unique[start:end] {
			args = append(args, strconv.FormatInt(agentID, 10))
		}
		result, err := rdb.Eval(ctx, script, []string{lockKey, retryZSet}, args...).Slice()
		if err != nil {
			return nil, err
		}
		if len(result) == 1 && fmt.Sprint(result[0]) == "LEASE_LOST" {
			return nil, agentcard.ErrReconcileLeaseLost
		}
		if len(result) != end-start {
			return nil, fmt.Errorf("agent card updater: malformed retry score result")
		}
		for i, raw := range result {
			score, err := strconv.ParseFloat(fmt.Sprint(raw), 64)
			if err != nil {
				return nil, fmt.Errorf("agent card updater: malformed retry score: %w", err)
			}
			if score > 0 {
				retryAt[unique[start+i]] = int64(score)
			}
		}
	}
	return retryAt, nil
}

func getSchemaUpgradeCursorFenced(ctx context.Context, rdb *redis.Client, lockKey, token string) (schemaUpgradeCursor, error) {
	key := schemaUpgradeCursorRedisKey()
	const script = `if redis.call("GET",KEYS[1]) ~= ARGV[1] then return "LEASE_LOST" end return redis.call("GET",KEYS[2]) or ""`
	raw, err := rdb.Eval(ctx, script, []string{lockKey, key}, token).Text()
	if err != nil {
		return schemaUpgradeCursor{}, err
	}
	if raw == "LEASE_LOST" {
		return schemaUpgradeCursor{}, agentcard.ErrReconcileLeaseLost
	}
	cursor := schemaUpgradeCursor{SchemaVersion: -1}
	if raw == "" {
		return cursor, nil
	}
	if _, err := fmt.Sscanf(raw, "%d:%d", &cursor.SchemaVersion, &cursor.AgentID); err != nil {
		return schemaUpgradeCursor{}, fmt.Errorf("agent card updater: malformed schema cursor %q: %w", raw, err)
	}
	return cursor, nil
}

func setSchemaUpgradeCursorFenced(ctx context.Context, rdb *redis.Client, cursor schemaUpgradeCursor, lockKey, token string) error {
	key := schemaUpgradeCursorRedisKey()
	const script = `if redis.call("GET",KEYS[1]) ~= ARGV[1] then return 0 end redis.call("SET",KEYS[2],ARGV[2],"EX",ARGV[3]); return 1`
	value := fmt.Sprintf("%d:%d", cursor.SchemaVersion, cursor.AgentID)
	result, err := rdb.Eval(ctx, script, []string{lockKey, key}, token, value, int64(agentCardSchemaRetryTTL/time.Second)).Int64()
	if err != nil {
		return err
	}
	if result != 1 {
		return agentcard.ErrReconcileLeaseLost
	}
	return nil
}

func schemaUpgradeCursorRedisKey() string {
	return agentCardSchemaCursorKey + strconv.Itoa(int(agentcard.SchemaVersion))
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
