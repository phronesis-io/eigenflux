package main

import (
	"context"
	"fmt"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/feed"
	"eigenflux_server/kitex_gen/eigenflux/item"
	"eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/feedcache"
	"eigenflux_server/pkg/impr"
	"eigenflux_server/pkg/itemstats"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/replaylog"
	itemDal "eigenflux_server/rpc/item/dal"
)

type FeedServiceImpl struct {
	bloomFilter     *bloomfilter.BloomFilter
	feedCache       *feedcache.FeedCache
	config          *config.Config
	impressionIDGen interface {
		NextID() (int64, error)
	}
}

func NewFeedServiceImpl(cfg *config.Config, impressionIDGen interface {
	NextID() (int64, error)
}) *FeedServiceImpl {
	return &FeedServiceImpl{
		bloomFilter:     bloomfilter.NewBloomFilter(db.RDB),
		feedCache:       feedcache.NewFeedCache(db.RDB),
		config:          cfg,
		impressionIDGen: impressionIDGen,
	}
}

func int32Ptr(i int32) *int32 {
	return &i
}

func (s *FeedServiceImpl) FetchFeed(ctx context.Context, req *feed.FetchFeedReq) (*feed.FetchFeedResp, error) {
	limit := req.GetLimit()
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	action := req.GetAction()
	if action == "" {
		action = "refresh"
	}

	logger.Ctx(ctx).Info("FetchFeed called", "agentID", req.AgentId, "action", action, "limit", limit)

	switch action {
	case "refresh":
		return s.handleRefresh(ctx, req.AgentId, limit)
	case "load_more":
		return s.handleLoadMore(ctx, req.AgentId, limit)
	default:
		return &feed.FetchFeedResp{
			Items:        []*feed.FeedItem{},
			HasMore:      false,
			ImpressionId: "",
			BaseResp:     &base.BaseResp{Code: 400, Msg: fmt.Sprintf("invalid action: %s", action)},
		}, nil
	}
}

