package dispatch

import (
	"path/filepath"
	"testing"
	"time"

	watchstate "cli.eigenflux.ai/internal/watch"
)

func TestMainNoticeQueuePreservesSameVersionEventsAndNoReplay(t *testing.T) {
	binding := validTestBinding(t)
	q, err := OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	first := MainNotice{ID: "11", OrderID: "901", Version: 3, Role: "buyer", Event: "order.state.changed.v1", State: "pending_payment"}
	second := first
	second.ID = "12"
	second.Event = "order.reminder.v1"
	for _, item := range []MainNotice{first, first, second} {
		if err = q.Add(item); err != nil {
			t.Fatal(err)
		}
	}
	item, ok, err := q.Next()
	if err != nil || !ok || item.ID != "11" {
		t.Fatalf("claim: %+v %v", item, err)
	}
	// Crash while running must not replay a possibly admitted host turn.
	q, err = OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err = q.Next()
	if err != nil || !ok || item.ID != "12" {
		t.Fatalf("replayed or lost event: %+v %v", item, err)
	}
	if err = q.Finish(item.ID, "skipped", "unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = q.Next(); err != nil || ok {
		t.Fatalf("duplicate pending: %v %v", ok, err)
	}
	if err = q.Add(second); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ = q.Next(); ok {
		t.Fatal("failed turn was automatically replayed")
	}
}

func TestMainNoticeQueueIsAccountScopedAndValidatesRouting(t *testing.T) {
	binding := validTestBinding(t)
	q, err := OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	item := MainNotice{ID: "11", OrderID: "901", Version: 3, Role: "buyer", State: "awaiting_buyer_confirmation"}
	if err = q.Add(item); err != nil {
		t.Fatal(err)
	}
	invalid := item
	invalid.ID = "12"
	invalid.Role = "admin"
	if q.Add(invalid) == nil {
		t.Fatal("invalid recipient role accepted")
	}
	invalid = item
	invalid.ID = "001"
	if q.Add(invalid) == nil {
		t.Fatal("noncanonical notification ID accepted")
	}
	other := validTestBinding(t)
	q2, err := OpenMainNoticeQueue(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := q2.Next(); ok {
		t.Fatal("account queue leaked")
	}
}

func TestMainNoticeQueueTimedRetriesAreBoundedAndDurable(t *testing.T) {
	binding := validTestBinding(t)
	q, err := OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	first := MainNotice{ID: "11", OrderID: "901", Version: 3, Role: "buyer", State: "pending_payment"}
	if err := q.Add(first); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	delays := []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}
	for attempt := 1; attempt <= 5; attempt++ {
		item, ok, err := q.NextAt(now)
		if err != nil || !ok || item.ID != first.ID || item.Attempts != attempt || item.NextAttemptAt != 0 {
			t.Fatalf("attempt %d: %+v %v %v", attempt, item, ok, err)
		}
		if err := q.FinishAt(item.ID, "retry_pending", "main_notice_busy", now); err != nil {
			t.Fatal(err)
		}
		q, err = OpenMainNoticeQueue(binding)
		if err != nil {
			t.Fatal(err)
		}
		if err := q.Add(first); err != nil {
			t.Fatal(err)
		}
		stored := q.state.Entries[0]
		if stored.Attempts != attempt {
			t.Fatalf("duplicate or restart reset attempts: %+v", stored)
		}
		if attempt == 5 {
			if stored.Status != "skipped" || stored.Code != "main_notice_retry_exhausted" || stored.NextAttemptAt != 0 {
				t.Fatalf("retry limit not persisted: %+v", stored)
			}
			break
		}
		due := now.Add(delays[attempt-1])
		if stored.Status != "pending" || stored.NextAttemptAt != due.UnixMilli() {
			t.Fatalf("incorrect retry deadline: %+v", stored)
		}
		if _, ok, err := q.NextAt(due.Add(-time.Millisecond)); err != nil || ok {
			t.Fatalf("ran before deadline: %v %v", ok, err)
		}
		now = due
	}
	if _, ok, err := q.NextAt(now.Add(24 * time.Hour)); err != nil || ok {
		t.Fatalf("exhausted notice replayed: %v %v", ok, err)
	}
}

