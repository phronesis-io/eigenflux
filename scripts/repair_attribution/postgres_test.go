package main

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPostgresLockedRepairReadsBothTablesAndIsIdempotent(t *testing.T) {
	dsn := os.Getenv("FIDELITY_REPAIR_TEST_DSN")
	if dsn == "" {
		t.Skip("set FIDELITY_REPAIR_TEST_DSN to an isolated test PostgreSQL")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	defer sql.Close()
	sql.SetMaxOpenConns(1)
	rollback := errors.New("rollback fixture")
	err = db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec("CREATE TEMP TABLE raw_items(item_id bigint PRIMARY KEY,author_agent_id bigint,raw_url text,raw_content text) ON COMMIT DROP").Error)
		require.NoError(t, tx.Exec("CREATE TEMP TABLE processed_items(item_id bigint PRIMARY KEY,group_id bigint,status int,expire_time text,summary text,summary_zh text,updated_at bigint) ON COMMIT DROP").Error)
		p := plan{ID: 1, Author: 2, Group: 3, URL: "https://example.test", Expiry: "2020-01-01T00:00:00Z", Before: textFields{"raw", "bad", "坏"}, After: textFields{"signature", "good", "好"}}
		require.NoError(t, tx.Exec("INSERT INTO raw_items VALUES(1,2,?,?)", p.URL, p.Before.Content).Error)
		require.NoError(t, tx.Exec("INSERT INTO processed_items VALUES(1,3,3,?,?,?,0)", p.Expiry, p.Before.Summary, p.Before.SummaryZh).Error)
		changed, err := repairText(tx, p, false, false, nil)
		require.NoError(t, err)
		require.False(t, changed)
		changed, err = repairText(tx, p, true, false, nil)
		require.NoError(t, err)
		require.True(t, changed)
		var got struct {
			Content, Summary, SummaryZh string
			Status                      int
			GroupID                     int64
			ExpireTime                  string
		}
		require.NoError(t, tx.Raw("SELECT r.raw_content AS content,p.summary,p.summary_zh,p.status,p.group_id,p.expire_time FROM raw_items r JOIN processed_items p USING(item_id) WHERE r.item_id=1").Scan(&got).Error)
		require.Equal(t, p.After, textFields{got.Content, got.Summary, got.SummaryZh})
		require.Equal(t, 3, got.Status)
		require.Equal(t, p.Group, got.GroupID)
		require.Equal(t, p.Expiry, got.ExpireTime)
		changed, err = repairText(tx, p, true, false, nil)
		require.NoError(t, err)
		require.False(t, changed)
		require.NoError(t, tx.Exec("CREATE TEMP TABLE agents(agent_id bigint PRIMARY KEY,email text) ON COMMIT DROP").Error)
		require.NoError(t, tx.Exec("INSERT INTO agents VALUES(2,'source@pgc.eigenflux.one')").Error)
		p.Before = p.After
		p.After.Summary = "reviewed live summary"
		p.After.SummaryZh = "核实后的摘要"
		p.Expiry = ""
		require.NoError(t, tx.Exec("UPDATE processed_items SET expire_time='' WHERE item_id=1").Error)
		changed, err = repairText(tx, p, true, true, []string{"@pgc.eigenflux.one"})
		require.NoError(t, err)
		require.True(t, changed)
		changed, err = repairText(tx, p, true, true, []string{"@pgc.eigenflux.one"})
		require.NoError(t, err)
		require.False(t, changed)
		require.NoError(t, tx.Exec("UPDATE agents SET email='person@example.com'").Error)
		_, err = repairText(tx, p, true, true, []string{"@pgc.eigenflux.one"})
		require.ErrorContains(t, err, "authoritative PGC")
		return rollback
	})
	require.ErrorIs(t, err, rollback)
}
