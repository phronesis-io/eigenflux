package agentcard

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// lastActiveHash maps agent_id -> last authenticated API request (epoch
	// millis). A plain hash (no TTL) so the value survives quiet periods;
	// ~5k agents keeps it tiny.
	lastActiveHash = "agentcard:last_active"
	// lastActiveGatePrefix throttles writes to at most one per agent per
	// window, keeping the auth hot path cheap.
	lastActiveGatePrefix = "agentcard:la:gate:"
	lastActiveWindow     = 5 * time.Minute

	// percentileHash maps agent_id -> influence percentile (0-100), written
	// by the cron ranker; absent until the first run.
	percentileHash = "agentcard:influence_percentile"
	// influenceSnapshotHash stores the last influence inputs successfully
	// projected into each card. The hourly ranker compares against it so only
	// dirty agents are rebuilt. Failed rebuilds deliberately leave the old
	// snapshot in place and are retried on the next pass.
	influenceSnapshotHash = "agentcard:influence_snapshot"
)

// InfluenceSnapshot contains every influence input that can change the card.
// A score change also covers top_items because total_score drives its order.
type InfluenceSnapshot struct {
	Score          int64
	BroadcastCount int64
	ConsumedCount  int64
	ScoredEvents   int64
	Percentile     int
}

// TouchLastActive records API activity, throttled to one write per agent per
// 5-minute window. Safe to call on every authenticated request (one SETNX in
// the common case). Best-effort: errors are swallowed.
func TouchLastActive(ctx context.Context, rdb *redis.Client, agentID int64) {
	if rdb == nil {
		return
	}
	gate := lastActiveGatePrefix + strconv.FormatInt(agentID, 10)
	ok, err := rdb.SetNX(ctx, gate, 1, lastActiveWindow).Result()
	if err != nil || !ok {
		return
	}
	_ = rdb.HSet(ctx, lastActiveHash, strconv.FormatInt(agentID, 10), time.Now().UnixMilli()).Err()
}

// GetLastActive returns the agent's last activity (epoch millis) and whether
// any activity was ever recorded.
func GetLastActive(ctx context.Context, rdb *redis.Client, agentID int64) (int64, bool) {
	if rdb == nil {
		return 0, false
	}
	v, err := rdb.HGet(ctx, lastActiveHash, strconv.FormatInt(agentID, 10)).Result()
	if err != nil {
		return 0, false
	}
	ms, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil {
		return 0, false
	}
	return ms, true
}

// GetInfluencePercentile returns the cron-computed percentile (0-100) and
// whether it has been computed yet.
func GetInfluencePercentile(ctx context.Context, rdb *redis.Client, agentID int64) (int, bool) {
	if rdb == nil {
		return 0, false
	}
	v, err := rdb.HGet(ctx, percentileHash, strconv.FormatInt(agentID, 10)).Result()
	if err != nil {
		return 0, false
	}
	p, perr := strconv.Atoi(v)
	if perr != nil {
		return 0, false
	}
	return p, true
}

// SetInfluencePercentiles bulk-writes the percentile ranking (cron only).
func SetInfluencePercentiles(ctx context.Context, rdb *redis.Client, byAgent map[int64]int) error {
	if rdb == nil || len(byAgent) == 0 {
		return nil
	}
	fields := make([]interface{}, 0, len(byAgent)*2)
	for id, p := range byAgent {
		fields = append(fields, strconv.FormatInt(id, 10), p)
	}
	return rdb.HSet(ctx, percentileHash, fields...).Err()
}

// GetInfluenceSnapshots returns the last snapshots successfully projected by
// the cron updater. Malformed entries are ignored and therefore become dirty.
func GetInfluenceSnapshots(ctx context.Context, rdb *redis.Client) (map[int64]InfluenceSnapshot, error) {
	out := map[int64]InfluenceSnapshot{}
	if rdb == nil {
		return out, nil
	}
	values, err := rdb.HGetAll(ctx, influenceSnapshotHash).Result()
	if err != nil {
		return nil, err
	}
	for rawID, rawSnapshot := range values {
		id, parseErr := strconv.ParseInt(rawID, 10, 64)
		if parseErr != nil {
			continue
		}
		snapshot, parseErr := parseInfluenceSnapshot(rawSnapshot)
		if parseErr != nil {
			continue
		}
		out[id] = snapshot
	}
	return out, nil
}

// SetInfluenceSnapshots marks snapshots as successfully projected. Call this
// only after Rebuild succeeds; otherwise the dirty signal would be lost.
func SetInfluenceSnapshots(ctx context.Context, rdb *redis.Client, byAgent map[int64]InfluenceSnapshot) error {
	if rdb == nil || len(byAgent) == 0 {
		return nil
	}
	fields := make([]interface{}, 0, len(byAgent)*2)
	for id, snapshot := range byAgent {
		fields = append(fields, strconv.FormatInt(id, 10), formatInfluenceSnapshot(snapshot))
	}
	return rdb.HSet(ctx, influenceSnapshotHash, fields...).Err()
}

// DeleteInfluenceState removes deleted agents and forces failed rebuilds to
// retry by clearing their last-success snapshot.
func DeleteInfluenceState(ctx context.Context, rdb *redis.Client, agentIDs []int64, deletePercentile bool) error {
	if rdb == nil || len(agentIDs) == 0 {
		return nil
	}
	fields := make([]string, 0, len(agentIDs))
	for _, id := range agentIDs {
		fields = append(fields, strconv.FormatInt(id, 10))
	}
	pipe := rdb.Pipeline()
	pipe.HDel(ctx, influenceSnapshotHash, fields...)
	if deletePercentile {
		pipe.HDel(ctx, percentileHash, fields...)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func formatInfluenceSnapshot(snapshot InfluenceSnapshot) string {
	return fmt.Sprintf("%d:%d:%d:%d:%d", snapshot.Score, snapshot.BroadcastCount, snapshot.ConsumedCount, snapshot.ScoredEvents, snapshot.Percentile)
}

func parseInfluenceSnapshot(raw string) (InfluenceSnapshot, error) {
	parts := strings.Split(raw, ":")
	if len(parts) != 5 {
		return InfluenceSnapshot{}, fmt.Errorf("invalid influence snapshot")
	}
	values := make([]int64, 5)
	for i, part := range parts {
		value, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return InfluenceSnapshot{}, err
		}
		values[i] = value
	}
	return InfluenceSnapshot{
		Score:          values[0],
		BroadcastCount: values[1],
		ConsumedCount:  values[2],
		ScoredEvents:   values[3],
		Percentile:     int(values[4]),
	}, nil
}
