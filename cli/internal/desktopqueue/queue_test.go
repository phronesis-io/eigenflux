package desktopqueue

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
)

func message(id string) desktopnotify.Message {
	return desktopnotify.Message{ID: id, Title: "阶段变化", Body: "订单动态", URL: "https://example.com/dashboard/notifications/open?agent_id=42&order_id=9007199254740995&role=buyer"}
}
func TestQueueDurableDedupAndAccountIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	q, err := Open(path, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := q.Enqueue(message("notice:1")); err != nil {
			t.Fatal(err)
		}
	}
	q, err = Open(path, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	if pending, sent := q.Counts(); pending != 1 || sent != 0 {
		t.Fatalf("counts %d/%d", pending, sent)
	}
	if _, err := Open(path, "account-b"); err == nil {
		t.Fatal("read another account queue")
	}
	if err := q.Complete("notice:1", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	q, _ = Open(path, "account-a")
	_ = q.Enqueue(message("notice:1"))
	if _, ok := q.Next(time.Now()); ok {
		t.Fatal("restart/duplicate replayed delivered notice")
	}
	// A same-order/version reminder is a distinct source notification.
	if err := q.Enqueue(message("notice:2")); err != nil {
		t.Fatal(err)
	}
	if pending, _ := q.Counts(); pending != 1 {
		t.Fatal("reminder was lost")
	}
}
func TestQueueFailureDoesNotInventReceiptAndRetries(t *testing.T) {
	q, _ := Open(filepath.Join(t.TempDir(), "queue.json"), "a")
	original := q.write
	q.write = func(string, any) error { return errors.New("disk failure") }
	if q.Enqueue(message("n1")) == nil {
		t.Fatal("ignored persistence failure")
	}
	if pending, _ := q.Counts(); pending != 0 {
		t.Fatal("mutated in-memory queue after write failure")
	}
	q.write = original
	_ = q.Enqueue(message("n1"))
	now := time.Now()
	if err := q.Complete("n1", false, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Next(now.Add(59 * time.Second)); ok {
		t.Fatal("retry storm")
	}
	if _, ok := q.Next(now.Add(time.Minute)); !ok {
		t.Fatal("retry lost")
	}
	q.write = func(string, any) error { return errors.New("disk failure") }
	if q.Complete("n1", true, now) == nil {
		t.Fatal("ignored receipt write failure")
	}
	if pending, sent := q.Counts(); pending != 1 || sent != 0 {
		t.Fatal("claimed OS delivery was recorded")
	}
}
