package legacy

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"eigenflux_server/pkg/config"
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

// resolveContentClasses classifies each item as UGC or PGC by its author's email
// suffix. PGC = author email ends with a configured PGC suffix (official bots,
// e.g. @bot.eigenflux.one / @pgc.eigenflux.one); every other author, including
// one that cannot be resolved, is UGC. Verdicts are cached per author
// (s.contentClassCache), so a hot feed only queries the agents table for authors
// not seen within the TTL window. The result is keyed by item ID.
func (s *Service) resolveContentClasses(ctx context.Context, items []sortDal.Item) map[int64]string {
	if s.replayContentClasses != nil {
		return s.replayContentClasses
	}
	classes := make(map[int64]string, len(items))
	if len(items) == 0 {
		return classes
	}
	authors := make([]int64, 0, len(items))
	for _, it := range items {
		authors = append(authors, it.AuthorAgentID)
	}
	pgcByAuthor, err := s.pgcAuthors(ctx, authors)
	if err != nil {
		logger.Ctx(ctx).Warn("content class author lookup failed, treating misses as UGC", "err", err)
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

// DiscoveryPGCAuthors classifies broadcast authors for the discovery rating
// model with the same cached verdicts. A failed lookup is an error rather than
// UGC, so the model never scores a guessed classification.
func (s *Service) DiscoveryPGCAuthors(ctx context.Context, authors []int64) (map[int64]bool, error) {
	out, err := s.pgcAuthors(ctx, authors)
	if err != nil {
		return nil, err
	}
	if slices.Contains(authors, 0) {
		out[0] = false
	}
	return out, nil
}

// pgcAuthors returns a verdict for every nonzero author. Authors without an
// agents row are UGC. On a failed lookup the returned map holds only cached
// verdicts.
func (s *Service) pgcAuthors(ctx context.Context, authors []int64) (map[int64]bool, error) {
	pgcByAuthor := make(map[int64]bool, len(authors))
	var misses []int64
	seen := make(map[int64]struct{}, len(authors))
	for _, id := range authors {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if isPGC, ok := s.contentClassCache.Get(id); ok {
			pgcByAuthor[id] = isPGC
		} else {
			misses = append(misses, id)
		}
	}
	if len(misses) == 0 {
		return pgcByAuthor, nil
	}
	if s.db == nil {
		return pgcByAuthor, fmt.Errorf("content class store unavailable")
	}
	resolved := make(map[int64]bool, len(misses))
	for _, id := range misses {
		// Missing authors are UGC; caching the verdict prevents re-querying an
		// author with no agents row every request.
		resolved[id] = false
	}
	var rows []struct {
		AgentID int64  `gorm:"column:agent_id"`
		Email   string `gorm:"column:email"`
	}
	if err := s.db.WithContext(ctx).Table("agents").
		Select("agent_id, email").
		Where("agent_id IN ?", misses).
		Scan(&rows).Error; err != nil {
		return pgcByAuthor, err
	}
	for _, r := range rows {
		resolved[r.AgentID] = config.EmailMatchesAnySuffix(r.Email, s.cfg.PGCEmailSuffixes)
	}
	for id, isPGC := range resolved {
		s.contentClassCache.PutUntil(id, isPGC, time.Now().Add(pgcClassCacheTTL))
	}
	maps.Copy(pgcByAuthor, resolved)
	return pgcByAuthor, nil
}
