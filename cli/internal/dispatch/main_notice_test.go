package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const noticeSession = "01a0d200-9d41-7cd2-9214-3f04761910c4"
const noticePrompt = "Order 123 was delivered. Summarize the result without confirming receipt."

func noticeFixtureTarget(t *testing.T, host, scenario string) MainNoticeTarget {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mode := "acp"
	if host == "codex" {
		mode = "native"
	}
	return MainNoticeTarget{Host: host, SessionID: noticeSession, VerifiedHiddenInput: true, Binding: Binding{
		Host: host, Mode: mode, Home: t.TempDir(), Server: "test", WorkDir: t.TempDir(), TimeoutSeconds: 5,
		Command: []string{exe, "-test.run=^TestMainNoticeFixture$", "--"},
		Env:     map[string]string{"EF_MAIN_NOTICE_FIXTURE": scenario, "EF_MAIN_NOTICE_MARKER": filepath.Join(t.TempDir(), "calls")},
	}}
}

// This peer is never a real Agent. It validates every admitted method and writes
// markers before replying, so failures prove that no alternative turn was sent.
func TestMainNoticeFixture(t *testing.T) {
	scenario := os.Getenv("EF_MAIN_NOTICE_FIXTURE")
	if scenario == "" {
		return
	}
	if strings.HasPrefix(scenario, "codex-") {
		args := os.Args
		if !reflect.DeepEqual(args[len(args)-2:], []string{"app-server", "proxy"}) {
			os.Exit(81)
		}
	}
	f, err := os.OpenFile(os.Getenv("EF_MAIN_NOTICE_MARKER"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(82)
	}
	defer f.Close()
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if decoder.Decode(&msg) != nil {
			return
		}
		_, _ = f.WriteString(msg.Method + "\n")
		response := func(value any) { _ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": value}) }
		notify := func(method string, params any) {
			_ = encoder.Encode(map[string]any{"method": method, "params": params})
		}
		final := func(session, turn, text, phase string) {
			notify("item/completed", map[string]any{"threadId": session, "turnId": turn, "item": map[string]string{"type": "agentMessage", "phase": phase, "text": text}})
		}
		switch msg.Method {
		case "initialize":
			if strings.HasPrefix(scenario, "codex-") {
				var params struct {
					Capabilities struct {
						Experimental bool `json:"experimentalApi"`
					} `json:"capabilities"`
				}
				if json.Unmarshal(msg.Params, &params) != nil || !params.Capabilities.Experimental {
					os.Exit(83)
				}
				response(map[string]any{})
			} else {
				response(map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]bool{"loadSession": scenario != "acp-no-load"}})
			}
		case "initialized":
		case "thread/read":
			var params struct {
				ThreadID     string `json:"threadId"`
				IncludeTurns bool   `json:"includeTurns"`
			}
			if json.Unmarshal(msg.Params, &params) != nil || params.ThreadID != noticeSession || params.IncludeTurns {
				os.Exit(84)
			}
			session, status := noticeSession, "idle"
			if scenario == "codex-wrong-read" {
				session = "other"
			}
			if scenario == "codex-busy" {
				status = "active"
			}
			response(map[string]any{"thread": map[string]any{"id": session, "status": map[string]string{"type": status}}})
		case "turn/start":
			var params struct {
				ThreadID string `json:"threadId"`
				Input    []any  `json:"input"`
				Context  map[string]struct {
					Kind  string `json:"kind"`
					Value string `json:"value"`
				} `json:"additionalContext"`
			}
			if json.Unmarshal(msg.Params, &params) != nil || params.ThreadID != noticeSession || params.Input == nil || len(params.Input) != 0 || len(params.Context) != 1 || params.Context["eigenflux_business_notice"].Kind != "untrusted" || params.Context["eigenflux_business_notice"].Value != noticePrompt {
				os.Exit(85)
			}
			if scenario == "codex-writer" {
				_ = encoder.Encode(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32600, "message": "already has an active writer; secret"}})
				continue
			}
			if scenario == "codex-before-response" {
				final(noticeSession, "turn-1", "result", "final_answer")
			}
			response(map[string]any{"turn": map[string]string{"id": "turn-1", "status": "inProgress"}})
			if scenario == "codex-timeout" {
				time.Sleep(10 * time.Second)
				return
			}
			if scenario == "codex-permission" {
				_ = encoder.Encode(map[string]any{"id": "approval", "method": "item/commandExecution/requestApproval", "params": map[string]string{"threadId": noticeSession}})
				continue
			}
			if scenario == "codex-oversize" {
				_, _ = os.Stdout.WriteString(strings.Repeat("x", maxRunnerOutput+10) + "\n")
				continue
			}
			final(noticeSession, "turn-1", "internal progress", "commentary")
			if scenario != "codex-before-response" && scenario != "codex-no-final" {
				session, turn := noticeSession, "turn-1"
				if scenario == "codex-wrong-thread" {
					session = "other"
				}
				if scenario == "codex-wrong-turn" {
					turn = "other"
				}
				final(session, turn, "result", "final_answer")
			}
			turn, status := "turn-1", "completed"
			if scenario == "codex-wrong-completion" {
				turn = "other"
			}
			if scenario == "codex-failed" {
				status = "failed"
			}
			notify("turn/completed", map[string]any{"threadId": noticeSession, "turn": map[string]string{"id": turn, "status": status}})
		case "session/load":
			var params struct {
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal(msg.Params, &params) != nil || params.SessionID != noticeSession {
				os.Exit(86)
			}
			session := noticeSession
			if scenario == "acp-wrong-load" {
				session = "other"
			}
			if scenario == "acp-missing-load-id" {
				response(map[string]any{})
			} else {
				response(map[string]string{"sessionId": session})
			}
		case "session/prompt":
			var params struct {
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal(msg.Params, &params) != nil || params.SessionID != noticeSession {
				os.Exit(87)
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": noticeSession, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "result"}}}})
			response(map[string]string{"stopReason": "end_turn"})
		default:
			// In particular, never permit session/new, thread/start, resume, fork,
			// queue, daemon setup or a retry after a writer error.
			os.Exit(88)
		}
	}
}

