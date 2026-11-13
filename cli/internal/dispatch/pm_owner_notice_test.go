package dispatch

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestPMOwnerNoticeDigestCoalescesAndClaimsOnce(t *testing.T) {
	ctx := context.Background()
	b := Binding{Home: t.TempDir(), Scope: "scope", Revision: "rev", AgentID: "owner"}
	save := func(conversation, topic, change string, urgent bool) {
		t.Helper()
		job := Job{Kind: "pm_push", Scope: b.Scope, Revision: b.Revision, Message: &Message{Conversation: conversation, SenderName: "Peer"}}
		if err := SavePMNotice(ctx, b, job, PMOwnerNotice{Topic: topic, Change: change, Summary: change, NeedsAction: urgent}); err != nil {
			t.Fatal(err)
		}
	}
	save("one", "materials", "available", false)
	save("one", "materials", "review-due", true)
	save("one", "materials", "available", false)
	for i := range 6 {
		save(fmt.Sprint(i), "outcome", "resolved", false)
	}
	var wg sync.WaitGroup
	batches := make(chan []PMNoticeEntry, 2)
	for range 2 {
		wg.Go(func() {
			items, err := ClaimPMNotices(ctx, b, "cycle-one", time.Now())
			if err != nil {
				t.Error(err)
			}
			batches <- items
		})
	}
	wg.Wait()
	close(batches)
	total := 0
	for items := range batches {
		total += len(items)
		if len(items) != 0 && (items[0].Notice.Change != "review-due" || items[0].Sender != "Peer") {
			t.Fatalf("wrong priority or stale update: %+v", items)
		}
	}
	if total != 5 {
		t.Fatalf("same cycle claimed %d items", total)
	}
	save("one", "materials", "review-due", true)
	items, err := ClaimPMNotices(ctx, b, "cycle-two", time.Now())
	if err != nil || len(items) != 2 {
		t.Fatalf("backlog or repeat: %+v %v", items, err)
	}
	items, err = ClaimPMNotices(ctx, b, "empty-cycle", time.Now())
	if err != nil || len(items) != 0 {
		t.Fatalf("empty cycle: %+v %v", items, err)
	}
	save("one", "materials", "review-received", false)
	items, err = ClaimPMNotices(ctx, b, "empty-cycle", time.Now())
	if err != nil || len(items) != 0 {
		t.Fatalf("empty cycle consumed a second time: %+v %v", items, err)
	}
	items, err = ClaimPMNotices(ctx, b, "next-cycle", time.Now())
	if err != nil || len(items) != 1 || items[0].Notice.Change != "review-received" {
		t.Fatalf("new change missing: %+v %v", items, err)
	}
}

func TestPMOwnerNoticeCodexRetryRequiresProvenPreSubmission(t *testing.T) {
	ctx := context.Background()
	b := Binding{Home: t.TempDir(), Scope: "scope", Revision: "rev", AgentID: "owner"}
	job := Job{Kind: "pm_push", Scope: b.Scope, Revision: b.Revision, Message: &Message{Conversation: "one"}}
	n := PMOwnerNotice{Topic: "materials", Change: "review", Summary: "Please review the materials."}
	if err := SavePMNotice(ctx, b, job, n); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	items, err := ClaimPMNotices(ctx, b, "", now)
	if err != nil || len(items) != 1 {
		t.Fatalf("first claim: %+v %v", items, err)
	}
	if err := RetryCodexPMNotice(ctx, b, items[0], true, now); err != nil {
		t.Fatal(err)
	}
	items, err = ClaimPMNotices(ctx, b, "", now)
	if err != nil || len(items) != 0 {
		t.Fatalf("busy target retried immediately: %+v %v", items, err)
	}
	items, err = ClaimPMNotices(ctx, b, "", now.Add(time.Minute))
	if err != nil || len(items) != 1 {
		t.Fatalf("busy retry did not resume: %+v %v", items, err)
	}
	if err := SavePMNotice(ctx, b, job, n); err != nil {
		t.Fatal(err)
	}
	items, err = ClaimPMNotices(ctx, b, "", now.Add(time.Hour))
	if err != nil || len(items) != 0 {
		t.Fatalf("uncertain claim replayed: %+v %v", items, err)
	}
}
