package cmd

import (
	"github.com/spf13/cobra"
	"strings"
	"testing"
)

// Cobra initializes a default help command and retains parsed help flags on the
// shared command tree. Restore those mutations so later command tests stay isolated.
func isolateLegacyAuthHelp(t *testing.T) {
	t.Helper()
	original := map[*cobra.Command]bool{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		original[c] = true
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(rootCmd)
	t.Cleanup(func() {
		var restore func(*cobra.Command)
		restore = func(c *cobra.Command) {
			if f := c.Flags().Lookup("help"); f != nil {
				_ = f.Value.Set("false")
				f.Changed = false
			}
			for _, child := range c.Commands() {
				if !original[child] && child.Name() == "help" {
					c.RemoveCommand(child)
				} else {
					restore(child)
				}
			}
		}
		restore(rootCmd)
	})
}

func TestLegacyAuthHiddenFromRootHelp(t *testing.T) {
	isolateLegacyAuthHelp(t)
	out, err := runArgs(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == "auth" {
			t.Fatalf("legacy auth listed in root help: %s", line)
		}
	}
}

func TestExplicitLegacyAuthHelpAndCommandsRemainAvailable(t *testing.T) {
	for _, path := range [][]string{{"auth"}, {"auth", "login"}, {"auth", "verify"}, {"auth", "logout"}} {
		t.Run(strings.Join(path, "/"), func(t *testing.T) {
			isolateLegacyAuthHelp(t)
			command, remaining, err := rootCmd.Find(path)
			if err != nil || len(remaining) != 0 || command == rootCmd {
				t.Fatalf("explicit legacy command unavailable: %v %v", remaining, err)
			}
			// Help executes the registered command path without touching credentials or HTTP.
			out, err := runArgs(t, append(append([]string{}, path...), "--help")...)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "Usage:") {
				t.Fatalf("missing explicit help: %s", out)
			}
			if len(path) == 1 || path[1] == "login" {
				for _, required := range []string{"ef-onboarding", "Email verification is required", "ef-profile", "same Agent Home and server", "Do not create or switch identities"} {
					if !strings.Contains(out, required) {
						t.Errorf("help missing %q: %s", required, out)
					}
				}
				if strings.Contains(out, "bind email optionally") {
					t.Fatalf("obsolete optional binding guidance: %s", out)
				}
			}
		})
	}
}
