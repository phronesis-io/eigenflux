package main

import (
	"context"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/featureindex"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type legacyCommissionSource struct{ featureindex.CommissionSource }

func (legacyCommissionSource) ListActiveIndexSnapshots(context.Context, int64, int) ([]featureindex.CommissionCatalogueSnapshot, int64, error) {
	return []featureindex.CommissionCatalogueSnapshot{{CommissionID: 42, SellerAgentID: 7, CatalogueVersion: 1, Status: "active", Title: "design", Currency: "CNY"}}, 0, nil
}
func (legacyCommissionSource) BatchGetStatistics(context.Context, []int64) ([]featureindex.CommissionStatisticsSnapshot, error) {
	return []featureindex.CommissionStatisticsSnapshot{{CommissionID: 42, StatisticsVersion: 2, CompletedCount: 4}}, nil
}

func TestLegacyCommissionLoaderRepairsExpiredCatalogue(t *testing.T) {
	server := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	index := featureindex.CommissionIndex{Redis: r, IndexName: "legacy"}
	require.NoError(t, index.Write(ctx, featureindex.CommissionDocument{CommissionID: 42, SellerAgentID: 7, Active: true, CatalogueVersion: 1, StatisticsVersion: 1}))
	server.FastForward(168*time.Hour + time.Second)
	require.NoError(t, index.WriteStatistics(ctx, featureindex.CommissionStatisticsSnapshot{CommissionID: 42, StatisticsVersion: 2}))
	missing, err := index.Read(ctx, []int64{42})
	require.NoError(t, err)
	require.Empty(t, missing)
	// Exercise the same registration and scheduler used by Pipeline, with the
	// routing switch off and no DB, ES, embedder, or other content source.
	loaders, err := featureLoaders(&config.Config{EnableCommissionIndex: true, CommissionIndexName: "legacy"}, nil, r, legacyCommissionSource{})
	require.NoError(t, err)
	require.NotNil(t, loaders)
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); loaders.Run(run) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool {
		docs, err := index.Read(ctx, []int64{42})
		d, ok := docs[42]
		return err == nil && ok && d.Active && d.CatalogueVersion == 1 && d.StatisticsVersion == 2 && d.CompletedCount == 4
	}, 3*time.Second, 10*time.Millisecond)
	require.Positive(t, r.PTTL(ctx, index.Forward().Key(42, "catalogue")).Val())
}

func TestFeatureLoaderDisabledAndMissingSource(t *testing.T) {
	loaders, err := featureLoaders(&config.Config{}, nil, nil, nil)
	require.NoError(t, err)
	require.Nil(t, loaders)
	_, err = featureLoaders(&config.Config{EnableCommissionIndex: true}, nil, nil, nil)
	require.Error(t, err)
}
