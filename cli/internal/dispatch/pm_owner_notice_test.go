package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func testPMDigest(ctx context.Context, b Binding, cycle string) ([]PMNoticeEntry, error) {
	var batch []PMNoticeEntry
	err := DeliverPMDigest(ctx, b, cycle, func(entries []PMNoticeEntry) error {
		batch = entries
		return nil
	})
	return batch, err
}

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
	writeErr := errors.New("broken output")
	err := DeliverPMDigest(ctx, b, "failed-cycle", func(batch []PMNoticeEntry) error {
		if len(batch) != 5 {
			t.Fatalf("first delivery: %d items", len(batch))
		}
		return writeErr
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("output failure: %v", err)
	}
	var wg sync.WaitGroup
	batches := make(chan []PMNoticeEntry, 2)
	for range 2 {
		wg.Go(func() {
			items, err := testPMDigest(ctx, b, "cycle-one")
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
	items, err := testPMDigest(ctx, b, "cycle-two")
	if err != nil || len(items) != 2 {
		t.Fatalf("backlog or repeat: %+v %v", items, err)
	}
	items, err = testPMDigest(ctx, b, "empty-cycle")
	if err != nil || len(items) != 0 {
		t.Fatalf("empty cycle: %+v %v", items, err)
	}
	save("one", "materials", "review-received", false)
	items, err = testPMDigest(ctx, b, "empty-cycle")
	if err != nil || len(items) != 0 {
		t.Fatalf("empty cycle consumed a second time: %+v %v", items, err)
	}
	items, err = testPMDigest(ctx, b, "next-cycle")
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
	entry, found, err := NextPMNotice(ctx, b, now)
	if err != nil || !found {
		t.Fatalf("first lookup: %+v %v", entry, err)
	}
	if err := RetryCodexPMNotice(ctx, b, entry, true, now); err != nil {
		t.Fatal(err)
	}
	entry, found, err = NextPMNotice(ctx, b, now)
	if err != nil || found {
		t.Fatalf("busy target retried immediately: %+v %v", entry, err)
	}
	entry, found, err = NextPMNotice(ctx, b, now.Add(time.Minute))
	if err != nil || !found {
		t.Fatalf("busy retry did not resume: %+v %v", entry, err)
	}
	if err := CommitPMNotice(ctx, b, entry.ID); err != nil {
		t.Fatal(err)
	}
	if err := SavePMNotice(ctx, b, job, n); err != nil {
		t.Fatal(err)
	}
	entry, found, err = NextPMNotice(ctx, b, now.Add(time.Hour))
	if err != nil || found {
		t.Fatalf("uncertain submission replayed: %+v %v", entry, err)
	}
}

func TestDesktopPMDigestDoesNotReplayAfterSubmission(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("uncertain=%t", uncertain), func(t *testing.T) {
			ctx := context.Background()
			b := Binding{Home: t.TempDir(), Scope: "scope", Revision: "rev", AgentID: "owner"}
			job := Job{Kind: "pm_push", Scope: b.Scope, Revision: b.Revision, Message: &Message{Conversation: "one"}}
			if err := SavePMNotice(ctx, b, job, PMOwnerNotice{Topic: "materials", Change: "ready", Summary: "Ready"}); err != nil {
				t.Fatal(err)
			}
			brokenOutput := errors.New("broken stdout")
			shows := 0
			show := func(entries []PMNoticeEntry) (bool, error) {
				shows++
				if len(entries) != 1 {
					t.Fatalf("unexpected batch: %+v", entries)
				}
				if uncertain {
					return false, context.DeadlineExceeded
				}
				return true, nil
			}
			err := DeliverDesktopPMDigest(ctx, b, "first", func([]PMNoticeEntry) error { return brokenOutput }, show)
			want := brokenOutput
			if uncertain {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatalf("handoff error = %v, want %v", err, want)
			}
			for _, cycle := range []string{"first", "next"} {
				if err := DeliverDesktopPMDigest(ctx, b, cycle, func(entries []PMNoticeEntry) error {
					if len(entries) != 0 {
						t.Fatalf("repeated using host delivery: %+v", entries)
					}
					return nil
				}, show); err != nil {
					t.Fatal(err)
				}
			}
			if shows != 1 {
				t.Fatalf("notification was submitted %d times", shows)
			}
			if err := withPMNotices(ctx, b, func(state *pmNoticeState) (bool, error) {
				if (len(state.DesktopUnconfirmed) != 0) != uncertain {
					t.Fatalf("wrong unconfirmed state: %+v", state.DesktopUnconfirmed)
				}
				return false, nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDesktopPMDigestKnownRejectionPreservesHostRetry(t *testing.T) {
	ctx := context.Background()
	b := Binding{Home: t.TempDir(), Scope: "scope", Revision: "rev", AgentID: "owner"}
	job := Job{Kind: "pm_push", Scope: b.Scope, Revision: b.Revision, Message: &Message{Conversation: "one"}}
	if err := SavePMNotice(ctx, b, job, PMOwnerNotice{Topic: "materials", Change: "ready", Summary: "Ready"}); err != nil {
		t.Fatal(err)
	}
	brokenOutput := errors.New("broken stdout")
	err := DeliverDesktopPMDigest(ctx, b, "first", func(entries []PMNoticeEntry) error {
		if len(entries) != 1 {
			t.Fatalf("missing host fallback: %+v", entries)
		}
		return brokenOutput
	}, func([]PMNoticeEntry) (bool, error) { return false, nil })
	if !errors.Is(err, brokenOutput) {
		t.Fatalf("host error: %v", err)
	}
	entries, err := testPMDigest(ctx, b, "next")
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed host handoff lost reminder: %+v %v", entries, err)
	}
}

func TestPMOwnerNoticeHandoffFailureKeepsPendingNotice(t *testing.T) {
	ctx := context.Background()
	b := Binding{Home: t.TempDir(), Scope: "scope", Revision: "rev", AgentID: "owner"}
	job := Job{Kind: "pm_push", Scope: b.Scope, Revision: b.Revision, Message: &Message{Conversation: "one"}}
	if err := SavePMNotice(ctx, b, job, PMOwnerNotice{Topic: "materials", Change: "ready", Summary: "Ready"}); err != nil {
		t.Fatal(err)
	}
	entry, found, err := NextPMNotice(ctx, b, time.Now())
	if err != nil || !found {
		t.Fatalf("missing pending notice: %v", err)
	}
	queueErr := errors.New("desktop queue write interrupted")
	if err := HandoffPMNotice(ctx, b, entry.ID, func() error { return queueErr }); !errors.Is(err, queueErr) {
		t.Fatalf("queue failure: %v", err)
	}
	recovered, found, err := NextPMNotice(ctx, b, time.Now())
	if err != nil || !found || recovered.ID != entry.ID {
		t.Fatalf("failed enqueue lost notice: %+v %v", recovered, err)
	}
}
