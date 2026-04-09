package main

import (
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
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

func filterSearchItemsByTimestamp(items []sortDal.Item, lastFetchTimeSec int64) []sortDal.Item {
	if lastFetchTimeSec == 0 {
		return items
	}

	filtered := make([]sortDal.Item, 0, len(items))
	for _, item := range items {
		if item.UpdatedAt.Unix() > lastFetchTimeSec {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (s *SortServiceESImpl) SortItems(ctx context.Context, req *sort.SortItemsReq) (*sort.SortItemsResp, error) {
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}

	logger.Ctx(ctx).Info("sort request", "agentID", req.GetAgentId(), "limit", limit, "lastUpdatedAt", req.GetLastUpdatedAt())

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
			logger.Ctx(ctx).Debug("profile from cache", "keywords", keywords, "domains", domains, "geo", geo)
		case cache.ErrCacheMiss:
			// Cache miss, fetch from DB
			logger.Ctx(ctx).Debug("profile cache miss, fetching from DB")
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

				logger.Ctx(ctx).Debug("profile from DB", "keywords", keywords, "domains", domains, "geo", geo)

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
		logger.Ctx(ctx).Debug("no profile cache, fetching from DB")
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
		logger.Ctx(ctx).Debug("profile from DB", "keywords", keywords, "domains", domains)
	}

	agentFeaturesJSON, _ := json.Marshal(map[string]interface{}{
		"keywords": keywords,
		"domains":  domains,
		"geo":      geo,
	})
	agentFeaturesStr := string(agentFeaturesJSON)

	// Build cache key for search results
	var cachedItems []cache.CachedItem
	var cacheKey string
	var searchResp *sortDal.SearchItemsResponse
	var err error

	if searchCache != nil && len(domains) > 0 {
		// Build cache key (excluding last_updated_at for better hit rate)
		cacheKey = searchCache.BuildCacheKey(domains, keywords, geo)
		logger.Ctx(ctx).Debug("search cache enabled", "key", cacheKey)

		// Use SingleFlight to deduplicate concurrent requests
		result, sfErr, _ := sfGroup.Do(cacheKey, func() (interface{}, error) {
			// Try cache first
			items, cacheErr := searchCache.Get(ctx, cacheKey)
			if cacheErr == nil {
				logger.Ctx(ctx).Debug("search cache HIT", "items", len(items))
				return items, nil
			}

			logger.Ctx(ctx).Debug("search cache MISS, querying ES")
			// Cache miss, query ES
			searchReq := &sortDal.SearchItemsRequest{
				Limit:           limit * 3, // Fetch more to account for dedup
				Domains:         domains,
				Keywords:        keywords,
				Geo:             geo,
				FreshnessOffset: cfg.FreshnessOffset,
				FreshnessScale:  cfg.FreshnessScale,
				FreshnessDecay:  cfg.FreshnessDecay,
			}

			resp, esErr := sortDal.SearchItems(ctx, searchReq)
			if esErr != nil {
				logger.Ctx(ctx).Error("ES query failed", "err", esErr)
				return nil, esErr
			}

			logger.Ctx(ctx).Info("ES returned items", "count", len(resp.Items), "total", resp.Total)

			// Convert to cached items
			cachedItems := make([]cache.CachedItem, len(resp.Items))
			for i, item := range resp.Items {
				ci := cache.CachedItem{
					ItemID:        fmt.Sprintf("%d", item.ID),
					Content:       item.Content,
					Summary:       item.Summary,
					BroadcastType: item.Type,
					Domains:       item.Domains,
					Keywords:      item.Keywords,
					Geo:           item.Geo,
					SourceType:    item.SourceType,
					QualityScore:  item.QualityScore,
					GroupID:       item.GroupID,
					Lang:          item.Lang,
					Timeliness:    item.Timeliness,
					CreatedAtMs:   item.CreatedAt.UnixMilli(),
					UpdatedAt:     item.UpdatedAt.Unix(),
					UpdatedAtMs:   item.UpdatedAt.UnixMilli(),
					Score:         item.Score,
				}
				if item.ExpireTime != nil {
					ms := item.ExpireTime.UnixMilli()
					ci.ExpireTimeMs = &ms
				}
				cachedItems[i] = ci
			}

			// Update cache (fire-and-forget)
			go func() {
				if setErr := searchCache.Set(context.Background(), cacheKey, cachedItems); setErr != nil {
					logger.Default().Warn("failed to update search cache", "err", setErr)
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
		logger.Ctx(ctx).Debug("no search cache, querying ES directly")
		searchReq := &sortDal.SearchItemsRequest{
			Limit:           limit * 3, // Fetch more to account for dedup
			Domains:         domains,
			Keywords:        keywords,
			Geo:             geo,
			FreshnessOffset: cfg.FreshnessOffset,
			FreshnessScale:  cfg.FreshnessScale,
			FreshnessDecay:  cfg.FreshnessDecay,
		}

		searchResp, err = sortDal.SearchItems(ctx, searchReq)
		if err != nil {
			logger.Ctx(ctx).Error("ES query failed", "err", err)
			return &sort.SortItemsResp{
				BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()},
			}, nil
		}
		logger.Ctx(ctx).Info("ES returned items", "count", len(searchResp.Items), "total", searchResp.Total)
	}

	// Handle error from cached path
	if err != nil {
		return &sort.SortItemsResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()},
		}, nil
	}

	// Apply timestamp filtering after ES retrieval so refresh semantics stay outside the ES DSL.
	if searchCache != nil && len(cachedItems) > 0 {
		lastFetchTime := req.GetLastUpdatedAt() / 1000
		beforeFilter := len(cachedItems)
		cachedItems = cache.FilterByTimestamp(cachedItems, lastFetchTime)
		logger.Ctx(ctx).Debug("timestamp filter", "before", beforeFilter, "after", len(cachedItems))
	} else if searchResp != nil && len(searchResp.Items) > 0 {
		lastFetchTime := req.GetLastUpdatedAt() / 1000
		beforeFilter := len(searchResp.Items)
		searchResp.Items = filterSearchItemsByTimestamp(searchResp.Items, lastFetchTime)
		logger.Ctx(ctx).Debug("timestamp filter", "before", beforeFilter, "after", len(searchResp.Items))
		if len(searchResp.Items) > 0 {
			searchResp.NextCursor = searchResp.Items[len(searchResp.Items)-1].UpdatedAt
		} else {
			searchResp.NextCursor = time.Time{}
		}
	}

	// Collect all group_ids for bloom filter dedup
	type candidateItem struct {
		itemID       int64
		groupID      int64
		score        float64
		itemFeatures string
	}
	var candidates []candidateItem

	if searchCache != nil && len(cachedItems) > 0 {
		for _, item := range cachedItems {
			var itemID int64
			fmt.Sscanf(item.ItemID, "%d", &itemID)
			feat := map[string]interface{}{
				"broadcast_type": item.BroadcastType,
				"domains":        item.Domains,
				"keywords":       item.Keywords,
				"geo":            item.Geo,
				"source_type":    item.SourceType,
				"quality_score":  item.QualityScore,
				"group_id":       item.GroupID,
				"lang":           item.Lang,
				"timeliness":     item.Timeliness,
				"updated_at":     item.UpdatedAtMs,
				"created_at":     item.CreatedAtMs,
			}
			if item.ExpireTimeMs != nil {
				feat["expire_time"] = *item.ExpireTimeMs
			}
			itemFeaturesJSON, _ := json.Marshal(feat)
			candidates = append(candidates, candidateItem{
				itemID:       itemID,
				groupID:      item.GroupID,
				score:        item.Score,
				itemFeatures: string(itemFeaturesJSON),
			})
		}
	} else if searchResp != nil {
		for _, item := range searchResp.Items {
			feat := map[string]interface{}{
				"broadcast_type": item.Type,
				"domains":        item.Domains,
				"keywords":       item.Keywords,
				"geo":            item.Geo,
				"source_type":    item.SourceType,
				"quality_score":  item.QualityScore,
				"group_id":       item.GroupID,
				"lang":           item.Lang,
				"timeliness":     item.Timeliness,
				"updated_at":     item.UpdatedAt.UnixMilli(),
				"created_at":     item.CreatedAt.UnixMilli(),
			}
			if item.ExpireTime != nil {
				feat["expire_time"] = item.ExpireTime.UnixMilli()
			}
			itemFeaturesJSON, _ := json.Marshal(feat)
			candidates = append(candidates, candidateItem{
				itemID:       item.ID,
				groupID:      item.GroupID,
				score:        item.Score,
				itemFeatures: string(itemFeaturesJSON),
			})
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
				logger.Ctx(ctx).Warn("bloom filter check failed", "err", bfErr)
			} else {
				seenGroupIDs = seenMap
				logger.Ctx(ctx).Debug("bloom filter result", "seenGroups", len(seenGroupIDs), "totalGroups", len(allGroupIDs))
			}
		}
	} else if cfg.ShouldDisableDedup() {
		logger.Ctx(ctx).Info("deduplication disabled", "env", cfg.AppEnv)
	}

	// Filter and collect final item IDs
	itemIDs := make([]int64, 0, limit)
	sortedItems := make([]*sort.SortedItem, 0, limit)
	dedupedCount := 0
	for _, c := range candidates {
		if c.groupID != 0 && seenGroupIDs[c.groupID] {
			dedupedCount++
			continue
		}
		itemIDs = append(itemIDs, c.itemID)
		agentFeatCopy := agentFeaturesStr
		itemFeatCopy := c.itemFeatures
		sortedItems = append(sortedItems, &sort.SortedItem{
			ItemId:        c.itemID,
			Score:         c.score,
			AgentFeatures: &agentFeatCopy,
			ItemFeatures:  &itemFeatCopy,
		})
		if len(itemIDs) >= limit {
			break
		}
	}

	logger.Ctx(ctx).Info("dedup result", "filtered", dedupedCount, "returned", len(itemIDs))

	// Calculate next cursor
	var nextCursor int64
	if searchResp != nil && !searchResp.NextCursor.IsZero() {
		nextCursor = searchResp.NextCursor.Unix()
	} else if len(cachedItems) > 0 && len(cachedItems) == limit*3 {
		// If cache is full, use last item's timestamp as cursor
		nextCursor = cachedItems[len(cachedItems)-1].UpdatedAt
	}

	return &sort.SortItemsResp{
		ItemIds:     itemIDs,
		NextCursor:  nextCursor,
		SortedItems: sortedItems,
		BaseResp:    &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}
