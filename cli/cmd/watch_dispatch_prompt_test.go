package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/dispatch"
)

func TestWatchPMPromptRecoveryAndFailureEvidence(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failures    int32
		status      int32
		wantHistory int32
		wantCode    string
	}{
		{"transient_recovers", 2, 503, 3, ""},
		{"bounded_failure", 9, 503, 3, "prompt_history_unavailable_http_503"},
		{"forbidden_not_retried", 9, 403, 1, "prompt_history_unavailable_http_403"},
		{"rate_limit_not_retried", 9, 429, 1, "prompt_history_unavailable_http_429"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDispatchWatchFixture(t, "")
			f.historyFailures.Store(tc.failures)
			f.historyStatus.Store(tc.status)
			calls := 0
			f.watch.runAgent = func(_ context.Context, req dispatch.Request) (dispatch.Result, error) {
				calls++
				return dispatchTestDecision(req, "reply"), nil
			}
			job := dispatchTestNext(t, f.watch)
			if err := f.watch.dispatchJob(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			state := dispatchTestStatus(t, f.watch, job.ID)
			if f.histories.Load() != tc.wantHistory {
				t.Fatalf("history attempts: %d", f.histories.Load())
			}
			if tc.wantCode == "" {
				if state.Status != "replied" || calls != 1 || f.sends.Load() != 1 {
					t.Fatalf("not exactly one execution/send: %+v calls=%d", state, calls)
				}
			} else {
				if state.Status != "failed" || state.Code != tc.wantCode || calls != 0 || f.sends.Load() != 0 {
					t.Fatalf("wrong failure: %+v calls=%d", state, calls)
				}
				raw, _ := json.Marshal(state)
				if strings.Contains(string(raw), "private diagnostic") {
					t.Fatal("raw error leaked")
				}
				persisted, err := dispatch.ReadJournalStatus(*f.watch.binding)
				if err != nil {
					t.Fatal(err)
				}
				if len(persisted) != 1 || persisted[0].Code != tc.wantCode {
					t.Fatalf("failure evidence not durable: %+v", persisted)
				}
			}
			if err := f.watch.deliverPM(context.Background(), "pm_push", json.RawMessage(dispatchTestMessages), "http_poll"); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := f.watch.journal.Next(); err != nil || ok {
				t.Fatalf("duplicate replay: %v %v", ok, err)
			}
		})
	}
}

func TestWatchPMPromptModifiedSkillsIsDiagnosable(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	path := filepath.Join(f.watch.binding.SkillsDir, "ef-profile", "SKILL.md")
	if err := os.WriteFile(path, []byte("local edit must remain intact"), 0600); err != nil {
		t.Fatal(err)
	}
	f.watch.runAgent = func(context.Context, dispatch.Request) (dispatch.Result, error) {
		t.Fatal("Agent invoked with modified rules")
		return dispatch.Result{}, nil
	}
	job := dispatchTestNext(t, f.watch)
	if err := f.watch.dispatchJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	state := dispatchTestStatus(t, f.watch, job.ID)
	if state.Code != "skills_local_modified" || state.Status != "failed" || f.histories.Load() != 0 {
		t.Fatalf("lost root cause: %+v", state)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "local edit must remain intact" {
		t.Fatal("local edit overwritten")
	}
}

func TestWatchPMPromptRetryCancellation(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	f.historyFailures.Store(9)
	f.historyStatus.Store(http.StatusServiceUnavailable)
	ctx, cancel := context.WithCancel(context.Background())
	f.onHistory = cancel
	job := dispatchTestNext(t, f.watch)
	started := time.Now()
	if err := f.watch.dispatchJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	state := dispatchTestStatus(t, f.watch, job.ID)
	if state.Code != "prompt_cancelled" || f.histories.Load() != 1 || time.Since(started) > time.Second {
		t.Fatalf("cancellation did not stop retries: %+v", state)
	}
}

func TestWatchPMPromptRetryRechecksAuthorization(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	f.historyFailures.Store(1)
	f.onHistory = func() { f.autoReply.Store(false) }
	f.watch.runAgent = func(context.Context, dispatch.Request) (dispatch.Result, error) {
		t.Fatal("Agent invoked after permission revocation")
		return dispatch.Result{}, nil
	}
	job := dispatchTestNext(t, f.watch)
	if err := f.watch.dispatchJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	state := dispatchTestStatus(t, f.watch, job.ID)
	if state.Status != "needs_user" || state.Code != "automatic_reply_not_authorized" || f.histories.Load() != 1 || f.sends.Load() != 0 {
		t.Fatalf("authorization bypassed during retry: %+v", state)
	}
}

func TestFreshControlContextRejectsBusinessErrorWithValidData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":42,"data":{"context_revision":1,"control_context":{"security_boundary":{"auto_reply_pm":true}}}}`))
	}))
	defer server.Close()
	c := client.New(server.URL, "", "", client.Meta{})
	if _, err := freshControlContext(c, "fixture", "agent-1", false); err == nil {
		t.Fatal("rejected control response authorized automatic reply")
	}
}
