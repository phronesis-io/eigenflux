package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresExecutionSnapshots(t *testing.T) {
	dsn := os.Getenv("DISCOVERY_TEST_DSN")
	if dsn == "" {
		t.Skip("set DISCOVERY_TEST_DSN to isolated PostgreSQL")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	schema := fmt.Sprintf("discovery_test_%d", time.Now().UnixNano())
	if err = db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP SCHEMA " + schema + " CASCADE")
	if err = db.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"CREATE TABLE agents(agent_id BIGINT PRIMARY KEY)", "INSERT INTO agents VALUES(1),(2)", "CREATE TABLE processed_items(item_id BIGINT PRIMARY KEY)"} {
		if err = db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile("../../../migrations/000107_discovery_contexts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(strings.Split(string(raw), "-- +goose Down")[0]).Error; err != nil {
		t.Fatal(err)
	}
	s := Store{DB: db}
	ctx := context.Background()
	c := Context{ID: 10, OwnerID: 1, State: "active", Persistence: "ephemeral", Origin: "need_input", Revision: 1, Kinds: []Kind{Agent}, SpecHash: "a", CreatedAt: 100, UpdatedAt: 100, ExpiresAt: 200, CapturedNeed: ptrSnapshot(capturedFixture(99, Agent)), SourceNeedID: 99, SourceNeedRevision: 2}
	if _, err = s.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	var stored row
	if err = db.First(&stored, "context_id=10").Error; err != nil {
		t.Fatal(err)
	}
	var got Context
	if err = json.Unmarshal([]byte(stored.Compiled), &got); err != nil {
		t.Fatal(err)
	}
	if got.NeedID() != 99 || got.CapturedNeed.InputID != 99 || got.SourceNeedRevision != 2 {
		t.Fatalf("lost provenance: %+v", got)
	}
	c.ID = 11
	c.Persistence = "saved"
	if _, err = s.Create(ctx, c); err == nil {
		t.Fatal("duplicate Need write accepted")
	}
	if err = s.Prune(ctx, 201); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err = db.Model(&row{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
func ptrSnapshot[T any](v T) *T { return &v }
