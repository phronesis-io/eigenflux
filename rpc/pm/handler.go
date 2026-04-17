package main

import (
	"context"
	"eigenflux_server/pkg/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/pm"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/rpc/pm/dal"
	"eigenflux_server/rpc/pm/icebreak"
	"eigenflux_server/rpc/pm/notifyutil"
	"eigenflux_server/rpc/pm/relations"
	"eigenflux_server/rpc/pm/validator"

	"gorm.io/gorm"
)

var (
	errFriendRequestAlreadyPending = errors.New("friend request already pending")
	errFriendRequestNotPending     = errors.New("request is no longer pending")
	errFriendRequestBlocked        = errors.New("request cannot be accepted while either side is blocked")
)

type pendingNotificationDeletion struct {
	agentID   int64
	requestID int64
}

type PMServiceImpl struct {
	convIDGen interface {
		NextID() (int64, error)
	}
	msgIDGen interface {
		NextID() (int64, error)
	}
	reqIDGen interface {
		NextID() (int64, error)
	}
	iceBreaker *icebreak.IceBreaker
	validator  *validator.Validator
}

func (s *PMServiceImpl) SendPM(ctx context.Context, req *pm.SendPMReq) (*pm.SendPMResp, error) {
	logger.Ctx(ctx).Info("SendPM", "senderID", req.SenderId, "receiverID", req.ReceiverId, "itemID", req.ItemId, "convID", req.ConvId)
	// Case 1: New conversation (item_id provided)
	if req.ItemId != nil && *req.ItemId > 0 {
		return s.handleNewConversation(ctx, req)
	}

	// Case 2: Reply (conv_id provided)
	if req.ConvId != nil && *req.ConvId > 0 {
		skipIceBreak := false
		if convInfo, err := s.validator.GetConversationInfo(ctx, *req.ConvId); err == nil && strings.EqualFold(convInfo.OriginType, "friend") {
			skipIceBreak = true
		}
		return s.handleReply(ctx, req, skipIceBreak)
	}

	// Case 3: Friend-based PM (neither item_id nor conv_id)
	if req.ReceiverId <= 0 {
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "receiver_id is required for friend conversations"},
		}, nil
	}
	return s.handleFriendPM(ctx, req)
}

