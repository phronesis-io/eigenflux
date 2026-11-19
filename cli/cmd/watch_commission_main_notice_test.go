package cmd

import (
	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/maintenance"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCommissionBuyerNoticeCarriesSignedAcceptanceReport(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
	installCommissionIntakeRules(t, w)
	rule, err := os.ReadFile("../../skills/ef-commission/references/main-notice.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"validating", "awaiting_buyer_confirmation"} {
		prompt, err := w.commissionMainNoticePrompt(context.Background(), dispatch.MainNotice{ID: "11", OrderID: "901", Version: 5, Role: "buyer", State: state})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(prompt, string(rule)) {
			t.Fatal("main notice omitted or replaced the signed acceptance instructions")
		}
		for _, required := range []string{"Download and inspect the delivery", "contract compliance", "defects or verification limits", "acceptance recommendation", "Distinguish platform validation from Agent content inspection", "reuse the verified inspection and restate its conclusion", "Do not pay, accept, confirm receipt"} {
			if !strings.Contains(prompt, required) {
				t.Errorf("%s notice missing acceptance boundary %q", state, required)
			}
		}
	}
}

func TestCommissionMainNoticeAllRolesAndFailureNoBusinessWrite(t *testing.T) {
	for _, role := range []string{"buyer", "seller"} {
		for _, state := range []string{"awaiting_seller", "pending_payment", "in_progress", "validating", "awaiting_buyer_confirmation", "completed", "cancelled", "refunded"} {
			t.Run(role+state, func(t *testing.T) {
				w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
				installCommissionIntakeRules(t, w)
				calls := 0
				w.runMainNotice = func(_ context.Context, prompt string) (dispatch.Result, error) {
					calls++
					if !strings.Contains(prompt, `"role":"`+role+`"`) || !strings.Contains(prompt, `"observed_state":"`+state+`"`) || !strings.Contains(prompt, "Do not pay, accept, confirm receipt") || !strings.Contains(prompt, "--homedir") || !strings.Contains(prompt, w.home) {
						t.Error("missing role/state/safety")
					}
					return dispatch.Result{}, errors.New("host unavailable")
				}
				item := dispatch.MainNotice{ID: "11", OrderID: "901", Version: 3, Role: role, State: state}
				status, _ := w.dispatchCommissionMainNotice(context.Background(), item)
				if status != "skipped" || calls != 1 {
					t.Fatalf("status=%s calls=%d", status, calls)
				}
				if len(w.journal.Snapshot()) != 0 {
					t.Fatal("notice changed business execution jobs")
				}
			})
		}
	}
}

func TestCommissionMainNoticeSuccessAndExplicitUnsupportedHost(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
	installCommissionIntakeRules(t, w)
	item := dispatch.MainNotice{ID: "11", OrderID: "901", Version: 3, Role: "buyer", State: "pending_payment"}
	w.runMainNotice = func(context.Context, string) (dispatch.Result, error) {
		return dispatch.Result{Text: "Payment is required"}, nil
	}
	if status, _ := w.dispatchCommissionMainNotice(context.Background(), item); status != "delivered" {
		t.Fatal(status)
	}
	w.runMainNotice = (dispatch.MainNoticeRunner{Target: dispatch.MainNoticeTarget{Host: w.binding.Host, Binding: *w.binding}}).Run
	if status, _ := w.dispatchCommissionMainNotice(context.Background(), item); status != "skipped" {
		t.Fatal("unverified host invoked")
	}
}

func TestCommissionMainNoticeCancelsOnIdentityChange(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
	installCommissionIntakeRules(t, w)
	w.runMainNotice = func(ctx context.Context, _ string) (dispatch.Result, error) {
		cred, err := auth.LoadV2Credentials(w.server.Name)
		if err != nil {
			t.Fatal(err)
		}
		cred.AgentID = "43"
		if err = auth.SaveV2Credentials(w.server.Name, cred); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			return dispatch.Result{}, ctx.Err()
		case <-time.After(3 * time.Second):
			t.Error("identity guard did not cancel")
			return dispatch.Result{}, errors.New("not cancelled")
		}
	}
	status, _ := w.dispatchCommissionMainNotice(context.Background(), dispatch.MainNotice{ID: "11", OrderID: "901", Version: 3, Role: "buyer", State: "pending_payment"})
	if status != "skipped" {
		t.Fatal(status)
	}
}