func (s *FeedServiceImpl) handleRefresh(ctx context.Context, agentID int64, limit int32) (*feed.FetchFeedResp, error) {
	logger.Ctx(ctx).Info("handleRefresh", "agentID", agentID, "limit", limit)

	impressionID, err := s.newImpressionID()
	if err != nil {
		logger.Ctx(ctx).Error("failed to generate impression id", "err", err)
		return &feed.FetchFeedResp{
			Items:        []*feed.FeedItem{},
			HasMore:      false,
			ImpressionId: "",
			BaseResp:     &base.BaseResp{Code: 500, Msg: "failed to generate impression id"},
		}, nil
	}

	if err := s.feedCache.Clear(ctx, agentID); err != nil {
		logger.Ctx(ctx).Warn("failed to clear cache", "err", err)
	}

	fetchLimit := limit * 10
	if fetchLimit > 500 {
		fetchLimit = 500
	}

	sortResp, err := sortClient.SortItems(ctx, &sort.SortItemsReq{
		AgentId: agentID,
		Limit:   int32Ptr(fetchLimit),
	})
	if err != nil {
		logger.Ctx(ctx).Error("SortService error", "err", err)
		return &feed.FetchFeedResp{
			Items:        []*feed.FeedItem{},
			HasMore:      false,
			ImpressionId: "",
			BaseResp:     &base.BaseResp{Code: 500, Msg: "sort service error: " + err.Error()},
		}, nil
	}
	if sortResp.BaseResp != nil && sortResp.BaseResp.Code != 0 {
		return &feed.FetchFeedResp{
			Items:        []*feed.FeedItem{},
			HasMore:      false,
			ImpressionId: "",
			BaseResp:     sortResp.BaseResp,
		}, nil
	}

	logger.Ctx(ctx).Info("SortService returned items", "count", len(sortResp.ItemIds))

	// Build replay data lookup from sorted_items keyed by the ranked item_id first.
	itemReplayLookup := make(map[int64]*replayData)
	if sortResp.SortedItems != nil {
		for _, si := range sortResp.SortedItems {
			rd := &replayData{score: si.Score}
			if si.AgentFeatures != nil {
				rd.agentFeatures = *si.AgentFeatures
			}
			if si.ItemFeatures != nil {
				rd.itemFeatures = *si.ItemFeatures
			}
			itemReplayLookup[si.ItemId] = rd
		}
	}

	if len(sortResp.ItemIds) == 0 {
		return &feed.FetchFeedResp{
			Items:        []*feed.FeedItem{},
			HasMore:      false,
			ImpressionId: impressionID,
			BaseResp:     &base.BaseResp{Code: 0, Msg: "success"},
		}, nil
	}

	batchResp, err := itemClient.BatchGetItems(ctx, &item.BatchGetItemsReq{
		ItemIds: sortResp.ItemIds,
	})
	if err != nil {
		logger.Ctx(ctx).Error("ItemService error", "err", err)
		return &feed.FetchFeedResp{
			Items:        []*feed.FeedItem{},
			HasMore:      false,
			ImpressionId: "",
			BaseResp:     &base.BaseResp{Code: 500, Msg: "item service error: " + err.Error()},
		}, nil
	}
	if batchResp.BaseResp != nil && batchResp.BaseResp.Code != 0 {
		return &feed.FetchFeedResp{
			Items:        []*feed.FeedItem{},
			HasMore:      false,
			ImpressionId: "",
			BaseResp:     batchResp.BaseResp,
		}, nil
	}

	logger.Ctx(ctx).Info("ItemService returned items", "count", len(batchResp.Items))

	piByItemID := make(map[int64]*item.ProcessedItem, len(batchResp.Items))
	for _, pi := range batchResp.Items {
		piByItemID[pi.ItemId] = pi
	}

	rankedEntries := make([]feedcache.Entry, 0, len(sortResp.ItemIds))
	itemMap := make(map[int64]*item.ProcessedItem)
	for _, id := range sortResp.ItemIds {
		pi, ok := piByItemID[id]
		if !ok || pi.GroupId == nil || *pi.GroupId == 0 {
			continue
		}
		if _, dup := itemMap[*pi.GroupId]; dup {
			continue
		}
		itemMap[*pi.GroupId] = pi
		entry := feedcache.Entry{
			GroupID:      *pi.GroupId,
			ItemID:       id,
			Position:     len(rankedEntries),
			ImpressionID: impressionID,
		}
		if rd, ok := itemReplayLookup[id]; ok {
			entry.Score = rd.score
			entry.AgentFeatures = rd.agentFeatures
			entry.ItemFeatures = rd.itemFeatures
		}
		rankedEntries = append(rankedEntries, entry)
	}

	if len(rankedEntries) == 0 {
		return &feed.FetchFeedResp{
			Items:        []*feed.FeedItem{},
			HasMore:      false,
			ImpressionId: impressionID,
			BaseResp:     &base.BaseResp{Code: 0, Msg: "success"},
		}, nil
	}

	var toReturn []feedcache.Entry
	var toCache []feedcache.Entry
	if len(rankedEntries) <= int(limit) {
		toReturn = rankedEntries
	} else {
		toReturn = rankedEntries[:limit]
		toCache = rankedEntries[limit:]
	}

	if len(toCache) > 0 {
		if err := s.feedCache.Push(ctx, agentID, toCache); err != nil {
			logger.Ctx(ctx).Warn("failed to cache items", "err", err)
		}
	}

	feedItems := s.buildFeedItems(groupIDsFromCacheEntries(toReturn), itemMap)
	toReturnReplayLookup := s.buildReplayLookupFromCacheEntries(toReturn)

	// Collect filtered SortedItems (present in SortedItems but not in ItemIds) for replay log.
	var filteredSortedItems []*sort.SortedItem
	if s.config.EnableReplayLog && sortResp.SortedItems != nil {
		servedIDs := make(map[int64]bool, len(sortResp.ItemIds))
		for _, id := range sortResp.ItemIds {
			servedIDs[id] = true
		}
		for _, si := range sortResp.SortedItems {
			if !servedIDs[si.ItemId] {
				filteredSortedItems = append(filteredSortedItems, si)
			}
		}
	}

	go func() {
		bgCtx := context.Background()
		s.recordImpressions(bgCtx, agentID, feedItems)
		if s.config.EnableReplayLog && len(toReturnReplayLookup) > 0 {
			s.publishReplayLog(bgCtx, impressionID, agentID, feedItems, toReturnReplayLookup)
		}
		if len(filteredSortedItems) > 0 {
			s.publishFilteredReplayLog(bgCtx, impressionID, agentID, len(feedItems), filteredSortedItems)
		}
	}()

	hasMore := len(toCache) > 0
	logger.Ctx(ctx).Info("returning items", "count", len(feedItems), "hasMore", hasMore, "filteredForAnalysis", len(filteredSortedItems))

	return &feed.FetchFeedResp{
		Items:        feedItems,
		HasMore:      hasMore,
		ImpressionId: impressionID,
		BaseResp:     &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *FeedServiceImpl) handleLoadMore(ctx context.Context, agentID int64, limit int32) (*feed.FetchFeedResp, error) {
	logger.Ctx(ctx).Info("handleLoadMore", "agentID", agentID, "limit", limit)

	cachedEntries, err := s.feedCache.Pop(ctx, agentID, int(limit))
	if err != nil {
		logger.Ctx(ctx).Warn("failed to pop from cache", "err", err)
		return s.handleRefresh(ctx, agentID, limit)
	}

	if len(cachedEntries) == 0 {
		logger.Ctx(ctx).Info("cache empty, falling back to refresh")
		return s.handleRefresh(ctx, agentID, limit)
	}

	resolvedEntries := make([]feedcache.Entry, 0, len(cachedEntries))
	for _, entry := range cachedEntries {
		if entry.GroupID == 0 {
			continue
		}
		if entry.ItemID != 0 {
			resolvedEntries = append(resolvedEntries, entry)
			continue
		}

		items, err := itemDal.GetItemsByGroupID(db.DB, entry.GroupID)
		if err != nil || len(items) == 0 {
			logger.Ctx(ctx).Warn("failed to get items for group", "groupID", entry.GroupID, "err", err)
			continue
		}
		entry.ItemID = items[0].ItemID
		resolvedEntries = append(resolvedEntries, entry)
	}

	impressionID := impressionIDFromCacheEntries(resolvedEntries)
	if impressionID == "" {
		logger.Ctx(ctx).Info("cached entries missing impression id, falling back to refresh")
		return s.handleRefresh(ctx, agentID, limit)
	}

	itemIDs := itemIDsFromCacheEntries(resolvedEntries)
	if len(itemIDs) == 0 {
		logger.Ctx(ctx).Info("no valid items found in cache, falling back to refresh")
		return s.handleRefresh(ctx, agentID, limit)
	}

	batchResp, err := itemClient.BatchGetItems(ctx, &item.BatchGetItemsReq{
		ItemIds: itemIDs,
	})
	if err != nil {
		logger.Ctx(ctx).Error("ItemService error", "err", err)
		return &feed.FetchFeedResp{
			Items:        []*feed.FeedItem{},
			HasMore:      false,
			ImpressionId: "",
			BaseResp:     &base.BaseResp{Code: 500, Msg: "item service error: " + err.Error()},
		}, nil
	}
	if batchResp.BaseResp != nil && batchResp.BaseResp.Code != 0 {
		return &feed.FetchFeedResp{
			Items:        []*feed.FeedItem{},
			HasMore:      false,
			ImpressionId: "",
			BaseResp:     batchResp.BaseResp,
		}, nil
	}

	itemMap := make(map[int64]*item.ProcessedItem)
	for _, pi := range batchResp.Items {
		if pi.GroupId != nil && *pi.GroupId != 0 {
			itemMap[*pi.GroupId] = pi
		}
	}

	feedItems := s.buildFeedItems(groupIDsFromCacheEntries(resolvedEntries), itemMap)

	go func() {
		bgCtx := context.Background()
		s.recordImpressions(bgCtx, agentID, feedItems)
		replayLookup := s.buildReplayLookupFromCacheEntries(resolvedEntries)
		if s.config.EnableReplayLog && len(replayLookup) > 0 {
			s.publishReplayLog(bgCtx, impressionID, agentID, feedItems, replayLookup)
		}
	}()

	cacheLen, err := s.feedCache.Len(ctx, agentID)
	if err != nil {
		logger.Ctx(ctx).Warn("failed to get cache length", "err", err)
		cacheLen = 0
	}
	hasMore := cacheLen > 0

	logger.Ctx(ctx).Info("returning items from cache", "count", len(feedItems), "hasMore", hasMore)

	return &feed.FetchFeedResp{
		Items:        feedItems,
		HasMore:      hasMore,
		ImpressionId: impressionID,
		BaseResp:     &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *FeedServiceImpl) buildFeedItems(groupIDs []int64, itemMap map[int64]*item.ProcessedItem) []*feed.FeedItem {
	var feedItems []*feed.FeedItem

	// Collect item IDs for batch author lookup
	itemIDs := make([]int64, 0, len(groupIDs))
	for _, gid := range groupIDs {
		if pi, ok := itemMap[gid]; ok {
			itemIDs = append(itemIDs, pi.ItemId)
		}
	}

	// Batch get author IDs
	authorMap, err := itemDal.BatchGetRawItemAuthors(db.DB, itemIDs)
	if err != nil {
		logger.Default().Warn("failed to batch get authors", "err", err)
		authorMap = make(map[int64]int64) // Continue with empty map
	}

	for _, gid := range groupIDs {
		pi, ok := itemMap[gid]
		if !ok {
			logger.Default().Warn("item for group not found in itemMap", "groupID", gid)
			continue
		}

		feedItem := &feed.FeedItem{
			ItemId:           pi.ItemId,
			Summary:          pi.Summary,
			BroadcastType:    pi.BroadcastType,
			Domains:          pi.Domains,
			Keywords:         pi.Keywords,
			ExpireTime:       pi.ExpireTime,
			Geo:              pi.Geo,
			SourceType:       pi.SourceType,
			ExpectedResponse: pi.ExpectedResponse,
			GroupId:          pi.GroupId,
			UpdatedAt:        pi.UpdatedAt,
		}

		// Add author_agent_id if found
		if authorID, found := authorMap[pi.ItemId]; found {
			feedItem.AuthorAgentId = &authorID
		}

		feedItems = append(feedItems, feedItem)
	}
	return feedItems
}

type replayData struct {
	position      int
	score         float64
	impressionID  string
	agentFeatures string
	itemFeatures  string
}

func (s *FeedServiceImpl) buildReplayLookupFromCacheEntries(entries []feedcache.Entry) map[int64]*replayData {
	lookup := make(map[int64]*replayData, len(entries))
	for _, entry := range entries {
		if entry.GroupID == 0 {
			continue
		}
		if entry.AgentFeatures == "" && entry.ItemFeatures == "" && entry.Score == 0 {
			continue
		}
		lookup[entry.GroupID] = &replayData{
			position:      entry.Position,
			score:         entry.Score,
			impressionID:  entry.ImpressionID,
			agentFeatures: entry.AgentFeatures,
			itemFeatures:  entry.ItemFeatures,
		}
	}
	return lookup
}

func groupIDsFromCacheEntries(entries []feedcache.Entry) []int64 {
	groupIDs := make([]int64, 0, len(entries))
	for _, entry := range entries {
		if entry.GroupID != 0 {
			groupIDs = append(groupIDs, entry.GroupID)
		}
	}
	return groupIDs
}

func itemIDsFromCacheEntries(entries []feedcache.Entry) []int64 {
	itemIDs := make([]int64, 0, len(entries))
	for _, entry := range entries {
		if entry.ItemID != 0 {
			itemIDs = append(itemIDs, entry.ItemID)
		}
	}
	return itemIDs
}

func impressionIDFromCacheEntries(entries []feedcache.Entry) string {
	for _, entry := range entries {
		if entry.ImpressionID != "" {
			return entry.ImpressionID
		}
	}
	return ""
}

func (s *FeedServiceImpl) publishReplayLog(ctx context.Context, impressionID string, agentID int64, feedItems []*feed.FeedItem, lookup map[int64]*replayData) {
	if len(feedItems) == 0 {
		return
	}

	var agentFeatures string
	for _, fi := range feedItems {
		if fi.GroupId == nil || *fi.GroupId == 0 {
			continue
		}
		if rd, ok := lookup[*fi.GroupId]; ok && rd.agentFeatures != "" {
			agentFeatures = rd.agentFeatures
			break
		}
	}

	served := make([]replaylog.ServedItem, 0, len(feedItems))
	for _, fi := range feedItems {
		si := replaylog.ServedItem{
			ItemID: fi.ItemId,
		}
		if fi.GroupId != nil {
			if rd, ok := lookup[*fi.GroupId]; ok {
				si.Position = rd.position
				si.Score = rd.score
				si.ItemFeatures = rd.itemFeatures
			}
		}
		if si.ItemFeatures == "" {
			si.ItemFeatures = "{}"
		}
		served = append(served, si)
	}

	if err := replaylog.Publish(ctx, impressionID, agentID, agentFeatures, served); err != nil {
		logger.Default().Warn("failed to publish replay log", "err", err)
	}
}

// publishFilteredReplayLog publishes below-threshold items to the replay log for
// offline analysis. These items were scored but not delivered to the user.
// positionOffset ensures positions don't collide with served items in the same impression.
func (s *FeedServiceImpl) publishFilteredReplayLog(ctx context.Context, impressionID string, agentID int64, positionOffset int, items []*sort.SortedItem) {
	if len(items) == 0 {
		return
	}

	var agentFeatures string
	for _, si := range items {
		if si.AgentFeatures != nil && *si.AgentFeatures != "" {
			agentFeatures = *si.AgentFeatures
			break
		}
	}

	served := make([]replaylog.ServedItem, 0, len(items))
	for i, si := range items {
		itemFeatures := "{}"
		if si.ItemFeatures != nil {
			itemFeatures = *si.ItemFeatures
		}
		served = append(served, replaylog.ServedItem{
			ItemID:       si.ItemId,
			Score:        si.Score,
			Position:     positionOffset + i,
			ItemFeatures: itemFeatures,
		})
	}

	if err := replaylog.Publish(ctx, impressionID, agentID, agentFeatures, served); err != nil {
		logger.Default().Warn("failed to publish filtered replay log", "err", err)
	}
}

func (s *FeedServiceImpl) newImpressionID() (string, error) {
	if s.impressionIDGen == nil {
		return "", fmt.Errorf("impression id generator is not initialized")
	}
	id, err := s.impressionIDGen.NextID()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("imp_%d", id), nil
}

func (s *FeedServiceImpl) recordImpressions(ctx context.Context, agentID int64, feedItems []*feed.FeedItem) {
	if len(feedItems) == 0 {
		return
	}

	bfGroupIDs := make([]int64, 0, len(feedItems))
	for _, fi := range feedItems {
		if fi.GroupId != nil && *fi.GroupId != 0 {
			bfGroupIDs = append(bfGroupIDs, *fi.GroupId)
		}
	}

	if len(bfGroupIDs) > 0 {
		if err := s.bloomFilter.Add(ctx, agentID, bfGroupIDs); err != nil {
			logger.Ctx(ctx).Warn("failed to add to bloom filter", "err", err)
		}
	}

	imprItems := make([]impr.ImprItem, 0, len(feedItems))
	for _, fi := range feedItems {
		ii := impr.ImprItem{ItemID: fi.ItemId}
		if fi.GroupId != nil && *fi.GroupId != 0 {
			ii.GroupID = *fi.GroupId
		}
		imprItems = append(imprItems, ii)
	}
	if err := impr.RecordImpressions(ctx, db.RDB, agentID, imprItems); err != nil {
		logger.Ctx(ctx).Warn("failed to record impressions", "err", err)
	}

	for _, fi := range feedItems {
		if _, err := itemstats.PublishConsumed(ctx, agentID, fi.ItemId); err != nil {
			logger.Ctx(ctx).Warn("failed to publish consumed stats event", "itemID", fi.ItemId, "err", err)
		}
	}
}
