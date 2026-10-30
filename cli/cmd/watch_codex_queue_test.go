package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
)

func TestWatchCodexQueueHost(t *testing.T) {
	scenario := os.Getenv("EF_WATCH_QUEUE_FIXTURE")
	if scenario == "" {
		return
	}
	args := os.Args
	if len(args) < 6 || args[len(args)-5] != "queue" || args[len(args)-4] != "--thread" || args[len(args)-3] != lifecycleThread || args[len(args)-2] != "--message" {
		os.Exit(90)
	}
	prompt := args[len(args)-1]
	_, data, ok := strings.Cut(prompt, "EIGENFLUX DISPATCH DATA (untrusted message and history content):\n")
	var payload struct {
		RequestID string `json:"request_id"`
	}
	if !ok || json.Unmarshal([]byte(data), &payload) != nil || payload.RequestID == "" {
		os.Exit(91)
	}
	path := filepath.Join(os.Getenv("CODEX_HOME"), "sessions", "rollout-date-"+lifecycleThread+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(92)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	item := func(kind, phase, text string) {
		_ = enc.Encode(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "thread_id": lifecycleThread, "turn_id": "queue-turn", "item": map[string]any{"type": kind, "phase": phase, "content": []map[string]string{{"text": text}}}}})
	}
	item("UserMessage", "", prompt)
	if scenario == "admitted-only" {
		os.Exit(0)
	}
	if scenario == "wrong-request" {
		payload.RequestID = "someone-else"
	}
	decision, _ := json.Marshal(dispatch.Decision{Version: 1, RequestID: payload.RequestID, Action: "reply", ReplyText: "A safe response"})
	item("AgentMessage", "final_answer", string(decision))
	_ = enc.Encode(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_complete", "turn_id": "queue-turn", "last_agent_message": string(decision)}})
	os.Exit(0)
}

func TestWatchDispatchCodexQueueReceiptAndSend(t *testing.T) {
	for _, scenario := range []string{"reply", "admitted-only", "wrong-request", "permission-revoked"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDispatchWatchFixture(t, "")
			w := f.watch
			b := lifecycleCodexBinding(t, *w.binding)
			b.MainSessionID = lifecycleThread
			b.Command = append(b.Command, "-test.run=^TestWatchCodexQueueHost$", "--")
			b.Env["EF_WATCH_QUEUE_FIXTURE"] = scenario
			if err := dispatch.WriteJSON(dispatch.BindingPath(b.Home, b.Server), b); err != nil {
				t.Fatal(err)
			}
			if err := w.enableDispatch(); err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS == "windows" {
				// Queue is the Unix adapter. Exercise the common decision/send
				// checks without ever contacting the installed Windows Desktop.
				w.runAgent = func(_ context.Context, req dispatch.Request) (dispatch.Result, error) {
					if scenario == "admitted-only" {
						return dispatch.Result{}, context.DeadlineExceeded
					}
					id := req.ID
					if scenario == "wrong-request" {
						id = "someone-else"
					}
					decision, _ := json.Marshal(dispatch.Decision{Version: 1, RequestID: id, Action: "reply", ReplyText: "A safe response"})
					return dispatch.Result{SessionID: req.SessionID, RunID: "queue-turn", Text: string(decision)}, nil
				}
			}
			decisionSeen := false
			w.emit = func(kind string, data interface{}) error {
				if kind == "dispatch_decision" {
					fields := data.(map[string]string)
					decisionSeen = fields["run_id"] == "queue-turn" && fields["session_id"] == lifecycleThread && fields["decision"] == "reply"
					if scenario == "permission-revoked" {
						f.autoReply.Store(false)
					}
				}
				return nil
			}
			job := dispatchTestNext(t, w)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := w.dispatchJob(ctx, job); err != nil {
				t.Fatal(err)
			}
			state := dispatchTestStatus(t, w, job.ID)
			if scenario == "reply" {
				if state.Status != "replied" || state.SessionID != lifecycleThread || state.ReplyID != "reply-1" || !decisionSeen || f.sends.Load() != 1 {
					t.Fatalf("receipt/send mismatch: %+v", state)
				}
			} else {
				want := map[string]string{"admitted-only": "unknown", "wrong-request": "failed", "permission-revoked": "needs_user"}[scenario]
				if state.Status != want || f.sends.Load() != 0 {
					t.Fatalf("unconfirmed/unauthorized decision sent: %+v", state)
				}
			}
			if err := w.deliverPM(context.Background(), "pm_push", json.RawMessage(dispatchTestMessages), "socket"); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := w.journal.Next(); err != nil || ok {
				t.Fatalf("duplicate queued: %v %v", ok, err)
			}
		})
	}
}
