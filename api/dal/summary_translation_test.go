package dal

import (
	"eigenflux_server/pkg/summary"
	"fmt"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTranslationPostgresSourceFenceAndLegacyIsolation(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required")
	}
	require.True(t, strings.Contains(dsn, "localhost") || strings.Contains(dsn, "127.0.0.1"), "use isolated loopback database")
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	defer sql.Close()
	sql.SetMaxOpenConns(1)
	schema := fmt.Sprintf("translation_%d", time.Now().UnixNano())
	require.NoError(t, db.Exec("CREATE SCHEMA "+schema).Error)
	defer db.Exec("DROP SCHEMA " + schema + " CASCADE")
	require.NoError(t, db.Exec("SET search_path TO "+schema).Error)
	require.NoError(t, db.Exec(`CREATE TABLE raw_items(item_id BIGINT PRIMARY KEY, raw_content TEXT); CREATE TABLE processed_items(item_id BIGINT PRIMARY KEY, summary TEXT, summary_zh TEXT, summary_zh_literal TEXT, title_zh_literal TEXT, summary_zh_key TEXT, title_zh_key TEXT,summary_remediation TEXT NOT NULL DEFAULT '')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO raw_items VALUES (1,'FTC says no'); INSERT INTO processed_items(item_id,summary,summary_zh) VALUES (1,'FTC says no','legacy invented organization')`).Error)
	require.NoError(t, UpdateZhTranslations(db, 1, "FTC says no", "FTC says no", "FTC 说不", "FTC 说不"))
	var row struct{ SummaryZhLiteral, SummaryZhKey, TitleZhLiteral string }
	require.NoError(t, db.Table("processed_items").First(&row).Error)
	require.Equal(t, "FTC 说不", row.SummaryZhLiteral)
	require.Equal(t, summary.TranslationKey("FTC says no"), row.SummaryZhKey)
	// An old binary writes its legacy cache without altering the current-policy cache.
	require.NoError(t, db.Exec(`UPDATE processed_items SET summary_zh='old writer expanded FTC',summary='FTC says perhaps'`).Error)
	require.Error(t, UpdateZhTranslations(db, 1, "FTC says no", "FTC says no", "stale", "stale"))
	require.NoError(t, db.Table("processed_items").First(&row).Error)
	require.Equal(t, "FTC 说不", row.SummaryZhLiteral)
	var visible string
	require.NoError(t, db.Raw(`SELECT CASE WHEN summary_zh_key='literal-zh-v1:' || md5(summary) THEN summary_zh_literal ELSE '' END FROM processed_items`).Scan(&visible).Error)
	require.Empty(t, visible)
	require.NoError(t, UpdateZhTranslations(db, 1, "FTC says perhaps", "FTC says no", "FTC 说也许", "FTC 说不"))
	require.NoError(t, db.Table("processed_items").First(&row).Error)
	require.Equal(t, "FTC 说也许", row.SummaryZhLiteral)
	// An excerpt-only item leaves the warmup queue once its body translation exists.
	require.NoError(t, db.Exec(`ALTER TABLE processed_items ADD COLUMN summary_origin TEXT DEFAULT 'unknown'; ALTER TABLE processed_items ADD COLUMN status SMALLINT DEFAULT 3; CREATE TABLE replay_logs(agent_id BIGINT,item_id BIGINT,item_score FLOAT,source_kind TEXT,request_mode TEXT,served_at BIGINT)`).Error)
	require.NoError(t, db.Exec(`UPDATE processed_items SET summary='',summary_origin='none'; INSERT INTO replay_logs VALUES(9,1,1,'broadcast','feed',100)`).Error)
	rows, err := ListUntranslatedTopItems(db, 0, 8, 60)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, db.Exec(`UPDATE processed_items SET title_zh_key=''`).Error)
	rows, err = ListUntranslatedTopItems(db, 0, 8, 60)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NoError(t, db.Exec(`UPDATE processed_items SET summary_remediation='body_only'`).Error)
	rows, err = ListUntranslatedTopItems(db, 0, 8, 60)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Error(t, UpdateZhTranslations(db, 1, "", "FTC says no", "", "late stale translation"))

}
