package commissionindex

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIndependentForwardVersionsAndTombstone(t *testing.T) {
	r := forwardRedis(t)
	ctx := context.Background()
	d := Document{CommissionID: 8, CatalogueVersion: 10, StatisticsVersion: 20, Title: "current", CompletedCount: 2, Active: true}
	require.NoError(t, WriteForward(ctx, r, "commissions-v1", d))
	d.CatalogueVersion, d.StatisticsVersion, d.Title, d.CompletedCount = 9, 21, "stale", 3
	require.NoError(t, WriteForward(ctx, r, "commissions-v1", d))
	rows, err := ReadForward(ctx, r, "commissions-v1", []int64{8})
	require.NoError(t, err)
	require.Equal(t, "current", rows[8].Title)
	require.EqualValues(t, 21, rows[8].StatisticsVersion)
	require.EqualValues(t, 3, rows[8].CompletedCount)
	withCommissionESTransport(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("statistics updates must not access ES")
		return nil, nil
	})
	s := ESStore{Redis: r, Index: "commissions-v1"}
	require.NoError(t, s.UpsertStatistics(ctx, StatisticsSnapshot{CommissionID: 8, StatisticsVersion: 19, CompletedCount: 99}))
	d.Active, d.CatalogueVersion = false, 11
	require.NoError(t, WriteForward(ctx, r, "commissions-v1", d))
	d.Active, d.CatalogueVersion = true, 10
	require.NoError(t, WriteForward(ctx, r, "commissions-v1", d))
	rows, err = ReadForward(ctx, r, "commissions-v1", []int64{8})
	require.NoError(t, err)
	require.False(t, rows[8].Active)
	require.EqualValues(t, 3, rows[8].CompletedCount)
}

func TestCommissionESContainsOnlyRetrievalFields(t *testing.T) {
	d := Document{CommissionID: 8, CatalogueVersion: 1, StatisticsVersion: 2, CompletionRateBPS: 9000, HasRating: true, AverageRatingMilli: 4500, PriceFen: 100}
	fields := d.SearchFields()
	properties := Mapping(2)["properties"].(map[string]any)
	for _, key := range []string{"statistics_version", "completed_count", "refunded_count", "completion_rate_bps", "average_rating_milli", "has_rating", "average_delivery_ms", "updated_at", "tags"} {
		require.NotContains(t, fields, key)
		require.NotContains(t, properties, key)
	}
	for _, key := range []string{"commission_id", "catalogue_version", "price_fen", "currency", "promised_delivery_ms", "retrieval_slots", "search_text", "embedding"} {
		require.Contains(t, fields, key)
		require.Contains(t, properties, key)
	}
	require.Equal(t, "strict", Mapping(2)["dynamic"])
}

func TestProjectionWritesRequireConcreteGeneration(t *testing.T) {
	r := forwardRedis(t)
	s := ESStore{Redis: r, Alias: "commissions"}
	require.Error(t, s.Upsert(context.Background(), Document{CommissionID: 8, CatalogueVersion: 1}))
	require.Error(t, s.UpsertStatistics(context.Background(), StatisticsSnapshot{CommissionID: 8, StatisticsVersion: 1}))
}
