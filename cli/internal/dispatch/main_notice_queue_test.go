package dispatch

import "testing"

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
