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

	"eigenflux_server/kitex_gen/eigenflux/profile"
	search "eigenflux_server/kitex_gen/eigenflux/recordsearch"
	"eigenflux_server/pkg/dashboardsearch"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/reqinfo"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/kitex/client/callopt"
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
		ctx = metainfo.WithPersistentValue(ctx, reqinfo.KeyAgentID, strconv.FormatInt(owner, 10))
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
				group, err = s.searchDashboardRPC(ctx, owner, category, text, status, cursor, limit)
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

type dashboardPMClient interface {
	SearchMessages(context.Context, *search.SearchReq, ...callopt.Option) (*search.SearchResp, error)
	SearchFriends(context.Context, *search.SearchReq, ...callopt.Option) (*search.SearchResp, error)
}
type dashboardItemClient interface {
	SearchOwnedBroadcasts(context.Context, *search.SearchReq, ...callopt.Option) (*search.SearchResp, error)
}
type dashboardProfileClient interface {
	MatchAgentsByName(context.Context, *profile.MatchAgentsByNameReq, ...callopt.Option) (*profile.MatchAgentsByNameResp, error)
}

func (s *Service) SetDashboardSearchClients(pm dashboardPMClient, item dashboardItemClient, profile dashboardProfileClient) {
	s.dashboardPM, s.dashboardItem, s.dashboardProfile = pm, item, profile
}

func (s *Service) dashboardCounterparties(ctx context.Context, text string) ([]int64, error) {
	if s.dashboardProfile == nil {
		return nil, errors.New("profile search unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resp, err := s.dashboardProfile.MatchAgentsByName(ctx, &profile.MatchAgentsByNameReq{Query: text})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.BaseResp == nil || resp.BaseResp.Code != 0 {
		return nil, errors.New("profile search failed")
	}
	if resp.HasMore {
		return nil, errors.New("counterparty query too broad")
	}
	return resp.AgentIds, nil
}

func (s *Service) searchDashboardRPC(ctx context.Context, owner int64, kind, text, status string, cursor int64, limit int) (dashboardsearch.Group, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req := &search.SearchReq{OwnerAgentId: owner, Query: text, Status: status, Cursor: cursor, Limit: int32(limit)}
	var resp *search.SearchResp
	var err error
	switch kind {
	case "message", "friend":
		if s.dashboardPM == nil {
			return dashboardsearch.Group{}, errors.New("PM search unavailable")
		}
		if kind == "message" {
			resp, err = s.dashboardPM.SearchMessages(ctx, req)
		} else {
			resp, err = s.dashboardPM.SearchFriends(ctx, req)
		}
	case "broadcast":
		if s.dashboardItem == nil {
			return dashboardsearch.Group{}, errors.New("item search unavailable")
		}
		resp, err = s.dashboardItem.SearchOwnedBroadcasts(ctx, req)
	default:
		return dashboardsearch.Group{}, errors.New("invalid search type")
	}
	if err != nil {
		return dashboardsearch.Group{}, err
	}
	if resp == nil || resp.BaseResp == nil || resp.BaseResp.Code != 0 {
		return dashboardsearch.Group{}, errors.New("record search failed")
	}
	group := dashboardsearch.Group{Type: kind, Items: []dashboardsearch.Result{}, HasMore: resp.HasMore}
	if resp.HasMore {
		group.NextCursor = strconv.FormatInt(resp.NextCursor, 10)
	}
	for _, r := range resp.Items {
		if r == nil {
			return dashboardsearch.Group{}, errors.New("invalid record search response")
		}
		id := strconv.FormatInt(r.Id, 10)
		conversationID, peerID := "", ""
		if r.GetConversationId() > 0 {
			conversationID = strconv.FormatInt(r.GetConversationId(), 10)
		}
		if r.GetPeerId() > 0 {
			peerID = strconv.FormatInt(r.GetPeerId(), 10)
		}
		target := "/dashboard/broadcasts/" + id
		if kind == "message" {
			target = "/dashboard/messages?conversation_id=" + url.QueryEscape(conversationID) + "&message_id=" + url.QueryEscape(id) + "&peer_id=" + url.QueryEscape(peerID)
		}
		if kind == "friend" {
			target = "/agent/" + url.PathEscape(r.GetShortId())
		}
		group.Items = append(group.Items, dashboardsearch.Result{ID: id, Title: r.Title, Preview: r.Preview, Status: r.Status, URL: target, UpdatedAt: r.UpdatedAt, ConversationID: conversationID, PeerID: peerID})
	}
	return group, nil
}
