package dal

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFeedbackHealthReportBounds(t *testing.T) {
	var absent *FeedbackHealthReport
	require.True(t, absent.Valid())
	r := FeedbackHealthReport{WindowMS: 1}
	require.True(t, r.Valid())
	for _, field := range []string{"Pulls", "DeliveredItems", "ScoredItems", "StatusRuns", "FeedbackAttempts", "QueueDrops", "QueueDepth"} {
		for _, n := range []int64{-1, 0, 1000000, 1000001} {
			r := FeedbackHealthReport{WindowMS: 1}
			reflect.ValueOf(&r).Elem().FieldByName(field).SetInt(n)
			require.Equal(t, n >= 0 && n <= 1000000, r.Valid(), "%s=%d", field, n)
		}
	}
	for _, n := range []int64{0, 1, 172800000, 172800001} {
		r.WindowMS = n
		require.Equal(t, n >= 1 && n <= 172800000, r.Valid())
	}
	r.WindowMS = 1
	for _, n := range []int64{-1, 0, 604800000, 604800001} {
		r.OldestQueuedAgeMS = n
		require.Equal(t, n >= 0 && n <= 604800000, r.Valid())
	}
	r.OldestQueuedAgeMS = 0
	r.FeedbackAttempts = 3
	for _, n := range []int64{-1, 0, 3, 4} {
		r.FeedbackFailures = n
		require.Equal(t, n >= 0 && n <= 3, r.Valid())
	}
}

func TestReportFeedbackHealthLatestShanghaiDay(t *testing.T) {
	database := observationDB(t)
	require.NoError(t, database.AutoMigrate(&FeedbackHealthDaily{}))
	require.NoError(t, ReportFeedbackHealth(database, 1, 0, "", nil))
	var count int64
	require.NoError(t, database.Model(&FeedbackHealthDaily{}).Count(&count).Error)
	require.Zero(t, count)
	at := time.Date(2026, 10, 20, 16, 0, 0, 0, time.UTC).UnixMilli()
	for _, delta := range []int64{1, 2, 0} {
		r := FeedbackHealthReport{WindowMS: 1000, Pulls: delta + 1}
		require.NoError(t, ReportFeedbackHealth(database, 1, at+delta, strings.Repeat("v", 40), &r))
	}
	var row FeedbackHealthDaily
	require.NoError(t, database.Where("day = ?", "2026-10-21").First(&row).Error)
	require.Equal(t, at+2, row.ReportedAt)
	require.Equal(t, int64(3), row.Pulls)
	require.Len(t, row.CLIVersion, 32)
	require.NoError(t, ReportFeedbackHealth(database, 1, at-1, "0.6.10", &FeedbackHealthReport{WindowMS: 1}))
	require.NoError(t, database.Model(&FeedbackHealthDaily{}).Count(&count).Error)
	require.Equal(t, int64(2), count)
}
