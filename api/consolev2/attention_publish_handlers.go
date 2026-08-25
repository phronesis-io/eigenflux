package consolev2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"
	redis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	attentionPublishBodyLimit  = 32 << 10
	attentionPublishBatchMax   = 10
	attentionHourlyTotal       = 20
	attentionHourlyParticipate = 4
	attentionHourlyFocus       = 16
	attentionRateWindow        = time.Hour
	attentionTextRetention     = 7 * 24 * time.Hour
)

type attentionSourceRef struct {
	Type     string  `json:"type"`
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id,omitempty"`
}

type attentionContextRef struct {
	ContextRevision     *int64  `json:"context_revision,omitempty"`
	NetworkGoalRevision *int64  `json:"network_goal_revision,omitempty"`
	IntentID            *string `json:"intent_id,omitempty"`
	Operation           string  `json:"operation,omitempty"`
}

type attentionProtocolAction struct {
	ActionKey  string `json:"action_key"`
	Kind       string `json:"kind"`
	Flag       string `json:"flag"`
	Appearance string `json:"appearance"`
}

type attentionPublishItem struct {
	ClientItemID   string                    `json:"client_item_id"`
	Surface        string                    `json:"surface"`
	Category       string                    `json:"category"`
	Language       string                    `json:"language"`
	Title          string                    `json:"title"`
	Body           string                    `json:"body"`
	Recommendation string                    `json:"recommendation,omitempty"`
	SourceRef      *attentionSourceRef       `json:"source_ref,omitempty"`
	ContextRef     attentionContextRef       `json:"context_ref,omitempty"`
	Actions        []attentionProtocolAction `json:"actions"`
	GeneratedAt    int64                     `json:"generated_at"`
	ExpiresAt      int64                     `json:"expires_at"`
	payloadHash    string
	sourceID       int64
}

type attentionPublishRequest struct {
	SchemaVersion  string                 `json:"schema_version"`
	IdempotencyKey string                 `json:"idempotency_key"`
	Items          []attentionPublishItem `json:"items"`
}

type attentionPublishRow struct {
	AttentionID  int64  `gorm:"column:attention_id"`
	ClientItemID string `gorm:"column:client_item_id"`
}

var attentionRateScript = redis.NewScript(`
local now_parts = redis.call('TIME')
local now = tonumber(now_parts[1]) * 1000 + math.floor(tonumber(now_parts[2]) / 1000)
local window = tonumber(ARGV[1])
local total_limit = tonumber(ARGV[2])
local participation_limit = tonumber(ARGV[3])
local focus_limit = tonumber(ARGV[4])

for i = 1, 3 do
  redis.call('ZREMRANGEBYSCORE', KEYS[i], '-inf', now - window)
end

local add_total = 0
local add_participation = 0
local add_focus = 0
for i = 5, #ARGV, 2 do
  local member = ARGV[i]
  local surface = ARGV[i + 1]
  if not redis.call('ZSCORE', KEYS[1], member) then add_total = add_total + 1 end
  if surface == 'participation' and not redis.call('ZSCORE', KEYS[2], member) then
    add_participation = add_participation + 1
  elseif surface == 'focus' and not redis.call('ZSCORE', KEYS[3], member) then
    add_focus = add_focus + 1
  end
end

if redis.call('ZCARD', KEYS[1]) + add_total > total_limit or
   redis.call('ZCARD', KEYS[2]) + add_participation > participation_limit or
   redis.call('ZCARD', KEYS[3]) + add_focus > focus_limit then
	local retry = 1
	local function include_retry(key)
		local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
		if #oldest == 2 then
			retry = math.max(retry, window - (now - tonumber(oldest[2])))
		end
	end
	if redis.call('ZCARD', KEYS[1]) + add_total > total_limit then include_retry(KEYS[1]) end
	if redis.call('ZCARD', KEYS[2]) + add_participation > participation_limit then include_retry(KEYS[2]) end
	if redis.call('ZCARD', KEYS[3]) + add_focus > focus_limit then include_retry(KEYS[3]) end
	return {0, retry}
end

for i = 5, #ARGV, 2 do
  local member = ARGV[i]
  local surface = ARGV[i + 1]
  redis.call('ZADD', KEYS[1], 'NX', now, member)
  if surface == 'participation' then
    redis.call('ZADD', KEYS[2], 'NX', now, member)
  else
    redis.call('ZADD', KEYS[3], 'NX', now, member)
  end
end
for i = 1, 3 do redis.call('PEXPIRE', KEYS[i], window + 60000) end
return {1, 0}
`)

