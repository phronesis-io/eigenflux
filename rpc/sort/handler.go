package main

import (
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
	"slices"
	"strconv"
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
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/recallsource"
	"eigenflux_server/pkg/reqinfo"
	profileDal "eigenflux_server/rpc/profile/dal"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/rank"
	"eigenflux_server/rpc/sort/ranker"
	"eigenflux_server/rpc/sort/rerank"
)

// SortServiceESImpl implements SortService using Elasticsearch
type SortServiceESImpl struct{}

// SingleFlight group for deduplicating concurrent requests
var sfGroup singleflight.Group

func collapseRankedByGroup(ranked []ranker.RankedItem, itemMap map[int64]sortDal.Item) ([]ranker.RankedItem, int) {
	if len(ranked) == 0 {
		return nil, 0
	}

	collapsed := make([]ranker.RankedItem, 0, len(ranked))
	seenGroupIDs := make(map[int64]bool, len(ranked))
	seenItemIDs := make(map[int64]bool, len(ranked))
	filtered := 0

	for _, ri := range ranked {
		if seenItemIDs[ri.ItemID] {
			filtered++
			continue
		}

		item, ok := itemMap[ri.ItemID]
		if !ok {
			collapsed = append(collapsed, ri)
			seenItemIDs[ri.ItemID] = true
			continue
		}
		if item.GroupID != 0 {
			if seenGroupIDs[item.GroupID] {
				filtered++
				continue
			}
			seenGroupIDs[item.GroupID] = true
		}

		seenItemIDs[ri.ItemID] = true
		collapsed = append(collapsed, ri)
	}

	return collapsed, filtered
}

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
		itemID, _ := strconv.ParseInt(ci.ItemID, 10, 64)
		item := sortDal.Item{
			ID:            itemID,
			AuthorAgentID: ci.AuthorAgentID,
			Content:       ci.Content,
			Summary:       ci.Summary,
			Type:          ci.BroadcastType,
			Domains:       ci.Domains,
			Keywords:      ci.Keywords,
			Geo:           ci.Geo,
			SourceType:    ci.SourceType,
			QualityScore:  ci.QualityScore,
			GroupID:       ci.GroupID,
			Lang:          ci.Lang,
			Timeliness:    ci.Timeliness,
			Embedding:     embcodec.Decode(ci.Embedding),
			Score:         ci.Score,
			CreatedAt:     time.UnixMilli(ci.CreatedAtMs),
			UpdatedAt:     time.UnixMilli(ci.UpdatedAtMs),
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

	agentFeaturesMap := map[string]interface{}{
		"keywords": keywords,
		"domains":  domains,
		"geo":      geo,
	}
	if ctxFeat := buildContextFeatures(reqinfo.ClientFromContext(ctx)); ctxFeat != nil {
		agentFeaturesMap["context"] = ctxFeat
	}
	agentFeaturesJSON, _ := json.Marshal(agentFeaturesMap)
	agentFeaturesStr := string(agentFeaturesJSON)

	// Launch kNN recall and recall sources in parallel with keyword recall
	var knnItems []sortDal.Item
	var knnErr error
	var wg sync.WaitGroup
	if rankerCfg.EnableKNNRecall && len(profileEmbedding) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			filters := sortDal.BuildRecallFiltersWithExclude("", time.Now(), req.AgentId)
			knnItems, knnErr = sortDal.SearchByEmbedding(ctx, profileEmbedding, filters, rankerCfg.KNNRecallK, rankerCfg.KNNRecallCandidates)
			if knnErr != nil {
				logger.Ctx(ctx).Warn("kNN recall failed, continuing with keyword only", "err", knnErr)
			}
		}()
	}

	// Launch all recall sources concurrently
	type recallResult struct {
		candidates []recallsource.Candidate
		name       string
	}
	recallResults := make([]recallResult, len(recallSources))
	for i, src := range recallSources {
		wg.Add(1)
		go func(idx int, rs recallsource.RecallSource) {
			defer wg.Done()
			candidates, err := rs.Recall(ctx, strconv.FormatInt(req.AgentId, 10), 0)
			if err != nil {
				logger.Ctx(ctx).Warn("recall source failed", "source", rs.Name(), "err", err)
				return
			}
			recallResults[idx] = recallResult{candidates: candidates, name: rs.Name()}
		}(i, src)
	}

	// Build cache key for search results
	var cachedItems []cache.CachedItem
	var cacheKey string
	var searchResp *sortDal.SearchItemsResponse
	var err error

	if searchCache != nil && len(domains) > 0 {
		// Build cache key (excluding last_updated_at for better hit rate). Partition by
		// requester so the self-author ES filter doesn't poison the shared cache.
		cacheKey = searchCache.BuildCacheKey(domains, keywords, geo, req.AgentId)
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
				Limit:                cfg.KeywordRecallSize,
				Domains:              domains,
				Keywords:             keywords,
				Geo:                  geo,
				FreshnessOffset:      cfg.FreshnessOffset,
				FreshnessScale:       cfg.FreshnessScale,
				FreshnessDecay:       cfg.FreshnessDecay,
				ExcludeAuthorAgentID: req.AgentId,
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
					AuthorAgentID: item.AuthorAgentID,
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
					Embedding:     embcodec.Encode(item.Embedding),
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
			Limit:                cfg.KeywordRecallSize,
			Domains:              domains,
			Keywords:             keywords,
			Geo:                  geo,
			FreshnessOffset:      cfg.FreshnessOffset,
			FreshnessScale:       cfg.FreshnessScale,
			FreshnessDecay:       cfg.FreshnessDecay,
			ExcludeAuthorAgentID: req.AgentId,
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

	// Wait for kNN recall and merge with source tracking
	wg.Wait()

	sourceMap := make(map[int64]recallsource.Source, len(esItems))
	for _, item := range esItems {
		sourceMap[item.ID] |= recallsource.Keyword
	}

	if knnErr == nil && len(knnItems) > 0 {
		seen := make(map[int64]bool, len(esItems))
		for _, item := range esItems {
			seen[item.ID] = true
		}
		added := 0
		for _, item := range knnItems {
			sourceMap[item.ID] |= recallsource.KNN
			if !seen[item.ID] {
				esItems = append(esItems, item)
				seen[item.ID] = true
				added++
			}
		}
		logger.Ctx(ctx).Info("kNN merge", "knnTotal", len(knnItems), "newItems", added, "mergedTotal", len(esItems))
	}

	// Merge recall source candidates — collect IDs that need full item data from ES
	var newRecallIDs []int64
	{
		seen := make(map[int64]bool, len(esItems))
		for _, item := range esItems {
			seen[item.ID] = true
		}
		for _, rr := range recallResults {
			for _, c := range rr.candidates {
				sourceMap[c.ItemID] |= c.Source
				if !seen[c.ItemID] {
					newRecallIDs = append(newRecallIDs, c.ItemID)
					seen[c.ItemID] = true
				}
			}
		}
	}

	// Fetch full item data from ES for new recall IDs
	if len(newRecallIDs) > 0 {
		fetchedItems, fetchErr := sortDal.FetchItemsByIDs(ctx, newRecallIDs)
		if fetchErr != nil {
			logger.Ctx(ctx).Warn("failed to fetch recall items from ES", "err", fetchErr, "count", len(newRecallIDs))
		} else {
			esItems = append(esItems, fetchedItems...)
			logger.Ctx(ctx).Info("recall source merge", "newIDs", len(newRecallIDs), "fetched", len(fetchedItems), "mergedTotal", len(esItems))
		}
	}

	// Drop items authored by the requester so users never see their own posts in the feed.
	if req.AgentId != 0 {
		kept := esItems[:0]
		dropped := 0
		for _, item := range esItems {
			if item.AuthorAgentID == req.AgentId {
				delete(sourceMap, item.ID)
				dropped++
				continue
			}
			kept = append(kept, item)
		}
		if dropped > 0 {
			logger.Ctx(ctx).Debug("filtered self-authored items", "agentID", req.AgentId, "dropped", dropped, "remaining", len(kept))
		}
		esItems = kept
	}

	esItems = applyItemRerankPolicies(ctx, esItems, sourceMap)

	// Friend-feed recall lane: surface friends' recent broadcasts in the requester's
	// feed, bypassing the relevance threshold in the split below. Group-collapse and
	// bloom dedup still apply (so a friend post shows at most once per group), and
	// inactive friends who never fetch are not reached — best-effort, not guaranteed.
	if cfg.FriendFeedEnabled {
		var friendIDs []int64
		if err := db.DB.Table("user_relations").
			Where("from_uid = ? AND rel_type = ?", req.AgentId, 1).
			Limit(cfg.FriendFeedMaxAuthors).
			Pluck("to_uid", &friendIDs).Error; err != nil {
			logger.Ctx(ctx).Warn("friend recall: list friends failed", "err", err)
		} else if len(friendIDs) > 0 {
			friendItems, ferr := sortDal.FetchRecentItemsByAuthors(ctx, friendIDs, cfg.FriendFeedWindowHours, cfg.FriendFeedMaxItems)
			if ferr != nil {
				logger.Ctx(ctx).Warn("friend recall: fetch items failed", "err", ferr)
			} else {
				existing := make(map[int64]bool, len(esItems))
				for _, it := range esItems {
					existing[it.ID] = true
				}
				added := 0
				for _, it := range friendItems {
					sourceMap[it.ID] |= recallsource.Friend
					if !existing[it.ID] {
						esItems = append(esItems, it)
						existing[it.ID] = true
						added++
					}
				}
				logger.Ctx(ctx).Info("friend recall merge", "friends", len(friendIDs), "candidates", len(friendItems), "newItems", added)
			}
		}
	}

	// Resolve UGC/PGC content class once per request (author email suffix lookup);
	// reused by the recall/feed category metrics and the UGC boost.
	contentClassByItem := resolveContentClasses(ctx, esItems)

	// Record recall source feed composition and recall-stage category mix.
	for _, item := range esItems {
		for _, name := range recallsource.Names(sourceMap[item.ID]) {
			metrics.RecallFeedTotal.WithLabelValues(name).Inc()
		}
		recordRecallCategory(item, contentClassByItem[item.ID])
	}

	esItemMap := make(map[int64]sortDal.Item, len(esItems))
	for _, item := range esItems {
		esItemMap[item.ID] = item
	}

	// Rank all recall candidates — no pre-truncation so dedup draws from the full pool.
	// Collapse same-group candidates first so thresholding and final selection operate
	// on distinct groups instead of repeated near-duplicates.
	allRanked := rankerInstance.Rank(esItems, userProfile, len(esItems))
	// Operator boosts (supply/demand, UGC) run here — after ranking so the score
	// edits survive, before the threshold split so a boosted item can cross into
	// the served set.
	allRanked = applyPostRankBoost(ctx, allRanked, esItemMap, contentClassByItem)
	allRanked, collapsedCount := collapseRankedByGroup(allRanked, esItemMap)
	logger.Ctx(ctx).Debug("group collapse", "before", len(esItems), "after", len(allRanked), "filtered", collapsedCount)

	// Split ranked items into above-threshold (served) and below-threshold (filtered).
	// Filtered items are excluded from delivery but included in SortedItems for replay
	// log analysis so raising MinRelevanceScore doesn't reduce analysis sample size.
	ranked := make([]ranker.RankedItem, 0, len(allRanked))
	filteredItems := make([]ranker.RankedItem, 0)
	for _, ri := range allRanked {
		// Friend-feed and new-UGC guarantee items bypass the relevance threshold;
		// they still pass through group-collapse (above) and bloom dedup (below).
		if ri.Score >= rankerCfg.MinRelevanceScore ||
			sourceMap[ri.ItemID].Has(recallsource.Friend) ||
			sourceMap[ri.ItemID].Has(recallsource.NewUGC) {
			ranked = append(ranked, ri)
		} else {
			filteredItems = append(filteredItems, ri)
		}
	}
	logger.Ctx(ctx).Debug("relevance filter", "before", len(allRanked), "after", len(ranked), "filtered", len(filteredItems), "threshold", rankerCfg.MinRelevanceScore)

	// Build set of ranked IDs for exploration exclusion
	rankedIDs := make(map[int64]bool, len(ranked))
	rankedGroupIDs := make(map[int64]bool, len(ranked))
	for _, ri := range ranked {
		rankedIDs[ri.ItemID] = true
		if item, ok := esItemMap[ri.ItemID]; ok && item.GroupID != 0 {
			rankedGroupIDs[item.GroupID] = true
		}
	}

	// Add exploration slots from remaining candidates
	if rankerCfg.ExplorationSlots > 0 {
		explorationItems := ranker.PickExplorationItems(esItems, rankedIDs, rankedGroupIDs, rankerCfg.ExplorationSlots, 48*time.Hour, 0.5)
		for _, ei := range explorationItems {
			ranked = append(ranked, ranker.RankedItem{ItemID: ei.ID, Score: 0.0})
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

	for _, ri := range ranked {
		item, ok := esItemMap[ri.ItemID]
		if !ok {
			continue
		}
		feat := map[string]interface{}{
			"broadcast_type":      item.Type,
			"domains":             item.Domains,
			"keywords":            item.Keywords,
			"geo":                 item.Geo,
			"source_type":         item.SourceType,
			"quality_score":       item.QualityScore,
			"group_id":            item.GroupID,
			"lang":                item.Lang,
			"timeliness":          item.Timeliness,
			"updated_at":          item.UpdatedAt.UnixMilli(),
			"created_at":          item.CreatedAt.UnixMilli(),
			"rank_scores":         ri.Scores,
			"recall_source":       int(sourceMap[ri.ItemID]),
			"recall_source_names": recallsource.Names(sourceMap[ri.ItemID]),
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

	// Force-insert configured recall channels into reserved feed positions.
	// Injected candidates (e.g. un-exposed UGC from new_ugc_recall) bypass the
	// relevance threshold above; the generic InjectPolicy then pulls the
	// highest-scoring ones into the configured slots so a low-relevance
	// candidate still survives the top-N truncation below. Rules come from
	// configs/sort/rerank.yaml (name: inject); matching by recall-source label
	// keeps the policy channel-agnostic — a new channel is added purely in YAML.
	injectReasons := map[int64][]string{}
	injectClaimTTL := time.Duration(0)
	if injectRules := itemRerankPolicies.InjectRules(); len(injectRules) > 0 && len(candidates) > 0 {
		// Real-time claim filter: the offline recall index refreshes only
		// periodically, so a just-exposed item lingers in it until the next
		// refresh. Skip items already force-inserted (claimed) recently so each
		// is injected ~once, not into every feed across the whole refresh
		// window. Batch the check to one round trip over the injectable IDs.
		var injectableIDs []int64
		for _, c := range candidates {
			names := recallsource.Names(sourceMap[c.itemID])
			for _, rule := range injectRules {
				if slices.Contains(names, rule.Source) {
					injectableIDs = append(injectableIDs, c.itemID)
					break
				}
			}
		}
		claimed := fetchInjectClaims(ctx, mq.RDB, injectableIDs)

		rc := make([]rank.Candidate, len(candidates))
		for i, c := range candidates {
			rc[i] = rank.NewCandidate(c.itemID, rank.CandidateItem, c.score, nil, nil)
		}

		for _, rule := range injectRules {
			label := rule.Source
			if ttl, _ := rule.ParsedClaimTTL(); ttl > injectClaimTTL {
				injectClaimTTL = ttl
			}
			rc = (&rerank.InjectPolicy{
				Match: func(c rank.Candidate) bool {
					if claimed[c.ID()] {
						return false
					}
					return slices.Contains(recallsource.Names(sourceMap[c.ID()]), label)
				},
				Count:     rule.Count,
				Positions: rule.Positions,
			}).Apply(rc)
		}

		if len(rc) == len(candidates) {
			byID := make(map[int64]candidateItem, len(candidates))
			for _, c := range candidates {
				byID[c.itemID] = c
			}
			newCands := make([]candidateItem, 0, len(rc))
			for _, c := range rc {
				if ci, ok := byID[c.ID()]; ok {
					newCands = append(newCands, ci)
				}
				if bc, ok := c.(*rank.BasicCandidate); ok {
					if rs := bc.Reasons(); len(rs) > 0 {
						injectReasons[c.ID()] = rs
					}
				}
			}
			if len(newCands) == len(candidates) {
				candidates = newCands
			}
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

	// Filter and collect final item IDs (delivery list)
	itemIDs := make([]int64, 0, limit)
	sortedItems := make([]*sort.SortedItem, 0, limit+len(filteredItems))
	var deliveredInjected []int64
	dedupedCount := 0
	for _, c := range candidates {
		if c.groupID != 0 && seenGroupIDs[c.groupID] {
			dedupedCount++
			continue
		}
		itemIDs = append(itemIDs, c.itemID)
		if item, ok := esItemMap[c.itemID]; ok {
			recordFeedCategory(item, contentClassByItem[c.itemID])
		}
		agentFeatCopy := agentFeaturesStr
		itemFeatCopy := c.itemFeatures
		if rs, ok := injectReasons[c.itemID]; ok {
			itemFeatCopy = withRerankReasons(c.itemFeatures, rs)
			metrics.NewUGCInjectedTotal.Inc()
			deliveredInjected = append(deliveredInjected, c.itemID)
		}
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

	// Claim the items we actually force-inserted so the next feeds skip them
	// until the offline index catches up (see fetchInjectClaims). Claim only on
	// real delivery — an item dropped by dedup/limit should stay eligible.
	claimInjectedItems(ctx, mq.RDB, deliveredInjected, injectClaimTTL)

	logger.Ctx(ctx).Info("dedup result", "filtered", dedupedCount, "returned", len(itemIDs))

	// Record recall source impressions
	for _, id := range itemIDs {
		for _, name := range recallsource.Names(sourceMap[id]) {
			metrics.RecallImpressionTotal.WithLabelValues(name).Inc()
		}
	}

	// Append below-threshold items to SortedItems with "filtered" marker for replay log analysis.
	for _, ri := range filteredItems {
		item, ok := esItemMap[ri.ItemID]
		if !ok {
			continue
		}
		feat := map[string]interface{}{
			"broadcast_type":      item.Type,
			"domains":             item.Domains,
			"keywords":            item.Keywords,
			"geo":                 item.Geo,
			"source_type":         item.SourceType,
			"quality_score":       item.QualityScore,
			"group_id":            item.GroupID,
			"lang":                item.Lang,
			"timeliness":          item.Timeliness,
			"updated_at":          item.UpdatedAt.UnixMilli(),
			"created_at":          item.CreatedAt.UnixMilli(),
			"rank_scores":         ri.Scores,
			"recall_source":       int(sourceMap[ri.ItemID]),
			"recall_source_names": recallsource.Names(sourceMap[ri.ItemID]),
			"filtered":            true,
		}
		if item.ExpireTime != nil {
			feat["expire_time"] = item.ExpireTime.UnixMilli()
		}
		itemFeaturesJSON, _ := json.Marshal(feat)
		agentFeatCopy := agentFeaturesStr
		itemFeatCopy := string(itemFeaturesJSON)
		sortedItems = append(sortedItems, &sort.SortedItem{
			ItemId:        ri.ItemID,
			Score:         ri.Score,
			AgentFeatures: &agentFeatCopy,
			ItemFeatures:  &itemFeatCopy,
		})
	}

	// Calculate next cursor
	var nextCursor int64
	if searchResp != nil && !searchResp.NextCursor.IsZero() {
		nextCursor = searchResp.NextCursor.Unix()
	} else if len(cachedItems) > 0 && len(cachedItems) >= cfg.KeywordRecallSize {
		// If cache is full, use last item's timestamp as cursor
		nextCursor = cachedItems[len(cachedItems)-1].UpdatedAt
	}

	// Mix trading services into the top-N when enabled. The rerank chain
	// guarantees at least one service appears inside the limit window via
	// BoundsPolicy.Floor on the service type.
	if cfg.EnableServiceMix {
		// The served slice may contain pre-truncation + below-threshold replay
		// padding. Mix only over the served prefix (first len(itemIDs)
		// entries) and append the remaining replay-padded items unchanged.
		servedSorted := sortedItems
		var tail []*sort.SortedItem
		if len(sortedItems) > len(itemIDs) {
			servedSorted = sortedItems[:len(itemIDs)]
			tail = sortedItems[len(itemIDs):]
		}
		itemIDs, servedSorted = mixServicesIntoFeed(ctx, itemIDs, servedSorted, keywords, domains, agentFeaturesStr, limit, cfg.ServiceMixRecallSize)
		sortedItems = append(servedSorted, tail...)
	}

	return &sort.SortItemsResp{
		ItemIds:     itemIDs,
		NextCursor:  nextCursor,
		SortedItems: sortedItems,
		BaseResp:    &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}
