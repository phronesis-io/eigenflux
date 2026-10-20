package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

		switch msg.Method {
		case "initialize":
			response(map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]bool{"loadSession": scenario != "acp-no-load"}})

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
		wantStatus := "untested"
		if MainNoticeHostVerified(host) {
			wantStatus = "verified_macos"
		}
		if capability.Protocol != want || capability.Status != wantStatus {
			t.Fatalf("capability %#v", capability)
		}
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
