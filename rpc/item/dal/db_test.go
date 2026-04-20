package dal

import (
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
)

func TestBatchGetRawItemInfo(t *testing.T) {
	cfg := config.Load()

	// Probe connectivity without calling db.Init (which calls os.Exit on failure).
	probe, err := sql.Open("pgx", cfg.PgDSN)
	if err != nil {
		t.Skipf("db not available: %v", err)
	}
	if err := probe.Ping(); err != nil {
		probe.Close()
		t.Skipf("db not available: %v", err)
	}
	probe.Close()

	db.Init(cfg.PgDSN)

	authorID := int64(999900001)
	withURLID := int64(999900100)
	withoutURLID := int64(999900101)

	db.DB.Exec("DELETE FROM raw_items WHERE item_id IN (?, ?)", withURLID, withoutURLID)
	t.Cleanup(func() {
		db.DB.Exec("DELETE FROM raw_items WHERE item_id IN (?, ?)", withURLID, withoutURLID)
	})

	if err := db.DB.Exec(
		"INSERT INTO raw_items (item_id, author_agent_id, raw_content, raw_url, created_at) VALUES (?, ?, 'x', 'https://ex.test/a', extract(epoch from now())::bigint)",
		withURLID, authorID,
	).Error; err != nil {
		t.Fatalf("insert with url: %v", err)
	}
	if err := db.DB.Exec(
		"INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES (?, ?, 'x', extract(epoch from now())::bigint)",
		withoutURLID, authorID,
	).Error; err != nil {
		t.Fatalf("insert without url: %v", err)
	}

	got, err := BatchGetRawItemInfo(db.DB, []int64{withURLID, withoutURLID})
	if err != nil {
		t.Fatalf("BatchGetRawItemInfo: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	if got[withURLID].AuthorAgentID != authorID || got[withURLID].RawURL != "https://ex.test/a" {
		t.Errorf("with-url row wrong: %+v", got[withURLID])
	}
	if got[withoutURLID].AuthorAgentID != authorID || got[withoutURLID].RawURL != "" {
		t.Errorf("without-url row wrong: %+v", got[withoutURLID])
	}
}
