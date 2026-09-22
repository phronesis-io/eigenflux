package consolev2

import (
	"context"
	"eigenflux_server/api/commissionaccess"
	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	sortapi "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/kitex_gen/eigenflux/sort/sortservice"
	"eigenflux_server/rpc/sort/discovery/transport"
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
	last *sortapi.DiscoveryReq
}

func (f *discoveryFeedFake) Discovery(_ context.Context, r *sortapi.DiscoveryReq, _ ...callopt.Option) (*sortapi.DiscoveryResp, error) {
	f.last = r
	return transport.Response(map[string]any{"items": []any{map[string]any{"source_ref": map[string]any{"type": "agent", "id": "9007199254740993"}, "context_id": "1", "match": map[string]any{"score": .8, "scorer_version": "test"}}}}, nil), nil
}

type discoverySortFake struct {
	sortservice.Client
	last *sortapi.DiscoveryReq
}

func (f *discoverySortFake) Discovery(_ context.Context, r *sortapi.DiscoveryReq, _ ...callopt.Option) (*sortapi.DiscoveryResp, error) {
	f.last = r
	return transport.Response(map[string]any{"context_id": "9007199254740993"}, nil), nil
}
func TestUnifiedDiscoveryAuthAndOwnerBoundary(t *testing.T) {
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
	feed := &discoveryFeedFake{}
	sort := &discoverySortFake{}
	svc := &Service{db: db, feedClient: feed}
	access, _ := commissionaccess.New(true, "43")
	h := server.New()
	svc.RegisterDiscovery(h, sort, access)
	for _, tc := range []struct {
		body   string
		status int
	}{{`{"query":"hello","source_kinds":["agent"]}`, 200}, {`{"query":"hello","agent_id":"99"}`, 400}, {`{"query":"hello","source_kinds":["commission"]}`, 403}} {
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
	r := ut.PerformRequest(h.Engine, "GET", "/api/v2/needs/9007199254740993", nil, ut.Header{Key: "Authorization", Value: "Bearer efv2a_discovery"}).Result()
	if r.StatusCode() != 200 || sort.last.AgentId != 42 || sort.last.GetResourceId() != 9007199254740993 {
		t.Fatalf("ID contract: %s", r.Body())
	}
	if status := ut.PerformRequest(h.Engine, "POST", "/api/v2/discovery/search", nil).Result().StatusCode(); status != 401 {
		t.Fatal(status)
	}
}
