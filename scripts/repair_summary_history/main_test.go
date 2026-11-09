package main

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHistoricalSuppressionAuditRetryAndReversal(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required")
	}
	require.True(t, strings.Contains(dsn, "127.0.0.1") || strings.Contains(dsn, "localhost"))
	db, e := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, e)
	sql, e := db.DB()
	require.NoError(t, e)
	defer sql.Close()
	sql.SetMaxOpenConns(1)
	schema := fmt.Sprintf("history_repair_%d", time.Now().UnixNano())
	require.NoError(t, db.Exec("CREATE SCHEMA "+schema).Error)
	defer db.Exec("DROP SCHEMA " + schema + " CASCADE")
	require.NoError(t, db.Exec("SET search_path TO "+schema).Error)
	exec := func(q string, args ...any) { t.Helper(); require.NoError(t, db.Exec(q, args...).Error) }
	exec(`CREATE TABLE raw_items(item_id BIGINT PRIMARY KEY,raw_content TEXT,raw_notes TEXT); CREATE TABLE processed_items(item_id BIGINT PRIMARY KEY,status SMALLINT,summary TEXT,summary_origin TEXT,summary_zh TEXT,title_zh TEXT,summary_zh_literal TEXT,title_zh_literal TEXT,summary_zh_key TEXT,title_zh_key TEXT); CREATE TABLE item_publish_outbox(item_id BIGINT PRIMARY KEY,created_at BIGINT,dispatched_at BIGINT)`)
	migration, e := os.ReadFile("../../migrations/000133_summary_remediation.sql")
	require.NoError(t, e)
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	exec(up)
	id := int64(376715300894146560)
	exec(`INSERT INTO raw_items VALUES(?,'original more undercorrection','{"summary":"author more undercorrection"}');`, id)
	exec(`INSERT INTO processed_items VALUES(?,3,'less undercorrection','unknown','更少漏纠','标题','更少漏纠','标题','s','t')`, id)
	s, e := read(db, id, false)
	require.NoError(t, e)
	p := plan{OperationID: "fixture", ItemID: id, Mode: approved[id], SourceHash: sourceHash(s), Before: s.Values, Reason: "verified reversal", Evidence: "case"}
	run := func(apply, revert bool) error {
		return db.Transaction(func(tx *gorm.DB) error { return process(tx, p, apply, revert) })
	}
	require.NoError(t, run(false, false))
	s, e = read(db, id, false)
	require.NoError(t, e)
	require.Equal(t, p.Before, s.Values)
	require.NoError(t, run(true, false))
	require.NoError(t, run(true, false))
	var count int64
	require.NoError(t, db.Table("summary_remediation_audit").Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.Table("item_publish_outbox").Count(&count).Error)
	require.EqualValues(t, 1, count)
	exec(`UPDATE processed_items SET summary='late stale writer',summary_zh='late translation',title_zh_literal='late title' WHERE item_id=?`, id)
	s, e = read(db, id, false)
	require.NoError(t, e)
	require.Equal(t, suppressed(p), s.Values)
	exec(`UPDATE raw_items SET raw_content='concurrent edit' WHERE item_id=?`, id)
	require.ErrorContains(t, run(true, true), "source/lifecycle")
	exec(`UPDATE raw_items SET raw_content='original more undercorrection' WHERE item_id=?`, id)
	require.NoError(t, run(true, true))
	require.NoError(t, run(true, true))
	s, e = read(db, id, false)
	require.NoError(t, e)
	require.Equal(t, p.Before, s.Values)
	require.Empty(t, s.SummaryRemediation)
	require.ErrorContains(t, run(true, false), "reversed")
}
