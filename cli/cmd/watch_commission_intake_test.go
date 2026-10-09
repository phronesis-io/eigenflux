package cmd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/skills"
)

func installCommissionIntakeRules(t *testing.T, w *accountWatch) {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(filepath.Join(dir, "ef-commission"), os.DirFS("../../skills/ef-commission")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "input-review"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "input-review", "SKILL.md"), []byte("---\nname: input-review\ndescription: Review supplied inputs.\n---\nRead the provided material.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, oldVersion := skills.VerifyPublicKeyBase64, version
	skills.VerifyPublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	version = "1.0.0"
	t.Cleanup(func() { skills.VerifyPublicKeyBase64 = oldKey; version = oldVersion })
	manifest, err := skills.GenerateManifest(dir, "1.0.0", "1.0.0", []string{"ef-commission"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Sequence = 1
	manifest.TarSHA256 = strings.Repeat("a", 64)
	manifest.ManagedBy = skills.ManagedByValue
	if err := skills.SignManifest(manifest, key); err != nil {
		t.Fatal(err)
	}
	if err := skills.WriteManifestAtomic(dir, manifest); err != nil {
		t.Fatal(err)
	}
	w.binding.SkillsDir = dir
	if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), *w.binding); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpdateServerWithCommission(w.server.Name, w.server.Endpoint, w.server.StreamEndpoint, w.server.Endpoint); err != nil {
		t.Fatal(err)
	}
	srv, _ := cfg.GetActive(w.server.Name)
	w.server = *srv
}

func claimCommissionIntake(t *testing.T, w *accountWatch) dispatch.Job {
	t.Helper()
	if err := w.journal.AddCommissionNotification(commissionTestNotification(81, 901, "42")); err != nil {
		t.Fatal(err)
	}
	job, ok, err := w.journal.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	return job
}

func testCommissionOrder() commissionIntakeOrder {
	return commissionIntakeOrder{OrderID: 901, BuyerID: 41, SellerID: 42, Version: 3, State: "pending_payment", SnapshotID: 902, BuyerInput: "Inspect these instructions", Contract: json.RawMessage(`{"fulfillment_skill":"input-review","requires_materials":false}`)}
}

func TestCommissionAwaitingSellerAcceptedOnlyAfterReadyAgent(t *testing.T) {
	for _, outcome := range []string{"ready", "needs_input", "needs_user", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			order := testCommissionOrder()
			order.State = "awaiting_seller"
			var ran, accepted bool
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/orders/901":
					materialTestResponse(out, map[string]any{"order": order})
				case "/api/v2/console/trade/orders/901":
					materialTestResponse(out, map[string]any{"order_id": "901", "role": "seller", "version": 3, "state": "awaiting_seller", "counterparty": map[string]string{"agent_id": "41"}, "files": map[string]any{"input": []any{}}})
				case "/api/v1/orders/901/accept":
					var body struct {
						Version int64 `json:"expected_version"`
					}
					if !ran || outcome != "ready" || r.Method != "POST" || r.Header.Get(idempotencyHeader) != "watch-accept-901-3" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Version != 3 {
						t.Error("acceptance bypassed verified Agent")
					}
					accepted = true
					acceptedOrder := order
					acceptedOrder.State, acceptedOrder.Version = "pending_payment", order.Version+1
					materialTestResponse(out, map[string]any{"order": acceptedOrder})
				default:
					t.Errorf("unexpected %s", r.URL.Path)
				}
			}))
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			job := claimCommissionIntake(t, w)
			w.runAgent = func(_ context.Context, request dispatch.Request) (dispatch.Result, error) {
				ran = true
				raw, _ := json.Marshal(dispatch.CommissionIntakeDecision{Version: 1, RequestID: request.ID, OrderID: "901", OrderVersion: 3, Outcome: outcome, Summary: "Inspected actual input", InspectedFiles: []string{}})
				return dispatch.Result{Text: string(raw)}, nil
			}
			if err := w.dispatchCommissionIntake(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if accepted != (outcome == "ready") {
				t.Fatalf("outcome %s accepted=%v", outcome, accepted)
			}
		})
	}
}

