package featureindex

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type loaderSource struct {
	CommissionSource
	stats []CommissionStatisticsSnapshot
	fail  bool
}

func (s loaderSource) ListActiveIndexSnapshots(context.Context, int64, int) ([]CommissionCatalogueSnapshot, int64, error) {
	if s.fail {
		return nil, 0, errors.New("source failed")
	}
	return []CommissionCatalogueSnapshot{{CommissionID: 7, CatalogueVersion: 3, Status: "active", Title: "design"}}, 7, nil
}
func (s loaderSource) BatchGetStatistics(context.Context, []int64) ([]CommissionStatisticsSnapshot, error) {
	return s.stats, nil
}
func TestPeriodicLoadRepairsBothViewsWithoutES(t *testing.T) {
	r := forwardRedis(t)
	ctx := context.Background()
	withCommissionESTransport(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("feature loader must not access ES")
		return nil, nil
	})
	source := loaderSource{stats: []CommissionStatisticsSnapshot{{CommissionID: 7, StatisticsVersion: 4, CompletionRateBPS: 8000}}}
	next, err := (CommissionIndex{Redis: r, Source: source, IndexName: "test"}).LoadPage(ctx, 0, 100)
	require.NoError(t, err)
	require.EqualValues(t, 7, next)
	rows, err := (CommissionIndex{Redis: r, IndexName: "test"}).Read(ctx, []int64{7})
	require.NoError(t, err)
	require.EqualValues(t, 8000, rows[7].CompletionRateBPS)
	require.Empty(t, rows[7].Title)
	require.EqualValues(t, 3, rows[7].CatalogueVersion)
	require.Empty(t, rows[7].Embedding)
	source.fail = true
	_, err = (CommissionIndex{Redis: r, Source: source, IndexName: "test"}).LoadPage(ctx, 0, 100)
	require.Error(t, err)
	source.fail = false
	source.stats = nil
	_, err = (CommissionIndex{Redis: r, Source: source, IndexName: "test"}).LoadPage(ctx, 0, 100)
	require.Error(t, err, "missing statistics must not turn into a zero-valued success")
}
