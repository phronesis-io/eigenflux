package main

import (
	"context"
	"errors"
	"strings"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/item"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/rpc/item/dal"

	"gorm.io/gorm"
)

type ItemServiceImpl struct {
	itemIDGen interface {
		NextID() (int64, error)
	}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func int64Ptr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func (s *ItemServiceImpl) PublishItem(ctx context.Context, req *item.PublishItemReq) (*item.PublishItemResp, error) {
	logger.FromContext(ctx).Info("PublishItem called", "agentID", req.AuthorAgentId)
	if s.itemIDGen == nil {
		return &item.PublishItemResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "item id generator is not initialized"},
		}, nil
	}
	itemID, genErr := s.itemIDGen.NextID()
	if genErr != nil {
		return &item.PublishItemResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to generate item id: " + genErr.Error()},
		}, nil
	}

	raw := &dal.RawItem{
		ItemID:        itemID,
		AuthorAgentID: req.AuthorAgentId,
		RawContent:    req.RawContent,
		RawNotes:      req.GetRawNotes(),
		RawURL:        req.GetRawUrl(),
	}
	if err := dal.CreateRawItem(db.DB, raw); err != nil {
		return &item.PublishItemResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()},
		}, nil
	}
	expectedResponse := ""
	if req.AcceptReply != nil && !*req.AcceptReply {
		expectedResponse = "no_reply"
	}
	pi := &dal.ProcessedItem{
		ItemID:           raw.ItemID,
		Status:           dal.StatusPending,
		ExpectedResponse: expectedResponse,
	}
	if err := dal.CreateProcessedItem(db.DB, pi); err != nil {
		return &item.PublishItemResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to create processed item: " + err.Error()},
		}, nil
	}

	// Create item stats record
	if err := dal.CreateItemStats(db.DB, raw.ItemID, req.AuthorAgentId); err != nil {
		logger.FromContext(ctx).Error("CreateItemStats error", "err", err)
	}

	return &item.PublishItemResp{
		ItemId:   raw.ItemID,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *ItemServiceImpl) FetchItems(ctx context.Context, req *item.FetchItemsReq) (*item.FetchItemsResp, error) {
	logger.FromContext(ctx).Debug("FetchItems called", "limit", req.GetLimit(), "lastItemID", req.GetLastItemId())
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	lastItemID := req.GetLastItemId()

	items, err := dal.GetLatestItems(db.DB, lastItemID, limit)
	if err != nil {
		return &item.FetchItemsResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()},
		}, nil
	}

	var respItems []*item.ProcessedItem
	var nextCursor int64
	for _, pi := range items {
		var keywords []string
		if pi.Keywords != "" {
			keywords = strings.Split(pi.Keywords, ",")
		}
		var domains []string
		if pi.Domains != "" {
			domains = strings.Split(pi.Domains, ",")
		}
		respItems = append(respItems, &item.ProcessedItem{
			ItemId:           pi.ItemID,
			Status:           int32(pi.Status),
			Summary:          strPtr(pi.Summary),
			BroadcastType:    pi.BroadcastType,
			Domains:          domains,
			Keywords:         keywords,
			ExpireTime:       strPtr(pi.ExpireTime),
			Geo:              strPtr(pi.Geo),
			SourceType:       strPtr(pi.SourceType),
			ExpectedResponse: strPtr(pi.ExpectedResponse),
			GroupId:          int64Ptr(pi.GroupID),
			UpdatedAt:        pi.UpdatedAt,
		})
		nextCursor = pi.ItemID
	}

	var suggestedActions []string
	if len(items) > 0 {
		suggestedActions = append(suggestedActions, "fetch_more")
	}

	return &item.FetchItemsResp{
		Items:            respItems,
		NextCursor:       nextCursor,
		SuggestedActions: suggestedActions,
		BaseResp:         &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *ItemServiceImpl) BatchGetItems(ctx context.Context, req *item.BatchGetItemsReq) (*item.BatchGetItemsResp, error) {
	logger.FromContext(ctx).Debug("BatchGetItems called", "count", len(req.ItemIds))
	items, err := dal.BatchGetProcessedItems(db.DB, req.ItemIds)
	if err != nil {
		return &item.BatchGetItemsResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()},
		}, nil
	}

	var respItems []*item.ProcessedItem
	for _, pi := range items {
		var keywords []string
		if pi.Keywords != "" {
			keywords = strings.Split(pi.Keywords, ",")
		}
		var domains []string
		if pi.Domains != "" {
			domains = strings.Split(pi.Domains, ",")
		}
		respItems = append(respItems, &item.ProcessedItem{
			ItemId:           pi.ItemID,
			Status:           int32(pi.Status),
			Summary:          strPtr(pi.Summary),
			BroadcastType:    pi.BroadcastType,
			Domains:          domains,
			Keywords:         keywords,
			ExpireTime:       strPtr(pi.ExpireTime),
			Geo:              strPtr(pi.Geo),
			SourceType:       strPtr(pi.SourceType),
			ExpectedResponse: strPtr(pi.ExpectedResponse),
			GroupId:          int64Ptr(pi.GroupID),
			UpdatedAt:        pi.UpdatedAt,
		})
	}

	return &item.BatchGetItemsResp{
		Items:    respItems,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *ItemServiceImpl) GetMyItems(ctx context.Context, req *item.GetMyItemsReq) (*item.GetMyItemsResp, error) {
	logger.FromContext(ctx).Debug("GetMyItems called", "agentID", req.AuthorAgentId)
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	lastItemID := req.GetLastItemId()
	items, err := dal.GetItemStatsByAuthor(db.DB, req.AuthorAgentId, lastItemID, limit)
	if err != nil {
		return &item.GetMyItemsResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()},
		}, nil
	}

	var respItems []*item.ItemWithStats
	var nextCursor int64
	for _, it := range items {
		respItems = append(respItems, &item.ItemWithStats{
			ItemId:            it.ItemID,
			RawContentPreview: it.RawContentPreview,
			Summary:           strPtr(it.Summary),
			BroadcastType:     it.BroadcastType,
			ConsumedCount:     it.ConsumedCount,
			ScoreNeg1Count:    it.ScoreNeg1Count,
			Score_1Count:      it.Score1Count,
			Score_2Count:      it.Score2Count,
			TotalScore:        it.TotalScore,
			UpdatedAt:         it.UpdatedAt,
		})
		nextCursor = it.ItemID
	}

	if len(items) == 0 {
		nextCursor = 0
	}

	return &item.GetMyItemsResp{
		Items:      respItems,
		NextCursor: nextCursor,
		BaseResp:   &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *ItemServiceImpl) DeleteMyItem(ctx context.Context, req *item.DeleteMyItemReq) (*item.DeleteMyItemResp, error) {
	logger.FromContext(ctx).Info("DeleteMyItem called", "itemID", req.ItemId, "agentID", req.AuthorAgentId)
	stats, err := dal.GetItemStatsByID(db.DB, req.ItemId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &item.DeleteMyItemResp{
				BaseResp: &base.BaseResp{Code: 404, Msg: "item not found"},
			}, nil
		}
		return &item.DeleteMyItemResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to look up item"},
		}, nil
	}
	if stats.AuthorAgentID != req.AuthorAgentId {
		return &item.DeleteMyItemResp{
			BaseResp: &base.BaseResp{Code: 403, Msg: "not authorized"},
		}, nil
	}
	if err := dal.UpdateProcessedItemStatus(db.DB, req.ItemId, dal.StatusDeleted); err != nil {
		return &item.DeleteMyItemResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()},
		}, nil
	}
	return &item.DeleteMyItemResp{
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}
