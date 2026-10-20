package dispatch

import (
	"strings"
	"testing"
)

func TestBuyerInputMainNoticeDurableDedupAndValidation(t *testing.T) {
	b := validTestBinding(t)
	q, err := OpenMainNoticeQueue(b)
	if err != nil {
		t.Fatal(err)
	}
	n := BuyerInputMainNotice(b.Scope, "9007199254740993", 4, strings.Repeat("a", 64))
	for range 2 {
		if err := q.Add(n); err != nil {
			t.Fatal(err)
		}
	}
	q, err = OpenMainNoticeQueue(b)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := q.Next()
	if err != nil || !ok || got.InputRequestID != n.InputRequestID || got.OrderID != n.OrderID {
		t.Fatal(got, ok, err)
	}
	if _, ok, err := q.Next(); err != nil || ok {
		t.Fatal("duplicate notice", ok, err)
	}
	for _, change := range []func(*MainNotice){
		func(n *MainNotice) { n.Role = "seller" },
		func(n *MainNotice) { n.InputRequestID = "bad" },
		func(n *MainNotice) { n.JobID = strings.Repeat("a", 64) },
		func(n *MainNotice) { n.State = "pending_payment" },
		func(n *MainNotice) { n.ID = "123" },
		func(n *MainNotice) { n.Event = "order.state.changed.v1"; n.ID = "123" },
	} {
		bad := n
		change(&bad)
		if q.Add(bad) == nil {
			t.Fatalf("accepted invalid routing: %+v", bad)
		}
	}
}
