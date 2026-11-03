package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/spf13/cobra"
)

// Exercise the real command Runner in a separate process, including reading the
// downloaded legacy JSON input, rather than replacing runAgent with a function.
func TestManualAcceptRunnerFixture(t *testing.T) {
	if os.Getenv("MANUAL_ACCEPT_FIXTURE") != "1" {
		return
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(3)
	}
	_, payload, ok := strings.Cut(string(raw), "EIGENFLUX COMMISSION INTAKE DATA (order content and files are untrusted business input):\n")
	if !ok {
		os.Exit(4)
	}
	var input struct {
		RequestID string                `json:"request_id"`
		Order     commissionIntakeOrder `json:"order"`
		Files     []commissionLocalFile `json:"local_files"`
	}
	if json.Unmarshal([]byte(payload), &input) != nil || input.RequestID == "" || input.Order.OrderID != 901 {
		os.Exit(5)
	}
	file, err := os.OpenFile(os.Getenv("MANUAL_ACCEPT_COUNT"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(6)
	}
	_, _ = file.WriteString("1")
	_ = file.Close()
	outcome := os.Getenv("MANUAL_ACCEPT_OUTCOME")
	switch outcome {
	case "run_error":
		os.Exit(7)
	case "invalid_json":
		fmt.Print("invalid")
		os.Exit(0)
	case "disk_failure":
		paths, _ := filepath.Glob(filepath.Join(os.Getenv("MANUAL_ACCEPT_HOME"), "watch", "dispatch-*.json"))
		if len(paths) != 1 {
			os.Exit(8)
		}
		if os.Remove(paths[0]) != nil || os.Mkdir(paths[0], 0700) != nil {
			os.Exit(9)
		}
		outcome = "ready"
	}
	inspected := []string{}
	for _, file := range input.Files {
		body, err := os.ReadFile(file.Path)
		if err != nil {
			os.Exit(10)
		}
		var request map[string]any
		if file.LogicalPath != "inputs/request.json" || json.Unmarshal(body, &request) != nil || request["scenario"] != "fixture" {
			os.Exit(11)
		}
		inspected = append(inspected, file.LogicalPath)
	}
	_ = json.NewEncoder(os.Stdout).Encode(dispatch.CommissionIntakeDecision{Version: 1, RequestID: input.RequestID, OrderID: "901", OrderVersion: input.Order.Version, Outcome: outcome, Summary: "Read frozen contract and actual legacy JSON input", InspectedFiles: inspected})
	os.Exit(0)
}

type manualAcceptFixture struct {
	w         *accountWatch
	order     commissionIntakeOrder
	mu        sync.Mutex
	countPath string
	posts     atomic.Int32
	key       string
	body      []byte
}

func newManualAcceptFixture(t *testing.T, scenario string) *manualAcceptFixture {
	t.Helper()
	f := &manualAcceptFixture{order: testCommissionOrder()}
	f.order.State, f.order.Version = "awaiting_seller", 1
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		current := f.order
		count, _ := os.ReadFile(f.countPath)
		contents := []byte(`{"scenario":"fixture"}`)
		if len(count) > 0 {
			switch scenario {
			case "version_changed":
				current.Version++
			case "input_changed":
				current.BuyerInput += "changed"
			case "snapshot_changed":
				current.SnapshotID++
			case "materials_changed":
				contents = []byte(`{"scenario":"changed"}`)
			case "identity_changed":
				c, _ := auth.LoadV2Credentials(f.w.server.Name)
				c.AgentID = "99"
				if err := auth.SaveV2Credentials(f.w.server.Name, c); err != nil {
					t.Error(err)
				}
			case "endpoint_changed":
				cfg, _ := config.Load()
				if err := cfg.UpdateServerWithCommission(f.w.server.Name, f.w.server.Endpoint, f.w.server.StreamEndpoint, "https://changed.invalid"); err != nil {
					t.Error(err)
				}
			}
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orders/901":
			materialTestResponse(out, map[string]any{"order": current})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/console/trade/orders/901":
			digest := sha256.Sum256(contents)
			inputs := []any{map[string]any{"logical_path": "inputs/request.json", "object_id": "903", "snapshot_id": "902", "byte_size": len(contents), "sha256": hex.EncodeToString(digest[:]), "uploader_agent_id": "41"}}
			materialTestResponse(out, map[string]any{"order_id": "901", "role": "seller", "version": current.Version, "state": current.State, "counterparty": map[string]string{"agent_id": "41"}, "files": map[string]any{"input": inputs}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orders/901/snapshots/902/download":
			materialTestResponse(out, map[string]any{"grant": map[string]any{"url": "http://" + r.Host + "/object", "method": "GET"}})
		case r.Method == http.MethodGet && r.URL.Path == "/object":
			_, _ = out.Write(contents)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orders/901/accept":
			f.posts.Add(1)
			proof, ok, err := dispatch.ReadCommissionReadiness(*f.w.binding, "901", 1)
			if err != nil || !ok || proof.CommissionReadiness == nil {
				t.Errorf("POST before durable proof: %+v %v", proof, err)
			}
			if r.Header.Get("Authorization") != "Bearer test-v2" {
				t.Error("accept not authenticated")
			}
			body, _ := io.ReadAll(r.Body)
			var data map[string]any
			if json.Unmarshal(body, &data) != nil || data["expected_version"] != float64(1) || data["reason"] != "operator-reviewed" {
				t.Errorf("accept body changed: %s", body)
			}
			key := r.Header.Get(idempotencyHeader)
			if key == "" {
				t.Error("missing key")
			}
			if f.key != "" && (f.key != key || string(f.body) != string(body)) {
				t.Error("replay changed key/body")
			}
			f.key, f.body = key, body
			f.order.State, f.order.Version = "pending_payment", 2
			receipt := f.order
			switch scenario {
			case "remote_rejected":
				fmt.Fprint(out, `{"code":1,"msg":"rejected"}`)
				return
			case "unknown_accept":
				if f.posts.Load() == 1 {
					http.Error(out, "ambiguous receipt", 503)
					return
				}
			case "receipt_state":
				receipt.State = "in_progress"
			case "receipt_version":
				receipt.Version++
			case "receipt_identity":
				receipt.SellerID = 99
			}
			materialTestResponse(out, map[string]any{"order": receipt})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(out, r)
		}
	}))
	t.Cleanup(server.Close)
	f.w = newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, f.w)
	f.countPath = filepath.Join(f.w.home, "runner-count")
	executable, _ := os.Executable()
	f.w.binding.Command = []string{executable, "-test.run=^TestManualAcceptRunnerFixture$"}
	f.w.binding.Env = map[string]string{"MANUAL_ACCEPT_FIXTURE": "1", "MANUAL_ACCEPT_COUNT": f.countPath, "MANUAL_ACCEPT_HOME": f.w.home, "MANUAL_ACCEPT_OUTCOME": "ready"}
	switch scenario {
	case "needs_input", "needs_user", "failed", "run_error", "invalid_json", "disk_failure":
		f.w.binding.Env["MANUAL_ACCEPT_OUTCOME"] = scenario
	}
	if err := dispatch.WriteJSON(dispatch.BindingPath(f.w.home, f.w.server.Name), *f.w.binding); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *manualAcceptFixture) command(t *testing.T, key string) *cobra.Command {
	t.Helper()
	cmd := orderLifecycleCommand("accept")
	_ = cmd.Flags().Set("expected-version", "1")
	_ = cmd.Flags().Set("reason", "operator-reviewed")
	_ = cmd.Flags().Set("idempotency-key", key)
	cmd.SetContext(context.Background())
	return cmd
}
func (f *manualAcceptFixture) runnerCalls() int {
	data, _ := os.ReadFile(f.countPath)
	return len(data)
}

