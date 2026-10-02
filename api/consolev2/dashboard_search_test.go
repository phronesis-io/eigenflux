package consolev2

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"eigenflux_server/pkg/dashboardsearch"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestDashboardSearchRejectsInvalidInputAndPreservesScope(t *testing.T) {
	s := &Service{}
	for _, query := range []string{"q=", "q=x&type=secret", "q=x&limit=51", "q=x&cursor=1", "q=x&type=broadcast&status=draft", "q=x&type=friend&cursor=-1"} {
		c := app.NewContext(0)
		c.Set("agent_id", int64(1))
		c.Request.SetRequestURI("/search?" + query)
		s.dashboardSearch(nil)(context.Background(), c)
		if c.Response.StatusCode() != 400 {
			t.Fatalf("%s: %s", query, c.Response.Body())
		}
	}
	c := app.NewContext(0)
	c.Set("agent_id", int64(1))
	c.Set("agent_scopes", pq.StringArray{"profile:read"})
	c.Request.SetRequestURI("/search?q=x&type=message")
	s.dashboardSearch(nil)(context.Background(), c)
	if !strings.Contains(string(c.Response.Body()), "AGENT_SCOPE_REQUIRED") {
		t.Fatal(string(c.Response.Body()))
	}
	c = app.NewContext(0)
	c.Set("agent_id", int64(1))
	c.Request.SetRequestURI("/search?q=x&type=service")
	s.dashboardSearch(nil)(context.Background(), c)
	if !strings.Contains(string(c.Response.Body()), "SEARCH_UNAVAILABLE") {
		t.Fatal(string(c.Response.Body()))
	}
}

func TestDashboardSearchPostgresIsolationLiteralsPagination(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	exec := func(query string, args ...interface{}) {
		t.Helper()
		if err := tx.Exec(query, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, ddl := range []string{
		`CREATE TEMP TABLE agents(agent_id BIGINT,short_id TEXT,agent_name TEXT,agent_name_en TEXT)`,
		`CREATE TEMP TABLE user_relations(id BIGINT,from_uid BIGINT,to_uid BIGINT,rel_type INT,remark TEXT,created_at BIGINT)`,
		`CREATE TEMP TABLE conversations(conv_id BIGINT,participant_a BIGINT,participant_b BIGINT,status INT,topic_status INT)`,
		`CREATE TEMP TABLE private_messages(msg_id BIGINT,conv_id BIGINT,content TEXT,created_at BIGINT)`,
		`CREATE TEMP TABLE raw_items(item_id BIGINT,author_agent_id BIGINT,raw_content TEXT,raw_notes TEXT)`,
		`CREATE TEMP TABLE processed_items(item_id BIGINT,status INT,summary TEXT,summary_zh TEXT,updated_at BIGINT)`,
	} {
		exec(ddl)
	}
	exec(`INSERT INTO agents VALUES (1,'Owner','Owner',''),(2,'AbCdE','好友名字','English NAME'),(3,'Other','Other','')`)
	exec(`INSERT INTO user_relations VALUES (101,1,2,1,'合同_%!',1),(102,2,1,1,'private reverse remark',1),(103,3,2,1,'secret stranger remark',1)`)
	exec(`INSERT INTO conversations VALUES (11,1,2,0,1),(12,3,2,0,1),(13,1,2,1,1)`)
	exec(`INSERT INTO private_messages VALUES (9007199254740993,11,'合同_%!  KeEp  spaces',1),(9007199254740994,11,'合同_%!',2),(9007199254740995,12,'secret 合同_%!',3),(9007199254740996,13,'hidden 合同_%!',4)`)
	exec(`INSERT INTO raw_items VALUES (201,1,'我的合同_%!','notes'),(202,2,'秘密合同_%!',''),(203,1,'已撤回合同_%!',''),(204,1,?,'')`, strings.Repeat("前", 500)+"正文命中"+strings.Repeat("后", 500))
	exec(`INSERT INTO processed_items VALUES (201,3,'summary','摘要',1),(202,3,'','',2),(203,5,'','',3),(204,0,'','',4)`)
	s := &Service{db: tx, enableCommunication: true}
	search := func(kind, q, status string, cursor int64, limit int) dashboardsearch.Group {
		t.Helper()
		g, err := s.searchDashboardLocal(context.Background(), 1, kind, q, status, cursor, limit)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	for _, kind := range []string{"message", "friend", "broadcast"} {
		group := search(kind, "合同_%!", "", 0, 10)
		want := 2
		if kind == "friend" {
			want = 1
		}
		if len(group.Items) != want {
			t.Fatalf("%s: %#v", kind, group)
		}
		if len(search(kind, "合同X", "", 0, 10).Items) != 0 {
			t.Fatal("wildcards expanded")
		}
	}
	first := search("message", "合同_%!", "", 0, 1)
	if !first.HasMore || first.NextCursor != "9007199254740994" || first.Items[0].ID != "9007199254740994" {
		t.Fatal(first)
	}
	second := search("message", "合同_%!", "", 9007199254740994, 1)
	if second.HasMore || len(second.Items) != 1 || second.Items[0].ID != "9007199254740993" {
		t.Fatal(second)
	}
	for _, q := range []string{"9007199254740995", "9007199254740996", "private reverse remark", "secret stranger remark"} {
		if len(search("message", q, "", 0, 10).Items) != 0 {
			t.Fatalf("leak: %s", q)
		}
	}
	if len(search("message", "007199254740993", "", 0, 10).Items) != 0 {
		t.Fatal("partial ID matched")
	}
	if len(search("message", "keep  SPACES", "", 0, 10).Items) != 1 {
		t.Fatal("literal whitespace/case")
	}
	if len(search("friend", "AbCdE", "", 0, 10).Items) != 1 || len(search("friend", "abcde", "", 0, 10).Items) != 0 {
		t.Fatal("short ID not exact")
	}
	if len(search("friend", "english name", "", 0, 10).Items) != 1 {
		t.Fatal("English name")
	}
	if len(search("broadcast", "合同", "retracted", 0, 10).Items) != 1 {
		t.Fatal("status")
	}
	g := search("broadcast", "正文命中", "", 0, 10)
	if len(g.Items) != 1 || !strings.Contains(g.Items[0].Preview, "正文命中") {
		t.Fatal(g)
	}
	c := app.NewContext(0)
	c.Set("agent_id", int64(1))
	c.Request.SetRequestURI("/search?" + url.Values{"q": {"合同_%!"}, "type": {"broadcast"}, "agent_id": {"2"}}.Encode())
	s.dashboardSearch(nil)(context.Background(), c)
	var payload struct {
		Data struct {
			Groups []dashboardsearch.Group `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(c.Response.Body(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, item := range payload.Data.Groups[0].Items {
		if item.ID == "202" {
			t.Fatal("viewer spoof")
		}
	}
}
