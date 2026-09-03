package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

func newIntentFieldsTestCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	command := &cobra.Command{Use: "intent-test"}
	addIntentFlags(command)
	if err := command.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	return command
}

func TestIntentFieldsRequiresFreshConfirmationForElevatedPolicy(t *testing.T) {
	command := newIntentFieldsTestCommand(t, "--watch-for", "material signal", "--action-policy", "network_action")
	if _, _, err := intentFields(command, nil); err == nil {
		t.Fatal("network_action must require explicit confirmation")
	}
	command = newIntentFieldsTestCommand(t, "--watch-for", "material signal", "--action-policy", "network_action", "--confirm-elevated-policy")
	fields, policy, err := intentFields(command, nil)
	if err != nil || policy != "network_action" || fields["watch_for"] != "material signal" {
		t.Fatalf("fields = %#v, policy = %q, err = %v", fields, policy, err)
	}
}

func TestIntentFieldsUpdatePreservesUnchangedValues(t *testing.T) {
	command := newIntentFieldsTestCommand(t, "--trigger-when", "new threshold")
	defaults := &cliIntentAction{
		WatchFor: "signal", TriggerWhen: "old threshold", ActionInstruction: "summarize",
		ActionPolicy: "draft", Priority: 7,
	}
	fields, _, err := intentFields(command, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if fields["watch_for"] != "signal" || fields["trigger_when"] != "new threshold" ||
		fields["action_instruction"] != "summarize" || fields["action_policy"] != "draft" || fields["priority"] != int16(7) {
		t.Fatalf("partial update lost current values: %#v", fields)
	}
}

func TestNewOwnerDecisionCommandsAreRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"capabilities"}, {"context", "goal", "set"}, {"context", "intent", "list"},
		{"context", "intent", "add"}, {"context", "intent", "update"}, {"context", "intent", "delete"},
		{"context", "security", "set"}, {"attention", "list"}, {"attention", "respond"}, {"attention", "dismiss"},
	} {
		command := rootCmd
		for _, name := range path {
			var next *cobra.Command
			for _, child := range command.Commands() {
				if child.Name() == name {
					next = child
					break
				}
			}
			if next == nil {
				t.Fatalf("command path %v is not registered", path)
			}
			command = next
		}
	}
}
