package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestACPBridgeDiagnosticsPreserveOnlyFixedMessages(t *testing.T) {
	known := "hermes_main_invalid_config: owner-url must use ws:// or wss://"
	for _, tc := range []struct{ name, scenario, input, want string }{
		{"stderr", "acp-stderr-diagnostic", "private-secret\nError: " + known + "\n", "runner_exit_failed: " + known},
		{"stderr-no-newline", "acp-stderr-diagnostic", "Error: " + known, "runner_exit_failed: " + known},
		{"stderr-unknown", "acp-stderr-diagnostic", "private-secret\nhermes_main_unknown_private-secret\n", "runner_exit_failed"},
		{"stderr-suffix", "acp-stderr-diagnostic", known + " private-secret\n", "runner_exit_failed"},
		{"stderr-overflow", "acp-stderr-diagnostic", strings.Repeat("private-secret", 100) + known + "\n", "runner_exit_failed"},
		{"rpc", "acp-rpc-diagnostic", "hermes_main_session_unavailable", "acp_rpc_error: hermes_main_session_unavailable"},
		{"rpc-config", "acp-rpc-diagnostic", known, "acp_rpc_error: " + known},
		{"rpc-unknown", "acp-rpc-diagnostic", "private-secret", "acp_rpc_error"},
		{"rpc-suffix", "acp-rpc-diagnostic", known + " private-secret", "acp_rpc_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := fixtureBinding(t, "acp", tc.scenario)
			binding.Env["EF_ACP_DIAGNOSTIC"] = tc.input
			result, err := (Runner{Binding: binding}).Run(context.Background(), Request{Prompt: "fixture only"})
			if err == nil || err.Error() != tc.want || result.Text != "" {
				t.Fatalf("got %#v / %v, want %s", result, err, tc.want)
			}
			var diagnostic *DiagnosticError
			if strings.HasPrefix(tc.scenario, "acp-stderr") && (!errors.As(err, &diagnostic) || diagnostic.Code != "runner_exit_failed") {
				t.Fatalf("lost existing diagnostic category: %v", err)
			}
			raw, _ := json.Marshal(err)
			if strings.Contains(err.Error(), "private-secret") || strings.Contains(string(raw), "private-secret") {
				t.Fatalf("leaked arbitrary bridge output: %s", raw)
			}
		})
	}
}

func TestBridgeDiagnosticWriterBoundsAndSplitLines(t *testing.T) {
	w := &bridgeDiagnosticWriter{}
	for _, chunk := range []string{strings.Repeat("s", 1<<20), "\nError: hermes_main_", "busy\r\n", "private-secret"} {
		if n, err := w.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatal(n, err)
		}
		if len(w.line) > 512 {
			t.Fatal("unbounded stderr line")
		}
	}
	if got := w.message(); got != "hermes_main_busy" {
		t.Fatalf("lost split diagnostic: %q", got)
	}
}

func TestMainNoticeProbeRetainsOnlySafeBridgeDiagnostic(t *testing.T) {
	known := "hermes_main_invalid_config: owner-url must use ws:// or wss://"
	for _, tc := range []struct{ name, scenario, input, want string }{
		{"startup", "acp-stderr-diagnostic", "Error: " + known + "\n", known},
		{"rpc", "acp-rpc-diagnostic", "hermes_main_session_unavailable", "hermes_main_session_unavailable"},
		{"private-stderr", "acp-stderr-diagnostic", "private-secret", ""},
		{"private-rpc", "acp-rpc-diagnostic", "private-secret", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := fixtureBinding(t, "native", tc.scenario)
			binding.Host, binding.MainSessionID = "hermes", "existing-owner"
			binding.MainCommand = append(append([]string{}, binding.Command...), binding.Args...)
			binding.Env["EF_ACP_DIAGNOSTIC"] = tc.input
			probe := ProbeMainNotice(context.Background(), MainNoticeTarget{Host: binding.Host, SessionID: binding.MainSessionID, Binding: binding})
			if probe.Code != "main_notice_owner_unavailable" || probe.Diagnostic != tc.want || probe.Enabled || probe.TargetMatched {
				t.Fatalf("lost safe probe diagnostic: %+v", probe)
			}
			raw, _ := json.Marshal(probe)
			if strings.Contains(string(raw), "private-secret") || tc.want == "" && strings.Contains(string(raw), "diagnostic") {
				t.Fatalf("unsafe or unexpected probe diagnostic: %s", raw)
			}
		})
	}
}
