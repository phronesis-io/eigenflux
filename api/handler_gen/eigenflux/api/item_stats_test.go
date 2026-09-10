package api

import (
	"context"
	"encoding/json"
	"testing"

	"eigenflux_server/pkg/db"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/route/param"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestGetItemAggregateStats(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	defer sqlDB.Close()
	previous := db.DB
	db.DB = database
	defer func() { db.DB = previous }()
	for _, query := range []string{
		`CREATE TABLE raw_items (item_id INTEGER PRIMARY KEY, author_agent_id INTEGER, raw_content TEXT, raw_url TEXT, created_at INTEGER)`,
		`CREATE TABLE processed_items (item_id INTEGER PRIMARY KEY, status INTEGER)`,
		`CREATE TABLE item_stats (item_id INTEGER PRIMARY KEY, author_agent_id INTEGER, consumed_count INTEGER, score_1_count INTEGER, score_2_count INTEGER, total_score INTEGER)`,
		`CREATE TABLE agent_cards (agent_id INTEGER PRIMARY KEY, private_card TEXT)`,
		`CREATE TABLE agents (agent_id INTEGER PRIMARY KEY, short_id TEXT, agent_name TEXT, agent_name_en TEXT, identity_state TEXT)`,
		`CREATE TABLE feedback_logs (id INTEGER PRIMARY KEY, item_id INTEGER, agent_id INTEGER, score INTEGER, feedback_at INTEGER)`,
		`CREATE TABLE agent_settings (agent_id INTEGER, show_add_friend BOOLEAN)`,
		`CREATE TABLE user_relations (from_uid INTEGER, to_uid INTEGER, rel_type INTEGER)`,
		`INSERT INTO raw_items VALUES (7, 42, 'broadcast', '', 10), (8, 42, 'new broadcast', '', 11), (9, 42, 'unknown statistics', '', 12)`,
		`INSERT INTO processed_items VALUES (7, 3), (8, 3), (9, 3)`,
		`INSERT INTO item_stats VALUES (7, 42, 153, 12, 4, 20), (8, 42, 0, 0, 0, 0)`,
	} {
		if err := database.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, itemID   string
		viewer         int64
		reads, helpful float64
		known          bool
	}{
		{"non-author", "7", 99, 153, 16, true},
		{"author", "7", 42, 153, 16, true},
		{"stored zero", "8", 99, 0, 0, true},
		{"missing stats", "9", 99, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := app.NewContext(1)
			c.Request.SetRequestURI("/api/v1/items/" + tc.itemID)
			c.Request.Header.SetMethod("GET")
			c.Params = param.Params{{Key: "item_id", Value: tc.itemID}}
			c.Set("agent_id", tc.viewer)
			GetItem(context.Background(), c)
			var result struct {
				Code int
				Data struct{ Item map[string]interface{} }
			}
			if err := json.Unmarshal(c.Response.Body(), &result); err != nil {
				t.Fatal(err)
			}
			if c.Response.StatusCode() != 200 || result.Code != 0 {
				t.Fatalf("response: %s", c.Response.Body())
			}
			item := result.Data.Item
			for key, want := range map[string]float64{"consumed_count": tc.reads, "praise_count": tc.helpful} {
				got, exists := item[key]
				if exists != tc.known || (exists && got != want) {
					t.Errorf("%s=%v (present %v), want %v (present %v)", key, got, exists, want, tc.known)
				}
			}
			for _, key := range []string{"interaction_total", "recent_interactions"} {
				_, exists := item[key]
				if exists != tc.known {
					t.Errorf("unexpected visibility for %s: %v", key, item)
				}
			}
		})
	}
}
