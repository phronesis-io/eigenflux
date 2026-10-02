package consolev2

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"eigenflux_server/pkg/dashboardsearch"
	"eigenflux_server/pkg/logger"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/lib/pq"
)

type DashboardTradeSearch interface {
	SearchDashboard(context.Context, int64, string, string, string, string, int, []int64) (dashboardsearch.Group, error)
}

var dashboardSearchTypes = []string{"message", "friend", "broadcast", "service", "order"}
var dashboardSearchScopes = map[string]string{"message": "communication:read", "friend": "relations:read", "broadcast": "profile:read", "service": "trade:write", "order": "trade:write"}

func (s *Service) RegisterDashboardSearch(h *server.Hertz, trade DashboardTradeSearch) {
	handler := s.dashboardSearch(trade)
	h.GET("/api/v2/console/search", s.ConsoleBFFHandlers(false, handler)...)
	h.GET("/api/v2/dashboard/search", s.agentAuthAny("communication:read", "relations:read", "profile:read", "trade:write"), s.requireCompleted, handler)
}

func (s *Service) dashboardSearch(trade DashboardTradeSearch) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		owner, ok := agentID(c)
		if !ok {
			fail(c, 401, "AUTH_REQUIRED", "authentication required", nil)
			return
		}
		text := strings.TrimSpace(c.Query("q"))
		kind, status := c.Query("type"), c.Query("status")
		if kind == "" {
			kind = "all"
		}
		limit := 10
		var err error
		if c.Query("limit") != "" {
			limit, err = strconv.Atoi(c.Query("limit"))
		}
		cursor, cursorErr := parseIDCursor(c.Query("cursor"))
		if !utf8.ValidString(text) || utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 100 || strings.ContainsRune(text, 0) || err != nil || limit < 1 || limit > 50 || cursorErr != nil || (kind != "all" && dashboardSearchScopes[kind] == "") || (kind == "all" && (status != "" || cursor > 0)) || !validDashboardStatus(kind, status) {
			fail(c, 400, "INVALID_SEARCH", "use 1–100 query characters, a valid type/status, limit 1–50, and a cursor only with one type", nil)
			return
		}
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		kinds := dashboardSearchTypes
		if kind != "all" {
			kinds = []string{kind}
		}
		groups := make([]dashboardsearch.Group, 0, len(kinds))
		for _, category := range kinds {
			group := dashboardsearch.Group{Type: category, Items: []dashboardsearch.Result{}}
			if raw, agentSession := c.Get("agent_scopes"); agentSession && !containsScope(raw.(pq.StringArray), dashboardSearchScopes[category]) {
				group.Error = "AGENT_SCOPE_REQUIRED"
			} else if category == "service" || category == "order" {
				var peers []int64
				var searchErr error
				if category == "order" {
					peers, searchErr = s.dashboardCounterparties(ctx, text)
				}
				if searchErr == nil && trade != nil {
					group, searchErr = trade.SearchDashboard(ctx, owner, category, text, status, c.Query("cursor"), limit, peers)
				} else if trade == nil {
					searchErr = errors.New("trade unavailable")
				}
				if searchErr != nil {
					logger.Ctx(ctx).Warn("Dashboard search category failed", "type", category, "error", searchErr)
					group = dashboardsearch.Group{Type: category, Items: []dashboardsearch.Result{}, Error: "SEARCH_UNAVAILABLE"}
				}
			} else if (category == "message" || category == "friend") && !s.enableCommunication {
				group.Error = "COMMUNICATION_UNAVAILABLE"
			} else {
				group, err = s.searchDashboardLocal(ctx, owner, category, text, status, cursor, limit)
				if err != nil {
					logger.Ctx(ctx).Warn("Dashboard search category failed", "type", category, "error", err)
					group = dashboardsearch.Group{Type: category, Items: []dashboardsearch.Result{}, Error: "SEARCH_UNAVAILABLE"}
				}
			}
			groups = append(groups, group)
		}
		reply(c, http.StatusOK, map[string]interface{}{"query": text, "groups": groups})
	}
}

func validDashboardStatus(kind, status string) bool {
	if status == "" {
		return true
	}
	values := map[string]string{
		"message": "pending_verify open closed", "friend": "friend", "broadcast": "pending processing failed published discarded retracted",
		"service": "draft active offline", "order": "awaiting_seller pending_payment in_progress validating awaiting_buyer_confirmation refund_pending refunded cancelled completed",
	}
	for _, candidate := range strings.Fields(values[kind]) {
		if status == candidate {
			return true
		}
	}
	return false
}
func dashboardLiteral(text string) (string, int64) {
	id, _ := strconv.ParseInt(text, 10, 64)
	if id <= 0 || strconv.FormatInt(id, 10) != text {
		id = 0
	}
	return "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(text) + "%", id
}
func (s *Service) dashboardCounterparties(ctx context.Context, text string) ([]int64, error) {
	pattern, _ := dashboardLiteral(text)
	var ids []int64
	err := s.db.WithContext(ctx).Raw(`SELECT agent_id FROM agents WHERE agent_name ILIKE ? ESCAPE '!' OR agent_name_en ILIKE ? ESCAPE '!' OR short_id = ? LIMIT 1001`, pattern, pattern, text).Scan(&ids).Error
	if len(ids) > 1000 {
		return nil, errors.New("counterparty query too broad")
	}
	return ids, err
}

