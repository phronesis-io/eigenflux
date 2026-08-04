package agentcard

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestInfluenceSnapshotsRoundTripAndDelete(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()

	want := map[int64]InfluenceSnapshot{
		11: {Score: 7, BroadcastCount: 3, ConsumedCount: 19, ScoredEvents: 4, Percentile: 80},
		22: {Score: 0, BroadcastCount: 1, ConsumedCount: 0, ScoredEvents: 0, Percentile: 0},
	}
	if err := SetInfluenceSnapshots(ctx, rdb, want); err != nil {
		t.Fatalf("SetInfluenceSnapshots: %v", err)
	}
	got, err := GetInfluenceSnapshots(ctx, rdb)
	if err != nil {
		t.Fatalf("GetInfluenceSnapshots: %v", err)
	}
	if len(got) != len(want) || got[11] != want[11] || got[22] != want[22] {
		t.Fatalf("snapshots = %#v, want %#v", got, want)
	}

	if err := SetInfluencePercentiles(ctx, rdb, map[int64]int{11: 80, 22: 0}); err != nil {
		t.Fatalf("SetInfluencePercentiles: %v", err)
	}
	if err := DeleteInfluenceState(ctx, rdb, []int64{11}, true); err != nil {
		t.Fatalf("DeleteInfluenceState: %v", err)
	}
	if rdb.HExists(ctx, influenceSnapshotHash, "11").Val() || rdb.HExists(ctx, percentileHash, "11").Val() {
		t.Fatal("deleted agent remained in influence state")
	}
	if !rdb.HExists(ctx, influenceSnapshotHash, "22").Val() || !rdb.HExists(ctx, percentileHash, "22").Val() {
		t.Fatal("unrelated agent was removed from influence state")
	}
}

func TestGetInfluenceSnapshotsIgnoresMalformedEntries(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	mr.HSet(influenceSnapshotHash, "bad-id", "1:2:3:4:5")
	mr.HSet(influenceSnapshotHash, "42", "not-a-snapshot")
	mr.HSet(influenceSnapshotHash, "7", "2:9:8:7:6:4:5")

	got, err := GetInfluenceSnapshots(context.Background(), rdb)
	if err != nil {
		t.Fatalf("GetInfluenceSnapshots: %v", err)
	}
	if len(got) != 1 || got[7] != (InfluenceSnapshot{Score: 9, BroadcastCount: 8, ConsumedCount: 7, ScoredEvents: 6, ContentRevision: 4, Percentile: 5}) {
		t.Fatalf("snapshots = %#v", got)
	}
	if rdb.HExists(context.Background(), influenceSnapshotHash, "bad-id").Val() || rdb.HExists(context.Background(), influenceSnapshotHash, "42").Val() {
		t.Fatal("malformed snapshots were not removed")
	}
}

func TestFullReconcileTimestampRoundTrip(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	want := time.UnixMilli(123456789)
	if err := SetLastFullReconcileAt(context.Background(), rdb, want); err != nil {
		t.Fatal(err)
	}
	got, err := GetLastFullReconcileAt(context.Background(), rdb)
	if err != nil || !got.Equal(want) {
		t.Fatalf("got %v, %v; want %v", got, err, want)
	}
}

func TestCorruptFullReconcileTimestampSelfHeals(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	mr.Set(fullReconcileAtKey, "not-an-integer")
	got, err := GetLastFullReconcileAt(ctx, rdb)
	if err != nil || !got.IsZero() {
		t.Fatalf("got %v, %v; want zero, nil", got, err)
	}
	if mr.Exists(fullReconcileAtKey) {
		t.Fatal("corrupt timestamp was not removed")
	}
}

