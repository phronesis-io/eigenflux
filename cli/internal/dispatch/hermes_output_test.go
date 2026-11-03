package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHermesOutputFixture(t *testing.T) {
	scenario := os.Getenv("EF_HERMES_OUTPUT_FIXTURE")
	if scenario == "" {
		return
	}
	probe := slices.Contains(os.Args, "--help")
	marker, err := os.OpenFile(os.Getenv("EF_HERMES_OUTPUT_MARKER"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(8)
	}
	if probe {
		_, _ = fmt.Fprintln(marker, "help")
	} else {
		_, _ = fmt.Fprintln(marker, "model")
	}
	_ = marker.Close()
	if probe {
		switch scenario {
		case "help-failed":
			fmt.Fprint(os.Stderr, "private-secret")
			os.Exit(2)
		case "help-timeout":
			time.Sleep(20 * time.Second)
			os.Exit(3)
		case "help-oversize":
			fmt.Print(strings.Repeat("x", maxRunnerOutput+1))
			os.Exit(0)
		case "legacy":
			fmt.Println("usage: hermes chat [-q QUERY] [--quiet]")
		default:
			fmt.Println("usage: hermes chat [--format {text,stream-json}] [-q QUERY]")
		}
		os.Exit(0)
	}
	if scenario == "command" {
		fmt.Print("command-result")
		os.Exit(0)
	}
	if scenario == "legacy" {
		if slices.Contains(os.Args, "--format") {
			os.Exit(4)
		}
		fmt.Fprint(os.Stdout, `{"answer":42}`)
		fmt.Fprintln(os.Stderr, "session_id: fixture-owner")
		os.Exit(0)
	}
	if !slices.Contains(os.Args, "stream-json") {
		os.Exit(4)
	}
	fmt.Println(`{"type":"system","subtype":"init","session_id":""}`)
	fmt.Println(`{"type":"text","text":"private-secret commentary"}`)
	fmt.Println(`{"type":"tool_result","output":"│ +private-secret diff","is_error":true}`)
	if scenario == "bad-receipt" {
		fmt.Println(`{"type":"result","session_id":"fixture-owner","exit_code":0,"text":"private-secret","error":"private-secret"}`)
	} else {
		fmt.Println(`{"type":"result","session_id":"fixture-owner","exit_code":0,"text":"{\"answer\":42}"}`)
	}
	os.Exit(0)
}

func hermesOutputBinding(t *testing.T, scenario string) (Binding, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "calls")
	b := Binding{Mode: "native", Host: "hermes", Command: []string{executable, "-test.run=^TestHermesOutputFixture$", "--"}, Home: t.TempDir(), WorkDir: t.TempDir(), Server: "fixture", TimeoutSeconds: 10, Env: map[string]string{"EF_HERMES_OUTPUT_FIXTURE": scenario, "EF_HERMES_OUTPUT_MARKER": marker}}
	return b, marker
}

func TestHermesNativeNegotiatesBeforeOneModelCall(t *testing.T) {
	for _, scenario := range []string{"stream", "legacy"} {
		t.Run(scenario, func(t *testing.T) {
			binding, marker := hermesOutputBinding(t, scenario)
			result, err := (Runner{Binding: binding}).Run(context.Background(), Request{Prompt: "local fixture"})
			if err != nil || result.Text != `{"answer":42}` || result.SessionID != "fixture-owner" {
				t.Fatalf("wrong final-only result: %+v %v", result, err)
			}
			calls, _ := os.ReadFile(marker)
			if string(calls) != "help\nmodel\n" {
				t.Fatalf("probe or model repeated: %q", calls)
			}
		})
	}
}

