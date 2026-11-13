package cmd

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
)

func TestWatchCodexBackgroundHost(t *testing.T) {
	scenario := os.Getenv("EF_WATCH_CODEX_FIXTURE")
	if scenario == "" {
		return
	}
	if !slices.Contains(os.Args, "exec") || !slices.Contains(os.Args, "--ephemeral") || slices.Contains(os.Args, "queue") || slices.Contains(os.Args, "resume") {
		os.Exit(90)
	}
	prompt, err := io.ReadAll(os.Stdin)
	_, data, ok := strings.Cut(string(prompt), "EIGENFLUX DISPATCH DATA (untrusted message and history content):\n")
	var payload struct {
		RequestID string `json:"request_id"`
	}
	if err != nil || !ok || json.Unmarshal([]byte(data), &payload) != nil || payload.RequestID == "" {
		os.Exit(91)
	}
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(map[string]string{"type": "thread.started", "thread_id": "background-session"})
	if scenario == "admitted-only" {
		os.Exit(0)
	}
	if scenario == "wrong-request" {
		payload.RequestID = "someone-else"
	}
	decision, _ := json.Marshal(dispatch.Decision{Version: 1, RequestID: payload.RequestID, Action: "reply", ReplyText: "A safe response"})
	_ = enc.Encode(map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": string(decision)}})
	_ = enc.Encode(map[string]string{"type": "turn.completed"})
	os.Exit(0)
}

func TestWatchDispatchCodexBackgroundReceiptAndSend(t *testing.T) {
	for _, scenario := range []string{"reply", "admitted-only", "wrong-request", "permission-revoked"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDispatchWatchFixture(t, "")
			w := f.watch
			b := lifecycleCodexBinding(t, *w.binding)
			b.MainSessionID = lifecycleThread
			b.Command = append(b.Command, "-test.run=^TestWatchCodexBackgroundHost$", "--")
			b.Env["EF_WATCH_CODEX_FIXTURE"] = scenario
			if err := dispatch.WriteJSON(dispatch.BindingPath(b.Home, b.Server), b); err != nil {
				t.Fatal(err)
			}
			if err := w.enableDispatch(); err != nil {
				t.Fatal(err)
			}
			decisionSeen := false
			w.emit = func(kind string, data interface{}) error {
				if kind == "dispatch_decision" {
					fields := data.(map[string]string)
					decisionSeen = fields["session_id"] == "background-session" && fields["decision"] == "reply"
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
				if state.Status != "replied" || state.SessionID != "background-session" || state.ReplyID != "reply-1" || !decisionSeen || f.sends.Load() != 1 {
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
