package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

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
