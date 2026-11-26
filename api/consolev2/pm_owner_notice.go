package consolev2

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type pmOwnerNoticeInput struct {
	ResultID       string `json:"result_id"`
	ConversationID string `json:"conversation_id"`
	Summary        string `json:"summary"`
	NeedsAction    bool   `json:"needs_action"`
	GeneratedAt    int64  `json:"generated_at"`
}

var pmOwnerResultID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (n pmOwnerNoticeInput) valid() bool {
	return pmOwnerResultID.MatchString(n.ResultID) && n.GeneratedAt > 0 &&
		n.Summary != "" && utf8.RuneCountInString(n.Summary) <= 300 &&
		!strings.ContainsRune(n.Summary, utf8.RuneError) && strings.IndexFunc(n.Summary, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
	}) < 0
}

func (s *Service) savePMOwnerNotice(ctx context.Context, c *app.RequestContext) {
	viewer, _ := agentID(c)
	var input pmOwnerNoticeInput
	if raw := c.Request.Body(); len(raw) > 4096 || json.Unmarshal(raw, &input) != nil {
		fail(c, http.StatusBadRequest, "INVALID_PM_OWNER_NOTICE", "invalid private-message result", nil)
		return
	}
	input.Summary = strings.TrimSpace(input.Summary)
	conversation, err := strconv.ParseInt(input.ConversationID, 10, 64)
	if err != nil || conversation <= 0 || !input.valid() {
		fail(c, http.StatusBadRequest, "INVALID_PM_OWNER_NOTICE", "invalid private-message result", nil)
		return
	}

	var allowed bool
	// Keep private summaries out of SQL logs, including failed writes.
	db := s.db.WithContext(ctx).Session(&gorm.Session{Logger: s.db.Logger.LogMode(gormlogger.Silent)})
	err = db.Raw(`
		WITH owned AS (
			SELECT conv_id FROM conversations
			WHERE conv_id = ? AND status = 0 AND (participant_a = ? OR participant_b = ?)
		), inserted AS (
			INSERT INTO pm_owner_notices (agent_id, conv_id, result_id, summary, needs_action, generated_at)
			SELECT ?, conv_id, ?, ?, ?, LEAST(?, floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint) FROM owned
			ON CONFLICT (agent_id, result_id) DO NOTHING
		)
		SELECT EXISTS (SELECT 1 FROM owned)`, conversation, viewer, viewer, viewer,
		input.ResultID, input.Summary, input.NeedsAction, input.GeneratedAt).Scan(&allowed).Error
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "PM_OWNER_NOTICE_UNAVAILABLE", "could not save private-message result", nil)
		return
	}
	if !allowed {
		fail(c, http.StatusNotFound, "CONVERSATION_NOT_FOUND", "conversation not found", nil)
		return
	}
	reply(c, http.StatusOK, map[string]string{"result_id": input.ResultID})
}

type pmOwnerNoticeHandledState struct {
	ResultID  string `json:"result_id"`
	HandledAt *int64 `json:"handled_at"`
	HandledBy string `json:"handled_by,omitempty"`
}

func (s *Service) handleAgentPMOwnerNotice(ctx context.Context, c *app.RequestContext) {
	s.setPMOwnerNoticeHandled(ctx, c, "agent", 0)
}

func (s *Service) handleConsolePMOwnerNotice(ctx context.Context, c *app.RequestContext) {
	var input struct {
		UndoFrom *int64 `json:"undo_from"`
	}
	if body := c.Request.Body(); len(body) > 1024 || json.Unmarshal(body, &input) != nil ||
		input.UndoFrom != nil && *input.UndoFrom <= 0 {
		fail(c, http.StatusBadRequest, "INVALID_PM_OWNER_NOTICE_STATE", "invalid handled state", nil)
		return
	}
	var undoFrom int64
	if input.UndoFrom != nil {
		undoFrom = *input.UndoFrom
	}
	s.setPMOwnerNoticeHandled(ctx, c, "console", undoFrom)
}

