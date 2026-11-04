package consolev2

import (
	"context"
	"eigenflux_server/api/commissionaccess"
	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	sortapi "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discovery/transport"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/kitex/client/callopt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type discoveryFeedFake struct {
	feedservice.Client
	last     *sortapi.DiscoveryReq
	response *discovery.Response
}

func (f *discoveryFeedFake) Discovery(_ context.Context, r *sortapi.DiscoveryReq, _ ...callopt.Option) (*sortapi.DiscoveryResp, error) {
	f.last = r
	if f.response != nil {
		return transport.Response(*f.response, nil), nil
	}
	return transport.Response(map[string]any{"items": []any{map[string]any{"source_ref": map[string]any{"type": "agent", "id": "9007199254740993"}, "context_id": "1", "match": map[string]any{"score": .8, "scorer_version": "test"}}}}, nil), nil
}

func discoveryHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"CREATE TABLE agents (agent_id INTEGER, identity_state TEXT)", "CREATE TABLE agent_principals (principal_id INTEGER, agent_id INTEGER, status TEXT, revoked_at INTEGER)", "CREATE TABLE agent_credential_sessions (session_id INTEGER, principal_id INTEGER, scopes TEXT, access_token_hash TEXT, audience TEXT, revoked_at INTEGER, expires_at INTEGER, access_refresh_required BOOLEAN)", "CREATE TABLE agent_onboarding_v2 (agent_id INTEGER, state TEXT, current_step INTEGER)", "INSERT INTO agents VALUES(42,'active')", "INSERT INTO agent_principals VALUES(1,42,'active',NULL)", "INSERT INTO agent_onboarding_v2 VALUES(42,'completed',1)"} {
		if err = db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Exec("INSERT INTO agent_credential_sessions VALUES(1,1,'{feed:read,context:read,context:write}',?,'agent_v2',NULL,?,FALSE)", hashString("efv2a_discovery"), time.Now().Add(time.Hour).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestUnifiedDiscoveryAuthAndOwnerBoundary(t *testing.T) {
	db := discoveryHandlerTestDB(t)
	feed := &discoveryFeedFake{}
	svc := &Service{db: db, feedClient: feed}
	access, _ := commissionaccess.New(true, "43")
	h := server.New()
	svc.RegisterDiscovery(h, access)
	for _, tc := range []struct {
		body   string
		status int
	}{{`{"query":"hello","source_kinds":["agent"]}`, 200}, {`{"query":"hello","agent_id":"99"}`, 400}, {`{"query":"hello","source_kinds":["commission"]}`, 403}, {`{"commission_id":"42"}`, 403}, {`{"commission_id":"42","source_kinds":["agent"]}`, 403}} {
		feed.last = nil
		r := ut.PerformRequest(h.Engine, "POST", "/api/v2/discovery/search", &ut.Body{Body: strings.NewReader(tc.body), Len: len(tc.body)}, ut.Header{Key: "Authorization", Value: "Bearer efv2a_discovery"}).Result()
		if r.StatusCode() != tc.status {
			t.Fatalf("status=%d body=%s", r.StatusCode(), r.Body())
		}
		if tc.status == 200 {
			if strings.Contains(string(r.Body()), `"score":`) {
				t.Fatal("numeric ranking diagnostics exposed")
			}
			if feed.last == nil || feed.last.AgentId != 42 {
				t.Fatal("owner not derived from auth")
			}
		} else if feed.last != nil {
			t.Fatal("invalid request reached RPC")
		}
	}
	if status := ut.PerformRequest(h.Engine, "POST", "/api/v2/needs", nil).Result().StatusCode(); status != 404 {
		t.Fatalf("duplicate Need capture route remains: %d", status)
	}
	if status := ut.PerformRequest(h.Engine, "POST", "/api/v2/discovery/search", nil).Result().StatusCode(); status != 401 {
		t.Fatal(status)
	}
}

func TestUnifiedDiscoveryCommissionSummaryAcrossHTTP(t *testing.T) {
	price, duration := int64(0), int64(86400000)
	item := discovery.PublicItem(discovery.Candidate{Document: discovery.Document{Ref: discovery.SourceRef{Type: discovery.Commission, ID: 9007199254740993}, Preview: "Free review", PriceFen: &price, Currency: "CNY", DurationMS: &duration}, Score: discovery.Score{Value: .8, Version: "test"}})
	feed := &discoveryFeedFake{response: &discovery.Response{Items: []discovery.ResultItem{item}}}
	svc := &Service{db: discoveryHandlerTestDB(t), feedClient: feed}
	access, _ := commissionaccess.New(true, "42")
	h := server.New()
	svc.RegisterDiscovery(h, access)
	for _, tc := range []struct{ path, body string }{
		{"/api/v2/discovery/search", `{"query":"review","source_kinds":["commission"]}`},
		{"/api/v2/discovery/recommendations", `{"source_kinds":["commission"]}`},
	} {
		result := ut.PerformRequest(h.Engine, "POST", tc.path, &ut.Body{Body: strings.NewReader(tc.body), Len: len(tc.body)}, ut.Header{Key: "Authorization", Value: "Bearer efv2a_discovery"}).Result()
		if result.StatusCode() != 200 {
			t.Fatalf("summary request failed: %d %s", result.StatusCode(), result.Body())
		}
		var payload struct {
			Data discovery.Response `json:"data"`
		}
		if err := json.Unmarshal(result.Body(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Data.Items) != 1 {
			t.Fatalf("unexpected summary count: %s", result.Body())
		}
		got := payload.Data.Items[0]
		if got.Ref.ID != item.Ref.ID || got.Title != "Free review" || got.PriceFen == nil || *got.PriceFen != 0 || got.Currency != "CNY" || got.PromisedDeliveryMS == nil || *got.PromisedDeliveryMS != duration {
			t.Fatalf("summary lost in unified HTTP response: %s", result.Body())
		}
		if _, present := got.Match["score"]; present {
			t.Fatal("adding summary exposed numeric ranking diagnostics")
		}
	}
}
