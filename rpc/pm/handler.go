package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/pm"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/rpc/pm/dal"
	"eigenflux_server/rpc/pm/icebreak"
	"eigenflux_server/rpc/pm/validator"

	"gorm.io/gorm"
)

type PMServiceImpl struct {
	convIDGen interface {
		NextID() (int64, error)
	}
	msgIDGen interface {
		NextID() (int64, error)
	}
	iceBreaker *icebreak.IceBreaker
	validator  *validator.Validator
}

func (s *PMServiceImpl) SendPM(ctx context.Context, req *pm.SendPMReq) (*pm.SendPMResp, error) {
	// Case 1: New conversation (item_id provided)
	if req.ItemId != nil && *req.ItemId > 0 {
		return s.handleNewConversation(ctx, req)
	}

	// Case 2: Reply (conv_id provided)
	if req.ConvId != nil && *req.ConvId > 0 {
		return s.handleReply(ctx, req)
	}

	return &pm.SendPMResp{
		BaseResp: &base.BaseResp{Code: 400, Msg: "either item_id or conv_id must be provided"},
	}, nil
}

func (s *PMServiceImpl) handleNewConversation(ctx context.Context, req *pm.SendPMReq) (*pm.SendPMResp, error) {
	itemID := *req.ItemId

	// Validate item ownership
	if err := s.validator.ValidateItemOwnership(ctx, itemID, req.ReceiverId); err != nil {
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: err.Error()},
		}, nil
	}

	// Check no_reply
	if err := s.validator.ValidateNoReply(ctx, itemID); err != nil {
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 403, Msg: err.Error()},
		}, nil
	}

	// Compute participant pair
	participantA, participantB := req.SenderId, req.ReceiverId
	if participantA > participantB {
		participantA, participantB = participantB, participantA
	}

	// Check if conversation already exists
	existingConvID, exists, err := s.validator.GetOrCreateConvID(ctx, participantA, participantB, itemID)
	if err != nil {
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to check existing conversation"},
		}, nil
	}

	if exists {
		// Treat as reply
		req.ConvId = &existingConvID
		return s.handleReply(ctx, req)
	}

	// Generate IDs
	convID, err := s.convIDGen.NextID()
	if err != nil {
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to generate conv_id"},
		}, nil
	}

	msgID, err := s.msgIDGen.NextID()
	if err != nil {
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to generate msg_id"},
		}, nil
	}

	// Lookup agent names
	nameMap, _ := dal.BatchGetAgentNames(db.DB, []int64{req.SenderId, req.ReceiverId})
	senderName := nameMap[req.SenderId]
	receiverName := nameMap[req.ReceiverId]

	// Map names to participant slots (A < B)
	nameA, nameB := senderName, receiverName
	if participantA == req.ReceiverId {
		nameA, nameB = receiverName, senderName
	}

	// DB transaction
	err = db.DB.Transaction(func(tx *gorm.DB) error {
		// Create conversation
		conv := &dal.Conversation{
			ConvID:           convID,
			ParticipantA:     participantA,
			ParticipantB:     participantB,
			InitiatorID:      req.SenderId,
			LastSenderID:     req.SenderId,
			OriginType:       "broadcast",
			OriginID:         itemID,
			MsgCount:         1,
			Status:           0,
			ParticipantAName: nameA,
			ParticipantBName: nameB,
		}
		if err := dal.CreateConversation(tx, conv); err != nil {
			return err
		}

		// Create message
		msg := &dal.PrivateMessage{
			MsgID:        msgID,
			ConvID:       convID,
			SenderID:     req.SenderId,
			ReceiverID:   req.ReceiverId,
			Content:      req.Content,
			IsRead:       false,
			SenderName:   senderName,
			ReceiverName: receiverName,
		}
		return dal.CreateMessage(tx, msg)
	})

	if err != nil {
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to create conversation: " + err.Error()},
		}, nil
	}

	// Post-commit: cache mapping, ice break, invalidate fetch cache
	_ = s.validator.CacheConvMapping(ctx, participantA, participantB, itemID, convID)
	_, _, _ = s.iceBreaker.CheckAndSetIceBreak(ctx, convID, req.SenderId)
	db.RDB.Del(ctx, fmt.Sprintf("pm:fetch:%d", req.ReceiverId))

	logger.FromContext(ctx).Info("new conversation", "convID", convID, "msgID", msgID, "senderID", req.SenderId, "receiverID", req.ReceiverId, "itemID", itemID)

	return &pm.SendPMResp{
		MsgId:    msgID,
		ConvId:   convID,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *PMServiceImpl) handleReply(ctx context.Context, req *pm.SendPMReq) (*pm.SendPMResp, error) {
	convID := *req.ConvId

	// Validate conversation membership
	receiverID, err := s.validator.ValidateConvMembership(ctx, convID, req.SenderId)
	if err != nil {
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 403, Msg: err.Error()},
		}, nil
	}

	// Ice break check
	iceStatus, lastSenderID, err := s.iceBreaker.CheckAndSetIceBreak(ctx, convID, req.SenderId)
	if err != nil {
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "ice break check failed"},
		}, nil
	}

	if iceStatus == icebreak.IceStatusFirstMsg && lastSenderID == req.SenderId {
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 429, Msg: "waiting for reply from the receiver"},
		}, nil
	}

	// Generate message ID
	msgID, err := s.msgIDGen.NextID()
	if err != nil {
		_ = s.iceBreaker.RollbackIceBreak(ctx, convID, iceStatus)
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to generate msg_id"},
		}, nil
	}

	// Lookup agent names
	nameMap, _ := dal.BatchGetAgentNames(db.DB, []int64{req.SenderId, receiverID})
	senderName := nameMap[req.SenderId]
	receiverName := nameMap[receiverID]

	// DB transaction
	err = db.DB.Transaction(func(tx *gorm.DB) error {
		// Create message
		msg := &dal.PrivateMessage{
			MsgID:        msgID,
			ConvID:       convID,
			SenderID:     req.SenderId,
			ReceiverID:   receiverID,
			Content:      req.Content,
			IsRead:       false,
			SenderName:   senderName,
			ReceiverName: receiverName,
		}
		if err := dal.CreateMessage(tx, msg); err != nil {
			return err
		}

		// Update conversation
		return dal.UpdateConversationAfterMessage(tx, convID, req.SenderId)
	})

	if err != nil {
		_ = s.iceBreaker.RollbackIceBreak(ctx, convID, iceStatus)
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to send message: " + err.Error()},
		}, nil
	}

	// Post-commit: invalidate fetch cache
	db.RDB.Del(ctx, fmt.Sprintf("pm:fetch:%d", receiverID))

	logger.FromContext(ctx).Info("reply sent", "convID", convID, "msgID", msgID, "senderID", req.SenderId, "receiverID", receiverID)

	return &pm.SendPMResp{
		MsgId:    msgID,
		ConvId:   convID,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *PMServiceImpl) FetchPM(ctx context.Context, req *pm.FetchPMReq) (*pm.FetchPMResp, error) {
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	cursor := req.GetCursor()

	// For cursor=0 (polling case), try Redis cache first
	if cursor == 0 {
		cacheKey := fmt.Sprintf("pm:fetch:%d", req.AgentId)
		cached, err := db.RDB.Get(ctx, cacheKey).Bytes()
		if err == nil {
			var resp pm.FetchPMResp
			if json.Unmarshal(cached, &resp) == nil {
				return &resp, nil
			}
		}
	}

	// Fetch unread messages from DB
	messages, err := dal.FetchUnreadMessages(db.DB, req.AgentId, cursor, limit)
	if err != nil {
		return &pm.FetchPMResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to fetch messages"},
		}, nil
	}

	if len(messages) == 0 {
		emptyResp := &pm.FetchPMResp{
			Messages:   []*pm.PMMessage{},
			NextCursor: cursor,
			BaseResp:   &base.BaseResp{Code: 0, Msg: "success"},
		}
		// Cache empty result for cursor=0
		if cursor == 0 {
			if data, err := json.Marshal(emptyResp); err == nil {
				db.RDB.Set(ctx, fmt.Sprintf("pm:fetch:%d", req.AgentId), data, 10*time.Second)
			}
		}
		return emptyResp, nil
	}

	// Mark as read
	msgIDs := make([]int64, len(messages))
	for i, msg := range messages {
		msgIDs[i] = msg.MsgID
	}
	_ = dal.MarkMessagesAsRead(db.DB, msgIDs)

	// Build response
	respMessages := make([]*pm.PMMessage, len(messages))
	for i, msg := range messages {
		respMessages[i] = &pm.PMMessage{
			MsgId:        msg.MsgID,
			ConvId:       msg.ConvID,
			SenderId:     msg.SenderID,
			ReceiverId:   msg.ReceiverID,
			Content:      msg.Content,
			IsRead:       true,
			CreatedAt:    msg.CreatedAt,
			SenderName:   &msg.SenderName,
			ReceiverName: &msg.ReceiverName,
		}
	}

	nextCursor := messages[len(messages)-1].MsgID

	return &pm.FetchPMResp{
		Messages:   respMessages,
		NextCursor: nextCursor,
		BaseResp:   &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *PMServiceImpl) ListConversations(ctx context.Context, req *pm.ListConversationsReq) (*pm.ListConversationsResp, error) {
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	cursor := req.GetCursor()

	convs, err := dal.ListConversations(db.DB, req.AgentId, cursor, limit)
	if err != nil {
		return &pm.ListConversationsResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to list conversations"},
		}, nil
	}

	conversations := make([]*pm.ConversationInfo, len(convs))
	for i, conv := range convs {
		conversations[i] = &pm.ConversationInfo{
			ConvId:           conv.ConvID,
			ParticipantA:     conv.ParticipantA,
			ParticipantB:     conv.ParticipantB,
			UpdatedAt:        conv.UpdatedAt,
			ParticipantAName: &conv.ParticipantAName,
			ParticipantBName: &conv.ParticipantBName,
		}
	}

	var nextCursor int64
	if len(convs) > 0 {
		nextCursor = convs[len(convs)-1].UpdatedAt
	}

	return &pm.ListConversationsResp{
		Conversations: conversations,
		NextCursor:    nextCursor,
		BaseResp:      &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *PMServiceImpl) GetConvHistory(ctx context.Context, req *pm.GetConvHistoryReq) (*pm.GetConvHistoryResp, error) {
	// Validate membership
	_, err := s.validator.ValidateConvMembership(ctx, req.ConvId, req.AgentId)
	if err != nil {
		return &pm.GetConvHistoryResp{
			BaseResp: &base.BaseResp{Code: 403, Msg: err.Error()},
		}, nil
	}

	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	cursor := req.GetCursor()

	msgs, err := dal.GetConvMessages(db.DB, req.ConvId, cursor, limit)
	if err != nil {
		return &pm.GetConvHistoryResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to fetch conversation history"},
		}, nil
	}

	pmMessages := make([]*pm.PMMessage, len(msgs))
	for i, msg := range msgs {
		pmMessages[i] = &pm.PMMessage{
			MsgId:        msg.MsgID,
			ConvId:       msg.ConvID,
			SenderId:     msg.SenderID,
			ReceiverId:   msg.ReceiverID,
			Content:      msg.Content,
			IsRead:       msg.IsRead,
			CreatedAt:    msg.CreatedAt,
			SenderName:   &msg.SenderName,
			ReceiverName: &msg.ReceiverName,
		}
	}

	var nextCursor int64
	if len(msgs) > 0 {
		nextCursor = msgs[len(msgs)-1].MsgID
	}

	return &pm.GetConvHistoryResp{
		Messages:   pmMessages,
		NextCursor: nextCursor,
		BaseResp:   &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *PMServiceImpl) CloseConv(ctx context.Context, req *pm.CloseConvReq) (*pm.CloseConvResp, error) {
	// Validate membership
	_, err := s.validator.ValidateConvMembership(ctx, req.ConvId, req.AgentId)
	if err != nil {
		return &pm.CloseConvResp{
			BaseResp: &base.BaseResp{Code: 403, Msg: err.Error()},
		}, nil
	}

	if err := dal.CloseConversation(db.DB, req.ConvId); err != nil {
		return &pm.CloseConvResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: err.Error()},
		}, nil
	}

	// Invalidate cached conv info so membership checks see the new status
	s.validator.InvalidateConvCache(ctx, req.ConvId)

	return &pm.CloseConvResp{
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}
