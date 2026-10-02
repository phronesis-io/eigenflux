package consolev2

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/profile"
	search "eigenflux_server/kitex_gen/eigenflux/recordsearch"
	"eigenflux_server/pkg/dashboardsearch"
	"eigenflux_server/pkg/reqinfo"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/lib/pq"
)

func TestDashboardSearchRejectsInvalidInputAndPreservesScope(t *testing.T) {
	s := &Service{}
	for _, query := range []string{"q=", "q=x&type=secret", "q=x&limit=51", "q=x&cursor=1", "q=x&type=broadcast&status=draft", "q=x&type=friend&cursor=-1"} {
		c := app.NewContext(0)
		c.Set("agent_id", int64(1))
		c.Request.SetRequestURI("/search?" + query)
		s.dashboardSearch(nil)(context.Background(), c)
		if c.Response.StatusCode() != 400 {
			t.Fatalf("%s: %s", query, c.Response.Body())
		}
	}
	c := app.NewContext(0)
	c.Set("agent_id", int64(1))
	c.Set("agent_scopes", pq.StringArray{"profile:read"})
	c.Request.SetRequestURI("/search?q=x&type=message")
	s.dashboardSearch(nil)(context.Background(), c)
	if !strings.Contains(string(c.Response.Body()), "AGENT_SCOPE_REQUIRED") {
		t.Fatal(string(c.Response.Body()))
	}
	c = app.NewContext(0)
	c.Set("agent_id", int64(1))
	c.Request.SetRequestURI("/search?q=x&type=service")
	s.dashboardSearch(nil)(context.Background(), c)
	if !strings.Contains(string(c.Response.Body()), "SEARCH_UNAVAILABLE") {
		t.Fatal(string(c.Response.Body()))
	}
}

type dashboardSearchRPCStub struct {
	search  func(context.Context, string, *search.SearchReq) (*search.SearchResp, error)
	profile func(context.Context, *profile.MatchAgentsByNameReq) (*profile.MatchAgentsByNameResp, error)
}

func (f dashboardSearchRPCStub) SearchMessages(ctx context.Context, req *search.SearchReq, _ ...callopt.Option) (*search.SearchResp, error) {
	return f.search(ctx, "message", req)
}
func (f dashboardSearchRPCStub) SearchFriends(ctx context.Context, req *search.SearchReq, _ ...callopt.Option) (*search.SearchResp, error) {
	return f.search(ctx, "friend", req)
}
func (f dashboardSearchRPCStub) SearchOwnedBroadcasts(ctx context.Context, req *search.SearchReq, _ ...callopt.Option) (*search.SearchResp, error) {
	return f.search(ctx, "broadcast", req)
}
func (f dashboardSearchRPCStub) MatchAgentsByName(ctx context.Context, req *profile.MatchAgentsByNameReq, _ ...callopt.Option) (*profile.MatchAgentsByNameResp, error) {
	return f.profile(ctx, req)
}