func TestCommissionIntakeWorkerChecksAndPersistsActualDecision(t *testing.T) {
	for _, scenario := range []string{"ready", "needs_input", "run_error", "permission", "wrong_files", "changed_order", "changed_identity", "wrong_seller", "missing_skill", "unsigned_rule", "invalid_result"} {
		t.Run(scenario, func(t *testing.T) {
			order := testCommissionOrder()
			var reads atomic.Int32
			var called bool
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-v2" {
					t.Error("not using pinned credentials")
				}
				switch r.URL.Path {
				case "/api/v1/orders/901":
					current := order
					if scenario == "wrong_seller" {
						current.SellerID = 43
					}
					if reads.Add(1) > 1 && scenario == "changed_order" {
						current.Version++
					}
					_ = json.NewEncoder(out).Encode(map[string]any{"code": 0, "data": map[string]any{"order": current}})
				case "/api/v2/console/trade/orders/901":
					_ = json.NewEncoder(out).Encode(map[string]any{"code": 0, "data": map[string]any{"order_id": "901", "role": "seller", "version": 3, "state": "pending_payment", "counterparty": map[string]string{"agent_id": "41"}, "files": map[string]any{"input": []any{}}}})
				default:
					t.Errorf("unexpected API request %s", r.URL.Path)
					http.NotFound(out, r)
				}
			}))
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			if scenario == "missing_skill" {
				if err := os.Remove(filepath.Join(w.binding.SkillsDir, "input-review", "SKILL.md")); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "unsigned_rule" {
				// Preserve a valid older manifest, but add Commission rules outside its signed membership.
				w.binding.SkillsDir = installHeartbeatTestRules(t)
				if err := os.CopyFS(filepath.Join(w.binding.SkillsDir, "ef-commission"), os.DirFS("../../skills/ef-commission")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(w.binding.SkillsDir, "input-review"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(w.binding.SkillsDir, "input-review", "SKILL.md"), []byte("---\nname: input-review\n---\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), *w.binding); err != nil {
					t.Fatal(err)
				}
			}
			job := claimCommissionIntake(t, w)
			w.runAgent = func(ctx context.Context, request dispatch.Request) (dispatch.Result, error) {
				called = true
				if request.ID != job.ID || request.Kind != "commission_order" || request.SessionID != "" {
					t.Errorf("unexpected request %+v", request)
				}
				if strings.Contains(request.Prompt, "test-v2") || strings.Contains(request.Prompt, "test-refresh") || strings.Contains(request.Prompt, `"cli_command"`) {
					t.Error("prompt exposed credentials or model-managed downloads")
				}
				if !strings.Contains(request.Prompt, `"local_files"`) || !strings.Contains(request.Prompt, "Read the provided material") {
					t.Error("prompt missing verified intake context")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Error("intake has no deadline")
				}
				if scenario == "run_error" {
					return dispatch.Result{}, errors.New("host interrupted")
				}
				if scenario == "permission" {
					return dispatch.Result{}, dispatch.ErrNeedsUser
				}
				if scenario == "invalid_result" {
					return dispatch.Result{Text: "ready"}, nil
				}
				decision := dispatch.CommissionIntakeDecision{Version: 1, RequestID: job.ID, OrderID: "901", OrderVersion: 3, Outcome: "ready", Summary: "检查完成", InspectedFiles: []string{}}
				if scenario == "needs_input" {
					decision.Outcome = "needs_input"
					decision.Summary = "缺少需求范围"
				}
				if scenario == "wrong_files" {
					decision.InspectedFiles = []string{"invented.txt"}
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
				raw, _ := json.Marshal(decision)
				return dispatch.Result{Text: string(raw), SessionID: "independent-session"}, nil
			}
			err := w.dispatchCommissionIntake(context.Background(), job)
			if err != nil && scenario != "changed_identity" {
				t.Fatal(err)
			}
			jobs := w.journal.Snapshot()
			if len(jobs) != 1 {
				t.Fatal(jobs)
			}
			got := jobs[0]
			switch scenario {
			case "ready", "needs_input":
				expected := "completed"
				if scenario == "needs_input" {
					expected = "needs_user"
				}
				if got.Status != expected || got.CommissionResult == nil || got.CommissionResult.Outcome != scenario || got.SessionID != "independent-session" {
					t.Fatalf("actual result lost: %+v", got)
				}
				reopened, err := dispatch.ReadJournalStatus(*w.binding)
				if err != nil || len(reopened) != 1 || reopened[0].CommissionResult == nil {
					t.Fatalf("result not persisted: %+v %v", reopened, err)
				}
			case "permission", "missing_skill":
				if got.Status != "needs_user" {
					t.Fatalf("expected permission/skill blocker: %+v", got)
				}
			case "run_error":
				if got.Status != "unknown" {
					t.Fatalf("uncertain result was replayable: %+v", got)
				}
			default:
				if got.Status != "failed" || got.CommissionResult != nil {
					t.Fatalf("unsafe success: %+v", got)
				}
			}
			if (scenario == "missing_skill" || scenario == "unsigned_rule" || scenario == "wrong_seller") && called {
				t.Fatal("Agent invoked before preflight passed")
			}
			entries, err := filepath.Glob(filepath.Join(w.binding.WorkDir, ".eigenflux-intake-*"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("material directory leaked: %v %v", entries, err)
			}
		})
	}
}

func TestCommissionInspectedFilesMustMatchVerifiedInputs(t *testing.T) {
	files := []commissionLocalFile{{LogicalPath: "a"}, {LogicalPath: "b"}}
	for _, tc := range []struct {
		outcome         string
		paths           []string
		required, valid bool
	}{
		{"ready", []string{"a", "b"}, true, true},
		{"ready", []string{"a"}, false, false},
		{"ready", []string{"a", "b", "c"}, false, false},
		{"needs_input", []string{"a"}, true, true},
		{"needs_input", []string{"c"}, false, false},
		{"ready", []string{"a", "a"}, false, false},
	} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			if got := validCommissionInspectedFiles(dispatch.CommissionIntakeDecision{Outcome: tc.outcome, InspectedFiles: tc.paths}, files, tc.required); got != tc.valid {
				t.Fatalf("got %v", got)
			}
		})
	}
	if validCommissionInspectedFiles(dispatch.CommissionIntakeDecision{Outcome: "ready", InspectedFiles: []string{}}, nil, true) {
		t.Fatal("required files treated as optional")
	}
	if !validCommissionInspectedFiles(dispatch.CommissionIntakeDecision{Outcome: "ready", InspectedFiles: []string{}}, nil, false) {
		t.Fatal("valid no-file contract rejected")
	}
}
