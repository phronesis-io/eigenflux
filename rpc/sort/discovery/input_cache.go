package discovery

import (
	"context"
	"sort"
	"time"

	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/need"
)

// CachedNeeds caches automatic selection only. Explicit Current/CheckIntent
// retain authoritative ownership and lifecycle checks at execution start.
type CachedNeeds struct {
	NeedReader
	Cache *cache.DiscoveryCache
}

func (s CachedNeeds) Active(ctx context.Context, owner int64, kinds []string, now int64) ([]need.Snapshot, error) {
	kinds = append([]string(nil), kinds...)
	sort.Strings(kinds)
	var out []need.Snapshot
	err := s.Cache.Load(ctx, owner, "needs", contextDigest(kinds), now, cache.DiscoveryInputTTL, &out, func(ctx context.Context) (any, int64, error) {
		rows, err := s.NeedReader.Active(ctx, owner, kinds, now)
		if err != nil {
			return nil, 0, err
		}
		until, err := needsUntil(rows, now)
		return rows, until, err
	})
	return out, err
}

// Eligible caches the unbounded list; rotation runs after the cache so the TTL
// never pins a window. It shares Active's expiry rules under its own key.
func (s CachedNeeds) Eligible(ctx context.Context, owner int64, kinds []string, now int64) ([]need.Snapshot, error) {
	reader, ok := s.NeedReader.(EligibleNeedReader)
	if !ok {
		return s.Active(ctx, owner, kinds, now)
	}
	kinds = append([]string(nil), kinds...)
	sort.Strings(kinds)
	var out []need.Snapshot
	err := s.Cache.Load(ctx, owner, "needs_eligible", contextDigest(kinds), now, cache.DiscoveryInputTTL, &out, func(ctx context.Context) (any, int64, error) {
		rows, err := reader.Eligible(ctx, owner, kinds, now)
		if err != nil {
			return nil, 0, err
		}
		until, err := needsUntil(rows, now)
		return rows, until, err
	})
	return out, err
}

func needsUntil(rows []need.Snapshot, now int64) (int64, error) {
	until := now + cache.DiscoveryInputTTL.Milliseconds()
	if len(rows) == 0 {
		until = now + cache.DiscoveryEmptyTTL.Milliseconds()
	}
	for _, row := range rows {
		in, err := row.ExecutionInput()
		if err != nil {
			return 0, err
		}
		if d := in.Constraints.DeadlineMS; d != nil && *d < until {
			until = *d
		}
	}
	return until, nil
}

func (s *Source) Owner(ctx context.Context, owner int64) (OwnerContext, error) {
	var out OwnerContext
	now := time.Now().UnixMilli()
	err := s.ContextCache.Load(ctx, owner, "owner", "v1", now, cache.DiscoveryInputTTL, &out, func(ctx context.Context) (any, int64, error) {
		value, err := s.loadOwner(ctx, owner)
		until := now + cache.DiscoveryInputTTL.Milliseconds()
		if len(value.Clauses) == 0 {
			until = now + cache.DiscoveryEmptyTTL.Milliseconds()
		}
		return value, until, err
	})
	return out, err
}
