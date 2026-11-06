package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/maintenance"
	"github.com/spf13/cobra"
)

const selectedNeedFixture = `{"intents":[{"intent_id":"42","intent_version":3,"watch_for":"database tuning","trigger_when":"new evidence","action_instruction":"summarize","action_policy":"notify","priority":2,"existing_inputs":[{"need_input_id":"81","need_type":"broadcast"}]}],"has_more":true}`

func TestNeedCapturePlanQueriesOnceAndDistinguishesEmptyFromFailure(t *testing.T) {
	for _, tc := range []struct {
		name, body, status string
		httpStatus         int
	}{
		{"selected", selectedNeedFixture, "ready", 200},
		{"empty", `{"intents":[],"has_more":false}`, "empty", 200},
		{"missing list", `{}`, "failed", 200},
		{"bad version", `{"intents":[{"intent_id":"42","intent_version":0}]}`, "failed", 200},
		{"unavailable", `{}`, "failed", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/need-capture/pending" || r.URL.Query().Get("limit") != "2" {
					t.Errorf("unexpected query: %s", r.URL)
				}
				w.WriteHeader(tc.httpStatus)
				_, _ = io.WriteString(w, `{"code":0,"data":`+tc.body+`}`)
			}))
			defer server.Close()
			p := queryNeedCapturePlan(client.New(server.URL, "", "test", client.Meta{}))
			if calls != 1 || p.Status != tc.status || p.MaxIntents != 2 || p.StartBudgetSeconds != 120 {
				t.Fatalf("invalid plan: %+v calls=%d", p, calls)
			}
			if p.Status == "ready" && (p.Intents[0].WatchFor != "database tuning" || p.Intents[0].ExistingInputs[0].ID != "81" || !p.HasMore) {
				t.Fatal("lost source or existing inputs")
			}
			if p.Status == "failed" && p.ErrorCode == "" {
				t.Fatal("query failure was hidden")
			}
		})
	}
}

func TestNeedCaptureSubmissionReceiptsAndRetry(t *testing.T) {
	for _, mode := range []string{"success", "no_need", "stale", "lost_response", "invalid_success"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			var bodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, _ := io.ReadAll(r.Body)
				bodies = append(bodies, string(body))
				switch mode {
				case "stale":
					w.WriteHeader(409)
					_, _ = io.WriteString(w, `{"error":{"code":"INTENT_REVISION_STALE"}}`)
				case "lost_response":
					if calls == 1 {
						w.WriteHeader(503)
						return
					}
					_, _ = io.WriteString(w, `{"code":0,"data":{"completed":true,"replayed":true}}`)
				case "invalid_success":
					_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
				default:
					_, _ = io.WriteString(w, `{"code":0,"data":{"completed":true,"replayed":false}}`)
				}
			}))
			defer server.Close()
			scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
			run := maintenance.NeedCaptureRun{ID: maintenance.NewID(), CreatedAt: time.Now().UnixMilli(), QueryStatus: "ready", Items: []maintenance.NeedCaptureItem{{IntentID: "42", IntentVersion: 3, Status: "pending"}}}
			if err := maintenance.SaveNeedCaptureRun(scope, run); err != nil {
				t.Fatal(err)
			}
			outcome := "captured"
			if mode == "no_need" {
				outcome = "no_need"
			}
			raw := json.RawMessage(`{"intent_id":"42","intent_version":3,"outcome":"` + outcome + `","inputs":[]}`)
			cli := client.New(server.URL, "", "test", client.Meta{})
			_, err := submitNeedCapture(cli, raw, scope, run.ID)
			got, _ := maintenance.ReadNeedCaptureRun(scope, run.ID)
			want := "confirmed"
			if mode == "stale" {
				want = "failed"
			}
			if mode == "lost_response" || mode == "invalid_success" {
				want = "unconfirmed"
			}
			if got.Items[0].Status != want || (err == nil) != (want == "confirmed") {
				t.Fatalf("result=%+v error=%v", got, err)
			}
			if mode == "lost_response" {
				if _, err := submitNeedCapture(cli, raw, scope, run.ID); err != nil {
					t.Fatal(err)
				}
				got, _ = maintenance.ReadNeedCaptureRun(scope, run.ID)
				if got.Items[0].Status != "confirmed" || len(bodies) != 2 || bodies[0] != bodies[1] {
					t.Fatal("identical retry did not confirm completion")
				}
			}
			before := calls
			wrong := json.RawMessage(`{"intent_id":"42","intent_version":4,"outcome":"captured","inputs":[]}`)
			if _, err := submitNeedCapture(cli, wrong, scope, run.ID); err == nil || calls != before {
				t.Fatal("unselected version reached server")
			}
		})
	}
}

