package consumer

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestDiscoveryReplayPostgres(t *testing.T) {
	dsn := os.Getenv("DISCOVERY_TEST_DSN")
	if dsn == "" {
		t.Skip("DISCOVERY_TEST_DSN required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	schema := fmt.Sprintf("discovery_replay_test_%d", time.Now().UnixNano())
	if err = db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP SCHEMA " + schema + " CASCADE")
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL search_path TO " + schema).Error; err != nil {
			return err
		}
		for _, f := range []string{"../../migrations/000010_replay_logs.sql", "../../migrations/000106_discovery_samples.sql"} {
			raw, e := os.ReadFile(f)
			if e != nil {
				return e
			}
			up := strings.Split(string(raw), "-- +goose Down")[0]
			if e = tx.Exec(up).Error; e != nil {
				return e
			}
		}
		if e := tx.Exec("ALTER TABLE replay_logs ADD COLUMN delivered BOOLEAN").Error; e != nil {
			return e
		}
		truth := true
		sourceID := int64(42)
		contextID := int64(10)
		rows := []ReplayLog{{ID: 1, ImpressionID: "old", AgentID: 1, ItemID: 42, AgentFeatures: "{}", ItemFeatures: "{}", Delivered: &truth}}
		for pos, kind := range []string{"broadcast", "commission", "agent"} {
			row := ReplayLog{ID: int64(pos + 2), ImpressionID: "new", AgentID: 1, PipelineVersion: "need_search_v1", RequestMode: "search", SampleSchemaVersion: 2, SourceKind: kind, SourceID: &sourceID, ContextID: &contextID, Position: pos, AgentFeatures: "{}", ItemFeatures: "{}", Delivered: &truth}
			if kind == "broadcast" {
				row.ItemID = 42
			}
			rows = append(rows, row)
		}
		if e := batchInsertReplayLogs(tx, rows); e != nil {
			return e
		}
		if e := batchInsertReplayLogs(tx, rows); e != nil {
			return e
		}
		var count int64
		if e := tx.Table("replay_logs").Count(&count).Error; e != nil {
			return e
		}
		if count != 4 {
			t.Fatalf("duplicate samples: %d", count)
		}
		var identity []struct {
			SourceKind          string
			ItemID              *int64
			PipelineVersion     string
			RequestMode         string
			SampleSchemaVersion int
		}
		if e := tx.Table("replay_logs").Order("id").Find(&identity).Error; e != nil {
			return e
		}
		if identity[0].PipelineVersion != "legacy_feed_v1" || identity[0].RequestMode != "feed" || identity[0].SampleSchemaVersion != 1 {
			t.Fatalf("legacy defaults: %+v", identity[0])
		}
		for _, r := range identity {
			if (r.SourceKind == "broadcast") != (r.ItemID != nil) {
				t.Fatalf("typed identity leaked: %+v", r)
			}
		}
		if e := tx.Transaction(func(inner *gorm.DB) error {
			return inner.Exec("INSERT INTO replay_logs(id,impression_id,agent_id,source_kind,position,served_at,created_at) VALUES (99,'invalid',1,'agent',0,0,0)").Error
		}); e == nil {
			t.Fatal("NULL typed source ID accepted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
