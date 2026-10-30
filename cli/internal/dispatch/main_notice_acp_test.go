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

const hiddenACPFixtureSession = "fixture-eigenflux-main-session"
const hiddenACPFixturePrompt = "LOCAL FIXTURE ONLY: order fixture-123 needs a short result; no real order exists."

func hiddenACPFixtureTarget(t *testing.T, host, scenario string) MainNoticeTarget {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workDir, marker := t.TempDir(), filepath.Join(t.TempDir(), "methods")
	return MainNoticeTarget{Host: host, SessionID: hiddenACPFixtureSession, HiddenInputEnabled: true, Binding: Binding{
		Host: host, Mode: "acp", MainSessionID: hiddenACPFixtureSession, WorkDir: workDir, Home: t.TempDir(), Server: "fixture", TimeoutSeconds: 3,
		Command: []string{executable, "-test.run=^TestHiddenACPMainFixture$", "--"},
		Env:     map[string]string{"EF_HIDDEN_ACP_SCENARIO": scenario, "EF_HIDDEN_ACP_HOST": host, "EF_HIDDEN_ACP_CWD": workDir, "EF_HIDDEN_ACP_METHODS": marker},
	}}
}

func hiddenACPFixtureMethods(t *testing.T, target MainNoticeTarget) []string {
	t.Helper()
	data, err := os.ReadFile(target.Binding.Env["EF_HIDDEN_ACP_METHODS"])
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

func TestHiddenACPMainSeparateCommandPreservesExecution(t *testing.T) {
	target := hiddenACPFixtureTarget(t, "hermes", "success")
	target.Binding.MainCommand = append([]string(nil), target.Binding.Command...)
	target.Binding.Mode = "command"
	target.Binding.Command = []string{"/explicit/original/execution-adapter"}
	target.Binding.Args = []string{"fresh-execution", "{prompt}"}
	originalCommand, originalArgs := append([]string(nil), target.Binding.Command...), append([]string(nil), target.Binding.Args...)
	result, err := (MainNoticeRunner{Target: target}).Run(context.Background(), hiddenACPFixturePrompt)
	if err != nil || result.Text != "fixture result only" {
		t.Fatalf("separate main bridge failed: %#v, %v", result, err)
	}
	if target.Binding.Mode != "command" || !reflect.DeepEqual(target.Binding.Command, originalCommand) || !reflect.DeepEqual(target.Binding.Args, originalArgs) {
		t.Fatal("main bridge changed the normal execution adapter")
	}
	probe := ProbeMainNotice(context.Background(), target)
	if !probe.Enabled || !probe.TargetMatched {
		t.Fatalf("separate main bridge probe failed: %#v", probe)
	}
}

// This child is an isolated protocol fixture, never WorkBuddy or another real
// Agent. It admits only the negotiated inspect/notify extension methods.
func TestHiddenACPMainFixture(t *testing.T) {
	scenario := os.Getenv("EF_HIDDEN_ACP_SCENARIO")
	if scenario == "" {
		return
	}
	host, cwd := os.Getenv("EF_HIDDEN_ACP_HOST"), os.Getenv("EF_HIDDEN_ACP_CWD")
	marker, err := os.OpenFile(os.Getenv("EF_HIDDEN_ACP_METHODS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(81)
	}
	defer marker.Close()
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if decoder.Decode(&request) != nil {
			return
		}
		_, _ = marker.WriteString(request.Method + "\n")
		reply := func(value any) {
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": value})
		}
		fail := func() {
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32603, "message": "PRIVATE_FIXTURE_DETAIL must stay local"}})
		}
		switch request.Method {
		case "initialize":
			capability := map[string]any{"version": 1, "host": host, "existingSession": true, "hiddenInput": true, "resultOnly": true}
			switch scenario {
			case "cap-wrong-version":
				capability["version"] = 2
			case "cap-wrong-host":
				capability["host"] = "different-fixture-host"
			case "cap-no-existing":
				capability["existingSession"] = false
			case "cap-no-hidden":
				capability["hiddenInput"] = false
			case "cap-no-result-only":
				capability["resultOnly"] = false
			}
			// Standard loadSession support does not grant hidden main-session access.
			caps := map[string]any{"loadSession": true}
			if scenario != "cap-missing" {
				caps["_meta"] = map[string]any{"ai.eigenflux.mainConversation": capability}
			}
			reply(map[string]any{"protocolVersion": 1, "agentCapabilities": caps})
		case "_eigenflux/main/inspect", "_eigenflux/main/notify":
			var params struct {
				SessionID string `json:"sessionId"`
				Cwd       string `json:"cwd"`
				RequestID string `json:"requestId"`
				Prompt    string `json:"prompt"`
			}
			if json.Unmarshal(request.Params, &params) != nil || params.SessionID != hiddenACPFixtureSession || params.Cwd != cwd {
				os.Exit(82)
			}
			if request.Method == "_eigenflux/main/inspect" {
				if params.Prompt != "" || params.RequestID != "" {
					os.Exit(83)
				}
				if scenario == "inspect-error" {
					fail()
					continue
				}
				if scenario == "inspect-timeout" {
					time.Sleep(10 * time.Second)
					continue
				}
				result := map[string]any{"sessionId": params.SessionID, "host": host, "cwd": cwd, "state": "idle", "existingSession": true, "hiddenInput": true, "resultOnly": true}
				switch scenario {
				case "busy":
					result["state"] = "busy"
				case "inspect-wrong-session":
					result["sessionId"] = "another-fixture-session"
				case "inspect-wrong-host":
					result["host"] = "another-fixture-host"
				case "inspect-wrong-cwd":
					result["cwd"] = filepath.Join(cwd, "another-fixture")
				case "inspect-unknown-state":
					result["state"] = "queued"
				case "inspect-no-existing":
					result["existingSession"] = false
				case "inspect-no-hidden":
					result["hiddenInput"] = false
				case "inspect-no-result-only":
					result["resultOnly"] = false
				}
				if scenario == "replay-before-result" {
					_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": params.SessionID, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "OLD_FIXTURE_REPLAY"}}}})
				}
				reply(result)
				continue
			}
			if params.RequestID == "" || params.Prompt != hiddenACPFixturePrompt {
				os.Exit(84)
			}
			if scenario == "notify-error" {
				fail()
				continue
			}
			if scenario == "notify-timeout" {
				time.Sleep(10 * time.Second)
				continue
			}
			result := map[string]any{"sessionId": params.SessionID, "requestId": params.RequestID, "runId": "fixture-main-run-1", "stopReason": "end_turn", "text": "fixture result only", "existingSession": true, "hiddenInput": true, "resultOnly": true}
			switch scenario {
			case "receipt-wrong-session":
				result["sessionId"] = "another-fixture-session"
			case "receipt-wrong-request":
				result["requestId"] = "another-fixture-request"
			case "receipt-empty-run":
				result["runId"] = ""
			case "receipt-incomplete":
				result["stopReason"] = "cancelled"
			case "receipt-empty-text":
				result["text"] = "   "
			case "receipt-no-existing":
				result["existingSession"] = false
			case "receipt-no-hidden":
				result["hiddenInput"] = false
			case "receipt-no-result-only":
				result["resultOnly"] = false
			}
			reply(result)
		default:
			// Ordinary load, new, prompt, cancel, resume and queue are forbidden.
			os.Exit(85)
		}
	}
}