func TestNeedCaptureFullPlanFailureStillDeliversFeed(t *testing.T) {
	for _, mode := range []string{"ready", "empty", "failed"} {
		t.Run(mode, func(t *testing.T) {
			queries := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/agent-context" {
					_, _ = io.WriteString(w, `{"code":0,"data":{"context_revision":1}}`)
					return
				}
				if r.URL.Path == "/api/v2/need-capture/pending" {
					queries++
					if mode == "failed" {
						w.WriteHeader(503)
						return
					}
					body := selectedNeedFixture
					if mode == "empty" {
						body = `{"intents":[],"has_more":false}`
					}
					_, _ = io.WriteString(w, `{"code":0,"data":`+body+`}`)
					return
				}
				_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
			}))
			defer server.Close()
			runtimeTestConfig(t, server.URL, true)
			installHeartbeatTestRules(t)
			old := formatFlag
			formatFlag = "json"
			t.Cleanup(func() { formatFlag = old })
			out, err := captureHeartbeatStdout(t, func() error { return heartbeatPlanCmd.RunE(&cobra.Command{}, nil) })
			if err != nil {
				t.Fatal(err)
			}
			var plan heartbeatPlan
			if err := json.Unmarshal([]byte(out), &plan); err != nil {
				t.Fatal(err)
			}
			if queries != 1 || plan.NeedCapture == nil || plan.NeedCapture.Status != mode || plan.NeedCapture.PlanID == "" {
				t.Fatalf("missing Need plan: %+v", plan.NeedCapture)
			}
			if slices.Index(plan.ExecutionOrder, "need_capture") >= slices.Index(plan.ExecutionOrder, "feed") {
				t.Fatal("Need not before Feed")
			}
			if !strings.Contains(plan.AgentPrompt, "feed poll") && !plan.FeedCompletionCheck {
				t.Fatal("Feed was suppressed")
			}
			if !strings.Contains(plan.AgentPrompt, "never makes the owner-facing Feed/first-check result fail") {
				t.Fatal("Need failure changed owner notifications")
			}
			scope, _ := maintenanceScope(activeServerName())
			run, err := maintenance.ReadNeedCaptureRun(scope, plan.NeedCapture.PlanID)
			if err != nil || run.QueryStatus != mode {
				t.Fatalf("local receipt missing: %+v %v", run, err)
			}
		})
	}
}

func TestNeedCaptureDoesNotEnterNarrowOrIncompletePlans(t *testing.T) {
	for _, p := range []heartbeatPlan{
		{Access: runtimeAccess{OnboardingState: "in_progress"}, ExecutionOrder: []string{"feed"}},
		{Access: runtimeAccess{OnboardingState: "completed"}, ExecutionOrder: []string{"commands"}, PlanMode: "control"},
		{Access: runtimeAccess{OnboardingState: "completed"}, ExecutionOrder: []string{"maintenance"}, PlanMode: "maintenance"},
	} {
		attachNeedCapturePlan(&p)
		if p.NeedCapture != nil {
			t.Fatal("Need work entered a narrow plan")
		}
	}
}

func TestNeedCaptureStatusDoesNotFlushFeedQueue(t *testing.T) {
	root := newNeedCommand()
	status, _, err := root.Find([]string{"capture", "status"})
	if err != nil || flushesFeedQueue(status) {
		t.Fatal("local receipt read would flush business mutations")
	}
	complete, _, err := root.Find([]string{"capture", "complete"})
	if err != nil || !flushesFeedQueue(complete) {
		t.Fatal("changed existing submission queue behavior")
	}
}