type dashboardSearchRow struct {
	ShortID        string
	Key            int64
	ID             string
	Title          string
	Body           string
	Status         string
	UpdatedAt      int64
	ConversationID string
	PeerID         string
}

func (s *Service) searchDashboardLocal(ctx context.Context, owner int64, kind, text, status string, cursor int64, limit int) (dashboardsearch.Group, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	pattern, id := dashboardLiteral(text)
	var sql string
	var args []interface{}
	switch kind {
	case "message":
		sql = `SELECT m.msg_id AS key, m.msg_id::text AS id, COALESCE(NULLIF(r.remark,''),a.agent_name,'') AS title,
   m.content || E'\n' || COALESCE(a.agent_name,'') || E'\n' || COALESCE(a.agent_name_en,'') || E'\n' || COALESCE(r.remark,'') AS body,
   CASE c.topic_status WHEN 0 THEN 'pending_verify' WHEN 2 THEN 'closed' ELSE 'open' END AS status,
   m.created_at AS updated_at, c.conv_id::text AS conversation_id, a.agent_id::text AS peer_id, a.short_id AS short_id
   FROM conversations c JOIN private_messages m ON m.conv_id=c.conv_id
   JOIN agents a ON a.agent_id=CASE WHEN c.participant_a=? THEN c.participant_b ELSE c.participant_a END
   LEFT JOIN user_relations r ON r.from_uid=? AND r.to_uid=a.agent_id AND r.rel_type=1
   WHERE c.status=0 AND (c.participant_a=? OR c.participant_b=?) AND
   (m.msg_id=? OR c.conv_id=? OR a.agent_id=? OR a.short_id=? OR m.content ILIKE ? ESCAPE '!' OR a.agent_name ILIKE ? ESCAPE '!' OR a.agent_name_en ILIKE ? ESCAPE '!' OR r.remark ILIKE ? ESCAPE '!')`
		args = []interface{}{owner, owner, owner, owner, id, id, id, text, pattern, pattern, pattern, pattern}
	case "friend":
		sql = `SELECT r.id AS key, a.agent_id::text AS id, COALESCE(NULLIF(r.remark,''),a.agent_name,'') AS title,
   a.agent_name || E'\n' || COALESCE(a.agent_name_en,'') || E'\n' || r.remark AS body, 'friend' AS status, r.created_at AS updated_at,
   '' AS conversation_id, a.agent_id::text AS peer_id, a.short_id AS short_id
   FROM user_relations r JOIN agents a ON a.agent_id=r.to_uid WHERE r.from_uid=? AND r.rel_type=1 AND
   (a.agent_id=? OR a.short_id=? OR a.agent_name ILIKE ? ESCAPE '!' OR a.agent_name_en ILIKE ? ESCAPE '!' OR r.remark ILIKE ? ESCAPE '!')`
		args = []interface{}{owner, id, text, pattern, pattern, pattern}
	case "broadcast":
		sql = `SELECT r.item_id AS key, r.item_id::text AS id, LEFT(r.raw_content,100) AS title,
   r.raw_content || E'\n' || COALESCE(r.raw_notes,'') || E'\n' || COALESCE(p.summary,'') || E'\n' || COALESCE(p.summary_zh,'') AS body,
   CASE p.status WHEN 0 THEN 'pending' WHEN 1 THEN 'processing' WHEN 2 THEN 'failed' WHEN 3 THEN 'published' WHEN 4 THEN 'discarded' WHEN 5 THEN 'retracted' END AS status,
   p.updated_at, '' AS conversation_id, '' AS peer_id, '' AS short_id
   FROM raw_items r JOIN processed_items p ON p.item_id=r.item_id WHERE r.author_agent_id=? AND
   (r.item_id=? OR r.raw_content ILIKE ? ESCAPE '!' OR r.raw_notes ILIKE ? ESCAPE '!' OR p.summary ILIKE ? ESCAPE '!' OR p.summary_zh ILIKE ? ESCAPE '!')`
		args = []interface{}{owner, id, pattern, pattern, pattern, pattern}
	default:
		return dashboardsearch.Group{}, errors.New("invalid type")
	}
	sql = `SELECT * FROM (` + sql + `) matches WHERE (CAST(? AS BIGINT)=0 OR key < ?) AND (?='' OR status=?) ORDER BY key DESC LIMIT ?`
	args = append(args, cursor, cursor, status, status, limit+1)
	var rows []dashboardSearchRow
	if err := s.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return dashboardsearch.Group{}, err
	}
	group := dashboardsearch.Group{Type: kind, Items: []dashboardsearch.Result{}, HasMore: len(rows) > limit}
	if group.HasMore {
		rows = rows[:limit]
		group.NextCursor = strconv.FormatInt(rows[len(rows)-1].Key, 10)
	}
	for _, r := range rows {
		target := "/dashboard/broadcasts/" + r.ID
		if kind == "message" {
			target = "/dashboard/messages?conversation_id=" + url.QueryEscape(r.ConversationID) + "&message_id=" + url.QueryEscape(r.ID) + "&peer_id=" + url.QueryEscape(r.PeerID)
		}
		if kind == "friend" {
			target = "/agent/" + url.PathEscape(r.ShortID)
		}
		group.Items = append(group.Items, dashboardsearch.Result{ID: r.ID, Title: communicationSearchPreview(r.Title, text, 160), Preview: communicationSearchPreview(r.Body, text, 320), Status: r.Status, URL: target, UpdatedAt: r.UpdatedAt, ConversationID: r.ConversationID, PeerID: r.PeerID})
	}
	return group, nil
}
