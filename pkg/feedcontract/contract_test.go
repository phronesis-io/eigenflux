package feedcontract

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contract.md")
	if err := os.WriteFile(path, []byte("  contract body\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := Load(path); got != "contract body" {
		t.Fatalf("Load()=%q", got)
	}
}

func TestLoadFindsRelativeContractFromNestedWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	contractDir := filepath.Join(root, "static")
	if err := os.MkdirAll(contractDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contractDir, "feed_contract.md"), []byte("nested contract\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "api", "consolev2")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	if got := Load("static/feed_contract.md"); got != "nested contract" {
		t.Fatalf("Load()=%q", got)
	}
}
