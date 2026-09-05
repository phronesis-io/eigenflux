package dedup_test

import (
	"context"
	"testing"

	"eigenflux_server/pkg/dedup"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestHashExistsIgnoresLegacyValueFormat(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	server.Set("dedup:hash:content-hash", "not-a-group-id")
	exists, err := dedup.HashExists(context.Background(), client, "content-hash")
	require.NoError(t, err)
	require.True(t, exists)

	missing, err := dedup.HashExists(context.Background(), client, "missing")
	require.NoError(t, err)
	require.False(t, missing)
}
