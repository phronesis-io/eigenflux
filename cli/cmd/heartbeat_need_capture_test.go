package cmd

import (
	"context"
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
				if r.Method != "GET" || r.URL.Path != "/need-capture/pending" || r.URL.Query().Get("limit") != "10" {
					t.Errorf("unexpected query: %s", r.URL)
				}
				w.WriteHeader(tc.httpStatus)
				_, _ = io.WriteString(w, `{"code":0,"data":`+tc.body+`}`)
			}))
			defer server.Close()
			p := queryNeedCapturePlan(client.New(server.URL, "", "test", client.Meta{}), 2, map[string]int{})
			if calls != 1 || p.Status != tc.status || p.MaxIntents != 2 {
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
			seedNeedCaptureHistory(t)
			installHeartbeatTestRules(t, true)
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
			if !strings.Contains(plan.AgentPrompt, "Need capture is optional for cycle completion") {
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

func seedNeedCaptureHistory(t *testing.T) {
	t.Helper()
	scope, err := maintenanceScope(activeServerName())
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.MarkNeedCaptureStarted(scope); err != nil {
		t.Fatal(err)
	}
}

func recordExtractionFailure(t *testing.T, p heartbeatPlan, intent plannedNeedIntent) {
	t.Helper()
	scope, err := maintenanceScope(activeServerName())
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.RecordNeedCaptureResult(scope, p.NeedCapture.PlanID, intent.IntentID, intent.IntentVersion, "extraction_failed", "", ""); err != nil {
		t.Fatal(err)
	}
}

const threePendingFixture = `{"intents":[{"intent_id":"1","intent_version":1,"existing_inputs":[]},{"intent_id":"2","intent_version":1,"existing_inputs":[]},{"intent_id":"3","intent_version":1,"existing_inputs":[]}],"has_more":false}`

func needPlanAgainst(t *testing.T, body string) heartbeatPlan {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/need-capture/pending" || r.URL.Query().Get("limit") != "10" {
			t.Errorf("unexpected query: %s", r.URL)
		}
		_, _ = io.WriteString(w, `{"code":0,"data":`+body+`}`)
	}))
	t.Cleanup(server.Close)
	runtimeTestConfig(t, server.URL, true)
	return heartbeatPlan{}
}

func attachFresh(order []string) heartbeatPlan {
	p := heartbeatPlan{Access: runtimeAccess{OnboardingState: "completed"}, ExecutionOrder: append([]string{}, order...), CLIVersion: "1.0.27", SkillRevision: "test"}
	attachNeedCapturePlan(&p)
	return p
}

var fullOrder = []string{"commands", "need_capture", "feed", "attention"}

func TestNeedCaptureFirstCycleRunsAfterFeedWithOneIntent(t *testing.T) {
	needPlanAgainst(t, threePendingFixture)
	first := attachFresh(fullOrder)
	if !slices.Equal(first.ExecutionOrder, []string{"commands", "feed", "need_capture", "attention"}) {
		t.Fatalf("first cycle order: %v", first.ExecutionOrder)
	}
	if first.NeedCapture == nil || !first.NeedCapture.FirstCycle || first.NeedCapture.MaxIntents != 1 || len(first.NeedCapture.Intents) != 1 || !first.NeedCapture.HasMore {
		t.Fatalf("first cycle selection: %+v", first.NeedCapture)
	}
	if !strings.Contains(needCaptureInstructions(first), "after Feed") {
		t.Fatal("first-cycle instructions do not place Need after Feed")
	}
	// Delivery, rather than plan construction, makes the next cycle ordinary.
	if p := attachFresh(fullOrder); !p.NeedCapture.FirstCycle {
		t.Fatal("an undelivered plan consumed the first cycle")
	}
	seedNeedCaptureHistory(t)
	next := attachFresh(fullOrder)
	if !slices.Equal(next.ExecutionOrder, fullOrder) || next.NeedCapture.FirstCycle || next.NeedCapture.MaxIntents != 2 || len(next.NeedCapture.Intents) != 2 {
		t.Fatalf("ordinary cycle: %v %+v", next.ExecutionOrder, next.NeedCapture)
	}
	if strings.Contains(needCaptureInstructions(next), "120") {
		t.Fatal("time budget text remains")
	}
}

func TestNeedCaptureSkipsVersionsAfterThreeExtractionFailures(t *testing.T) {
	needPlanAgainst(t, threePendingFixture)
	seedNeedCaptureHistory(t)
	for i := 0; i < maintenance.NeedCaptureStrikeLimit; i++ {
		attachFresh(fullOrder) // Denied or interrupted: no extraction result.
	}
	for i := 0; i < maintenance.NeedCaptureStrikeLimit; i++ {
		p := attachFresh(fullOrder)
		if got := p.NeedCapture.Intents; len(got) != 2 || got[0].IntentID != "1" || got[1].IntentID != "2" {
			t.Fatalf("strike %d selection: %+v", i, got)
		}
		for _, intent := range p.NeedCapture.Intents {
			recordExtractionFailure(t, p, intent)
			recordExtractionFailure(t, p, intent) // Same cycle counts once.
		}
	}
	p := attachFresh(fullOrder)
	got := p.NeedCapture.Intents
	if len(got) != 1 || got[0].IntentID != "3" || p.NeedCapture.Skipped != 2 {
		t.Fatalf("struck versions not skipped: %+v", p.NeedCapture)
	}
}

