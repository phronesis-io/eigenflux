package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupClaimRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()}), mr
}

func TestClaimInjectedItems_ThenFetchSeesThem(t *testing.T) {
	rdb, _ := setupClaimRedis(t)
	ctx := context.Background()

	claimInjectedItems(ctx, rdb, []int64{1, 2, 3}, time.Minute)

	claimed := fetchInjectClaims(ctx, rdb, []int64{1, 2, 3, 4})
	assert.True(t, claimed[1])
	assert.True(t, claimed[2])
	assert.True(t, claimed[3])
	assert.False(t, claimed[4], "unclaimed item is not reported")
}

func TestClaimInjectedItems_TTLExpiryReleasesClaim(t *testing.T) {
	rdb, mr := setupClaimRedis(t)
	ctx := context.Background()

	claimInjectedItems(ctx, rdb, []int64{7}, time.Minute)
	assert.True(t, fetchInjectClaims(ctx, rdb, []int64{7})[7])

	mr.FastForward(90 * time.Second) // past TTL
	assert.False(t, fetchInjectClaims(ctx, rdb, []int64{7})[7], "expired claim is released")
}

func TestClaimInjectedItems_SetNXDoesNotExtendLiveClaim(t *testing.T) {
	rdb, mr := setupClaimRedis(t)
	ctx := context.Background()

	claimInjectedItems(ctx, rdb, []int64{9}, time.Minute)
	mr.FastForward(45 * time.Second)
	// Re-claim mid-life must NOT reset the TTL (SET NX).
	claimInjectedItems(ctx, rdb, []int64{9}, time.Minute)
	mr.FastForward(20 * time.Second) // total 65s > original 60s TTL
	assert.False(t, fetchInjectClaims(ctx, rdb, []int64{9})[9], "NX claim keeps original TTL, so it expires on schedule")
}

func TestClaimHelpers_ZeroTTLAndEmptyAreNoops(t *testing.T) {
	rdb, _ := setupClaimRedis(t)
	ctx := context.Background()

	claimInjectedItems(ctx, rdb, []int64{5}, 0)    // ttl<=0 → no write
	claimInjectedItems(ctx, rdb, nil, time.Minute) // no ids → no write
	assert.Empty(t, fetchInjectClaims(ctx, rdb, []int64{5}))
	assert.Empty(t, fetchInjectClaims(ctx, rdb, nil))
}

func TestFetchInjectClaims_NilClientFailsOpen(t *testing.T) {
	assert.Empty(t, fetchInjectClaims(context.Background(), nil, []int64{1}))
}

func TestClaimKeyFormat(t *testing.T) {
	assert.Equal(t, "sort:inject:claim:42", fmt.Sprintf(ugcClaimKeyFmt, int64(42)))
}