func (s *PMServiceImpl) handleNewConversation(ctx context.Context, req *pm.SendPMReq) (*pm.SendPMResp, error) {
	itemID := *req.ItemId

	receiverID, err := s.validator.GetItemOwner(ctx, itemID)
	if err != nil {
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: err.Error()},
		}, nil
	}

	// Block check - silent success if blocked
	blocked, _ := relations.IsBlockedCached(ctx, db.RDB, db.DB, receiverID, req.SenderId)
	if blocked {
		logger.Ctx(ctx).Info("SendPM blocked", "senderID", req.SenderId, "receiverID", receiverID)
		return &pm.SendPMResp{MsgId: 0, ConvId: 0, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
	}

	// Check no_reply
	if err := s.validator.ValidateNoReply(ctx, itemID); err != nil {
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 403, Msg: err.Error()},
		}, nil
	}

	// Compute participant pair
	participantA, participantB := req.SenderId, receiverID
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
		return s.handleReply(ctx, req, false)
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
	nameMap, _ := dal.BatchGetAgentNames(db.DB, []int64{req.SenderId, receiverID})
	senderName := nameMap[req.SenderId]
	receiverName := nameMap[receiverID]

	// Map names to participant slots (A < B)
	nameA, nameB := senderName, receiverName
	if participantA == receiverID {
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
			ReceiverID:   receiverID,
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
	db.RDB.Del(ctx, fmt.Sprintf("pm:fetch:%d", receiverID))
	db.RDB.Publish(ctx, fmt.Sprintf("pm:push:%d", receiverID), fmt.Sprintf("%d", msgID))

	logger.Ctx(ctx).Info("new conversation", "convID", convID, "msgID", msgID, "senderID", req.SenderId, "receiverID", receiverID, "itemID", itemID)

	return &pm.SendPMResp{
		MsgId:    msgID,
		ConvId:   convID,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *PMServiceImpl) handleReply(ctx context.Context, req *pm.SendPMReq, skipIceBreak bool) (*pm.SendPMResp, error) {
	convID := req.GetConvId()

	// Validate conversation membership
	receiverID, err := s.validator.ValidateConvMembership(ctx, convID, req.SenderId)
	if err != nil {
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 403, Msg: err.Error()},
		}, nil
	}

	// Block check - silent success if blocked
	blocked, _ := relations.IsBlockedCached(ctx, db.RDB, db.DB, receiverID, req.SenderId)
	if blocked {
		logger.Ctx(ctx).Info("SendPM blocked", "senderID", req.SenderId, "receiverID", receiverID)
		return &pm.SendPMResp{MsgId: 0, ConvId: 0, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
	}

	// Ice break check (skipped for friend conversations)
	var iceStatus int
	if !skipIceBreak {
		var lastSenderID int64
		iceStatus, lastSenderID, err = s.iceBreaker.CheckAndSetIceBreak(ctx, convID, req.SenderId)
		if err != nil {
			return &pm.SendPMResp{
				BaseResp: &base.BaseResp{Code: 500, Msg: "ice break check failed"},
			}, nil
		}

		if iceStatus == icebreak.IceStatusFirstMsg && lastSenderID == req.SenderId {
			logger.Ctx(ctx).Info("reply rejected (icebreak)", "convID", convID, "senderID", req.SenderId)
			return &pm.SendPMResp{
				BaseResp: &base.BaseResp{Code: 429, Msg: "waiting for reply from the receiver"},
			}, nil
		}
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
	db.RDB.Publish(ctx, fmt.Sprintf("pm:push:%d", receiverID), fmt.Sprintf("%d", msgID))

	logger.Ctx(ctx).Info("reply sent", "convID", convID, "msgID", msgID, "senderID", req.SenderId, "receiverID", receiverID)

	return &pm.SendPMResp{
		MsgId:    msgID,
		ConvId:   convID,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *PMServiceImpl) handleFriendPM(ctx context.Context, req *pm.SendPMReq) (*pm.SendPMResp, error) {
	blocked, _ := relations.IsBlockedCached(ctx, db.RDB, db.DB, req.ReceiverId, req.SenderId)
	if blocked {
		logger.Ctx(ctx).Info("SendPM blocked", "senderID", req.SenderId, "receiverID", req.ReceiverId)
		return &pm.SendPMResp{MsgId: 0, ConvId: 0, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
	}

	isFriend, _ := relations.IsFriendCached(ctx, db.RDB, db.DB, req.SenderId, req.ReceiverId)
	if !isFriend {
		logger.Ctx(ctx).Info("FriendPM rejected (not friends)", "senderID", req.SenderId, "receiverID", req.ReceiverId)
		return &pm.SendPMResp{BaseResp: &base.BaseResp{Code: 403, Msg: "not friends"}}, nil
	}
	logger.Ctx(ctx).Info("FriendPM", "senderID", req.SenderId, "receiverID", req.ReceiverId)
	participantA, participantB := req.SenderId, req.ReceiverId
	if participantA > participantB {
		participantA, participantB = participantB, participantA
	}
	existingConvID, exists, err := s.validator.GetOrCreateConvID(ctx, participantA, participantB, 0)
	if err != nil {
		return &pm.SendPMResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to check conversation"}}, nil
	}
	if exists {
		req.ConvId = &existingConvID
		return s.handleReply(ctx, req, true)
	}
	convID, _ := s.convIDGen.NextID()
	msgID, _ := s.msgIDGen.NextID()
	nameMap, _ := dal.BatchGetAgentNames(db.DB, []int64{req.SenderId, req.ReceiverId})
	nameA, nameB := nameMap[participantA], nameMap[participantB]
	err = db.DB.Transaction(func(tx *gorm.DB) error {
		conv := &dal.Conversation{
			ConvID: convID, ParticipantA: participantA, ParticipantB: participantB,
			InitiatorID: req.SenderId, LastSenderID: req.SenderId, OriginType: "friend",
			OriginID: 0, MsgCount: 1, Status: 0, ParticipantAName: nameA, ParticipantBName: nameB,
		}
		if err := dal.CreateConversation(tx, conv); err != nil {
			return err
		}
		msg := &dal.PrivateMessage{
			MsgID: msgID, ConvID: convID, SenderID: req.SenderId, ReceiverID: req.ReceiverId,
			Content: req.Content, IsRead: false, SenderName: nameMap[req.SenderId], ReceiverName: nameMap[req.ReceiverId],
		}
		return dal.CreateMessage(tx, msg)
	})
	if err != nil {
		return &pm.SendPMResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to create"}}, nil
	}
	_ = s.validator.CacheConvMapping(ctx, participantA, participantB, 0, convID)
	db.RDB.Del(ctx, fmt.Sprintf("pm:fetch:%d", req.ReceiverId))
	db.RDB.Publish(ctx, fmt.Sprintf("pm:push:%d", req.ReceiverId), fmt.Sprintf("%d", msgID))
	logger.Ctx(ctx).Info("FriendPM new conv", "convID", convID, "msgID", msgID, "senderID", req.SenderId, "receiverID", req.ReceiverId)
	return &pm.SendPMResp{MsgId: msgID, ConvId: convID, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
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
		logger.Ctx(ctx).Error("FetchPM failed", "agentID", req.AgentId, "err", err)
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
	logger.Ctx(ctx).Info("FetchPM", "agentID", req.AgentId, "count", len(messages))

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

	logger.Ctx(ctx).Info("CloseConv", "agentID", req.AgentId, "convID", req.ConvId)
	return &pm.CloseConvResp{
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *PMServiceImpl) SendFriendRequest(ctx context.Context, req *pm.SendFriendRequestReq) (*pm.SendFriendRequestResp, error) {
	logger.Ctx(ctx).Info("SendFriendRequest", "fromUID", req.FromUid, "toUID", req.ToUid)

	rateLimitKey := fmt.Sprintf("ratelimit:friend_request:%d", req.FromUid)
	count, err := db.RDB.Incr(ctx, rateLimitKey).Result()
	if err == nil {
		if count == 1 {
			db.RDB.Expire(ctx, rateLimitKey, time.Hour)
		}
		if count > 10 {
			logger.Ctx(ctx).Warn("SendFriendRequest rate limited", "fromUID", req.FromUid, "count", count)
			return &pm.SendFriendRequestResp{BaseResp: &base.BaseResp{Code: 429, Msg: "too many requests, please try again later"}}, nil
		}
	}

	greeting := ""
	if req.Greeting != nil {
		greeting = truncateByWeightedLength(*req.Greeting, 200)
	}

	remark := ""
	if req.Remark != nil {
		remark = truncateByWeightedLength(*req.Remark, 100)
	}

	blocked, _ := relations.IsBlockedCached(ctx, db.RDB, db.DB, req.FromUid, req.ToUid)
	if blocked {
		logger.Ctx(ctx).Info("SendFriendRequest blocked (sender blocked target)", "fromUID", req.FromUid, "toUID", req.ToUid)
		return &pm.SendFriendRequestResp{BaseResp: &base.BaseResp{Code: 403, Msg: "cannot send request"}}, nil
	}
	blocked, _ = relations.IsBlockedCached(ctx, db.RDB, db.DB, req.ToUid, req.FromUid)
	if blocked {
		logger.Ctx(ctx).Info("SendFriendRequest blocked (target blocked sender)", "fromUID", req.FromUid, "toUID", req.ToUid)
		return &pm.SendFriendRequestResp{BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
	}

	isFriend, _ := relations.IsFriendCached(ctx, db.RDB, db.DB, req.FromUid, req.ToUid)
	if isFriend {
		logger.Ctx(ctx).Info("SendFriendRequest rejected (already friends)", "fromUID", req.FromUid, "toUID", req.ToUid)
		return &pm.SendFriendRequestResp{BaseResp: &base.BaseResp{Code: 400, Msg: "already friends"}}, nil
	}

	var requestID int64
	var notifyRecipient bool
	var deletions []pendingNotificationDeletion

	err = db.DB.Transaction(func(tx *gorm.DB) error {
		if err := dal.LockRelationPair(tx, req.FromUid, req.ToUid); err != nil {
			return err
		}

		outgoingReq, err := dal.GetFriendRequestBetweenForUpdate(tx, req.FromUid, req.ToUid)
		if err != nil {
			return err
		}
		incomingReq, err := dal.GetFriendRequestBetweenForUpdate(tx, req.ToUid, req.FromUid)
		if err != nil {
			return err
		}

		if incomingReq != nil && incomingReq.Status == dal.RequestStatusPending {
			updated, err := dal.UpdateRequestStatusIfPending(tx, incomingReq.ID, dal.RequestStatusAccepted)
			if err != nil {
				return err
			}
			if !updated {
				return errFriendRequestNotPending
			}
			if err := dal.CreateFriendRelation(tx, req.FromUid, req.ToUid, remark, incomingReq.Remark); err != nil {
				return err
			}
			requestID = incomingReq.ID
			deletions = append(deletions, pendingNotificationDeletion{agentID: req.FromUid, requestID: incomingReq.ID})
			return nil
		}

		if outgoingReq != nil {
			switch outgoingReq.Status {
			case dal.RequestStatusPending:
				return errFriendRequestAlreadyPending
			case dal.RequestStatusRejected, dal.RequestStatusCancelled, dal.RequestStatusUnfriended, dal.RequestStatusAccepted:
				requestID, err = s.reqIDGen.NextID()
				if err != nil {
					return err
				}
				if err := dal.ResetFriendRequest(tx, outgoingReq.ID, requestID, greeting, remark); err != nil {
					return err
				}
				deletions = append(deletions, pendingNotificationDeletion{agentID: req.ToUid, requestID: outgoingReq.ID})
				notifyRecipient = true
				return nil
			default:
				return fmt.Errorf("unsupported friend request status: %d", outgoingReq.Status)
			}
		}

		requestID, err = s.reqIDGen.NextID()
		if err != nil {
			return err
		}
		if _, err := dal.CreateFriendRequest(tx, requestID, req.FromUid, req.ToUid, greeting, remark); err != nil {
			return err
		}
		notifyRecipient = true
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errFriendRequestAlreadyPending):
			return &pm.SendFriendRequestResp{BaseResp: &base.BaseResp{Code: 400, Msg: err.Error()}}, nil
		default:
			logger.Ctx(ctx).Error("SendFriendRequest failed", "fromUID", req.FromUid, "toUID", req.ToUid, "err", err)
			return &pm.SendFriendRequestResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to create request"}}, nil
		}
	}

	if notifyRecipient {
		logger.Ctx(ctx).Info("SendFriendRequest created", "requestID", requestID, "fromUID", req.FromUid, "toUID", req.ToUid)
		go func() {
			if err := notifyutil.WriteFriendRequestNotification(context.Background(), db.RDB, requestID, req.ToUid, greeting); err != nil {
				logger.Default().Error("failed to write friend request notification", "requestID", requestID, "agentID", req.ToUid, "err", err)
			}
		}()
	} else {
		logger.Ctx(ctx).Info("SendFriendRequest auto-accepted mutual request", "requestID", requestID, "fromUID", req.FromUid, "toUID", req.ToUid)
	}
	s.deletePendingFriendRequestNotifications(deletions)
	_ = relations.InvalidateFriendCache(ctx, db.RDB, req.FromUid)
	_ = relations.InvalidateFriendCache(ctx, db.RDB, req.ToUid)

	return &pm.SendFriendRequestResp{RequestId: requestID, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) HandleFriendRequest(ctx context.Context, req *pm.HandleFriendRequestReq) (*pm.HandleFriendRequestResp, error) {
	logger.Ctx(ctx).Info("HandleFriendRequest", "requestID", req.RequestId, "agentID", req.AgentId, "action", req.Action)

	reason := ""
	if req.Reason != nil {
		reason = truncateByWeightedLength(*req.Reason, 200)
	}

	var friendReq *dal.FriendRequest
	var responseNotifType string
	var deletePending bool

	err := db.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		friendReq, err = dal.GetFriendRequestForUpdate(tx, req.RequestId)
		if err != nil {
			return err
		}
		if friendReq == nil {
			return gorm.ErrRecordNotFound
		}
		if err := dal.LockRelationPair(tx, friendReq.FromUID, friendReq.ToUID); err != nil {
			return err
		}
		if friendReq.Status != dal.RequestStatusPending {
			return errFriendRequestNotPending
		}

		switch req.Action {
		case pm.FriendRequestAction_ACCEPT:
			if friendReq.ToUID != req.AgentId {
				return fmt.Errorf("not recipient")
			}
			blockedBySender, err := dal.IsBlocked(tx, friendReq.FromUID, friendReq.ToUID)
			if err != nil {
				return err
			}
			blockedByRecipient, err := dal.IsBlocked(tx, friendReq.ToUID, friendReq.FromUID)
			if err != nil {
				return err
			}
			if blockedBySender || blockedByRecipient {
				return errFriendRequestBlocked
			}

			recipientRemark := ""
			if req.Remark != nil {
				recipientRemark = truncateByWeightedLength(*req.Remark, 100)
			}
			updated, err := dal.UpdateRequestStatusIfPending(tx, req.RequestId, dal.RequestStatusAccepted)
			if err != nil {
				return err
			}
			if !updated {
				return errFriendRequestNotPending
			}
			isFriend, err := dal.IsFriend(tx, friendReq.FromUID, friendReq.ToUID)
			if err != nil {
				return err
			}
			if !isFriend {
				if err := dal.CreateFriendRelation(tx, friendReq.FromUID, friendReq.ToUID, friendReq.Remark, recipientRemark); err != nil {
					return err
				}
			}
			responseNotifType = "friend_accepted"
			deletePending = true

		case pm.FriendRequestAction_REJECT:
			if friendReq.ToUID != req.AgentId {
				return fmt.Errorf("not recipient")
			}
			updated, err := dal.UpdateRequestStatusIfPending(tx, req.RequestId, dal.RequestStatusRejected)
			if err != nil {
				return err
			}
			if !updated {
				return errFriendRequestNotPending
			}
			responseNotifType = "friend_rejected"
			deletePending = true

		case pm.FriendRequestAction_CANCEL:
			if friendReq.FromUID != req.AgentId {
				return fmt.Errorf("not sender")
			}
			updated, err := dal.UpdateRequestStatusIfPending(tx, req.RequestId, dal.RequestStatusCancelled)
			if err != nil {
				return err
			}
			if !updated {
				return errFriendRequestNotPending
			}
			deletePending = true

		default:
			return fmt.Errorf("invalid action")
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 404, Msg: "request not found"}}, nil
		case errors.Is(err, errFriendRequestNotPending):
			return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 400, Msg: err.Error()}}, nil
		case errors.Is(err, errFriendRequestBlocked):
			return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 403, Msg: err.Error()}}, nil
		case err.Error() == "not recipient":
			return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 403, Msg: "not recipient"}}, nil
		case err.Error() == "not sender":
			return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 403, Msg: "not sender"}}, nil
		case err.Error() == "invalid action":
			return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 400, Msg: "invalid action"}}, nil
		default:
			logger.Ctx(ctx).Error("HandleFriendRequest failed", "requestID", req.RequestId, "agentID", req.AgentId, "err", err)
			return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to handle request"}}, nil
		}
	}

	if deletePending {
		s.deletePendingFriendRequestNotifications([]pendingNotificationDeletion{{agentID: friendReq.ToUID, requestID: friendReq.ID}})
	}
	_ = relations.InvalidateFriendCache(ctx, db.RDB, friendReq.FromUID)
	_ = relations.InvalidateFriendCache(ctx, db.RDB, friendReq.ToUID)

	switch responseNotifType {
	case "friend_accepted":
		logger.Ctx(ctx).Info("FriendRequest accepted", "requestID", req.RequestId, "fromUID", friendReq.FromUID, "toUID", friendReq.ToUID)
		go func() {
			if err := notifyutil.WriteFriendResponseNotification(context.Background(), db.RDB, req.RequestId, friendReq.FromUID, responseNotifType, reason); err != nil {
				logger.Default().Error("failed to write friend accepted notification", "requestID", req.RequestId, "agentID", friendReq.FromUID, "err", err)
			}
		}()
	case "friend_rejected":
		logger.Ctx(ctx).Info("FriendRequest rejected", "requestID", req.RequestId, "agentID", req.AgentId)
		go func() {
			if err := notifyutil.WriteFriendResponseNotification(context.Background(), db.RDB, req.RequestId, friendReq.FromUID, responseNotifType, reason); err != nil {
				logger.Default().Error("failed to write friend rejected notification", "requestID", req.RequestId, "agentID", friendReq.FromUID, "err", err)
			}
		}()
	default:
		logger.Ctx(ctx).Info("FriendRequest cancelled", "requestID", req.RequestId, "agentID", req.AgentId)
	}

	return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) Unfriend(ctx context.Context, req *pm.UnfriendReq) (*pm.UnfriendResp, error) {
	logger.Ctx(ctx).Info("Unfriend", "fromUID", req.FromUid, "toUID", req.ToUid)

	err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := dal.DeleteFriendRelation(tx, req.FromUid, req.ToUid); err != nil {
			return err
		}
		return tx.Model(&dal.FriendRequest{}).
			Where("((from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?)) AND status = ?",
				req.FromUid, req.ToUid, req.ToUid, req.FromUid, dal.RequestStatusAccepted).
			Update("status", dal.RequestStatusUnfriended).Error
	})
	if err != nil {
		return &pm.UnfriendResp{BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()}}, nil
	}
	_ = relations.InvalidateFriendCache(ctx, db.RDB, req.FromUid)
	_ = relations.InvalidateFriendCache(ctx, db.RDB, req.ToUid)
	logger.Ctx(ctx).Info("Unfriend done", "fromUID", req.FromUid, "toUID", req.ToUid)
	return &pm.UnfriendResp{BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) BlockUser(ctx context.Context, req *pm.BlockUserReq) (*pm.BlockUserResp, error) {
	logger.Ctx(ctx).Info("BlockUser", "fromUID", req.FromUid, "toUID", req.ToUid)

	remark := ""
	if req.Remark != nil {
		remark = truncateByWeightedLength(*req.Remark, 100)
	}

	var deletions []pendingNotificationDeletion
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := dal.LockRelationPair(tx, req.FromUid, req.ToUid); err != nil {
			return err
		}
		outgoingReq, err := dal.GetFriendRequestBetweenForUpdate(tx, req.FromUid, req.ToUid)
		if err != nil {
			return err
		}
		incomingReq, err := dal.GetFriendRequestBetweenForUpdate(tx, req.ToUid, req.FromUid)
		if err != nil {
			return err
		}
		if err := dal.CreateBlockRelation(tx, req.FromUid, req.ToUid, remark); err != nil {
			return err
		}
		tx.Where("((from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?)) AND rel_type = ?",
			req.FromUid, req.ToUid, req.ToUid, req.FromUid, dal.RelTypeFriend).Delete(&dal.UserRelation{})
		for _, request := range []*dal.FriendRequest{outgoingReq, incomingReq} {
			if request == nil {
				continue
			}
			switch request.Status {
			case dal.RequestStatusPending:
				updated, err := dal.UpdateRequestStatusIfPending(tx, request.ID, dal.RequestStatusCancelled)
				if err != nil {
					return err
				}
				if updated {
					deletions = append(deletions, pendingNotificationDeletion{agentID: request.ToUID, requestID: request.ID})
				}
			case dal.RequestStatusAccepted:
				if err := dal.UpdateRequestStatus(tx, request.ID, dal.RequestStatusUnfriended); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return &pm.BlockUserResp{BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()}}, nil
	}
	_ = db.RDB.SAdd(ctx, fmt.Sprintf("block:%d", req.FromUid), req.ToUid)
	_ = relations.InvalidateFriendCache(ctx, db.RDB, req.FromUid)
	_ = relations.InvalidateFriendCache(ctx, db.RDB, req.ToUid)
	s.deletePendingFriendRequestNotifications(deletions)
	logger.Ctx(ctx).Info("BlockUser done", "fromUID", req.FromUid, "toUID", req.ToUid)
	return &pm.BlockUserResp{BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) UnblockUser(ctx context.Context, req *pm.UnblockUserReq) (*pm.UnblockUserResp, error) {
	logger.Ctx(ctx).Info("UnblockUser", "fromUID", req.FromUid, "toUID", req.ToUid)

	err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := dal.DeleteBlockRelation(tx, req.FromUid, req.ToUid); err != nil {
			return err
		}
		return tx.Model(&dal.FriendRequest{}).
			Where("from_uid = ? AND to_uid = ? AND status = ?", req.ToUid, req.FromUid, dal.RequestStatusPending).
			Update("status", dal.RequestStatusCancelled).Error
	})
	if err != nil {
		return &pm.UnblockUserResp{BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()}}, nil
	}
	_ = db.RDB.SRem(ctx, fmt.Sprintf("block:%d", req.FromUid), req.ToUid)
	logger.Ctx(ctx).Info("UnblockUser done", "fromUID", req.FromUid, "toUID", req.ToUid)
	return &pm.UnblockUserResp{BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) ListFriendRequests(ctx context.Context, req *pm.ListFriendRequestsReq) (*pm.ListFriendRequestsResp, error) {
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	cursor := req.GetCursor()

	requests, hasMore, err := dal.ListFriendRequests(db.DB, req.AgentId, req.Direction, cursor, limit)
	if err != nil {
		logger.Ctx(ctx).Error("ListFriendRequests failed", "agentID", req.AgentId, "direction", req.Direction, "err", err)
		return &pm.ListFriendRequestsResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to list"}}, nil
	}
	logger.Ctx(ctx).Info("ListFriendRequests", "agentID", req.AgentId, "direction", req.Direction, "count", len(requests))

	var result []*pm.FriendRequestInfo
	if len(requests) > 0 {
		var uids []int64
		for _, r := range requests {
			uids = append(uids, r.FromUID, r.ToUID)
		}
		nameMap, _ := dal.BatchGetAgentNames(db.DB, uids)
		for _, r := range requests {
			fromName := nameMap[r.FromUID]
			toName := nameMap[r.ToUID]
			info := &pm.FriendRequestInfo{
				RequestId: r.ID,
				FromUid:   r.FromUID,
				ToUid:     r.ToUID,
				CreatedAt: r.CreatedAt,
				FromName:  &fromName,
				ToName:    &toName,
			}
			if r.Greeting != "" {
				info.Greeting = &r.Greeting
			}
			result = append(result, info)
		}
	}

	var nextCursor int64
	if len(requests) > 0 {
		nextCursor = requests[len(requests)-1].ID
	}
	return &pm.ListFriendRequestsResp{Requests: result, NextCursor: nextCursor, HasMore: &hasMore, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) ListFriends(ctx context.Context, req *pm.ListFriendsReq) (*pm.ListFriendsResp, error) {
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	cursor := req.GetCursor()

	friends, err := dal.ListFriends(db.DB, req.AgentId, cursor, limit)
	if err != nil {
		logger.Ctx(ctx).Error("ListFriends failed", "agentID", req.AgentId, "err", err)
		return &pm.ListFriendsResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to list"}}, nil
	}
	logger.Ctx(ctx).Info("ListFriends", "agentID", req.AgentId, "count", len(friends))

	var result []*pm.FriendInfo
	for _, f := range friends {
		info := &pm.FriendInfo{
			AgentId:     f.AgentID,
			AgentName:   f.AgentName,
			FriendSince: f.FriendSince,
		}
		if f.Remark != "" {
			info.Remark = &f.Remark
		}
		result = append(result, info)
	}

	var nextCursor int64
	if len(friends) > 0 {
		nextCursor = friends[len(friends)-1].RelationID
	}
	return &pm.ListFriendsResp{Friends: result, NextCursor: nextCursor, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) UpdateFriendRemark(ctx context.Context, req *pm.UpdateFriendRemarkReq) (*pm.UpdateFriendRemarkResp, error) {
	logger.Ctx(ctx).Info("UpdateFriendRemark", "agentID", req.AgentId, "friendUID", req.FriendUid)

	remark := truncateByWeightedLength(req.Remark, 100)

	if err := dal.UpdateFriendRemark(db.DB, req.AgentId, req.FriendUid, remark); err != nil {
		logger.Ctx(ctx).Error("UpdateFriendRemark failed", "agentID", req.AgentId, "friendUID", req.FriendUid, "err", err)
		return &pm.UpdateFriendRemarkResp{BaseResp: &base.BaseResp{Code: 400, Msg: err.Error()}}, nil
	}

	logger.Ctx(ctx).Info("UpdateFriendRemark done", "agentID", req.AgentId, "friendUID", req.FriendUid)
	return &pm.UpdateFriendRemarkResp{BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

// truncateByWeightedLength truncates a string to fit within maxLen weighted characters.
// ASCII characters count as 1, CJK characters count as 2.
func truncateByWeightedLength(s string, maxLen int) string {
	length := 0
	for i, r := range s {
		w := 1
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			w = 2
		}
		if length+w > maxLen {
			return s[:i]
		}
		length += w
	}
	return s
}

func (s *PMServiceImpl) deletePendingFriendRequestNotifications(items []pendingNotificationDeletion) {
	if db.RDB == nil || len(items) == 0 {
		return
	}

	go func() {
		for _, item := range items {
			if err := notifyutil.DeletePMNotifications(context.Background(), db.RDB, item.agentID, item.requestID); err != nil {
				logger.Default().Error("failed to delete friend request notification", "requestID", item.requestID, "agentID", item.agentID, "err", err)
			}
		}
	}()
}

// FetchPMHistory returns the last N messages involving the agent that
// are NOT in the unread-received set. Read-only: does NOT mark anything.
// Intended for recovery-on-reconnect (WS) and every REST pm/fetch call.
// Must be called BEFORE FetchPM in handlers that call both, because
// FetchPM's mark-as-read side effect would leak new read messages into
// a subsequent history call.
func (s *PMServiceImpl) FetchPMHistory(ctx context.Context, req *pm.FetchPMHistoryReq) (*pm.FetchPMHistoryResp, error) {
	if req.AgentId <= 0 {
		return &pm.FetchPMHistoryResp{
			Messages: []*pm.PMMessage{},
			BaseResp: &base.BaseResp{Code: 400, Msg: "invalid agent_id"},
		}, nil
	}

	limit := 20
	if req.Limit != nil {
		limit = int(*req.Limit)
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}

	messages, err := dal.FetchRecentReadMessages(db.DB, req.AgentId, limit)
	if err != nil {
		logger.Ctx(ctx).Error("FetchPMHistory failed", "agentID", req.AgentId, "err", err)
		return &pm.FetchPMHistoryResp{
			Messages: []*pm.PMMessage{},
			BaseResp: &base.BaseResp{Code: 500, Msg: "internal error"},
		}, nil
	}

	respMessages := make([]*pm.PMMessage, len(messages))
	for i, msg := range messages {
		respMessages[i] = &pm.PMMessage{
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

	logger.Ctx(ctx).Info("FetchPMHistory", "agentID", req.AgentId, "count", len(respMessages))
	return &pm.FetchPMHistoryResp{
		Messages: respMessages,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

// FetchPendingFriendRequests returns up to N pending incoming friend requests
// for the agent (newest first) plus the total pending-incoming count.
// Intended for WS initial push so agents see unread friend invites on reconnect.
func (s *PMServiceImpl) FetchPendingFriendRequests(ctx context.Context, req *pm.FetchPendingFriendRequestsReq) (*pm.FetchPendingFriendRequestsResp, error) {
	if req.AgentId <= 0 {
		return &pm.FetchPendingFriendRequestsResp{
			Requests: []*pm.FriendRequestInfo{},
			BaseResp: &base.BaseResp{Code: 400, Msg: "invalid agent_id"},
		}, nil
	}

	limit := 5
	if req.Limit != nil {
		limit = int(*req.Limit)
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}

	rows, total, err := dal.FetchRecentPendingFriendRequests(db.DB, req.AgentId, limit)
	if err != nil {
		logger.Ctx(ctx).Error("FetchPendingFriendRequests failed", "agentID", req.AgentId, "err", err)
		return &pm.FetchPendingFriendRequestsResp{
			Requests: []*pm.FriendRequestInfo{},
			BaseResp: &base.BaseResp{Code: 500, Msg: "internal error"},
		}, nil
	}

	var result []*pm.FriendRequestInfo
	if len(rows) > 0 {
		uids := make([]int64, 0, len(rows)*2)
		for _, r := range rows {
			uids = append(uids, r.FromUID, r.ToUID)
		}
		nameMap, _ := dal.BatchGetAgentNames(db.DB, uids)
		result = make([]*pm.FriendRequestInfo, len(rows))
		for i, r := range rows {
			fromName := nameMap[r.FromUID]
			toName := nameMap[r.ToUID]
			info := &pm.FriendRequestInfo{
				RequestId: r.ID,
				FromUid:   r.FromUID,
				ToUid:     r.ToUID,
				CreatedAt: r.CreatedAt,
				FromName:  &fromName,
				ToName:    &toName,
			}
			if r.Greeting != "" {
				info.Greeting = &r.Greeting
			}
			result[i] = info
		}
	}

	logger.Ctx(ctx).Info("FetchPendingFriendRequests", "agentID", req.AgentId, "returned", len(result), "total", total)
	return &pm.FetchPendingFriendRequestsResp{
		Requests:   result,
		TotalCount: total,
		BaseResp:   &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}
