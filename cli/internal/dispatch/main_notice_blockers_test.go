package dispatch

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestMainNoticeLocalBlockerOnceAcrossRestartAndRetention(t *testing.T) {
	binding := validTestBinding(t)
	q, err := OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	b := CommissionBlocker{ID: strings.Repeat("a", 64), OrderID: "901", Role: "seller", Status: "unknown", Code: "commission_fulfillment_unconfirmed", Kind: "commission_fulfillment", OrderVersion: 3}
	if err = q.SyncLocalBlockers([]CommissionBlocker{b}); err != nil {
		t.Fatal(err)
	}
	item, ok, err := q.Next()
	if err != nil || !ok || !item.MatchesLocalBlocker(binding.Scope, b) || item.JobCode != "execution_unconfirmed" {
		t.Fatalf("invalid local notice: %+v %v", item, err)
	}
	if err = q.Finish(item.ID, "delivered", "main_notice_confirmed"); err != nil {
		t.Fatal(err)
	}
	q, err = OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	// Fill retention with completed server receipts; the current blocker remains pinned.
	for i := 1; i < 1024; i++ {
		q.state.Entries = append(q.state.Entries, MainNotice{ID: fmt.Sprint(i), OrderID: "901", Version: 3, Role: "seller", Status: "delivered"})
	}
	if err = q.Add(MainNotice{ID: "2000", OrderID: "901", Version: 3, Role: "seller"}); err != nil {
		t.Fatal(err)
	}
	if err = q.SyncLocalBlockers([]CommissionBlocker{b}); err != nil {
		t.Fatal(err)
	}
	item, ok, err = q.Next()
	if err != nil || !ok || item.ID != "2000" {
		t.Fatalf("blocker was replayed: %+v %v", item, err)
	}
	if err = q.Finish(item.ID, "skipped", "unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = q.Next(); err != nil || ok {
		t.Fatalf("duplicate blocker: %v %v", ok, err)
	}
	if err = q.SyncLocalBlockers(nil); err != nil {
		t.Fatal(err)
	}
	for _, entry := range q.state.Entries {
		if entry.IsLocalBlocker() {
			t.Fatal("resolved blocker retained")
		}
	}
}

func TestMainNoticeLocalBlockerCrashDoesNotReplay(t *testing.T) {
	binding := validTestBinding(t)
	q, err := OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	b := CommissionBlocker{ID: strings.Repeat("a", 64), OrderID: "901", Role: "seller", Status: "failed", Code: "commission_fulfillment_artifacts_invalid", OrderVersion: 3}
	if err = q.SyncLocalBlockers([]CommissionBlocker{b}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := q.Next(); err != nil || !ok {
		t.Fatal(ok, err)
	}
	q, err = OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.SyncLocalBlockers([]CommissionBlocker{b}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := q.Next(); err != nil || ok {
		t.Fatalf("uncertain call replayed: %v %v", ok, err)
	}
	b.Code = "commission_skill_missing"
	if err = q.SyncLocalBlockers([]CommissionBlocker{b}); err != nil {
		t.Fatal(err)
	}
	item, ok, err := q.Next()
	if err != nil || !ok || !item.MatchesLocalBlocker(binding.Scope, b) {
		t.Fatalf("changed result lost: %+v %v", item, err)
	}
}

func TestMainNoticeCapacityErrorIsTypedAndLeavesPendingEntries(t *testing.T) {
	q, err := OpenMainNoticeQueue(validTestBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 1024; i++ {
		q.state.Entries = append(q.state.Entries, MainNotice{ID: fmt.Sprint(i), OrderID: "901", Version: 3, Role: "buyer", Status: "pending"})
	}
	if err = q.Add(MainNotice{ID: "2000", OrderID: "901", Version: 3, Role: "buyer"}); !errors.Is(err, ErrMainNoticeQueueFull) {
		t.Fatal(err)
	}
	if len(q.state.Entries) != 1024 || q.state.Entries[0].ID != "1" {
		t.Fatal("active notices changed on overflow")
	}
}

func TestMainNoticeBlockerUsesCurrentExecutionVersion(t *testing.T) {
	binding := validTestBinding(t)
	q, err := OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	b := CommissionBlocker{ID: strings.Repeat("a", 64), OrderID: "901", Role: "seller", Status: "failed", Code: "commission_skill_missing", OrderVersion: 3}
	if err = q.SyncLocalBlockers([]CommissionBlocker{b}); err != nil {
		t.Fatal(err)
	}
	first, ok, err := q.Next()
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = q.Finish(first.ID, "delivered", "main_notice_confirmed"); err != nil {
		t.Fatal(err)
	}
	b.OrderVersion++
	if err = q.SyncLocalBlockers([]CommissionBlocker{b}); err != nil {
		t.Fatal(err)
	}
	current, ok, err := q.Next()
	if err != nil || !ok || current.ID == first.ID || !current.MatchesLocalBlocker(binding.Scope, b) {
		t.Fatalf("new execution version lost: %+v %v", current, err)
	}
}
