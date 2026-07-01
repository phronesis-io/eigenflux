package feedevent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFeed(t *testing.T, broadcastsDir, date, stamp, impressionID string, itemIDs ...string) {
	t.Helper()
	dir := filepath.Join(broadcastsDir, date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	items := make([]map[string]any, 0, len(itemIDs))
	for _, id := range itemIDs {
		items = append(items, map[string]any{"item_id": id, "summary": "sum-" + id})
	}
	body, _ := json.Marshal(map[string]any{"impression_id": impressionID, "items": items})
	if err := os.WriteFile(filepath.Join(dir, "feeds-"+stamp+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerLookupAndNewestWins(t *testing.T) {
	bc := t.TempDir()
	// Older response served item 1 with impression A; newer served item 1 with B.
	writeFeed(t, bc, "20260101", "20260101-100000", "imp-A", "1", "2")
	writeFeed(t, bc, "20260102", "20260102-100000", "imp-B", "1", "3")
	now := time.Now().UnixMilli()
	l := NewLedger(bc, "srv")

	if e, s := l.Lookup("1", now); s != StatusHit || e.ImpressionID != "imp-B" {
		t.Fatalf("item 1: want hit/imp-B, got %s/%s (newest should win)", s, e.ImpressionID)
	}
	if e, s := l.Lookup("2", now); s != StatusHit || e.ImpressionID != "imp-A" {
		t.Fatalf("item 2: want hit/imp-A, got %s/%s", s, e.ImpressionID)
	}
	if _, s := l.Lookup("999", now); s != StatusMissing {
		t.Fatalf("item 999: want missing, got %s", s)
	}
}

func TestLedgerExpired(t *testing.T) {
	bc := t.TempDir()
	writeFeed(t, bc, "20260101", "20260101-100000", "imp-A", "1")
	l := NewLedger(bc, "srv")
	// now far in the future (> 8 days after the file mtime) => expired.
	future := time.Now().Add(30 * 24 * time.Hour).UnixMilli()
	if _, s := l.Lookup("1", future); s != StatusExpired {
		t.Fatalf("want expired, got %s", s)
	}
}

func TestQueueEnqueueFlushCollapseAndRemaining(t *testing.T) {
	q := NewQueue(t.TempDir())
	now := time.Now().UnixMilli()
	// two events, one duplicate dedup_key -> collapses to 2 unique (a, a-dup->1) ... build 3, dk x2.
	ev := func(id, dk string) Event {
		return Event{"item_id": id, "kind": "surface", "dedup_key": dk, "ts": now}
	}
	if err := q.Enqueue([]Event{ev("1", "k1"), ev("2", "k2"), ev("1", "k1")}); err != nil {
		t.Fatal(err)
	}

	var pushed [][]Event
	push := func(b []Event) error { pushed = append(pushed, b); return nil }
	flushed, remaining, err := q.Flush(now, push)
	if err != nil {
		t.Fatal(err)
	}
	if flushed != 2 || remaining != 0 {
		t.Fatalf("want flushed=2 remaining=0 (dedup collapse), got %d/%d", flushed, remaining)
	}
	if len(pushed) != 1 || len(pushed[0]) != 2 {
		t.Fatalf("push batch wrong: %v", pushed)
	}
	// Queue now empty.
	if f2, r2, _ := q.Flush(now, push); f2 != 0 || r2 != 0 {
		t.Fatalf("second flush should be empty, got %d/%d", f2, r2)
	}
}

func TestQueueFlushPushFailureKeepsQueue(t *testing.T) {
	q := NewQueue(t.TempDir())
	now := time.Now().UnixMilli()
	q.Enqueue([]Event{{"item_id": "1", "kind": "surface", "dedup_key": "k", "ts": now}})
	failing := func(b []Event) error { return errBoom }
	flushed, remaining, err := q.Flush(now, failing)
	if err == nil || flushed != 0 || remaining != 1 {
		t.Fatalf("want push failure kept queued (0/1/err), got %d/%d/%v", flushed, remaining, err)
	}
	// Recover: a successful flush drains it.
	f2, r2, _ := q.Flush(now, func(b []Event) error { return nil })
	if f2 != 1 || r2 != 0 {
		t.Fatalf("recovery flush want 1/0, got %d/%d", f2, r2)
	}
}

func TestQueueDropsStale(t *testing.T) {
	q := NewQueue(t.TempDir())
	now := time.Now().UnixMilli()
	old := now - MaxAgeMs - 1000
	q.Enqueue([]Event{
		{"item_id": "old", "dedup_key": "a", "ts": old},
		{"item_id": "new", "dedup_key": "b", "ts": now},
	})
	var got []Event
	q.Flush(now, func(b []Event) error { got = b; return nil })
	if len(got) != 1 || got[0]["item_id"] != "new" {
		t.Fatalf("stale event should be dropped, pushed=%v", got)
	}
}

func TestQueueBatchCap(t *testing.T) {
	q := NewQueue(t.TempDir())
	now := time.Now().UnixMilli()
	var evs []Event
	for i := 0; i < MaxBatch+10; i++ {
		evs = append(evs, Event{"item_id": itoa(i), "dedup_key": itoa(i), "ts": now})
	}
	q.Enqueue(evs)
	flushed, remaining, _ := q.Flush(now, func(b []Event) error { return nil })
	if flushed != MaxBatch || remaining != 10 {
		t.Fatalf("want flushed=%d remaining=10, got %d/%d", MaxBatch, flushed, remaining)
	}
}

func TestLockExclusive(t *testing.T) {
	dir := t.TempDir()
	l1, ok1 := acquireLock(dir)
	if !ok1 {
		t.Fatal("first lock failed")
	}
	if _, ok2 := acquireLock(dir); ok2 {
		t.Fatal("second lock should fail while held")
	}
	l1.release()
	if l3, ok3 := acquireLock(dir); !ok3 {
		t.Fatal("lock should reacquire after release")
	} else {
		l3.release()
	}
}

var errBoom = &boomErr{}

type boomErr struct{}

func (*boomErr) Error() string { return "boom" }

func itoa(i int) string {
	const d = "0123456789"
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{d[i%10]}, b...)
		i /= 10
	}
	return string(b)
}
