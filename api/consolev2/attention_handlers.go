package consolev2

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

type attentionCursor struct {
	CreatedAt   int64 `json:"created_at"`
	AttentionID int64 `json:"attention_id"`
}

type attentionView struct {
	AttentionID    int64  `gorm:"column:attention_id"`
	Title          string `gorm:"column:title"`
	Summary        string `gorm:"column:summary"`
	SourceType     string `gorm:"column:source_type"`
	SourceID       int64  `gorm:"column:source_id"`
	Proposed       string `gorm:"column:proposed_actions"`
	MatchedIntents string `gorm:"column:matched_intent_ids"`
	Status         string `gorm:"column:status"`
	CreatedAt      int64  `gorm:"column:created_at"`
	ExpiresAt      *int64 `gorm:"column:expires_at"`
}

func encodeAttentionCursor(cursor attentionCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeAttentionCursor(raw string) (attentionCursor, error) {
	if raw == "" {
		return attentionCursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return attentionCursor{}, err
	}
	var cursor attentionCursor
	if json.Unmarshal(decoded, &cursor) != nil || cursor.CreatedAt < 0 || cursor.AttentionID < 0 {
		return attentionCursor{}, fmt.Errorf("invalid attention cursor")
	}
	return cursor, nil
}

func attentionResponse(row attentionView) map[string]interface{} {
	var actions, intents interface{}
	if json.Unmarshal([]byte(row.Proposed), &actions) != nil {
		actions = []interface{}{}
	}
	if json.Unmarshal([]byte(row.MatchedIntents), &intents) != nil {
		intents = []interface{}{}
	}
	return map[string]interface{}{
		"attention_id": fmt.Sprintf("%d", row.AttentionID), "title": row.Title, "summary": row.Summary,
		"source_ref":         map[string]interface{}{"type": row.SourceType, "id": fmt.Sprintf("%d", row.SourceID)},
		"matched_intent_ids": intents, "proposed_actions": actions, "status": row.Status,
		"created_at": row.CreatedAt, "expires_at": row.ExpiresAt,
	}
}

const attentionSelect = `SELECT item.attention_id, item.title, item.summary, item.source_type, item.source_id,
	item.proposed_actions::text AS proposed_actions, item.status, item.created_at, item.expires_at,
	COALESCE(jsonb_agg(link.intent_id::text ORDER BY link.intent_id)
		FILTER (WHERE link.intent_id IS NOT NULL), '[]'::jsonb)::text AS matched_intent_ids
	FROM agent_attention_items item
	LEFT JOIN agent_attention_intents link ON link.agent_id = item.agent_id
	 AND link.attention_id = item.attention_id`

func (s *Service) listAttentionItems(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			fail(c, http.StatusBadRequest, "INVALID_LIMIT", "limit must be between 1 and 50", nil)
			return
		}
		limit = parsed
	}
	cursor, err := decodeAttentionCursor(c.Query("cursor"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CURSOR", "attention cursor is invalid", nil)
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	if status == "" {
		status = "open"
	}
	if status != "open" && status != "acted" && status != "dismissed" && status != "expired" {
		fail(c, http.StatusBadRequest, "INVALID_STATUS", "attention status is invalid", nil)
		return
	}
	var rows []attentionView
	query := attentionSelect + ` WHERE item.agent_id = ? AND item.status = ?`
	args := []interface{}{agentIDValue, status}
	if cursor.AttentionID > 0 {
		query += ` AND (item.created_at, item.attention_id) < (?, ?)`
		args = append(args, cursor.CreatedAt, cursor.AttentionID)
	}
	query += ` GROUP BY item.attention_id ORDER BY item.created_at DESC, item.attention_id DESC LIMIT ?`
	args = append(args, limit)
	if err := s.db.Raw(query, args...).Scan(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "ATTENTION_LIST_FAILED", "could not list attention items", nil)
		return
	}
	items := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		items = append(items, attentionResponse(row))
	}
	nextCursor := ""
	if len(rows) == limit {
		last := rows[len(rows)-1]
		nextCursor = encodeAttentionCursor(attentionCursor{CreatedAt: last.CreatedAt, AttentionID: last.AttentionID})
	}
	reply(c, http.StatusOK, map[string]interface{}{"attention_items": items, "next_cursor": nextCursor, "has_more": len(rows) == limit})
}

func (s *Service) getAttentionItem(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	attentionID, err := strconv.ParseInt(c.Param("attention_id"), 10, 64)
	if err != nil || attentionID <= 0 {
		fail(c, http.StatusBadRequest, "INVALID_ATTENTION_ID", "attention_id is invalid", nil)
		return
	}
	var rows []attentionView
	query := attentionSelect + ` WHERE item.agent_id = ? AND item.attention_id = ? GROUP BY item.attention_id`
	if err := s.db.Raw(query, agentIDValue, attentionID).Scan(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "ATTENTION_READ_FAILED", "could not read attention item", nil)
		return
	}
	if len(rows) == 0 {
		fail(c, http.StatusNotFound, "ATTENTION_NOT_FOUND", "attention item was not found", nil)
		return
	}
	reply(c, http.StatusOK, attentionResponse(rows[0]))
}

func (s *Service) dismissAttentionItem(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	attentionID, err := strconv.ParseInt(c.Param("attention_id"), 10, 64)
	if err != nil || attentionID <= 0 {
		fail(c, http.StatusBadRequest, "INVALID_ATTENTION_ID", "attention_id is invalid", nil)
		return
	}
	result := s.db.Exec(`UPDATE agent_attention_items SET status = 'dismissed'
		WHERE agent_id = ? AND attention_id = ? AND status = 'open'`, agentIDValue, attentionID)
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "ATTENTION_UPDATE_FAILED", "could not dismiss attention item", nil)
		return
	}
	if result.RowsAffected != 1 {
		fail(c, http.StatusConflict, "ATTENTION_NOT_OPEN", "attention item is missing or no longer open", nil)
		return
	}
	reply(c, http.StatusOK, map[string]interface{}{"attention_id": fmt.Sprintf("%d", attentionID), "status": "dismissed"})
}
