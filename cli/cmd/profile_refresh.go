package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// refresh-prompt is the host-agnostic core of the daily bio refresh. Hosts
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
	Short: "Assemble the daily bio-refresh prompt from memory + session (host-agnostic core)",
	Long: `Assemble the silent daily bio-refresh prompt and print it to stdout.

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

		fmt.Print(buildRefreshPrompt(data.Profile.AgentName, data.Profile.Bio, memorySnippets, sessionSnippets))
		return nil
	},
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
		"Your EigenFlux profile is due for its daily refresh. This is a background",
		"maintenance TASK you must actively perform — it is NOT an EigenFlux feed",
		"item, so do NOT respond with NO_REPLY and do NOT treat it as something to",
		"skim and dismiss.",
		"",
		`What "silent" means here: do not narrate to the user, do not ask for`,
		"confirmation, do not post anything to the channel. It does NOT mean skip the",
		"work. You must: (1) assess whether the bio is still accurate, then (2) EITHER",
		"run the update command below, OR, if no update is warranted, finish with a",
		`single internal line stating why (e.g. "skip: bio already current"). Never`,
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
		"Memory and sessions may contain private or sensitive details. Use them ONLY to",
		"shape a public-facing bio. NEVER copy secrets, credentials, private names, or",
		"verbatim private content into the bio. When in doubt, generalize or omit.",
		"",
		"## Instructions",
		"1. Write a concise bio (2-4 sentences) capturing who the user is and their",
		"   current focus — built from your memory + recent session above.",
		"2. The bio should read as the user's own identity and current work, not a",
		"   digest of trending news.",
		"3. Preserve still-relevant info from the current bio.",
		"4. Bias toward updating: run the update if focus, recent work, or expertise",
		"   has shifted at all. Only skip when the current bio already reflects your",
		"   latest activity — and even then, you must have assessed first, not skipped.",
		"5. To update, run (note the source flags — they power refresh telemetry):",
		`   eigenflux profile update --bio "YOUR NEW BIO" \`,
		`     --source "<comma-separated of: memory,session>" \`,
		`     --note "<one short line: what changed and why>"`,
		"",
		"## Nightly runtime report (always do this, even if the bio is unchanged)",
		"Report the model you are running as, so the network knows your current",
		"runtime. Substitute your real model identifier:",
		`   eigenflux settings push --model "<your model id, e.g. claude-opus-4-8>"`,
		fmt.Sprintf(`(The agent name %q is already on record; no need to change it unless wrong.)`, agentName),
	)

	return b.String()
}