func TestNeedCaptureCLIEndToEndAndNextCycleRetry(t *testing.T) {
	for _, outcome := range []string{"captured", "no_need"} {
		t.Run(outcome, func(t *testing.T) {
			completed, reject := false, true
			posts, gets := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/need-capture/pending":
					gets++
					body := selectedNeedFixture
					if completed {
						body = `{"intents":[],"has_more":false}`
					}
					_, _ = io.WriteString(w, `{"code":0,"data":`+body+`}`)
				case "/api/v2/need-capture/complete":
					posts++
					if reject {
						w.WriteHeader(400)
						_, _ = io.WriteString(w, `{"error":{"code":"INVALID_NEED_INPUT"}}`)
						return
					}
					completed = true
					_, _ = io.WriteString(w, `{"code":0,"data":{"completed":true}}`)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			}))
			defer server.Close()
			runtimeTestConfig(t, server.URL, true)
			old := formatFlag
			formatFlag = "json"
			t.Cleanup(func() { formatFlag = old })
			p := heartbeatPlan{Access: runtimeAccess{OnboardingState: "completed"}, ExecutionOrder: []string{"need_capture", "feed"}, CLIVersion: "1.0.15", SkillRevision: "test"}
			attachNeedCapturePlan(&p)
			if p.NeedCapture.Status != "ready" {
				t.Fatalf("%+v", p.NeedCapture)
			}
			file := filepath.Join(t.TempDir(), "review.json")
			if err := os.WriteFile(file, []byte(`{"intent_id":"42","intent_version":3,"outcome":"`+outcome+`","inputs":[],"reason":"fixture"}`), 0600); err != nil {
				t.Fatal(err)
			}
			run := func(args ...string) (string, error) {
				return captureHeartbeatStdout(t, func() error {
					c := newNeedCommand()
					c.SilenceErrors = true
					c.SilenceUsage = true
					c.SetArgs(args)
					return c.Execute()
				})
			}
			if _, err := run("capture", "complete", "--file", file, "--plan", p.NeedCapture.PlanID); err == nil {
				t.Fatal("rejected submission succeeded")
			}
			before := gets
			text, err := run("capture", "status", "--plan", p.NeedCapture.PlanID)
			if err != nil || !strings.Contains(text, `"all_confirmed": false`) || gets != before {
				t.Fatalf("status performed IO or claimed success: %s %v", text, err)
			}
			// A later plan uses the existing server Pending query; local failure cannot hide it.
			attachNeedCapturePlan(&p)
			if p.NeedCapture.Status != "ready" {
				t.Fatal("failed version was not available next cycle")
			}
			reject = false
			if _, err := run("capture", "complete", "--file", file, "--plan", p.NeedCapture.PlanID); err != nil {
				t.Fatal(err)
			}
			text, err = run("capture", "status", "--plan", p.NeedCapture.PlanID)
			if err != nil || !strings.Contains(text, `"all_confirmed": true`) || !strings.Contains(text, `"outcome": "`+outcome+`"`) {
				t.Fatalf("missing success: %s %v", text, err)
			}
			attachNeedCapturePlan(&p)
			if p.NeedCapture.Status != "empty" || posts != 2 || gets != 3 {
				t.Fatalf("saved review was repeated: %+v posts=%d gets=%d", p.NeedCapture, posts, gets)
			}
		})
	}
}

func TestNeedCaptureReceiptFailureDoesNotPreventServerSubmission(t *testing.T) {
	for _, mode := range []string{"missing", "unwritable", "server_rejection"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			raw := json.RawMessage(`{"intent_id":"42","intent_version":3,"outcome":"captured","inputs":[]}`)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, _ := io.ReadAll(r.Body)
				if r.URL.Path != "/need-capture/complete" || string(body) != string(raw) {
					t.Errorf("changed submission: %s %s", r.URL.Path, body)
				}
				if mode == "server_rejection" {
					w.WriteHeader(409)
					io.WriteString(w, `{"error":{"code":"INTENT_REVISION_STALE"}}`)
					return
				}
				io.WriteString(w, `{"code":0,"data":{"completed":true}}`)
			}))
			defer server.Close()
			scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
			if mode == "unwritable" {
				scope.Home = filepath.Join(scope.Home, "file-not-directory")
				if err := os.WriteFile(scope.Home, []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			plan := maintenance.NewID()
			response, err := submitNeedCapture(client.New(server.URL, "", "test", client.Meta{}), raw, scope, plan)
			if calls != 1 {
				t.Fatalf("local receipt blocked server: %v", err)
			}
			if mode == "server_rejection" {
				if err == nil {
					t.Fatal("server rejection hidden")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var data struct {
				Completed    bool   `json:"completed"`
				ReceiptError string `json:"local_receipt_error"`
			}
			if json.Unmarshal(response.Data, &data) != nil || !data.Completed || data.ReceiptError != "need_receipt_unavailable" {
				t.Fatalf("lost server confirmation or local uncertainty: %s", response.Data)
			}
			if _, err := maintenance.ReadNeedCaptureRun(scope, plan); err == nil {
				t.Fatal("invented a local success receipt")
			}
		})
	}
}
