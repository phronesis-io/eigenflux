package mainbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHermesMainBridgeRejectsExecutionClientBeforeConnecting(t *testing.T) {
	for _, metadata := range []any{nil, map[string]any{}, map[string]any{mainExtension: map[string]int{"version": 2}}} {
		config := HermesConfig{URL: "ws://127.0.0.1:1/api/ws", SessionID: "existing", Cwd: t.TempDir()}
		var input, output bytes.Buffer
		_ = json.NewEncoder(&input).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"_meta": metadata}}})
		if err := ServeHermes(context.Background(), &input, &output, config); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), mainOnlyBridge) || strings.Contains(output.String(), "connection_failed") {
			t.Fatalf("execution client contacted host or lost guidance: %s", output.String())
		}
	}
}

func TestHermesConfigDiagnosticFieldsAndRedaction(t *testing.T) {
	base := HermesConfig{URL: "ws://127.0.0.1:9000/api/ws", SessionID: "live-session", Cwd: t.TempDir()}
	for _, tc := range []struct {
		name   string
		change func(*HermesConfig)
		want   string
	}{
		{"malformed", func(c *HermesConfig) { c.URL = "ws://%private-secret/api/ws" }, invalidOwnerURL},
		{"http-root", func(c *HermesConfig) { c.URL = "http://127.0.0.1:9000" }, invalidOwnerScheme},
		{"http-path", func(c *HermesConfig) { c.URL = "http://127.0.0.1:9000/api/ws" }, invalidOwnerScheme},
		{"query-token", func(c *HermesConfig) { c.URL += "?token=private-secret" }, invalidOwnerCredentials},
		{"empty-query", func(c *HermesConfig) { c.URL += "?" }, invalidOwnerCredentials},
		{"userinfo", func(c *HermesConfig) { c.URL = "ws://user:private-secret@localhost:9000/api/ws" }, invalidOwnerCredentials},
		{"fragment", func(c *HermesConfig) { c.URL += "#private-secret" }, invalidOwnerCredentials},
		{"wrong-path", func(c *HermesConfig) { c.URL = "ws://localhost:9000/private-secret" }, invalidOwnerPath},
		{"encoded-path", func(c *HermesConfig) { c.URL = "ws://localhost:9000/api/%77s" }, invalidOwnerPath},
		{"no-port", func(c *HermesConfig) { c.URL = "ws://localhost/api/ws" }, invalidOwnerPort},
		{"port-zero", func(c *HermesConfig) { c.URL = "ws://localhost:0/api/ws" }, invalidOwnerPort},
		{"port-range", func(c *HermesConfig) { c.URL = "ws://localhost:65536/api/ws" }, invalidOwnerPort},
		{"remote", func(c *HermesConfig) { c.URL = "wss://private-secret.example:443/api/ws" }, "hermes_main_requires_loopback"},
		{"empty-session", func(c *HermesConfig) { c.SessionID = "" }, invalidMainSession},
		{"session-space", func(c *HermesConfig) { c.SessionID = " private-secret" }, invalidMainSession},
		{"session-control", func(c *HermesConfig) { c.SessionID = "private-secret\n" }, invalidMainSession},
		{"relative-workdir", func(c *HermesConfig) { c.Cwd = "private-secret" }, invalidOwnerWorkdir},
		{"unclean-workdir", func(c *HermesConfig) { c.Cwd += string(filepath.Separator) + "." }, invalidOwnerWorkdir},
		{"relative-token", func(c *HermesConfig) { c.TokenFile = "private-secret" }, invalidTokenPath},
		{"missing-token", func(c *HermesConfig) { c.TokenFile = filepath.Join(base.Cwd, "private-secret") }, "hermes_main_token_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := base
			tc.change(&config)
			var out bytes.Buffer
			err := ServeHermes(context.Background(), bytes.NewReader(nil), &out, config)
			if err == nil || err.Error() != tc.want || out.Len() != 0 {
				t.Fatalf("got %v / %q, want %s", err, out.String(), tc.want)
			}
			if DiagnosticMessage(err.Error()) != err.Error() || strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), base.Cwd) {
				t.Fatalf("unsafe or lost diagnostic: %v", err)
			}
		})
	}
}

func TestHermesConfigAcceptsOpaqueSessionsAndRawToken(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("fixture-secret-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"20261102_123456_abcdef", "01a19bb9-df0f-7cc1-b76e-308bfcfed715", "agent:joe:main"} {
		for _, endpoint := range []string{"ws://127.0.0.1:9000/api/ws", "wss://localhost:9000/api/ws", "ws://[::1]:9000/api/ws"} {
			config := HermesConfig{URL: endpoint, SessionID: session, Cwd: dir, TokenFile: tokenPath}
			u, err := validateHermesConfig(config)
			if err != nil || u.Query().Get("token") != "fixture-secret-token" {
				t.Fatalf("valid routing rejected: %v", err)
			}
		}
	}
	for _, content := range []string{"", "\n", "secret\nsecond-line", strings.Repeat("x", 8193)} {
		if err := os.WriteFile(tokenPath, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := validateHermesConfig(HermesConfig{URL: "ws://localhost:9000/api/ws", SessionID: "existing", Cwd: dir, TokenFile: tokenPath})
		if err == nil || err.Error() != invalidTokenContent {
			t.Fatalf("invalid raw token accepted: %v", err)
		}
	}
}