func TestHiddenACPMainUsesOnlyNegotiatedExtensionForBoundHosts(t *testing.T) {
	for _, host := range []string{"claude-code", "openclaw", "workbuddy", "hermes", "unknown-fixture-agent"} {
		t.Run(host, func(t *testing.T) {
			target := hiddenACPFixtureTarget(t, host, "success")
			result, err := (MainNoticeRunner{Target: target}).Run(context.Background(), hiddenACPFixturePrompt)
			if err != nil || result.Text != "fixture result only" || result.SessionID != hiddenACPFixtureSession || result.RunID != "fixture-main-run-1" {
				t.Fatalf("wrong exact-session receipt: %#v %v", result, err)
			}
			want := []string{"initialize", "_eigenflux/main/inspect", "_eigenflux/main/notify"}
			if calls := hiddenACPFixtureMethods(t, target); !reflect.DeepEqual(calls, want) {
				t.Fatalf("unexpected main-session methods: %q", calls)
			}
		})
	}
}

func TestHiddenACPMainRejectsUnprovenCapabilitiesTargetsAndReceipts(t *testing.T) {
	cases := map[string]int{
		"cap-missing": 1, "cap-wrong-version": 1, "cap-wrong-host": 1, "cap-no-existing": 1, "cap-no-hidden": 1, "cap-no-result-only": 1,
		"busy": 2, "inspect-wrong-session": 2, "inspect-wrong-host": 2, "inspect-wrong-cwd": 2, "inspect-unknown-state": 2, "inspect-no-existing": 2, "inspect-no-hidden": 2, "inspect-no-result-only": 2, "inspect-error": 2,
		"notify-error": 3, "receipt-wrong-session": 3, "receipt-wrong-request": 3, "receipt-empty-run": 3, "receipt-incomplete": 3, "receipt-empty-text": 3, "receipt-no-existing": 3, "receipt-no-hidden": 3, "receipt-no-result-only": 3,
	}
	for scenario, count := range cases {
		t.Run(scenario, func(t *testing.T) {
			target := hiddenACPFixtureTarget(t, "workbuddy", scenario)
			result, err := (MainNoticeRunner{Target: target}).Run(context.Background(), hiddenACPFixturePrompt)
			if err == nil || result != (Result{}) || strings.Contains(err.Error(), "PRIVATE_FIXTURE_DETAIL") {
				t.Fatalf("unproven response escaped: %#v %v", result, err)
			}
			if scenario == "busy" && !errors.Is(err, ErrMainNoticeBusy) {
				t.Fatalf("busy target was not identified: %v", err)
			}
			wantRetry := scenario == "busy" || scenario == "inspect-error" || scenario == "inspect-unknown-state"
			if MainNoticeCanRetry(err) != wantRetry {
				t.Fatalf("unsafe retry classification: %v, want retry=%v", err, wantRetry)
			}
			want := []string{"initialize", "_eigenflux/main/inspect", "_eigenflux/main/notify"}[:count]
			if calls := hiddenACPFixtureMethods(t, target); !reflect.DeepEqual(calls, want) {
				t.Fatalf("failed notification retried or fell back: %q", calls)
			}
		})
	}
}

