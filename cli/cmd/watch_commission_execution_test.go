package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
)

func TestCommissionExecutionReportRetryUsesSameFenceAndKey(t *testing.T) {
	for _, result := range []string{"retry", "conflict", "invalid_receipt", "identity_changed"} {
		t.Run(result, func(t *testing.T) {
			var requests int
			var firstKey, firstBody string
			srv := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, req *http.Request) {
				requests++
				if req.Method != "POST" || req.URL.Path != "/api/v1/orders/901/execution-report" {
					t.Errorf("unexpected operation %s %s", req.Method, req.URL.Path)
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				raw, _ := json.Marshal(body)
				if len(body) != 6 || string(body["code"]) != `"skill_missing"` || string(body["expected_execution_revision"]) != "7" {
					t.Errorf("wrong report %s", raw)
				}
				if requests == 1 {
					firstKey, firstBody = req.Header.Get("Idempotency-Key"), string(raw)
				} else if req.Header.Get("Idempotency-Key") != firstKey || string(raw) != firstBody {
					t.Error("retry changed its key or fence")
				}
				if firstKey == "" {
					t.Error("missing idempotency key")
				}
				if result == "conflict" {
					out.WriteHeader(409)
					return
				}
				if result == "retry" && requests == 1 {
					out.WriteHeader(503)
					return
				}
				if result == "invalid_receipt" {
					materialTestResponse(out, map[string]any{"execution_revision": 99})
					return
				}
				materialTestResponse(out, map[string]any{"execution_revision": 8})
			}))
			defer srv.Close()
			w := newCommissionWatch(t, srv.URL, "commission_order")
			cfg, cfgErr := config.Load()
			if cfgErr != nil {
				t.Fatal(cfgErr)
			}
			if err := cfg.UpdateServerWithCommission(w.server.Name, w.server.Endpoint, w.server.StreamEndpoint, srv.URL); err != nil {
				t.Fatal(err)
			}
			w.server.CommissionEndpoint = srv.URL
			if err := w.journal.AddCommissionNotification(commissionTestNotification(81, 901, "42")); err != nil {
				t.Fatal(err)
			}
			job, _, _ := w.journal.NextKind("commission_order")
			if err := w.journal.SetCommissionExecution(job.ID, "901", 3, 7); err != nil {
				t.Fatal(err)
			}
			if err := w.journal.Update(job.ID, "needs_user", "commission_skill_missing", "", ""); err != nil {
				t.Fatal(err)
			}
			if result == "identity_changed" {
				cred, _ := auth.LoadV2Credentials(w.server.Name)
				cred.AgentID = "43"
				if err := auth.SaveV2Credentials(w.server.Name, cred); err != nil {
					t.Fatal(err)
				}
			}
			err := w.syncCommissionExecutionReports(context.Background())
			if result == "retry" {
				if err == nil || len(w.journal.CommissionExecutionReports()) != 1 {
					t.Fatal("failed upload lost report", err)
				}
				w.journal, err = dispatch.OpenJournal(*w.binding)
				if err != nil {
					t.Fatal(err)
				}
				err = w.syncCommissionExecutionReports(context.Background())
			}
			if result == "invalid_receipt" || result == "identity_changed" {
				if err == nil || len(w.journal.CommissionExecutionReports()) != 1 {
					t.Fatal("invalid upload acknowledged")
				}
				if result == "identity_changed" && requests != 0 {
					t.Fatal("cross-account report sent")
				}
			} else if err != nil || len(w.journal.CommissionExecutionReports()) != 0 {
				t.Fatal(err)
			}
			if state := w.journal.Snapshot()[0]; state.Status != "needs_user" || state.Code != "commission_skill_missing" {
				t.Fatal("report changed execution", state)
			}
		})
	}
}

func TestCommissionBlockerCopyExplainsLocalSkillAndUncertainty(t *testing.T) {
	_, body := desktopCommissionBlockerText("zh", "needs_user", "commission_skill_missing")
	if !strings.Contains(body, "本地缺少履约技能") || !strings.Contains(body, "订单时限") {
		t.Fatal(body)
	}
	_, body = desktopCommissionBlockerText("en", "unknown", "private /path token")
	if !strings.Contains(body, "Check existing results") || strings.Contains(body, "private") || strings.Contains(body, "token") {
		t.Fatal(body)
	}
}
