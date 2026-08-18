package consolev2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"

	"eigenflux_server/pkg/agentcard"
)

const (
	todayEncounterLimit     = 8
	todayParticipationLimit = 5
	todayFocusLimit         = 8
)

type todayEncounter struct {
	PeerAgentID      int64 `gorm:"column:peer_agent_id" json:"peer_agent_id,string"`
	LastInteraction  int64 `gorm:"column:last_interaction_at" json:"last_interaction_at"`
	InteractionCount int64 `gorm:"column:interaction_count" json:"interaction_count"`
	TotalCount       int64 `gorm:"column:total_count" json:"-"`
}

var consoleV2CardCompletionFields = []string{
	"agent_name", "agent_description", "human_description", "working_languages",
	"seeking", "offering", "geo", "timezone", "agent_status", "human_status", "interests_negative",
}

type todayCommandReceipt struct {
	AttentionID int64  `gorm:"column:attention_id"`
	CommandID   int64  `gorm:"column:command_id"`
	Status      string `gorm:"column:status"`
	CreatedAt   int64  `gorm:"column:created_at"`
	CompletedAt *int64 `gorm:"column:completed_at"`
	Result      string `gorm:"column:result"`
}

func cardFieldPresent(value interface{}) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []interface{}:
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				return true
			}
		}
		return false
	case map[string]interface{}:
		return len(typed) > 0
	default:
		return value != nil
	}
}

func calculateCardCompletion(publicJSON, privateJSON string) (int, int, int, error) {
	publicCard := map[string]interface{}{}
	privateCard := map[string]interface{}{}
	if strings.TrimSpace(publicJSON) != "" {
		if err := json.Unmarshal([]byte(publicJSON), &publicCard); err != nil {
			return 0, 0, 0, err
		}
	}
	if strings.TrimSpace(privateJSON) != "" {
		if err := json.Unmarshal([]byte(privateJSON), &privateCard); err != nil {
			return 0, 0, 0, err
		}
	}
	completed := 0
	for _, field := range consoleV2CardCompletionFields {
		source := privateCard
		for _, spec := range agentcard.EditableFields {
			if spec.Name == field && spec.Public {
				source = publicCard
				break
			}
		}
		if cardFieldPresent(source[field]) {
			completed++
		}
	}
	total := len(consoleV2CardCompletionFields)
	percent := 0
	if total > 0 {
		percent = completed * 100 / total
	}
	return completed, total, percent, nil
}

func todayLocationFromPrivateCard(privateJSON string) (*time.Location, string) {
	location := time.UTC
	name := "UTC"
	var privateCard map[string]interface{}
	if json.Unmarshal([]byte(privateJSON), &privateCard) == nil {
		if timezone, ok := privateCard["timezone"].(string); ok {
			candidate := strings.TrimSpace(strings.SplitN(timezone, "(", 2)[0])
			if loaded, err := time.LoadLocation(candidate); err == nil {
				location = loaded
				name = candidate
			}
		}
	}
	return location, name
}

func todayStartFromPrivateCard(privateJSON string, now time.Time) int64 {
	location, _ := todayLocationFromPrivateCard(privateJSON)
	localNow := now.In(location)
	return time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, location).UTC().UnixMilli()
}

