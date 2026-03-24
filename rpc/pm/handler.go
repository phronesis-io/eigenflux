package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/pm"
	"eigenflux_server/pkg/db"
	"eigenflux_server/rpc/pm/dal"
	"eigenflux_server/rpc/pm/icebreak"
	"eigenflux_server/rpc/pm/notifyutil"
	"eigenflux_server/rpc/pm/relations"
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
	// Block check - silent success if blocked
	blocked, _ := relations.IsBlockedCached(ctx, db.RDB, db.DB, req.ReceiverId, req.SenderId)
	if blocked {
		return &pm.SendPMResp{MsgId: 0, ConvId: 0, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
	}

	// Case 1: New conversation (item_id provided)
	if req.ItemId != nil && *req.ItemId > 0 {
		return s.handleNewConversation(ctx, req)
	}

	// Case 2: Reply (conv_id provided)
	if req.ConvId != nil && *req.ConvId > 0 {
		return s.handleReply(ctx, req)
	}

	// Case 3: Friend-based PM (neither item_id nor conv_id)
	return s.handleFriendPM(ctx, req)
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

	log.Printf("[PM] New conversation: conv_id=%d msg_id=%d sender=%d receiver=%d item=%d", convID, msgID, req.SenderId, req.ReceiverId, itemID)

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

	log.Printf("[PM] Reply: conv_id=%d msg_id=%d sender=%d receiver=%d", convID, msgID, req.SenderId, receiverID)

	return &pm.SendPMResp{
		MsgId:    msgID,
		ConvId:   convID,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *PMServiceImpl) handleFriendPM(ctx context.Context, req *pm.SendPMReq) (*pm.SendPMResp, error) {
	isFriend, _ := relations.IsFriendCached(ctx, db.RDB, db.DB, req.SenderId, req.ReceiverId)
	if !isFriend {
		return &pm.SendPMResp{BaseResp: &base.BaseResp{Code: 403, Msg: "not friends"}}, nil
	}
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
		return s.handleReply(ctx, req)
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

func (s *PMServiceImpl) SendFriendRequest(ctx context.Context, req *pm.SendFriendRequestReq) (*pm.SendFriendRequestResp, error) {
	// Check if either blocked
	blocked, _ := relations.IsBlockedCached(ctx, db.RDB, db.DB, req.FromUid, req.ToUid)
	if blocked {
		return &pm.SendFriendRequestResp{BaseResp: &base.BaseResp{Code: 403, Msg: "cannot send request"}}, nil
	}
	blocked, _ = relations.IsBlockedCached(ctx, db.RDB, db.DB, req.ToUid, req.FromUid)
	if blocked {
		return &pm.SendFriendRequestResp{BaseResp: &base.BaseResp{Code: 403, Msg: "cannot send request"}}, nil
	}

	// Check mutual pending
	mutualReq, err := dal.GetPendingRequestBetween(db.DB, req.ToUid, req.FromUid)
	if err == nil && mutualReq != nil {
		// Auto-accept
		err = db.DB.Transaction(func(tx *gorm.DB) error {
			if err := dal.UpdateRequestStatus(tx, mutualReq.ID, dal.RequestStatusAccepted); err != nil {
				return err
			}
			return dal.CreateFriendRelation(tx, req.FromUid, req.ToUid)
		})
		if err != nil {
			return &pm.SendFriendRequestResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to accept"}}, nil
		}
		_ = relations.InvalidateFriendCache(ctx, db.RDB, req.FromUid)
		_ = relations.InvalidateFriendCache(ctx, db.RDB, req.ToUid)
		return &pm.SendFriendRequestResp{RequestId: mutualReq.ID, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
	}

	requestID, err := dal.CreateFriendRequest(db.DB, req.FromUid, req.ToUid)
	if err != nil {
		return &pm.SendFriendRequestResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to create request"}}, nil
	}

	// Fire-and-forget: notify recipient of new friend request
	go func() {
		if err := notifyutil.WriteFriendRequestNotification(context.Background(), db.RDB, requestID, req.ToUid); err != nil {
			log.Printf("[PM] Failed to write friend request notification for request %d to agent %d: %v", requestID, req.ToUid, err)
		}
	}()

	return &pm.SendFriendRequestResp{RequestId: requestID, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) HandleFriendRequest(ctx context.Context, req *pm.HandleFriendRequestReq) (*pm.HandleFriendRequestResp, error) {
	friendReq, err := dal.GetFriendRequest(db.DB, req.RequestId)
	if err != nil {
		return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 404, Msg: "request not found"}}, nil
	}

	switch req.Action {
	case pm.FriendRequestAction_ACCEPT:
		if friendReq.ToUID != req.AgentId {
			return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 403, Msg: "not recipient"}}, nil
		}
		err = db.DB.Transaction(func(tx *gorm.DB) error {
			if err := dal.UpdateRequestStatus(tx, req.RequestId, dal.RequestStatusAccepted); err != nil {
				return err
			}
			return dal.CreateFriendRelation(tx, friendReq.FromUID, friendReq.ToUID)
		})
		if err != nil {
			return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to accept"}}, nil
		}
		_ = relations.InvalidateFriendCache(ctx, db.RDB, friendReq.FromUID)
		_ = relations.InvalidateFriendCache(ctx, db.RDB, friendReq.ToUID)

	case pm.FriendRequestAction_REJECT:
		if friendReq.ToUID != req.AgentId {
			return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 403, Msg: "not recipient"}}, nil
		}
		if err := dal.UpdateRequestStatus(db.DB, req.RequestId, dal.RequestStatusRejected); err != nil {
			return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to reject"}}, nil
		}

	case pm.FriendRequestAction_CANCEL:
		if friendReq.FromUID != req.AgentId {
			return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 403, Msg: "not sender"}}, nil
		}
		if err := dal.UpdateRequestStatus(db.DB, req.RequestId, dal.RequestStatusCancelled); err != nil {
			return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to cancel"}}, nil
		}

	default:
		return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 400, Msg: "invalid action"}}, nil
	}

	return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) Unfriend(ctx context.Context, req *pm.UnfriendReq) (*pm.UnfriendResp, error) {
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
	return &pm.UnfriendResp{BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) BlockUser(ctx context.Context, req *pm.BlockUserReq) (*pm.BlockUserResp, error) {
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := dal.CreateBlockRelation(tx, req.FromUid, req.ToUid); err != nil {
			return err
		}
		tx.Where("((from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?)) AND rel_type = ?",
			req.FromUid, req.ToUid, req.ToUid, req.FromUid, dal.RelTypeFriend).Delete(&dal.UserRelation{})
		return tx.Model(&dal.FriendRequest{}).
			Where("from_uid = ? AND to_uid = ? AND status = ?", req.ToUid, req.FromUid, dal.RequestStatusPending).
			Update("status", dal.RequestStatusCancelled).Error
	})
	if err != nil {
		return &pm.BlockUserResp{BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()}}, nil
	}
	_ = db.RDB.SAdd(ctx, fmt.Sprintf("block:%d", req.FromUid), req.ToUid)
	_ = relations.InvalidateFriendCache(ctx, db.RDB, req.FromUid)
	_ = relations.InvalidateFriendCache(ctx, db.RDB, req.ToUid)
	return &pm.BlockUserResp{BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) UnblockUser(ctx context.Context, req *pm.UnblockUserReq) (*pm.UnblockUserResp, error) {
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
	cursor := int(req.GetCursor())

	requests, err := dal.ListFriendRequests(db.DB, req.AgentId, req.Direction, cursor, limit)
	if err != nil {
		return &pm.ListFriendRequestsResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to list"}}, nil
	}

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
			result = append(result, &pm.FriendRequestInfo{
				RequestId: r.ID,
				FromUid:   r.FromUID,
				ToUid:     r.ToUID,
				CreatedAt: r.CreatedAt,
				FromName:  &fromName,
				ToName:    &toName,
			})
		}
	}

	var nextCursor int64
	if len(requests) > 0 {
		nextCursor = requests[len(requests)-1].CreatedAt
	}
	return &pm.ListFriendRequestsResp{Requests: result, NextCursor: nextCursor, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) ListFriends(ctx context.Context, req *pm.ListFriendsReq) (*pm.ListFriendsResp, error) {
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	cursor := int(req.GetCursor())

	friends, err := dal.ListFriends(db.DB, req.AgentId, cursor, limit)
	if err != nil {
		return &pm.ListFriendsResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to list"}}, nil
	}

	var result []*pm.FriendInfo
	for _, f := range friends {
		result = append(result, &pm.FriendInfo{
			AgentId:     f.AgentID,
			AgentName:   f.AgentName,
			FriendSince: f.FriendSince,
		})
	}

	var nextCursor int64
	if len(friends) > 0 {
		nextCursor = friends[len(friends)-1].FriendSince
	}
	return &pm.ListFriendsResp{Friends: result, NextCursor: nextCursor, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}


