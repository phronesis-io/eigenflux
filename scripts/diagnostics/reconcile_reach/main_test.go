package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestDatabaseStartupRespectsCommandDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { require.NoError(t, listener.Close()) }()
	accepted := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		close(accepted)
		// Accept the socket but never answer the PostgreSQL startup handshake.
		<-stop
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = openDatabase(ctx, "postgres://fixture:fixture@"+listener.Addr().String()+"/fixture?sslmode=disable")
	require.Error(t, err)
	require.Less(t, time.Since(start), time.Second)
	select {
	case <-accepted:
	default:
		t.Fatal("startup timeout must exercise the accepted database connection")
	}
}

func TestParseIDs(t *testing.T) {
	ids, err := parseIDs("3, 1,3")
	require.NoError(t, err)
	require.Equal(t, []int64{1, 3}, ids)
	for _, input := range []string{"", "0", "-1", "1,", "9223372036854775808"} {
		_, err := parseIDs(input)
		require.Error(t, err)
	}
}

func reachFixture(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("REACH_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated loopback REACH_TEST_DSN required")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, u.Hostname())
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	schema := fmt.Sprintf("reach_repair_%d", time.Now().UnixNano())
	require.NoError(t, root.Exec("CREATE SCHEMA "+schema).Error)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
		require.NoError(t, root.Exec("DROP SCHEMA "+schema+" CASCADE").Error)
		sqlRoot, _ := root.DB()
		_ = sqlRoot.Close()
	})
	require.NoError(t, db.Exec(`CREATE TABLE item_stats(item_id bigint PRIMARY KEY, consumed_count bigint NOT NULL, updated_at bigint NOT NULL DEFAULT 0)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE replay_logs(item_id bigint, agent_id bigint, impression_id text, source_kind text, delivered boolean, served_at bigint)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO item_stats(item_id,consumed_count) VALUES(1,0),(2,10),(3,0)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO replay_logs VALUES
(1,11,'a','broadcast',true,100),(1,11,'a','broadcast',true,100),
(1,11,'b','broadcast',true,101),(1,12,'c','broadcast',true,102),
(1,12,'d','broadcast',false,103),(1,12,'e','broadcast',NULL,103),
(1,12,'f','agent',true,103),(1,12,'g','broadcast',true,200),
(1,12,'','broadcast',true,103),(2,11,'h','broadcast',true,100)`).Error)
	return db
}

func TestPostgresReachFloorPreviewApplyAndRetry(t *testing.T) {
	db := reachFixture(t)
	rows, err := reconcile(context.Background(), db, []int64{1, 2, 3}, 200, false)
	require.NoError(t, err)
	require.Equal(t, []report{{1, 0, 3, 2, 3}, {2, 10, 1, 1, 10}, {3, 0, 0, 0, 0}}, rows)
	var count int64
	require.NoError(t, db.Table("item_stats").Select("consumed_count").Where("item_id=1").Scan(&count).Error)
	require.Zero(t, count)
	rows, err = reconcile(context.Background(), db, []int64{1, 2, 3}, 200, true)
	require.NoError(t, err)
	require.Equal(t, int64(3), rows[0].After)
	// A live increment after repair must survive every later repair retry.
	require.NoError(t, db.Exec("UPDATE item_stats SET consumed_count=consumed_count+1 WHERE item_id=1").Error)
	rows, err = reconcile(context.Background(), db, []int64{1}, 200, true)
	require.NoError(t, err)
	require.Equal(t, int64(4), rows[0].Before)
	require.Equal(t, int64(4), rows[0].After)
	// A missing row aborts the entire invocation rather than applying a partial batch.
	require.NoError(t, db.Exec("UPDATE item_stats SET consumed_count=0 WHERE item_id=1").Error)
	_, err = reconcile(context.Background(), db, []int64{1, 99}, 200, true)
	require.Error(t, err)
	require.NoError(t, db.Table("item_stats").Select("consumed_count").Where("item_id=1").Scan(&count).Error)
	require.Zero(t, count)
}

func TestPostgresReachRepairPreservesConcurrentIncrement(t *testing.T) {
	db := reachFixture(t)
	writer := db.Begin()
	require.NoError(t, writer.Error)
	var pid int
	require.NoError(t, writer.Raw("SELECT pg_backend_pid()").Scan(&pid).Error)
	require.NoError(t, writer.Exec("UPDATE item_stats SET consumed_count=11 WHERE item_id=1").Error)
	defer writer.Rollback()
	done := make(chan error, 1)
	go func() { _, err := reconcile(context.Background(), db, []int64{1}, 200, true); done <- err }()
	require.Eventually(t, func() bool {
		var blocked bool
		err := db.Raw(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE ? = ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked).Error
		return err == nil && blocked
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, writer.Commit().Error)
	require.NoError(t, <-done)
	var count int64
	require.NoError(t, db.Table("item_stats").Select("consumed_count").Where("item_id=1").Scan(&count).Error)
	require.Equal(t, int64(11), count)
}