func TestDashboardSearchUsesAuthenticatedRPCAndProjectsStringIDs(t *testing.T) {
	for _, kind := range []string{"message", "friend", "broadcast"} {
		t.Run(kind, func(t *testing.T) {
			called := false
			fake := dashboardSearchRPCStub{search: func(ctx context.Context, gotKind string, req *search.SearchReq) (*search.SearchResp, error) {
				called = true
				if gotKind != kind || req.OwnerAgentId != 1 || reqinfo.AuthFromContext(ctx).AgentID != 1 || req.Query != "合同_%!" || req.Cursor != 9007199254740995 || req.Limit != 2 || req.Status != "" {
					t.Fatalf("%s: %+v", gotKind, req)
				}
				conv, peer, short := int64(11), int64(2), "AbCdE"
				return &search.SearchResp{Items: []*search.Record{{Id: 9007199254740993, Title: "title", Preview: "合同_%!", Status: "open", UpdatedAt: 7, ConversationId: &conv, PeerId: &peer, ShortId: &short}}, NextCursor: 9007199254740993, HasMore: true, BaseResp: &base.BaseResp{}}, nil
			}}
			// Deliberately no DB: the search BFF must work only through service clients.
			s := &Service{enableCommunication: true}
			s.SetDashboardSearchClients(fake, fake, fake)
			c := app.NewContext(0)
			c.Set("agent_id", int64(1))
			c.Request.SetRequestURI("/search?" + url.Values{"q": {"合同_%!"}, "type": {kind}, "agent_id": {"2"}, "cursor": {"9007199254740995"}, "limit": {"2"}}.Encode())
			s.dashboardSearch(nil)(context.Background(), c)
			var payload struct {
				Data struct {
					Groups []dashboardsearch.Group `json:"groups"`
				} `json:"data"`
			}
			if err := json.Unmarshal(c.Response.Body(), &payload); err != nil {
				t.Fatal(err)
			}
			if !called || len(payload.Data.Groups) != 1 {
				t.Fatal(string(c.Response.Body()))
			}
			group := payload.Data.Groups[0]
			if group.Type != kind || group.Error != "" || !group.HasMore || group.NextCursor != "9007199254740993" || len(group.Items) != 1 {
				t.Fatal(group)
			}
			hit := group.Items[0]
			if hit.ID != "9007199254740993" || hit.ConversationID != "11" || hit.PeerID != "2" || hit.Preview != "合同_%!" {
				t.Fatal(hit)
			}
			wantURL := map[string]string{"message": "/dashboard/messages?conversation_id=11&message_id=9007199254740993&peer_id=2", "friend": "/agent/AbCdE", "broadcast": "/dashboard/broadcasts/9007199254740993"}[kind]
			if hit.URL != wantURL {
				t.Fatal(hit.URL)
			}
		})
	}
}

func TestDashboardSearchRPCFailuresAreCategoryErrors(t *testing.T) {
	for _, failure := range []string{"transport", "nil", "missing status", "business"} {
		t.Run(failure, func(t *testing.T) {
			fake := dashboardSearchRPCStub{search: func(context.Context, string, *search.SearchReq) (*search.SearchResp, error) {
				switch failure {
				case "transport":
					return nil, errors.New("offline")
				case "nil":
					return nil, nil
				case "missing status":
					return &search.SearchResp{}, nil
				default:
					return &search.SearchResp{BaseResp: &base.BaseResp{Code: 403}}, nil
				}
			}}
			s := &Service{enableCommunication: true}
			s.SetDashboardSearchClients(fake, fake, fake)
			c := app.NewContext(0)
			c.Set("agent_id", int64(1))
			c.Request.SetRequestURI("/search?q=x&type=message")
			s.dashboardSearch(nil)(context.Background(), c)
			if !strings.Contains(string(c.Response.Body()), "SEARCH_UNAVAILABLE") {
				t.Fatal(string(c.Response.Body()))
			}
		})
	}
}

func TestDashboardCounterpartiesRejectsTruncatedProfileResults(t *testing.T) {
	for _, more := range []bool{false, true} {
		fake := dashboardSearchRPCStub{profile: func(ctx context.Context, req *profile.MatchAgentsByNameReq) (*profile.MatchAgentsByNameResp, error) {
			if req.Query != "name" {
				t.Fatal(req)
			}
			return &profile.MatchAgentsByNameResp{AgentIds: []int64{2}, HasMore: more, BaseResp: &base.BaseResp{}}, nil
		}}
		s := &Service{}
		s.SetDashboardSearchClients(fake, fake, fake)
		ids, err := s.dashboardCounterparties(context.Background(), "name")
		if more && err == nil {
			t.Fatal("silently truncated")
		}
		if !more && (err != nil || len(ids) != 1 || ids[0] != 2) {
			t.Fatalf("%v %v", ids, err)
		}
	}
}