var attentionRateReleaseScript = redis.NewScript(`
for i = 1, #ARGV do
	for key_index = 1, #KEYS do
		redis.call('ZREM', KEYS[key_index], ARGV[i])
	end
end
return 1
`)

var attentionActionKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

var participationCategories = map[string]bool{
	"action_recommendation": true, "goal_calibration": true,
	"intent_update": true, "other_decision": true,
}

var focusCategories = map[string]bool{
	"important_signal": true, "opportunity": true, "relationship_created": true,
	"relationship_feedback": true, "watch_update": true, "other_attention": true,
}

var participationActionFlags = map[string]bool{
	"approve_first_contact": true, "observe_first": true,
	"apply_goal_update": true, "keep_goal": true,
	"apply_intent_update": true, "keep_intent": true,
	"follow_up": true, "not_interested": true,
}

var focusActionFlags = map[string]bool{
	"open_source": true, "ask_agent_contact": true, "add_watch": true,
	"ask_agent_summarize": true, "draft_broadcast": true,
	"follow_up": true, "not_interested": true,
}

var attentionSourceTypes = map[string]bool{
	"broadcast": true, "broadcast_reply": true, "friend_request": true,
	"relation": true, "private_message": true, "context": true, "activity": true,
}

func decodeAttentionPublishBody(c *app.RequestContext) (attentionPublishRequest, []byte, error) {
	raw, err := c.Body()
	if err != nil || len(raw) == 0 || len(raw) > attentionPublishBodyLimit {
		return attentionPublishRequest{}, nil, errors.New("request body must be between 1 byte and 32 KiB")
	}
	var req attentionPublishRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return req, raw, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return req, raw, errors.New("request body must contain one JSON document")
	}
	return req, raw, nil
}

func validAttentionID(value string) bool {
	return telemetryIDPattern.MatchString(value)
}

