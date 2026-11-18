package consolev2

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresPMOwnerNoticesRetainLatestAndIsolateOwners(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for the private-message results PostgreSQL test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Exec(`
		CREATE SCHEMA pm_owner_notice_test;
		SET LOCAL search_path TO pm_owner_notice_test;
		CREATE TABLE agents (agent_id BIGINT PRIMARY KEY, agent_name TEXT, short_id TEXT);
		CREATE TABLE conversations (conv_id BIGINT PRIMARY KEY, status INT NOT NULL,
			participant_a BIGINT NOT NULL, participant_b BIGINT NOT NULL, origin_type TEXT);
		INSERT INTO agents VALUES (1, 'Owner', 'owner'), (2, 'Peer', 'peer'), (3, 'Third', 'third');
		INSERT INTO conversations VALUES (99, 0, 1, 2, 'friend'), (100, 0, 2, 3, 'friend');
	`).Error; err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/000139_pm_owner_notices.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	if err := tx.Exec(up).Error; err != nil {
		t.Fatal(err)
	}
	svc := &Service{db: tx}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.POST("/api/v2/pm/owner-notices", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", int64(1))
		svc.savePMOwnerNotice(ctx, c)
	})
	h.GET("/api/v2/console/pm/owner-notices", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", int64(1))
		svc.listPMOwnerNotices(ctx, c)
	})
	h.GET("/api/v2/console/pm/owner-notices/peer", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", int64(2))
		svc.listPMOwnerNotices(ctx, c)
	})

	post := func(input pmOwnerNoticeInput, expected int) {
		t.Helper()
		status, payload, _ := performJSON(t, h, http.MethodPost, "/api/v2/pm/owner-notices", input)
		if status != expected {
			t.Fatalf("status=%d payload=%#v", status, payload)
		}
	}
	input := pmOwnerNoticeInput{ResultID: strings.Repeat("a", 64), ConversationID: "99", Summary: "Newest result", NeedsAction: true, GeneratedAt: 2000}
	post(input, http.StatusOK)
	post(input, http.StatusOK)
	post(pmOwnerNoticeInput{ResultID: strings.Repeat("b", 64), ConversationID: "99", Summary: "Older retry", GeneratedAt: 1000}, http.StatusOK)
	input.ConversationID = "100"
	post(input, http.StatusNotFound)

	var count int64
	if err := tx.Table("pm_owner_notices").Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	status, payload, _ := performJSON(t, h, http.MethodGet, "/api/v2/console/pm/owner-notices", nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d payload=%#v", status, payload)
	}
	data := responseData(t, payload)
	rows := data["results"].([]interface{})
	if len(rows) != 1 {
		t.Fatalf("results=%#v", rows)
	}
	got := rows[0].(map[string]interface{})
	if got["summary"] != "Newest result" || got["conversation_id"] != "99" || got["peer_name"] != "Peer" || got["needs_action"] != true {
		t.Fatalf("result=%#v", got)
	}
	status, payload, _ = performJSON(t, h, http.MethodGet, "/api/v2/console/pm/owner-notices/peer", nil)
	if status != http.StatusOK || len(responseData(t, payload)["results"].([]interface{})) != 0 {
		t.Fatalf("peer status=%d payload=%#v", status, payload)
	}
	if err := tx.Exec(`
		INSERT INTO conversations
		SELECT id, 0, 1, 2, 'friend' FROM generate_series(200, 219) AS id;
		INSERT INTO pm_owner_notices (agent_id, conv_id, result_id, summary, needs_action, generated_at)
		SELECT 1, conv_id, conv_id::text, 'Later update', false, 4000 FROM conversations WHERE conv_id >= 200;
	`).Error; err != nil {
		t.Fatal(err)
	}
	path := "/api/v2/console/pm/owner-notices"
	status, payload, _ = performJSON(t, h, http.MethodGet, path, nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d payload=%#v", status, payload)
	}
	data = responseData(t, payload)
	rows = data["results"].([]interface{})
	if len(rows) != 20 || rows[0].(map[string]interface{})["conversation_id"] != "99" {
		t.Fatalf("first page=%#v", data)
	}
	cursor := data["next_cursor"].(string)
	status, payload, _ = performJSON(t, h, http.MethodGet, path+"?cursor="+cursor, nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d payload=%#v", status, payload)
	}
	data = responseData(t, payload)
	rows = data["results"].([]interface{})
	if len(rows) != 1 || data["next_cursor"] != "" {
		t.Fatalf("second page=%#v", data)
	}
	status, payload, _ = performJSON(t, h, http.MethodGet, path+"?conversations=200,100", nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d payload=%#v", status, payload)
	}
	rows = responseData(t, payload)["results"].([]interface{})
	if len(rows) != 1 || rows[0].(map[string]interface{})["conversation_id"] != "200" ||
		rows[0].(map[string]interface{})["result_id"] != "200" {
		t.Fatalf("scoped results=%#v", rows)
	}
	status, payload, _ = performJSON(t, h, http.MethodGet, path+"?conversations=99&results="+strings.Repeat("b", 64), nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d payload=%#v", status, payload)
	}
	rows = responseData(t, payload)["results"].([]interface{})
	if len(rows) != 1 || rows[0].(map[string]interface{})["summary"] != "Older retry" {
		t.Fatalf("notification should resolve its own result after a newer summary replaces it: %#v", rows)
	}
}
