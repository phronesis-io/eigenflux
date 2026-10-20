package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/dispatch"
)

func TestMainNoticeOverflowPreservesBusinessDesktopAndACK(t *testing.T) {
	acks := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/notifications/ack" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(out, r)
			return
		}
		acks++
		materialTestResponse(out, map[string]any{"acknowledged": 1})
	}))
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	if err := w.enableDesktop(); err != nil {
		t.Fatal(err)
	}
	entries := make([]dispatch.MainNotice, 1024)
	for i := range entries {
		entries[i] = dispatch.MainNotice{ID: fmt.Sprint(i + 1), OrderID: "901", Version: 3, Role: "buyer", Status: "pending"}
	}
	path := filepath.Join(w.home, "watch", w.scope+"."+w.binding.Revision+".main-notices.json")
	if err := dispatch.WriteJSON(path, map[string]any{"scope": w.scope, "binding_revision": w.binding.Revision, "entries": entries}); err != nil {
		t.Fatal(err)
	}
	statusEvents := 0
	w.emit = func(kind string, data interface{}) error {
		if kind == "commission_main_notice_status" {
			statusEvents++
		}
		return nil
	}
	raw := commissionTestNotification(2000, 901, "42")
	if err := w.deliverCommissionNotifications(context.Background(), commissionTestPage(raw), "socket"); err != nil {
		t.Fatal(err)
	}
	if acks != 1 || len(w.journal.Snapshot()) != 1 || statusEvents != 1 {
		t.Fatalf("overflow blocked business: ACK=%d jobs=%d skipped=%d", acks, len(w.journal.Snapshot()), statusEvents)
	}
	if pending, _ := w.desktop.Counts(); pending != 1 {
		t.Fatalf("desktop notice lost: %d", pending)
	}
}

func TestLocalBlockerMainNoticeChecksCurrentJobAndOrder(t *testing.T) {
	for _, scenario := range []string{"current", "advanced_execution", "resolved", "new_version", "terminal", "read_failed"} {
		t.Run(scenario, func(t *testing.T) {
			order := paidCommissionTestOrder()
			if scenario == "advanced_execution" {
				order.Version++
			}
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/orders/901" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.NotFound(out, r)
					return
				}
				if scenario == "read_failed" {
					http.Error(out, "unavailable", 503)
					return
				}
				current := order
				if scenario == "new_version" {
					current.Version++
				}
				if scenario == "terminal" {
					current.State = "completed"
				}
				materialTestResponse(out, map[string]any{"order": current})
			}))
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			if err := w.journal.AddCommissionNotification(commissionTestNotification(81, 901, "42")); err != nil {
				t.Fatal(err)
			}
			job, ok, err := w.journal.NextKind("commission_order")
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			if scenario == "advanced_execution" {
				if err = w.journal.SetCommissionExecution(job.ID, "901", order.Version, 0); err != nil {
					t.Fatal(err)
				}
			}
			if err = w.journal.Update(job.ID, "failed", "commission_skill_missing", "", ""); err != nil {
				t.Fatal(err)
			}
			blockers, err := w.journal.CommissionBlockers()
			if err != nil || len(blockers) != 1 {
				t.Fatal(blockers, err)
			}
			q, err := w.mainNoticeQueue()
			if err != nil {
				t.Fatal(err)
			}
			if err = q.SyncLocalBlockers(blockers); err != nil {
				t.Fatal(err)
			}
			item, ok, err := q.Next()
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			if scenario == "resolved" {
				if err = w.journal.Reconcile(job.ID, "completed", ""); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			w.runMainNotice = func(_ context.Context, prompt string) (dispatch.Result, error) {
				calls++
				if !strings.Contains(prompt, `"event_kind":"order.execution.blocked.local.v1"`) || !strings.Contains(prompt, `"local_execution"`) || !strings.Contains(prompt, `"code":"skill_missing"`) {
					t.Error("missing verified local result")
				}
				var payload map[string]any
				_, data, found := strings.Cut(prompt, "\n\nUNTRUSTED COMMISSION EVENT:\n")
				if !found || json.Unmarshal([]byte(data), &payload) != nil {
					t.Error("invalid event")
				}
				return dispatch.Result{Text: "The order is blocked by a missing skill."}, nil
			}
			status, code := w.dispatchCommissionMainNotice(context.Background(), item)
			if scenario == "current" || scenario == "advanced_execution" {
				if status != "delivered" || calls != 1 {
					t.Fatalf("current blocker missing: %s %s calls=%d", status, code, calls)
				}
			} else if status != "skipped" || calls != 0 {
				t.Fatalf("stale blocker invoked: %s %s calls=%d", status, code, calls)
			}
		})
	}
}
