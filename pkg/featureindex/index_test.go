package featureindex

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Exercise each implementation through the same typed interface, including
// stale writes and source-independent warm reads. No domain-specific dispatch.
func checkIndexContract[T any](t *testing.T, index Index[T], fresh, stale T, id int64) {
	t.Helper()
	ctx := context.Background()
	empty, err := index.Read(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
	require.NoError(t, index.Write(ctx, fresh))
	rows, err := index.Read(ctx, []int64{id})
	require.NoError(t, err)
	require.Equal(t, fresh, rows[id])
	require.NoError(t, index.Write(ctx, stale))
	rows, err = index.Read(ctx, []int64{id})
	require.NoError(t, err)
	require.Equal(t, fresh, rows[id], "old versions cannot overwrite current features")
	for _, limit := range []int{0, -1, 1001} {
		_, err := index.LoadPage(ctx, 0, limit)
		require.Error(t, err)
	}
	_, err = index.LoadPage(ctx, 0, 1)
	require.Error(t, err, "a loader without its source returns an error rather than panicking")
}

func TestBuiltInIndexContract(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	t.Run("broadcast", func(t *testing.T) {
		checkIndexContract(t, BroadcastIndex{Redis: r},
			BroadcastDocument{ItemID: 7, Version: 10, Active: true, ContentHash: "fresh", QualityScore: .8},
			BroadcastDocument{ItemID: 7, Version: 9, ContentHash: "stale"}, 7)
	})
	t.Run("agent", func(t *testing.T) {
		checkIndexContract(t, AgentIndex{Redis: r, IndexName: "test"},
			AgentDocument{AgentID: 7, Version: 10, ProjectionVersion: 10, Active: true, ActivityAt: 123},
			AgentDocument{AgentID: 7, Version: 9, ProjectionVersion: 9, ActivityAt: 100}, 7)
	})
	t.Run("commission", func(t *testing.T) {
		checkIndexContract(t, CommissionIndex{Redis: r, IndexName: "test"},
			CommissionDocument{CommissionID: 7, CatalogueVersion: 10, StatisticsVersion: 10, Active: true, PriceFen: 100, CompletedCount: 3},
			CommissionDocument{CommissionID: 7, CatalogueVersion: 9, StatisticsVersion: 9, PriceFen: 200}, 7)
	})
	loaders := Loaders{Redis: r}
	require.Error(t, loaders.Register(Loader{}))
	for _, index := range []Materializer{BroadcastIndex{Redis: r}, AgentIndex{Redis: r, IndexName: "test"}, CommissionIndex{Redis: r, IndexName: "test"}} {
		schedule := Loader{Index: index, Interval: time.Minute, Timeout: time.Second, BatchSize: 100}
		require.NoError(t, loaders.Register(schedule))
		require.Error(t, loaders.Register(schedule), "same view/generation cannot be registered twice")
	}
	require.Len(t, loaders.jobs, 3)
}
