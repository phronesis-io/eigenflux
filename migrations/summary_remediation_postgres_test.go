package migrations_test

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSummaryRemediationRejectsLegacyResurrection(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "localhost"}, cfg.Host)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := pgx.ConnectConfig(ctx, cfg)
	require.NoError(t, err)
	defer c.Close(ctx)
	tx, err := c.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	exec := func(q string) { t.Helper(); _, err := tx.Exec(ctx, q); require.NoError(t, err) }
	schema := fmt.Sprintf("summary_remediation_%d", time.Now().UnixNano())
	exec("CREATE SCHEMA " + schema + "; SET LOCAL search_path TO " + schema + ",public")
	exec(`CREATE TABLE raw_items(item_id BIGINT PRIMARY KEY); INSERT INTO raw_items VALUES(1),(2);
 CREATE TABLE processed_items(item_id BIGINT PRIMARY KEY,summary TEXT,summary_origin TEXT,summary_zh TEXT,title_zh TEXT,summary_zh_literal TEXT,title_zh_literal TEXT,summary_zh_key TEXT,title_zh_key TEXT);
 INSERT INTO processed_items VALUES(1,'bad','unknown','坏','标题','坏','标题','s','t'),(2,'FTC','unknown','联邦贸易委员会','正文','译文','正文','s','t')`)
	up, _, _ := strings.Cut(migration(t, "000133_summary_remediation.sql"), "-- +goose Down")
	exec(up)
	exec(`UPDATE processed_items SET summary_remediation='body_only' WHERE item_id=1; UPDATE processed_items SET summary_remediation='summary_original' WHERE item_id=2`)
	// Both pre-upgrade and current writers attempt to resurrect the old text.
	exec(`UPDATE processed_items SET summary_zh='old bad',summary_zh_literal='new bad',summary_zh_key='stale'; UPDATE processed_items SET summary='old bad',summary_origin='platform',title_zh='bad',title_zh_literal='bad' WHERE item_id=1`)
	var summary, origin, zh, literal, title string
	require.NoError(t, tx.QueryRow(ctx, "SELECT summary,summary_origin,summary_zh,summary_zh_literal,title_zh_literal FROM processed_items WHERE item_id=1").Scan(&summary, &origin, &zh, &literal, &title))
	require.Empty(t, summary)
	require.Equal(t, "none", origin)
	require.Empty(t, zh)
	require.Empty(t, literal)
	require.Empty(t, title)
	require.NoError(t, tx.QueryRow(ctx, "SELECT summary,summary_zh,summary_zh_literal,title_zh_literal FROM processed_items WHERE item_id=2").Scan(&summary, &zh, &literal, &title))
	require.Equal(t, "FTC", summary)
	require.Empty(t, zh)
	require.Empty(t, literal)
	require.Equal(t, "正文", title)
}
