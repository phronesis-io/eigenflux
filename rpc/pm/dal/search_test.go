package dal

import (
	"context"
	"os"
	"testing"

	search "eigenflux_server/kitex_gen/eigenflux/recordsearch"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRecordSearchPostgresIsolationLiteralsPagination(t *testing.T) {
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
	} {
		exec(ddl)
	}
	exec(`INSERT INTO agents VALUES (1,'Owner','Owner',''),(2,'AbCdE','好友名字','English NAME'),(3,'Other','Other','')`)
	exec(`INSERT INTO user_relations VALUES (101,1,2,1,'合同_%!',1),(102,2,1,1,'private reverse remark',1),(103,3,2,1,'secret stranger remark',1)`)
	exec(`INSERT INTO conversations VALUES (11,1,2,0,1),(12,3,2,0,1),(13,1,2,1,1)`)
	exec(`INSERT INTO private_messages VALUES (9007199254740993,11,'合同_%!  KeEp  spaces',1),(9007199254740994,11,'合同_%!',2),(9007199254740995,12,'secret 合同_%!',3),(9007199254740996,13,'hidden 合同_%!',4)`)

	find := func(kind, q, status string, cursor int64, limit int) *search.SearchResp {
		t.Helper()
		req := &search.SearchReq{OwnerAgentId: 1, Query: q, Status: status, Cursor: cursor, Limit: int32(limit)}

		var result *search.SearchResp
		var err error
		if kind == "message" {
			result, err = SearchMessages(context.Background(), tx, req)
		} else {
			result, err = SearchFriends(context.Background(), tx, req)
		}
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, kind := range []string{"message", "friend"} {
		group := find(kind, "合同_%!", "", 0, 10)
		want := 2
		if kind == "friend" {
			want = 1
		}
		if len(group.Items) != want {
			t.Fatalf("%s: %#v", kind, group)
		}
		if len(find(kind, "合同X", "", 0, 10).Items) != 0 {
			t.Fatal("wildcards expanded")
		}
	}
	first := find("message", "合同_%!", "", 0, 1)
	if !first.HasMore || first.NextCursor != 9007199254740994 || first.Items[0].Id != 9007199254740994 {
		t.Fatal(first)
	}
	second := find("message", "合同_%!", "", 9007199254740994, 1)
	if second.HasMore || len(second.Items) != 1 || second.Items[0].Id != 9007199254740993 {
		t.Fatal(second)
	}
	for _, q := range []string{"9007199254740995", "9007199254740996", "private reverse remark", "secret stranger remark"} {
		if len(find("message", q, "", 0, 10).Items) != 0 {
			t.Fatalf("leak: %s", q)
		}
	}
	if len(find("message", "007199254740993", "", 0, 10).Items) != 0 {
		t.Fatal("partial ID matched")
	}
	if len(find("message", "keep  SPACES", "", 0, 10).Items) != 1 {
		t.Fatal("literal whitespace/case")
	}
	if len(find("friend", "AbCdE", "", 0, 10).Items) != 1 || len(find("friend", "abcde", "", 0, 10).Items) != 0 {
		t.Fatal("short ID not exact")
	}
	if len(find("friend", "english name", "", 0, 10).Items) != 1 {
		t.Fatal("English name")
	}
}
