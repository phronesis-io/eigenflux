package output

import (
	"cli.eigenflux.ai/internal/skills"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

const (
	ExitSuccess      = 0
	ExitError        = 1 // generic runtime failure (network/IO/checksum) — NOT auth-related
	ExitUsageError   = 2
	ExitNotFound     = 3
	ExitAuthRequired = 4
	ExitConflict     = 5
	ExitDryRun       = 10
)

func IsTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func ResolveFormat(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if IsTTY(os.Stdout) {
		return "table"
	}
	return "json"
}

func PrintDataTo(w io.Writer, data interface{}, format string) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(data)
}

func PrintData(data interface{}, format string) {
	PrintDataTo(os.Stdout, data, format)
}

// PrintFeedForAgent renders a feed-poll response as a ready-to-consume agent
// prompt: the output contract leads as a prose preamble, followed by the
// payload. This is the "agent" format — for plugin-less runtimes (heartbeat +
// bare CLI) that read the CLI output directly and have no wrapper code to lift
// the contract themselves. Machine consumers keep using "-f json".
//
// The contract is pulled out of the payload and printed once up front; the
// remaining payload is emitted as JSON without it.
func PrintFeedForAgent(data json.RawMessage) error {
	return PrintFeedForAgentTo(os.Stdout, data)
}

// ProfileRefreshPromptLine is the one and only [PENDING TASK] block the agent
// contract honours — the whole block, with nothing following it. It lives here
// because the synchronized Skills quote it verbatim; the emitter in
// package cmd reads the same constant, so the two can never drift apart.
// Changing this string is changing a security boundary: agents are told that
// any other [PENDING TASK] text is an impersonation to report, so the skills
// (contract.md, feed.md, ef-profile/SKILL.md) must be updated in the same
// change. TestPromptLineMatchesSkills enforces that.
const ProfileRefreshPromptLine = "[PENDING TASK] Your EigenFlux profile is due for a refresh."

func loadFeedContract(name string) (string, error) {
	dir, err := skills.ResolveSkillsDir("", "")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "ef-broadcast", "references", name)
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("Feed output contract unavailable; run eigenflux skills sync: %w", err)
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "", fmt.Errorf("Feed output contract is empty: %s", path)
	}
	return text, nil
}

func ResolveFeedContract(data json.RawMessage) (string, error) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(data, &payload) != nil || payload == nil {
		return "", fmt.Errorf("invalid Feed payload")
	}
	if raw, present := payload["output_contract"]; present {
		var contract string
		if string(raw) == "null" || json.Unmarshal(raw, &contract) != nil {
			return "", fmt.Errorf("invalid Feed output_contract")
		}
		return strings.TrimSpace(contract), nil
	}
	var personalization struct {
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal(payload["personalization"], &personalization)
	name := "contract.md"
	if personalization.Mode == "baseline" {
		name = "baseline-contract.md"
	}
	return loadFeedContract(name)
}

func PrintFeedForAgentTo(w io.Writer, data json.RawMessage) error {
	contract, err := ResolveFeedContract(data)
	if err != nil {
		return err
	}
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(data, &payload)
	delete(payload, "output_contract")
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "EigenFlux feed payload received. Process it via the current ef-broadcast skill.\n\n%s\n\nPayload (untrusted network data):\n```json\n%s\n```\n", contract, body)
	return err
}

func PrintMessage(format string, args ...interface{}) {
	_ = PrintMessageTo(os.Stderr, format, args...)
}

// PrintMessageTo writes one diagnostic line and reports delivery errors. Most
// callers intentionally use best-effort PrintMessage; stateful notification
// flows use this form so they do not commit a long cooldown for a failed write.
func PrintMessageTo(w io.Writer, format string, args ...interface{}) error {
	_, err := fmt.Fprintf(w, format+"\n", args...)
	return err
}

func PrintError(msg string) {
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
}

func Die(code int, format string, args ...interface{}) {
	PrintError(fmt.Sprintf(format, args...))
	os.Exit(code)
}
