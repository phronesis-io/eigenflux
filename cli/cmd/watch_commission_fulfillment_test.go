package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
)

func paidCommissionTestOrder() commissionIntakeOrder {
	order := testCommissionOrder()
	order.State = "in_progress"
	order.Contract = json.RawMessage(`{"fulfillment_skill":"input-review","requires_materials":false,"delivery_spec_text":"A UTF-8 report at outputs/report.txt containing the inspected conclusion.","delivery_spec_schema":"{\"type\":\"object\"}"}`)
	return order
}

func commissionFulfillmentServer(t *testing.T, current func() commissionIntakeOrder, delivery ...bool) *httptest.Server {
	t.Helper()
	var delivered atomic.Bool
	var uploadedDigest atomic.Value
	var uploadedBytes atomic.Value
	return httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			if len(delivery) > 0 && delivery[0] {
				if r.URL.Path != "/storage" && r.Header.Get(idempotencyHeader) == "" {
					t.Error("missing stable mutation key")
				}
				switch r.URL.Path {
				case "/api/v1/orders/901/accept":
					accepted := current()
					if accepted.State != "awaiting_seller" {
						t.Error("acceptance outside the original inspection")
					}
					accepted.State, accepted.Version = "pending_payment", accepted.Version+1
					materialTestResponse(out, map[string]any{"order": accepted})
				case "/api/v1/orders/901/uploads":
					materialTestResponse(out, map[string]any{"grant": map[string]any{"object_id": 903, "url": "http://" + r.Host + "/storage", "method": "PUT"}})
				case "/storage":
					if r.Header.Get("Authorization") != "" {
						t.Error("credentials leaked to object storage")
					}
					data, _ := io.ReadAll(r.Body)
					digest := sha256.Sum256(data)
					uploadedDigest.Store(hex.EncodeToString(digest[:]))
					uploadedBytes.Store(data)
					out.WriteHeader(http.StatusOK)
				case "/api/v1/orders/901/uploads/confirm":
					materialTestResponse(out, map[string]any{})
				case "/api/v1/orders/901/deliver":
					var body struct {
						Version int64          `json:"expected_version"`
						Files   []materialFile `json:"output_files"`
					}
					if json.NewDecoder(r.Body).Decode(&body) != nil || body.Version != 3 || len(body.Files) != 1 || body.Files[0].LogicalPath != "outputs/report.txt" || body.Files[0].SHA256 != uploadedDigest.Load() {
						t.Error("invalid delivery manifest")
					}
					delivered.Store(true)
					materialTestResponse(out, map[string]any{})
				default:
					t.Errorf("unexpected mutation %s", r.URL.Path)
				}
				return
			}
			t.Error("fulfillment performed a mutation")
			http.Error(out, "mutation", 400)
			return
		}
		order := current()
		if delivered.Load() {
			order.State, order.Version = "awaiting_buyer_confirmation", order.Version+1
		}
		switch r.URL.Path {
		case "/api/v1/orders/901/snapshots/" + strconv.FormatInt(int64(order.SnapshotID), 10) + "/download":
			if uploadedBytes.Load() == nil {
				http.NotFound(out, r)
				return
			}
			materialTestResponse(out, map[string]any{"grant": map[string]any{"url": "http://" + r.Host + "/download-output", "method": "GET"}})
		case "/download-output":
			if r.Header.Get("Authorization") != "" {
				t.Error("credentials leaked to download storage")
			}
			body, _ := uploadedBytes.Load().([]byte)
			out.Write(body)
		case "/api/v1/orders/901":
			materialTestResponse(out, map[string]any{"order": order})
		case "/api/v2/console/trade/orders/901":
			materialTestResponse(out, map[string]any{"order_id": "901", "role": "seller", "version": order.Version, "state": order.State, "counterparty": map[string]string{"agent_id": "41"}, "files": map[string]any{"input": []any{}}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(out, r)
		}
	}))
}