func TestMainNoticeHiddenInputGateDoesNotStartProcess(t *testing.T) {
	for _, host := range []string{"codex", "hermes", "unknown"} {
		target := noticeFixtureTarget(t, host, "unused")
		target.VerifiedHiddenInput = false
		result, err := (MainNoticeRunner{Target: target}).Run(context.Background(), noticePrompt)
		if !errors.Is(err, ErrMainNoticeUnsupported) || result != (Result{}) {
			t.Fatalf("%s: result %#v error %v", host, result, err)
		}
		if _, err := os.Stat(target.Binding.Env["EF_MAIN_NOTICE_MARKER"]); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s process started: %v", host, err)
		}
		capability := MainNoticeCapability(host)
		want := "acp"
		if host == "codex" {
			want = "codex_turn"
		}
		if capability.Protocol != want || capability.Status != "untested" {
			t.Fatalf("capability %#v", capability)
		}
	}
}

func TestMainNoticeCodexProxyReceipts(t *testing.T) {
	for _, scenario := range []string{"codex-success", "codex-before-response", "codex-busy", "codex-wrong-read", "codex-writer", "codex-no-final", "codex-wrong-thread", "codex-wrong-turn", "codex-wrong-completion", "codex-failed", "codex-permission", "codex-oversize", "codex-timeout"} {
		t.Run(scenario, func(t *testing.T) {
			target := noticeFixtureTarget(t, "codex", scenario)
			// Existing native execution flags cannot change a notification proxy
			// into a separately owned host or relax the thread's permissions.
			target.Binding.Args = []string{"--listen", "stdio://", "--dangerously-bypass-approvals-and-sandbox"}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if scenario == "codex-timeout" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
			}
			defer cancel()
			result, err := (MainNoticeRunner{Target: target}).Run(ctx, noticePrompt)
			if scenario == "codex-success" || scenario == "codex-before-response" {
				if err != nil || result != (Result{Text: "result", SessionID: noticeSession, RunID: "turn-1"}) {
					t.Fatalf("result %#v error %v", result, err)
				}
			} else if err == nil || result != (Result{}) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("failure leaked result or diagnostics: %#v %v", result, err)
			}
			calls, readErr := os.ReadFile(target.Binding.Env["EF_MAIN_NOTICE_MARKER"])
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, forbidden := range []string{"thread/resume", "thread/start", "thread/fork", "session/new", "turn/interrupt"} {
				if strings.Contains(string(calls), forbidden) {
					t.Fatalf("forbidden call %s: %s", forbidden, calls)
				}
			}
			count := strings.Count(string(calls), "turn/start\n")
			if scenario == "codex-busy" || scenario == "codex-wrong-read" {
				if count != 0 {
					t.Fatalf("turn sent before validating target: %s", calls)
				}
			} else if count != 1 {
				t.Fatalf("replayed or missing turn: %s", calls)
			}
		})
	}
}

func TestMainNoticeACPRequiresExactLoadedSession(t *testing.T) {
	for _, scenario := range []string{"acp-success", "acp-no-load", "acp-wrong-load", "acp-missing-load-id"} {
		t.Run(scenario, func(t *testing.T) {
			target := noticeFixtureTarget(t, "hermes", scenario)
			result, err := (MainNoticeRunner{Target: target}).Run(context.Background(), noticePrompt)
			if scenario == "acp-success" || scenario == "acp-missing-load-id" {
				if err != nil || result.Text != "result" || result.SessionID != noticeSession {
					t.Fatalf("result %#v error %v", result, err)
				}
			} else if err == nil || result != (Result{}) {
				t.Fatalf("failure returned result %#v error %v", result, err)
			}
			calls, err := os.ReadFile(target.Binding.Env["EF_MAIN_NOTICE_MARKER"])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "session/new") || scenario != "acp-success" && scenario != "acp-missing-load-id" && strings.Contains(string(calls), "session/prompt") {
				t.Fatalf("unexpected calls: %s", calls)
			}
		})
	}
}

func TestMainNoticeRejectsMissingOrChangedTarget(t *testing.T) {
	for _, change := range []func(*MainNoticeTarget){
		func(target *MainNoticeTarget) { target.SessionID = "" },
		func(target *MainNoticeTarget) { target.Binding.Host = "other" },
		func(target *MainNoticeTarget) { target.Binding.MainSessionID = "other" },
		func(target *MainNoticeTarget) { target.SessionID = "--last" },
	} {
		target := noticeFixtureTarget(t, "codex", "unused")
		change(&target)
		if result, err := (MainNoticeRunner{Target: target}).Run(context.Background(), noticePrompt); err == nil || result != (Result{}) {
			t.Fatalf("result %#v error %v", result, err)
		}
		if _, err := os.Stat(target.Binding.Env["EF_MAIN_NOTICE_MARKER"]); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("process started: %v", err)
		}
	}
}
