package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestReleaseLockDoesNotDeleteNewOwner(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	token, acquired, err := acquireLock(ctx, rdb, "lock:test", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire: token=%q acquired=%v err=%v", token, acquired, err)
	}
	if err := rdb.Set(ctx, "lock:test", "new-owner", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	releaseLock(rdb, "lock:test", token)
	if got := rdb.Get(ctx, "lock:test").Val(); got != "new-owner" {
		t.Fatalf("lock owner = %q, want new-owner", got)
	}
}

func TestRenewLockReportsLostOwnership(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	token, acquired, err := acquireLock(ctx, rdb, "lock:renew", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire: %v", err)
	}
	if err := rdb.Set(ctx, "lock:renew", "new-owner", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	renewed, err := renewLock(ctx, rdb, "lock:renew", token, time.Minute)
	if err != nil || renewed {
		t.Fatalf("renewed=%v err=%v, want lost ownership", renewed, err)
	}
}
