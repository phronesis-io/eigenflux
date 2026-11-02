package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestWatchACPBridgeRejectsUnsupportedOrRemoteOwner(t *testing.T) {
	for _, host := range []string{"workbuddy", "claude-code", "openclaw", "hermes"} {
		cmd := newWatchACPBridgeCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs([]string{"--host", host, "--owner-url", "ws://remote.invalid:1234/api/ws", "--main-session-id", "existing-main", "--workdir", t.TempDir()})
		if err := cmd.Execute(); err == nil || out.Len() != 0 {
			t.Fatalf("%s accepted unsupported owner or wrote non-RPC output: %q %v", host, out.String(), err)
		}
	}
}

func TestWatchACPBridgeRequiresExplicitRouting(t *testing.T) {
	cmd := newWatchACPBridgeCommand()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"--host", "hermes"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "required flag") {
		t.Fatalf("missing owner routing accepted: %v", err)
	}
}

func TestWatchACPBridgeReturnsFieldGuidanceWithoutRouteValues(t *testing.T) {
	for _, tc := range []struct{ url, want string }{
		{"http://localhost:9000/api/ws", "owner-url must use ws:// or wss://"},
		{"ws://localhost:9000/api/ws?token=private-secret", "use token-file for the raw gateway token"},
		{"ws://localhost/api/ws", "owner-url must include an explicit port"},
	} {
		cmd := newWatchACPBridgeCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs([]string{"--host", "hermes", "--owner-url", tc.url, "--main-session-id", "existing-owner", "--workdir", t.TempDir()})
		err := cmd.Execute()
		if err == nil || !strings.HasPrefix(err.Error(), "hermes_main_invalid_config:") || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private-secret") || out.Len() != 0 {
			t.Fatalf("lost guidance or leaked route outside RPC: %v / %q", err, out.String())
		}
	}
}
