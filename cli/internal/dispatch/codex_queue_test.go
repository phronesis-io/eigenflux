package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func codexQueueFixture(scenario string) {
	args := os.Args
	queue := -1
	for i, a := range args {
		if a == "queue" {
			queue = i
			break
		}
	}
	if queue < 0 || len(args) != queue+5 || args[queue+1] != "--thread" || args[queue+2] != mainTestID || args[queue+3] != "--message" {
		os.Exit(91)
	}
	fmt.Println("Queued message fixture for thread " + mainTestID)
	if marker := os.Getenv("EF_QUEUE_MARKER"); marker != "" {
		_ = os.WriteFile(marker, nil, 0600)
	}
	if scenario == "codex-queue-only" {
		return
	}
	b := Binding{Mode: "native", Host: "codex", MainSessionID: mainTestID, Env: map[string]string{"CODEX_HOME": os.Getenv("CODEX_HOME")}}
	path, err := b.mainSessionPath()
	if err != nil {
		os.Exit(92)
	}
	if scenario == "codex-queue-replaced" {
		_ = os.Rename(path, path+".previous")
		_ = os.WriteFile(path, []byte("{}\n"), 0600)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(93)
	}
	defer f.Close()
	id := mainTestID
	if scenario == "codex-wrong-main" {
		id = "01a0d200-9d41-7cd2-9214-3f04761910c5"
	}
	encode := func(payload map[string]any) {
		_ = json.NewEncoder(f).Encode(map[string]any{"type": "event_msg", "payload": payload})
	}
	// Another turn's successful result must never become this request's receipt.
	encode(queueItem("neighbor", mainTestID, "AgentMessage", "final_answer", "unrelated"))
	encode(map[string]any{"type": "task_complete", "turn_id": "neighbor", "last_agent_message": "unrelated"})
	encode(queueItem("own-turn", id, "UserMessage", "", args[queue+4]))
	turn := "own-turn"
	if scenario == "codex-queue-wrong-turn" {
		turn = "neighbor"
	}
	encode(queueItem(turn, id, "AgentMessage", "commentary", "not a decision"))
	encode(queueItem(turn, id, "AgentMessage", "final_answer", "decision"))
	if scenario != "codex-queue-partial" {
		encode(map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": "decision"})
	}
}

func queueItem(turn, thread, kind, phase, text string) map[string]any {
	return map[string]any{"type": "item_completed", "thread_id": thread, "turn_id": turn,
		"item": map[string]any{"type": kind, "phase": phase, "content": []map[string]string{{"type": "text", "text": text}}}}
}

func TestCodexQueueRequiresCompletedMatchingTurn(t *testing.T) {
	for _, scenario := range []string{"codex-main", "codex-wrong-main", "codex-queue-only", "codex-queue-partial", "codex-queue-wrong-turn", "codex-queue-replaced"} {
		t.Run(scenario, func(t *testing.T) {
			b := mainSessionFixture(t)
			b.Command = append(b.Command, "-test.run=^TestRunnerFixture$", "--")
			b.Env["EF_RUNNER_FIXTURE"] = scenario
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			got, err := (Runner{Binding: b}).runCodexQueue(ctx, Request{ID: "request", Kind: "pm_push", SessionID: mainTestID, Prompt: "untrusted $(command)\n--argument"})
			if scenario == "codex-main" {
				if err != nil || got.SessionID != mainTestID || got.RunID != "own-turn" || got.Text != "decision" {
					t.Fatalf("missing matched receipt: %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatalf("queue admission/partial/wrong result accepted: %+v", got)
			}
		})
	}
}

func TestCodexQueueCancellation(t *testing.T) {
	b := mainSessionFixture(t)
	path, err := b.mainSessionPath()
	if err != nil {
		t.Fatal(err)
	}
	// The old turn is appending a record when our reader takes its watermark.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"old`)
	_ = f.Close()
	b.Command = append(b.Command, "-test.run=^TestRunnerFixture$", "--")
	b.Env["EF_RUNNER_FIXTURE"] = "codex-queue-only"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (Runner{Binding: b}).runCodexQueue(ctx, Request{Kind: "pm_push", SessionID: mainTestID, Prompt: "probe"})
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled observer continued: %v", err)
	}
}

func TestCodexQueueReceiptRejectsUncorrelatedAndIncompleteEvents(t *testing.T) {
	r := codexQueueReceipt{request: Request{Prompt: "expected", SessionID: mainTestID}}
	feed := func(p map[string]any) (bool, error) {
		b, _ := json.Marshal(map[string]any{"type": "event_msg", "payload": p})
		return r.consume(b)
	}
	if done, err := feed(queueItem("wrong", mainTestID, "UserMessage", "", "not our input")); done || err != nil || r.turnID != "" {
		t.Fatal("unrelated input claimed")
	}
	_, _ = feed(queueItem("own", mainTestID, "UserMessage", "", "expected"))
	_, _ = feed(queueItem("other", mainTestID, "AgentMessage", "final_answer", "wrong"))
	if done, err := feed(map[string]any{"type": "task_complete", "turn_id": "own", "last_agent_message": "wrong"}); done || err == nil {
		t.Fatal("completion without the matching final answer accepted")
	}
	if _, err := feed(queueItem("duplicate", mainTestID, "UserMessage", "", "expected")); err == nil {
		t.Fatal("duplicate input in another turn accepted")
	}
	if _, err := r.consume([]byte(strings.Repeat("x", maxRunnerOutput+1))); err == nil {
		t.Fatal("oversize receipt accepted")
	}
}

func TestCodexQueueReadsSplitAppendsAfterExistingPartialRecord(t *testing.T) {
	b := mainSessionFixture(t)
	path, err := b.mainSessionPath()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _ = f.WriteString(`{"type":"old`)
	marker := filepath.Join(t.TempDir(), "queued")
	b.Command = append(b.Command, "-test.run=^TestRunnerFixture$", "--")
	b.Env["EF_RUNNER_FIXTURE"], b.Env["EF_QUEUE_MARKER"] = "codex-queue-only", marker
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := (Runner{Binding: b}).runCodexQueue(ctx, Request{Kind: "pm_push", SessionID: mainTestID, Prompt: "probe"})
		done <- outcome{result, err}
	}()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("queue command was not invoked")
		case <-time.After(10 * time.Millisecond):
		}
	}
	_, _ = f.WriteString("\"}\n")
	user, _ := json.Marshal(map[string]any{"type": "event_msg", "payload": queueItem("own", mainTestID, "UserMessage", "", "probe")})
	_, _ = f.Write(user[:len(user)/2])
	select {
	case got := <-done:
		t.Fatalf("partial append accepted: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
	_, _ = f.Write(append(user[len(user)/2:], '\n'))
	encoder := json.NewEncoder(f)
	_ = encoder.Encode(map[string]any{"type": "event_msg", "payload": queueItem("own", mainTestID, "AgentMessage", "final_answer", "decision")})
	_ = encoder.Encode(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_complete", "turn_id": "own", "last_agent_message": "decision"}})
	got := <-done
	if got.err != nil || got.result.Text != "decision" || got.result.RunID != "own" {
		t.Fatalf("split read: %+v", got)
	}
}

func TestCodexMainQueueLocal(t *testing.T) {
	exe, id, home := os.Getenv("EIGENFLUX_TEST_CODEX_EXECUTABLE"), os.Getenv("EIGENFLUX_TEST_CODEX_SESSION"), os.Getenv("EIGENFLUX_TEST_CODEX_HOME")
	if exe == "" || id == "" || home == "" {
		t.Skip("explicit live Codex queue smoke test is not configured")
	}
	b := Binding{Mode: "native", Host: "codex", MainSessionID: id, Command: []string{exe}, Args: []string{"--sandbox", "workspace-write", "--ask-for-approval", "never"}, WorkDir: t.TempDir(), Home: t.TempDir(), Server: "test", TimeoutSeconds: 90, Env: map[string]string{"CODEX_HOME": home}}
	requestID, err := mainNoticeUUID()
	if err != nil {
		t.Fatal(err)
	}
	decision, _ := json.Marshal(Decision{Version: 1, RequestID: requestID, Action: "no_reply"})
	data, _ := json.Marshal(map[string]string{"request_id": requestID})
	prompt := "EigenFlux main adapter smoke test. Do not use tools, send messages, or change files/settings. Return exactly this JSON: " + string(decision) + codexPMDataMarker + string(data)
	got, err := (Runner{Binding: b}).Run(context.Background(), Request{ID: requestID, Kind: "pm_push", SessionID: id, Prompt: prompt})
	if err != nil || got.SessionID != id || got.RunID == "" {
		t.Fatalf("live host receipt: %+v %v", got, err)
	}
	if _, err = ParseDecision(got.Text, requestID); err != nil {
		t.Fatal(err)
	}
	t.Logf("completed main receipt: session=%s turn=%s request=%s", got.SessionID, got.RunID, requestID)
}
