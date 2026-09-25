package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestDiscoveryInputGuards(t *testing.T) {
	for _, s := range []string{"0", "-1", "9223372036854775808", "someone"} {
		if validDiscoveryID(s) == nil {
			t.Fatal(s)
		}
	}
	if err := validDiscoveryID("9223372036854775807"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "need.json")
	if err := os.WriteFile(p, []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := discoveryFile(p); err == nil {
		t.Fatal("array accepted")
	}
	commands := newDiscoveryCommands()
	search := commands[0]
	if err := search.Flags().Set("need", "123"); err != nil {
		t.Fatal(err)
	}
	if err := search.RunE(search, []string{"query"}); err == nil {
		t.Fatal("ambiguous search inputs")
	}
}

func TestDiscoveryFilePreservesLargeMoney(t *testing.T) {
	p := filepath.Join(t.TempDir(), "filters.json")
	if err := os.WriteFile(p, []byte(`{"budget_max_fen":9007199254740993}`), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := discoveryFile(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(v)
	if err != nil || string(raw) != `{"budget_max_fen":9007199254740993}` {
		t.Fatal(string(raw), err)
	}
}

// The capture root is the only Need authoring surface.
func TestNeedCaptureIsTheOnlyNeedLifecycle(t *testing.T) {
	var needs []*cobra.Command
	for _, command := range rootCmd.Commands() {
		if command.Name() == "need" {
			needs = append(needs, command)
		}
	}
	if len(needs) != 1 {
		t.Fatalf("need roots: %d", len(needs))
	}
	if children := needs[0].Commands(); len(children) != 1 || children[0].Name() != "input" {
		t.Fatal("parallel discovery Need lifecycle still registered")
	}
	for _, path := range [][]string{{"input", "create"}, {"input", "get"}, {"input", "list"}} {
		command, rest, err := needs[0].Find(path)
		if err != nil || len(rest) != 0 || command.Name() != path[len(path)-1] || command.RunE == nil {
			t.Fatalf("need %v is unreachable: %v, %v", path, rest, err)
		}
		if len(path) == 2 && command.Parent().Name() != "input" {
			t.Fatalf("wrong capture command: %v", path)
		}
	}
}
