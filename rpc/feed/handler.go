package main

import (
	"context"
	"fmt"
	"log"

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

	log.Printf("[Feed] FetchFeed called: agent_id=%d, action=%s, limit=%d", req.AgentId, action, limit)

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
	log.Printf("[Feed] handleRefresh: agent_id=%d, limit=%d", agentID, limit)

	if err := s.feedCache.Clear(ctx, agentID); err != nil {
		log.Printf("[Feed] Failed to clear cache: %v", err)
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
		log.Printf("[Feed] SortService error: %v", err)
		return &feed.FetchFeedResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "sort service error: " + err.Error()},
		}, nil
	}
	if sortResp.BaseResp != nil && sortResp.BaseResp.Code != 0 {
		return &feed.FetchFeedResp{BaseResp: sortResp.BaseResp}, nil
	}

	log.Printf("[Feed] SortService returned %d items", len(sortResp.ItemIds))

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
		log.Printf("[Feed] ItemService error: %v", err)
		return &feed.FetchFeedResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "item service error: " + err.Error()},
		}, nil
	}
	if batchResp.BaseResp != nil && batchResp.BaseResp.Code != 0 {
		return &feed.FetchFeedResp{BaseResp: batchResp.BaseResp}, nil
	}

	log.Printf("[Feed] ItemService returned %d items", len(batchResp.Items))

	piByItemID := make(map[int64]*item.ProcessedItem, len(batchResp.Items))
	for _, pi := range batchResp.Items {
		piByItemID[pi.ItemId] = pi
	}

	groupIDs := make([]int64, 0, len(sortResp.ItemIds))
	itemMap := make(map[int64]*item.ProcessedItem)
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
		if err := s.feedCache.Push(ctx, agentID, toCache); err != nil {
			log.Printf("[Feed] Failed to cache items: %v", err)
		}
	}

	feedItems := s.buildFeedItems(toReturn, itemMap)

	go s.recordImpressions(context.Background(), agentID, feedItems)

	hasMore := len(toCache) > 0
	log.Printf("[Feed] Returning %d items, has_more=%v", len(feedItems), hasMore)

	return &feed.FetchFeedResp{
		Items:    feedItems,
		HasMore:  hasMore,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *FeedServiceImpl) handleLoadMore(ctx context.Context, agentID int64, limit int32) (*feed.FetchFeedResp, error) {
	log.Printf("[Feed] handleLoadMore: agent_id=%d, limit=%d", agentID, limit)

	cachedGroupIDs, err := s.feedCache.Pop(ctx, agentID, int(limit))
	if err != nil {
		log.Printf("[Feed] Failed to pop from cache: %v", err)
		return s.handleRefresh(ctx, agentID, limit)
	}

	if len(cachedGroupIDs) == 0 {
		log.Printf("[Feed] Cache empty, falling back to refresh")
		return s.handleRefresh(ctx, agentID, limit)
	}

	var itemIDs []int64
	for _, gid := range cachedGroupIDs {
		items, err := itemDal.GetItemsByGroupID(db.DB, gid)
		if err != nil || len(items) == 0 {
			log.Printf("[Feed] Failed to get items for group_id %d: %v", gid, err)
			continue
		}
		itemIDs = append(itemIDs, items[0].ItemID)
	}

	if len(itemIDs) == 0 {
		log.Printf("[Feed] No valid items found in cache, falling back to refresh")
		return s.handleRefresh(ctx, agentID, limit)
	}

	batchResp, err := itemClient.BatchGetItems(ctx, &item.BatchGetItemsReq{
		ItemIds: itemIDs,
	})
	if err != nil {
		log.Printf("[Feed] ItemService error: %v", err)
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

	go s.recordImpressions(context.Background(), agentID, feedItems)

	cacheLen, err := s.feedCache.Len(ctx, agentID)
	if err != nil {
		log.Printf("[Feed] Failed to get cache length: %v", err)
		cacheLen = 0
	}
	hasMore := cacheLen > 0

	log.Printf("[Feed] Returning %d items from cache, has_more=%v", len(feedItems), hasMore)

	return &feed.FetchFeedResp{
		Items:    feedItems,
		HasMore:  hasMore,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *FeedServiceImpl) buildFeedItems(groupIDs []int64, itemMap map[int64]*item.ProcessedItem) []*feed.FeedItem {
	var feedItems []*feed.FeedItem
	for _, gid := range groupIDs {
		pi, ok := itemMap[gid]
		if !ok {
			log.Printf("[Feed] Item for group_id %d not found in itemMap", gid)
			continue
		}

		feedItems = append(feedItems, &feed.FeedItem{
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
		})
	}
	return feedItems
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
			log.Printf("[Feed] Failed to add to bloom filter: %v", err)
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
		log.Printf("[Feed] Failed to record impressions: %v", err)
	}

	for _, fi := range feedItems {
		if _, err := itemstats.PublishConsumed(ctx, agentID, fi.ItemId); err != nil {
			log.Printf("[Feed] Failed to publish consumed stats event for item %d: %v", fi.ItemId, err)
		}
	}
}