func (s *Service) loadTodayAttentions(agentID, since int64) ([]attentionView, []attentionView, int64, int64, error) {
	var counts struct {
		Focus         int64 `gorm:"column:focus_count"`
		Participation int64 `gorm:"column:participation_count"`
	}
	if err := s.db.Raw(`SELECT COUNT(*) FILTER (WHERE NOT (proposed_actions @> '[{"requires_user_confirmation":true}]'::jsonb)) AS focus_count,
		COUNT(*) FILTER (WHERE proposed_actions @> '[{"requires_user_confirmation":true}]'::jsonb) AS participation_count
		FROM agent_attention_items WHERE agent_id = ? AND status = 'open' AND created_at >= ?`, agentID, since).Scan(&counts).Error; err != nil {
		return nil, nil, 0, 0, err
	}

	load := func(participation bool, limit int) ([]attentionView, error) {
		var rows []attentionView
		query := attentionSelect + ` WHERE item.agent_id = ? AND item.created_at >= ? AND item.status = 'open'`
		if participation {
			query += ` AND item.proposed_actions @> '[{"requires_user_confirmation":true}]'::jsonb`
		} else {
			query += ` AND NOT (item.proposed_actions @> '[{"requires_user_confirmation":true}]'::jsonb)`
		}
		query += ` GROUP BY item.attention_id ORDER BY item.created_at DESC, item.attention_id DESC LIMIT ?`
		if err := s.db.Raw(query, agentID, since, limit).Scan(&rows).Error; err != nil {
			return nil, err
		}
		return rows, nil
	}
	participation, err := load(true, todayParticipationLimit)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	focus, err := load(false, todayFocusLimit)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	return participation, focus, counts.Focus, counts.Participation, nil
}

func (s *Service) loadTodayCommandReceipts(agentID int64, attentionIDs []int64) (map[int64]map[string]interface{}, error) {
	result := make(map[int64]map[string]interface{}, len(attentionIDs))
	if len(attentionIDs) == 0 {
		return result, nil
	}
	var rows []todayCommandReceipt
	if err := s.db.Raw(`SELECT DISTINCT ON (attention_id) attention_id, command_id, status,
		created_at, completed_at, COALESCE(result, '{}'::jsonb)::text AS result
		FROM agent_commands
		WHERE agent_id = ? AND attention_id = ANY(?)
		ORDER BY attention_id, created_at DESC, command_id DESC`, agentID, pq.Array(attentionIDs)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		var receiptResult interface{}
		if json.Unmarshal([]byte(row.Result), &receiptResult) != nil {
			receiptResult = map[string]interface{}{}
		}
		result[row.AttentionID] = map[string]interface{}{
			"command_id": fmt.Sprintf("%d", row.CommandID), "status": row.Status,
			"created_at": row.CreatedAt, "completed_at": row.CompletedAt, "result": receiptResult,
		}
	}
	return result, nil
}

