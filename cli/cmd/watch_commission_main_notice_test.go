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
