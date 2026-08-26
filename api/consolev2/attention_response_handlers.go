package consolev2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"
)

type respondAttentionRequest struct {
	ActionKey            string `json:"action_key"`
	ExpectedItemRevision int64  `json:"expected_item_revision"`
	IdempotencyKey       string `json:"idempotency_key"`
}

func attentionActionDisplay(flag, language string) string {
	zh := map[string]string{
		"approve_first_contact": "允许首次联系", "observe_first": "先观察",
		"apply_goal_update": "更新为近期重点", "keep_goal": "保持当前目标",
		"apply_intent_update": "接受意图更新", "keep_intent": "保持当前意图",
		"follow_up": "继续跟进", "not_interested": "不感兴趣", "open_source": "原始活动记录",
		"ask_agent_contact": "帮我建立联系", "add_watch": "加入观察重点",
		"ask_agent_summarize": "帮我总结影响", "draft_broadcast": "把它发展成广播",
	}
	en := map[string]string{
		"approve_first_contact": "Allow first contact", "observe_first": "Observe first",
		"apply_goal_update": "Update recent focus", "keep_goal": "Keep current goal",
		"apply_intent_update": "Accept intent update", "keep_intent": "Keep current intent",
		"follow_up": "Follow up", "not_interested": "Not interested", "open_source": "Original activity",
		"ask_agent_contact": "Help me connect", "add_watch": "Add to watchlist",
		"ask_agent_summarize": "Summarize the impact", "draft_broadcast": "Develop into a broadcast",
	}
	if language == "zh-CN" {
		return zh[flag]
	}
	return en[flag]
}

func attentionActionTerminal(action attentionProtocolAction) bool {
	return action.Flag != "open_source"
}

func loadAttentionResponseReplay(tx *gorm.DB, agentID, attentionID, expectedRevision int64, actionKey string) (int64, string, string, bool, error) {
	var row struct {
		Actions      string `gorm:"column:actions_snapshot"`
		SourceRef    string `gorm:"column:source_ref"`
		Status       string `gorm:"column:status"`
		ItemRevision int64  `gorm:"column:item_revision"`
	}
	result := tx.Raw(`SELECT actions_snapshot::text AS actions_snapshot,
		source_ref::text AS source_ref, status, item_revision FROM agent_attention_items
		WHERE agent_id = ? AND attention_id = ? AND producer = 'agent'
		  AND protocol_version = 'agent_attention.v1'`, agentID, attentionID).Scan(&row)
	if result.Error != nil {
		return 0, "", "", false, result.Error
	}
	if result.RowsAffected != 1 || expectedRevision <= 0 {
		return 0, "", "", false, errConflict
	}
	var actions []attentionProtocolAction
	if json.Unmarshal([]byte(row.Actions), &actions) != nil {
		return 0, "", "", false, errConflict
	}
	selectedFlag := ""
	for _, action := range actions {
		if action.ActionKey == actionKey {
			selectedFlag = action.Flag
			break
		}
	}
	if selectedFlag == "" {
		return 0, "", "", false, errConflict
	}
	var source map[string]interface{}
	sourceAvailable := json.Unmarshal([]byte(row.SourceRef), &source) == nil && len(source) > 0
	return row.ItemRevision, row.Status, selectedFlag, sourceAvailable, nil
}

