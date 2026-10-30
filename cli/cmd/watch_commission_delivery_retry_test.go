package cmd

import (
	"bytes"
	"cli.eigenflux.ai/internal/auth"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
)

func dueCommissionDelivery(t *testing.T, w *accountWatch, jobID string) dispatch.Job {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(w.home, "watch", "dispatch-*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("fixture journal path: %v %v", paths, err)
	}
	body, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Version  int               `json:"version"`
		Scope    string            `json:"scope"`
		Revision string            `json:"binding_revision"`
		AgentID  string            `json:"agent_id"`
		Sequence uint64            `json:"sequence"`
		Jobs     []dispatch.Job    `json:"jobs"`
		Sessions map[string]string `json:"sessions"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range state.Jobs {
		job := &state.Jobs[i]
		if job.ID == jobID {
			if job.Status != "pending" || job.CommissionDelivery == nil {
				t.Fatalf("not scheduled: %+v", job)
			}
			job.CommissionDelivery.NextAttemptAt = time.Now().Add(-time.Second).UnixMilli()
			found = true
		}
	}
	if !found {
		t.Fatal("missing scheduled job")
	}
	if err := dispatch.WriteJSON(paths[0], state); err != nil {
		t.Fatal(err)
	}
	w.journal, err = dispatch.OpenJournal(*w.binding)
	if err != nil {
		t.Fatal(err)
	}
	job, ok, err := w.journal.NextKind("commission_fulfillment")
	if err != nil || !ok || job.ID != jobID {
		t.Fatalf("due claim %v %v %+v", ok, err, job)
	}
	return job
}

type deliveryRetryFixture struct {
	mu                                                              sync.Mutex
	order                                                           commissionIntakeOrder
	mode                                                            string
	beginCalls, putCalls, confirmCalls, deliverCalls, downloadCalls int
	admitted, delivered                                             bool
	body                                                            []byte
	seen                                                            map[string][]byte
	keys                                                            map[string]string
}

func newDeliveryRetryFixture(t *testing.T, mode string) (*deliveryRetryFixture, *httptest.Server) {
	t.Helper()
	f := &deliveryRetryFixture{order: paidCommissionTestOrder(), mode: mode, seen: map[string][]byte{}, keys: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == "POST" {
			body, _ := io.ReadAll(r.Body)
			if old, ok := f.seen[r.URL.Path]; ok && !bytes.Equal(old, body) {
				t.Errorf("changed mutation body %s: %s vs %s", r.URL.Path, old, body)
			}
			f.seen[r.URL.Path] = body
			key := r.Header.Get(idempotencyHeader)
			if key == "" {
				t.Error("missing mutation key")
			}
			if old, ok := f.keys[r.URL.Path]; ok && old != key {
				t.Errorf("changed mutation key %s", r.URL.Path)
			}
			f.keys[r.URL.Path] = key
		}
		switch r.URL.Path {
		case "/api/v1/orders/901":
			order := f.order
			if f.delivered {
				order.State = "awaiting_buyer_confirmation"
				order.Version++
				order.SnapshotID++
			}
			materialTestResponse(out, map[string]any{"order": order})
		case "/api/v2/console/trade/orders/901":
			materialTestResponse(out, map[string]any{"order_id": "901", "role": "seller", "version": f.order.Version, "state": f.order.State, "counterparty": map[string]string{"agent_id": "41"}, "files": map[string]any{"input": []any{}}})
		case "/api/v1/orders/901/uploads":
			f.beginCalls++
			if mode == "always_unavailable" || mode == "lost_begin" && f.beginCalls == 1 {
				http.Error(out, "temporary", 503)
				return
			}
			if mode == "permission" {
				http.Error(out, "denied", 403)
				return
			}
			grant := map[string]any{"object_id": 903}
			if !f.admitted {
				grant["method"] = "PUT"
				grant["url"] = "http://" + r.Host + "/storage"
			}
			if mode == "expired_grant" {
				grant["expires_at"] = time.Now().Add(-time.Minute).UnixMilli()
			}
			materialTestResponse(out, map[string]any{"grant": grant})
		case "/storage":
			f.putCalls++
			if r.Header.Get("Authorization") != "" {
				t.Error("API token forwarded to storage")
			}
			body, _ := io.ReadAll(r.Body)
			if len(f.body) > 0 && !bytes.Equal(f.body, body) {
				t.Error("retry uploaded different artifact bytes")
			}
			f.body = body
			if mode == "temporary_storage" && f.putCalls == 1 {
				http.Error(out, "temporary", 503)
				return
			}
			out.WriteHeader(200)
		case "/api/v1/orders/901/uploads/confirm":
			f.confirmCalls++
			f.admitted = true
			if mode == "lost_confirm" && f.confirmCalls == 1 {
				http.Error(out, "response lost", 503)
				return
			}
			materialTestResponse(out, map[string]any{})
		case "/api/v1/orders/901/deliver":
			f.deliverCalls++
			if !f.admitted {
				t.Error("delivery before file admission")
			}
			f.delivered = true
			if mode == "lost_deliver" && f.deliverCalls == 1 {
				http.Error(out, "response lost", 503)
				return
			}
			materialTestResponse(out, map[string]any{})
		case "/download-output":
			f.downloadCalls++
			if mode == "temporary_download" && f.downloadCalls == 1 {
				http.Error(out, "temporary", 503)
				return
			}
			if r.Header.Get("Authorization") != "" {
				t.Error("API token forwarded to download storage")
			}
			body := f.body
			if f.mode == "remote_mismatch" {
				body = []byte("wrong report")
			}
			out.Write(body)
		default:
			if strings.Contains(r.URL.Path, "/snapshots/") && strings.HasSuffix(r.URL.Path, "/download") {
				if !f.delivered {
					http.NotFound(out, r)
					return
				}
				if r.URL.Query().Get("path") != "outputs/report.txt" {
					t.Error("unexpected output path")
				}
				materialTestResponse(out, map[string]any{"grant": map[string]any{"method": "GET", "url": "http://" + r.Host + "/download-output"}})
				return
			}
			t.Errorf("unexpected delivery fixture request %s %s", r.Method, r.URL.Path)
			http.NotFound(out, r)
		}
	}))
	return f, server
}

func generateDeliveryOnce(t *testing.T, w *accountWatch) (dispatch.Job, *int) {
	t.Helper()
	installCommissionIntakeRules(t, w)
	job := claimPaidCommission(t, w)
	calls := new(int)
	w.runAgent = func(ctx context.Context, request dispatch.Request) (dispatch.Result, error) {
		*calls++
		data := fulfillmentTestPayload(t, request)
		if err := os.WriteFile(filepath.Join(data.OutputDirectory, "report.txt"), []byte("Verified report\n"), 0600); err != nil {
			t.Fatal(err)
		}
		decision := dispatch.CommissionFulfillmentDecision{Version: 1, RequestID: job.ID, OrderID: "901", OrderVersion: 3, Outcome: "artifacts_ready", Summary: "Generated report", SelfCheck: "Read bytes", Artifacts: []dispatch.CommissionArtifact{{LogicalPath: "outputs/report.txt", RelativePath: "report.txt"}}}
		raw, _ := json.Marshal(decision)
		return dispatch.Result{Text: string(raw), SessionID: "generated-once"}, nil
	}
	return job, calls
}

func TestCommissionDeliveryRetriesSameKeysAndBytesWithoutAgent(t *testing.T) {
	for _, mode := range []string{"lost_begin", "lost_confirm", "lost_deliver", "temporary_storage", "temporary_download"} {
		t.Run(mode, func(t *testing.T) {
			f, server := newDeliveryRetryFixture(t, mode)
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			job, calls := generateDeliveryOnce(t, w)
			if err := w.dispatchCommissionFulfillment(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			stopped := w.commissionDeliveryJob(job)
			if stopped.Status != "pending" || stopped.CommissionDelivery == nil || stopped.CommissionDelivery.Attempts != 1 || stopped.CommissionDelivery.NextAttemptAt <= time.Now().UnixMilli() {
				t.Fatalf("failure was not scheduled: %+v", stopped)
			}
			// OpenJournal preserves pending schedules, counts and exact evidence.
			var err error
			w.journal, err = dispatch.OpenJournal(*w.binding)
			if err != nil {
				t.Fatal(err)
			}
			reopened := w.commissionDeliveryJob(job)
			if reopened.CommissionDelivery.NextAttemptAt != stopped.CommissionDelivery.NextAttemptAt || reopened.CommissionDirectory != stopped.CommissionDirectory {
				t.Fatal("restart reset delivery schedule or artifacts")
			}
			if _, ok, err := w.journal.NextKind("commission_fulfillment"); ok || err != nil {
				t.Fatal("waiting job claimed early", err)
			}
			job = dueCommissionDelivery(t, w, job.ID)
			if err := w.dispatchCommissionFulfillment(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			finished := w.commissionDeliveryJob(job)
			if finished.Status != "completed" || finished.Code != "commission_delivery_confirmed" || finished.CommissionDelivery.Attempts != 2 || finished.CommissionDirectory != stopped.CommissionDirectory || *calls != 1 {
				t.Fatalf("failed idempotent recovery calls=%d job=%+v", *calls, finished)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.deliverCalls != 1 || mode == "lost_confirm" && f.putCalls != 1 || mode == "lost_deliver" && f.beginCalls != 1 {
				t.Fatalf("duplicated finished work begin=%d put=%d confirm=%d deliver=%d", f.beginCalls, f.putCalls, f.confirmCalls, f.deliverCalls)
			}
			if f.downloadCalls == 0 {
				t.Fatal("delivery accepted without remote artifact proof")
			}
		})
	}
}

func TestCommissionDeliveryRetryStopsAtFiveAcrossRestarts(t *testing.T) {
	f, server := newDeliveryRetryFixture(t, "always_unavailable")
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	job, calls := generateDeliveryOnce(t, w)
	for attempt := 1; attempt <= dispatch.CommissionDeliveryMaxAttempts; attempt++ {
		if err := w.dispatchCommissionFulfillment(context.Background(), job); err != nil {
			t.Fatal(err)
		}
		stopped := w.commissionDeliveryJob(job)
		if stopped.CommissionDelivery.Attempts != attempt {
			t.Fatalf("attempt counter reset: %+v", stopped)
		}
		if attempt < dispatch.CommissionDeliveryMaxAttempts {
			job = dueCommissionDelivery(t, w, job.ID)
		}
	}
	stopped := w.commissionDeliveryJob(job)
	if stopped.Status != "unknown" || stopped.Code != "commission_delivery_retry_exhausted" || stopped.CommissionDelivery.NextAttemptAt != 0 || *calls != 1 {
		t.Fatalf("retry limit failed: %+v calls=%d", stopped, *calls)
	}
	var err error
	w.journal, err = dispatch.OpenJournal(*w.binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := w.journal.NextKind("commission_fulfillment"); ok || err != nil {
		t.Fatal("exhausted job ran again", err)
	}
	if err := w.journal.Retry(job.ID); err == nil {
		t.Fatal("explicit retry reset bounded/uncertain delivery")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beginCalls != 5 || f.deliverCalls != 0 {
		t.Fatalf("unexpected writes %d %d", f.beginCalls, f.deliverCalls)
	}
}

func TestCommissionDeliveryRetryPermanentChangesStop(t *testing.T) {
	for _, scenario := range []string{"permission", "expired_grant", "cancelled", "refunded", "changed_bytes", "identity_changed", "configuration_changed", "inputs_changed", "remote_mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			mode := "lost_confirm"
			if scenario == "permission" || scenario == "expired_grant" {
				mode = scenario
			}
			if scenario == "remote_mismatch" {
				mode = "lost_deliver"
			}
			f, server := newDeliveryRetryFixture(t, mode)
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			job, calls := generateDeliveryOnce(t, w)
			if err := w.dispatchCommissionFulfillment(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if scenario != "permission" && scenario != "expired_grant" {
				job = dueCommissionDelivery(t, w, job.ID)
				f.mu.Lock()
				switch scenario {
				case "cancelled", "refunded":
					f.order.State = scenario
					f.order.Version++
				case "inputs_changed":
					f.order.BuyerInput = "different contract input"
				case "remote_mismatch":
					f.mode = "remote_mismatch"
				}
				f.mu.Unlock()
				if scenario == "changed_bytes" {
					if err := os.WriteFile(filepath.Join(job.CommissionDirectory, "report.txt"), []byte("tampered"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "identity_changed" {
					c, err := auth.LoadV2Credentials(w.server.Name)
					if err != nil {
						t.Fatal(err)
					}
					c.AgentID = "43"
					if err := auth.SaveV2Credentials(w.server.Name, c); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "configuration_changed" {
					b := *w.binding
					b.MainSessionID = "another-owner"
					if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), b); err != nil {
						t.Fatal(err)
					}
				}
				err := w.dispatchCommissionFulfillment(context.Background(), job)
				if err != nil && scenario != "identity_changed" && scenario != "configuration_changed" {
					t.Fatal(err)
				}
			}
			stopped := w.commissionDeliveryJob(job)
			if stopped.Status == "pending" || stopped.Status == "completed" || *calls != 1 {
				t.Fatalf("unsafe delivery continued %+v calls=%d", stopped, *calls)
			}
			if _, ok, err := w.journal.NextKind("commission_fulfillment"); ok || err != nil {
				t.Fatal("permanent stop remained automatically runnable", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.beginCalls != 1 {
				t.Fatalf("permanent change repeated upload %d", f.beginCalls)
			}
		})
	}
}

func TestCommissionDeliveryLockPreparationFailureSchedulesWithoutAgent(t *testing.T) {
	for _, phase := range []string{"api_initialization", "pre_upload_lock", "material_preparation"} {
		t.Run(phase, func(t *testing.T) {
			f, server := newDeliveryRetryFixture(t, "lost_confirm")
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			job, calls := generateDeliveryOnce(t, w)
			// Save only verified artifacts to model a pre-write credential-lock timeout.
			dir := t.TempDir()
			if err := w.journal.SetCommissionFulfillmentDirectory(job.ID, dir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte("Verified report\n"), 0600); err != nil {
				t.Fatal(err)
			}
			decision := dispatch.CommissionFulfillmentDecision{Version: 1, RequestID: job.ID, OrderID: "901", OrderVersion: 3, Outcome: "artifacts_ready", Summary: "Generated report", SelfCheck: "Read bytes", Artifacts: []dispatch.CommissionArtifact{{LogicalPath: "outputs/report.txt", RelativePath: "report.txt"}}}
			artifacts, err := validateCommissionArtifacts(dir, decision)
			if err != nil {
				t.Fatal(err)
			}
			result := dispatch.CommissionFulfillmentResult{Decision: decision, OutputDirectory: dir, Artifacts: artifacts}
			retry := dispatch.CommissionDeliveryRetry{Attempts: 1, BuyerID: "41", SellerID: "42", SnapshotID: strconv.FormatInt(int64(f.order.SnapshotID), 10)}
			if err := w.journal.SaveCommissionCheckpoint(job.ID, result, "generated-once", retry); err != nil {
				t.Fatal(err)
			}
			if err := w.failCommissionLocalCheck(job, fmt.Errorf("%s: %w", phase, auth.ErrV2CredentialsLockTimeout), "generated-once"); err != nil {
				t.Fatal(err)
			}
			stored := w.commissionDeliveryJob(job)
			if stored.Status != "pending" || stored.CommissionDelivery.LastCode != "commission_credential_lock_unavailable" || stored.CommissionCheckpoint == nil || *calls != 0 {
				t.Fatalf("plain lock timeout did not schedule original bytes %+v calls=%d", stored, *calls)
			}
		})
	}
}
