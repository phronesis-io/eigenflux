package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/base"
	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	"eigenflux_server/pkg/integrationfeed"
	"eigenflux_server/pkg/integrationidentity"
	"eigenflux_server/pkg/integrationprofile"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/kitex/client/callopt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestEnterpriseHTTPFeedRetriesAndDisable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := db.DB()
	conn.SetMaxOpenConns(1)
	defer conn.Close()
	if err := db.AutoMigrate(&integrationidentity.App{}, &integrationidentity.Agent{}, &integrationidentity.Installation{}, &integrationprofile.MatchingProfile{}, &integrationfeed.Batch{}); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`CREATE TABLE integration_control(id INTEGER PRIMARY KEY,enabled BOOLEAN,revision BIGINT)`,
		`INSERT INTO integration_control VALUES(1,true,1)`,
		`ALTER TABLE integration_apps ADD COLUMN max_feed_batches_per_day INTEGER DEFAULT 10000`,
		`ALTER TABLE integration_apps ADD COLUMN feed_usage_day BIGINT DEFAULT 0`,
		`ALTER TABLE integration_apps ADD COLUMN feed_batches_today BIGINT DEFAULT 0`,
		`ALTER TABLE integration_apps ADD COLUMN feed_batches_total BIGINT DEFAULT 0`,
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`ALTER TABLE integration_agents ADD COLUMN request_count BIGINT DEFAULT 0`,
		`ALTER TABLE integration_agents ADD COLUMN last_request_at BIGINT DEFAULT 0`,
		`ALTER TABLE integration_agents ADD COLUMN last_failure_code TEXT DEFAULT ''`,
		`ALTER TABLE integration_agents ADD COLUMN last_failure_at BIGINT DEFAULT 0`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	secret, hash, err := integrationidentity.NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&integrationidentity.App{AppID: "partner", Enabled: true, CredentialHash: hash, MinPollIntervalSeconds: 3600}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&integrationidentity.Agent{AgentID: 1, AppID: "partner", Enabled: true, RequestedPollIntervalSeconds: 3600}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&integrationidentity.Installation{InstallationID: "install", AgentID: 1, Enabled: true, RuntimeInstanceID: "run"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&integrationprofile.MatchingProfile{AgentID: 1, SourceRevision: 5, Keywords: "[]"}).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	sourceFails := false
	h := server.New()
	(&Service{Store: &integrationidentity.Store{DB: db}, Feed: func(_ context.Context, id, revision int64, limit int) (integrationfeed.Generated, error) {
		calls++
		if sourceFails {
			return integrationfeed.Generated{}, errors.New("lost response")
		}
		if id != 1 || revision != 5 || limit != 20 {
			t.Fatal(id, revision, limit)
		}
		return integrationfeed.Generated{Payload: json.RawMessage(`{"schema_version":"integration.feed.v1","items":[{"item_id":"9007199254740993"}]}`), ItemCount: 1, MatchingVersion: revision}, nil
	}}).Register(h)
	request := func(method, payload string) (int, []byte, string) {
		r := ut.PerformRequest(h.Engine, method, "/api/v2/integrations/agents/me/feed", &ut.Body{Body: strings.NewReader(payload), Len: len(payload)}, ut.Header{Key: "Authorization", Value: "Bearer " + secret}, ut.Header{Key: "X-EigenFlux-Installation-ID", Value: "install"}, ut.Header{Key: "X-EigenFlux-Runtime-Instance-ID", Value: "run"}).Result()
		return r.StatusCode(), append([]byte(nil), r.Body()...), string(r.Header.Peek("Retry-After"))
	}
	status, first, _ := request("POST", `{"sequence":1}`)
	if status != 200 || calls != 1 || !strings.Contains(string(first), "9007199254740993") {
		t.Fatal(status, string(first), calls)
	}
	status, again, _ := request("POST", `{"sequence":1}`)
	if status != 200 || calls != 1 || string(first) != string(again) {
		t.Fatal("retry changed payload", status, string(again), calls)
	}
	status, _, retry := request("POST", `{"sequence":2}`)
	if status != 429 || retry == "" || calls != 1 {
		t.Fatal(status, retry, calls)
	}
	if err := db.Exec(`UPDATE integration_apps SET enabled=false`).Error; err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		status, _, _ = request(method, `{"sequence":1}`)
		if status != 403 || calls != 1 {
			t.Fatal("disabled cache read", status, calls)
		}
	}
	if err := db.Exec(`UPDATE integration_apps SET enabled=true`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE integration_agents SET next_poll_at=0`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE integration_feed_batches SET created_at=created_at-3600000`).Error; err != nil {
		t.Fatal(err)
	}
	sourceFails = true
	status, _, _ = request("POST", `{"sequence":2}`)
	if status != 503 || calls != 2 {
		t.Fatal(status, calls)
	}
	status, _, _ = request("POST", `{"sequence":2}`)
	if status != 409 || calls != 2 {
		t.Fatal("unknown was regenerated", status, calls)
	}
	var observation struct {
		RequestCount    int64
		LastFailureCode string
	}
	if err := db.Table("integration_agents").Where("agent_id=1").Scan(&observation).Error; err != nil || observation.RequestCount != 7 || observation.LastFailureCode != "FEED_GENERATION_UNKNOWN" {
		t.Fatal(observation, err)
	}
}

type rpcFeedFake struct {
	feedservice.Client
	fetch func(*feedrpc.FetchFeedReq) *feedrpc.FetchFeedResp
}

func (f rpcFeedFake) FetchFeed(_ context.Context, req *feedrpc.FetchFeedReq, _ ...callopt.Option) (*feedrpc.FetchFeedResp, error) {
	return f.fetch(req), nil
}

func TestEnterpriseRPCAdapterIsDataOnlyAndVersionFenced(t *testing.T) {
	version := int64(7)
	suggestion := "internal delivery instructions"
	summary := "Data summary"
	client := rpcFeedFake{fetch: func(req *feedrpc.FetchFeedReq) *feedrpc.FetchFeedResp {
		if req.ManagedMatchingVersion == nil || *req.ManagedMatchingVersion < 1 {
			t.Fatal(req)
		}
		return &feedrpc.FetchFeedResp{ManagedMatchingVersion: &version, Items: []*feedrpc.FeedItem{{ItemId: 9007199254740993, Summary: &summary, Suggestion: &suggestion, ExpectedResponse: &suggestion}}, BaseResp: &base.BaseResp{Code: 0}}
	}}
	result, err := RPCFeedSource(client)(context.Background(), 1, 7, 20)
	if err != nil || result.ItemCount != 1 {
		t.Fatal(result, err)
	}
	text := string(result.Payload)
	if !strings.Contains(text, `"item_id":"9007199254740993"`) || strings.Contains(text, suggestion) || strings.Contains(text, "output_contract") {
		t.Fatal(text)
	}
	if _, err := RPCFeedSource(client)(context.Background(), 1, 8, 20); err == nil {
		t.Fatal("stale matching version accepted")
	}
}
