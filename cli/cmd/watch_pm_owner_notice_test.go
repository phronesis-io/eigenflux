package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/dispatch"
)

func TestWatchPMOwnerNoticeRoutesByHost(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		t.Run(host, func(t *testing.T) {
			ctx := context.Background()
			f := newDispatchWatchFixture(t, "")
			w := f.watch
			w.binding.Host = host
			if host == "codex" {
				w.binding.MainSessionID = "bound-main"
			}
			if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), w.binding); err != nil {
				t.Fatal(err)
			}
			mainCalls := 0
			w.runMainNotice = func(_ context.Context, prompt string) (dispatch.Result, error) {
				mainCalls++
				if !strings.Contains(prompt, "材料已齐") || strings.Contains(prompt, "Ignore routing") || strings.Contains(prompt, "A safe response") {
					t.Fatalf("incorrect presentation payload: %s", prompt)
				}
				return dispatch.Result{SessionID: "bound-main", RunID: "notice-turn", Text: "材料已齐"}, nil
			}
			for i := range 3 {
				w.runAgent = func(_ context.Context, req dispatch.Request) (dispatch.Result, error) {
					if host == "codex" && req.SessionID != "" {
						t.Fatal("Codex PM decision used an existing session")
					}
					d := dispatch.Decision{Version: 1, RequestID: req.ID, Action: "no_reply"}
					if i == 0 {
						d.Action, d.ReplyText = "reply", "A safe response"
					} else {
						d.OwnerNotice = &dispatch.PMOwnerNotice{Topic: "materials", Change: "ready", Summary: "材料已齐，请周四前确认。", NeedsAction: true}
					}
					if i == 2 && !strings.Contains(req.Prompt, "材料已齐") {
						t.Fatal("prior owner notice missing from decision context")
					}
					body, err := json.Marshal(d)
					return dispatch.Result{Text: string(body)}, err
				}
				raw := strings.ReplaceAll(dispatchTestMessages, "incoming-1", []string{"incoming-1", "incoming-2", "incoming-3"}[i])
				if err := w.deliverPM(ctx, "pm_push", json.RawMessage(raw), "socket"); err != nil {
					t.Fatal(err)
				}
				job, ok, err := w.journal.Next()
				if err != nil || !ok {
					t.Fatalf("no PM job: %v", err)
				}
				if err := w.dispatchJob(ctx, job); err != nil {
					t.Fatal(err)
				}
				if host == "codex" {
					if err := w.deliverCodexPMOwnerNotice(ctx); err != nil {
						t.Fatal(err)
					}
					if (i == 0 && mainCalls != 0) || (i > 0 && mainCalls != 1) {
						t.Fatalf("unexpected Codex notices after message %d: %d", i, mainCalls)
					}
				}
			}
			if f.sends.Load() != 1 {
				t.Fatalf("owner-only material reached peer: %d sends", f.sends.Load())
			}
			plan := heartbeatPlan{PlanMode: "full", Access: runtimeAccess{OnboardingState: "completed"}, ExecutionOrder: []string{"feed", "communication"}, CLIPrefix: "eigenflux"}
			if err := attachHeartbeatPMNotices(ctx, &plan, host); err != nil {
				t.Fatal(err)
			}
			if host == "codex" {
				if plan.PMNoticeToken != "" || slices.Contains(plan.ExecutionOrder, "communication") {
					t.Fatal("Codex Watch PM handling leaked into the heartbeat")
				}
				return
			}
			if !plan.WakeOnEmpty || slices.Contains(plan.ExecutionOrder, "communication") || !strings.Contains(renderHeartbeatPlanForAgent(plan), "heartbeat pm-digest --cycle "+plan.PMNoticeToken) {
				t.Fatalf("digest missing from plan: %+v", plan)
			}
			cmd, _, err := heartbeatCmd.Find([]string{"pm-digest"})
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Flags().Set("cycle", plan.PMNoticeToken); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Flags().Set("cycle", ""); cmd.SetOut(nil) })
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetContext(ctx)
			for _, want := range []string{"材料已齐", `"items":[]`} {
				if err := cmd.RunE(cmd, nil); err != nil || !strings.Contains(out.String(), want) {
					t.Fatalf("digest output: %s %v", out.String(), err)
				}
				out.Reset()
			}
			quiet := heartbeatPlan{PlanMode: "full", Access: plan.Access, ExecutionOrder: []string{"feed", "communication"}}
			if err := attachHeartbeatPMNotices(ctx, &quiet, host); err != nil || quiet.PMNoticeToken != "" || quiet.WakeOnEmpty || slices.Contains(quiet.ExecutionOrder, "communication") {
				t.Fatalf("empty spool woke the host or restored inbox polling: %+v %v", quiet, err)
			}
			if mainCalls != 0 {
				t.Fatal("non-Codex used main-session delivery")
			}
		})
	}
}
