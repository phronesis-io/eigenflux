package discovery

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresHistoricalContextRetention(t *testing.T) {
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
	if err := db.Exec(`INSERT INTO discovery_contexts (context_id,agent_id,persistence,input_origin,state,revision,compiled,embedding,expires_at,created_at,updated_at) VALUES (10,1,'ephemeral','query','active',1,'{}','null',200,100,100)`).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.Prune(ctx, 201); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err = db.Table("discovery_contexts").Count(&count).Error; err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
func ptrSnapshot[T any](v T) *T { return &v }