func (s *Service) respondAttentionItem(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	attentionID, parseErr := strconv.ParseInt(c.Param("attention_id"), 10, 64)
	var req respondAttentionRequest
	if parseErr != nil || attentionID <= 0 || decodeBody(c, &req) != nil ||
		!attentionActionKeyPattern.MatchString(req.ActionKey) || req.ExpectedItemRevision <= 0 || !validAttentionID(req.IdempotencyKey) {
		fail(c, http.StatusBadRequest, "INVALID_ATTENTION_RESPONSE", "attention response is invalid", nil)
		return
	}
	requestHash := hashString(fmt.Sprintf("%d\x00%d\x00%s", attentionID, req.ExpectedItemRevision, req.ActionKey))
	now := time.Now().UnixMilli()
	var commandID, itemRevision int64
	var itemStatus, commandStatus, selectedFlag string
	var replay, sourceAvailable bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var prior struct {
			CommandID   int64  `gorm:"column:command_id"`
			PayloadHash string `gorm:"column:payload_hash"`
			Status      string `gorm:"column:status"`
		}
		if err := tx.Raw(`SELECT command_id, payload_hash, status FROM agent_commands
			WHERE agent_id = ? AND idempotency_key = ?`, agentIDValue, req.IdempotencyKey).Scan(&prior).Error; err != nil {
			return err
		}
		if prior.CommandID != 0 {
			if prior.PayloadHash != requestHash {
				return errConflict
			}
			commandID, commandStatus, replay = prior.CommandID, prior.Status, true
			var loadErr error
			itemRevision, itemStatus, selectedFlag, sourceAvailable, loadErr = loadAttentionResponseReplay(
				tx, agentIDValue, attentionID, req.ExpectedItemRevision, req.ActionKey)
			return loadErr
		}

		var item struct {
			Producer       string `gorm:"column:producer"`
			Status         string `gorm:"column:status"`
			Surface        string `gorm:"column:surface"`
			Category       string `gorm:"column:category"`
			Language       string `gorm:"column:language"`
			Title          string `gorm:"column:title"`
			Body           string `gorm:"column:body"`
			Recommendation string `gorm:"column:recommendation"`
			Actions        string `gorm:"column:actions_snapshot"`
			ContextRef     string `gorm:"column:context_ref"`
			PayloadHash    string `gorm:"column:payload_hash"`
			Revision       int64  `gorm:"column:item_revision"`
			ExpiresAt      *int64 `gorm:"column:expires_at"`
			SourceRef      string `gorm:"column:source_ref"`
		}
		if err := tx.Raw(`SELECT producer, status, surface, category, language, title, body, recommendation,
			actions_snapshot::text AS actions_snapshot, context_ref::text AS context_ref,
			payload_hash, item_revision, expires_at, source_ref::text AS source_ref
			FROM agent_attention_items WHERE agent_id = ? AND attention_id = ? AND producer = 'agent'
			  AND protocol_version = 'agent_attention.v1'
			  AND expires_at > (extract(epoch FROM clock_timestamp())*1000)::bigint FOR UPDATE`,
			agentIDValue, attentionID).Scan(&item).Error; err != nil {
			return err
		}
		if item.Producer != "agent" || item.Revision == 0 || item.Revision != req.ExpectedItemRevision ||
			item.Status != "open" || (item.ExpiresAt != nil && *item.ExpiresAt <= now) {
			return errConflict
		}
		var actions []attentionProtocolAction
		if json.Unmarshal([]byte(item.Actions), &actions) != nil {
			return errConflict
		}
		var selected *attentionProtocolAction
		for index := range actions {
			if actions[index].ActionKey == req.ActionKey {
				selected = &actions[index]
				break
			}
		}
		if selected == nil {
			return errConflict
		}
		if selected.Flag == "open_source" {
			return errAttentionSourceDirect
		}
		selectedFlag = selected.Flag
		terminal := attentionActionTerminal(*selected)
		var contextRef attentionContextRef
		if json.Unmarshal([]byte(item.ContextRef), &contextRef) != nil {
			return errConflict
		}
		if selected.Flag == "apply_intent_update" && contextRef.Operation == "add" {
			var active int64
			if err := tx.Raw(`SELECT COUNT(*) FROM agent_intent_actions WHERE agent_id = ? AND status = 'active'`, agentIDValue).Scan(&active).Error; err != nil {
				return err
			}
			if active >= 10 {
				return errIntentCapacity
			}
		}
		var contextRevision int64
		if err := tx.Raw(`SELECT active_revision FROM agent_context_heads WHERE agent_id = ? FOR UPDATE`, agentIDValue).Scan(&contextRevision).Error; err != nil {
			return err
		}
		if contextRevision <= 0 {
			return errConflict
		}
		if terminal && contextRef.ContextRevision != nil && *contextRef.ContextRevision != contextRevision {
			return errAttentionContextStale
		}
		if terminal && item.Category == "goal_calibration" && contextRef.NetworkGoalRevision != nil {
			var currentGoalVersion int64
			if err := tx.Raw(`SELECT version FROM agent_network_goals
				WHERE agent_id = ? AND status = 'active' LIMIT 1`, agentIDValue).Scan(&currentGoalVersion).Error; err != nil {
				return err
			}
			if currentGoalVersion == 0 || currentGoalVersion != *contextRef.NetworkGoalRevision {
				return errAttentionContextStale
			}
		}
		displayText := selected.Flag
		if selected.Kind == "preset" {
			displayText = attentionActionDisplay(selected.Flag, item.Language)
		}
		var sourceRef map[string]interface{}
		if json.Unmarshal([]byte(item.SourceRef), &sourceRef) != nil {
			return errConflict
		}
		payload := map[string]interface{}{
			"protocol_version": attentionProtocolVersion,
			"command_type":     "attention_response", "attention_id": fmt.Sprintf("%d", attentionID),
			"selected_action":         map[string]interface{}{"action_key": selected.ActionKey, "kind": selected.Kind, "flag": selected.Flag, "display_text": displayText},
			"attention_snapshot_hash": "sha256:" + item.PayloadHash, "context_revision": contextRevision,
			"attention_snapshot": map[string]interface{}{
				"title": item.Title, "body": item.Body, "recommendation": item.Recommendation,
				"source_ref": sourceRef, "category": item.Category, "surface": item.Surface,
				"language": item.Language, "actions": actions, "context_ref": contextRef,
			},
			"context_ref": contextRef, "selected_at": now, "terminal": terminal,
		}
		payloadBytes, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return marshalErr
		}
		if err := tx.Raw(`INSERT INTO agent_commands
			(agent_id, attention_id, command_type, payload, payload_hash, required_context_revision,
			 status, idempotency_key, created_at)
			VALUES (?, ?, 'attention_response', ?::jsonb, ?, ?, 'pending', ?, ?) RETURNING command_id`,
			agentIDValue, attentionID, string(payloadBytes), requestHash, contextRevision, req.IdempotencyKey, now).Scan(&commandID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO control_wakeup_outbox
			(agent_id, event_type, entity_id, payload, status, next_attempt_at, created_at)
			VALUES (?, 'command_available', ?, jsonb_build_object('command_id', CAST(? AS text)), 'pending', ?, ?)`,
			agentIDValue, commandID, fmt.Sprintf("%d", commandID), now, now).Error; err != nil {
			return err
		}
		itemStatus = "open"
		if terminal {
			itemStatus = "pending"
		}
		if err := tx.Raw(`UPDATE agent_attention_items SET status = ?, selected_action_key = ?,
			response_status = 'pending', responded_at = ?, updated_at = ?, item_revision = item_revision + 1
			WHERE agent_id = ? AND attention_id = ? AND producer = 'agent'
			  AND protocol_version = 'agent_attention.v1'
			  AND status = 'open' AND item_revision = ?
			  AND expires_at > (extract(epoch FROM clock_timestamp())*1000)::bigint
			RETURNING item_revision`, itemStatus, selected.ActionKey, now, now, agentIDValue, attentionID,
			req.ExpectedItemRevision).Scan(&itemRevision).Error; err != nil {
			return err
		}
		if itemRevision == 0 {
			return errConflict
		}
		commandStatus = "pending"
		var source map[string]interface{}
		sourceAvailable = json.Unmarshal([]byte(item.SourceRef), &source) == nil && len(source) > 0
		return nil
	})
	if errors.Is(err, errIntentCapacity) {
		fail(c, http.StatusConflict, "INTENT_CAPACITY_REACHED", "active intent limit has been reached", nil)
		return
	}
	if errors.Is(err, errAttentionContextStale) {
		fail(c, http.StatusConflict, "ATTENTION_CONTEXT_STALE", "Attention context changed; refresh before responding", nil)
		return
	}
	if errors.Is(err, errAttentionSourceDirect) {
		fail(c, http.StatusBadRequest, "ATTENTION_SOURCE_DIRECT_ONLY", "open_source must use the authenticated source endpoint", nil)
		return
	}
	if errors.Is(err, errConflict) || isUniqueViolation(err) {
		var existing struct {
			CommandID   int64  `gorm:"column:command_id"`
			PayloadHash string `gorm:"column:payload_hash"`
			Status      string `gorm:"column:status"`
		}
		if readErr := s.db.Raw(`SELECT command_id, payload_hash, status FROM agent_commands WHERE agent_id = ? AND idempotency_key = ?`, agentIDValue, req.IdempotencyKey).Scan(&existing).Error; readErr == nil && existing.CommandID != 0 && existing.PayloadHash == requestHash {
			commandID, commandStatus, replay = existing.CommandID, existing.Status, true
			var loadErr error
			itemRevision, itemStatus, selectedFlag, sourceAvailable, loadErr = loadAttentionResponseReplay(
				s.db, agentIDValue, attentionID, req.ExpectedItemRevision, req.ActionKey)
			if loadErr != nil {
				fail(c, http.StatusInternalServerError, "ATTENTION_RESPONSE_FAILED", "could not read Attention response state", nil)
				return
			}
		} else {
			fail(c, http.StatusConflict, "ATTENTION_RESPONSE_CONFLICT", "Attention item changed or action was already selected", nil)
			return
		}
	} else if err != nil {
		fail(c, http.StatusInternalServerError, "ATTENTION_RESPONSE_FAILED", "could not record Attention response", nil)
		return
	}
	reply(c, map[bool]int{true: http.StatusOK, false: http.StatusAccepted}[replay], map[string]interface{}{
		"attention_id": fmt.Sprintf("%d", attentionID), "item_revision": itemRevision, "status": itemStatus,
		"selected_action_key": req.ActionKey, "selected_flag": selectedFlag,
		"command_id": fmt.Sprintf("%d", commandID), "command_status": commandStatus,
		"source_available": sourceAvailable, "replay": replay,
	})
}

var (
	errIntentCapacity        = errors.New("intent capacity reached")
	errAttentionContextStale = errors.New("Attention context is stale")
	errAttentionSourceDirect = errors.New("Attention source must be opened directly")
)

func (s *Service) getAttentionSource(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	attentionID, err := strconv.ParseInt(c.Param("attention_id"), 10, 64)
	if err != nil || attentionID <= 0 {
		fail(c, http.StatusBadRequest, "INVALID_ATTENTION_ID", "attention_id is invalid", nil)
		return
	}
	var stored string
	storedResult := s.db.Raw(`SELECT source_ref::text FROM agent_attention_items
		WHERE agent_id = ? AND attention_id = ? AND producer = 'agent'
		  AND protocol_version = 'agent_attention.v1'
		  AND expires_at > (extract(epoch FROM clock_timestamp())*1000)::bigint`, agentIDValue, attentionID).Scan(&stored)
	if storedResult.Error != nil {
		fail(c, http.StatusInternalServerError, "ATTENTION_SOURCE_READ_FAILED", "could not read Attention source", nil)
		return
	}
	if storedResult.RowsAffected != 1 {
		fail(c, http.StatusNotFound, "ATTENTION_SOURCE_NOT_FOUND", "Attention source is unavailable", nil)
		return
	}
	var source attentionSourceRef
	if json.Unmarshal([]byte(stored), &source) != nil || !attentionSourceTypes[source.Type] {
		fail(c, http.StatusNotFound, "ATTENTION_SOURCE_NOT_FOUND", "Attention source is unavailable", nil)
		return
	}
	sourceID, parseErr := strconv.ParseInt(source.ID, 10, 64)
	if parseErr != nil || sourceID <= 0 {
		fail(c, http.StatusNotFound, "ATTENTION_SOURCE_NOT_FOUND", "Attention source is unavailable", nil)
		return
	}
	detail := map[string]interface{}{}
	found := false
	peerAgentID := int64(0)
	switch source.Type {
	case "broadcast":
		var row struct {
			Content       string `gorm:"column:content"`
			Summary       string `gorm:"column:summary"`
			URL           string `gorm:"column:url"`
			AuthorID      int64  `gorm:"column:author_id"`
			UpdatedAt     int64  `gorm:"column:updated_at"`
			ConsumedCount int64  `gorm:"column:consumed_count"`
			HelpfulCount  int64  `gorm:"column:helpful_count"`
			TotalScore    int64  `gorm:"column:total_score"`
		}
		result := s.db.Raw(`SELECT raw.raw_content AS content, processed.summary, raw.raw_url AS url,
			raw.author_agent_id AS author_id, processed.updated_at,
			COALESCE(stats.consumed_count, 0) AS consumed_count,
			COALESCE(stats.score_1_count, 0) + COALESCE(stats.score_2_count, 0) AS helpful_count,
			COALESCE(stats.total_score, 0) AS total_score
			FROM agent_feed_exposures exposure
			JOIN raw_items raw ON raw.item_id = exposure.source_id
			JOIN processed_items processed ON processed.item_id = exposure.source_id
			LEFT JOIN item_stats stats ON stats.item_id = exposure.source_id
			WHERE exposure.agent_id = ? AND exposure.source_type = 'broadcast' AND exposure.source_id = ?`, agentIDValue, sourceID).Scan(&row)
		err = result.Error
		found = result.RowsAffected == 1 && row.AuthorID > 0
		detail = map[string]interface{}{
			"content": row.Content, "summary": row.Summary, "url": row.URL,
			"author_agent_id": fmt.Sprintf("%d", row.AuthorID), "updated_at": row.UpdatedAt,
			"interaction": map[string]interface{}{
				"consumed_count": row.ConsumedCount, "helpful_count": row.HelpfulCount, "total_score": row.TotalScore,
			},
		}
		if found {
			identities, identityErr := s.resolveIdentityAssertions([]int64{row.AuthorID})
			if identityErr != nil {
				err = identityErr
				break
			}
			if identity, exists := identities[row.AuthorID]; exists {
				detail["author_identity"] = identity
			}
			relations, relationErr := s.loadViewerRelations(agentIDValue, []int64{row.AuthorID})
			if relationErr != nil {
				err = relationErr
				break
			}
			authorRelation := relations[row.AuthorID]
			if authorRelation == "" {
				authorRelation = "none"
			}
			detail["author_relation"] = authorRelation

			const replyLimit = 20
			var replyRows []struct {
				MessageID      int64  `gorm:"column:msg_id"`
				ConversationID int64  `gorm:"column:conv_id"`
				SenderID       int64  `gorm:"column:sender_id"`
				ReceiverID     int64  `gorm:"column:receiver_id"`
				Content        string `gorm:"column:content"`
				CreatedAt      int64  `gorm:"column:created_at"`
			}
			repliesResult := s.db.Raw(`WITH source_conversations AS (
				SELECT conversation.conv_id
				FROM conversations conversation
				WHERE conversation.origin_type = 'broadcast' AND conversation.origin_id = ?
				  AND conversation.status = 0 AND conversation.participant_a = ?
				UNION
				SELECT conversation.conv_id
				FROM conversations conversation
				WHERE conversation.origin_type = 'broadcast' AND conversation.origin_id = ?
				  AND conversation.status = 0 AND conversation.participant_b = ?
			) SELECT message.msg_id, message.conv_id, message.sender_id,
				message.receiver_id, message.content, message.created_at
				FROM source_conversations source
				JOIN private_messages message ON message.conv_id = source.conv_id
				ORDER BY message.msg_id DESC LIMIT ?`, sourceID, agentIDValue, sourceID, agentIDValue, replyLimit+1).Scan(&replyRows)
			if repliesResult.Error != nil {
				err = repliesResult.Error
				break
			}
			hasMoreReplies := len(replyRows) > replyLimit
			if hasMoreReplies {
				replyRows = replyRows[:replyLimit]
			}
			replies := make([]map[string]interface{}, 0, len(replyRows))
			for _, replyRow := range replyRows {
				content, truncated := truncateRunes(replyRow.Content, 2000)
				replies = append(replies, map[string]interface{}{
					"message_id":      strconv.FormatInt(replyRow.MessageID, 10),
					"conversation_id": strconv.FormatInt(replyRow.ConversationID, 10),
					"sender_id":       strconv.FormatInt(replyRow.SenderID, 10),
					"receiver_id":     strconv.FormatInt(replyRow.ReceiverID, 10),
					"content":         content, "content_truncated": truncated, "created_at": replyRow.CreatedAt,
				})
			}
			detail["related_replies"] = replies
			detail["related_replies_has_more"] = hasMoreReplies
		}
	case "broadcast_reply":
		parentID, ok := parseOptionalPositiveID(source.ParentID)
		if !ok {
			fail(c, http.StatusNotFound, "ATTENTION_SOURCE_NOT_FOUND", "Attention source is unavailable", nil)
			return
		}
		var row struct {
			ReplyMessageID      int64  `gorm:"column:reply_message_id"`
			ConversationID      int64  `gorm:"column:conversation_id"`
			ReplySenderID       int64  `gorm:"column:reply_sender_id"`
			ReplyReceiverID     int64  `gorm:"column:reply_receiver_id"`
			ReplyContent        string `gorm:"column:reply_content"`
			ReplyCreatedAt      int64  `gorm:"column:reply_created_at"`
			ParentItemID        int64  `gorm:"column:parent_item_id"`
			ParentContent       string `gorm:"column:parent_content"`
			ParentSummary       string `gorm:"column:parent_summary"`
			ParentURL           string `gorm:"column:parent_url"`
			ParentAuthorAgentID int64  `gorm:"column:parent_author_agent_id"`
			ParentUpdatedAt     int64  `gorm:"column:parent_updated_at"`
		}
		result := s.db.Raw(`SELECT message.msg_id AS reply_message_id,
			message.conv_id AS conversation_id, message.sender_id AS reply_sender_id,
			message.receiver_id AS reply_receiver_id, message.content AS reply_content,
			message.created_at AS reply_created_at, raw.item_id AS parent_item_id,
			raw.raw_content AS parent_content, processed.summary AS parent_summary,
			raw.raw_url AS parent_url, raw.author_agent_id AS parent_author_agent_id,
			processed.updated_at AS parent_updated_at
			FROM private_messages message
			JOIN conversations conversation ON conversation.conv_id = message.conv_id
			JOIN agent_feed_exposures exposure ON exposure.agent_id = ?
			 AND exposure.source_type = 'broadcast' AND exposure.source_id = ?
			JOIN raw_items raw ON raw.item_id = exposure.source_id
			JOIN processed_items processed ON processed.item_id = exposure.source_id
			WHERE message.msg_id = ? AND conversation.origin_type = 'broadcast'
			  AND conversation.origin_id > 0
			  AND conversation.status = 0
			  AND conversation.origin_id = ?
			  AND (message.sender_id = ? OR message.receiver_id = ?)`,
			agentIDValue, parentID, sourceID, parentID, agentIDValue, agentIDValue).Scan(&row)
		err = result.Error
		found = result.RowsAffected == 1 && row.ReplyMessageID > 0 && row.ParentItemID == parentID
		parentContent, parentTruncated := truncateRunes(row.ParentContent, 12000)
		replyContent, replyTruncated := truncateRunes(row.ReplyContent, 2000)
		detail = map[string]interface{}{
			"parent_broadcast": map[string]interface{}{
				"source_ref": map[string]interface{}{"type": "broadcast", "id": strconv.FormatInt(row.ParentItemID, 10)},
				"content":    parentContent, "content_truncated": parentTruncated,
				"summary": row.ParentSummary, "url": row.ParentURL,
				"author_agent_id": strconv.FormatInt(row.ParentAuthorAgentID, 10), "updated_at": row.ParentUpdatedAt,
			},
			"reply": map[string]interface{}{
				"message_id":      strconv.FormatInt(row.ReplyMessageID, 10),
				"conversation_id": strconv.FormatInt(row.ConversationID, 10),
				"sender_id":       strconv.FormatInt(row.ReplySenderID, 10),
				"receiver_id":     strconv.FormatInt(row.ReplyReceiverID, 10),
				"content":         replyContent, "content_truncated": replyTruncated, "created_at": row.ReplyCreatedAt,
			},
		}
		if found {
			participantIDs := uniqueInt64([]int64{row.ParentAuthorAgentID, row.ReplySenderID, row.ReplyReceiverID})
			identities, identityErr := s.resolveIdentityAssertions(participantIDs)
			if identityErr != nil {
				err = identityErr
				break
			}
			identityDetail := make(map[string]interface{}, len(identities))
			for id, identity := range identities {
				identityDetail[strconv.FormatInt(id, 10)] = identity
			}
			detail["agent_identities"] = identityDetail
			peerAgentID = row.ReplySenderID
			if peerAgentID == agentIDValue {
				peerAgentID = row.ReplyReceiverID
			}
		}
	case "private_message":
		var row struct {
			MessageID      int64  `gorm:"column:msg_id"`
			ConversationID int64  `gorm:"column:conv_id"`
			SenderID       int64  `gorm:"column:sender_id"`
			ReceiverID     int64  `gorm:"column:receiver_id"`
			Content        string `gorm:"column:content"`
			CreatedAt      int64  `gorm:"column:created_at"`
			OriginID       *int64 `gorm:"column:origin_id"`
		}
		query := `SELECT message.msg_id, message.conv_id, message.sender_id, message.receiver_id, message.content,
			message.created_at, conversation.origin_id FROM private_messages message
			JOIN conversations conversation ON conversation.conv_id = message.conv_id
			WHERE message.msg_id = ? AND (message.sender_id = ? OR message.receiver_id = ?)`
		result := s.db.Raw(query, sourceID, agentIDValue, agentIDValue).Scan(&row)
		err = result.Error
		found = result.RowsAffected == 1 && row.MessageID > 0
		content, truncated := truncateRunes(row.Content, 2000)
		detail = map[string]interface{}{"message_id": fmt.Sprintf("%d", row.MessageID), "conversation_id": fmt.Sprintf("%d", row.ConversationID), "sender_id": fmt.Sprintf("%d", row.SenderID), "receiver_id": fmt.Sprintf("%d", row.ReceiverID), "content": content, "content_truncated": truncated, "created_at": row.CreatedAt, "origin_id": row.OriginID}
	case "friend_request":
		var row struct {
			ID        int64  `gorm:"column:id"`
			FromID    int64  `gorm:"column:from_uid"`
			ToID      int64  `gorm:"column:to_uid"`
			Status    int16  `gorm:"column:status"`
			Greeting  string `gorm:"column:greeting"`
			CreatedAt int64  `gorm:"column:created_at"`
		}
		result := s.db.Raw(`SELECT id, from_uid, to_uid, status, greeting, created_at FROM friend_requests
			WHERE id = ? AND (from_uid = ? OR to_uid = ?)`, sourceID, agentIDValue, agentIDValue).Scan(&row)
		err = result.Error
		found = result.RowsAffected == 1 && row.ID > 0
		peerAgentID = row.FromID
		if peerAgentID == agentIDValue {
			peerAgentID = row.ToID
		}
		requestStatus := map[int16]string{0: "pending", 1: "accepted", 2: "rejected"}[row.Status]
		if requestStatus == "" {
			requestStatus = "unknown"
		}
		detail = map[string]interface{}{"request_id": fmt.Sprintf("%d", row.ID), "from_agent_id": fmt.Sprintf("%d", row.FromID), "to_agent_id": fmt.Sprintf("%d", row.ToID), "status": requestStatus, "greeting": row.Greeting, "created_at": row.CreatedAt}
	case "relation":
		var row struct {
			ID        int64 `gorm:"column:id"`
			FromID    int64 `gorm:"column:from_uid"`
			ToID      int64 `gorm:"column:to_uid"`
			Relation  int16 `gorm:"column:rel_type"`
			CreatedAt int64 `gorm:"column:created_at"`
		}
		result := s.db.Raw(`SELECT id, from_uid, to_uid, rel_type, created_at FROM user_relations
			WHERE id = ? AND (from_uid = ? OR to_uid = ?)`, sourceID, agentIDValue, agentIDValue).Scan(&row)
		err = result.Error
		found = result.RowsAffected == 1 && row.ID > 0
		peerAgentID = row.FromID
		if peerAgentID == agentIDValue {
			peerAgentID = row.ToID
		}
		detail = map[string]interface{}{"relation_id": fmt.Sprintf("%d", row.ID), "from_agent_id": fmt.Sprintf("%d", row.FromID), "to_agent_id": fmt.Sprintf("%d", row.ToID), "relation_type": row.Relation, "created_at": row.CreatedAt}
	case "context":
		var row struct {
			GeneratedRevision int64  `gorm:"column:generated_revision"`
			GeneratedContext  string `gorm:"column:generated_context"`
			GeneratedAt       int64  `gorm:"column:generated_at"`
			CurrentRevision   int64  `gorm:"column:current_revision"`
			CurrentContext    string `gorm:"column:current_context"`
			CurrentAt         int64  `gorm:"column:current_at"`
		}
		result := s.db.Raw(`SELECT generated.revision AS generated_revision,
			generated.compiled_context::text AS generated_context,
			generated.generated_at, current.revision AS current_revision,
			current.compiled_context::text AS current_context,
			current.generated_at AS current_at
			FROM agent_context_revisions generated
			JOIN agent_context_heads head ON head.agent_id = generated.agent_id
			JOIN agent_context_revisions current ON current.agent_id = head.agent_id
			 AND current.revision = head.active_revision
			WHERE generated.agent_id = ? AND generated.revision = ?`, agentIDValue, sourceID).Scan(&row)
		err = result.Error
		found = result.RowsAffected == 1 && row.GeneratedRevision > 0 && row.CurrentRevision > 0
		var generatedContext, currentContext interface{}
		_ = json.Unmarshal([]byte(row.GeneratedContext), &generatedContext)
		_ = json.Unmarshal([]byte(row.CurrentContext), &currentContext)
		detail = map[string]interface{}{
			"generated_context": map[string]interface{}{
				"revision": row.GeneratedRevision, "compiled_context": generatedContext, "generated_at": row.GeneratedAt,
			},
			"current_context": map[string]interface{}{
				"revision": row.CurrentRevision, "compiled_context": currentContext, "generated_at": row.CurrentAt,
			},
			"is_current": row.GeneratedRevision == row.CurrentRevision,
		}
	case "activity":
		var row struct {
			LogID     int64  `gorm:"column:log_id"`
			EventType string `gorm:"column:event_type"`
			Summary   string `gorm:"column:summary"`
			Detail    string `gorm:"column:detail"`
			CreatedAt int64  `gorm:"column:created_at"`
		}
		result := s.db.Raw(`SELECT log_id, event_type, summary, detail::text AS detail, created_at
			FROM agent_activity_log WHERE agent_id = ? AND log_id = ?`, agentIDValue, sourceID).Scan(&row)
		err = result.Error
		found = result.RowsAffected == 1 && row.LogID > 0
		var eventDetail interface{}
		_ = json.Unmarshal([]byte(row.Detail), &eventDetail)
		detail = map[string]interface{}{"log_id": fmt.Sprintf("%d", row.LogID), "event_type": row.EventType, "summary": row.Summary, "detail": eventDetail, "created_at": row.CreatedAt}
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "ATTENTION_SOURCE_READ_FAILED", "could not read Attention source", nil)
		return
	}
	if !found || len(detail) == 0 {
		fail(c, http.StatusNotFound, "ATTENTION_SOURCE_NOT_FOUND", "Attention source is unavailable", nil)
		return
	}
	if peerAgentID > 0 {
		relations := map[int64]string{peerAgentID: "pending"}
		if source.Type == "relation" {
			relations, err = s.loadViewerRelations(agentIDValue, []int64{peerAgentID})
			if err != nil {
				fail(c, http.StatusInternalServerError, "ATTENTION_SOURCE_READ_FAILED", "could not read Attention relation state", nil)
				return
			}
		}
		contexts, contextErr := s.loadCommunicationContexts(agentIDValue, []int64{peerAgentID}, relations)
		if contextErr != nil {
			fail(c, http.StatusInternalServerError, "ATTENTION_SOURCE_READ_FAILED", "could not read Attention Agent summary", nil)
			return
		}
		if agentContext, exists := contexts[strconv.FormatInt(peerAgentID, 10)]; exists {
			detail["agent_context"] = agentContext
		}
		if source.Type == "relation" {
			var recent communicationMessage
			recentResult := s.db.Raw(`SELECT message.msg_id, message.conv_id, message.sender_id,
				message.receiver_id, message.content, message.is_read, message.created_at
				FROM conversations conversation
				JOIN private_messages message ON message.conv_id = conversation.conv_id
				WHERE conversation.status = 0
				  AND ((conversation.participant_a = ? AND conversation.participant_b = ?)
				    OR (conversation.participant_a = ? AND conversation.participant_b = ?))
				ORDER BY message.msg_id DESC LIMIT 1`, agentIDValue, peerAgentID, peerAgentID, agentIDValue).Scan(&recent)
			if recentResult.Error != nil {
				fail(c, http.StatusInternalServerError, "ATTENTION_SOURCE_READ_FAILED", "could not read recent relation activity", nil)
				return
			}
			if recentResult.RowsAffected == 1 && recent.MsgID > 0 {
				boundCommunicationMessage(&recent, 2000)
				detail["last_message"] = recent
			}
		}
	}
	reply(c, http.StatusOK, map[string]interface{}{"attention_id": fmt.Sprintf("%d", attentionID), "source_ref": source, "detail": detail})
}
