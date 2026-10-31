package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/api/integrations"
	"eigenflux_server/kitex_gen/eigenflux/base"
	feedapi "eigenflux_server/kitex_gen/eigenflux/feed"
	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	itemapi "eigenflux_server/kitex_gen/eigenflux/item"
	"eigenflux_server/kitex_gen/eigenflux/item/itemservice"
	sortapi "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/kitex_gen/eigenflux/sort/sortservice"
	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/integrationfeed"
	"eigenflux_server/pkg/integrationidentity"
	"eigenflux_server/pkg/integrationprofile"
	"eigenflux_server/pkg/mq"
	itemdal "eigenflux_server/rpc/item/dal"
	sortdal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/legacy"
	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/network/standard"
	"github.com/cloudwego/kitex/client/callopt"
	elasticsearch "github.com/elastic/go-elasticsearch/v8"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type acceptanceIDs struct{ next atomic.Int64 }

func (i *acceptanceIDs) NextID() (int64, error) { return i.next.Add(1), nil }

type acceptanceModels struct{}

func (acceptanceModels) ExtractKeywords(_ context.Context, text string) ([]string, string, error) {
	if strings.Contains(text, "python") {
		return []string{"python"}, "", nil
	}
	return []string{"golang"}, "", nil
}
func (acceptanceModels) GetEmbedding(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}
func (acceptanceModels) Model() string { return "acceptance-fixed-model" }

type acceptanceSort struct {
	sortservice.Client
	service *legacy.Service
}

func (s acceptanceSort) SortItems(ctx context.Context, req *sortapi.SortItemsReq, _ ...callopt.Option) (*sortapi.SortItemsResp, error) {
	return s.service.SortItems(ctx, req)
}

type acceptanceFeed struct {
	feedservice.Client
	service *FeedServiceImpl
}

func (s acceptanceFeed) FetchFeed(ctx context.Context, req *feedapi.FetchFeedReq, _ ...callopt.Option) (*feedapi.FetchFeedResp, error) {
	return s.service.FetchFeed(ctx, req)
}

type acceptanceItems struct {
	itemservice.Client
	database *gorm.DB
}

func (s acceptanceItems) BatchGetItems(_ context.Context, req *itemapi.BatchGetItemsReq, _ ...callopt.Option) (*itemapi.BatchGetItemsResp, error) {
	rows, err := itemdal.BatchGetProcessedItems(s.database, req.ItemIds)
	if err != nil {
		return nil, err
	}
	out := &itemapi.BatchGetItemsResp{BaseResp: &base.BaseResp{}}
	for _, row := range rows {
		out.Items = append(out.Items, &itemapi.ProcessedItem{ItemId: row.ItemID, Status: int32(row.Status), Summary: &row.Summary, BroadcastType: row.BroadcastType, Domains: strings.Split(row.Domains, ","), Keywords: strings.Split(row.Keywords, ","), GroupId: &row.GroupID, UpdatedAt: row.UpdatedAt})
	}
	return out, nil
}

func acceptanceDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("INTEGRATION_TEST_PG_DSN")
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("migrated isolated loopback PostgreSQL required")
	}
	open := func(dsn string) *gorm.DB {
		database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		return database
	}
	admin := open(dsn)
	conn, _ := admin.DB()
	t.Cleanup(func() { _ = conn.Close() })
	schema := "sdk_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error })
	query := u.Query()
	query.Set("search_path", schema+",public")
	u.RawQuery = query.Encode()
	database := open(u.String())
	connection, _ := database.DB()
	connection.SetMaxOpenConns(12)
	t.Cleanup(func() { _ = connection.Close() })
	for _, table := range []string{"agents", "agent_profiles", "agent_profile_change_events", "agent_bio_history", "agent_cards", "raw_items", "processed_items"} {
		if err := database.Exec("CREATE TABLE " + table + " (LIKE public." + table + " INCLUDING ALL)").Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"000125_enterprise_integrations.sql", "000126_integration_contexts.sql", "000127_integration_feed_batches.sql", "000128_integration_request_observation.sql"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(raw), "-- +goose Down")
		if err := database.Exec(up).Error; err != nil {
			t.Fatal(err)
		}
	}
	return database
}

