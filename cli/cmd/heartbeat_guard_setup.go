package cmd

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"

	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/maintenance"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/spf13/cobra"
)

var guardThreadID = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

func codexGuardCommands(cli, home, server, thread, agent, action, promptHash string) (string, string, error) {
	if !filepath.IsAbs(cli) || !filepath.IsAbs(home) || server == "" || !guardThreadID.MatchString(thread) {
		return "", "", fmt.Errorf("guard requires absolute CLI/Home, explicit server and owner thread UUID")
	}
	args := []string{cli, "--homedir", home, "--server", server, "--runtime-mode", "skill", "heartbeat", action, "--thread", thread, "--expected-agent", agent}
	if action == "host-observe" {
		if decoded, err := hex.DecodeString(promptHash); err != nil || len(decoded) != sha256.Size {
			return "", "", fmt.Errorf("guard requires exact scheduler prompt hash")
		}
		args = append(args, "--prompt-hash", promptHash)
	}
	posix, ps := make([]string, len(args)), make([]string, len(args))
	for i, arg := range args {
		if strings.ContainsAny(arg, "\r\n\x00") {
			return "", "", fmt.Errorf("invalid guard command argument")
		}
		posix[i] = shellQuote(arg)
		ps[i] = "'" + strings.ReplaceAll(arg, "'", "''") + "'"
	}
	// commandWindows is launched by cmd.exe. An encoded PowerShell command avoids
	// both cmd expansion and a second interpretation of user path characters.
	script := "$OutputEncoding = New-Object System.Text.UTF8Encoding($false); " +
		"[Console]::InputEncoding = New-Object System.Text.UTF8Encoding($false); " +
		"[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false); " +
		"$hookInput = [Console]::In.ReadToEnd(); $hookInput | & " + strings.Join(ps, " ") + "; exit $LASTEXITCODE"
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 2*len(units))
	for i, unit := range units {
		binary.LittleEndian.PutUint16(raw[i*2:], unit)
	}
	windows := "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(raw)
	return strings.Join(posix, " "), windows, nil
}

// The full canonical prompt is fenced to the stable CLI, Home and server.
// A copied fragment in an ordinary conversation cannot admit a heartbeat turn.
func codexGuardPromptHash(cli, home, server, supplied string) (string, error) {
	launcher, err := renderHeartbeatLauncher(cli, home, server, "skill", "")
	if err != nil {
		return "", err
	}
	canonical := heartbeatSchedulerPrompt(launcher)
	prompt := strings.TrimSpace(supplied)
	if prompt == "" {
		prompt = strings.TrimSpace(canonical)
	}
	if prompt != strings.TrimSpace(canonical) && prompt != strings.TrimSpace(heartbeatSchedulerPrompt(launcher+" --watch-managed")) {
		return "", fmt.Errorf("scheduler prompt must exactly match this installation's canonical prompt")
	}
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:]), nil
}

