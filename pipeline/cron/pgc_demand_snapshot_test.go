package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestPGCDemandSnapshotRefreshUsesDistributedLock(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()

	called := 0
	refresh := func(context.Context) error {
		called++
		return nil
	}
	if !refreshPGCDemandSnapshotWithLock(ctx, rdb, refresh) {
		t.Fatal("first refresh did not run")
	}
	if called != 1 {
		t.Fatalf("refresh calls = %d, want 1", called)
	}
	if mr.Exists(lockKeyPGCDemandSnapshot) {
		t.Fatal("successful refresh left its lock behind")
	}

	if err := rdb.Set(ctx, lockKeyPGCDemandSnapshot, "other-owner", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if refreshPGCDemandSnapshotWithLock(ctx, rdb, refresh) {
		t.Fatal("refresh ran while another replica held the lock")
	}
	if called != 1 {
		t.Fatalf("refresh calls = %d, want 1", called)
	}
}

func TestPGCDemandSnapshotRefreshFailureReleasesLock(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	refresh := func(context.Context) error { return errors.New("database unavailable") }
	if refreshPGCDemandSnapshotWithLock(context.Background(), rdb, refresh) {
		t.Fatal("failed refresh reported success")
	}
	if mr.Exists(lockKeyPGCDemandSnapshot) {
		t.Fatal("failed refresh left its lock behind")
	}
}
