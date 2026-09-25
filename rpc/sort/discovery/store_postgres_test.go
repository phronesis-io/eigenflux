package discovery

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresContextOwnershipAndCAS(t *testing.T) {
	dsn := os.Getenv("DISCOVERY_TEST_DSN")
	if dsn == "" {
		t.Skip("set DISCOVERY_TEST_DSN to isolated PostgreSQL")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
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
	raw, err := os.ReadFile("../../../migrations/000106_discovery_contexts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(strings.Split(string(raw), "-- +goose Down")[0]).Error; err != nil {
		t.Fatal(err)
	}
	s := Store{DB: db}
	ctx := context.Background()
	c := Context{ID: 10, OwnerID: 1, State: "active", Persistence: "saved", Origin: "saved_need", Revision: 1, Kinds: []Kind{Agent}, SpecHash: "a", CreatedAt: 100, UpdatedAt: 100}
	if _, err = s.Create(ctx, c, "same"); err != nil {
		t.Fatal(err)
	}
	retry := c
	retry.ID = 11
	if got, err := s.Create(ctx, retry, "same"); err != nil || got.ID != 10 {
		t.Fatal(got, err)
	}
	retry.SpecHash = "b"
	if _, err = s.Create(ctx, retry, "same"); err == nil {
		t.Fatal("idempotency conflict missing")
	}
	if _, err = s.Get(ctx, 2, 10, true); err == nil {
		t.Fatal("owner leak")
	}
	c.UpdatedAt = 200
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Update(ctx, c, 1); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("CAS", successes.Load())
	}
	for i := int64(20); i < 29; i++ {
		n := c
		n.ID = i
		n.SpecHash = fmt.Sprint(i)
		if _, err = s.Create(ctx, n, ""); err != nil {
			t.Fatal(err)
		}
	}
	c.ID = 30
	if _, err = s.Create(ctx, c, ""); err == nil {
		t.Fatal("active limit")
	}
	if _, err = s.SetState(ctx, 1, 10, 2, "paused", 300); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(ctx, c, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetState(ctx, 1, 10, 3, "active", 400); err == nil {
		t.Fatal("resume exceeded active limit")
	}
	rows, err := s.List(ctx, 2, "expired", 0, 20, 500)
	if err != nil || len(rows) != 0 {
		t.Fatal("expired owner scope", rows, err)
	}
}
