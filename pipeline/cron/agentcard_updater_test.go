package main

import (
	"testing"
	"time"

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
