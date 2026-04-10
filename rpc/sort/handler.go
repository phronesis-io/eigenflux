package main

import (
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/db"
	embcodec "eigenflux_server/pkg/embedding"
	"eigenflux_server/pkg/logger"
	profileDal "eigenflux_server/rpc/profile/dal"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/ranker"
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

func cachedItemsToItems(cached []cache.CachedItem) []sortDal.Item {
	items := make([]sortDal.Item, 0, len(cached))
	for _, ci := range cached {
		var itemID int64
		fmt.Sscanf(ci.ItemID, "%d", &itemID)
		item := sortDal.Item{
			ID:           itemID,
			Content:      ci.Content,
			Summary:      ci.Summary,
			Type:         ci.BroadcastType,
			Domains:      ci.Domains,
			Keywords:     ci.Keywords,
			Geo:          ci.Geo,
			SourceType:   ci.SourceType,
			QualityScore: ci.QualityScore,
			GroupID:      ci.GroupID,
			Lang:         ci.Lang,
			Timeliness:   ci.Timeliness,
			Score:        ci.Score,
			CreatedAt:    time.UnixMilli(ci.CreatedAtMs),
			UpdatedAt:    time.UnixMilli(ci.UpdatedAtMs),
		}
		if ci.ExpireTimeMs != nil {
			t := time.UnixMilli(*ci.ExpireTimeMs)
			item.ExpireTime = &t
		}
		items = append(items, item)
	}
	return items
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

	// Fetch profile embedding for semantic scoring
	var profileEmbedding []float32
	if embeddingCache != nil {
		raw, err := embeddingCache.Get(ctx, req.AgentId)
		if err == nil && len(raw) > 0 {
			profileEmbedding = embcodec.Decode(raw)
		} else {
			// Cache miss — try DB
			ap2, _ := profileDal.GetAgentProfile(db.DB, req.AgentId)
			if ap2 != nil && len(ap2.ProfileEmbedding) > 0 {
				profileEmbedding = embcodec.Decode(ap2.ProfileEmbedding)
				// Warm cache
				go embeddingCache.Set(context.Background(), req.AgentId, ap2.ProfileEmbedding)
			}
		}
	}

	agentFeaturesJSON, _ := json.Marshal(map[string]interface{}{
		"keywords": keywords,
		"domains":  domains,
		"geo":      geo,
	})
	agentFeaturesStr := string(agentFeaturesJSON)

	// Launch kNN recall in parallel with keyword recall
	var knnItems []sortDal.Item
	var knnErr error
	var wg sync.WaitGroup
	if rankerCfg.EnableKNNRecall && len(profileEmbedding) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			filters := sortDal.BuildRecallFilters("")
			knnItems, knnErr = sortDal.SearchByEmbedding(ctx, profileEmbedding, filters, rankerCfg.KNNRecallK, rankerCfg.KNNRecallCandidates)
			if knnErr != nil {
				logger.Ctx(ctx).Warn("kNN recall failed, continuing with keyword only", "err", knnErr)
			}
		}()
	}

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
				Limit:           cfg.KeywordRecallSize,
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
			Limit:           cfg.KeywordRecallSize,
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

	// Build user profile for ranker
	userProfile := &ranker.UserProfile{
		Keywords:  keywords,
		Domains:   domains,
		Geo:       geo,
		Embedding: profileEmbedding,
	}

	// Convert to unified sortDal.Item list for ranker
	var esItems []sortDal.Item
	if searchCache != nil && len(cachedItems) > 0 {
		esItems = cachedItemsToItems(cachedItems)
	} else if searchResp != nil {
		esItems = searchResp.Items
	}

	// Wait for kNN recall and merge results
	wg.Wait()
	if knnErr == nil && len(knnItems) > 0 {
		seen := make(map[int64]bool, len(esItems))
		for _, item := range esItems {
			seen[item.ID] = true
		}
		added := 0
		for _, item := range knnItems {
			if !seen[item.ID] {
				esItems = append(esItems, item)
				seen[item.ID] = true
				added++
			}
		}
		logger.Ctx(ctx).Info("kNN merge", "knnTotal", len(knnItems), "newItems", added, "mergedTotal", len(esItems))
	}

	// Rank all recall candidates — no pre-truncation so dedup draws from the full pool.
	// Then drop low-relevance items so they don't fill the feed with irrelevant content.
	allRanked := rankerInstance.Rank(esItems, userProfile, len(esItems))
	ranked := make([]ranker.RankedItem, 0, len(allRanked))
	for _, ri := range allRanked {
		if ri.Score >= rankerCfg.MinRelevanceScore {
			ranked = append(ranked, ri)
		}
	}
	logger.Ctx(ctx).Debug("relevance filter", "before", len(allRanked), "after", len(ranked), "threshold", rankerCfg.MinRelevanceScore)

	// Build set of ranked IDs for exploration exclusion
	rankedIDs := make(map[int64]bool, len(ranked))
	for _, ri := range ranked {
		rankedIDs[ri.ItemID] = true
	}

	// Add exploration slots from remaining candidates
	if rankerCfg.ExplorationSlots > 0 {
		explorationItems := ranker.PickExplorationItems(esItems, rankedIDs, rankerCfg.ExplorationSlots, 48*time.Hour, 0.5)
		for _, ei := range explorationItems {
			ranked = append(ranked, ranker.RankedItem{ItemID: ei.ID, Score: 0.0})
		}
	}

	// Build ranked item lookup for features
	esItemMap := make(map[int64]sortDal.Item, len(esItems))
	for _, item := range esItems {
		esItemMap[item.ID] = item
	}

	// Collect all group_ids for bloom filter dedup
	type candidateItem struct {
		itemID       int64
		groupID      int64
		score        float64
		itemFeatures string
	}
	var candidates []candidateItem

	for _, ri := range ranked {
		item, ok := esItemMap[ri.ItemID]
		if !ok {
			continue
		}
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
			"rank_scores":    ri.Scores,
		}
		if item.ExpireTime != nil {
			feat["expire_time"] = item.ExpireTime.UnixMilli()
		}
		itemFeaturesJSON, _ := json.Marshal(feat)
		candidates = append(candidates, candidateItem{
			itemID:       ri.ItemID,
			groupID:      item.GroupID,
			score:        ri.Score,
			itemFeatures: string(itemFeaturesJSON),
		})
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
	} else if len(cachedItems) > 0 && len(cachedItems) == limit*5 {
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
