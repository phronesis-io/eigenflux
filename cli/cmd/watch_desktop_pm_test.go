package cmd

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/desktopqueue"
	"cli.eigenflux.ai/internal/dispatch"
)

func TestDesktopPrivateMessageNeedsUserRecoveredAndNotReplayed(t *testing.T) {
	w := newCommissionWatch(t, "https://example.com", "pm_push")
	if err := w.enableDesktop(); err != nil || w.desktop == nil {
		t.Fatalf("PM-only binding did not enable desktop delivery: %v", err)
	}
	qpath := filepath.Join(w.home, "watch", "desktop-"+w.scope+".json")
	raw, _ := json.Marshal(map[string]any{"messages": []dispatch.Message{{
		ID: "81", Conversation: "901", Sender: "43", Receiver: "42",
		Content: "private revision request https://attacker.example/?token=secret",
	}}})
	if err := w.journal.AddMessages(raw); err != nil {
		t.Fatal(err)
	}
	job, ok, err := w.journal.Next()
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := w.finishDispatch(job, "needs_user", "agent_decision", "main-session", ""); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the decision was saved but before desktop enqueue.
	w.journal, err = dispatch.OpenJournal(*w.binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.enqueueDesktopBlockers(); err != nil {
		t.Fatal(err)
	}
	entry, ok := w.desktop.Next(time.Now())
	if !ok || entry.Message.URL != "https://example.com/dashboard/messages" {
		t.Fatal(entry)
	}
	for _, private := range []string{"private revision", "attacker", "secret", "43", "901"} {
		if strings.Contains(entry.Message.Title+entry.Message.Body+entry.Message.URL, private) {
			t.Fatal("counterparty content leaked into desktop notification")
		}
	}
	if err := w.desktop.Complete(entry.Message.ID, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	w.desktop, err = desktopqueue.Open(qpath, w.scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.enqueueDesktopBlockers(); err != nil {
		t.Fatal(err)
	}
	if pending, submitted := w.desktop.Counts(); pending != 0 || submitted != 1 {
		t.Fatalf("notification replayed after restart: %d/%d", pending, submitted)
	}
	snap := w.journal.Snapshot()
	if len(snap) != 1 || snap[0].Status != "needs_user" || snap[0].ReplyID != "" {
		t.Fatal("desktop delivery changed message handling", snap)
	}
}

func TestDesktopPrivateMessageDoesNotNotifyWithoutSubscriptionOrDecision(t *testing.T) {
	for _, status := range []string{"pending", "running", "no_reply", "replied"} {
		t.Run(status, func(t *testing.T) {
			w := newCommissionWatch(t, "https://example.com", "pm_push")
			if err := w.enableDesktop(); err != nil {
				t.Fatal(err)
			}
			if err := w.journal.AddMessages(json.RawMessage(`{"messages":[{"msg_id":"81","conv_id":"901","sender_id":"43","receiver_id":"42","content":"private"}]}`)); err != nil {
				t.Fatal(err)
			}
			if status != "pending" {
				job, _, _ := w.journal.Next()
				if status != "running" {
					reply := ""
					if status == "replied" {
						reply = "receipt"
					}
					if err := w.journal.Update(job.ID, status, "agent_decision", "session", reply); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := w.enqueueDesktopBlockers(); err != nil {
				t.Fatal(err)
			}
			if pending, submitted := w.desktop.Counts(); pending+submitted != 0 {
				t.Fatal("non-blocking PM notified")
			}
		})
	}
	w := newCommissionWatch(t, "https://example.com", "maintenance_due")
	if err := w.enableDesktop(); err != nil || w.desktop != nil {
		t.Fatal("unsubscribed binding enabled desktop notifications", err)
	}
}

func TestDesktopPrivateMessageLinkAndCopy(t *testing.T) {
	for _, origin := range []string{"https://example.com", "http://localhost:3000"} {
		target, err := desktopConsoleURL(origin, "/dashboard/messages", nil)
		u, _ := url.Parse(target)
		if err != nil || u.Path != "/dashboard/messages" || u.RawQuery != "" {
			t.Fatal(target, err)
		}
	}
	for _, origin := range []string{"javascript:alert(1)", "http://example.com", "https://user:pass@example.com", "https://example.com?token=secret"} {
		if _, err := desktopConsoleURL(origin, "/dashboard/messages", nil); err == nil {
			t.Fatal("unsafe PM target", origin)
		}
	}
	zhTitle, zhBody := desktopPrivateMessageCopy("zh")
	enTitle, enBody := desktopPrivateMessageCopy("en")
	if !strings.Contains(zhTitle, "私信") || !strings.Contains(zhBody, "尚未自动回复") || !strings.Contains(enTitle, "Private message") || !strings.Contains(enBody, "No reply has been sent") {
		t.Fatal("notification copy lost the pending decision boundary")
	}
}