func TestHiddenACPMainTimeoutDoesNotCancelOrResend(t *testing.T) {
	for _, scenario := range []string{"inspect-timeout", "notify-timeout"} {
		t.Run(scenario, func(t *testing.T) {
			target := hiddenACPFixtureTarget(t, "workbuddy", scenario)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			result, err := (MainNoticeRunner{Target: target}).Run(ctx, hiddenACPFixturePrompt)
			if err == nil || result != (Result{}) {
				t.Fatalf("timeout returned a result: %#v %v", result, err)
			}
			if MainNoticeCanRetry(err) != (scenario == "inspect-timeout") {
				t.Fatalf("timeout admission classification: %v", err)
			}
			want := []string{"initialize", "_eigenflux/main/inspect"}
			if scenario == "notify-timeout" {
				want = append(want, "_eigenflux/main/notify")
			}
			if calls := hiddenACPFixtureMethods(t, target); !reflect.DeepEqual(calls, want) {
				t.Fatalf("uncertain notification retried or canceled: %q", calls)
			}
		})
	}
}

func TestHiddenACPMainIgnoresHistoryReplay(t *testing.T) {
	target := hiddenACPFixtureTarget(t, "hermes", "replay-before-result")
	result, err := (MainNoticeRunner{Target: target}).Run(context.Background(), hiddenACPFixturePrompt)
	if err != nil || result.Text != "fixture result only" || strings.Contains(result.Text, "OLD_FIXTURE_REPLAY") {
		t.Fatalf("history replay was mistaken for this result: %#v %v", result, err)
	}
}

func TestHiddenACPMainProbeOnlyInitializesAndInspects(t *testing.T) {
	for _, host := range []string{"claude-code", "openclaw", "workbuddy", "hermes", "unknown-fixture-agent"} {
		t.Run(host, func(t *testing.T) {
			target := hiddenACPFixtureTarget(t, host, "success")
			probe := ProbeMainNotice(context.Background(), target)
			if probe.Protocol != "acp" || !probe.TransportAvailable || !probe.TargetMatched || probe.HostVerified {
				t.Fatalf("incorrect fixture-only probe: %#v", probe)
			}
			if calls := hiddenACPFixtureMethods(t, target); !reflect.DeepEqual(calls, []string{"initialize", "_eigenflux/main/inspect"}) {
				t.Fatalf("read-only probe started or loaded a turn: %q", calls)
			}
		})
	}
}

func TestHiddenACPMainDisabledGateDoesNotStartProcess(t *testing.T) {
	target := hiddenACPFixtureTarget(t, "workbuddy", "success")
	target.HiddenInputEnabled = false
	result, err := (MainNoticeRunner{Target: target}).Run(context.Background(), hiddenACPFixturePrompt)
	if !errors.Is(err, ErrMainNoticeUnsupported) || result != (Result{}) {
		t.Fatalf("disabled adapter admitted work: %#v %v", result, err)
	}
	if _, err := os.Stat(target.Binding.Env["EF_HIDDEN_ACP_METHODS"]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled adapter started its process: %v", err)
	}
}