func TestMainNoticeQueueRetryDeadlineDoesNotBlockOtherDueNotices(t *testing.T) {
	q, err := OpenMainNoticeQueue(validTestBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	first := MainNotice{ID: "11", OrderID: "901", Version: 3, Role: "buyer", State: "pending_payment"}
	if err := q.Add(first); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	if _, ok, err := q.NextAt(now); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := q.FinishAt(first.ID, "retry_pending", "main_notice_busy", now); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "12"
	if err := q.Add(second); err != nil {
		t.Fatal(err)
	}
	item, ok, err := q.NextAt(now)
	if err != nil || !ok || item.ID != second.ID || item.Attempts != 1 {
		t.Fatalf("delayed notice blocked due work: %+v %v %v", item, ok, err)
	}
}

func TestMainNoticeQueueDeliveredNoticeNeverRetries(t *testing.T) {
	binding := validTestBinding(t)
	q, err := OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	item := MainNotice{ID: "11", OrderID: "901", Version: 3, Role: "buyer", State: "awaiting_buyer_confirmation"}
	if err := q.Add(item); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, ok, err := q.NextAt(now); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := q.FinishAt(item.ID, "retry_pending", "main_notice_busy", now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := q.NextAt(now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("retry: %v %v", ok, err)
	}
	if err := q.FinishAt(item.ID, "delivered", "main_notice_completed", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	q, err = OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Add(item); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := q.NextAt(now.Add(24 * time.Hour)); err != nil || ok {
		t.Fatalf("successful notice replayed: %v %v", ok, err)
	}
	if entry := q.state.Entries[0]; entry.Attempts != 2 || entry.NextAttemptAt != 0 || entry.Status != "delivered" {
		t.Fatalf("receipt changed: %+v", entry)
	}
}

func TestMainNoticeQueueRetryStateValidationAndLegacyCompatibility(t *testing.T) {
	legacy := MainNotice{ID: "11", OrderID: "901", Version: 3, Role: "buyer", State: "pending_payment", Status: "pending"}
	for _, status := range []string{"pending", "running", "delivered", "skipped"} {
		item := legacy
		item.Status = status
		if !validMainNotice(item) {
			t.Fatalf("legacy zero counters rejected for %s", status)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*MainNotice)
	}{
		{"negative_attempts", func(n *MainNotice) { n.Attempts = -1 }},
		{"too_many_attempts", func(n *MainNotice) { n.Attempts = 6 }},
		{"exhausted_pending", func(n *MainNotice) { n.Attempts, n.NextAttemptAt = 5, 1 }},
		{"negative_deadline", func(n *MainNotice) { n.NextAttemptAt = -1 }},
		{"overflow_deadline", func(n *MainNotice) { n.Attempts, n.NextAttemptAt = 1, 253402300800000 }},
		{"deadline_before_first_attempt", func(n *MainNotice) { n.NextAttemptAt = 1 }},
		{"retry_without_deadline", func(n *MainNotice) { n.Attempts = 1 }},
		{"terminal_deadline", func(n *MainNotice) { n.Status, n.Attempts, n.NextAttemptAt = "delivered", 1, 1 }},
		{"running_deadline", func(n *MainNotice) { n.Status, n.Attempts, n.NextAttemptAt = "running", 1, 1 }},
		{"transient_status_on_disk", func(n *MainNotice) { n.Status = "retry_pending" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := validTestBinding(t)
			q, err := OpenMainNoticeQueue(binding)
			if err != nil {
				t.Fatal(err)
			}
			item := legacy
			tc.change(&item)
			if err := WriteJSON(q.path, mainNoticeQueueState{Scope: binding.Scope, Revision: binding.Revision, Entries: []MainNotice{item}}); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenMainNoticeQueue(binding); err == nil {
				t.Fatalf("invalid retry record accepted: %+v", item)
			}
		})
	}
}

func TestMainNoticeQueueDoesNotAdvanceOnPersistenceFailure(t *testing.T) {
	q, err := OpenMainNoticeQueue(validTestBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	item := MainNotice{ID: "11", OrderID: "901", Version: 3, Role: "buyer", State: "pending_payment"}
	if err := q.Add(item); err != nil {
		t.Fatal(err)
	}
	path := q.path
	q.path = filepath.Dir(path)
	if _, _, err := q.Next(); err == nil {
		t.Fatal("claim succeeded without durable receipt")
	}
	if entry := q.state.Entries[0]; entry.Status != "pending" || entry.Attempts != 0 {
		t.Fatalf("failed persistence changed memory: %+v", entry)
	}
	q.path = path
	if _, ok, err := q.Next(); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	q.path = filepath.Dir(path)
	if err := q.Finish(item.ID, "retry_pending", "main_notice_busy"); err == nil {
		t.Fatal("finish succeeded without durable receipt")
	}
	if entry := q.state.Entries[0]; entry.Status != "running" || entry.Attempts != 1 || entry.NextAttemptAt != 0 {
		t.Fatalf("failed persistence changed memory: %+v", entry)
	}
}

func TestMainNoticeQueueRetryCrashAndSameHomeAccountIsolation(t *testing.T) {
	binding := validTestBinding(t)
	q, err := OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	item := MainNotice{ID: "11", OrderID: "901", Version: 3, Role: "buyer", State: "pending_payment"}
	if err := q.Add(item); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, ok, err := q.NextAt(now); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := q.FinishAt(item.ID, "retry_pending", "main_notice_busy", now); err != nil {
		t.Fatal(err)
	}
	other := binding
	other.AgentID, other.PrincipalID = "other-agent", "other-principal"
	other.Scope = watchstate.Scope(other.Home, other.Server, other.AgentID, other.PrincipalID)
	otherQueue, err := OpenMainNoticeQueue(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := otherQueue.NextAt(now.Add(time.Hour)); err != nil || ok {
		t.Fatalf("retry crossed account boundary: %v %v", ok, err)
	}
	if _, ok, err := q.NextAt(now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("retry: %v %v", ok, err)
	}
	q, err = OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	if entry := q.state.Entries[0]; entry.Status != "skipped" || entry.Code != "execution_unconfirmed" || entry.Attempts != 2 || entry.NextAttemptAt != 0 {
		t.Fatalf("uncertain second attempt replayable after crash: %+v", entry)
	}
	if err := q.Add(item); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := q.NextAt(now.Add(time.Hour)); err != nil || ok {
		t.Fatalf("uncertain retry repeated: %v %v", ok, err)
	}
}
