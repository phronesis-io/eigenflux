package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestPGCFeedbackSnapshotRefreshUsesDistributedLock(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()

	called := 0
	refresh := func(context.Context) error {
		called++
		return nil
	}
	if !refreshPGCFeedbackSnapshotWithLock(ctx, rdb, refresh) {
		t.Fatal("first refresh did not run")
	}
	if called != 1 {
		t.Fatalf("refresh calls = %d, want 1", called)
	}
	if mr.Exists(lockKeyPGCFeedbackSnapshot) {
		t.Fatal("successful refresh left its lock behind")
	}

	if err := rdb.Set(ctx, lockKeyPGCFeedbackSnapshot, "other-owner", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if refreshPGCFeedbackSnapshotWithLock(ctx, rdb, refresh) {
		t.Fatal("refresh ran while another replica held the lock")
	}
	if called != 1 {
		t.Fatalf("refresh calls = %d, want 1", called)
	}
}

func TestPGCFeedbackSnapshotRefreshFailureReleasesLock(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	refresh := func(context.Context) error { return errors.New("database unavailable") }
	if refreshPGCFeedbackSnapshotWithLock(context.Background(), rdb, refresh) {
		t.Fatal("failed refresh reported success")
	}
	if mr.Exists(lockKeyPGCFeedbackSnapshot) {
		t.Fatal("failed refresh left its lock behind")
	}
}
