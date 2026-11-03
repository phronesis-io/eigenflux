package followuplog

import (
	"context"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pkg/mq"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestDetailEventIdentityAndBounds(t *testing.T) {
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	first := detailEvent(1, 2, "imp-a", now)
	require.Equal(t, DetailFetch, first.Kind)
	require.Equal(t, "imp-a", first.ImpressionID)
	require.Len(t, first.DedupKey, 32)
	require.Equal(t, now.UnixMilli(), first.ReportedAt)
	require.Equal(t, first.DedupKey, detailEvent(1, 2, "imp-a", now.Add(48*time.Hour)).DedupKey)
	for _, other := range []Event{detailEvent(2, 2, "imp-a", now), detailEvent(1, 3, "imp-a", now), detailEvent(1, 2, "imp-b", now)} {
		require.NotEqual(t, first.DedupKey, other.DedupKey)
	}
	unknown := detailEvent(1, 2, "", now)
	for _, invalid := range []string{"", strings.Repeat("x", 65), "imp\x00a", "imp\na"} {
		event := detailEvent(1, 2, invalid, now.Add(time.Hour))
		require.Empty(t, event.ImpressionID)
		require.Equal(t, unknown.DedupKey, event.DedupKey)
	}
	require.NotEqual(t, unknown.DedupKey, detailEvent(1, 2, "", now.Add(24*time.Hour)).DedupKey)
}

func TestRecordDetailFetchSurvivesRequestCancellation(t *testing.T) {
	srv := miniredis.RunT(t)
	previous := mq.RDB
	mq.RDB = redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = mq.RDB.Close(); mq.RDB = previous })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	RecordDetailFetch(ctx, 1, 1, 2, "self")
	RecordDetailFetch(ctx, 0, 2, 3, "unauthenticated")
	RecordDetailFetch(ctx, 1, 2, 0, "invalid")
	RecordDetailFetch(ctx, 1, 2, 3, "original-exposure")
	require.Eventually(t, func() bool { return mq.RDB.XLen(context.Background(), StreamName).Val() == 1 }, time.Second, time.Millisecond)
	rows, err := mq.RDB.XRange(context.Background(), StreamName, "-", "+").Result()
	require.NoError(t, err)
	require.Equal(t, "1", rows[0].Values["agent_id"])
	require.Equal(t, "3", rows[0].Values["item_id"])
	require.Equal(t, DetailFetch, rows[0].Values["kind"])
	require.Equal(t, "original-exposure", rows[0].Values["impression_id"])
	require.Empty(t, rows[0].Values["brief"])
}
