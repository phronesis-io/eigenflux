package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cli.eigenflux.ai/internal/dispatch"
)

func TestWatchMainSessionRoutingAndReceipt(t *testing.T) {
	for _, actual := range []string{"bound-main", "wrong-session", ""} {
		t.Run("result-"+actual, func(t *testing.T) {
			f := newDispatchWatchFixture(t, "")
			w := f.watch
			w.binding.Host, w.binding.Mode = "codex", "native"
			w.binding.MainSessionID = "bound-main"
			if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), w.binding); err != nil {
				t.Fatal(err)
			}
			started := false
			w.emit = func(kind string, data interface{}) error {
				if kind == "dispatch_started" {
					value := data.(map[string]string)
					if value["session_id"] != "bound-main" || value["main_session_id"] != "bound-main" {
						t.Fatal(value)
					}
					started = true
				}
				return nil
			}
			w.runAgent = func(_ context.Context, r dispatch.Request) (dispatch.Result, error) {
				if r.SessionID != "bound-main" || !started {
					t.Fatal("did not dispatch to the bound main session")
				}
				out := dispatchTestDecision(r, "reply")
				out.SessionID = actual
				return out, nil
			}
			job := dispatchTestNext(t, w)
			if err := w.dispatchJob(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			got := dispatchTestStatus(t, w, job.ID)
			if actual == "bound-main" {
				if f.sends.Load() != 1 || got.Status != "replied" || got.SessionID != actual {
					t.Fatalf("missing receipt: %+v", got)
				}
				if err := w.deliverPM(context.Background(), "pm_push", json.RawMessage(dispatchTestMessages), "http_poll"); err != nil {
					t.Fatal(err)
				}
				if _, ok, err := w.journal.Next(); err != nil || ok {
					t.Fatalf("duplicate queued: %v", err)
				}
			} else if f.sends.Load() != 0 || got.Code != "main_session_mismatch" || got.Status != "unknown" {
				t.Fatalf("mismatched session sent: %+v", got)
			}
		})
	}
}

func TestWatchMainSessionUpdateInvalidatesRunningBinding(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	b := *f.watch.binding
	b.MainSessionID = "replacement"
	if err := dispatch.WriteJSON(dispatch.BindingPath(b.Home, b.Server), b); err != nil {
		t.Fatal(err)
	}
	if err := f.watch.dispatchIdentityOK(); err == nil {
		t.Fatal("running watcher ignored a route change")
	}
}

func TestWatchSessionCommandPreservesPendingWorkAndRejectsStaleUpdates(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	b := *f.watch.binding
	b.Mode, b.Host = "native", "codex"
	if err := dispatch.WriteJSON(dispatch.BindingPath(b.Home, b.Server), b); err != nil {
		t.Fatal(err)
	}
	job := dispatchTestNext(t, f.watch)
	home := t.TempDir()
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id := "01a0d200-9d41-7cd2-9214-3f04761910c4"
	if err := os.WriteFile(filepath.Join(dir, "rollout-date-"+id+".jsonl"), []byte(`{"type":"session_meta","payload":{"id":"`+id+`"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_THREAD_ID", id)
	command, _ := bindingTestCommand()
	command.Flags().String("main-session-id", "", "")
	command.Flags().Bool("current-session", false, "")
	command.Flags().String("codex-home", "", "")
	command.Flags().String("expected-id", "", "")
	command.Flags().String("expected-revision", "", "")
	for key, value := range map[string]string{"current-session": "true", "codex-home": home, "expected-id": "", "expected-revision": b.Revision} {
		if err := command.Flags().Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := updateWatchMainSession(command); err != nil {
		t.Fatal(err)
	}
	got, err := dispatch.ReadBinding(b.Home, b.Server)
	if err != nil || got.MainSessionID != id || got.Revision != b.Revision {
		t.Fatalf("binding: %+v %v", got, err)
	}
	jobs, err := dispatch.ReadJournalStatus(got)
	if err != nil || len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("lost existing journal: %+v %v", jobs, err)
	}
	if err := updateWatchMainSession(command); err == nil {
		t.Fatal("stale update accepted")
	}
}
