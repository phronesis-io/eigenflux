package integrationprofile

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/integrationidentity"
	profiledal "eigenflux_server/rpc/profile/dal"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type profileIDs struct{ next atomic.Int64 }

func (g *profileIDs) NextID() (int64, error) { return g.next.Add(1), nil }

func postgresContextStore(t *testing.T) (*Store, int64) {
	t.Helper()
	dsn := os.Getenv("INTEGRATION_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("INTEGRATION_TEST_PG_DSN required (migrated isolated loopback PostgreSQL)")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("explicit loopback PostgreSQL DSN required")
	}
	open := func(dsn string) *gorm.DB {
		t.Helper()
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	admin := open(dsn)
	adminConn, _ := admin.DB()
	t.Cleanup(func() { _ = adminConn.Close() })
	schema := "context_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error })
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	db := open(u.String())
	conn, _ := db.DB()
	conn.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = conn.Close() })
	// Clone fact tables from the fully migrated test database. Other Card
	// dependencies are read-only; all writes stay within this random schema.
	for _, table := range []string{"agents", "agent_profiles", "agent_profile_change_events", "agent_bio_history", "agent_cards"} {
		if err := db.Exec("CREATE TABLE " + table + " (LIKE public." + table + " INCLUDING ALL)").Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"000125_enterprise_integrations.sql", "000126_integration_contexts.sql"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		up, _, ok := strings.Cut(string(raw), "-- +goose Down")
		if !ok {
			t.Fatal("missing down migration")
		}
		if err := db.Exec(up).Error; err != nil {
			t.Fatal(err)
		}
	}
	identity := &integrationidentity.Store{DB: db, IDs: &profileIDs{}}
	app, err := identity.CreateApp(context.Background(), integrationidentity.App{AppID: "partner", Name: "Partner", MaxAgents: 2, MaxInstallationsPerAgent: 1, MinPollIntervalSeconds: 3600}, "test")
	_ = app
	if err != nil {
		t.Fatal(err)
	}
	for _, appID := range []string{"", "partner"} {
		if err := identity.SetEnabled(context.Background(), appID, 0, true, "test", "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	registered, err := identity.Register(context.Background(), "partner", integrationidentity.Registration{ExternalAgentID: "agent", RuntimeName: "jarvis", RuntimeVersion: "2", SDKVersion: "1", RuntimeInstanceID: "run"})
	if err != nil {
		t.Fatal(err)
	}
	return &Store{DB: db}, registered.Agent.AgentID
}

type testModels struct {
	extract     func(string)
	embed       func(string)
	err         error
	extractions int
	embeddings  int
}

func (m *testModels) ExtractKeywords(_ context.Context, text string) ([]string, string, error) {
	m.extractions++
	if m.extract != nil {
		m.extract(text)
	}
	return []string{"private-research"}, "", m.err
}
func (m *testModels) GetEmbedding(_ context.Context, text string) ([]float32, error) {
	m.embeddings++
	if m.embed != nil {
		m.embed(text)
	}
	return []float32{0.5, 0.25}, m.err
}
func (*testModels) Model() string { return "test-embedding" }

func TestPostgresContextProcessingPrivacyAndMatching(t *testing.T) {
	s, id := postgresContextStore(t)
	ctx := context.Background()
	models := &testModels{}
	p := &Processor{Store: s, Extractor: models, Embedder: models}
	if _, err := s.Accept(ctx, id, summaryInput(1, "secret-client-roadmap")); err != nil {
		t.Fatal(err)
	}
	if err := p.Process(ctx, id); err != nil {
		t.Fatal(err)
	}
	state, err := s.Get(ctx, id)
	if err != nil || state.Status != "ready" || state.AppliedMatchingVersion != 1 {
		t.Fatal(state, err)
	}
	matching, err := LoadMatching(ctx, s.DB, id)
	if err != nil || matching == nil || !strings.Contains(matching.Keywords, "private-research") || len(matching.Embedding) != 8 {
		t.Fatal(matching, err)
	}
	agent, err := profiledal.GetAgentByID(s.DB, id)
	if err != nil || agent.Bio != "" {
		t.Fatal("summary leaked into bio", agent, err)
	}
	redisServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	defer rdb.Close()
	if err := agentcard.Rebuild(ctx, s.DB, rdb, id); err != nil {
		t.Fatal(err)
	}
	card, err := profiledal.GetAgentCard(s.DB, id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(card.PublicCard, "private-research") || strings.Contains(card.PublicCard, "secret-client-roadmap") {
		t.Fatal("private context in public Card")
	}
	if !strings.Contains(card.PrivateCard, "private-research") {
		t.Fatal("private focus missing")
	}
	if _, err := s.Accept(ctx, id, summaryInput(1, "secret-client-roadmap")); err != nil {
		t.Fatal(err)
	}
	if err := p.Process(ctx, id); err != nil || models.extractions != 1 || models.embeddings != 1 {
		t.Fatal("duplicate processing", err, models)
	}
	fields := map[string]json.RawMessage{"agent_description": json.RawMessage(`"Public introduction"`), "demands": json.RawMessage(`["private demand"]`)}
	if _, err := s.Accept(ctx, id, Input{SourceRevision: 2, Fields: fields}); err != nil {
		t.Fatal(err)
	}
	if err := p.Process(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := agentcard.Rebuild(ctx, s.DB, rdb, id); err != nil {
		t.Fatal(err)
	}
	card, err = profiledal.GetAgentCard(s.DB, id)
	if err != nil || !strings.Contains(card.PublicCard, "Public introduction") || strings.Contains(card.PublicCard, "private demand") {
		t.Fatal(card, err)
	}
}

func TestPostgresContextConcurrentClaimsAndStaleResults(t *testing.T) {
	s, id := postgresContextStore(t)
	ctx := context.Background()
	if _, err := s.Accept(ctx, id, summaryInput(1, "first")); err != nil {
		t.Fatal(err)
	}
	var claims atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := s.Claim(ctx, id)
			if err != nil {
				t.Error(err)
			}
			if job != nil {
				claims.Add(1)
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("claims=%d", claims.Load())
	}
	if err := s.DB.Model(&State{}).Where("agent_id = ?", id).Update("lease_until", 0).Error; err != nil {
		t.Fatal(err)
	}
	models := &testModels{extract: func(string) {
		if _, err := s.Accept(ctx, id, summaryInput(2, "newest")); err != nil {
			t.Fatal(err)
		}
	}}
	p := &Processor{Store: s, Extractor: models, Embedder: models}
	if err := p.Process(ctx, id); err != nil {
		t.Fatal(err)
	}
	matching, err := LoadMatching(ctx, s.DB, id)
	if err != nil || matching != nil {
		t.Fatal("stale result applied", matching, err)
	}
	models.extract = nil
	if err := p.Process(ctx, id); err != nil {
		t.Fatal(err)
	}
	matching, err = LoadMatching(ctx, s.DB, id)
	if err != nil || matching == nil || matching.SourceRevision != 2 {
		t.Fatal(matching, err)
	}
	if err := s.DB.Exec(`DROP TABLE integration_control`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(ctx, id, summaryInput(3, "storage failure")); err == nil {
		t.Fatal("authorization storage failure accepted")
	}
}

func TestModelFailureRemainsRetryableWithoutPrivateErrors(t *testing.T) {
	s, id := postgresContextStore(t)
	ctx := context.Background()
	if _, err := s.Accept(ctx, id, summaryInput(1, "private input")); err != nil {
		t.Fatal(err)
	}
	models := &testModels{err: errors.New("provider echoed secret input")}
	p := &Processor{Store: s, Extractor: models, Embedder: models}
	if err := p.Process(ctx, id); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	state, err := s.Get(ctx, id)
	if err != nil || state.Status != "pending" || state.FailureCode != "PROCESSING_FAILED" {
		t.Fatal(state, err)
	}
}

func TestPostgresConcurrentProfileEditIsPreserved(t *testing.T) {
	s, id := postgresContextStore(t)
	ctx := context.Background()
	if _, err := s.Accept(ctx, id, summaryInput(1, "private context")); err != nil {
		t.Fatal(err)
	}
	models := &testModels{embed: func(string) {
		if err := s.DB.Exec(`UPDATE agent_profiles SET profile_version=profile_version+1,profile_data='{"current_focus":["newer-human-edit"]}' WHERE agent_id=?`, id).Error; err != nil {
			t.Fatal(err)
		}
	}}
	p := &Processor{Store: s, Extractor: models, Embedder: models}
	if err := p.Process(ctx, id); err == nil {
		t.Fatal("concurrent edit was overwritten")
	}
	_, data, err := profiledal.GetProfileVersionAndData(s.DB, id)
	if err != nil || !strings.Contains(string(data["current_focus"]), "newer-human-edit") {
		t.Fatal(data, err)
	}
	matching, err := LoadMatching(ctx, s.DB, id)
	if matching != nil || err != nil {
		t.Fatal(matching, err)
	}
}

func TestPostgresDisableDuringExtractionStopsEmbedding(t *testing.T) {
	s, id := postgresContextStore(t)
	ctx := context.Background()
	if _, err := s.Accept(ctx, id, summaryInput(1, "context")); err != nil {
		t.Fatal(err)
	}
	models := &testModels{extract: func(string) {
		if err := s.DB.Exec(`UPDATE integration_control SET enabled=false`).Error; err != nil {
			t.Fatal(err)
		}
	}}
	p := &Processor{Store: s, Extractor: models, Embedder: models}
	if err := p.Process(ctx, id); err == nil || models.embeddings != 0 {
		t.Fatal(err, models.embeddings)
	}
	matching, err := LoadMatching(ctx, s.DB, id)
	if matching != nil || err != nil {
		t.Fatal(matching, err)
	}
}
