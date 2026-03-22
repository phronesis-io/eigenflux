package main

import (
	"context"
	"fmt"
	"log"
	"strconv"

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
	"eigenflux_server/pkg/milestone"
	"eigenflux_server/pkg/notification"
	itemDal "eigenflux_server/rpc/item/dal"
)

type FeedServiceImpl struct {
	bloomFilter  *bloomfilter.BloomFilter
	feedCache    *feedcache.FeedCache
	config       *config.Config
	milestoneSvc *milestone.Service
	notifSvc     *notification.Service
}

func NewFeedServiceImpl(cfg *config.Config, milestoneSvc *milestone.Service, notifSvc *notification.Service) *FeedServiceImpl {
	return &FeedServiceImpl{
		bloomFilter:  bloomfilter.NewBloomFilter(db.RDB),
		feedCache:    feedcache.NewFeedCache(db.RDB),
		config:       cfg,
		milestoneSvc: milestoneSvc,
		notifSvc:     notifSvc,
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
		resp, err := s.handleRefresh(ctx, req.AgentId, limit)
		if err != nil {
			return nil, err
		}
		s.attachNotifications(ctx, req.AgentId, resp, true)
		return resp, nil
	case "load_more":
		resp, err := s.handleLoadMore(ctx, req.AgentId, limit)
		if err != nil {
			return nil, err
		}
		s.attachNotifications(ctx, req.AgentId, resp, false)
		return resp, nil
	default:
		return &feed.FetchFeedResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: fmt.Sprintf("invalid action: %s", action)},
		}, nil
	}
}

func (s *FeedServiceImpl) AckNotifications(ctx context.Context, req *feed.AckNotificationsReq) (*feed.AckNotificationsResp, error) {
	if req == nil {
		return &feed.AckNotificationsResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "nil request"},
		}, nil
	}
	if len(req.Items) == 0 {
		return &feed.AckNotificationsResp{
			BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
		}, nil
	}

	// Separate by source type
	var milestoneIDs []int64
	var systemItems []notification.AckItem
	for _, item := range req.Items {
		if item == nil {
			continue
		}
		switch item.SourceType {
		case notification.SourceTypeMilestone:
			milestoneIDs = append(milestoneIDs, item.NotificationId)
		case notification.SourceTypeSystem:
			systemItems = append(systemItems, notification.AckItem{
				SourceType: notification.SourceTypeSystem,
				SourceID:   item.NotificationId,
			})
		default:
			log.Printf("[Feed] Unknown source_type in ack: %s", item.SourceType)
		}
	}

	// Ack milestone notifications
	if len(milestoneIDs) > 0 && s.milestoneSvc != nil {
		if err := s.milestoneSvc.MarkNotified(ctx, req.AgentId, milestoneIDs); err != nil {
			log.Printf("[Feed] Failed to ack milestone notifications for agent %d: %v", req.AgentId, err)
		}
	}

	// Ack system notifications via unified delivery table
	if len(systemItems) > 0 && s.notifSvc != nil {
		if err := s.notifSvc.AckNotifications(ctx, req.AgentId, systemItems); err != nil {
			log.Printf("[Feed] Failed to ack system notifications for agent %d: %v", req.AgentId, err)
		}
	}

	return &feed.AckNotificationsResp{
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *FeedServiceImpl) handleRefresh(ctx context.Context, agentID int64, limit int32) (*feed.FetchFeedResp, error) {
	log.Printf("[Feed] handleRefresh: agent_id=%d, limit=%d", agentID, limit)

	// Step 1: Clear old cache
	if err := s.feedCache.Clear(ctx, agentID); err != nil {
		log.Printf("[Feed] Failed to clear cache: %v", err)
		// Continue anyway, this is not critical
	}

	// Step 2: Fetch candidates from search engine (via SortService)
	// Fetch more than needed for deduplication
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
		log.Printf("[Feed] SortService returned error code: %d, msg: %s", sortResp.BaseResp.Code, sortResp.BaseResp.Msg)
		return &feed.FetchFeedResp{
			BaseResp: sortResp.BaseResp,
		}, nil
	}

	log.Printf("[Feed] SortService returned %d items", len(sortResp.ItemIds))

	if len(sortResp.ItemIds) == 0 {
		return &feed.FetchFeedResp{
			Items:    []*feed.FeedItem{},
			HasMore:  false,
			BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
		}, nil
	}

	// Step 3: Get full item details
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
		log.Printf("[Feed] ItemService returned error code: %d, msg: %s", batchResp.BaseResp.Code, batchResp.BaseResp.Msg)
		return &feed.FetchFeedResp{
			BaseResp: batchResp.BaseResp,
		}, nil
	}

	log.Printf("[Feed] ItemService returned %d items", len(batchResp.Items))

	// Step 4: Build group_id → item map, preserving SortService order
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

	// Step 5: Take first `limit` items and cache the rest
	var toReturn []int64
	var toCache []int64
	if len(groupIDs) <= int(limit) {
		toReturn = groupIDs
	} else {
		toReturn = groupIDs[:limit]
		toCache = groupIDs[limit:]
	}

	// Cache remaining items
	if len(toCache) > 0 {
		if err := s.feedCache.Push(ctx, agentID, toCache); err != nil {
			log.Printf("[Feed] Failed to cache items: %v", err)
			// Continue anyway
		}
	}

	// Step 6: Build feed items
	feedItems := s.buildFeedItems(toReturn, itemMap)

	// Step 7: Record impressions asynchronously
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

	// Step 1: Try to pop from cache
	cachedGroupIDs, err := s.feedCache.Pop(ctx, agentID, int(limit))
	if err != nil {
		log.Printf("[Feed] Failed to pop from cache: %v", err)
		// Fallback to refresh
		return s.handleRefresh(ctx, agentID, limit)
	}

	// Step 2: If cache is empty, fallback to refresh
	if len(cachedGroupIDs) == 0 {
		log.Printf("[Feed] Cache empty, falling back to refresh")
		return s.handleRefresh(ctx, agentID, limit)
	}

	// Step 3: Get full item details
	// First, get item IDs from group IDs
	var itemIDs []int64
	for _, gid := range cachedGroupIDs {
		// Query database to get item_id from group_id
		items, err := itemDal.GetItemsByGroupID(db.DB, gid)
		if err != nil || len(items) == 0 {
			log.Printf("[Feed] Failed to get items for group_id %d: %v", gid, err)
			continue
		}
		// Take the first item (representative of the group)
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
		log.Printf("[Feed] ItemService returned error code: %d, msg: %s", batchResp.BaseResp.Code, batchResp.BaseResp.Msg)
		return &feed.FetchFeedResp{
			BaseResp: batchResp.BaseResp,
		}, nil
	}

	// Step 4: Build item map
	itemMap := make(map[int64]*item.ProcessedItem)
	for _, pi := range batchResp.Items {
		if pi.GroupId != nil && *pi.GroupId != 0 {
			itemMap[*pi.GroupId] = pi
		}
	}

	// Step 5: Build feed items
	feedItems := s.buildFeedItems(cachedGroupIDs, itemMap)

	// Step 6: Record impressions asynchronously
	go s.recordImpressions(context.Background(), agentID, feedItems)

	// Step 7: Check if there are more items in cache
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

	// Record to impr (used for feedback validation)
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

	// Publish item stats events for consumed impressions
	for _, fi := range feedItems {
		if _, err := itemstats.PublishConsumed(ctx, agentID, fi.ItemId); err != nil {
			log.Printf("[Feed] Failed to publish consumed stats event for item %d: %v", fi.ItemId, err)
		}
	}
}

