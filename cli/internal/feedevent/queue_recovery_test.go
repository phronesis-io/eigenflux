package feedevent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueueScopedPartialAckAndConcurrentEnqueue(t *testing.T) {
	q := NewQueue(t.TempDir())
	now := time.Now().UnixMilli()
	events := []Event{{"kind": "feedback", "client_key": "a", "_scope": "A", "ts": now}, {"kind": "surface", "dedup_key": "legacy", "ts": now}}
	for i := 0; i < 60; i++ {
		events = append(events, Event{"kind": "surface", "dedup_key": string(rune('a' + i)), "_scope": "B", "ts": now})
	}
	events = append(events, Event{"kind": "feedback", "client_key": "b", "_scope": "B", "ts": now})
	if err := q.Enqueue(events); err != nil {
		t.Fatal(err)
	}
	result, err := q.FlushAcknowledged(now, FlushOptions{Scope: "B"}, func(batch []Event) ([]string, error) {
		if batch[0]["client_key"] != "b" {
			t.Fatal("feedback starved", batch)
		}
		for _, e := range batch {
			if e["_scope"] != "B" {
				t.Fatal("foreign or legacy event sent", e)
			}
		}
		// A real concurrent writer must finish while the network operation remains active.
		done := make(chan error, 1)
		go func() {
			done <- q.Enqueue([]Event{{"kind": "feedback", "client_key": "new", "_scope": "B", "ts": now}})
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("network holds queue lock")
		}
		return []string{Identity(batch[0])}, errors.New("behavior failed")
	}, nil)
	if err == nil || result.Flushed != 1 || result.ScopeMismatch != 1 || result.Remaining != 63 {
		t.Fatal(result, err)
	}
	state, err := q.loadUnlocked()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range state.Events {
		if e["client_key"] == "b" {
			t.Fatal("accepted feedback retained")
		}
	}
	if state.Events[0]["_scope"] != "A" {
		t.Fatal("foreign feedback removed")
	}
}

func TestQueueAuthBlockRecovery(t *testing.T) {
	for _, mode := range []string{"foreground", "credential"} {
		t.Run(mode, func(t *testing.T) {
			q := NewQueue(t.TempDir())
			now := time.Now().UnixMilli()
			if err := q.Enqueue([]Event{{"kind": "feedback", "client_key": "a", "_scope": "A", "ts": now}}); err != nil {
				t.Fatal(err)
			}
			options := FlushOptions{Scope: "A"}
			calls := 0
			push := func(batch []Event) ([]string, error) { calls++; return nil, ErrAuthentication }
			if _, err := q.FlushAcknowledged(now, options, push, nil); !errors.Is(err, ErrAuthentication) {
				t.Fatal(err)
			}
			if _, err := q.FlushAcknowledged(now+1, options, push, nil); err != nil || calls != 1 {
				t.Fatal(calls, err)
			}
			if mode == "foreground" {
				options.Foreground = true
			} else {
				options.CredentialsModifiedAt = now + 2
			}
			if _, err := q.FlushAcknowledged(now+3, options, func(batch []Event) ([]string, error) { calls++; return []string{Identity(batch[0])}, nil }, nil); err != nil || calls != 2 {
				t.Fatal(calls, err)
			}
		})
	}
}

func TestQueueMissingTimestampAndOldRewrite(t *testing.T) {
	q := NewQueue(t.TempDir())
	now := time.Now().UnixMilli()
	if err := q.Enqueue([]Event{{"kind": "surface", "dedup_key": "a"}, {"kind": "feedback", "item_id": "1"}, {"kind": "feedback", "item_id": "2"}}); err != nil {
		t.Fatal(err)
	}
	// An older binary drops additive top-level fields but retains the events array.
	state, err := q.loadUnlocked()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"events": state.Events})
	if err = os.WriteFile(q.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = q.Flush(now, func(batch []Event) error {
		if len(batch) != 3 {
			t.Fatal(batch)
		}
		return errors.New("offline")
	})
	if err == nil {
		t.Fatal("missing error")
	}
	drops := 0
	n, remaining, err := q.FlushWithDrops(now+MaxAgeMs+1, func([]Event) error { t.Fatal("expired entries sent"); return nil }, func(n int) error { drops += n; return nil })
	if err != nil || n != 0 || remaining != 0 || drops != 3 {
		t.Fatal(n, remaining, drops, err)
	}
	if err = os.WriteFile(q.path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = q.Stats(now); err == nil {
		t.Fatal("corruption looks empty")
	}
}

func TestQueueCrashAfterSendRetriesSameIdentity(t *testing.T) {
	q := NewQueue(t.TempDir())
	now := time.Now().UnixMilli()
	if err := q.Enqueue([]Event{{"kind": "feedback", "client_key": "a", "ts": now}}); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("missing simulated crash")
			}
		}()
		_, _, _ = q.Flush(now, func(batch []Event) error { panic("after send") })
	}()
	n, left, err := NewQueue(q.dir).Flush(now, func(batch []Event) error {
		if Identity(batch[0]) != "feedback:a" {
			t.Fatal(batch)
		}
		return nil
	})
	if err != nil || n != 1 || left != 0 {
		t.Fatal(n, left, err)
	}
}

func TestQueueExpirySaveFailureDoesNotReportDrops(t *testing.T) {
	q := NewQueue(t.TempDir())
	now := time.Now().UnixMilli()
	q.path = filepath.Join(q.dir, "input.json")
	raw, _ := json.Marshal(queueFile{Events: []Event{{"kind": "surface", "dedup_key": "old", "ts": now - MaxAgeMs - 1}}})
	if err := os.WriteFile(q.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(q.dir, QueueFileName), 0700); err != nil {
		t.Fatal(err)
	}
	_, _, err := q.FlushWithDrops(now, func([]Event) error { t.Fatal("send after failed save"); return nil }, func(int) error { t.Fatal("reported unsaved expiry"); return nil })
	if err == nil {
		t.Fatal("expiry save error hidden")
	}
}
