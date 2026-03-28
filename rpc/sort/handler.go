package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	profileDal "eigenflux_server/rpc/profile/dal"
	sortDal "eigenflux_server/rpc/sort/dal"
)

// SortServiceESImpl implements SortService using Elasticsearch
type SortServiceESImpl struct{}

// SingleFlight group for deduplicating concurrent requests
var sfGroup singleflight.Group

func (s *SortServiceESImpl) SortItems(ctx context.Context, req *sort.SortItemsReq) (*sort.SortItemsResp, error) {
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}

	logger.FromContext(ctx).Info("sort request", "agentID", req.GetAgentId(), "limit", limit, "lastUpdatedAt", req.GetLastUpdatedAt())

	// Get user profile (with caching if enabled)
	var keywords []string
	var domains []string
	var geo string

	if profileCache != nil {
		// Try cache first
		cachedProfile, err := profileCache.Get(ctx, req.AgentId)
		switch err {
		case nil:
			keywords = cachedProfile.Keywords
			domains = cachedProfile.Domains
			geo = cachedProfile.Geo
			logger.FromContext(ctx).Debug("profile from cache", "keywords", keywords, "domains", domains, "geo", geo)
		case cache.ErrCacheMiss:
			// Cache miss, fetch from DB
			logger.FromContext(ctx).Debug("profile cache miss, fetching from DB")
			ap, _ := profileDal.GetAgentProfile(db.DB, req.AgentId)
			if ap != nil && ap.Keywords != "" && ap.Status == 3 {
				kws := strings.Split(ap.Keywords, ",")
				cleanKeywords := make([]string, 0, len(kws))
				for _, kw := range kws {
					kw = strings.TrimSpace(kw)
					if kw != "" {
						cleanKeywords = append(cleanKeywords, kw)
					}
				}
				keywords = cleanKeywords
				domains = cleanKeywords
				geo = "" // TODO: extract from profile if available

				logger.FromContext(ctx).Debug("profile from DB", "keywords", keywords, "domains", domains, "geo", geo)

				// Update cache
				profileCache.Set(ctx, &cache.CachedProfile{
					AgentID:  req.AgentId,
					Keywords: keywords,
					Domains:  domains,
					Geo:      geo,
				})
			}
		default:
		}
	} else {
		// No cache, fetch directly from DB
		logger.FromContext(ctx).Debug("no profile cache, fetching from DB")
		ap, _ := profileDal.GetAgentProfile(db.DB, req.AgentId)
		if ap != nil && ap.Keywords != "" && ap.Status == 3 {
			kws := strings.Split(ap.Keywords, ",")
			cleanKeywords := make([]string, 0, len(kws))
			for _, kw := range kws {
				kw = strings.TrimSpace(kw)
				if kw != "" {
					cleanKeywords = append(cleanKeywords, kw)
				}
			}
			keywords = cleanKeywords
			domains = cleanKeywords
		}
		logger.FromContext(ctx).Debug("profile from DB", "keywords", keywords, "domains", domains)
	}

	// Build cache key for search results
	var cachedItems []cache.CachedItem
	var cacheKey string
	var searchResp *sortDal.SearchItemsResponse
	var err error

	if searchCache != nil && len(domains) > 0 {
		// Build cache key (excluding last_updated_at for better hit rate)
		cacheKey = searchCache.BuildCacheKey(domains, keywords, geo)
		logger.FromContext(ctx).Debug("search cache enabled", "key", cacheKey)

		// Use SingleFlight to deduplicate concurrent requests
		result, sfErr, _ := sfGroup.Do(cacheKey, func() (interface{}, error) {
			// Try cache first
			items, cacheErr := searchCache.Get(ctx, cacheKey)
			if cacheErr == nil {
				logger.FromContext(ctx).Debug("search cache HIT", "items", len(items))
				return items, nil
			}

			logger.FromContext(ctx).Debug("search cache MISS, querying ES")
			// Cache miss, query ES
			searchReq := &sortDal.SearchItemsRequest{
				Limit:    limit * 3, // Fetch more to account for dedup
				Domains:  domains,
				Keywords: keywords,
				Geo:      geo,
			}

			// Set cursor pagination (align to seconds for ES cache)
			if req.GetLastUpdatedAt() > 0 {
				timestampSec := req.GetLastUpdatedAt() / 1000
				searchReq.LastUpdatedAt = time.Unix(timestampSec, 0)
			}

			resp, esErr := sortDal.SearchItems(ctx, searchReq)
			if esErr != nil {
				logger.FromContext(ctx).Error("ES query failed", "err", esErr)
				return nil, esErr
			}

			logger.FromContext(ctx).Info("ES returned items", "count", len(resp.Items), "total", resp.Total)

			// Convert to cached items
			cachedItems := make([]cache.CachedItem, len(resp.Items))
			for i, item := range resp.Items {
				cachedItems[i] = cache.CachedItem{
					ItemID:    fmt.Sprintf("%d", item.ID),
					Content:   item.Content,
					Summary:   item.Summary,
					Type:      item.Type,
					Domains:   item.Domains,
					Keywords:  item.Keywords,
					GroupID:   item.GroupID,
					UpdatedAt: item.UpdatedAt.Unix(),
					Score:     0, // TODO: calculate score if needed
				}
			}

			// Update cache (fire-and-forget)
			go func() {
				if setErr := searchCache.Set(context.Background(), cacheKey, cachedItems); setErr != nil {
					slog.Warn("failed to update search cache", "err", setErr)
				}
			}()

			return cachedItems, nil
		})

		if sfErr != nil {
			err = sfErr
		} else {
			cachedItems = result.([]cache.CachedItem)
		}
	} else {
		// No cache, query ES directly
		logger.FromContext(ctx).Debug("no search cache, querying ES directly")
		searchReq := &sortDal.SearchItemsRequest{
			Limit:    limit * 3, // Fetch more to account for dedup
			Domains:  domains,
			Keywords: keywords,
			Geo:      geo,
		}

		if req.GetLastUpdatedAt() > 0 {
			timestampSec := req.GetLastUpdatedAt() / 1000
			searchReq.LastUpdatedAt = time.Unix(timestampSec, 0)
		}

		searchResp, err = sortDal.SearchItems(ctx, searchReq)
		if err != nil {
			logger.FromContext(ctx).Error("ES query failed", "err", err)
			return &sort.SortItemsResp{
				BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()},
			}, nil
		}
		logger.FromContext(ctx).Info("ES returned items", "count", len(searchResp.Items), "total", searchResp.Total)
	}

	// Handle error from cached path
	if err != nil {
		return &sort.SortItemsResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()},
		}, nil
	}

	// Filter by timestamp if using cache
	if searchCache != nil && len(cachedItems) > 0 {
		lastFetchTime := req.GetLastUpdatedAt() / 1000
		beforeFilter := len(cachedItems)
		cachedItems = cache.FilterByTimestamp(cachedItems, lastFetchTime)
		logger.FromContext(ctx).Debug("timestamp filter", "before", beforeFilter, "after", len(cachedItems))
	}

	// Collect all group_ids for bloom filter dedup
	type candidateItem struct {
		itemID  int64
		groupID int64
	}
	var candidates []candidateItem

	if searchCache != nil && len(cachedItems) > 0 {
		for _, item := range cachedItems {
			var itemID int64
			fmt.Sscanf(item.ItemID, "%d", &itemID)
			candidates = append(candidates, candidateItem{itemID: itemID, groupID: item.GroupID})
		}
	} else if searchResp != nil {
		for _, item := range searchResp.Items {
			candidates = append(candidates, candidateItem{itemID: item.ID, groupID: item.GroupID})
		}
	}

	// Bloom filter dedup by group_id (unless disabled in dev/test)
	seenGroupIDs := make(map[int64]bool)
	if !cfg.ShouldDisableDedup() && bf != nil {
		allGroupIDs := make([]int64, 0, len(candidates))
		for _, c := range candidates {
			if c.groupID != 0 {
				allGroupIDs = append(allGroupIDs, c.groupID)
			}
		}
		if len(allGroupIDs) > 0 {
			seenMap, bfErr := bf.CheckExists(ctx, req.AgentId, allGroupIDs)
			if bfErr != nil {
				logger.FromContext(ctx).Warn("bloom filter check failed", "err", bfErr)
			} else {
				seenGroupIDs = seenMap
				logger.FromContext(ctx).Debug("bloom filter result", "seenGroups", len(seenGroupIDs), "totalGroups", len(allGroupIDs))
			}
		}
	} else if cfg.ShouldDisableDedup() {
		logger.FromContext(ctx).Info("deduplication disabled", "env", cfg.AppEnv)
	}

	// Filter and collect final item IDs
	itemIDs := make([]int64, 0, limit)
	dedupedCount := 0
	for _, c := range candidates {
		if c.groupID != 0 && seenGroupIDs[c.groupID] {
			dedupedCount++
			continue
		}
		itemIDs = append(itemIDs, c.itemID)
		if len(itemIDs) >= limit {
			break
		}
	}

	logger.FromContext(ctx).Info("dedup result", "filtered", dedupedCount, "returned", len(itemIDs))

	// Calculate next cursor
	var nextCursor int64
	if searchResp != nil && !searchResp.NextCursor.IsZero() {
		nextCursor = searchResp.NextCursor.Unix()
	} else if len(cachedItems) > 0 && len(cachedItems) == limit*3 {
		// If cache is full, use last item's timestamp as cursor
		nextCursor = cachedItems[len(cachedItems)-1].UpdatedAt
	}

	return &sort.SortItemsResp{
		ItemIds:    itemIDs,
		NextCursor: nextCursor,
		BaseResp:   &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}
