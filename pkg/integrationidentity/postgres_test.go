package integrationidentity

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPostgresRegistrationQuotaAndMigration(t *testing.T) {
	dsn := os.Getenv("INTEGRATION_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("INTEGRATION_TEST_PG_DSN required (isolated loopback database)")
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
	defer adminConn.Close()
	schema := "integration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + schema + " CASCADE")
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db := open(u.String())
	conn, _ := db.DB()
	conn.SetMaxOpenConns(8)
	defer conn.Close()
	for _, statement := range []string{
		`CREATE TABLE agents(agent_id BIGINT PRIMARY KEY,short_id TEXT,email TEXT UNIQUE,email_kind TEXT,agent_name TEXT,bio TEXT,created_at BIGINT,updated_at BIGINT)`,
		`CREATE UNIQUE INDEX agent_short ON agents(short_id) WHERE short_id IS NOT NULL`,
		`CREATE TABLE agent_profiles(agent_id BIGINT PRIMARY KEY,status SMALLINT,updated_at BIGINT)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	sql, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000125_enterprise_integrations.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(sql), "-- +goose Down")
	if !ok {
		t.Fatal("missing migration rollback")
	}
	if err := db.Exec(up).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"000126_integration_contexts.sql", "000127_integration_feed_batches.sql", "000128_integration_request_observation.sql"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(raw), "-- +goose Down")
		if err := db.Exec(up).Error; err != nil {
			t.Fatal(err)
		}
	}
	var enabled bool
	if err := db.Table("integration_control").Select("enabled").Where("id = 1").Scan(&enabled).Error; err != nil || enabled {
		t.Fatal("migration must not open enterprise access", enabled, err)
	}
	secret, hash, err := NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	app := App{AppID: "partner", Name: "Partner", Enabled: true, CredentialHash: hash, MaxAgents: 1, MaxInstallationsPerAgent: 1, MinPollIntervalSeconds: 3600, Revision: 1}
	if err := db.Create(&app).Error; err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: db, IDs: &testIDs{}}
	if _, err := s.Authenticate(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled(context.Background(), "", 0, true, "test", "concurrency fixture"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 16)
	successes := make(chan RegistrationResult, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := registration()
			req.ExternalAgentID = fmt.Sprintf("agent-%d", i)
			result, err := s.Register(context.Background(), "partner", req)
			if err != nil {
				errors <- err
			} else {
				successes <- result
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	close(successes)
	if len(successes) != 1 {
		t.Fatalf("concurrent registrations exceeded single approved slot: %d", len(successes))
	}
	for err := range errors {
		requireCode(t, err, 403, "AGENT_LIMIT_REACHED")
	}
	var count int64
	if err := db.Table("agents").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("identity leaked outside quota transaction", count, err)
	}
	winner := <-successes
	req := registration()
	req.ExternalAgentID = winner.Agent.ExternalAgentID
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Register(context.Background(), "partner", req)
			if err != nil {
				t.Error(err)
				return
			}
			if r.Agent.AgentID != winner.Agent.AgentID || r.Installation.InstallationID != winner.Installation.InstallationID {
				t.Error("idempotent registration changed identity")
			}
		}()
	}
	wg.Wait()
	if err := s.SetEnabled(context.Background(), "partner", winner.Agent.AgentID, false, "test", "stop"); err != nil {
		t.Fatal(err)
	}
	_, err = s.RequireBusiness(context.Background(), "partner", winner.Installation.InstallationID, "run-1")
	requireCode(t, err, 403, "AGENT_DISABLED")
	if err := db.Exec(down).Error; err == nil {
		t.Fatal("rollback discarded live managed identity bindings")
	}

	policy := AppPolicy{MaxAgents: 3, MaxInstallationsPerAgent: 2, MinPollIntervalSeconds: 7200, MaxFeedBatchesPerDay: 12}
	if err := s.SetAppPolicy(context.Background(), "partner", policy, "operator", "contract check"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.RecordRequest(context.Background(), winner.Agent.AgentID, ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := s.RecordRequest(context.Background(), winner.Agent.AgentID, "AGENT_DISABLED"); err != nil {
		t.Fatal(err)
	}
	view, err := s.Inspect(context.Background(), "partner", winner.Agent.AgentID, 0)
	if err != nil || len(view.Agents) != 1 || len(view.Applications) != 1 || len(view.Installations) != 1 {
		t.Fatal(view, err)
	}
	if view.Agents[0].RequestCount != 13 || view.Agents[0].LastFailureCode != "AGENT_DISABLED" || view.Agents[0].EffectivePollIntervalSeconds != 7200 || view.Applications[0].AppPolicy != policy {
		t.Fatal(view)
	}
	if view.Agents[0].Enabled {
		t.Fatal("policy update re-enabled disabled Agent")
	}
}
