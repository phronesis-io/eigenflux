package agentcard

import (
	"context"
	"strconv"
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
)

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
