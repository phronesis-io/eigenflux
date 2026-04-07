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
	bloomFilter *bloomfilter.BloomFilter
	feedCache   *feedcache.FeedCache
	config      *config.Config
}

func NewFeedServiceImpl(cfg *config.Config) *FeedServiceImpl {
	return &FeedServiceImpl{
		bloomFilter: bloomfilter.NewBloomFilter(db.RDB),
		feedCache:   feedcache.NewFeedCache(db.RDB),
		config:      cfg,
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
			BaseResp: &base.BaseResp{Code: 400, Msg: fmt.Sprintf("invalid action: %s", action)},
		}, nil
	}
}

func (s *FeedServiceImpl) handleRefresh(ctx context.Context, agentID int64, limit int32) (*feed.FetchFeedResp, error) {
	logger.Ctx(ctx).Info("handleRefresh", "agentID", agentID, "limit", limit)

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
			BaseResp: &base.BaseResp{Code: 500, Msg: "sort service error: " + err.Error()},
		}, nil
	}
	if sortResp.BaseResp != nil && sortResp.BaseResp.Code != 0 {
		return &feed.FetchFeedResp{BaseResp: sortResp.BaseResp}, nil
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
			Items:    []*feed.FeedItem{},
			HasMore:  false,
			BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
		}, nil
	}

	batchResp, err := itemClient.BatchGetItems(ctx, &item.BatchGetItemsReq{
		ItemIds: sortResp.ItemIds,
	})
	if err != nil {
		logger.Ctx(ctx).Error("ItemService error", "err", err)
		return &feed.FetchFeedResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "item service error: " + err.Error()},
		}, nil
	}
	if batchResp.BaseResp != nil && batchResp.BaseResp.Code != 0 {
		return &feed.FetchFeedResp{BaseResp: batchResp.BaseResp}, nil
	}

	logger.Ctx(ctx).Info("ItemService returned items", "count", len(batchResp.Items))

	piByItemID := make(map[int64]*item.ProcessedItem, len(batchResp.Items))
	for _, pi := range batchResp.Items {
		piByItemID[pi.ItemId] = pi
	}

	groupIDs := make([]int64, 0, len(sortResp.ItemIds))
	itemMap := make(map[int64]*item.ProcessedItem)
	groupReplayLookup := make(map[int64]*replayData)
	for _, id := range sortResp.ItemIds {
		pi, ok := piByItemID[id]
		if !ok || pi.GroupId == nil || *pi.GroupId == 0 {
			continue
		}
		if _, dup := itemMap[*pi.GroupId]; dup {
			continue
		}
		groupIDs = append(groupIDs, *pi.GroupId)
		itemMap[*pi.GroupId] = pi
		if rd, ok := itemReplayLookup[id]; ok {
			groupReplayLookup[*pi.GroupId] = rd
		}
	}

	if len(groupIDs) == 0 {
		return &feed.FetchFeedResp{
			Items:    []*feed.FeedItem{},
			HasMore:  false,
			BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
		}, nil
	}

	var toReturn []int64
	var toCache []int64
	if len(groupIDs) <= int(limit) {
		toReturn = groupIDs
	} else {
		toReturn = groupIDs[:limit]
		toCache = groupIDs[limit:]
	}

	if len(toCache) > 0 {
		if err := s.feedCache.Push(ctx, agentID, s.buildFeedCacheEntries(toCache, groupReplayLookup)); err != nil {
			logger.Ctx(ctx).Warn("failed to cache items", "err", err)
		}
	}

	feedItems := s.buildFeedItems(toReturn, itemMap)

	go func() {
		bgCtx := context.Background()
		s.recordImpressions(bgCtx, agentID, feedItems)
		if s.config.EnableReplayLog && len(groupReplayLookup) > 0 {
			s.publishReplayLog(bgCtx, agentID, feedItems, groupReplayLookup)
		}
	}()

	hasMore := len(toCache) > 0
	logger.Ctx(ctx).Info("returning items", "count", len(feedItems), "hasMore", hasMore)

	return &feed.FetchFeedResp{
		Items:    feedItems,
		HasMore:  hasMore,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
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

	cachedGroupIDs := make([]int64, 0, len(cachedEntries))
	var itemIDs []int64
	for _, entry := range cachedEntries {
		cachedGroupIDs = append(cachedGroupIDs, entry.GroupID)
		items, err := itemDal.GetItemsByGroupID(db.DB, entry.GroupID)
		if err != nil || len(items) == 0 {
			logger.Ctx(ctx).Warn("failed to get items for group", "groupID", entry.GroupID, "err", err)
			continue
		}
		itemIDs = append(itemIDs, items[0].ItemID)
	}

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
			BaseResp: &base.BaseResp{Code: 500, Msg: "item service error: " + err.Error()},
		}, nil
	}
	if batchResp.BaseResp != nil && batchResp.BaseResp.Code != 0 {
		return &feed.FetchFeedResp{BaseResp: batchResp.BaseResp}, nil
	}

	itemMap := make(map[int64]*item.ProcessedItem)
	for _, pi := range batchResp.Items {
		if pi.GroupId != nil && *pi.GroupId != 0 {
			itemMap[*pi.GroupId] = pi
		}
	}

	feedItems := s.buildFeedItems(cachedGroupIDs, itemMap)

	go func() {
		bgCtx := context.Background()
		s.recordImpressions(bgCtx, agentID, feedItems)
		replayLookup := s.buildReplayLookupFromCacheEntries(cachedEntries)
		if s.config.EnableReplayLog && len(replayLookup) > 0 {
			s.publishReplayLog(bgCtx, agentID, feedItems, replayLookup)
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
		Items:    feedItems,
		HasMore:  hasMore,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
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
	score         float64
	agentFeatures string
	itemFeatures  string
}

func (s *FeedServiceImpl) buildFeedCacheEntries(groupIDs []int64, lookup map[int64]*replayData) []feedcache.Entry {
	entries := make([]feedcache.Entry, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		entry := feedcache.Entry{GroupID: groupID}
		if rd, ok := lookup[groupID]; ok {
			entry.Score = rd.score
			entry.AgentFeatures = rd.agentFeatures
			entry.ItemFeatures = rd.itemFeatures
		}
		entries = append(entries, entry)
	}
	return entries
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
			score:         entry.Score,
			agentFeatures: entry.AgentFeatures,
			itemFeatures:  entry.ItemFeatures,
		}
	}
	return lookup
}

func (s *FeedServiceImpl) publishReplayLog(ctx context.Context, agentID int64, feedItems []*feed.FeedItem, lookup map[int64]*replayData) {
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
	for i, fi := range feedItems {
		si := replaylog.ServedItem{
			ItemID:   fi.ItemId,
			Position: i,
		}
		if fi.GroupId != nil {
			if rd, ok := lookup[*fi.GroupId]; ok {
				si.Score = rd.score
				si.ItemFeatures = rd.itemFeatures
			}
		}
		if si.ItemFeatures == "" {
			si.ItemFeatures = "{}"
		}
		served = append(served, si)
	}

	if err := replaylog.Publish(ctx, agentID, agentFeatures, served); err != nil {
		logger.Default().Warn("failed to publish replay log", "err", err)
	}
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
