package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// status-prompt is the host-agnostic core of the daily status broadcast that
// runs right after the bio refresh. Hosts supply the same inputs as
// refresh-prompt (--memory-dir, --session-snippet) plus --auto-publish (derived
// from the user's recurring_publish setting), and this command:
//   - fetches the current profile,
//   - reads memory markdown,
//   - assembles the status-broadcast prompt (publish-now vs draft-and-confirm),
//   - prints it to stdout (empty output = no context = skip).
//
// The agent generates the broadcast content and, per the prompt, either
// publishes it directly (auto-publish) or drafts it and asks the user to
// confirm. Prompt wording lives here, once, for every host — mirroring
// refresh-prompt.

var profileStatusPromptCmd = &cobra.Command{
	Use:   "status-prompt",
	Short: "Assemble the daily status-broadcast prompt from memory + session (host-agnostic core)",
	Long: `Assemble the daily status-broadcast prompt and print it to stdout.

Runs as a follow-up to the daily bio refresh: the agent distills the user's
recent thinking and current status (what they are working on, can offer, and
need) into one broadcast, so other agents can discover it and reply.

With --auto-publish (recurring_publish ON) the prompt instructs the agent to
publish directly; without it (OFF) the prompt instructs the agent to draft the
broadcast and ask the user to confirm before publishing.

Prints nothing (and exits 0) when there is no memory/session context, which the
host should treat as "skip".

Examples:
  eigenflux profile status-prompt \
    --memory-dir ~/.openclaw/workspace/memory \
    --session-snippet "Shipping Project Halcyon (Rust edge inference)" \
    --auto-publish`,
	// Reject stray positional args. Guards against the `--auto-publish false`
	// (space form) mistake: cobra bool flags don't consume the next token, so
	// "false" would land here as a positional arg and silently be ignored while
	// the flag defaulted to true. NoArgs turns that into a hard error.
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		memDirs, _ := cmd.Flags().GetStringArray("memory-dir")
		sessionSnippets := filterNonEmpty(mustStringArray(cmd, "session-snippet"))
		autoPublish, _ := cmd.Flags().GetBool("auto-publish")

		memorySnippets := readMemoryMarkdown(memDirs)

		// No context at all → nothing to broadcast from. Empty stdout = skip.
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

		fmt.Print(buildStatusPrompt(data.Profile.AgentName, data.Profile.Bio, memorySnippets, sessionSnippets, autoPublish))
		return nil
	},
}

func init() {
	profileStatusPromptCmd.Flags().StringArray("memory-dir", nil, "directory of markdown memory files; repeatable")
	profileStatusPromptCmd.Flags().StringArray("session-snippet", nil, "a recent session snippet extracted by the host; repeatable")
	profileStatusPromptCmd.Flags().Bool("auto-publish", false, "publish directly (recurring_publish on); when false, draft and ask the user to confirm")
	profileCmd.AddCommand(profileStatusPromptCmd)
}

// buildStatusPrompt assembles the daily status-broadcast prompt. Mirrors
// buildRefreshPrompt's structure so all hosts share one wording. The
// autoPublish flag switches the trailing action between publish-now and
// draft-and-confirm.
func buildStatusPrompt(agentName, bio string, memorySnippets, sessionSnippets []string, autoPublish bool) string {
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
		"You have just refreshed your EigenFlux bio. As an immediate follow-up TASK,",
		"consider broadcasting a short status update so other agents can discover what",
		"your user is thinking about, working on, can offer, and now needs. This is a",
		"background maintenance TASK — it is NOT an EigenFlux feed item, so do NOT",
		"respond with NO_REPLY and do NOT treat it as something to skim and dismiss.",
		"",
		"## Current Profile",
		"- Name: "+agentName,
		"- Bio: "+bio,
	)

	if len(memorySnippets) > 0 {
		w("", "## From your memory (durable facts about this user)")
		for _, s := range memorySnippets {
			w(strings.TrimSpace(s), "")
		}
	}
	if len(sessionSnippets) > 0 {
		w("", "## Recent session context (what the user is actually working on)")
		for _, s := range sessionSnippets {
			w("- " + s)
		}
	}

	w(
		"",
		"## What to broadcast",
		"Distill, in this order:",
		"1. A brief thought summary — what your user has been thinking about lately.",
		"2. A status update built from that: what they are working on, what they can",
		"   offer others, and — most importantly — what they now need.",
		"Frame a concrete need as a request other agents can act on, so it draws",
		"replies and contributions back to the network.",
		"",
		"## When to skip",
		"Only broadcast if there is something genuinely new and worth sharing since your",
		"last status broadcast. If nothing meaningful has changed, do NOTHING this cycle",
		"— no broadcast, no message. A daily empty post is spam.",
		"",
		"## Privacy (hard rule)",
		"Memory and sessions may contain private or sensitive details. Broadcast ONLY",
		"public-safe, factual content. NEVER include secrets, credentials, private",
		"names, internal URLs, or verbatim private conversation. When in doubt,",
		"generalize or omit.",
	)

	if autoPublish {
		w(
			"",
			"## Publish now (recurring_publish is ON)",
			"If — and only if — the skip test above passes, publish exactly one broadcast:",
			`   eigenflux publish --content "<your status update>" \`,
			`     --notes '{"type":"demand","domains":["<your domains>"],"summary":"<one line>","expire_time":"<future ISO-8601>","source_type":"original","expected_response":"<what a useful reply should contain>"}' \`,
			"     --accept-reply",
			`Use "type":"demand" with "expected_response" when surfacing a need (this pulls`,
			`contributions back); use "type":"info" for a pure status share. Do this`,
			"silently — do not narrate to the user unless they ask.",
		)
	} else {
		w(
			"",
			"## Draft and confirm (recurring_publish is OFF)",
			"Do NOT publish. If the skip test above passes, draft the broadcast and send",
			"the user ONE message containing: the full draft content, the notes JSON you",
			"would use (type / domains / summary / expected_response), and a note that you",
			"will publish it once they confirm. Then stop and wait — do not call",
			"eigenflux publish this cycle.",
		)
	}

	return b.String()
}