func (s *Service) setPMOwnerNoticeHandled(ctx context.Context, c *app.RequestContext, source string, undoFrom int64) {
	viewer, _ := agentID(c)
	resultID := c.Param("result_id")
	if !pmOwnerResultID.MatchString(resultID) {
		fail(c, http.StatusBadRequest, "INVALID_PM_OWNER_NOTICE", "invalid private-message result", nil)
		return
	}
	set := `handled_at = COALESCE(n.handled_at, floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint),
		handled_by = CASE WHEN ? = 'agent' THEN 'agent' ELSE COALESCE(n.handled_by, 'console') END`
	args := []interface{}{source}
	if undoFrom > 0 {
		// Undo only the acknowledged Console action. Retain any intervening
		// Agent action or another Console action.
		set = `handled_at = CASE WHEN n.handled_at = ? AND n.handled_by = 'console' THEN NULL ELSE n.handled_at END,
			handled_by = CASE WHEN n.handled_at = ? AND n.handled_by = 'console' THEN NULL ELSE n.handled_by END`
		args = []interface{}{undoFrom, undoFrom}
	}
	args = append(args, viewer, resultID, viewer, viewer)
	var state pmOwnerNoticeHandledState
	err := s.db.WithContext(ctx).Raw(`
		UPDATE pm_owner_notices n SET `+set+`
		FROM conversations c
		WHERE n.agent_id = ? AND n.result_id = ? AND c.conv_id = n.conv_id
			AND c.status = 0 AND (c.participant_a = ? OR c.participant_b = ?)
		RETURNING n.result_id, n.handled_at, COALESCE(n.handled_by, '') AS handled_by`, args...).Scan(&state).Error
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "PM_OWNER_NOTICE_UNAVAILABLE", "could not update private-message result", nil)
		return
	}
	if state.ResultID == "" {
		fail(c, http.StatusNotFound, "PM_OWNER_NOTICE_NOT_FOUND", "private-message result not found", nil)
		return
	}
	if undoFrom > 0 && state.HandledAt != nil {
		fail(c, http.StatusConflict, "PM_OWNER_NOTICE_STATE_CHANGED", "private-message result has changed", nil)
		return
	}
	reply(c, http.StatusOK, state)
}

type pmOwnerNoticeRow struct {
	ID             int64  `json:"-"`
	ResultID       string `json:"result_id"`
	ConversationID string `json:"conversation_id"`
	Summary        string `json:"summary"`
	NeedsAction    bool   `json:"needs_action"`
	GeneratedAt    int64  `json:"generated_at"`
	PeerName       string `json:"peer_name"`
	PeerShortID    string `json:"peer_short_id"`
	OriginType     string `json:"origin_type"`
	HandledAt      *int64 `json:"handled_at,omitempty"`
	HandledBy      string `json:"handled_by,omitempty"`
}

