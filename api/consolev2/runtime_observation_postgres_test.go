package consolev2

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/reqinfo"
	"fmt"
	"github.com/cloudwego/hertz/pkg/app"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"eigenflux_server/api/clients"
	"eigenflux_server/api/dal"
	apihandler "eigenflux_server/api/handler_gen/eigenflux/api"
	"eigenflux_server/api/middleware"
	authrpc "eigenflux_server/kitex_gen/eigenflux/auth"
	"eigenflux_server/kitex_gen/eigenflux/auth/authservice"
	"eigenflux_server/kitex_gen/eigenflux/base"
	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/config"
	sharedDB "eigenflux_server/pkg/db"
	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/kitex/client/callopt"
	redis "github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type runtimeEmptyFeed struct {
	feedservice.Client
	code int32
}

func (f *runtimeEmptyFeed) FetchFeed(context.Context, *feedrpc.FetchFeedReq, ...callopt.Option) (*feedrpc.FetchFeedResp, error) {
	return &feedrpc.FetchFeedResp{BaseResp: &base.BaseResp{Code: f.code}}, nil
}

type runtimeAuthClient struct {
	authservice.Client
	agentID int64
}

func (f *runtimeAuthClient) ValidateSession(context.Context, *authrpc.ValidateSessionReq, ...callopt.Option) (*authrpc.ValidateSessionResp, error) {
	return &authrpc.ValidateSessionResp{AgentId: f.agentID, BaseResp: &base.BaseResp{Code: 0}}, nil
}