func (s *Service) getToday(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	now := time.Now().UTC()

	var goal struct {
		GoalID   int64  `gorm:"column:goal_id"`
		GoalText string `gorm:"column:goal_text"`
	}
	if err := s.db.Raw(`SELECT goal_id, goal_text FROM agent_network_goals
		WHERE agent_id = ? AND status = 'active' ORDER BY updated_at DESC LIMIT 1`, agentIDValue).Scan(&goal).Error; err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load network goal", nil)
		return
	}

	var card struct {
		PublicCard  string `gorm:"column:public_card"`
		PrivateCard string `gorm:"column:private_card"`
	}
	if err := s.db.Raw(`SELECT public_card::text AS public_card, private_card::text AS private_card
		FROM agent_cards WHERE agent_id = ?`, agentIDValue).Scan(&card).Error; err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load Agent Card", nil)
		return
	}
	completedFields, totalFields, completionPercent, err := calculateCardCompletion(card.PublicCard, card.PrivateCard)
	if err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not calculate Agent Card completion", nil)
		return
	}
	location, timezoneName := todayLocationFromPrivateCard(card.PrivateCard)
	todayStart := todayStartFromPrivateCard(card.PrivateCard, now)
	todayDay := now.In(location).Format("2006-01-02")

	var encounters []todayEncounter
	if err := s.db.Raw(`WITH recent AS (
			SELECT participant_b AS peer_agent_id, updated_at, conv_id
			FROM conversations WHERE participant_a = ? AND updated_at >= ?
			UNION ALL
			SELECT participant_a AS peer_agent_id, updated_at, conv_id
			FROM conversations WHERE participant_b = ? AND updated_at >= ?
			UNION ALL
			SELECT (item.payload_snapshot->'author_identity'->>'agent_id')::bigint AS peer_agent_id,
				item.created_at AS updated_at, item.batch_item_id AS conv_id
			FROM feed_batch_items item
			JOIN feed_batches batch ON batch.batch_id = item.batch_id
			WHERE batch.agent_id = ? AND batch.created_at >= ? AND item.created_at >= ?
			  AND jsonb_typeof(item.payload_snapshot->'author_identity') = 'object'
			  AND COALESCE(item.payload_snapshot->'author_identity'->>'agent_id', '') ~ '^[0-9]+$'
		), grouped AS (
			SELECT peer_agent_id, MAX(updated_at) AS last_interaction_at,
				COUNT(*)::bigint AS interaction_count
			FROM recent WHERE peer_agent_id <> ? GROUP BY peer_agent_id
		)
		SELECT peer_agent_id, last_interaction_at, interaction_count,
			COUNT(*) OVER() AS total_count
		FROM grouped ORDER BY last_interaction_at DESC, peer_agent_id DESC LIMIT ?`,
		agentIDValue, todayStart, agentIDValue, todayStart,
		agentIDValue, todayStart-int64(feedMaxLeaseAge/time.Millisecond), todayStart,
		agentIDValue, todayEncounterLimit).Scan(&encounters).Error; err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load today's Agent encounters", nil)
		return
	}
	peerIDs := make([]int64, 0, len(encounters))
	encounterCount := int64(0)
	if len(encounters) > 0 {
		encounterCount = encounters[0].TotalCount
	}
	for _, encounter := range encounters {
		peerIDs = append(peerIDs, encounter.PeerAgentID)
	}
	relations, err := s.loadViewerRelations(agentIDValue, peerIDs)
	if err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load encountered Agent relations", nil)
		return
	}
	contexts, err := s.loadCommunicationContexts(agentIDValue, peerIDs, relations)
	if err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load encountered Agent summaries", nil)
		return
	}

	participation, focus, focusCount, participationCount, err := s.loadTodayAttentions(agentIDValue, todayStart)
	if err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load Attention items", nil)
		return
	}
	attentionIDs := make([]int64, 0, len(participation))
	for _, item := range participation {
		attentionIDs = append(attentionIDs, item.AttentionID)
	}
	receipts, err := s.loadTodayCommandReceipts(agentIDValue, attentionIDs)
	if err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load participation receipts", nil)
		return
	}
	participationItems := make([]map[string]interface{}, 0, len(participation))
	for _, item := range participation {
		view := attentionResponse(item)
		if receipt, exists := receipts[item.AttentionID]; exists {
			view["latest_command"] = receipt
		}
		participationItems = append(participationItems, view)
	}
	focusItems := make([]map[string]interface{}, 0, len(focus))
	for _, item := range focus {
		focusItems = append(focusItems, attentionResponse(item))
	}

	var activityCount int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM agent_activity_log WHERE agent_id = ? AND created_at >= ?`, agentIDValue, todayStart).Scan(&activityCount).Error; err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load today's activity count", nil)
		return
	}

	goalData := interface{}(nil)
	if goal.GoalID > 0 {
		goalData = map[string]interface{}{"goal_id": strconv.FormatInt(goal.GoalID, 10), "goal_text": goal.GoalText}
	}
	reply(c, http.StatusOK, map[string]interface{}{
		"generated_at": now.UnixMilli(),
		"day":          todayDay, "timezone": timezoneName, "window_start": todayStart,
		"capabilities": map[string]interface{}{"control_enabled": s.enableControl},
		"network_goal": goalData,
		"card_completion": map[string]interface{}{
			"completed_fields": completedFields, "total_fields": totalFields, "percent": completionPercent,
		},
		"brief": map[string]interface{}{
			"focus_count": focusCount, "participation_count": participationCount,
			"encounter_count": encounterCount, "activity_count": activityCount,
		},
		"encounters": encounters, "agent_contexts": contexts,
		"participation_items": participationItems, "focus_items": focusItems,
	})
}