func (s *FeedServiceImpl) attachNotifications(ctx context.Context, agentID int64, resp *feed.FetchFeedResp, include bool) {
	if resp == nil || resp.BaseResp == nil || resp.BaseResp.Code != 0 {
		return
	}
	if !include {
		resp.Notifications = []*feed.Notification{}
		return
	}

	var rpcNotifications []*feed.Notification

	// Milestone notifications from Redis
	if s.milestoneSvc != nil {
		milestoneNotifs, err := s.milestoneSvc.ListNotifications(ctx, agentID)
		if err != nil {
			log.Printf("[Feed] Failed to list milestone notifications for agent %d: %v", agentID, err)
		} else {
			for _, n := range milestoneNotifs {
				eventID, err := strconv.ParseInt(n.NotificationID, 10, 64)
				if err != nil {
					log.Printf("[Feed] Invalid milestone notification id %q for agent %d: %v", n.NotificationID, agentID, err)
					continue
				}
				rpcNotifications = append(rpcNotifications, &feed.Notification{
					NotificationId: eventID,
					Type:           n.Type,
					Content:        n.Content,
					CreatedAt:      n.CreatedAt,
					SourceType:     notification.SourceTypeMilestone,
				})
			}
		}
	}

	// System notifications from Redis + DB delivery check
	if s.notifSvc != nil {
		sysNotifs, err := s.notifSvc.ListPendingSystemNotifications(ctx, agentID)
		if err != nil {
			log.Printf("[Feed] Failed to list system notifications for agent %d: %v", agentID, err)
		} else {
			for _, n := range sysNotifs {
				rpcNotifications = append(rpcNotifications, &feed.Notification{
					NotificationId: n.NotificationID,
					Type:           n.Type,
					Content:        n.Content,
					CreatedAt:      n.CreatedAt,
					SourceType:     notification.SourceTypeSystem,
				})
			}
		}
	}

	if rpcNotifications == nil {
		rpcNotifications = []*feed.Notification{}
	}
	resp.Notifications = rpcNotifications
}
