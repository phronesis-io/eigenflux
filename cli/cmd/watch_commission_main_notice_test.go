package cmd

import (
	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
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
		prompt, err := w.commissionMainNoticePrompt(dispatch.MainNotice{ID: "11", OrderID: "901", Version: 5, Role: "buyer", State: state})
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
	installCommissionIntakeRules(t, w)
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
		if _, err := w.enqueueModerationNotification(notice); err != nil {
			t.Fatal(err)
		}
	}
	// Reopen from disk to verify the durable queue, not only the in-memory wake-up.
	queue, err := dispatch.OpenMainNoticeQueue(*w.binding)
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err := queue.Next()
	if err != nil || !ok || !item.IsListingPublication() || item.CommissionID != "9007199254740993" || item.DraftVersion != 3 {
		t.Fatalf("wrong queued notice: %+v, %v", item, err)
	}
	prompt, err := w.commissionMainNoticePrompt(item)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"commission_id":"9007199254740993"`, `"draft_version":3`, "public_revision.source_draft_version", "SIGNED BROADCAST REFERENCE", "recurring_publish"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("publication prompt missing %q", expected)
		}
	}
	if _, ok, err := queue.Next(); err != nil || ok {
		t.Fatalf("unpublished or duplicate notice queued: %t, %v", ok, err)
	}

	completion, err := w.commissionMainNoticePrompt(dispatch.MainNotice{
		ID: "83", OrderID: "901", Version: 9, Role: "buyer", Event: "order.state.changed.v1", State: "completed",
	})
	if err != nil || !strings.Contains(completion, "SIGNED BROADCAST REFERENCE") || !strings.Contains(completion, "completion-<order-id>") {
		t.Fatalf("completion is missing the shared broadcast rules: %v", err)
	}
}
