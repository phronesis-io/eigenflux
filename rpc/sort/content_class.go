package main

import (
	"context"
	"maps"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	sortDal "eigenflux_server/rpc/sort/dal"
)

const (
	contentClassUGC = "ugc"
	contentClassPGC = "pgc"
)

// contentClassCache is a process-local author_agent_id → isPGC LRU. PGC
// membership is derived from the author's email suffix and is effectively static
// (a bot account stays a bot), so it caches well; feed traffic is hot and
// author-concentrated, so in steady state nearly every lookup is a cache hit and
// the agents table is only touched for authors not seen recently.
//
// The TTL bounds staleness (only relevant for the rare email change); the size
// cap bounds memory (each entry is one int64+bool, so the cap is generous).
const (
	pgcClassCacheTTL  = 12 * time.Hour
	pgcClassCacheSize = 100_000
)

var contentClassCache = expirable.NewLRU[int64, bool](pgcClassCacheSize, nil, pgcClassCacheTTL)

// resolveContentClasses classifies each item as UGC or PGC by its author's email
// suffix. PGC = author email ends with a configured PGC suffix (official bots,
// e.g. @bot.eigenflux.one / @pgc.eigenflux.one); every other author, including
// one that cannot be resolved, is UGC. Verdicts are cached per author
// (contentClassCache), so a hot feed only queries the agents table for authors
// not seen within the TTL window. The result is keyed by item ID.
func resolveContentClasses(ctx context.Context, items []sortDal.Item) map[int64]string {
	classes := make(map[int64]string, len(items))
	if len(items) == 0 {
		return classes
	}

	pgcByAuthor := make(map[int64]bool, len(items))
	var misses []int64
	seen := make(map[int64]struct{}, len(items))
	for _, it := range items {
		if it.AuthorAgentID == 0 {
			continue
		}
		if _, ok := seen[it.AuthorAgentID]; ok {
			continue
		}
		seen[it.AuthorAgentID] = struct{}{}
		if isPGC, ok := contentClassCache.Get(it.AuthorAgentID); ok {
			pgcByAuthor[it.AuthorAgentID] = isPGC
		} else {
			misses = append(misses, it.AuthorAgentID)
		}
	}

	if len(misses) > 0 {
		resolved := make(map[int64]bool, len(misses))
		for _, id := range misses {
			// Default missing/unresolved authors to UGC; caching the verdict
			// prevents re-querying an author with no agents row every request.
			resolved[id] = false
		}
		var rows []struct {
			AgentID int64  `gorm:"column:agent_id"`
			Email   string `gorm:"column:email"`
		}
		if err := db.DB.WithContext(ctx).Table("agents").
			Select("agent_id, email").
			Where("agent_id IN ?", misses).
			Scan(&rows).Error; err != nil {
			logger.Ctx(ctx).Warn("content class author lookup failed, treating misses as UGC", "err", err)
		} else {
			for _, r := range rows {
				resolved[r.AgentID] = config.EmailMatchesAnySuffix(r.Email, cfg.PGCEmailSuffixes)
			}
			for id, isPGC := range resolved {
				contentClassCache.Add(id, isPGC)
			}
		}
		maps.Copy(pgcByAuthor, resolved)
	}

	for _, it := range items {
		if pgcByAuthor[it.AuthorAgentID] {
			classes[it.ID] = contentClassPGC
		} else {
			classes[it.ID] = contentClassUGC
		}
	}
	return classes
}
