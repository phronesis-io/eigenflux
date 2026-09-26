package discovery_test

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/agentindex"
	"eigenflux_server/pkg/commissionindex"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/need"
	"eigenflux_server/rpc/feed/delivery"
	"eigenflux_server/rpc/sort/discovery"
	searchindex "eigenflux_server/rpc/sort/discovery/index"

	sortdal "eigenflux_server/rpc/sort/dal"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type integrationIDs struct{ n atomic.Int64 }

func (i *integrationIDs) NextID() (int64, error) { return i.n.Add(1), nil }

type integrationEmbedding struct{}

func (integrationEmbedding) GetEmbedding(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}

// Exercise the same operation decoding as the Feed-to-Sort RPC boundary.
type integrationExecutor struct{ service discovery.Service }

func (e integrationExecutor) Execute(ctx context.Context, owner int64, r discovery.Request, mode discovery.Mode, now int64) (discovery.Execution, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return discovery.Execution{}, err
	}
	result, err := e.service.Run(ctx, owner, discovery.Operation{Name: string(mode), Payload: string(raw)}, now)
	if err != nil {
		return discovery.Execution{}, err
	}
	return result.(discovery.Execution), nil
}

func TestPostgresESRedisThreeKinds(t *testing.T) {
	dsn, esURL, redisAddr := os.Getenv("DISCOVERY_TEST_DSN"), os.Getenv("DISCOVERY_TEST_ES"), os.Getenv("DISCOVERY_TEST_REDIS")
	if dsn == "" || esURL == "" || redisAddr == "" {
		t.Skip("isolated DISCOVERY_TEST_DSN/ES/REDIS required")
	}
	ctx := context.Background()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	r := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer r.Close()
	t.Setenv("ES_URL", esURL)
	if err = es.InitClient(); err != nil {
		t.Fatal(err)
	}
	seed := time.Now().UnixNano() / 1000
	owner, author, itemID := seed, seed+1, seed+2
	now := time.Now().UnixMilli()
	ids := &integrationIDs{}
	ids.n.Store(seed + 100)
	for _, id := range []int64{owner, author} {
		if err = db.Exec("INSERT INTO agents(agent_id,email,agent_name,created_at,updated_at,profile_completed_at) VALUES(?,?,?,?,?,?)", id, fmt.Sprintf("discovery-%d@example.invalid", id), "designer", now, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		db.Exec("DELETE FROM discovery_contexts WHERE agent_id=?", owner)
		db.Exec("DELETE FROM user_relations WHERE from_uid IN ? OR to_uid IN ?", []int64{owner, author}, []int64{owner, author})
		db.Exec("DELETE FROM processed_items WHERE item_id=?", itemID)
		db.Exec("DELETE FROM raw_items WHERE item_id=?", itemID)
		db.Exec("DELETE FROM agent_cards WHERE agent_id IN ?", []int64{owner, author})
		db.Exec("DELETE FROM agents WHERE agent_id IN ?", []int64{owner, author})
	}()
	card := `{"display_name":"designer","agent_description":"landing page design","working_languages":["en"],"offering":["design"],"runtime_name":"known","geo":"must-not-index","secret":"must-not-index"}`
	if err = db.Exec("INSERT INTO agent_cards(agent_id,public_card,private_card,schema_version,source_version,card_version,generated_at,rebuild_fence,public_card_version,public_card_generated_at) VALUES(?,?::jsonb,'{}',1,1,1,?,1,1,?)", author, card, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("INSERT INTO raw_items(item_id,author_agent_id,raw_content,created_at) VALUES(?,?,?,?)", itemID, author, "landing page design", now).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("INSERT INTO processed_items(item_id,status,summary,broadcast_type,source_type,quality_score,lang,updated_at) VALUES(?,3,'landing page design','info','original',0.8,'en',?)", itemID, now).Error; err != nil {
		t.Fatal(err)
	}
	bi, ci, ai := fmt.Sprintf("discovery-b-%d", seed), fmt.Sprintf("discovery-c-%d", seed), fmt.Sprintf("discovery-a-%d", seed)
	defer func() {
		resp, e := es.Client.Indices.Delete([]string{bi, ci, ai})
		if e == nil {
			resp.Body.Close()
		}
	}()
	mapping := map[string]any{"properties": map[string]any{"content": map[string]any{"type": "text"}, "summary": map[string]any{"type": "text"}, "embedding": map[string]any{"type": "dense_vector", "dims": 2, "index": true, "similarity": "cosine"}, "lang": map[string]any{"type": "keyword"}, "retrieval_slots": searchindex.SlotsMapping()}}
	for _, x := range []struct {
		name    string
		mapping any
	}{{bi, mapping}, {ci, commissionindex.Mapping(2)}} {
		b, _ := json.Marshal(map[string]any{"mappings": x.mapping})
		resp, e := es.Client.Indices.Create(x.name, es.Client.Indices.Create.WithBody(bytes.NewReader(b)))
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.IsError() {
			t.Fatal(resp.StatusCode)
		}
	}
	if err = agentindex.Ensure(ctx, ai, 2); err != nil {
		t.Fatal(err)
	}
	if err = agentindex.Ensure(ctx, ai, 2); err != nil {
		t.Fatal(err)
	}
	if err = agentindex.Ensure(ctx, ai, 3); err == nil {
		t.Fatal("incompatible existing Agent vector mapping accepted")
	}
	cat := commissionindex.CatalogueSnapshot{CommissionID: itemID, SellerAgentID: author, Status: "active", CatalogueVersion: 1, Title: "landing page design", Currency: "CNY", PriceFen: 0, PromisedDeliveryMS: 1000, CreatedAt: now, UpdatedAt: now}
	people, err := agentindex.Load(ctx, db, []int64{author})
	if err != nil || len(people) != 1 {
		t.Fatal(people, err)
	}
	people[0].Embedding = []float32{1, 0}
	public, _ := json.Marshal(people[0])
	if bytes.Contains(public, []byte("must-not-index")) {
		t.Fatal("private field leak")
	}
	indexed := sortdal.Item{ID: itemID, AuthorAgentID: author, Content: "landing page design", Summary: "landing page design", Type: "info", SourceType: "original", Lang: "en", QualityScore: .8, CreatedAt: time.UnixMilli(now), UpdatedAt: time.UnixMilli(now + 13), Embedding: []float32{1, 0}}
	for _, x := range []struct {
		index string
		doc   any
	}{{bi, indexed}, {ci, commissionindex.BuildDocument(cat, commissionindex.StatisticsSnapshot{}, []float32{1, 0}).SearchFields()}} {
		b, _ := json.Marshal(x.doc)
		resp, e := es.Client.Index(x.index, bytes.NewReader(b), es.Client.Index.WithDocumentID(fmt.Sprint(itemID)), es.Client.Index.WithRefresh("true"))
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.IsError() {
			t.Fatal(resp.StatusCode)
		}
	}
	if err := (agentindex.Projector{Redis: r, DB: db, Index: ai, Embedder: integrationEmbedding{}}).Project(ctx, author); err != nil {
		t.Fatal(err)
	}
	if err := commissionindex.WriteForward(ctx, r, ci, commissionindex.BuildDocument(cat, commissionindex.StatisticsSnapshot{}, []float32{1, 0})); err != nil {
		t.Fatal(err)
	}
	defer r.Del(ctx, agentindex.Forward(r, ai).Key(author, "card"), commissionindex.Forward(r, ci).Key(itemID, "catalogue"), commissionindex.Forward(r, ci).Key(itemID, "statistics"))
	responseRefresh, err := es.Client.Indices.Refresh(es.Client.Indices.Refresh.WithIndex(ai))
	if err != nil {
		t.Fatal(err)
	}
	responseRefresh.Body.Close()
	source := &discovery.Source{DB: db, Redis: r, BroadcastIndex: bi, CommissionIndex: ci, AgentIndex: ai}
	rules := discovery.Rules{}
	for _, k := range discovery.AllKinds {
		rules[k] = map[discovery.Mode]discovery.Rule{}
		for _, mode := range []discovery.Mode{discovery.Search, discovery.Recommendation} {
			rules[k][mode] = discovery.Rule{Version: "integration-only", BM25Scale: 1, CosineFloor: 0, MinRelevance: .01, Threshold: .01, HalfLifeMS: 86400000}
		}
	}
	engine := &discovery.Engine{Compiler: &discovery.Compiler{Taxonomy: &searchindex.Vocabulary{Version: "fixture", Categories: []searchindex.Node{{ID: "design", Name: "Design"}}}, Embedder: integrationEmbedding{}}, Store: discovery.Store{DB: db}, Needs: need.Store{DB: db}, IDs: ids, Sources: source, Rules: rules}
	serve := delivery.Service{Redis: r, IDs: ids, Executor: integrationExecutor{service: discovery.Service{Engine: engine}}}
	request := discovery.Request{Query: "landing page design"}
	response, err := serve.Serve(ctx, owner, request, discovery.Search, "integration")
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 3 {
		t.Fatalf("expected all three kinds, got %+v", response)
	}
	again, err := serve.Serve(ctx, owner, request, discovery.Search, "integration")
	if err != nil || again.ImpressionID != response.ImpressionID {
		t.Fatal(again, err)
	}
	zero := int64(0)
	result, err := serve.Serve(ctx, owner, discovery.Request{Query: "design", SourceKinds: []discovery.Kind{discovery.Commission}, Filters: discovery.Filters{BudgetMaxFen: &zero, Currency: "CNY"}}, discovery.Search, "")
	if err != nil || len(result.Items) != 1 {
		t.Fatal("known zero price", result, err)
	}
	if err = db.Exec("INSERT INTO user_relations(from_uid,to_uid,rel_type,created_at) VALUES(?,?,2,?)", owner, author, now).Error; err != nil {
		t.Fatal(err)
	}
	cached, err := serve.Serve(ctx, owner, request, discovery.Search, "integration")
	if err != nil || cached.ImpressionID != response.ImpressionID || len(cached.Items) != 3 {
		t.Fatal("frozen cache retry", cached, err)
	}
	fresh, err := serve.Serve(ctx, owner, request, discovery.Search, "fresh-after-block")
	if err != nil || len(fresh.Items) != 0 {
		t.Fatal("new request ignored current block", fresh, err)
	}
}

func TestESChinesePhraseBoost(t *testing.T) {
	url := os.Getenv("DISCOVERY_TEST_ES")
	if url == "" {
		t.Skip("isolated DISCOVERY_TEST_ES required")
	}
	t.Setenv("ES_URL", url)
	if err := es.InitClient(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	index := fmt.Sprintf("discovery-phrase-%d", time.Now().UnixNano())
	if err := agentindex.Ensure(ctx, index, 2); err != nil {
		t.Fatal(err)
	}
	defer func() {
		response, err := es.Client.Indices.Delete([]string{index})
		if err == nil {
			response.Body.Close()
		}
	}()
	for i, text := range []string{"人工智能", "人工 系统 智能", "ＡＩ", "AI"} {
		doc := agentindex.Document{AgentID: int64(i + 1), Version: 1, ProjectionVersion: 1, Active: true, SearchText: text}
		raw, _ := json.Marshal(doc.SearchFields())
		response, err := es.Client.Index(index, bytes.NewReader(raw), es.Client.Index.WithDocumentID(fmt.Sprint(i+1)), es.Client.Index.WithRefresh("true"))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.IsError() {
			t.Fatal(response.StatusCode)
		}
	}
	compiler := discovery.Compiler{Taxonomy: &searchindex.Vocabulary{Version: "phrase-fixture", Categories: []searchindex.Node{{ID: "tech", Name: "Technology"}}}, Embedder: integrationEmbedding{}}
	compiled, err := compiler.Query(ctx, 99, 100, time.Now().UnixMilli(), discovery.Request{Query: "人工智能", SourceKinds: []discovery.Kind{discovery.Agent}}, "query")
	if err != nil {
		t.Fatal(err)
	}
	source := &discovery.Source{AgentIndex: index}
	boosted, err := source.Recall(ctx, compiled, discovery.Agent, "lexical", 10)
	if err != nil {
		t.Fatal(err)
	}
	compiled.QueryAnalysis = nil
	plain, err := source.Recall(ctx, compiled, discovery.Agent, "lexical", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(boosted) != 2 || len(plain) != 2 {
		t.Fatalf("unexpected recall sizes: boosted=%d plain=%d", len(boosted), len(plain))
	}
	scores := map[int64]float64{}
	for _, d := range plain {
		scores[d.Ref.ID] = d.Lexical
	}
	for _, d := range boosted {
		if d.Ref.ID == 1 && d.Lexical <= scores[1] {
			t.Fatal("continuous Chinese phrase did not receive a boost")
		}
		if d.Ref.ID == 2 && d.Lexical != scores[2] {
			t.Fatal("separated Chinese terms incorrectly received a phrase boost")
		}
	}
	compiled, err = compiler.Query(ctx, 99, 101, time.Now().UnixMilli(), discovery.Request{Query: "ＡＩ", SourceKinds: []discovery.Kind{discovery.Agent}}, "query")
	if err != nil {
		t.Fatal(err)
	}
	variants, err := source.Recall(ctx, compiled, discovery.Agent, "lexical", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(variants) != 2 {
		t.Fatalf("normalization must retain full-width documents and add ASCII matches: %+v", variants)
	}
	for _, d := range variants {
		if d.Ref.ID != 3 && d.Ref.ID != 4 {
			t.Fatalf("unexpected normalization match: %+v", d.Ref)
		}
	}

}
