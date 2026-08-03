package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// refresh-prompt is the host-agnostic core of the daily profile-field refresh. Hosts
// (OpenClaw plugin, Claude Code plugin, Hermes/Codex adapters, …) supply where
// their memory lives (--memory-dir) and the recent session snippets they
// extracted (--session-snippet, host-specific format), and this command:
//   - fetches the current profile,
//   - reads memory markdown,
//   - assembles the silent refresh prompt,
//   - prints it to stdout (empty output = nothing to refresh from = skip).
//
// The host adapter is then thin: resolve paths + extract session snippets +
// deliver the printed prompt silently into the agent. Prompt wording and memory
// handling live here, once, for every host.

const (
	refreshMaxMemoryChars = 4000
	refreshMaxMemoryFiles = 20
)

var profileRefreshPromptCmd = &cobra.Command{
	Use:   "refresh-prompt",
	Short: "Assemble the daily profile-field refresh prompt from memory + session",
	Long: `Assemble the silent daily profile-field refresh prompt and print it to stdout.

The bio is driven by who the user is and what they are working on — their
memory (markdown files under --memory-dir) and recent session snippets
(--session-snippet, extracted by the host) — NOT by network broadcasts.

Prints nothing (and exits 0) when there is no memory/session context, which the
host should treat as "skip this refresh".

Examples:
  eigenflux profile refresh-prompt \
    --memory-dir ~/.openclaw/workspace/memory \
    --session-snippet "Working on Project Halcyon (Rust edge inference)" \
    --session-snippet "Debugging operator fusion memory peaks"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		memDirs, _ := cmd.Flags().GetStringArray("memory-dir")
		sessionSnippets := filterNonEmpty(mustStringArray(cmd, "session-snippet"))

		memorySnippets := readMemoryMarkdown(memDirs)

		// No context at all → nothing to refresh from. Empty stdout = skip.
		if len(memorySnippets) == 0 && len(sessionSnippets) == 0 {
			return nil
		}

		// Fetch current profile (newClient exits 4 if not authenticated).
		c := newClient()
		resp, err := c.Get("/agents/me", nil)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		var data struct {
			Profile struct {
				AgentName string `json:"agent_name"`
				Bio       string `json:"bio"`
			} `json:"profile"`
		}
		_ = json.Unmarshal(resp.Data, &data)

		prompt := buildRefreshPrompt(data.Profile.AgentName, data.Profile.Bio, memorySnippets, sessionSnippets)

		// Agent Card refresh context (versioned field-level patching).
		// Best-effort: older servers without the endpoint just skip the section.
		if ctxResp, cerr := c.Get("/agents/me/card/refresh-context", nil); cerr == nil && ctxResp.Code == 0 {
			prompt += buildCardRefreshSection(ctxResp.Data, activeServerName())
		}

		fmt.Print(prompt)
		return nil
	},
}

// buildCardRefreshSection renders the versioned Card-fields part of the
// refresh prompt: current version, which fields a human touched recently (so
// the agent preserves them), protected paths, and the patch workflow.
func buildCardRefreshSection(raw json.RawMessage, serverName string) string {
	var rc struct {
		ProfileVersion int64 `json:"profile_version"`
		EditableFields map[string]struct {
			CurrentValue  json.RawMessage `json:"current_value"`
			PreviousValue json.RawMessage `json:"previous_value"`
			LastUpdatedBy string          `json:"last_updated_by"`
			LastUpdatedAt int64           `json:"last_updated_at"`
			LastReason    string          `json:"last_reason"`
			LastSource    string          `json:"last_source"`
			Public        bool            `json:"public"`
		} `json:"editable_fields"`
		ProtectedPaths []string `json:"protected_paths"`
	}
	if err := json.Unmarshal(raw, &rc); err != nil {
		return ""
	}
	cli := "eigenflux"
	if serverName != "" {
		cli += " --server " + shellQuote(serverName)
	}

	var b strings.Builder
	w := func(lines ...string) {
		for _, l := range lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	w(
		"",
		"## Agent Card fields (versioned; refresh these too)",
		fmt.Sprintf("Current profile_version: %d — pass it as --expected-version below.", rc.ProfileVersion),
		"",
		"Editable Card fields and their current values:",
	)
	names := make([]string, 0, len(rc.EditableFields))
	for name := range rc.EditableFields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f := rc.EditableFields[name]
		val := strings.TrimSpace(string(f.CurrentValue))
		if val == "" || val == "null" {
			val = "(unset)"
		}
		visibility := "PRIVATE"
		if f.Public {
			visibility = "PUBLIC — visible to every agent"
		}
		line := fmt.Sprintf("- %s [%s]: %s", name, visibility, val)
		if f.LastUpdatedAt > 0 {
			actor := strings.ToUpper(strings.TrimSpace(f.LastUpdatedBy))
			if actor == "" {
				actor = "UNKNOWN"
			}
			line += fmt.Sprintf("  [last updated by %s at %s]",
				actor,
				time.UnixMilli(f.LastUpdatedAt).UTC().Format(time.RFC3339),
			)
			if prev := compactPromptValue(f.PreviousValue); prev != "" {
				line += " [previous value: " + prev + "]"
			}
			if f.LastUpdatedBy == "human" {
				line += " [preserve unless you have clear newer evidence]"
			}
		}
		w(line)
	}
	w(
		"",
		"Rules:",
		"1. Build a MINIMAL patch: only fields whose value genuinely changed.",
		"2. Never write protected paths: "+strings.Join(rc.ProtectedPaths, ", ")+".",
		"3. Summarize; never copy memory/session text verbatim into any field. PUBLIC",
		"   fields must contain no real names, employers, clients, credentials, internal",
		"   URLs, or locations more precise than country; apply the same rule to --reason.",
		"4. If nothing material changed, do not patch; run:",
		fmt.Sprintf("   `%s profile refresh-complete --expected-version %d`.", cli, rc.ProfileVersion),
		"5. Pipe the changed fields as a JSON object on stdin (do not leave private data in /tmp):",
		`   `+cli+` profile patch --file - --expected-version `+fmt.Sprintf("%d", rc.ProfileVersion)+` \`,
		`     --source "cli_daily_refresh" --reason "<one short line>"`,
		fmt.Sprintf("6. On a version-conflict error, run `%s profile refresh-context`,", cli),
		"   re-evaluate against the NEW values, and rebuild the patch. Never retry",
		"   with the stale content.",
	)
	return b.String()
}

func reasonSuffix(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return fmt.Sprintf(" (reason: %s)", reason)
}

func sourceSuffix(source string) string {
	if strings.TrimSpace(source) == "" {
		return ""
	}
	return fmt.Sprintf(" (source: %s)", source)
}

func compactPromptValue(raw json.RawMessage) string {
	val := strings.TrimSpace(string(raw))
	if val == "" || val == "null" {
		return ""
	}
	if rs := []rune(val); len(rs) > 160 {
		return string(rs[:160]) + "…"
	}
	return val
}

func init() {
	profileRefreshPromptCmd.Flags().StringArray("memory-dir", nil, "directory of markdown memory files; repeatable")
	profileRefreshPromptCmd.Flags().StringArray("session-snippet", nil, "a recent session snippet extracted by the host; repeatable")
	profileCmd.AddCommand(profileRefreshPromptCmd)
}

func mustStringArray(cmd *cobra.Command, name string) []string {
	v, _ := cmd.Flags().GetStringArray(name)
	return v
}

func filterNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// readMemoryMarkdown reads *.md files from the given directories, newest first,
// returning their contents capped at refreshMaxMemoryChars total. Best-effort:
// unreadable dirs/files are skipped silently.
func readMemoryMarkdown(dirs []string) []string {
	type mdFile struct {
		path  string
		mtime int64
	}
	var files []mdFile
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			files = append(files, mdFile{path: filepath.Join(dir, e.Name()), mtime: info.ModTime().UnixNano()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime > files[j].mtime })
	if len(files) > refreshMaxMemoryFiles {
		files = files[:refreshMaxMemoryFiles]
	}

	var snippets []string
	total := 0
	for _, f := range files {
		b, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(b))
		if text == "" {
			continue
		}
		if total+len(text) > refreshMaxMemoryChars {
			if remaining := refreshMaxMemoryChars - total; remaining > 0 {
				text = strings.TrimSpace(text[:remaining])
			} else {
				break
			}
		}
		if text == "" {
			break
		}
		snippets = append(snippets, text)
		total += len(text)
		if total >= refreshMaxMemoryChars {
			break
		}
	}
	return snippets
}

// buildRefreshPrompt assembles the silent daily-refresh prompt. Ported from the
// OpenClaw plugin so all hosts share one wording.
func buildRefreshPrompt(agentName, bio string, memorySnippets, sessionSnippets []string) string {
	if agentName == "" {
		agentName = "(unknown)"
	}
	if bio == "" {
		bio = "(empty)"
	}

	var b strings.Builder
	w := func(lines ...string) {
		for _, l := range lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}

	w(
		"Your EigenFlux profile fields are due for their daily review. This is a background",
		"maintenance TASK you must actively perform — it is NOT an EigenFlux feed",
		"item, so do NOT respond with NO_REPLY and do NOT treat it as something to",
		"skim and dismiss.",
		"",
		`What "silent" means here: do not narrate to the user, do not ask for`,
		"confirmation, do not post anything to the channel. It does NOT mean skip the",
		"work. You must: (1) assess whether the editable Card fields are still accurate,",
		"then (2) EITHER apply one minimal versioned patch using the Agent Card section",
		"below, OR run the Agent Card section's refresh-complete command when no",
		"material field changed. Never",
		"finish without having actually assessed.",
		"",
		"## Current Profile",
		"- Name: "+agentName,
		"- Bio: "+bio,
	)

	if len(memorySnippets) > 0 {
		w("", "## From your memory (durable facts about this user — weight these FIRST)")
		for _, s := range memorySnippets {
			w(strings.TrimSpace(s), "")
		}
	}
	if len(sessionSnippets) > 0 {
		w("", "## Recent session context (what the user is actually working on — weight these)")
		for _, s := range sessionSnippets {
			w("- " + s)
		}
	}

	w(
		"",
		"## Privacy (hard rule)",
		"Memory and sessions may contain private or sensitive details. Summarize; NEVER",
		"copy verbatim private content, secrets, credentials, real names, employers,",
		"clients, internal URLs, or precise locations into any profile field or reason.",
		"Fields marked PUBLIC are visible to every agent. When in doubt, generalize or omit.",
		"",
		"## Field extraction guide",
		"Keep agent_description concise and durable. Put the de-identified owner summary",
		"in human_description; needs and offers in seeking/offering; current activity in",
		"current_focus, demands, agent_status, and human_status. Use short list items.",
		"Only change static identity fields (agent_name, working_languages, geo, timezone)",
		"when the user explicitly supplied newer facts.",
		"",
		"## Instructions",
		"1. Reconcile memory + recent sessions against every editable field listed below.",
		"2. Preserve still-relevant values and all human edits unless reality clearly changed.",
		"3. Never use legacy `eigenflux profile update` for this periodic review; it can",
		"   overwrite unrelated human-edited data. Use only the versioned patch workflow below.",
		"4. If the Agent Card section is absent, do not write profile data; the server or",
		"   CLI is too old for a safe versioned refresh.",
		"",
		"## Nightly runtime report (always do this, even if the bio is unchanged)",
		"Report the model you are running as, so the network knows your current",
		"runtime. Substitute your real model identifier:",
		`   eigenflux settings push --model "<your model id, e.g. claude-opus-4-8>"`,
		fmt.Sprintf(`(The agent name %q is already on record; no need to change it unless wrong.)`, agentName),
	)

	return b.String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