func TestHermesProbeAndReceiptFailuresNeverRetryModel(t *testing.T) {
	for _, scenario := range []string{"help-failed", "help-timeout", "help-oversize", "bad-receipt"} {
		t.Run(scenario, func(t *testing.T) {
			binding, marker := hermesOutputBinding(t, scenario)
			ctx := context.Background()
			if scenario == "help-timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			result, err := (Runner{Binding: binding}).Run(ctx, Request{Prompt: "local fixture"})
			if err == nil || result.Text != "" || strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("failure accepted or leaked: %+v %v", result, err)
			}
			wantCode, wantCalls := "hermes_capability_probe_failed", "help\n"
			if scenario == "help-timeout" {
				wantCode = "hermes_capability_probe_timed_out"
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("lost parent deadline")
				}
			}
			if scenario == "bad-receipt" {
				wantCode, wantCalls = "native_agent_failed", "help\nmodel\n"
			}
			calls, _ := os.ReadFile(marker)
			if err.Error() != wantCode || string(calls) != wantCalls {
				t.Fatalf("wrong diagnosis or model repeated: %v / %q", err, calls)
			}
		})
	}
}

func TestHermesCommandModeDoesNotProbe(t *testing.T) {
	binding, marker := hermesOutputBinding(t, "command")
	binding.Mode = "command"
	result, err := (Runner{Binding: binding}).Run(context.Background(), Request{Prompt: "fixture"})
	if err != nil || result.Text != "command-result" {
		t.Fatal(result, err)
	}
	calls, _ := os.ReadFile(marker)
	if string(calls) != "model\n" {
		t.Fatalf("command mode unexpectedly negotiated: %q", calls)
	}
}

func TestHermesStreamJSONRejectsUnconfirmedOrMixedOutput(t *testing.T) {
	init := `{"type":"system","subtype":"init","session_id":""}` + "\n"
	final := `{"type":"result","session_id":"session:opaque","exit_code":0,"text":"{\"answer\":42}"}` + "\n"
	for _, tc := range []struct{ name, raw string }{
		{"human-prefix", "│ diff preview\n" + init + final},
		{"missing-init", final},
		{"missing-result", init + `{"type":"text","text":"{\"answer\":42}"}`},
		{"duplicate-result", init + final + final},
		{"trailing-event", init + final + `{"type":"text","text":"private-secret"}`},
		{"trailing-prose", init + final + "private-secret"},
		{"unknown-event", init + `{"type":"unexpected"}` + "\n" + final},
		{"duplicate-init", init + init + final},
		{"missing-exit", init + `{"type":"result","session_id":"session","text":"ok"}`},
		{"null-exit", init + `{"type":"result","session_id":"session","text":"ok","exit_code":null}`},
		{"failed-exit", init + `{"type":"result","session_id":"session","text":"ok","exit_code":1}`},
		{"error-receipt", init + `{"type":"result","session_id":"session","text":"ok","exit_code":0,"error":"private-secret"}`},
		{"missing-text", init + `{"type":"result","session_id":"session","exit_code":0}`},
		{"empty-session", init + `{"type":"result","session_id":"","text":"ok","exit_code":0}`},
		{"control-session", init + `{"type":"result","session_id":"bad\nsession","text":"ok","exit_code":0}`},
		{"changed-session", strings.ReplaceAll(init, `"session_id":""`, `"session_id":"original"`) + final},
		{"duplicate-field", init + `{"type":"result","session_id":"session","exit_code":1,"exit_code":0,"text":"ok"}`},
		{"oversize", init + strings.Repeat("x", maxRunnerOutput)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := parseHermesStreamJSON([]byte(tc.raw))
			if err == nil || result.Text != "" || strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("unconfirmed result accepted or leaked: %+v %v", result, err)
			}
		})
	}
	result, err := parseHermesStreamJSON([]byte(init + `{"type":"tool_result","is_error":true,"output":"private-secret"}` + "\n" + final))
	if err != nil || result.Text != `{"answer":42}` || result.SessionID != "session:opaque" {
		t.Fatal(result, err)
	}
	var decision map[string]int
	if json.Unmarshal([]byte(result.Text), &decision) != nil || decision["answer"] != 42 {
		t.Fatal("final text was not preserved byte-for-byte")
	}
}
