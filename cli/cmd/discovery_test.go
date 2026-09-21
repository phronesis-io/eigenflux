package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
