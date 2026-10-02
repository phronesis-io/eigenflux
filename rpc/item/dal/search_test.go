package dal

import (
	"context"
	"os"
	"strings"
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
		`CREATE TEMP TABLE raw_items(item_id BIGINT,author_agent_id BIGINT,raw_content TEXT,raw_notes TEXT)`,
		`CREATE TEMP TABLE processed_items(item_id BIGINT,status INT,summary TEXT,summary_zh TEXT,updated_at BIGINT)`,
	} {
		exec(ddl)
	}
	exec(`INSERT INTO raw_items VALUES (201,1,'我的合同_%!','notes'),(202,2,'秘密合同_%!',''),(203,1,'已撤回合同_%!',''),(204,1,?,'')`, strings.Repeat("前", 500)+"正文命中"+strings.Repeat("后", 500))
	exec(`INSERT INTO processed_items VALUES (201,3,'summary','摘要',1),(202,3,'','',2),(203,5,'','',3),(204,0,'','',4)`)

	find := func(kind, q, status string, cursor int64, limit int) *search.SearchResp {
		t.Helper()
		req := &search.SearchReq{OwnerAgentId: 1, Query: q, Status: status, Cursor: cursor, Limit: int32(limit)}
		result, err := SearchOwnedBroadcasts(context.Background(), tx, req)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	for _, q := range []string{"合同_%!", "合同"} {
		if got := find("broadcast", q, "", 0, 10); len(got.Items) != 2 {
			t.Fatalf("%s: %#v", q, got)
		}
	}
	for _, q := range []string{"202", "秘密", "合同X", "0201"} {
		if got := find("broadcast", q, "", 0, 10); len(got.Items) != 0 {
			t.Fatalf("leak/false match %s: %#v", q, got)
		}
	}
	if got := find("broadcast", "201", "", 0, 10); len(got.Items) != 1 || got.Items[0].Id != 201 {
		t.Fatal(got)
	}
	first := find("broadcast", "合同", "", 0, 1)
	if !first.HasMore || first.NextCursor != 203 || first.Items[0].Id != 203 {
		t.Fatal(first)
	}
	second := find("broadcast", "合同", "", first.NextCursor, 1)
	if second.HasMore || len(second.Items) != 1 || second.Items[0].Id != 201 {
		t.Fatal(second)
	}
	if len(find("broadcast", "合同", "retracted", 0, 10).Items) != 1 {
		t.Fatal("status")
	}
	g := find("broadcast", "正文命中", "", 0, 10)
	if len(g.Items) != 1 || !strings.Contains(g.Items[0].Preview, "正文命中") {
		t.Fatal(g)
	}
}
