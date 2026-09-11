package consolev2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/kitex/client/callopt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"eigenflux_server/api/commissionaccess"
	"eigenflux_server/api/commissiondiscovery"
	base "eigenflux_server/kitex_gen/eigenflux/base"
	sortmodel "eigenflux_server/kitex_gen/eigenflux/sort"
)

type discoveryV2Sort struct {
	search    *sortmodel.SearchCommissionsReq
	recommend *sortmodel.RecommendCommissionsReq
}

func (f *discoveryV2Sort) SearchCommissions(_ context.Context, req *sortmodel.SearchCommissionsReq, _ ...callopt.Option) (*sortmodel.SearchCommissionsResp, error) {
	f.search = req
	return &sortmodel.SearchCommissionsResp{BaseResp: &base.BaseResp{}, Candidates: []*sortmodel.CommissionCandidate{{CommissionId: 355993847441915904}}}, nil
}

func (f *discoveryV2Sort) RecommendCommissions(_ context.Context, req *sortmodel.RecommendCommissionsReq, _ ...callopt.Option) (*sortmodel.RecommendCommissionsResp, error) {
	f.recommend = req
	return &sortmodel.RecommendCommissionsResp{BaseResp: &base.BaseResp{}, Candidates: []*sortmodel.CommissionCandidate{{CommissionId: 355993847441915904}}}, nil
}

func TestCommissionDiscoveryV2AuthenticationAndRouting(t *testing.T) {
	for _, tc := range []struct {
		name, token, scopes, state, update string
		allowed, disabled                  bool
		status                             int
	}{
		{name: "success", token: "efv2a_test", scopes: "{feed:read}", state: "completed", allowed: true, status: 200},
		{name: "missing token", scopes: "{feed:read}", state: "completed", allowed: true, status: 401},
		{name: "legacy token", token: "at_test", scopes: "{feed:read}", state: "completed", allowed: true, status: 401},
		{name: "wrong scope", token: "efv2a_test", scopes: "{profile:read}", state: "completed", allowed: true, status: 401},
		{name: "incomplete onboarding", token: "efv2a_test", scopes: "{feed:read}", state: "draft", allowed: true, status: 409},
		{name: "not allowlisted", token: "efv2a_test", scopes: "{feed:read}", state: "completed", status: 403},
		{name: "revoked", token: "efv2a_test", scopes: "{feed:read}", state: "completed", allowed: true, update: "UPDATE agent_credential_sessions SET revoked_at=1", status: 401},
		{name: "expired", token: "efv2a_test", scopes: "{feed:read}", state: "completed", allowed: true, update: "UPDATE agent_credential_sessions SET expires_at=0", status: 401},
		{name: "refresh required", token: "efv2a_test", scopes: "{feed:read}", state: "completed", allowed: true, update: "UPDATE agent_credential_sessions SET access_refresh_required=TRUE", status: 401},
		{name: "inactive identity", token: "efv2a_test", scopes: "{feed:read}", state: "completed", allowed: true, update: "UPDATE agents SET identity_state='deleted'", status: 401},
		{name: "disabled", token: "efv2a_test", scopes: "{feed:read}", state: "completed", allowed: true, disabled: true, status: 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })
			for _, sql := range []string{
				"CREATE TABLE agents (agent_id INTEGER, identity_state TEXT)",
				"CREATE TABLE agent_principals (principal_id INTEGER, agent_id INTEGER, status TEXT, revoked_at INTEGER)",
				"CREATE TABLE agent_credential_sessions (session_id INTEGER, principal_id INTEGER, scopes TEXT, access_token_hash TEXT, audience TEXT, revoked_at INTEGER, expires_at INTEGER, access_refresh_required BOOLEAN)",
				"CREATE TABLE agent_onboarding_v2 (agent_id INTEGER, state TEXT, current_step INTEGER)",
				"INSERT INTO agents VALUES (42, 'active')",
				"INSERT INTO agent_principals VALUES (1, 42, 'active', NULL)",
			} {
				if err := db.Exec(sql).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Exec("INSERT INTO agent_credential_sessions VALUES (1,1,?,?,'agent_v2',NULL,?,FALSE)", tc.scopes, hashString("efv2a_test"), time.Now().Add(time.Hour).UnixMilli()).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("INSERT INTO agent_onboarding_v2 VALUES (42,?,1)", tc.state).Error; err != nil {
				t.Fatal(err)
			}
			if tc.update != "" {
				if err := db.Exec(tc.update).Error; err != nil {
					t.Fatal(err)
				}
			}
			ids := "43"
			if tc.allowed {
				ids = "42"
			}
			access, err := commissionaccess.New(true, ids)
			if err != nil {
				t.Fatal(err)
			}
			backend := &discoveryV2Sort{}
			discovery := commissiondiscovery.New(backend, &fixedIDGenerator{}, nil, access)
			h := server.New()
			commissiondiscovery.Register(h, discovery)
			if tc.disabled {
				discovery = nil
			}
			svc := &Service{db: db}
			svc.RegisterCommissionDiscovery(h, discovery)
			for _, path := range []string{"/api/v2/commissions/search?query=prototype&limit=3", "/api/v2/commissions/search?commission_id=355993847441915904", "/api/v2/commissions/recommendations?agent_id=999"} {
				response := ut.PerformRequest(h.Engine, "GET", path, nil, ut.Header{Key: "Authorization", Value: "Bearer " + tc.token}).Result()
				if response.StatusCode() != tc.status {
					t.Fatalf("%s: status=%d body=%s", path, response.StatusCode(), response.Body())
				}
				if tc.status == 200 && !strings.Contains(string(response.Body()), `"commission_id":"355993847441915904"`) {
					t.Fatalf("missing string ID: %s", response.Body())
				}
				if tc.status == 200 && strings.Contains(path, "query=") {
					if backend.search.Query != "prototype" || backend.search.GetLimit() != 3 {
						t.Fatalf("search parameters changed: %+v", backend.search)
					}
				}
			}
			if tc.status == 200 {
				if backend.search.GetCommissionId() != 355993847441915904 || backend.recommend.AgentId != 42 {
					t.Fatalf("incorrect RPC arguments: %+v %+v", backend.search, backend.recommend)
				}
			} else if backend.search != nil || backend.recommend != nil {
				t.Fatal("unauthorized request reached Sort")
			}
			for _, path := range []string{"/api/v1/commissions/search", "/api/v1/commissions/recommendations"} {
				if status := ut.PerformRequest(h.Engine, "GET", path, nil).Result().StatusCode(); status != 401 {
					t.Fatalf("V1 route %s changed: %d", path, status)
				}
			}
		})
	}
}
