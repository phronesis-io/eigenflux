package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

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
	log.Printf("[PM] SendPM: sender=%d receiver=%d item_id=%v conv_id=%v", req.SenderId, req.ReceiverId, req.ItemId, req.ConvId)

	// Block check - silent success if blocked
	blocked, _ := relations.IsBlockedCached(ctx, db.RDB, db.DB, req.ReceiverId, req.SenderId)
	if blocked {
		log.Printf("[PM] SendPM blocked: sender=%d receiver=%d", req.SenderId, req.ReceiverId)
		return &pm.SendPMResp{MsgId: 0, ConvId: 0, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
	}

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

func (s *PMServiceImpl) handleReply(ctx context.Context, req *pm.SendPMReq, skipIceBreak bool) (*pm.SendPMResp, error) {
	convID := *req.ConvId

	// Validate conversation membership
	receiverID, err := s.validator.ValidateConvMembership(ctx, convID, req.SenderId)
	if err != nil {
		return &pm.SendPMResp{
			BaseResp: &base.BaseResp{Code: 403, Msg: err.Error()},
		}, nil
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
			log.Printf("[PM] Reply rejected (icebreak): conv_id=%d sender=%d", convID, req.SenderId)
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
		log.Printf("[PM] FriendPM rejected: sender=%d receiver=%d (not friends)", req.SenderId, req.ReceiverId)
		return &pm.SendPMResp{BaseResp: &base.BaseResp{Code: 403, Msg: "not friends"}}, nil
	}
	log.Printf("[PM] FriendPM: sender=%d receiver=%d", req.SenderId, req.ReceiverId)
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
	log.Printf("[PM] FriendPM new conv: conv_id=%d msg_id=%d sender=%d receiver=%d", convID, msgID, req.SenderId, req.ReceiverId)
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
		log.Printf("[PM] FetchPM failed: agent=%d err=%v", req.AgentId, err)
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
	log.Printf("[PM] FetchPM: agent=%d count=%d", req.AgentId, len(messages))

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

	log.Printf("[PM] CloseConv: agent=%d conv_id=%d", req.AgentId, req.ConvId)
	return &pm.CloseConvResp{
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *PMServiceImpl) SendFriendRequest(ctx context.Context, req *pm.SendFriendRequestReq) (*pm.SendFriendRequestResp, error) {
	log.Printf("[Relation] SendFriendRequest: from=%d to=%d", req.FromUid, req.ToUid)

	rateLimitKey := fmt.Sprintf("ratelimit:friend_request:%d", req.FromUid)
	count, err := db.RDB.Incr(ctx, rateLimitKey).Result()
	if err == nil {
		if count == 1 {
			db.RDB.Expire(ctx, rateLimitKey, time.Hour)
		}
		if count > 10 {
			log.Printf("[Relation] SendFriendRequest rate limited: from=%d count=%d", req.FromUid, count)
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
		log.Printf("[Relation] SendFriendRequest blocked: from=%d to=%d (sender blocked target)", req.FromUid, req.ToUid)
		return &pm.SendFriendRequestResp{BaseResp: &base.BaseResp{Code: 403, Msg: "cannot send request"}}, nil
	}
	blocked, _ = relations.IsBlockedCached(ctx, db.RDB, db.DB, req.ToUid, req.FromUid)
	if blocked {
		log.Printf("[Relation] SendFriendRequest blocked: from=%d to=%d (target blocked sender)", req.FromUid, req.ToUid)
		return &pm.SendFriendRequestResp{BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
	}

	isFriend, _ := relations.IsFriendCached(ctx, db.RDB, db.DB, req.FromUid, req.ToUid)
	if isFriend {
		log.Printf("[Relation] SendFriendRequest rejected: from=%d to=%d (already friends)", req.FromUid, req.ToUid)
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
			log.Printf("[Relation] SendFriendRequest failed: from=%d to=%d err=%v", req.FromUid, req.ToUid, err)
			return &pm.SendFriendRequestResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to create request"}}, nil
		}
	}

	if notifyRecipient {
		log.Printf("[Relation] SendFriendRequest created: request_id=%d from=%d to=%d", requestID, req.FromUid, req.ToUid)
		go func() {
			if err := notifyutil.WriteFriendRequestNotification(context.Background(), db.RDB, requestID, req.ToUid, greeting); err != nil {
				log.Printf("[PM] Failed to write friend request notification for request %d to agent %d: %v", requestID, req.ToUid, err)
			}
		}()
	} else {
		log.Printf("[Relation] SendFriendRequest auto-accepted mutual request: request_id=%d from=%d to=%d", requestID, req.FromUid, req.ToUid)
	}
	s.deletePendingFriendRequestNotifications(deletions)
	_ = relations.InvalidateFriendCache(ctx, db.RDB, req.FromUid)
	_ = relations.InvalidateFriendCache(ctx, db.RDB, req.ToUid)

	return &pm.SendFriendRequestResp{RequestId: requestID, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) HandleFriendRequest(ctx context.Context, req *pm.HandleFriendRequestReq) (*pm.HandleFriendRequestResp, error) {
	log.Printf("[Relation] HandleFriendRequest: request_id=%d agent=%d action=%v", req.RequestId, req.AgentId, req.Action)

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
			log.Printf("[Relation] HandleFriendRequest failed: request_id=%d agent=%d err=%v", req.RequestId, req.AgentId, err)
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
		log.Printf("[Relation] FriendRequest accepted: request_id=%d from=%d to=%d", req.RequestId, friendReq.FromUID, friendReq.ToUID)
		go func() {
			if err := notifyutil.WriteFriendResponseNotification(context.Background(), db.RDB, req.RequestId, friendReq.FromUID, responseNotifType, reason); err != nil {
				log.Printf("[PM] Failed to write friend accepted notification for request %d to agent %d: %v", req.RequestId, friendReq.FromUID, err)
			}
		}()
	case "friend_rejected":
		log.Printf("[Relation] FriendRequest rejected: request_id=%d by=%d", req.RequestId, req.AgentId)
		go func() {
			if err := notifyutil.WriteFriendResponseNotification(context.Background(), db.RDB, req.RequestId, friendReq.FromUID, responseNotifType, reason); err != nil {
				log.Printf("[PM] Failed to write friend rejected notification for request %d to agent %d: %v", req.RequestId, friendReq.FromUID, err)
			}
		}()
	default:
		log.Printf("[Relation] FriendRequest cancelled: request_id=%d by=%d", req.RequestId, req.AgentId)
	}

	return &pm.HandleFriendRequestResp{BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) Unfriend(ctx context.Context, req *pm.UnfriendReq) (*pm.UnfriendResp, error) {
	log.Printf("[Relation] Unfriend: from=%d to=%d", req.FromUid, req.ToUid)

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
	log.Printf("[Relation] Unfriend done: from=%d to=%d", req.FromUid, req.ToUid)
	return &pm.UnfriendResp{BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) BlockUser(ctx context.Context, req *pm.BlockUserReq) (*pm.BlockUserResp, error) {
	log.Printf("[Relation] BlockUser: from=%d to=%d", req.FromUid, req.ToUid)

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
	log.Printf("[Relation] BlockUser done: from=%d to=%d", req.FromUid, req.ToUid)
	return &pm.BlockUserResp{BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *PMServiceImpl) UnblockUser(ctx context.Context, req *pm.UnblockUserReq) (*pm.UnblockUserResp, error) {
	log.Printf("[Relation] UnblockUser: from=%d to=%d", req.FromUid, req.ToUid)

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
	log.Printf("[Relation] UnblockUser done: from=%d to=%d", req.FromUid, req.ToUid)
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

	requests, err := dal.ListFriendRequests(db.DB, req.AgentId, req.Direction, cursor, limit)
	if err != nil {
		log.Printf("[Relation] ListFriendRequests failed: agent=%d direction=%s err=%v", req.AgentId, req.Direction, err)
		return &pm.ListFriendRequestsResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to list"}}, nil
	}
	log.Printf("[Relation] ListFriendRequests: agent=%d direction=%s count=%d", req.AgentId, req.Direction, len(requests))

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
	cursor := req.GetCursor()

	friends, err := dal.ListFriends(db.DB, req.AgentId, cursor, limit)
	if err != nil {
		log.Printf("[Relation] ListFriends failed: agent=%d err=%v", req.AgentId, err)
		return &pm.ListFriendsResp{BaseResp: &base.BaseResp{Code: 500, Msg: "failed to list"}}, nil
	}
	log.Printf("[Relation] ListFriends: agent=%d count=%d", req.AgentId, len(friends))

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
	log.Printf("[Relation] UpdateFriendRemark: agent=%d friend=%d", req.AgentId, req.FriendUid)

	remark := truncateByWeightedLength(req.Remark, 100)

	if err := dal.UpdateFriendRemark(db.DB, req.AgentId, req.FriendUid, remark); err != nil {
		log.Printf("[Relation] UpdateFriendRemark failed: agent=%d friend=%d err=%v", req.AgentId, req.FriendUid, err)
		return &pm.UpdateFriendRemarkResp{BaseResp: &base.BaseResp{Code: 400, Msg: err.Error()}}, nil
	}

	log.Printf("[Relation] UpdateFriendRemark done: agent=%d friend=%d", req.AgentId, req.FriendUid)
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
				log.Printf("[PM] Failed to delete friend request notification %d for agent %d: %v", item.requestID, item.agentID, err)
			}
		}
	}()
}
