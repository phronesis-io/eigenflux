package cmd

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

const guardTestThread = "11111111-2222-3333-4444-555555555555"

func TestCodexGuardPreservesExistingHooksAndTrustBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	initial := `{"description":"preserve metadata","custom":{"nested":true},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"other hook","timeout":4}]}],"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"untouched"}]}]}}`
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	cli, home := filepath.Join(dir, "bin", "eigenflux"), filepath.Join(dir, "agent")
	changed, err := installCodexGuard(dir, cli, home, "eigenflux", guardTestThread, "42")
	if err != nil || !changed {
		t.Fatalf("first install: %v %v", changed, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["custom"]) != `{"nested":true}` || string(got["description"]) != `"preserve metadata"` {
		t.Fatalf("metadata changed: %s", raw)
	}
	var hooks map[string][]map[string]json.RawMessage
	if err = json.Unmarshal(got["hooks"], &hooks); err != nil {
		t.Fatal(err)
	}
	if len(hooks["Stop"]) != 2 || len(hooks["SessionStart"]) != 1 {
		t.Fatalf("existing hooks replaced: %s", raw)
	}
	for _, event := range []string{"Stop", "UserPromptSubmit", "PermissionRequest", "Interrupt"} {
		if len(hooks[event]) == 0 {
			t.Fatal("missing " + event)
		}
		if !strings.Contains(string(hooks[event][len(hooks[event])-1]["hooks"]), "--expected-agent") {
			t.Fatal("missing account fence")
		}
	}
	changed, err = installCodexGuard(dir, cli, home, "eigenflux", guardTestThread, "42")
	if err != nil || changed {
		t.Fatalf("repeat changed definition/trust hash: %v %v", changed, err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(raw) {
		t.Fatal("idempotent setup rewrote hook trust definition")
	}
	if strings.Contains(string(raw), "bypass") || strings.Contains(string(raw), "trusted") {
		t.Fatal("setup mutates host trust")
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %o", info.Mode().Perm())
	}
}

func TestCodexGuardRejectsInvalidFilesWithoutReplacement(t *testing.T) {
	for _, body := range []string{`{"hooks":[]}`, `{"hooks":{"Stop":{}}}`, `null`, `{"hooks":{"Stop":[{"hooks":{}}]}}`} {
		dir := t.TempDir()
		path := filepath.Join(dir, "hooks.json")
		_ = os.WriteFile(path, []byte(body), 0600)
		_, err := installCodexGuard(dir, filepath.Join(dir, "cli"), dir, "server", guardTestThread, "42")
		if err == nil {
			t.Fatalf("accepted %s", body)
		}
		got, _ := os.ReadFile(path)
		if string(got) != body {
			t.Fatal("replaced invalid user config")
		}
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	_ = os.WriteFile(target, []byte(`{}`), 0600)
	if err := os.Symlink(target, filepath.Join(dir, "hooks.json")); err != nil {
		t.Skip(err)
	}
	if _, err := installCodexGuard(dir, filepath.Join(dir, "cli"), dir, "server", guardTestThread, "42"); err == nil {
		t.Fatal("followed hooks symlink")
	}
}

func TestCodexGuardWindowsCommandHasExactArguments(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "space's $() & % ! 中文", "eigenflux")
	posix, windows, err := codexGuardCommands(cli, dir, "server", guardTestThread, "42", "host-observe", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(posix, "--expected-agent") || !strings.HasPrefix(windows, "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand ") {
		t.Fatal("wrong command shape")
	}
	encoded := windows[strings.LastIndex(windows, " ")+1:]
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	words := make([]uint16, len(raw)/2)
	for i := range words {
		words[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	script := string(utf16.Decode(words))
	escaped := "'" + strings.ReplaceAll(cli, "'", "''") + "'"
	if !strings.Contains(script, escaped) {
		t.Fatalf("lost quoted executable: %q", script)
	}
	if !strings.Contains(script, "[Console]::In.ReadToEnd(); $hookInput | & ") || !strings.Contains(script, "[Console]::InputEncoding = New-Object System.Text.UTF8Encoding($false)") || !strings.Contains(script, "[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)") {
		t.Fatal("hook JSON stdin or Unicode encoding not forwarded")
	}
	if !strings.HasSuffix(script, "; exit $LASTEXITCODE") {
		t.Fatal("lost original hook exit status")
	}
}

func TestCodexGuardFencesExactNativeSchedulerPrompt(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "bin", "eigenflux")
	home := filepath.Join(dir, "agent")
	launcher, err := renderHeartbeatLauncher(cli, home, "server", "skill", "")
	if err != nil {
		t.Fatal(err)
	}
	canonical := heartbeatSchedulerPrompt(launcher)
	hash, err := codexGuardPromptHash(cli, home, "server", "")
	if err != nil {
		t.Fatal(err)
	}
	same, err := codexGuardPromptHash(cli, home, "server", "\n"+canonical+"\n")
	if err != nil || same != hash {
		t.Fatal("trailing whitespace changed native prompt fence")
	}
	managed, err := codexGuardPromptHash(cli, home, "server", heartbeatSchedulerPrompt(launcher+" --watch-managed"))
	if err != nil || managed == hash {
		t.Fatal("watch-managed prompt lost exact distinction")
	}
	for _, invalid := range []string{"Explain: " + canonical, canonical + " extra work", heartbeatSchedulerPrompt("eigenflux --homedir /other heartbeat plan"), strings.Replace(canonical, home, filepath.Join(dir, "other"), 1)} {
		if _, err := codexGuardPromptHash(cli, home, "server", invalid); err == nil {
			t.Fatal("accepted foreign or quoted scheduler prompt")
		}
	}
	changed, err := installCodexGuard(dir, cli, home, "server", guardTestThread, "42", canonical)
	if err != nil || !changed {
		t.Fatalf("canonical setup: %v %v", changed, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "hooks.json"))
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			}
		}
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for event, groups := range doc.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				if event == "Stop" {
					if strings.Contains(h.Command, "--prompt-hash") {
						t.Fatal("Stop needs no prompt hash")
					}
				} else if !strings.Contains(h.Command, hash) || !strings.Contains(h.Command, "--prompt-hash") {
					t.Fatal("observer missing exact prompt fence")
				}
			}
		}
	}
}

func TestCodexGuardAcceptsExactSupportedNativeShells(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "bin", "eigenflux")
	home := filepath.Join(dir, "agent")
	for _, shell := range []string{"posix", "powershell", "cmd"} {
		launcher, err := renderHeartbeatLauncher(cli, home, "server", "skill", shell)
		if err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"", " --watch-managed"} {
			prompt := heartbeatSchedulerPrompt(launcher + suffix)
			got, err := codexGuardPromptHash(cli, home, "server", prompt)
			if err != nil || got == "" {
				t.Fatalf("valid %s scheduler readback rejected: %v", shell, err)
			}
			foreign := strings.Replace(prompt, home, filepath.Join(dir, "different-agent"), 1)
			if _, err := codexGuardPromptHash(cli, home, "server", foreign); err == nil {
				t.Fatalf("%s admitted a different Agent Home", shell)
			}
		}
	}
}