func TestRuntimeObservationPostgresHTTP(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required for runtime HTTP/PostgreSQL contracts")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	priorDB := sharedDB.DB
	sharedDB.DB = database
	t.Cleanup(func() { sharedDB.DB = priorDB })
	id := time.Now().UnixNano()
	now := time.Now().UnixMilli()
	if err := database.Exec(`INSERT INTO agents (agent_id,email,agent_name,bio,created_at,updated_at,identity_state) VALUES (?,?,'Runtime HTTP','',?,?,'active')`, id, fmt.Sprintf("runtime-http-%d@example.test", id), now, now).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Exec("DELETE FROM agents WHERE agent_id = ?", id) })
	var principalID int64
	if err := database.Raw(`INSERT INTO agent_principals (agent_id,key_type,key_fingerprint,public_key,key_version,status,created_at,last_seen_at) VALUES (?,'ed25519-v1',?,decode(repeat('42',32),'hex'),1,'active',?,?) RETURNING principal_id`, id, fmt.Sprintf("runtime-http-%d", id), now, now).Scan(&principalID).Error; err != nil {
		t.Fatal(err)
	}
	token := fmt.Sprintf("efv2a_runtime_http_%d", id)
	if err := database.Exec(`INSERT INTO agent_credential_sessions (principal_id,family_id,access_token_hash,refresh_token_hash,audience,scopes,issued_at,expires_at,absolute_expires_at,last_seen_at) VALUES (?,?,?,?,'agent_v2',ARRAY['feed:read','commands:claim','settings:read','settings:write'],?,?,?,?)`, principalID, fmt.Sprintf("runtime-family-%d", id), hashString(token), hashString(token+"refresh"), now, now+3600000, now+7200000, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO agent_context_revisions (agent_id,revision,compiled_context,schema_version,generated_at) VALUES (?,1,'{}'::jsonb,1,?)`, id, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO agent_onboarding_v2 (agent_id,state,current_step,revision,active_context_revision,completed_at,created_at,updated_at) VALUES (?,'completed',5,1,1,?,?,?)`, id, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&dal.AgentSettings{AgentID: id, AgentCreatedAtMs: now}).Error; err != nil {
		t.Fatal(err)
	}
	cache := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: cache.Addr()})
	t.Cleanup(func() { rdb.Close() })
	svc, err := NewService(database, &fixedIDGenerator{id: id + 100}, &config.Config{EnableFeedV2: true, EnableControlChannelV2: true, ConsoleV2BootstrapSecret: "runtime-test", ConsoleV2OTPPepper: "runtime-test", ConsoleV2PublicURL: "https://console.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	svc.redisClient = rdb
	svc.SetFeedClient(&runtimeEmptyFeed{})
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	svc.Register(h)
	auth := ut.Header{Key: "Authorization", Value: "Bearer " + token}
	read := func() dal.AgentSettings {
		t.Helper()
		var row dal.AgentSettings
		if err := database.First(&row, "agent_id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	call := func(method, path string, body interface{}, headers ...ut.Header) (int, map[string]interface{}) {
		t.Helper()
		headers = append(headers, auth)
		status, payload, _ := performJSON(t, h, method, path, body, headers...)
		return status, payload
	}
	for _, path := range []string{"/api/v2/feed", "/api/v2/runtime/heartbeat", "/api/v2/agent-settings/heartbeat-compatibility"} {
		t.Run(path, func(t *testing.T) {
			if err := database.Model(&dal.AgentSettings{}).Where("agent_id = ?", id).UpdateColumns(map[string]interface{}{"runtime_name": "", "runtime_version": "", "runtime_reported_at": 0, "last_activity_at": 0, "mode": ""}).Error; err != nil {
				t.Fatal(err)
			}
			method := http.MethodPost
			body := map[string]interface{}{}
			if path == "/api/v2/runtime/heartbeat" {
				body["runtime_instance_id"] = "runtime-test"
				body["capabilities"] = []string{}
			}
			if path == "/api/v2/agent-settings/heartbeat-compatibility" {
				method = http.MethodPut
				body["heartbeat_contract_version"] = "eigenflux_heartbeat.v1"
				body["skill_revision"] = "runtime-test"
			}
			status, payload := call(method, path, body, ut.Header{Key: "X-Client-Host", Value: "workbuddy/5.5.4"}, ut.Header{Key: "X-Client-Mode", Value: "skill"}, ut.Header{Key: "X-CLI-Ver", Value: "0.0.44"})
			if status != 200 {
				t.Fatalf("status=%d payload=%+v", status, payload)
			}
			row := read()
			if row.RuntimeName != "workbuddy" || row.RuntimeVersion != "5.5.4" || row.Mode != "skill" || row.LastActivityAt <= 0 {
				t.Fatalf("route did not persist metadata/activity: %+v", row)
			}
		})
	}

	t.Run("authenticated command claim and completion templates", func(t *testing.T) {
		if err := database.Exec("UPDATE agent_runtime_leases SET context_revision_applied=1 WHERE agent_id=?", id).Error; err != nil {
			t.Fatal(err)
		}
		var commandID int64
		if err := database.Raw(`INSERT INTO agent_commands (agent_id,command_type,payload,payload_hash,required_context_revision,status,idempotency_key,created_at) VALUES (?,'human_instruction','{}'::jsonb,'runtime-test',1,'pending','runtime-observation-test',?) RETURNING command_id`, id, now).Scan(&commandID).Error; err != nil {
			t.Fatal(err)
		}
		resetActivity := func() {
			t.Helper()
			if err := database.Model(&dal.AgentSettings{}).Where("agent_id = ?", id).UpdateColumn("last_activity_at", 0).Error; err != nil {
				t.Fatal(err)
			}
		}
		resetActivity()
		status, payload := call(http.MethodPost, fmt.Sprintf("/api/v2/agent-commands/%d/claim", commandID), map[string]interface{}{"runtime_instance_id": "runtime-test", "applied_context_revision": 1})
		if status != 200 {
			t.Fatalf("claim status=%d payload=%+v", status, payload)
		}
		if row := read(); row.LastActivityAt <= 0 {
			t.Fatalf("command claim did not record activity: %+v", row)
		}
		proof := responseData(t, payload)
		resetActivity()
		status, payload = call(http.MethodPost, fmt.Sprintf("/api/v2/agent-commands/%d/complete", commandID), map[string]interface{}{"runtime_instance_id": "runtime-test", "claim_epoch": proof["claim_epoch"], "claim_token": proof["claim_token"], "status": "completed", "result": map[string]interface{}{}})
		if status != 200 {
			t.Fatalf("completion status=%d payload=%+v", status, payload)
		}
		if row := read(); row.LastActivityAt <= 0 {
			t.Fatalf("command completion did not record activity: %+v", row)
		}
	})
	t.Run("V1 authenticated Feed", func(t *testing.T) {
		previousAuth, previousFeed := clients.AuthClient, clients.FeedClient
		defer func() { clients.AuthClient = previousAuth; clients.FeedClient = previousFeed }()
		clients.AuthClient = &runtimeAuthClient{agentID: id}
		feed := &runtimeEmptyFeed{}
		clients.FeedClient = feed
		h.GET("/api/v1/items/feed", middleware.ClientInfoMiddleware(), middleware.AuthMiddleware(), apihandler.Feed)
		if err := database.Model(&dal.AgentSettings{}).Where("agent_id = ?", id).UpdateColumns(map[string]interface{}{"runtime_reported_at": 0, "last_activity_at": 0}).Error; err != nil {
			t.Fatal(err)
		}
		headers := []ut.Header{{Key: "Authorization", Value: "Bearer local-test"}, {Key: "X-CLI-Ver", Value: "0.0.44"}, {Key: "X-Client-Host", Value: "workbuddy/5.5.4"}, {Key: "X-Client-Mode", Value: "skill"}}
		response := ut.PerformRequest(h.Engine, http.MethodGet, "/api/v1/items/feed?action=load_more", nil, headers...).Result()
		if response.StatusCode() != 200 {
			t.Fatalf("V1 status=%d", response.StatusCode())
		}
		before := read()
		if before.LastActivityAt <= 0 || before.RuntimeName != "workbuddy" {
			t.Fatalf("V1 successful Feed not observed: %+v", before)
		}
		feed.code = 123
		headers[2].Value = "openclaw/old"
		response = ut.PerformRequest(h.Engine, http.MethodGet, "/api/v1/items/feed?action=load_more", nil, headers...).Result()
		if response.StatusCode() != 200 {
			t.Fatalf("V1 business failure must test HTTP200: %d", response.StatusCode())
		}
		after := read()
		if after.RuntimeName != before.RuntimeName || after.RuntimeReportedAt != before.RuntimeReportedAt || after.LastActivityAt != before.LastActivityAt {
			t.Fatalf("V1 business failure mutated identity: %+v", after)
		}
	})
	t.Run("Discovery structured product projection", func(t *testing.T) {
		shortID, err := agentidentity.GenerateShortID()
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Exec("UPDATE agents SET short_id=? WHERE agent_id=?", shortID, id).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Exec(`INSERT INTO agent_cards (agent_id,public_card,private_card,schema_version,source_version,rebuild_fence,card_version,public_card_version,generated_at,public_card_generated_at) VALUES (?,'{"runtime":"plugin","runtime_name":"workbuddy","runtime_version":"5.5.4","runtime_mode":"skill"}'::jsonb,'{}'::jsonb,5,1,0,1,1,?,?)`, id, now, now).Error; err != nil {
			t.Fatal(err)
		}
		rules := []homeDiscoveryRule{{Key: "active", Rows: []homeDiscoveryCandidateRow{{AgentID: id}}}}
		rows, err := svc.hydrateHomeDiscovery(context.Background(), rules)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].RuntimeName != "workbuddy" || rows[0].RuntimeVersion != "5.5.4" || rows[0].RuntimeMode != "skill" || rows[0].Runtime != "plugin" {
			t.Fatalf("Discovery lost independent product fields: %+v", rows)
		}
		if err := database.Exec(`UPDATE agent_cards SET public_card='{"runtime":"plugin"}'::jsonb WHERE agent_id=?`, id).Error; err != nil {
			t.Fatal(err)
		}
		rows, err = svc.hydrateHomeDiscovery(context.Background(), rules)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].RuntimeName != "" || rows[0].RuntimeMode != "" {
			t.Fatalf("Discovery invented facts from legacy runtime: %+v", rows)
		}
	})

	t.Run("optional malformed metadata does not fail HTTP report", func(t *testing.T) {
		if err := database.Model(&dal.AgentSettings{}).Where("agent_id = ?", id).UpdateColumns(map[string]interface{}{"runtime_reported_at": 0, "last_activity_at": 0, "cli_version": "0.0.44", "model": "known-model"}).Error; err != nil {
			t.Fatal(err)
		}
		for _, request := range []struct{ method, path string }{{http.MethodPost, "/api/v2/feed"}, {http.MethodPut, "/api/v2/agent-settings"}} {
			status, payload := call(request.method, request.path, map[string]interface{}{}, ut.Header{Key: "X-Client-Host", Value: "workbuddy/5.5.4"}, ut.Header{Key: "X-Client-Mode", Value: "skill"}, ut.Header{Key: "X-CLI-Ver", Value: strings.Repeat("v", 33)}, ut.Header{Key: "X-Client-Model", Value: strings.Repeat("m", 129)})
			if status != 200 {
				t.Fatalf("oversized optional metadata failed %s: status=%d payload=%+v", request.path, status, payload)
			}
			row := read()
			if row.RuntimeName != "workbuddy" || row.Mode != "skill" || row.LastActivityAt <= 0 || row.CLIVersion != "0.0.44" || row.Model != "known-model" {
				t.Fatalf("valid HTTP facts lost or optional metadata truncated: %+v", row)
			}
		}
	})
	t.Run("entry time survives blocked authentication", func(t *testing.T) {
		for index, handler := range []app.HandlerFunc{apihandler.PutMySettings, svc.createHandoff} {
			if err := database.Model(&dal.AgentSettings{}).Where("agent_id = ?", id).UpdateColumn("runtime_reported_at", 0).Error; err != nil {
				t.Fatal(err)
			}
			entered := make(chan int64, 1)
			release := make(chan struct{})
			done := make(chan int, 1)
			block := func(ctx context.Context, c *app.RequestContext) {
				entered <- reqinfo.RequestStartedAt(ctx)
				<-release
				c.Next(ctx)
			}
			path := fmt.Sprintf("/runtime-entry-test/%d", index)
			h.PUT(path, middleware.ClientInfoMiddleware(), block, svc.agentAuth("settings:write"), handler)
			body := []byte(`{"mode":"plugin"}`)
			if index == 1 {
				body = []byte(`{"browser_nonce":"01234567890123456789012345678901"}`)
			}
			go func() {
				response := ut.PerformRequest(h.Engine, http.MethodPut, path, &ut.Body{Body: bytes.NewReader(body), Len: len(body)}, auth, ut.Header{Key: "Content-Type", Value: "application/json"}, ut.Header{Key: "X-Client-Host", Value: "openclaw/old"}, ut.Header{Key: "X-Client-Mode", Value: "plugin"}).Result()
				done <- response.StatusCode()
			}()
			entry := <-entered
			if entry <= 0 {
				close(release)
				<-done
				t.Fatal("request timestamp was not captured before auth")
			}
			_, err := dal.ObserveRuntime(database, id, dal.RuntimeObservation{Host: "workbuddy/5.5.4", Mode: "skill", ObservedAt: entry + 1, Explicit: true})
			if err != nil {
				close(release)
				<-done
				t.Fatal(err)
			}
			// The old request reaches its handler after the newer explicit report.
			time.Sleep(5 * time.Millisecond)
			close(release)
			if status := <-done; status != 409 {
				t.Fatalf("delayed explicit request %d status=%d, want409", index, status)
			}
			row := read()
			if row.RuntimeName != "workbuddy" || row.Mode != "skill" || row.RuntimeReportedAt != entry+1 {
				t.Fatalf("delayed request crossed entry fence: %+v", row)
			}
		}
	})
	before := read()
	status, payload := call(http.MethodGet, "/api/v2/agent-settings", map[string]interface{}{}, ut.Header{Key: "X-Client-Host", Value: "codex/old"}, ut.Header{Key: "X-Client-Mode", Value: "plugin"})
	if status != 200 {
		t.Fatalf("read status=%d payload=%+v", status, payload)
	}
	after := read()
	if after.RuntimeName != before.RuntimeName || after.LastActivityAt != before.LastActivityAt || after.RuntimeReportedAt != before.RuntimeReportedAt {
		t.Fatalf("settings read altered observation: before=%+v after=%+v", before, after)
	}
	status, payload = call(http.MethodPut, "/api/v2/agent-settings", map[string]interface{}{"mode": "skill"}, ut.Header{Key: "X-Client-Host", Value: "workbuddy"}, ut.Header{Key: "X-Client-Mode", Value: "plugin"})
	if status != 200 {
		t.Fatalf("settings status=%d payload=%+v", status, payload)
	}
	after = read()
	if after.RuntimeVersion != "" || after.Mode != "skill" || after.LastActivityAt != before.LastActivityAt {
		t.Fatalf("explicit settings facts/active boundary failed: %+v", after)
	}
	before = after
	status, payload = call(http.MethodPost, "/api/v2/runtime/heartbeat", map[string]interface{}{"runtime_instance_id": ""}, ut.Header{Key: "X-Client-Host", Value: "codex"})
	if status != 400 {
		t.Fatalf("invalid heartbeat status=%d payload=%+v", status, payload)
	}
	after = read()
	if after.RuntimeName != before.RuntimeName || after.LastActivityAt != before.LastActivityAt {
		t.Fatalf("failed heartbeat changed observation: %+v", after)
	}
}