// Run this opt-in test alone: it owns the Feed process's global DB/RPC adapters.
// The actual HTTP gateway, SQL processing, ranking and Feed implementation are
// exercised; external model/ES services and the Item RPC wire are local adapters.
func TestEnterpriseSDKAcceptance(t *testing.T) {
	python := os.Getenv("EIGENFLUX_SDK_PYTHON")
	binary := os.Getenv("EIGENFLUX_SDK_RUNTIME")
	if python == "" || binary == "" || os.Getenv("INTEGRATION_TEST_PG_DSN") == "" {
		t.Skip("explicit private-wheel Python, compiled SDK binary and migrated loopback DB required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database := acceptanceDatabase(t)
	r := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: r.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	db.DB = database
	db.RDB = redisClient
	mq.RDB = redisClient
	identity := &integrationidentity.Store{DB: database, IDs: &acceptanceIDs{}}
	secret, err := identity.CreateApp(ctx, integrationidentity.App{AppID: "reference", Name: "Reference B", MaxAgents: 1, MaxInstallationsPerAgent: 1, MinPollIntervalSeconds: 3600}, "acceptance")
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"", "reference"} {
		if err := identity.SetEnabled(ctx, app, 0, true, "acceptance", "isolated fixture"); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixMilli()
	if err := database.Exec(`INSERT INTO agents(agent_id,short_id,email,email_kind,agent_name,bio,created_at,updated_at) VALUES(5000,'ZZZZZ','publisher@example.test','legacy_real','Test Publisher','',?,?)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	for index, topic := range []string{"python", "golang"} {
		id := int64(9001 + index)
		if err := database.Create(&itemdal.RawItem{ItemID: id, AuthorAgentID: 5000, RawContent: topic + " update", CreatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Create(&itemdal.ProcessedItem{ItemID: id, Status: itemdal.StatusCompleted, Summary: topic + " personalized result", BroadcastType: "info", Domains: topic, Keywords: topic, GroupID: id, QualityScore: 1, SourceType: "original", UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var queryPython, queryGo atomic.Int64
	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		topic := "golang"
		id := int64(9002)
		if strings.Contains(string(raw), "python") {
			topic = "python"
			id = 9001
			queryPython.Add(1)
		} else if strings.Contains(string(raw), "golang") {
			queryGo.Add(1)
		} else {
			t.Error("ranking query did not contain current managed keywords")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		item := sortdal.Item{ID: id, AuthorAgentID: 5000, Content: topic + " update", Summary: topic + " personalized result", Type: "info", Domains: []string{topic}, Keywords: []string{topic}, GroupID: id, QualityScore: 1, SourceType: "original", CreatedAt: time.UnixMilli(now), UpdatedAt: time.UnixMilli(now), Embedding: []float32{1, 0}}
		_ = json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"total": map[string]any{"value": 1}, "hits": []any{map[string]any{"_id": strconv.FormatInt(id, 10), "_score": 1, "_source": item}}}})
	}))
	defer searchServer.Close()
	es.Client, err = elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{searchServer.URL}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Load()
	cfg.EnableSDKIntegrations = true
	cfg.EnableNeedSearch = false
	cfg.EnableSearchCache = false
	cfg.EnableKNNRecall = false
	cfg.EnableHotRecall = false
	cfg.EnableNewRecall = false
	cfg.EnableNewUGCRecall = false
	cfg.EnableSwingI2IRecall = false
	cfg.FriendFeedEnabled = false
	cfg.EnableReplayLog = false
	cfg.LRRankerEnabled = false
	cfg.MinRelevanceScore = 0
	sorter := legacy.New(cfg, database, redisClient)
	defer sorter.Close()
	sortClient = acceptanceSort{service: sorter}
	itemClient = acceptanceItems{database: database}
	feedService := NewFeedServiceImpl(cfg, &acceptanceIDs{})
	upstream := integrations.RPCFeedSource(acceptanceFeed{service: feedService})
	var sourceCalls atomic.Int64
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := server.New(server.WithListener(listener), server.WithTransport(standard.NewTransporter))
	(&integrations.Service{Store: identity, Feed: func(ctx context.Context, id, version int64, limit int) (integrationfeed.Generated, error) {
		sourceCalls.Add(1)
		return upstream(ctx, id, version, limit)
	}}).Register(h)
	go func() { _ = h.Run() }()
	defer h.Close()
	origin := "http://" + listener.Addr().String()
	httpClient := &http.Client{Timeout: 5 * time.Second}
	defer httpClient.CloseIdleConnections()
	for deadline := time.Now().Add(5 * time.Second); ; {
		response, err := httpClient.Get(origin + "/api/v2/integrations/agents/me/feed")
		if err == nil {
			response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("HTTP gateway did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	processor := &integrationprofile.Processor{Store: &integrationprofile.Store{DB: database}, Extractor: acceptanceModels{}, Embedder: acceptanceModels{}, Rebuild: func(ctx context.Context, id int64) {
		if err := agentcard.Rebuild(ctx, database, redisClient, id); err != nil && ctx.Err() == nil {
			t.Error(err)
		}
	}}
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); processor.Run(ctx) }()
	defer func() { cancel(); <-workerDone }()
	work := t.TempDir()
	stateDir := filepath.Join(work, "runtime")
	inbox := filepath.Join(work, "agent.sqlite")
	contextFile := filepath.Join(work, "context.json")
	if err := os.WriteFile(contextFile, []byte(`{"summary":"private-python-goal"}`), 0600); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "examples", "enterprise_sdk", "reference_agent.py"))
	if err != nil {
		t.Fatal(err)
	}
	command := func(version, statusFile string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, python, script, "--state-dir", stateDir, "--inbox", inbox, "--context-file", contextFile, "--agent-id", "reference-agent-001", "--runtime-version", version, "--once", "--timeout", "80", "--status-file", statusFile)
		cmd.Env = append(os.Environ(), "EIGENFLUX_BASE_URL="+origin, "EIGENFLUX_APP_CREDENTIAL="+secret, "EIGENFLUX_SDK_RUNTIME="+binary)
		return cmd
	}
	var first struct {
		AcceptedBatches int `json:"accepted_batches"`
		Notes           []struct {
			ItemID string `json:"item_id"`
		} `json:"notes"`
	}
	output, err := command("1.0.0", filepath.Join(work, "round1.status.json")).CombinedOutput()
	if err != nil {
		status, _ := os.ReadFile(filepath.Join(work, "round1.status.json"))
		t.Fatalf("reference Agent failed: %v\n%s\nstatus=%s", err, output, status)
	}
	if err := json.Unmarshal(output, &first); err != nil || first.AcceptedBatches != 1 || len(first.Notes) != 1 || first.Notes[0].ItemID != "9001" {
		t.Fatal(string(output), err)
	}
	view, err := identity.Inspect(ctx, "reference", 0, 0)
	if err != nil || len(view.Agents) != 1 || len(view.Installations) != 1 {
		t.Fatal(view, err)
	}
	id := view.Agents[0].AgentID
	installation := view.Installations[0]
	if view.Agents[0].AppliedMatchingVersion != 1 || view.Agents[0].EffectivePollIntervalSeconds != 3600 || view.Agents[0].RequestCount < 1 || installation.SDKVersion == "" {
		t.Fatal(view)
	}
	var public string
	if err := database.Table("agent_cards").Select("public_card").Where("agent_id = ?", id).Scan(&public).Error; err != nil || strings.Contains(public, "private-python-goal") {
		t.Fatal("private input exposed", public, err)
	}
	for _, table := range []string{"agent_principals", "agent_onboarding_v2"} {
		var count int64
		if err := database.Table(table).Where("agent_id = ?", id).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("managed registration created personal state", table, count, err)
		}
	}
	request := func(method, path, payload string) int {
		req, err := http.NewRequestWithContext(ctx, method, origin+path, strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("X-EigenFlux-Installation-ID", installation.InstallationID)
		req.Header.Set("X-EigenFlux-Runtime-Instance-ID", installation.RuntimeInstanceID)
		response, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if status := request("POST", "/api/v2/integrations/agents/me/feed", `{"sequence":1}`); status != 200 || sourceCalls.Load() != 1 {
		t.Fatal("retry regenerated Feed", status, sourceCalls.Load())
	}
	if status := request("POST", "/api/v2/integrations/agents/me/feed", `{"sequence":2}`); status != 429 || sourceCalls.Load() != 1 {
		t.Fatal("hourly gate bypassed", status, sourceCalls.Load())
	}
	if err := identity.SetEnabled(ctx, "reference", 0, false, "acceptance", "disable during acceptance"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v2/integrations/agents/me/feed", "/api/v2/integrations/agents/me/card", "/api/v2/integrations/agents/me/context"} {
		if status := request("GET", path, ""); status != 403 {
			t.Fatal("disabled data read", path, status)
		}
	}
	// Advance only isolated fixture timestamps instead of waiting a real hour.
	// The same adjustment is applied to server reservation and SDK hint state.
	if err := database.Exec(`UPDATE integration_agents SET next_poll_at=0 WHERE agent_id=?`, id).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`UPDATE integration_feed_batches SET created_at=created_at-3600001,next_poll_at=0 WHERE agent_id=?`, id).Error; err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(stateDir, "state.json")
	raw, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state["next_poll_at"] = json.RawMessage("0")
	raw, _ = json.Marshal(state)
	if err := os.WriteFile(stateFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contextFile, []byte(`{"summary":"private-golang-goal"}`), 0600); err != nil {
		t.Fatal(err)
	}
	statusFile := filepath.Join(work, "round2.status.json")
	second := command("2.0.1", statusFile)
	var buffer bytes.Buffer
	second.Stdout = &buffer
	second.Stderr = &buffer
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- second.Wait() }()
	for deadline := time.Now().Add(10 * time.Second); ; {
		raw, _ := os.ReadFile(statusFile)
		if strings.Contains(string(raw), "waiting_for_registration") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disabled SDK did not pause registration")
		}
		select {
		case err := <-done:
			t.Fatalf("SDK exited while disabled: %v %s", err, buffer.String())
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	if sourceCalls.Load() != 1 {
		t.Fatal("disabled SDK generated another batch")
	}
	if err := identity.SetEnabled(ctx, "reference", 0, true, "acceptance", "resume during acceptance"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("resumed reference Agent failed: %v\n%s", err, buffer.String())
		}
	case <-ctx.Done():
		t.Fatal("SDK did not resume")
	}
	var final struct {
		AcceptedBatches int `json:"accepted_batches"`
		Notes           []struct {
			ItemID string `json:"item_id"`
		} `json:"notes"`
	}
	if err := json.Unmarshal(buffer.Bytes(), &final); err != nil || final.AcceptedBatches != 2 || len(final.Notes) != 2 {
		t.Fatal(buffer.String(), err)
	}
	ids := map[string]bool{}
	for _, note := range final.Notes {
		ids[note.ItemID] = true
	}
	if !ids["9001"] || !ids["9002"] || queryPython.Load() == 0 || queryGo.Load() == 0 || sourceCalls.Load() != 2 {
		t.Fatal("private context did not change actual ranked Feed", final, queryPython.Load(), queryGo.Load(), sourceCalls.Load())
	}
	if status := request("GET", "/api/v2/integrations/agents/me/feed", ""); status != 403 {
		t.Fatal("old runtime binding retained access", status)
	}
	view, err = identity.Inspect(ctx, "reference", id, 0)
	if err != nil || view.Agents[0].RuntimeVersion != "2.0.1" || view.Agents[0].AppliedMatchingVersion != 2 || view.Applications[0].FeedBatchesTotal != 2 || view.Agents[0].FeedBatchesTotal != 2 {
		t.Fatal(view, err)
	}
	t.Log(fmt.Sprintf("acceptance passed: registered anonymous Agent, applied context revisions 1/2, consumed distinct Feed batches, denied hourly/disabled/stale-instance reads, and resumed SDK after re-enable; source generations=%d", sourceCalls.Load()))
}
