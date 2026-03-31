package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/db"
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

	log.Printf("[Sort] Request: agent_id=%d, limit=%d, last_updated_at=%d",
		req.GetAgentId(), limit, req.GetLastUpdatedAt())

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
			log.Printf("[Sort] Profile from cache: keywords=%v, domains=%v, geo=%s",
				keywords, domains, geo)
		case cache.ErrCacheMiss:
			// Cache miss, fetch from DB
			log.Printf("[Sort] Profile cache miss, fetching from DB")
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

				log.Printf("[Sort] Profile from DB: keywords=%v, domains=%v, geo=%s",
					keywords, domains, geo)

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
		log.Printf("[Sort] No profile cache, fetching from DB")
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
		log.Printf("[Sort] Profile from DB: keywords=%v, domains=%v", keywords, domains)
	}

	// Build cache key for search results
	var cachedItems []cache.CachedItem
	var cacheKey string
	var searchResp *sortDal.SearchItemsResponse
	var err error

	if searchCache != nil && len(domains) > 0 {
		// Build cache key (excluding last_updated_at for better hit rate)
		cacheKey = searchCache.BuildCacheKey(domains, keywords, geo)
		log.Printf("[Sort] Search cache enabled, key=%s", cacheKey)

		// Use SingleFlight to deduplicate concurrent requests
		result, sfErr, _ := sfGroup.Do(cacheKey, func() (interface{}, error) {
			// Try cache first
			items, cacheErr := searchCache.Get(ctx, cacheKey)
			if cacheErr == nil {
				log.Printf("[Sort] Search cache HIT, items=%d", len(items))
				return items, nil
			}

			log.Printf("[Sort] Search cache MISS, querying ES")
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
				log.Printf("[Sort] ES query failed: %v", esErr)
				return nil, esErr
			}

			log.Printf("[Sort] ES returned %d items, total=%d", len(resp.Items), resp.Total)

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
					log.Printf("Failed to update search cache: %v", setErr)
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
		log.Printf("[Sort] No search cache, querying ES directly")
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
			log.Printf("[Sort] ES query failed: %v", err)
			return &sort.SortItemsResp{
				BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()},
			}, nil
		}
		log.Printf("[Sort] ES returned %d items, total=%d", len(searchResp.Items), searchResp.Total)
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
		log.Printf("[Sort] Timestamp filter: before=%d, after=%d", beforeFilter, len(cachedItems))
	} else if searchResp != nil && len(searchResp.Items) > 0 {
		lastFetchTime := req.GetLastUpdatedAt() / 1000
		beforeFilter := len(searchResp.Items)
		searchResp.Items = filterSearchItemsByTimestamp(searchResp.Items, lastFetchTime)
		log.Printf("[Sort] Timestamp filter (non-cache): before=%d, after=%d", beforeFilter, len(searchResp.Items))
		if len(searchResp.Items) > 0 {
			searchResp.NextCursor = searchResp.Items[len(searchResp.Items)-1].UpdatedAt
		} else {
			searchResp.NextCursor = time.Time{}
		}
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
				log.Printf("[Sort] Warning: bloom filter check failed: %v", bfErr)
			} else {
				seenGroupIDs = seenMap
				log.Printf("[Sort] Bloom filter: %d seen groups out of %d", len(seenGroupIDs), len(allGroupIDs))
			}
		}
	} else if cfg.ShouldDisableDedup() {
		log.Printf("[Sort] Deduplication disabled (DISABLE_DEDUP_IN_TEST=true in %s environment)", cfg.AppEnv)
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

	log.Printf("[Sort] Dedup result: filtered=%d, returned=%d", dedupedCount, len(itemIDs))

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
