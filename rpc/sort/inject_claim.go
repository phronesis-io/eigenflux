package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"eigenflux_server/pkg/logger"
)

// ugcClaimKeyFmt namespaces per-item force-insert claims. A claim means "this
// item was already force-inserted into a feed recently, don't inject it again
// until the claim expires".
const ugcClaimKeyFmt = "sort:inject:claim:%d"

// fetchInjectClaims returns the subset of itemIDs that currently hold a
// force-insert claim, in one pipelined round trip. Fail-open: on Redis error it
// returns an empty set, so a transient Redis problem risks a rare double-insert
// rather than suppressing the exposure guarantee entirely.
func fetchInjectClaims(ctx context.Context, rdb *redis.Client, itemIDs []int64) map[int64]bool {
	claimed := make(map[int64]bool)
	if rdb == nil || len(itemIDs) == 0 {
		return claimed
	}
	pipe := rdb.Pipeline()
	cmds := make(map[int64]*redis.IntCmd, len(itemIDs))
	for _, id := range itemIDs {
		cmds[id] = pipe.Exists(ctx, fmt.Sprintf(ugcClaimKeyFmt, id))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		logger.Ctx(ctx).Warn("inject: claim check failed, treating all as unclaimed", "err", err)
		return map[int64]bool{}
	}
	for id, cmd := range cmds {
		if cmd.Val() > 0 {
			claimed[id] = true
		}
	}
	return claimed
}

// claimInjectedItems marks itemIDs as force-inserted for ttl so subsequent feeds
// within the window skip them, bounding over-exposure to ~once per claim window
// (which is sized to span an offline recall-index refresh). SET NX so a live
// claim's TTL is not extended. Best-effort; errors are logged, not fatal.
func claimInjectedItems(ctx context.Context, rdb *redis.Client, itemIDs []int64, ttl time.Duration) {
	if rdb == nil || len(itemIDs) == 0 || ttl <= 0 {
		return
	}
	pipe := rdb.Pipeline()
	for _, id := range itemIDs {
		pipe.SetNX(ctx, fmt.Sprintf(ugcClaimKeyFmt, id), 1, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		logger.Ctx(ctx).Warn("inject: claim write failed", "err", err)
	}
}