func TestFencedStateWriteRejectsLostLease(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	mr.Set("lock:test", "new-owner")
	err := SetInfluencePercentilesFenced(ctx, rdb, map[int64]int{7: 80}, "lock:test", "old-owner")
	if !errors.Is(err, ErrReconcileLeaseLost) {
		t.Fatalf("err = %v, want ErrReconcileLeaseLost", err)
	}
	if rdb.HExists(ctx, percentileHash, "7").Val() {
		t.Fatal("lost lease mutated percentile state")
	}
}

func TestOversizedSnapshotIsFilteredInsideRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	mr.HSet(influenceSnapshotHash, "7", strings.Repeat("x", 1024))
	got, err := GetInfluenceSnapshots(context.Background(), rdb)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || rdb.HExists(context.Background(), influenceSnapshotHash, "7").Val() {
		t.Fatal("oversized snapshot was returned or retained")
	}
}

func TestFencedSelfHealRejectsLostLease(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	mr.Set("lock:test", "new-owner")
	mr.HSet(influenceSnapshotHash, "7", strings.Repeat("x", 1024))

	_, err := GetInfluenceSnapshotsFenced(ctx, rdb, "lock:test", "old-owner")
	if !errors.Is(err, ErrReconcileLeaseLost) {
		t.Fatalf("err = %v, want ErrReconcileLeaseLost", err)
	}
	if !rdb.HExists(ctx, influenceSnapshotHash, "7").Val() {
		t.Fatal("lost lease performed cache self-healing mutation")
	}
}

func TestFullReconcileProgressSpansRuns(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	mr.Set("lock:test", "owner")
	requireNoError(t, EnsureFullReconcileProgressFenced(ctx, rdb, 123, "lock:test", "owner"))
	requireNoError(t, MarkFullReconcileDoneFenced(ctx, rdb, []int64{1, 2}, "lock:test", "owner"))
	active, done, err := GetFullReconcileProgress(ctx, rdb)
	requireNoError(t, err)
	if !active || len(done) != 2 {
		t.Fatalf("active=%v done=%v", active, done)
	}
	requireNoError(t, MarkFullReconcileDoneFenced(ctx, rdb, []int64{3}, "lock:test", "owner"))
	requireNoError(t, CompleteFullReconcileFenced(ctx, rdb, time.UnixMilli(456), "lock:test", "owner"))
	active, done, err = GetFullReconcileProgress(ctx, rdb)
	requireNoError(t, err)
	if active || len(done) != 0 {
		t.Fatalf("completed progress remained active: %v %v", active, done)
	}
}

func TestFullReconcileProgressPersistsBeyondOneRunLimit(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	mr.Set("lock:test", "owner")
	requireNoError(t, EnsureFullReconcileProgressFenced(ctx, rdb, 123, "lock:test", "owner"))

	ids := make([]int64, 5001)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	requireNoError(t, MarkFullReconcileDoneFenced(ctx, rdb, ids, "lock:test", "owner"))
	active, done, err := GetFullReconcileProgress(ctx, rdb)
	requireNoError(t, err)
	if !active || len(done) != len(ids) {
		t.Fatalf("active=%v done=%d, want active and %d", active, len(done), len(ids))
	}
	got := make([]int64, 0, len(done))
	for id := range done {
		got = append(got, id)
	}
	slices.Sort(got)
	if !slices.Equal(got, ids) {
		t.Fatal("persisted full-reconcile IDs differ from input")
	}
}

func TestCorruptFullReconcileProgressSelfHealsUnderLease(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	mr.Set("lock:test", "owner")
	mr.Set(fullReconcileEpochKey, "123")
	mr.SAdd(fullReconcileDoneKey, "not-an-agent-id")

	epoch, done, err := GetFullReconcileProgressFenced(ctx, rdb, "lock:test", "owner")
	requireNoError(t, err)
	if !epoch.IsZero() || len(done) != 0 {
		t.Fatalf("epoch=%v done=%v, want reset state", epoch, done)
	}
	if mr.Exists(fullReconcileEpochKey) || mr.Exists(fullReconcileDoneKey) {
		t.Fatal("corrupt full-reconcile state was not removed")
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