func (s *Service) listPMOwnerNotices(ctx context.Context, c *app.RequestContext) {
	viewer, _ := agentID(c)
	view := c.Query("view")
	rawCursor := c.Query("cursor")
	var cursorTime int64
	if view == "handled" && rawCursor != "" {
		before, id, ok := strings.Cut(rawCursor, ".")
		var err error
		cursorTime, err = parseIDCursor(before)
		if !ok || err != nil || cursorTime <= 0 || id == "" || id == "0" {
			fail(c, http.StatusBadRequest, "INVALID_CURSOR", "invalid cursor", nil)
			return
		}
		rawCursor = id
	}
	cursor, err := parseIDCursor(rawCursor)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CURSOR", "invalid cursor", nil)
		return
	}
	// A desktop notification can look up its conversations even when they sort
	// below the first page of the account's summaries.
	args := []interface{}{viewer, viewer, viewer}
	filter := ""
	if raw := c.Query("conversations"); raw != "" {
		parts := strings.Split(raw, ",")
		if len(parts) > 5 {
			fail(c, http.StatusBadRequest, "INVALID_CONVERSATIONS", "invalid conversation list", nil)
			return
		}
		ids := make([]int64, 0, len(parts))
		for _, part := range parts {
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil || id <= 0 {
				fail(c, http.StatusBadRequest, "INVALID_CONVERSATIONS", "invalid conversation list", nil)
				return
			}
			ids = append(ids, id)
		}
		filter = " AND n.conv_id IN ?"
		args = append(args, ids)
	}
	if raw := c.Query("results"); raw != "" {
		ids := strings.Split(raw, ",")
		if len(ids) > 5 {
			fail(c, http.StatusBadRequest, "INVALID_RESULTS", "invalid result list", nil)
			return
		}
		for _, id := range ids {
			if !pmOwnerResultID.MatchString(id) {
				fail(c, http.StatusBadRequest, "INVALID_RESULTS", "invalid result list", nil)
				return
			}
		}
		filter += " AND n.result_id IN ?"
		args = append(args, ids)
	}
	latestPerConversation := "DISTINCT ON (n.conv_id)"
	latestOrder := "ORDER BY n.conv_id, n.generated_at DESC, n.id DESC"
	visible := ""
	order := "latest.needs_action DESC, latest.generated_at DESC, latest.id DESC"
	pageBoundary := `(CAST(? AS BIGINT) = 0 OR (latest.needs_action, latest.generated_at, latest.id) < (
		SELECT mark.needs_action, mark.generated_at, mark.id
		FROM pm_owner_notices mark WHERE mark.agent_id = ? AND mark.id = ?))`
	switch view {
	case "":
	case "pending":
		// Filter after picking the current result so clicking Done never
		// resurfaces a superseded result from the same conversation.
		visible = "latest.handled_at IS NULL AND "
	case "handled":
		filter += " AND n.handled_at IS NOT NULL"
		latestPerConversation, latestOrder = "", ""
		order = "latest.handled_at DESC, latest.id DESC"
		pageBoundary = "(CAST(? AS BIGINT) = 0 OR (latest.handled_at, latest.id) < (CAST(? AS BIGINT), CAST(? AS BIGINT)))"
	default:
		fail(c, http.StatusBadRequest, "INVALID_PM_OWNER_NOTICE_VIEW", "invalid private-message result view", nil)
		return
	}
	const pageSize = 20
	if view == "handled" {
		// The cursor's time stays usable even if its result is later undone.
		args = append(args, cursor, cursorTime, cursor, pageSize+1)
	} else {
		args = append(args, cursor, viewer, cursor, pageSize+1)
	}
	rows := []pmOwnerNoticeRow{}
	err = s.db.WithContext(ctx).Raw(`
		SELECT latest.id, latest.result_id, latest.conv_id::text AS conversation_id, latest.summary, latest.needs_action,
			latest.generated_at, COALESCE(a.agent_name, '') AS peer_name, COALESCE(a.short_id, '') AS peer_short_id,
			latest.origin_type, latest.handled_at, COALESCE(latest.handled_by, '') AS handled_by
		FROM (
			SELECT `+latestPerConversation+` n.*,
				CASE WHEN c.participant_a = n.agent_id THEN c.participant_b ELSE c.participant_a END AS peer_id,
				COALESCE(c.origin_type, '') AS origin_type
			FROM pm_owner_notices n JOIN conversations c USING (conv_id)
			WHERE n.agent_id = ? AND c.status = 0 AND (c.participant_a = ? OR c.participant_b = ?)`+filter+`
			`+latestOrder+`
		) latest LEFT JOIN agents a ON a.agent_id = latest.peer_id AND a.identity_state = 'active'
		WHERE `+visible+pageBoundary+`
		ORDER BY `+order+`
		LIMIT ?`, args...).Scan(&rows).Error
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "PM_OWNER_NOTICE_UNAVAILABLE", "could not read private-message results", nil)
		return
	}
	next := ""
	if len(rows) > pageSize {
		rows = rows[:pageSize]
		last := rows[pageSize-1]
		next = strconv.FormatInt(last.ID, 10)
		if view == "handled" {
			next = strconv.FormatInt(*last.HandledAt, 10) + "." + next
		}
	}
	reply(c, http.StatusOK, map[string]interface{}{"results": rows, "next_cursor": next})
}