// installCodexGuard only appends exact account/thread-scoped handlers. Hook
// trust belongs to Codex and is never written or inferred here.
func installCodexGuard(codexHome, cli, home, server, thread, agent string, suppliedPrompt ...string) (bool, error) {
	prompt := ""
	if len(suppliedPrompt) > 1 {
		return false, fmt.Errorf("only one scheduler prompt may be supplied")
	}
	if len(suppliedPrompt) == 1 {
		prompt = suppliedPrompt[0]
	}
	promptHash, err := codexGuardPromptHash(cli, home, server, prompt)
	if err != nil {
		return false, err
	}
	if !filepath.IsAbs(codexHome) {
		return false, fmt.Errorf("--codex-home must be absolute")
	}
	if info, err := os.Lstat(codexHome); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("Codex Home must not be a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		return false, err
	}
	path := filepath.Join(codexHome, "hooks.json")
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return false, fmt.Errorf("hooks.json must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	unlock, err := watchstate.AcquirePath(path + ".eigenflux.lock")
	if err != nil {
		return false, err
	}
	defer unlock()
	doc := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil {
		if len(raw) > 1<<20 {
			return false, fmt.Errorf("hooks.json exceeds size limit")
		}
		if err = json.Unmarshal(raw, &doc); err != nil || doc == nil {
			return false, fmt.Errorf("invalid hooks.json object")
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	hooks := map[string]json.RawMessage{}
	if raw, ok := doc["hooks"]; ok {
		if err = json.Unmarshal(raw, &hooks); err != nil || hooks == nil {
			return false, fmt.Errorf("invalid hooks object")
		}
	}
	changed := false
	for _, event := range []string{"Stop", "UserPromptSubmit", "PermissionRequest", "Interrupt"} {
		action := "host-observe"
		timeout := 3
		if event == "Stop" {
			action = "stop-check"
			timeout = 10
		}
		posix, windows, err := codexGuardCommands(cli, home, server, thread, agent, action, promptHash)
		if err != nil {
			return false, err
		}
		handler := map[string]any{"type": "command", "command": posix, "commandWindows": windows, "timeout": timeout}
		var groups []map[string]json.RawMessage
		if raw, ok := hooks[event]; ok {
			if err = json.Unmarshal(raw, &groups); err != nil || groups == nil {
				return false, fmt.Errorf("invalid %s hooks", event)
			}
		}
		exists := false
		for _, g := range groups {
			var handlers []map[string]json.RawMessage
			if err = json.Unmarshal(g["hooks"], &handlers); err != nil || handlers == nil {
				return false, fmt.Errorf("invalid %s handlers", event)
			}
			for _, h := range handlers {
				if h == nil {
					return false, fmt.Errorf("invalid %s handler", event)
				}
				var command, commandWindows, kind string
				_ = json.Unmarshal(h["command"], &command)
				_ = json.Unmarshal(h["commandWindows"], &commandWindows)
				_ = json.Unmarshal(h["type"], &kind)
				if kind == "command" && command == posix && commandWindows == windows {
					exists = true
				}
			}
		}
		if !exists {
			raw, _ := json.Marshal([]map[string]any{handler})
			groups = append(groups, map[string]json.RawMessage{"hooks": raw})
			raw, _ = json.Marshal(groups)
			hooks[event] = raw
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	raw, err := json.Marshal(hooks)
	if err != nil {
		return false, err
	}
	doc["hooks"] = raw
	if err = dispatch.WriteJSON(path, doc); err != nil {
		return false, err
	}
	return true, nil
}

func newHeartbeatGuardSetupCommand() *cobra.Command {
	command := &cobra.Command{Use: "guard-setup", Short: "Install scoped Codex lifecycle hooks; owner review is still required", Args: cobra.NoArgs}
	command.Flags().String("codex-home", "", "Absolute owner Codex configuration directory")
	command.Flags().String("thread", "", "Exact owner thread UUID")
	command.Flags().String("scheduler-prompt-file", "", "Owner-only file containing the exact native scheduler prompt readback")
	command.RunE = func(cmd *cobra.Command, _ []string) error {
		codexHome, _ := cmd.Flags().GetString("codex-home")
		thread, _ := cmd.Flags().GetString("thread")
		cli, err := stableInstalledExecutable()
		if err != nil {
			return err
		}
		scope, err := maintenanceScope(activeServerName())
		if err != nil {
			return err
		}
		promptFile, _ := cmd.Flags().GetString("scheduler-prompt-file")
		var supplied []string
		if promptFile != "" {
			if !filepath.IsAbs(promptFile) {
				return fmt.Errorf("--scheduler-prompt-file must be absolute")
			}
			info, err := os.Lstat(promptFile)
			if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
				return fmt.Errorf("scheduler prompt file must be a bounded regular file")
			}
			raw, err := os.ReadFile(promptFile)
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(raw)) == "" {
				return fmt.Errorf("scheduler prompt file must not be empty")
			}
			supplied = []string{string(raw)}
		}
		changed, err := installCodexGuard(codexHome, cli, scope.Home, scope.Server, thread, scope.AgentID, supplied...)
		if err != nil {
			return err
		}
		if runtimeObservationsEnabled() {
			_ = maintenance.RecordLifecycle(scope, maintenance.NewID(), "guard", "configured", maintenance.RuntimeObservation{Product: "codex", CLI: version})
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"status": "configured", "changed": changed, "trust_state": "trust_pending", "active_verified": false, "hooks_file": filepath.Join(codexHome, "hooks.json"), "next_action": "Review and trust these hooks in Codex /hooks or Settings > Hooks; verify a real owned scheduled run separately."})
	}
	return command
}

func init() { heartbeatCmd.AddCommand(newHeartbeatGuardSetupCommand()) }