func TestNeedCaptureConfirmedVersionsEarnNoStrikes(t *testing.T) {
	needPlanAgainst(t, threePendingFixture)
	seedNeedCaptureHistory(t)
	scope, _ := maintenanceScope(activeServerName())
	for i := 0; i < maintenance.NeedCaptureStrikeLimit+1; i++ {
		p := attachFresh(fullOrder)
		if len(p.NeedCapture.Intents) == 0 || p.NeedCapture.Intents[0].IntentID != "1" {
			t.Fatalf("confirmed version was skipped at %d: %+v", i, p.NeedCapture)
		}
		if err := maintenance.RecordNeedCaptureResult(scope, p.NeedCapture.PlanID, "1", 1, "confirmed", "captured", ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNeedCaptureNewIntentVersionRestoresEligibility(t *testing.T) {
	body := threePendingFixture
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"data":`+body+`}`)
	}))
	t.Cleanup(server.Close)
	runtimeTestConfig(t, server.URL, true)
	seedNeedCaptureHistory(t)
	for i := 0; i < maintenance.NeedCaptureStrikeLimit; i++ {
		p := attachFresh(fullOrder)
		for _, intent := range p.NeedCapture.Intents {
			recordExtractionFailure(t, p, intent)
		}
	}
	if p := attachFresh(fullOrder); len(p.NeedCapture.Intents) != 1 || p.NeedCapture.Intents[0].IntentID != "3" {
		t.Fatalf("precondition: struck versions still selected: %+v", p.NeedCapture)
	}
	// Editing Intent 1 creates version 2, which has no strikes.
	body = `{"intents":[{"intent_id":"1","intent_version":2,"existing_inputs":[]},{"intent_id":"2","intent_version":1,"existing_inputs":[]}],"has_more":false}`
	p := attachFresh(fullOrder)
	if len(p.NeedCapture.Intents) != 1 || p.NeedCapture.Intents[0].IntentID != "1" || p.NeedCapture.Intents[0].IntentVersion != 2 || p.NeedCapture.Skipped != 1 {
		t.Fatalf("new version not eligible: %+v", p.NeedCapture)
	}
}

func TestCodexNeedCapturePolicyDoesNotAdmitPrompt(t *testing.T) {
	for _, result := range []string{"allow", "prompt", "forbidden", "unknown"} {
		t.Run(result, func(t *testing.T) {
			calls := 0
			run := func(_ context.Context, _ string, _ []string) ([]byte, error) {
				calls++
				decision := "allow"
				if calls == 2 {
					decision = result
				}
				return []byte(`{"decision":"` + decision + `","matchedRules":[{}]}`), nil
			}
			if got := codexNeedPolicyAllows(context.Background(), "codex", nil, []string{"eigenflux"}, run); got != (result == "allow") {
				t.Fatalf("policy %s admitted=%v", result, got)
			}
		})
	}
}

func TestNeedCapturePendingTimeoutAndUnauthorizedNeverRefresh(t *testing.T) {
	for _, status := range []int{0, http.StatusUnauthorized} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if status == 0 {
				<-r.Context().Done()
				return
			}
			w.WriteHeader(status)
		}))
		runtimeTestConfig(t, srv.URL, true)
		scope, err := maintenanceScope(activeServerName())
		if err != nil {
			t.Fatal(err)
		}
		c, err := newNeedCaptureClient(scope, client.Meta{}, needCaptureQueryTimeout)
		if err != nil {
			t.Fatal(err)
		}
		if c.HTTPClient.Timeout != needCaptureQueryTimeout {
			t.Fatal("missing Pending timeout")
		}
		c.HTTPClient.Timeout = 75 * time.Millisecond
		p := queryNeedCapturePlan(c, 1, nil)
		srv.Close()
		if p.Status != "failed" || calls != 1 {
			t.Fatalf("status=%d: plan=%+v calls=%d", status, p, calls)
		}
	}
}

func TestNeedCapturePrioritizesIntentWithoutCurrentNeed(t *testing.T) {
	needPlanAgainst(t, `{"intents":[{"intent_id":"1","intent_version":1,"existing_inputs":[{"need_input_id":"8","need_type":"broadcast"}]},{"intent_id":"2","intent_version":1,"existing_inputs":[]}],"has_more":false}`)
	p := attachFresh(fullOrder)
	if len(p.NeedCapture.Intents) != 1 || p.NeedCapture.Intents[0].IntentID != "2" {
		t.Fatalf("first slot went to an Intent with a Need: %+v", p.NeedCapture)
	}
}
