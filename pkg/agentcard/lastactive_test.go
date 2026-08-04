package agentcard

import (
	"context"
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
