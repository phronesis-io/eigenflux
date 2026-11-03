package followuplog

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestBroadcastBehaviorAttributionPostgres(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, cfg.Host)
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(ctx) })
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	_, err = tx.Exec(ctx, `CREATE TEMP TABLE followup_labels (id bigint, agent_id bigint, item_id bigint, impression_id text, kind text, reported_at bigint);
        CREATE TEMP TABLE feedback_logs (id bigint, agent_id bigint, item_id bigint, impression_id text, score smallint, feedback_at bigint);
        CREATE TEMP TABLE replay_logs (id bigint, agent_id bigint, item_id bigint, impression_id text, source_kind text, delivered boolean, served_at bigint, request_mode text, pipeline_version text);
        INSERT INTO replay_logs VALUES
          (1,1,10,'good','broadcast',true,1000,'recommendation','need_search_v1'),
          (2,1,10,'search','broadcast',true,1000,'search','need_search_v1'),
          (3,1,10,'duplicate','broadcast',true,1000,'recommendation','need_search_v1'),
          (4,1,10,'duplicate','broadcast',true,1000,'recommendation','need_search_v1'),
          (5,1,10,'future','broadcast',true,9000,'recommendation','need_search_v1'),
          (6,2,10,'foreign','broadcast',true,1000,'recommendation','need_search_v1'),
          (7,1,11,'other-item','broadcast',true,1000,'recommendation','need_search_v1'),
          (8,1,10,'not-delivered','broadcast',false,1000,'recommendation','need_search_v1'),
          (9,1,10,'other-kind','agent',true,1000,'recommendation','need_search_v1');
        INSERT INTO followup_labels VALUES
          (1,1,10,'good','detail_fetch',2000), (2,1,10,'good','surface',3000),
          (3,1,10,'good','question',4000), (4,1,10,'good','discussion',5000),
          (5,1,10,'search','task',6000), (6,1,10,'','detail_fetch',2000),
          (7,1,10,'missing','detail_fetch',2000), (8,1,10,'duplicate','detail_fetch',2000),
          (9,1,10,'future','detail_fetch',2000), (10,1,10,'foreign','detail_fetch',2000),
          (11,1,10,'other-item','detail_fetch',2000), (12,1,10,'not-delivered','detail_fetch',2000),
          (13,1,10,'other-kind','detail_fetch',2000), (14,1,10,'good','detail_fetch',999),
          (15,1,10,'good','detail_fetch',10000);
        INSERT INTO feedback_logs VALUES (1,1,10,'good',2,7000);`)
	require.NoError(t, err)
	raw, err := os.ReadFile("../../scripts/diagnostics/broadcast_behavior_signals.sql")
	require.NoError(t, err)
	query := strings.NewReplacer(":'since'", "$1", ":'until'", "$2").Replace(string(raw))
	rows, err := tx.Query(ctx, query, time.UnixMilli(1000), time.UnixMilli(10000))
	require.NoError(t, err)
	got := map[string][]any{}
	for rows.Next() {
		values, err := rows.Values()
		require.NoError(t, err)
		got[values[0].(string)] = values
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, 14)
	for id, status := range map[string]string{"1": "matched", "2": "matched", "3": "matched", "4": "matched", "5": "matched", "6": "missing_impression", "7": "unmatched", "8": "ambiguous", "9": "before_exposure", "10": "unmatched", "11": "unmatched", "12": "unmatched", "13": "unmatched"} {
		row := got["followup:"+id]
		require.Equal(t, status, row[8], id)
		if status != "matched" {
			require.Nil(t, row[9], id)
			require.Nil(t, row[11], id)
		}
	}
	require.Equal(t, "server", got["followup:1"][7])
	require.Equal(t, "agent_report", got["followup:2"][7])
	require.Equal(t, "search", got["followup:5"][11])
	require.Equal(t, int16(2), got["feedback:1"][5])
	require.Equal(t, "matched", got["feedback:1"][8])
}
