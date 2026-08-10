package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"eigenflux_server/pkg/agentcard"
)

func TestBuildInfluenceSnapshotsUsesTieAwarePercentiles(t *testing.T) {
	rows := []agentInfluenceRow{
		{AgentID: 1, Score: 0, BroadcastCount: 2, ConsumedCount: 5, ScoredEvents: 1, ContentRevision: 10},
		{AgentID: 2, Score: 0, BroadcastCount: 1, ConsumedCount: 3, ScoredEvents: 0},
		{AgentID: 3, Score: 4, BroadcastCount: 7, ConsumedCount: 11, ScoredEvents: 3},
		{AgentID: 4, Score: 9, BroadcastCount: 8, ConsumedCount: 13, ScoredEvents: 6},
	}

	got := buildInfluenceSnapshots(rows)
	want := map[int64]agentcard.InfluenceSnapshot{
		1: {Score: 0, BroadcastCount: 2, ConsumedCount: 5, ScoredEvents: 1, ContentRevision: 10, Percentile: 0},
		2: {Score: 0, BroadcastCount: 1, ConsumedCount: 3, ScoredEvents: 0, Percentile: 0},
		3: {Score: 4, BroadcastCount: 7, ConsumedCount: 11, ScoredEvents: 3, Percentile: 50},
		4: {Score: 9, BroadcastCount: 8, ConsumedCount: 13, ScoredEvents: 6, Percentile: 75},
	}
	if len(got) != len(want) {
		t.Fatalf("len(snapshots) = %d, want %d", len(got), len(want))
	}
	for id, expected := range want {
		if got[id] != expected {
			t.Errorf("snapshot[%d] = %#v, want %#v", id, got[id], expected)
		}
	}
}

func TestShouldRecoverInfluenceSnapshotsAfterPartialRedisLoss(t *testing.T) {
	now := time.Now()
	if !shouldRecoverInfluenceSnapshots(5000, 0, now) {
		t.Fatal("missing snapshot hash with a retained reconcile timestamp must recover in batches")
	}
	if shouldRecoverInfluenceSnapshots(5000, 4999, now) {
		t.Fatal("one normal missing snapshot should not suppress a scheduled full reconcile")
	}
}

func TestCountMissingInfluenceSnapshotsIgnoresOrphans(t *testing.T) {
	current := map[int64]agentcard.InfluenceSnapshot{1: {}, 2: {}, 3: {}}
	previous := map[int64]agentcard.InfluenceSnapshot{1: {}, 99: {}, 100: {}}
	if got := countMissingInfluenceSnapshots(current, previous); got != 2 {
		t.Fatalf("missing = %d, want 2", got)
	}
}

func TestRotateInfluenceRowsChangesRecoveryStart(t *testing.T) {
	rows := make([]agentInfluenceRow, 12)
	for i := range rows {
		rows[i].AgentID = int64(i + 1)
	}
	first := rotateInfluenceRows(rows, time.Unix(0, 0), 5)
	second := rotateInfluenceRows(rows, time.Unix(int64(time.Hour/time.Second), 0), 5)
	if first[0].AgentID == second[0].AgentID {
		t.Fatal("hourly rotation did not advance the recovery start")
	}
}

func TestPrioritizeInfluenceRowsDeduplicatesSchemaCandidates(t *testing.T) {
	rows := []agentInfluenceRow{{AgentID: 1}, {AgentID: 2}, {AgentID: 3}, {AgentID: 4}}
	got := prioritizeInfluenceRows(rows, []int64{3, 1, 3, 99}, time.Unix(0, 0), 10)
	want := []int64{3, 1, 2, 4}
	if len(got) != len(want) {
		t.Fatalf("len(rows) = %d, want %d", len(got), len(want))
	}
	for i, agentID := range want {
		if got[i].AgentID != agentID {
			t.Fatalf("rows[%d].AgentID = %d, want %d", i, got[i].AgentID, agentID)
		}
	}
}

func TestSchemaUpgradeRetryDelay(t *testing.T) {
	tests := []struct {
		count int64
		want  time.Duration
	}{
		{count: 1, want: time.Hour},
		{count: 2, want: time.Hour},
		{count: 3, want: 6 * time.Hour},
		{count: 9, want: 6 * time.Hour},
		{count: 10, want: 24 * time.Hour},
	}
	for _, tt := range tests {
		if got := schemaUpgradeRetryDelay(tt.count); got != tt.want {
			t.Errorf("schemaUpgradeRetryDelay(%d) = %s, want %s", tt.count, got, tt.want)
		}
	}
}

