package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"

	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/heartbeatmigration"
	"cli.eigenflux.ai/internal/maintenance"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/spf13/cobra"
)

var guardThreadID = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

func codexGuardCommands(cli, home, server, thread, agent, action, promptHash string, definition ...string) (string, string, error) {
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
	if len(definition) > 1 {
		return "", "", fmt.Errorf("only one guard definition allowed")
	}
	if len(definition) == 1 {
		if decoded, err := hex.DecodeString(definition[0]); err != nil || len(decoded) != sha256.Size {
			return "", "", fmt.Errorf("invalid guard definition")
		}
		args = append(args, "--guard-definition", definition[0])
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

// Fingerprint the actual scoped handlers, selected Codex store and prompt. The
// Stop handler carries this identity so a trusted old definition cannot accept
// a newly configured but unreviewed guard in another store or for another prompt.
func codexGuardDefinition(codexHome, cli, home, server, thread, agent, promptHash string) (string, error) {
	parts := []string{filepath.Clean(codexHome)}
	for _, event := range []string{"Stop", "UserPromptSubmit", "PermissionRequest", "Interrupt"} {
		action := "host-observe"
		timeout := "3"
		if event == "Stop" {
			action = "stop-check"
			timeout = "10"
		}
		posix, windows, err := codexGuardCommands(cli, home, server, thread, agent, action, promptHash)
		if err != nil {
			return "", err
		}
		parts = append(parts, event, timeout, posix, windows)
	}
	b, _ := json.Marshal(parts)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:]), nil
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
	matched := false
	for _, shell := range []string{"posix", "powershell", "cmd"} {
		candidate, err := renderHeartbeatLauncher(cli, home, server, "skill", shell)
		if err != nil {
			continue
		}
		if prompt == strings.TrimSpace(heartbeatSchedulerPrompt(candidate)) || prompt == strings.TrimSpace(heartbeatSchedulerPrompt(candidate+" --watch-managed")) {
			matched = true
			break
		}
	}
	if !matched {
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
	return installCodexGuardWithPromptHash(codexHome, cli, home, server, thread, agent, promptHash)
}

func installCodexGuardWithPromptHash(codexHome, cli, home, server, thread, agent, promptHash string) (bool, error) {
	definition, err := codexGuardDefinition(codexHome, cli, home, server, thread, agent, promptHash)
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
		var definitions []string
		if action == "stop-check" {
			definitions = []string{definition}
		}
		posix, windows, err := codexGuardCommands(cli, home, server, thread, agent, action, promptHash, definitions...)
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
	command.Flags().String("scheduler-readback-file", "", "Fresh complete host scheduler JSON readback; required for a custom prompt")
	command.Flags().String("scheduler-task-id", "", "Exact task ID in the host readback")
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
		prompt := ""
		if len(supplied) == 1 {
			prompt = supplied[0]
		}
		readbackFile, _ := cmd.Flags().GetString("scheduler-readback-file")
		taskID, _ := cmd.Flags().GetString("scheduler-task-id")
		var promptHash string
		if readbackFile != "" || taskID != "" {
			if readbackFile == "" || taskID == "" || promptFile == "" {
				return fmt.Errorf("custom prompt setup requires scheduler readback file, task ID and prompt file together")
			}
			promptHash, err = codexGuardReadbackPromptHash(readbackFile, scope.Home, scope.Server, scope.AgentID, thread, taskID, prompt)
		} else {
			promptHash, err = codexGuardPromptHash(cli, scope.Home, scope.Server, prompt)
		}
		if err != nil {
			return err
		}
		changed, err := installCodexGuardWithPromptHash(codexHome, cli, scope.Home, scope.Server, thread, scope.AgentID, promptHash)
		if err != nil {
			return err
		}
		definition, err := codexGuardDefinition(codexHome, cli, scope.Home, scope.Server, thread, scope.AgentID, promptHash)
		if err != nil {
			return err
		}
		if err := bindUpgradeRepairScheduler(scope, thread, definition); err != nil {
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

// A custom prompt is opt-in and must match the fresh host-owned task in full.
// This validates host input; it neither reads the scheduler nor grants hook trust.
func codexGuardReadbackPromptHash(path, home, server, agent, thread, taskID, prompt string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("scheduler readback file must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() >= 2<<20 {
		return "", fmt.Errorf("scheduler readback must be a bounded regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 2<<20))
	if err != nil || len(raw) >= 2<<20 {
		return "", fmt.Errorf("cannot read bounded scheduler readback")
	}
	var in schedulerReportInput
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return "", fmt.Errorf("invalid scheduler readback: %w", err)
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return "", fmt.Errorf("unexpected trailing scheduler JSON")
	}
	return codexGuardVerifiedPromptHash(in, home, server, agent, thread, taskID, prompt)
}

func codexGuardVerifiedPromptHash(in schedulerReportInput, home, server, agent, thread, taskID, prompt string) (string, error) {
	if in.ErrorCode != "" || taskID == "" || !guardThreadID.MatchString(thread) {
		return "", fmt.Errorf("successful scheduler readback and explicit task/thread required")
	}
	receipt, err := schedulerReadback(in.Inventory, home, server, agent, in.ObservedAt)
	if err != nil {
		return "", err
	}
	if receipt.State != "enabled" {
		return "", fmt.Errorf("custom prompt requires an enabled owned heartbeat")
	}
	plan, err := heartbeatmigration.Build(in.Inventory, home, server, heartbeatLauncher(home, server, "skill"))
	if err != nil {
		return "", err
	}
	if plan.TaskID != taskID || len(plan.Before.Tasks) != 1 || plan.Before.Tasks[0].ThreadID != thread {
		return "", fmt.Errorf("scheduler readback does not match the requested task and thread")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || len(prompt) > 64<<10 || prompt != strings.TrimSpace(plan.Before.Tasks[0].Prompt) {
		return "", fmt.Errorf("prompt must exactly match the complete bounded scheduler prompt")
	}
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:]), nil
}
