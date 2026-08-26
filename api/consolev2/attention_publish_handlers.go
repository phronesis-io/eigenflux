package consolev2

import (
	"bytes"
	"context"
	"database/sql"
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
	attentionProtocolVersion    = "agent_attention.v1"
	attentionPublishBodyLimit   = 32 << 10
	attentionPublishBatchMax    = 10
	attentionHourlyTotal        = 20
	attentionHourlyParticipate  = 4
	attentionHourlyFocus        = 16
	attentionRateWindow         = time.Hour
	attentionTextRetention      = 7 * 24 * time.Hour
	attentionPublishDBTimeout   = 2 * time.Second
	attentionPublishReadTimeout = 500 * time.Millisecond
	attentionRedisTimeout       = 250 * time.Millisecond
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

type attentionRateRemaining struct {
	Total         int64 `json:"total"`
	Participation int64 `json:"participation"`
	Focus         int64 `json:"focus"`
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

local current_total = redis.call('ZCARD', KEYS[1])
local current_participation = redis.call('ZCARD', KEYS[2])
local current_focus = redis.call('ZCARD', KEYS[3])
local next_total = current_total + add_total
local next_participation = current_participation + add_participation
local next_focus = current_focus + add_focus

if next_total > total_limit or next_participation > participation_limit or next_focus > focus_limit then
	local retry = 1
	local boundary_found = false
	local function include_retry(key, overflow)
		if overflow > 0 then
			local boundary = redis.call('ZRANGE', key, overflow - 1, overflow - 1, 'WITHSCORES')
			if #boundary == 2 then
				boundary_found = true
				retry = math.max(retry, window - (now - tonumber(boundary[2])))
			end
		end
	end
	include_retry(KEYS[1], next_total - total_limit)
	include_retry(KEYS[2], next_participation - participation_limit)
	include_retry(KEYS[3], next_focus - focus_limit)
	if not boundary_found then retry = window end
	return {0, retry,
		math.max(total_limit - current_total, 0),
		math.max(participation_limit - current_participation, 0),
		math.max(focus_limit - current_focus, 0)}
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
return {1, 0,
	math.max(total_limit - next_total, 0),
	math.max(participation_limit - next_participation, 0),
	math.max(focus_limit - next_focus, 0)}
`)

var attentionRateReleaseScript = redis.NewScript(`
for i = 1, #ARGV do
	for key_index = 1, #KEYS do
		redis.call('ZREM', KEYS[key_index], ARGV[i])
	end
end
return 1
`)

var attentionRateReconcileScript = redis.NewScript(`
local window = tonumber(ARGV[1])
for i = 1, 3 do
	redis.call('DEL', KEYS[i])
end
for i = 2, #ARGV, 3 do
	local member = ARGV[i]
	local surface = ARGV[i + 1]
	local score = ARGV[i + 2]
	redis.call('ZADD', KEYS[1], score, member)
	if surface == 'participation' then
		redis.call('ZADD', KEYS[2], score, member)
	else
		redis.call('ZADD', KEYS[3], score, member)
	end
end
for i = 1, 3 do
	if redis.call('ZCARD', KEYS[i]) > 0 then
		redis.call('PEXPIRE', KEYS[i], window + 60000)
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
	if !utf8.Valid(raw) {
		return attentionPublishRequest{}, nil, errors.New("request body must be valid UTF-8")
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
	if req.SchemaVersion != attentionProtocolVersion || !validAttentionID(req.IdempotencyKey) ||
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

func attentionRateKeys(agentID int64) []string {
	prefix := fmt.Sprintf("console:v2:attention:{%d}:", agentID)
	return []string{prefix + "total", prefix + "participation", prefix + "focus"}
}

func attentionRateMember(clientItemID, reservationToken string) string {
	member := hashString(clientItemID)
	if reservationToken != "" {
		member += ":" + reservationToken
	}
	return member
}

func (s *Service) allowAttentionPublish(ctx context.Context, agentID int64, items []attentionPublishItem, reservationToken string) (int64, attentionRateRemaining, error) {
	if s.redisClient == nil {
		return 0, attentionRateRemaining{}, errors.New("Attention rate limiter is unavailable")
	}
	keys := attentionRateKeys(agentID)
	args := []interface{}{attentionRateWindow.Milliseconds(), attentionHourlyTotal, attentionHourlyParticipate, attentionHourlyFocus}
	for _, item := range items {
		args = append(args, attentionRateMember(item.ClientItemID, reservationToken), item.Surface)
	}
	redisCtx, cancel := context.WithTimeout(ctx, attentionRedisTimeout)
	defer cancel()
	values, err := attentionRateScript.Run(redisCtx, s.redisClient, keys, args...).Slice()
	if err != nil || len(values) != 5 {
		return 0, attentionRateRemaining{}, fmt.Errorf("Attention rate limiter failed: %w", err)
	}
	allowed, _ := values[0].(int64)
	retryAfter, _ := values[1].(int64)
	remaining := attentionRateRemaining{}
	remaining.Total, _ = values[2].(int64)
	remaining.Participation, _ = values[3].(int64)
	remaining.Focus, _ = values[4].(int64)
	if allowed != 1 {
		return retryAfter, remaining, errConflict
	}
	return 0, remaining, nil
}

func remainingAttentionQuota(total, participation, focus int64) attentionRateRemaining {
	remaining := attentionRateRemaining{
		Total:         attentionHourlyTotal - total,
		Participation: attentionHourlyParticipate - participation,
		Focus:         attentionHourlyFocus - focus,
	}
	if remaining.Total < 0 {
		remaining.Total = 0
	}
	if remaining.Participation < 0 {
		remaining.Participation = 0
	}
	if remaining.Focus < 0 {
		remaining.Focus = 0
	}
	return remaining
}

func (s *Service) releaseAttentionPublish(ctx context.Context, agentID int64, items []attentionPublishItem, reservationToken string) error {
	if len(items) == 0 {
		return nil
	}
	if s.redisClient == nil {
		return errors.New("Attention rate limiter is unavailable")
	}
	keys := attentionRateKeys(agentID)
	members := make([]interface{}, 0, len(items))
	for _, item := range items {
		members = append(members, attentionRateMember(item.ClientItemID, reservationToken))
	}
	redisCtx, cancel := context.WithTimeout(ctx, attentionRedisTimeout)
	defer cancel()
	return attentionRateReleaseScript.Run(redisCtx, s.redisClient, keys, members...).Err()
}

type attentionRateRow struct {
	ClientItemID string `gorm:"column:client_item_id"`
	Surface      string `gorm:"column:surface"`
	CreatedAt    int64  `gorm:"column:created_at"`
}

func loadAttentionRateRows(tx *gorm.DB, agentID, cutoff int64) ([]attentionRateRow, error) {
	var rows []attentionRateRow
	err := tx.Raw(`SELECT client_item_id, surface, created_at
		FROM agent_attention_items
		WHERE agent_id = ? AND producer = 'agent'
		  AND protocol_version = 'agent_attention.v1' AND created_at >= ?
		ORDER BY created_at, attention_id`, agentID, cutoff).Scan(&rows).Error
	return rows, err
}

func (s *Service) reconcileAttentionRateWindow(ctx context.Context, agentID int64, rows []attentionRateRow) error {
	if s.redisClient == nil {
		return errors.New("Attention rate limiter is unavailable")
	}
	args := make([]interface{}, 0, 1+len(rows)*3)
	args = append(args, attentionRateWindow.Milliseconds())
	for _, row := range rows {
		args = append(args, attentionRateMember(row.ClientItemID, ""), row.Surface, row.CreatedAt)
	}
	redisCtx, cancel := context.WithTimeout(ctx, attentionRedisTimeout)
	defer cancel()
	return attentionRateReconcileScript.Run(redisCtx, s.redisClient, attentionRateKeys(agentID), args...).Err()
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
				  AND conversation.origin_id > 0
				  AND conversation.status = 0
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

type attentionExistingItem struct {
	ClientItemID string `gorm:"column:client_item_id"`
	PayloadHash  string `gorm:"column:payload_hash"`
}

func attentionClientIDs(items []attentionPublishItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ClientItemID)
	}
	return ids
}

func newAttentionPublishItems(items []attentionPublishItem, existing []attentionExistingItem) ([]attentionPublishItem, error) {
	existingHashes := make(map[string]string, len(existing))
	for _, row := range existing {
		existingHashes[row.ClientItemID] = row.PayloadHash
	}
	newItems := make([]attentionPublishItem, 0, len(items))
	for _, item := range items {
		if oldHash, exists := existingHashes[item.ClientItemID]; exists {
			if oldHash != item.payloadHash {
				return nil, errConflict
			}
			continue
		}
		newItems = append(newItems, item)
	}
	return newItems, nil
}

func attentionRateCounts(rows []attentionRateRow) (total, participation, focus int64) {
	total = int64(len(rows))
	for _, row := range rows {
		if row.Surface == "participation" {
			participation++
		} else {
			focus++
		}
	}
	return total, participation, focus
}

func attentionSurfaceCounts(items []attentionPublishItem) (participation, focus int64) {
	for _, item := range items {
		if item.Surface == "participation" {
			participation++
		} else {
			focus++
		}
	}
	return participation, focus
}

func attentionItemDifference(reserved, inserted []attentionPublishItem) []attentionPublishItem {
	insertedIDs := make(map[string]struct{}, len(inserted))
	for _, item := range inserted {
		insertedIDs[item.ClientItemID] = struct{}{}
	}
	difference := make([]attentionPublishItem, 0, len(reserved))
	for _, item := range reserved {
		if _, exists := insertedIDs[item.ClientItemID]; !exists {
			difference = append(difference, item)
		}
	}
	return difference
}

func containsAllAttentionItems(candidates, actual []attentionPublishItem) bool {
	candidateIDs := make(map[string]struct{}, len(candidates))
	for _, item := range candidates {
		candidateIDs[item.ClientItemID] = struct{}{}
	}
	for _, item := range actual {
		if _, exists := candidateIDs[item.ClientItemID]; !exists {
			return false
		}
	}
	return true
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
	readCtx, cancelRead := context.WithTimeout(ctx, attentionPublishReadTimeout)
	found, conflict, readErr := loadIdempotentResponseFrom(s.db.WithContext(readCtx), agentIDValue,
		"attention_publish", req.IdempotencyKey, requestHash, &replayPayload)
	cancelRead()
	if readErr != nil {
		if attentionPublishTemporarilyUnavailable(readErr) {
			c.Header("Retry-After", "1")
			fail(c, http.StatusServiceUnavailable, "ATTENTION_PUBLISH_BUSY", "Attention upload is temporarily busy", nil)
			return
		}
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
	// Phase 1 is an advisory snapshot only: Redis reservations are made from a
	// consistent committed view, then the write transaction rechecks every fact.
	clientIDs := attentionClientIDs(req.Items)
	cutoff := now - attentionRateWindow.Milliseconds()
	preflightCtx, cancelPreflight := context.WithTimeout(ctx, attentionPublishReadTimeout)
	preflightDB := s.db.WithContext(preflightCtx)
	var preflightExisting []attentionExistingItem
	var rateRows []attentionRateRow
	preflightErr := preflightDB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT client_item_id, payload_hash FROM agent_attention_items
			WHERE agent_id = ? AND producer = 'agent'
			  AND protocol_version = 'agent_attention.v1' AND client_item_id = ANY(?)`,
			agentIDValue, pq.Array(clientIDs)).Scan(&preflightExisting).Error; err != nil {
			return err
		}
		var err error
		rateRows, err = loadAttentionRateRows(tx, agentIDValue, cutoff)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	candidateItems := []attentionPublishItem(nil)
	if preflightErr == nil {
		candidateItems, preflightErr = newAttentionPublishItems(req.Items, preflightExisting)
	}
	cancelPreflight()
	if preflightErr != nil {
		if errors.Is(preflightErr, errConflict) {
			fail(c, http.StatusConflict, "ATTENTION_CONFLICT", "Attention content, context, or intent capacity changed", nil)
			return
		}
		if attentionPublishTemporarilyUnavailable(preflightErr) {
			c.Header("Retry-After", "1")
			fail(c, http.StatusServiceUnavailable, "ATTENTION_PUBLISH_BUSY", "Attention upload is temporarily busy", nil)
			return
		}
		fail(c, http.StatusInternalServerError, "ATTENTION_PUBLISH_FAILED", "could not prepare Attention upload", nil)
		return
	}
	result := map[string]interface{}{}
	rateRetryAfter := int64(0)
	total, participation, focus := attentionRateCounts(rateRows)
	rateRemaining := remainingAttentionQuota(total, participation, focus)
	candidateParticipation, candidateFocus := attentionSurfaceCounts(candidateItems)
	if total+int64(len(candidateItems)) > attentionHourlyTotal ||
		participation+candidateParticipation > attentionHourlyParticipate ||
		focus+candidateFocus > attentionHourlyFocus {
		rateRetryAfter = attentionRateWindow.Milliseconds()
		retryAfterSeconds := (rateRetryAfter + 999) / 1000
		c.Header("Retry-After", strconv.FormatInt(retryAfterSeconds, 10))
		fail(c, http.StatusTooManyRequests, "ATTENTION_RATE_LIMITED", "Attention upload limit was reached", map[string]interface{}{
			"remaining": rateRemaining, "retry_after_seconds": retryAfterSeconds, "retry_after_ms": rateRetryAfter,
		})
		return
	}
	reservationToken := ""
	var reservedItems []attentionPublishItem
	if len(candidateItems) > 0 {
		var tokenErr error
		reservationToken, tokenErr = randomToken("", 12)
		if tokenErr != nil {
			fail(c, http.StatusInternalServerError, "ATTENTION_PUBLISH_FAILED", "could not reserve Attention quota", nil)
			return
		}
		expectedRemaining := remainingAttentionQuota(
			total+int64(len(candidateItems)), participation+candidateParticipation, focus+candidateFocus)
		var limitErr error
		reservationMayExist := false
		rateRetryAfter, rateRemaining, limitErr = s.allowAttentionPublish(ctx, agentIDValue, candidateItems, reservationToken)
		if limitErr == nil {
			reservationMayExist = true
		}
		if errors.Is(limitErr, errConflict) || (limitErr == nil && rateRemaining != expectedRemaining) {
			if reconcileErr := s.reconcileAttentionRateWindow(ctx, agentIDValue, rateRows); reconcileErr != nil {
				if reservationMayExist {
					_ = s.releaseAttentionPublish(context.Background(), agentIDValue, candidateItems, reservationToken)
				}
				fail(c, http.StatusServiceUnavailable, "ATTENTION_RATE_LIMIT_UNAVAILABLE", "Attention upload is temporarily unavailable", nil)
				return
			}
			reservationMayExist = false
			rateRetryAfter, rateRemaining, limitErr = s.allowAttentionPublish(ctx, agentIDValue, candidateItems, reservationToken)
			if limitErr == nil {
				reservationMayExist = true
			}
		}
		if limitErr != nil {
			if reservationMayExist {
				_ = s.releaseAttentionPublish(context.Background(), agentIDValue, candidateItems, reservationToken)
			}
			if errors.Is(limitErr, errConflict) {
				retryAfterSeconds := (rateRetryAfter + 999) / 1000
				c.Header("Retry-After", strconv.FormatInt(retryAfterSeconds, 10))
				fail(c, http.StatusTooManyRequests, "ATTENTION_RATE_LIMITED", "Attention upload limit was reached", map[string]interface{}{
					"remaining": rateRemaining, "retry_after_seconds": retryAfterSeconds, "retry_after_ms": rateRetryAfter,
				})
				return
			}
			fail(c, http.StatusServiceUnavailable, "ATTENTION_RATE_LIMIT_UNAVAILABLE", "Attention upload is temporarily unavailable", nil)
			return
		}
		reservedItems = candidateItems
	}
	var insertedItems []attentionPublishItem
	// Phase 2 is the short authoritative write boundary. It intentionally has
	// no Redis calls while holding the per-Agent PostgreSQL advisory lock.
	operationCtx, cancelOperation := context.WithTimeout(ctx, attentionPublishDBTimeout)
	defer cancelOperation()
	err = s.db.WithContext(operationCtx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL lock_timeout = '250ms'`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`SET LOCAL statement_timeout = '1750ms'`).Error; err != nil {
			return err
		}
		var lockAcquired bool
		if err := tx.Raw(`SELECT pg_try_advisory_xact_lock(?)`, agentIDValue^int64(0x41_54_54_4e)).Scan(&lockAcquired).Error; err != nil {
			return err
		}
		if !lockAcquired {
			return errAttentionPublishBusy
		}
		var existing []attentionExistingItem
		if err := tx.Raw(`SELECT client_item_id, payload_hash FROM agent_attention_items
			WHERE agent_id = ? AND producer = 'agent'
			  AND protocol_version = 'agent_attention.v1' AND client_item_id = ANY(?)`, agentIDValue, pq.Array(clientIDs)).Scan(&existing).Error; err != nil {
			return err
		}
		newItems, filterErr := newAttentionPublishItems(req.Items, existing)
		if filterErr != nil {
			return filterErr
		}
		if !containsAllAttentionItems(candidateItems, newItems) {
			return errAttentionPublishBusy
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
				FROM agent_attention_items WHERE agent_id = ? AND producer = 'agent'
				  AND protocol_version = 'agent_attention.v1' AND created_at >= ?`,
				agentIDValue, now-attentionRateWindow.Milliseconds()).Scan(&quotas).Error; err != nil {
				return err
			}
			newParticipation, newFocus := attentionSurfaceCounts(newItems)
			rateRemaining = remainingAttentionQuota(quotas.Total, quotas.Participation, quotas.Focus)
			if quotas.Total+int64(len(newItems)) > attentionHourlyTotal ||
				quotas.Participation+newParticipation > attentionHourlyParticipate ||
				quotas.Focus+newFocus > attentionHourlyFocus {
				rateRetryAfter = attentionRateWindow.Milliseconds()
				return errAttentionRateLimited
			}
			rateRemaining = remainingAttentionQuota(
				quotas.Total+int64(len(newItems)), quotas.Participation+newParticipation, quotas.Focus+newFocus)
			if err := authorizeAttentionSources(tx, agentIDValue, newItems); err != nil {
				return err
			}
			if err := validateAttentionContextRefs(tx, agentIDValue, newItems); err != nil {
				return err
			}
			insertedItems = append(insertedItems[:0], newItems...)
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
				(agent_id, producer, protocol_version, surface, category, client_item_id, payload_hash, language,
				 title, body, recommendation, source_type, source_id, source_ref, context_ref,
				 actions_snapshot, status, item_revision, response_status,
				 generated_at, created_at, updated_at, expires_at)
				SELECT ?, 'agent', 'agent_attention.v1', seed.surface, seed.category, seed.client_item_id, seed.payload_hash,
				 seed.language, seed.title, seed.body, seed.recommendation, seed.source_type,
				 seed.source_id, seed.source_ref, seed.context_ref, seed.actions,
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
			WHERE agent_id = ? AND producer = 'agent'
			  AND protocol_version = 'agent_attention.v1' AND client_item_id = ANY(?) ORDER BY attention_id`, agentIDValue, pq.Array(clientIDs)).Scan(&rows).Error; err != nil {
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
	// Phase 3 compensates a rollback, or canonicalizes Redis from committed DB
	// rows after success so duplicate candidates never consume quota.
	if len(reservedItems) > 0 {
		itemsToRelease := reservedItems
		if err == nil {
			itemsToRelease = attentionItemDifference(reservedItems, insertedItems)
			finalizeCtx, cancelFinalize := context.WithTimeout(context.Background(), attentionPublishReadTimeout)
			committedRows, loadErr := loadAttentionRateRows(s.db.WithContext(finalizeCtx), agentIDValue, cutoff)
			if loadErr == nil && s.reconcileAttentionRateWindow(finalizeCtx, agentIDValue, committedRows) == nil {
				itemsToRelease = nil
			}
			cancelFinalize()
		}
		if len(itemsToRelease) > 0 {
			_ = s.releaseAttentionPublish(context.Background(), agentIDValue, itemsToRelease, reservationToken)
		}
	}
	if errors.Is(err, errAttentionRateLimited) {
		retryAfterSeconds := (rateRetryAfter + 999) / 1000
		c.Header("Retry-After", strconv.FormatInt(retryAfterSeconds, 10))
		fail(c, http.StatusTooManyRequests, "ATTENTION_RATE_LIMITED", "Attention upload limit was reached", map[string]interface{}{
			"remaining": rateRemaining, "retry_after_seconds": retryAfterSeconds, "retry_after_ms": rateRetryAfter,
		})
		return
	}
	if attentionPublishTemporarilyUnavailable(err) {
		c.Header("Retry-After", "1")
		fail(c, http.StatusServiceUnavailable, "ATTENTION_PUBLISH_BUSY", "Attention upload is temporarily busy", nil)
		return
	}
	if errors.Is(err, errUnauthorized) {
		fail(c, http.StatusForbidden, "ATTENTION_SOURCE_FORBIDDEN", "one or more Attention sources are unavailable", nil)
		return
	}
	if errors.Is(err, errConflict) || isUniqueViolation(err) {
		var replay map[string]interface{}
		replayCtx, cancelReplay := context.WithTimeout(ctx, attentionPublishReadTimeout)
		found, conflict, replayErr := loadIdempotentResponseFrom(s.db.WithContext(replayCtx), agentIDValue,
			"attention_publish", req.IdempotencyKey, requestHash, &replay)
		cancelReplay()
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

var (
	errAttentionRateLimited = errors.New("Attention rate limited")
	errAttentionPublishBusy = errors.New("Attention publish busy")
)

func attentionPublishTemporarilyUnavailable(err error) bool {
	return errors.Is(err, errAttentionPublishBusy) || errors.Is(err, context.DeadlineExceeded) ||
		sqlState(err) == "55P03" || sqlState(err) == "57014"
}
