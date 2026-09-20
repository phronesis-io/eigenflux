package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// A command group holds subcommands and runs nothing itself. Cobra checks Runnable()
// before it validates arguments, so a group's Args validator never runs and the group
// swallows whatever it was given: `eigenflux profile card shwo` printed the group's help
// and exited 0. A mistyped subcommand in a script or a skill then looks exactly like a
// command that did its job. Cobra applies the check to the root command only.

// commandGroups returns every non-runnable command that owns subcommands.
func commandGroups(t *testing.T) []*cobra.Command {
	t.Helper()
	var groups []*cobra.Command
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		if command != rootCmd && command.HasSubCommands() && !command.Runnable() {
			groups = append(groups, command)
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(rootCmd)
	if len(groups) == 0 {
		t.Fatal("no command groups found; the walk is broken, not the command tree")
	}
	return groups
}

// runArgs drives the same path main() takes, without exiting the test binary.
func runArgs(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	// run() must finish before the buffer is read: a single return statement would
	// evaluate out.String() first and hand back an empty string every time.
	err := run()
	return out.String(), err
}

func TestEveryCommandGroupRejectsAnUnknownSubcommand(t *testing.T) {
	for _, group := range commandGroups(t) {
		path := strings.Fields(group.CommandPath())[1:] // drop the binary name
		args := append(append([]string{}, path...), "definitely-not-a-subcommand")
		_, err := runArgs(t, args...)
		if err == nil {
			t.Errorf("%q accepted an argument that is not one of its subcommands and reported success",
				group.CommandPath())
		}
	}
}

func TestUnknownSubcommandNamesTheArgumentAndTheCommand(t *testing.T) {
	_, err := runArgs(t, "profile", "card", "shwo")
	if err == nil {
		t.Fatal("a mistyped subcommand reported success")
	}
	if !strings.Contains(err.Error(), `"shwo"`) || !strings.Contains(err.Error(), `"eigenflux profile card"`) {
		t.Errorf("message must name the argument and the command, got %q", err.Error())
	}
}

func TestUnknownSubcommandStillSuggestsTheNearMiss(t *testing.T) {
	// The root command offers suggestions for a near miss; a group must not be worse.
	_, err := runArgs(t, "profile", "crd")
	if err == nil {
		t.Fatal("a near-miss subcommand reported success")
	}
	if !strings.Contains(err.Error(), "Did you mean this?") || !strings.Contains(err.Error(), "card") {
		t.Errorf("expected a suggestion of %q, got %q", "card", err.Error())
	}
}

func TestUnknownSubcommandPrintsNoHelpBeforeTheError(t *testing.T) {
	// The root command prints the error alone. A group used to print its whole help first,
	// which buries the one line that says what was wrong.
	out, err := runArgs(t, "profile", "card", "shwo")
	if err == nil {
		t.Fatal("a mistyped subcommand reported success")
	}
	if strings.Contains(out, "Usage:") || strings.Contains(out, "Available Commands:") {
		t.Errorf("help was printed before the error:\n%s", out)
	}
}

func TestAskingAGroupForHelpIsUnchanged(t *testing.T) {
	// The fix is about a wrong subcommand, never about asking a group what it offers.
	for _, args := range [][]string{{"profile", "card"}, {"profile", "card", "--help"}, {"agent"}} {
		out, err := runArgs(t, args...)
		if err != nil {
			t.Errorf("%v returned an error: %v", args, err)
		}
		if !strings.Contains(out, "Available Commands:") {
			t.Errorf("%v printed no command list:\n%s", args, out)
		}
	}
}

func TestARunnableCommandKeepsItsOwnArgumentHandling(t *testing.T) {
	// unknownSubcommand must never speak for a command that runs something: those validate
	// their own arguments, and some of them take positional ones.
	for _, name := range []string{"show", "version"} {
		var found *cobra.Command
		var walk func(*cobra.Command)
		walk = func(command *cobra.Command) {
			if command.Name() == name && command.Runnable() && found == nil {
				found = command
			}
			for _, child := range command.Commands() {
				walk(child)
			}
		}
		walk(rootCmd)
		if found == nil {
			t.Fatalf("expected to find a runnable %q command", name)
		}
		if err := unknownSubcommand(found); err != nil {
			t.Errorf("%q was treated as a command group: %v", found.CommandPath(), err)
		}
	}
}
