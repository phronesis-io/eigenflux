package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestProfileCleanupLockOnlyOwnerCanRelease(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	token, acquired, err := acquireProfileCleanupLock(context.Background(), rdb, time.Minute)
	if err != nil || !acquired || token == "" {
		t.Fatalf("acquire lock = (%q, %v, %v)", token, acquired, err)
	}
	if _, acquired, err := acquireProfileCleanupLock(context.Background(), rdb, time.Minute); err != nil || acquired {
		t.Fatalf("second acquire = (%v, %v), want held", acquired, err)
	}

	releaseProfileCleanupLock(rdb, "not-the-owner")
	if !mr.Exists(lockKeyProfileChangeCleanup) {
		t.Fatal("a non-owner token deleted the live lock")
	}
	releaseProfileCleanupLock(rdb, token)
	if mr.Exists(lockKeyProfileChangeCleanup) {
		t.Fatal("the owner token did not release the lock")
	}
}

func TestProfileCleanupSharedSuccessMarkerSkipsReplicaWork(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	if err := rdb.Set(context.Background(), lastProfileChangeCleanupKey, "done", 24*time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if got := cleanupProfileChangesWithLock(context.Background(), rdb); got != 24*time.Hour {
		t.Fatalf("next run = %s, want 24h", got)
	}
	if mr.Exists(lockKeyProfileChangeCleanup) {
		t.Fatal("replica skip left the distributed lock behind")
	}
}
