package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
)

// Run a real child through the native adapter, interrupt it after thread.started,
// reopen the journal, and exercise the normal seller worker against an isolated API.
func TestCommissionGenerationRecoveryEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX process groups")
	}
	for _, scenario := range []string{"empty", "existing", "incomplete", "remote_advanced", "cancelled", "disconnected", "remote_output", "expired", "exhausted", "external_effects", "materials_cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			fixture, api := newDeliveryRetryFixture(t, "healthy")
			defer api.Close()
			var interruptMaterials atomic.Bool
			recoveryCtx, cancelRecovery := context.WithCancel(context.Background())
			defer cancelRecovery()
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if interruptMaterials.Load() && r.URL.Path == "/api/v2/console/trade/orders/901" {
					cancelRecovery()
					return
				}
				if r.URL.Path != "/api/v1/orders/901/files" {
					api.Config.Handler.ServeHTTP(out, r)
					return
				}
				if scenario == "disconnected" {
					http.Error(out, "offline", http.StatusServiceUnavailable)
					return
				}
				outputs := []string{}
				if scenario == "remote_output" {
					outputs = append(outputs, "report.txt")
				}
				materialTestResponse(out, map[string]any{"output_files": outputs})
			}))
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			codex := filepath.Join(w.home, "codex")
			script := "#!/bin/sh\nif [ \"$1\" = --help ]; then echo --no-daemon; exit; fi\nif [ \"$1\" = exec ] && [ \"$2\" = --help ]; then echo --ignore-user-config --ignore-rules --add-dir; exit; fi\ncat >/dev/null\necho '{\"type\":\"thread.started\",\"thread_id\":\"test-session\"}'\nexit 1\n"
			if err := os.WriteFile(codex, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			w.binding.Host, w.binding.Mode, w.binding.Command = "codex", "native", []string{codex}
			w.binding.Args = []string{"--sandbox", "workspace-write", "--ask-for-approval", "never"}
			realVerifier := os.Getenv("EIGENFLUX_COMMISSION_NATIVE_SMOKE") == "1" && scenario == "existing"
			if realVerifier {
				binary, err := exec.LookPath("codex")
				if err != nil {
					t.Fatal(err)
				}
				w.binding.Command, w.binding.TimeoutSeconds = []string{binary}, 180
			}
			installCommissionIntakeRules(t, w)
			if scenario != "external_effects" {
				skill := filepath.Join(w.binding.SkillsDir, "input-review", "SKILL.md")
				if err := os.WriteFile(skill, []byte("---\nname: input-review\ngeneration_recovery: local_only\n---\nInspect local files.\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			job := claimPaidCommission(t, w)
			var firstDirectory string
			if scenario == "expired" {
				fixture.order.DeliveryDueAt = time.Now().Add(2 * time.Second).UnixMilli()
			}
			generateCalls, verifyCalls := 0, 0
			w.runAgent = func(ctx context.Context, req dispatch.Request) (dispatch.Result, error) {
				data := fulfillmentTestPayload(t, req)
				path := filepath.Join(data.OutputDirectory, "report.txt")
				if realVerifier {
					path = filepath.Join(data.OutputDirectory, "outputs", "report.txt")
					if !req.GenerationVerifyOnly {
						if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
							t.Fatal(err)
						}
					}
				}
				if firstDirectory == "" {
					firstDirectory = data.OutputDirectory
				} else if !req.GenerationVerifyOnly {
					if firstDirectory == data.OutputDirectory {
						t.Fatal("new generation reused the interrupted directory")
					}
					if err := os.WriteFile(filepath.Join(firstDirectory, "late.txt"), []byte("late old writer"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if req.GenerationVerifyOnly {
					verifyCalls++
				} else {
					generateCalls++
				}
				if generateCalls == 1 && verifyCalls == 0 || scenario == "exhausted" {
					if scenario == "existing" || scenario == "incomplete" {
						if err := os.WriteFile(path, []byte("Inspection complete. The provided order instructions were reviewed and the requested report is ready."), 0600); err != nil {
							t.Fatal(err)
						}
					}
					interrupted := *w.binding
					interrupted.Command = []string{codex}
					return (dispatch.Runner{Binding: interrupted}).Run(ctx, req)
				}
				if realVerifier {
					if !req.GenerationVerifyOnly {
						t.Fatal("expected read-only Codex")
					}
					res, err := (dispatch.Runner{Binding: *w.binding}).Run(ctx, req)
					if err != nil {
						t.Logf("native verifier: %v", err)
					} else {
						decision, parseErr := dispatch.ParseCommissionFulfillmentDecision(res.Text, req.ID, "901", 3)
						if parseErr == nil {
							t.Logf("native verification %s: %s", decision.Outcome, decision.Summary)
						}
					}
					return res, err
				}
				decision := dispatch.CommissionFulfillmentDecision{Version: 1, RequestID: job.ID, OrderID: "901", OrderVersion: 3, Outcome: "artifacts_ready", Summary: "Report ready", SelfCheck: "Verified contract", Artifacts: []dispatch.CommissionArtifact{{LogicalPath: "outputs/report.txt", RelativePath: "report.txt"}}}
				if req.GenerationVerifyOnly {
					if b, err := os.ReadFile(path); err != nil || string(b) != "Inspection complete. The provided order instructions were reviewed and the requested report is ready." {
						t.Fatalf("original bytes unavailable: %q, %v", b, err)
					}
					if scenario == "incomplete" {
						decision.Outcome, decision.Artifacts = "needs_user", []dispatch.CommissionArtifact{}
					}
				} else if err := os.WriteFile(path, []byte("Recovered report"), 0600); err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(decision)
				if err != nil {
					t.Fatal(err)
				}
				return dispatch.Result{Text: string(raw), SessionID: "second-run"}, nil
			}
			if err := w.dispatchCommissionFulfillment(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			old := w.commissionDeliveryJob(job)
			if old.Status != "pending" || old.CommissionGeneration == nil || old.CommissionGeneration.Attempts != 1 || old.SessionID != "test-session" {
				t.Fatalf("missing evidence after interruption: %+v", old)
			}
			fixture.mu.Lock()
			switch scenario {
			case "remote_advanced":
				fixture.delivered = true
			case "cancelled":
				fixture.order.State = "cancelled"
				fixture.order.Version++
			case "expired":
				time.Sleep(time.Until(time.UnixMilli(fixture.order.DeliveryDueAt)))
			case "materials_cancelled":
				interruptMaterials.Store(true)
			}
			fixture.mu.Unlock()
			checks := 1
			if scenario == "exhausted" {
				checks = 3
			}
			for i := 0; i < checks; i++ {
				recovered := dueCommissionRetry(t, w, job.ID)
				if err := w.dispatchCommissionFulfillment(recoveryCtx, recovered); err != nil {
					t.Fatal(err)
				}
			}
			got := w.commissionDeliveryJob(job)
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			switch scenario {
			case "existing", "empty":
				wantVerify := 0
				if scenario == "existing" {
					wantVerify = 1
				}
				if got.Code != "commission_delivery_confirmed" || fixture.deliverCalls != 1 || verifyCalls != wantVerify || generateCalls != 2-wantVerify {
					t.Fatalf("completion %s, deliveries %d, generate %d, verify %d", got.Code, fixture.deliverCalls, generateCalls, verifyCalls)
				}
			default:
				want := map[string]string{"incomplete": "commission_generation_verification_incomplete", "remote_advanced": "commission_generation_delivery_observed", "cancelled": "commission_generation_order_closed", "disconnected": "commission_generation_probe_pending", "remote_output": "commission_generation_remote_output_exists", "expired": "commission_generation_deadline_reached", "exhausted": "commission_generation_retry_exhausted", "external_effects": "commission_generation_effects_unconfirmed", "materials_cancelled": "commission_generation_probe_pending"}[scenario]
				if got.Code != want || fixture.deliverCalls != 0 || (scenario != "exhausted" && generateCalls != 1) || (scenario == "exhausted" && generateCalls != 3) {
					t.Fatalf("want %s, got %s; deliveries %d, generate %d", want, got.Code, fixture.deliverCalls, generateCalls)
				}
			}
		})
	}
}