func TestPublishedModerationQueuesSignedBroadcastRules(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
	w.binding.MainSessionID = "existing-main"
	w.binding.MainCommand = w.binding.Command
	installCommissionIntakeRules(t, w)
	orders, err := w.mainNoticeQueue()
	if err != nil {
		t.Fatal(err)
	}
	if err := orders.Add(dispatch.MainNotice{ID: "80", OrderID: "901", Version: 3, Role: "seller", State: "in_progress"}); err != nil {
		t.Fatal(err)
	}
	w.runMainNotice = func(_ context.Context, _ string) (dispatch.Result, error) {
		return dispatch.Result{}, errors.New("unexpected direct submission")
	}
	for _, event := range []struct {
		id    notificationID
		state string
	}{{81, "review"}, {82, "pass"}, {83, "published"}, {83, "published"}} {
		notice, err := parseModerationNotification(moderationTestNotification("42", event.state), "42")
		if err != nil {
			t.Fatal(err)
		}
		notice.ID = event.id
		if _, err := w.enqueueModerationNotification(notice, func(e maintenance.Event) {
			if err := maintenance.RecordListingBroadcasts(w.maintenanceScope(), []maintenance.Event{e}); err != nil {
				t.Fatal(err)
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Reopen from disk to verify the durable queue, not only the in-memory wake-up.
	orders, err = dispatch.OpenMainNoticeQueue(*w.binding)
	if err != nil {
		t.Fatal(err)
	}
	order, ok, err := orders.Next()
	if err != nil || !ok || order.OrderID != "901" {
		t.Fatalf("existing Order queue no longer readable: %+v, %v", order, err)
	}
	if item, ok, err := orders.Next(); err != nil || ok {
		t.Fatalf("listing leaked into the Order queue: %+v, %v", item, err)
	}
	queue, err := dispatch.OpenListingNoticeQueue(*w.binding)
	if err != nil {
		t.Fatal(err)
	}
	item, ok := queue.Lookup("83")
	if !ok || !item.IsListingPublication() || item.CommissionID != "9007199254740993" || item.DraftVersion != 3 {
		t.Fatalf("wrong queued notice: %+v", item)
	}
	for _, id := range []string{"81", "82"} {
		if _, ok := queue.Lookup(id); ok {
			t.Fatalf("non-published notice %s queued for broadcast", id)
		}
	}
	prompt, err := w.commissionMainNoticePrompt(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"commission_id":"9007199254740993"`, `"draft_version":3`, "public_revision.source_draft_version", "SIGNED BROADCAST REFERENCE", "recurring_publish"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("publication prompt missing %q", expected)
		}
	}
	w.mainNotices, w.listingNotices = orders, queue
	scope := w.maintenanceScope().ForListingBroadcast()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runs := 0
	w.runMainNotice = func(context.Context, string) (dispatch.Result, error) {
		runs++
		state, err := maintenance.Snapshot(scope)
		if err != nil || len(state.Events) != 1 || state.Events[0].Result != "enqueued" {
			t.Fatalf("unexpected handoff telemetry before the runner: %+v %v", state.Events, err)
		}
		return dispatch.Result{}, dispatch.ErrMainNoticeBusy
	}
	w.emit = func(event string, value interface{}) error {
		if event == "commission_main_notice_status" {
			info := value.(map[string]any)
			if info["notification_id"] != item.ID || info["status"] != "retry_pending" || info["code"] != "main_notice_target_busy" {
				t.Errorf("unexpected notice result: %v", info)
			}
			cancel()
		}
		return nil
	}
	if err := w.commissionMainNoticeLoop(ctx); !errors.Is(err, context.Canceled) || runs != 1 {
		t.Fatalf("notice worker did not finish one attempt: runs=%d err=%v", runs, err)
	}
	if receipt, ok := queue.Lookup(item.ID); !ok || receipt.Status != "pending" || receipt.NextAttemptAt <= 0 {
		t.Fatalf("retry was not persisted: %+v", receipt)
	}
	if _, ok, err := queue.Next(); err != nil || ok {
		t.Fatalf("duplicate notice became runnable: %t, %v", ok, err)
	}
	state, err := maintenance.Snapshot(scope)
	if err != nil || len(state.Events) != 2 {
		t.Fatalf("expected enqueue and handoff result only: %+v %v", state.Events, err)
	}
	for i, want := range []struct{ stage, result, reason string }{
		{"queue", "enqueued", ""}, {"handoff", "retry_pending", "main_busy"},
	} {
		event := state.Events[i]
		d := event.ListingBroadcast
		if event.Result != want.result || d == nil || d.Stage != want.stage || d.Reason != want.reason ||
			d.NotificationID != "83" || d.CommissionID != item.CommissionID || d.DraftVersion != 3 {
			t.Fatalf("wrong observation: %+v %+v", event, d)
		}
	}

	completion, err := w.commissionMainNoticePrompt(context.Background(), dispatch.MainNotice{
		ID: "83", OrderID: "901", Version: 9, Role: "buyer", Event: "order.state.changed.v1", State: "completed",
	})
	if err != nil || !strings.Contains(completion, "SIGNED BROADCAST REFERENCE") || !strings.Contains(completion, "completion-<order-id>") {
		t.Fatalf("completion is missing the shared broadcast rules: %v", err)
	}
}

func TestListingHandoffReportsBridgeReasonWithoutChangingOutcome(t *testing.T) {
	for _, tc := range []struct {
		reason string
		runErr error
	}{
		{"main_notice_transport_unverified", dispatch.ErrMainNoticeUnsupported},
		{"main_session_mismatch", dispatch.ErrMainSessionMismatch},
		{"main_unconfirmed", errors.New("no confirmed receipt")},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
			w.binding.MainSessionID, w.binding.MainCommand = "existing-main", w.binding.Command
			installCommissionIntakeRules(t, w)
			queue, err := w.noticeQueue(true)
			if err != nil {
				t.Fatal(err)
			}
			item := dispatch.MainNotice{
				ID: "83", CommissionID: "90", DraftVersion: 3,
				Role: "seller", Event: "commission.moderation.changed.v1", State: "published",
			}
			if err := queue.Add(item); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			calls := 0
			w.runMainNotice = func(context.Context, string) (dispatch.Result, error) {
				calls++
				return dispatch.Result{}, tc.runErr
			}
			w.emit = func(event string, _ interface{}) error {
				if event == "commission_main_notice_status" {
					cancel()
				}
				return nil
			}
			if err := w.commissionMainNoticeLoop(ctx); !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("notice worker ran %d times, err=%v", calls, err)
			}
			if receipt, ok := queue.Lookup(item.ID); !ok || receipt.Status != "skipped" || receipt.Code != "main_notice_unconfirmed" || receipt.NextAttemptAt != 0 {
				t.Fatalf("existing handoff outcome changed: %+v", receipt)
			}
			state, err := maintenance.Snapshot(w.maintenanceScope().ForListingBroadcast())
			if err != nil || len(state.Events) != 1 {
				t.Fatalf("expected one handoff observation: %+v %v", state.Events, err)
			}
			event := state.Events[0]
			if d := event.ListingBroadcast; d == nil || event.Result != "skipped" || d.Stage != "handoff" || d.Reason != tc.reason || d.NotificationID != item.ID {
				t.Fatalf("wrong handoff observation: %+v %+v", event, d)
			}
		})
	}
}

func TestListingHandoffReportsInterruptedRecoveryWithoutReplay(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
	w.binding.MainSessionID, w.binding.MainCommand = "existing-main", w.binding.Command
	queue, err := dispatch.OpenListingNoticeQueue(*w.binding)
	if err != nil {
		t.Fatal(err)
	}
	item := dispatch.MainNotice{
		ID: "83", CommissionID: "90", DraftVersion: 3,
		Role: "seller", Event: "commission.moderation.changed.v1", State: "published",
	}
	if err := queue.Add(item); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := queue.Next(); err != nil || !ok {
		t.Fatalf("could not persist a running notice: %v %v", ok, err)
	}
	// Let startup open and recover the queue, then stop before claiming work.
	w.handoff = newWatchHandoff()
	w.handoff.request("test")
	if err := w.commissionMainNoticeLoop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if receipt, ok := w.listingNotices.Lookup(item.ID); !ok || receipt.Status != "skipped" || receipt.Code != "execution_unconfirmed" || receipt.Attempts != 1 {
		t.Fatalf("existing restart outcome changed: %+v", receipt)
	}
	if _, ok, err := w.listingNotices.Next(); err != nil || ok {
		t.Fatalf("interrupted notice became runnable: %v %v", ok, err)
	}
	scope := w.maintenanceScope().ForListingBroadcast()
	observed := false
	if err := maintenance.Flush(scope, time.Now(), func(events []maintenance.Event) error {
		if len(events) != 1 {
			t.Fatalf("expected one recovered observation: %+v", events)
		}
		event := events[0]
		if d := event.ListingBroadcast; d == nil || event.Result != "skipped" || d.Stage != "handoff" ||
			d.Reason != "execution_unconfirmed" || d.NotificationID != item.ID || d.CommissionID != item.CommissionID || d.DraftVersion != item.DraftVersion {
			t.Fatalf("wrong recovery observation: %+v %+v", event, d)
		}
		observed = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !observed {
		t.Fatal("interrupted handoff observation missing")
	}
	w.listingNotices = nil
	if err := w.commissionMainNoticeLoop(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := maintenance.Snapshot(scope)
	if err != nil || len(state.Events) != 0 {
		t.Fatalf("subsequent startup repeated recovery: %+v %v", state.Events, err)
	}
}
