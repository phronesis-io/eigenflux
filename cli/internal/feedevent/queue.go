package feedevent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Queue is the on-disk followup-event queue under <data>/events/. All mutating
// operations take the directory lock so concurrent record/flush can't corrupt it.
type Queue struct {
	dir  string
	path string
}

type queueFile struct {
	Events []Event `json:"events"`
}

// NewQueue returns a queue rooted at dir (e.g. <data>/events).
func NewQueue(dir string) *Queue {
	return &Queue{dir: dir, path: filepath.Join(dir, QueueFileName)}
}

func (q *Queue) loadUnlocked() ([]Event, error) {
	data, err := os.ReadFile(q.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var qf queueFile
	if err := json.Unmarshal(data, &qf); err != nil {
		return nil, fmt.Errorf("read feedback queue: %w", err)
	}
	return qf.Events, nil
}

// saveAtomic writes the queue with the temp-file+rename pattern (0600).
func (q *Queue) saveAtomic(events []Event) error {
	if err := os.MkdirAll(q.dir, dirPerm); err != nil {
		return err
	}
	return writeAtomicJSON(q.dir, QueueFileName, queueFile{Events: events})
}

// Enqueue appends events and persists (locked).
func (q *Queue) Enqueue(events []Event) error {
	if len(events) == 0 {
		return nil
	}
	if err := os.MkdirAll(q.dir, dirPerm); err != nil {
		return err
	}
	l, ok := acquireLock(q.dir)
	if !ok {
		return fmt.Errorf("feedevent: queue busy")
	}
	defer l.release()
	existing, err := q.loadUnlocked()
	if err != nil {
		return err
	}
	all := append(existing, events...)
	return q.saveAtomic(all)
}

// Flush drops stale events, collapses duplicates by dedup_key, sends one batch
// (<=MaxBatch) via push, removes the flushed events on success, and returns the
// counts. It does NOT retry/backoff — the caller owns the cadence. `push` is
// injected so this package stays network-free.
func (q *Queue) Flush(nowMs int64, push func([]Event) error) (flushed, remaining int, err error) {
	return q.FlushWithDrops(nowMs, push, nil)
}

func (q *Queue) FlushWithDrops(nowMs int64, push func([]Event) error, dropped func(int) error) (flushed, remaining int, err error) {
	if err := os.MkdirAll(q.dir, dirPerm); err != nil {
		return 0, 0, err
	}
	l, ok := acquireLock(q.dir)
	if !ok {
		return 0, 0, fmt.Errorf("feedevent: queue busy")
	}
	defer l.release()

	events, loadErr := q.loadUnlocked()
	if loadErr != nil {
		return 0, 0, loadErr
	}
	fresh := dropStale(events, nowMs)
	if len(events) > len(fresh) && dropped != nil {
		if err := dropped(len(events) - len(fresh)); err != nil {
			return 0, len(events), err
		}
	}
	collapsed := collapseByDedup(fresh)
	if len(collapsed) == 0 {
		if len(events) != len(collapsed) {
			_ = q.saveAtomic(collapsed)
		}
		return 0, 0, nil
	}
	batch := collapsed
	if len(batch) > MaxBatch {
		batch = collapsed[:MaxBatch]
	}
	if err := push(batch); err != nil {
		// Keep everything queued for the caller's next flush.
		_ = q.saveAtomic(collapsed)
		return 0, len(collapsed), err
	}
	rest := collapsed[len(batch):]
	if err := q.saveAtomic(rest); err != nil {
		return len(batch), len(rest), err
	}
	return len(batch), len(rest), nil
}

func dropStale(events []Event, nowMs int64) []Event {
	out := events[:0:0]
	for _, e := range events {
		ts := asInt64(e["ts"])
		if ts == 0 || nowMs-ts <= MaxAgeMs {
			out = append(out, e)
		}
	}
	return out
}

func collapseByDedup(events []Event) []Event {
	seen := map[string]bool{}
	out := make([]Event, 0, len(events))
	for _, e := range events {
		dk, _ := e["dedup_key"].(string)
		if e["kind"] == "feedback" {
			key, _ := e["client_key"].(string)
			dk = "feedback:" + key
		}
		if dk != "" {
			if seen[dk] {
				continue
			}
			seen[dk] = true
		}
		out = append(out, e)
	}
	return out
}

func asInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	default:
		return 0
	}
}

// Stats returns gauges from the current queue without modifying it.
func (q *Queue) Stats(now int64) (depth int, oldestAge int64) {
	events, err := q.loadUnlocked()
	if err != nil {
		return 0, 0
	}
	for _, e := range collapseByDedup(dropStale(events, now)) {
		depth++
		if ts := asInt64(e["ts"]); ts > 0 {
			oldestAge = max(oldestAge, now-ts)
		}
	}
	return
}
