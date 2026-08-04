package agentcard

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrReconcileLeaseLost means a cron state mutation was rejected because the
// caller no longer owns its distributed lease.
var ErrReconcileLeaseLost = errors.New("agentcard: reconciliation lease lost")

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
	fullReconcileAtKey    = "agentcard:last_full_reconcile_at"
	redisInfluenceBatch   = 1000
	// Redis state is a cache, not an unbounded input surface. A cardinality
	// far above the agent population indicates corruption or a bad writer;
	// fail the run before allocating unbounded maps/slices.
	maxInfluenceStateEntries = 100000
)

// InfluenceSnapshot contains every influence input that can change the card.
// ContentRevision covers per-item score redistribution and summary/status
// changes even when the author's aggregate score is unchanged.
type InfluenceSnapshot struct {
	Score           int64
	BroadcastCount  int64
	ConsumedCount   int64
	ScoredEvents    int64
	ContentRevision int64
	Percentile      int
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
	if perr != nil || p < 0 || p > 100 {
		return 0, false
	}
	return p, true
}

// SetInfluencePercentiles bulk-writes the percentile ranking (cron only).
func SetInfluencePercentiles(ctx context.Context, rdb *redis.Client, byAgent map[int64]int) error {
	return setInfluencePercentiles(ctx, rdb, byAgent, "", "")
}

func SetInfluencePercentilesFenced(ctx context.Context, rdb *redis.Client, byAgent map[int64]int, lockKey, token string) error {
	return setInfluencePercentiles(ctx, rdb, byAgent, lockKey, token)
}

func setInfluencePercentiles(ctx context.Context, rdb *redis.Client, byAgent map[int64]int, lockKey, token string) error {
	if rdb == nil || len(byAgent) == 0 {
		return nil
	}
	fields := make([]interface{}, 0, redisInfluenceBatch*2)
	for id, p := range byAgent {
		if p < 0 || p > 100 {
			return fmt.Errorf("invalid influence percentile %d for agent %d", p, id)
		}
		fields = append(fields, strconv.FormatInt(id, 10), p)
		if len(fields) == redisInfluenceBatch*2 {
			if err := hsetWithLease(ctx, rdb, percentileHash, fields, lockKey, token); err != nil {
				return err
			}
			fields = fields[:0]
		}
	}
	if len(fields) > 0 {
		if err := hsetWithLease(ctx, rdb, percentileHash, fields, lockKey, token); err != nil {
			return err
		}
	}
	return nil
}

// GetInfluencePercentileIDs scans the independently stored percentile hash so
// orphaned entries are cleaned even when their snapshot is missing/corrupt.
func GetInfluencePercentileIDs(ctx context.Context, rdb *redis.Client) (map[int64]struct{}, error) {
	return getInfluencePercentileIDs(ctx, rdb, "", "")
}

func GetInfluencePercentileIDsFenced(ctx context.Context, rdb *redis.Client, lockKey, token string) (map[int64]struct{}, error) {
	return getInfluencePercentileIDs(ctx, rdb, lockKey, token)
}

