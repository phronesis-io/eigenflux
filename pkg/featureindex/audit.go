package featureindex

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"github.com/redis/go-redis/v9"
)

// AuditPage adopts retention without extending existing TTLs. SCAN is bounded
// by the caller's timeout; COUNT is a hint, not a strict page size.
func AuditPage(ctx context.Context, r *redis.Client, cursor uint64, count int64) (uint64, error) {
	next, _, err := auditPage(ctx, r, cursor, count)
	return next, err
}

func auditPage(ctx context.Context, r *redis.Client, cursor uint64, count int64) (uint64, map[string]int64, error) {
	keys, next, err := r.Scan(ctx, cursor, "discovery:forward:*", count).Result()
	if err != nil {
		return cursor, nil, err
	}
	type entry struct {
		view    string
		ttl     *redis.DurationCmd
		adopted *redis.Cmd
	}
	entries := []entry{}
	pipe := r.Pipeline()
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		parts := strings.Split(strings.TrimPrefix(key, "discovery:forward:"), ":")
		if len(parts) < 4 {
			continue
		}
		id, err := strconv.ParseInt(parts[len(parts)-2], 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		view, err := defaultRegistry.resolve(strings.Join(parts[:len(parts)-2], ":"), parts[len(parts)-1])
		if err != nil {
			continue
		}
		e := entry{view: view.Name, ttl: pipe.PTTL(ctx, key)}
		if view.RetentionTTL > 0 {
			e.adopted = pipe.Do(ctx, "PEXPIRE", key, view.RetentionTTL.Milliseconds(), "NX")
		}
		entries = append(entries, e)
	}
	counts := map[string]int64{}
	if len(entries) == 0 {
		return next, counts, nil
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return cursor, nil, err
	}
	for _, e := range entries {
		ttl, err := e.ttl.Result()
		if err != nil {
			return cursor, nil, err
		}
		// go-redis preserves the sentinel -2 without converting it to milliseconds.
		if ttl != -2 {
			counts[e.view]++
			metrics.FeatureAudit.WithLabelValues("observed").Inc()
		}
		if e.adopted != nil {
			adopted, err := e.adopted.Int()
			if err != nil {
				return cursor, nil, err
			}
			if adopted == 1 {
				metrics.FeatureAudit.WithLabelValues("retention_adopted").Inc()
			}
		}
	}
	return next, counts, nil
}

const auditPrefix = "discovery:feature:census:v1"

// Checkpoint counts and cursor together, once per leased page. Publish only a
// complete census, including zeroes, so retries/replica changes cannot add a page
// twice or expose partial counts. State is O(registered views), not O(keys).
var finishAudit = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
if ARGV[2] == '0' then redis.call('DEL', KEYS[3]) end
for i=4,#ARGV,2 do redis.call('HINCRBY', KEYS[3], ARGV[i], ARGV[i+1]) end
redis.call('SET', KEYS[2], ARGV[3])
local now = redis.call('TIME')
local millis = now[1]*1000 + math.floor(now[2]/1000)
local delay = 1000
if ARGV[3] == '0' then
 redis.call('DEL', KEYS[4])
 local counts = redis.call('HGETALL', KEYS[3])
 for i=1,#counts,2 do redis.call('HSET', KEYS[4], counts[i], counts[i+1]) end
 redis.call('HSET', KEYS[4], '_completed_at', now[1])
 redis.call('DEL', KEYS[3])
 delay = 300000
end
redis.call('SET', KEYS[5], millis + delay)
redis.call('DEL', KEYS[1])
return 1
`)

func (m *Loaders) runAudit(ctx context.Context) {
	for ctx.Err() == nil {
		if err := m.auditStep(ctx); err != nil && ctx.Err() == nil {
			metrics.FeatureAudit.WithLabelValues("error").Inc()
			logger.Default().Warn("feature key census failed", "err", err)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Every replica exports the same shared snapshot. Dashboards deduplicate replicas
// with max, never sum. No census gauge is emitted before the first complete scan.
func (m *Loaders) publishKeyCounts(ctx context.Context) error {
	snapshot, err := m.Redis.HGetAll(ctx, auditPrefix+":snapshot").Result()
	if err != nil {
		return err
	}
	if len(snapshot) == 0 {
		return nil
	}
	completed, err := strconv.ParseInt(snapshot["_completed_at"], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid feature census timestamp: %w", err)
	}
	counts := map[string]int64{}
	for _, d := range Definitions() {
		raw, ok := snapshot[d.Name]
		if !ok {
			// A newly deployed view has no observation until the next full scan.
			continue
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid feature census count for %s", d.Name)
		}
		counts[d.Name] = n
	}
	for _, d := range Definitions() {
		if n, ok := counts[d.Name]; ok {
			metrics.FeatureKeys.WithLabelValues(d.Entity, d.Name).Set(float64(n))
		}
	}
	metrics.FeatureKeyScanCompleted.Set(float64(completed))
	return nil
}

func (m *Loaders) auditStep(ctx context.Context) error {
	work, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := m.publishKeyCounts(work); err != nil {
		return err
	}
	due, err := m.Redis.Get(work, auditPrefix+":next_due").Int64()
	if err != nil && err != redis.Nil {
		return err
	}
	if due > time.Now().UnixMilli() {
		return nil
	}
	token := rand.Text()
	ok, err := m.Redis.SetNX(work, auditPrefix+":lock", token, 25*time.Second).Result()
	if err != nil || !ok {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = finishLoad.Run(cleanup, m.Redis, []string{auditPrefix + ":lock", auditPrefix + ":cursor"}, token, "").Err()
	}()
	// A different replica may have completed a page between the first due check
	// and acquiring the lease. Recheck under the lease to preserve global pacing.
	due, err = m.Redis.Get(work, auditPrefix+":next_due").Int64()
	if err != nil && err != redis.Nil {
		return err
	}
	if due > time.Now().UnixMilli() {
		return nil
	}
	cursor, err := m.Redis.Get(work, auditPrefix+":cursor").Uint64()
	if err != nil && err != redis.Nil {
		return err
	}
	next, counts, err := auditPage(work, m.Redis, cursor, 1000)
	if err != nil {
		return err
	}
	args := []any{token, cursor, next}
	for _, d := range Definitions() {
		args = append(args, d.Name, counts[d.Name])
	}
	n, err := finishAudit.Run(work, m.Redis, []string{auditPrefix + ":lock", auditPrefix + ":cursor", auditPrefix + ":counts", auditPrefix + ":snapshot", auditPrefix + ":next_due"}, args...).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("feature census lease lost")
	}
	if next == 0 {
		metrics.FeatureAudit.WithLabelValues("cycle_complete").Inc()
	}
	return m.publishKeyCounts(work)
}
