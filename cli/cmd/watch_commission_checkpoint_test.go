package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
)

func TestCommissionVerifiedCheckpointResumesWithoutAgent(t *testing.T) {
	for _, scenario := range []string{"resume", "changed_bytes", "missing_file", "symlink_directory", "order_changed", "inputs_changed", "identity_changed", "binding_changed"} {
		t.Run(scenario, func(t *testing.T) {
			var current atomic.Pointer[commissionIntakeOrder]
			order := paidCommissionTestOrder()
			current.Store(&order)
			server := commissionFulfillmentServer(t, func() commissionIntakeOrder { return *current.Load() }, true)
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			job := claimPaidCommission(t, w)
			credentials, err := auth.LoadV2Credentials(w.server.Name)
			if err != nil {
				t.Fatal(err)
			}
			var calls int
			w.runAgent = func(ctx context.Context, request dispatch.Request) (dispatch.Result, error) {
				calls++
				data := fulfillmentTestPayload(t, request)
				if err := os.WriteFile(filepath.Join(data.OutputDirectory, "report.txt"), []byte("Verified report\n"), 0600); err != nil {
					t.Fatal(err)
				}
				// A local read failure after generation must not discard evidence or
				// imply another account was selected.
				if err := auth.DeleteV2Credentials(w.server.Name); err != nil {
					t.Fatal(err)
				}
				decision := dispatch.CommissionFulfillmentDecision{Version: 1, RequestID: job.ID, OrderID: "901", OrderVersion: 3, Outcome: "artifacts_ready", Summary: "Report generated", SelfCheck: "Read actual report", Artifacts: []dispatch.CommissionArtifact{{LogicalPath: "outputs/report.txt", RelativePath: "report.txt"}}}
				raw, _ := json.Marshal(decision)
				return dispatch.Result{Text: string(raw), SessionID: "generated-once"}, nil
			}
			if err := w.dispatchCommissionFulfillment(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if err := auth.SaveV2Credentials(w.server.Name, credentials); err != nil {
				t.Fatal(err)
			}
			// Reopen so the test exercises durable evidence, not just memory.
			w.journal, err = dispatch.OpenJournal(*w.binding)
			if err != nil {
				t.Fatal(err)
			}
			var stopped dispatch.Job
			for _, candidate := range w.journal.Snapshot() {
				if candidate.ID == job.ID {
					stopped = candidate
				}
			}
			if stopped.Status != "failed" || stopped.Code != "commission_local_state_missing" || stopped.CommissionCheckpoint == nil || stopped.CommissionFulfillment != nil {
				t.Fatalf("verified pre-upload evidence lost or mislabeled: %+v", stopped)
			}
			if err := w.journal.Retry(job.ID); err != nil {
				t.Fatal(err)
			}
			job, ok, err := w.journal.NextKind("commission_fulfillment")
			if err != nil || !ok || job.CommissionCheckpoint == nil {
				t.Fatal("retry did not preserve checkpoint", err)
			}
			switch scenario {
			case "changed_bytes":
				err = os.WriteFile(filepath.Join(job.CommissionDirectory, "report.txt"), []byte("Tampered report\n"), 0600)
			case "missing_file":
				err = os.Remove(filepath.Join(job.CommissionDirectory, "report.txt"))
			case "symlink_directory":
				err = os.Rename(job.CommissionDirectory, job.CommissionDirectory+"-moved")
				if err == nil {
					err = os.Symlink(job.CommissionDirectory+"-moved", job.CommissionDirectory)
					if errors.Is(err, os.ErrPermission) {
						t.Skip("symlink creation is unavailable for this test user")
					}
				}
			case "order_changed":
				changed := order
				changed.Version++
				current.Store(&changed)
			case "inputs_changed":
				changed := order
				changed.BuyerInput = "Different buyer request"
				current.Store(&changed)
			case "identity_changed":
				credentials.AgentID = "43"
				err = auth.SaveV2Credentials(w.server.Name, credentials)
			case "binding_changed":
				changed := *w.binding
				changed.MainSessionID = "another-session"
				err = dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), changed)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = w.dispatchCommissionFulfillment(context.Background(), job)
			if scenario == "identity_changed" && !errors.Is(err, errWatchIdentity) || scenario == "binding_changed" && !errors.Is(err, errWatchConfiguration) {
				t.Fatalf("changed local owner was not rejected: %v", err)
			}
			if err != nil && scenario != "identity_changed" && scenario != "binding_changed" {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("checkpoint reran model %d times", calls)
			}
			var final dispatch.Job
			for _, candidate := range w.journal.Snapshot() {
				if candidate.ID == job.ID {
					final = candidate
				}
			}
			if scenario == "resume" {
				if final.Status != "completed" || final.Code != "commission_delivery_confirmed" || final.CommissionCheckpoint != nil || final.CommissionFulfillment == nil || final.CommissionDirectory != stopped.CommissionDirectory || final.SessionID != "generated-once" {
					t.Fatalf("checkpoint did not deliver original bytes: %+v", final)
				}
			} else if final.Status == "completed" || final.CommissionFulfillment != nil || final.CommissionCheckpoint == nil {
				t.Fatalf("unsafe checkpoint was uploaded: %+v", final)
			}
		})
	}
}

func TestCommissionLocalCheckClassification(t *testing.T) {
	for _, test := range []struct {
		err  error
		code string
	}{
		{errWatchIdentity, "identity_changed"},
		{errWatchConfiguration, "configuration_changed"},
		{context.Canceled, "commission_local_check_cancelled"},
		{context.DeadlineExceeded, "commission_local_check_cancelled"},
		{&os.PathError{Op: "read", Path: "private", Err: os.ErrPermission}, "commission_local_permission_denied"},
		{&os.PathError{Op: "read", Path: "private", Err: os.ErrNotExist}, "commission_local_state_missing"},
		{&os.PathError{Op: "read", Path: "private", Err: errors.New("read failure")}, "commission_local_io_unavailable"},
		{errors.New("invalid local state"), "commission_local_check_unavailable"},
	} {
		if code := commissionLocalCheckCode(test.err); code != test.code {
			t.Fatalf("want %s, got %s", test.code, code)
		}
	}
}

func TestCommissionLocalReadRetryIsBoundedAndCancellable(t *testing.T) {
	server := commissionFulfillmentServer(t, paidCommissionTestOrder)
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	bindingPath := dispatch.BindingPath(w.home, w.server.Name)
	if err := os.Rename(bindingPath, bindingPath+"-temporarily-unavailable"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := w.commissionFulfillmentIdentityOK(context.Background()); !errors.Is(err, os.ErrNotExist) || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("local read failure was not bounded/retried: %v %s", err, time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.commissionFulfillmentIdentityOK(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read was retried: %v", err)
	}
	// The same guard can recover a short read outage without rerunning work.
	restored := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		restored <- os.Rename(bindingPath+"-temporarily-unavailable", bindingPath)
	}()
	err := w.commissionFulfillmentIdentityOK(context.Background())
	if restoreErr := <-restored; restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if err != nil {
		t.Fatalf("short local read outage was not recovered: %v", err)
	}
	changed := *w.binding
	changed.MainSessionID = "another-owner"
	if err := dispatch.WriteJSON(bindingPath, changed); err != nil {
		t.Fatal(err)
	}
	if err := w.commissionFulfillmentIdentityOK(context.Background()); !errors.Is(err, errWatchConfiguration) {
		t.Fatalf("positive configuration change was not rejected: %v", err)
	}
}