func getInfluencePercentileIDs(ctx context.Context, rdb *redis.Client, lockKey, token string) (map[int64]struct{}, error) {
	out := map[int64]struct{}{}
	var cursor uint64
	invalid := make([]string, 0)
	for {
		values, next, err := boundedHScan(ctx, rdb, percentileHash, cursor, 16, lockKey, token)
		if err != nil {
			return nil, err
		}
		for i := 0; i+1 < len(values); i += 2 {
			if len(out)+len(invalid) >= maxInfluenceStateEntries {
				if err := deleteHashWithLease(ctx, rdb, percentileHash, lockKey, token); err != nil {
					return nil, err
				}
				return map[int64]struct{}{}, nil
			}
			id, idErr := strconv.ParseInt(values[i], 10, 64)
			p, pErr := strconv.Atoi(values[i+1])
			if idErr != nil || id <= 0 || pErr != nil || p < 0 || p > 100 {
				invalid = append(invalid, values[i])
				continue
			}
			out[id] = struct{}{}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	for len(invalid) > 0 {
		n := redisInfluenceBatch
		if len(invalid) < n {
			n = len(invalid)
		}
		if err := hdelHashWithLease(ctx, rdb, percentileHash, invalid[:n], lockKey, token); err != nil {
			return nil, err
		}
		invalid = invalid[n:]
	}
	return out, nil
}

func ClearInfluenceState(ctx context.Context, rdb *redis.Client) error {
	// Activity is independent of influence reconciliation and must survive an
	// empty-agent or recovery pass.
	return rdb.Del(ctx, influenceSnapshotHash, percentileHash).Err()
}

func ClearInfluenceStateFenced(ctx context.Context, rdb *redis.Client, lockKey, token string) error {
	const script = `if redis.call("GET", KEYS[1]) ~= ARGV[1] then return 0 end redis.call("DEL", KEYS[2], KEYS[3]) return 1`
	result, err := rdb.Eval(ctx, script, []string{lockKey, influenceSnapshotHash, percentileHash}, token).Int64()
	if err != nil {
		return err
	}
	if result != 1 {
		return ErrReconcileLeaseLost
	}
	return nil
}

// GetInfluenceSnapshots returns the last snapshots successfully projected by
// the cron updater. Malformed entries are ignored and therefore become dirty.
func GetInfluenceSnapshots(ctx context.Context, rdb *redis.Client) (map[int64]InfluenceSnapshot, error) {
	return getInfluenceSnapshots(ctx, rdb, "", "")
}

func GetInfluenceSnapshotsFenced(ctx context.Context, rdb *redis.Client, lockKey, token string) (map[int64]InfluenceSnapshot, error) {
	return getInfluenceSnapshots(ctx, rdb, lockKey, token)
}

func getInfluenceSnapshots(ctx context.Context, rdb *redis.Client, lockKey, token string) (map[int64]InfluenceSnapshot, error) {
	out := map[int64]InfluenceSnapshot{}
	if rdb == nil {
		return out, nil
	}
	var cursor uint64
	invalid := make([]string, 0)
	for {
		values, next, err := boundedHScan(ctx, rdb, influenceSnapshotHash, cursor, 256, lockKey, token)
		if err != nil {
			return nil, err
		}
		for i := 0; i+1 < len(values); i += 2 {
			if len(out)+len(invalid) >= maxInfluenceStateEntries {
				if err := deleteHashWithLease(ctx, rdb, influenceSnapshotHash, lockKey, token); err != nil {
					return nil, err
				}
				return map[int64]InfluenceSnapshot{}, nil
			}
			rawID, rawSnapshot := values[i], values[i+1]
			id, parseErr := strconv.ParseInt(rawID, 10, 64)
			if parseErr != nil || id <= 0 || len(rawSnapshot) > 256 {
				invalid = append(invalid, rawID)
				continue
			}
			snapshot, parseErr := parseInfluenceSnapshot(rawSnapshot)
			if parseErr != nil {
				invalid = append(invalid, rawID)
				continue
			}
			out[id] = snapshot
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	for len(invalid) > 0 {
		n := redisInfluenceBatch
		if len(invalid) < n {
			n = len(invalid)
		}
		if err := hdelHashWithLease(ctx, rdb, influenceSnapshotHash, invalid[:n], lockKey, token); err != nil {
			return nil, err
		}
		invalid = invalid[n:]
	}
	return out, nil
}

// SetInfluenceSnapshots marks snapshots as successfully projected. Call this
// only after Rebuild succeeds; otherwise the dirty signal would be lost.
func SetInfluenceSnapshots(ctx context.Context, rdb *redis.Client, byAgent map[int64]InfluenceSnapshot) error {
	return setInfluenceSnapshots(ctx, rdb, byAgent, "", "")
}

func SetInfluenceSnapshotsFenced(ctx context.Context, rdb *redis.Client, byAgent map[int64]InfluenceSnapshot, lockKey, token string) error {
	return setInfluenceSnapshots(ctx, rdb, byAgent, lockKey, token)
}

func setInfluenceSnapshots(ctx context.Context, rdb *redis.Client, byAgent map[int64]InfluenceSnapshot, lockKey, token string) error {
	if rdb == nil || len(byAgent) == 0 {
		return nil
	}
	fields := make([]interface{}, 0, redisInfluenceBatch*2)
	for id, snapshot := range byAgent {
		fields = append(fields, strconv.FormatInt(id, 10), formatInfluenceSnapshot(snapshot))
		if len(fields) == redisInfluenceBatch*2 {
			if err := hsetWithLease(ctx, rdb, influenceSnapshotHash, fields, lockKey, token); err != nil {
				return err
			}
			fields = fields[:0]
		}
	}
	if len(fields) > 0 {
		if err := hsetWithLease(ctx, rdb, influenceSnapshotHash, fields, lockKey, token); err != nil {
			return err
		}
	}
	return nil
}

// DeleteInfluenceState removes deleted agents and forces failed rebuilds to
// retry by clearing their last-success snapshot.
func DeleteInfluenceState(ctx context.Context, rdb *redis.Client, agentIDs []int64, deletePercentile bool) error {
	return deleteInfluenceState(ctx, rdb, agentIDs, deletePercentile, "", "")
}

func DeleteInfluenceStateFenced(ctx context.Context, rdb *redis.Client, agentIDs []int64, deletePercentile bool, lockKey, token string) error {
	return deleteInfluenceState(ctx, rdb, agentIDs, deletePercentile, lockKey, token)
}

func deleteInfluenceState(ctx context.Context, rdb *redis.Client, agentIDs []int64, deletePercentile bool, lockKey, token string) error {
	if rdb == nil || len(agentIDs) == 0 {
		return nil
	}
	fields := make([]string, 0, len(agentIDs))
	for _, id := range agentIDs {
		fields = append(fields, strconv.FormatInt(id, 10))
	}
	for len(fields) > 0 {
		n := redisInfluenceBatch
		if len(fields) < n {
			n = len(fields)
		}
		batch := fields[:n]
		if err := hdelWithLease(ctx, rdb, batch, deletePercentile, lockKey, token); err != nil {
			return err
		}
		fields = fields[n:]
	}
	return nil
}

func formatInfluenceSnapshot(snapshot InfluenceSnapshot) string {
	return fmt.Sprintf("2:%d:%d:%d:%d:%d:%d", snapshot.Score, snapshot.BroadcastCount, snapshot.ConsumedCount, snapshot.ScoredEvents, snapshot.ContentRevision, snapshot.Percentile)
}

func parseInfluenceSnapshot(raw string) (InfluenceSnapshot, error) {
	parts := strings.Split(raw, ":")
	if len(parts) != 7 || parts[0] != "2" {
		return InfluenceSnapshot{}, fmt.Errorf("invalid influence snapshot")
	}
	values := make([]int64, 6)
	for i, part := range parts[1:] {
		value, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return InfluenceSnapshot{}, err
		}
		values[i] = value
	}
	return InfluenceSnapshot{
		Score:           values[0],
		BroadcastCount:  values[1],
		ConsumedCount:   values[2],
		ScoredEvents:    values[3],
		ContentRevision: values[4],
		Percentile:      int(values[5]),
	}, nil
}

func GetLastFullReconcileAt(ctx context.Context, rdb *redis.Client) (time.Time, error) {
	return getLastFullReconcileAt(ctx, rdb, "", "")
}

func GetLastFullReconcileAtFenced(ctx context.Context, rdb *redis.Client, lockKey, token string) (time.Time, error) {
	return getLastFullReconcileAt(ctx, rdb, lockKey, token)
}

func getLastFullReconcileAt(ctx context.Context, rdb *redis.Client, lockKey, token string) (time.Time, error) {
	raw, err := rdb.Get(ctx, fullReconcileAtKey).Result()
	if err == redis.Nil {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		// This is disposable cache state. Self-heal corruption so one bad Redis
		// value cannot permanently disable the hourly reconciler.
		if delErr := deleteHashWithLease(ctx, rdb, fullReconcileAtKey, lockKey, token); delErr != nil {
			return time.Time{}, delErr
		}
		return time.Time{}, nil
	}
	return time.UnixMilli(ms), nil
}

func SetLastFullReconcileAt(ctx context.Context, rdb *redis.Client, at time.Time) error {
	return rdb.Set(ctx, fullReconcileAtKey, at.UnixMilli(), 0).Err()
}

func SetLastFullReconcileAtFenced(ctx context.Context, rdb *redis.Client, at time.Time, lockKey, token string) error {
	const script = `if redis.call("GET", KEYS[1]) ~= ARGV[1] then return 0 end redis.call("SET", KEYS[2], ARGV[2]) return 1`
	result, err := rdb.Eval(ctx, script, []string{lockKey, fullReconcileAtKey}, token, at.UnixMilli()).Int64()
	if err != nil {
		return err
	}
	if result != 1 {
		return ErrReconcileLeaseLost
	}
	return nil
}

func hsetWithLease(ctx context.Context, rdb *redis.Client, hash string, fields []interface{}, lockKey, token string) error {
	if lockKey == "" {
		return rdb.HSet(ctx, hash, fields...).Err()
	}
	const script = `if redis.call("GET", KEYS[1]) ~= ARGV[1] then return 0 end for i=2,#ARGV,2 do redis.call("HSET", KEYS[2], ARGV[i], ARGV[i+1]) end return 1`
	args := make([]interface{}, 0, len(fields)+1)
	args = append(args, token)
	args = append(args, fields...)
	result, err := rdb.Eval(ctx, script, []string{lockKey, hash}, args...).Int64()
	if err != nil {
		return err
	}
	if result != 1 {
		return ErrReconcileLeaseLost
	}
	return nil
}

func hdelWithLease(ctx context.Context, rdb *redis.Client, fields []string, deletePercentile bool, lockKey, token string) error {
	if lockKey == "" {
		pipe := rdb.Pipeline()
		pipe.HDel(ctx, influenceSnapshotHash, fields...)
		if deletePercentile {
			pipe.HDel(ctx, percentileHash, fields...)
			pipe.HDel(ctx, lastActiveHash, fields...)
		}
		_, err := pipe.Exec(ctx)
		return err
	}
	// Use explicit hashes and one HDEL per field; Redis Lua 5.1 cannot slice
	// unpack arguments portably across the supported server versions.
	actual := `if redis.call("GET", KEYS[1]) ~= ARGV[1] then return 0 end for i=3,#ARGV do redis.call("HDEL", KEYS[2], ARGV[i]) if ARGV[2] == "1" then redis.call("HDEL", KEYS[3], ARGV[i]); redis.call("HDEL", KEYS[4], ARGV[i]) end end return 1`
	args := make([]interface{}, 0, len(fields)+2)
	args = append(args, token)
	if deletePercentile {
		args = append(args, "1")
	} else {
		args = append(args, "0")
	}
	for _, field := range fields {
		args = append(args, field)
	}
	result, err := rdb.Eval(ctx, actual, []string{lockKey, influenceSnapshotHash, percentileHash, lastActiveHash}, args...).Int64()
	if err != nil {
		return err
	}
	if result != 1 {
		return ErrReconcileLeaseLost
	}
	return nil
}

// boundedHScan filters oversized values inside Redis before returning a batch,
// so one corrupt field cannot allocate its full payload in the Go process.
func boundedHScan(ctx context.Context, rdb *redis.Client, hash string, cursor uint64, maxValueBytes int, lockKey, token string) ([]string, uint64, error) {
	const script = `if KEYS[2] ~= "" and redis.call("GET",KEYS[2]) ~= ARGV[4] then return {"LEASE_LOST"} end; local scan=redis.call("HSCAN",KEYS[1],ARGV[1],"COUNT",ARGV[2]); local out={scan[1]}; for i=1,#scan[2],2 do if string.len(scan[2][i+1]) <= tonumber(ARGV[3]) then table.insert(out,scan[2][i]); table.insert(out,scan[2][i+1]); else redis.call("HDEL",KEYS[1],scan[2][i]); end end; return out`
	raw, err := rdb.Eval(ctx, script, []string{hash, lockKey}, cursor, redisInfluenceBatch, maxValueBytes, token).Slice()
	if err != nil {
		return nil, 0, err
	}
	if len(raw) == 0 {
		return nil, 0, fmt.Errorf("agentcard: malformed bounded HSCAN response")
	}
	if fmt.Sprint(raw[0]) == "LEASE_LOST" {
		return nil, 0, ErrReconcileLeaseLost
	}
	next, err := strconv.ParseUint(fmt.Sprint(raw[0]), 10, 64)
	if err != nil {
		return nil, 0, err
	}
	values := make([]string, 0, len(raw)-1)
	for _, value := range raw[1:] {
		values = append(values, fmt.Sprint(value))
	}
	return values, next, nil
}

func deleteHashWithLease(ctx context.Context, rdb *redis.Client, hash, lockKey, token string) error {
	if lockKey == "" {
		return rdb.Del(ctx, hash).Err()
	}
	const script = `if redis.call("GET",KEYS[1]) ~= ARGV[1] then return 0 end redis.call("DEL",KEYS[2]) return 1`
	result, err := rdb.Eval(ctx, script, []string{lockKey, hash}, token).Int64()
	if err != nil {
		return err
	}
	if result != 1 {
		return ErrReconcileLeaseLost
	}
	return nil
}

func hdelHashWithLease(ctx context.Context, rdb *redis.Client, hash string, fields []string, lockKey, token string) error {
	if lockKey == "" {
		return rdb.HDel(ctx, hash, fields...).Err()
	}
	const script = `if redis.call("GET",KEYS[1]) ~= ARGV[1] then return 0 end for i=2,#ARGV do redis.call("HDEL",KEYS[2],ARGV[i]) end return 1`
	args := make([]interface{}, 0, len(fields)+1)
	args = append(args, token)
	for _, field := range fields {
		args = append(args, field)
	}
	result, err := rdb.Eval(ctx, script, []string{lockKey, hash}, args...).Int64()
	if err != nil {
		return err
	}
	if result != 1 {
		return ErrReconcileLeaseLost
	}
	return nil
}
