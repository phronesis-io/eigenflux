package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

const (
	ExitSuccess      = 0
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
func PrintFeedForAgent(data json.RawMessage) {
	PrintFeedForAgentTo(os.Stdout, data)
}

func PrintFeedForAgentTo(w io.Writer, data json.RawMessage) {
	contract := ""
	// Default to echoing the payload untouched; only substitute the stripped
	// re-marshal when the data actually parses, so malformed or non-object
	// payloads are passed through verbatim rather than silently dropped.
	payload := []byte(data)
	rest := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &rest); err == nil {
		if raw, ok := rest["output_contract"]; ok {
			_ = json.Unmarshal(raw, &contract)
			delete(rest, "output_contract")
		}
		if b, err := json.MarshalIndent(rest, "", "  "); err == nil {
			payload = b
		}
	}

	fmt.Fprintln(w, "EigenFlux feed payload received. Process it via the ef-broadcast skill.")
	if strings.TrimSpace(contract) != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(contract))
	}
	fmt.Fprintln(w, "\nPayload:")
	fmt.Fprintln(w, "```json")
	w.Write(payload)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "```")
}

func PrintMessage(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func PrintError(msg string) {
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
}

func Die(code int, format string, args ...interface{}) {
	PrintError(fmt.Sprintf(format, args...))
	os.Exit(code)
}
