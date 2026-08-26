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
	AttentionID     int64  `gorm:"column:attention_id"`
	Producer        string `gorm:"column:producer"`
	ProtocolVersion string `gorm:"column:protocol_version"`
	Surface         string `gorm:"column:surface"`
	Category        string `gorm:"column:category"`
	ClientItemID    string `gorm:"column:client_item_id"`
	Language        string `gorm:"column:language"`
	Title           string `gorm:"column:title"`
	Body            string `gorm:"column:body"`
	Recommendation  string `gorm:"column:recommendation"`
	SourceType      string `gorm:"column:source_type"`
	SourceID        int64  `gorm:"column:source_id"`
	SourceRef       string `gorm:"column:source_ref"`
	ContextRef      string `gorm:"column:context_ref"`
	Actions         string `gorm:"column:actions_snapshot"`
	Status          string `gorm:"column:status"`
	ItemRevision    int64  `gorm:"column:item_revision"`
	SelectedAction  string `gorm:"column:selected_action_key"`
	ResponseStatus  string `gorm:"column:response_status"`
	CreatedAt       int64  `gorm:"column:created_at"`
	GeneratedAt     int64  `gorm:"column:generated_at"`
	UpdatedAt       int64  `gorm:"column:updated_at"`
	RespondedAt     *int64 `gorm:"column:responded_at"`
	ExpiresAt       *int64 `gorm:"column:expires_at"`
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
	var actions, sourceRef interface{}
	var contextRef map[string]interface{}
	if json.Unmarshal([]byte(row.Actions), &actions) != nil {
		actions = []interface{}{}
	}
	if json.Unmarshal([]byte(row.SourceRef), &sourceRef) != nil {
		sourceRef = map[string]interface{}{}
	}
	if json.Unmarshal([]byte(row.ContextRef), &contextRef) != nil {
		contextRef = map[string]interface{}{}
	}
	matchedIntents := make([]interface{}, 0, 1)
	if intentID, ok := contextRef["intent_id"].(string); ok && intentID != "" {
		matchedIntents = append(matchedIntents, intentID)
	}
	return map[string]interface{}{
		"attention_id": fmt.Sprintf("%d", row.AttentionID), "title": row.Title,
		"schema_version": row.ProtocolVersion, "producer": row.Producer,
		"surface": row.Surface, "category": row.Category,
		"client_item_id": row.ClientItemID, "language": row.Language,
		"body": row.Body, "recommendation": row.Recommendation,
		"source_ref": sourceRef, "context_ref": contextRef,
		"actions": actions, "matched_intent_ids": matchedIntents,
		"status": row.Status, "item_revision": row.ItemRevision,
		"selected_action_key": row.SelectedAction, "response_status": row.ResponseStatus,
		"created_at": row.CreatedAt, "generated_at": row.GeneratedAt, "updated_at": row.UpdatedAt,
		"responded_at": row.RespondedAt, "expires_at": row.ExpiresAt,
	}
}

const attentionSelect = `SELECT item.attention_id, item.producer, item.protocol_version, item.surface, item.category,
	item.client_item_id, item.language, item.title, item.body, item.recommendation,
	item.source_type, item.source_id, item.source_ref::text AS source_ref,
	item.context_ref::text AS context_ref,
	item.actions_snapshot::text AS actions_snapshot, item.status, item.item_revision,
	COALESCE(item.selected_action_key, '') AS selected_action_key, item.response_status,
	item.created_at, item.generated_at, item.updated_at, item.responded_at, item.expires_at
	FROM agent_attention_items item`

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
	if status != "open" && status != "selected" && status != "pending" && status != "acted" && status != "dismissed" && status != "expired" {
		fail(c, http.StatusBadRequest, "INVALID_STATUS", "attention status is invalid", nil)
		return
	}
	var rows []attentionView
	query := attentionSelect + ` WHERE item.agent_id = ? AND item.producer = 'agent'
		AND item.protocol_version = 'agent_attention.v1' AND item.status = ?`
	args := []interface{}{agentIDValue, status}
	if status == "open" || status == "selected" || status == "pending" {
		query += ` AND item.expires_at > (extract(epoch FROM clock_timestamp())*1000)::bigint`
	}
	if cursor.AttentionID > 0 {
		query += ` AND (item.created_at, item.attention_id) < (?, ?)`
		args = append(args, cursor.CreatedAt, cursor.AttentionID)
	}
	query += ` ORDER BY item.created_at DESC, item.attention_id DESC LIMIT ?`
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
	query := attentionSelect + ` WHERE item.agent_id = ? AND item.attention_id = ?
		AND item.producer = 'agent' AND item.protocol_version = 'agent_attention.v1'
		AND (item.status NOT IN ('open','selected','pending')
		  OR item.expires_at > (extract(epoch FROM clock_timestamp())*1000)::bigint)`
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
		WHERE agent_id = ? AND attention_id = ? AND producer = 'agent'
		  AND protocol_version = 'agent_attention.v1' AND status = 'open'
		  AND expires_at > (extract(epoch FROM clock_timestamp())*1000)::bigint`, agentIDValue, attentionID)
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
