package maintenance

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestQueueBoundedIsolatedAndReplaySafe(t *testing.T) {
	s := Scope{Home: t.TempDir(), Server: "same", AgentID: "1"}
	for i := 0; i < MaxEvents+3; i++ {
		if err := Record(s, NewEvent(NewID(), "cli", "auto", "check", "started")); err != nil {
			t.Fatal(err)
		}
	}
	st, err := Snapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Events) != MaxEvents || st.Dropped != 3 {
		t.Fatalf("unbounded queue: %d dropped=%d", len(st.Events), st.Dropped)
	}
	other := s
	other.AgentID = "2"
	fresh, _ := Snapshot(other)
	if len(fresh.Events) != 0 {
		t.Fatal("account switch exposed another identity's events")
	}
	now := time.Now()
	sends := 0
	failure := errors.New("offline")
	if err := Flush(s, now, func([]Event) error { sends++; return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := Flush(s, now, func([]Event) error { sends++; return nil }); err != nil {
		t.Fatal(err)
	}
	if sends != 1 {
		t.Fatal("failed flush ignored backoff")
	}
	var first string
	if err := Flush(s, now.Add(time.Minute), func(events []Event) error { first = events[0].EventID; return nil }); err != nil {
		t.Fatal(err)
	}
	st, _ = Snapshot(s)
	if len(st.Events) != MaxEvents-50 {
		t.Fatal("ack did not remove only delivered batch")
	}
	for _, e := range st.Events {
		if e.EventID == first {
			t.Fatal("acknowledged event remains")
		}
	}
}

func TestQueueConcurrentRecordAndFlushPreservesNewEvents(t *testing.T) {
	s := Scope{Home: t.TempDir(), Server: "server", AgentID: "42"}
	first := NewEvent(NewID(), "skills", "auto", "install", "installed")
	if err := Record(s, first); err != nil {
		t.Fatal(err)
	}
	if err := Flush(s, time.Now(), func(batch []Event) error {
		if len(batch) != 1 {
			t.Fatal("unexpected batch")
		}
		return Record(s, NewEvent(NewID(), "plugin", "auto", "load", "restart_required"))
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := Snapshot(s)
	if len(st.Events) != 1 || st.Events[0].Component != "plugin" {
		t.Fatal("concurrent event was lost")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Record(s, first); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	st, _ = Snapshot(s)
	if len(st.Events) != 2 {
		t.Fatalf("stable event IDs failed deduplication: %d", len(st.Events))
	}
}

func TestRetentionDueAndReexecAttempt(t *testing.T) {
	s := Scope{Home: t.TempDir(), Server: "server", AgentID: "42"}
	now := time.Now()
	due, err := Due(s, now, 24*time.Hour)
	if err != nil || !due {
		t.Fatal("first maintenance not due")
	}
	if err := MarkAttempt(s, now); err != nil {
		t.Fatal(err)
	}
	due, _ = Due(s, now.Add(time.Hour), 24*time.Hour)
	if due {
		t.Fatal("daily throttle missing")
	}
	e := NewEvent(NewID(), "cli", "auto", "install", "installed")
	e.ToVersion = "1.0.0"
	if err := Record(s, e); err != nil {
		t.Fatal(err)
	}
	saved, err := LastAttempt(s, "cli")
	if err != nil || saved.AttemptID != e.AttemptID || saved.ToVersion != "1.0.0" {
		t.Fatal("reexec installation details lost")
	}
	e = NewEvent(NewID(), "cli", "auto", "check", "started")
	e.EventAt = now.Add(-Retention - time.Minute).UnixMilli()
	_ = Record(s, e)
	st, _ := Snapshot(s)
	if len(st.Events) != 1 || st.Dropped != 1 {
		t.Fatal("retention does not match accepted server window")
	}
}

func TestMetadataRejectsPrivatePaths(t *testing.T) {
	e := NewEvent(NewID(), "cli", "auto", "install", "failed")
	e.ErrorCode = "/Users/someone/private"
	if Validate(e) == nil {
		t.Fatal("raw path accepted as error code")
	}
}

func TestReadAttemptWindowIsBoundedAndSurvivesEventUpload(t *testing.T) {
	scope := Scope{Home: t.TempDir(), Server: "same", AgentID: "42"}
	now := time.Now()
	var first, last string
	for i := 0; i < 40; i++ {
		event := NewEvent(NewID(), "skills", "auto", "install", "no_update")
		event.ToVersion = "revision1"
		event.EventAt = now.Add(time.Duration(i) * time.Millisecond).UnixMilli()
		if i == 0 {
			first = event.AttemptID
		}
		last = event.AttemptID
		if err := Record(scope, event); err != nil {
			t.Fatal(err)
		}
	}
	state, err := Snapshot(scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ReadAttempts) != 32 {
		t.Fatal("read receipt window exceeded its bound")
	}
	if _, exists, err := ReadAttempt(scope, first); err != nil || exists {
		t.Fatal("oldest receipt not evicted")
	}
	if err := Flush(scope, time.Now(), func([]Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	event, exists, err := ReadAttempt(scope, last)
	if err != nil || !exists || event.ToVersion != "revision1" {
		t.Fatal("upload erased the Agent's pending read receipt")
	}
	scope.AgentID = "43"
	if _, exists, err := ReadAttempt(scope, last); err != nil || exists {
		t.Fatal("read receipt crossed identity boundary")
	}
}