func TestRebuildRetryStateIsAtomicAndLeaseFenced(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	const lockKey = "lock:agentcard-test"
	if err := rdb.Set(ctx, lockKey, "owner-a", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000_000, 0)
	retryAt, err := deferSchemaUpgradeRetry(ctx, rdb, 42, now, lockKey, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(time.Hour); !retryAt.Equal(want) {
		t.Fatalf("retryAt = %s, want %s", retryAt, want)
	}
	retryZSet, retryHash := schemaUpgradeRetryKeys()
	if got := rdb.HGet(ctx, retryHash, "42").Val(); got != "1" {
		t.Fatalf("retry count = %q, want 1", got)
	}
	if got := int64(rdb.ZScore(ctx, retryZSet, "42").Val()); got != retryAt.Unix() {
		t.Fatalf("retry score = %d, want %d", got, retryAt.Unix())
	}
	if _, err := deferSchemaUpgradeRetry(ctx, rdb, 42, now, lockKey, "owner-a"); err != nil {
		t.Fatal(err)
	}
	retryAt, err = deferSchemaUpgradeRetry(ctx, rdb, 42, now, lockKey, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(6 * time.Hour); !retryAt.Equal(want) {
		t.Fatalf("third retryAt = %s, want %s", retryAt, want)
	}

	if err := rdb.Set(ctx, lockKey, "owner-b", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := deferSchemaUpgradeRetry(ctx, rdb, 42, now, lockKey, "owner-a"); !errors.Is(err, agentcard.ErrReconcileLeaseLost) {
		t.Fatalf("stale defer error = %v, want lease lost", err)
	}
	if err := clearSchemaUpgradeRetry(ctx, rdb, 42, lockKey, "owner-a"); !errors.Is(err, agentcard.ErrReconcileLeaseLost) {
		t.Fatalf("stale clear error = %v, want lease lost", err)
	}
	if got := rdb.HGet(ctx, retryHash, "42").Val(); got != "3" {
		t.Fatalf("stale owner changed retry count to %q", got)
	}
	if err := clearSchemaUpgradeRetry(ctx, rdb, 42, lockKey, "owner-b"); err != nil {
		t.Fatal(err)
	}
	if rdb.HExists(ctx, retryHash, "42").Val() || rdb.ZScore(ctx, retryZSet, "42").Err() != redis.Nil {
		t.Fatal("new owner did not atomically clear retry state")
	}
}

func TestRetryReadsAndSchemaCursorRejectStaleLease(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	const lockKey = "lock:agentcard-cursor-test"
	if err := rdb.Set(ctx, lockKey, "owner-a", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	wantCursor := schemaUpgradeCursor{SchemaVersion: 3, AgentID: 99}
	if err := setSchemaUpgradeCursorFenced(ctx, rdb, wantCursor, lockKey, "owner-a"); err != nil {
		t.Fatal(err)
	}
	if got, err := getSchemaUpgradeCursorFenced(ctx, rdb, lockKey, "owner-a"); err != nil || got != wantCursor {
		t.Fatalf("cursor = %#v, err=%v, want %#v", got, err, wantCursor)
	}
	if _, err := deferSchemaUpgradeRetry(ctx, rdb, 7, time.Unix(2_000_000, 0), lockKey, "owner-a"); err != nil {
		t.Fatal(err)
	}
	if got, err := getRebuildRetryAtFenced(ctx, rdb, []int64{7, 8}, lockKey, "owner-a"); err != nil || got[7] == 0 || got[8] != 0 {
		t.Fatalf("retry state = %#v, err=%v", got, err)
	}
	if err := rdb.Set(ctx, lockKey, "owner-b", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := getSchemaUpgradeCursorFenced(ctx, rdb, lockKey, "owner-a"); !errors.Is(err, agentcard.ErrReconcileLeaseLost) {
		t.Fatalf("stale cursor read error = %v, want lease lost", err)
	}
	if _, err := getRebuildRetryAtFenced(ctx, rdb, []int64{7}, lockKey, "owner-a"); !errors.Is(err, agentcard.ErrReconcileLeaseLost) {
		t.Fatalf("stale retry read error = %v, want lease lost", err)
	}
}

func TestSelectGeneralRebuildRowsReservesSchemaLane(t *testing.T) {
	rows := []agentInfluenceRow{{AgentID: 1}, {AgentID: 2}, {AgentID: 3}, {AgentID: 4}}
	schema := map[int64]struct{}{1: {}}
	dirty := map[int64]struct{}{1: {}, 2: {}, 3: {}}
	fullDone := map[int64]struct{}{3: {}}
	selected, workRows := selectGeneralRebuildRows(rows, schema, dirty, fullDone, true, time.Unix(0, 0), 2)
	if workRows != 3 {
		t.Fatalf("general work rows = %d, want 3", workRows)
	}
	if len(selected) != 2 || selected[0].AgentID != 2 || selected[1].AgentID != 3 {
		t.Fatalf("selected = %#v, want agents 2 and 3", selected)
	}
	for _, row := range selected {
		if row.AgentID == 1 {
			t.Fatal("schema candidate leaked into the general lane")
		}
	}
}

func TestBuildInfluenceSnapshotsDetectsTopItemContentChanges(t *testing.T) {
	before := buildInfluenceSnapshots([]agentInfluenceRow{{AgentID: 1, Score: 3, BroadcastCount: 2, ScoredEvents: 2, ContentRevision: 100}})
	after := buildInfluenceSnapshots([]agentInfluenceRow{{AgentID: 1, Score: 3, BroadcastCount: 2, ScoredEvents: 2, ContentRevision: 101}})
	if before[1] == after[1] {
		t.Fatal("content revision did not change the influence snapshot")
	}
}

func TestBuildInfluenceSnapshotsDetectsNonScoreChanges(t *testing.T) {
	before := buildInfluenceSnapshots([]agentInfluenceRow{{AgentID: 1, Score: 3, BroadcastCount: 1, ConsumedCount: 2}})
	after := buildInfluenceSnapshots([]agentInfluenceRow{{AgentID: 1, Score: 3, BroadcastCount: 1, ConsumedCount: 3}})
	if before[1] == after[1] {
		t.Fatal("consumed_count change did not change the influence snapshot")
	}
	after = buildInfluenceSnapshots([]agentInfluenceRow{{AgentID: 1, Score: 3, BroadcastCount: 1, ConsumedCount: 2, ScoredEvents: 1}})
	if before[1] == after[1] {
		t.Fatal("negative feedback did not change the influence snapshot")
	}
}