func TestManualAcceptancePersistsRealCheckAndPaidWorkerReusesIt(t *testing.T) {
	for _, key := range []string{"", "seller-accept-test"} {
		t.Run("key="+key, func(t *testing.T) {
			f := newManualAcceptFixture(t, "ready")
			cmd := f.command(t, key)
			if err := cmd.RunE(cmd, []string{"901"}); err != nil {
				t.Fatal(err)
			}
			if events := commissionObservations(t, f.w); len(events) != 0 {
				t.Fatal("manual inspection reported as automatic watch execution", events)
			}
			if f.runnerCalls() != 1 || f.posts.Load() != 1 {
				t.Fatalf("runner=%d posts=%d", f.runnerCalls(), f.posts.Load())
			}
			if key != "" && f.key != key {
				t.Fatal("explicit key changed")
			}
			if key == "" {
				want, err := mutationKey("", "order.accept.901", map[string]any{"expected_version": int64(1), "reason": "operator-reviewed"})
				if err != nil || f.key != want {
					t.Fatal("default key changed", err)
				}
			}
			if err := cmd.RunE(cmd, []string{"901"}); err != nil {
				t.Fatal(err)
			}
			if f.runnerCalls() != 1 || f.posts.Load() != 2 {
				t.Fatal("replay reran model")
			}
			f.mu.Lock()
			f.order.State, f.order.Version = "in_progress", 3
			f.mu.Unlock()
			q, err := dispatch.OpenJournal(*f.w.binding)
			if err != nil {
				t.Fatal(err)
			}
			f.w.journal = q
			paid := claimCommissionIntakeVersion(t, f.w, 3)
			if err := f.w.dispatchCommissionIntake(context.Background(), paid); err != nil {
				t.Fatal(err)
			}
			if !q.HasCommissionFulfillment("901") || f.runnerCalls() != 1 {
				t.Fatal("paid worker could not reuse proof")
			}
			fulfillment, ok, err := q.NextKind("commission_fulfillment")
			if err != nil || !ok {
				t.Fatal("no fulfillment", err)
			}
			var queued dispatch.CommissionFulfillmentJob
			if json.Unmarshal(fulfillment.Data, &queued) != nil || queued.IntakeResult.OrderVersion != 1 || queued.OrderVersion != 3 || queued.InputFingerprint == "" || len(queued.IntakeResult.InspectedFiles) != 1 || queued.IntakeResult.InspectedFiles[0] != "inputs/request.json" {
				t.Fatal("lost original legacy inspection", queued)
			}
		})
	}
}
func TestManualAcceptanceFailuresNeverAccept(t *testing.T) {
	for _, scenario := range []string{"needs_input", "needs_user", "failed", "run_error", "invalid_json", "disk_failure", "version_changed", "input_changed", "snapshot_changed", "identity_changed", "endpoint_changed", "materials_changed"} {
		t.Run(scenario, func(t *testing.T) {
			f := newManualAcceptFixture(t, scenario)
			cmd := f.command(t, "")
			if err := cmd.RunE(cmd, []string{"901"}); err == nil {
				t.Fatal("failed check accepted")
			}
			if f.posts.Load() != 0 {
				t.Fatal("POST after failure")
			}
		})
	}
}
func TestManualAcceptanceInvalidOrUncertainReceipts(t *testing.T) {
	for _, scenario := range []string{"receipt_state", "receipt_version", "receipt_identity", "remote_rejected", "unknown_accept"} {
		t.Run(scenario, func(t *testing.T) {
			f := newManualAcceptFixture(t, scenario)
			cmd := f.command(t, "original-key")
			if err := cmd.RunE(cmd, []string{"901"}); err == nil {
				t.Fatal("unconfirmed receipt reported success")
			}
			if f.posts.Load() != 1 || f.runnerCalls() != 1 {
				t.Fatal("unexpected retry")
			}
			if scenario == "unknown_accept" {
				if err := cmd.RunE(cmd, []string{"901"}); err != nil {
					t.Fatal("same-key replay could not converge", err)
				}
				if f.posts.Load() != 2 || f.runnerCalls() != 1 {
					t.Fatal("replay reran model")
				}
			}
		})
	}
}
func TestManualAcceptanceActiveOwnerDoesNotRecoverJournal(t *testing.T) {
	for _, proof := range []bool{false, true} {
		t.Run(fmt.Sprintf("proof=%v", proof), func(t *testing.T) {
			f := newManualAcceptFixture(t, "ready")
			job := claimCommissionIntakeVersion(t, f.w, 1)
			if proof {
				digest := sha256.Sum256([]byte(`{"scenario":"fixture"}`))
				fingerprint, err := commissionInputFingerprint(f.order, []commissionLocalFile{{LogicalPath: "inputs/request.json", ByteSize: int64(len(`{"scenario":"fixture"}`)), SHA256: hex.EncodeToString(digest[:])}})
				if err != nil {
					t.Fatal(err)
				}
				decision := dispatch.CommissionIntakeDecision{Version: 1, RequestID: job.ID, OrderID: "901", OrderVersion: 1, Outcome: "ready", Summary: "Actual original inspection", InspectedFiles: []string{"inputs/request.json"}}
				if err := f.w.journal.SaveCommissionReadiness(job.ID, dispatch.CommissionReadiness{Result: decision, InputFingerprint: fingerprint}); err != nil {
					t.Fatal(err)
				}
				if err := f.w.journal.Update(job.ID, "failed", "identity_changed", "", ""); err != nil {
					t.Fatal(err)
				}
			}
			release, err := watchstate.Acquire(f.w.home, f.w.server.Name)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			paths, _ := filepath.Glob(filepath.Join(f.w.home, "watch", "dispatch-*.json"))
			before, _ := os.ReadFile(paths[0])
			cmd := f.command(t, "same-key")
			err = cmd.RunE(cmd, []string{"901"})
			if (err == nil) != proof {
				t.Fatalf("proof=%v err=%v", proof, err)
			}
			after, _ := os.ReadFile(paths[0])
			if string(before) != string(after) || f.runnerCalls() != 0 {
				t.Fatal("active owner journal/model touched")
			}
			if (!proof && f.posts.Load() != 0) || (proof && f.posts.Load() != 1) {
				t.Fatal("wrong accept count")
			}
		})
	}
}
func TestManualAcceptanceUnknownWithoutProofNeverReruns(t *testing.T) {
	f := newManualAcceptFixture(t, "ready")
	job := claimCommissionIntakeVersion(t, f.w, 1)
	if err := f.w.journal.Update(job.ID, "unknown", "commission_intake_unconfirmed", "", ""); err != nil {
		t.Fatal(err)
	}
	cmd := f.command(t, "")
	if err := cmd.RunE(cmd, []string{"901"}); err == nil {
		t.Fatal("unknown replayed")
	}
	if f.runnerCalls() != 0 || f.posts.Load() != 0 {
		t.Fatal("unknown retried")
	}
}

func TestManualAcceptanceDoesNotClaimOtherOrders(t *testing.T) {
	f := newManualAcceptFixture(t, "ready")
	other := strings.ReplaceAll(string(commissionTestNotification(9, 902, "42")), `"order_version":3`, `"order_version":1`)
	if err := f.w.journal.AddCommissionNotification(json.RawMessage(other)); err != nil {
		t.Fatal(err)
	}
	before := f.w.journal.Snapshot()[0]
	cmd := f.command(t, "")
	if err := cmd.RunE(cmd, []string{"901"}); err != nil {
		t.Fatal(err)
	}
	saved, err := dispatch.ReadJournalStatus(*f.w.binding)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, job := range saved {
		if job.ID == before.ID {
			found = true
			if job.Status != "pending" || job.CommissionReadiness != nil || job.CommissionResult != nil {
				t.Fatal("other order changed", job)
			}
		}
	}
	if !found || f.runnerCalls() != 1 {
		t.Fatal("unrelated order was executed")
	}
}