func containsForbiddenCustomText(value string) bool {
	if strings.ContainsAny(value, "\r\n<>") {
		return true
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func validateAttentionPublish(req *attentionPublishRequest, now int64) error {
	if req.SchemaVersion != "agent_attention.v1" || !validAttentionID(req.IdempotencyKey) ||
		len(req.Items) < 1 || len(req.Items) > attentionPublishBatchMax {
		return errors.New("schema_version, idempotency_key, or items are invalid")
	}
	clientIDs := make(map[string]struct{}, len(req.Items))
	for index := range req.Items {
		item := &req.Items[index]
		if !validAttentionID(item.ClientItemID) {
			return fmt.Errorf("items[%d].client_item_id is invalid", index)
		}
		if _, exists := clientIDs[item.ClientItemID]; exists {
			return fmt.Errorf("items[%d].client_item_id is duplicated", index)
		}
		clientIDs[item.ClientItemID] = struct{}{}
		validCategory := item.Surface == "participation" && participationCategories[item.Category]
		validCategory = validCategory || item.Surface == "focus" && focusCategories[item.Category]
		if !validCategory || (item.Language != "zh-CN" && item.Language != "en") {
			return fmt.Errorf("items[%d].surface, category, or language is invalid", index)
		}
		if strings.TrimSpace(item.Title) == "" || utf8.RuneCountInString(item.Title) > 120 || strings.TrimSpace(item.Body) == "" ||
			utf8.RuneCountInString(item.Body) > 2000 || utf8.RuneCountInString(item.Recommendation) > 1000 ||
			(item.Surface == "participation" && strings.TrimSpace(item.Recommendation) == "") {
			return fmt.Errorf("items[%d] text fields are invalid", index)
		}
		if item.GeneratedAt < 1_000_000_000_000 || item.GeneratedAt > now+int64(5*time.Minute/time.Millisecond) ||
			item.ExpiresAt <= item.GeneratedAt || item.ExpiresAt > item.GeneratedAt+int64(90*24*time.Hour/time.Millisecond) {
			return fmt.Errorf("items[%d] timestamps are invalid", index)
		}
		if item.SourceRef != nil {
			if !attentionSourceTypes[item.SourceRef.Type] || item.SourceRef.ID == "" {
				return fmt.Errorf("items[%d].source_ref is invalid", index)
			}
			parsed, err := strconv.ParseInt(item.SourceRef.ID, 10, 64)
			if err != nil || parsed <= 0 {
				return fmt.Errorf("items[%d].source_ref.id must be a positive decimal identifier", index)
			}
			item.sourceID = parsed
			if item.SourceRef.ParentID != nil {
				if _, ok := parseOptionalPositiveID(item.SourceRef.ParentID); !ok {
					return fmt.Errorf("items[%d].source_ref.parent_id must be a positive decimal identifier", index)
				}
			}
			if item.SourceRef.Type == "broadcast_reply" && item.SourceRef.ParentID == nil {
				return fmt.Errorf("items[%d].source_ref.parent_id is required for broadcast_reply", index)
			}
		}
		if (item.Category == "action_recommendation" || item.Category == "important_signal" ||
			item.Category == "opportunity" || item.Category == "relationship_created" ||
			item.Category == "relationship_feedback") && item.SourceRef == nil {
			return fmt.Errorf("items[%d].source_ref is required", index)
		}
		refPresent := item.ContextRef.ContextRevision != nil || item.ContextRef.NetworkGoalRevision != nil ||
			item.ContextRef.IntentID != nil || item.ContextRef.Operation != ""
		if refPresent {
			if item.ContextRef.ContextRevision == nil || *item.ContextRef.ContextRevision <= 0 ||
				(item.ContextRef.Operation != "" && item.ContextRef.Operation != "add" && item.ContextRef.Operation != "update") {
				return fmt.Errorf("items[%d].context_ref is invalid", index)
			}
			if item.ContextRef.NetworkGoalRevision != nil && *item.ContextRef.NetworkGoalRevision <= 0 {
				return fmt.Errorf("items[%d].context_ref.network_goal_revision is invalid", index)
			}
			if item.ContextRef.IntentID != nil {
				if _, ok := parseOptionalPositiveID(item.ContextRef.IntentID); !ok {
					return fmt.Errorf("items[%d].context_ref.intent_id is invalid", index)
				}
			}
		}
		if item.Category == "goal_calibration" {
			if item.ContextRef.ContextRevision == nil || *item.ContextRef.ContextRevision <= 0 ||
				item.ContextRef.NetworkGoalRevision == nil || *item.ContextRef.NetworkGoalRevision <= 0 ||
				(item.ContextRef.Operation != "" && item.ContextRef.Operation != "update") || item.ContextRef.IntentID != nil {
				return fmt.Errorf("items[%d].context_ref is invalid for goal calibration", index)
			}
		}
		if item.Category == "intent_update" {
			if item.ContextRef.ContextRevision == nil || *item.ContextRef.ContextRevision <= 0 ||
				(item.ContextRef.Operation != "add" && item.ContextRef.Operation != "update") ||
				(item.ContextRef.Operation == "add" && item.ContextRef.IntentID != nil) ||
				(item.ContextRef.Operation == "update" && item.ContextRef.IntentID == nil) {
				return fmt.Errorf("items[%d].context_ref is invalid for intent update", index)
			}
		}
		if len(item.Actions) < 1 || len(item.Actions) > 5 {
			return fmt.Errorf("items[%d].actions must contain 1 to 5 entries", index)
		}
		actionKeys := make(map[string]struct{}, len(item.Actions))
		primaryCount := 0
		for actionIndex := range item.Actions {
			action := &item.Actions[actionIndex]
			if !attentionActionKeyPattern.MatchString(action.ActionKey) {
				return fmt.Errorf("items[%d].actions[%d].action_key is invalid", index, actionIndex)
			}
			if _, exists := actionKeys[action.ActionKey]; exists {
				return fmt.Errorf("items[%d].actions[%d].action_key is duplicated", index, actionIndex)
			}
			actionKeys[action.ActionKey] = struct{}{}
			if action.Appearance != "primary" && action.Appearance != "secondary" {
				return fmt.Errorf("items[%d].actions[%d].appearance is invalid", index, actionIndex)
			}
			if action.Appearance == "primary" {
				primaryCount++
			}
			switch action.Kind {
			case "preset":
				allowed := item.Surface == "participation" && participationActionFlags[action.Flag]
				allowed = allowed || item.Surface == "focus" && focusActionFlags[action.Flag]
				if !allowed {
					return fmt.Errorf("items[%d].actions[%d].flag is invalid", index, actionIndex)
				}
			case "custom":
				if action.Flag == "" || strings.TrimSpace(action.Flag) != action.Flag || !utf8.ValidString(action.Flag) || len([]byte(action.Flag)) > 20 || containsForbiddenCustomText(action.Flag) {
					return fmt.Errorf("items[%d].actions[%d].custom flag is invalid", index, actionIndex)
				}
			default:
				return fmt.Errorf("items[%d].actions[%d].kind is invalid", index, actionIndex)
			}
			if action.Flag == "open_source" && item.SourceRef == nil {
				return fmt.Errorf("items[%d].actions[%d].open_source requires source_ref", index, actionIndex)
			}
		}
		if primaryCount > 1 {
			return fmt.Errorf("items[%d].actions contains more than one primary action", index)
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return err
		}
		item.payloadHash = hashString(string(encoded))
	}
	return nil
}

func (s *Service) allowAttentionPublish(ctx context.Context, agentID int64, items []attentionPublishItem) (int64, error) {
	if s.redisClient == nil {
		return 0, errors.New("Attention rate limiter is unavailable")
	}
	prefix := fmt.Sprintf("console:v2:attention:{%d}:", agentID)
	keys := []string{prefix + "total", prefix + "participation", prefix + "focus"}
	args := []interface{}{attentionRateWindow.Milliseconds(), attentionHourlyTotal, attentionHourlyParticipate, attentionHourlyFocus}
	for _, item := range items {
		args = append(args, hashString(item.ClientItemID), item.Surface)
	}
	values, err := attentionRateScript.Run(ctx, s.redisClient, keys, args...).Slice()
	if err != nil || len(values) != 2 {
		return 0, fmt.Errorf("Attention rate limiter failed: %w", err)
	}
	allowed, _ := values[0].(int64)
	retryAfter, _ := values[1].(int64)
	if allowed != 1 {
		return retryAfter, errConflict
	}
	return 0, nil
}

func (s *Service) releaseAttentionPublish(ctx context.Context, agentID int64, items []attentionPublishItem) error {
	if len(items) == 0 {
		return nil
	}
	if s.redisClient == nil {
		return errors.New("Attention rate limiter is unavailable")
	}
	prefix := fmt.Sprintf("console:v2:attention:{%d}:", agentID)
	keys := []string{prefix + "total", prefix + "participation", prefix + "focus"}
	members := make([]interface{}, 0, len(items))
	for _, item := range items {
		members = append(members, hashString(item.ClientItemID))
	}
	return attentionRateReleaseScript.Run(ctx, s.redisClient, keys, members...).Err()
}

func parseOptionalPositiveID(raw *string) (int64, bool) {
	if raw == nil {
		return 0, false
	}
	value, err := strconv.ParseInt(strings.TrimSpace(*raw), 10, 64)
	return value, err == nil && value > 0
}

func authorizeAttentionSources(tx *gorm.DB, agentID int64, items []attentionPublishItem) error {
	grouped := make(map[string][]int64)
	parents := make(map[int64]int64)
	for _, item := range items {
		if item.SourceRef == nil {
			continue
		}
		grouped[item.SourceRef.Type] = append(grouped[item.SourceRef.Type], item.sourceID)
		if item.SourceRef.ParentID != nil {
			parent, ok := parseOptionalPositiveID(item.SourceRef.ParentID)
			if !ok {
				return errUnauthorized
			}
			if existing, exists := parents[item.sourceID]; exists && existing != parent {
				return errUnauthorized
			}
			parents[item.sourceID] = parent
		}
	}
	for sourceType, ids := range grouped {
		var count int64
		var query *gorm.DB
		switch sourceType {
		case "broadcast":
			query = tx.Raw(`SELECT COUNT(DISTINCT source_id) FROM agent_feed_exposures
				WHERE agent_id = ? AND source_type = 'broadcast' AND source_id = ANY(?)`, agentID, pq.Array(ids)).Scan(&count)
		case "broadcast_reply":
			pairs := make([]map[string]int64, 0, len(ids))
			for _, id := range ids {
				parent, exists := parents[id]
				if !exists {
					return errUnauthorized
				}
				pairs = append(pairs, map[string]int64{"message_id": id, "parent_id": parent})
			}
			encoded, err := json.Marshal(pairs)
			if err != nil {
				return err
			}
			query = tx.Raw(`WITH requested AS (
					SELECT * FROM jsonb_to_recordset(?::jsonb) AS row(message_id bigint, parent_id bigint)
				)
				SELECT COUNT(DISTINCT message.msg_id) FROM requested
				JOIN private_messages message ON message.msg_id = requested.message_id
				JOIN conversations conversation ON conversation.conv_id = message.conv_id
				JOIN agent_feed_exposures exposure ON exposure.agent_id = ?
				 AND exposure.source_type = 'broadcast' AND exposure.source_id = requested.parent_id
				WHERE conversation.origin_type = 'broadcast'
				  AND conversation.origin_id = requested.parent_id
				  AND (message.sender_id = ? OR message.receiver_id = ?)`, string(encoded), agentID, agentID, agentID).Scan(&count)
		case "private_message":
			query = tx.Raw(`SELECT COUNT(DISTINCT msg_id) FROM private_messages
				WHERE msg_id = ANY(?) AND (sender_id = ? OR receiver_id = ?)`, pq.Array(ids), agentID, agentID).Scan(&count)
		case "friend_request":
			query = tx.Raw(`SELECT COUNT(DISTINCT id) FROM friend_requests
				WHERE id = ANY(?) AND (from_uid = ? OR to_uid = ?)`, pq.Array(ids), agentID, agentID).Scan(&count)
		case "relation":
			query = tx.Raw(`SELECT COUNT(DISTINCT id) FROM user_relations
				WHERE id = ANY(?) AND (from_uid = ? OR to_uid = ?)`, pq.Array(ids), agentID, agentID).Scan(&count)
		case "context":
			query = tx.Raw(`SELECT COUNT(DISTINCT revision) FROM agent_context_revisions
				WHERE agent_id = ? AND revision = ANY(?)`, agentID, pq.Array(ids)).Scan(&count)
		case "activity":
			query = tx.Raw(`SELECT COUNT(DISTINCT log_id) FROM agent_activity_log
				WHERE agent_id = ? AND log_id = ANY(?)`, agentID, pq.Array(ids)).Scan(&count)
		}
		if query == nil {
			return errUnauthorized
		}
		if query.Error != nil {
			return query.Error
		}
		if count != int64(len(uniqueInt64(ids))) {
			return errUnauthorized
		}
	}
	return nil
}

func uniqueInt64(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func validateAttentionContextRefs(tx *gorm.DB, agentID int64, items []attentionPublishItem) error {
	contextRevisions := make([]int64, 0, len(items))
	goalRevisions := make([]int64, 0, len(items))
	intentIDs := make([]int64, 0, len(items))
	intentAdds := 0
	for _, item := range items {
		if item.ContextRef.ContextRevision != nil {
			contextRevisions = append(contextRevisions, *item.ContextRef.ContextRevision)
		}
		if item.ContextRef.NetworkGoalRevision != nil {
			goalRevisions = append(goalRevisions, *item.ContextRef.NetworkGoalRevision)
		}
		if id, ok := parseOptionalPositiveID(item.ContextRef.IntentID); ok {
			intentIDs = append(intentIDs, id)
		} else if item.ContextRef.IntentID != nil {
			return errConflict
		}
		if item.Category == "intent_update" && item.ContextRef.Operation == "add" {
			intentAdds++
		}
	}
	if len(contextRevisions) > 0 {
		var count int64
		query := tx.Raw(`SELECT COUNT(DISTINCT revision) FROM agent_context_revisions
			WHERE agent_id = ? AND revision = ANY(?)`, agentID, pq.Array(contextRevisions)).Scan(&count)
		if query.Error != nil {
			return query.Error
		}
		if count != int64(len(uniqueInt64(contextRevisions))) {
			return errConflict
		}
	}
	if len(goalRevisions) > 0 {
		var count int64
		query := tx.Raw(`SELECT COUNT(DISTINCT version) FROM agent_network_goals
			WHERE agent_id = ? AND version = ANY(?)`, agentID, pq.Array(goalRevisions)).Scan(&count)
		if query.Error != nil {
			return query.Error
		}
		if count != int64(len(uniqueInt64(goalRevisions))) {
			return errConflict
		}
	}
	if len(intentIDs) > 0 {
		var count int64
		query := tx.Raw(`SELECT COUNT(DISTINCT intent_id) FROM agent_intent_actions
			WHERE agent_id = ? AND intent_id = ANY(?)`, agentID, pq.Array(intentIDs)).Scan(&count)
		if query.Error != nil {
			return query.Error
		}
		if count != int64(len(uniqueInt64(intentIDs))) {
			return errConflict
		}
	}
	if intentAdds > 0 {
		var active int64
		query := tx.Raw(`SELECT COUNT(*) FROM agent_intent_actions WHERE agent_id = ? AND status = 'active'`, agentID).Scan(&active)
		if query.Error != nil {
			return query.Error
		}
		if active >= 10 {
			return errConflict
		}
	}
	return nil
}

type attentionInsertSeed struct {
	ClientItemID   string                    `json:"client_item_id"`
	PayloadHash    string                    `json:"payload_hash"`
	Surface        string                    `json:"surface"`
	Category       string                    `json:"category"`
	Language       string                    `json:"language"`
	Title          string                    `json:"title"`
	Body           string                    `json:"body"`
	Recommendation string                    `json:"recommendation"`
	SourceType     string                    `json:"source_type"`
	SourceID       int64                     `json:"source_id"`
	SourceRef      interface{}               `json:"source_ref"`
	ContextRef     interface{}               `json:"context_ref"`
	Actions        []attentionProtocolAction `json:"actions"`
	GeneratedAt    int64                     `json:"generated_at"`
	ExpiresAt      int64                     `json:"expires_at"`
}

func (s *Service) publishAttentionItems(ctx context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	req, raw, err := decodeAttentionPublishBody(c)
	now := time.Now().UnixMilli()
	if err == nil {
		err = validateAttentionPublish(&req, now)
	}
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_ATTENTION_BATCH", err.Error(), nil)
		return
	}
	requestHash := hashString(string(raw))
	var replayPayload map[string]interface{}
	if found, conflict, readErr := s.loadIdempotentResponse(agentIDValue, "attention_publish", req.IdempotencyKey, requestHash, &replayPayload); readErr != nil {
		fail(c, http.StatusInternalServerError, "ATTENTION_PUBLISH_FAILED", "could not verify Attention request", nil)
		return
	} else if conflict {
		fail(c, http.StatusConflict, "ATTENTION_IDEMPOTENCY_CONFLICT", "idempotency key was used with different content", nil)
		return
	} else if found {
		replayPayload["replay"] = true
		reply(c, http.StatusOK, replayPayload)
		return
	}
	result := map[string]interface{}{}
	rateRetryAfter := int64(0)
	rateUnavailable := false
	var reservedItems []attentionPublishItem
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, agentIDValue^int64(0x41_54_54_4e)).Error; err != nil {
			return err
		}
		clientIDs := make([]string, 0, len(req.Items))
		for _, item := range req.Items {
			clientIDs = append(clientIDs, item.ClientItemID)
		}
		var existing []struct {
			ClientItemID string `gorm:"column:client_item_id"`
			PayloadHash  string `gorm:"column:payload_hash"`
		}
		if err := tx.Raw(`SELECT client_item_id, payload_hash FROM agent_attention_items
			WHERE agent_id = ? AND client_item_id = ANY(?)`, agentIDValue, pq.Array(clientIDs)).Scan(&existing).Error; err != nil {
			return err
		}
		existingHashes := make(map[string]string, len(existing))
		for _, row := range existing {
			existingHashes[row.ClientItemID] = row.PayloadHash
		}
		newItems := make([]attentionPublishItem, 0, len(req.Items))
		for _, item := range req.Items {
			if oldHash, exists := existingHashes[item.ClientItemID]; exists {
				if oldHash != item.payloadHash {
					return errConflict
				}
				continue
			}
			newItems = append(newItems, item)
		}
		if len(newItems) > 0 {
			var quotas struct {
				Total         int64 `gorm:"column:total"`
				Participation int64 `gorm:"column:participation"`
				Focus         int64 `gorm:"column:focus"`
			}
			if err := tx.Raw(`SELECT COUNT(*) AS total,
				COUNT(*) FILTER (WHERE surface = 'participation') AS participation,
				COUNT(*) FILTER (WHERE surface = 'focus') AS focus
				FROM agent_attention_items WHERE agent_id = ? AND producer = 'agent' AND created_at >= ?`,
				agentIDValue, now-attentionRateWindow.Milliseconds()).Scan(&quotas).Error; err != nil {
				return err
			}
			newParticipation := 0
			for _, item := range newItems {
				if item.Surface == "participation" {
					newParticipation++
				}
			}
			if quotas.Total+int64(len(newItems)) > attentionHourlyTotal ||
				quotas.Participation+int64(newParticipation) > attentionHourlyParticipate ||
				quotas.Focus+int64(len(newItems)-newParticipation) > attentionHourlyFocus {
				rateRetryAfter = attentionRateWindow.Milliseconds()
				return errAttentionRateLimited
			}
			if err := authorizeAttentionSources(tx, agentIDValue, newItems); err != nil {
				return err
			}
			if err := validateAttentionContextRefs(tx, agentIDValue, newItems); err != nil {
				return err
			}
			var limitErr error
			rateRetryAfter, limitErr = s.allowAttentionPublish(ctx, agentIDValue, newItems)
			if limitErr != nil {
				if !errors.Is(limitErr, errConflict) {
					rateUnavailable = true
				}
				return limitErr
			}
			reservedItems = newItems
			seeds := make([]attentionInsertSeed, 0, len(newItems))
			for _, item := range newItems {
				sourceType, sourceRef := "agent", interface{}(map[string]interface{}{})
				if item.SourceRef != nil {
					sourceType, sourceRef = item.SourceRef.Type, item.SourceRef
				}
				seeds = append(seeds, attentionInsertSeed{ClientItemID: item.ClientItemID, PayloadHash: item.payloadHash,
					Surface: item.Surface, Category: item.Category, Language: item.Language, Title: item.Title, Body: item.Body,
					Recommendation: item.Recommendation, SourceType: sourceType, SourceID: item.sourceID, SourceRef: sourceRef,
					ContextRef: item.ContextRef, Actions: item.Actions, GeneratedAt: item.GeneratedAt, ExpiresAt: item.ExpiresAt})
			}
			encoded, marshalErr := json.Marshal(seeds)
			if marshalErr != nil {
				return marshalErr
			}
			if err := tx.Exec(`INSERT INTO agent_attention_items
				(agent_id, producer, surface, category, client_item_id, payload_hash, language,
				 title, summary, body, recommendation, source_type, source_id, source_ref, context_ref,
				 proposed_actions, actions_snapshot, status, item_revision, response_status,
				 generated_at, created_at, updated_at, expires_at)
				SELECT ?, 'agent', seed.surface, seed.category, seed.client_item_id, seed.payload_hash,
				 seed.language, seed.title, seed.body, seed.body, seed.recommendation, seed.source_type,
				 seed.source_id, seed.source_ref, seed.context_ref, seed.actions, seed.actions,
				 'open', 1, 'none', seed.generated_at, ?, ?, seed.expires_at
				FROM jsonb_to_recordset(?::jsonb) AS seed(
				 client_item_id text, payload_hash text, surface text, category text, language text,
				 title text, body text, recommendation text, source_type text, source_id bigint,
				 source_ref jsonb, context_ref jsonb, actions jsonb, generated_at bigint, expires_at bigint)`,
				agentIDValue, now, now, string(encoded)).Error; err != nil {
				return err
			}
		}
		var rows []attentionPublishRow
		if err := tx.Raw(`SELECT attention_id, client_item_id FROM agent_attention_items
			WHERE agent_id = ? AND client_item_id = ANY(?) ORDER BY attention_id`, agentIDValue, pq.Array(clientIDs)).Scan(&rows).Error; err != nil {
			return err
		}
		items := make([]map[string]interface{}, 0, len(rows))
		for _, row := range rows {
			items = append(items, map[string]interface{}{"client_item_id": row.ClientItemID, "attention_id": fmt.Sprintf("%d", row.AttentionID)})
		}
		result = map[string]interface{}{"schema_version": "agent_attention.v1", "accepted": len(newItems), "items": items, "replay": len(newItems) == 0}
		snapshot, _ := json.Marshal(result)
		if err := tx.Exec(`INSERT INTO agent_idempotency_requests
			(agent_id, operation, idempotency_key, request_hash, response_snapshot, expires_at, created_at)
			VALUES (?, 'attention_publish', ?, ?, ?::jsonb, ?, ?)`, agentIDValue, req.IdempotencyKey,
			requestHash, string(snapshot), now+int64(24*time.Hour/time.Millisecond), now).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil && len(reservedItems) > 0 {
		_ = s.releaseAttentionPublish(ctx, agentIDValue, reservedItems)
	}
	if errors.Is(err, errAttentionRateLimited) {
		c.Header("Retry-After", strconv.FormatInt((rateRetryAfter+999)/1000, 10))
		fail(c, http.StatusTooManyRequests, "ATTENTION_RATE_LIMITED", "Attention upload limit was reached", map[string]interface{}{"retry_after_ms": rateRetryAfter})
		return
	}
	if rateUnavailable {
		fail(c, http.StatusServiceUnavailable, "ATTENTION_RATE_LIMIT_UNAVAILABLE", "Attention upload is temporarily unavailable", nil)
		return
	}
	if errors.Is(err, errUnauthorized) {
		fail(c, http.StatusForbidden, "ATTENTION_SOURCE_FORBIDDEN", "one or more Attention sources are unavailable", nil)
		return
	}
	if errors.Is(err, errConflict) || isUniqueViolation(err) {
		var replay map[string]interface{}
		found, conflict, replayErr := s.loadIdempotentResponse(agentIDValue, "attention_publish", req.IdempotencyKey, requestHash, &replay)
		if replayErr == nil && found && !conflict {
			replay["replay"] = true
			reply(c, http.StatusOK, replay)
			return
		}
		fail(c, http.StatusConflict, "ATTENTION_CONFLICT", "Attention content, context, or intent capacity changed", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "ATTENTION_PUBLISH_FAILED", "could not publish Attention items", nil)
		return
	}
	reply(c, http.StatusCreated, result)
}

var errAttentionRateLimited = errors.New("Attention rate limited")
