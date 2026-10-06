package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRecommendationEffectLockAndFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	called := 0
	refresh := func(ctx context.Context) error {
		called++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("batch has no deadline")
		}
		return errors.New("fixture failed")
	}
	ctx := context.Background()
	if refreshRecommendationEffectWithLock(ctx, rdb, refresh) || called != 1 || mr.Exists(lockKeyRecommendationEffect) {
		t.Fatal("failure was reported as success or leaked the lock")
	}
	if err := rdb.Set(ctx, lockKeyRecommendationEffect, "other", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if refreshRecommendationEffectWithLock(ctx, rdb, refresh) || called != 1 {
		t.Fatal("replica refreshed while another owns the lease")
	}
}
