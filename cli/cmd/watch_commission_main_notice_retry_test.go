package cmd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/dispatch"
)

type mainNoticePermissionProvider struct{ permission error }

func (p *mainNoticePermissionProvider) Check(context.Context) error { return p.permission }
func (p *mainNoticePermissionProvider) Setup(context.Context) error {
	return errors.New("retry must not request permission")
}
func (p *mainNoticePermissionProvider) Show(context.Context, desktopnotify.Message) error {
	return errors.New("main retry must not display a toast")
}

func enableMainNoticeRetryTest(t *testing.T, w *accountWatch) *mainNoticePermissionProvider {
	t.Helper()
	if err := w.enableDesktop(); err != nil {
		t.Fatal(err)
	}
	provider := &mainNoticePermissionProvider{}
	w.desktopProvider = provider
	return provider
}

func TestCommissionMainNoticeRetryReopensAndDeliversOnce(t *testing.T) {
	for _, state := range []string{"pending_payment", "awaiting_buyer_confirmation"} {
		t.Run(state, func(t *testing.T) {
			order := testCommissionOrder()
			order.BuyerID, order.SellerID, order.State = 42, 43, state
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/orders/901" {
					t.Errorf("notice performed a business mutation: %s %s", r.Method, r.URL.Path)
					http.NotFound(out, r)
					return
				}
				materialTestResponse(out, map[string]any{"order": order})
			}))
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			enableMainNoticeRetryTest(t, w)
			queue, err := w.mainNoticeQueue()
			if err != nil {
				t.Fatal(err)
			}
			original := dispatch.MainNotice{ID: "11", OrderID: "901", Version: order.Version, Role: "buyer", State: state}
			if err := queue.Add(original); err != nil {
				t.Fatal(err)
			}
			calls := 0
			w.runMainNotice = func(context.Context, string) (dispatch.Result, error) {
				calls++
				if calls == 1 {
					return dispatch.Result{}, dispatch.ErrMainNoticeBusy
				}
				return dispatch.Result{Text: "Verified order result"}, nil
			}
			now := time.Now()
			item, ok, err := queue.NextAt(now)
			if err != nil || !ok {
				t.Fatal(item, ok, err)
			}
			status, code := w.dispatchCommissionMainNotice(context.Background(), item)
			if status != "retry_pending" || code != "main_notice_target_busy" || item.Attempts != 1 {
				t.Fatal(status, code, item)
			}
			if err := queue.FinishAt(item.ID, status, code, now); err != nil {
				t.Fatal(err)
			}
			queue, err = dispatch.OpenMainNoticeQueue(*w.binding)
			if err != nil {
				t.Fatal(err)
			}
			if err := queue.Add(original); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := queue.NextAt(now.Add(59 * time.Second)); err != nil || ok {
				t.Fatal("duplicate/restart removed retry delay", ok, err)
			}
			item, ok, err = queue.NextAt(now.Add(time.Minute))
			if err != nil || !ok || item.Attempts != 2 {
				t.Fatal(item, ok, err)
			}
			status, code = w.dispatchCommissionMainNotice(context.Background(), item)
			if status != "delivered" || calls != 2 {
				t.Fatal(status, code, calls)
			}
			if err := queue.Finish(item.ID, status, code); err != nil {
				t.Fatal(err)
			}
			if err := queue.Add(original); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := queue.NextAt(now.Add(time.Hour)); err != nil || ok {
				t.Fatal("delivered notification was replayed", ok, err)
			}
			if len(w.journal.Snapshot()) != 0 {
				t.Fatal("notice retries changed business execution")
			}
		})
	}
}

func TestCommissionMainNoticeRetryRequiresPermissionAndCertainNonAdmission(t *testing.T) {
	for _, tt := range []struct {
		name       string
		permission error
		runError   error
		want       string
	}{
		{"busy-enabled", nil, dispatch.ErrMainNoticeBusy, "retry_pending"},
		{"busy-disabled", desktopnotify.ErrPermission, dispatch.ErrMainNoticeBusy, "skipped"},
		{"busy-unknown", desktopnotify.ErrPendingFirstNotification, dispatch.ErrMainNoticeBusy, "skipped"},
		{"uncertain-enabled", nil, errors.New("receipt lost after execution"), "skipped"},
		{"unsupported-enabled", nil, dispatch.ErrMainNoticeUnsupported, "skipped"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
			installCommissionIntakeRules(t, w)
			provider := enableMainNoticeRetryTest(t, w)
			provider.permission = tt.permission
			calls := 0
			w.runMainNotice = func(context.Context, string) (dispatch.Result, error) {
				calls++
				return dispatch.Result{}, tt.runError
			}
			status, _ := w.dispatchCommissionMainNotice(context.Background(), dispatch.MainNotice{ID: "11", OrderID: "901", Version: 3, Role: "buyer", State: "pending_payment", Attempts: 1})
			if status != tt.want || calls != 1 {
				t.Fatal(status, calls)
			}
		})
	}
}

func TestCommissionMainNoticeRetryStopsAfterPermissionRevokedOrOrderChanged(t *testing.T) {
	for _, role := range []string{"buyer", "seller"} {
		for _, revoked := range []bool{false, true} {
			t.Run(role+map[bool]string{false: "/order-changed", true: "/permission-revoked"}[revoked], func(t *testing.T) {
				order := testCommissionOrder()
				if role == "buyer" {
					order.BuyerID, order.SellerID = 42, 43
				}
				order.Version++
				server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
					if revoked || r.Method != http.MethodGet || r.URL.Path != "/api/v1/orders/901" {
						t.Error("revoked or unexpected order request", r.Method, r.URL.Path)
					}
					materialTestResponse(out, map[string]any{"order": order})
				}))
				defer server.Close()
				w := newCommissionWatch(t, server.URL, "commission_order")
				installCommissionIntakeRules(t, w)
				provider := enableMainNoticeRetryTest(t, w)
				if revoked {
					provider.permission = desktopnotify.ErrPermission
				}
				w.runMainNotice = func(context.Context, string) (dispatch.Result, error) {
					t.Fatal("obsolete/revoked notice reached Agent")
					return dispatch.Result{}, nil
				}
				status, code := w.dispatchCommissionMainNotice(context.Background(), dispatch.MainNotice{ID: "11", OrderID: "901", Version: 3, Role: role, State: "pending_payment", Attempts: 2})
				wantCode := "main_notice_order_changed"
				if revoked {
					wantCode = "main_notice_retry_permission_unavailable"
				}
				if status != "skipped" || code != wantCode {
					t.Fatal(status, code)
				}
			})
		}
	}
}