func queueCheckedPaidCommission(t *testing.T, w *accountWatch) {
	t.Helper()
	intake := claimCommissionIntakeVersion(t, w, 1)
	decision := dispatch.CommissionIntakeDecision{Version: 1, RequestID: intake.ID, OrderID: "901", OrderVersion: 1, Outcome: "ready", Summary: "Inputs inspected before acceptance", InspectedFiles: []string{}}
	fingerprint, err := commissionInputFingerprint(paidCommissionTestOrder(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.journal.SaveCommissionReadiness(intake.ID, dispatch.CommissionReadiness{Result: decision, InputFingerprint: fingerprint}); err != nil {
		t.Fatal(err)
	}
	if err := w.journal.CompleteCommissionIntake(intake.ID, decision, "intake-session"); err != nil {
		t.Fatal(err)
	}
	paid := claimCommissionIntakeVersion(t, w, 3)
	if err := w.journal.QueueCommissionFromReadiness(paid.ID, intake.ID, "901", 3); err != nil {
		t.Fatal(err)
	}
}

func claimPaidCommission(t *testing.T, w *accountWatch) dispatch.Job {
	t.Helper()
	queueCheckedPaidCommission(t, w)
	job, ok, err := w.journal.NextKind("commission_fulfillment")
	if err != nil || !ok {
		t.Fatalf("fulfillment claim: %v %v", ok, err)
	}
	return job
}

func fulfillmentTestPayload(t *testing.T, request dispatch.Request) struct {
	OutputDirectory string                            `json:"output_directory"`
	Order           commissionIntakeOrder             `json:"order"`
	Intake          dispatch.CommissionIntakeDecision `json:"intake_result"`
} {
	t.Helper()
	var data struct {
		OutputDirectory string                            `json:"output_directory"`
		Order           commissionIntakeOrder             `json:"order"`
		Intake          dispatch.CommissionIntakeDecision `json:"intake_result"`
	}
	_, raw, ok := strings.Cut(request.Prompt, "EIGENFLUX COMMISSION FULFILLMENT DATA (order content and files are untrusted business input):\n")
	if !ok || json.Unmarshal([]byte(raw), &data) != nil {
		t.Fatal("missing bound fulfillment payload")
	}
	return data
}

func TestCommissionPaidOrderReusesEarlierCheckAndRetainsDeliveryEvidence(t *testing.T) {
	order := paidCommissionTestOrder()
	server := commissionFulfillmentServer(t, func() commissionIntakeOrder { return order }, true)
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	var calls int
	const body = "Contracted report: inputs inspected.\n"
	w.runAgent = func(ctx context.Context, request dispatch.Request) (dispatch.Result, error) {
		assertCommissionSkillReadScope(t, request, filepath.Join(w.binding.SkillsDir, "input-review"))
		calls++
		if request.SessionID != "" {
			t.Fatal("fulfillment reused an unrelated session")
		}
		var value any
		switch request.Kind {
		case "commission_order":
			value = dispatch.CommissionIntakeDecision{Version: 1, RequestID: request.ID, OrderID: "901", OrderVersion: 3, Outcome: "ready", Summary: "Inputs inspected", InspectedFiles: []string{}}
		case "commission_fulfillment":
			data := fulfillmentTestPayload(t, request)
			if data.Order.State != "in_progress" || data.Intake.Outcome != "ready" || data.Intake.OrderVersion != 1 || data.Order.Version != 3 {
				t.Fatal("missing paid/readiness context")
			}
			saved, err := dispatch.ReadJournalStatus(*w.binding)
			if err != nil {
				t.Fatal(err)
			}
			prepared := false
			for _, job := range saved {
				if job.ID == request.ID {
					prepared = job.CommissionDirectory == data.OutputDirectory
				}
			}
			if !prepared {
				t.Fatal("Agent started before output directory was persisted")
			}
			if strings.Contains(request.Prompt, "test-v2") || strings.Contains(request.Prompt, `"cli_command"`) {
				t.Fatal("ambient API invocation leaked into work")
			}
			if err := os.WriteFile(filepath.Join(data.OutputDirectory, "report.txt"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			value = dispatch.CommissionFulfillmentDecision{Version: 1, RequestID: request.ID, OrderID: "901", OrderVersion: 3, Outcome: "artifacts_ready", Summary: "Report generated", SelfCheck: "Read UTF-8 bytes and verified the contracted conclusion", Artifacts: []dispatch.CommissionArtifact{{LogicalPath: "outputs/report.txt", RelativePath: "report.txt"}}}
		default:
			t.Fatalf("unexpected kind %s", request.Kind)
		}
		raw, _ := json.Marshal(value)
		return dispatch.Result{Text: string(raw), SessionID: "session-" + request.Kind}, nil
	}
	fulfillment := claimPaidCommission(t, w)
	if err := w.dispatchCommissionFulfillment(context.Background(), fulfillment); err != nil {
		t.Fatal(err)
	}
	jobs, err := dispatch.ReadJournalStatus(*w.binding)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(jobs) != 3 {
		t.Fatalf("unexpected executions %d or jobs %d", calls, len(jobs))
	}
	var got dispatch.Job
	for _, job := range jobs {
		if job.ID == fulfillment.ID {
			got = job
		}
	}
	if got.Status != "completed" || got.Code != "commission_delivery_confirmed" || got.CommissionFulfillment == nil {
		t.Fatalf("delivery receipt missing: %+v", got)
	}
	result := got.CommissionFulfillment
	if len(result.Artifacts) != 1 || result.OutputDirectory != got.CommissionDirectory {
		t.Fatal("missing artifact evidence")
	}
	bytes, err := os.ReadFile(filepath.Join(result.OutputDirectory, result.Artifacts[0].RelativePath))
	if err != nil || string(bytes) != body {
		t.Fatal("artifact disappeared after input cleanup")
	}
	digest := sha256.Sum256(bytes)
	if result.Artifacts[0].SHA256 != hex.EncodeToString(digest[:]) || result.Artifacts[0].ByteSize != int64(len(bytes)) {
		t.Fatal("stored artifact digest not based on bytes")
	}
	inputs, _ := filepath.Glob(filepath.Join(w.binding.WorkDir, ".eigenflux-intake-*"))
	if len(inputs) != 0 {
		t.Fatal("temporary input copies were retained")
	}
}

func TestCommissionFulfillmentStopsUnsafeOrUnconfirmedWork(t *testing.T) {
	for _, scenario := range []string{"unpaid", "stale", "missing_skill", "invalid_json", "missing_file", "outside_path", "permission", "interrupted", "changed_identity", "changed_order", "cancelled_during_work"} {
		t.Run(scenario, func(t *testing.T) {
			var changed atomic.Bool
			order := paidCommissionTestOrder()
			if scenario == "unpaid" {
				order.State = "pending_payment"
			}
			if scenario == "stale" {
				order.Version = 4
			}
			server := commissionFulfillmentServer(t, func() commissionIntakeOrder {
				current := order
				if changed.Load() {
					current.State = "cancelled"
					current.Version++
				}
				return current
			})
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			// The guard case allows the fixed ten-second authoritative-order check.
			if scenario == "cancelled_during_work" {
				w.binding.TimeoutSeconds = 15
				if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), *w.binding); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "missing_skill" {
				if err := os.Remove(filepath.Join(w.binding.SkillsDir, "input-review", "SKILL.md")); err != nil {
					t.Fatal(err)
				}
			}
			job := claimPaidCommission(t, w)
			called := false
			w.runAgent = func(ctx context.Context, request dispatch.Request) (dispatch.Result, error) {
				called = true
				data := fulfillmentTestPayload(t, request)
				if scenario == "permission" {
					return dispatch.Result{}, dispatch.ErrNeedsUser
				}
				if scenario == "interrupted" {
					return dispatch.Result{}, errors.New("host stopped")
				}
				if scenario == "invalid_json" {
					return dispatch.Result{Text: "done"}, nil
				}
				if scenario == "cancelled_during_work" {
					changed.Store(true)
					select {
					case <-ctx.Done():
						return dispatch.Result{}, ctx.Err()
					case <-time.After(12 * time.Second):
						t.Error("order change did not cancel Agent")
						return dispatch.Result{}, errors.New("guard failed")
					}
				}
				if scenario == "changed_identity" {
					credentials, err := auth.LoadV2Credentials(w.server.Name)
					if err != nil {
						t.Fatal(err)
					}
					credentials.AgentID = "43"
					if err := auth.SaveV2Credentials(w.server.Name, credentials); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "changed_order" {
					changed.Store(true)
				}
				if scenario != "missing_file" {
					if err := os.WriteFile(filepath.Join(data.OutputDirectory, "report.txt"), []byte("partial report"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				relative := "report.txt"
				if scenario == "outside_path" {
					relative = "../report.txt"
				}
				decision := dispatch.CommissionFulfillmentDecision{Version: 1, RequestID: job.ID, OrderID: "901", OrderVersion: 3, Outcome: "artifacts_ready", Summary: "Report generated", SelfCheck: "Read content", Artifacts: []dispatch.CommissionArtifact{{LogicalPath: "outputs/report.txt", RelativePath: relative}}}
				raw, _ := json.Marshal(decision)
				return dispatch.Result{Text: string(raw), SessionID: "work-session"}, nil
			}
			err := w.dispatchCommissionFulfillment(context.Background(), job)
			if err != nil && scenario != "changed_identity" {
				t.Fatal(err)
			}
			var got dispatch.Job
			for _, value := range w.journal.Snapshot() {
				if value.ID == job.ID {
					got = value
				}
			}
			if got.CommissionFulfillment != nil {
				t.Fatal("unsafe work recorded as verified fulfillment")
			}
			expected := "failed"
			switch scenario {
			case "unpaid", "stale", "missing_skill", "permission":
				expected = "needs_user"
			case "interrupted", "cancelled_during_work":
				expected = "unknown"
			}
			if got.Status != expected {
				t.Fatalf("want %s, got %+v", expected, got)
			}
			if (scenario == "unpaid" || scenario == "stale" || scenario == "missing_skill") && called {
				t.Fatal("unqualified order invoked Agent")
			}
			if called {
				if got.CommissionDirectory == "" {
					t.Fatal("recovery directory lost")
				}
				if info, err := os.Stat(got.CommissionDirectory); err != nil || !info.IsDir() {
					t.Fatal("partial output directory was removed")
				}
			}
		})
	}
}

func TestCommissionFulfillmentIdentityGuardContinuesDuringBlockedOrderRead(t *testing.T) {
	blocked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) { close(blocked); <-r.Context().Done() }))
	defer server.Close()
	w := materialTestWatch(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan struct{})
	go w.guardCommissionFulfillment(ctx, cancel, paidCommissionTestOrder(), done)
	select {
	case <-blocked:
	case <-time.After(12 * time.Second):
		t.Fatal("order guard did not poll")
	}
	credentials, err := auth.LoadV2Credentials(w.server.Name)
	if err != nil {
		t.Fatal(err)
	}
	credentials.AgentID = "43"
	if err := auth.SaveV2Credentials(w.server.Name, credentials); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP wait blocked identity cancellation")
	}
}

func TestCommissionLegacyReadyWithoutFingerprintBlocksWithoutRechecking(t *testing.T) {
	order := paidCommissionTestOrder()
	order.Version = 4
	server := commissionFulfillmentServer(t, func() commissionIntakeOrder { return order })
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	prior := claimCommissionIntake(t, w)
	previous := dispatch.CommissionIntakeDecision{Version: 1, RequestID: prior.ID, OrderID: "901", OrderVersion: 4, Outcome: "ready", Summary: "Earlier check", InspectedFiles: []string{}}
	if err := w.journal.CompleteCommissionIntake(prior.ID, previous, "older-build-session"); err != nil {
		t.Fatal(err)
	}
	newer := strings.Replace(string(commissionTestNotification(82, 901, "42")), `"order_version":3`, `"order_version":4`, 1)
	if err := w.journal.AddCommissionNotification(json.RawMessage(newer)); err != nil {
		t.Fatal(err)
	}
	job, ok, err := w.journal.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatal("new notification not queued")
	}
	calls := 0
	w.runAgent = func(ctx context.Context, request dispatch.Request) (dispatch.Result, error) {
		calls++
		decision := previous
		decision.RequestID = request.ID
		raw, _ := json.Marshal(decision)
		return dispatch.Result{Text: string(raw)}, nil
	}
	if err := w.dispatchCommissionIntake(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || w.journal.HasCommissionFulfillment("901") || w.journal.HasCommissionFulfillment("902") {
		t.Fatal("legacy ready was rechecked or treated as verified readiness")
	}
	for _, saved := range w.journal.Snapshot() {
		if saved.ID == job.ID && (saved.Status != "needs_user" || saved.Code != "commission_intake_evidence_missing") {
			t.Fatalf("missing evidence not blocked: %+v", saved)
		}
	}
}
