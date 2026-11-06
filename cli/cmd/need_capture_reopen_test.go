package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/maintenance"
)

func TestNeedCaptureSelectedReopenCLI(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/api/v2/need-capture/review":
			if r.Method != "GET" || r.URL.Query().Get("intent_id") != "42" || r.URL.Query().Get("intent_version") != "3" {
				t.Errorf("wrong review query: %s", r.URL)
			}
			io.WriteString(w, `{"code":0,"data":{"intent_id":"42","intent_version":3,"capture_revision":0,"state":"no_need"}}`)
		case "/api/v2/need-capture/reopen":
			var body map[string]any
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 3 || body["intent_id"] != "42" || body["intent_version"] != float64(3) || body["expected_capture_revision"] != float64(0) {
				t.Errorf("wrong reopen request: %+v", body)
			}
			io.WriteString(w, `{"code":0,"data":{"review":{"intent_id":"42","intent_version":3,"capture_revision":1,"state":"pending"},"replayed":false}}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	defer srv.Close()
	runtimeTestConfig(t, srv.URL, true)
	for _, args := range [][]string{{"review", "--intent-id", "42", "--intent-version", "3"}, {"reopen", "--intent-id", "42", "--intent-version", "3", "--expected-capture-revision", "0"}} {
		c := newNeedCaptureCommand()
		c.SetArgs(args)
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	for _, args := range [][]string{
		{"reopen", "--intent-id", "42", "--intent-version", "3"},
		{"reopen", "--intent-id", "42", "--intent-version", "3", "--expected-capture-revision", "-1"},
		{"review", "--intent-id", "042", "--intent-version", "3"},
		{"review", "--intent-id", "42", "--intent-version", "0"},
	} {
		c := newNeedCaptureCommand()
		c.SetArgs(args)
		if c.Execute() == nil {
			t.Fatal(args)
		}
	}
	if calls != 2 {
		t.Fatal("invalid selection reached server")
	}
}

func TestNeedCapturePlanAndReceiptsPinReopenedRevision(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/need-capture/pending" {
			io.WriteString(w, `{"code":0,"data":`+strings.Replace(selectedNeedFixture, `"intent_version":3`, `"intent_version":3,"capture_revision":2`, 1)+`}`)
			return
		}
		calls++
		io.WriteString(w, `{"code":0,"data":{"completed":true}}`)
	}))
	defer srv.Close()
	cli := client.New(srv.URL, "", "test", client.Meta{})
	plan := queryNeedCapturePlan(cli)
	if plan.Status != "ready" || plan.Intents[0].CaptureRevision != 2 {
		t.Fatalf("lost revision: %+v", plan)
	}
	scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	run := maintenance.NeedCaptureRun{ID: maintenance.NewID(), CreatedAt: time.Now().UnixMilli(), QueryStatus: "ready", Items: []maintenance.NeedCaptureItem{{IntentID: "42", IntentVersion: 3, CaptureRevision: 2, Status: "pending"}}}
	if err := maintenance.SaveNeedCaptureRun(scope, run); err != nil {
		t.Fatal(err)
	}
	old := json.RawMessage(`{"intent_id":"42","intent_version":3,"outcome":"captured","inputs":[]}`)
	if _, err := submitNeedCapture(cli, old, scope, run.ID); err == nil || calls != 0 {
		t.Fatal("old review escaped selected revision validation")
	}
	current := json.RawMessage(strings.Replace(string(old), `"intent_version":3`, `"intent_version":3,"capture_revision":2`, 1))
	if _, err := submitNeedCapture(cli, current, scope, run.ID); err != nil {
		t.Fatal(err)
	}
	got, err := maintenance.ReadNeedCaptureRun(scope, run.ID)
	if err != nil || calls != 1 || got.Items[0].CaptureRevision != 2 || got.Items[0].Status != "confirmed" {
		t.Fatalf("%+v %v", got, err)
	}
}
