package mainbridge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/mainbridge"
)

func TestHermesBridgeProcess(t *testing.T) {
	if os.Getenv("EF_HERMES_BRIDGE_PROCESS") != "1" {
		return
	}
	var config mainbridge.HermesConfig
	if json.Unmarshal([]byte(os.Getenv("EF_HERMES_BRIDGE_CONFIG")), &config) != nil {
		os.Exit(2)
	}
	if err := mainbridge.ServeHermes(context.Background(), os.Stdin, os.Stdout, config); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestHermesBridgeRunnerDiagnostics(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, tc := range []struct{ name, url, want string }{
		{"startup-config", "http://127.0.0.1:1/api/ws", "runner_exit_failed: hermes_main_invalid_config: owner-url must use ws:// or wss://"},
		{"rpc-main-only", "ws://127.0.0.1:1/api/ws", "acp_rpc_error: hermes_main_execution_adapter_required:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := mainbridge.HermesConfig{URL: tc.url, SessionID: "live-owner", Cwd: dir}
			encoded, _ := json.Marshal(config)
			binding := dispatch.Binding{Mode: "acp", Host: "hermes", Command: []string{executable, "-test.run=^TestHermesBridgeProcess$"}, WorkDir: dir, Home: t.TempDir(), Server: "fixture", TimeoutSeconds: 5, Env: map[string]string{"EF_HERMES_BRIDGE_PROCESS": "1", "EF_HERMES_BRIDGE_CONFIG": string(encoded)}}
			_, err := (dispatch.Runner{Binding: binding}).Run(context.Background(), dispatch.Request{Kind: "commission_publish_trial", Prompt: "fixture only"})
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) || strings.Contains(err.Error(), dir) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}
